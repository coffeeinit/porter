package volumes

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSanitizeID(t *testing.T) {
	tests := []struct{ in, want string }{
		{"abc-123", "abc-123"},
		{"abc/..", "abc---"}, // "/" and "." are replaced with "-" (path-traversal safe)
		{"a b", "a-b"},
		{"../etc/passwd", "---etc-passwd"},
	}
	for _, tt := range tests {
		if got := SanitizeID(tt.in); got != tt.want {
			t.Errorf("SanitizeID(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCreateAndUsage(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	if err := m.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	path, err := m.Create("vol1", 4)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Exists("vol1") {
		t.Fatal("expected volume to exist")
	}
	img := filepath.Join(path, "data.img")
	fi, err := os.Stat(img)
	if err != nil {
		t.Fatalf("expected data.img: %v", err)
	}
	if fi.Size() != 4*1024*1024 {
		t.Fatalf("expected 4MiB sparse image, got %d", fi.Size())
	}
	// Write a file and confirm usage grows.
	if err := os.WriteFile(filepath.Join(path, "hello.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	used, err := m.Usage("vol1")
	if err != nil {
		t.Fatal(err)
	}
	if used < 5 {
		t.Fatalf("expected usage >= 5 bytes, got %d", used)
	}
	// Delete removes the dir.
	if err := m.Delete("vol1"); err != nil {
		t.Fatal(err)
	}
	if m.Exists("vol1") {
		t.Fatal("expected volume removed")
	}
}
