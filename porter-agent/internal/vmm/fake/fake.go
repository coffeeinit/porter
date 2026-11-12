// Package fake provides an in-memory vmm.Driver for tests and simulations.
//
// It records every call so a test can assert the agent core asked for the
// right operations without starting a real VMM.
package fake

import (
	"context"
	"fmt"
	"sync"

	"porter/agent/internal/vmm"
)

// Call records one driver invocation.
type Call struct {
	Op          string
	Socket      string
	Spec        vmm.MachineSpec
	Snap        vmm.SnapshotSpec
	HostDevName string
}

// Driver is a vmm.Driver that records calls and can inject failures.
type Driver struct {
	mu     sync.Mutex
	calls  []Call
	failOn map[string]error
}

// New returns an empty fake driver.
func New() *Driver { return &Driver{failOn: map[string]error{}} }

// FailOn makes the named operation return err.
func (d *Driver) FailOn(op string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failOn[op] = err
}

// Calls returns a copy of the recorded calls.
func (d *Driver) Calls() []Call {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Call, len(d.calls))
	copy(out, d.calls)
	return out
}

func (d *Driver) record(c Call) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, c)
	if err, ok := d.failOn[c.Op]; ok {
		return err
	}
	return nil
}

// Name implements vmm.Driver.
func (*Driver) Name() string { return "fake" }

// Binary implements vmm.Driver.
func (*Driver) Binary() string { return "fake-vmm" }

// SocketName implements vmm.Driver.
func (*Driver) SocketName() string { return "fake.sock" }

// LogName implements vmm.Driver.
func (*Driver) LogName() string { return "fake.log" }

// Boot implements vmm.Driver.
func (d *Driver) Boot(_ context.Context, socket string, spec vmm.MachineSpec) error {
	return d.record(Call{Op: "Boot", Socket: socket, Spec: spec})
}

// Pause implements vmm.Driver.
func (d *Driver) Pause(_ context.Context, socket string) error {
	return d.record(Call{Op: "Pause", Socket: socket})
}

// Resume implements vmm.Driver.
func (d *Driver) Resume(_ context.Context, socket string) error {
	return d.record(Call{Op: "Resume", Socket: socket})
}

// Snapshot implements vmm.Driver.
func (d *Driver) Snapshot(_ context.Context, socket string, snap vmm.SnapshotSpec) error {
	return d.record(Call{Op: "Snapshot", Socket: socket, Snap: snap})
}

// Restore implements vmm.Driver.
func (d *Driver) Restore(_ context.Context, socket string, snap vmm.SnapshotSpec, hostDevName string) error {
	return d.record(Call{Op: "Restore", Socket: socket, Snap: snap, HostDevName: hostDevName})
}

// String renders the recorded operations, for test failure messages.
func (d *Driver) String() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	ops := make([]string, len(d.calls))
	for i, c := range d.calls {
		ops[i] = c.Op
	}
	return fmt.Sprint(ops)
}
