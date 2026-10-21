// Package controller implements the Porter reconciliation controllers.
//
// Controllers continuously converge actual state toward desired state. Each
// controller watches a resource type it can enumerate from the store (the
// single durable truth), reconciles every object it listed, and persists any
// state change back through the same store. A controller never assumes an API
// ack means success — it re-derives reality from the store each cycle and its
// actions are idempotent (per the SRS §18–19 contract in the design doc).
package controller

import (
	"context"
	"log"
	"sync"
	"time"
)

// Reconciler is the interface all controllers implement. List returns the
// current objects of the controller's type from the store; Reconcile converges
// one of them toward desired state.
type Reconciler interface {
	// List returns every object of this controller's type from the store.
	List(ctx context.Context) ([]interface{}, error)

	// Reconcile performs one reconciliation cycle for a single object.
	Reconcile(ctx context.Context, obj interface{}) error

	// GetName returns the controller name.
	GetName() string
}

// Manager drives every registered controller on a ticker.
type Manager struct {
	mu       sync.RWMutex
	ctrls    map[string]Reconciler
	stopCh   chan struct{}
	wg       sync.WaitGroup
	interval time.Duration
}

// NewManager creates a new controller manager.
func NewManager(interval time.Duration) *Manager {
	return &Manager{
		ctrls:    make(map[string]Reconciler),
		stopCh:   make(chan struct{}),
		interval: interval,
	}
}

// Register adds a reconciler to the manager.
func (m *Manager) Register(r Reconciler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ctrls[r.GetName()] = r
}

// Start launches one reconciliation goroutine per controller.
func (m *Manager) Start(ctx context.Context) {
	m.mu.RLock()
	ctrls := make([]Reconciler, 0, len(m.ctrls))
	for _, ctrl := range m.ctrls {
		ctrls = append(ctrls, ctrl)
	}
	m.mu.RUnlock()

	for _, ctrl := range ctrls {
		m.wg.Add(1)
		go m.runController(ctx, ctrl)
	}
}

// Stop halts all controller goroutines.
func (m *Manager) Stop() {
	close(m.stopCh)
	m.wg.Wait()
}

// runController runs a single controller loop.
func (m *Manager) runController(ctx context.Context, ctrl Reconciler) {
	defer m.wg.Done()

	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	// Reconcile once immediately, then on each tick.
	m.reconcileAll(ctx, ctrl)
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.reconcileAll(ctx, ctrl)
		}
	}
}

// reconcileAll enumerates every object of the controller's type and reconciles
// each. Failures are logged and never stop the cycle for other objects.
func (m *Manager) reconcileAll(ctx context.Context, ctrl Reconciler) {
	objs, err := ctrl.List(ctx)
	if err != nil {
		log.Printf("controller %s: list: %v", ctrl.GetName(), err)
		return
	}
	for _, obj := range objs {
		if err := ctrl.Reconcile(ctx, obj); err != nil {
			log.Printf("controller %s: reconcile: %v", ctrl.GetName(), err)
		}
	}
}
