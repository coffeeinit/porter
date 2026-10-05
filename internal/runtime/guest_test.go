package runtime

import (
	"strings"
	"testing"
)

func TestBuildKernelArgs(t *testing.T) {
	got := BuildKernelArgs("root=/dev/vda rw")
	for _, want := range []string{"root=/dev/vda", "console=ttyS0,115200",
		"porter.hypervisor=firecracker", "porter.managed=true", "random.trust_cpu=on"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	if !IsPorterManaged(got) {
		t.Fatal("built args must verify as managed")
	}
	// User-supplied spoof markers are replaced, never duplicated.
	spoofed := BuildKernelArgs("porter.managed=false")
	if strings.Contains(spoofed, "porter.managed=false") {
		t.Fatalf("spoof survived: %q", spoofed)
	}
	if strings.Count(spoofed, "porter.managed=true") != 1 {
		t.Fatalf("marker must appear exactly once: %q", spoofed)
	}
	if IsPorterManaged("console=ttyS0 root=/dev/vda rw") {
		t.Fatal("foreign cmdline must not verify")
	}
	if err := ValidateKernelArgs("  "); err == nil {
		t.Fatal("empty args must fail")
	}
}
