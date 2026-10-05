// Package runtime manages Firecracker MicroVM lifecycle.
package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"porter/internal/jailer"
)

// FCClient is the Firecracker HTTP API client
type FCClient struct {
	binPath    string
	socketDir  string
	httpClient *http.Client
	vms        map[string]*VMHandle
	mu         sync.Mutex
}

// VMHandle tracks a running VM
type VMHandle struct {
	VMID       string
	SocketPath string
	PID        int
	Cmd        *os.Process
	CreatedAt  time.Time
}

// NewFCClient creates a new Firecracker client
func NewFCClient(binPath, socketDir string) *FCClient {
	return &FCClient{
		binPath:    binPath,
		socketDir:  socketDir,
		vms:        make(map[string]*VMHandle),
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// CreateAndStartVM creates and starts a Firecracker VM
func (c *FCClient) CreateAndStartVM(ctx context.Context, cfg FCVMConfig) error {
	if err := RequireMicroVMHost("Firecracker boot"); err != nil {
		return err
	}
	socketPath := filepath.Join(c.socketDir, cfg.VMID+".sock")

	// Ensure socket directory exists
	if err := os.MkdirAll(c.socketDir, 0755); err != nil {
		return fmt.Errorf("create socket dir: %w", err)
	}

	// Start Firecracker process (jailer when configured, direct otherwise).
	// API-driven boot: no --config-file. Passing /dev/null made Firecracker
	// >= 1.x parse empty JSON, exit before serving, and the boot then failed
	// with "connection refused" on the first configure call.
	var cmd *exec.Cmd
	if len(cfg.Jailer) > 0 {
		cmd = exec.Command(cfg.Jailer[0], cfg.Jailer[1:]...)
	} else {
		cmd = exec.Command(c.binPath, "--api-sock", socketPath)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start firecracker: %w", err)
	}

	handle := &VMHandle{
		VMID:       cfg.VMID,
		SocketPath: socketPath,
		PID:        cmd.Process.Pid,
		Cmd:        cmd.Process,
		CreatedAt:  time.Now(),
	}

	c.mu.Lock()
	c.vms[cfg.VMID] = handle
	c.mu.Unlock()

	// Wait for socket to be ready
	if err := c.waitForSocket(ctx, socketPath); err != nil {
		cmd.Process.Kill()
		return fmt.Errorf("wait for socket: %w", err)
	}

	// Configure VM
	if err := c.configureVM(ctx, socketPath, cfg); err != nil {
		// The VMM is useless without full pre-boot config: tear it down and
		// remove the socket so the next attempt starts clean.
		cmd.Process.Kill()
		_ = os.Remove(socketPath)
		c.mu.Lock()
		delete(c.vms, cfg.VMID)
		c.mu.Unlock()
		return fmt.Errorf("configure VM: %w", err)
	}

	// Start VM
	if err := c.startVM(ctx, socketPath); err != nil {
		cmd.Process.Kill()
		_ = os.Remove(socketPath)
		c.mu.Lock()
		delete(c.vms, cfg.VMID)
		c.mu.Unlock()
		return fmt.Errorf("start VM: %w", err)
	}

	return nil
}

// waitForSocket waits for the Firecracker socket to be available. A socket
// file alone is not proof of a live VMM: a crashed Firecracker process leaves
// the file behind (a restart then "configures" a dead socket and fails with
// connection refused). Each poll therefore dial-tests the socket and clears
// stale files so the wait can only succeed against a listening VMM.
func (c *FCClient) waitForSocket(ctx context.Context, socketPath string) error {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			if conn, derr := net.DialTimeout("unix", socketPath, time.Second); derr == nil {
				_ = conn.Close()
				return nil
			}
			_ = os.Remove(socketPath)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("timeout waiting for socket %s", socketPath)
}

// configureVM configures the VM via Firecracker API
func (c *FCClient) configureVM(ctx context.Context, socketPath string, cfg FCVMConfig) error {
	// Boot source
	if err := c.put(ctx, socketPath, "/boot-source", map[string]string{
		"kernel_image_path": cfg.KernelPath,
		"boot_args":         fmt.Sprintf("console=ttyS0 reboot=k panic=1 pci=off nomodules ro root=/dev/vda %s", c.buildBootArgs(cfg)),
	}); err != nil {
		return err
	}

	// Root drive
	if err := c.put(ctx, socketPath, "/drives/rootfs", map[string]interface{}{
		"drive_id":       "rootfs",
		"path_on_host":   cfg.RootfsPath,
		"is_root_device": true,
		"is_read_only":   false,
	}); err != nil {
		return err
	}

	// Network
	if cfg.NetworkSpec != nil {
		if err := c.put(ctx, socketPath, "/network-interfaces/eth0", map[string]string{
			"iface_id":      "eth0",
			"guest_mac":     cfg.NetworkSpec.MAC,
			"host_dev_name": cfg.NetworkSpec.TapName,
		}); err != nil {
			return err
		}
	}

	// Persistent data drives (volumes independent of VM lifecycle, G4).
	// The first data drive is conventionally the VM's /dev/vdb.
	for _, d := range cfg.DataDrives {
		if err := c.put(ctx, socketPath, "/drives/"+d.ID, map[string]interface{}{
			"drive_id":       d.ID,
			"path_on_host":   d.Path,
			"is_root_device": false,
			"is_read_only":   false,
		}); err != nil {
			return err
		}
	}

	// MMDS metadata (V2, pre-boot only, needs the NIC above). Skipped when
	// no payload so VMs without secrets keep the exact prior call sequence.
	if len(cfg.MMDSData) > 0 {
		if err := c.putMMDSConfig(ctx, socketPath, cfg.MMDSData); err != nil {
			return err
		}
	}

	// Vsock (pre-boot only) + balloon (install-only amount). Both optional.
	if cfg.VsockUDSPath != "" {
		if err := c.putVsock(ctx, socketPath, cfg.VsockUDSPath, cfg.VsockGuestCID); err != nil {
			return err
		}
	}
	if cfg.BalloonMiB > 0 {
		if err := c.putBalloon(ctx, socketPath, cfg.BalloonMiB); err != nil {
			return err
		}
	}
	// Token-bucket rate limiters (post-NIC, pre-boot). Zero = disabled.
	if cfg.RxBandwidthKbps+cfg.TxBandwidthKbps+cfg.RxOpsPerSec+cfg.TxOpsPerSec > 0 {
		if err := c.putRateLimiter(ctx, socketPath, cfg.RxBandwidthKbps, cfg.TxBandwidthKbps, cfg.RxOpsPerSec, cfg.TxOpsPerSec); err != nil {
			return err
		}
	}

	// Machine config
	if err := c.put(ctx, socketPath, "/machine-config", map[string]int{
		"vcpu_count":   cfg.VCPUs,
		"mem_size_mib": cfg.MemMiB,
	}); err != nil {
		return err
	}

	return nil
}

// putMMDSConfig writes the MMDS V2 payload (PUT /mmds/config replaces all).
// Callers must pass already-redacted structure; values are never logged here.
func (c *FCClient) putMMDSConfig(ctx context.Context, socketPath string, data map[string]any) error {
	return c.put(ctx, socketPath, "/mmds/config", map[string]any{
		"version":            "V2",
		"network_interfaces": []string{"eth0"},
		"mmds_data":          data,
	})
}

// putBalloon installs the balloon device (manual §4). amountMiB = 0 disables.
func (c *FCClient) putBalloon(ctx context.Context, socketPath string, amountMiB int) error {
	return c.put(ctx, socketPath, "/balloon", map[string]any{
		"amount_mib":               amountMiB,
		"deflate_on_oom":           true,
		"stats_polling_interval_s": 10,
	})
}

// putVsock installs the vsock device pre-boot (manual §6: host CID 2).
func (c *FCClient) putVsock(ctx context.Context, socketPath, udsPath string, guestCID int) error {
	if guestCID < 3 {
		guestCID = 3
	}
	return c.put(ctx, socketPath, "/vsock", map[string]any{
		"guest_cid": guestCID,
		"uds_path":  udsPath,
	})
}

// putRateLimiter applies per-interface token buckets post-NIC (manual §6).
// Zero fields are omitted so unset limits stay disabled, never zero-capped.
func (c *FCClient) putRateLimiter(ctx context.Context, socketPath string, rxBW, txBW, rxOps, txOps int) error {
	bucket := func(bw, ops int) map[string]any {
		m := map[string]any{}
		if bw > 0 {
			m["bandwidth"] = map[string]any{"size": bw * 1024, "one_time_burst": bw * 1024, "refill_time": 1000}
		}
		if ops > 0 {
			m["ops"] = map[string]any{"size": ops, "one_time_burst": ops, "refill_time": 1000}
		}
		return m
	}
	body := map[string]any{"iface_id": "eth0"}
	if rx := bucket(rxBW, rxOps); len(rx) > 0 {
		body["rx_rate_limiter"] = rx
	}
	if tx := bucket(txBW, txOps); len(tx) > 0 {
		body["tx_rate_limiter"] = tx
	}
	if len(body) == 1 {
		return nil
	}
	return c.patch(ctx, socketPath, "/network-interfaces/eth0", body)
}

// jailArgs builds the jailer argv for one VM (manual §3). Empty when disabled
// so dev/Windows keeps direct boot.
func jailArgs(jailerBin, vmID, fcBin, socketDir, cgroupVer string) []string {
	if jailerBin == "" {
		return nil
	}
	sock := filepath.Join(socketDir, vmID+".sock")
	args, err := jailer.Config{
		Binary: jailerBin, VMID: vmID, Firecracker: fcBin,
		SocketPath: sock, CgroupVersion: cgroupVer,
	}.Args()
	if err != nil {
		return nil
	}
	return args
}

func (c *FCClient) buildBootArgs(cfg FCVMConfig) string {
	args := ""
	if cfg.NetworkSpec != nil {
		args += fmt.Sprintf(" ip=%s::%s:255.255.255.0::eth0:off", cfg.NetworkSpec.IPAddress, cfg.NetworkSpec.Gateway)
	}
	return args
}

// startVM starts the VM
func (c *FCClient) startVM(ctx context.Context, socketPath string) error {
	return c.put(ctx, socketPath, "/actions", map[string]string{
		"action_type": "InstanceStart",
	})
}

// SetVMState pauses or resumes a running VM through PATCH /vm. Firecracker
// accepts "Paused" and "Resumed"; this backs replica pause/resume. The VM must
// be tracked by this process, otherwise there is no socket to address and the
// error says so rather than silently reporting success.
func (c *FCClient) SetVMState(ctx context.Context, vmID, state string) error {
	c.mu.Lock()
	handle, ok := c.vms[vmID]
	c.mu.Unlock()
	if !ok {
		return fmt.Errorf("set vm state %q: vm %s is not running", state, vmID)
	}
	return c.patch(ctx, handle.SocketPath, "/vm", map[string]string{"state": state})
}

// StopVM stops a VM
func (c *FCClient) StopVM(ctx context.Context, vmID string) error {
	c.mu.Lock()
	handle, ok := c.vms[vmID]
	c.mu.Unlock()

	if !ok {
		return nil // Already stopped
	}

	// Send stop action
	if err := c.put(ctx, handle.SocketPath, "/actions", map[string]string{
		"action_type": "SendCtrlAltDel",
	}); err != nil {
		// Force kill if graceful stop fails
		handle.Cmd.Kill()
	}

	// Wait for process to exit
	done := make(chan error, 1)
	go func() {
		_, err := handle.Cmd.Wait()
		done <- err
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		handle.Cmd.Kill()
		<-done
	}

	c.mu.Lock()
	delete(c.vms, vmID)
	c.mu.Unlock()

	// Clear the API socket so a later boot cannot dial-test a dead endpoint.
	_ = os.Remove(handle.SocketPath)

	return nil
}

// CreateSnapshot creates a snapshot
func (c *FCClient) CreateSnapshot(ctx context.Context, vmID, snapshotDir string) (SnapshotResult, error) {
	c.mu.Lock()
	handle, ok := c.vms[vmID]
	c.mu.Unlock()

	if !ok {
		return SnapshotResult{}, fmt.Errorf("VM not found: %s", vmID)
	}

	snapPath := filepath.Join(snapshotDir, vmID+".snap")
	memPath := filepath.Join(snapshotDir, vmID+".mem")

	if err := c.put(ctx, handle.SocketPath, "/snapshot/create", map[string]string{
		"snapshot_type": "Full",
		"snapshot_path": snapPath,
		"mem_file_path": memPath,
	}); err != nil {
		return SnapshotResult{}, err
	}

	return SnapshotResult{
		SnapshotPath: snapPath,
		MemoryPath:   memPath,
		CreatedAt:    time.Now(),
	}, nil
}

// LoadSnapshot boots a VM from a full snapshot (memory + state files).
// The snapshot must come from an identical host/shape/paths configuration;
// resume is requested so the guest continues without a reboot.
func (c *FCClient) LoadSnapshot(ctx context.Context, vmID, snapPath, memPath string) error {
	if err := RequireMicroVMHost("Firecracker snapshot restore"); err != nil {
		return err
	}
	socketPath := filepath.Join(c.socketDir, vmID+".sock")

	if err := os.MkdirAll(c.socketDir, 0755); err != nil {
		return fmt.Errorf("create socket dir: %w", err)
	}

	cmd := exec.Command(c.binPath, "--api-sock", socketPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start firecracker: %w", err)
	}

	handle := &VMHandle{
		VMID:       vmID,
		SocketPath: socketPath,
		PID:        cmd.Process.Pid,
		Cmd:        cmd.Process,
		CreatedAt:  time.Now(),
	}

	c.mu.Lock()
	c.vms[vmID] = handle
	c.mu.Unlock()

	if err := c.waitForSocket(ctx, socketPath); err != nil {
		return fmt.Errorf("wait for socket: %w", err)
	}

	if err := c.put(ctx, socketPath, "/snapshot/load", map[string]interface{}{
		"snapshot_path": snapPath,
		"mem_backend": map[string]string{
			"backend_path": memPath,
			"backend_type": "File",
		},
		"resume_vm": true,
	}); err != nil {
		return err
	}

	return nil
}

// put sends a PUT request to Firecracker socket
func (c *FCClient) put(ctx context.Context, socketPath, path string, body interface{}) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		"http://localhost"+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("unix", socketPath)
		},
	}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("firecracker API error: %s", resp.Status)
	}

	return nil
}

// patch sends a PATCH request to the Firecracker socket (rate limiters, vm state).
func (c *FCClient) patch(ctx context.Context, socketPath, path string, body interface{}) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch,
		"http://localhost"+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("unix", socketPath)
		},
	}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("firecracker API error: %s", resp.Status)
	}
	return nil
}

// Close cleans up all VMs
func (c *FCClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, handle := range c.vms {
		handle.Cmd.Kill()
	}
	c.vms = make(map[string]*VMHandle)
}
