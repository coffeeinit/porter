package runtime

import "testing"

func TestSelectRuntime(t *testing.T) {
	allow := map[string]bool{RuntimeQEMUMicroVM: true, RuntimeQEMUAQ35: true}
	if got, err := SelectRuntime("", "debian", true, true, nil); err != nil || got != RuntimeFirecracker {
		t.Fatalf("default fc: %v %q", err, got)
	}
	if _, err := SelectRuntime("", "debian", true, false, nil); err == nil {
		t.Fatal("fc without binary must fail")
	}
	if _, err := SelectRuntime("", "openwrt", true, true, allow); err == nil {
		t.Fatal("router guest on fc must fail")
	}
	if got, err := SelectRuntime(RuntimeQEMUAQ35, "openwrt", false, false, allow); err != nil || got != RuntimeQEMUAQ35 {
		t.Fatalf("q35 fallback: %v %q", err, got)
	}
	if _, err := SelectRuntime(RuntimeQEMUAQ35, "openwrt", true, true, nil); err == nil {
		t.Fatal("q35 without capability must fail")
	}
	if _, err := SelectRuntime(RuntimeQEMUMicroVM, "smolbsd", true, false, allow); err == nil {
		t.Fatal("smolbsd needs q35, not microvm")
	}
	if _, err := SelectRuntime("lxc", "debian", true, true, allow); err == nil {
		t.Fatal("unknown runtime must fail")
	}
}
