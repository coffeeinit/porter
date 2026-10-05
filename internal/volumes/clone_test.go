package volumes

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClone(t *testing.T) {
	m := NewManager(t.TempDir())
	if _, err := m.Create("src1", 16); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.Path("src1"), "data.img"), []byte("payload-1"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst, err := m.Clone("src1", "dst1", 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "data.img"))
	if err != nil || string(got) != "payload-1" {
		t.Fatalf("clone content wrong: %q,%v", got, err)
	}
	if _, err := m.Clone("src1", "dst1", 0); err == nil {
		t.Fatal("existing target must fail")
	}
	if _, err := m.Clone("ghost", "dst2", 0); err == nil {
		t.Fatal("missing source must fail")
	}
	if _, err := m.Clone("x", "x", 0); err == nil {
		t.Fatal("self-clone must fail")
	}
}
