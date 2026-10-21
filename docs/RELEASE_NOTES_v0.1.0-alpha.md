# Porter v0.1.0-alpha — Foundation Release

This is the first release of the new two-binary Porter architecture.

## Included

### Porter control plane
- SQLite store and schema
- admin token auth
- join tokens + agent enrollment
- node registry / heartbeat
- projects, apps, deployments, replicas
- scheduler + command queue + operation ledger
- autoscaling, idle timeout, scale-to-zero, wake
- volumes, snapshots, logs, metrics
- domains + routes + gateway foundation
- request logging and gateway stats
- image catalog
- Dockerfile/Docker/OCI/Git/Compose-primary image builds
- guest-init injection into built rootfs
- MCP endpoint and tools
- embedded single-page dashboard

### Porter agent
- one binary on every node
- local SQLite
- join + heartbeat
- real Firecracker lifecycle
- TAP/bridge/NAT
- kernel `ip=` guest network setup
- persistent `/dev/vdb` volume
- snapshot create/restore command
- serial log shipping
- process liveness health reporting

### Guest init
- `porter-guest-init` binary
- reads network config from kernel cmdline
- configures eth0/route/DNS
- starts app entrypoint

## Verified
- real Firecracker boot on KVM in WSL
- TAP/bridge/NAT
- persistent volume attach
- snapshot create
- logs/metrics
- Dockerfile -> OCI -> rootfs.ext4
- guest-init injection
- MCP tools
- agent restart preserving VM
- Porter restart preserving SQLite state

## Known limitations
- UI is still single-page
- no ACME automation yet
- no real guest HTTP/TCP health checks yet
- full GitHub App/webhook integration missing
- multi-service Compose missing
- backups/restore verification missing
- image builder uses host Docker
- test coverage is minimal

## Upgrade note
This release is not a drop-in continuation of legacy `v0.0.1-alpha`. The new
implementation lives in `porter/`, `porter-agent/`, and `protocol/`. The old
code is archived under `legacy/`.
