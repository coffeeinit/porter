package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"porter/agent/internal/store"
	"porter/agent/internal/vmm"
	"porter/agent/internal/vmm/firecracker"
	"porter/protocol"
)

type Engine struct {
	BaseDir string
	// Driver is the VMM boundary. The agent core never speaks to a VMM
	// directly; everything VMM-specific lives behind this interface.
	Driver vmm.Driver
	// FirecrackerBin is retained for callers that only override the
	// Firecracker executable. It is ignored when Driver is set.
	FirecrackerBin string
	Kernel         string
	Rootfs         string
	Network        NetworkConfig
	InitPath       string
	supervisor     processSupervisor
	volumeMu       sync.Mutex
	volumeLeases   map[string]*volumeLease
}

func New(baseDir, kernel, rootfs string, network NetworkConfig) *Engine {
	if abs, err := filepath.Abs(baseDir); err == nil {
		baseDir = abs
	}
	return &Engine{BaseDir: baseDir, Driver: firecracker.Driver{}, Kernel: kernel, Rootfs: rootfs, Network: network, supervisor: newSystemdSupervisor()}
}

// driver returns the configured VMM driver, defaulting to Firecracker. The
// default keeps FirecrackerBin working for callers that only override the
// executable path.
func (e *Engine) driver() vmm.Driver {
	if e.Driver != nil {
		return e.Driver
	}
	return firecracker.Driver{BinaryPath: e.FirecrackerBin}
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
	sock := filepath.Join(dir, e.driver().SocketName())
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
			if manifest.VolumePath != "" {
				leaseErr := e.acquireVolumeLease(spec.VMID, manifest.VolumePath)
				if leaseErr == nil {
					var shared bool
					shared, leaseErr = e.volumeUsedByOtherVM(spec.VMID, manifest.VolumePath)
					if leaseErr == nil && shared {
						leaseErr = fmt.Errorf("writable volume is referenced by another VM")
					}
				}
				if leaseErr != nil {
					if stopErr := e.stopRuntime(manifest.VM, true, false); stopErr != nil {
						leaseErr = fmt.Errorf("%v; stop conflicting VM: %w", leaseErr, stopErr)
					}
					return manifest.VM, fmt.Errorf("cannot adopt VM with writable volume: %w", leaseErr)
				}
			}
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
	leaseAcquired, keepVolumeLease := false, false
	if spec.VolumeID != "" {
		volumePath, err = e.canonicalVolumePath(filepath.Join(e.BaseDir, "volumes", spec.VolumeID+".ext4"), true)
		if err != nil {
			return store.VM{ID: spec.VMID, ReplicaID: spec.ReplicaID, State: "failed"}, err
		}
		if err := e.acquireVolumeLease(spec.VMID, volumePath); err != nil {
			return store.VM{ID: spec.VMID, ReplicaID: spec.ReplicaID, State: "failed"}, err
		}
		leaseAcquired = true
		defer func() {
			if !leaseAcquired || keepVolumeLease {
				return
			}
			statusCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, active, statusErr := manager.Status(statusCtx, spec.VMID)
			if statusErr == nil && !active {
				_ = e.releaseVolumeLease(spec.VMID)
			}
		}()
		if shared, err := e.volumeUsedByOtherVM(spec.VMID, volumePath); err != nil || shared {
			if err != nil {
				return store.VM{ID: spec.VMID, ReplicaID: spec.ReplicaID, State: "failed"}, err
			}
			return store.VM{ID: spec.VMID, ReplicaID: spec.ReplicaID, State: "failed"}, fmt.Errorf("writable volume is already assigned to another VM")
		}
		if err := ensureVolumeFile(volumePath, spec.VolumeSizeMB); err != nil {
			return store.VM{ID: spec.VMID, ReplicaID: spec.ReplicaID, State: "failed"}, err
		}
		volumePath, err = e.canonicalVolumePath(volumePath, false)
		if err != nil {
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
	logPath := filepath.Join(dir, e.driver().LogName())
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
		VMID: spec.VMID, Binary: e.driver().Binary(), Socket: sock, WorkDir: dir, LogPath: logPath,
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
	if err := e.driver().Boot(ctx, sock, vmm.MachineSpec{
		KernelPath:  spec.KernelPath,
		RootfsPath:  privateRootfs,
		VolumePath:  volumePath,
		GuestMAC:    spec.GuestMAC,
		HostDevName: tap,
		GuestIP:     spec.GuestIP,
		GatewayIP:   spec.GatewayIP,
		BridgeCIDR:  spec.BridgeCIDR,
		VCPUs:       spec.VCPUs,
		MemoryMB:    spec.MemoryMB,
		InitPath:    e.InitPath,
	}); err != nil {
		return e.failApply(vm, err)
	}
	vm.State, vm.Error = "running", ""
	manifest.VM = vm
	if err := e.writeManifest(manifest); err != nil {
		return e.failApply(vm, fmt.Errorf("persist running VM state: %w", err))
	}
	if spec.VolumeID != "" {
		keepVolumeLease = true
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
		if err := removeIfPresent(vm.Socket); err != nil {
			return err
		}
		return e.releaseVolumeLease(vm.ID)
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
	return e.stopRuntimeWithLease(vm, removeTap, removeState, false)
}

func (e *Engine) stopRuntimeKeepingVolumeLease(vm store.VM, removeTap, removeState bool) error {
	return e.stopRuntimeWithLease(vm, removeTap, removeState, true)
}

func (e *Engine) stopRuntimeWithLease(vm store.VM, removeTap, removeState, keepVolumeLease bool) error {
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
	if !keepVolumeLease {
		return e.releaseVolumeLease(vm.ID)
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
	return fmt.Errorf("timed out waiting for VMM control socket %s", path)
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	parentInfo, err := os.Lstat(filepath.Dir(path))
	if err != nil || parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
		return fmt.Errorf("volume parent must be a real directory")
	}
	if sizeMB <= 0 {
		sizeMB = 1024
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	if !st.Mode().IsRegular() || !privateFileHasSingleLink(st) {
		_ = f.Close()
		return fmt.Errorf("volume image must be a single-link regular file")
	}
	if st.Size() < int64(sizeMB)*1024*1024 {
		if err := f.Truncate(int64(sizeMB) * 1024 * 1024); err != nil {
			_ = f.Close()
			return err
		}
		if err := f.Sync(); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func (e *Engine) Snapshot(ctx context.Context, vm store.VM, snapshotID string) (protocol.SnapshotRecord, error) {
	if !validVMID(vm.ID) || vm.Socket == "" {
		return protocol.SnapshotRecord{}, fmt.Errorf("vm socket missing")
	}
	if !safeSnapshotID(snapshotID) {
		return protocol.SnapshotRecord{}, fmt.Errorf("invalid snapshot id")
	}
	sock, err := e.vmSocket(vm.ID)
	if err != nil {
		return protocol.SnapshotRecord{}, err
	}
	if !socketMatches(sock, vm.Socket) {
		return protocol.SnapshotRecord{}, fmt.Errorf("VM socket path does not match the configured runtime directory")
	}
	if vm.State != "running" {
		return protocol.SnapshotRecord{}, fmt.Errorf("snapshot requires a running VM")
	}
	vm.Socket = sock
	unlock, err := e.vmLock(ctx, vm.ID)
	if err != nil {
		return protocol.SnapshotRecord{}, err
	}
	defer unlock()
	dir, err := e.vmDir(vm.ID, true)
	if err != nil {
		return protocol.SnapshotRecord{}, err
	}
	manifest, err := e.readManifest(vm.ID)
	if err != nil {
		return protocol.SnapshotRecord{}, fmt.Errorf("snapshot requires saved VM runtime configuration: %w", err)
	}
	if err := validatePrivateRootfs(dir, manifest); err != nil {
		return protocol.SnapshotRecord{}, err
	}
	if manifest.RootfsPath == "" || manifest.VCPUs <= 0 || manifest.MemoryMB <= 0 || manifest.KernelPath == "" {
		return protocol.SnapshotRecord{}, fmt.Errorf("snapshot requires a complete persisted VM runtime configuration")
	}
	diskSources := []struct{ kind, path string }{{kind: "rootfs", path: manifest.RootfsPath}}
	if manifest.VolumePath != "" {
		volumePath, err := e.canonicalVolumePath(manifest.VolumePath, false)
		if err != nil {
			return protocol.SnapshotRecord{}, fmt.Errorf("snapshot attached volume: %w", err)
		}
		manifest.VolumePath = volumePath
		if filepath.Clean(volumePath) == filepath.Clean(manifest.RootfsPath) {
			return protocol.SnapshotRecord{}, fmt.Errorf("root disk and attached volume paths must differ")
		}
		if err := e.acquireVolumeLease(vm.ID, volumePath); err != nil {
			return protocol.SnapshotRecord{}, fmt.Errorf("snapshot requires exclusive attached-volume ownership: %w", err)
		}
		shared, err := e.volumeUsedByOtherVM(vm.ID, volumePath)
		if err != nil {
			return protocol.SnapshotRecord{}, err
		}
		if shared {
			return protocol.SnapshotRecord{}, fmt.Errorf("snapshot refused: attached volume is shared by another VM")
		}
		diskSources = append(diskSources, struct{ kind, path string }{kind: "volume", path: volumePath})
	}
	if record, found, err := e.loadSnapshotRecord(dir, vm.ID, snapshotID, manifest); err != nil {
		return protocol.SnapshotRecord{}, err
	} else if found {
		return record, nil
	}
	statePath := filepath.Join(dir, snapshotID+".snap")
	memPath := filepath.Join(dir, snapshotID+".mem")
	recordPath := filepath.Join(dir, snapshotID+".record.json")
	for _, path := range []string{statePath, memPath} {
		if _, err := os.Lstat(path); err == nil {
			return protocol.SnapshotRecord{}, fmt.Errorf("snapshot id already exists")
		} else if !os.IsNotExist(err) {
			return protocol.SnapshotRecord{}, err
		}
	}
	for _, disk := range diskSources {
		if _, err := os.Lstat(filepath.Join(dir, snapshotID+"."+disk.kind+".disk")); err == nil {
			return protocol.SnapshotRecord{}, fmt.Errorf("snapshot id already exists")
		} else if !os.IsNotExist(err) {
			return protocol.SnapshotRecord{}, err
		}
	}
	tmpState, err := reserveSnapshotPath(dir, snapshotID, ".snap.tmp")
	if err != nil {
		return protocol.SnapshotRecord{}, err
	}
	tmpMem, err := reserveSnapshotPath(dir, snapshotID, ".mem.tmp")
	if err != nil {
		_ = os.Remove(tmpState)
		return protocol.SnapshotRecord{}, err
	}
	diskPaths := make([]string, 0, len(diskSources))
	cleanup := func() {
		_ = os.Remove(tmpState)
		_ = os.Remove(tmpMem)
		_ = os.Remove(statePath)
		_ = os.Remove(memPath)
		_ = os.Remove(recordPath)
		for _, path := range diskPaths {
			_ = os.Remove(path)
		}
		_ = syncDir(dir)
	}
	resume := func() error {
		resumeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return e.driver().Resume(resumeCtx, vm.Socket)
	}
	if err := e.driver().Pause(ctx, vm.Socket); err != nil {
		resumeErr := resume()
		cleanup()
		if resumeErr != nil {
			if stopErr := e.stopRuntime(vm, true, false); stopErr != nil {
				return protocol.SnapshotRecord{}, fmt.Errorf("pause VM: %v; resume after failed pause: %v; stop VM: %w", err, resumeErr, stopErr)
			}
			return protocol.SnapshotRecord{}, fmt.Errorf("pause VM: %v; resume after failed pause: %v; VM was stopped", err, resumeErr)
		}
		return protocol.SnapshotRecord{}, err
	}
	failWhilePaused := func(cause error) error {
		if resumeErr := resume(); resumeErr != nil {
			if stopErr := e.stopRuntime(vm, true, false); stopErr != nil {
				return fmt.Errorf("%v; resume VM: %v; stop paused VM: %w", cause, resumeErr, stopErr)
			}
			return fmt.Errorf("%v; resume VM: %v; VM was stopped", cause, resumeErr)
		}
		return cause
	}
	for _, disk := range diskSources {
		if err := syncDiskSource(disk.path); err != nil {
			cause := failWhilePaused(fmt.Errorf("sync %s disk: %w", disk.kind, err))
			cleanup()
			return protocol.SnapshotRecord{}, cause
		}
	}
	if err := e.driver().Snapshot(ctx, vm.Socket, vmm.SnapshotSpec{StatePath: tmpState, MemPath: tmpMem}); err != nil {
		cause := failWhilePaused(fmt.Errorf("create Firecracker snapshot: %w", err))
		cleanup()
		return protocol.SnapshotRecord{}, cause
	}
	stateSize, stateHash, err := snapshotFileDigest(tmpState)
	if err != nil {
		cause := failWhilePaused(fmt.Errorf("verify snapshot state: %w", err))
		cleanup()
		return protocol.SnapshotRecord{}, cause
	}
	memSize, memHash, err := snapshotFileDigest(tmpMem)
	if err != nil {
		cause := failWhilePaused(fmt.Errorf("verify snapshot memory: %w", err))
		cleanup()
		return protocol.SnapshotRecord{}, cause
	}
	result := protocol.SnapshotRecord{ID: snapshotID, VMID: vm.ID, StatePath: statePath, MemPath: memPath, StateSHA256: stateHash, MemSHA256: memHash, SizeBytes: stateSize + memSize}
	for _, disk := range diskSources {
		backupPath := filepath.Join(dir, snapshotID+"."+disk.kind+".disk")
		diskPaths = append(diskPaths, backupPath)
		if err := copyFileDurable(disk.path, backupPath); err != nil {
			cause := failWhilePaused(fmt.Errorf("backup %s disk: %w", disk.kind, err))
			cleanup()
			return protocol.SnapshotRecord{}, cause
		}
		size, digest, err := snapshotFileDigest(backupPath)
		if err != nil {
			cause := failWhilePaused(fmt.Errorf("verify %s disk backup: %w", disk.kind, err))
			cleanup()
			return protocol.SnapshotRecord{}, cause
		}
		result.DiskFiles = append(result.DiskFiles, protocol.SnapshotFile{Kind: disk.kind, OriginalPath: disk.path, BackupPath: backupPath, SizeBytes: size, SHA256: digest})
		result.SizeBytes += size
	}
	if err := resume(); err != nil {
		stopErr := e.stopRuntime(vm, true, false)
		cleanup()
		if stopErr != nil {
			return protocol.SnapshotRecord{}, fmt.Errorf("snapshot created but VM resume failed: %v; stop paused VM: %w", err, stopErr)
		}
		return protocol.SnapshotRecord{}, fmt.Errorf("snapshot artifacts created but VM resume failed: %w", err)
	}
	if err := publishSnapshotPath(tmpState, statePath); err != nil {
		cleanup()
		return protocol.SnapshotRecord{}, err
	}
	if err := publishSnapshotPath(tmpMem, memPath); err != nil {
		cleanup()
		return protocol.SnapshotRecord{}, err
	}
	if err := syncDir(dir); err != nil {
		cleanup()
		return protocol.SnapshotRecord{}, fmt.Errorf("sync snapshot directory: %w", err)
	}
	if err := persistSnapshotRecord(dir, result); err != nil {
		cleanup()
		return protocol.SnapshotRecord{}, fmt.Errorf("persist snapshot record: %w", err)
	}
	return result, nil
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
	manifest, manifestErr := e.readManifest(vm.ID)
	if manifestErr != nil && !os.IsNotExist(manifestErr) {
		return vm, false, manifestErr
	}
	pid, active, err := e.supervisorOrDefault().Status(ctx, vm.ID)
	if err != nil {
		return vm, false, err
	}
	if manifestErr == nil && manifest.VolumePath != "" {
		leaseErr := e.acquireVolumeLease(vm.ID, manifest.VolumePath)
		if leaseErr == nil {
			var shared bool
			shared, leaseErr = e.volumeUsedByOtherVM(vm.ID, manifest.VolumePath)
			if leaseErr == nil && shared {
				leaseErr = fmt.Errorf("writable volume is referenced by another VM")
			}
		}
		if leaseErr != nil {
			if active {
				if stopErr := e.stopRuntime(vm, true, false); stopErr != nil {
					return vm, false, fmt.Errorf("volume lease conflict and VM could not be stopped: %v; %w", leaseErr, stopErr)
				}
			}
			_ = e.releaseVolumeLease(vm.ID)
			vm.State, vm.PID, vm.Socket, vm.Health = "failed", 0, "", "unhealthy"
			vm.Error = "writable volume lease conflict: " + leaseErr.Error()
			manifest.VM = vm
			if err := e.writeManifest(manifest); err != nil {
				return vm, false, err
			}
			return vm, false, nil
		}
	}
	journalPath, err := e.restoreJournalPath(vm.ID)
	if err != nil {
		return vm, false, err
	}
	if info, statErr := os.Lstat(journalPath); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return vm, false, fmt.Errorf("restore journal is not a regular file")
		}
		data, readErr := os.ReadFile(journalPath)
		if readErr != nil {
			return vm, false, readErr
		}
		var journal diskRestoreJournal
		if err := json.Unmarshal(data, &journal); err != nil || journal.VMID != vm.ID || !safeSnapshotID(journal.SnapshotID) {
			return vm, false, fmt.Errorf("invalid pending disk restore journal")
		}
		if !journal.Committed {
			if err := e.stopRuntimeKeepingVolumeLease(vm, true, false); err != nil {
				return vm, false, fmt.Errorf("stop VM before recovering interrupted disk restore: %w", err)
			}
			if err := e.recoverDiskRestore(vm.ID); err != nil {
				return vm, false, fmt.Errorf("roll back interrupted disk restore: %w", err)
			}
			vm.PID, vm.Socket, vm.State, vm.Health = 0, "", "failed", "unhealthy"
			vm.Error = "interrupted disk restore was rolled back during agent reconciliation"
			if manifestErr == nil {
				manifest.VM = vm
				if err := e.writeManifest(manifest); err != nil {
					return vm, false, err
				}
			}
			if err := e.releaseVolumeLease(vm.ID); err != nil {
				return vm, false, err
			}
			return vm, false, nil
		}
		if err := e.recoverDiskRestore(vm.ID); err != nil {
			return vm, false, fmt.Errorf("finalize committed disk restore: %w", err)
		}
	} else if !os.IsNotExist(statErr) {
		return vm, false, statErr
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
	if !commandMatchesFirecracker(cmdline, e.driver().Binary(), vm.Socket) {
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
	path := filepath.Join(e.BaseDir, vm.ID, e.driver().LogName())
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
	path, err := validatePrivateRootfsPath(vmDir, m)
	if err != nil {
		return err
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

func validatePrivateRootfsPath(vmDir string, m runtimeManifest) (string, error) {
	root, err := filepath.Abs(vmDir)
	if err != nil {
		return "", err
	}
	if len(m.RootfsIdentity) != sha256.Size*2 {
		return "", fmt.Errorf("VM rootfs identity is missing or invalid")
	}
	if _, err := hex.DecodeString(m.RootfsIdentity); err != nil {
		return "", fmt.Errorf("VM rootfs identity is not valid SHA-256 hex")
	}
	path, err := filepath.Abs(m.RootfsPath)
	if err != nil {
		return "", err
	}
	if filepath.Dir(path) != root || m.RootfsIdentity == "" || filepath.Base(path) != "rootfs-"+m.RootfsIdentity+".img" {
		return "", fmt.Errorf("VM rootfs is not a verified private rootfs inside its VM directory")
	}
	return path, nil
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


// RestoreSnapshotV2 resolves the path-free controller command to a verified
// node-local record, then delegates to the existing confined restore path.
func (e *Engine) RestoreSnapshotV2(ctx context.Context, vm store.VM, command protocol.SnapshotRestoreCommand) (store.VM, protocol.SnapshotArtifactReceipt, error) {
	var empty protocol.SnapshotArtifactReceipt
	if err := command.Validate(); err != nil {
		return vm, empty, err
	}
	if command.VMID != vm.ID {
		return vm, empty, fmt.Errorf("snapshot restore command VM does not match the local VM")
	}
	dir, err := e.vmDir(vm.ID, false)
	if err != nil {
		return vm, empty, fmt.Errorf("resolve VM snapshot directory: %w", err)
	}
	manifest, err := e.readManifest(vm.ID)
	if err != nil {
		return vm, empty, fmt.Errorf("read local VM runtime configuration: %w", err)
	}
	record, found, err := e.loadSnapshotRecord(dir, vm.ID, command.SnapshotID, manifest)
	if err != nil {
		return vm, empty, fmt.Errorf("verify local snapshot record: %w", err)
	}
	if !found {
		return vm, empty, fmt.Errorf("requested snapshot is not present on this node")
	}
	receipt, err := protocol.SnapshotArtifactReceiptFor(record)
	if err != nil {
		return vm, empty, err
	}
	if receipt.Consistency != command.Consistency || receipt.ArtifactSHA256 != command.ArtifactSHA256 || receipt.SizeBytes != command.SizeBytes {
		return vm, empty, fmt.Errorf("local snapshot artifact does not match the control-plane receipt")
	}
	spec := protocol.SnapshotRestoreSpec{
		SnapshotID: record.ID, VMID: record.VMID, StatePath: record.StatePath, MemPath: record.MemPath,
		SizeBytes: record.SizeBytes, StateSHA256: record.StateSHA256, MemSHA256: record.MemSHA256,
		DiskFiles: append([]protocol.SnapshotFile(nil), record.DiskFiles...),
	}
	restored, err := e.RestoreSnapshot(ctx, vm, spec)
	return restored, receipt, err
}

func (e *Engine) RestoreSnapshot(ctx context.Context, vm store.VM, spec protocol.SnapshotRestoreSpec) (store.VM, error) {
	if !validVMID(vm.ID) || spec.VMID != vm.ID || !safeSnapshotID(spec.SnapshotID) {
		return vm, fmt.Errorf("invalid VM or snapshot identity")
	}
	if spec.StatePath == "" || spec.MemPath == "" || !validSnapshotHash(spec.StateSHA256) || !validSnapshotHash(spec.MemSHA256) {
		return vm, fmt.Errorf("snapshot state and memory integrity metadata are required")
	}
	snapshotDir, err := e.vmDir(vm.ID, false)
	if err != nil {
		return vm, fmt.Errorf("resolve VM snapshot directory: %w", err)
	}
	expectedState := filepath.Join(snapshotDir, spec.SnapshotID+".snap")
	expectedMem := filepath.Join(snapshotDir, spec.SnapshotID+".mem")
	if spec.StatePath != expectedState || spec.MemPath != expectedMem {
		return vm, fmt.Errorf("snapshot state or memory path is outside the VM directory")
	}
	sock, err := e.vmSocket(vm.ID)
	if err != nil {
		return vm, err
	}
	if !socketMatches(sock, vm.Socket) {
		return vm, fmt.Errorf("VM socket path does not match the configured runtime directory")
	}
	vm.Socket = sock
	unlock, err := e.vmLock(ctx, vm.ID)
	if err != nil {
		return vm, err
	}
	defer unlock()
	releaseLeaseOnReturn := false
	defer func() {
		if releaseLeaseOnReturn {
			_ = e.releaseVolumeLease(vm.ID)
		}
	}()
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
	if err := validateSnapshotArtifact(snapshotDir, expectedState, ".snap"); err != nil {
		return vm, fmt.Errorf("invalid snapshot state file: %w", err)
	}
	if err := validateSnapshotArtifact(snapshotDir, expectedMem, ".mem"); err != nil {
		return vm, fmt.Errorf("invalid snapshot memory file: %w", err)
	}
	stateSize, stateHash, err := snapshotFileDigest(expectedState)
	if err != nil || stateHash != spec.StateSHA256 {
		return vm, fmt.Errorf("snapshot state checksum or file validation failed")
	}
	memSize, memHash, err := snapshotFileDigest(expectedMem)
	if err != nil || memHash != spec.MemSHA256 {
		return vm, fmt.Errorf("snapshot memory checksum or file validation failed")
	}
	if manifest.VolumePath != "" {
		volumePath, err := e.canonicalVolumePath(manifest.VolumePath, false)
		if err != nil {
			return vm, fmt.Errorf("snapshot restore attached volume: %w", err)
		}
		if filepath.Clean(volumePath) == filepath.Clean(manifest.RootfsPath) {
			return vm, fmt.Errorf("root disk and attached volume paths must differ")
		}
		manifest.VolumePath = volumePath
		e.volumeMu.Lock()
		heldLease := e.volumeLeases[vm.ID]
		leaseAlreadyHeld := heldLease != nil && heldLease.path == volumePath
		e.volumeMu.Unlock()
		if err := e.acquireVolumeLease(vm.ID, volumePath); err != nil {
			return vm, fmt.Errorf("restore requires exclusive attached-volume ownership: %w", err)
		}
		if !leaseAlreadyHeld && vm.State == "stopped" {
			releaseLeaseOnReturn = true
		}
		shared, err := e.volumeUsedByOtherVM(vm.ID, volumePath)
		if err != nil {
			return vm, err
		}
		if shared {
			return vm, fmt.Errorf("restore refused: attached volume is shared by another VM")
		}
	}
	diskSize, err := e.validateRestoreDiskFiles(vm.ID, spec.SnapshotID, manifest, spec.DiskFiles)
	if err != nil {
		return vm, err
	}
	if spec.SizeBytes != stateSize+memSize+diskSize {
		return vm, fmt.Errorf("snapshot aggregate size does not match its persisted files")
	}
	tap := manifest.VM.TapName
	if tap == "" {
		tap = vm.TapName
	}
	if tap != "" && (manifest.Network.Bridge == "" || manifest.Network != e.Network) {
		return vm, fmt.Errorf("snapshot restore requires the VM's original host network configuration")
	}
	releaseLeaseOnReturn = false
	if err := e.stopRuntimeKeepingVolumeLease(vm, false, false); err != nil {
		return vm, err
	}
	vm.State, vm.PID, vm.Socket = "stopped", 0, sock
	if err := e.recoverDiskRestore(vm.ID); err != nil {
		vm.State, vm.Error = "failed", "recover interrupted disk restore: "+err.Error()
		return vm, fmt.Errorf("recover interrupted disk restore: %w", err)
	}
	if tap != "" {
		netSpec := protocol.VMApplySpec{VMID: vm.ID, TapName: tap}
		tap, err = e.ensureNetwork(ctx, &netSpec)
		if err != nil {
			_ = e.releaseVolumeLease(vm.ID)
			vm.Error = err.Error()
			return vm, fmt.Errorf("recreate VM tap for snapshot restore: %w", err)
		}
	}
	journal, err := e.prepareDiskRestore(vm.ID, spec.SnapshotID, spec.DiskFiles)
	if err != nil {
		recoveryErr := e.recoverDiskRestore(vm.ID)
		if recoveryErr == nil {
			_ = e.releaseVolumeLease(vm.ID)
		} else {
			err = errors.Join(err, recoveryErr)
		}
		vm.Error, vm.State = err.Error(), "failed"
		manifest.VM = vm
		_ = e.writeManifest(manifest)
		return vm, err
	}
	dir, err := e.vmDir(vm.ID, true)
	if err != nil {
		recoveryErr := e.recoverDiskRestore(vm.ID)
		if recoveryErr == nil {
			_ = e.releaseVolumeLease(vm.ID)
		} else {
			err = errors.Join(err, fmt.Errorf("recover staged disk restore: %w", recoveryErr))
		}
		return vm, err
	}
	sock = filepath.Join(dir, e.driver().SocketName())
	logPath := filepath.Join(dir, strings.TrimSuffix(e.driver().LogName(), ".log")+"-restore.log")
	rollback := func(cause error) (store.VM, error) {
		stopErr := e.stopRuntimeKeepingVolumeLease(vm, true, false)
		if stopErr != nil {
			cause = errors.Join(cause, fmt.Errorf("stop failed restore process: %w", stopErr))
		}
		restoreErr := e.recoverDiskRestore(vm.ID)
		if restoreErr != nil {
			cause = errors.Join(cause, fmt.Errorf("disk rollback failed: %w", restoreErr))
		}
		vm.State, vm.PID, vm.Socket, vm.Health, vm.Error = "failed", 0, "", "unhealthy", cause.Error()
		manifest.VM = vm
		if err := e.writeManifest(manifest); err != nil {
			cause = errors.Join(cause, fmt.Errorf("persist failed restore state: %w", err))
		}
		if stopErr == nil && restoreErr == nil {
			_ = e.releaseVolumeLease(vm.ID)
		}
		return vm, cause
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return rollback(err)
	}
	if err := logFile.Close(); err != nil {
		return rollback(err)
	}
	vm.State, vm.PID, vm.Socket, vm.TapName, vm.Error = "booting", 0, sock, tap, ""
	manifest.VM = vm
	if err := e.writeManifest(manifest); err != nil {
		return rollback(err)
	}
	pid, err := e.supervisorOrDefault().Start(ctx, processSpec{
		VMID: vm.ID, Binary: e.driver().Binary(), Socket: sock, WorkDir: dir, LogPath: logPath,
		KernelPath: manifest.KernelPath, RootfsPath: manifest.RootfsPath, VolumePath: manifest.VolumePath,
		VCPUs: manifest.VCPUs, MemoryMB: manifest.MemoryMB,
	})
	if err != nil {
		return rollback(err)
	}
	vm.PID = pid
	manifest.VM = vm
	if err := e.writeManifest(manifest); err != nil {
		return rollback(err)
	}
	if err := e.waitSocket(ctx, sock, 10*time.Second); err != nil {
		return rollback(err)
	}
	if err := e.driver().Restore(ctx, sock, vmm.SnapshotSpec{StatePath: expectedState, MemPath: expectedMem}, tap); err != nil {
		return rollback(err)
	}
	vm.State, vm.Error, vm.Health = "running", "", "checking"
	manifest.VM = vm
	if err := e.writeManifest(manifest); err != nil {
		return rollback(err)
	}
	journal.Committed = true
	if err := e.writeDiskRestoreJournal(journal); err != nil {
		return rollback(err)
	}
	if err := e.recoverDiskRestore(vm.ID); err != nil {
		vm.Error = "restore committed; journal finalization pending: " + err.Error()
		manifest.VM = vm
		if persistErr := e.writeManifest(manifest); persistErr != nil {
			err = errors.Join(err, fmt.Errorf("persist finalization-pending state: %w", persistErr))
		}
		return vm, fmt.Errorf("snapshot restored but rollback-journal finalization failed: %w", err)
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
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !privateFileHasSingleLink(info) {
		return fmt.Errorf("snapshot artifact must be a single-link regular file")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return fmt.Errorf("snapshot artifact changed while being opened")
	}
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
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !privateFileHasSingleLink(info) {
		return fmt.Errorf("snapshot artifact must be a regular, non-symlink file")
	}
	return nil
}

func (e *Engine) ResizeVolume(ctx context.Context, volumeID string, sizeMB int) error {
	if !validVMID(volumeID) || sizeMB <= 0 {
		return fmt.Errorf("volume id and size required")
	}
	path, err := e.canonicalVolumePath(filepath.Join(e.BaseDir, "volumes", volumeID+".ext4"), false)
	if err != nil {
		return err
	}
	lock, err := openVolumeLock(path)
	if err != nil {
		return err
	}
	defer func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}()
	shared, err := e.volumeUsedByOtherVM("", path)
	if err != nil {
		return err
	}
	if shared {
		return fmt.Errorf("cannot resize a volume referenced by a VM")
	}
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !privateFileHasSingleLink(info) {
		_ = f.Close()
		return fmt.Errorf("volume image must be a single-link regular file")
	}
	if err := f.Truncate(int64(sizeMB) * 1024 * 1024); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}
