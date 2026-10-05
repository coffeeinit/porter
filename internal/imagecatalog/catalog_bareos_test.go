package imagecatalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestShippedBareOSCatalog guards the on-disk bare-OS library
// (repo data/images/*.json, the default ImagesDir): every entry must carry a
// kernel + rootfs host path and pinned digests so deploys resolve without
// hidden defaults.
func TestShippedBareOSCatalog(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "data", "images")
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("no shipped catalog at %s: %v", dir, err)
	}
	c := New(dir)
	all := c.All()
	if len(all) < 4 {
		t.Fatalf("expected >=4 bare-OS images, got %d", len(all))
	}
	want := map[string]bool{
		"base://alpine-3.8": false, "base://alpine-3.21": false,
		"base://debian-12": false, "base://ubuntu-24.04": false,
	}
	for _, m := range all {
		if _, ok := want[m.Image]; !ok {
			continue
		}
		want[m.Image] = true
		if m.Kernel == "" || m.Rootfs == "" {
			t.Errorf("%s: kernel/rootfs paths must be set", m.Image)
		}
		if len(m.KernelSHA256) != 64 || len(m.RootfsSHA256) != 64 {
			t.Errorf("%s: digests must be pinned sha256", m.Image)
		}
		if !strings.HasPrefix(m.Status, "ready") {
			t.Errorf("%s: status must be ready, got %q", m.Image, m.Status)
		}
	}
	for ref, seen := range want {
		if !seen {
			t.Errorf("catalog missing %s", ref)
		}
	}
}
