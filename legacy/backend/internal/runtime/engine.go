// Engine adapts VMManager to the control plane's executor surface: it hands
// each VM a per-project /24 subnet + static IP, MAC, and host TAP device,
// then lets the runtime boot a kernel + rootfs.ext4.
//
// It lives here (not in cmd/porter) so server, agent tooling, and tests share
// one executor. Snapshot intentionally returns the runtime's SnapshotResult,
// not api.SnapshotInfo: internal/api already depends on this package
// transitively (api -> controller -> runtime), so importing api here would be
// an import cycle. Callers needing api.VMRunner wrap Engine with a one-method
// adapter at the edge (see cmd/porter).
package runtime

import (
	"context"
	"fmt"
	"io"

	"porter/internal/event"
	"porter/internal/netmgr"
	"porter/internal/store"
	"porter/internal/types"
)

// Engine adapts the direct-Firecracker VMManager to the executor interface
// the API and workers boot replicas through.
type Engine struct {
	// Rt is the underlying runtime manager. Exported so the server wiring can
	// pass the concrete *VMManager where a narrower surface is required
	// (replica controller, one-shot provisioner, secret-key setup).
	Rt *VMManager

	net   *netmgr.NetManager
	subs  map[string]string // projectID -> allocated /24 subnet
	Use30 bool
}

// NewEngine builds an Engine bound to one Firecracker configuration.
func NewEngine(cfg FCConfig, st *store.Store, hub *event.Hub) *Engine {
	return &Engine{
		Rt:   NewVMManager(cfg, st, hub),
		net:  netmgr.NewNetManager(),
		subs: map[string]string{},
	}
}

// Boot assigns the VM its network identity, then boots kernel + rootfs.
func (e *Engine) Boot(ctx context.Context, vm *types.VM) error {
	if vm == nil {
		return fmt.Errorf("boot: nil vm")
	}
	subnet := e.subs[vm.ProjectID]
	if subnet == "" {
		subnet = e.net.AllocateProjectSubnet()
		e.subs[vm.ProjectID] = subnet
	}
	// /30 cutover flag: same third octet as the /24, guest block+2 + /30 mask.
	if e.Use30 && vm.ReplicaIndex >= 0 && vm.ReplicaIndex <= 63 {
		var third int
		if _, err := fmt.Sscanf(subnet, "10.42.%d.", &third); err == nil {
			if spec30, err := netmgr.AllocateVMNetwork30(third, vm.ReplicaIndex, vm.ID); err == nil {
				return e.bootWithSpec(vm, spec30)
			}
		}
	}
	spec, err := e.net.AllocateVMNetwork(subnet, vm.ReplicaIndex, vm.ID)
	if err != nil {
		return fmt.Errorf("boot: configure direct Firecracker network: %w", err)
	}
	return e.bootWithSpec(vm, spec)
}

// bootWithSpec boots the VM and reflects the boot outcome back on the passed
// VM row: the guest IP from the allocated spec on success (the replica
// controller and API both key off IPAddress to promote booting → running),
// and the error to the caller on failure — never silently swallowed.
func (e *Engine) bootWithSpec(vm *types.VM, spec netmgr.BootSpec) error {
	if err := e.Rt.Boot(vm, spec); err != nil {
		return fmt.Errorf("boot vm %s: %w", vm.ID, err)
	}
	vm.IPAddress = spec.CIDR
	return nil
}

// Stop halts the VM's Firecracker process.
func (e *Engine) Stop(ctx context.Context, vm *types.VM) error {
	if vm == nil {
		return fmt.Errorf("stop: nil vm")
	}
	e.Rt.Stop(vm)
	return nil
}

// Restart stops, then boots the VM again.
func (e *Engine) Restart(ctx context.Context, vm *types.VM) error {
	if err := e.Stop(ctx, vm); err != nil {
		return err
	}
	return e.Boot(ctx, vm)
}

// Pause freezes a running replica in place (replica pause action).
func (e *Engine) Pause(ctx context.Context, vm *types.VM) error {
	if vm == nil {
		return fmt.Errorf("pause: nil vm")
	}
	return e.Rt.Pause(ctx, vm)
}

// Resume unfreezes a paused replica.
func (e *Engine) Resume(ctx context.Context, vm *types.VM) error {
	if vm == nil {
		return fmt.Errorf("resume: nil vm")
	}
	return e.Rt.Resume(ctx, vm)
}

// Reboot restarts a replica in place. Firecracker has no in-place reboot, so
// this is stop-then-boot with the existing network identity — the honest
// equivalent rather than a fabricated action.
func (e *Engine) Reboot(ctx context.Context, vm *types.VM) error {
	return e.Restart(ctx, vm)
}

// Delete stops the guest; DB rows are the caller's job.
func (e *Engine) Delete(ctx context.Context, vm *types.VM) error {
	return e.Stop(ctx, vm)
}

// Snapshot captures the VM state to the configured snapshot directory.
func (e *Engine) Snapshot(ctx context.Context, vm *types.VM) (SnapshotResult, error) {
	return e.Rt.Snapshot(ctx, vm)
}

// Restore resumes a VM from its recorded snapshot paths.
func (e *Engine) Restore(ctx context.Context, vm *types.VM) error {
	return e.Rt.Restore(ctx, vm, vm.SnapshotPath, vm.SnapshotMemPath)
}

// Exec satisfies the Execer surface (api + sshgw): commands run inside the
// guest over the vsock agent channel (explicit error until a guest agent
// connects).
func (e *Engine) Exec(ctx context.Context, vmID string, argv []string, stdin io.Reader, stdout io.Writer) error {
	return e.Rt.Exec(ctx, vmID, argv, stdin, stdout)
}

// NetSpec exposes the runtime allocator so isolated builders and the
// ephemeral provisioner can boot through the engine (satisfies the
// buildkit/controller provisioner interfaces with the real runtime).
func (e *Engine) NetSpec(vm *types.VM) (netmgr.BootSpec, error) {
	return e.Rt.NetSpec(vm)
}

// Close tears down all tracked VMs.
func (e *Engine) Close() { e.Rt.Close() }
