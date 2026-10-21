// Package controller implements the Porter reconciliation controllers.
package controller

import (
	"context"
	"fmt"
	"time"

	"porter/internal/event"
	"porter/internal/health"
	"porter/internal/resource"
	"porter/internal/runtime"
	"porter/internal/store"
	"porter/internal/types"
)

// ReplicaController reconciles types.VM records (the store's replica model)
// against the Firecracker runtime. Desired state lives in the store; the
// controller boots/stops VMs and reflects observed state back through the store.
// Health (G3) runs in the loop: a running VM with a healthcheck is probed each
// cycle and replaced through the normal failed path when unhealthy.
type ReplicaController struct {
	store    *store.Store
	runtime  *runtime.VMManager
	hub      *event.Hub
	checker  *health.Checker
	interval time.Duration
}

// NewReplicaController creates a new replica controller.
func NewReplicaController(st *store.Store, rt *runtime.VMManager, hub *event.Hub, interval time.Duration) *ReplicaController {
	c := &ReplicaController{store: st, runtime: rt, hub: hub, interval: interval}
	c.checker = health.New(st, hub, func(ctx context.Context, vmID string) {
		if vm, ok := st.GetVM(vmID); ok && vm != nil {
			vm.State = resource.StateFailed
			vm.Error = "healthcheck failed"
			st.PutVM(vm)
		}
	})
	return c
}

// GetName returns the controller name.
func (c *ReplicaController) GetName() string { return "replica-controller" }

// List enumerates every VM the store knows about.
func (c *ReplicaController) List(ctx context.Context) ([]interface{}, error) {
	out := make([]interface{}, 0, len(c.store.ListVMs()))
	for _, vm := range c.store.ListVMs() {
		out = append(out, vm)
	}
	return out, nil
}

// Reconcile converges one VM toward its desired state.
func (c *ReplicaController) Reconcile(ctx context.Context, obj interface{}) error {
	vm, ok := obj.(*types.VM)
	if !ok {
		return fmt.Errorf("expected *types.VM, got %T", obj)
	}
	switch vm.State {
	case resource.StatePending, "":
		return c.reconcilePending(ctx, vm)
	case resource.StateBooting:
		return c.reconcileBooting(ctx, vm)
	case resource.StateRunning:
		return c.reconcileRunning(ctx, vm)
	case resource.StateStopping:
		return c.reconcileStopping(ctx, vm)
	case resource.StateStopped:
		return c.reconcileStopped(ctx, vm)
	case resource.StateFailed:
		return c.reconcileFailed(ctx, vm)
	case resource.StateDeleting, resource.StateDeleted:
		return c.reconcileDelete(ctx, vm)
	default:
		return c.reconcilePending(ctx, vm)
	}
}

// reconcilePending allocates network identity and boots the VM.
func (c *ReplicaController) reconcilePending(ctx context.Context, vm *types.VM) error {
	c.emit(vm, "vm.create.started", "replica reconcile: boot requested")
	spec, err := c.runtime.NetSpec(vm)
	if err != nil {
		vm.State = resource.StateFailed
		vm.Error = fmt.Sprintf("network allocation failed: %v", err)
		_ = c.updateStatus(ctx, vm)
		c.emit(vm, "vm.failed", vm.Error)
		return nil
	}
	vm.State = resource.StateBooting
	vm.HealthStatus = resource.HealthChecking
	if err := c.updateStatus(ctx, vm); err != nil {
		return err
	}
	if err := c.runtime.Boot(vm, spec); err != nil {
		vm.State = resource.StateFailed
		vm.Error = fmt.Sprintf("boot failed: %v", err)
		_ = c.updateStatus(ctx, vm)
		c.emit(vm, "vm.failed", vm.Error)
		return nil
	}
	vm.State = resource.StateRunning
	vm.IPAddress = spec.CIDR
	if err := c.updateStatus(ctx, vm); err != nil {
		return err
	}
	// T8: persist the runtime truth row + record the durable event.
	_ = c.store.UpsertMicroVM(vm.ID, vm.ID, "", string(resource.StateRunning), vm.VCPUs*1000, vm.MemMiB)
	c.emit(vm, "vm.created", "replica running")
	// Usage spine (commerce.md: meters from day one, rating later).
	c.store.RecordUsage(vm.ProjectID, "vm/"+vm.ID, "vcpu.count", float64(vm.VCPUs), "count", "boot:"+vm.ID)
	c.store.RecordUsage(vm.ProjectID, "vm/"+vm.ID, "mem.mib", float64(vm.MemMiB), "mib", "boot-mem:"+vm.ID)
	return nil
}

// reconcileBooting waits for a booting VM to reach a terminal state.
func (c *ReplicaController) reconcileBooting(ctx context.Context, vm *types.VM) error {
	// The runtime drives boot synchronously; a VM left in "booting" means the
	// expected running state was never persisted. Promote it if it has an IP.
	if vm.IPAddress != "" {
		vm.State = resource.StateRunning
		return c.updateStatus(ctx, vm)
	}
	return nil
}

// reconcileRunning keeps a running VM registered and detects disappearance.
// G3: a running VM declaring a healthcheck is probed every cycle; an unhealthy
// VM is failed so the next cycle replaces it through the restart policy.
func (c *ReplicaController) reconcileRunning(ctx context.Context, vm *types.VM) error {
	if vm.Healthcheck != nil {
		spec := health.HealthSpec{
			Type:        vm.Healthcheck.Type,
			Path:        vm.Healthcheck.Path,
			Port:        vm.Healthcheck.Port,
			IntervalSec: vm.Healthcheck.IntervalSec,
		}
		if !c.checker.Probe(ctx, vm, spec) {
			vm.State = resource.StateFailed
			vm.HealthStatus = resource.HealthUnhealthy
			vm.Error = "healthcheck failed"
			_ = c.updateStatus(ctx, vm)
			_ = c.store.SetMicroVMState(vm.ID, string(resource.StateFailed))
			c.emit(vm, "vm.failed", "healthcheck failed")
			return nil
		}
		if vm.HealthStatus != resource.HealthHealthy {
			vm.HealthStatus = resource.HealthHealthy
			return c.updateStatus(ctx, vm)
		}
	}
	return nil
}

// reconcileStopping stops the VM.
func (c *ReplicaController) reconcileStopping(ctx context.Context, vm *types.VM) error {
	if err := c.runtime.Stop(vm); err != nil {
		vm.Error = fmt.Sprintf("stop failed: %v", err)
		return c.updateStatus(ctx, vm)
	}
	vm.State = resource.StateStopped
	_ = c.store.SetMicroVMState(vm.ID, string(resource.StateStopped))
	return c.updateStatus(ctx, vm)
}

// reconcileStopped returns a VM to pending if it should be running again.
func (c *ReplicaController) reconcileStopped(ctx context.Context, vm *types.VM) error {
	// Restart policy is a desired-state concern; without a persisted desired
	// replica count on types.VM we leave stopped VMs stopped.
	return nil
}

// reconcileFailed attempts snapshot recovery or a retry.
func (c *ReplicaController) reconcileFailed(ctx context.Context, vm *types.VM) error {
	if vm.SnapshotStatus == "ready" && vm.SnapshotPath != "" {
		vm.State = resource.StateBooting
		vm.HealthStatus = resource.HealthChecking
		if err := c.updateStatus(ctx, vm); err != nil {
			return err
		}
		if err := c.runtime.Restore(ctx, vm, vm.SnapshotPath, vm.SnapshotMemPath); err != nil {
			vm.Error = fmt.Sprintf("restore failed: %v", err)
			return c.updateStatus(ctx, vm)
		}
		vm.State = resource.StateRunning
		return c.updateStatus(ctx, vm)
	}
	if vm.Restart == "always" || vm.Restart == "on-failure" {
		vm.State = resource.StatePending
		return c.updateStatus(ctx, vm)
	}
	return nil
}

// reconcileDelete tears down the VM from the runtime and store.
func (c *ReplicaController) reconcileDelete(ctx context.Context, vm *types.VM) error {
	_ = c.runtime.Stop(vm)
	_ = c.store.DeleteMicroVM(vm.ID)
	c.store.DeleteVM(vm.ID)
	c.emit(vm, "vm.deleted", "replica torn down")
	return nil
}

// emit records a durable lifecycle event and fans it out on the SSE hub.
func (c *ReplicaController) emit(vm *types.VM, name, desc string) {
	_ = c.store.AppendEvent(store.Event{
		Name:        name,
		Version:     1,
		Payload:     map[string]interface{}{"vm_id": vm.ID, "project_id": vm.ProjectID, "state": vm.State, "detail": desc},
		ScopeType:   "project",
		ScopeID:     vm.ProjectID,
		ResourceRef: "vm/" + vm.ID,
	})
	if c.hub != nil {
		c.hub.Broadcast(name, map[string]interface{}{"vm_id": vm.ID, "project_id": vm.ProjectID, "state": vm.State})
	}
}

// updateStatus persists the VM via the store.
func (c *ReplicaController) updateStatus(ctx context.Context, vm *types.VM) error {
	c.store.PutVM(vm)
	return nil
}
