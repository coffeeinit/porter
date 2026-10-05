// Package resource defines the canonical Porter domain types.
package resource

import (
	"time"
)

// Node is a registered compute host in a Porter cluster.
type Node struct {
	Metadata `json:"metadata"`
	Spec     NodeSpec   `json:"spec"`
	Status   NodeStatus `json:"status"`
}

func (n *Node) GetMetadata() *Metadata { return &n.Metadata }
func (n *Node) GetSpec() interface{}   { return n.Spec }
func (n *Node) GetStatus() *Status     { return &n.Status.Status }
func (n *Node) SetStatus(s *Status)    { n.Status.Status = *s }
func (n *Node) GetKind() string        { return KindNode }

// NodeSpec is the desired state of a node
type NodeSpec struct {
	// Name is the node name
	Name string `json:"name"`

	// Address is the node address
	Address string `json:"address"`

	// Region is the node region
	Region string `json:"region,omitempty"`

	// Zone is the node zone
	Zone string `json:"zone,omitempty"`

	// NodePool is the node pool
	NodePool string `json:"node_pool,omitempty"`

	// VCPUs is the total vCPUs
	VCPUs int `json:"vcpus"`

	// MemMiB is the total memory
	MemMiB int `json:"mem_mib"`

	// Labels for scheduling
	Labels map[string]string `json:"labels,omitempty"`

	// Taints for scheduling
	Taints []Taint `json:"taints,omitempty"`

	// Capabilities is the list of capabilities
	Capabilities []string `json:"capabilities,omitempty"`

	// Config is the node configuration
	Config NodeConfig `json:"config,omitempty"`
}

// NodeConfig is the node configuration
type NodeConfig struct {
	// FirecrackerBin path
	FirecrackerBin string `json:"firecracker_bin,omitempty"`

	// KernelImage path
	KernelImage string `json:"kernel_image,omitempty"`

	// RootfsPath path
	RootfsPath string `json:"rootfs_path,omitempty"`

	// SocketDir for Firecracker sockets
	SocketDir string `json:"socket_dir,omitempty"`

	// SnapshotDir for snapshots
	SnapshotDir string `json:"snapshot_dir,omitempty"`

	// LogsDir for logs
	LogsDir string `json:"logs_dir,omitempty"`

	// VolumesDir for volumes
	VolumesDir string `json:"volumes_dir,omitempty"`

	// ImagesDir for images
	ImagesDir string `json:"images_dir,omitempty"`
}

// Taint is a node taint
type Taint struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Effect string `json:"effect"` // NoSchedule, PreferNoSchedule, NoExecute
}

// NodeStatus is the observed state of a node
type NodeStatus struct {
	Status `json:",inline"`

	// Phase is the current phase
	Phase string `json:"phase"` // Register, AgentConnected, HardwareDiscovered, KVMVerified, FirecrackerReady, BuildKitReady, NetworkReady, StorageReady, CapabilitiesRegistered, Ready, Cordoned, Draining, Maintenance, Unhealthy

	// LastTransitionTime is when the current phase was entered
	LastTransitionTime time.Time `json:"last_transition_time,omitempty"`

	// Conditions node conditions
	Conditions []Condition `json:"conditions,omitempty"`

	// Allocatable resources
	Allocatable NodeResources `json:"allocatable,omitempty"`

	// Capacity total resources
	Capacity NodeResources `json:"capacity,omitempty"`

	// Used resources
	Used NodeResources `json:"used,omitempty"`

	// Projects count
	Projects int `json:"projects"`

	// VMs count
	VMs int `json:"vms"`

	// LastHeartbeat is the last heartbeat time
	LastHeartbeat *time.Time `json:"last_heartbeat,omitempty"`

	// Version is the Porter agent version
	Version string `json:"version,omitempty"`

	// OS is the operating system
	OS string `json:"os,omitempty"`

	// Arch is the architecture
	Arch string `json:"arch,omitempty"`

	// FirecrackerVersion is the Firecracker version
	FirecrackerVersion string `json:"firecracker_version,omitempty"`

	// BuildKitVersion is the BuildKit version
	BuildKitVersion string `json:"buildkit_version,omitempty"`
}

// NodeResources represents compute resources
type NodeResources struct {
	VCPUs  int64 `json:"vcpus"`
	MemMiB int64 `json:"mem_mib"`
	Pods   int   `json:"pods,omitempty"` // max replicas
}
