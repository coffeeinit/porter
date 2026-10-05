package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	"porter/internal/store"
	"porter/internal/types"
)

// T11 hermetic host-failover tests. They reuse fakeHostStore from
// host_controller_test.go (same package) — no new fakes, no PG.

// migrateOpsByVMT11 indexes migrate ops by target VM id.
func migrateOpsByVMT11(ops []createdOp) map[string]createdOp {
	out := map[string]createdOp{}
	for _, op := range ops {
		if op.kind == "migrate" {
			out[op.resourceID] = op
		}
	}
	return out
}

func countKindT11(ops []createdOp, kind string) int {
	n := 0
	for _, op := range ops {
		if op.kind == kind {
			n++
		}
	}
	return n
}

// Failover across several live candidates: every VM placed on the dead node
// gets a migrate op whose target is non-empty, is never the dead source, and
// is one of the live nodes.
func TestT11FailoverPicksNonEmptyTargetSkippingDead(t *testing.T) {
	now := time.Now().Truncate(time.Hour)
	fs := &fakeHostStore{
		vms: []*types.VM{
			{ID: "t11-vm1", ProjectID: "p1", VCPUs: 2, MemMiB: 1024},
			{ID: "t11-vm2", ProjectID: "p1", VCPUs: 1, MemMiB: 512},
		},
		servers: []*types.Server{
			{ID: "t11-dead", Status: "ready", VCPUs: 8, MemMiB: 16384},
			{ID: "t11-live-a", Status: "ready", VCPUs: 4, MemMiB: 8192},
			{ID: "t11-live-b", Status: "ready", VCPUs: 8, MemMiB: 16384},
		},
		place: map[string]store.Placement{
			"t11-vm1": {ID: "pl1", WorkloadID: "t11-vm1", NodeID: "t11-dead", State: "active"},
			"t11-vm2": {ID: "pl2", WorkloadID: "t11-vm2", NodeID: "t11-dead", State: "active"},
		},
	}
	c := NewHostControllerWithStore(fs, time.Minute, time.Minute)
	c.now = func() time.Time { return now }

	if err := c.Reconcile(context.Background(),
		StaleNode{store.Heartbeat{NodeID: "t11-dead", ReportedAt: now.Add(-time.Hour)}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if countKindT11(fs.created, "host-unreachable") != 1 {
		t.Fatalf("expected 1 host-unreachable op, got %+v", fs.created)
	}
	got := migrateOpsByVMT11(fs.created)
	if len(got) != 2 {
		t.Fatalf("expected 2 migrate ops, got %+v", fs.created)
	}
	live := map[string]bool{"t11-live-a": true, "t11-live-b": true}
	keys := map[string]bool{}
	for vmID, op := range got {
		if op.resourceKind != "vm" {
			t.Errorf("vm %s: migrate op must target resource_kind vm, got %q", vmID, op.resourceKind)
		}
		if op.payload["source"] != "t11-dead" {
			t.Errorf("vm %s: payload must carry source t11-dead, got %+v", vmID, op.payload)
		}
		tgt := op.payload["target"]
		if tgt == "" {
			t.Errorf("vm %s: scheduler-picked target must be non-empty", vmID)
		}
		if tgt == "t11-dead" {
			t.Errorf("vm %s: target must skip the dead source", vmID)
		}
		if tgt != "" && !live[tgt] {
			t.Errorf("vm %s: target %q is not a live node", vmID, tgt)
		}
		if op.idemKey == "" {
			t.Errorf("vm %s: migrate op must carry an idempotency key", vmID)
		}
		if keys[op.idemKey] {
			t.Errorf("vm %s: duplicate idempotency key %q", vmID, op.idemKey)
		}
		keys[op.idemKey] = true
	}
}

// No-fit: no migrate op is created, the host-unreachable NEEDS_ATTENTION op
// stays as the visible trail, and a same-hour re-tick duplicates nothing.
func TestT11NoFitNoOpAttentionPreservedAndDeduped(t *testing.T) {
	now := time.Now().Truncate(time.Hour)
	fs := &fakeHostStore{
		vms: []*types.VM{
			{ID: "t11-huge", ProjectID: "p1", VCPUs: 128, MemMiB: 262144},
		},
		servers: []*types.Server{
			{ID: "t11-small", Status: "ready", VCPUs: 1, MemMiB: 1024},
		},
		place: map[string]store.Placement{
			"t11-huge": {ID: "pl1", WorkloadID: "t11-huge", NodeID: "t11-gone", State: "active"},
		},
	}
	c := NewHostControllerWithStore(fs, time.Minute, time.Minute)
	c.now = func() time.Time { return now }
	stale := StaleNode{store.Heartbeat{NodeID: "t11-gone", ReportedAt: now.Add(-time.Hour)}}

	if err := c.Reconcile(context.Background(), stale); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(fs.created) != 1 || fs.created[0].kind != "host-unreachable" {
		t.Fatalf("no fit must leave only the host-unreachable op, got %+v", fs.created)
	}
	if got := fs.states[fs.created[0].idemKey]; got != "NEEDS_ATTENTION" {
		t.Fatalf("trail op must be NEEDS_ATTENTION, got %q", got)
	}

	before := len(fs.created)
	if err := c.Reconcile(context.Background(), stale); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if len(fs.created) != before {
		t.Fatalf("same-hour ticks must dedupe: %d before, %d after", before, len(fs.created))
	}
	if n := countKindT11(fs.created, "migrate"); n != 0 {
		t.Fatalf("no fit must never queue migrate ops, got %d", n)
	}
}

// Mixed fit: the VM that fits gets a migrate op, the oversized one is skipped
// without failing the whole failover.
func TestT11MixedFitOnlyFittingVMMigrates(t *testing.T) {
	now := time.Now().Truncate(time.Hour)
	fs := &fakeHostStore{
		vms: []*types.VM{
			{ID: "t11-fits", ProjectID: "p1", VCPUs: 1, MemMiB: 512},
			{ID: "t11-toobig", ProjectID: "p1", VCPUs: 64, MemMiB: 131072},
		},
		servers: []*types.Server{
			{ID: "t11-mid", Status: "Ready", VCPUs: 4, MemMiB: 8192},
		},
		place: map[string]store.Placement{
			"t11-fits":   {ID: "pl1", WorkloadID: "t11-fits", NodeID: "t11-dead", State: "active"},
			"t11-toobig": {ID: "pl2", WorkloadID: "t11-toobig", NodeID: "t11-dead", State: "active"},
		},
	}
	c := NewHostControllerWithStore(fs, time.Minute, time.Minute)
	c.now = func() time.Time { return now }

	if err := c.Reconcile(context.Background(),
		StaleNode{store.Heartbeat{NodeID: "t11-dead", ReportedAt: now.Add(-time.Hour)}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := migrateOpsByVMT11(fs.created)
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 migrate op (t11-fits), got %+v", fs.created)
	}
	op, ok := got["t11-fits"]
	if !ok {
		t.Fatalf("expected migrate op for t11-fits, got %+v", fs.created)
	}
	if op.payload["target"] != "t11-mid" {
		t.Fatalf("expected target t11-mid, got %+v", op.payload)
	}
}

// Hour scoping: same-hour ticks dedupe, but the next hour re-keys and queues
// fresh ops (host-unreachable + migrate) again.
func TestT11FailoverHourScopingRekeysNextHour(t *testing.T) {
	now := time.Now().Truncate(time.Hour)
	fs := &fakeHostStore{
		vms: []*types.VM{
			{ID: "t11-vm", ProjectID: "p1", VCPUs: 1, MemMiB: 512},
		},
		servers: []*types.Server{
			{ID: "t11-live", Status: "online", VCPUs: 8, MemMiB: 16384},
		},
		place: map[string]store.Placement{
			"t11-vm": {ID: "pl1", WorkloadID: "t11-vm", NodeID: "t11-dead", State: "active"},
		},
	}
	c := NewHostControllerWithStore(fs, time.Minute, time.Minute)
	c.now = func() time.Time { return now }
	stale := func() StaleNode {
		return StaleNode{store.Heartbeat{NodeID: "t11-dead", ReportedAt: c.now().Add(-time.Hour)}}
	}

	if err := c.Reconcile(context.Background(), stale()); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if len(fs.created) != 2 { // host-unreachable + migrate
		t.Fatalf("tick 1 must queue 2 ops, got %+v", fs.created)
	}
	if err := c.Reconcile(context.Background(), stale()); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if len(fs.created) != 2 {
		t.Fatalf("same-hour tick 2 must dedupe, got %+v", fs.created)
	}

	later := now.Add(2 * time.Hour)
	c.now = func() time.Time { return later }
	if err := c.Reconcile(context.Background(), stale()); err != nil {
		t.Fatalf("next-hour tick: %v", err)
	}
	if len(fs.created) != 4 {
		t.Fatalf("next hour must re-key fresh ops (2 more), got %+v", fs.created)
	}
	for _, op := range fs.created[2:] {
		if !strings.Contains(op.idemKey, "migrate|") && !strings.Contains(op.idemKey, "host-unreachable|") {
			t.Fatalf("re-keyed op must carry an idempotency key, got %+v", op)
		}
	}
}
