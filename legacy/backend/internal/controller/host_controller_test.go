package controller

import (
	"context"
	"testing"
	"time"

	"porter/internal/store"
	"porter/internal/types"
)

// fakeHostStore implements hostStore in memory.
type fakeHostStore struct {
	stale   []store.Heartbeat
	stuck   []store.Operation
	vms     []*types.VM
	servers []*types.Server
	place   map[string]store.Placement
	created []createdOp
	states  map[string]string
}

type createdOp struct {
	kind, resourceKind, resourceID, idemKey string
	payload                                 map[string]string
}

func (f *fakeHostStore) StaleHeartbeats(_ time.Time) []store.Heartbeat { return f.stale }
func (f *fakeHostStore) ListStuckOperations(_ time.Time) []store.Operation {
	return f.stuck
}
func (f *fakeHostStore) CreateOperation(kind, resourceKind, resourceID, idemKey string) (string, error) {
	// Mirror the real store: lock_key wins on replay.
	for _, c := range f.created {
		if c.idemKey != "" && c.idemKey == idemKey {
			return idemKey, nil
		}
	}
	f.created = append(f.created, createdOp{kind: kind, resourceKind: resourceKind, resourceID: resourceID, idemKey: idemKey})
	return idemKey, nil
}
func (f *fakeHostStore) CreateOperationWithPayload(kind, resourceKind, resourceID, idemKey string, extra map[string]string) (string, error) {
	// lock_key wins: replaying an idempotency key returns the existing row
	// without recording a duplicate.
	for _, c := range f.created {
		if c.idemKey != "" && c.idemKey == idemKey {
			return idemKey, nil
		}
	}
	f.created = append(f.created, createdOp{kind: kind, resourceKind: resourceKind, resourceID: resourceID, idemKey: idemKey, payload: extra})
	return idemKey, nil
}
func (f *fakeHostStore) SetOperationState(id, state, _ string) error {
	if f.states == nil {
		f.states = map[string]string{}
	}
	f.states[id] = state
	return nil
}
func (f *fakeHostStore) ListVMs() []*types.VM         { return f.vms }
func (f *fakeHostStore) ListServers() []*types.Server { return f.servers }
func (f *fakeHostStore) ActivePlacement(workloadID string) (store.Placement, bool) {
	p, ok := f.place[workloadID]
	return p, ok
}

func TestHostControllerStaleNodeQueuesMigratePerVM(t *testing.T) {
	now := time.Now().Truncate(time.Hour)
	fs := &fakeHostStore{
		vms: []*types.VM{
			{ID: "vm1", ProjectID: "p1"},
			{ID: "vm2", ProjectID: "p1"},
			{ID: "vm3", ProjectID: "p1"}, // placed on a healthy node: no migrate
		},
		servers: []*types.Server{
			{ID: "node-dead", Status: "ready", VCPUs: 8, MemMiB: 16384},
			{ID: "node-live", Status: "ready", VCPUs: 8, MemMiB: 16384},
		},
		place: map[string]store.Placement{
			"vm1": {ID: "pl1", WorkloadID: "vm1", NodeID: "node-dead", State: "active"},
			"vm2": {ID: "pl2", WorkloadID: "vm2", NodeID: "node-dead", State: "active"},
			"vm3": {ID: "pl3", WorkloadID: "vm3", NodeID: "node-live", State: "active"},
		},
	}
	c := NewHostControllerWithStore(fs, time.Minute, time.Minute)
	c.now = func() time.Time { return now }

	err := c.Reconcile(context.Background(), StaleNode{store.Heartbeat{NodeID: "node-dead", ReportedAt: now.Add(-time.Hour)}})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var hostOps, migrateOps []createdOp
	for _, op := range fs.created {
		switch op.kind {
		case "host-unreachable":
			hostOps = append(hostOps, op)
		case "migrate":
			migrateOps = append(migrateOps, op)
		}
	}
	if len(hostOps) != 1 {
		t.Fatalf("expected 1 host-unreachable op, got %d", len(hostOps))
	}
	if len(migrateOps) != 2 {
		t.Fatalf("expected 2 migrate ops (vm1, vm2), got %d: %+v", len(migrateOps), migrateOps)
	}
	seen := map[string]bool{}
	for _, op := range migrateOps {
		if op.resourceKind != "vm" {
			t.Fatalf("migrate op must target resource_kind vm, got %q", op.resourceKind)
		}
		if op.payload["source"] != "node-dead" {
			t.Fatalf("migrate payload must carry source node, got %+v", op.payload)
		}
		if op.payload["target"] == "" {
			t.Fatalf("migrate payload must carry a scheduler-picked target, got %+v", op.payload)
		}
		if op.payload["target"] == "node-dead" {
			t.Fatalf("migrate target must never be the dead source, got %+v", op.payload)
		}
		if op.payload["target"] != "node-live" {
			t.Fatalf("expected failover target node-live, got %+v", op.payload)
		}
		if seen[op.resourceID] {
			t.Fatalf("duplicate migrate op for %s", op.resourceID)
		}
		seen[op.resourceID] = true
	}
	if !seen["vm1"] || !seen["vm2"] {
		t.Fatalf("expected migrate ops for vm1+vm2, got %v", seen)
	}

	// A second tick in the same hour must not duplicate (idempotency keys).
	before := len(fs.created)
	if err := c.Reconcile(context.Background(), StaleNode{store.Heartbeat{NodeID: "node-dead", ReportedAt: now.Add(-time.Hour)}}); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if len(fs.created) != before {
		t.Fatalf("ticks must never duplicate: %d ops before, %d after", before, len(fs.created))
	}
}

func TestHostControllerStaleNodeWithoutVMsOnlyMarksHost(t *testing.T) {
	now := time.Now().Truncate(time.Hour)
	fs := &fakeHostStore{}
	c := NewHostControllerWithStore(fs, time.Minute, time.Minute)
	c.now = func() time.Time { return now }

	if err := c.Reconcile(context.Background(), StaleNode{store.Heartbeat{NodeID: "node-empty", ReportedAt: now}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(fs.created) != 1 || fs.created[0].kind != "host-unreachable" {
		t.Fatalf("expected only the host-unreachable op, got %+v", fs.created)
	}
}

func TestHostControllerFailoverNoFitSkipsMigrateKeepsAttention(t *testing.T) {
	now := time.Now().Truncate(time.Hour)
	fs := &fakeHostStore{
		vms: []*types.VM{
			{ID: "vm-big", ProjectID: "p1", VCPUs: 64, MemMiB: 131072},
		},
		servers: []*types.Server{
			// Too small for vm-big: scheduler finds no fit.
			{ID: "node-small", Status: "Ready", VCPUs: 2, MemMiB: 4096},
		},
		place: map[string]store.Placement{
			"vm-big": {ID: "pl1", WorkloadID: "vm-big", NodeID: "node-dead", State: "active"},
		},
	}
	c := NewHostControllerWithStore(fs, time.Minute, time.Minute)
	c.now = func() time.Time { return now }

	if err := c.Reconcile(context.Background(), StaleNode{store.Heartbeat{NodeID: "node-dead", ReportedAt: now.Add(-time.Hour)}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(fs.created) != 1 || fs.created[0].kind != "host-unreachable" {
		t.Fatalf("no fit must leave only the host-unreachable op, got %+v", fs.created)
	}
	key := fs.created[0].idemKey
	if fs.states[key] != "NEEDS_ATTENTION" {
		t.Fatalf("host-unreachable op must be NEEDS_ATTENTION, got %q", fs.states[key])
	}
}

func TestHostControllerFailoverSkipsDeadSource(t *testing.T) {
	now := time.Now().Truncate(time.Hour)
	fs := &fakeHostStore{
		vms: []*types.VM{
			{ID: "vm1", ProjectID: "p1"},
		},
		servers: []*types.Server{
			// Only the dead node itself is schedulable: it must be
			// skipped, so no self-migrate op is created.
			{ID: "node-dead", Status: "ONLINE", VCPUs: 8, MemMiB: 16384},
		},
		place: map[string]store.Placement{
			"vm1": {ID: "pl1", WorkloadID: "vm1", NodeID: "node-dead", State: "active"},
		},
	}
	c := NewHostControllerWithStore(fs, time.Minute, time.Minute)
	c.now = func() time.Time { return now }

	if err := c.Reconcile(context.Background(), StaleNode{store.Heartbeat{NodeID: "node-dead", ReportedAt: now.Add(-time.Hour)}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	for _, op := range fs.created {
		if op.kind == "migrate" {
			t.Fatalf("dead source must never be its own failover target, got %+v", op)
		}
	}
	if len(fs.created) != 1 || fs.created[0].kind != "host-unreachable" {
		t.Fatalf("expected only the host-unreachable op, got %+v", fs.created)
	}
}
