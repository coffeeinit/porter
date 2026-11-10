# Snapshot and restore verification status

## Beta gate: blocked

The accepted beta contract is **crash-consistent recovery only**. Porter does not promise application-consistent backups: there is no guest freeze/thaw protocol. The current work adds durable root-disk and attached-volume snapshots with checksums and rollback, but it has not passed real Firecracker/KVM recovery acceptance. Do not call backup/restore production-verified or use it to approve v1.0.0-beta yet.

## What is implemented

The agent pauses a running VM, asks Firecracker to sync snapshot files, syncs writable disks, and captures Firecracker state and memory plus the VM's private rootfs and optional attached volume. Disk backups use a filesystem reflink when available and a synced file copy otherwise. Each artifact has a SHA-256 digest and size. A durable local record supports retrying the same snapshot ID, and the control-plane store accepts only byte-identical duplicate records.

Attached volumes use a cross-process exclusive lock. Runtime paths reject symlinks and hard-linked writable disks, and the agent checks other VM manifests before starting, adopting, snapshotting, restoring, or resizing a volume.

Restore validates snapshot files and hashes before stopping the VM. It stages and syncs replacement disks, then uses a durable journal and previous-disk files to roll back an interrupted restore. On agent reconciliation, an uncommitted journal is rolled back before the VM can be adopted; a committed journal is finalized.

This is a selective integration of reviewed persistence semantics, not a wholesale merge of candidate `f06bf61d2c261000644e1e5a5e824f49ec3f4888`; current private-rootfs, canonical socket/path, TAP, and supervisor guards remain in place.

## What portable tests prove

Fake-based runtime tests cover state/memory/rootfs/volume capture and restore, snapshot retry, checksum tampering rejected before VM shutdown, rollback after a failed snapshot load, recovery of an interrupted disk swap, and exclusive volume locking with symlink/hard-link rejection. The control-plane store tests cover legacy-schema migration, checksum-manifest round-trip, and idempotent record retry. These tests do not run Firecracker or prove behavior through the full API-to-agent deployment path.

## What remains unproven

- **Application consistency:** explicitly out of scope; guest applications are not frozen or quiesced.
- **Real runtime acceptance:** no `/dev/kvm`, Firecracker binary, or KVM-capable runner is available in this environment. A real guest-data restore test has not run.
- **Crash/fault coverage:** portable tests cover rollback after load failure and agent reconciliation of an interrupted swap, but do not inject a host crash at every journal write, rename, directory sync, or power-loss boundary.
- **Full beta flow:** API-to-agent snapshot/restore, host restart, guest data verification on both root and attached disks, network recovery, DNS/TLS, and load balancing still need acceptance on the exact release commit.

## Required before unblocking

1. Run `make kvm-acceptance` on a real KVM-capable runner with guest data written to both the private rootfs and attached volume; verify bytes after restore and restart.
2. Add or run fault-injection coverage across the journal and each disk-swap boundary, including recovery after agent/host restart.
3. Pass safe API-to-agent beta acceptance, plus the other mandatory release gates, on the exact release commit.
4. Keep application consistency labeled unsupported unless a guest freeze/thaw protocol is implemented and tested.
