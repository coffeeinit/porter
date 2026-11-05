package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"porter/agent/internal/store"
	"porter/protocol"
)

const (
	vmMemoryMiB       = int64(1024 * 1024)
	vmMemoryOverhead  = int64(128) * vmMemoryMiB
	vmMaxTasks        = 64
	vmStopTimeout     = 5 * time.Second
	vmServiceStopTime = "2s"
)

type processSpec struct {
	VMID       string
	Binary     string
	Socket     string
	WorkDir    string
	LogPath    string
	KernelPath string
	RootfsPath string
	VolumePath string
	VCPUs      int
	MemoryMB   int
}

type processSupervisor interface {
	Start(context.Context, processSpec) (int, error)
	Status(context.Context, string) (pid int, active bool, err error)
	Stop(context.Context, string) error
}

type systemdSupervisor struct {
	SystemdRun string
	Systemctl  string
	PollEvery  time.Duration
	StartLimit time.Duration
	StopLimit  time.Duration
}

func newSystemdSupervisor() *systemdSupervisor {
	return &systemdSupervisor{
		SystemdRun: "systemd-run",
		Systemctl:  "systemctl",
		PollEvery:  50 * time.Millisecond,
		StartLimit: 10 * time.Second,
		StopLimit:  vmStopTimeout,
	}
}

type runtimeManifest struct {
	Version    int           `json:"version"`
	SpecHash   string        `json:"spec_hash"`
	KernelPath string        `json:"kernel_path"`
	RootfsPath string        `json:"rootfs_path"`
	VolumePath string        `json:"volume_path,omitempty"`
	Network    NetworkConfig `json:"network"`
	GuestMAC   string        `json:"guest_mac,omitempty"`
	GatewayIP  string        `json:"gateway_ip,omitempty"`
	BridgeCIDR string        `json:"bridge_cidr,omitempty"`
	VCPUs      int           `json:"vcpus"`
	MemoryMB   int           `json:"memory_mb"`
	VM         store.VM      `json:"vm"`
	UpdatedAt  time.Time     `json:"updated_at"`
}

func (e *Engine) vmLock(ctx context.Context, id string) (func(), error) {
	if !validVMID(id) {
		return nil, fmt.Errorf("invalid VM identity")
	}
	lockDir := filepath.Join(e.BaseDir, ".locks")
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(lockDir, id+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
				_ = file.Close()
			}, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			_ = file.Close()
			return nil, fmt.Errorf("lock VM %s: %w", id, err)
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func validVMID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for i, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || (i > 0 && (r == '-' || r == '_')) {
			continue
		}
		return false
	}
	return true
}

func vmUnitName(id string) string { return "porter-vm-" + id }

func normalizedSpec(spec protocol.VMApplySpec, defaultKernel, defaultRootfs string) (protocol.VMApplySpec, error) {
	if !validVMID(spec.VMID) {
		return spec, fmt.Errorf("vm id must be 1-64 letters, digits, or hyphens and start with a letter or digit")
	}
	if spec.VCPUs <= 0 {
		spec.VCPUs = 1
	}
	if spec.VCPUs > 256 {
		return spec, fmt.Errorf("vcpus must not exceed 256")
	}
	if spec.MemoryMB <= 0 {
		spec.MemoryMB = 256
	}
	if spec.MemoryMB > 1<<20 {
		return spec, fmt.Errorf("memory_mb must not exceed 1048576")
	}
	if spec.KernelPath == "" {
		spec.KernelPath = defaultKernel
	}
	if spec.RootfsPath == "" {
		spec.RootfsPath = defaultRootfs
	}
	if spec.KernelPath == "" || spec.RootfsPath == "" {
		return spec, fmt.Errorf("kernel and rootfs paths required")
	}
	if spec.TapName != "" && !validTapNameForVM(spec.VMID, spec.TapName) {
		return spec, fmt.Errorf("tap name does not match VM identity")
	}
	var err error
	if spec.KernelPath, err = filepath.Abs(spec.KernelPath); err != nil {
		return spec, fmt.Errorf("kernel path: %w", err)
	}
	if spec.RootfsPath, err = filepath.Abs(spec.RootfsPath); err != nil {
		return spec, fmt.Errorf("rootfs path: %w", err)
	}
	if spec.VolumeID != "" && !validVMID(spec.VolumeID) {
		return spec, fmt.Errorf("invalid volume id")
	}
	return spec, nil
}

func fingerprintSpec(spec protocol.VMApplySpec) (string, error) {
	data, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (e *Engine) vmDir(vmID string, create bool) (string, error) {
	if !validVMID(vmID) || e.BaseDir == "" {
		return "", fmt.Errorf("invalid VM runtime directory")
	}
	base, err := filepath.Abs(e.BaseDir)
	if err != nil {
		return "", fmt.Errorf("resolve VM base directory: %w", err)
	}
	if create {
		if err := os.MkdirAll(base, 0o700); err != nil {
			return "", fmt.Errorf("create VM base directory: %w", err)
		}
	}
	resolvedBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !create {
			return filepath.Join(base, vmID), nil
		}
		return "", fmt.Errorf("resolve VM base directory: %w", err)
	}
	baseInfo, err := os.Stat(resolvedBase)
	if err != nil {
		return "", err
	}
	if !baseInfo.IsDir() {
		return "", fmt.Errorf("VM base path is not a directory")
	}
	path := filepath.Join(resolvedBase, vmID)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && create {
		if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("create VM directory: %w", err)
		}
		info, err = os.Lstat(path)
	}
	if errors.Is(err, os.ErrNotExist) && !create {
		return path, nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("VM runtime path must be a real directory")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve VM directory: %w", err)
	}
	if filepath.Dir(resolved) != resolvedBase || filepath.Base(resolved) != vmID {
		return "", fmt.Errorf("VM directory resolves outside the configured base")
	}
	if create {
		if err := os.Chmod(resolved, 0o700); err != nil {
			return "", fmt.Errorf("secure VM directory permissions: %w", err)
		}
	}
	return resolved, nil
}

func (e *Engine) vmSocket(vmID string) (string, error) {
	dir, err := e.vmDir(vmID, false)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "firecracker.sock"), nil
}

func socketMatches(expected, supplied string) bool {
	if supplied == "" {
		return true
	}
	absolute, err := filepath.Abs(supplied)
	return err == nil && filepath.Clean(absolute) == filepath.Clean(expected)
}

func (e *Engine) manifestPath(vmID string, create bool) (string, error) {
	dir, err := e.vmDir(vmID, create)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "runtime.json"), nil
}

func (e *Engine) readManifest(vmID string) (runtimeManifest, error) {
	var m runtimeManifest
	path, err := e.manifestPath(vmID, false)
	if err != nil {
		return m, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("decode VM runtime state: %w", err)
	}
	if m.Version != 1 || m.VM.ID != vmID {
		return m, fmt.Errorf("unsupported or mismatched VM runtime state")
	}
	return m, nil
}

func (e *Engine) writeManifest(m runtimeManifest) error {
	path, err := e.manifestPath(m.VM.ID, true)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	m.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".runtime-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return nil
}

func (e *Engine) removeManifest(vmID string) error {
	path, err := e.manifestPath(vmID, false)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func systemdProperties(spec processSpec) ([]string, error) {
	if !validVMID(spec.VMID) {
		return nil, fmt.Errorf("invalid vm id")
	}
	if spec.VCPUs <= 0 || spec.VCPUs > 256 || spec.MemoryMB <= 0 || spec.MemoryMB > 1<<20 {
		return nil, fmt.Errorf("invalid VM resource limits")
	}
	limit := int64(spec.MemoryMB)*vmMemoryMiB + int64(spec.MemoryMB)*vmMemoryMiB/4 + vmMemoryOverhead
	if limit <= 0 {
		return nil, fmt.Errorf("memory limit overflow")
	}
	paths := []string{spec.WorkDir, spec.RootfsPath, spec.LogPath}
	if spec.VolumePath != "" {
		paths = append(paths, spec.VolumePath)
	}
	for _, path := range paths {
		if path == "" || !filepath.IsAbs(path) || strings.ContainsAny(path, "\n\r") {
			return nil, fmt.Errorf("invalid writable path")
		}
	}
	if spec.KernelPath == "" || !filepath.IsAbs(spec.KernelPath) || strings.ContainsAny(spec.KernelPath, "\n\r") ||
		spec.Socket == "" || !filepath.IsAbs(spec.Socket) || strings.ContainsAny(spec.Socket, "\n\r") {
		return nil, fmt.Errorf("invalid kernel path")
	}

	return []string{
		"CPUQuota=" + strconv.Itoa(spec.VCPUs*100) + "%",
		"MemoryMax=" + strconv.FormatInt(limit, 10),
		"TasksMax=" + strconv.Itoa(vmMaxTasks),
		"LimitNOFILE=1024",
		"TimeoutStopSec=" + vmServiceStopTime,
		"SendSIGKILL=yes",
		"KillMode=control-group",
		"Restart=no",
		"User=0",
		"Group=0",
		"NoNewPrivileges=yes",
		"CapabilityBoundingSet=CAP_NET_ADMIN",
		"AmbientCapabilities=CAP_NET_ADMIN",
		"DevicePolicy=closed",
		"DeviceAllow=/dev/kvm rw",
		"DeviceAllow=/dev/net/tun rw",
		"PrivateTmp=yes",
		"ProtectSystem=strict",
		"ProtectHome=read-only",
		"ProtectKernelTunables=yes",
		"ProtectKernelModules=yes",
		"ProtectKernelLogs=yes",
		"ProtectControlGroups=yes",
		"RestrictSUIDSGID=yes",
		"LockPersonality=yes",
		"RestrictRealtime=yes",
		"WorkingDirectory=" + systemdEscapeValue(spec.WorkDir),
		"ReadWritePaths=" + joinSystemdPaths(paths),
		"ReadOnlyPaths=" + systemdEscapeValue(spec.KernelPath),
		"StandardOutput=append:" + systemdEscapeValue(spec.LogPath),
		"StandardError=append:" + systemdEscapeValue(spec.LogPath),
	}, nil
}

func systemdEscapeValue(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, " ", `\s`)
	s = strings.ReplaceAll(s, "\t", `\t`)
	return s
}

func joinSystemdPaths(paths []string) string {
	values := make([]string, 0, len(paths))
	for _, path := range paths {
		values = append(values, systemdEscapeValue(path))
	}
	return strings.Join(values, " ")
}

func (s *systemdSupervisor) Start(ctx context.Context, spec processSpec) (int, error) {
	if s == nil {
		return 0, fmt.Errorf("systemd supervisor is not configured")
	}
	binary, err := exec.LookPath(spec.Binary)
	if err != nil {
		return 0, fmt.Errorf("find Firecracker binary: %w", err)
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return 0, err
	}
	spec.Binary = binary
	properties, err := systemdProperties(spec)
	if err != nil {
		return 0, err
	}
	unit := vmUnitName(spec.VMID)
	limit := s.StartLimit
	if limit <= 0 {
		limit = 10 * time.Second
	}
	startCtx, startCancel := context.WithTimeout(ctx, limit)
	defer startCancel()
	_, _ = exec.CommandContext(startCtx, s.Systemctl, "reset-failed", unit).CombinedOutput()
	args := []string{"--quiet", "--collect", "--no-block", "--service-type=exec", "--unit=" + unit}
	for _, property := range properties {
		args = append(args, "--property="+property)
	}
	args = append(args, "--", spec.Binary, "--api-sock", spec.Socket)
	out, err := exec.CommandContext(startCtx, s.SystemdRun, args...).CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("start VM systemd unit %s: %w: %s", unit, err, strings.TrimSpace(string(out)))
	}

	poll := s.PollEvery
	if poll <= 0 {
		poll = 50 * time.Millisecond
	}
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		pid, active, statusErr := s.Status(startCtx, spec.VMID)
		if statusErr == nil && active && pid > 0 {
			return pid, nil
		}
		if statusErr != nil {
			cleanupErr := s.Stop(context.Background(), spec.VMID)
			if cleanupErr != nil {
				return 0, fmt.Errorf("inspect VM service %s after launch: %w; cleanup failed: %v", unit, statusErr, cleanupErr)
			}
			return 0, fmt.Errorf("inspect VM service %s after launch: %w", unit, statusErr)
		}
		select {
		case <-startCtx.Done():
			cleanupErr := s.Stop(context.Background(), spec.VMID)
			if cleanupErr != nil {
				return 0, fmt.Errorf("%w; cleanup failed: %v", startCtx.Err(), cleanupErr)
			}
			return 0, startCtx.Err()
		case <-deadline.C:
			cleanupErr := s.Stop(context.Background(), spec.VMID)
			if cleanupErr != nil {
				return 0, fmt.Errorf("timed out waiting for VM service %s to start; cleanup failed: %v", unit, cleanupErr)
			}
			return 0, fmt.Errorf("timed out waiting for VM service %s to start", unit)
		case <-ticker.C:
		}
	}
}

func (s *systemdSupervisor) Status(ctx context.Context, vmID string) (int, bool, error) {
	if s == nil || !validVMID(vmID) {
		return 0, false, fmt.Errorf("invalid systemd VM identity")
	}
	unit := vmUnitName(vmID)
	infoOut, err := exec.CommandContext(ctx, s.Systemctl, "show", "--property=LoadState", "--property=ActiveState", "--property=MainPID", unit).CombinedOutput()
	if err != nil {
		message := strings.ToLower(string(infoOut))
		if strings.Contains(message, "no such unit") || strings.Contains(message, "could not be found") || strings.Contains(message, "not loaded") {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("inspect VM systemd unit %s: %w", unit, err)
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(infoOut), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[key] = value
		}
	}
	if values["LoadState"] == "not-found" || values["LoadState"] == "" {
		return 0, false, nil
	}
	state := values["ActiveState"]
	if state != "active" && state != "activating" && state != "reloading" && state != "deactivating" {
		return 0, false, nil
	}
	pid, err := strconv.Atoi(strings.TrimSpace(values["MainPID"]))
	if err != nil {
		return 0, false, fmt.Errorf("invalid MainPID for %s: %w", unit, err)
	}
	return pid, true, nil
}

func (s *systemdSupervisor) Stop(ctx context.Context, vmID string) error {
	if !validVMID(vmID) {
		return fmt.Errorf("invalid VM identity")
	}
	unit := vmUnitName(vmID)
	limit := s.StopLimit
	if limit <= 0 {
		limit = vmStopTimeout
	}
	deadline := time.Now().Add(limit)
	if parentDeadline, ok := ctx.Deadline(); ok && parentDeadline.Before(deadline) {
		deadline = parentDeadline
	}
	overallCtx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	stopCtx, stopCancel := context.WithTimeout(overallCtx, 2500*time.Millisecond)
	out, stopErr := exec.CommandContext(stopCtx, s.Systemctl, "stop", unit).CombinedOutput()
	stopCancel()
	_, active, statusErr := s.Status(overallCtx, vmID)
	if statusErr != nil {
		if stopErr != nil {
			return fmt.Errorf("stop VM unit %s: %w: %s (status: %v)", unit, stopErr, strings.TrimSpace(string(out)), statusErr)
		}
		return fmt.Errorf("verify VM unit %s stop: %w", unit, statusErr)
	}
	if !active {
		// The transient unit may already have been collected by systemd.
		return nil
	}

	killCtx, killCancel := context.WithTimeout(overallCtx, 500*time.Millisecond)
	killOut, killErr := exec.CommandContext(killCtx, s.Systemctl, "kill", "--kill-whom=all", "--signal=KILL", unit).CombinedOutput()
	killCancel()
	if killErr != nil {
		_, stillActive, verifyErr := s.Status(overallCtx, vmID)
		if verifyErr == nil && !stillActive {
			return nil
		}
		return fmt.Errorf("force-stop VM unit %s: %w: %s (status: %v)", unit, killErr, strings.TrimSpace(string(killOut)), verifyErr)
	}
	forceCtx, forceCancel := context.WithTimeout(overallCtx, time.Second)
	_, forceErr := exec.CommandContext(forceCtx, s.Systemctl, "stop", unit).CombinedOutput()
	forceCancel()
	_, stillActive, verifyErr := s.Status(overallCtx, vmID)
	if verifyErr != nil || stillActive {
		return fmt.Errorf("VM unit %s did not stop after SIGKILL (stop error: %v, status error: %v)", unit, forceErr, verifyErr)
	}
	return nil
}

func (e *Engine) supervisorOrDefault() processSupervisor {
	if e.supervisor != nil {
		return e.supervisor
	}
	return newSystemdSupervisor()
}
