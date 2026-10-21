// Volume clone + resize operations (FCM-19): golden-image cloning (copy
// rootfs with FRESH MAC/IP — never copy-paste identity) plus online expand
// and offline shrink of ext4 volumes. Volumes survive VM deletion; clone
// and resize are audited operations on the volume, not the VM.
package storage

import "fmt"

// ClonePlan copies a volume to a new volume with fresh network identity.
type ClonePlan struct {
	SourceVolume string
	TargetVolume string
	NewMAC       string // must differ from source
	NewIP        string // must differ from source
	SourceMAC    string
	SourceIP     string
}

// Validate gates clone plans: identity must be fresh on both axes.
func (p ClonePlan) Validate() error {
	if p.SourceVolume == "" || p.TargetVolume == "" {
		return fmt.Errorf("storage: clone needs source and target volumes")
	}
	if p.SourceVolume == p.TargetVolume {
		return fmt.Errorf("storage: clone target must differ from source")
	}
	if p.NewMAC == "" || p.NewIP == "" {
		return fmt.Errorf("storage: clone needs fresh MAC and IP")
	}
	if p.NewMAC == p.SourceMAC || p.NewIP == p.SourceIP {
		return fmt.Errorf("storage: clone must not reuse source MAC/IP")
	}
	return nil
}

// Resize kinds.
const (
	ResizeExpand = "expand" // online: resize2fs up
	ResizeShrink = "shrink" // offline: e2fsck + resize2fs down
)

// ResizeOp is one volume resize.
type ResizeOp struct {
	Volume  string
	Kind    string
	SizeMiB int64
	Online  bool // shrink must be offline
}

// Validate gates resizes: shrink is never online, sizes positive.
func (r ResizeOp) Validate() error {
	if r.Volume == "" || r.SizeMiB <= 0 {
		return fmt.Errorf("storage: resize needs a volume and positive size")
	}
	switch r.Kind {
	case ResizeExpand:
	case ResizeShrink:
		if r.Online {
			return fmt.Errorf("storage: shrink must be offline")
		}
	default:
		return fmt.Errorf("storage: unknown resize kind %q", r.Kind)
	}
	return nil
}
