// Package volumes manages real persistent storage volumes. Each volume maps
// to a host directory under the volumes dir containing a sparse `data.img`
// file of the requested size (used as the backing block device) plus the
// files a running VM writes into it.
package volumes

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Manager creates and manages real volume directories on the host.
type Manager struct {
	dir string // e.g. "volumes"
}

// NewManager returns a volume manager rooted at dir (created if missing).
func NewManager(dir string) *Manager {
	if dir == "" {
		dir = "volumes"
	}
	return &Manager{dir: dir}
}

// EnsureRoot creates the volumes root directory.
func (m *Manager) EnsureRoot() error {
	return os.MkdirAll(m.dir, 0o755)
}

// Create provisions a real volume: a directory named volumeID plus a sparse
// data.img of sizeMiB. Returns the volume's host path.
func (m *Manager) Create(volumeID string, sizeMiB int) (string, error) {
	if sizeMiB <= 0 {
		sizeMiB = 1024
	}
	if err := m.EnsureRoot(); err != nil {
		return "", fmt.Errorf("volumes: ensure root: %w", err)
	}
	path := filepath.Join(m.dir, volumeID)
	if err := os.MkdirAll(path, 0o755); err != nil {
		return "", fmt.Errorf("volumes: mkdir %s: %w", path, err)
	}
	// Sparse image file: reports the requested size but allocates on write.
	img := filepath.Join(path, "data.img")
	f, err := os.OpenFile(img, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return "", fmt.Errorf("volumes: create image: %w", err)
	}
	defer f.Close()
	if err := f.Truncate(int64(sizeMiB) * 1024 * 1024); err != nil {
		return "", fmt.Errorf("volumes: truncate %s: %w", img, err)
	}
	// Real disk usage marker so the dashboard shows a live number.
	if err := os.WriteFile(filepath.Join(path, "VOLUME"), []byte(fmt.Sprintf("size_mib=%d\n", sizeMiB)), 0o644); err != nil {
		return "", fmt.Errorf("volumes: write marker: %w", err)
	}
	return path, nil
}

// Delete removes the host directory for a volume.
func (m *Manager) Delete(volumeID string) error {
	return os.RemoveAll(filepath.Join(m.dir, volumeID))
}

// Clone duplicates a volume's backing files under a new id (golden-image
// and scale-from-snapshot flows). The clone is an independent volume;
// sizes come from the copied marker when sizeMiB <= 0.
func (m *Manager) Clone(srcID, dstID string, sizeMiB int) (string, error) {
	srcID, dstID = SanitizeID(srcID), SanitizeID(dstID)
	if srcID == "" || dstID == "" || srcID == dstID {
		return "", fmt.Errorf("volumes: clone needs distinct source and target ids")
	}
	src, dst := filepath.Join(m.dir, srcID), filepath.Join(m.dir, dstID)
	if _, err := os.Stat(src); err != nil {
		return "", fmt.Errorf("volumes: missing source %s", srcID)
	}
	if _, err := os.Stat(dst); err == nil {
		return "", fmt.Errorf("volumes: target %s exists", dstID)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return "", fmt.Errorf("volumes: mkdir %s: %w", dstID, err)
	}
	if err := copyFile(filepath.Join(src, "data.img"), filepath.Join(dst, "data.img")); err != nil {
		_ = os.RemoveAll(dst)
		return "", err
	}
	marker := sizeMiB
	if marker <= 0 {
		marker = readMarkerMiB(filepath.Join(src, "VOLUME"))
	}
	if err := os.WriteFile(filepath.Join(dst, "VOLUME"), []byte(fmt.Sprintf("size_mib=%d\ncloned_from=%s\n", marker, srcID)), 0o644); err != nil {
		_ = os.RemoveAll(dst)
		return "", fmt.Errorf("volumes: write marker: %w", err)
	}
	return dst, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("volumes: open source: %w", err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("volumes: create target: %w", err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("volumes: copy: %w", err)
	}
	return nil
}

func readMarkerMiB(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		var v int
		if _, err := fmt.Sscanf(strings.TrimSpace(line), "size_mib=%d", &v); err == nil {
			return v
		}
	}
	return 0
}

// Usage walks the volume path and returns total bytes used by regular files.
func (m *Manager) Usage(volumeID string) (int64, error) {
	root := filepath.Join(m.dir, volumeID)
	var total int64
	err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return filepath.SkipDir
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

// Path returns the host path for a volume (empty if not created).
func (m *Manager) Path(volumeID string) string {
	path := filepath.Join(m.dir, volumeID)
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

// Exists reports whether a volume directory exists on disk.
func (m *Manager) Exists(volumeID string) bool {
	return m.Path(volumeID) != ""
}

// SanitizeID makes a volume ID safe to use as a directory name.
func SanitizeID(id string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '-'
	}, id)
}
