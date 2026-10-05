package volumes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Hermetic: clone copies content, inherits the size marker when sizeMiB <= 0,
// records cloned_from, honors an explicit size, and stays independent.
func TestA5CloneMarkerAndIndependence(t *testing.T) {
	m := NewManager(t.TempDir())
	if _, err := m.Create("src-a5", 1); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.Path("src-a5"), "data.img"), []byte("payload-a5"), 0o644); err != nil {
		t.Fatal(err)
	}

	dst, err := m.Clone("src-a5", "dst-a5", 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "data.img"))
	if err != nil || string(got) != "payload-a5" {
		t.Fatalf("clone content wrong: %q,%v", got, err)
	}
	marker, err := os.ReadFile(filepath.Join(dst, "VOLUME"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(marker), "size_mib=1") {
		t.Fatalf("clone must inherit source size, got %q", marker)
	}
	if !strings.Contains(string(marker), "cloned_from=src-a5") {
		t.Fatalf("clone must record source, got %q", marker)
	}

	if _, err := m.Clone("src-a5", "dst-sized", 7); err != nil {
		t.Fatal(err)
	}
	sized, err := os.ReadFile(filepath.Join(m.Path("dst-sized"), "VOLUME"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sized), "size_mib=7") {
		t.Fatalf("explicit size must win, got %q", sized)
	}

	// Independence: mutating the clone leaves the source alone.
	if err := os.WriteFile(filepath.Join(dst, "data.img"), []byte("mutated"), 0o644); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join(m.Path("src-a5"), "data.img"))
	if err != nil || string(src) != "payload-a5" {
		t.Fatalf("source must be independent of clone: %q,%v", src, err)
	}
}

// Hermetic: validation failures never leave a half-built target behind.
func TestA5CloneValidation(t *testing.T) {
	m := NewManager(t.TempDir())
	if _, err := m.Create("src-v", 1); err != nil {
		t.Fatal(err)
	}
	for name, fn := range map[string]func() (string, error){
		"empty source":    func() (string, error) { return m.Clone("", "dst-x", 0) },
		"empty target":    func() (string, error) { return m.Clone("src-v", "", 0) },
		"self clone":      func() (string, error) { return m.Clone("src-v", "src-v", 0) },
		"missing source":  func() (string, error) { return m.Clone("ghost-a5", "dst-x", 0) },
		"existing target": func() (string, error) { return m.Clone("src-v", "src-v", 1) },
	} {
		if _, err := fn(); err == nil {
			t.Fatalf("%s must fail", name)
		}
	}
	// Existing target id must fail even for a different source.
	if _, err := m.Clone("ghost-a5", "src-v", 0); err == nil {
		t.Fatal("clone onto existing target must fail")
	}
	// Missing data.img: clone fails and the dst dir is cleaned up.
	if err := os.Remove(filepath.Join(m.Path("src-v"), "data.img")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Clone("src-v", "dst-broken", 0); err == nil {
		t.Fatal("clone with missing data.img must fail")
	}
	if m.Exists("dst-broken") {
		t.Fatal("failed clone must not leave a target dir")
	}
}

// Hermetic: ids are sanitized to safe directory names.
func TestA5CloneSanitizedIDs(t *testing.T) {
	m := NewManager(t.TempDir())
	if _, err := m.Create("plain-a5", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Clone("plain-a5", "a/b", 0); err != nil {
		t.Fatal(err)
	}
	if !m.Exists(SanitizeID("a/b")) {
		t.Fatalf("sanitized target %q must exist", SanitizeID("a/b"))
	}
}
