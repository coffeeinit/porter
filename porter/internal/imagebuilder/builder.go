package imagebuilder

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Builder struct {
	GuestInitPath string
	KernelPath    string
	WorkDir       string
	Log           func(string)
}

type Result struct {
	RootfsPath string `json:"rootfs_path"`
	KernelPath string `json:"kernel_path"`
	Digest     string `json:"digest"`
	SizeBytes  int64  `json:"size_bytes"`
}

func (b *Builder) logf(format string, args ...interface{}) {
	if b.Log != nil {
		b.Log(fmt.Sprintf(format, args...))
	}
}

func New(guestInit, kernel, workDir string) *Builder {
	if workDir == "" {
		workDir = os.TempDir()
	}
	_ = os.MkdirAll(workDir, 0o755)
	return &Builder{GuestInitPath: guestInit, KernelPath: kernel, WorkDir: workDir}
}

func (b *Builder) BuildDockerfile(ctx context.Context, contextDir, dockerfile, name string, sizeMB int) (Result, error) {
	if name == "" {
		name = "porter-app-" + fmt.Sprint(time.Now().UnixNano())
	}
	if sizeMB < 64 {
		sizeMB = 512
	}
	if err := os.MkdirAll(b.WorkDir, 0o755); err != nil {
		return Result{}, err
	}
	tarPath := filepath.Join(b.WorkDir, name+".tar")
	rootfsPath := filepath.Join(b.WorkDir, name+".ext4")
	b.logf("docker build starting for %s", name)
	args := []string{"build", "-t", name, "-f", dockerfile, contextDir}
	if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		b.logf("%s", string(out))
		return Result{}, fmt.Errorf("docker build: %w: %s", err, strings.TrimSpace(string(out)))
	}
	b.logf("docker build completed for %s", name)
	b.logf("docker save starting for %s", name)
	if out, err := exec.CommandContext(ctx, "docker", "save", "-o", tarPath, name).CombinedOutput(); err != nil {
		b.logf("%s", string(out))
		return Result{}, fmt.Errorf("docker save: %w: %s", err, strings.TrimSpace(string(out)))
	}
	b.logf("docker save completed for %s", name)
	return b.ConvertArchive(ctx, tarPath, rootfsPath, sizeMB)
}

func (b *Builder) ConvertArchive(ctx context.Context, archivePath, rootfsPath string, sizeMB int) (Result, error) {
	if sizeMB < 64 {
		sizeMB = 512
	}
	unpacked, err := os.MkdirTemp(b.WorkDir, "porter-image-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(unpacked)
	if err := extractArchive(archivePath, unpacked); err != nil {
		return Result{}, err
	}
	root := filepath.Join(unpacked, "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return Result{}, err
	}
	manifest, err := readDockerManifest(filepath.Join(unpacked, "manifest.json"))
	if err == nil && len(manifest) > 0 {
		for _, layer := range manifest[0].Layers {
			layerPath := filepath.Join(unpacked, filepath.FromSlash(layer))
			if err := applyLayer(layerPath, root); err != nil {
				return Result{}, err
			}
		}
	} else if _, statErr := os.Stat(filepath.Join(unpacked, "index.json")); statErr == nil {
		if err := applyOCIArchive(unpacked, root); err != nil {
			return Result{}, err
		}
	} else if err := applyImageRoot(unpacked, root); err != nil {
		return Result{}, err
	}
	if err := writeAppEntrypoint(unpacked, root); err != nil {
		return Result{}, err
	}
	if b.GuestInitPath != "" {
		_ = os.MkdirAll(filepath.Join(root, "sbin"), 0o755)
		if err := copyFile(b.GuestInitPath, filepath.Join(root, "sbin", "porter-guest-init"), 0o755); err != nil {
			return Result{}, fmt.Errorf("inject guest init: %w", err)
		}
	}
	b.logf("unpacked image layers")
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
		return Result{}, err
	}
	f, err := os.OpenFile(rootfsPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o640)
	if err != nil {
		return Result{}, err
	}
	if err := f.Truncate(int64(sizeMB) * 1024 * 1024); err != nil {
		f.Close()
		return Result{}, err
	}
	f.Close()
	b.logf("creating ext4 rootfs (%d MiB)", sizeMB)
	if out, err := exec.CommandContext(ctx, "mkfs.ext4", "-F", "-d", root, rootfsPath).CombinedOutput(); err != nil {
		b.logf("%s", string(out))
		return Result{}, fmt.Errorf("mkfs.ext4: %w: %s", err, strings.TrimSpace(string(out)))
	}
	digest, size, err := fileDigest(rootfsPath)
	if err != nil {
		return Result{}, err
	}
	b.logf("image ready: digest=%s size=%d", digest, size)
	return Result{RootfsPath: rootfsPath, KernelPath: b.KernelPath, Digest: digest, SizeBytes: size}, nil
}

type dockerManifest []struct {
	Config string   `json:"Config"`
	Layers []string `json:"Layers"`
}

func readDockerManifest(path string) (dockerManifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m dockerManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func extractArchive(path, dest string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(h.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			continue
		}
		target := filepath.Join(dest, name)
		switch h.Typeflag {
		case tar.TypeDir:
			_ = os.MkdirAll(target, 0o755)
		case tar.TypeReg:
			_ = os.MkdirAll(filepath.Dir(target), 0o755)
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(h.Mode)&0o777)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, tr)
			out.Close()
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func applyLayer(path, root string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var r io.Reader = f
	head := make([]byte, 2)
	n, _ := f.Read(head)
	_, _ = f.Seek(0, io.SeekStart)
	if strings.HasSuffix(path, ".gz") || (n == 2 && head[0] == 0x1f && head[1] == 0x8b) {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
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
		name := filepath.Clean(h.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			continue
		}
		base := filepath.Base(name)
		if strings.HasPrefix(base, ".wh.") {
			target := filepath.Join(root, filepath.Dir(name), strings.TrimPrefix(base, ".wh."))
			_ = os.RemoveAll(target)
			continue
		}
		target := filepath.Join(root, name)
		switch h.Typeflag {
		case tar.TypeDir:
			_ = os.MkdirAll(target, 0o755)
		case tar.TypeReg:
			_ = os.MkdirAll(filepath.Dir(target), 0o755)
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(h.Mode)&0o777)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, tr)
			out.Close()
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			_ = os.MkdirAll(filepath.Dir(target), 0o755)
			_ = os.Symlink(h.Linkname, target)
		}
	}
}

func applyImageRoot(unpacked, root string) error {
	entries, _ := os.ReadDir(unpacked)
	for _, e := range entries {
		if e.Name() == "manifest.json" || strings.HasSuffix(e.Name(), ".tar") || strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		src := filepath.Join(unpacked, e.Name())
		dst := filepath.Join(root, e.Name())
		if e.IsDir() {
			_ = copyDir(src, dst)
		}
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, mode)
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target, info.Mode())
	})
}

func fileDigest(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), n, nil
}

func (b *Builder) BuildDockerImage(ctx context.Context, ref, name string, sizeMB int) (Result, error) {
	if name == "" {
		name = "porter-app-" + fmt.Sprint(time.Now().UnixNano())
	}
	if sizeMB < 64 {
		sizeMB = 512
	}
	if err := os.MkdirAll(b.WorkDir, 0o755); err != nil {
		return Result{}, err
	}
	if out, err := exec.CommandContext(ctx, "docker", "pull", ref).CombinedOutput(); err != nil {
		return Result{}, fmt.Errorf("docker pull: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.CommandContext(ctx, "docker", "tag", ref, name).CombinedOutput(); err != nil {
		return Result{}, fmt.Errorf("docker tag: %w: %s", err, strings.TrimSpace(string(out)))
	}
	tarPath := filepath.Join(b.WorkDir, name+".tar")
	rootfsPath := filepath.Join(b.WorkDir, name+".ext4")
	if out, err := exec.CommandContext(ctx, "docker", "save", "-o", tarPath, name).CombinedOutput(); err != nil {
		return Result{}, fmt.Errorf("docker save: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return b.ConvertArchive(ctx, tarPath, rootfsPath, sizeMB)
}

func applyOCIArchive(unpacked, root string) error {
	indexBytes, err := os.ReadFile(filepath.Join(unpacked, "index.json"))
	if err != nil {
		return err
	}
	var index struct {
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(indexBytes, &index); err != nil {
		return err
	}
	if len(index.Manifests) == 0 {
		return fmt.Errorf("empty OCI index")
	}
	manifestBytes, err := readBlob(unpacked, index.Manifests[0].Digest)
	if err != nil {
		return err
	}
	var manifest struct {
		Layers []struct {
			Digest string `json:"digest"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return err
	}
	for _, layer := range manifest.Layers {
		layerPath, err := blobPath(unpacked, layer.Digest)
		if err != nil {
			return err
		}
		if err := applyLayer(layerPath, root); err != nil {
			return err
		}
	}
	return nil
}

func blobPath(unpacked, digest string) (string, error) {
	hex := strings.TrimPrefix(digest, "sha256:")
	return filepath.Join(unpacked, "blobs", "sha256", hex), nil
}

func readBlob(unpacked, digest string) ([]byte, error) {
	p, err := blobPath(unpacked, digest)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

type dockerConfig struct {
	Config struct {
		Entrypoint []string `json:"Entrypoint"`
		Cmd        []string `json:"Cmd"`
		Env        []string `json:"Env"`
		WorkingDir string   `json:"WorkingDir"`
	} `json:"config"`
}

func writeAppEntrypoint(unpacked, root string) error {
	cfg, err := loadImageConfig(unpacked)
	if err != nil || len(cfg.Config.Entrypoint)+len(cfg.Config.Cmd) == 0 {
		return nil
	}
	args := append([]string{}, cfg.Config.Entrypoint...)
	args = append(args, cfg.Config.Cmd...)
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	if cfg.Config.WorkingDir != "" {
		b.WriteString("cd " + shellQuote(cfg.Config.WorkingDir) + " || true\n")
	}
	for _, e := range cfg.Config.Env {
		b.WriteString("export " + e + "\n")
	}
	b.WriteString("exec")
	for _, a := range args {
		b.WriteString(" " + shellQuote(a))
	}
	b.WriteString("\n")
	_ = os.MkdirAll(filepath.Join(root, "porter"), 0o755)
	return os.WriteFile(filepath.Join(root, "porter", "entrypoint"), []byte(b.String()), 0o755)
}

func loadImageConfig(unpacked string) (dockerConfig, error) {
	var cfg dockerConfig
	if b, err := os.ReadFile(filepath.Join(unpacked, "manifest.json")); err == nil {
		var m dockerManifest
		if json.Unmarshal(b, &m) == nil && len(m) > 0 {
			c, err := os.ReadFile(filepath.Join(unpacked, filepath.FromSlash(m[0].Config)))
			if err != nil {
				return cfg, err
			}
			return cfg, json.Unmarshal(c, &cfg)
		}
	}
	idx, err := os.ReadFile(filepath.Join(unpacked, "index.json"))
	if err != nil {
		return cfg, err
	}
	var index struct {
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(idx, &index); err != nil {
		return cfg, err
	}
	if len(index.Manifests) == 0 {
		return cfg, fmt.Errorf("empty OCI index")
	}
	mb, err := readBlob(unpacked, index.Manifests[0].Digest)
	if err != nil {
		return cfg, err
	}
	var man struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if err := json.Unmarshal(mb, &man); err != nil {
		return cfg, err
	}
	cb, err := readBlob(unpacked, man.Config.Digest)
	if err != nil {
		return cfg, err
	}
	return cfg, json.Unmarshal(cb, &cfg)
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
