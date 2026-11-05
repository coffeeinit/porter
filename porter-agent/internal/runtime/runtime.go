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
	supervisor     processSupervisor
}

func New(baseDir, kernel, rootfs string, network NetworkConfig) *Engine {
	if abs, err := filepath.Abs(baseDir); err == nil {
		baseDir = abs
	}
	return &Engine{BaseDir: baseDir, FirecrackerBin: "firecracker", Kernel: kernel, Rootfs: rootfs, Network: network, supervisor: newSystemdSupervisor()}
}

func (e *Engine) Apply(ctx context.Context, spec protocol.VMApplySpec) (store.VM, error) {
	return e.apply(ctx, spec, false)
}

func (e *Engine) Start(ctx context.Context, spec protocol.VMApplySpec) (store.VM, error) {
	return e.apply(ctx, spec, false)
}

func (e *Engine) Restart(ctx context.Context, spec protocol.VMApplySpec) (store.VM, error) {
	return e.apply(ctx, spec, true)
}

func (e *Engine) apply(ctx context.Context, spec protocol.VMApplySpec, forceRestart bool) (store.VM, error) {
	var err error
	if spec, err = normalizedSpec(spec, e.Kernel, e.Rootfs); err != nil {
		return store.VM{ID: spec.VMID, ReplicaID: spec.ReplicaID, State: "failed", Error: err.Error()}, err
	}
	unlock, err := e.vmLock(ctx, spec.VMID)
	if err != nil {
		return store.VM{ID: spec.VMID, ReplicaID: spec.ReplicaID, State: "failed"}, err
	}
	defer unlock()

	dir, err := e.vmDir(spec.VMID, true)
	if err != nil {
		return store.VM{ID: spec.VMID, ReplicaID: spec.ReplicaID, State: "failed"}, err
	}
	sock := filepath.Join(dir, "firecracker.sock")
	manager := e.supervisorOrDefault()
	fingerprint, err := fingerprintSpec(spec)
	if err != nil {
		return store.VM{}, err
	}
	if pid, active, statusErr := manager.Status(ctx, spec.VMID); statusErr != nil {
		return store.VM{ID: spec.VMID, ReplicaID: spec.ReplicaID, State: "failed"}, statusErr
	} else if active {
		manifest, readErr := e.readManifest(spec.VMID)
		if readErr != nil {
			return store.VM{ID: spec.VMID, ReplicaID: spec.ReplicaID, State: "failed"}, fmt.Errorf("VM service %s is already active but cannot be safely adopted: %w", vmUnitName(spec.VMID), readErr)
		}
		if manifest.SpecHash != fingerprint {
			return manifest.VM, fmt.Errorf("VM %s is already active with a different specification; stop it before applying changes", spec.VMID)
		}
		if forceRestart {
			if err := validatePrivateRootfs(dir, manifest); err != nil {
				return manifest.VM, fmt.Errorf("refusing to restart VM with a non-private rootfs: %w", err)
			}
			if err := e.stopRuntime(manifest.VM, true, false); err != nil {
				return manifest.VM, fmt.Errorf("stop VM before restart: %w", err)
			}
		} else {
			if err := e.waitSocket(ctx, sock, 10*time.Second); err != nil {
				return manifest.VM, fmt.Errorf("adopt active VM %s: %w", spec.VMID, err)
			}
			pid, active, err = manager.Status(ctx, spec.VMID)
			if err != nil || !active || pid <= 0 {
				if err == nil {
					err = fmt.Errorf("VM systemd unit stopped during adoption")
				}
				return manifest.VM, err
			}
			vm := manifest.VM
			vm.PID, vm.State, vm.Error = pid, "running", ""
			manifest.VM = vm
			if err := e.writeManifest(manifest); err != nil {
				return vm, fmt.Errorf("persist adopted VM state: %w", err)
			}
			return vm, nil
		}
	}

	// Keep the prior manifest long enough to establish ownership of an existing
	// private root disk across restart. A root file without that proof is a collision.
	priorManifest, priorManifestErr := e.readManifest(spec.VMID)
	if priorManifestErr != nil && !errors.Is(priorManifestErr, os.ErrNotExist) {
		return store.VM{ID: spec.VMID, ReplicaID: spec.ReplicaID, State: "failed"}, fmt.Errorf("refusing to replace unreadable VM runtime state: %w", priorManifestErr)
	}
	if priorManifestErr == nil {
		if err := validatePrivateRootfs(dir, priorManifest); err != nil {
			return store.VM{ID: spec.VMID, ReplicaID: spec.ReplicaID, State: "failed"}, fmt.Errorf("refusing to reapply VM with a non-private rootfs: %w", err)
		}
	}
	// The unit and socket are stale, but keep the old manifest as the ownership
	// proof for its writable rootfs until writeManifest atomically replaces it.
	if err := removeIfPresent(sock); err != nil {
		return store.VM{ID: spec.VMID, ReplicaID: spec.ReplicaID, State: "failed"}, fmt.Errorf("remove stale VM socket: %w", err)
	}
	for _, item := range []struct{ name, path string }{{"kernel", spec.KernelPath}, {"rootfs", spec.RootfsPath}} {
		fi, statErr := os.Lstat(item.path)
		if statErr != nil {
			return store.VM{ID: spec.VMID, ReplicaID: spec.ReplicaID, State: "failed"}, fmt.Errorf("%s image %q: %w", item.name, item.path, statErr)
		}
		if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
			return store.VM{ID: spec.VMID, ReplicaID: spec.ReplicaID, State: "failed"}, fmt.Errorf("%s image %q is not a regular file", item.name, item.path)
		}
	}
	volumePath := ""
	if spec.VolumeID != "" {
		volumePath = filepath.Join(e.BaseDir, "volumes", spec.VolumeID+".ext4")
		if err := ensureVolumeFile(volumePath, spec.VolumeSizeMB); err != nil {
			return store.VM{ID: spec.VMID, ReplicaID: spec.ReplicaID, State: "failed"}, err
		}
	}
	tap, netErr := e.ensureNetwork(ctx, &spec)
	if netErr != nil {
		return store.VM{ID: spec.VMID, ReplicaID: spec.ReplicaID, State: "failed"}, fmt.Errorf("network: %w", netErr)
	}
	if tap == "" {
		tap = spec.TapName
	}
	vm := store.VM{ID: spec.VMID, ReplicaID: spec.ReplicaID, State: "booting", Socket: sock, TapName: tap, GuestIP: spec.GuestIP, Port: spec.Port, Health: "checking"}
	logPath := filepath.Join(dir, "firecracker.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return vm, e.cleanupCreatedTap(ctx, spec.VMID, tap, true, err)
	}
	if err := logFile.Close(); err != nil {
		return vm, e.cleanupCreatedTap(ctx, spec.VMID, tap, true, err)
	}
	privateRootfs, identity, cloneErr := e.ensurePrivateRootfs(dir, spec.RootfsPath, priorManifest, priorManifestErr == nil)
	if cloneErr != nil {
		return vm, e.cleanupCreatedTap(ctx, spec.VMID, tap, true, cloneErr)
	}
	manifest := runtimeManifest{
		Version: 1, SpecHash: fingerprint, KernelPath: spec.KernelPath, RootfsPath: privateRootfs, RootfsIdentity: identity,
		VolumePath: volumePath, Network: e.Network, GuestMAC: spec.GuestMAC, GatewayIP: spec.GatewayIP,
		BridgeCIDR: spec.BridgeCIDR, VCPUs: spec.VCPUs, MemoryMB: spec.MemoryMB, VM: vm,
	}
	if err := e.writeManifest(manifest); err != nil {
		return vm, e.cleanupCreatedTap(ctx, spec.VMID, tap, true, err)
	}
	pid, err := manager.Start(ctx, processSpec{
		VMID: spec.VMID, Binary: e.FirecrackerBin, Socket: sock, WorkDir: dir, LogPath: logPath,
		KernelPath: spec.KernelPath, RootfsPath: privateRootfs, VolumePath: volumePath,
		VCPUs: spec.VCPUs, MemoryMB: spec.MemoryMB,
	})
	if err != nil {
		return e.failApply(vm, err)
	}
	vm.PID = pid
	manifest.VM = vm
	if err := e.writeManifest(manifest); err != nil {
		return e.failApply(vm, fmt.Errorf("persist started VM state: %w", err))
	}
	if err := e.waitSocket(ctx, sock, 10*time.Second); err != nil {
		return e.failApply(vm, err)
	}
	client := socketClient(sock)
	bootArgs := "console=ttyS0 reboot=k panic=1 pci=off root=/dev/vda ro"
	if e.InitPath != "" {
		bootArgs += " init=" + e.InitPath
	}
	if spec.GuestIP != "" && spec.GatewayIP != "" {
		bootArgs += " ip=" + spec.GuestIP + "::" + spec.GatewayIP + ":" + netmask(spec.BridgeCIDR) + "::eth0:off"
	}
	if err := putJSON(ctx, client, "/boot-source", map[string]string{"kernel_image_path": spec.KernelPath, "boot_args": bootArgs}); err != nil {
		return e.failApply(vm, err)
	}
	if err := putJSON(ctx, client, "/drives/rootfs", map[string]interface{}{"drive_id": "rootfs", "path_on_host": privateRootfs, "is_root_device": true, "is_read_only": false}); err != nil {
		return e.failApply(vm, err)
	}
	if spec.VolumeID != "" {
		if err := putJSON(ctx, client, "/drives/vdb", map[string]interface{}{"drive_id": "vdb", "path_on_host": volumePath, "is_root_device": false, "is_read_only": false}); err != nil {
			return e.failApply(vm, err)
		}
	}
	if tap != "" {
		if err := putJSON(ctx, client, "/network-interfaces/eth0", map[string]string{"iface_id": "eth0", "guest_mac": spec.GuestMAC, "host_dev_name": tap}); err != nil {
			return e.failApply(vm, err)
		}
	}
	if err := putJSON(ctx, client, "/machine-config", map[string]int{"vcpu_count": spec.VCPUs, "mem_size_mib": spec.MemoryMB}); err != nil {
		return e.failApply(vm, err)
	}
	if err := putJSON(ctx, client, "/actions", map[string]string{"action_type": "InstanceStart"}); err != nil {
		return e.failApply(vm, err)
	}
	vm.State, vm.Error = "running", ""
	manifest.VM = vm
	if err := e.writeManifest(manifest); err != nil {
		return e.failApply(vm, fmt.Errorf("persist running VM state: %w", err))
	}
	return vm, nil
}

func (e *Engine) failApply(vm store.VM, cause error) (store.VM, error) {
	cleanupErr := e.stopRuntime(vm, true, false)
	vm.State = "failed"
	vm.Error = cause.Error()
	vm.PID, vm.Socket = 0, ""
	if cleanupErr != nil {
		vm.Error += "; cleanup failed: " + cleanupErr.Error()
	}
	if manifest, err := e.readManifest(vm.ID); err == nil {
		manifest.VM = vm
		if err := e.writeManifest(manifest); err != nil {
			vm.Error += "; persist failure state: " + err.Error()
		}
	} else if !os.IsNotExist(err) {
		vm.Error += "; read failure state: " + err.Error()
	}
	if cleanupErr != nil {
		return vm, fmt.Errorf("%s", vm.Error)
	}
	return vm, cause
}

func (e *Engine) Stop(vm store.VM) error {
	if e.supervisor == nil {
		if e.BaseDir != "" {
			sock, err := e.vmSocket(vm.ID)
			if err != nil {
				return err
			}
			if !socketMatches(sock, vm.Socket) {
				return fmt.Errorf("VM socket path does not match the configured runtime directory")
			}
			vm.Socket = sock
		}
		if vm.PID > 0 {
			running, err := e.ProcessRunning(vm)
			if err != nil {
				return fmt.Errorf("verify Firecracker process before stop: %w", err)
			}
			if running {
				if err := syscall.Kill(vm.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
					return err
				}
				if err := e.waitForProcessExit(vm, 5*time.Second); err != nil {
					running, checkErr := e.ProcessRunning(vm)
					if checkErr != nil {
						return checkErr
					}
					if running {
						if err := syscall.Kill(vm.PID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
							return err
						}
						if err := e.waitForProcessExit(vm, 2*time.Second); err != nil {
							return err
						}
					}
				}
			}
			if err := reapChild(vm.PID); err != nil {
				return err
			}
		}
		return removeIfPresent(vm.Socket)
	}
	if !validVMID(vm.ID) {
		return fmt.Errorf("invalid VM identity")
	}
	ctx, cancel := context.WithTimeout(context.Background(), vmStopTimeout+2*time.Second)
	defer cancel()
	unlock, err := e.vmLock(ctx, vm.ID)
	if err != nil {
		return err
	}
	defer unlock()
	return e.stopRuntime(vm, true, false)
}

func (e *Engine) Delete(ctx context.Context, vm store.VM) error {
	if !validVMID(vm.ID) {
		return fmt.Errorf("invalid VM identity")
	}
	unlock, err := e.vmLock(ctx, vm.ID)
	if err != nil {
		return err
	}
	defer unlock()
	manifest, manifestErr := e.readManifest(vm.ID)
	if manifestErr != nil && !errors.Is(manifestErr, os.ErrNotExist) {
		return fmt.Errorf("read VM runtime state before delete: %w", manifestErr)
	}
	privateRootfs := ""
	if manifestErr == nil {
		if dir, err := e.vmDir(vm.ID, false); err != nil {
			return err
		} else if validateErr := validatePrivateRootfs(dir, manifest); validateErr == nil {
			privateRootfs = manifest.RootfsPath
		}
	}
	if err := e.stopRuntime(vm, true, false); err != nil {
		return err
	}
	if privateRootfs != "" {
		if err := removeIfPresent(privateRootfs); err != nil {
			return fmt.Errorf("remove private VM rootfs: %w", err)
		}
	}
	return e.removeManifest(vm.ID)
}

func (e *Engine) stopRuntime(vm store.VM, removeTap, removeState bool) error {
	if !validVMID(vm.ID) {
		return fmt.Errorf("invalid VM identity")
	}
	sock, err := e.vmSocket(vm.ID)
	if err != nil {
		return err
	}
	if !socketMatches(sock, vm.Socket) {
		return fmt.Errorf("VM socket path does not match the configured runtime directory")
	}
	manager := e.supervisorOrDefault()
	ctx, cancel := context.WithTimeout(context.Background(), vmStopTimeout+2*time.Second)
	defer cancel()
	statusCtx, statusCancel := context.WithTimeout(ctx, time.Second)
	_, active, err := manager.Status(statusCtx, vm.ID)
	statusCancel()
	if err != nil {
		return fmt.Errorf("inspect VM %s before stop: %w", vm.ID, err)
	}
	if active {
		if err := manager.Stop(ctx, vm.ID); err != nil {
			return fmt.Errorf("stop VM %s: %w", vm.ID, err)
		}
	}
	if err := removeIfPresent(sock); err != nil {
		return fmt.Errorf("remove VM socket: %w", err)
	}
	if removeState {
		if err := e.removeManifest(vm.ID); err != nil {
			return fmt.Errorf("remove VM runtime state: %w", err)
		}
	} else {
		manifest, err := e.readManifest(vm.ID)
		if err == nil {
			manifest.VM.State, manifest.VM.PID, manifest.VM.Socket, manifest.VM.Error = "stopped", 0, sock, ""
			manifest.VM.Health = "unknown"
			if err := e.writeManifest(manifest); err != nil {
				return fmt.Errorf("persist stopped VM state: %w", err)
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("read VM runtime state during stop: %w", err)
		}
	}
	if removeTap && vm.TapName != "" {
		if err := e.deleteTap(ctx, vm.ID, vm.TapName); err != nil {
			return err
		}
	}
	return nil
}

func removeIfPresent(path string) error {
	if path == "" {
		return nil
	}
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
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

func (e *Engine) ensurePrivateRootfs(dir, base string, prior runtimeManifest, hasPrior bool) (string, string, error) {
	f, err := os.OpenFile(base, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", "", fmt.Errorf("open base rootfs: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() <= 0 {
		return "", "", fmt.Errorf("base rootfs is not a regular file")
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", "", err
	}
	if n != st.Size() {
		return "", "", fmt.Errorf("base rootfs changed while being hashed")
	}
	identity := hex.EncodeToString(h.Sum(nil))
	target := filepath.Join(dir, "rootfs-"+identity+".img")
	if fi, err := os.Lstat(target); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
			return "", "", fmt.Errorf("private rootfs path collision: not a regular file")
		}
		if os.SameFile(st, fi) || !privateFileHasSingleLink(fi) {
			return "", "", fmt.Errorf("private rootfs path collision: existing file may be shared")
		}
		if !hasPrior || prior.RootfsIdentity != identity || filepath.Clean(prior.RootfsPath) != filepath.Clean(target) {
			// An interrupted apply can leave a base copy not yet named by the current
			// manifest. Reuse it only if its complete contents match the immutable base.
			existing, openErr := os.OpenFile(target, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
			if openErr != nil {
				return "", "", fmt.Errorf("open existing private rootfs: %w", openErr)
			}
			existingInfo, statErr := existing.Stat()
			existingHash := sha256.New()
			existingBytes, hashErr := io.Copy(existingHash, existing)
			closeErr := existing.Close()
			if statErr != nil || hashErr != nil || closeErr != nil {
				return "", "", fmt.Errorf("verify existing private rootfs: %v", errors.Join(statErr, hashErr, closeErr))
			}
			if !existingInfo.Mode().IsRegular() || existingInfo.Size() != st.Size() || existingBytes != st.Size() || hex.EncodeToString(existingHash.Sum(nil)) != identity {
				return "", "", fmt.Errorf("private rootfs path collision: contents do not match the immutable base")
			}
			if err := os.Chmod(target, 0o600); err != nil {
				return "", "", fmt.Errorf("secure existing private rootfs: %w", err)
			}
		}
		return target, identity, nil
	} else if !os.IsNotExist(err) {
		return "", "", err
	}
	tmp, err := os.CreateTemp(dir, ".rootfs-*.tmp")
	if err != nil {
		return "", "", err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := f.Seek(0, 0); err != nil {
		tmp.Close()
		return "", "", err
	}
	copyHash := sha256.New()
	copied, err := io.Copy(io.MultiWriter(tmp, copyHash), f)
	if err != nil {
		tmp.Close()
		return "", "", err
	}
	if copied != st.Size() || hex.EncodeToString(copyHash.Sum(nil)) != identity {
		tmp.Close()
		return "", "", fmt.Errorf("base rootfs changed while being copied")
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", "", err
	}
	if err := tmp.Close(); err != nil {
		return "", "", err
	}
	if err := os.Link(tmpPath, target); err != nil {
		return "", "", fmt.Errorf("create private rootfs without overwrite: %w", err)
	}
	d, err := os.Open(dir)
	if err == nil {
		err = d.Sync()
		_ = d.Close()
	}
	if err != nil {
		_ = os.Remove(target)
		return "", "", err
	}
	return target, identity, nil
}

func privateFileHasSingleLink(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1
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
	if !validVMID(vm.ID) || vm.Socket == "" {
		return "", "", 0, fmt.Errorf("vm socket missing")
	}
	if !validVMID(snapshotID) {
		return "", "", 0, fmt.Errorf("invalid snapshot id")
	}
	sock, err := e.vmSocket(vm.ID)
	if err != nil {
		return "", "", 0, err
	}
	if !socketMatches(sock, vm.Socket) {
		return "", "", 0, fmt.Errorf("VM socket path does not match the configured runtime directory")
	}
	vm.Socket = sock
	unlock, err := e.vmLock(ctx, vm.ID)
	if err != nil {
		return "", "", 0, err
	}
	defer unlock()
	dir, err := e.vmDir(vm.ID, true)
	if err != nil {
		return "", "", 0, err
	}
	manifest, err := e.readManifest(vm.ID)
	if err != nil {
		return "", "", 0, fmt.Errorf("snapshot requires saved VM runtime configuration: %w", err)
	}
	if err := validatePrivateRootfs(dir, manifest); err != nil {
		return "", "", 0, err
	}
	statePath := filepath.Join(dir, snapshotID+".snap")
	memPath := filepath.Join(dir, snapshotID+".mem")
	client := socketClient(vm.Socket)
	if err := methodJSON(ctx, client, http.MethodPatch, "/vm", map[string]string{"state": "Paused"}); err != nil {
		return "", "", 0, err
	}
	snapshotErr := putJSON(ctx, client, "/snapshot/create", map[string]string{"snapshot_type": "Full", "snapshot_path": statePath, "mem_file_path": memPath})
	resumeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	resumeErr := methodJSON(resumeCtx, client, http.MethodPatch, "/vm", map[string]string{"state": "Resumed"})
	cancel()
	if snapshotErr != nil {
		if resumeErr != nil {
			return "", "", 0, fmt.Errorf("create snapshot: %v; resume VM: %w", snapshotErr, resumeErr)
		}
		return "", "", 0, snapshotErr
	}
	if resumeErr != nil {
		return "", "", 0, fmt.Errorf("snapshot created but VM could not be resumed: %w", resumeErr)
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
	if !validVMID(vm.ID) || vm.Socket == "" {
		return false
	}
	sock, err := e.vmSocket(vm.ID)
	if err != nil || !socketMatches(sock, vm.Socket) {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	pid, active, err := e.supervisorOrDefault().Status(ctx, vm.ID)
	if err != nil || !active || pid <= 0 || (vm.PID > 0 && vm.PID != pid) {
		return false
	}
	conn, err := net.DialTimeout("unix", sock, 250*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// Reconcile adopts a VM service that survived an agent restart, or marks a
// missing/failed runtime as failed after bounded cleanup. systemd owns the VM
// process and its cgroup, so the agent never treats a saved PID alone as proof
// that a workload is still alive.
func (e *Engine) Reconcile(ctx context.Context, vm store.VM) (store.VM, bool, error) {
	if !validVMID(vm.ID) {
		return vm, false, fmt.Errorf("invalid VM identity")
	}
	if vm.State != "running" && vm.State != "booting" && vm.State != "failed" {
		return vm, false, nil
	}
	unlock, err := e.vmLock(ctx, vm.ID)
	if err != nil {
		return vm, false, err
	}
	defer unlock()
	pid, active, err := e.supervisorOrDefault().Status(ctx, vm.ID)
	if err != nil {
		return vm, false, err
	}
	if vm.State == "failed" {
		if err := e.stopRuntime(vm, true, false); err != nil {
			return vm, false, err
		}
		if manifest, err := e.readManifest(vm.ID); err == nil {
			manifest.VM.State, manifest.VM.PID, manifest.VM.Socket = "failed", 0, ""
			manifest.VM.Error = vm.Error
			if err := e.writeManifest(manifest); err != nil {
				return vm, false, err
			}
		} else if !os.IsNotExist(err) {
			return vm, false, err
		}
		return vm, false, nil
	}
	if active && pid > 0 {
		sock, err := e.vmSocket(vm.ID)
		if err != nil {
			return vm, false, err
		}
		if !socketMatches(sock, vm.Socket) {
			return vm, false, fmt.Errorf("VM socket path does not match the configured runtime directory")
		}
		conn, dialErr := net.DialTimeout("unix", sock, 250*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			manifest, readErr := e.readManifest(vm.ID)
			if readErr == nil && manifest.VM.ID == vm.ID {
				changed := vm.PID != pid || vm.Socket != sock || vm.State != "running" || vm.Error != ""
				vm.PID, vm.Socket, vm.State, vm.Error = pid, sock, "running", ""
				if changed {
					manifest.VM = vm
					if err := e.writeManifest(manifest); err != nil {
						return vm, false, err
					}
				}
				return vm, true, nil
			}
		}
	}
	if err := e.stopRuntime(vm, true, false); err != nil {
		return vm, false, err
	}
	vm.State, vm.Health = "failed", "unhealthy"
	vm.PID, vm.Socket = 0, ""
	vm.Error = "Firecracker service was not active during agent reconciliation"
	if manifest, err := e.readManifest(vm.ID); err == nil {
		manifest.VM = vm
		if err := e.writeManifest(manifest); err != nil {
			return vm, false, err
		}
	} else if !os.IsNotExist(err) {
		return vm, false, err
	}
	return vm, false, nil
}

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

func validatePrivateRootfs(vmDir string, m runtimeManifest) error {
	root, err := filepath.Abs(vmDir)
	if err != nil {
		return err
	}
	if len(m.RootfsIdentity) != sha256.Size*2 {
		return fmt.Errorf("VM rootfs identity is missing or invalid")
	}
	if _, err := hex.DecodeString(m.RootfsIdentity); err != nil {
		return fmt.Errorf("VM rootfs identity is not valid SHA-256 hex")
	}
	path, err := filepath.Abs(m.RootfsPath)
	if err != nil {
		return err
	}
	if filepath.Dir(path) != root || m.RootfsIdentity == "" || filepath.Base(path) != "rootfs-"+m.RootfsIdentity+".img" {
		return fmt.Errorf("VM rootfs is not a verified private rootfs inside its VM directory")
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect private VM rootfs: %w", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() || fi.Size() <= 0 || !privateFileHasSingleLink(fi) {
		return fmt.Errorf("private VM rootfs is not a regular file")
	}
	return nil
}

func (e *Engine) validateSnapshotPath(vmID, path string) (string, error) {
	root, err := filepath.EvalSymlinks(filepath.Join(e.BaseDir, vmID))
	if err != nil {
		return "", fmt.Errorf("resolve VM snapshot directory: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve snapshot file %q: %w", path, err)
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("snapshot file %q is outside VM %s snapshot directory", path, vmID)
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("snapshot file %q: %w", resolved, err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("snapshot file %q is not a regular file", resolved)
	}
	return resolved, nil
}

func netmask(cidr string) string {
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		return "255.255.0.0"
	}
	return net.IP(n.Mask).String()
}

func (e *Engine) RestoreSnapshot(ctx context.Context, vm store.VM, statePath, memPath, stateSHA256, memSHA256 string) (store.VM, error) {
	if !validVMID(vm.ID) {
		return vm, fmt.Errorf("invalid VM identity")
	}
	if statePath == "" || memPath == "" {
		return vm, fmt.Errorf("snapshot state and memory paths are required")
	}
	stateID := strings.TrimSuffix(filepath.Base(statePath), ".snap")
	memID := strings.TrimSuffix(filepath.Base(memPath), ".mem")
	if !safeSnapshotID(stateID) || stateID != memID {
		return vm, fmt.Errorf("snapshot artifact names do not match")
	}
	if statePath == memPath {
		return vm, fmt.Errorf("snapshot state and memory paths must be different")
	}
	var err error
	if statePath, err = filepath.Abs(statePath); err != nil {
		return vm, fmt.Errorf("snapshot state path: %w", err)
	}
	if memPath, err = filepath.Abs(memPath); err != nil {
		return vm, fmt.Errorf("snapshot memory path: %w", err)
	}
	snapshotDir, err := e.vmDir(vm.ID, false)
	if err != nil {
		return vm, fmt.Errorf("resolve VM snapshot directory: %w", err)
	}
	if err := validateSnapshotArtifact(snapshotDir, statePath, ".snap"); err != nil {
		return vm, fmt.Errorf("invalid snapshot state file: %w", err)
	}
	if err := validateSnapshotArtifact(snapshotDir, memPath, ".mem"); err != nil {
		return vm, fmt.Errorf("invalid snapshot memory file: %w", err)
	}
	if err := verifySnapshotDigest(statePath, stateSHA256); err != nil {
		return vm, fmt.Errorf("invalid snapshot state digest: %w", err)
	}
	if err := verifySnapshotDigest(memPath, memSHA256); err != nil {
		return vm, fmt.Errorf("invalid snapshot memory digest: %w", err)
	}
	for _, path := range []string{statePath, memPath} {
		validated, validateErr := e.validateSnapshotPath(vm.ID, path)
		if validateErr != nil {
			return vm, validateErr
		}
		if path == statePath {
			statePath = validated
		} else {
			memPath = validated
		}
	}
	unlock, err := e.vmLock(ctx, vm.ID)
	if err != nil {
		return vm, err
	}
	defer unlock()
	manifest, err := e.readManifest(vm.ID)
	if err != nil {
		return vm, fmt.Errorf("restore requires saved VM runtime configuration: %w", err)
	}
	if err := validatePrivateRootfs(snapshotDir, manifest); err != nil {
		return vm, fmt.Errorf("snapshot restore rootfs is unsafe: %w", err)
	}
	if manifest.VCPUs <= 0 || manifest.MemoryMB <= 0 || manifest.KernelPath == "" || manifest.RootfsPath == "" {
		return vm, fmt.Errorf("saved VM runtime configuration is incomplete")
	}
	tap := manifest.VM.TapName
	if tap == "" {
		tap = vm.TapName
	}
	if tap != "" && (manifest.Network.Bridge == "" || manifest.Network != e.Network) {
		return vm, fmt.Errorf("snapshot restore requires the VM's original host network configuration")
	}
	if err := e.stopRuntime(vm, false, false); err != nil {
		return vm, err
	}
	if tap != "" {
		netSpec := protocol.VMApplySpec{VMID: vm.ID, TapName: tap}
		tap, err = e.ensureNetwork(ctx, &netSpec)
		if err != nil {
			return vm, fmt.Errorf("recreate VM tap for snapshot restore: %w", err)
		}
	}
	dir, err := e.vmDir(vm.ID, true)
	if err != nil {
		return vm, err
	}
	sock := filepath.Join(dir, "firecracker.sock")
	logPath := filepath.Join(dir, "firecracker-restore.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return vm, err
	}
	if err := logFile.Close(); err != nil {
		return vm, err
	}
	vm.State, vm.PID, vm.Socket, vm.TapName = "booting", 0, sock, tap
	manifest.VM = vm
	if err := e.writeManifest(manifest); err != nil {
		return vm, err
	}
	pid, err := e.supervisorOrDefault().Start(ctx, processSpec{
		VMID: vm.ID, Binary: e.FirecrackerBin, Socket: sock, WorkDir: dir, LogPath: logPath,
		KernelPath: manifest.KernelPath, RootfsPath: manifest.RootfsPath, VolumePath: manifest.VolumePath,
		VCPUs: manifest.VCPUs, MemoryMB: manifest.MemoryMB,
	})
	if err != nil {
		return e.failApply(vm, err)
	}
	vm.PID = pid
	manifest.VM = vm
	if err := e.writeManifest(manifest); err != nil {
		return e.failApply(vm, err)
	}
	if err := e.waitSocket(ctx, sock, 10*time.Second); err != nil {
		return e.failApply(vm, err)
	}
	client := socketClient(sock)
	loadRequest := map[string]interface{}{
		"snapshot_path": statePath,
		"mem_backend":   map[string]string{"backend_path": memPath, "backend_type": "File"},
		"resume_vm":     false,
	}
	if tap != "" {
		loadRequest["network_overrides"] = []map[string]string{{"iface_id": "eth0", "host_dev_name": tap}}
	}
	if err := putJSON(ctx, client, "/snapshot/load", loadRequest); err != nil {
		return e.failApply(vm, err)
	}
	if err := methodJSON(ctx, client, http.MethodPatch, "/vm", map[string]string{"state": "Resumed"}); err != nil {
		return e.failApply(vm, err)
	}
	vm.State, vm.Error, vm.Health = "running", "", "checking"
	manifest.VM = vm
	if err := e.writeManifest(manifest); err != nil {
		return e.failApply(vm, err)
	}
	return vm, nil
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
