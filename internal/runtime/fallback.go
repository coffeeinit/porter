// VMM selection with QEMU fallback (PVE-21 remainder). Firecracker is the
// default and the only production runtime; QEMU microvm covers dev hosts
// without FC/KVM, and QEMU q35 covers specialist guests that cannot run
// under Firecracker/microvm (BSD, Plan 9, router images). q35 requires the
// explicit qemu_fallback.allow capability — never a silent downgrade.
package runtime

import "fmt"

// Runtimes.
const (
	RuntimeFirecracker = "firecracker"
	RuntimeQEMUMicroVM = "qemu-microvm"
	RuntimeQEMUAQ35    = "qemu-q35"
)

// Guest families that cannot run under Firecracker.
var q35Guests = map[string]bool{
	"smolbsd": true, "openwrt": true, "opnsense": true,
	"9front": true, "osv": true, "gokrazy": true,
}

// SelectRuntime resolves the VMM for a boot request. kvmPresent/fcPresent
// describe the host; allowed lists the tenant's granted runtimes
// (qemu_fallback.allow gates both QEMU variants).
func SelectRuntime(want, guestOS string, kvmPresent, fcPresent bool, allowed map[string]bool) (string, error) {
	needsQ35 := q35Guests[guestOS]
	switch want {
	case "", RuntimeFirecracker:
		if needsQ35 {
			return "", fmt.Errorf("runtime: guest %q needs qemu-q35, not firecracker", guestOS)
		}
		if !kvmPresent || !fcPresent {
			return "", fmt.Errorf("runtime: firecracker needs KVM + firecracker binary")
		}
		return RuntimeFirecracker, nil
	case RuntimeQEMUMicroVM:
		if needsQ35 {
			return "", fmt.Errorf("runtime: guest %q needs qemu-q35, not qemu-microvm", guestOS)
		}
		if !allowed[RuntimeQEMUMicroVM] {
			return "", fmt.Errorf("runtime: qemu-microvm needs qemu_fallback.allow")
		}
		if !kvmPresent {
			return "", fmt.Errorf("runtime: qemu-microvm needs KVM")
		}
		return RuntimeQEMUMicroVM, nil
	case RuntimeQEMUAQ35:
		if !allowed[RuntimeQEMUAQ35] {
			return "", fmt.Errorf("runtime: qemu-q35 needs qemu_fallback.allow")
		}
		return RuntimeQEMUAQ35, nil
	default:
		return "", fmt.Errorf("runtime: unknown runtime %q", want)
	}
}
