// Host-directory share model (PVE-06): workspace/debug mounts via virtiofs
// (daemon, better large I/O) or 9p (built-in, simpler). Shares attach before
// boot only — no hot-add. The agent consumes rendezvous files; this package
// validates specs and derives the rendezvous paths.
package storage

import (
	"fmt"
	"regexp"
	"strings"
)

// Share drivers.
const (
	DriverVirtioFS = "virtiofs"
	Driver9P       = "9p"
)

var tagRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// Share is one host → guest directory mount.
type Share struct {
	Tag      string // guest mount tag, e.g. "shared"
	HostPath string // absolute host directory
	Driver   string // virtiofs | 9p
}

// Validate gates share specs before the agent sees them.
func (s Share) Validate() error {
	if !tagRe.MatchString(s.Tag) {
		return fmt.Errorf("storage: bad share tag %q", s.Tag)
	}
	if !strings.HasPrefix(s.HostPath, "/") {
		return fmt.Errorf("storage: share path must be absolute")
	}
	switch s.Driver {
	case DriverVirtioFS, Driver9P:
	default:
		return fmt.Errorf("storage: unknown share driver %q", s.Driver)
	}
	return nil
}

// RendezvousPath returns the agent-consumed rendezvous file for a share:
// virtiofs socket or 9p conf under the run dir (PVE-06 convention).
func RendezvousPath(runDir, vmID string, s Share) (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	safe := strings.ReplaceAll(vmID, "/", "-")
	if s.Driver == DriverVirtioFS {
		return fmt.Sprintf("%s/%s-virtiofs.sock", strings.TrimSuffix(runDir, "/"), safe), nil
	}
	return fmt.Sprintf("%s/%s-9p.conf", strings.TrimSuffix(runDir, "/"), safe), nil
}

// GuestMount renders the in-guest mount command for a share.
func GuestMount(s Share, mountPoint string) (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	if s.Driver == DriverVirtioFS {
		return fmt.Sprintf("mount -t virtiofs %s %s", s.Tag, mountPoint), nil
	}
	return fmt.Sprintf("mount -t 9p %s %s -o trans=virtio,version=9p2000.L", s.Tag, mountPoint), nil
}
