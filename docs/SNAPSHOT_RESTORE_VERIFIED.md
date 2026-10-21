# Snapshot Restore — Verified E2E

Date: 2026-10-19

## Verified path

```text
build nginx app image
-> boot Firecracker VM
-> app healthy
-> gateway returns porter-nginx-ok
-> create snapshot (pause -> snapshot -> resume)
-> restore snapshot
-> operation succeeded
-> gateway still returns porter-nginx-ok
```

## Evidence

```text
HEALTHY
SNAPSHOT_READY
RESTORE_SUCCEEDED
== gateway response after restore ==
porter-nginx-ok
```

## Test

```bash
sudo bash scripts/e2e-snapshot.sh
```

## Notes

- restore preserves the TAP device name
- guest IP/port/health metadata is carried forward
- gateway routing remains functional after restore
- this is the first recovery-path proof for Porter
