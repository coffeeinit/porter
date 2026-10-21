package volumes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Hermetic: IDs that sanitize to the same directory collide like identical IDs.
func TestT13CloneSanitizedCollision(t *testing.T) {
	m := NewManager(t.TempDir())
	if _, err := m.Create("a-b", 1); err != nil {
		t.Fatal(err)
	}
	// "a/b" sanitizes to "a-b": same source and same target after sanitize.
	if _, err := m.Clone("a-b", "a/b", 0); err == nil {
		t.Fatal("sanitized self-clone must fail")
	}
	if _, err := m.Clone("a/b", "c-t13", 0); err != nil {
		t.Fatalf("sanitized source must resolve to existing dir: %v", err)
	}
	if !m.Exists(SanitizeID("c-t13")) {
		t.Fatal("clone from sanitized source must exist")
	}
	// Target colliding with an existing sanitized dir must fail.
	if _, err := m.Clone("c-t13", "a/b", 0); err == nil {
		t.Fatal("clone onto sanitized-existing target must fail")
	}
}

// Hermetic: traversal IDs stay inside the volumes root.
func TestT13CloneTraversalSafe(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root)
	if _, err := m.Create("src-t13", 1); err != nil {
		t.Fatal(err)
	}
	dst, err := m.Clone("src-t13", "../evil-t13", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(filepath.Clean(dst), filepath.Clean(root)) {
		t.Fatalf("clone escaped root: %q", dst)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "evil-t13")); !os.IsNotExist(err) {
		t.Fatal("clone must not create a sibling outside the root")
	}
}

// Hermetic: a copy failure (data.img replaced by a directory) leaves no dst dir.
func TestT13CloneCopyFailureCleansUp(t *testing.T) {
	m := NewManager(t.TempDir())
	if _, err := m.Create("src-copyfail", 1); err != nil {
		t.Fatal(err)
	}
	img := filepath.Join(m.Path("src-copyfail"), "data.img")
	if err := os.Remove(img); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(img, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Clone("src-copyfail", "dst-copyfail", 0); err == nil {
		t.Fatal("clone of unreadable data.img must fail")
	}
	if m.Exists(SanitizeID("dst-copyfail")) {
		t.Fatal("failed clone must not leave a target dir")
	}
	if !m.Exists("src-copyfail") {
		t.Fatal("failed clone must leave the source alone")
	}
}

// Hermetic: size inheritance edges — negative inherits, corrupt marker yields 0.
func TestT13CloneSizeInheritanceEdges(t *testing.T) {
	m := NewManager(t.TempDir())
	if _, err := m.Create("src-size", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Clone("src-size", "dst-neg", -5); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(m.Path(SanitizeID("dst-neg")), "VOLUME"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "size_mib=3") {
		t.Fatalf("negative size must inherit source size, got %q", raw)
	}
	// Corrupt the source marker: inheritance degrades to size_mib=0.
	if err := os.WriteFile(filepath.Join(m.Path("src-size"), "VOLUME"), []byte("garbage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Clone("src-size", "dst-corrupt", 0); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(filepath.Join(m.Path(SanitizeID("dst-corrupt")), "VOLUME"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "size_mib=0") {
		t.Fatalf("corrupt marker must degrade to size_mib=0, got %q", raw)
	}
	if !strings.Contains(string(raw), "cloned_from=src-size") {
		t.Fatalf("degraded clone must still record source, got %q", raw)
	}
}

// Hermetic: only data.img + VOLUME are cloned; the source marker is untouched.
func TestT13CloneScopeAndSourceUntouched(t *testing.T) {
	m := NewManager(t.TempDir())
	if _, err := m.Create("src-scope", 1); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.Path("src-scope"), "extra.txt"), []byte("sidecar"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst, err := m.Clone("src-scope", "dst-scope", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "extra.txt")); !os.IsNotExist(err) {
		t.Fatal("clone copies data.img only; sidecar files must not leak into the clone")
	}
	srcMarker, err := os.ReadFile(filepath.Join(m.Path("src-scope"), "VOLUME"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(srcMarker), "cloned_from=") {
		t.Fatalf("source marker must be untouched, got %q", srcMarker)
	}
}
