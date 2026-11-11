package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"porter/agent/internal/store"
	"porter/protocol"
)

type snapshotFixture struct {
	engine    *Engine
	vm        store.VM
	rootfs    string
	volume    string
	rootBytes []byte
	volBytes  []byte
}

func newSnapshotFixture(t *testing.T, withVolume bool) snapshotFixture {
	t.Helper()
	base, err := os.MkdirTemp("/tmp", "porter-snap-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	engine := New(base, "fake-firecracker", "", NetworkConfig{})
	vmID := "vm-snapshot-test"
	vmDir, err := engine.vmDir(vmID, true)
	if err != nil {
		t.Fatal(err)
	}
	baseRoot := filepath.Join(base, "base-rootfs.img")
	rootBytes := []byte("rootfs-before-snapshot")
	if err := os.WriteFile(baseRoot, rootBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	rootfs, identity, err := engine.ensurePrivateRootfs(vmDir, baseRoot, runtimeManifest{}, false)
	if err != nil {
		t.Fatal(err)
	}
	vm := store.VM{ID: vmID, ReplicaID: "replica-snapshot-test", State: "running", PID: 4242, Socket: filepath.Join(vmDir, "firecracker.sock")}
	volumePath := ""
	var volumeBytes []byte
	if withVolume {
		volumePath, err = engine.canonicalVolumePath(filepath.Join(base, "volumes", "volume-snapshot-test.ext4"), true)
		if err != nil {
			t.Fatal(err)
		}
		volumeBytes = []byte("attached-volume-before-snapshot")
		if err := os.WriteFile(volumePath, volumeBytes, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manifest := runtimeManifest{
		Version: 1, SpecHash: "fixture-spec-hash", KernelPath: filepath.Join(base, "vmlinux"),
		RootfsPath: rootfs, RootfsIdentity: identity, VolumePath: volumePath,
		VCPUs: 1, MemoryMB: 128, VM: vm,
	}
	if err := engine.writeManifest(manifest); err != nil {
		t.Fatal(err)
	}
	return snapshotFixture{engine: engine, vm: vm, rootfs: rootfs, volume: volumePath, rootBytes: rootBytes, volBytes: volumeBytes}
}

func serveSnapshotAPI(t *testing.T, socket string) func() {
	t.Helper()
	if err := os.Remove(socket); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/snapshot/create" {
			var request struct {
				SnapshotPath      string `json:"snapshot_path"`
				MemFilePath       string `json:"mem_file_path"`
				SyncSnapshotFiles bool   `json:"sync_snapshot_files"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || !request.SyncSnapshotFiles {
				http.Error(w, "invalid snapshot request", http.StatusBadRequest)
				return
			}
			if err := os.WriteFile(request.SnapshotPath, []byte("firecracker-state"), 0o600); err != nil {
				http.Error(w, fmt.Sprintf("write state: %v", err), http.StatusInternalServerError)
				return
			}
			if err := os.WriteFile(request.MemFilePath, []byte("firecracker-memory"), 0o600); err != nil {
				http.Error(w, fmt.Sprintf("write memory: %v", err), http.StatusInternalServerError)
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})}
	go func() { _ = server.Serve(listener) }()
	var once sync.Once
	closeServer := func() {
		once.Do(func() { _ = server.Close() })
	}
	t.Cleanup(closeServer)
	return closeServer
}

func takeSnapshot(t *testing.T, fixture snapshotFixture, snapshotID string) protocol.SnapshotRecord {
	t.Helper()
	closeServer := serveSnapshotAPI(t, fixture.vm.Socket)
	record, err := fixture.engine.Snapshot(context.Background(), fixture.vm, snapshotID)
	closeServer()
	if err != nil {
		t.Fatalf("create crash-consistent snapshot: %v", err)
	}
	return record
}

func restoreSpec(record protocol.SnapshotRecord) protocol.SnapshotRestoreSpec {
	return protocol.SnapshotRestoreSpec{
		SnapshotID: record.ID, VMID: record.VMID, StatePath: record.StatePath, MemPath: record.MemPath,
		SizeBytes: record.SizeBytes, StateSHA256: record.StateSHA256, MemSHA256: record.MemSHA256,
		DiskFiles: record.DiskFiles,
	}
}

func TestSnapshotRestoresPrivateRootfsAndAttachedVolume(t *testing.T) {
	fixture := newSnapshotFixture(t, true)
	record := takeSnapshot(t, fixture, "snapshot-one")
	localRecord := filepath.Join(filepath.Dir(fixture.vm.Socket), record.ID+".record.json")
	if err := os.Remove(localRecord); err != nil {
		t.Fatalf("simulate crash before local record commit: %v", err)
	}
	reconstructed, err := fixture.engine.Snapshot(context.Background(), fixture.vm, record.ID)
	if err != nil || reconstructed.StateSHA256 != record.StateSHA256 || reconstructed.MemSHA256 != record.MemSHA256 || len(reconstructed.DiskFiles) != len(record.DiskFiles) || reconstructed.DiskFiles[0] != record.DiskFiles[0] {
		t.Fatalf("complete published snapshot was not reconstructed after record-marker loss: record=%+v err=%v", reconstructed, err)
	}
	retry, err := fixture.engine.Snapshot(context.Background(), fixture.vm, record.ID)
	if err != nil || retry.StateSHA256 != record.StateSHA256 || retry.MemSHA256 != record.MemSHA256 || len(retry.DiskFiles) != len(record.DiskFiles) || retry.DiskFiles[0] != record.DiskFiles[0] {
		t.Fatalf("snapshot retry did not return the persisted record: retry=%+v err=%v", retry, err)
	}
	if record.VMID != fixture.vm.ID || len(record.DiskFiles) != 2 || record.DiskFiles[0].Kind != "rootfs" || record.DiskFiles[1].Kind != "volume" {
		t.Fatalf("snapshot record is incomplete: %+v", record)
	}
	if record.SizeBytes <= int64(len(fixture.rootBytes)+len(fixture.volBytes)) {
		t.Fatalf("aggregate snapshot size omitted state or disk data: %d", record.SizeBytes)
	}
	rootAfter := []byte("rootfs-after-snapshot")
	volumeAfter := []byte("attached-volume-after-snapshot")
	if err := os.WriteFile(fixture.rootfs, rootAfter, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.volume, volumeAfter, 0o600); err != nil {
		t.Fatal(err)
	}

	supervisor := &fakeProcessSupervisor{}
	fixture.engine.supervisor = supervisor
	receipt, err := protocol.SnapshotArtifactReceiptFor(record)
	if err != nil {
		t.Fatalf("build snapshot receipt: %v", err)
	}
	command := protocol.SnapshotRestoreCommand{
		VMID: record.VMID, SnapshotID: record.ID, Consistency: protocol.SnapshotCrashConsistent,
		ArtifactSHA256: receipt.ArtifactSHA256, SizeBytes: receipt.SizeBytes,
	}
	restored, gotReceipt, err := fixture.engine.RestoreSnapshotV2(context.Background(), fixture.vm, command)
	if err != nil {
		t.Fatalf("restore crash-consistent snapshot: %v", err)
	}
	if gotReceipt != receipt {
		t.Fatalf("restore receipt = %+v, want %+v", gotReceipt, receipt)
	}
	if restored.State != "running" || !supervisor.active {
		t.Fatalf("restored VM not running: vm=%+v active=%v", restored, supervisor.active)
	}
	if got, err := os.ReadFile(fixture.rootfs); err != nil || string(got) != string(fixture.rootBytes) {
		t.Fatalf("rootfs was not restored: got=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(fixture.volume); err != nil || string(got) != string(fixture.volBytes) {
		t.Fatalf("attached volume was not restored: got=%q err=%v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(fixture.vm.Socket), "restore-journal.json")); !os.IsNotExist(err) {
		t.Fatalf("committed restore journal was not finalized: %v", err)
	}
	if err := fixture.engine.Stop(restored); err != nil {
		t.Fatalf("stop restored VM: %v", err)
	}
	other := New(filepath.Dir(filepath.Dir(fixture.rootfs)), "fake-firecracker", "", NetworkConfig{})
	if err := other.acquireVolumeLease("vm-other", fixture.volume); err != nil {
		t.Fatalf("volume lease was not released after stop: %v", err)
	}
	if err := other.releaseVolumeLease("vm-other"); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreSnapshotRejectsDiskChecksumBeforeStoppingVM(t *testing.T) {
	fixture := newSnapshotFixture(t, true)
	record := takeSnapshot(t, fixture, "snapshot-tamper")
	supervisor := &fakeProcessSupervisor{active: true, pid: 4242}
	fixture.engine.supervisor = supervisor
	spec := restoreSpec(record)
	spec.DiskFiles[0].SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	if _, err := fixture.engine.RestoreSnapshot(context.Background(), fixture.vm, spec); err == nil {
		t.Fatal("restore accepted a tampered rootfs checksum")
	}
	if !supervisor.active || supervisor.startCount() != 0 {
		t.Fatalf("checksum rejection changed runtime state: active=%v starts=%d", supervisor.active, supervisor.startCount())
	}
}

func TestRestoreSnapshotReleasesNewLeaseAfterStoppedVMPreflightFailure(t *testing.T) {
	fixture := newSnapshotFixture(t, true)
	record := takeSnapshot(t, fixture, "snapshot-lease-preflight")
	if err := fixture.engine.releaseVolumeLease(fixture.vm.ID); err != nil {
		t.Fatal(err)
	}
	vm := fixture.vm
	vm.State = "stopped"
	spec := restoreSpec(record)
	spec.DiskFiles[0].SHA256 = strings.Repeat("0", 64)
	if _, err := fixture.engine.RestoreSnapshot(context.Background(), vm, spec); err == nil {
		t.Fatal("restore accepted invalid disk metadata")
	}
	probe := New(filepath.Dir(filepath.Dir(fixture.rootfs)), "fake-firecracker", "", NetworkConfig{})
	if err := probe.acquireVolumeLease("vm-probe", fixture.volume); err != nil {
		t.Fatalf("failed restore retained its newly acquired volume lease: %v", err)
	}
	if err := probe.releaseVolumeLease("vm-probe"); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreSnapshotRollsBackDiskSwapOnLoadFailure(t *testing.T) {
	fixture := newSnapshotFixture(t, true)
	record := takeSnapshot(t, fixture, "snapshot-rollback")
	rootAfter := []byte("rootfs-after-snapshot")
	volumeAfter := []byte("attached-volume-after-snapshot")
	if err := os.WriteFile(fixture.rootfs, rootAfter, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.volume, volumeAfter, 0o600); err != nil {
		t.Fatal(err)
	}
	supervisor := &fakeProcessSupervisor{failPath: "/snapshot/load"}
	fixture.engine.supervisor = supervisor
	vm, err := fixture.engine.RestoreSnapshot(context.Background(), fixture.vm, restoreSpec(record))
	if err == nil || vm.State != "failed" {
		t.Fatalf("failed snapshot load was not surfaced: vm=%+v err=%v", vm, err)
	}
	if got, err := os.ReadFile(fixture.rootfs); err != nil || string(got) != string(rootAfter) {
		t.Fatalf("rootfs rollback failed: got=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(fixture.volume); err != nil || string(got) != string(volumeAfter) {
		t.Fatalf("volume rollback failed: got=%q err=%v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(fixture.vm.Socket), "restore-journal.json")); !os.IsNotExist(err) {
		t.Fatalf("rollback journal remains after successful rollback: %v", err)
	}
	if supervisor.active {
		t.Fatal("failed restore process remained active")
	}
}

func TestReconcileRollsBackInterruptedDiskRestore(t *testing.T) {
	fixture := newSnapshotFixture(t, true)
	record := takeSnapshot(t, fixture, "snapshot-interrupted")
	rootAfter := []byte("rootfs-current-before-restore")
	volumeAfter := []byte("volume-current-before-restore")
	if err := os.WriteFile(fixture.rootfs, rootAfter, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.volume, volumeAfter, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.engine.prepareDiskRestore(fixture.vm.ID, record.ID, record.DiskFiles); err != nil {
		t.Fatalf("prepare interrupted restore state: %v", err)
	}
	fixture.engine.supervisor = &fakeProcessSupervisor{active: true, pid: 4242}
	reconciled, adopted, err := fixture.engine.Reconcile(context.Background(), fixture.vm)
	if err != nil {
		t.Fatalf("reconcile interrupted restore: %v", err)
	}
	if adopted || reconciled.State != "failed" {
		t.Fatalf("interrupted restore VM was incorrectly adopted: vm=%+v adopted=%v", reconciled, adopted)
	}
	if got, err := os.ReadFile(fixture.rootfs); err != nil || string(got) != string(rootAfter) {
		t.Fatalf("reconciliation did not roll back rootfs: got=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(fixture.volume); err != nil || string(got) != string(volumeAfter) {
		t.Fatalf("reconciliation did not roll back volume: got=%q err=%v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(fixture.vm.Socket), "restore-journal.json")); !os.IsNotExist(err) {
		t.Fatalf("recovery journal remains after reconciliation: %v", err)
	}
}

func TestRecoverDiskRestoreHandlesMissingRootfsWithRollbackCopy(t *testing.T) {
	fixture := newSnapshotFixture(t, true)
	record := takeSnapshot(t, fixture, "snapshot-rootfs-missing")
	rootAfter := []byte("rootfs-before-interrupted-restore")
	volumeAfter := []byte("volume-before-interrupted-restore")
	if err := os.WriteFile(fixture.rootfs, rootAfter, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.volume, volumeAfter, 0o600); err != nil {
		t.Fatal(err)
	}
	journal, err := fixture.engine.prepareDiskRestore(fixture.vm.ID, record.ID, record.DiskFiles)
	if err != nil {
		t.Fatalf("prepare disk restore: %v", err)
	}
	if err := os.Rename(fixture.rootfs, journal.Swaps[0].Stage); err != nil {
		t.Fatalf("simulate crash after old rootfs was moved aside: %v", err)
	}
	if _, err := os.Lstat(fixture.rootfs); !os.IsNotExist(err) {
		t.Fatalf("test setup did not leave rootfs path absent: %v", err)
	}
	if err := fixture.engine.recoverDiskRestore(fixture.vm.ID); err != nil {
		t.Fatalf("recover interrupted rootfs replacement: %v", err)
	}
	if got, err := os.ReadFile(fixture.rootfs); err != nil || string(got) != string(rootAfter) {
		t.Fatalf("rollback did not restore rootfs from its journaled previous file: got=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(fixture.volume); err != nil || string(got) != string(volumeAfter) {
		t.Fatalf("rollback did not restore attached volume: got=%q err=%v", got, err)
	}
}

func TestRecoverDiskRestoreRejectsJournalPathOutsideSnapshotManifest(t *testing.T) {
	fixture := newSnapshotFixture(t, true)
	record := takeSnapshot(t, fixture, "snapshot-journal-tamper")
	vmDir := filepath.Dir(fixture.vm.Socket)
	unrelated := filepath.Join(vmDir, "unrelated.img")
	if err := os.WriteFile(unrelated, []byte("must remain untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	validVolumeStage := filepath.Join(filepath.Dir(fixture.volume), "."+record.ID+"-volume-forged.restore-stage")
	if err := os.WriteFile(validVolumeStage, []byte("must remain untouched until all swaps validate"), 0o600); err != nil {
		t.Fatal(err)
	}
	journal := diskRestoreJournal{
		VMID: fixture.vm.ID, SnapshotID: record.ID,
		Swaps: []diskRestoreSwap{
			{Original: unrelated, Stage: filepath.Join(vmDir, "."+record.ID+"-rootfs-forged.restore-stage"), Previous: unrelated + ".porter-previous-" + record.ID},
			{Original: fixture.volume, Stage: validVolumeStage, Previous: fixture.volume + ".porter-previous-" + record.ID},
		},
	}
	if err := fixture.engine.writeDiskRestoreJournal(journal); err != nil {
		t.Fatal(err)
	}
	if err := fixture.engine.recoverDiskRestore(fixture.vm.ID); err == nil {
		t.Fatal("recovery accepted a journal targeting an unrelated file in the VM directory")
	}
	if got, err := os.ReadFile(unrelated); err != nil || string(got) != "must remain untouched" {
		t.Fatalf("journal validation changed unrelated file: got=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(validVolumeStage); err != nil || string(got) != "must remain untouched until all swaps validate" {
		t.Fatalf("journal validation changed a valid sibling stage before rejecting the journal: got=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(fixture.rootfs); err != nil || string(got) != string(fixture.rootBytes) {
		t.Fatalf("journal validation changed the manifest rootfs: got=%q err=%v", got, err)
	}
}

func TestRestoreSnapshotReturnsErrorWhenCommittedJournalCannotFinalize(t *testing.T) {
	fixture := newSnapshotFixture(t, true)
	record := takeSnapshot(t, fixture, "snapshot-finalize-error")
	previous := fixture.rootfs + ".porter-previous-" + record.ID
	hookErr := make(chan error, 1)
	supervisor := &fakeProcessSupervisor{}
	supervisor.afterResume = func() {
		if err := os.Remove(previous); err != nil {
			hookErr <- err
			return
		}
		hookErr <- os.Symlink(fixture.rootfs, previous)
	}
	fixture.engine.supervisor = supervisor
	vm, err := fixture.engine.RestoreSnapshot(context.Background(), fixture.vm, restoreSpec(record))
	if hookErrValue := <-hookErr; hookErrValue != nil {
		t.Fatalf("inject finalization failure: %v", hookErrValue)
	}
	if err == nil || !strings.Contains(err.Error(), "journal finalization failed") {
		t.Fatalf("committed restore finalization failure was hidden: vm=%+v err=%v", vm, err)
	}
	if vm.State != "running" || !supervisor.active {
		t.Fatalf("committed restore result did not report running VM: vm=%+v active=%v", vm, supervisor.active)
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(fixture.vm.Socket), "restore-journal.json")); err != nil {
		t.Fatalf("failed journal finalization did not preserve the recovery journal: %v", err)
	}
	if err := os.Remove(previous); err != nil {
		t.Fatal(err)
	}
	if err := fixture.engine.recoverDiskRestore(fixture.vm.ID); err != nil {
		t.Fatalf("finish committed journal after removing injected symlink: %v", err)
	}
}

func TestVolumeLeaseIsExclusiveAndRejectsAliases(t *testing.T) {
	base := t.TempDir()
	first := New(base, "fake-firecracker", "", NetworkConfig{})
	second := New(base, "fake-firecracker", "", NetworkConfig{})
	path, err := first.canonicalVolumePath(filepath.Join(base, "volumes", "volume-lock.ext4"), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := first.acquireVolumeLease("vm-one", path); err != nil {
		t.Fatal(err)
	}
	if err := second.acquireVolumeLease("vm-two", path); err == nil {
		t.Fatal("second VM acquired an already-held volume lease")
	}
	if err := first.releaseVolumeLease("vm-one"); err != nil {
		t.Fatal(err)
	}
	if err := second.acquireVolumeLease("vm-two", path); err != nil {
		t.Fatalf("volume lease was not reusable after release: %v", err)
	}
	if err := second.releaseVolumeLease("vm-two"); err != nil {
		t.Fatal(err)
	}

	alias := filepath.Join(base, "volumes", "volume-alias.ext4")
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := second.canonicalVolumePath(path, false); err == nil {
		t.Fatal("volume with a hard-link alias was accepted")
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(base, "volumes", "volume-symlink.ext4")
	if err := os.Symlink(path, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := second.canonicalVolumePath(symlink, false); err == nil {
		t.Fatal("symlinked volume was accepted")
	}
}
