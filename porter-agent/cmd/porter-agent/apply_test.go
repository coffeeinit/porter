package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"porter/agent/internal/store"
	"porter/protocol"
)

func openApplyTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("open agent store: %v", err)
	}
	return st
}

func TestApplyVMIdempotentDoesNotLaunchDuplicate(t *testing.T) {
	st := openApplyTestStore(t)
	defer st.Close()
	spec := protocol.VMApplySpec{VMID: "vm-1", ReplicaID: "replica-1", GuestIP: "10.0.0.2", Port: 8080}
	applyCalls := 0
	apply := func(_ context.Context, got protocol.VMApplySpec) (store.VM, error) {
		applyCalls++
		if got.VMID != spec.VMID {
			t.Fatalf("apply got VM %q, want %q", got.VMID, spec.VMID)
		}
		return store.VM{ID: got.VMID, ReplicaID: got.ReplicaID, State: "running", PID: 4242, Socket: "/tmp/fc.sock", Health: "checking"}, nil
	}
	healthy := func(vm store.VM) bool { return vm.PID == 4242 && vm.Socket == "/tmp/fc.sock" }

	processRunning := func(store.VM) (bool, error) { return false, nil }
	first, err := applyVMIdempotent(context.Background(), st, spec, apply, healthy, processRunning)
	if err != nil || first.State != "running" {
		t.Fatalf("first apply: vm=%+v err=%v", first, err)
	}
	second, err := applyVMIdempotent(context.Background(), st, spec, apply, healthy, processRunning)
	if err != nil || second.State != "running" {
		t.Fatalf("retried apply: vm=%+v err=%v", second, err)
	}
	if applyCalls != 1 {
		t.Fatalf("runtime Apply called %d times, want exactly once", applyCalls)
	}
	if second.GuestIP != spec.GuestIP || second.Port != spec.Port {
		t.Fatalf("retry lost VM metadata: %+v", second)
	}
}

func TestApplyVMIdempotentRejectsLiveVMDifferentReplica(t *testing.T) {
	st := openApplyTestStore(t)
	defer st.Close()
	if err := st.UpsertVM(store.VM{ID: "vm-1", ReplicaID: "replica-old", State: "running", PID: 4242, Socket: "/tmp/fc.sock"}); err != nil {
		t.Fatalf("seed running VM: %v", err)
	}
	applyCalls := 0
	_, err := applyVMIdempotent(context.Background(), st,
		protocol.VMApplySpec{VMID: "vm-1", ReplicaID: "replica-new"},
		func(context.Context, protocol.VMApplySpec) (store.VM, error) { applyCalls++; return store.VM{}, nil },
		func(store.VM) bool { return true },
		func(store.VM) (bool, error) { return true, nil },
	)
	if err == nil || !strings.Contains(err.Error(), "already running for replica") {
		t.Fatalf("expected live identity conflict, got %v", err)
	}
	if applyCalls != 0 {
		t.Fatalf("runtime Apply ran %d times after identity conflict", applyCalls)
	}
}

func TestApplyVMIdempotentPropagatesLocalPersistenceFailure(t *testing.T) {
	st := openApplyTestStore(t)
	if err := st.Close(); err != nil {
		t.Fatalf("close test store: %v", err)
	}
	applyCalls := 0
	_, err := applyVMIdempotent(context.Background(), st,
		protocol.VMApplySpec{VMID: "vm-closed-store", ReplicaID: "replica-1"},
		func(_ context.Context, spec protocol.VMApplySpec) (store.VM, error) {
			applyCalls++
			return store.VM{ID: spec.VMID, ReplicaID: spec.ReplicaID, State: "running", PID: 4242, Socket: "/tmp/fc.sock"}, nil
		},
		func(store.VM) bool { return false },
		func(store.VM) (bool, error) { return false, nil },
	)
	if err == nil || !strings.Contains(err.Error(), "persist local VM state") {
		t.Fatalf("local database failure was not propagated: %v", err)
	}
	if applyCalls != 1 {
		t.Fatalf("runtime Apply called %d times, want one", applyCalls)
	}
}

func TestApplyVMIdempotentRefusesDuplicateWhenPriorProcessIsStillRunning(t *testing.T) {
	st := openApplyTestStore(t)
	defer st.Close()
	if err := st.UpsertVM(store.VM{ID: "vm-1", ReplicaID: "replica-1", State: "running", PID: 4242, Socket: "/tmp/fc.sock"}); err != nil {
		t.Fatalf("seed running VM: %v", err)
	}
	applyCalls := 0
	_, err := applyVMIdempotent(context.Background(), st,
		protocol.VMApplySpec{VMID: "vm-1", ReplicaID: "replica-1"},
		func(context.Context, protocol.VMApplySpec) (store.VM, error) { applyCalls++; return store.VM{}, nil },
		func(store.VM) bool { return false },
		func(store.VM) (bool, error) { return true, nil },
	)
	if err == nil || !strings.Contains(err.Error(), "refusing duplicate apply") {
		t.Fatalf("expected duplicate-apply refusal, got %v", err)
	}
	if applyCalls != 0 {
		t.Fatalf("runtime Apply called %d times while prior process was live", applyCalls)
	}
}

func TestApplyVMIdempotentRefusesWhenPriorProcessCannotBeVerified(t *testing.T) {
	st := openApplyTestStore(t)
	defer st.Close()
	if err := st.UpsertVM(store.VM{ID: "vm-1", ReplicaID: "replica-1", State: "failed", PID: 4242, Socket: "/tmp/fc.sock"}); err != nil {
		t.Fatalf("seed failed VM: %v", err)
	}
	applyCalls := 0
	_, err := applyVMIdempotent(context.Background(), st,
		protocol.VMApplySpec{VMID: "vm-1", ReplicaID: "replica-1"},
		func(context.Context, protocol.VMApplySpec) (store.VM, error) { applyCalls++; return store.VM{}, nil },
		func(store.VM) bool { return false },
		func(store.VM) (bool, error) { return false, errors.New("process identity unavailable") },
	)
	if err == nil || !strings.Contains(err.Error(), "process identity unavailable") {
		t.Fatalf("expected process verification error, got %v", err)
	}
	if applyCalls != 0 {
		t.Fatalf("runtime Apply called %d times with an unverifiable prior PID", applyCalls)
	}
}

func TestRunVMLifecycleCommandStartIsIdempotent(t *testing.T) {
	st := openApplyTestStore(t)
	defer st.Close()
	spec := protocol.VMApplySpec{VMID: "vm-start", ReplicaID: "replica-start", GuestIP: "10.0.0.2", Port: 8080}
	startCalls, restartCalls := 0, 0
	start := func(_ context.Context, got protocol.VMApplySpec) (store.VM, error) {
		startCalls++
		if got.VMID != spec.VMID {
			t.Fatalf("start got VM %q, want %q", got.VMID, spec.VMID)
		}
		return store.VM{ID: got.VMID, ReplicaID: got.ReplicaID, State: "running", PID: 4242, Socket: "/tmp/vm-start.sock", Health: "healthy"}, nil
	}
	restart := func(context.Context, protocol.VMApplySpec) (store.VM, error) {
		restartCalls++
		return store.VM{}, nil
	}
	healthy := func(vm store.VM) bool { return vm.State == "running" && vm.Health == "healthy" }
	processRunning := func(store.VM) (bool, error) { return false, nil }

	for range 2 {
		vm, err := runVMLifecycleCommand(context.Background(), st, "vm.start", spec, start, restart, healthy, processRunning)
		if err != nil || vm.State != "running" {
			t.Fatalf("start command: vm=%+v err=%v", vm, err)
		}
	}
	if startCalls != 1 || restartCalls != 0 {
		t.Fatalf("start calls=%d restart calls=%d, want 1 and 0", startCalls, restartCalls)
	}
}

func TestRunVMLifecycleCommandRestartAlwaysRunsAndPersistsFailureState(t *testing.T) {
	st := openApplyTestStore(t)
	defer st.Close()
	spec := protocol.VMApplySpec{VMID: "vm-restart", ReplicaID: "replica-restart", GuestIP: "10.0.0.3", Port: 8081}
	if err := st.UpsertVM(store.VM{ID: spec.VMID, ReplicaID: spec.ReplicaID, State: "running", PID: 4242, Socket: "/tmp/vm-restart.sock", Health: "healthy"}); err != nil {
		t.Fatalf("seed running VM: %v", err)
	}
	startCalls, restartCalls := 0, 0
	start := func(context.Context, protocol.VMApplySpec) (store.VM, error) {
		startCalls++
		return store.VM{}, nil
	}
	restartErr := errors.New("restart stop failed")
	restart := func(_ context.Context, got protocol.VMApplySpec) (store.VM, error) {
		restartCalls++
		if got.VMID != spec.VMID {
			t.Fatalf("restart got VM %q, want %q", got.VMID, spec.VMID)
		}
		return store.VM{ID: got.VMID, ReplicaID: got.ReplicaID, State: "running", PID: 4242, Socket: "/tmp/vm-restart.sock", Health: "healthy"}, restartErr
	}
	vm, err := runVMLifecycleCommand(context.Background(), st, "vm.restart", spec, start, restart, func(store.VM) bool { return true }, func(store.VM) (bool, error) { return true, nil })
	if !errors.Is(err, restartErr) || vm.State != "running" {
		t.Fatalf("restart result: vm=%+v err=%v", vm, err)
	}
	if startCalls != 0 || restartCalls != 1 {
		t.Fatalf("start calls=%d restart calls=%d, want 0 and 1", startCalls, restartCalls)
	}
	got, ok := st.GetVM(spec.VMID)
	if !ok || got.State != "running" || got.Error != restartErr.Error() {
		t.Fatalf("restart failure did not preserve runtime state/error: ok=%v vm=%+v", ok, got)
	}
}

func TestRunVMLifecycleCommandRestartPersistsRuntimeState(t *testing.T) {
	st := openApplyTestStore(t)
	defer st.Close()
	spec := protocol.VMApplySpec{VMID: "vm-restart-ok", ReplicaID: "replica-restart-ok"}
	restartCalls := 0
	restarted := store.VM{ID: spec.VMID, ReplicaID: spec.ReplicaID, State: "running", PID: 5151, Socket: "/tmp/vm-restart-ok.sock", Health: "checking"}
	vm, err := runVMLifecycleCommand(context.Background(), st, "vm.restart", spec,
		func(context.Context, protocol.VMApplySpec) (store.VM, error) {
			t.Fatal("restart command called the start function")
			return store.VM{}, nil
		},
		func(_ context.Context, got protocol.VMApplySpec) (store.VM, error) {
			restartCalls++
			if got.VMID != spec.VMID {
				t.Fatalf("restart got VM %q, want %q", got.VMID, spec.VMID)
			}
			return restarted, nil
		},
		func(store.VM) bool { return true },
		func(store.VM) (bool, error) { return true, nil },
	)
	if err != nil || vm.State != "running" || vm.PID != restarted.PID {
		t.Fatalf("restart result: vm=%+v err=%v", vm, err)
	}
	if restartCalls != 1 {
		t.Fatalf("restart called %d times, want exactly once", restartCalls)
	}
	got, ok := st.GetVM(spec.VMID)
	if !ok || got.State != "running" || got.PID != restarted.PID {
		t.Fatalf("restart runtime state was not persisted: ok=%v vm=%+v", ok, got)
	}
}

func TestApplyVMIdempotentAllowsRetryAfterConfirmedProcessExit(t *testing.T) {
	st := openApplyTestStore(t)
	defer st.Close()
	if err := st.UpsertVM(store.VM{ID: "vm-1", ReplicaID: "replica-1", State: "failed", PID: 4242, Socket: "/tmp/fc.sock"}); err != nil {
		t.Fatalf("seed failed VM: %v", err)
	}
	applyCalls := 0
	vm, err := applyVMIdempotent(context.Background(), st,
		protocol.VMApplySpec{VMID: "vm-1", ReplicaID: "replica-1"},
		func(_ context.Context, spec protocol.VMApplySpec) (store.VM, error) {
			applyCalls++
			return store.VM{ID: spec.VMID, ReplicaID: spec.ReplicaID, State: "running"}, nil
		},
		func(store.VM) bool { return false },
		func(store.VM) (bool, error) { return false, nil },
	)
	if err != nil || vm.State != "running" {
		t.Fatalf("retry after confirmed exit: vm=%+v err=%v", vm, err)
	}
	if applyCalls != 1 {
		t.Fatalf("runtime Apply called %d times, want one", applyCalls)
	}
}
