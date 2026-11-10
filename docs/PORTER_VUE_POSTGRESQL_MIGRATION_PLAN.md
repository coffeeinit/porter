# Porter Vue + PostgreSQL migration plan

## Target architecture

- **Web UI:** Vue/Vite is the customer-facing Porter UI. Serve its built assets from the control plane or a same-origin static endpoint.
- **API:** Keep a Go control-plane API. The browser calls one versioned API; it never connects directly to PostgreSQL.
- **Global control-plane state:** PostgreSQL is authoritative for projects, services, releases, instances, fleet placement, operations, and events.
- **Node-local state:** Keep the agent's local SQLite ledger for host-local reconciliation and runtime state. It is not part of the global database migration.
- **Production workload runtime:** Firecracker/KVM only. Git, Dockerfile, and the supported Docker Compose subset are build inputs, not production container runtimes.
- **Build isolation:** Build in disposable Firecracker builder MicroVMs. Do not enable the existing unisolated build override as a substitute.
- **Scope guard:** Do not migrate SaaS billing, customer/reseller accounts, multi-tenant organization RBAC, a marketplace, or templates as part of this cutover.

## What exists now

The active root control plane uses SQLite (`porter/internal/store/store.go`) and serves its embedded HTML (`porter/internal/api/api.go`, `porter/web`). Its image-build API is disabled unless the explicitly unsafe `PORTER_ENABLE_UNISOLATED_BUILDS` override is enabled (`porter/internal/api/build_safety.go`, `porter/internal/api/images_build.go`). Keep that gate closed.

A separate Vue/Vite app lives at `legacy/backend/web`, uses `/api/v1` (`src/lib/api.js`), and is paired with a PostgreSQL-backed legacy backend (`legacy/backend/go.mod`, `legacy/backend/internal/store/store.go`). This is not yet the active root control plane. The two APIs and data models are not interchangeable: their route prefixes and resource shapes differ, and the legacy backend contains broader features outside the agreed Porter scope.

The Vue refresh in this branch is a UI pass, not a production cutover. The service-source wizard, seeded Observability data, per-instance SSH preview, and Fleet control preview are explicitly marked as simulated; they do not start builds, execute guest commands, or mutate nodes.

### Current migration-slice status

The isolated PostgreSQL work now includes forward migrations through version 0007, scoped resource and event stores, metadata-only domains/storage/backups, durable typed operations and command leases, and a separate internal HTTPS v2 agent transport. V2 enrollment issues ten-minute, single-use tokens whose hashes alone are stored; redemption binds an operator-selected same-host or remote-agent target and returns a hash-backed node credential once. The Fleet UI provides manual instructions, and the opt-in v2 agent join stores that credential in a local owner-only file without running remote commands. Server and agent v2 paths remain explicit opt-ins; legacy `/v1` SQLite behavior and data are unchanged. The v2 agent supports only allowlisted VM lifecycle commands and checks for Linux/KVM/Firecracker before dispatch, but tests use a fake runtime and do not establish real Firecracker acceptance or remote production readiness. These slices do not satisfy Phase 3's full persistence/scheduler parity gate, source-build isolation gate, end-to-end production gates, or data-cutover gate.

## Phases

### 1. Freeze the product contract and API boundary

Publish one versioned API contract and map existing names to the UI vocabulary: Project, Service, Release, Instance, and Fleet node. Specify IDs, lifecycle states, pagination, errors, permissions, event formats, and source/build fields. Treat the current Vue/API pairing as presentation reference only until each API route is confirmed. Keep Go as the API boundary and keep PostgreSQL credentials server-side.

**Gate:** Vue's API client has a documented contract; no screen relies on ambiguous meanings such as one legacy `project` being both a project and a deployable service.

### 2. Build the minimal PostgreSQL core

Create versioned, forward-only migrations for the in-scope global state: projects, services, releases, instances/replicas, runtime images, fleet nodes, operations/commands, domains/routes, volumes/snapshots, logs/metrics/traces, and audit events. Preserve stable identifiers and explicit foreign keys. Define indexes, transaction boundaries, retention, backup/restore, and connection-pool settings. Compare the existing PostgreSQL migrations before reusing them; do not copy the entire legacy schema wholesale.

**Gate:** Fresh-database migrations and repeatable CRUD/lifecycle tests pass against PostgreSQL; unsupported SaaS tables and behavior are excluded.

### 3. Move active control-plane persistence behind PostgreSQL

Add PostgreSQL store implementations to the root control plane behind the existing store/service boundary. Migrate all global control-plane reads and writes, including scheduler state and durable agent operations. Keep the agent protocol stable during this phase and leave per-node SQLite in place. Add transaction and failure tests for enroll, heartbeat, placement, command dispatch/result, release rollout/rollback, and backup metadata.

**Gate:** The root control plane can run on PostgreSQL with the same agent contract and passes the existing API, store, scheduler, and lifecycle suites.

### 4. Deliver isolated builds before enabling source deploys

Create a build service that launches a disposable Firecracker builder MicroVM, checks out Git, and handles Dockerfile builds plus the documented single-primary-service Compose subset. Destroy builder state after each job; publish an immutable, digest-addressed guest artifact and build logs. Reject unsupported Compose features rather than silently passing them through. No host Docker build or unisolated fallback may be used for untrusted sources.

**Gate:** KVM acceptance proves the builder boundary, cleanup, cancellation, artifact integrity, and fail-closed behavior when isolation is unavailable. Production instances still launch only through Firecracker/KVM.

### 5. Unify API routes and connect the Vue screens

Implement the contract in the active control plane and update the Vue API adapter once, rather than adding per-view compatibility hacks. Connect the service-source form to real build/release APIs only after Phase 4 passes. Connect Observability to persisted collectors; connect the Fleet controls to authorized operations with queued/running/succeeded/failed states; replace the SSH preview with the approved guest-agent/SSH capability only when its security and audit behavior are ready. Keep UI labels compact and show node, host, agent, IP, and guest diagnostics only in operator views.

**Gate:** UI route tests cover navigation and permission states; end-to-end tests verify a source-to-release-to-instance flow, rollback, logs/metrics/traces, and Fleet actions without simulated success states.

### 6. Cut over the UI and data

Serve the built Vue app as the active user interface after route/API parity is complete. For existing SQLite control-plane data, create a deterministic export/import tool with a dry run, stable ID mapping, per-table counts, checksums, and referential-integrity checks. Take a verified backup, pause writes for the cutover window, import to PostgreSQL, run reconciliation checks, and switch the API to PostgreSQL as the sole writer. Avoid long-lived dual writes.

**Gate:** Counts/checksums match, all projects/services/releases/instances and fleet registrations reconcile, smoke tests pass, and the rollback procedure has been rehearsed against the backup.

### 7. Remove superseded surfaces

After an agreed observation window, remove the embedded HTML as the active UI, retire the duplicate legacy backend path, and remove temporary compatibility routes. Keep the agent's local SQLite database and its recovery tooling. Keep operator-only screens for Fleet, agent enrollment, node controls, and host/guest diagnostics.

**Gate:** One supported web/API/database path remains; agent-local state and Firecracker/KVM workload operation are unchanged by the web/database cleanup.

## Release acceptance checklist

1. PostgreSQL is the only global control-plane database; the browser has no DB credentials.
2. Project/service/release/instance lifecycle, scaling, release rollback, domains/DNS, load balancing, volumes, and crash-consistent backup/restore pass end-to-end tests.
3. Fleet enrollment, heartbeat, cordon, drain, and agent command/result flows are authorized, durable, audited, and observable.
4. Production workloads run only in Firecracker/KVM MicroVMs; there is no Docker/Compose workload runtime.
5. Builds use disposable Firecracker builder MicroVMs and fail closed when KVM isolation is unavailable.
6. Logs, metrics, and traces come from real collectors before the UI removes its sample-data labels.
7. Migration backups, reconciliation, cutover, and rollback are tested before changing the active stack.
