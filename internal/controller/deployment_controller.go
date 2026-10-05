// Package controller implements the Porter reconciliation controllers.
package controller

import (
	"context"
	"fmt"
	"time"

	"porter/internal/resource"
	"porter/internal/store"
	"porter/internal/types"
)

// DeploymentController reconciles types.Deployment records. The deployments
// table (via store.CreateDeployment) is the durable truth; this controller
// converges each deployment's build + rollout state toward its desired route.
type DeploymentController struct {
	store    *store.Store
	interval time.Duration
}

// NewDeploymentController creates a new deployment controller.
func NewDeploymentController(st *store.Store, interval time.Duration) *DeploymentController {
	return &DeploymentController{store: st, interval: interval}
}

// GetName returns the controller name.
func (c *DeploymentController) GetName() string { return "deployment-controller" }

// List enumerates every deployment across all projects.
func (c *DeploymentController) List(ctx context.Context) ([]interface{}, error) {
	var out []interface{}
	for _, proj := range c.store.ListProjects() {
		for _, d := range c.store.ListDeployments(proj.ID) {
			out = append(out, d)
		}
	}
	return out, nil
}

// Reconcile converges one deployment toward its desired state.
func (c *DeploymentController) Reconcile(ctx context.Context, obj interface{}) error {
	d, ok := obj.(*types.Deployment)
	if !ok {
		return fmt.Errorf("expected *types.Deployment, got %T", obj)
	}

	// Rollback is the highest-priority intent.
	if d.RollbackTo != "" {
		return c.reconcileRollback(ctx, d)
	}

	switch c.phase(d) {
	case "building":
		return c.reconcileBuilding(ctx, d)
	case "rolling":
		return c.reconcileRolling(ctx, d)
	case "promoting":
		return c.reconcilePromoting(ctx, d)
	case "failed":
		return c.reconcileFailed(ctx, d)
	default: // ready
		return nil
	}
}

// phase derives the deployment lifecycle stage from its persisted fields.
func (c *DeploymentController) phase(d *types.Deployment) string {
	switch d.BuildStatus {
	case "", "pending", "building":
		return "building"
	case "failed":
		return "failed"
	case "completed", "success":
		if d.RouteWeight > 0 && d.RouteWeight < 100 {
			return "rolling"
		}
		if d.IsProduction && d.RouteWeight == 100 {
			return "promoting"
		}
		return "ready"
	}
	return "building"
}

// reconcileBuilding waits on the associated build to complete.
func (c *DeploymentController) reconcileBuilding(ctx context.Context, d *types.Deployment) error {
	build, ok := c.store.GetBuild(d.ID) // deployments and builds share the id today
	if !ok {
		return nil // build still queued
	}
	switch build.BuildStatus {
	case "completed", "success":
		d.BuildStatus = "completed"
		if build.Image != "" {
			d.ImageDigest = build.Image
		}
	case "failed":
		d.BuildStatus = "failed"
	default:
		return nil // still building
	}
	return c.updateStatus(ctx, d)
}

// reconcileRolling monitors progress of a partial route rollout (task T9).
// Traffic weight steps 0→100 with the healthy count against the desired pool
// size (project ReplicasDesired, falling back to the vm_ids approximation);
// full promotion happens only when every desired replica is healthy.
func (c *DeploymentController) reconcileRolling(ctx context.Context, d *types.Deployment) error {
	running := 0
	for _, vm := range c.store.ListReplicas(d.ProjectID) {
		if vm.DeploymentID != d.ID {
			continue
		}
		if vm.State == resource.StateRunning {
			running++
		}
	}
	desired := copyCount(d)
	if proj, ok := c.store.GetProject(d.ProjectID); ok && proj != nil {
		if proj.ReplicasDesired > 0 {
			desired = proj.ReplicasDesired
		}
		// WP1 VPS: a persistent workload never converges to zero. When
		// anything drives a persistent project's desired count below its
		// floor (autoscaler min 0, paused deploy), clamp desired state back
		// up instead of letting the ephemeral scale-to-zero path tear the
		// last replica out. Idempotent: a no-op once ReplicasDesired >= 1,
		// and this controller never stops or deletes replicas itself.
		if floor := proj.ReplicaFloor(); desired < floor {
			desired = floor
			proj.ReplicasDesired = floor
			c.store.PutProject(proj)
		}
	}
	// Reflect the observed fleet in vm_ids so the API can report progress.
	d.VMIDs = vmIDsByDeployment(c.store.ListReplicas(d.ProjectID), d.ID)
	if running >= desired {
		return c.reconcilePromoting(ctx, d)
	}
	weight := 0
	if desired > 0 {
		weight = running * 100 / desired
	}
	if weight != d.RouteWeight {
		d.RouteWeight = weight
		d.RolloutPercent = weight
		return c.updateStatus(ctx, d)
	}
	return c.updateStatus(ctx, d)
}

// reconcilePromoting completes the rollout to full production traffic.
func (c *DeploymentController) reconcilePromoting(ctx context.Context, d *types.Deployment) error {
	d.RouteWeight = 100
	d.RolloutPercent = 100
	return c.updateStatus(ctx, d)
}

// reconcileFailed marks a build-failed deployment for retry or leaves it for
// manual intervention; there is no auto-retry without a strategy field.
func (c *DeploymentController) reconcileFailed(ctx context.Context, d *types.Deployment) error {
	d.BuildStatus = "failed"
	return c.updateStatus(ctx, d)
}

// reconcileRollback restores the fields of a prior deployment into this one.
func (c *DeploymentController) reconcileRollback(ctx context.Context, d *types.Deployment) error {
	target, ok := c.store.GetDeployment(d.ProjectID, d.RollbackTo)
	if !ok {
		// Point of no return: clear the rollback request so we don't loop.
		d.RollbackTo = ""
		return c.updateStatus(ctx, d)
	}
	d.ImageDigest = target.ImageDigest
	d.GuestBase = target.GuestBase
	d.Environment = target.Environment
	d.RouteWeight = target.RouteWeight
	d.BuildStatus = "completed"
	d.RollbackTo = ""
	d.Revision++
	return c.updateStatus(ctx, d)
}

// updateStatus persists the deployment via the store's upsert.
func (c *DeploymentController) updateStatus(ctx context.Context, d *types.Deployment) error {
	return c.store.CreateDeployment(d)
}

// vmIDsByDeployment returns the ids of every VM in a deployment, in order.
func vmIDsByDeployment(vms []*types.VM, deploymentID string) []string {
	var ids []string
	for _, vm := range vms {
		if vm.DeploymentID == deploymentID {
			ids = append(ids, vm.ID)
		}
	}
	return ids
}

// copyCount returns how many replicas a deployment should hold. With no
// persisted replica count on types.Deployment, we approximate from vm_ids.
func copyCount(d *types.Deployment) int {
	if len(d.VMIDs) == 0 {
		return 1
	}
	return len(d.VMIDs)
}
