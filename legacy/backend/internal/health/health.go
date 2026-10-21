// Package health runs per-VM healthcheck goroutines, drains unhealthy VMs
// from the pool, and triggers replacement through vmmanager (it never boots a
// VM itself — it calls a replace hook).
package health

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"porter/internal/types"
)

// HealthSpec describes one service's healthcheck.
type HealthSpec struct {
	Type        string `json:"type"` // http | tcp (http when Path is set)
	Path        string `json:"path"`
	Port        int    `json:"port"`
	IntervalSec int    `json:"interval_sec"`
}

// Store is the narrow persistence surface the checker needs.
type Store interface {
	GetVM(id string) (*types.VM, bool)
	PutVM(vm *types.VM)
}

// Hub broadcasts health transitions to the dashboard.
type Hub interface {
	Broadcast(event string, data any)
}

// Checker watches VMs and reports/replaces them.
type Checker struct {
	store     Store
	hub       Hub
	onReplace func(ctx context.Context, vmID string) // provided by the caller (vmmanager-backed)
	timeout   time.Duration
}

// New builds a checker. onReplace is called when an unhealthy VM should be
// replaced; the caller wires it to the runtime's Restart/Boot.
func New(store Store, hub Hub, onReplace func(ctx context.Context, vmID string)) *Checker {
	return &Checker{store: store, hub: hub, onReplace: onReplace, timeout: 3 * time.Second}
}

// Watch probes one VM on the given spec until ctx is cancelled.
func (c *Checker) Watch(ctx context.Context, vmID string, spec HealthSpec) {
	if spec.IntervalSec <= 0 {
		spec.IntervalSec = 10
	}
	ticker := time.NewTicker(time.Duration(spec.IntervalSec) * time.Second)
	defer ticker.Stop()

	// Probe immediately, then on the ticker.
	c.probeOnce(ctx, vmID, spec)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.probeOnce(ctx, vmID, spec)
		}
	}
}

func (c *Checker) probeOnce(ctx context.Context, vmID string, spec HealthSpec) {
	vm, ok := c.store.GetVM(vmID)
	if !ok || vm == nil || vm.State != types.StateRunning {
		return
	}
	ok = c.Probe(ctx, vm, spec)
	if ok {
		c.setHealth(vm, types.HealthHealthy, "")
		return
	}
	c.setHealth(vm, types.HealthUnhealthy, "healthcheck failed")
	if c.onReplace != nil {
		go c.onReplace(ctx, vmID)
	}
}

// Probe runs the actual HTTP or TCP check.
func (c *Checker) Probe(ctx context.Context, vm *types.VM, spec HealthSpec) bool {
	host := vm.IPAddress
	if host == "" {
		return false
	}
	port := spec.Port
	if port == 0 && len(vm.Ports) > 0 {
		port = vm.Ports[0].ContainerPort
	}
	if port == 0 {
		port = 80
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	if spec.Type == "http" || spec.Path != "" {
		path := spec.Path
		if path == "" {
			path = "/"
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://%s:%d%s", host, port, path), nil)
		if err != nil {
			return false
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode >= 200 && resp.StatusCode < 400
	}

	dialer := &net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", fmt.Sprintf("%s:%d", host, port))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func (c *Checker) setHealth(vm *types.VM, status, detail string) {
	if vm.HealthStatus == status {
		return
	}
	vm.HealthStatus = status
	c.store.PutVM(vm)
	if c.hub != nil {
		c.hub.Broadcast("replica.health", map[string]any{
			"vm_id": vm.ID, "service": vm.ServiceName, "health_status": status,
		})
	}
	if detail != "" {
		log.Printf("health: vm %s (%s) -> %s: %s", vm.ID, vm.ServiceName, status, detail)
	}
}
