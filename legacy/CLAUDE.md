# CLAUDE.md — Porter1 Agent Contract

> Consolidated 2026-09-18 from all prior root `*.md`. Canonical root is now minimal:
> `README.md` + `SRS.md` + `CLAUDE.md` (this file).
> Archived (history only, in `docs/archive/`): `ARCHITECTURE_FLOW.md`, `task.md`, `plan.md`,
> `db-rbac-model.md`, `firecracker-docs.md`, `changelog.md`, `2026-09-12-1054-porter1-ideas.md`.
> `references/` = boot artifacts only (atlas/central/pilot/press/sitekit/vyoma/ui
> all removed 2026-09-18, distilled to `docs/`).
> Active research notes: `docs/atlas-central-rbac-flows.md`, `docs/atlas-features.md`,
> `docs/central-features.md`, `docs/db-flows-screens-apis.md`, `docs/screens-audit.md`,
> `docs/build-pipeline.md`, `docs/firecracker-manual.md`, `docs/plan-additions.md`
> (planning gaps + phased suggestions), `docs/vyoma-features.md`.
> Working contract docs: `docs/architecture.md` (living topology/ownership/lifecycle),
> `docs/data-model.md` (migrations/tenancy/RBAC/RLS), `docs/security.md` (boundaries/auth),
> `docs/glossary.md`, `docs/api.md` (~300 routes + gaps), `docs/ux-flows.md` (journeys),
> `docs/operations.md` (runbook), `docs/testing.md` (matrix/e2e/completion),
> `docs/commerce.md` (thin, deferred tables), `docs/adr.md` (7 accepted decisions),
> `docs/completion-audit.md` (flow audit: working vs left, ordered remaining work).
> Execution: `docs/backend-completion-plan.md` (P0–P14 update flow for the Go backend),
> `docs/mvp-completion-plan.md` (full MVP feature checklist from the internal read).

## 1. What Porter is

Self-hosted **MicroVM-native PaaS / hosting control plane**, primarily in **Go**.
Turn bare-metal / cloud VMs / private infra into a managed app platform.

- **Isolation boundary:** Firecracker MicroVM (one process per VM, KVM-backed). Never Docker-as-runtime, never Kubernetes-as-control-plane.
- **Durable truth:** PostgreSQL only. Redis = optional cache/queue/coordination, never authoritative.
- **Style:** declarative control plane + controllers + reconciliation (`spec` desired → `status` observed).
- **Product surface (backend-only right now):** REST API. No frontend (`frontend/`, `web/dist`, Vue removed). Consumers = thin `porter` CLI, SDKs, Terraform/HCL, webhooks/SSE, AI agents — all through the same API + same RBAC.
- **Thesis:** Simple outside, sophisticated inside. `Intent → Plan → AuthZ/Policy/Quota → Desired State → Schedule → Provision → MicroVM → Net/Storage/Gateway/DNS/TLS → Health → Reconcile → Usage/Billing/Audit`.

What Porter is NOT (initially): K8s distro, Docker UI/replacement, hyperscale cloud clone, generic hypervisor, AWS replacement, cPanel/WHMCS/Vercel/Railway/Render/Coolify/Dokploy/CapRover copy. Those are UX references only.

## 2. Normative doc hierarchy (read in this order)

## 2. Normative doc hierarchy (read in this order)

1. **`SRS.md` (v1.0, 67 sections, ~2139 lines) — NORMATIVE product/engineering requirements.** `SHALL/MUST` = mandatory, `SHOULD` = required unless documented reason, `MAY` = optional. Supersedes older ideology-only SRS.
2. **`ARCHITECTURE_FLOW.md` (~1271 lines) — NORMATIVE architecture/flow contract.** Control-plane topology, request lifecycle, deployment/build/OCI→MicroVM/node/scheduler/replica/network/storage/backup/restore/billing/AI/extension/observability flows, implementation order §33, agent rules §34, anti-patterns §35, e2e path §36, completion rule §37, status labels §38.
3. **`task.md` — CURRENT MVP task tracker (canonical, 51 tasks).** T1–T12 core + T1a–T12a subtasks + T13–T18 full-surface groups (G1–G6). Has dependency table + critical path. Keep in sync when working.
4. **`plan.md` — CURRENT backend-only "make it compile" workstream (living, 2026-09-12).** Build-gate history (`internal/resource` fixed, `internal/runtime` reconstruction). Note: it references `references/*.md` paths — those docs now live at **repo root**.
5. **`db-rbac-model.md` — ACTUAL data model.** Tenant hierarchy, capability RBAC, SQL for identity/RBAC/tenancy/compute/network/storage/build/billing/ops, seed data, Atlas auth patterns §11. MVP slice = Identity + RBAC + Tenancy + Services/Replicas/MicroVM + Networks/Domains. **No billing tables until subscriptions ship.**
6. **`firecracker-docs.md` — Consolidated upstream Firecracker reference.** SLA, jailer/seccomp floor, kernel policy (**only 6.18 in support as of 2026-09**), block/balloon/virtio-mem/pmem, snapshot, MMDS V2, metrics, release policy, status matrix (STABLE / DEV-PREVIEW / DEPRECATED / EOL). Networking: single-queue TAP + `/30` NAT + per-interface token-bucket rate limiters + vsock for guestagent.
7. **`2026-09-12-1054-porter1-ideas.md` — Living ideas + appended DB/RBAC model + backend-only hardening + vyoma/Atlas blocks.** North-star (torii → control plane → Nomad → Firecracker), license boundaries (Porter MIT fork OK; torii/press/central/atlas AGPL = patterns only, separate process; Nomad MPL = executor only; vyoma Rust = inspiration only, reimplement natively in Go against Firecracker).
8. **`README.md` — Product thesis, resource model, reconciliation, deployment/networking/storage/commerce/AI-safety summaries, phased dev order (Phase 1–8), release path, repo contract.**
9. **`changelog.md` — Keep-a-Changelog, backend-focused. Check Unreleased first.**

> `ARCHITECTURE_FLOW.md`, `task.md`, `plan.md`, `db-rbac-model.md`, `firecracker-docs.md`,
> `changelog.md`, `2026-09-12-1054-porter1-ideas.md` now live in `docs/archive/` (history only).

## 2b. Origins & reference projects (author intent — binding for agents)

- Author: **Frappe Atlas/Central contributor**, polyglot (Go/Python/Rust/Java/PHP). Porter is deliberately built in **Go for flexibility + deeper Firecracker control** (direct FC API, jailer/seccomp, TAP/vsock, snapshot/MMDs handling without fighting Rust VMM internals). Prior path: Laravel → Go → Rust → Go.
- **Main inspiration = Atlas + Central** (sibling checkouts at `D:\github\atlas`, `D:\github\central`, both AGPL — patterns only, never copy code). Author loves them because they are **pure OSS + SaaS in production at Frappe Cloud scale (millions of users)** — that OSS+SaaS, multi-tenant operational model is what Porter copies at the product level:
  - `atlas/` = regional VM runtime control system for Frappe Cloud V2 (controller + Metal host daemon in Go + HTTP proxy + WG mesh). Take: placement, host daemon, proxy/TLS routing, mesh, auth model.
  - `central/` = control plane for IAM/billing/add-ons (Frappe/Python). Take: IAM, capabilities, teams/billing, Atlas-tunnel integration.
- **Product goal = Portainer-like simplicity, but for Firecracker MicroVMs (not Docker).** Single-pane ops UX over MicroVM-native PaaS — not a Docker UI, not K8s.
- **Closest functional parallel = Vyoma** (REMOVED 2026-09-18, distilled to `docs/vyoma-features.md` — was Rust + Cloud Hypervisor, Apache-2.0, design inspiration only, reimplement natively in Go against Firecracker): OCI→MicroVM (`run <image>`), swarm/VXLAN mesh + deterministic IPAM, compose stacks, COW/snapshot/teleport/hibernate, guest-agent exec/logs (no SSH), `doctor` preflight, privdrop (`vyoma` user + caps), cgroups enforcement, snapshot diffs, vTPM/attestation. See ideas doc vyoma blocks.
- **UI-only reference = SiteKit** (REMOVED 2026-09-18 — was Laravel 12 + Filament 3, kept only for UX/flow ideas in early phase; Porter stays Go, API-first, no Filament/Livewire).
- **Parked / secondary refs:** `references/pilot/` + `references/press-master/` were REMOVED 2026-09-18 (were Frappe bench/press patterns-only); `ui/` (moved to root 2026-09-18: static HTML mockups `apple.html` + `final desing only .html` + `image/` PNGs), `references/rootfs.ext4` + `vmlinux` (local boot-test artifacts).
- Rule: Atlas/Central/press/pilot/torii = AGPL → separate process or pattern-reimplementation only. Vyoma = Rust → Go reimplementation. SiteKit = Laravel → UX reference only.

## 3. Repo layout

```
porter1/
├── CLAUDE.md  (this file)
├── README.md / SRS.md
├── docs/archive/          # ARCHITECTURE_FLOW.md / task.md / plan.md / db-rbac-model.md
│                          # firecracker-docs.md / changelog.md / 2026-09-12-1054-porter1-ideas.md
├── backend/               # <-- all Go work happens here
│   ├── cmd/porter/        # server|worker|kernel|version entrypoint
│   ├── cmd/porter-cli/    # thin CLI (same API)
│   ├── internal/          # api, auth, rbac, resource, types, store, controller,
│   │                      #   scheduler, runtime, firecracker, agent, network/netmgr,
│   │                      #   gateway, dns, tls, certificate, storage/volumes, build/buildkit,
│   │                      #   deployment, imagecatalog, compose, health, observability,
│   │                      #   metrics, logging, event, workflow, policy, billing, ai, etc. (43 pkgs)
│   ├── migrations/        # 0001..0016 (+0017 planned for capabilities). Postgres-only.
│   ├── api/ configs/ scripts/ tests/ porter.toml(.example)
│   ├── Makefile / go.mod (module porter, go 1.25.0, toolchain go1.26.6) / embed.go
│   └── web/ volumes/
├── ui/                      # static HTML mockups (moved from references/ui 2026-09-18)
└── references/              # boot artifacts only (porterlogo/rootfs/vmlinux; atlas/central/pilot/press/sitekit/vyoma/ui all removed 2026-09-18, distilled to docs/)
```

Planned Go structure (README, illustrative — boundaries > directories): `cmd/porter`, `cmd/porter-cli`, `internal/{api,auth,rbac,policy,resource,controller,scheduler,runtime,firecracker,agent,network,gateway,dns,certificate,storage,build,deployment,observability,billing,workflow,incident,marketplace,ai}`, `migrations/`, `web/`, `api/`, `configs/`, `scripts/`, `tests/`.

## 4. Build / test / run (backend/)

```bash
cd backend
go build ./... && go test ./...   # GATE: must stay green after every task
go vet ./...
make build                         # -> bin/porter (embeds web/dist; VERSION via git describe)
make run ARGS="server"             # ./bin/porter server
make dev                           # bash ../scripts/backend/dev.sh up (Docker Postgres + simulate mode)
DB_URL=postgres://porter:porter@localhost:5432/porter?sslmode=disable ./bin/porter server
```

- Binary embeds frontend (`embed.go`) — but frontend is currently removed; backend-only.
- `store.Migrate` runs on boot; Docker image = Go binary + migrate (no Node step).
- Baseline per `task.md` §1: build+tests pass; `internal/runtime` + `internal/controller` PARTIAL; schema 41 tables PARTIAL; `internal/resource` vs `internal/types` split unresolved (see §7).

## 5. Core invariants (MUST remain true — SRS §67)

1. Firecracker = workload isolation boundary. 2. PostgreSQL = authoritative durable state.
3. Redis = optional acceleration only. 4. OCI images = artifacts, not inherently VMs (explicit guest/rootfs transform required).
5. BuildKit builds; never runs customer workloads. 6. Desired state drives reconciliation.
7. Controllers idempotent (crash/duplicate/delayed events safe). 8. Important actions observable + auditable.
9. AI cannot bypass authZ. 10. Extensions cannot bypass authZ (no direct PG, out-of-process).
11. Workloads isolated from control-plane state (no PG/host sockets/FC sockets/agent creds/other-tenant volumes).
12. Cloudflare optional (first-class DNS/edge, not hard dep). 13. MinIO optional; `ObjectStore` abstraction (S3/R2/B2/Wasabi/Hetzner/MinIO).
14. Billing state separate from runtime state (billing emits policy actions, never kills VMs directly).
15. UI/CLI/automation/AI use the same API (no hidden logic, no superuser AI API).
16. Every complex capability: simple default + precise info + safe automation + advanced escape hatch.

## 6. Universal resource contract (README/SRS §8)

Every major resource: `metadata, spec (desired), status (observed), conditions, generation/observedGeneration, owner/dependencies, labels, annotations, events, relationships, permissions, usage`. API distinguishes desired vs observed; status authoritative or sources telemetry explicitly.

Request lifecycle (ARCH §5): `Client → API → AuthN → AuthZ → RateLimit → Quota/Entitlement → Admission/Policy → Validate → Defaults → Persist desired → Task/Event → return resource+opID → async controller → status/events → stream/poll`.

Reconcile loop everywhere: `Desired → Observe → Diff → Plan → Act → Verify → Status → Repeat`.

## 7. Auth / RBAC / tenancy (db-rbac-model §§1–5,11; task.md T1–T5)

- Hierarchy: `Platform → Reseller? → Organization → Team → Project → Environment → Services/Workloads → Replicas → MicroVMs`. Infra dimension separate: `Provider → Region → Zone → NodePool → Node → MicroVM`. Cross-linked by IDs.
- Principals: `user | service_account | agent` (+ extension, reseller, node/machine identities).
- Capabilities (`namespace.action`): compute (`workload.*`, `replica.scale`, `vm.console`, `snapshot.create`, `backup.restore`), network (`domain.attach`, `certificate.issue`, `route.manage`, `dns.manage`), identity (`user.manage`, `role.assign`, `secret.manage`), billing (`subscription.view`, `invoice.view`, `plan.change`), ops (`audit.read`, `incident.manage`, `impersonate`).
- Roles (seed): `platform_admin, infra_operator, billing_operator, support_operator, reseller_admin, org_admin, org_developer, org_reader, service_account, ai_agent, extension`.
- Edge: `role_assignments(principal, role, scope)`; effective perms = union at scope + ancestors; **deny overrides allow**. No hardcoded role strings in handlers — resolve via `HasCapability(principal, capability, scope)`.
- Defense in depth: API RBAC + Postgres RLS on tenancy tables.
- Atlas patterns to adopt: JWT EdDSA+JWKS + opaque API keys validator; `AuthContext{principal_type, principal_id, scope, claims}`; `X-Tenant-ID` header (fallback JWT `tenant` claim, `"*"` = central); scoped list filtering (`get_permission_query_conditions` style); `ensure_tenant_user` bootstrap; first-user-is-admin seed.
- Tables: `users, service_accounts, auth_sessions, api_keys, capabilities, roles, role_capabilities, role_assignments, resellers, organizations, teams, projects, environments, memberships` (+ compute/network/storage/build/ops per §6; billing only when subscriptions ship).

## 8. Runtime / network / storage essentials

- **Firecracker lifecycle:** `Provisioning → Starting → Running → Degraded → Stopping → Stopped → Failed → Recovering → Deleting → Deleted`. Ops: create/configure/start/stop/pause/resume/reboot/delete/snapshot/restore + vCPU/mem/drives/NICs/kernel/metadata/diagnostics. Privileged host ops isolated behind agent/runtime boundary.
- **OCI→MicroVM (explicit, never direct boot):** `OCI manifest → digest → fetch layers → unpack → runtime transform → guest rootfs → kernel → drives → NIC → FC config → start → workload`. Artifacts immutable by digest.
- **Node enrollment:** token → OS/arch → KVM → FC → BuildKit → net → storage → keypair → verify → capabilities → READY. Identity cryptographic, not IP/HW-derived.
- **Network (Porter owns abstraction, Linux primitives underneath):** IPAM, v4/v6, private/public, TAP/bridge, routing, NAT, internal DNS/SD, ingress/egress policy, SGs/firewall, bandwidth limits+accounting. Per-VM: TAP + `/30` NAT + token-bucket rate limiters (post-boot tunable) + vsock for guestagent (host CID 2, guest ≥3). TAP IP ≠ host subnet. Gateway: HTTP/S, TCP, WS, H2, TLS termination, ACME, routing, LB, health-aware/weighted/canary/blue-green, redirects/headers/rate-limits.
- **Storage split:** ephemeral (root/scratch) / persistent block (independent of VM lifecycle: create/attach/detach/resize/snapshot/backup/restore/clone) / object (`Put/Get/Delete/Copy/List/Multipart/Presign/Stat/Tags/Lifecycle` via `ObjectStore`) / backup (app-aware → checksum/encrypt → object store → external dest; **not trusted until restore verified**; restore can target new resource).
- **Scheduler (own, not Nomad-as-truth — Nomad only executor in north-star):** filter (arch/capacity/health/labels/taints/storage/net/placement) → score (binpack/resource/topology/cost/failure-domain) → persist placement → provision → observe. Decisions reproducible from persisted state.
- **Node failure:** heartbeat lost → Unhealthy → stop placement → disruption-budget check → replacement capacity → recreate → net/volumes → health → traffic → audit.

## 9. Current work: MVP task order (task.md — follow it)

**Gate per task (ARCH §37):** API contract + authZ + persistence + controller/workflow + real infra op + status + events + audit + failure handling + retry/idempotency + tests. Label every feature `IMPLEMENTED / PARTIAL / EXPERIMENTAL / PLANNED` (ARCH §38) — never present PLANNED as shipped.

Critical path: **T1 → T2 → T3 → T7 → T8 → T9 → T11 → T12**. Seed = **T1a** (migration 0017: `capabilities, role_capabilities, role_assignments` + seed).

- T1 Schema hardening (T1a caps/roles, T1b resellers/teams/ip_allocations/micro_vms, T1c Go types) — blocks 2,13 — NO billing tables.
- T2 Capability RBAC engine (`HasCapability`, inherit+deny) → T3 token/auth middleware (JWT+opaque, `AuthContext`, bootstrap admin) → T4 RBAC on every route + scoped read filtering → T10 API surface (idempotency keys, cursor pagination, ETag/`If-Match`, field select, request IDs, structured errors, POST-for-actions).
- T5 tenancy tree wiring → T6 networking (`ip_allocations` + allocator, unify `netmgr` 10.42/16 vs `runtime.NetworkManager` 172.16 + MAC conventions, TAP//30 NAT/limiters).
- T7 register controllers in `cmd/porter` (Deployment/Replica/Node; resolve controller-loop vs `vmEngine` split; unify VM state constants `resource` vs `types` — controllers stay on `types.*`).
- T8 MicroVM chain `service→deployment→replica→micro_vm` + lifecycle events/audit → T9 real rollback + health-gated rollout (0→100 weight).
- T11 API-driven e2e harness (§36 path: install→node→preflight→project→build→schedule→provision→net→start→health→gateway→domain→traffic) + security/tenancy acceptance → T12 completion audit.
- Full surface G1–G6 (block as noted): G1 exec/console (`vm.console`) + logs (`log.read`); G2 BuildKit durable task → immutable digest artifact (+SBOM/sign/scan) → rootfs+kernel→boot; G3 health in reconcile + snapshot crash recovery; G4 scoped encrypted secrets (never in logs) + volumes surviving VM delete (`/dev/vdb`); G5 durable versioned events + audit (`event.read`/`audit.read`); G6 per-resource caps on gateway/DNS/certs/cron/hook/alert/drain/firewall/volume writes.

Known baseline quirks (plan.md + task.md §1): `secret.go SetStatus` param clash; `replica.go ReplicaNetworkSpec` rename done; duplicate `Mode` in runtime; `FCVMConfig.NetworkSpec` retype; missing `runtime.NetworkManager.AllocateVMNetwork`; `VMManager` vs `Manager` duality; `netmgr` vs `runtime` allocator divergence.

## 10. Commercial / observability / AI / safety (summary)

- **Commerce (defer past MVP but model correctly):** `Product→Plan→Price/Charge→Entitlement→Subscription→Service→UsageEvent→Meter→Charge→Invoice→Payment`; meters (vcpu/mem/storage/net/IP/snapshot/backup/build/deploy/fn/queue/log/trace); replayable+deduplicated (idempotency_key); invoice traceable to resource/node; lifecycle DRAFT→PENDING→TRIALING→ACTIVE→PAST_DUE→RECOVERY→SUSPENDED→CANCELLED→EXPIRED; dunning workflow; entitlements (`max_vcpu/mem/storage/domains/projects/IPs/volumes`, `private_networking`, `custom_domains`, `gpu`) gate admission/quota.
- **Observability:** logs/metrics/traces/events/health/alerts/incidents/history/audit across plane/nodes/agents/VMs/builds/gateway/net/storage/billing. Firecracker JSON metrics (60s + FlushMetrics) + structured logs consumed; own PG event/audit store = truth. Alerts (threshold/anomaly/absence/rate/SLO/composite, group/dedupe/silence/escalate) → incidents (severity/timeline/affected/deployments/nodes/alerts/remediation/postmortem) → workflow/AI. SLOs + synthetic (DNS/TLS/HTTP/TCP/API/latency/cert).
- **AI = governed operator, same API/RBAC/audit, no superuser.** Typed tools (`inspect/list/logs/metrics/traces/events/topology/usage/cost/deploy/scale/rollback/restart/snapshot/restore/network/domain/cert/diagnostics`); arbitrary shell = explicit privileged cap. Non-trivial changes produce plan (intent/state/diff/deps/risk/cost/perms/approval/steps/verify/rollback). Safety classes: READ auto / LOW-RISK policy / PROD policy+approval / DESTRUCTIVE explicit approval.
- **Tasks/workflows:** durable `QUEUED/RUNNING/WAITING/SUCCEEDED/FAILED/RETRYING/CANCELLED/PARTIALLY_SUCCEEDED/NEEDS_ATTENTION`; workflows = Trigger/Condition/Action/Schedule. Destructive ops need confirm/RBAC/audit/dep-check/impact-preview/backup option/rollback/maintenance window. Impersonation only via request→policy→consent→time-box→banner→audit→expiry.
- **Doctor:** `porter doctor` (+UI equiv) deterministic checks (OS/arch/kernel/KVM/cgroups/FC/BuildKit/PG/agent/TAP/bridge/routing/firewall/IPAM/gateway/DNS/TLS/storage/scheduler/capacity) returning `check/status/severity/observed/expected/cause/action/evidence`. Useful without AI.

## 11. Agent rules (binding)

From README/SRS §66/ARCH §§33–35:
1. Read SRS before changing architecture; ARCH before implementing a subsystem. Treat SRS normative; don't fake unimplemented work — mark PLANNED/EXPERIMENTAL, preserve resource model/API/desired-actual, add tests, document deviations.
2. Prefer: durable state over memory; reconciliation over imperative; provider-neutral interfaces; native abstractions for core, extensions for external, workflows for deterministic automation, AI for analysis/ambiguous ops.
3. Small coherent Go components over daemon zoo — no `porter-vmd/-networkd/-buildd/-dnsd/-gatewayd/-autoscalerd` without concrete security/reliability/scale/lifecycle justification.
4. UI/CLI/Terraform/AI = API clients; no frontend business logic; no hidden privileged path.
5. Keep `spec`/`status`, idempotent controllers, versioned events (`name, version, payload, scope, occurred_at` + id/correlation/idempotency/actor/source/tenant/resource/timestamp), durable tasks, audit on sensitive ops, secrets encrypted + never in logs.
6. Tests per subsystem: unit + integration + failure-path + idempotency + authZ; Linux integration for runtime/net/scheduler; 18-point e2e (§62: enrollment, preflight, create/restart/recovery, net, build, artifact, deploy, health, gateway, domain/TLS, rollback, node failure, restart-reconcile, quota, secret isolation, audit, backup/restore).
7. First vertical slice wins over feature count: `GitHub → Build → OCI artifact → guest prep → Firecracker → Network → Gateway → Domain → TLS → Healthy`.
8. Releases: v0.1-beta runtime → v0.2 build → v0.3 gateway/DNS/TLS → v0.4 multi-node → v0.5 services → v0.6 observability → v0.7 commerce → v0.8 services/integrations → v0.9 advanced orchestration → v0.10 extensions → v1.0-alpha hardening → v1.1-final.

**Anti-patterns (ARCH §35):** Docker-as-runtime, K8s-as-plane, Redis-as-truth, frontend-only logic, unbounded AI shell, unscoped extension PG, mutable artifacts, non-idempotent controllers, silent escalation, generic "failed" without conditions.

## 12. UX reference (when UI returns)

Apple simplicity × Zerodha precision × Zoho breadth (qualities, not copying). Levels: Intent ("Deploy my API") → Guided (runtime/scale/domain/resources) → Infra (VM/scheduler/net/storage/node/policy). ~3 interactions per common op; explicit states (never generic spinner — show deployment-flow stages); one primary action; global search/command palette; timelines; dense tables; keyboard nav; distinguish UI vs infra latency; Automatic→Explain→Override. Resource pages: Header/Summary/Activity/Config/Observability/Deps/Relationships/Infra/Audit/Danger. Customer-360 + Infra-360 (Provider→…→Workload→Deployment/Net/Storage/Domain/Telemetry) traversals.
