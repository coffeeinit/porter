package metrics

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCgroupSampler(t *testing.T) {
	dir := t.TempDir()
	scope := filepath.Join(dir, "porter-vm1.scope")
	if err := os.MkdirAll(scope, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scope, "memory.current"), []byte("134217728\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scope, "cpu.stat"), []byte("usage_usec 2000000\nnr_periods 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := (CgroupSampler{Base: dir}).Sample("vm1")
	if err != nil {
		t.Fatal(err)
	}
	if s.MemMiB != 128 {
		t.Fatalf("want 128MiB, got %v", s.MemMiB)
	}
	if _, err := (CgroupSampler{Base: dir}).Sample("ghost"); err == nil {
		t.Fatal("missing scope must error (skip, never fabricate)")
	}
	if _, err := (CgroupSampler{Base: dir}).Sample("../evil"); err == nil {
		t.Fatal("traversal must fail")
	}
}
