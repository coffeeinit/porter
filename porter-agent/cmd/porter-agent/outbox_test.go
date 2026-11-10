//go:build linux

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"porter/agent/internal/store"
	"porter/protocol"
)

type fakeOutboxControlPlane struct {
	commands     []protocol.CommandV2
	commandErr   error
	resultErr    error
	commandCalls int
	results      []protocol.CommandResultV2
	onResult     func(protocol.CommandResultV2)
}

func (f *fakeOutboxControlPlane) Commands(context.Context) ([]protocol.CommandV2, error) {
	f.commandCalls++
	return f.commands, f.commandErr
}

func (f *fakeOutboxControlPlane) Result(_ context.Context, result protocol.CommandResultV2) error {
	f.results = append(f.results, result)
	if f.onResult != nil {
		f.onResult(result)
	}
	return f.resultErr
}

type fakeHTTPStatusError int

func (e fakeHTTPStatusError) Error() string       { return "mock HTTP status error" }
func (e fakeHTTPStatusError) HTTPStatusCode() int { return int(e) }

func openOutboxAgentStore(t *testing.T, path string) *store.Store {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open agent store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestPollCommandsRetriesDurableResultBeforeFetching(t *testing.T) {
	path := filepath.Join(t.TempDir(), "porter-agent.db")
	st := openOutboxAgentStore(t, path)
	want := outboxV2Result("cmd-1", "op-1", "node-1", 1, true, "running", "")
	if err := st.QueueCommandResult(want); err != nil {
		t.Fatalf("queue command result: %v", err)
	}
	fake := &fakeOutboxControlPlane{
		commands:  []protocol.CommandV2{outboxV2Command("cmd-2", "op-2", "node-1")},
		resultErr: errors.New("connection lost after result POST"),
	}
	if _, err := pollCommands(context.Background(), fake, st); err == nil {
		t.Fatal("pollCommands succeeded while result delivery failed")
	}
	if fake.commandCalls != 0 {
		t.Fatalf("command poll calls after failed result POST = %d, want 0", fake.commandCalls)
	}
	pending, err := st.PendingCommandResults()
	if err != nil || len(pending) != 1 || pending[0] != want {
		t.Fatalf("pending after failed POST = %+v, err=%v; want original result", pending, err)
	}

	if err := st.Close(); err != nil {
		t.Fatalf("close agent store: %v", err)
	}
	st = openOutboxAgentStore(t, path)
	fake.resultErr = nil
	commands, err := pollCommands(context.Background(), fake, st)
	if err != nil {
		t.Fatalf("poll after reconnect: %v", err)
	}
	if len(commands) != 1 || commands[0].ID != "cmd-2" || fake.commandCalls != 1 {
		t.Fatalf("commands after successful retry = %+v, calls=%d", commands, fake.commandCalls)
	}
	if len(fake.results) != 2 || fake.results[0] != want || fake.results[1] != want {
		t.Fatalf("retried results = %+v, want the identical result twice", fake.results)
	}
	pending, err = st.PendingCommandResults()
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after accepted retry = %+v, err=%v; want none", pending, err)
	}
}

func TestPermanentResultRejectionRetainsOutboxAndStopsPolling(t *testing.T) {
	st := openOutboxAgentStore(t, filepath.Join(t.TempDir(), "porter-agent.db"))
	want := outboxV2Result("cmd-5", "op-5", "node-1", 1, false, "failed", "rejected")
	if err := st.QueueCommandResult(want); err != nil {
		t.Fatalf("queue result: %v", err)
	}
	fake := &fakeOutboxControlPlane{
		commands:  []protocol.CommandV2{outboxV2Command("cmd-next", "op-next", "node-1")},
		resultErr: fakeHTTPStatusError(400),
	}
	if _, err := pollCommands(context.Background(), fake, st); err == nil || !isPermanentCommandResultError(err) {
		t.Fatalf("poll error = %v, want permanent command-result rejection", err)
	}
	if fake.commandCalls != 0 {
		t.Fatalf("command poll calls after permanent rejection = %d, want 0", fake.commandCalls)
	}
	pending, err := st.PendingCommandResults()
	if err != nil || len(pending) != 1 || pending[0] != want {
		t.Fatalf("permanently rejected result was not retained: pending=%+v err=%v", pending, err)
	}
}

func TestResultHTTPStatusRetryClassification(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   bool
	}{
		{status: 400, want: true},
		{status: 401, want: true},
		{status: 409, want: true},
		{status: 408, want: false},
		{status: 425, want: false},
		{status: 429, want: false},
		{status: 500, want: false},
		{status: 503, want: false},
	} {
		if got := isPermanentHTTPStatusError(fakeHTTPStatusError(tc.status)); got != tc.want {
			t.Errorf("HTTP %d permanent = %v, want %v", tc.status, got, tc.want)
		}
	}
}

func TestHandlePersistsResultBeforePosting(t *testing.T) {
	st := openOutboxAgentStore(t, filepath.Join(t.TempDir(), "porter-agent.db"))
	fake := &fakeOutboxControlPlane{resultErr: errors.New("server unavailable")}
	fake.onResult = func(result protocol.CommandResultV2) {
		pending, err := st.PendingCommandResults()
		if err != nil {
			t.Errorf("read outbox during POST: %v", err)
			return
		}
		if len(pending) != 1 || pending[0] != result {
			t.Errorf("result was posted before durable enqueue: pending=%+v posted=%+v", pending, result)
		}
	}
	command := outboxV2Command("cmd-3", "op-3", "node-1")
	if err := handle(context.Background(), fake, nil, st, command); err != nil {
		t.Fatalf("handle idempotent delete command: %v", err)
	}
	if len(fake.results) != 1 || !fake.results[0].Success || fake.results[0].CommandID != command.ID || fake.results[0].State != "deleted" {
		t.Fatalf("posted results = %+v, want durable v2 result for %s", fake.results, command.ID)
	}
	pending, err := st.PendingCommandResults()
	if err != nil || len(pending) != 1 || pending[0] != fake.results[0] {
		t.Fatalf("pending after failed POST = %+v, err=%v", pending, err)
	}
}

func TestHandleDoesNotPostWhenOutboxPersistenceFails(t *testing.T) {
	st := openOutboxAgentStore(t, filepath.Join(t.TempDir(), "porter-agent.db"))
	if err := st.Close(); err != nil {
		t.Fatalf("close agent store: %v", err)
	}
	fake := &fakeOutboxControlPlane{}
	command := outboxV2Command("cmd-4", "op-4", "node-1")
	if err := handle(context.Background(), fake, nil, st, command); err == nil {
		t.Fatal("handle succeeded after local outbox write failed")
	}
	if len(fake.results) != 0 {
		t.Fatalf("posted %d results despite outbox persistence failure", len(fake.results))
	}
}

func outboxV2Command(id, operationID, nodeID string) protocol.CommandV2 {
	payload, _ := json.Marshal(protocol.VMApplySpec{VMID: "missing-vm"})
	return protocol.CommandV2{
		ProtocolVersion: protocol.VersionV2, ID: id, NodeID: nodeID, OperationID: operationID,
		Attempt: 1, Kind: "vm.delete", LeaseToken: base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
		LeaseExpiresAt: time.Now().UTC().Add(time.Minute), Payload: payload, State: "leased", CreatedAt: time.Now().UTC(),
	}
}

func outboxV2Result(commandID, operationID, nodeID string, attempt int, success bool, state, resultError string) protocol.CommandResultV2 {
	return protocol.CommandResultV2{
		ProtocolVersion: protocol.VersionV2, CommandID: commandID, OperationID: operationID, NodeID: nodeID,
		Attempt: attempt, Kind: "vm.start", LeaseToken: base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
		Success: success, State: state, Error: resultError,
	}
}
