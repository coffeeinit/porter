// Base-image store + popular-image cache (learned from all three refs):
// kernels, rootfs bases, and per-VM copies live under one state dir with
// SHA256 manifest sidecars; multiple versions coexist under releases/; the
// popular-image cache pre-pulls the famous images users deploy most so
// `porter run` / first deploys never wait on a cold pull; default templates
// pin the blessed base per profile. Same method as OCM (GCS manifests +
// SHA256 verify + reflink copies) with PVE's template discipline, on the
// ObjectStore instead of GCS-only paths.
package imagecatalog

import (
	"fmt"
	"strings"
)

// Store layout roots (relative to the state dir).
const (
	DirImages  = "images"  // base rootfs + manifests
	DirKernels = "kernels" // versioned kernels + manifests
	DirVMs     = "vms"     // per-VM copies (same filesystem: reflink-able)
	DirCache   = "cache"   // popular-image OCI cache
)

// ManifestName is the SHA256 sidecar next to every stored artifact.
const ManifestName = ".manifest.json"

// BaseImage is one pinned base (rootfs or kernel).
type BaseImage struct {
	Name    string // e.g. debian-trixie, vmlinux-6.18
	Kind    string // rootfs | kernel
	Version string
	Digest  string // sha256 hex, mandatory
	Size    int64
}

// Validate gates base entries: digest-pinned, sized, known kind.
func (b BaseImage) Validate() error {
	if b.Name == "" || b.Version == "" {
		return fmt.Errorf("imagecatalog: base image needs name and version")
	}
	switch b.Kind {
	case "rootfs", "kernel":
	default:
		return fmt.Errorf("imagecatalog: unknown base kind %q", b.Kind)
	}
	if len(b.Digest) != 64 {
		return fmt.Errorf("imagecatalog: base image needs a sha256 digest")
	}
	if b.Size <= 0 {
		return fmt.Errorf("imagecatalog: base image needs a size")
	}
	return nil
}

// ReleasePath returns the versioned releases/ path for a base.
func ReleasePath(kind, name, version string) string {
	return fmt.Sprintf("%s/%s/releases/%s", kind, name, version)
}

// PopularImages are the famous images pre-pulled into the OCI cache so
// first deploys are warm. Tags are pinned, not floating.
var PopularImages = []string{
	"docker.io/library/alpine:3.21",
	"docker.io/library/debian:trixie-slim",
	"docker.io/library/ubuntu:24.04",
	"docker.io/library/python:3.12-slim",
	"docker.io/library/node:24-slim",
	"docker.io/library/nginx:alpine",
	"docker.io/library/postgres:16",
	"docker.io/library/redis:7-alpine",
}

// NormalizeRef expands short refs to fully-qualified pinned refs
// (docker.io/library/...), mirroring the PVE skopeo convention.
func NormalizeRef(ref string) string {
	if strings.Contains(ref, "://") {
		return ref
	}
	if !strings.Contains(ref, "/") {
		ref = "docker.io/library/" + ref
	}
	if !strings.Contains(ref, ":") {
		ref += ":latest"
	}
	return ref
}

// DefaultTemplates pins the blessed base per guest profile.
var DefaultTemplates = map[string]string{
	"minimal":  "alpine:3.21",
	"standard": "debian:trixie-slim",
	"full":     "ubuntu:24.04",
}

// DefaultTemplate resolves the base image for a profile.
func DefaultTemplate(profile string) (string, error) {
	base, ok := DefaultTemplates[strings.ToLower(profile)]
	if !ok {
		return "", fmt.Errorf("imagecatalog: unknown profile %q", profile)
	}
	return base, nil
}
