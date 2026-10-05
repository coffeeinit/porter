// Remote image catalog.
//
// Porter ships no guest images. ImagesDir is a local cache, and the set of
// images an operator can deploy comes from a small JSON document published in a
// separate repository (typically GitHub raw):
//
//	{
//	  "version": 1,
//	  "updated_at": "2026-10-05T00:00:00Z",
//	  "images": [
//	    { "name": "alpine", "image": "base://alpine",
//	      "description": "Alpine 3.21 minimal microVM",
//	      "architecture": "x86_64", "vcpus": 1, "mem_mib": 256,
//	      "tags": ["alpine", "minimal"],
//	      "rootfs_url": "https://…/alpine-3.21.ext4",
//	      "rootfs_sha256": "<64 hex>", "rootfs_size": 268435456,
//	      "kernel_url": "https://…/vmlinux",
//	      "kernel_sha256": "<64 hex>", "kernel_size": 21450848 }
//	  ]
//	}
//
// Updating an image upstream is an edit to that JSON — the operator does not
// re-download Porter to get new images, and only the changed document is
// fetched, not the artifacts.
//
// The fetch is lazy and cached: the catalog is served from disk while fresh,
// re-fetched when stale, and a failed fetch falls back to the stale copy so a
// GitHub outage never breaks the dashboard.
package imagecatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"porter/internal/types"
)

// CatalogFile is the cached copy of the upstream document inside ImagesDir.
// The leading dot keeps it out of the *.json image-manifest scan.
const CatalogFile = ".catalog.json"

// DefaultCatalogTTL is how long a fetched catalog stays fresh.
const DefaultCatalogTTL = 24 * time.Hour

// maxCatalogBytes bounds the document so a wrong URL cannot fill the disk.
const maxCatalogBytes = 4 << 20 // 4 MiB

// catalogCache is the on-disk shape: the upstream document plus when we read it.
// Keeping fetched_at here means the TTL survives a restart.
type catalogCache struct {
	Version   int                   `json:"version"`
	UpdatedAt string                `json:"updated_at,omitempty"`
	FetchedAt time.Time             `json:"fetched_at"`
	SourceURL string                `json:"source_url"`
	Images    []types.ImageManifest `json:"images"`
}

// RemoteCatalog serves the upstream image list with a local cache.
type RemoteCatalog struct {
	url    string
	ttl    time.Duration
	dir    string
	client *http.Client

	mu      sync.Mutex
	loaded  bool
	entries []types.ImageManifest
	updated string
	fetched time.Time
	lastErr error

	now func() time.Time
}

// NewRemote builds a catalog reader. A blank url disables remote images.
func NewRemote(url string, ttl time.Duration, dir string, client *http.Client) *RemoteCatalog {
	if ttl <= 0 {
		ttl = DefaultCatalogTTL
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &RemoteCatalog{url: strings.TrimSpace(url), ttl: ttl, dir: dir, client: client, now: time.Now}
}

// Enabled reports whether an upstream catalog is configured.
func (r *RemoteCatalog) Enabled() bool { return r != nil && r.url != "" }

// URL returns the configured catalog location (may be empty).
func (r *RemoteCatalog) URL() string {
	if r == nil {
		return ""
	}
	return r.url
}

// FetchedAt reports when the served copy was retrieved (zero when never).
func (r *RemoteCatalog) FetchedAt() time.Time {
	if r == nil {
		return time.Time{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fetched
}

// UpdatedAt is the upstream document's own timestamp, used to show operators
// how current the catalog is.
func (r *RemoteCatalog) UpdatedAt() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.updated
}

// LastError reports the most recent fetch failure, if any. A non-nil error with
// entries present means the served list is stale.
func (r *RemoteCatalog) LastError() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastErr
}

// Entries returns the catalog, refreshing it when the cached copy is stale.
// It never blocks the caller on a failing upstream: on error the previous
// entries are served and the error is available via LastError.
func (r *RemoteCatalog) Entries(ctx context.Context) []types.ImageManifest {
	if !r.Enabled() {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.loaded {
		r.loadLocked()
	}
	if r.loaded && len(r.entries) > 0 && r.now().Sub(r.fetched) < r.ttl {
		return r.entries
	}
	if err := r.refreshLocked(ctx); err != nil {
		// Serve the stale copy rather than emptying the marketplace.
		return r.entries
	}
	return r.entries
}

// Refresh forces a fetch regardless of the TTL.
func (r *RemoteCatalog) Refresh(ctx context.Context) error {
	if !r.Enabled() {
		return fmt.Errorf("imagecatalog: no catalog URL configured")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.loaded {
		r.loadLocked()
	}
	return r.refreshLocked(ctx)
}

// loadLocked reads the cached document from disk. A missing or unreadable cache
// is not an error: the first refresh simply has nothing to fall back to.
func (r *RemoteCatalog) loadLocked() {
	r.loaded = true
	if r.dir == "" {
		return
	}
	raw, err := os.ReadFile(filepath.Join(r.dir, CatalogFile))
	if err != nil {
		return
	}
	var c catalogCache
	if err := json.Unmarshal(raw, &c); err != nil {
		log.Printf("imagecatalog: cached catalog %s is unreadable (%v); will refetch", CatalogFile, err)
		return
	}
	r.entries = validEntries(c.Images)
	r.updated = c.UpdatedAt
	r.fetched = c.FetchedAt
}

// refreshLocked fetches the document, validates it, and replaces the cache.
func (r *RemoteCatalog) refreshLocked(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.url, nil)
	if err != nil {
		r.lastErr = err
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "porter-imagecatalog")

	resp, err := r.client.Do(req)
	if err != nil {
		r.lastErr = fmt.Errorf("fetch catalog: %w", err)
		log.Printf("imagecatalog: %v (serving %d cached entries)", r.lastErr, len(r.entries))
		return r.lastErr
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		r.lastErr = fmt.Errorf("fetch catalog: %s", resp.Status)
		log.Printf("imagecatalog: %v (serving %d cached entries)", r.lastErr, len(r.entries))
		return r.lastErr
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBytes+1))
	if err != nil {
		r.lastErr = fmt.Errorf("read catalog: %w", err)
		return r.lastErr
	}
	if len(body) > maxCatalogBytes {
		r.lastErr = fmt.Errorf("catalog exceeds %d bytes", maxCatalogBytes)
		return r.lastErr
	}

	var c catalogCache
	if err := json.Unmarshal(body, &c); err != nil {
		r.lastErr = fmt.Errorf("parse catalog: %w", err)
		return r.lastErr
	}

	entries := validEntries(c.Images)
	if len(entries) == 0 {
		// An empty publish is almost always a mistake; keep what we had.
		r.lastErr = fmt.Errorf("catalog contains no usable images")
		return r.lastErr
	}

	r.entries = entries
	r.updated = c.UpdatedAt
	r.fetched = r.now()
	r.lastErr = nil
	r.writeCacheLocked(c.UpdatedAt)
	log.Printf("imagecatalog: catalog refreshed from %s (%d images)", r.url, len(entries))
	return nil
}

// writeCacheLocked persists the freshly fetched list so the TTL survives a
// restart. A write failure is logged, never fatal.
func (r *RemoteCatalog) writeCacheLocked(updated string) {
	if r.dir == "" {
		return
	}
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		log.Printf("imagecatalog: create cache dir: %v", err)
		return
	}
	blob, err := json.MarshalIndent(catalogCache{
		Version: 1, UpdatedAt: updated, FetchedAt: r.fetched,
		SourceURL: r.url, Images: r.entries,
	}, "", "  ")
	if err != nil {
		return
	}
	tmp := filepath.Join(r.dir, CatalogFile+".tmp")
	if err := os.WriteFile(tmp, blob, 0o644); err != nil {
		log.Printf("imagecatalog: write catalog cache: %v", err)
		return
	}
	if err := os.Rename(tmp, filepath.Join(r.dir, CatalogFile)); err != nil {
		log.Printf("imagecatalog: replace catalog cache: %v", err)
	}
}

// validEntries drops entries a microVM could not boot from, so a malformed
// catalog degrades to "fewer images" instead of "unbootable projects". An
// entry must name itself and carry either a local artifact path or a remote URL
// with its digest — an unpinned artifact is refused (SRS §31: artifacts are
// immutable by digest).
func validEntries(in []types.ImageManifest) []types.ImageManifest {
	out := make([]types.ImageManifest, 0, len(in))
	seen := map[string]bool{}
	for _, m := range in {
		m.Name = strings.TrimSpace(m.Name)
		if m.Name == "" {
			continue
		}
		if m.Image == "" {
			m.Image = "base://" + m.Name
		}
		if m.Type == "" {
			m.Type = "base"
		}
		if m.ID == "" {
			m.ID = m.Name
		}
		if m.Status == "" {
			m.Status = "remote"
		}

		local := m.Rootfs != ""
		remote := m.RootfsURL != "" && m.RootfsSHA256 != ""
		if !local && !remote {
			log.Printf("imagecatalog: skipping %q: rootfs needs a local path, or a URL with a sha256 digest", m.Name)
			continue
		}
		// A kernel is optional (boot falls back to firecracker.kernel_image),
		// but a remote kernel must be pinned too.
		if m.KernelURL != "" && m.KernelSHA256 == "" {
			log.Printf("imagecatalog: skipping %q: remote kernel needs a sha256 digest", m.Name)
			continue
		}
		if m.VCPUs <= 0 {
			m.VCPUs = 1
		}
		if m.MemMiB <= 0 {
			m.MemMiB = 256
		}
		if seen[m.Image] {
			continue
		}
		seen[m.Image] = true
		out = append(out, m)
	}
	return out
}

// Lookup finds a catalog entry by reference (image reference, id, or name).
func (r *RemoteCatalog) Lookup(ctx context.Context, ref string) (types.ImageManifest, bool) {
	if ref == "" {
		return types.ImageManifest{}, false
	}
	for _, m := range r.Entries(ctx) {
		if m.Image == ref || m.ID == ref || m.Name == ref {
			return m, true
		}
	}
	return types.ImageManifest{}, false
}
