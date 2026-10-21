# Missing DevOps Features & Roadmap Suggestions

This is a gap review for a MicroVM-based deployment platform. It extends the
UI prototype feature inventory with backend/DevOps capabilities that are easy
to forget.

Priority: `P0` V1.0.0, `P1` first follow-up, `P2` later, `LATER` not V1.

## 1. Source and build

| Feature | Priority | Notes |
|---|---|---|
| Git provider OAuth/GitHub App | P0 | install, webhooks, branch/PR events |
| GitLab/Gitea webhooks | P1 | generic signed webhook first |
| Repository detection | P0 | framework, Dockerfile, monorepo root |
| BuildKit Dockerfile builds | P0 | OCI output + rollback on failure |
| Prebuilt OCI image deploy | P0 | bypass build path |
| Build cache | P1 | registry/local cache |
| Build logs streaming | P0 | SSE/WebSocket, bounded retention |
| Build secrets | P1 | never in argv or logs |
| Build args/env | P1 | per environment |
| Monorepo/subdirectory support | P1 | root directory + watch paths |
| Buildpacks/nixpacks/railpack | P2 | optional frontends |
| SBOM generation | P1 | per image digest |
| Image vulnerability scan | P1 | required before production promote |
| Image signing/verification | P2 | cosign-style |
| Reproducible image digest | P0 | immutable deployment identity |
| Build concurrency limits | P1 | per project/global |
| Build queue + cancellation | P0 | durable operations |

## 2. Deployment and release

| Feature | Priority | Notes |
|---|---|---|
| Immutable deployments | P0 | image digest + config snapshot |
| Rolling deployment | P0 | health-gated |
| Canary traffic split | P1 | weight 1..100 |
| Blue/green | P1 | alias switch |
| Instant rollback | P0 | previous deployment ID |
| Promote preview -> production | P0 | same artifact |
| Deployment freeze | P1 | block new deploys |
| Concurrency lock | P0 | one active release per app |
| Health checks | P0 | HTTP/TCP/exec |
| Startup/readiness/liveness | P1 | distinct checks |
| Restart policy | P0 | never/on-failure/always |
| Rollout timeout + auto-abort | P0 | no stuck rollouts |
| Deployment diff | P1 | image/config/env changes |
| Release notes/changelog | P2 | from commits |
| Preview TTL cleanup | P0 | PR merge/close/timeout |

## 3. Runtime

| Feature | Priority | Notes |
|---|---|---|
| Firecracker create/start/stop/delete | P0 | direct API |
| Pause/resume | P1 | Firecracker PATCH /vm |
| Snapshot/restore | P1 | VM and volume snapshots |
| Jailer support | P1 | production isolation |
| cgroups enforcement | P0 | CPU/memory |
| ballooning | P2 | reclaim idle memory |
| guest agent via vsock | P1 | exec/logs/health |
| serial console logs | P0 | always available |
| interactive console | P2 | requires guest agent |
| per-VM rootfs cache | P1 | avoid rebuilding identical images |
| graceful shutdown | P0 | ACPI/guest signal |
| crash detection/recovery | P0 | reconcile on agent restart |
| multi-arch amd64/arm64 | P2 | image/node arch matching |
| GPU passthrough | LATER | not V1 |

## 4. Networking

| Feature | Priority | Notes |
|---|---|---|
| Per-project network | P0 | bridge + NAT |
| Per-VM static IP/MAC | P0 | persisted in agent SQLite |
| Gateway host routing | P0 | host -> deployment |
| Custom domains | P1 | CNAME verification |
| Wildcard preview domains | P0 | `<pr>-<app>.<base>` |
| TLS/ACME | P1 | wildcard + custom certs |
| HTTP/2 + WebSocket | P1 | gateway support |
| gRPC/TCP routes | P2 | L4 forwarding |
| Health-aware load balancing | P0 | remove unhealthy replicas |
| Canary weight routing | P1 | route-level weights |
| Egress NAT | P0 | outbound access |
| Egress policy/firewall | P1 | allow/deny destinations |
| Ingress security groups | P1 | per app/port |
| Service discovery/internal DNS | P2 | project-local names |
| IPv6 | P2 | not V1 |
| WireGuard overlay | P1 | optional private fleet network |
| Anycast/edge | LATER | not V1 |

## 5. Storage

| Feature | Priority | Notes |
|---|---|---|
| Ephemeral rootfs | P0 | deleted with VM |
| Persistent ext4 volume | P1 | attach as `/dev/vdb` |
| Attach/detach | P1 | drain/reconcile safe |
| Resize | P1 | offline first |
| Snapshot volume | P1 | crash-consistent |
| Restore snapshot | P1 | target new/existing |
| Backup to object store | P2 | S3-compatible |
| Object storage abstraction | P2 | S3/R2/MinIO |
| Volume placement affinity | P1 | volume follows replica |
| Orphan detection | P1 | agent SQLite vs files |
| Quota/limits | P1 | per project |

## 6. Observability

| Feature | Priority | Notes |
|---|---|---|
| VM logs | P0 | serial + app |
| Build logs | P0 | streaming |
| Gateway request logs | P1 | bounded |
| Metrics: CPU/mem/disk/net | P0 | fleet heartbeat/VM stats |
| Time-series storage | P1 | SQLite may be too small; plan rollup |
| Prometheus endpoint | P2 | optional scrape |
| Distributed traces | P2 | OTLP optional |
| Alerts | P1 | threshold/absence |
| Incidents | P2 | deferred |
| Uptime/SLO | P2 | derived metrics |
| Doctor everywhere | P1 | deterministic, no AI |
| Log search | P1 | SQLite FTS or file index |
| Retention policy | P1 | bounded by default |

## 7. Security

| Feature | Priority | Notes |
|---|---|---|
| Admin auth | P0 | password + API token |
| mTLS agent identity | P0 | Ed25519 cert |
| Join-token expiry/single use | P0 | hashed at rest |
| Secret encryption | P1 | AES-256-GCM |
| Secret redaction | P0 | logs/events/errors |
| CSRF for browser API | P0 | cookie/session mode |
| Rate limiting | P0 | login/enroll |
| Jailer/seccomp | P1 | production |
| Network isolation | P1 | egress policy |
| Image signing/scans | P1 | release gate |
| SSH/console audit | P1 | operational events |
| Compliance audit | LATER | not V1 |

## 8. Fleet operations

| Feature | Priority | Notes |
|---|---|---|
| One-command join | P0 | curl script |
| Outbound mTLS WebSocket | P0 | NAT friendly |
| Node registry/heartbeat | P0 | manager DB |
| Cordon/drain | P0 | safe maintenance |
| Node labels | P1 | scheduling hints |
| Node taints/affinity | P2 | later |
| Node upgrade rollout | P1 | waves + rollback |
| Agent local SQLite | P0 | actual-state ledger |
| Manager/agent independent updates | P0 | signed versions |
| WireGuard overlay | P1 | optional |
| Multi-region | P2 | labels + placement |
| Node failure reschedule | P1 | disruption budget |
| Capacity accounting | P0 | CPU/mem/disk |

## 9. Developer experience

| Feature | Priority | Notes |
|---|---|---|
| Web dashboard | P0 | embedded |
| CLI | P0 | deploy/logs/scale/fleet |
| `porter init` | P1 | detect project config |
| Terraform provider | P2 | API first |
| GitHub Action | P1 | preview/prod deploys |
| Preview URL on PR | P0 | comment later |
| Environment variables UI/CLI | P1 | per env |
| Rollback command | P0 | one command |
| Local dev tunnel | P2 | connect local service to preview domain |
| API docs/OpenAPI | P1 | generated |
| SDK | P2 | Go first |
| Deploy webhooks | P1 | generic signed |

## 10. Control plane / operations

| Feature | Priority | Notes |
|---|---|---|
| SQLite WAL + backup | P0 | `porter backup` |
| Operation ledger | P0 | durable, idempotent |
| Event stream | P0 | SSE |
| Optimistic concurrency | P1 | ETag/If-Match |
| Pagination/filtering | P1 | lists |
| Request IDs | P0 | all APIs |
| Graceful shutdown | P0 | no stuck ops |
| Schema migrations | P0 | embedded |
| Single-binary install | P0 | systemd/Docker |
| HA control plane | LATER | not V1 |
| Multi-tenant RBAC | LATER | not V1 |
| Customer/billing | LATER | not V1 |

## 11. Suggested additions not yet in the prototype

1. **Deployment diff view** before promote/rollback.
2. **Build artifact page**: digest, SBOM, scan result, layer size.
3. **Preview cleanup policy** visible per project.
4. **Environment model** (`preview`, `staging`, `production`) even if UI is simple.
5. **Project settings** for build root/watch paths and healthcheck defaults.
6. **Operation center** with retry/cancel and cross-resource failures.
7. **Node upgrade page** with agent version matrix and rolling upgrade waves.
8. **Fleet manager health page** (connections, cert expiry, command backlog).
9. **Certificate expiry dashboard**.
10. **Capacity planning page**: projected fleet exhaustion.
11. **Cost estimate** per replica/project, clearly non-billing.
12. **API token/CLI onboarding page**.
13. **GitHub App status page** and webhook delivery log.
14. **Image cache status** per node.
15. **Backup restore test** status, not just backup completed.

## 12. Revised implementation phases

### Phase 1 — Control plane skeleton
Porter SQLite, API, admin auth, project/app/deployment/replica CRUD, operations.

### Phase 2 — Fleet manager
Node registry, join token, enrollment, mTLS certs, manager DB, internal API,
agent connection hub.

### Phase 3 — Porter agent
Agent SQLite, enrollment, heartbeat, systemd unit, reconnect, local actual state.

### Phase 4 — Firecracker runtime
Agent Firecracker lifecycle, TAP/NAT, volumes, logs, observed state.

### Phase 5 — Scheduler + reconcilers
Porter -> manager -> agent command flow, node capacity, placement, reconcile.

### Phase 6 — Image/deploy
Prebuilt OCI/rootfs deploy, build pipeline, immutable deployments, rollback.

### Phase 7 — Preview environments
PR webhook, preview URL, ephemeral deployment, TTL cleanup.

### Phase 8 — Networking
Gateway, domains, TLS, canary weights, custom domains.

### Phase 9 — Persistence
Volumes, snapshots, backup/restore, orphan detection.

### Phase 10 — Observability and DevEx
Logs, metrics, doctor, CLI, webhooks, OpenAPI.

### Phase 11 — Hardening
Upgrade flows, compatibility matrix, backup/restore tests, security hardening.

