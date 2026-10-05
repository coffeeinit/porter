package buildkit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectPriority(t *testing.T) {
	dir := t.TempDir()
	if Detect(dir) != EngineBuildpacks {
		t.Fatal("empty dir must fall back to buildpacks")
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if Detect(dir) != EngineNixpacks {
		t.Fatal("node manifest must detect nixpacks")
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Dockerfile alone does not beat explicit manifests here; explicit pack
	// config always wins.
	if err := os.WriteFile(filepath.Join(dir, "railpack.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if Detect(dir) != EngineRailpack {
		t.Fatal("railpack.json must win")
	}
}
