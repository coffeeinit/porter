package store

import (
	"encoding/base64"
	"path/filepath"
	"testing"

	"porter/protocol"
)

func openOutboxTestStore(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open agent store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestCommandResultOutboxPersistsAcrossReopenAndAcknowledges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "porter-agent.db")
	st := openOutboxTestStore(t, path)
	want := validOutboxResult("cmd-1", "op-1", 2, false, "failed", "temporary runtime failure")
	if err := st.QueueCommandResult(want); err != nil {
		t.Fatalf("queue result: %v", err)
	}
	if err := st.QueueCommandResult(want); err != nil {
		t.Fatalf("queue identical result twice: %v", err)
	}
	pending, err := st.PendingCommandResults()
	if err != nil {
		t.Fatalf("list queued results: %v", err)
	}
	if len(pending) != 1 || pending[0] != want {
		t.Fatalf("queued results = %+v, want one copy of %+v", pending, want)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close agent store: %v", err)
	}

	reopened := openOutboxTestStore(t, path)
	pending, err = reopened.PendingCommandResults()
	if err != nil {
		t.Fatalf("list results after reopen: %v", err)
	}
	if len(pending) != 1 || pending[0] != want {
		t.Fatalf("results after reopen = %+v, want %+v", pending, want)
	}
	if err := reopened.AckCommandResult(want.CommandID, want.Attempt); err != nil {
		t.Fatalf("ack result: %v", err)
	}
	if err := reopened.AckCommandResult(want.CommandID, want.Attempt); err != nil {
		t.Fatalf("repeat ack: %v", err)
	}
	pending, err = reopened.PendingCommandResults()
	if err != nil {
		t.Fatalf("list after ack: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending results after ack = %+v, want none", pending)
	}
}

func TestCommandResultOutboxRejectsConflictingDuplicate(t *testing.T) {
	st := openOutboxTestStore(t, filepath.Join(t.TempDir(), "porter-agent.db"))
	first := validOutboxResult("cmd-2", "op-2", 1, true, "running", "")
	if err := st.QueueCommandResult(first); err != nil {
		t.Fatalf("queue first result: %v", err)
	}
	conflict := first
	conflict.Success = false
	conflict.Error = "different outcome"
	conflict.State = "failed"
	if err := st.QueueCommandResult(conflict); err == nil {
		t.Fatal("conflicting result for the same attempt was accepted")
	}
	pending, err := st.PendingCommandResults()
	if err != nil {
		t.Fatalf("list queued result: %v", err)
	}
	if len(pending) != 1 || pending[0] != first {
		t.Fatalf("conflicting enqueue changed outbox: %+v", pending)
	}
}

func TestCommandResultOutboxRequiresCompleteIdentity(t *testing.T) {
	st := openOutboxTestStore(t, filepath.Join(t.TempDir(), "porter-agent.db"))
	valid := validOutboxResult("cmd-3", "op-3", 1, true, "running", "")
	invalid := []protocol.CommandResultV2{
		{},
		{OperationID: valid.OperationID, NodeID: valid.NodeID, Attempt: valid.Attempt},
		{CommandID: valid.CommandID, NodeID: valid.NodeID, Attempt: valid.Attempt},
		{CommandID: valid.CommandID, OperationID: valid.OperationID, Attempt: valid.Attempt},
		{CommandID: valid.CommandID, OperationID: valid.OperationID, NodeID: valid.NodeID},
	}
	for i, result := range invalid {
		if err := st.QueueCommandResult(result); err == nil {
			t.Errorf("invalid identity case %d was accepted", i)
		}
	}
}

func validOutboxResult(commandID, operationID string, attempt int, success bool, state, resultError string) protocol.CommandResultV2 {
	return protocol.CommandResultV2{
		ProtocolVersion: protocol.VersionV2,
		CommandID:       commandID, OperationID: operationID, NodeID: "node-1", Attempt: attempt,
		Kind: "vm.start", LeaseToken: base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
		Success: success, State: state, Error: resultError,
	}
}
