package buildkit

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestConvertRealOCI exercises ConvertOCIToExt4 against a real buildkit OCI
// tar. Gated by PORTER_TEST_OCI_TAR (a build artifact, not a fixture), so
// unit runs skip: set it in Linux e2e to prove the OCI -> rootfs leg.
func TestConvertRealOCI(t *testing.T) {
	oci := os.Getenv("PORTER_TEST_OCI_TAR")
	if oci == "" {
		t.Skip("PORTER_TEST_OCI_TAR not set; skipping real OCI conversion")
	}
	out := os.Getenv("PORTER_TEST_ROOTFS_OUT")
	if out == "" {
		t.Skip("PORTER_TEST_ROOTFS_OUT not set; skipping real OCI conversion")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	res, err := ConvertOCIToExt4(ctx, oci, out, 256)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	st, err := os.Stat(res.RootfsPath)
	if err != nil || st.Size() == 0 {
		t.Fatalf("rootfs missing/empty: %v %v", res.RootfsPath, err)
	}
	t.Logf("rootfs=%s bytes=%d entrypoint=%v cmd=%v", res.RootfsPath, st.Size(), res.Entrypoint, res.Cmd)
}
