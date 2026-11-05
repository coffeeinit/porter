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
