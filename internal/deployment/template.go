// Immutable template catalog with clone policy (PVE-16 remainder):
// templates are content-addressed and never mutated; refresh replaces the
// digest. Linked clones are fast but pin the base (refcounted); full clones
// cost disk but travel for migrate/HA. The scheduler picks per request.
package deployment

import "fmt"

// Clone types.
const (
	CloneLinked = "linked"
	CloneFull   = "full"
)

// Template is one immutable template revision.
type Template struct {
	Digest   string // sha256 of the packed rootfs
	Source   string // image ref or specialist-os name
	RefCount int    // live linked clones pinning this base
}

// Validate gates template registration.
func (t Template) Validate() error {
	if len(t.Digest) != 64 {
		return fmt.Errorf("deployment: template digest must be sha256 hex")
	}
	if t.Source == "" {
		return fmt.Errorf("deployment: template needs a source")
	}
	if t.RefCount < 0 {
		return fmt.Errorf("deployment: negative refcount")
	}
	return nil
}

// Collectible reports whether the base can be garbage-collected: zero live
// linked clones. Full clones never pin.
func (t Template) Collectible() bool { return t.RefCount == 0 }

// CloneDecision picks linked vs full: linked only on the same node while the
// base is present; migrate/HA or cross-node always goes full.
func CloneDecision(sameNode, basePresent, forMigrate bool) string {
	if forMigrate || !sameNode || !basePresent {
		return CloneFull
	}
	return CloneLinked
}
