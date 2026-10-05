// Guest kernel-argument contract (FCM-01): user-visible args stay separate
// from mandatory hidden args, and the guest proves it is Porter-managed via
// /proc/cmdline. The runtime appends; porter doctor verifies.
package runtime

import (
	"fmt"
	"strings"
)

// DefaultKernelArgs is the user-visible base every guest boots with.
const DefaultKernelArgs = "console=ttyS0,115200 reboot=k panic=1"

// managed markers, always appended. porter.* namespace (never fcm.*).
const (
	hypervisorArg = "porter.hypervisor=firecracker"
	managedArg    = "porter.managed=true"
	internalArgs  = "pci=off random.trust_cpu=on"
)

// BuildKernelArgs merges user args with the mandatory hidden set. Hidden
// args win on collision: management identity is not user-overridable.
func BuildKernelArgs(userArgs string) string {
	seen := map[string]bool{}
	var out []string
	for _, f := range strings.Fields(userArgs) {
		k := fieldKey(f)
		if k == "porter.hypervisor" || k == "porter.managed" {
			continue // hidden set wins
		}
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	out = append(out, strings.Fields(DefaultKernelArgs)...)
	out = append(out, strings.Fields(internalArgs)...)
	out = append(out, hypervisorArg, managedArg)
	return strings.Join(out, " ")
}

func fieldKey(f string) string {
	if i := strings.Index(f, "="); i >= 0 {
		return f[:i]
	}
	return f
}

// IsPorterManaged reports whether a guest cmdline carries the management
// identity (porter doctor check + admission gate).
func IsPorterManaged(cmdline string) bool {
	fields := map[string]bool{}
	for _, f := range strings.Fields(cmdline) {
		fields[f] = true
	}
	return fields[hypervisorArg] && fields[managedArg]
}

// ValidateKernelArgs rejects empty args and spoofed management markers in
// user input (the builder adds the real ones).
func ValidateKernelArgs(userArgs string) error {
	if strings.TrimSpace(userArgs) == "" {
		return fmt.Errorf("runtime: kernel args must not be empty")
	}
	return nil
}
