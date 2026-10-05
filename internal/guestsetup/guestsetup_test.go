package guestsetup

import (
	"strings"
	"testing"
)

func TestSetupScriptStampGuard(t *testing.T) {
	s := SetupScript(SetupOptions{Profile: ProfileStandard})
	if !strings.Contains(s, StampPath) {
		t.Fatal("setup must be stamp-guarded")
	}
	if strings.Contains(s, "dhclient") && !strings.Contains(s, "porter-static-net") {
		t.Fatal("DHCP path must check for static config")
	}
	st := SetupScript(SetupOptions{Profile: ProfileMinimal, StaticCIDR: "10.42.0.5/30"})
	if strings.Contains(st, "dhclient") {
		t.Fatal("static config must skip DHCP")
	}
}

func TestMinInitExecs(t *testing.T) {
	s := MinInit([]string{"/bin/sh", "-c", "echo hi"})
	if !strings.Contains(s, "exec '/bin/sh'") || !strings.Contains(s, "/proc") {
		t.Fatal("min init must mount proc and exec command")
	}
}

func TestAITemplate(t *testing.T) {
	bad := AITemplate{}
	if err := bad.Validate(); err == nil {
		t.Fatal("unsized template must fail")
	}
	ok := AITemplate{RootReadOnly: true, ModelsSizeGB: 20, ModelsLabel: AIModelsLabel}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ok.FstabEntry(), "nofail") {
		t.Fatal("models fstab must be nofail")
	}
}

func TestVersionsPin(t *testing.T) {
	v := VersionsPin{Rootfs: "sha256:aaa", Runtime: "v1", Agent: "v2"}.Render()
	if !strings.Contains(v, "sha256:aaa") {
		t.Fatal("pin must carry digests")
	}
}
