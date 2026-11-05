package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"porter/agent/internal/store"
)

func TestTransitionVMDoesNotPersistStateWhenCleanupFails(t *testing.T) {
	st := openApplyTestStore(t)
	defer st.Close()
	initial := store.VM{ID: "vm-1", ReplicaID: "replica-1", State: "running", PID: 1234, Socket: "/run/vm.sock", TapName: "ptvm1"}
	if err := st.UpsertVM(initial); err != nil {
		t.Fatalf("seed VM: %v", err)
	}
	cleanupErr := errors.New("tap is still in use")
	err := transitionVM(context.Background(), st, initial.ID, "deleted", func(_ context.Context, got store.VM) error {
		if got.ID != initial.ID || got.State != "running" {
			t.Fatalf("cleanup received unexpected VM: %+v", got)
		}
		return cleanupErr
	})
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("transition error = %v, want cleanup error", err)
	}
	got, ok := st.GetVM(initial.ID)
	if !ok || got.State != "running" {
		t.Fatalf("failed cleanup changed local state: ok=%v vm=%+v", ok, got)
	}
}

func TestTransitionVMPersistsSuccessfulCleanupAndKeepsIdentity(t *testing.T) {
	st := openApplyTestStore(t)
	defer st.Close()
	initial := store.VM{ID: "vm-1", ReplicaID: "replica-1", State: "running", PID: 1234, Socket: "/run/vm.sock", TapName: "ptvm1", GuestIP: "10.42.0.2", Port: 8080, Health: "healthy"}
	if err := st.UpsertVM(initial); err != nil {
		t.Fatalf("seed VM: %v", err)
	}
	if err := transitionVM(context.Background(), st, initial.ID, "stopped", func(context.Context, store.VM) error { return nil }); err != nil {
		t.Fatalf("transition VM: %v", err)
	}
	got, ok := st.GetVM(initial.ID)
	if !ok || got.State != "stopped" || got.ReplicaID != initial.ReplicaID || got.GuestIP != initial.GuestIP || got.Port != initial.Port {
		t.Fatalf("successful transition lost identity or state: ok=%v vm=%+v", ok, got)
	}
	if got.Health != "" || got.Error != "" {
		t.Fatalf("successful transition retained stale health/error: %+v", got)
	}
}

func TestTransitionVMReportsPersistenceFailure(t *testing.T) {
	st := openApplyTestStore(t)
	initial := store.VM{ID: "vm-1", ReplicaID: "replica-1", State: "running"}
	if err := st.UpsertVM(initial); err != nil {
		t.Fatalf("seed VM: %v", err)
	}
	err := transitionVM(context.Background(), st, initial.ID, "deleted", func(context.Context, store.VM) error {
		if err := st.Close(); err != nil {
			t.Fatalf("close store during cleanup: %v", err)
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "persist vm") {
		t.Fatalf("persistence failure was not propagated: %v", err)
	}
}

func TestTransitionVMRequiresID(t *testing.T) {
	st := openApplyTestStore(t)
	defer st.Close()
	err := transitionVM(context.Background(), st, "", "deleted", nil)
	if err == nil || !strings.Contains(err.Error(), "vm id required") {
		t.Fatalf("empty VM ID error = %v", err)
	}
}
