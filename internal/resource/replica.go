// Package resource defines the canonical Porter domain types.
package resource

import (
	"time"
)

// Replica is a single microVM instance within a project's replica pool.
// This corresponds to a Firecracker MicroVM.
type Replica struct {
	Metadata `json:"metadata"`
	Spec     ReplicaSpec   `json:"spec"`
	Status   ReplicaStatus `json:"status"`
}

func (r *Replica) GetMetadata() *Metadata { return &r.Metadata }
func (r *Replica) GetSpec() interface{}   { return r.Spec }
func (r *Replica) GetStatus() *Status     { return &r.Status.Status }
func (r *Replica) SetStatus(s *Status)    { r.Status.Status = *s }
func (r *Replica) GetKind() string        { return KindReplica }

// ReplicaSpec is the desired state of a replica
type ReplicaSpec struct {
	// ProjectID is the owning project
	ProjectID string `json:"project_id"`

	// ReplicaIndex is the index within the project's replica pool
	ReplicaIndex int `json:"replica_index"`

	// Image is the image reference to boot
	Image string `json:"image"`

	// RootfsPath is the path to the ext4 rootfs on the host
	RootfsPath string `json:"rootfs_path,omitempty"`

	// Kernel is the per-VM vmlinux path (custom images); falls back to shared kernel
	Kernel string `json:"kernel,omitempty"`

	// VCPUs is the number of virtual CPUs
	VCPUs int `json:"vcpus"`

	// MemMiB is the memory in MiB
	MemMiB int `json:"mem_mib"`

	// Ports are the port mappings
	Ports []Port `json:"ports"`

	// Env environment variables
	Env map[string]string `json:"env,omitempty"`

	// VolumeID is the persistent volume attached as /dev/vdb
	VolumeID string `json:"volume_id,omitempty"`

	// Healthcheck configuration
	Healthcheck *Healthcheck `json:"healthcheck,omitempty"`

	// Restart policy
	Restart string `json:"restart,omitempty"`

	// GuestBase is the base image reference
	GuestBase string `json:"guest_base,omitempty"`

	// DeploymentID links to the deployment that created this replica
	DeploymentID string `json:"deployment_id,omitempty"`

	// DeploymentVersion is the deployment revision
	DeploymentVersion string `json:"deployment_version,omitempty"`

	// DeploymentEnvironment is the environment name
	DeploymentEnvironment string `json:"deployment_environment,omitempty"`

	// SnapshotPolicy snapshot configuration
	SnapshotPolicy string `json:"snapshot_policy,omitempty"`
}

// ReplicaStatus is the observed state of a replica
type ReplicaStatus struct {
	Status `json:",inline"`

	// State is the current lifecycle state
	State string `json:"state"`

	// HealthStatus is the current health
	HealthStatus string `json:"health_status"`

	// LastTransitionTime is when the current phase was entered
	LastTransitionTime time.Time `json:"last_transition_time,omitempty"`

	// DeploymentVersion is the deployment revision that owns this replica
	DeploymentVersion string `json:"deployment_version,omitempty"`

	// IPAddress is the assigned IP
	IPAddress string `json:"ip_address,omitempty"`

	// ContainerID is the Firecracker container ID
	ContainerID string `json:"container_id,omitempty"`

	// TaskID is the async task ID for this operation
	TaskID string `json:"task_id,omitempty"`

	// StartedAt is when the replica entered running state
	StartedAt *time.Time `json:"started_at,omitempty"`

	// Crashed indicates if the replica has crashed
	Crashed bool `json:"crashed,omitempty"`

	// Error message if failed
	Error string `json:"error,omitempty"`

	// SnapshotPath for snapshot/restore
	SnapshotPath string `json:"snapshot_path,omitempty"`

	// SnapshotMemPath for memory snapshot
	SnapshotMemPath string `json:"snapshot_mem_path,omitempty"`

	// SnapshotStatus snapshot state
	SnapshotStatus string `json:"snapshot_status,omitempty"`

	// SnapshotError if snapshot failed
	SnapshotError string `json:"snapshot_error,omitempty"`

	// SnapshotCreatedAt when snapshot was created
	SnapshotCreatedAt *time.Time `json:"snapshot_created_at,omitempty"`

	// LastRecoveredAt when last recovered
	LastRecoveredAt *time.Time `json:"last_recovered_at,omitempty"`

	// RecoveryCount number of recoveries
	RecoveryCount int `json:"recovery_count,omitempty"`

	// MACAddress of the TAP interface
	MACAddress string `json:"mac_address,omitempty"`

	// NetworkSpec for the TAP/bridge config
	NetworkSpec *ReplicaNetworkSpec `json:"network_spec,omitempty"`
}

// Port defines a port mapping
type Port struct {
	ContainerPort int    `json:"container_port"`
	HostPort      int    `json:"host_port,omitempty"`
	Protocol      string `json:"protocol"`
}

// NetworkSpec defines the network configuration for a replica
type ReplicaNetworkSpec struct {
	Subnet    string `json:"subnet"`
	IPAddress string `json:"ip_address"`
	Gateway   string `json:"gateway"`
	MAC       string `json:"mac"`
	Bridge    string `json:"bridge"`
	TapName   string `json:"tap_name"`
}
