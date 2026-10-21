// External-format converters into bootable artifacts (FCM-15): registry,
// compose-service and qemu-image funnels behind one Converter contract.
// BuildKit builds; converters transform; neither ever runs workloads.
package build

import (
	"fmt"
	"strings"
)

// Converter sources.
const (
	SourceRegistry = "registry"
	SourceCompose  = "compose"
	SourceQEMU     = "qemu"
)

// ConvertSpec is the desired-state input for one conversion.
type ConvertSpec struct {
	Source     string // registry | compose | qemu
	Ref        string // image ref, compose service, or qemu image path
	SizeGiB    int    // 0 = auto
	InstallSSH bool
}

// Validate enforces converter hygiene: known source, pinned digest for
// registry refs (never floating tags per FCM-15), no daemon dependence on
// the default path.
func (s ConvertSpec) Validate() error {
	switch s.Source {
	case SourceRegistry:
		if err := RequireDigest(s.Ref); err != nil {
			return err
		}
	case SourceCompose, SourceQEMU:
		if strings.TrimSpace(s.Ref) == "" {
			return fmt.Errorf("build: %s converter needs a ref", s.Source)
		}
	default:
		return fmt.Errorf("build: unknown converter source %q", s.Source)
	}
	if s.SizeGiB < 0 {
		return fmt.Errorf("build: negative size")
	}
	return nil
}

// RequireDigest pins registry refs to content digests (no :latest).
func RequireDigest(ref string) error {
	if !strings.Contains(ref, "@sha256:") {
		return fmt.Errorf("build: registry ref %q must be digest-pinned (@sha256:)", ref)
	}
	parts := strings.SplitN(ref, "@sha256:", 2)
	if len(parts[1]) != 64 {
		return fmt.Errorf("build: registry ref %q has a bad digest", ref)
	}
	return nil
}

// SizeGiBFor grows the unpacked byte count by 35%% with a 2 GiB floor
// (FCM RegistryToFC sizing rule).
func SizeGiBFor(unpackedBytes int64) int64 {
	const gib = int64(1024 * 1024 * 1024)
	sized := unpackedBytes + unpackedBytes*35/100 + gib - 1
	gibn := sized / gib
	if gibn < 2 {
		gibn = 2
	}
	return gibn
}
