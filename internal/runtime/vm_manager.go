// VM operations over the legacy types.VM model.
//
// VMManager is the adapter the REST API's vmEngine drives directly. It differs
// from Manager (resource.Replica based) because the API surfaces the types.VM
// projection; both share the same FCClient underneath.
package runtime

import (
	"context"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"

	"porter/internal/event"
	"porter/internal/guestagent"
	"porter/internal/netmgr"
	"porter/internal/resource"
	"porter/internal/secretbox"
	"porter/internal/store"
	"porter/internal/types"
)

// VMManager drives Firecracker lifecycle operations for a types.VM.
type VMManager struct {
	store     *store.Store
	config    FCConfig
	hub       *event.Hub
	fc        *FCClient
	net       *netmgr.NetManager
	subs      map[string]string // projectID -> allocated /24 subnet
	secretKey []byte
	mu        sync.Mutex
}

// SetSecretKey configures AES-256-GCM decryption for project secrets so Boot
// can inject them into the guest over MMDS. Empty key = injection skipped.
func (m *VMManager) SetSecretKey(key []byte) { m.secretKey = key }

// NewVMManager builds a VMManager bound to one Firecracker configuration.
// It keeps store + hub for future event emission and state reconciliation
// from a types.VM; today it drives Firecracker directly.
func NewVMManager(cfg FCConfig, st *store.Store, hub *event.Hub) *VMManager {
	return &VMManager{
		store:  st,
		config: cfg,
		hub:    hub,
		fc:     NewFCClient(cfg.FirecrackerBin, cfg.SocketDir),
		net:    netmgr.NewNetManager(),
		subs:   map[string]string{},
	}
}

// NetSpec allocates (or reuses) the per-project subnet and derives the
// per-VM boot identity for vm. It lets controllers boot a VM without
// re-implementing netmgr/IPAM bookkeeping.
func (m *VMManager) NetSpec(vm *types.VM) (netmgr.BootSpec, error) {
	if vm == nil {
		return netmgr.BootSpec{}, fmt.Errorf("NetSpec: nil vm")
	}
	m.mu.Lock()
	if _, ok := m.subs[vm.ProjectID]; !ok {
		m.subs[vm.ProjectID] = m.net.AllocateProjectSubnet()
	}
	subnet := m.subs[vm.ProjectID]
	use30 := m.config.UseNetwork30
	m.mu.Unlock()
	if use30 && vm.ReplicaIndex >= 0 && vm.ReplicaIndex <= 63 {
		var third int
		if _, err := fmt.Sscanf(subnet, "10.42.%d.", &third); err == nil {
			return netmgr.AllocateVMNetwork30(third, vm.ReplicaIndex, vm.ID)
		}
	}
	return m.net.AllocateVMNetwork(subnet, vm.ReplicaIndex, vm.ID)
}

// networkFor is intentionally absent: subnet allocation lives in the caller
// (netmgr + vmEngine), which derives the BootSpec it passes to Boot.
func (m *VMManager) fcConfigFor(vm *types.VM, spec *resource.ReplicaNetworkSpec) FCVMConfig {
	cfg := FCVMConfig{
		VMID:          vm.ID,
		VCPUs:         vm.VCPUs,
		MemMiB:        vm.MemMiB,
		NetworkSpec:   spec,
		SnapshotDir:   m.config.SnapshotDir,
		LogsDir:       m.config.LogsDir,
		BalloonMiB:    m.config.BalloonMiB,
		VsockUDSPath:  m.config.VsockUDSPath,
		VsockGuestCID: 3,
	}
	if m.config.EnableJailer {
		cfg.Jailer = jailArgs(m.config.JailerBin, vm.ID, m.config.FirecrackerBin, m.config.SocketDir, m.config.CgroupVersion)
	}
	cfg.KernelPath = vm.Kernel
	if cfg.KernelPath == "" {
		cfg.KernelPath = m.config.KernelImage
	}
	cfg.RootfsPath = vm.RootfsPath
	if cfg.RootfsPath == "" {
		cfg.RootfsPath = m.config.RootfsPath
	}
	return cfg
}

// Boot boots a types.VM as a Firecracker MicroVM. The caller (netmgr) already
// picked the subnet and derived the per-VM identity into spec; Boot just records
// the subnet for later allocations and hands the identity to Firecracker.
func (m *VMManager) Boot(vm *types.VM, spec netmgr.BootSpec) error {
	if vm == nil {
		return fmt.Errorf("Boot: nil vm")
	}
	if err := RequireMicroVMHost("VM boot"); err != nil {
		return err
	}
	netSpec := &resource.ReplicaNetworkSpec{
		Subnet:    spec.CIDR,
		IPAddress: ipFromCIDR(spec.CIDR),
		Gateway:   spec.GatewayAddr,
		MAC:       spec.MacAddress,
		TapName:   spec.HostDevName,
	}
	fcCfg := m.fcConfigFor(vm, netSpec)
	// G4: attach the VM's persistent volume (survives delete/restart).
	if vm.VolumeID != "" {
		if vol, ok := m.store.GetVolume(vm.VolumeID); ok && vol != nil && vol.Path != "" {
			fcCfg.DataDrives = []FCDataDrive{{ID: "vdb", Path: vol.Path}}
		}
	}
	// G4 secrets: decrypt project secrets and hand them to the guest via MMDS
	// V2 (never via boot args, env export in logs, or events).
	if len(m.secretKey) > 0 && m.store != nil && vm.ProjectID != "" {
		plain := map[string]string{}
		for _, sec := range m.store.ListSecrets(vm.ProjectID) {
			if sec == nil || sec.Name == "" || len(sec.ValueEncrypted) == 0 {
				continue
			}
			raw, err := secretbox.Open(m.secretKey, sec.ValueEncrypted)
			if err != nil {
				continue
			}
			plain[sec.Name] = string(raw)
		}
		if len(plain) > 0 {
			fcCfg.MMDSData = secretbox.MMDSPayload(vm.ProjectID, plain)
		}
	}
	return m.fc.CreateAndStartVM(context.Background(), fcCfg)
}

// Stop stops a running VM.
func (m *VMManager) Stop(vm *types.VM) error {
	if vm == nil {
		return fmt.Errorf("Stop: nil vm")
	}
	return m.fc.StopVM(context.Background(), vm.ID)
}

// Pause freezes a running VM without tearing it down (Firecracker PATCH /vm
// {"state":"Paused"}). The guest stops consuming CPU but keeps its memory.
func (m *VMManager) Pause(ctx context.Context, vm *types.VM) error {
	if vm == nil {
		return fmt.Errorf("Pause: nil vm")
	}
	return m.fc.SetVMState(ctx, vm.ID, "Paused")
}

// Resume unfreezes a VM previously paused by Pause.
func (m *VMManager) Resume(ctx context.Context, vm *types.VM) error {
	if vm == nil {
		return fmt.Errorf("Resume: nil vm")
	}
	return m.fc.SetVMState(ctx, vm.ID, "Resumed")
}

// Snapshot captures the VM state to the configured snapshot directory.
func (m *VMManager) Snapshot(ctx context.Context, vm *types.VM) (SnapshotResult, error) {
	if vm == nil {
		return SnapshotResult{}, fmt.Errorf("Snapshot: nil vm")
	}
	return m.fc.CreateSnapshot(ctx, vm.ID, m.config.SnapshotDir)
}

// Restore resumes a VM from a prior snapshot.
func (m *VMManager) Restore(ctx context.Context, vm *types.VM, snapPath, memPath string) error {
	if vm == nil {
		return fmt.Errorf("Restore: nil vm")
	}
	return m.fc.LoadSnapshot(ctx, vm.ID, snapPath, memPath)
}

// Exec runs a command inside a running VM over the guest-agent channel (G1).
// The agent socket appears at <socketDir>/<vmID>.agent.sock once the guest
// agent is present; without it Exec fails explicitly (never fakes success).
func (m *VMManager) Exec(ctx context.Context, vmID string, argv []string, stdin io.Reader, stdout io.Writer) error {
	if len(argv) == 0 {
		return fmt.Errorf("exec: no command given")
	}
	sockPath := filepath.Join(m.config.SocketDir, vmID+".agent.sock")
	client := guestagent.Client{
		Dial: func(dialCtx context.Context) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(dialCtx, "unix", sockPath)
		},
	}
	in := ""
	if stdin != nil {
		if b, err := io.ReadAll(stdin); err == nil {
			in = string(b)
		}
	}
	result, err := client.Exec(ctx, argv, in)
	if err != nil {
		// Distinguish "no agent" (expected until the guest image ships one)
		// from a failed command so callers surface explicit conditions.
		if isDialError(err) {
			return fmt.Errorf("guest agent not connected for vm %s (socket %s)", vmID, sockPath)
		}
		if stdout != nil {
			_, _ = io.WriteString(stdout, result.Stdout+result.Stderr)
		}
		return err
	}
	if stdout != nil {
		_, _ = io.WriteString(stdout, result.Stdout)
	}
	return nil
}

func isDialError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "connect:") || strings.Contains(s, "no such file") || strings.Contains(s, "dial ")
}

// Close tears down all tracked VMs.
func (m *VMManager) Close() { m.fc.Close() }

// ipFromCIDR strips the prefix from "10.42.1.5/24" -> "10.42.1.5".
func ipFromCIDR(cidr string) string {
	if i := strings.IndexByte(cidr, '/'); i >= 0 {
		return cidr[:i]
	}
	return cidr
}
