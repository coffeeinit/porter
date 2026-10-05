// Guest-preparation pipeline model (FCM-05, PVE-01/02/04): the explicit
// OCI/image → bootable guest transform. Execution stays in buildkit and the
// agent; this package owns stage order, distro detection, profile package
// sets, and the static-net renderer the agent writes before boot.
package build

import (
	"fmt"
	"strings"
)

// Prep stages run in order; Verify gates scheduling (PVE admission).
const (
	StageSeedEntropy  = "seed_entropy"
	StagePatchDNS     = "patch_dns"
	StageFixInit      = "fix_init"
	StageFixSecuretty = "fix_securetty"
	StageVerify       = "verify"
)

// Stages is the canonical pipeline order.
var Stages = []string{StageSeedEntropy, StagePatchDNS, StageFixInit, StageFixSecuretty, StageVerify}

// Distro package managers detected from /etc/os-release (PVE-02).
const (
	DistroApt = "apt" // debian, ubuntu
	DistroApk = "apk" // alpine
	DistroDnf = "dnf" // fedora, rocky, alma, amazon, oracle, ubi, photon
)

// DetectDistro maps os-release ID[/ID_LIKE] to a package manager. Unknown
// distros return "" and the caller must refuse admission, never guess.
func DetectDistro(osRelease string) string {
	id, like := "", ""
	for _, line := range strings.Split(osRelease, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := cutPrefix(line, "ID="); ok {
			id = unquote(strings.ToLower(v))
		}
		if v, ok := cutPrefix(line, "ID_LIKE="); ok {
			like = unquote(strings.ToLower(v))
		}
	}
	hit := id + " " + like
	switch {
	case containsAny(hit, "debian", "ubuntu"):
		return DistroApt
	case containsAny(hit, "alpine"):
		return DistroApk
	case containsAny(hit, "fedora", "rhel", "centos", "rocky", "alma", "amzn", "oracle", "suse", "photon", "azure", "mariner"):
		return DistroDnf
	default:
		return ""
	}
}

func cutPrefix(s, pre string) (string, bool) {
	if strings.HasPrefix(s, pre) {
		return s[len(pre):], true
	}
	return "", false
}

func unquote(s string) string { return strings.Trim(s, "\"'") }

func containsAny(hay string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(hay, n) {
			return true
		}
	}
	return false
}

// Guest profiles (PVE-02): minimal = shell only, standard = SSH + agent +
// network (default), full = + container runtime (needs BPF overlay).
const (
	ProfileMinimal  = "minimal"
	ProfileStandard = "standard"
	ProfileFull     = "full"
)

// ProfilePackages returns the chroot package set for a distro + profile.
// Wrong-distro installs are the caller's bug: unknown combos fail closed.
func ProfilePackages(distro, profile string) ([]string, error) {
	switch distro {
	case DistroApt:
		switch profile {
		case ProfileMinimal:
			return []string{"busybox-static"}, nil
		case ProfileStandard:
			return []string{"openssh-server", "qemu-guest-agent", "systemd", "dbus", "iproute2", "cloud-init"}, nil
		case ProfileFull:
			p, _ := ProfilePackages(distro, ProfileStandard)
			return append(p, "docker-ce"), nil
		}
	case DistroApk:
		switch profile {
		case ProfileMinimal:
			return []string{"busybox"}, nil
		case ProfileStandard:
			return []string{"openssh", "qemu-guest-agent", "openrc", "dhclient", "cloud-init"}, nil
		case ProfileFull:
			p, _ := ProfilePackages(distro, ProfileStandard)
			return append(p, "docker"), nil
		}
	case DistroDnf:
		switch profile {
		case ProfileMinimal:
			return []string{"busybox"}, nil
		case ProfileStandard:
			// Full util-linux (not -core) and NetworkManager on EL (PVE-02).
			return []string{"openssh-server", "qemu-guest-agent", "systemd", "NetworkManager", "util-linux", "cloud-init"}, nil
		case ProfileFull:
			p, _ := ProfilePackages(distro, ProfileStandard)
			return append(p, "docker-ce"), nil
		}
	}
	return nil, fmt.Errorf("build: no package set for distro %q profile %q", distro, profile)
}

// GuestPrep tracks per-stage completion for status.guestPrep.
type GuestPrep struct {
	Done map[string]bool
}

// NewGuestPrep starts an empty prep record.
func NewGuestPrep() *GuestPrep { return &GuestPrep{Done: map[string]bool{}} }

// Complete marks one stage; unknown stages are rejected.
func (g *GuestPrep) Complete(stage string) error {
	for _, s := range Stages {
		if s == stage {
			g.Done[stage] = true
			return nil
		}
	}
	return fmt.Errorf("build: unknown prep stage %q", stage)
}

// Ready reports whether all stages (including verify) completed.
func (g *GuestPrep) Ready() bool {
	for _, s := range Stages {
		if !g.Done[s] {
			return false
		}
	}
	return true
}
