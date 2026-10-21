# Plan Audit — Old Plans vs Current Reality

**Date:** 2026-10-18  
**Scope:** planning/docs only. No code changes.

## 1. What changed from the old plans

| Old plan / assumption | Current reality |
|---|---|
| 3 binaries: porter, fleet-manager, fleet-agent | 2 projects: `porter` and `porter-agent`; fleet manager is embedded in Porter |
| Porter runs inside a MicroVM | Deferred. V1 runs Porter as an unprivileged host systemd service |
| PostgreSQL authoritative state | V1 uses SQLite (`porter.db`) on the host and `porter-agent.db` per node |
| 845-route legacy API | Current API is small, focused, and MCP-exposed |
| Customers/billing/audit in early scope | Explicitly deferred |
| Docker Compose as runtime | Rejected. Compose is only a build input; OCI is canonical |
| OCI/Docker support only planned | Dockerfile/Docker/OCI/Git/Compose-primary image build is implemented |
| UI planned as Vue/full dashboard | Current embedded single-page dark UI; needs routed multi-page redesign |
| Preview URLs planned | Domain/preview route APIs exist; UI and TLS still need work |
| VM IPs/ports visible to users | Corrected: users must see domains/preview URLs only |
| Ephemeral = dispose app/build | Corrected: ephemeral = no local data persistence; image remains reusable and can run in production |

## 2. Current architecture truth

```text
Host
  ├── porter        unprivileged control plane + API/UI/scheduler/gateway
  │     ├── porter.db
  │     ├── embedded fleet manager
  │     └── image builder (temporary host Docker)
  └── porter-agent  privileged host executor
        ├── porter-agent.db
        ├── Firecracker
        ├── TAP/bridge/NAT
        └── volumes/snapshots
```

- `porter` serves admin UI/API, scheduler, controllers, gateway, MCP.
- `porter-agent` is the only privileged component.
- All workload actions go through agent commands with operation IDs.
- One agent binary works on every host.
- Porter is installed on the first host; agents on every host.

## 3. Current product principles

1. Firecracker is the only workload runtime.
2. OCI digest is the canonical image artifact.
3. Dockerfile/Compose/GitHub are input adapters.
4. Users manage domains and deployments, never VM IPs/ports.
5. Persistent vs ephemeral is about data, not build frequency.
6. Default persistence is `persistent`; previews should be ephemeral.
7. Every action is a durable operation.
8. AI uses MCP and the same authorization path.
9. UI must show honest implementation state.

## 4. What is actually working

- Porter API + SQLite
- one-command agent join
- heartbeat/capacity
- scheduler + command queue
- real Firecracker VM lifecycle
- TAP/bridge/NAT
- kernel `ip=` guest networking
- persistent `/dev/vdb` volume
- snapshot create (pause/resume)
- logs + metrics
- autoscaling / idle / wake
- domains + routes + gateway foundation
- image build: Dockerfile/Docker/OCI/Git/Compose-primary
- guest-init injection into built rootfs
- MCP tools for most resources
- embedded single-page UI

## 5. Misleading old items to stop saying

Do not say:

- "Porter is complete."
- "ACME is done." Only cert/key TLS listener exists.
- "Full GitHub integration is done." Local git clone build exists; GitHub App/webhooks do not.
- "Full Compose is supported." Primary service only.
- "GUI is finished." Single page only.
- "Backups/restore verified." Not yet.
- "Guest HTTP health checks work." Process liveness only.

## 6. Risk list

| Risk | Impact | Mitigation |
|---|---|---|
| Single-page UI becomes unmaintainable | UX debt | move to routed pages now |
| Host Docker is build runtime | security/isolation | move builder into MicroVM |
| Guest app startup unverified | no real traffic | guest-init + MMDS + health checks |
| ACME absent | no public TLS | implement ACME next |
| Volume resize metadata-only | data risk | implement host file resize safely |
| Restore untested | recovery risk | E2E snapshot restore test |
| No CI/meaningful tests | regressions | add test gates |
