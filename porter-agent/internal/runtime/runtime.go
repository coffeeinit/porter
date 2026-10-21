package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"porter/agent/internal/store"
	"porter/protocol"
)

type Engine struct {
	BaseDir        string
	FirecrackerBin string
	Kernel         string
	Rootfs         string
	Network        NetworkConfig
	InitPath       string
}

func New(baseDir, kernel, rootfs string, network NetworkConfig) *Engine {
	return &Engine{BaseDir: baseDir, FirecrackerBin: "firecracker", Kernel: kernel, Rootfs: rootfs, Network: network}
}

func (e *Engine) Apply(ctx context.Context, spec protocol.VMApplySpec) (store.VM, error) {
	if spec.VMID == "" {
		return store.VM{}, fmt.Errorf("vm id required")
	}
	dir := filepath.Join(e.BaseDir, spec.VMID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return store.VM{}, err
	}
	sock := filepath.Join(dir, "firecracker.sock")
	_ = os.Remove(sock)
	logPath := filepath.Join(dir, "firecracker.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return store.VM{}, err
	}
	tap, netErr := e.ensureNetwork(ctx, &spec)
	if netErr != nil {
		logFile.Close()
		return store.VM{}, fmt.Errorf("network: %w", netErr)
	}
	cmd := exec.Command(e.FirecrackerBin, "--api-sock", sock)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		e.deleteTap(ctx, tap)
		return store.VM{}, err
	}
	vm := store.VM{ID: spec.VMID, ReplicaID: spec.ReplicaID, State: "booting", PID: cmd.Process.Pid, Socket: sock, TapName: tap}
	if err := e.waitSocket(ctx, sock, 10*time.Second); err != nil {
		_ = cmd.Process.Kill()
		e.deleteTap(ctx, tap)
		return vm, err
	}
	client := socketClient(sock)
	kernel := spec.KernelPath
	if kernel == "" {
		kernel = e.Kernel
	}
	rootfs := spec.RootfsPath
	if rootfs == "" {
		rootfs = e.Rootfs
	}
	if kernel == "" || rootfs == "" {
		_ = cmd.Process.Kill()
		e.deleteTap(ctx, tap)
		return vm, fmt.Errorf("kernel and rootfs paths required")
	}
	bootArgs := "console=ttyS0 reboot=k panic=1 pci=off root=/dev/vda ro"
	if e.InitPath != "" {
		bootArgs += " init=" + e.InitPath
	}
	if spec.GuestIP != "" && spec.GatewayIP != "" {
		bootArgs += " ip=" + spec.GuestIP + "::" + spec.GatewayIP + ":" + netmask(spec.BridgeCIDR) + "::eth0:off"
	}
	if err := putJSON(ctx, client, "/boot-source", map[string]string{"kernel_image_path": kernel, "boot_args": bootArgs}); err != nil {
		_ = cmd.Process.Kill()
		e.deleteTap(ctx, tap)
		return vm, err
	}
	if err := putJSON(ctx, client, "/drives/rootfs", map[string]interface{}{"drive_id": "rootfs", "path_on_host": rootfs, "is_root_device": true, "is_read_only": false}); err != nil {
		_ = cmd.Process.Kill()
		e.deleteTap(ctx, tap)
		return vm, err
	}
	if spec.VolumeID != "" {
		volPath := filepath.Join(e.BaseDir, "volumes", spec.VolumeID+".ext4")
		if err := ensureVolumeFile(volPath, spec.VolumeSizeMB); err != nil {
			_ = cmd.Process.Kill()
			e.deleteTap(ctx, tap)
			return vm, err
		}
		if err := putJSON(ctx, client, "/drives/vdb", map[string]interface{}{"drive_id": "vdb", "path_on_host": volPath, "is_root_device": false, "is_read_only": false}); err != nil {
			_ = cmd.Process.Kill()
			e.deleteTap(ctx, tap)
			return vm, err
		}
	}
	if tap != "" {
		if err := putJSON(ctx, client, "/network-interfaces/eth0", map[string]string{"iface_id": "eth0", "guest_mac": spec.GuestMAC, "host_dev_name": tap}); err != nil {
			_ = cmd.Process.Kill()
			e.deleteTap(ctx, tap)
			return vm, err
		}
	}
	vcpus := spec.VCPUs
	if vcpus <= 0 {
		vcpus = 1
	}
	mem := spec.MemoryMB
	if mem <= 0 {
		mem = 256
	}
	if err := putJSON(ctx, client, "/machine-config", map[string]int{"vcpu_count": vcpus, "mem_size_mib": mem}); err != nil {
		_ = cmd.Process.Kill()
		e.deleteTap(ctx, tap)
		return vm, err
	}
	if err := putJSON(ctx, client, "/actions", map[string]string{"action_type": "InstanceStart"}); err != nil {
		_ = cmd.Process.Kill()
		e.deleteTap(ctx, tap)
		return vm, err
	}
	vm.State = "running"
	return vm, nil
}

func (e *Engine) Stop(vm store.VM) error {
	if vm.PID > 0 {
		_ = syscall.Kill(vm.PID, syscall.SIGTERM)
	}
	if vm.Socket != "" {
		_ = os.Remove(vm.Socket)
	}
	return nil
}

func (e *Engine) Delete(ctx context.Context, vm store.VM) error {
	_ = e.Stop(vm)
	e.deleteTap(ctx, vm.TapName)
	return nil
}

func (e *Engine) waitSocket(ctx context.Context, path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", path, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return fmt.Errorf("timed out waiting for firecracker socket %s", path)
}

func socketClient(sock string) *http.Client {
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
}

func putJSON(ctx context.Context, c *http.Client, path string, payload interface{}) error {
	return methodJSON(ctx, c, http.MethodPut, path, payload)
}

func methodJSON(ctx context.Context, c *http.Client, method, path string, payload interface{}) error {
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
		return fmt.Errorf("firecracker %s: %s %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func ensureVolumeFile(path string, sizeMB int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if sizeMB <= 0 {
		sizeMB = 1024
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Size() < int64(sizeMB)*1024*1024 {
		return f.Truncate(int64(sizeMB) * 1024 * 1024)
	}
	return nil
}

func (e *Engine) Snapshot(ctx context.Context, vm store.VM, snapshotID string) (string, string, int64, error) {
	if vm.Socket == "" {
		return "", "", 0, fmt.Errorf("vm socket missing")
	}
	dir := filepath.Join(e.BaseDir, vm.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", 0, err
	}
	statePath := filepath.Join(dir, snapshotID+".snap")
	memPath := filepath.Join(dir, snapshotID+".mem")
	client := socketClient(vm.Socket)
	if err := methodJSON(ctx, client, http.MethodPatch, "/vm", map[string]string{"state": "Paused"}); err != nil {
		return "", "", 0, err
	}
	defer func() {
		_ = methodJSON(context.Background(), client, http.MethodPatch, "/vm", map[string]string{"state": "Resumed"})
	}()
	if err := putJSON(ctx, client, "/snapshot/create", map[string]string{"snapshot_type": "Full", "snapshot_path": statePath, "mem_file_path": memPath}); err != nil {
		return "", "", 0, err
	}
	var size int64
	for _, p := range []string{statePath, memPath} {
		if fi, err := os.Stat(p); err == nil {
			size += fi.Size()
		}
	}
	return statePath, memPath, size, nil
}

func (e *Engine) Healthy(vm store.VM) bool {
	if vm.PID <= 0 {
		return false
	}
	return syscall.Kill(vm.PID, 0) == nil
}

func (e *Engine) TailLog(vm store.VM, maxLines int) []string {
	path := filepath.Join(e.BaseDir, vm.ID, "firecracker.log")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return lines
}

func netmask(cidr string) string {
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		return "255.255.0.0"
	}
	return net.IP(n.Mask).String()
}

func (e *Engine) RestoreSnapshot(ctx context.Context, vm store.VM, statePath, memPath string) (store.VM, error) {
	_ = e.Stop(vm)
	dir := filepath.Join(e.BaseDir, vm.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return vm, err
	}
	sock := filepath.Join(dir, "firecracker.sock")
	_ = os.Remove(sock)
	logFile, err := os.Create(filepath.Join(dir, "firecracker-restore.log"))
	if err != nil {
		return vm, err
	}
	cmd := exec.Command(e.FirecrackerBin, "--api-sock", sock)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return vm, err
	}
	if err := e.waitSocket(ctx, sock, 10*time.Second); err != nil {
		_ = cmd.Process.Kill()
		return vm, err
	}
	client := socketClient(sock)
	if err := putJSON(ctx, client, "/snapshot/load", map[string]interface{}{"snapshot_path": statePath, "mem_file_path": memPath, "enable_diff_snapshots": false}); err != nil {
		_ = cmd.Process.Kill()
		return vm, err
	}
	_ = methodJSON(ctx, client, http.MethodPatch, "/vm", map[string]string{"state": "Resumed"})
	return store.VM{ID: vm.ID, ReplicaID: vm.ReplicaID, State: "running", PID: cmd.Process.Pid, Socket: sock, TapName: vm.TapName, GuestIP: vm.GuestIP, Port: vm.Port, Health: "checking"}, nil
}

func (e *Engine) ResizeVolume(ctx context.Context, volumeID string, sizeMB int) error {
	if volumeID == "" || sizeMB <= 0 {
		return fmt.Errorf("volume id and size required")
	}
	path := filepath.Join(e.BaseDir, "volumes", volumeID+".ext4")
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Truncate(int64(sizeMB) * 1024 * 1024)
}
