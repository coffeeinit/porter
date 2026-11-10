package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"porter/protocol"
)

const ficlone = 0x40049409

func snapshotFileDigest(path string) (int64, string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 || !privateFileHasSingleLink(info) {
		return 0, "", fmt.Errorf("snapshot artifact must be a non-empty regular file")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return 0, "", err
	}
	if !opened.Mode().IsRegular() || opened.Size() != info.Size() || !os.SameFile(info, opened) || !privateFileHasSingleLink(opened) {
		return 0, "", fmt.Errorf("snapshot artifact changed while being opened")
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return 0, "", err
	}
	if n != info.Size() {
		return 0, "", fmt.Errorf("snapshot artifact size changed while hashing")
	}
	if err := f.Sync(); err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

func syncDiskSource(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 || !privateFileHasSingleLink(info) {
		return fmt.Errorf("writable disk must be a non-empty, exclusively linked regular file")
	}
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	if !opened.Mode().IsRegular() || opened.Size() != info.Size() || !os.SameFile(info, opened) || !privateFileHasSingleLink(opened) {
		return fmt.Errorf("writable disk changed while being opened")
	}
	return f.Sync()
}

// copyFileDurable uses a filesystem reflink when available and falls back to a
// complete copy. The destination is published atomically only after Sync.
func copyFileDurable(source, destination string) error {
	if source == "" || destination == "" {
		return fmt.Errorf("source and destination paths are required")
	}
	source = filepath.Clean(source)
	destination = filepath.Clean(destination)
	if source == destination {
		return fmt.Errorf("source and destination paths must differ")
	}
	sourceInfo, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if sourceInfo.Mode()&os.ModeSymlink != 0 || !sourceInfo.Mode().IsRegular() || sourceInfo.Size() <= 0 {
		return fmt.Errorf("disk image must be a non-empty regular file")
	}
	src, err := os.OpenFile(source, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer src.Close()
	srcInfo, err := src.Stat()
	if err != nil {
		return err
	}
	if !srcInfo.Mode().IsRegular() || srcInfo.Size() != sourceInfo.Size() || !os.SameFile(sourceInfo, srcInfo) {
		return fmt.Errorf("source disk changed while being opened")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("destination already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".porter-disk-copy-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	_, _, cloneErr := syscall.Syscall(syscall.SYS_IOCTL, tmp.Fd(), ficlone, src.Fd())
	if cloneErr != 0 {
		if _, err := src.Seek(0, io.SeekStart); err != nil {
			_ = tmp.Close()
			return err
		}
		if err := tmp.Truncate(0); err != nil {
			_ = tmp.Close()
			return err
		}
		if _, err := io.Copy(tmp, src); err != nil {
			_ = tmp.Close()
			return err
		}
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Link(tmpPath, destination); err != nil {
		return err
	}
	if err := os.Remove(tmpPath); err != nil {
		_ = os.Remove(destination)
		return err
	}
	return syncDir(filepath.Dir(destination))
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func validSnapshotHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func reserveSnapshotPath(dir, snapshotID, suffix string) (string, error) {
	f, err := os.CreateTemp(dir, "."+snapshotID+"-*"+suffix)
	if err != nil {
		return "", err
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	if err := os.Remove(path); err != nil {
		return "", err
	}
	return path, nil
}

func reserveDiskRestorePath(dir, snapshotID, kind string) (string, error) {
	if !safeSnapshotID(snapshotID) || (kind != "rootfs" && kind != "volume") {
		return "", fmt.Errorf("invalid disk restore staging identity")
	}
	f, err := os.CreateTemp(dir, "."+snapshotID+"-"+kind+"-*.restore-stage")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	if err := os.Remove(path); err != nil {
		return "", err
	}
	return path, nil
}

func publishSnapshotPath(temporary, destination string) error {
	if filepath.Dir(temporary) != filepath.Dir(destination) {
		return fmt.Errorf("snapshot temporary and final paths must share a directory")
	}
	f, err := os.OpenFile(temporary, os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		_ = f.Close()
		return fmt.Errorf("snapshot temporary artifact is not a non-empty regular file")
	}
	if err := f.Chmod(0o600); err != nil {
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
	if err := os.Link(temporary, destination); err != nil {
		return err
	}
	if err := os.Remove(temporary); err != nil {
		_ = os.Remove(destination)
		return err
	}
	return syncDir(filepath.Dir(destination))
}

func (e *Engine) loadSnapshotRecord(dir, vmID, snapshotID string, manifest runtimeManifest) (protocol.SnapshotRecord, bool, error) {
	path := filepath.Join(dir, snapshotID+".record.json")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		record := protocol.SnapshotRecord{
			ID: snapshotID, VMID: vmID,
			StatePath: filepath.Join(dir, snapshotID+".snap"),
			MemPath:   filepath.Join(dir, snapshotID+".mem"),
		}
		paths := []string{record.StatePath, record.MemPath}
		kinds := []struct{ kind, original string }{{"rootfs", manifest.RootfsPath}}
		if manifest.VolumePath != "" {
			kinds = append(kinds, struct{ kind, original string }{"volume", manifest.VolumePath})
		}
		for _, disk := range kinds {
			paths = append(paths, filepath.Join(dir, snapshotID+"."+disk.kind+".disk"))
		}
		present := 0
		for _, candidate := range paths {
			candidateInfo, statErr := os.Lstat(candidate)
			if os.IsNotExist(statErr) {
				continue
			}
			if statErr != nil {
				return protocol.SnapshotRecord{}, false, statErr
			}
			if candidateInfo.Mode()&os.ModeSymlink != 0 || !candidateInfo.Mode().IsRegular() || !privateFileHasSingleLink(candidateInfo) {
				return protocol.SnapshotRecord{}, false, fmt.Errorf("uncommitted snapshot artifact is not a single-link regular file")
			}
			present++
		}
		if present == 0 {
			return protocol.SnapshotRecord{}, false, nil
		}
		if present != len(paths) {
			return protocol.SnapshotRecord{}, false, fmt.Errorf("snapshot artifacts are incomplete and have no durable record")
		}
		if err := validateSnapshotArtifact(dir, record.StatePath, ".snap"); err != nil {
			return protocol.SnapshotRecord{}, false, err
		}
		if err := validateSnapshotArtifact(dir, record.MemPath, ".mem"); err != nil {
			return protocol.SnapshotRecord{}, false, err
		}
		var stateSize, memSize int64
		stateSize, record.StateSHA256, err = snapshotFileDigest(record.StatePath)
		if err != nil {
			return protocol.SnapshotRecord{}, false, err
		}
		memSize, record.MemSHA256, err = snapshotFileDigest(record.MemPath)
		if err != nil {
			return protocol.SnapshotRecord{}, false, err
		}
		for _, disk := range kinds {
			backup := filepath.Join(dir, snapshotID+"."+disk.kind+".disk")
			size, digest, digestErr := snapshotFileDigest(backup)
			if digestErr != nil {
				return protocol.SnapshotRecord{}, false, digestErr
			}
			record.DiskFiles = append(record.DiskFiles, protocol.SnapshotFile{Kind: disk.kind, OriginalPath: disk.original, BackupPath: backup, SizeBytes: size, SHA256: digest})
		}
		diskSize, err := e.validateRestoreDiskFiles(vmID, snapshotID, manifest, record.DiskFiles)
		if err != nil {
			return protocol.SnapshotRecord{}, false, err
		}
		record.SizeBytes = stateSize + memSize + diskSize
		if err := persistSnapshotRecord(dir, record); err != nil {
			return protocol.SnapshotRecord{}, false, fmt.Errorf("persist reconstructed snapshot record: %w", err)
		}
		return record, true, nil
	}
	if err != nil {
		return protocol.SnapshotRecord{}, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !privateFileHasSingleLink(info) {
		return protocol.SnapshotRecord{}, false, fmt.Errorf("local snapshot record must be a single-link regular file")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return protocol.SnapshotRecord{}, false, err
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = f.Close()
		return protocol.SnapshotRecord{}, false, fmt.Errorf("local snapshot record changed while being opened")
	}
	data, readErr := io.ReadAll(f)
	closeErr := f.Close()
	if readErr != nil || closeErr != nil {
		return protocol.SnapshotRecord{}, false, fmt.Errorf("read local snapshot record: %w", errors.Join(readErr, closeErr))
	}
	var record protocol.SnapshotRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return protocol.SnapshotRecord{}, false, fmt.Errorf("decode local snapshot record: %w", err)
	}
	if record.ID != snapshotID || record.VMID != vmID || record.StatePath != filepath.Join(dir, snapshotID+".snap") || record.MemPath != filepath.Join(dir, snapshotID+".mem") {
		return protocol.SnapshotRecord{}, false, fmt.Errorf("local snapshot record identity or paths do not match")
	}
	if err := validateSnapshotArtifact(dir, record.StatePath, ".snap"); err != nil {
		return protocol.SnapshotRecord{}, false, err
	}
	if err := validateSnapshotArtifact(dir, record.MemPath, ".mem"); err != nil {
		return protocol.SnapshotRecord{}, false, err
	}
	stateSize, stateHash, err := snapshotFileDigest(record.StatePath)
	if err != nil || stateHash != record.StateSHA256 {
		return protocol.SnapshotRecord{}, false, fmt.Errorf("local snapshot state failed checksum verification")
	}
	memSize, memHash, err := snapshotFileDigest(record.MemPath)
	if err != nil || memHash != record.MemSHA256 {
		return protocol.SnapshotRecord{}, false, fmt.Errorf("local snapshot memory failed checksum verification")
	}
	diskSize, err := e.validateRestoreDiskFiles(vmID, snapshotID, manifest, record.DiskFiles)
	if err != nil {
		return protocol.SnapshotRecord{}, false, err
	}
	if record.SizeBytes != stateSize+memSize+diskSize {
		return protocol.SnapshotRecord{}, false, fmt.Errorf("local snapshot aggregate size does not match its files")
	}
	return record, true, nil
}

func persistSnapshotRecord(dir string, record protocol.SnapshotRecord) error {
	path := filepath.Join(dir, record.ID+".record.json")
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("local snapshot record already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".snapshot-record-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
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
	if err := os.Link(tmpPath, path); err != nil {
		return err
	}
	if err := os.Remove(tmpPath); err != nil {
		_ = os.Remove(path)
		return err
	}
	return syncDir(dir)
}

func (e *Engine) resolvedBaseDir(create bool) (string, error) {
	if e.BaseDir == "" {
		return "", fmt.Errorf("VM base directory is required")
	}
	base, err := filepath.Abs(e.BaseDir)
	if err != nil {
		return "", err
	}
	if create {
		if err := os.MkdirAll(base, 0o700); err != nil {
			return "", err
		}
	}
	resolved, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("VM base directory is not a real directory")
	}
	return filepath.Clean(resolved), nil
}

func (e *Engine) resolvedVolumeDir(create bool) (string, error) {
	base, err := e.resolvedBaseDir(create)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "volumes")
	if create {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("volume directory must be a real directory")
	}
	if create {
		if err := os.Chmod(dir, 0o700); err != nil {
			return "", err
		}
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || filepath.Clean(resolved) != filepath.Clean(dir) {
		return "", fmt.Errorf("volume directory resolves through a symlink")
	}
	return dir, nil
}

func (e *Engine) canonicalVolumePath(path string, allowMissing bool) (string, error) {
	if path == "" {
		return "", fmt.Errorf("volume path is required")
	}
	dir, err := e.resolvedVolumeDir(allowMissing)
	if err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil || filepath.Clean(parent) != filepath.Clean(dir) {
		return "", fmt.Errorf("volume path is outside the configured volume directory")
	}
	name := filepath.Base(absolute)
	if !strings.HasSuffix(name, ".ext4") || !safePathComponent(strings.TrimSuffix(name, ".ext4"), 64) {
		return "", fmt.Errorf("volume path has an invalid identity")
	}
	canonical := filepath.Join(dir, name)
	info, err := os.Lstat(canonical)
	if os.IsNotExist(err) && allowMissing {
		return canonical, nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 || !privateFileHasSingleLink(info) {
		return "", fmt.Errorf("volume must be a non-empty, single-link regular file")
	}
	return canonical, nil
}

func (e *Engine) volumeUsedByOtherVM(vmID, volumePath string) (bool, error) {
	target, err := e.canonicalVolumePath(volumePath, true)
	if err != nil {
		return false, err
	}
	base, err := e.resolvedBaseDir(false)
	if err != nil {
		return false, err
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		otherID := entry.Name()
		if !entry.IsDir() || otherID == vmID || !validVMID(otherID) {
			continue
		}
		manifestPath, err := e.manifestPath(otherID, false)
		if err != nil {
			return false, err
		}
		if _, err := os.Lstat(manifestPath); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return false, err
		}
		manifest, err := e.readManifest(otherID)
		if err != nil {
			return false, fmt.Errorf("cannot determine whether volume is shared with VM %s: %w", otherID, err)
		}
		if manifest.VolumePath == "" {
			continue
		}
		otherPath, err := e.canonicalVolumePath(manifest.VolumePath, true)
		if err != nil {
			return false, fmt.Errorf("cannot validate VM %s volume path: %w", otherID, err)
		}
		if otherPath == target {
			return true, nil
		}
	}
	return false, nil
}

type diskRestoreSwap struct {
	Original string `json:"original"`
	Stage    string `json:"stage"`
	Previous string `json:"previous"`
}

type diskRestoreJournal struct {
	VMID       string            `json:"vm_id"`
	SnapshotID string            `json:"snapshot_id"`
	Committed  bool              `json:"committed"`
	Swaps      []diskRestoreSwap `json:"swaps"`
}

func (e *Engine) restoreJournalPath(vmID string) (string, error) {
	dir, err := e.vmDir(vmID, false)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "restore-journal.json"), nil
}

func (e *Engine) validateRestoreDiskFiles(vmID string, snapshotID string, manifest runtimeManifest, files []protocol.SnapshotFile) (int64, error) {
	if len(files) == 0 || len(files) > 2 {
		return 0, fmt.Errorf("snapshot is missing root-disk or volume metadata")
	}
	vmDir, err := e.vmDir(vmID, false)
	if err != nil {
		return 0, err
	}
	expected := map[string]string{"rootfs": filepath.Clean(manifest.RootfsPath)}
	if manifest.VolumePath != "" {
		expected["volume"] = filepath.Clean(manifest.VolumePath)
	}
	if expected["rootfs"] == "" {
		return 0, fmt.Errorf("saved VM root-disk path is missing")
	}
	seen := map[string]bool{}
	var total int64
	for _, file := range files {
		if file.Kind != "rootfs" && file.Kind != "volume" || seen[file.Kind] {
			return 0, fmt.Errorf("snapshot has an unknown or duplicate disk kind")
		}
		original, ok := expected[file.Kind]
		if !ok || file.OriginalPath != original || file.SizeBytes <= 0 || !validSnapshotHash(file.SHA256) {
			return 0, fmt.Errorf("snapshot %s disk metadata does not match the saved VM", file.Kind)
		}
		backup := filepath.Join(vmDir, snapshotID+"."+file.Kind+".disk")
		if file.BackupPath != backup {
			return 0, fmt.Errorf("snapshot %s disk path is outside the VM snapshot directory", file.Kind)
		}
		if err := validateSnapshotArtifact(vmDir, backup, ".disk"); err != nil {
			return 0, fmt.Errorf("invalid snapshot %s disk artifact: %w", file.Kind, err)
		}
		size, digest, err := snapshotFileDigest(backup)
		if err != nil {
			return 0, fmt.Errorf("verify snapshot %s disk: %w", file.Kind, err)
		}
		if size != file.SizeBytes || digest != file.SHA256 {
			return 0, fmt.Errorf("snapshot %s disk checksum or size mismatch", file.Kind)
		}
		seen[file.Kind] = true
		total += size
	}
	if !seen["rootfs"] || (manifest.VolumePath != "" && !seen["volume"]) || (manifest.VolumePath == "" && seen["volume"]) {
		return 0, fmt.Errorf("snapshot disk set does not match the saved VM root disk and attached volume")
	}
	return total, nil
}

func (e *Engine) prepareDiskRestore(vmID, snapshotID string, files []protocol.SnapshotFile) (diskRestoreJournal, error) {
	journal := diskRestoreJournal{VMID: vmID, SnapshotID: snapshotID}
	for _, file := range files {
		original := filepath.Clean(file.OriginalPath)
		parent := filepath.Dir(original)
		resolvedParent, err := filepath.EvalSymlinks(parent)
		if err != nil || filepath.Clean(resolvedParent) != parent {
			return journal, fmt.Errorf("disk parent directory is missing or contains a symlink")
		}
		info, err := os.Lstat(original)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !privateFileHasSingleLink(info) {
			return journal, fmt.Errorf("refusing to replace a non-regular disk image")
		}
		stage, err := reserveDiskRestorePath(parent, snapshotID, file.Kind)
		if err != nil {
			return journal, err
		}
		if err := copyFileDurable(file.BackupPath, stage); err != nil {
			_ = os.Remove(stage)
			for _, prev := range journal.Swaps {
				_ = os.Remove(prev.Stage)
			}
			return journal, fmt.Errorf("stage %s disk restore: %w", file.Kind, err)
		}
		stageSize, stageDigest, err := snapshotFileDigest(stage)
		if err != nil || stageSize != file.SizeBytes || stageDigest != file.SHA256 {
			_ = os.Remove(stage)
			for _, prev := range journal.Swaps {
				_ = os.Remove(prev.Stage)
			}
			return journal, fmt.Errorf("staged %s disk checksum or size mismatch", file.Kind)
		}
		previous := original + ".porter-previous-" + snapshotID
		if _, err := os.Lstat(previous); err == nil {
			_ = os.Remove(stage)
			for _, prev := range journal.Swaps {
				_ = os.Remove(prev.Stage)
			}
			return journal, fmt.Errorf("disk restore rollback path already exists")
		} else if !os.IsNotExist(err) {
			_ = os.Remove(stage)
			for _, prev := range journal.Swaps {
				_ = os.Remove(prev.Stage)
			}
			return journal, err
		}
		journal.Swaps = append(journal.Swaps, diskRestoreSwap{Original: original, Stage: stage, Previous: previous})
	}
	if len(journal.Swaps) == 0 {
		return journal, fmt.Errorf("snapshot contains no disk images")
	}
	if err := e.writeDiskRestoreJournal(journal); err != nil {
		for _, swap := range journal.Swaps {
			_ = os.Remove(swap.Stage)
		}
		return journal, err
	}
	for _, swap := range journal.Swaps {
		if err := os.Rename(swap.Original, swap.Previous); err != nil {
			_ = e.recoverDiskRestore(vmID)
			return journal, fmt.Errorf("preserve current disk before restore: %w", err)
		}
		if err := os.Rename(swap.Stage, swap.Original); err != nil {
			_ = e.recoverDiskRestore(vmID)
			return journal, fmt.Errorf("install restored disk: %w", err)
		}
		if err := syncDir(filepath.Dir(swap.Original)); err != nil {
			_ = e.recoverDiskRestore(vmID)
			return journal, err
		}
	}
	return journal, nil
}

func (e *Engine) writeDiskRestoreJournal(journal diskRestoreJournal) error {
	path, err := e.restoreJournalPath(journal.VMID)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	data, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".restore-journal-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
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
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return syncDir(dir)
}

func (e *Engine) recoverDiskRestore(vmID string) error {
	path, err := e.restoreJournalPath(vmID)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !privateFileHasSingleLink(info) {
		return fmt.Errorf("restore journal is not a single-link regular file")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = f.Close()
		return fmt.Errorf("restore journal changed while being opened")
	}
	data, readErr := io.ReadAll(f)
	closeErr := f.Close()
	if readErr != nil || closeErr != nil {
		return fmt.Errorf("read restore journal: %w", errors.Join(readErr, closeErr))
	}
	var journal diskRestoreJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return fmt.Errorf("decode restore journal: %w", err)
	}
	if journal.VMID != vmID || !validVMID(journal.VMID) || !validVMID(journal.SnapshotID) || len(journal.Swaps) == 0 {
		return fmt.Errorf("restore journal identity or contents are invalid")
	}
	vmDir, err := e.vmDir(vmID, false)
	if err != nil {
		return err
	}
	manifest, err := e.readManifest(vmID)
	if err != nil {
		return fmt.Errorf("read VM manifest for restore recovery: %w", err)
	}
	rootfsPath, err := validatePrivateRootfsPath(vmDir, manifest)
	if err != nil {
		return fmt.Errorf("validate VM rootfs path for restore recovery: %w", err)
	}
	if manifest.VolumePath != "" {
		savedVolumePath := manifest.VolumePath
		manifest.VolumePath, err = e.canonicalVolumePath(savedVolumePath, true)
		if err != nil {
			return fmt.Errorf("validate attached volume for restore recovery: %w", err)
		}
		if savedVolumePath != manifest.VolumePath {
			return fmt.Errorf("attached-volume path is not canonical for restore recovery")
		}
	}
	if manifest.RootfsPath != rootfsPath {
		return fmt.Errorf("VM rootfs path is not canonical for restore recovery")
	}
	record, found, err := e.loadSnapshotRecord(vmDir, vmID, journal.SnapshotID, manifest)
	if err != nil {
		return fmt.Errorf("validate restore snapshot record: %w", err)
	}
	if !found || len(journal.Swaps) != len(record.DiskFiles) {
		return fmt.Errorf("restore journal does not match a complete persisted snapshot")
	}
	expected := make(map[string]protocol.SnapshotFile, len(record.DiskFiles))
	for _, file := range record.DiskFiles {
		expected[file.OriginalPath] = file
	}
	seen := make(map[string]bool, len(record.DiskFiles))
	for _, swap := range journal.Swaps {
		original := filepath.Clean(swap.Original)
		file, ok := expected[original]
		if !ok || seen[original] || swap.Original != original {
			return fmt.Errorf("restore journal refers to an unexpected or duplicate disk")
		}
		base := filepath.Base(swap.Stage)
		if !safeSnapshotID(journal.SnapshotID) || !filepath.IsAbs(swap.Original) || original != file.OriginalPath ||
			!filepath.IsAbs(swap.Previous) || !filepath.IsAbs(swap.Stage) || swap.Previous != filepath.Clean(swap.Previous) || swap.Stage != filepath.Clean(swap.Stage) ||
			filepath.Dir(original) != filepath.Dir(swap.Previous) || filepath.Dir(original) != filepath.Dir(swap.Stage) ||
			swap.Previous != original+".porter-previous-"+journal.SnapshotID ||
			!strings.HasPrefix(base, "."+journal.SnapshotID+"-"+file.Kind+"-") || !strings.HasSuffix(base, ".restore-stage") {
			return fmt.Errorf("restore journal contains invalid disk paths")
		}
		if file.Kind == "rootfs" && original != rootfsPath || file.Kind == "volume" && original != manifest.VolumePath {
			return fmt.Errorf("restore journal disk path does not match its manifest kind")
		}
		originalInfo, originalErr := os.Lstat(original)
		if originalErr == nil && (originalInfo.Mode()&os.ModeSymlink != 0 || !originalInfo.Mode().IsRegular() || originalInfo.Size() <= 0 || !privateFileHasSingleLink(originalInfo)) {
			return fmt.Errorf("restore journal original disk is not a single-link regular file")
		}
		if originalErr != nil && !os.IsNotExist(originalErr) {
			return originalErr
		}
		previousInfo, previousErr := os.Lstat(swap.Previous)
		if previousErr == nil && (previousInfo.Mode()&os.ModeSymlink != 0 || !previousInfo.Mode().IsRegular() || previousInfo.Size() <= 0 || !privateFileHasSingleLink(previousInfo)) {
			return fmt.Errorf("restore journal rollback disk is not a single-link regular file")
		}
		if previousErr != nil && !os.IsNotExist(previousErr) {
			return previousErr
		}
		if originalErr != nil && previousErr != nil {
			return fmt.Errorf("restore rollback lost both original and backup disk")
		}
		if journal.Committed && originalErr != nil {
			return fmt.Errorf("committed restore is missing its installed disk")
		}
		stageInfo, stageErr := os.Lstat(swap.Stage)
		if stageErr == nil && (stageInfo.Mode()&os.ModeSymlink != 0 || !stageInfo.Mode().IsRegular() || stageInfo.Size() <= 0 || !privateFileHasSingleLink(stageInfo)) {
			return fmt.Errorf("restore journal stage is not a single-link regular file")
		}
		if stageErr != nil && !os.IsNotExist(stageErr) {
			return stageErr
		}
		seen[original] = true
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("restore journal does not cover every snapshot disk")
	}
	for i := len(journal.Swaps) - 1; i >= 0; i-- {
		swap := journal.Swaps[i]
		if journal.Committed {
			for _, candidate := range []string{swap.Previous, swap.Stage} {
				if err := removeRegularIfExists(candidate); err != nil {
					return err
				}
			}
			if err := syncDir(filepath.Dir(swap.Original)); err != nil {
				return err
			}
			continue
		}
		if _, err := os.Lstat(swap.Previous); err == nil {
			if err := removeRegularIfExists(swap.Original); err != nil {
				return err
			}
			if err := os.Rename(swap.Previous, swap.Original); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		} else if _, originalErr := os.Lstat(swap.Original); os.IsNotExist(originalErr) {
			return fmt.Errorf("restore rollback lost both original and backup disk")
		}
		if err := removeRegularIfExists(swap.Stage); err != nil {
			return err
		}
		if err := syncDir(filepath.Dir(swap.Original)); err != nil {
			return err
		}
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func removeRegularIfExists(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !privateFileHasSingleLink(info) {
		return fmt.Errorf("refusing to remove non-regular restore file")
	}
	return os.Remove(path)
}

type volumeLease struct {
	path string
	file *os.File
}

func openVolumeLock(path string) (*os.File, error) {
	lockPath := path + ".porter.lock"
	if info, err := os.Lstat(lockPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !privateFileHasSingleLink(info) {
			return nil, fmt.Errorf("volume lock must be a single-link regular file")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !privateFileHasSingleLink(info) {
		_ = f.Close()
		return nil, fmt.Errorf("volume lock must be a single-link regular file")
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return nil, fmt.Errorf("writable volume is already leased by another VM")
		}
		return nil, fmt.Errorf("lock writable volume: %w", err)
	}
	return f, nil
}

func (e *Engine) acquireVolumeLease(vmID, volumePath string) error {
	if !validVMID(vmID) || volumePath == "" {
		return fmt.Errorf("VM identity and volume path are required for a volume lease")
	}
	path, err := e.canonicalVolumePath(volumePath, true)
	if err != nil {
		return err
	}
	e.volumeMu.Lock()
	defer e.volumeMu.Unlock()
	if e.volumeLeases == nil {
		e.volumeLeases = make(map[string]*volumeLease)
	}
	if held := e.volumeLeases[vmID]; held != nil {
		if held.path == path {
			return nil
		}
		return fmt.Errorf("VM already holds a lease for a different writable volume")
	}
	f, err := openVolumeLock(path)
	if err != nil {
		return err
	}
	e.volumeLeases[vmID] = &volumeLease{path: path, file: f}
	return nil
}

func (e *Engine) releaseVolumeLease(vmID string) error {
	e.volumeMu.Lock()
	defer e.volumeMu.Unlock()
	lease := e.volumeLeases[vmID]
	if lease == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(lease.file.Fd()), syscall.LOCK_UN)
	if unlockErr != nil {
		return unlockErr
	}
	closeErr := lease.file.Close()
	delete(e.volumeLeases, vmID)
	return closeErr
}
