// Package resource defines the canonical Porter domain types.
package resource

import (
	"time"
)

// Volume is a persistent storage volume that a replica can attach.
type Volume struct {
	Metadata `json:"metadata"`
	Spec     VolumeSpec   `json:"spec"`
	Status   VolumeStatus `json:"status"`
}

func (v *Volume) GetMetadata() *Metadata { return &v.Metadata }
func (v *Volume) GetSpec() interface{}   { return v.Spec }
func (v *Volume) GetStatus() *Status     { return &v.Status.Status }
func (v *Volume) SetStatus(s *Status)    { v.Status.Status = *s }
func (v *Volume) GetKind() string        { return KindVolume }

// VolumeSpec is the desired state of a volume
type VolumeSpec struct {
	// ProjectID is the owning project
	ProjectID string `json:"project_id,omitempty"`

	// SizeMiB is the volume size in MiB
	SizeMiB int `json:"size_mib"`

	// StorageClass is the storage class (standard, high-performance, nvme, archive)
	StorageClass string `json:"storage_class,omitempty"`

	// Filesystem is the filesystem type (ext4, xfs)
	Filesystem string `json:"filesystem,omitempty"`

	// Encryption enables encryption at rest
	Encryption bool `json:"encryption,omitempty"`

	// IOPS is the provisioned IOPS (for high-performance classes)
	IOPS int `json:"iops,omitempty"`

	// ThroughputMBps is the provisioned throughput
	ThroughputMBps int `json:"throughput_mbps,omitempty"`

	// SnapshotPolicy is the snapshot schedule
	SnapshotPolicy string `json:"snapshot_policy,omitempty"`

	// BackupPolicy is the backup schedule
	BackupPolicy string `json:"backup_policy,omitempty"`

	// ReplicaCount for replicated storage
	ReplicaCount int `json:"replica_count,omitempty"`

	// AccessModes are the supported access modes
	AccessModes []string `json:"access_modes,omitempty"`
}

// VolumeStatus is the observed state of a volume
type VolumeStatus struct {
	Status `json:",inline"`

	// Phase is the current phase
	Phase string `json:"phase"`

	// Capacity is the actual capacity
	Capacity int64 `json:"capacity"`

	// Used is the used space
	Used int64 `json:"used"`

	// AttachedTo is the replica ID this volume is attached to
	AttachedTo string `json:"attached_to,omitempty"`

	// DevicePath is the device path on the host
	DevicePath string `json:"device_path,omitempty"`

	// MountPath is the mount path in the VM
	MountPath string `json:"mount_path,omitempty"`

	// NodeID is the node where the volume resides
	NodeID string `json:"node_id,omitempty"`

	// LastSnapshotAt is the time of the last snapshot
	LastSnapshotAt *time.Time `json:"last_snapshot_at,omitempty"`

	// LastBackupAt is the time of the last backup
	LastBackupAt *time.Time `json:"last_backup_at,omitempty"`
}
