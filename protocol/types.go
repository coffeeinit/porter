package protocol

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

const Version = "v1"

type Envelope struct {
	ID          string      `json:"id"`
	Type        string      `json:"type"`
	NodeID      string      `json:"node_id,omitempty"`
	OperationID string      `json:"operation_id,omitempty"`
	SentAt      time.Time   `json:"sent_at"`
	Payload     interface{} `json:"payload,omitempty"`
}

type EnrollRequest struct {
	Token    string `json:"token"`
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	CPUs     int    `json:"cpus"`
	MemoryMB int    `json:"memory_mb"`
	DiskMB   int    `json:"disk_mb"`
}

type EnrollResponse struct {
	NodeID    string `json:"node_id"`
	NodeToken string `json:"node_token"`
}

type Heartbeat struct {
	NodeID       string    `json:"node_id"`
	CPUsFree     int       `json:"cpus_free"`
	MemoryFreeMB int       `json:"memory_free_mb"`
	DiskFreeMB   int       `json:"disk_free_mb"`
	VMCount      int       `json:"vm_count"`
	At           time.Time `json:"at"`
}

type VMApplySpec struct {
	VMID         string            `json:"vm_id"`
	ReplicaID    string            `json:"replica_id"`
	ProjectID    string            `json:"project_id"`
	DeploymentID string            `json:"deployment_id"`
	Image        string            `json:"image"`
	KernelPath   string            `json:"kernel_path"`
	RootfsPath   string            `json:"rootfs_path"`
	VCPUs        int               `json:"vcpus"`
	MemoryMB     int               `json:"memory_mb"`
	DiskMB       int               `json:"disk_mb"`
	Env          map[string]string `json:"env,omitempty"`
	Port         int               `json:"port,omitempty"`
	TapName      string            `json:"tap_name,omitempty"`
	GuestIP      string            `json:"guest_ip,omitempty"`
	GuestMAC     string            `json:"guest_mac,omitempty"`
	GatewayIP    string            `json:"gateway_ip,omitempty"`
	Bridge       string            `json:"bridge,omitempty"`
	BridgeCIDR   string            `json:"bridge_cidr,omitempty"`
	VolumeID     string            `json:"volume_id,omitempty"`
	VolumeSizeMB int               `json:"volume_size_mb,omitempty"`
}

type Command struct {
	ID          string      `json:"id"`
	NodeID      string      `json:"node_id"`
	OperationID string      `json:"operation_id"`
	Attempt     int         `json:"attempt"`
	Kind        string      `json:"kind"`
	Payload     interface{} `json:"payload"`
	State       string      `json:"state"`
	CreatedAt   time.Time   `json:"created_at"`
}

const CommandBuilderBuild = "builder.build"

// BuilderBuildSpec contains pinned source identifiers and bounded build
// resources. It deliberately has no host paths, credentials, environment
// variables, or arbitrary command payloads.
type BuilderBuildSpec struct {
	BuildID        string `json:"build_id"`
	RepositoryURL  string `json:"repository_url"`
	Commit         string `json:"commit"`
	ContextSubdir  string `json:"context_subdir,omitempty"`
	DockerfilePath string `json:"dockerfile_path,omitempty"`
	ImageName      string `json:"image_name"`
	Architecture   string `json:"architecture"`
	VCPUs          int    `json:"vcpus"`
	MemoryMB       int    `json:"memory_mb"`
	DiskMB         int    `json:"disk_mb"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// Validate checks wire-level invariants. It does not prove that a builder
// guest exists or that the source is reachable.
func (s BuilderBuildSpec) Validate() error {
	if !validProtocolID(s.BuildID) {
		return fmt.Errorf("build_id must be a 1-64 character identifier")
	}
	u, err := url.Parse(s.RepositoryURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return fmt.Errorf("repository_url must be an https URL without credentials, query, or fragment")
	}
	if !validGitCommit(s.Commit) {
		return fmt.Errorf("commit must be a full 40- or 64-character hexadecimal object id")
	}
	if !validRelativeBuildPath(s.ContextSubdir) || !validRelativeBuildPath(s.DockerfilePath) {
		return fmt.Errorf("context_subdir and dockerfile_path must be relative paths without traversal")
	}
	if !validBuilderImageName(s.ImageName) {
		return fmt.Errorf("image_name must be a non-empty registry-safe name")
	}
	if s.Architecture != "amd64" && s.Architecture != "arm64" {
		return fmt.Errorf("architecture must be amd64 or arm64")
	}
	if s.VCPUs < 1 || s.VCPUs > 16 || s.MemoryMB < 256 || s.MemoryMB > 32768 || s.DiskMB < 1024 || s.DiskMB > 51200 {
		return fmt.Errorf("builder resource limits are outside the supported bounds")
	}
	if s.TimeoutSeconds < 1 || s.TimeoutSeconds > 3600 {
		return fmt.Errorf("timeout_seconds must be between 1 and 3600")
	}
	return nil
}

type BuilderBuildResult struct {
	BuildID      string `json:"build_id"`
	OCIReference string `json:"oci_reference"`
	Digest       string `json:"digest"`
	Architecture string `json:"architecture"`
	SizeBytes    int64  `json:"size_bytes"`
}

func (r BuilderBuildResult) ValidateFor(spec BuilderBuildSpec) error {
	if r.BuildID != spec.BuildID {
		return fmt.Errorf("builder result build_id does not match request")
	}
	if r.Architecture != spec.Architecture {
		return fmt.Errorf("builder result architecture does not match request")
	}
	if !strings.HasPrefix(r.Digest, "sha256:") || !validHexDigest(strings.TrimPrefix(r.Digest, "sha256:")) {
		return fmt.Errorf("builder result must include a sha256 digest")
	}
	if r.OCIReference == "" || strings.ContainsAny(r.OCIReference, "\\\x00\r\n") || !strings.Contains(r.OCIReference, "@"+r.Digest) {
		return fmt.Errorf("builder result must be a digest-addressed OCI reference")
	}
	if r.SizeBytes <= 0 {
		return fmt.Errorf("builder result size_bytes must be positive")
	}
	return nil
}

type CommandResult struct {
	CommandID     string              `json:"command_id"`
	OperationID   string              `json:"operation_id"`
	NodeID        string              `json:"node_id"`
	Attempt       int                 `json:"attempt"`
	Success       bool                `json:"success"`
	Error         string              `json:"error,omitempty"`
	State         string              `json:"state,omitempty"`
	BuilderResult *BuilderBuildResult `json:"builder_result,omitempty"`
}

func validProtocolID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for i, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || (i > 0 && (r == '-' || r == '_')) {
			continue
		}
		return false
	}
	return true
}

func validGitCommit(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	return validHexDigest(value)
}

func validHexDigest(value string) bool {
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return len(value) > 0
}

func validRelativeBuildPath(value string) bool {
	if value == "" {
		return true
	}
	if strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || strings.ContainsRune(value, '\x00') {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." || part == "." || part == "" {
			return false
		}
	}
	return true
}

func validBuilderImageName(value string) bool {
	if value == "" || len(value) > 255 || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.Contains(value, "//") {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("/._-:", r) {
			continue
		}
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." || strings.HasPrefix(component, ".") {
			return false
		}
	}
	return true
}

type SnapshotSpec struct {
	VMID        string `json:"vm_id"`
	SnapshotID  string `json:"snapshot_id"`
	Consistency string `json:"consistency"`
}

type SnapshotRecord struct {
	ID          string         `json:"id"`
	VMID        string         `json:"vm_id"`
	StatePath   string         `json:"state_path"`
	MemPath     string         `json:"mem_path"`
	SizeBytes   int64          `json:"size_bytes"`
	StateSHA256 string         `json:"state_sha256"`
	MemSHA256   string         `json:"mem_sha256"`
	DiskFiles   []SnapshotFile `json:"disk_files,omitempty"`
}

type SnapshotFile struct {
	Kind         string `json:"kind"`
	OriginalPath string `json:"original_path"`
	BackupPath   string `json:"backup_path"`
	SizeBytes    int64  `json:"size_bytes"`
	SHA256       string `json:"sha256"`
}

type SnapshotRestoreSpec struct {
	SnapshotID  string         `json:"snapshot_id"`
	VMID        string         `json:"vm_id"`
	StatePath   string         `json:"state_path"`
	MemPath     string         `json:"mem_path"`
	SizeBytes   int64          `json:"size_bytes"`
	StateSHA256 string         `json:"state_sha256"`
	MemSHA256   string         `json:"mem_sha256"`
	DiskFiles   []SnapshotFile `json:"disk_files"`
}

type VolumeResizeSpec struct {
	VolumeID string `json:"volume_id"`
	SizeMB   int    `json:"size_mb"`
}
