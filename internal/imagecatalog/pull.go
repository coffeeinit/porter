// Pulling catalog images into the local cache.
//
// Porter ships no guest images. ImagesDir holds only what has actually been
// used: pulling an entry downloads its artifacts, verifies each against the
// digest the catalog pinned, and writes the local manifest the rest of the
// system already understands. Nothing downstream learns about URLs — by the
// time a microVM boots, the image looks exactly like one registered with
// `porter image add`.
package imagecatalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"porter/internal/types"
)

// pullHTTPTimeout bounds a single artifact download. Guest images are large and
// operators may be on a slow link, so this is generous rather than tight.
const pullHTTPTimeout = 30 * time.Minute

// maxArtifactBytes caps a download (16 GiB) so a wrong URL cannot fill the disk.
const maxArtifactBytes = 16 << 30

// Pull materialises a catalog entry into dir and returns the resulting local
// manifest (Status "ready", Rootfs/Kernel pointing at the downloaded files).
//
// A file that is already present with the pinned digest is reused, so a second
// pull is free. Downloads land in a .part file and are renamed only after the
// digest matches, so an interrupted pull can never be mistaken for a good image.
func Pull(ctx context.Context, client *http.Client, m types.ImageManifest, dir string) (types.ImageManifest, error) {
	if dir == "" {
		return m, fmt.Errorf("pull %s: no image directory configured", m.Name)
	}
	if m.RootfsURL == "" {
		return m, fmt.Errorf("pull %s: entry has no rootfs_url", m.Name)
	}
	if client == nil {
		client = &http.Client{Timeout: pullHTTPTimeout}
	}

	name := safeName(m.Name)
	if name == "" {
		return m, fmt.Errorf("pull: entry name %q is not a usable directory name", m.Name)
	}
	artDir := filepath.Join(dir, name)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		return m, fmt.Errorf("pull %s: %w", m.Name, err)
	}

	rootfsPath := filepath.Join(artDir, "rootfs.ext4")
	if err := fetchVerified(ctx, client, m.RootfsURL, rootfsPath, m.RootfsSHA256, m.RootfsSize); err != nil {
		return m, fmt.Errorf("pull %s rootfs: %w", m.Name, err)
	}

	kernelPath := ""
	if m.KernelURL != "" {
		kernelPath = filepath.Join(artDir, "vmlinux")
		if err := fetchVerified(ctx, client, m.KernelURL, kernelPath, m.KernelSHA256, m.KernelSize); err != nil {
			return m, fmt.Errorf("pull %s kernel: %w", m.Name, err)
		}
	}

	local := m
	local.Rootfs = rootfsPath
	local.Kernel = kernelPath
	local.RootfsURL = ""
	local.KernelURL = ""
	local.Status = "ready"
	local.Type = "custom"

	// Re-validate through the same gate `porter image add` uses, so a pulled
	// image cannot be weaker than a locally registered one.
	if report, err := ValidateArtifacts(local.Rootfs, local.Kernel); err != nil {
		return m, fmt.Errorf("pull %s: downloaded artifacts failed validation: %w", m.Name, err)
	} else {
		local.Architecture = report.Architecture
		local.RootfsSHA256 = report.RootfsSHA256
		local.KernelSHA256 = report.KernelSHA256
	}

	if err := writeLocalManifest(dir, name, local); err != nil {
		return m, fmt.Errorf("pull %s: register local manifest: %w", m.Name, err)
	}
	log.Printf("imagecatalog: pulled image %s (%s)", m.Name, artDir)
	return local, nil
}

// writeLocalManifest writes <dir>/<name>.json, the file the catalog scan reads.
// It goes out through a temp file so a reader never sees a half-written
// manifest.
func writeLocalManifest(dir, name string, m types.ImageManifest) error {
	blob, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, name+".json.tmp")
	if err := os.WriteFile(tmp, append(blob, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, name+".json"))
}

// fetchVerified ensures dest holds the content at url with the expected digest.
// An existing matching file short-circuits the download; an existing file with
// the wrong digest is replaced.
func fetchVerified(ctx context.Context, client *http.Client, url, dest, wantSHA string, wantSize int64) error {
	if wantSHA == "" {
		return fmt.Errorf("refusing %s: catalog entry carries no sha256 digest", url)
	}
	if ok, err := fileMatches(dest, wantSHA); err != nil {
		return err
	} else if ok {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	part := dest + ".part"
	defer os.Remove(part)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "porter-imagecatalog")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}

	out, err := os.Create(part)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(resp.Body, maxArtifactBytes+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	if n > maxArtifactBytes {
		return fmt.Errorf("download %s: exceeds the %d byte limit", url, int64(maxArtifactBytes))
	}

	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, wantSHA) {
		return fmt.Errorf("digest mismatch for %s: catalog says %s, downloaded %s", url, wantSHA, got)
	}
	if wantSize > 0 && n != wantSize {
		return fmt.Errorf("size mismatch for %s: catalog says %d bytes, downloaded %d", url, wantSize, n)
	}
	return os.Rename(part, dest)
}

// fileMatches reports whether path already holds size bytes hashing to wantSHA.
// A missing file is not an error — it simply needs downloading.
func fileMatches(path, wantSHA string) (bool, error) {
	if path == "" || wantSHA == "" {
		return false, nil
	}
	_, got, err := inspectFile(path, filepath.Base(path))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, nil
	}
	return strings.EqualFold(got, wantSHA), nil
}

// safeName reduces a catalog entry name to something usable as a directory
// name, refusing anything that could escape the images directory.
func safeName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\\", "-")
	name = strings.ReplaceAll(name, "/", "-")
	name = strings.Trim(name, ".")
	if name == "" || name == "." || name == ".." || strings.Contains(name, "..") {
		return ""
	}
	return name
}
