# Snapshot and restore verification status

## Release status: blocked

The selected beta contract is **crash-consistent recovery only**; application-consistent backups are not promised. The current checkout does not prove complete VM crash-consistent recovery. Do not describe snapshot/restore as verified or use it to approve a v1.0.0-beta release until the required disk, volume, rollback, and end-to-end checks pass.

## What the current code does

`porter-agent/internal/runtime/runtime.go` pauses Firecracker, writes Firecracker state and memory snapshot files, then resumes the VM. Restore validates those artifact files and hashes, starts Firecracker with the saved runtime configuration, and loads the state and memory files.

New VM creation now gives each VM a private writable rootfs copy keyed by the SHA-256 of the base image. This prevents new replicas from writing to a shared image. Existing manifests without private-rootfs ownership metadata are treated as legacy: active VMs may continue running, but restart/reapply after they stop and snapshot/restore fail closed; recreate those VMs before relying on disk recovery. This isolation change does not itself back up any disk.

The separate control-plane SQLite backup uses `Store.BackupTo` in `porter/internal/store/store.go`. That protects the control-plane database; it does not back up VM root filesystems or attached data volumes.

## Persistence candidate (not integrated)

Candidate commit `f06bf61d2c261000644e1e5a5e824f49ec3f4888` on `work/porter-edge-persistence` reports crash-consistent snapshots of rootfs, Firecracker state/memory, and attached volumes; exclusive cross-process writable-volume locks that fail closed on conflicts; checksums; rollback-journal disk replacement; and restore-queue delivery of the checksum manifest. It remains separate from the current integration. Do not merge the branch wholesale: its runtime overlaps newer socket/path/TAP/systemd safeguards, and its `Healthy` path accepts a caller-supplied socket without the current canonical-socket check.

Here, candidate `go test ./... -count=1` passed in `porter`, `porter-agent`, and `protocol`; `go test -race ./internal/runtime -count=1` passed in `porter-agent`; focused `legacy/backend` buildkit, DNS, gateway, and store tests passed. The full `legacy/backend` suite could not compile its API/server packages because this worktree lacks the generated `web/dist` embed output. The PostgreSQL-backed domain test and real Firecracker/KVM recovery drill were not run. These results do not verify crash recovery end to end.

## What is not proven

- **Application consistency:** the guest init has no freeze/thaw control channel. Application-consistent backups are explicitly out of beta scope; a future claim would require a tested guest quiescence protocol.
- **Disk recovery:** the current snapshot path does not copy and checksum the rootfs and every attached volume as snapshot artifacts. Restore reuses the configured disk paths, so it does not establish restoration of their contents.
- **Shared-volume safety:** the current path does not prove exclusive attachment or coordinate concurrent writers before a snapshot or restore.
- **Transactional recovery:** the current path does not demonstrate an atomic disk swap with a durable rollback journal that recovers from interruption at each restore step.
- **End-to-end acceptance:** no current active KVM acceptance test proves guest data survives snapshot, restore, process failure, and VM/host restart.

## Historical E2E note

The earlier nginx snapshot/restore sequence in this file is not current acceptance evidence. The legacy `scripts/e2e-snapshot.sh` now fails closed before its old test body; see [`scripts/README.md`](../scripts/README.md). The historical text output cannot establish root/data disk recovery, exclusive volume safety, transactional rollback, or crash recovery.

## Required before unblocking

1. Prove exclusive cross-process locks for every writable volume before VM adoption/reconciliation; reject or stop a conflicting VM safely.
2. Sync Firecracker state/memory and durable disk state according to a documented crash-consistency sequence.
3. Capture rootfs and every attached data volume with checksums.
4. Stage disk changes and restore with a durable transaction/rollback journal.
5. Add fault-injection tests for every restore step and a real KVM test that verifies guest data bytes after restore and restart.
6. Reconcile only reviewed slices into the current integration while preserving its canonical socket/path, TAP ownership, supervisor, and idempotency guards; then pass the end-to-end recovery gate on that exact integration commit. Keep application-consistency explicitly out of scope unless a guest freeze/thaw channel is implemented and tested.
