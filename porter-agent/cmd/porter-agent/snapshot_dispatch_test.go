//go:build linux

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"porter/agent/internal/client"
	"porter/agent/internal/store"
	"porter/protocol"
)

type snapshotDispatchAPI struct {
	mu       sync.Mutex
	commands []protocol.Command
	records  []protocol.SnapshotRecord
	results  []protocol.CommandResult
}

func (a *snapshotDispatchAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/fleet/commands":
		commands := a.commands
		a.commands = nil
		_ = json.NewEncoder(w).Encode(commands)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/fleet/snapshot":
		var record protocol.SnapshotRecord
		if err := json.NewDecoder(r.Body).Decode(&record); err != nil {
			http.Error(w, "invalid snapshot", http.StatusBadRequest)
			return
		}
		a.records = append(a.records, record)
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/fleet/result":
		var result protocol.CommandResult
		if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
			http.Error(w, "invalid result", http.StatusBadRequest)
			return
		}
		a.results = append(a.results, result)
		w.WriteHeader(http.StatusOK)
	default:
		http.NotFound(w, r)
	}
}

func (a *snapshotDispatchAPI) enqueue(command protocol.Command) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.commands = append(a.commands, command)
}

func (a *snapshotDispatchAPI) snapshotRecords() []protocol.SnapshotRecord {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]protocol.SnapshotRecord(nil), a.records...)
}

func (a *snapshotDispatchAPI) commandResults() []protocol.CommandResult {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]protocol.CommandResult(nil), a.results...)
}

type fakeSnapshotCommandRuntime struct {
	record        protocol.SnapshotRecord
	snapshotVMID  string
	snapshotID    string
	snapshotCalls int
	restoreSpec   protocol.SnapshotRestoreSpec
	restoreCalls  int
	restoreVM     store.VM
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
func (f *fakeSnapshotCommandRuntime) RestoreSnapshot(_ context.Context, _ store.VM, spec protocol.SnapshotRestoreSpec) (store.VM, error) {
	f.restoreCalls++
	f.restoreSpec = spec
	return f.restoreVM, nil
}
func (f *fakeSnapshotCommandRuntime) ResizeVolume(context.Context, string, int) error { return nil }

var _ commandRuntime = (*fakeSnapshotCommandRuntime)(nil)

func TestSnapshotCreateAndRestoreCommandRoundTripThroughHTTPClient(t *testing.T) {
	ctx := context.Background()
	api := &snapshotDispatchAPI{}
	server := httptest.NewServer(api)
	defer server.Close()
	controlPlane := client.New(server.URL, "node-snapshot-test", "node-token")
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
	engine := &fakeSnapshotCommandRuntime{record: record, restoreVM: store.VM{ID: vm.ID, ReplicaID: vm.ReplicaID, State: "running", Health: "checking"}}

	api.enqueue(protocol.Command{
		ID: "command-create-snapshot", NodeID: "node-snapshot-test", OperationID: "operation-create-snapshot", Attempt: 1,
		Kind: "snapshot.create", Payload: protocol.SnapshotSpec{VMID: vm.ID, SnapshotID: record.ID},
	})
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
	if got := api.snapshotRecords(); len(got) != 1 || !reflect.DeepEqual(got[0], record) {
		t.Fatalf("snapshot record sent through HTTP = %+v, want %+v", got, record)
	}
	results := api.commandResults()
	if len(results) != 1 || !results[0].Success || results[0].CommandID != "command-create-snapshot" || results[0].State != "snapshot_ready" {
		t.Fatalf("create-snapshot command results = %+v", results)
	}

	restoreSpec := protocol.SnapshotRestoreSpec{
		SnapshotID: record.ID, VMID: record.VMID, StatePath: record.StatePath, MemPath: record.MemPath,
		SizeBytes: record.SizeBytes, StateSHA256: record.StateSHA256, MemSHA256: record.MemSHA256,
		DiskFiles: append([]protocol.SnapshotFile(nil), record.DiskFiles...),
	}
	api.enqueue(protocol.Command{
		ID: "command-restore-snapshot", NodeID: "node-snapshot-test", OperationID: "operation-restore-snapshot", Attempt: 1,
		Kind: "snapshot.restore", Payload: restoreSpec,
	})
	commands, err = pollCommands(ctx, controlPlane, st)
	if err != nil || len(commands) != 1 {
		t.Fatalf("poll restore-snapshot command: commands=%+v err=%v", commands, err)
	}
	if err := handle(ctx, controlPlane, engine, st, commands[0]); err != nil {
		t.Fatalf("handle restore-snapshot command: %v", err)
	}
	if engine.restoreCalls != 1 || !reflect.DeepEqual(engine.restoreSpec, restoreSpec) {
		t.Fatalf("runtime restore dispatch = %+v, want exact persisted snapshot spec %+v", engine.restoreSpec, restoreSpec)
	}
	if persisted, ok := st.GetVM(vm.ID); !ok || persisted.State != "running" {
		t.Fatalf("restored VM state was not persisted locally: vm=%+v found=%v", persisted, ok)
	}
	results = api.commandResults()
	if len(results) != 2 || !results[1].Success || results[1].CommandID != "command-restore-snapshot" || results[1].State != "running" {
		t.Fatalf("restore-snapshot command results = %+v", results)
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

	engine := &fakeSnapshotCommandRuntime{restoreVM: store.VM{ID: vm.ID, State: "running", Health: "checking"}}
	controlPlane := &fakeOutboxControlPlane{}
	command := protocol.Command{
		ID: "command-restore-ledger-failure", OperationID: "operation-restore-ledger-failure",
		NodeID: "node-restore-ledger-failure", Attempt: 1, Kind: "snapshot.restore",
		Payload: protocol.SnapshotRestoreSpec{SnapshotID: "snapshot-ledger-failure", VMID: vm.ID},
	}
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
