// Package runtime manages Firecracker MicroVM lifecycle.
package runtime

import (
	"context"
	"fmt"
	"sync"
	"time"

	"porter/internal/resource"
	"porter/internal/store"
)

// Manager orchestrates Firecracker VM operations
type Manager struct {
	store   *store.Store
	config  FCConfig
	vms     map[string]*resource.Replica
	vmsMu   sync.RWMutex
	fc      *FCClient
	network *NetworkManager
}

// FCConfig holds Firecracker configuration
type FCConfig struct {
	Mode           Mode
	FirecrackerBin string
	KernelImage    string
	RootfsPath     string
	SocketDir      string
	SnapshotDir    string
	LogsDir        string
	// SecretKey decrypts project secrets for guest injection (G4). Nil
	// disables injection; guests boot without the env map.
	SecretKey []byte
	// Hardening (firecracker-manual §3): jailer + cgroups + seccomp. Empty =
	// direct boot (dev/Windows); set on Linux hosts for multi-tenant.
	JailerBin     string
	EnableJailer  bool
	CgroupVersion string // "v1" | "v2" (default v2)
	SeccompFilter string // empty = Firecracker defaults (recommended)
	// Vsock: host CID is always 2; guest CIDs >= 3 per manual §6.
	VsockUDSPath string
	// Balloon target MiB (0 = device not installed).
	BalloonMiB int
	// UseNetwork30 selects the /30 per-link layout (default /24 guests).
	UseNetwork30 bool
}

// NewManager creates a new runtime manager
func NewManager(cfg FCConfig, st *store.Store, netMgr *NetworkManager) *Manager {
	return &Manager{
		store:   st,
		config:  cfg,
		vms:     make(map[string]*resource.Replica),
		fc:      NewFCClient(cfg.FirecrackerBin, cfg.SocketDir),
		network: netMgr,
	}
}

// Boot boots a replica as a Firecracker MicroVM
func (m *Manager) Boot(ctx context.Context, r *resource.Replica) error {
	if err := RequireMicroVMHost("Replica boot"); err != nil {
		return err
	}
	m.vmsMu.Lock()
	m.vms[r.Metadata.ID] = r
	m.vmsMu.Unlock()

	// Configure Firecracker
	fcCfg := FCVMConfig{
		VMID:          r.Metadata.ID,
		KernelPath:    r.Spec.Kernel,
		RootfsPath:    r.Spec.RootfsPath,
		VCPUs:         r.Spec.VCPUs,
		MemMiB:        r.Spec.MemMiB,
		NetworkSpec:   r.Status.NetworkSpec,
		SnapshotDir:   m.config.SnapshotDir,
		LogsDir:       m.config.LogsDir,
		BalloonMiB:    m.config.BalloonMiB,
		VsockUDSPath:  m.config.VsockUDSPath,
		VsockGuestCID: 3,
	}
	if m.config.EnableJailer {
		fcCfg.Jailer = jailArgs(m.config.JailerBin, r.Metadata.ID, m.config.FirecrackerBin, m.config.SocketDir, m.config.CgroupVersion)
	}

	if fcCfg.KernelPath == "" {
		fcCfg.KernelPath = m.config.KernelImage
	}
	if fcCfg.RootfsPath == "" {
		fcCfg.RootfsPath = m.config.RootfsPath
	}

	return m.fc.CreateAndStartVM(ctx, fcCfg)
}

// Stop stops a replica
func (m *Manager) Stop(ctx context.Context, r *resource.Replica) error {
	err := m.fc.StopVM(ctx, r.Metadata.ID)
	m.vmsMu.Lock()
	delete(m.vms, r.Metadata.ID)
	m.vmsMu.Unlock()
	return err
}

// Restart restarts a replica
func (m *Manager) Restart(ctx context.Context, r *resource.Replica) error {
	if err := m.Stop(ctx, r); err != nil {
		return err
	}
	time.Sleep(500 * time.Millisecond)
	return m.Boot(ctx, r)
}

// Delete deletes a replica
func (m *Manager) Delete(ctx context.Context, r *resource.Replica) error {
	return m.Stop(ctx, r)
}

// GetVM gets VM info
func (m *Manager) GetVM(ctx context.Context, vmID string) (*resource.Replica, error) {
	m.vmsMu.RLock()
	r, ok := m.vms[vmID]
	m.vmsMu.RUnlock()

	if !ok {
		// The runtime tracks VMs in its in-memory map; there is no
		// resource.Replica equivalent in the types-based store yet
		// (resource vs types split to reconcile).
		return nil, fmt.Errorf("vm %q not found in runtime", vmID)
	}
	return r, nil
}

// Snapshot creates a snapshot
func (m *Manager) Snapshot(ctx context.Context, r *resource.Replica) (SnapshotResult, error) {
	return m.fc.CreateSnapshot(ctx, r.Metadata.ID, m.config.SnapshotDir)
}

// Restore restores from snapshot
func (m *Manager) Restore(ctx context.Context, r *resource.Replica, snapPath, memPath string) error {
	return m.fc.LoadSnapshot(ctx, r.Metadata.ID, snapPath, memPath)
}

// Network returns the network manager
func (m *Manager) Network() *NetworkManager {
	return m.network
}

// Close cleans up
func (m *Manager) Close() {
	m.fc.Close()
}

// FCVMConfig is the Firecracker VM configuration
type FCVMConfig struct {
	VMID        string
	KernelPath  string
	RootfsPath  string
	VCPUs       int
	MemMiB      int
	NetworkSpec *resource.ReplicaNetworkSpec
	DataDrives  []FCDataDrive
	SnapshotDir string
	LogsDir     string
	// MMDSData is the guest metadata payload (MMDS V2 JSON). When non-nil it
	// is PUT to /mmds/config after the NIC exists (pre-boot only). Secrets
	// travel here decrypted at boot; never log the values.
	MMDSData map[string]any
	// Jailer argv (nil = direct boot). Built by jailArgs from FCConfig.
	Jailer []string
	// BalloonMiB installs the balloon device (0 = skip).
	BalloonMiB int
	// VsockUDSPath installs vsock pre-boot (empty = skip); guest CID auto >= 3.
	VsockUDSPath  string
	VsockGuestCID int
	// Rate limits (token bucket, manual §6 PATCH-tunable). Zero = disabled.
	// Applied at boot when > 0 so the guest can never exceed host policy.
	RxBandwidthKbps int
	TxBandwidthKbps int
	RxOpsPerSec     int
	TxOpsPerSec     int
}

// FCDataDrive is one persistent volume attached to the VM (G4). The first
// entry appears in the guest as /dev/vdb; volumes survive VM delete/restart.
type FCDataDrive struct {
	ID   string
	Path string
}

// SnapshotResult is the result of a snapshot operation
type SnapshotResult struct {
	SnapshotPath string
	MemoryPath   string
	CreatedAt    time.Time
}
