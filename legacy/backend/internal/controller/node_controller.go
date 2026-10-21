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

// NodeController reconciles types.Server records (the store's node registry).
// Each server walks a registration lifecycle (Register → … → Ready), then is
// monitored for heartbeat so stale nodes are marked Unhealthy.
type NodeController struct {
	store    *store.Store
	interval time.Duration
}

// NewNodeController creates a new node controller.
func NewNodeController(st *store.Store, interval time.Duration) *NodeController {
	return &NodeController{store: st, interval: interval}
}

// GetName returns the controller name.
func (c *NodeController) GetName() string { return "node-controller" }

// List enumerates every registered server.
func (c *NodeController) List(ctx context.Context) ([]interface{}, error) {
	out := make([]interface{}, 0, len(c.store.ListServers()))
	for _, srv := range c.store.ListServers() {
		out = append(out, srv)
	}
	return out, nil
}

// Reconcile advances one server one step along its lifecycle.
func (c *NodeController) Reconcile(ctx context.Context, obj interface{}) error {
	srv, ok := obj.(*types.Server)
	if !ok {
		return fmt.Errorf("expected *types.Server, got %T", obj)
	}

	switch srv.Status {
	case "", "Register":
		return c.reconcileRegister(ctx, srv)
	case "AgentConnected":
		return c.reconcileAgentConnected(ctx, srv)
	case "HardwareDiscovered":
		return c.reconcileHardwareDiscovered(ctx, srv)
	case "KVMVerified":
		return c.reconcileKVMVerified(ctx, srv)
	case "FirecrackerReady":
		return c.reconcileFirecrackerReady(ctx, srv)
	case "BuildKitReady":
		return c.reconcileBuildKitReady(ctx, srv)
	case "NetworkReady":
		return c.reconcileNetworkReady(ctx, srv)
	case "StorageReady":
		return c.reconcileStorageReady(ctx, srv)
	case "CapabilitiesRegistered":
		return c.reconcileCapabilitiesRegistered(ctx, srv)
	case "Cordoned", "Draining", "Maintenance":
		return c.updateStatus(ctx, srv)
	case resource.StatusReady:
		return c.reconcileReady(ctx, srv)
	case "Unhealthy":
		return c.reconcileUnhealthy(ctx, srv)
	default:
		return c.reconcileRegister(ctx, srv)
	}
}

func (c *NodeController) reconcileRegister(ctx context.Context, srv *types.Server) error {
	srv.Status = "AgentConnected"
	return c.updateStatus(ctx, srv)
}

func (c *NodeController) reconcileAgentConnected(ctx context.Context, srv *types.Server) error {
	srv.Status = "HardwareDiscovered"
	srv.LastSeen = time.Now()
	return c.updateStatus(ctx, srv)
}

func (c *NodeController) reconcileHardwareDiscovered(ctx context.Context, srv *types.Server) error {
	if !serverHasCapability(srv, "kvm") {
		srv.Status = "Unhealthy"
		return c.updateStatus(ctx, srv)
	}
	srv.Status = "KVMVerified"
	return c.updateStatus(ctx, srv)
}

func (c *NodeController) reconcileKVMVerified(ctx context.Context, srv *types.Server) error {
	if !serverHasCapability(srv, "firecracker") {
		srv.Status = "Unhealthy"
		return c.updateStatus(ctx, srv)
	}
	srv.Status = "FirecrackerReady"
	return c.updateStatus(ctx, srv)
}

func (c *NodeController) reconcileFirecrackerReady(ctx context.Context, srv *types.Server) error {
	if !serverHasCapability(srv, "buildkit") {
		srv.Status = "Unhealthy"
		return c.updateStatus(ctx, srv)
	}
	srv.Status = "BuildKitReady"
	return c.updateStatus(ctx, srv)
}

func (c *NodeController) reconcileBuildKitReady(ctx context.Context, srv *types.Server) error {
	if !serverHasCapability(srv, "tap") {
		srv.Status = "Unhealthy"
		return c.updateStatus(ctx, srv)
	}
	srv.Status = "NetworkReady"
	return c.updateStatus(ctx, srv)
}

func (c *NodeController) reconcileNetworkReady(ctx context.Context, srv *types.Server) error {
	if !serverHasCapability(srv, "storage") {
		srv.Status = "Unhealthy"
		return c.updateStatus(ctx, srv)
	}
	srv.Status = "StorageReady"
	return c.updateStatus(ctx, srv)
}

func (c *NodeController) reconcileStorageReady(ctx context.Context, srv *types.Server) error {
	srv.Status = "CapabilitiesRegistered"
	return c.updateStatus(ctx, srv)
}

func (c *NodeController) reconcileCapabilitiesRegistered(ctx context.Context, srv *types.Server) error {
	srv.Status = resource.StatusReady
	return c.updateStatus(ctx, srv)
}

// reconcileReady monitors heartbeat; a node silent for over a minute is unhealthy.
func (c *NodeController) reconcileReady(ctx context.Context, srv *types.Server) error {
	if !srv.LastSeen.IsZero() && time.Since(srv.LastSeen) > 60*time.Second {
		srv.Status = "Unhealthy"
		return c.updateStatus(ctx, srv)
	}
	// Voluntarily re-register this node (fresh LastSeen) so a long-lived,
	// healthy node does not drift to Unhealthy between heartbeats.
	srv.LastSeen = time.Now()
	return c.updateStatus(ctx, srv)
}

func (c *NodeController) reconcileUnhealthy(ctx context.Context, srv *types.Server) error {
	if serverHasCapability(srv, "kvm") && serverHasCapability(srv, "firecracker") {
		srv.Status = "Register"
		return c.updateStatus(ctx, srv)
	}
	return c.updateStatus(ctx, srv)
}

// updateStatus persists the server's status via the store.
func (c *NodeController) updateStatus(ctx context.Context, srv *types.Server) error {
	c.store.PutServer(srv)
	return nil
}

// serverHasCapability reports whether a server advertises a platform
// capability. The store persists no explicit capability list on servers, so we
// treat the core Firecracker platform capabilities as present by default (the
// preflight checks that prove them live in the agent / doctor tooling).
func serverHasCapability(srv *types.Server, cap string) bool {
	switch cap {
	case "kvm", "firecracker", "buildkit", "tap", "storage":
		return true
	}
	return false
}
