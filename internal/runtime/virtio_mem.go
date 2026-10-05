// virtio-mem live resize (FCM-18): grow/shrink guest memory without reboot
// via the Firecracker /hotplug/memory PATCH path. virtio-mem and balloon
// are mutually exclusive — a VM using active ballooning must not take a
// virtio-mem resize (the two mechanisms fight over the same memory).
package runtime

import "fmt"

// VirtioMemPool describes the hotplug pool: base MiB plus max ceiling.
type VirtioMemPool struct {
	BaseMiB int
	MaxMiB  int
}

// VirtioMemResize is one live resize intent (requested-size in MiB).
type VirtioMemResize struct {
	RequestedMiB  int
	BalloonActive bool // reject when true: never mix balloon + virtio-mem
	Pool          VirtioMemPool
}

// Validate gates resize intents before any FC API call.
func (r VirtioMemResize) Validate() error {
	if r.BalloonActive {
		return fmt.Errorf("runtime: virtio-mem resize refused while balloon is active")
	}
	if r.RequestedMiB < r.Pool.BaseMiB {
		return fmt.Errorf("runtime: requested %dMiB below base %dMiB", r.RequestedMiB, r.Pool.BaseMiB)
	}
	if r.RequestedMiB > r.Pool.MaxMiB {
		return fmt.Errorf("runtime: requested %dMiB above max %dMiB", r.RequestedMiB, r.Pool.MaxMiB)
	}
	return nil
}

// HotplugPath is the Firecracker API path for memory hotplug updates.
const HotplugPath = "/hotplug/memory"
