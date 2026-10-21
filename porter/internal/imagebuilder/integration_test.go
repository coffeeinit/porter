//go:build integration

package imagebuilder

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildHTTPImageIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	dir := t.TempDir()
	dockerfile := filepath.Join(dir, "Dockerfile")
	body := "FROM alpine:3.21\nRUN mkdir -p /www && echo porter-http-ok > /www/index.html\nCMD [\"httpd\",\"-f\",\"-p\",\"80\",\"-h\",\"/www\"]\n"
	if err := os.WriteFile(dockerfile, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	b := New("/tmp/porterbin/porter-guest-init", "/mnt/d/github/porter/legacy/porter-images/bases/alpine/3.21/out/vmlinux", t.TempDir())
	res, err := b.BuildDockerfile(ctx, dir, dockerfile, "porter-http-integration", 64)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("rootfs=%s digest=%s size=%d", res.RootfsPath, res.Digest, res.SizeBytes)
}
