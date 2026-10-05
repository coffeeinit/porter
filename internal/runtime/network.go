// Package runtime manages Firecracker MicroVM lifecycle.
package runtime

import (
	"fmt"
	"hash/fnv"
	"strconv"

	"porter/internal/netmgr"
	"porter/internal/resource"
)

// defaultBridge is the host bridge or TAP veth the per-VM TAPs attach to.
const defaultBridge = "porter-br0"

// defaultBase is the single private base shared with netmgr (task T6b: one
// allocator math — 10.42.0.0/16, gateway .1, guests .10+index, 06: MACs).
// The /30-per-link layout from the Firecracker reference stays a follow-up:
// both allocators boot guests with a /24 mask today and changing it requires
// coordinated guest-image + TAP-addr changes with Linux e2e cover.
const defaultBase = "10.42"

// NetworkManager allocates per-VM address space: a host TAP, a guest IP, a
// /24 subnet (matching the boot-args convention in fc_client.go) and a MAC.
// Allocation is deterministic on (projectID, replicaIndex) so restarts of a
// controller re-derive the same mapping for the same replica.
type NetworkManager struct {
	bridge string
	base   string // e.g. "10.42" — third octet is derived per replica
}

// NewNetworkManager builds a NetworkManager. Empty args fall back to defaults
// shared with netmgr.
func NewNetworkManager(bridge, base string) *NetworkManager {
	if bridge == "" {
		bridge = defaultBridge
	}
	if base == "" {
		base = defaultBase
	}
	return &NetworkManager{bridge: bridge, base: base}
}

// AllocateVMNetwork deterministically returns a TAP/guest-IP/MAC/subnet set for
// a replica. projectID and vmID are free-form; the TAP name is hashed to stay
// within Linux IFNAMSIZ (15 chars). Math matches netmgr (T6b unified).
func (m *NetworkManager) AllocateVMNetwork(projectID string, replicaIndex int, vmID string) (*resource.ReplicaNetworkSpec, error) {
	if replicaIndex < 0 || replicaIndex > 245 {
		return nil, fmt.Errorf("runtime: replica index %d out of range (0..245)", replicaIndex)
	}
	key := fmt.Sprintf("%s:%d", projectID, replicaIndex)
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	o := h.Sum32()

	third := int(o % 253) // 0..252, keep 253-255 spare
	guest := fmt.Sprintf("%s.%d.%d", m.base, third, 10+replicaIndex)
	gateway := fmt.Sprintf("%s.%d.%d", m.base, third, 1)

	macAddr, err := netmgr.DeterministicMAC(vmID)
	if err != nil {
		return nil, fmt.Errorf("runtime: derive MAC: %w", err)
	}

	return &resource.ReplicaNetworkSpec{
		Subnet:    fmt.Sprintf("%s.%d.0.0/24", m.base, third),
		IPAddress: guest,
		Gateway:   gateway,
		MAC:       macAddr.String(),
		Bridge:    m.bridge,
		TapName:   "tap" + strconv.FormatUint(uint64(o), 16),
	}, nil
}

// AllocateVMNetwork30 is the /30-per-link variant (firecracker-manual §6).
// Same hash for the third octet as AllocateVMNetwork, but the fourth octet is
// a 4-address block: gateway = block+1 (TAP), guest = block+2, mask /30.
// Returned Subnet is the /30 itself so boot args + TAP addr stay consistent.
func (m *NetworkManager) AllocateVMNetwork30(projectID string, replicaIndex int, vmID string) (*resource.ReplicaNetworkSpec, error) {
	if replicaIndex < 0 || replicaIndex > 63 {
		return nil, fmt.Errorf("runtime: /30 replica index %d out of range (0..63)", replicaIndex)
	}
	key := fmt.Sprintf("%s:%d", projectID, replicaIndex)
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	o := h.Sum32()
	third := int(o % 253)
	base4 := replicaIndex * 4
	gateway := fmt.Sprintf("%s.%d.%d", m.base, third, base4+1)
	guest := fmt.Sprintf("%s.%d.%d", m.base, third, base4+2)
	macAddr, err := netmgr.DeterministicMAC(vmID)
	if err != nil {
		return nil, fmt.Errorf("runtime: derive MAC: %w", err)
	}
	return &resource.ReplicaNetworkSpec{
		Subnet:    fmt.Sprintf("%s.%d.%d/30", m.base, third, base4),
		IPAddress: guest,
		Gateway:   gateway,
		MAC:       macAddr.String(),
		Bridge:    m.bridge,
		TapName:   "tap" + strconv.FormatUint(uint64(o), 16),
	}, nil
}
