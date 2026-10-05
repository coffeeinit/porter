package buildkit

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTarEntry appends one file entry to tw.
func writeTarEntry(t *testing.T, tw *tar.Writer, name string, body []byte) {
	t.Helper()
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
}

// writeOCILayoutTar writes a minimal OCI-layout tar. When dotSlash is true,
// entry names carry the "./" prefix some writers emit.
func writeOCILayoutTar(t *testing.T, path string, dotSlash bool) {
	t.Helper()
	prefix := ""
	if dotSlash {
		prefix = "./"
	}
	layerInner := new(bytes.Buffer)
	ltw := tar.NewWriter(layerInner)
	writeTarEntry(t, ltw, "hello.txt", []byte("hi"))
	if err := ltw.Close(); err != nil {
		t.Fatal(err)
	}
	var layerBuf bytes.Buffer
	gz := gzip.NewWriter(&layerBuf)
	if _, err := gz.Write(layerInner.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tw := tar.NewWriter(f)
	defer tw.Close()
	writeTarEntry(t, tw, prefix+"index.json", []byte(`{"manifests":[{"digest":"sha256:manifest1"}]}`))
	writeTarEntry(t, tw, prefix+"blobs/sha256/manifest1", []byte(`{"config":{"digest":"sha256:config1"},"layers":[{"digest":"sha256:layer1"}]}`))
	writeTarEntry(t, tw, prefix+"blobs/sha256/config1", []byte(`{"config":{"Entrypoint":["/bin/echo"],"Cmd":["hi"],"Env":["FOO=bar"],"WorkingDir":"/app"}}`))
	writeTarEntry(t, tw, prefix+"blobs/sha256/layer1", layerBuf.Bytes())
}

// writeDockerSaveTar writes a minimal `docker save` archive: manifest.json +
// config file + one plain (uncompressed) layer.tar.
func writeDockerSaveTar(t *testing.T, path string) {
	t.Helper()
	layerInner := new(bytes.Buffer)
	ltw := tar.NewWriter(layerInner)
	writeTarEntry(t, ltw, "from-pack.txt", []byte("pack"))
	if err := ltw.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tw := tar.NewWriter(f)
	defer tw.Close()
	writeTarEntry(t, tw, "manifest.json", []byte(`[{"Config":"abc123.json","RepoTags":["porter-build-1:latest"],"Layers":["abc123/layer.tar"]}]`))
	writeTarEntry(t, tw, "abc123.json", []byte(`{"config":{"Entrypoint":["/bin/sh"],"Cmd":["-c","run"],"Env":["PACK=1"],"WorkingDir":"/srv"}}`))
	writeTarEntry(t, tw, "abc123/layer.tar", layerInner.Bytes())
}

func TestReadOCIConfigDotSlash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oci.tar")
	writeOCILayoutTar(t, path, true)
	cfg, err := readOCIConfig(path)
	if err != nil {
		t.Fatalf("./-prefixed OCI layout must parse: %v", err)
	}
	if len(cfg.Config.Entrypoint) != 1 || cfg.Config.Entrypoint[0] != "/bin/echo" {
		t.Fatalf("bad entrypoint: %+v", cfg.Config)
	}
	if cfg.Config.WorkingDir != "/app" || len(cfg.Config.Env) != 1 {
		t.Fatalf("bad config: %+v", cfg.Config)
	}
}

func TestUnpackOCILayoutDotSlash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oci.tar")
	writeOCILayoutTar(t, path, true)
	root := t.TempDir()
	if err := unpackOCI(path, root); err != nil {
		t.Fatalf("unpack ./-prefixed layout: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "hello.txt")); err != nil || string(b) != "hi" {
		t.Fatalf("layer content missing: %q %v", b, err)
	}
}

func TestDockerSaveCompat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "saved.tar")
	writeDockerSaveTar(t, path)
	if !validOCITar(path) {
		t.Fatal("docker-save archive must count as a reusable prebuilt artifact")
	}
	cfg, err := readOCIConfig(path)
	if err != nil {
		t.Fatalf("docker-save config must parse: %v", err)
	}
	if len(cfg.Config.Entrypoint) == 0 || cfg.Config.Entrypoint[0] != "/bin/sh" {
		t.Fatalf("bad entrypoint: %+v", cfg.Config)
	}
	root := t.TempDir()
	if err := unpackOCI(path, root); err != nil {
		t.Fatalf("unpack docker-save: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "from-pack.txt")); err != nil || string(b) != "pack" {
		t.Fatalf("layer content missing: %q %v", b, err)
	}
}

func TestUnpackRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "junk.tar")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	writeTarEntry(t, tw, "repositories", []byte("x"))
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := unpackOCI(path, t.TempDir()); err == nil ||
		!strings.Contains(err.Error(), "index.json") {
		t.Fatalf("garbage tar must fail naming index.json, got %v", err)
	}
}

func TestBuildWithPlanStaticIsGuidance(t *testing.T) {
	b := Builder{Bin: "definitely-not-buildctl"}
	p := Plan{Engine: EngineStatic, ContextDir: t.TempDir(), OutputOCI: filepath.Join(t.TempDir(), "o.tar")}
	if err := b.BuildWithPlan(context.Background(), p); err == nil ||
		!strings.Contains(err.Error(), "prebuilt OCI") {
		t.Fatalf("static engine must error with prebuilt-OCI guidance, got %v", err)
	}
}
