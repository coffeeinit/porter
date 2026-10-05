package imagecatalog

import "testing"

func TestResolveGuestBaseDefaultsToAlpine(t *testing.T) {
	base, err := ResolveGuestBase("", "alpine", nil)
	if err != nil || base.Family != "alpine" || base.Reference != "porter://alpine/latest" {
		t.Fatalf("unexpected default base: %#v, %v", base, err)
	}
}

func TestResolveManagedGuestBaseAliases(t *testing.T) {
	for _, want := range []string{"debian", "ubuntu"} {
		base, err := ResolveGuestBase(want, "alpine", nil)
		if err != nil || base.Family != want || !base.Managed {
			t.Fatalf("unexpected %s base: %#v, %v", want, base, err)
		}
	}
}

func TestResolveCustomGuestBaseRequiresArtifacts(t *testing.T) {
	if _, err := ResolveGuestBase("custom://broken", "alpine", map[string]GuestBase{}); err == nil {
		t.Fatal("expected unregistered custom base to fail")
	}
	base, err := ResolveGuestBase("custom://ok", "alpine", map[string]GuestBase{
		"custom://ok": {Reference: "custom://ok", Family: "custom", KernelPath: "/k/vmlinux", RootfsPath: "/r/rootfs.ext4"},
	})
	if err != nil || base.Managed || base.Family != "custom" {
		t.Fatalf("unexpected custom base: %#v, %v", base, err)
	}
}
