// Package kernel owns kernel policy and the kernel build pipeline
// (FCM-20, FCM-21, PVE-20, PVE-21/25): Porter pins one supported kernel
// series (6.18), builds go through a single-active state machine with
// progress, fleet versions are tracked as trains with re-scan, and the
// required Kconfig families plus the builtin-vs-module tradeoff are
// enforced by policy (fail-closed verify).
package kernel

import (
	"fmt"
	"strings"
	"sync"
)

// PinnedSeries is the only kernel series Porter supports.
const PinnedSeries = "6.18"

// Build states in lifecycle order.
const (
	BuildPending     = "pending"
	BuildFetching    = "fetching"
	BuildConfiguring = "configuring"
	BuildCompiling   = "compiling"
	BuildCompleted   = "completed"
	BuildFailed      = "failed"
)

// Build tracks one kernel build. Only one build per version may be active;
// the manager enforces that, this type enforces legal transitions.
type Build struct {
	Version  string
	State    string
	Progress int // 0-100
	Error    string
}

// Advance moves the build forward; terminal states are sticky.
func (b *Build) Advance(next string, progress int) error {
	ok := map[string][]string{
		"":               {BuildPending},
		BuildPending:     {BuildFetching, BuildFailed},
		BuildFetching:    {BuildConfiguring, BuildFailed},
		BuildConfiguring: {BuildCompiling, BuildFailed},
		BuildCompiling:   {BuildCompleted, BuildFailed},
	}[b.State]
	for _, s := range ok {
		if s == next {
			b.State = next
			b.Progress = progress
			return nil
		}
	}
	return fmt.Errorf("kernel: illegal transition %q -> %q", b.State, next)
}

// Manager guards single-active builds per version.
type Manager struct {
	mu     sync.Mutex
	active map[string]*Build
}

// NewManager builds an empty manager.
func NewManager() *Manager { return &Manager{active: map[string]*Build{}} }

// Start begins a build unless one is already active for the version.
func (m *Manager) Start(version string) (*Build, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, dup := m.active[version]; dup && b.State != BuildCompleted && b.State != BuildFailed {
		return nil, fmt.Errorf("kernel: build for %q already active (%s)", version, b.State)
	}
	b := &Build{Version: version}
	if err := b.Advance(BuildPending, 0); err != nil {
		return nil, err
	}
	m.active[version] = b
	return b, nil
}

// RequiredFamilies lists Kconfig families the Porter kernel must carry
// (PVE-21 inventory, condensed). VerifyConfig fails closed when absent.
var RequiredFamilies = []string{
	"CONFIG_VIRTIO_NET",
	"CONFIG_VIRTIO_BLK",
	"CONFIG_VIRTIO_CONSOLE",
	"CONFIG_VIRTIO_BALLOON",
	"CONFIG_VIRTIO_MMIO_CMDLINE",
	"CONFIG_VIRTIO_VSOCK",
	"CONFIG_TUN",
	"CONFIG_BRIDGE_NETFILTER",
	"CONFIG_NF_TABLES",
	"CONFIG_CGROUPS",
	"CONFIG_MEMCG",
	"CONFIG_BPF_SYSCALL",
	"CONFIG_OVERLAY_FS",
	"CONFIG_EXT4_FS",
	"CONFIG_9P_FS",
}

// VerifyConfig checks a .config body for the required families (y or m).
// Missing families are returned so CI can fail with the exact gap.
func VerifyConfig(body string) []string {
	present := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		for _, fam := range RequiredFamilies {
			if strings.HasPrefix(line, fam+"=y") || strings.HasPrefix(line, fam+"=m") {
				present[fam] = true
			}
		}
	}
	var missing []string
	for _, fam := range RequiredFamilies {
		if !present[fam] {
			missing = append(missing, fam)
		}
	}
	return missing
}

// ModuleMode documents the builtin-vs-module tradeoff (PVE-25): built-in
// (=y) boots without initrd and is smaller operationally; modules (=m)
// need an initramfs with ordered insmod. Firecracker rootfs images use
// built-in virtio; PCIe/QEMU images may use modules + initrd.
type ModuleMode string

const (
	ModeBuiltin ModuleMode = "builtin"
	ModeModules ModuleMode = "modules"
)

// Train tracks one fleet kernel version (FCM-21): known-good versions,
// where to fetch, and whether virtio support was re-scanned after download.
type Train struct {
	Version       string
	SourceURL     string
	VirtioScanned bool
	Compatible    bool
}

// Validate gates train entries: pinned series only, rescan before trust.
func (t Train) Validate() error {
	if !strings.HasPrefix(t.Version, PinnedSeries) {
		return fmt.Errorf("kernel: version %q outside pinned series %s", t.Version, PinnedSeries)
	}
	if t.SourceURL == "" {
		return fmt.Errorf("kernel: train entry needs a source URL")
	}
	if t.Compatible && !t.VirtioScanned {
		return fmt.Errorf("kernel: compatibility requires a virtio rescan")
	}
	return nil
}
