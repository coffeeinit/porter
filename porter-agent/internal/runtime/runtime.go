package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	defer logFile.Close()
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
		_ = killStartedProcess(cmd)
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
		_ = killStartedProcess(cmd)
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
		_ = killStartedProcess(cmd)
		e.deleteTap(ctx, tap)
		return vm, err
	}
	if err := putJSON(ctx, client, "/drives/rootfs", map[string]interface{}{"drive_id": "rootfs", "path_on_host": rootfs, "is_root_device": true, "is_read_only": false}); err != nil {
		_ = killStartedProcess(cmd)
		e.deleteTap(ctx, tap)
		return vm, err
	}
	if spec.VolumeID != "" {
		volPath := filepath.Join(e.BaseDir, "volumes", spec.VolumeID+".ext4")
		if err := ensureVolumeFile(volPath, spec.VolumeSizeMB); err != nil {
			_ = killStartedProcess(cmd)
			e.deleteTap(ctx, tap)
			return vm, err
		}
		if err := putJSON(ctx, client, "/drives/vdb", map[string]interface{}{"drive_id": "vdb", "path_on_host": volPath, "is_root_device": false, "is_read_only": false}); err != nil {
			_ = killStartedProcess(cmd)
			e.deleteTap(ctx, tap)
			return vm, err
		}
	}
	if tap != "" {
		if err := putJSON(ctx, client, "/network-interfaces/eth0", map[string]string{"iface_id": "eth0", "guest_mac": spec.GuestMAC, "host_dev_name": tap}); err != nil {
			_ = killStartedProcess(cmd)
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
		_ = killStartedProcess(cmd)
		e.deleteTap(ctx, tap)
		return vm, err
	}
	if err := putJSON(ctx, client, "/actions", map[string]string{"action_type": "InstanceStart"}); err != nil {
		_ = killStartedProcess(cmd)
		e.deleteTap(ctx, tap)
		return vm, err
	}
	vm.State = "running"
	return vm, nil
}

func (e *Engine) Stop(vm store.VM) error {
	if vm.PID > 0 {
		running, err := e.ProcessRunning(vm)
		if err != nil {
			return fmt.Errorf("verify Firecracker process before stop: %w", err)
		}
		if running {
			if err := syscall.Kill(vm.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
				return fmt.Errorf("send SIGTERM to Firecracker PID %d: %w", vm.PID, err)
			}
			if err := e.waitForProcessExit(vm, 5*time.Second); err != nil {
				running, checkErr := e.ProcessRunning(vm)
				if checkErr != nil {
					return fmt.Errorf("verify Firecracker process after SIGTERM: %w", checkErr)
				}
				if running {
					if killErr := syscall.Kill(vm.PID, syscall.SIGKILL); killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
						return fmt.Errorf("send SIGKILL to Firecracker PID %d: %w", vm.PID, killErr)
					}
					if waitErr := e.waitForProcessExit(vm, 2*time.Second); waitErr != nil {
						return fmt.Errorf("wait for Firecracker PID %d to exit: %w", vm.PID, waitErr)
					}
				}
			}
		}
		if err := reapChild(vm.PID); err != nil {
			return fmt.Errorf("reap Firecracker PID %d: %w", vm.PID, err)
		}
	}
	if vm.Socket != "" {
		if err := os.Remove(vm.Socket); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove Firecracker socket %q: %w", vm.Socket, err)
		}
	}
	return nil
}

func (e *Engine) Delete(ctx context.Context, vm store.VM) error {
	if err := e.Stop(vm); err != nil {
		return err
	}
	return e.deleteTap(ctx, vm.TapName)
}

func (e *Engine) waitForProcessExit(vm store.VM, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		running, err := e.ProcessRunning(vm)
		if err != nil {
			return err
		}
		if !running {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("timeout after %s", timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func reapChild(pid int) error {
	for {
		var status syscall.WaitStatus
		_, err := syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if errors.Is(err, syscall.ECHILD) {
			return nil
		}
		return err
	}
}

func killStartedProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	_, err := cmd.Process.Wait()
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
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
	if !safePathComponent(vm.ID, 128) {
		return "", "", 0, fmt.Errorf("invalid vm id")
	}
	if !safeSnapshotID(snapshotID) {
		return "", "", 0, fmt.Errorf("invalid snapshot id")
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
	running, err := e.ProcessRunning(vm)
	return err == nil && running && vm.Socket != ""
}

// ProcessRunning returns true only when the saved PID is running and its
// command line still identifies the expected Firecracker binary and socket.
// An identity-read failure is an error, not proof that it is safe to relaunch.
func (e *Engine) ProcessRunning(vm store.VM) (bool, error) {
	if vm.PID <= 0 {
		return false, nil
	}
	if vm.Socket == "" {
		return false, fmt.Errorf("saved API socket identity is missing")
	}
	if err := syscall.Kill(vm.PID, 0); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return false, nil
		}
		return false, fmt.Errorf("probe Firecracker PID %d: %w", vm.PID, err)
	}
	zombie, err := processIsZombie(vm.PID)
	if err != nil {
		return false, fmt.Errorf("inspect Firecracker PID %d state: %w", vm.PID, err)
	}
	if zombie {
		return false, nil
	}
	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", vm.PID))
	if err != nil {
		return false, fmt.Errorf("read Firecracker PID %d identity: %w", vm.PID, err)
	}
	if !commandMatchesFirecracker(cmdline, e.FirecrackerBin, vm.Socket) {
		return false, nil
	}
	return true, nil
}

func processIsZombie(pid int) (bool, error) {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	end := strings.LastIndexByte(string(stat), ')')
	if end < 0 {
		return false, fmt.Errorf("malformed process stat")
	}
	fields := strings.Fields(string(stat[end+1:]))
	if len(fields) == 0 {
		return false, fmt.Errorf("missing process state")
	}
	return fields[0] == "Z" || fields[0] == "X", nil
}

func commandMatchesFirecracker(cmdline []byte, binary, socket string) bool {
	args := strings.Split(string(cmdline), "\x00")
	if len(args) == 0 || args[0] == "" || filepath.Base(args[0]) != filepath.Base(binary) {
		return false
	}
	return commandUsesAPISocket(cmdline, socket)
}

func commandUsesAPISocket(cmdline []byte, socket string) bool {
	args := strings.Split(string(cmdline), "\x00")
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--api-sock" && args[i+1] == socket {
			return true
		}
	}
	return false
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

func (e *Engine) RestoreSnapshot(ctx context.Context, vm store.VM, statePath, memPath, stateSHA256, memSHA256 string) (store.VM, error) {
	if !safePathComponent(vm.ID, 128) {
		return vm, fmt.Errorf("invalid vm id")
	}
	stateID := strings.TrimSuffix(filepath.Base(statePath), ".snap")
	memID := strings.TrimSuffix(filepath.Base(memPath), ".mem")
	if !safeSnapshotID(stateID) || stateID != memID {
		return vm, fmt.Errorf("snapshot artifact names do not match")
	}
	dir := filepath.Join(e.BaseDir, vm.ID)
	if err := validateSnapshotArtifact(dir, statePath, ".snap"); err != nil {
		return vm, fmt.Errorf("invalid snapshot state file: %w", err)
	}
	if err := validateSnapshotArtifact(dir, memPath, ".mem"); err != nil {
		return vm, fmt.Errorf("invalid snapshot memory file: %w", err)
	}
	if err := verifySnapshotDigest(statePath, stateSHA256); err != nil {
		return vm, fmt.Errorf("invalid snapshot state digest: %w", err)
	}
	if err := verifySnapshotDigest(memPath, memSHA256); err != nil {
		return vm, fmt.Errorf("invalid snapshot memory digest: %w", err)
	}
	if err := e.Stop(vm); err != nil {
		return vm, fmt.Errorf("stop current VM before snapshot restore: %w", err)
	}
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

func safePathComponent(value string, maxLen int) bool {
	if value == "" || len(value) > maxLen || value == "." || value == ".." {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func safeSnapshotID(value string) bool { return safePathComponent(value, 64) }

func verifySnapshotDigest(path, expected string) error {
	if len(expected) != sha256.Size*2 {
		return fmt.Errorf("SHA-256 digest is missing or malformed")
	}
	for _, r := range expected {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return fmt.Errorf("SHA-256 digest is malformed")
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if actual != strings.ToLower(expected) {
		return fmt.Errorf("SHA-256 mismatch")
	}
	return nil
}

func validateSnapshotArtifact(dir, path, suffix string) error {
	if filepath.Ext(path) != suffix {
		return fmt.Errorf("expected %s extension", suffix)
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("VM snapshot directory must be a real directory")
	}
	file, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if filepath.Dir(file) != root {
		return fmt.Errorf("snapshot file is outside the VM directory")
	}
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	parentReal, err := filepath.EvalSymlinks(filepath.Dir(file))
	if err != nil {
		return err
	}
	if rootReal != parentReal {
		return fmt.Errorf("snapshot directory resolves outside the VM directory")
	}
	info, err := os.Lstat(file)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("snapshot artifact must be a regular, non-symlink file")
	}
	return nil
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
