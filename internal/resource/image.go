// Package resource defines the canonical Porter domain types.
package resource

import (
	"time"
)

// Image is a direct Firecracker image manifest (vmlinux + rootfs.ext4).
type Image struct {
	Metadata `json:"metadata"`
	Spec     ImageSpec   `json:"spec"`
	Status   ImageStatus `json:"status"`
}

func (i *Image) GetMetadata() *Metadata { return &i.Metadata }
func (i *Image) GetSpec() interface{}   { return i.Spec }
func (i *Image) GetStatus() *Status     { return &i.Status.Status }
func (i *Image) SetStatus(s *Status)    { i.Status.Status = *s }
func (i *Image) GetKind() string        { return KindImage }

// ImageSpec is the desired state of an image
type ImageSpec struct {
	// Name is the image name
	Name string `json:"name"`

	// Description is the image description
	Description string `json:"description,omitempty"`

	// Type is the image type (direct, custom, base)
	Type string `json:"type"`

	// Image is the stable catalog reference
	Image string `json:"image,omitempty"`

	// Rootfs is the host path to ext4 rootfs
	Rootfs string `json:"rootfs,omitempty"`

	// Kernel is the host path to vmlinux
	Kernel string `json:"kernel,omitempty"`

	// Architecture is the CPU architecture
	Architecture string `json:"architecture,omitempty"`

	// RootfsSHA256 is the rootfs checksum
	RootfsSHA256 string `json:"rootfs_sha256,omitempty"`

	// KernelSHA256 is the kernel checksum
	KernelSHA256 string `json:"kernel_sha256,omitempty"`

	// VCPUs is the default vCPUs
	VCPUs int `json:"vcpus"`

	// MemMiB is the default memory
	MemMiB int `json:"mem_mib"`

	// Ports are the default ports
	Ports []Port `json:"ports"`

	// Env are the default environment variables
	Env map[string]string `json:"env,omitempty"`

	// Tags for categorization
	Tags []string `json:"tags,omitempty"`

	// Logo URL for dashboard
	Logo string `json:"logo,omitempty"`

	// Version of the image
	Version string `json:"version,omitempty"`

	// Kind is the image kind (direct, custom)
	Kind string `json:"kind,omitempty"`
}

// ImageStatus is the observed state of an image
type ImageStatus struct {
	Status `json:",inline"`

	// Phase is the current phase
	Phase string `json:"status"` // ready, missing, invalid, uploading

	// ValidatedAt is when the image was validated
	ValidatedAt *time.Time `json:"validated_at,omitempty"`

	// Size is the total size in bytes
	Size int64 `json:"size,omitempty"`

	// DownloadURL for the image
	DownloadURL string `json:"download_url,omitempty"`
}
