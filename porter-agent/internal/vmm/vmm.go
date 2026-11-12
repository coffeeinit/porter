// Package vmm defines the boundary between the Porter agent core and the
// virtual machine monitor (VMM) that actually runs guest MicroVMs.
//
// The agent core — reconciliation, local state, command handling, disk
// snapshots, networking, volume leases — must not know which VMM is in use.
// Everything specific to a VMM (its executable, its control socket, its boot
// API, its snapshot format) lives behind Driver. Upgrading Firecracker, or
// adding a second VMM, must not require touching the agent core.
package vmm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// MachineSpec is the VMM-agnostic description of a guest to boot.
type MachineSpec struct {
	KernelPath  string
	RootfsPath  string
	VolumePath  string
	GuestMAC    string
	HostDevName string
	GuestIP     string
	GatewayIP   string
	BridgeCIDR  string
	VCPUs       int
	MemoryMB    int
	InitPath    string
}

// SnapshotSpec names the two files a VMM snapshot is made of: the device and
// CPU state, and the guest memory image.
type SnapshotSpec struct {
	StatePath string
	MemPath   string
}

// Driver is the boundary between the Porter agent core and one VMM.
//
// Implementations must be safe for concurrent use by different VMs; a single
// Driver value is shared by every VM on the node.
type Driver interface {
	// Name identifies the VMM, for example "firecracker".
	Name() string
	// Binary is the executable the process supervisor launches.
	Binary() string
	// SocketName is the control socket file name inside a VM directory.
	SocketName() string
	// LogName is the VMM log file name inside a VM directory.
	LogName() string
	// Boot configures a freshly started VMM and starts the guest.
	Boot(ctx context.Context, socket string, spec MachineSpec) error
	// Pause freezes a running guest.
	Pause(ctx context.Context, socket string) error
	// Resume thaws a paused guest.
	Resume(ctx context.Context, socket string) error
	// Snapshot writes a full snapshot of a paused guest.
	Snapshot(ctx context.Context, socket string, snap SnapshotSpec) error
	// Restore loads a snapshot into a freshly started VMM and resumes it.
	// hostDevName, when non-empty, overrides the snapshot's network device.
	Restore(ctx context.Context, socket string, snap SnapshotSpec, hostDevName string) error
}

// Client returns an HTTP client bound to a VMM control socket.
func Client(socket string) *http.Client {
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
}

// Put sends a JSON PUT to the VMM control API.
func Put(ctx context.Context, c *http.Client, path string, payload interface{}) error {
	return Do(ctx, c, http.MethodPut, path, payload)
}

// Do sends a JSON request to the VMM control API.
func Do(ctx context.Context, c *http.Client, method, path string, payload interface{}) error {
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, method, "http://localhost"+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("vmm %s %s: %s %s", method, path, resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

// Netmask converts a CIDR prefix length to a dotted-quad netmask.
func Netmask(cidr string) string {
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		return "255.255.0.0"
	}
	return net.IP(n.Mask).String()
}
