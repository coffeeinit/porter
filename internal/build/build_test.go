package build

import (
	"testing"
)

func TestDetectDistro(t *testing.T) {
	cases := map[string]string{
		"ID=debian\n":                      DistroApt,
		"ID=ubuntu\nID_LIKE=debian\n":      DistroApt,
		"ID=alpine\n":                      DistroApk,
		"ID=fedora\n":                      DistroDnf,
		"ID=\"rocky\"\nID_LIKE=\"rhel\"\n": DistroDnf,
		"ID=amzn\nID_LIKE=\"fedora\"\n":    DistroDnf,
		"ID=plan9\n":                       "",
		"":                                 "",
	}
	for in, want := range cases {
		if got := DetectDistro(in); got != want {
			t.Fatalf("DetectDistro(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProfilePackages(t *testing.T) {
	p, err := ProfilePackages(DistroApt, ProfileStandard)
	if err != nil || len(p) == 0 {
		t.Fatalf("apt standard: %v %v", p, err)
	}
	el, err := ProfilePackages(DistroDnf, ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	foundNM, foundFull := false, false
	for _, x := range el {
		if x == "NetworkManager" {
			foundNM = true
		}
		if x == "util-linux" {
			foundFull = true
		}
	}
	if !foundNM || !foundFull {
		t.Fatalf("EL needs NetworkManager + full util-linux: %v", el)
	}
	if _, err := ProfilePackages("plan9", ProfileStandard); err == nil {
		t.Fatal("unknown distro must fail closed")
	}
}

func TestGuestPrep(t *testing.T) {
	g := NewGuestPrep()
	if g.Ready() {
		t.Fatal("empty prep must not be ready")
	}
	if err := g.Complete("bogus"); err == nil {
		t.Fatal("unknown stage must fail")
	}
	for _, s := range Stages {
		if err := g.Complete(s); err != nil {
			t.Fatal(err)
		}
	}
	if !g.Ready() {
		t.Fatal("full pipeline must be ready")
	}
}

func TestConvert(t *testing.T) {
	ok := ConvertSpec{Source: SourceRegistry, Ref: "debian@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := ConvertSpec{Source: SourceRegistry, Ref: "debian:trixie-slim"}
	if err := bad.Validate(); err == nil {
		t.Fatal("floating tag must fail")
	}
	if got := SizeGiBFor(0); got != 2 {
		t.Fatalf("floor must be 2GiB, got %d", got)
	}
	if got := SizeGiBFor(2 * 1024 * 1024 * 1024); got < 2 {
		t.Fatalf("growth must apply, got %d", got)
	}
}
