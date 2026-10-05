// Extension and template catalog model (FCM-12/16, OCM-14). Core Porter
// stays useful without any marketplace content; third-party code runs
// out-of-process with scoped capabilities and never gets direct DB access.
// This package owns manifest validation and chunked-fetch planning.
package marketplace

import (
	"fmt"
	"strings"
)

// Item is one catalog entry (provider adapter, template, workflow pack...).
type Item struct {
	Publisher   string
	Name        string
	Version     string
	Description string
	Digest      string // sha256 of the packed content
	ManifestURI string // ObjectStore URI
}

// ID returns the stable publisher/name coordinate.
func (i Item) ID() string { return i.Publisher + "/" + i.Name }

// Validate gates catalog ingestion.
func (i Item) Validate() error {
	if i.Publisher == "" || i.Name == "" || i.Version == "" {
		return fmt.Errorf("marketplace: publisher, name and version are required")
	}
	if len(i.Digest) != 64 {
		return fmt.Errorf("marketplace: digest must be sha256 hex")
	}
	if i.ManifestURI == "" {
		return fmt.Errorf("marketplace: manifest uri is required")
	}
	return nil
}

// ManifestV2 is the checksummed package descriptor (FCM-09, ObjectStore
// hosted). SHA-256 only.
type ManifestV2 struct {
	APIVersion string            `json:"apiVersion"`
	Name       string            `json:"name"`
	Version    string            `json:"version"`
	Checksums  map[string]string `json:"checksums"` // file -> sha256 hex
}

// Validate enforces manifest hygiene before install.
func (m ManifestV2) Validate() error {
	if m.APIVersion == "" || m.Name == "" || m.Version == "" {
		return fmt.Errorf("marketplace: manifest needs apiVersion, name, version")
	}
	if len(m.Checksums) == 0 {
		return fmt.Errorf("marketplace: manifest needs at least one checksum")
	}
	for f, sum := range m.Checksums {
		if len(sum) != 64 || strings.TrimSpace(f) == "" {
			return fmt.Errorf("marketplace: bad checksum entry for %q", f)
		}
	}
	return nil
}

// Part is one chunk of a split-part download (FCM-16 store pattern).
type Part struct {
	Filename string
	Size     int64
	SHA256   string
}

// FetchPlan orders parts deterministically for resumable download.
func FetchPlan(parts []Part) []Part {
	out := append([]Part(nil), parts...)
	for i := 1; i < len(out); i++ {
		j := i
		for j > 0 && out[j-1].Filename > out[j].Filename {
			out[j-1], out[j] = out[j], out[j-1]
			j--
		}
	}
	return out
}
