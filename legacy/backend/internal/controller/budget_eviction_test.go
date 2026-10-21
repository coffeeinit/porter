package controller

import (
	"context"
	"fmt"
	"testing"
	"time"

	"porter/internal/autoscale"
	"porter/internal/store"
	"porter/internal/types"
)

// Disruption-budget gate on the failover path (SRS 58): budgeted workloads
// only shed as many VMs per cycle as the budget allows, the rest wait for
// the next reconcile, and the decision is explicit (logged), never silent.

// fakeBudgetStore extends fakeHostStore with the eviction-gate surface
// (project settings + active operations), mirroring *store.Store.
type fakeBudgetStore struct {
	*fakeHostStore
	settings  map[string]map[string]any // projectID -> section -> payload
	activeOps []store.Operation
}

func (f *fakeBudgetStore) GetProjectSettings(projectID, section string) map[string]any {
	if sec, ok := f.settings[projectID]; ok {
		if data, ok := sec[section].(map[string]any); ok {
			return data
		}
	}
	return nil
}

func (f *fakeBudgetStore) ActiveOperations() []store.Operation { return f.activeOps }

// failoverScenario: ten healthy VMs of project p1, all placed on the dead
// node, one live target server.
func failoverScenario() *fakeBudgetStore {
	fs := &fakeBudgetStore{fakeHostStore: &fakeHostStore{
		place: map[string]store.Placement{},
	}}
	for i := 1; i <= 10; i++ {
		id := fmt.Sprintf("vm%d", i)
		fs.vms = append(fs.vms, &types.VM{ID: id, ProjectID: "p1", HealthStatus: types.HealthHealthy})
		fs.place[id] = store.Placement{ID: "pl" + id, WorkloadID: id, NodeID: "node-dead", State: "active"}
	}
	fs.servers = []*types.Server{{ID: "node-live", Status: "ready", VCPUs: 64, MemMiB: 131072}}
	return fs
}

// completeMigration models a migrate op that finished: the VM now lives on
// the target node and its op is no longer active.
func completeMigration(fs *fakeBudgetStore, vmID string) {
	fs.place[vmID] = store.Placement{ID: "pl" + vmID, WorkloadID: vmID, NodeID: "node-live", State: "active"}
	kept := fs.activeOps[:0]
	for _, op := range fs.activeOps {
		if op.ResourceID != vmID {
			kept = append(kept, op)
		}
	}
	fs.activeOps = kept
}

func p1MigrateOps(ops []createdOp) []createdOp {
	out := []createdOp{}
	for _, op := range ops {
		if op.kind == "migrate" && op.resourceID != "vm-p2" {
			out = append(out, op)
		}
	}
	return out
}

func hasHostUnreachable(ops []createdOp) bool {
	for _, op := range ops {
		if op.kind == "host-unreachable" {
			return true
		}
	}
	return false
}

func TestHostControllerFailoverRespectsDisruptionBudget(t *testing.T) {
	now := time.Now().Truncate(time.Hour)
	fs := failoverScenario()
	fs.settings = map[string]map[string]any{
		"p1": {autoscale.BudgetSection: map[string]any{"min_available": 8}},
	}
	c := NewHostControllerWithStore(fs, time.Minute, time.Minute)
	c.now = func() time.Time { return now }

	if err := c.Reconcile(context.Background(), StaleNode{store.Heartbeat{NodeID: "node-dead", ReportedAt: now.Add(-time.Hour)}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := len(p1MigrateOps(fs.created)); got != 2 {
		t.Fatalf("min_available 8 over 10 replicas must allow exactly 2 evictions, got %d: %+v", got, fs.created)
	}
	if !hasHostUnreachable(fs.created) {
		t.Fatal("host-unreachable op must still be recorded")
	}

	// Stateless per invocation: with both evictions in flight the budget is
	// exhausted — a retry must not create new ops (the in-flight holders
	// replay their idempotency keys, everything else stays skipped).
	fs.activeOps = []store.Operation{
		{ID: "op1", Kind: "migrate", ResourceKind: "vm", ResourceID: "vm1"},
		{ID: "op2", Kind: "migrate", ResourceKind: "vm", ResourceID: "vm2"},
	}
	before := len(fs.created)
	if err := c.Reconcile(context.Background(), StaleNode{store.Heartbeat{NodeID: "node-dead", ReportedAt: now.Add(-time.Hour)}}); err != nil {
		t.Fatalf("retry reconcile: %v", err)
	}
	if got := len(fs.created[before:]); got != 0 {
		t.Fatalf("exhausted budget must skip every VM, got %d new ops: %+v", got, fs.created[before:])
	}

	// As evictions complete the budget refills, and the next reconcile
	// retries the VMs it skipped: two slots free up, vm3+vm4 go next.
	completeMigration(fs, "vm1")
	completeMigration(fs, "vm2")
	before = len(fs.created)
	if err := c.Reconcile(context.Background(), StaleNode{store.Heartbeat{NodeID: "node-dead", ReportedAt: now.Add(-time.Hour)}}); err != nil {
		t.Fatalf("refill reconcile: %v", err)
	}
	got := p1MigrateOps(fs.created[before:])
	if len(got) != 2 {
		t.Fatalf("completed evictions must free two slots, got %d new migrate ops: %+v", len(got), got)
	}
	for _, op := range got {
		if op.resourceID != "vm3" && op.resourceID != "vm4" {
			t.Fatalf("expected retries for the previously skipped vm3/vm4, got %s", op.resourceID)
		}
	}
}

func TestHostControllerFailoverMaxUnavailableBudget(t *testing.T) {
	now := time.Now().Truncate(time.Hour)
	fs := failoverScenario()
	fs.settings = map[string]map[string]any{
		"p1": {autoscale.BudgetSection: map[string]any{"max_unavailable": 3}},
	}
	c := NewHostControllerWithStore(fs, time.Minute, time.Minute)
	c.now = func() time.Time { return now }

	if err := c.Reconcile(context.Background(), StaleNode{store.Heartbeat{NodeID: "node-dead", ReportedAt: now.Add(-time.Hour)}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := len(p1MigrateOps(fs.created)); got != 3 {
		t.Fatalf("max_unavailable 3 must allow exactly 3 evictions, got %d", got)
	}
}

func TestHostControllerFailoverUnbudgetedWorkloadUngated(t *testing.T) {
	now := time.Now().Truncate(time.Hour)
	fs := failoverScenario()
	c := NewHostControllerWithStore(fs, time.Minute, time.Minute)
	c.now = func() time.Time { return now }

	if err := c.Reconcile(context.Background(), StaleNode{store.Heartbeat{NodeID: "node-dead", ReportedAt: now.Add(-time.Hour)}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := len(p1MigrateOps(fs.created)); got != 10 {
		t.Fatalf("workloads without a budget must stay ungated, got %d of 10 migrate ops", got)
	}
}

func TestHostControllerFailoverUnreadableBudgetUngated(t *testing.T) {
	now := time.Now().Truncate(time.Hour)
	fs := failoverScenario()
	// A typo'd budget must not wedge failover: it is logged and ignored.
	fs.settings = map[string]map[string]any{
		"p1": {autoscale.BudgetSection: map[string]any{"min_available": "eight"}},
	}
	c := NewHostControllerWithStore(fs, time.Minute, time.Minute)
	c.now = func() time.Time { return now }

	if err := c.Reconcile(context.Background(), StaleNode{store.Heartbeat{NodeID: "node-dead", ReportedAt: now.Add(-time.Hour)}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := len(p1MigrateOps(fs.created)); got != 10 {
		t.Fatalf("unreadable budget must leave the workload ungated, got %d of 10 migrate ops", got)
	}
}

func TestHostControllerFailoverGateCountsOnlySameWorkload(t *testing.T) {
	now := time.Now().Truncate(time.Hour)
	fs := failoverScenario()
	fs.settings = map[string]map[string]any{
		"p1": {autoscale.BudgetSection: map[string]any{"min_available": 8}},
	}
	// An unrelated project's VM shares the dead node and holds an active
	// eviction; it must not consume p1's budget.
	fs.vms = append(fs.vms, &types.VM{ID: "vm-p2", ProjectID: "p2"})
	fs.place["vm-p2"] = store.Placement{ID: "pl-p2", WorkloadID: "vm-p2", NodeID: "node-dead", State: "active"}
	fs.activeOps = []store.Operation{{ID: "op-other", Kind: "migrate", ResourceKind: "vm", ResourceID: "vm-p2"}}
	c := NewHostControllerWithStore(fs, time.Minute, time.Minute)
	c.now = func() time.Time { return now }

	if err := c.Reconcile(context.Background(), StaleNode{store.Heartbeat{NodeID: "node-dead", ReportedAt: now.Add(-time.Hour)}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := len(p1MigrateOps(fs.created)); got != 2 {
		t.Fatalf("other workloads' evictions must not consume the budget, want 2 p1 migrate ops, got %d", got)
	}
	found := false
	for _, op := range migrateOpsAll(fs.created) {
		if op.resourceID == "vm-p2" {
			found = true
		}
	}
	if !found {
		t.Fatal("unbudgeted p2 VM must be failed over without a gate")
	}
}

func migrateOpsAll(ops []createdOp) []createdOp {
	out := []createdOp{}
	for _, op := range ops {
		if op.kind == "migrate" {
			out = append(out, op)
		}
	}
	return out
}
