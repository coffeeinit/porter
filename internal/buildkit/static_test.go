package buildkit

// Static-engine tests. Everything here runs without KVM or a buildkit
// daemon: rootfs-tree assembly, server verification against the unpacked
// base, OCI round-trip, and init-shim content. The one real-conversion case
// (OCI -> ext4 via mkfs.ext4) is gated behind env vars like
// convert_real_test.go so unit runs skip it.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeStaticBaseTar writes a minimal OCI-layout base tar whose single
// layer contains the given files and whose config carries env/cmd.
func writeStaticBaseTar(t *testing.T, path string, files map[string]string, cfgJSON string) {
	t.Helper()
	layerInner := new(bytes.Buffer)
	ltw := tar.NewWriter(layerInner)
	for name, body := range files {
		if err := ltw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := ltw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
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
	manifestBytes := []byte(`{"schemaVersion":2,"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:cfg1","size":1},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:layer1","size":1}]}`)
	manSum := sha256.Sum256(manifestBytes)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tw := tar.NewWriter(f)
	defer tw.Close()
	writeTarEntry(t, tw, "index.json", []byte(`{"manifests":[{"digest":"sha256:`+hex.EncodeToString(manSum[:])+`"}]}`))
	writeTarEntry(t, tw, "blobs/sha256/"+hex.EncodeToString(manSum[:]), manifestBytes)
	writeTarEntry(t, tw, "blobs/sha256/cfg1", []byte(cfgJSON))
	writeTarEntry(t, tw, "blobs/sha256/layer1", layerBuf.Bytes())
}

func TestLocatePublishDir(t *testing.T) {
	dir := t.TempDir()
	if _, err := locatePublishDir(dir); err == nil || !strings.Contains(err.Error(), "dist/") {
		t.Fatalf("empty context must name the checked inputs, got %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := locatePublishDir(dir); err != nil || filepath.Base(got) != "dist" {
		t.Fatalf("dist/ must win: %q %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := locatePublishDir(dir); err != nil || filepath.Base(got) != "dist" {
		t.Fatalf("dist/ still wins over root index.html: %q %v", got, err)
	}
	if err := os.Remove(filepath.Join(dir, "dist")); err != nil {
		t.Fatal(err)
	}
	if got, err := locatePublishDir(dir); err != nil || got != dir {
		t.Fatalf("root index.html must serve as publish dir: %q %v", got, err)
	}
}

func TestPickStaticServerVerifiesBusyboxHTTPDApplet(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Alpine default: busybox present, httpd applet absent. Must NOT assume
	// httpd works — the base is rejected naming busybox-extras.
	if err := os.WriteFile(filepath.Join(root, "bin", "busybox"), []byte("bb"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := pickStaticServer(root, 80); err == nil || !strings.Contains(err.Error(), "busybox-extras") {
		t.Fatalf("busybox without httpd applet must be rejected naming busybox-extras, got %v", err)
	}
	// busybox-extras parity: the httpd entry appears, httpd is chosen.
	if err := os.MkdirAll(filepath.Join(root, "usr", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "usr", "bin", "httpd"), []byte("!"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv, err := pickStaticServer(root, 80)
	if err != nil {
		t.Fatalf("busybox + httpd applet must serve: %v", err)
	}
	want := []string{"/bin/busybox", "httpd", "-f", "-p", "80", "-h", "/srv"}
	if strings.Join(srv.Argv, " ") != strings.Join(want, " ") || srv.Docroot != "/srv" {
		t.Fatalf("bad server: %+v", srv)
	}
}

func TestPickStaticServerNginx(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "usr", "sbin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "usr", "share", "nginx", "html"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "usr", "sbin", "nginx"), []byte("n"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv, err := pickStaticServer(root, 80)
	if err != nil {
		t.Fatalf("nginx base must serve: %v", err)
	}
	if srv.Argv[0] != "/usr/sbin/nginx" || !strings.Contains(strings.Join(srv.Argv, " "), "daemon off;") {
		t.Fatalf("nginx must run in foreground: %+v", srv)
	}
	if srv.Docroot != "/usr/share/nginx/html" {
		t.Fatalf("nginx docroot: %q", srv.Docroot)
	}
}

func TestPickStaticServerNothing(t *testing.T) {
	if _, err := pickStaticServer(t.TempDir(), 80); err == nil ||
		!strings.Contains(err.Error(), "no verified static server") {
		t.Fatalf("server-less base must fail explicitly, got %v", err)
	}
}

// TestBuildStaticOCIRoundTrip assembles a site onto a busybox base and
// proves the artifact: valid OCI tar, config Cmd = verified server, env
// inherited, site in /srv next to the base's own files.
func TestBuildStaticOCIRoundTrip(t *testing.T) {
	base := filepath.Join(t.TempDir(), "base.tar")
	writeStaticBaseTar(t, base, map[string]string{
		"bin/busybox":    "busybox-bytes",
		"usr/bin/httpd":  "applet-link",
		"etc/os-release": "alpine",
	}, `{"architecture":"x86_64","os":"linux","config":{"Env":["PATH=/usr/local/sbin:/usr/sbin:/usr/bin:/sbin:/bin"],"Cmd":["/bin/sh"]}}`)

	ctxDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ctxDir, "dist", "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctxDir, "dist", "index.html"), []byte("<h1>hi</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctxDir, "dist", "assets", "app.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "site.oci.tar")
	b := Builder{Bin: "definitely-not-buildctl"}
	p := Plan{Engine: EngineStatic, ContextDir: ctxDir, OutputOCI: out, Args: map[string]string{StaticBaseArg: base}}
	if err := b.BuildWithPlan(context.Background(), p); err != nil {
		t.Fatalf("static build: %v", err)
	}
	if !validOCITar(out) {
		t.Fatal("static output must be a reusable OCI tar")
	}
	cfg, err := readOCIConfig(out)
	if err != nil {
		t.Fatalf("read composed config: %v", err)
	}
	if len(cfg.Config.Cmd) != 7 || cfg.Config.Cmd[0] != "/bin/busybox" || cfg.Config.Cmd[1] != "httpd" {
		t.Fatalf("composed Cmd must exec busybox httpd: %+v", cfg.Config.Cmd)
	}
	foundEnv := false
	for _, kv := range cfg.Config.Env {
		if strings.HasPrefix(kv, "PATH=") {
			foundEnv = true
		}
	}
	if !foundEnv {
		t.Fatalf("base env must be inherited, got %+v", cfg.Config.Env)
	}
	root := t.TempDir()
	if err := unpackOCI(out, root); err != nil {
		t.Fatalf("unpack composed tar: %v", err)
	}
	for _, want := range []string{"srv/index.html", "srv/assets/app.js", "bin/busybox", "etc/os-release"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(want))); err != nil {
			t.Fatalf("composed rootfs missing %s: %v", want, err)
		}
	}
}

// TestBuildStaticOCIInitShimConvention proves the composed config feeds the
// shared init shim: BuildInitScript (the exact call ConvertOCIToExt4 makes)
// must exec the verified server as PID 1. Boot itself is KVM-dependent and
// stays untested here.
func TestBuildStaticOCIInitShimConvention(t *testing.T) {
	base := filepath.Join(t.TempDir(), "base.tar")
	writeStaticBaseTar(t, base, map[string]string{
		"bin/busybox":   "bb",
		"usr/bin/httpd": "!",
	}, `{"config":{"Env":["PATH=/usr/bin"],"Cmd":["/bin/sh"]}}`)
	ctxDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ctxDir, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctxDir, "dist", "index.html"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "site.oci.tar")
	b := Builder{}
	if err := b.BuildWithPlan(context.Background(), Plan{Engine: EngineStatic, ContextDir: ctxDir, OutputOCI: out, Args: map[string]string{StaticBaseArg: base}}); err != nil {
		t.Fatal(err)
	}
	cfg, err := readOCIConfig(out)
	if err != nil {
		t.Fatal(err)
	}
	script, err := BuildInitScript(cfg.Config.Entrypoint, cfg.Config.Cmd, cfg.Config.Env, cfg.Config.WorkingDir)
	if err != nil {
		t.Fatalf("init shim from static config: %v", err)
	}
	if !strings.HasPrefix(script, "#!/bin/sh") || !strings.Contains(script, "mount -t proc proc /proc") {
		t.Fatalf("shim must follow the shared PID-1 conventions: %s", script)
	}
	if !strings.Contains(script, "exec '/bin/busybox' 'httpd'") {
		t.Fatalf("shim must exec the static server: %s", script)
	}
}

func TestBuildStaticOCIArgsAndShortcuts(t *testing.T) {
	ctxDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ctxDir, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctxDir, "dist", "index.html"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := Builder{}
	out := filepath.Join(t.TempDir(), "site.oci.tar")

	// No base arg: explicit guidance naming the arg, never a fake image.
	err := b.BuildWithPlan(context.Background(), Plan{Engine: EngineStatic, ContextDir: ctxDir, OutputOCI: out})
	if err == nil || !strings.Contains(err.Error(), StaticBaseArg) {
		t.Fatalf("missing base must name base_oci, got %v", err)
	}

	// Prebuilt output wins before anything else is touched.
	prebuilt := filepath.Join(t.TempDir(), "prebuilt.tar")
	writeOCILayoutTar(t, prebuilt, false)
	if err := b.BuildWithPlan(context.Background(), Plan{Engine: EngineStatic, ContextDir: t.TempDir(), OutputOCI: prebuilt}); err != nil {
		t.Fatalf("prebuilt OCI tar must be reused untouched: %v", err)
	}

	// Invalid base tar: rejected with the layout expectation.
	badBase := filepath.Join(t.TempDir(), "bad.tar")
	if err := os.WriteFile(badBase, []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = b.BuildWithPlan(context.Background(), Plan{Engine: EngineStatic, ContextDir: ctxDir, OutputOCI: out, Args: map[string]string{StaticBaseArg: badBase}})
	if err == nil || !strings.Contains(err.Error(), "OCI/docker-save") {
		t.Fatalf("bad base must fail naming the layout expectation, got %v", err)
	}

	// Bad port: rejected before any base unpacking.
	goodBase := filepath.Join(t.TempDir(), "good.tar")
	writeStaticBaseTar(t, goodBase, map[string]string{
		"bin/busybox":   "bb",
		"usr/bin/httpd": "!",
	}, `{"config":{"Env":["PATH=/usr/bin"]}}`)
	err = b.BuildWithPlan(context.Background(), Plan{Engine: EngineStatic, ContextDir: ctxDir, OutputOCI: out, Args: map[string]string{StaticBaseArg: goodBase, StaticPortArg: "99999"}})
	if err == nil || !strings.Contains(err.Error(), StaticPortArg) {
		t.Fatalf("bad port must fail naming static_port, got %v", err)
	}
}

func TestBuildStaticOCIEmptyPublishDir(t *testing.T) {
	base := filepath.Join(t.TempDir(), "base.tar")
	writeStaticBaseTar(t, base, map[string]string{
		"bin/busybox":   "bb",
		"usr/bin/httpd": "!",
	}, `{"config":{"Env":["PATH=/usr/bin"]}}`)
	ctxDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(ctxDir, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	b := Builder{}
	err := b.BuildWithPlan(context.Background(), Plan{
		Engine: EngineStatic, ContextDir: ctxDir, OutputOCI: filepath.Join(t.TempDir(), "o.tar"),
		Args: map[string]string{StaticBaseArg: base},
	})
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty publish dir must fail, got %v", err)
	}
}

// TestStaticRealRootfs converts the composed static OCI to an ext4 rootfs.
// Gated: needs Linux, mkfs.ext4, and a staged base tar (PORTER_TEST_STATIC_BASE_OCI);
// output rootfs path goes to PORTER_TEST_ROOTFS_OUT.
func TestStaticRealRootfs(t *testing.T) {
	base := os.Getenv("PORTER_TEST_STATIC_BASE_OCI")
	out := os.Getenv("PORTER_TEST_ROOTFS_OUT")
	if base == "" || out == "" {
		t.Skip("PORTER_TEST_STATIC_BASE_OCI/PORTER_TEST_ROOTFS_OUT not set; skipping real static conversion")
	}
	ctxDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ctxDir, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctxDir, "dist", "index.html"), []byte("<h1>porter static</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	ociOut := filepath.Join(t.TempDir(), "static.oci.tar")
	b := Builder{}
	if err := b.BuildWithPlan(context.Background(), Plan{
		Engine: EngineStatic, ContextDir: ctxDir, OutputOCI: ociOut,
		Args: map[string]string{StaticBaseArg: base},
	}); err != nil {
		t.Fatalf("static build: %v", err)
	}
	res, err := ConvertOCIToExt4(context.Background(), ociOut, out, 256)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	st, err := os.Stat(res.RootfsPath)
	if err != nil || st.Size() == 0 {
		t.Fatalf("rootfs missing/empty: %v %v", res.RootfsPath, err)
	}
	t.Logf("static rootfs=%s bytes=%d cmd=%v", res.RootfsPath, st.Size(), res.Cmd)
}
