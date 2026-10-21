// Package imagecatalog loads the on-disk image library (vms/images/*.json)
// and exposes it to the API for the dashboard's image picker.
package imagecatalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"porter/internal/types"
)

// Catalog is an on-disk image library. The directory is retained so API reads
// can refresh manifests created after server startup.
type Catalog struct {
	dir    string
	images []types.ImageManifest
}

// New scans dir for *.json image manifests. A missing or empty dir yields
// an empty catalog (the API stays usable, just with no quick-deploy picker).
func New(dir string) *Catalog {
	c := &Catalog{dir: dir}
	c.Reload()
	return c
}

// Reload refreshes the immutable-in-use snapshot from disk. Invalid manifests
// are skipped, while a missing directory simply produces an empty catalog.
func (c *Catalog) Reload() {
	images := []types.ImageManifest{}
	if c == nil || c.dir == "" {
		if c != nil {
			c.images = images
		}
		return
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		c.images = images
		return
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" || strings.HasSuffix(e.Name(), ".builder.json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(c.dir, e.Name()))
		if err != nil {
			continue
		}
		var m types.ImageManifest
		if err := json.Unmarshal(b, &m); err != nil {
			continue
		}
		if m.ID == "" {
			m.ID = strings.TrimSuffix(e.Name(), ".json")
		}
		images = append(images, m)
	}
	sort.Slice(images, func(i, j int) bool {
		if images[i].Name == images[j].Name {
			return images[i].Image < images[j].Image
		}
		return images[i].Name < images[j].Name
	})
	c.images = images
}

// All returns the current catalog entries.
func (c *Catalog) All() []types.ImageManifest {
	if c == nil {
		return nil
	}
	return c.images
}
