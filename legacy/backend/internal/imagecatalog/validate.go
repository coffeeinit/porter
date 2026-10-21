package imagecatalog

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"porter/internal/types"
)

// ArtifactReport is the operator-facing readiness result for a bootable
// direct Firecracker image.
type ArtifactReport struct {
	Status       string `json:"status"`
	Architecture string `json:"architecture"`
	RootfsSHA256 string `json:"rootfs_sha256,omitempty"`
	KernelSHA256 string `json:"kernel_sha256,omitempty"`
	RootfsBytes  int64  `json:"rootfs_bytes,omitempty"`
	KernelBytes  int64  `json:"kernel_bytes,omitempty"`
	Error        string `json:"error,omitempty"`
}

// ValidateArtifacts requires regular, non-empty host files and records their
// digests. Symlinks are rejected so a manifest cannot redirect the runtime to
// an unexpected path after registration.
func ValidateArtifacts(rootfs, kernel string) (ArtifactReport, error) {
	report := ArtifactReport{Status: "invalid", Architecture: "x86_64"}
	rootfsBytes, rootfsSHA, err := inspectFile(rootfs, "rootfs.ext4")
	if err != nil {
		report.Error = err.Error()
		return report, err
	}
	kernelBytes, kernelSHA, err := inspectFile(kernel, "vmlinux")
	if err != nil {
		report.Error = err.Error()
		return report, err
	}
	report.Status = "ready"
	report.RootfsSHA256 = rootfsSHA
	report.KernelSHA256 = kernelSHA
	report.RootfsBytes = rootfsBytes
	report.KernelBytes = kernelBytes
	return report, nil
}

func inspectFile(path, label string) (int64, string, error) {
	if path == "" {
		return 0, "", fmt.Errorf("%s path is empty", label)
	}
	clean := filepath.Clean(path)
	info, err := os.Lstat(clean)
	if err != nil {
		return 0, "", fmt.Errorf("%s is unavailable: %w", label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return 0, "", fmt.Errorf("%s must be a regular non-symlink file", label)
	}
	if info.Size() <= 0 {
		return 0, "", fmt.Errorf("%s is empty", label)
	}
	f, err := os.Open(clean)
	if err != nil {
		return 0, "", fmt.Errorf("open %s: %w", label, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return 0, "", fmt.Errorf("hash %s: %w", label, err)
	}
	return info.Size(), hex.EncodeToString(h.Sum(nil)), nil
}

// ManifestFromArtifacts creates a persisted direct-image record from known
// host artifacts. It never accepts a Docker/OCI image reference as boot data.
func ManifestFromArtifacts(name, reference, description, kind, rootfs, kernel string, vcpus, memMiB int) *types.GoldenImage {
	if kind == "" {
		kind = "base"
	}
	if reference == "" {
		reference = "base://" + name
	}
	report, err := ValidateArtifacts(rootfs, kernel)
	status := report.Status
	if err != nil {
		status = "invalid"
	}
	if vcpus <= 0 {
		vcpus = 1
	}
	if memMiB <= 0 {
		memMiB = 256
	}
	now := timeNow()
	return &types.GoldenImage{
		ID: uuid.NewString(), Name: name, Image: reference, Description: description,
		Kind: kind, Rootfs: filepath.Clean(rootfs), Kernel: filepath.Clean(kernel),
		Architecture: report.Architecture, RootfsSHA256: report.RootfsSHA256,
		KernelSHA256: report.KernelSHA256, Status: status, ValidatedAt: &now,
		VCPUs: vcpus, MemMiB: memMiB, Ports: []types.Port{}, Env: map[string]string{}, Tags: []string{},
		Logo: "", Version: "v1",
		CreatedAt: now,
	}
}

// timeNow is a small seam for future deterministic catalog tests.
var timeNow = func() time.Time { return time.Now() }
