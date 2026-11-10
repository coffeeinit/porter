//go:build linux

package main

import (
	"context"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"porter/agent/internal/client"
	"porter/agent/internal/store"
	"porter/protocol"
)

type snapshotDispatchAPI struct {
	mu       sync.Mutex
	commands []protocol.CommandV2
	results  []protocol.CommandResultV2
}

func (a *snapshotDispatchAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer node-token" || r.Header.Get("X-Porter-Node-ID") != "node-snapshot-test" {
		http.Error(w, "missing agent identity", http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/internal/agent/commands":
		if r.URL.Query().Get("limit") != "20" {
			http.Error(w, "bad command limit", http.StatusBadRequest)
			return
		}
		commands := a.commands
		a.commands = nil
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(commands)
	case r.Method == http.MethodPost && r.URL.Path == "/internal/agent/results":
		var result protocol.CommandResultV2
		if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
			http.Error(w, "invalid result", http.StatusBadRequest)
			return
		}
		if err := result.Validate(); err != nil {
			http.Error(w, "invalid v2 result", http.StatusBadRequest)
			return
		}
		a.results = append(a.results, result)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func (a *snapshotDispatchAPI) enqueue(command protocol.CommandV2) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.commands = append(a.commands, command)
}

func (a *snapshotDispatchAPI) commandResults() []protocol.CommandResultV2 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]protocol.CommandResultV2(nil), a.results...)
}

type fakeSnapshotCommandRuntime struct {
	record         protocol.SnapshotRecord
	snapshotVMID   string
	snapshotID     string
	snapshotCalls  int
	restoreCommand protocol.SnapshotRestoreCommand
	restoreCalls   int
	restoreVM      store.VM
	restoreReceipt protocol.SnapshotArtifactReceipt
}

func (f *fakeSnapshotCommandRuntime) Apply(context.Context, protocol.VMApplySpec) (store.VM, error) {
	return store.VM{}, nil
}
func (f *fakeSnapshotCommandRuntime) Start(context.Context, protocol.VMApplySpec) (store.VM, error) {
	return store.VM{}, nil
}
func (f *fakeSnapshotCommandRuntime) Restart(context.Context, protocol.VMApplySpec) (store.VM, error) {
	return store.VM{}, nil
}
func (f *fakeSnapshotCommandRuntime) Stop(store.VM) error                    { return nil }
func (f *fakeSnapshotCommandRuntime) Delete(context.Context, store.VM) error { return nil }
func (f *fakeSnapshotCommandRuntime) Healthy(store.VM) bool                  { return true }
func (f *fakeSnapshotCommandRuntime) ProcessRunning(store.VM) (bool, error)  { return false, nil }
func (f *fakeSnapshotCommandRuntime) Snapshot(_ context.Context, vm store.VM, snapshotID string) (protocol.SnapshotRecord, error) {
	f.snapshotCalls++
	f.snapshotVMID, f.snapshotID = vm.ID, snapshotID
	return f.record, nil
}
func (f *fakeSnapshotCommandRuntime) RestoreSnapshotV2(_ context.Context, _ store.VM, command protocol.SnapshotRestoreCommand) (store.VM, protocol.SnapshotArtifactReceipt, error) {
	f.restoreCalls++
	f.restoreCommand = command
	return f.restoreVM, f.restoreReceipt, nil
}
func (f *fakeSnapshotCommandRuntime) ResizeVolume(context.Context, string, int) error { return nil }

var _ commandRuntime = (*fakeSnapshotCommandRuntime)(nil)

func TestSnapshotCreateAndRestoreCommandRoundTripThroughV2HTTPClient(t *testing.T) {
	ctx := context.Background()
	api := &snapshotDispatchAPI{}
	server := httptest.NewTLSServer(api)
	defer server.Close()
	controlPlane, err := client.NewWithTLS(server.URL, "node-snapshot-test", "node-token", client.TLSOptions{ServerCAFile: snapshotDispatchServerCA(t, server)})
	if err != nil {
		t.Fatal(err)
	}
	st := openOutboxAgentStore(t, filepath.Join(t.TempDir(), "porter-agent.db"))

	vm := store.VM{ID: "vm-snapshot-roundtrip", ReplicaID: "replica-roundtrip", State: "running", Health: "healthy"}
	if err := st.UpsertVM(vm); err != nil {
		t.Fatalf("persist test VM: %v", err)
	}
	record := protocol.SnapshotRecord{
		ID: "snapshot-roundtrip", VMID: vm.ID,
		StatePath: "/var/lib/porter/vms/vm-snapshot-roundtrip/snapshot-roundtrip.snap",
		MemPath:   "/var/lib/porter/vms/vm-snapshot-roundtrip/snapshot-roundtrip.mem",
		SizeBytes: 7000, StateSHA256: strings.Repeat("a", 64), MemSHA256: strings.Repeat("b", 64),
		DiskFiles: []protocol.SnapshotFile{
			{Kind: "rootfs", OriginalPath: "/var/lib/porter/vms/vm-snapshot-roundtrip/rootfs.ext4", BackupPath: "/var/lib/porter/vms/vm-snapshot-roundtrip/snapshot-roundtrip.rootfs.disk", SizeBytes: 2048, SHA256: strings.Repeat("c", 64)},
			{Kind: "volume", OriginalPath: "/var/lib/porter/volumes/volume-roundtrip.ext4", BackupPath: "/var/lib/porter/vms/vm-snapshot-roundtrip/snapshot-roundtrip.volume.disk", SizeBytes: 4096, SHA256: strings.Repeat("d", 64)},
		},
	}
	receipt, err := protocol.SnapshotArtifactReceiptFor(record)
	if err != nil {
		t.Fatalf("make snapshot receipt: %v", err)
	}
	engine := &fakeSnapshotCommandRuntime{
		record:         record,
		restoreVM:      store.VM{ID: vm.ID, ReplicaID: vm.ReplicaID, State: "running", Health: "checking"},
		restoreReceipt: receipt,
	}

	api.enqueue(dispatchV2Command("command-create-snapshot", "snapshot.create", protocol.SnapshotSpec{VMID: vm.ID, SnapshotID: record.ID, Consistency: protocol.SnapshotCrashConsistent}))
	commands, err := pollCommands(ctx, controlPlane, st)
	if err != nil || len(commands) != 1 {
		t.Fatalf("poll create-snapshot command: commands=%+v err=%v", commands, err)
	}
	if err := handle(ctx, controlPlane, engine, st, commands[0]); err != nil {
		t.Fatalf("handle create-snapshot command: %v", err)
	}
	if engine.snapshotCalls != 1 || engine.snapshotVMID != vm.ID || engine.snapshotID != record.ID {
		t.Fatalf("runtime snapshot dispatch = calls %d, vm %q, id %q", engine.snapshotCalls, engine.snapshotVMID, engine.snapshotID)
	}
	results := api.commandResults()
	if len(results) != 1 || !results[0].Success || results[0].CommandID != "command-create-snapshot" || results[0].State != "snapshot_ready" || !reflect.DeepEqual(results[0].SnapshotArtifact, &receipt) {
		t.Fatalf("create-snapshot v2 receipts = %+v", results)
	}

	restoreCommand := protocol.SnapshotRestoreCommand{
		VMID: record.VMID, SnapshotID: record.ID, Consistency: protocol.SnapshotCrashConsistent,
		ArtifactSHA256: receipt.ArtifactSHA256, SizeBytes: receipt.SizeBytes,
	}
	api.enqueue(dispatchV2Command("command-restore-snapshot", "snapshot.restore", restoreCommand))
	commands, err = pollCommands(ctx, controlPlane, st)
	if err != nil || len(commands) != 1 {
		t.Fatalf("poll restore-snapshot command: commands=%+v err=%v", commands, err)
	}
	if err := handle(ctx, controlPlane, engine, st, commands[0]); err != nil {
		t.Fatalf("handle restore-snapshot command: %v", err)
	}
	if engine.restoreCalls != 1 || engine.restoreCommand != restoreCommand {
		t.Fatalf("runtime restore dispatch = %+v, want restricted command %+v", engine.restoreCommand, restoreCommand)
	}
	if persisted, ok := st.GetVM(vm.ID); !ok || persisted.State != "running" {
		t.Fatalf("restored VM state was not persisted locally: vm=%+v found=%v", persisted, ok)
	}
	results = api.commandResults()
	if len(results) != 2 || !results[1].Success || results[1].CommandID != "command-restore-snapshot" || results[1].State != "restored" || !reflect.DeepEqual(results[1].SnapshotArtifact, &receipt) {
		t.Fatalf("restore-snapshot v2 receipts = %+v", results)
	}
	pending, err := st.PendingCommandResults()
	if err != nil || len(pending) != 0 {
		t.Fatalf("command-result outbox after both round trips = %+v err=%v", pending, err)
	}
}

func TestRestoreCommandDoesNotReportSuccessWhenLocalVMStateWriteFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "porter-agent.db")
	st := openOutboxAgentStore(t, path)
	vm := store.VM{ID: "vm-restore-ledger-failure", State: "stopped", Health: "unknown"}
	if err := st.UpsertVM(vm); err != nil {
		t.Fatalf("persist initial stopped VM: %v", err)
	}

	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open trigger connection: %v", err)
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_vm_state_update BEFORE UPDATE ON vms BEGIN SELECT RAISE(FAIL, 'injected VM state write failure'); END`); err != nil {
		_ = db.Close()
		t.Fatalf("create VM state failure trigger: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close trigger connection: %v", err)
	}

	receipt := protocol.SnapshotArtifactReceipt{VMID: vm.ID, SnapshotID: "snapshot-ledger-failure", Consistency: protocol.SnapshotCrashConsistent, ArtifactSHA256: strings.Repeat("a", 64), SizeBytes: 100}
	engine := &fakeSnapshotCommandRuntime{restoreVM: store.VM{ID: vm.ID, State: "running", Health: "checking"}, restoreReceipt: receipt}
	controlPlane := &fakeOutboxControlPlane{}
	payload := protocol.SnapshotRestoreCommand{VMID: vm.ID, SnapshotID: receipt.SnapshotID, Consistency: receipt.Consistency, ArtifactSHA256: receipt.ArtifactSHA256, SizeBytes: receipt.SizeBytes}
	command := dispatchV2Command("command-restore-ledger-failure", "snapshot.restore", payload)
	if err := handle(context.Background(), controlPlane, engine, st, command); err != nil {
		t.Fatalf("handle restore after state-write failure: %v", err)
	}
	if engine.restoreCalls != 1 {
		t.Fatalf("runtime restore calls = %d, want 1", engine.restoreCalls)
	}
	if len(controlPlane.results) != 1 || controlPlane.results[0].Success || controlPlane.results[0].CommandID != command.ID || !strings.Contains(controlPlane.results[0].Error, "persist VM state failed") {
		t.Fatalf("restore was not reported as a persistence failure: %+v", controlPlane.results)
	}
	persisted, ok := st.GetVM(vm.ID)
	if !ok || persisted.State != "stopped" {
		t.Fatalf("failed local state update unexpectedly changed VM ledger: vm=%+v found=%v", persisted, ok)
	}
	pending, err := st.PendingCommandResults()
	if err != nil || len(pending) != 0 {
		t.Fatalf("failed restore result was not durably delivered: pending=%+v err=%v", pending, err)
	}
}

func dispatchV2Command(id, kind string, payload any) protocol.CommandV2 {
	data, _ := json.Marshal(payload)
	return protocol.CommandV2{
		ProtocolVersion: protocol.VersionV2, ID: id, NodeID: "node-snapshot-test", OperationID: "operation-" + id,
		Attempt: 1, Kind: kind, LeaseToken: base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
		LeaseExpiresAt: time.Now().UTC().Add(time.Minute), Payload: data, State: "leased", CreatedAt: time.Now().UTC(),
	}
}

func snapshotDispatchServerCA(t *testing.T, server *httptest.Server) string {
	t.Helper()
	cert, err := x509.ParseCertificate(server.Certificate().Raw)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "server-ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
