// Package ptyd models the standalone PTY terminal daemon (OCM-17): a tiny
// guest-side server on :7681 that owns terminal sessions, fronted by the
// control plane through the authproxy scope=terminal path. It is a separate
// binary concern from the agent so terminals survive agent restarts and can
// be reasoned about, audited, and rate-limited independently. No SSH.
package ptyd

import (
	"fmt"
	"sync"
	"time"
)

// Port is the guest-local terminal port the authproxy forwards to.
const Port = 7681

// Scope is the authproxy scope required to reach the terminal.
const Scope = "terminal"

// Session tracks one terminal session inside the guest.
type Session struct {
	ID        string
	VMID      string
	Principal string // who opened it (for audit)
	OpenedAt  time.Time
	Cols      int
	Rows      int
}

// Validate gates session requests before any PTY is allocated.
func (s Session) Validate() error {
	if s.ID == "" || s.VMID == "" {
		return fmt.Errorf("ptyd: session needs id and vm id")
	}
	if s.Principal == "" {
		return fmt.Errorf("ptyd: session needs an audited principal")
	}
	if s.Cols <= 0 || s.Rows <= 0 {
		return fmt.Errorf("ptyd: session needs positive dimensions")
	}
	return nil
}

// Registry tracks live terminal sessions per VM with a per-VM cap so one
// tenant cannot exhaust guest PTYs.
type Registry struct {
	mu       sync.Mutex
	sessions map[string]Session // by session ID
	perVMMax int
}

// NewRegistry builds a registry allowing up to perVMMax sessions per VM.
func NewRegistry(perVMMax int) *Registry {
	if perVMMax <= 0 {
		perVMMax = 4
	}
	return &Registry{sessions: map[string]Session{}, perVMMax: perVMMax}
}

// Open validates and records a session, enforcing the per-VM cap.
func (r *Registry) Open(s Session) error {
	if err := s.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.sessions[s.ID]; dup {
		return fmt.Errorf("ptyd: duplicate session %q", s.ID)
	}
	n := 0
	for _, cur := range r.sessions {
		if cur.VMID == s.VMID {
			n++
		}
	}
	if n >= r.perVMMax {
		return fmt.Errorf("ptyd: vm %q at session cap %d", s.VMID, r.perVMMax)
	}
	r.sessions[s.ID] = s
	return nil
}

// Close removes a session; closing an unknown ID is a no-op (idempotent).
func (r *Registry) Close(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, id)
}

// Count returns live sessions for vmID.
func (r *Registry) Count(vmID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, s := range r.sessions {
		if s.VMID == vmID {
			n++
		}
	}
	return n
}
