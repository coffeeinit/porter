package buildkit

import (
	"archive/tar"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// mkfsExt4OrSkip gates the real-conversion tests on e2fsprogs (Linux hosts
// only; mkfs.ext4 -d is what ConvertOCIToExt4 shells out to).
func mkfsExt4OrSkip(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("mkfs.ext4"); err != nil {
		t.Skip("mkfs.ext4 not available; rootfs formatting requires Linux e2fsprogs")
	}
}

// newTestTar opens a tar writer for a garbage-archive fixture.
func newTestTar(t *testing.T, path string) *tar.Writer {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return tar.NewWriter(f)
}

func closeTestTar(t *testing.T, path string) {
	t.Helper()
	// The writer was already closed by the caller via the returned *tar.Writer;
	// this helper only exists to keep the fixture call sites symmetric.
	_ = path
}

// TestConvertRealOCILayout proves the prebuilt OCI-layout leg end to end:
// a hand-built OCI tar → unpack → init-shim install → ext4 rootfs, with the
// image config (Entrypoint/Cmd/Env/WorkingDir) carried through in the Result.
func TestConvertRealOCILayout(t *testing.T) {
	mkfsExt4OrSkip(t)
	oci := filepath.Join(t.TempDir(), "image-oci.tar")
	writeOCILayoutTar(t, oci, false)
	if !ValidOCITar(oci) {
		t.Fatal("OCI layout tar rejected by ValidOCITar")
	}
	out := filepath.Join(t.TempDir(), "rootfs.ext4")
	res, err := ConvertOCIToExt4(context.Background(), oci, out, 64)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if res.RootfsPath != out {
		t.Fatalf("rootfs path = %q, want %q", res.RootfsPath, out)
	}
	st, err := os.Stat(out)
	if err != nil || st.Size() == 0 {
		t.Fatalf("rootfs missing/empty: %v %v", st, err)
	}
	if len(res.Entrypoint) != 1 || res.Entrypoint[0] != "/bin/echo" {
		t.Errorf("entrypoint = %v, want [/bin/echo]", res.Entrypoint)
	}
	if len(res.Cmd) != 1 || res.Cmd[0] != "hi" {
		t.Errorf("cmd = %v, want [hi]", res.Cmd)
	}
	if res.WorkingDir != "/app" {
		t.Errorf("workdir = %q, want /app", res.WorkingDir)
	}
	if len(res.Env) != 1 || res.Env[0] != "FOO=bar" {
		t.Errorf("env = %v, want [FOO=bar]", res.Env)
	}
}

// TestConvertRealDockerSave covers the `docker save` variant the buildpacks
// engine exports (manifest.json layout, no index.json).
func TestConvertRealDockerSave(t *testing.T) {
	mkfsExt4OrSkip(t)
	oci := filepath.Join(t.TempDir(), "image-docker-save.tar")
	writeDockerSaveTar(t, oci)
	if !ValidOCITar(oci) {
		t.Fatal("docker-save tar rejected by ValidOCITar")
	}
	out := filepath.Join(t.TempDir(), "rootfs.ext4")
	res, err := ConvertOCIToExt4(context.Background(), oci, out, 64)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if len(res.Entrypoint) != 1 || res.Entrypoint[0] != "/bin/sh" {
		t.Errorf("entrypoint = %v, want [/bin/sh]", res.Entrypoint)
	}
	if st, err := os.Stat(out); err != nil || st.Size() == 0 {
		t.Fatalf("rootfs missing/empty: %v %v", st, err)
	}
}

// TestValidOCITarRejectsGarbage pins the upload route's pre-flight: a tar
// without index.json/manifest.json must not pass as an image.
func TestValidOCITarRejectsGarbage(t *testing.T) {
	junk := filepath.Join(t.TempDir(), "junk.tar")
	tw := newTestTar(t, junk)
	writeTarEntry(t, tw, "repositories", []byte("x"))
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if ValidOCITar(junk) {
		t.Error("garbage tar accepted as an OCI image")
	}
	if ValidOCITar(filepath.Join(t.TempDir(), "missing.tar")) {
		t.Error("missing file accepted as an OCI image")
	}
}
