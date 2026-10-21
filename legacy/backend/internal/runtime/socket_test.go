package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSocketPathIsSanitizedAndScoped(t *testing.T) {
	dir := t.TempDir()
	path := socketPath(dir, "org/project replica")
	if filepath.Dir(path) != dir {
		t.Fatalf("socket escaped configured directory: %s", path)
	}
	if !strings.HasPrefix(filepath.Base(path), "porter-org-project-replica") || !strings.HasSuffix(path, ".sock") {
		t.Fatalf("unexpected socket path: %s", path)
	}
}

func TestRemoveSocketIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "porter-test.sock")
	if err := removeSocket(path); err != nil {
		t.Fatalf("remove missing socket: %v", err)
	}
	if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeSocket(path); err != nil {
		t.Fatalf("remove socket: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("socket still exists or stat failed: %v", err)
	}
}
