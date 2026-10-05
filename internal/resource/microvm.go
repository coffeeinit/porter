// Package resource defines the canonical Porter domain types.
package resource

// MicroVM is the runtime truth of one Firecracker MicroVM. It mirrors the
// micro_vms table (migration 0017): replica_id → replicas pool,
// node_id → servers. Lifecycle: provisioning → starting → running → degraded →
// stopping → stopped → failed → recovering → deleting → deleted.
type MicroVM struct {
	Metadata `json:"metadata"`
	Spec     MicroVMSpec   `json:"spec"`
	Status   MicroVMStatus `json:"status"`
}

func (m *MicroVM) GetMetadata() *Metadata { return &m.Metadata }
func (m *MicroVM) GetSpec() interface{}   { return m.Spec }
func (m *MicroVM) GetStatus() *Status     { return &m.Status.Status }
func (m *MicroVM) SetStatus(s *Status)    { m.Status.Status = *s }
func (m *MicroVM) GetKind() string        { return KindMicroVM }

// MicroVMSpec is the desired state of a MicroVM.
type MicroVMSpec struct {
	// ReplicaID links the MicroVM to a replica in the replicas pool.
	ReplicaID string `json:"replica_id,omitempty"`

	// NodeID links the MicroVM to a node (servers table).
	NodeID string `json:"node_id,omitempty"`

	// CPUMillicores is the vCPU allocation in millicores.
	CPUMillicores int `json:"cpu_millicores,omitempty"`

	// MemMiB is the memory allocation in MiB.
	MemMiB int `json:"mem_mib,omitempty"`

	// TargetState carries scheduler/placement intent for reconciliation.
	TargetState map[string]interface{} `json:"target_state,omitempty"`
}

// MicroVMStatus is the observed state of a MicroVM.
type MicroVMStatus struct {
	Status `json:",inline"`

	// State is the runtime lifecycle state.
	State string `json:"state,omitempty"`
}

// IPAllocation is a durable IP allocation backing the per-VM allocator (T6).
// It mirrors the ip_allocations table (migration 0017).
type IPAllocation struct {
	Metadata `json:"metadata"`
	Spec     IPAllocationSpec   `json:"spec"`
	Status   IPAllocationStatus `json:"status"`
}

func (a *IPAllocation) GetMetadata() *Metadata { return &a.Metadata }
func (a *IPAllocation) GetSpec() interface{}   { return a.Spec }
func (a *IPAllocation) GetStatus() *Status     { return &a.Status.Status }
func (a *IPAllocation) SetStatus(s *Status)    { a.Status.Status = *s }
func (a *IPAllocation) GetKind() string        { return KindIPAllocation }

// IPAllocationSpec is the desired state of an IP allocation.
type IPAllocationSpec struct {
	// NetworkID links the allocation to a network.
	NetworkID string `json:"network_id,omitempty"`

	// IP is the allocated address.
	IP string `json:"ip,omitempty"`

	// MicroVMID links the allocation to a MicroVM.
	MicroVMID string `json:"micro_vm_id,omitempty"`
}

// IPAllocationStatus is the observed state of an IP allocation.
type IPAllocationStatus struct {
	Status `json:",inline"`
}
