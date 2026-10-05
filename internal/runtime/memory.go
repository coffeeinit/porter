// Memory oversubscription model (PVE-07): always-on balloon with
// free-page-reporting + deflate-on-oom, OR virtio-mem hotplug — never both
// (the two mechanisms fight over the same pages). The runtime renders flags;
// the scheduler accounts max; admission validates.
package runtime

import "fmt"

// MemoryPolicy is the desired memory shape for one VM.
type MemoryPolicy struct {
	SizeMiB       int // boot size
	BalloonMinMiB int // active balloon floor, 0 = balloon reports only
	VirtioMemMiB  int // hotplug pool, 0 = disabled
}

// Validate enforces sizing floors and the never-both rule.
func (m MemoryPolicy) Validate() error {
	if m.SizeMiB < 64 {
		return fmt.Errorf("runtime: vm needs at least 64MiB (got %d)", m.SizeMiB)
	}
	if m.BalloonMinMiB < 0 || m.VirtioMemMiB < 0 {
		return fmt.Errorf("runtime: negative memory bound")
	}
	if m.BalloonMinMiB > 0 && m.VirtioMemMiB > 0 {
		return fmt.Errorf("runtime: balloon and virtio-mem must never combine")
	}
	if m.BalloonMinMiB > m.SizeMiB {
		return fmt.Errorf("runtime: balloon floor above vm size")
	}
	return nil
}

// BalloonArgs renders the always-emitted balloon device flags.
func BalloonArgs() string {
	return "virtio-balloon,free-page-reporting=on,deflate-on-oom=on"
}

// AccountableMiB is what the scheduler books: boot size plus any hotplug
// pool (balloon deflate never exceeds boot size, so it adds nothing).
func (m MemoryPolicy) AccountableMiB() int {
	return m.SizeMiB + m.VirtioMemMiB
}
