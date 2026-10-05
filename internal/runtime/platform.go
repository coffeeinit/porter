package runtime

import (
	"fmt"
	goruntime "runtime"
)

// MicroVMHost reports whether this OS can run Firecracker MicroVMs.
// Firecracker is Linux-only (KVM, TAP, jailer, cgroups): the control plane
// compiles and serves the API everywhere, but boots require Linux.
func MicroVMHost() bool { return goruntime.GOOS == "linux" }

// RequireMicroVMHost fails an operation explicitly on non-Linux hosts so
// callers surface a clear condition instead of an obscure exec/socket error.
// Controllers persist the message on the VM (never generic "failed").
func RequireMicroVMHost(op string) error {
	if MicroVMHost() {
		return nil
	}
	return fmt.Errorf("%s requires a Linux host with KVM (running on %s: control plane works, MicroVM boot does not)",
		op, goruntime.GOOS)
}
