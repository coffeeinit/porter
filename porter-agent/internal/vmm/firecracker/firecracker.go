// Package firecracker implements the vmm.Driver boundary for Firecracker.
//
// Every Firecracker-specific detail — the executable name, the control socket
// name, the REST boot sequence, the pause/resume calls, and the snapshot file
// format — lives here. Nothing outside this package may assume Firecracker.
package firecracker

import (
	"context"
	"net/http"

	"porter/agent/internal/vmm"
)

// Driver drives Firecracker over its unix-socket REST API.
type Driver struct {
	// BinaryPath overrides the firecracker executable. Empty means "firecracker"
	// resolved from PATH.
	BinaryPath string
}

// Name implements vmm.Driver.
func (Driver) Name() string { return "firecracker" }

// Binary implements vmm.Driver.
func (d Driver) Binary() string {
	if d.BinaryPath != "" {
		return d.BinaryPath
	}
	return "firecracker"
}

// SocketName implements vmm.Driver.
func (Driver) SocketName() string { return "firecracker.sock" }

// LogName implements vmm.Driver.
func (Driver) LogName() string { return "firecracker.log" }

// Boot implements vmm.Driver. It configures a freshly started Firecracker
// process and starts the guest.
func (d Driver) Boot(ctx context.Context, socket string, spec vmm.MachineSpec) error {
	client := vmm.Client(socket)
	bootArgs := "console=ttyS0 reboot=k panic=1 pci=off root=/dev/vda ro"
	if spec.InitPath != "" {
		bootArgs += " init=" + spec.InitPath
	}
	if spec.GuestIP != "" && spec.GatewayIP != "" {
		bootArgs += " ip=" + spec.GuestIP + "::" + spec.GatewayIP + ":" + vmm.Netmask(spec.BridgeCIDR) + "::eth0:off"
	}
	if err := vmm.Put(ctx, client, "/boot-source", map[string]string{"kernel_image_path": spec.KernelPath, "boot_args": bootArgs}); err != nil {
		return err
	}
	if err := vmm.Put(ctx, client, "/drives/rootfs", map[string]interface{}{"drive_id": "rootfs", "path_on_host": spec.RootfsPath, "is_root_device": true, "is_read_only": false}); err != nil {
		return err
	}
	if spec.VolumePath != "" {
		if err := vmm.Put(ctx, client, "/drives/vdb", map[string]interface{}{"drive_id": "vdb", "path_on_host": spec.VolumePath, "is_root_device": false, "is_read_only": false}); err != nil {
			return err
		}
	}
	if spec.HostDevName != "" {
		if err := vmm.Put(ctx, client, "/network-interfaces/eth0", map[string]string{"iface_id": "eth0", "guest_mac": spec.GuestMAC, "host_dev_name": spec.HostDevName}); err != nil {
			return err
		}
	}
	if err := vmm.Put(ctx, client, "/machine-config", map[string]int{"vcpu_count": spec.VCPUs, "mem_size_mib": spec.MemoryMB}); err != nil {
		return err
	}
	return vmm.Put(ctx, client, "/actions", map[string]string{"action_type": "InstanceStart"})
}

// Pause implements vmm.Driver.
func (Driver) Pause(ctx context.Context, socket string) error {
	return vmm.Do(ctx, vmm.Client(socket), http.MethodPatch, "/vm", map[string]string{"state": "Paused"})
}

// Resume implements vmm.Driver.
func (Driver) Resume(ctx context.Context, socket string) error {
	return vmm.Do(ctx, vmm.Client(socket), http.MethodPatch, "/vm", map[string]string{"state": "Resumed"})
}

// Snapshot implements vmm.Driver.
func (Driver) Snapshot(ctx context.Context, socket string, snap vmm.SnapshotSpec) error {
	return vmm.Put(ctx, vmm.Client(socket), "/snapshot/create", map[string]interface{}{
		"snapshot_type":       "Full",
		"snapshot_path":       snap.StatePath,
		"mem_file_path":       snap.MemPath,
		"sync_snapshot_files": true,
	})
}

// Restore implements vmm.Driver.
func (Driver) Restore(ctx context.Context, socket string, snap vmm.SnapshotSpec, hostDevName string) error {
	client := vmm.Client(socket)
	loadRequest := map[string]interface{}{
		"snapshot_path": snap.StatePath,
		"mem_backend":   map[string]string{"backend_path": snap.MemPath, "backend_type": "File"},
		"resume_vm":     false,
	}
	if hostDevName != "" {
		loadRequest["network_overrides"] = []map[string]string{{"iface_id": "eth0", "host_dev_name": hostDevName}}
	}
	if err := vmm.Put(ctx, client, "/snapshot/load", loadRequest); err != nil {
		return err
	}
	return vmm.Do(ctx, client, http.MethodPatch, "/vm", map[string]string{"state": "Resumed"})
}
