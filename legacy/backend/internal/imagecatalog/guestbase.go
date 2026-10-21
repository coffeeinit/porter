package imagecatalog

import (
	"fmt"
	"strings"
)

// GuestBase describes the host artifacts and userspace contract used to turn
// an OCI filesystem into a bootable Firecracker rootfs.
type GuestBase struct {
	Name        string `json:"name"`
	Reference   string `json:"reference"`
	Family      string `json:"family"` // alpine | debian | ubuntu | custom
	Description string `json:"description"`
	KernelPath  string `json:"kernel_path"`
	RootfsPath  string `json:"rootfs_path"`
	AgentPath   string `json:"agent_path"`
	Managed     bool   `json:"managed"`
}

// ManagedGuestBases returns the supported Porter bases. Artifact paths are
// resolved by the deployment service from configuration; the catalog remains
// usable before host artifacts have been installed.
func ManagedGuestBases() []GuestBase {
	return []GuestBase{
		{Name: "Alpine", Reference: "porter://alpine/latest", Family: "alpine", Description: "Small musl-based guest optimized for fast startup and low storage use", Managed: true},
		{Name: "Debian", Reference: "porter://debian/latest", Family: "debian", Description: "General-purpose glibc guest with broad package compatibility", Managed: true},
		{Name: "Ubuntu", Reference: "porter://ubuntu/latest", Family: "ubuntu", Description: "glibc guest for Ubuntu packages, Python, ML, and enterprise workloads", Managed: true},
	}
}

func ResolveGuestBase(ref string, defaultRef string, custom map[string]GuestBase) (GuestBase, error) {
	if ref == "" {
		ref = defaultRef
	}
	ref = strings.TrimSpace(strings.ToLower(ref))
	if ref == "" {
		ref = "alpine"
	}
	if ref == "alpine" || ref == "porter://alpine" || ref == "porter://alpine/latest" {
		return ManagedGuestBases()[0], nil
	}
	if ref == "debian" || ref == "porter://debian" || ref == "porter://debian/latest" {
		return ManagedGuestBases()[1], nil
	}
	if ref == "ubuntu" || ref == "porter://ubuntu" || ref == "porter://ubuntu/latest" {
		return ManagedGuestBases()[2], nil
	}
	if strings.HasPrefix(ref, "custom://") {
		if base, ok := custom[ref]; ok && base.KernelPath != "" && base.RootfsPath != "" {
			base.Managed = false
			return base, nil
		}
		return GuestBase{}, fmt.Errorf("custom guest base %q is not registered with kernel and rootfs artifacts", ref)
	}
	return GuestBase{}, fmt.Errorf("unsupported guest base %q (want alpine, debian, ubuntu, or custom://...)", ref)
}
