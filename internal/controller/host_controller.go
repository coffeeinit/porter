// Package controller host reconciler (OCM-06): node liveness from agent
// heartbeats plus stuck-operation expiry. Stale nodes (silent past the
// threshold) get a durable NEEDS_ATTENTION operation so the outage is
// visible in /operations; stuck ops are failed with a reason so placements
// and routes unblock. Node state marking stays in the node controller,
// which owns the servers schema.
package controller

import (
	"context"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"porter/internal/autoscale"
	"porter/internal/scheduler"
	"porter/internal/store"
	"porter/internal/types"
)

// hostStore is the narrow persistence surface the host reconciler needs.
// *store.Store satisfies it in production; tests inject fakes via
// NewHostControllerWithStore.
type hostStore interface {
	StaleHeartbeats(olderThan time.Time) []store.Heartbeat
	ListStuckOperations(olderThan time.Time) []store.Operation
	CreateOperation(kind, resourceKind, resourceID, idemKey string) (string, error)
	CreateOperationWithPayload(kind, resourceKind, resourceID, idemKey string, extra map[string]string) (string, error)
	SetOperationState(id, state, errMsg string) error
	ListVMs() []*types.VM
	ListServers() []*types.Server
	ActivePlacement(workloadID string) (store.Placement, bool)
}

// evictionGateStore is the optional store surface the disruption-budget gate
// (SRS 58) needs: budgets ride the existing project_settings JSON store, and
// in-flight evictions are the active migrate ops. *store.Store satisfies it;
// minimal fakes may omit it, in which case the gate is disabled and
// failover behaves exactly as before the budget existed.
type evictionGateStore interface {
	GetProjectSettings(projectID, section string) map[string]any
	ActiveOperations() []store.Operation
}

// HostController reconciles node liveness and operation leases.
type HostController struct {
	st         hostStore
	staleAfter time.Duration
	stuckAfter time.Duration
	now        func() time.Time
}

// NewHostController wires the host reconciler. staleAfter marks silence,
// stuckAfter expires operation leases (OCM: 180s heartbeats, 30m ops).
func NewHostController(st *store.Store, staleAfter, stuckAfter time.Duration) *HostController {
	return NewHostControllerWithStore(st, staleAfter, stuckAfter)
}

// NewHostControllerWithStore wires the reconciler against any hostStore
// (production *store.Store or a test fake).
func NewHostControllerWithStore(st hostStore, staleAfter, stuckAfter time.Duration) *HostController {
	return &HostController{st: st, staleAfter: staleAfter, stuckAfter: stuckAfter, now: time.Now}
}

// GetName implements Reconciler.
func (c *HostController) GetName() string { return "host" }

// StaleNode is one silent agent report.
type StaleNode struct {
	store.Heartbeat
}

// StuckOp is one leased operation that never finished.
type StuckOp struct {
	store.Operation
}

// List implements Reconciler: stale heartbeats plus stuck operations.
func (c *HostController) List(_ context.Context) ([]interface{}, error) {
	now := c.now()
	out := []interface{}{}
	for _, h := range c.st.StaleHeartbeats(now.Add(-c.staleAfter)) {
		out = append(out, StaleNode{h})
	}
	for _, o := range c.st.ListStuckOperations(now.Add(-c.stuckAfter)) {
		out = append(out, StuckOp{o})
	}
	return out, nil
}

// Reconcile implements Reconciler: durable NEEDS_ATTENTION per stale node
// (idempotency-keyed per node-hour, so ticks never duplicate), FAILED with
// reason per stuck op. A stale node also gets one QUEUED migrate operation
// per VM with an active placement on it (idempotency-keyed per vm-node-hour):
// the payload carries the source node plus a scheduler-picked target, so the
// plan passes migrate.Plan.Validate downstream. When no healthy node fits, no
// migrate op is created — the host-unreachable NEEDS_ATTENTION op recorded
// here stays as the visible trail.
func (c *HostController) Reconcile(_ context.Context, obj interface{}) error {
	switch o := obj.(type) {
	case StaleNode:
		hour := c.now().Truncate(time.Hour).Unix()
		key := fmt.Sprintf("host-unreachable|%s|%d", o.NodeID, hour)
		id, err := c.st.CreateOperation("host-unreachable", "node", o.NodeID, key)
		if err != nil {
			return err
		}
		if err := c.st.SetOperationState(id, "NEEDS_ATTENTION",
			fmt.Sprintf("agent silent since %s", o.ReportedAt.UTC().Format(time.RFC3339))); err != nil {
			return err
		}
		return c.failoverNodeVMs(o.NodeID, hour)
	case StuckOp:
		return c.st.SetOperationState(o.ID, "FAILED",
			fmt.Sprintf("reconciler: operation lease expired in %s", o.State))
	default:
		return fmt.Errorf("host controller: unknown object %T", obj)
	}
}

// failoverNodeVMs queues one migrate operation per VM with an active
// placement on the stale node, with the target filled by scheduler.PickNode
// over healthy servers (the dead source is never a candidate). Keys are per
// vm-node-hour so ticks never duplicate; lock_key wins on replay
// (CreateOperationWithPayload returns the existing row id). When no node
// fits, no op is created for that VM — an empty target would fail
// migrate.Plan.Validate in the task runner, so the VM is skipped and the
// host-unreachable NEEDS_ATTENTION op stands as the trail. When the VM's
// workload has a disruption budget (SRS 58) and it is exhausted — too many
// evictions already in flight — the VM is skipped this cycle with the
// explicit reason logged; the next reconcile retries it (the check is
// stateless per invocation). Per-VM errors are attempted-all,
// first-error-returned so one bad row never blocks the rest of the failover.
func (c *HostController) failoverNodeVMs(nodeID string, hour int64) error {
	vms := c.st.ListVMs()
	nodes := make([]scheduler.Node, 0)
	for _, srv := range c.st.ListServers() {
		if srv == nil || srv.ID == "" || srv.ID == nodeID {
			continue
		}
		nodes = append(nodes, scheduler.Node{
			ID:    srv.ID,
			Ready: serverReady(srv.Status),
			Arch:  srv.Arch,
			// Servers expose totals, not free capacity; use them as a
			// coarse proxy here and let admission re-check actual fit.
			VCPUFree:   srv.VCPUs,
			MemFreeMiB: srv.MemMiB,
		})
	}
	gate, gated := c.st.(evictionGateStore)
	allowance := map[string]int{}
	var firstErr error
	for _, vm := range vms {
		if vm == nil {
			continue
		}
		p, ok := c.st.ActivePlacement(vm.ID)
		if !ok || p.NodeID != nodeID {
			continue
		}
		target := scheduler.PickNode(nodes, scheduler.Request{VCPUs: vm.VCPUs, MemMiB: vm.MemMiB})
		if target == "" {
			log.Printf("host controller: no failover target for vm %s (source %s), skipping migrate op", vm.ID, nodeID)
			continue
		}
		if gated {
			key := vm.ProjectID + "/" + vm.ServiceName
			n, known := allowance[key]
			if !known {
				n = evictionAllowance(gate, vms, vm)
				allowance[key] = n
			}
			if n <= 0 {
				log.Printf("host controller: disruption budget exhausted for %s: vm %s eviction skipped this cycle, next reconcile retries", key, vm.ID)
				continue
			}
			allowance[key] = n - 1
		}
		key := fmt.Sprintf("migrate|%s|%s|%d", vm.ID, nodeID, hour)
		_, err := c.st.CreateOperationWithPayload("migrate", "vm", vm.ID, key,
			map[string]string{"source": nodeID, "target": target})
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// evictionAllowance computes how many more evictions the candidate's
// workload permits right now (SRS 58). The budget comes from the
// project_settings "disruption_budget" section ({"min_available":N} or
// {"max_unavailable":N}); an unset budget leaves the workload ungated, and
// an unreadable one is logged and ungated rather than wedged — failover must
// not stall on a typo'd setting, and the log line keeps that explicit. The
// pool counts persisted state only, so every call is reproducible:
//
//	desired  — all VMs of the project+service;
//	healthy  — VMs reporting healthy (blank health counts as healthy: an
//	           unset check never blocks);
//	evicting — active migrate/evict ops against the workload's VMs.
func evictionAllowance(gate evictionGateStore, vms []*types.VM, candidate *types.VM) int {
	raw := gate.GetProjectSettings(candidate.ProjectID, autoscale.BudgetSection)
	b, err := autoscale.BudgetFromSettings(raw)
	if err != nil {
		log.Printf("host controller: disruption budget for project %s unreadable (%v), evictions ungated", candidate.ProjectID, err)
		return math.MaxInt
	}
	if !b.Set() {
		return math.MaxInt
	}
	evicting := map[string]bool{}
	for _, op := range gate.ActiveOperations() {
		if op.ResourceKind == "vm" && (op.Kind == "migrate" || op.Kind == "evict") {
			evicting[op.ResourceID] = true
		}
	}
	pool := autoscale.Pool{}
	for _, vm := range vms {
		if vm == nil || vm.ProjectID != candidate.ProjectID || vm.ServiceName != candidate.ServiceName {
			continue
		}
		pool.Desired++
		if vm.HealthStatus == "" || vm.HealthStatus == types.HealthHealthy {
			pool.Healthy++
		}
		if evicting[vm.ID] {
			pool.Evicting++
		}
	}
	return autoscale.Allowance(b, pool)
}

// serverReady reports schedulability from a server status string.
func serverReady(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "ready", "online", "active":
		return true
	}
	return false
}
