// Package buildkit builds Dockerfiles through BuildKit and converts the
// resulting OCI image layout into a Firecracker-compatible ext4 rootfs.
package buildkit

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"porter/internal/guestsetup"
)

type Builder struct {
	Bin         string
	Addr        string
	NixpacksBin string
	RailpackBin string
	PackBin     string // buildpacks `pack` CLI; empty = auto PATH
	// Args are non-secret build args (--build-arg dockerfile, --env packs).
	Args map[string]string
	// Secrets are build secrets, Dockerfile-engine only (buildkit --secret
	// id=,src= temp files, never argv). Packs with Secrets set fail
	// explicitly (their --env would leak values via ps).
	Secrets map[string]string
	// CacheFrom/To wire buildkit layer caching (registry refs).
	CacheFrom string
	CacheTo   string
}

// PaketoBuilder is the default buildpacks builder (buildpacks parity).
const PaketoBuilder = "paketobuildpacks/builder:base"

type Result struct {
	OCIPath     string
	RootfsPath  string
	Entrypoint  []string
	Cmd         []string
	Env         []string
	WorkingDir  string
	ImageDigest string
}

type ociIndex struct {
	Manifests []struct {
		Digest string `json:"digest"`
	} `json:"manifests"`
}
type ociManifest struct {
	Config struct {
		Digest string `json:"digest"`
	} `json:"config"`
	Layers []struct {
		Digest    string `json:"digest"`
		MediaType string `json:"mediaType"`
	} `json:"layers"`
}
type imageConfig struct {
	Config struct {
		Entrypoint []string `json:"Entrypoint"`
		Cmd        []string `json:"Cmd"`
		Env        []string `json:"Env"`
		WorkingDir string   `json:"WorkingDir"`
	} `json:"config"`
}

// BuildWithPlan executes the detected engine. Dockerfile uses BuildKit;
// nixpacks/railpack shell out to their binaries (plan → OCI);
// buildpacks shells to `pack` (needs builder + docker for export);
// static packs a publish dir; buildpacks needs a runner. Missing runners
// return an explicit error guiding to Dockerfile/prebuilt OCI (never fake
// an image).
func (b Builder) BuildWithPlan(ctx context.Context, p Plan) error {
	args, secrets := p.Args, p.Secrets
	if args == nil {
		args = b.Args
	}
	if secrets == nil {
		secrets = b.Secrets
	}
	switch p.Engine {
	case EngineDockerfile:
		if p.Dockerfile == "" {
			return fmt.Errorf("buildkit: dockerfile engine but no Dockerfile in %s", p.ContextDir)
		}
		return b.BuildWith(ctx, p.ContextDir, p.Dockerfile, p.OutputOCI, args, secrets)
	case EngineNixpacks:
		return runPackEnv(ctx, firstNonEmpty(b.NixpacksBin, "nixpacks"), []string{"build", "--format", "oci", p.ContextDir, "--out", p.OutputOCI}, p.OutputOCI, args, secrets)
	case EngineRailpack:
		return runPackEnv(ctx, firstNonEmpty(b.RailpackBin, "railpack"), []string{"build", "--format", "oci", p.ContextDir, "--out", p.OutputOCI}, p.OutputOCI, args, secrets)
	case EngineBuildpacks:
		return b.runPackBuildpacks(ctx, p, args, secrets)
	case EngineStatic:
		return b.buildStatic(ctx, p, args)
	default:
		return fmt.Errorf("buildkit: unknown engine %q for %s", p.Engine, p.ContextDir)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// runPack runs an external pack binary; a usable prebuilt OCI tar after the
// run still counts as success (daemon flaked but artifact exists).
func runPack(ctx context.Context, bin string, args []string, outputOCI string) error {
	return runPackEnv(ctx, bin, args, outputOCI, nil, nil)
}

// runPackEnv runs a pack binary with --env KEY=VAL build args. Secrets are
// refused here (pack --env would leak values via ps); use the Dockerfile
// engine for secret builds.
func runPackEnv(ctx context.Context, bin string, args []string, outputOCI string, env, secrets map[string]string) error {
	if len(secrets) > 0 {
		return fmt.Errorf("buildkit: %s pack engine cannot take secrets (would leak via ps); use the Dockerfile engine", bin)
	}
	full := append([]string{}, args...)
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		full = append(full, "--env", k+"="+env[k])
	}
	cmd := exec.CommandContext(ctx, bin, full...)
	if out, err := cmd.CombinedOutput(); err != nil {
		if validOCITar(outputOCI) {
			return nil
		}
		return fmt.Errorf("buildkit: %s: %w: %s", bin, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// runPackBuildpacks builds via the `pack` CLI (Paketo
// parity), then exports with `docker save` when a docker daemon is present.
// Without docker there is no OCI tar to continue with, so it fails with
// guidance instead of faking an artifact.
func (b Builder) runPackBuildpacks(ctx context.Context, p Plan, args, secrets map[string]string) error {
	if len(secrets) > 0 {
		return fmt.Errorf("buildkit: buildpacks engine cannot take secrets; use the Dockerfile engine")
	}
	packBin := firstNonEmpty(b.PackBin, "pack")
	tag := fmt.Sprintf("porter-build-%d", time.Now().UnixNano())
	pargs := []string{"build", tag, "--builder", PaketoBuilder, "--path", p.ContextDir, "--pull-policy", "if-not-present"}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		pargs = append(pargs, "--env", k+"="+args[k])
	}
	if out, err := exec.CommandContext(ctx, packBin, pargs...).CombinedOutput(); err != nil {
		if validOCITar(p.OutputOCI) {
			return nil
		}
		return fmt.Errorf("buildkit: pack: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("buildkit: pack built %s but no docker daemon to export it; install docker or provide a Dockerfile/prebuilt OCI tar (want %s)", tag, p.OutputOCI)
	}
	if out, err := exec.CommandContext(ctx, "docker", "save", "-o", p.OutputOCI, tag).CombinedOutput(); err != nil {
		return fmt.Errorf("buildkit: docker save: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// EnsureOCI builds via BuildKit, falling back to a prebuilt OCI tar when the
// daemon/binary is unavailable (prebuilt-image parity).
// If outputOCI already holds a valid OCI layout (index.json present) it is
// reused as-is so air-gapped / CI-built images never rebuild.
func (b Builder) EnsureOCI(ctx context.Context, contextDir, dockerfile, outputOCI string) error {
	if outputOCI != "" && validOCITar(outputOCI) {
		return nil
	}
	if err := b.Build(ctx, contextDir, dockerfile, outputOCI); err != nil {
		if outputOCI != "" && validOCITar(outputOCI) {
			return nil // daemon flaked but a usable artifact exists
		}
		return err
	}
	return nil
}

// validOCITar reports whether path looks like a usable image tar: a BuildKit
// OCI layout (index.json) or a `docker save` archive (manifest.json, which is
// what the buildpacks engine exports via `docker save`). Either layout feeds
// ConvertOCIToExt4, so either counts as a reusable prebuilt artifact.
// ValidOCITar reports whether path is a usable prebuilt image tar (OCI layout
// or docker-save archive). Exported for the /images/custom/oci upload route,
// which must reject non-image uploads before paying for a conversion.
func ValidOCITar(path string) bool { return validOCITar(path) }

func validOCITar(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		h, e := tr.Next()
		if e == io.EOF {
			return false
		}
		if e != nil {
			return false
		}
		switch h.Name {
		case "index.json", "./index.json", "manifest.json", "./manifest.json":
			return true
		}
	}
}

func (b Builder) Build(ctx context.Context, contextDir, dockerfile, outputOCI string) error {
	return b.BuildWith(ctx, contextDir, dockerfile, outputOCI, b.Args, b.Secrets)
}

// BuildWith runs a Dockerfile build with args, secrets (temp files, never
// argv), and optional layer-cache endpoints.
func (b Builder) BuildWith(ctx context.Context, contextDir, dockerfile, outputOCI string, args, secrets map[string]string) error {
	if b.Bin == "" {
		b.Bin = "buildctl"
	}
	if b.Addr == "" {
		b.Addr = "unix:///run/buildkit/buildkitd.sock"
	}
	if contextDir == "" || dockerfile == "" || outputOCI == "" {
		return fmt.Errorf("buildkit: context, dockerfile, and output are required")
	}
	if err := os.MkdirAll(filepath.Dir(outputOCI), 0o750); err != nil {
		return err
	}
	argv := []string{"--addr", b.Addr, "build", "--frontend", "dockerfile.v0", "--local", "context=" + contextDir, "--local", "dockerfile=" + filepath.Dir(dockerfile), "--opt", "filename=" + filepath.Base(dockerfile), "--output", "type=oci,dest=" + outputOCI}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		argv = append(argv, "--opt", "build-arg:"+k+"="+args[k])
	}
	if b.CacheFrom != "" {
		argv = append(argv, "--opt", "import-cache="+b.CacheFrom)
	}
	if b.CacheTo != "" {
		argv = append(argv, "--opt", "export-cache="+b.CacheTo)
	}
	var secretFiles []string
	defer func() {
		for _, f := range secretFiles {
			_ = os.Remove(f)
		}
	}()
	skeys := make([]string, 0, len(secrets))
	for k := range secrets {
		skeys = append(skeys, k)
	}
	sort.Strings(skeys)
	for _, k := range skeys {
		f, err := os.CreateTemp("", "porter-build-secret-")
		if err != nil {
			return err
		}
		secretFiles = append(secretFiles, f.Name())
		if _, err := f.WriteString(secrets[k]); err != nil {
			f.Close()
			return err
		}
		f.Close()
		if err := os.Chmod(f.Name(), 0o600); err != nil {
			return err
		}
		argv = append(argv, "--secret", "id="+k+",src="+f.Name())
	}
	cmd := exec.CommandContext(ctx, b.Bin, argv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("buildkit build: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func ConvertOCIToExt4(ctx context.Context, ociPath, rootfsPath string, sizeMiB int) (Result, error) {
	if sizeMiB < 64 {
		sizeMiB = 512
	}
	root, err := os.MkdirTemp("", "porter-oci-root-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(root)
	if err := unpackOCI(ociPath, root); err != nil {
		return Result{}, err
	}
	cfg, cfgErr := readOCIConfig(ociPath)
	if cfgErr == nil {
		// Images without ENTRYPOINT/CMD (plain OS bases) keep their own /sbin/init.
		if script, err := BuildInitScript(cfg.Config.Entrypoint, cfg.Config.Cmd, cfg.Config.Env, cfg.Config.WorkingDir); err == nil {
			if err := InstallInit(root, script); err != nil {
				return Result{}, fmt.Errorf("install init shim: %w", err)
			}
		}
		// Idempotent first-boot setup (stamp-guarded, standard profile): DHCP
		// skip when static, nocloud datasource, serial console. Runs under the
		// init shim on first boot; reruns are no-ops by stamp file.
		setup := guestsetup.SetupScript(guestsetup.SetupOptions{Profile: guestsetup.ProfileStandard})
		if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
			return Result{}, fmt.Errorf("install setup script: %w", err)
		}
		if err := os.WriteFile(filepath.Join(root, "etc", "porter-setup.sh"), []byte(setup), 0o755); err != nil {
			return Result{}, fmt.Errorf("install setup script: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o750); err != nil {
		return Result{}, err
	}
	f, err := os.OpenFile(rootfsPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o640)
	if err != nil {
		return Result{}, err
	}
	if err := f.Truncate(int64(sizeMiB) * 1024 * 1024); err != nil {
		f.Close()
		return Result{}, err
	}
	f.Close()
	if _, err := exec.LookPath("mkfs.ext4"); err != nil {
		return Result{}, fmt.Errorf("mkfs.ext4 not found: rootfs formatting requires Linux with e2fsprogs (unpack to %s already staged, image build must run on Linux)", root)
	}
	cmd := exec.CommandContext(ctx, "mkfs.ext4", "-F", "-d", root, rootfsPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return Result{}, fmt.Errorf("mkfs.ext4: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if cfgErr != nil {
		return Result{RootfsPath: rootfsPath}, cfgErr
	}
	return Result{OCIPath: ociPath, RootfsPath: rootfsPath, Entrypoint: cfg.Config.Entrypoint, Cmd: cfg.Config.Cmd, Env: cfg.Config.Env, WorkingDir: cfg.Config.WorkingDir}, nil
}

func unpackOCI(path, root string) error {
	// Scan for index.json anywhere: real buildx tars list blobs/ first, so no
	// first-entry assumption (that strict check rejected valid OCI layouts).
	if indexBytes, err := readTarEntry(path, "index.json"); err == nil {
		return unpackOCILayout(path, root, indexBytes)
	}
	// No OCI layout: accept the `docker save` archive the buildpacks engine
	// exports (manifest.json + <layer>/layer.tar entries) so pack-built
	// images convert instead of failing with "missing index.json".
	if _, err := readTarEntry(path, "manifest.json"); err == nil {
		return unpackDockerSave(path, root)
	}
	return fmt.Errorf("oci archive missing index.json")
}

func unpackOCILayout(path, root string, indexBytes []byte) error {
	var index ociIndex
	if err := json.Unmarshal(indexBytes, &index); err != nil || len(index.Manifests) == 0 {
		return fmt.Errorf("invalid OCI index")
	}
	manifestBytes, err := readBlob(path, index.Manifests[0].Digest)
	if err != nil {
		return err
	}
	var manifest ociManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return err
	}
	for _, layer := range manifest.Layers {
		data, err := readBlob(path, layer.Digest)
		if err != nil {
			return err
		}
		if err := applyLayer(data, root); err != nil {
			return err
		}
	}
	return nil
}

// dockerSaveManifest mirrors the top-level manifest.json of a `docker save`
// archive: one entry per tagged image with its config file and layer tars.
type dockerSaveManifest []struct {
	Config string   `json:"Config"`
	Layers []string `json:"Layers"`
}

func unpackDockerSave(path, root string) error {
	raw, err := readTarEntry(path, "manifest.json")
	if err != nil {
		return fmt.Errorf("invalid docker-save manifest: %w", err)
	}
	var manifests dockerSaveManifest
	if err := json.Unmarshal(raw, &manifests); err != nil || len(manifests) == 0 || len(manifests[0].Layers) == 0 {
		return fmt.Errorf("invalid docker-save manifest")
	}
	for _, layer := range manifests[0].Layers {
		data, err := readTarEntry(path, layer)
		if err != nil {
			return err
		}
		if err := applyLayer(data, root); err != nil {
			return err
		}
	}
	return nil
}

func readOCIConfig(path string) (imageConfig, error) {
	if idx, err := readTarEntry(path, "index.json"); err == nil {
		var i ociIndex
		if err := json.Unmarshal(idx, &i); err != nil {
			return imageConfig{}, err
		}
		if len(i.Manifests) == 0 {
			return imageConfig{}, fmt.Errorf("invalid OCI index")
		}
		m, err := readBlob(path, i.Manifests[0].Digest)
		if err != nil {
			return imageConfig{}, err
		}
		var man ociManifest
		if err := json.Unmarshal(m, &man); err != nil {
			return imageConfig{}, err
		}
		c, err := readBlob(path, man.Config.Digest)
		if err != nil {
			return imageConfig{}, err
		}
		var cfg imageConfig
		return cfg, json.Unmarshal(c, &cfg)
	}
	// docker-save fallback: manifest.json points at the image config file,
	// which carries the same "config" (Entrypoint/Cmd/Env/WorkingDir) shape.
	raw, err := readTarEntry(path, "manifest.json")
	if err != nil {
		return imageConfig{}, err
	}
	var manifests dockerSaveManifest
	if err := json.Unmarshal(raw, &manifests); err != nil || len(manifests) == 0 || manifests[0].Config == "" {
		return imageConfig{}, fmt.Errorf("invalid docker-save manifest")
	}
	c, err := readTarEntry(path, manifests[0].Config)
	if err != nil {
		return imageConfig{}, err
	}
	var cfg imageConfig
	return cfg, json.Unmarshal(c, &cfg)
}

// readTarEntry returns the bytes of the first tar entry matching one of
// names, tolerating the leading "./" prefix some writers emit.
func readTarEntry(path string, names ...string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
		want["./"+n] = true
	}
	tr := tar.NewReader(f)
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		if want[h.Name] {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("archive entry %s not found", names[0])
}

func readBlob(path, digest string) ([]byte, error) {
	want := "blobs/" + strings.Replace(digest, ":", "/", 1)
	data, err := readTarEntry(path, want)
	if err != nil {
		return nil, fmt.Errorf("OCI blob %s not found", digest)
	}
	return data, nil
}

func applyLayer(data []byte, root string) error {
	var r io.Reader = bytes.NewReader(data)
	if gz, err := gzip.NewReader(bytes.NewReader(data)); err == nil {
		defer gz.Close()
		r = gz
	}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		rel := filepath.Clean(h.Name)
		if rel == "." || strings.HasPrefix(rel, "../") {
			continue
		}
		dst := filepath.Join(root, rel)
		base := filepath.Base(rel)
		if strings.HasPrefix(base, ".wh.") {
			if base == ".wh..wh..opq" {
				entries, _ := os.ReadDir(filepath.Dir(dst))
				for _, e := range entries {
					_ = os.RemoveAll(filepath.Join(filepath.Dir(dst), e.Name()))
				}
			} else {
				_ = os.RemoveAll(filepath.Join(filepath.Dir(dst), strings.TrimPrefix(base, ".wh.")))
			}
			continue
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, os.FileMode(h.Mode)); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode))
			if err != nil {
				return err
			}
			_, err = io.Copy(out, tr)
			out.Close()
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			_ = os.RemoveAll(dst)
			if err := os.Symlink(h.Linkname, dst); err != nil {
				return err
			}
		}
	}
}
