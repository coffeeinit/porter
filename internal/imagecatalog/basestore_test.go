package imagecatalog

import (
	"strings"
	"testing"
)

func TestBaseValidate(t *testing.T) {
	ok := BaseImage{Name: "debian-trixie", Kind: "rootfs", Version: "1", Digest: strings.Repeat("a", 64), Size: 100}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := ok
	bad.Digest = "short"
	if err := bad.Validate(); err == nil {
		t.Fatal("short digest must fail")
	}
}

func TestNormalizeRef(t *testing.T) {
	if got := NormalizeRef("alpine"); got != "docker.io/library/alpine:latest" {
		t.Fatal(got)
	}
	if got := NormalizeRef("python:3.12-slim"); got != "docker.io/library/python:3.12-slim" {
		t.Fatal(got)
	}
	if len(PopularImages) == 0 {
		t.Fatal("cache list must not be empty")
	}
	if _, err := DefaultTemplate("standard"); err != nil {
		t.Fatal(err)
	}
	if _, err := DefaultTemplate("nope"); err == nil {
		t.Fatal("unknown profile must fail")
	}
}
