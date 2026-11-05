# Snapshot and restore verification status

## Release status: blocked

The current checkout does not prove a guest-consistent backup or a complete VM recovery. Do not describe snapshot/restore as a verified backup feature or use it to approve a v1.0.0-beta release.

## What the current code does

`porter-agent/internal/runtime/runtime.go` pauses Firecracker, writes Firecracker state and memory snapshot files, then resumes the VM. Restore validates those artifact files and hashes, starts Firecracker with the saved runtime configuration, and loads the state and memory files.

The separate control-plane SQLite backup uses `Store.BackupTo` in `porter/internal/store/store.go`. That protects the control-plane database; it does not back up VM root filesystems or attached data volumes.

## What is not proven

- **Guest and application consistency:** Firecracker pause is not a guest filesystem freeze or an application flush. The guest init has no demonstrated freeze/thaw control channel, so even verified disk recovery would be crash-consistent only unless a quiescence handshake is implemented and tested. The current path has no demonstrated fail-closed behavior if quiescence fails.
- **Disk recovery:** the current snapshot path does not copy and checksum the rootfs and every attached volume as snapshot artifacts. Restore reuses the configured disk paths, so it does not establish restoration of their contents.
- **Shared-volume safety:** the current path does not prove exclusive attachment or coordinate concurrent writers before a snapshot or restore.
- **Transactional recovery:** the current path does not demonstrate an atomic disk swap with a durable rollback journal that recovers from interruption at each restore step.
- **End-to-end acceptance:** no current active KVM acceptance test proves guest data survives snapshot, restore, process failure, and VM/host restart.

## Historical E2E note

The earlier nginx snapshot/restore sequence in this file is not current acceptance evidence. The legacy `scripts/e2e-snapshot.sh` now fails closed before its old test body; see [`scripts/README.md`](../scripts/README.md). The historical text output cannot establish root/data disk recovery, guest quiescence, shared-volume safety, or crash recovery.

## Required before unblocking

1. Define guest/filesystem/application quiescence, including timeout and failure behavior; prove the VM is thawed after both success and failure.
2. Capture rootfs and all attached data volumes with checksums. Reject or safely coordinate shared writable volumes.
3. Stage disk changes and restore with a durable transaction/rollback journal.
4. Add non-destructive fault-injection tests for every restore step and a real KVM test that verifies guest data bytes after restore and restart.
5. Make an explicit release decision about crash-consistent versus application-consistent backups. Do not describe crash-consistent recovery as application-consistent.
6. Record the exact tested commit and complete test output before changing this status to verified.
