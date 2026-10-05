// Package netmgr owns the networking pieces Porter is responsible for: a
// per-project private /24 subnet, static IP assignment per replica, a
// deterministic MAC per VM, and a host TAP device consumed directly by
// Firecracker. Porter owns the IPAM and passes the resulting TAP/MAC contract
// to the Unix-socket runtime.
package netmgr

import (
	"fmt"
	"hash/fnv"
	"net"
	"os/exec"
	goruntime "runtime"
	"sync"
)

// BootSpec is the per-VM network identity the runtime consumes at boot
// (host tap device name, guest CIDR, gateway, deterministic MAC).
type BootSpec struct {
	MacAddress  string
	HostDevName string // host-side tap device name
	CIDR        string // guest IP + prefix, e.g. "10.42.1.5/24"
	GatewayAddr string
}

// AllocateProjectSubnet returns the next /24 as a string (10.42.N.0/24).
// It shares the single subnet counter with AllocateSubnet.
func (n *NetManager) AllocateProjectSubnet() string {
	ipn, err := n.AllocateSubnet()
	if err != nil {
		return "10.42.0.0/24"
	}
	return ipn.String()
}

// AllocateVMNetwork derives the boot-time network identity for one VM within
// its project subnet and creates/configures the host-side TAP device. The
// gateway address is assigned to the TAP so the guest's static route has a
// real host endpoint before the Firecracker API receives InstanceStart.
func (n *NetManager) AllocateVMNetwork(subnetCIDR string, replicaIndex int, vmID string) (BootSpec, error) {
	if goruntime.GOOS != "linux" {
		return BootSpec{}, fmt.Errorf("netmgr: TAP creation requires Linux (running on %s: control plane works, host networking does not)", goruntime.GOOS)
	}
	var a, b, c int
	if _, err := fmt.Sscanf(subnetCIDR, "%d.%d.%d.", &a, &b, &c); err != nil {
		return BootSpec{}, fmt.Errorf("netmgr: invalid project subnet %q: %w", subnetCIDR, err)
	}
	if replicaIndex < 0 || replicaIndex > 245 {
		return BootSpec{}, fmt.Errorf("netmgr: replica index %d out of range", replicaIndex)
	}
	ip := fmt.Sprintf("%d.%d.%d.%d", a, b, c, 10+replicaIndex)
	gw := fmt.Sprintf("%d.%d.%d.1", a, b, c)
	macAddr, err := DeterministicMAC(vmID)
	if err != nil {
		return BootSpec{}, fmt.Errorf("netmgr: derive MAC: %w", err)
	}
	mac := macAddr.String()
	tapName := "tap-" + bootShortID(vmID)
	// A stale TAP can survive a crashed Porter process. Delete only this
	// deterministic device, then recreate it with an explicit gateway address.
	_ = exec.Command("ip", "link", "del", tapName).Run()
	if err := exec.Command("ip", "tuntap", "add", "dev", tapName, "mode", "tap").Run(); err != nil {
		return BootSpec{}, fmt.Errorf("netmgr: create TAP %s: %w", tapName, err)
	}
	if err := exec.Command("ip", "addr", "replace", gw+"/24", "dev", tapName).Run(); err != nil {
		_ = exec.Command("ip", "link", "del", tapName).Run()
		return BootSpec{}, fmt.Errorf("netmgr: assign gateway %s to TAP %s: %w", gw, tapName, err)
	}
	if err := exec.Command("ip", "link", "set", "dev", tapName, "up").Run(); err != nil {
		_ = exec.Command("ip", "link", "del", tapName).Run()
		return BootSpec{}, fmt.Errorf("netmgr: bring TAP %s up: %w", tapName, err)
	}
	return BootSpec{MacAddress: mac, HostDevName: tapName, CIDR: ip + "/24", GatewayAddr: gw}, nil
}

// bootShortID truncates a VM id for TAP device names (IFNAMSIZ = 15 chars:
// "tap-" + 8 chars fits with room).
func bootShortID(vmID string) string {
	if len(vmID) > 8 {
		return vmID[:8]
	}
	return vmID
}

// AllocateVMNetwork30 derives the Firecracker-reference /30 per-link identity
// (docs/firecracker-manual.md §6): 4 addresses per VM — .0 network, .1 TAP
// gateway, .2 guest, .3 broadcast. The TAP gets <gw>/30 (never the host
// subnet), the guest boots with <guest>/30 + 255.255.255.252 mask. Pure math
// (no host ioctl) so it is unit-testable; AllocateVMNetwork stays the default
// until guest images + Linux e2e cover the cutover.
func AllocateVMNetwork30(baseThirdOctet, replicaIndex int, vmID string) (BootSpec, error) {
	if baseThirdOctet < 0 || baseThirdOctet > 252 {
		return BootSpec{}, fmt.Errorf("netmgr: base third octet %d out of range", baseThirdOctet)
	}
	if replicaIndex < 0 || replicaIndex > 63 {
		return BootSpec{}, fmt.Errorf("netmgr: /30 replica index %d out of range (0..63 per /24)", replicaIndex)
	}
	base4 := replicaIndex * 4
	gw := fmt.Sprintf("10.42.%d.%d", baseThirdOctet, base4+1)
	guest := fmt.Sprintf("10.42.%d.%d", baseThirdOctet, base4+2)
	macAddr, err := DeterministicMAC(vmID)
	if err != nil {
		return BootSpec{}, fmt.Errorf("netmgr: derive MAC: %w", err)
	}
	return BootSpec{
		MacAddress:  macAddr.String(),
		HostDevName: "tap-" + bootShortID(vmID),
		CIDR:        guest + "/30",
		GatewayAddr: gw,
	}, nil
}

// NetManager allocates subnets and IPs. It is a process-local allocator; the
// authoritative subnet per project is persisted in Postgres by the caller.
type NetManager struct {
	mu         sync.Mutex
	nextSubnet int // index into 10.42.0.0/16, incremented per allocation
}

const baseSubnet = "10.42.0.0/16"

// NewNetManager starts allocating from 10.42.0.0/24 upward.
func NewNetManager() *NetManager {
	return &NetManager{nextSubnet: 0}
}

// AllocateSubnet returns the next free /24 in 10.42.0.0/16, e.g. 10.42.3.0/24.
func (n *NetManager) AllocateSubnet() (net.IPNet, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.nextSubnet > 255 {
		return net.IPNet{}, fmt.Errorf("netmgr: exhausted 10.42.0.0/16 (255 subnets)")
	}
	sub := fmt.Sprintf("10.42.%d.0/24", n.nextSubnet)
	n.nextSubnet++
	_, ipnet, err := net.ParseCIDR(sub)
	if err != nil {
		return net.IPNet{}, fmt.Errorf("netmgr: parse %s: %w", sub, err)
	}
	return *ipnet, nil
}

// AllocateIP returns the static IP for a replica inside a /24 subnet. Hosts
// use .1, the VM replicas start at .10 so .2-.9 stay free for infrastructure.
func (n *NetManager) AllocateIP(subnet net.IPNet, replicaIndex int) (net.IP, error) {
	ip := subnet.IP.To4()
	if ip == nil {
		return nil, fmt.Errorf("netmgr: subnet %s is not IPv4", subnet.String())
	}
	if replicaIndex < 0 || replicaIndex > 245 {
		return nil, fmt.Errorf("netmgr: replica index %d out of range (0..245)", replicaIndex)
	}
	out := make(net.IP, 4)
	copy(out, ip)
	out[3] = byte(10 + replicaIndex) // .10, .11, ...
	return out, nil
}

// GatewayIP returns the host-side .1 address for a project /24. Direct
// Firecracker networking assigns this address to the per-VM TAP device.
func GatewayIP(subnet net.IPNet) net.IP {
	ip := subnet.IP.To4()
	if ip == nil {
		return nil
	}
	out := make(net.IP, 4)
	copy(out, ip)
	out[3] = 1
	return out
}

// DeterministicMAC derives a stable MAC address from a VM id, so a VM keeps
// the same link-layer identity across restarts.
func DeterministicMAC(vmID string) (net.HardwareAddr, error) {
	h := fnv.New64a()
	_, _ = h.Write([]byte(vmID))
	sum := h.Sum64()
	// Use the 06: local-bit prefix and derive the rest from the hash.
	return net.HardwareAddr{
		0x06,
		byte(sum >> 40), byte(sum >> 32), byte(sum >> 24), byte(sum >> 16), byte(sum >> 8),
	}, nil
}
