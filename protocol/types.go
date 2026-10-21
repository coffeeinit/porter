package protocol

import "time"

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
	Kind        string      `json:"kind"`
	Payload     interface{} `json:"payload"`
	State       string      `json:"state"`
	CreatedAt   time.Time   `json:"created_at"`
}

type CommandResult struct {
	CommandID   string `json:"command_id"`
	OperationID string `json:"operation_id"`
	NodeID      string `json:"node_id"`
	Success     bool   `json:"success"`
	Error       string `json:"error,omitempty"`
	State       string `json:"state,omitempty"`
}

type SnapshotSpec struct {
	VMID       string `json:"vm_id"`
	SnapshotID string `json:"snapshot_id"`
}

type VolumeResizeSpec struct {
	VolumeID string `json:"volume_id"`
	SizeMB   int    `json:"size_mb"`
}
