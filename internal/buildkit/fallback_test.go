package buildkit

import (
	"archive/tar"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeFakeOCI(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tw := tar.NewWriter(f)
	defer tw.Close()
	idx := []byte(`{"manifests":[{"digest":"sha256:abc"}]}`)
	if err := tw.WriteHeader(&tar.Header{Name: "index.json", Mode: 0o644, Size: int64(len(idx))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(idx); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureOCIReusesPrebuiltTar(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "image.oci.tar")
	writeFakeOCI(t, out)
	b := Builder{Bin: "definitely-not-buildctl", Addr: "unix:///none"}
	if err := b.EnsureOCI(context.Background(), "", "", out); err != nil {
		t.Fatalf("prebuilt OCI tar must be reused without daemon: %v", err)
	}
}

func TestValidOCITarRejectsEmpty(t *testing.T) {
	if validOCITar(filepath.Join(t.TempDir(), "missing.tar")) {
		t.Fatal("missing file must not validate")
	}
}
