# PostgreSQL foundation for the control plane

## Status

The beta control-plane server is PostgreSQL-only. It loads the DSN and operator Bearer only through systemd `LoadCredential` from root-owned `0600` files under `/etc/porter` (systemd 247+), verifies schema through migration 0012 at startup, and fails closed if credentials, PostgreSQL, or migrations are missing. It does not open SQLite or fall back to legacy handlers. All legacy `/v1` routes return explicit `410 Gone`. Migrations remain explicit and are never applied at server startup. The panel root returns `503` until its embedded Vue bundle is verified PG-only and mock-free. This does not claim completed KVM acceptance or broader runtime changes.

The migrations are embedded at [`porter/internal/postgres/migrations`](../porter/internal/postgres/migrations). The package provides an explicit `Apply` entry point, and the separate `cmd/porter-migrate` utility is the only migration invocation path. Build it from the repository root with `make porter-migrate`, then run the `porter-migrate.service` unit before starting the server. Both units read the same systemd-provided DSN credential; never pass the DSN in an environment variable or argument:

```sh
make porter-migrate
sudo systemctl start porter-migrate.service
sudo systemctl status porter-migrate.service
```

This applies the numbered migrations through version 12 (`0012_release_builder_receipts.sql`), which the mounted panel/enrollment API requires. Server startup validates the schema but never applies migrations.

## Version 1 tables

All tables live under the new `porter_control_plane` schema so they do not collide with tables in the separate legacy PostgreSQL backend. IDs are `TEXT`, not native UUIDs: this keeps identifiers opaque and permits stable ID preservation in a later, separately approved import.

| Table | Purpose | Important rules |
| --- | --- | --- |
| `projects` | Project identity and lifecycle | Non-empty name; `active` or `deleting` state; stable creation-order index |
| `services` | Deployable service under a Project | Unique name per project; resource bounds; `runtime = 'firecracker'` only |
| `releases` | Immutable source/build intent and artifact lifecycle | `source_ref` is only a requested branch/tag selector; `resolved_commit` pins the 40- or 64-character Git object ID; ready/deploying states require guest-boot evidence for the exact SHA-256 artifact; activation also requires the matching deployment health gate |
| `fleet_nodes` | Minimal placement target with nullable `last_seen_at` and an explicit enrollment target | `ready`, `cordoned`, `draining`, or `offline`; PostgreSQL v2 enrollment and heartbeat are mounted in the PG-only server |
| `instances` | One Firecracker guest for a release and ordinal | Composite FK keeps its release and service consistent; ordinal is unique per release; optional fleet-node FK; Firecracker runtime only |

Foreign keys use `ON DELETE RESTRICT` for the Project → Service → Release → Instance history. A Fleet node reference can be cleared if a node row is removed; the Instance history remains. The partial unique index on active releases prevents two Releases from serving as active for one Service. JSON build specs must be objects with `kind = dockerfile` or `kind = compose`; the database checks the required path or Compose primary-service fields. Application validation still must enforce the Compose allowlist and all builder security policy.

A database check prevents `ready`, `deploying`, `active`, or `superseded` Releases without a valid `resolved_commit`. A database trigger prevents changes to a Release's ID, Service, sequence, requested source selector, or build spec after insertion. `resolved_commit` may be pinned during `building`, then cannot be changed or cleared; `artifact_digest` may be set as the build completes, then also cannot be changed or cleared. Migration 0009 adds immutable guest-ready evidence for the exact artifact digest and a single-use health-gate result; the database rejects ready/deploying without the guest receipt and active without a matching passed health gate.

Every version-1 resource table has UTC-capable `TIMESTAMPTZ` creation/update fields, and the schema includes indexes for project/service/release pagination, service lifecycle reads, and node placement lookups. SQL defaults use the database clock. The schema is intentionally small; it does not try to model every current SQLite table.

## Version 2 resource events

Migration `0002_resource_events.sql` adds `resource_events`, an append-only event stream with stable event IDs, UTC timestamps, optional Project/Service/Release/Instance/Fleet-node references, and structured JSON data. Events have no resource foreign keys so retained history does not cascade away during later resource cleanup. The database rejects `UPDATE` and `DELETE` on event rows.

The PostgreSQL store writes each event in the same transaction as its resource mutation, including creates and state changes, Release commit/artifact pinning and promotion, Instance placement, Fleet-node metadata, domain proof, and route-intent changes. Events can reference domain claims and gateway routes without foreign keys. Payloads omit source URLs, failure text, and other secret-bearing details. Event reads use descending `(occurred_at, id)` keyset pagination.

## Version 3 domain claims and route intent

Migration `0003_domains_routes.sql` adds `domain_claims` and `gateway_routes` and extends the append-only event table with optional domain/route references. Domain claims require an active Project and Service, store a normalized hostname plus a random manual TXT challenge, and bind to the exact Project/Service pair with composite foreign keys. An ownership proof is persisted only after the API's DNS resolver observes the exact TXT value; the observed value and timestamp are required by database constraints and cannot be rewritten after verification.

| Table | Purpose | Important rules |
| --- | --- | --- |
| `domain_claims` | Project/Service-scoped hostname ownership claim | Unique hostname; immutable TXT challenge; verified state requires the matching observed TXT value and timestamp |
| `gateway_routes` | Saved path-prefix route intent for a domain claim | Disabled by default; composite FK preserves Project/Service scope; enabling requires a verified claim; no applied/live state is stored |

Gateway routes begin with `desired_enabled = false`. A database trigger rejects attempts to enable route intent before the associated claim has persisted TXT proof. The API and Vue view describe this as saved preview configuration only: `gateway_status` is `unavailable`, and there is no provider DNS write, ACME/TLS automation, gateway reconciler, or live traffic change. Migration 0003 is required before the root server mounts the adapter.

## Version 4 Storage and Backups metadata

Migration `0004_storage_backup_metadata.sql` adds metadata tables only. A `volume_metadata` row is a Project/Service-scoped `single_writer` declaration, with optional Fleet-node affinity and integrity columns; the database rejects shared-writable mode and keeps the current integrity state at `not_available`. It does not provision, mount, attach, resize, clone, or delete a disk.

Under migration 0004 alone, `snapshot_metadata` records only an unavailable crash-consistent intent; no guest data is captured. Migration 0008 adds a durable lifecycle: `queued`/`capturing`/`captured`/`failed`, a fenced capture-operation link, an agent-local VM identity, and verified aggregate integrity metadata. `backup_metadata` still records metadata only and `off_host_status = unavailable`; there is no off-host copy. PostgreSQL composite foreign keys reject cross-scope references.

The authenticated API retains metadata-only records and adds capture/restore operations under migration 0008. Volume metadata remains single-writer and does not provision, attach, resize, or delete disks. Backups remain local snapshots only: scheduling, off-host copies, and other recovery paths are unavailable.

## Version 5 operations and typed command leases

Migration `0005_operations_command_leases.sql` adds Project/Service/Instance-scoped `operations`, versioned `agent_commands`, and append-only `agent_command_results`, and adds optional `operation_id` and `command_id` references to resource events. A project-scoped idempotency key is bound to a SHA-256 request fingerprint; one active operation per Instance is enforced. Commands use a closed kind allowlist and kind-specific Go payload types (`vm.apply`, `vm.delete`, `vm.start`, `vm.stop`, `vm.restart`, `snapshot.create`, `snapshot.restore`, and `volume.resize`). Unknown kinds and arbitrary shell command strings are rejected.

Leases are short-lived and attempt-fenced. Each claim increments the attempt once and returns a fresh random token; PostgreSQL stores only its SHA-256 hash. Expiry requeues the same command without incrementing the attempt, and the next successful claim increments it. Results must repeat the matching node, operation, attempt, and command kind and present the unexpired lease token; the result state must also be valid for that typed command. An exact duplicate result is accepted without another receipt or event; conflicting duplicates and stale attempts/tokens fail. The result receipt, terminal operation/command state, applicable Instance state change, and event append commit in one transaction. Result receipts and resource events are append-only.

The `/api/v1` operator API exposes operation reads and authenticated snapshot capture/restore actions. Snapshot actions return a durable operation pointer; payloads and lease tokens are not exposed. Retry, cancel, generic dispatch, and arbitrary command enqueue remain unavailable. The mounted internal agent v2 transport enforces lease-token claim/result fencing by node, operation, attempt, command kind, token, and expiry. The PostgreSQL-only mux never forwards legacy `/v1` routes to SQLite.

## Version 6 node credentials and authenticated agent transport

Migration `0006_agent_node_credentials.sql` stores per-node random bearer-token SHA-256 hashes, generation, issuance, bounded previous-token grace, and revocation metadata; plaintext node tokens are never persisted. The mounted internal HTTPS v2 handler provides heartbeat, bounded typed-command polling, and fenced result submission after explicit migrations are applied. V2 leases only allowlisted VM lifecycle and snapshot commands; no arbitrary shell command is accepted. Tests use a fake runtime only; no real Firecracker/KVM or production dispatch is claimed.

## Version 7 one-time agent enrollment

Migration `0007_agent_enrollment.sql` adds short-lived, single-use enrollment records with token hashes only and records the operator-selected `same_host` or `remote_host` topology on Fleet nodes. `POST /api/v1/fleet/join-tokens` issues a ten-minute token for an HTTPS control-plane origin; `POST /api/v1/fleet/enroll` atomically consumes it, creates/binds a node identity, issues its hash-only v2 credential, and appends durable events. Expired, malformed, or replayed tokens fail closed with a generic `401`. Secret-bearing responses are `no-store`; token values are not written to events or server logs.

The public credential-rotation route is `POST /api/v1/fleet/nodes/{node_id}/credentials`; it returns a new credential once, increments the generation, and keeps the previous token valid for five minutes by default (maximum thirty). After a lost enrollment `201`, do not retry; identify exactly one offline Fleet row by hostname/time, rotate that node ID, then verify its authenticated heartbeat. The server does not install, start, or remotely execute agents. UI, installer, and agent-client work belongs to separate tracks; this API lane does not claim those clients are complete. Migration 0007 is required before the Fleet API can read `enrollment_target` and before enrollment can be mounted.

## Version 8 crash-consistent snapshot/restore operations

Migration `0008_snapshot_restore_lifecycle.sql` adds an agent-local `runtime_vm_id` to Instances and operation links/statuses to snapshot metadata. Capture is allowed only for a running Instance; restore is allowed only while stopped or failed, and only when the verified snapshot belongs to the same Instance, assigned Fleet node, and runtime VM identity. Shared-writable volumes remain rejected by the existing runtime checks. Capture/restore operations, command records, snapshot metadata transitions, and resource events are transactionally linked.

The operator routes are `POST /api/v1/instances/{id}/snapshots/capture` with `{"consistency":"crash_consistent","idempotency_key":"…"}` and `POST /api/v1/instances/{id}/snapshots/{snapshot_id}/restore` with an idempotency key. Both require the existing operator bearer token. V2 agent results must include a lease-fenced, path-free receipt containing only VM/snapshot IDs, crash-consistent mode, aggregate SHA-256, and size. Local manifests resolve artifacts on the assigned agent host; filesystem paths and the Firecracker Unix socket are never sent to or stored by the control plane. Restore verifies the expected digest and size before invoking the existing rollback-journal runtime path.

These are crash-consistent snapshots only. Local artifact durability does not provide off-host backup, and loss of the agent host can still lose the artifact. Volume provisioning/attachment, backup scheduling, off-host copy, application-consistent snapshots, and real KVM acceptance remain unavailable or unverified. The mounted agent API and fake-runtime tests do not establish production or KVM readiness.

## Version 9 isolated builder boundary and release readiness

Migration `0009_release_readiness_health_gate.sql` persists the digest that was guest-boot verified, its timestamp, and the health-gate state for that same digest. Release state changes to `ready` or `deploying` require guest-ready proof. A transition to `active` requires a matching `passed` health-gate receipt. Completed guest-ready and health-gate evidence cannot be rewritten, including through direct SQL; the store repeats these checks and returns a stable failed-precondition error.

`porter/internal/postgres/buildservice` defines explicit typed interfaces for an isolated builder runtime, opaque agent-local VM identity, pinned commit, content-addressed artifact receipt, guest-boot receipt, and cleanup. It has no host process runner. It validates public HTTPS Git repository URLs, immutable commit IDs, relative Dockerfile/Compose paths, and the supported single-primary-service Compose subset. The required runtime implementation must resolve the source ref and perform Git/build steps only inside a disposable Firecracker builder guest, verify the bootable artifact through the agent's local Firecracker socket, clean the guest and temporary artifacts on every exit, and return only path-free digests and bounded metadata.

No KVM-capable builder agent or accepted/versioned builder guest image is available in this environment. The normal PostgreSQL API therefore installs `DisabledBuilder`: `POST /services/{id}/releases` validates the request and returns `412 failed_precondition` without persisting a release or queueing an operation. The typed service is tested with a fake runtime, but it is not wired to real agents, and no Git checkout, Docker, BuildKit, shell, or untrusted source runs on the control-plane or production app host. Legacy SQLite image-build routes, GitHub webhook builds, MCP build/detect tools, and `PORTER_ENABLE_UNISOLATED_BUILDS` all fail closed; there is no opt-in host fallback.

The Vue Releases page reads the PostgreSQL release records and shows build state, requested ref versus resolved commit, artifact digest, guest readiness, health-gate status, and safe failure-stage text. It exposes no activation, rollback, or build-log controls: deployment/health-check workers and an activation endpoint are not implemented. Activation remains unavailable, not simulated. Progress is limited to persisted lifecycle state and bounded failure stage; there is no live build stream.

## Separate persistence package

`porter/internal/postgres/store` persists the v1 Project, Service, Release, Instance, Fleet-node, domain-claim, gateway-route, volume, snapshot-metadata, backup-metadata, operation, typed command-lease, result-receipt, node-credential, and enrollment records. It supports scoped create/get/list operations, soft-state and desired-capacity updates, Release build/deploy transitions, commit and artifact pinning, optional Instance-to-Fleet-node placement, TXT proof persistence, preview route intent, single-writer volume declarations, crash-consistent metadata records, operation idempotency, lease expiry/retry, result fencing, one-time enrollment, and token rotation. Durable events are written in the same transactions. Release numbers are serialized per Service; promoting a Release and superseding the prior active Release share one transaction. The database retains final authority for Firecracker-only runtime, immutable source/build identity, persisted-proof route gating, storage scope/mode/consistency, commit/digest immutability, foreign keys, and one-active-Release constraints.

The package accepts an injected `*sql.DB`; its constructor neither opens a connection pool nor applies migrations. The server mounts the PostgreSQL resource adapter under `/api/v1` and the HTTPS-only v2 agent API over one PostgreSQL store. Public Fleet join/enroll/credential aliases adapt to the existing agent handlers; enrollment is never routed through legacy SQLite handlers. This server does not implement remote installation or guest lifecycle verification. Instance methods persist records and state only; they never launch or modify Firecracker guests.

## Phased `/api/v1` route slice

`cmd/porter` reads the PostgreSQL DSN and operator Bearer from `$CREDENTIALS_DIRECTORY/porter-postgres-dsn` and `$CREDENTIALS_DIRECTORY/porter-admin-token`; it ignores process-environment/argv secrets and requires systemd 247+ `LoadCredential`. It verifies required tables and migrations through version 12, and fails if they are missing. It never applies migrations or silently changes databases. `/api/v1` and agent-only `/internal/` routes are mounted; all legacy `/v1` routes return explicit `410 Gone` and never reach SQLite. Remote listeners require API TLS; the agent v2 handler always rejects plaintext HTTP.

The server uses the systemd-provided operator Bearer as a strict admin credential. The Vue UI must keep the browser token only in memory, use a masked prompt, and verify `GET /api/v1/fleet/capabilities` before enabling token controls. It must clear the token on auth/probe failures and never use local/session storage or token URLs. The current embedded UI is withheld with `503` until these conditions and the no-mock PG API contract are verified. The Overview, Projects, Services, read-only Fleet, Domains, Storage, Backups, Operations/Events, and Releases views use PostgreSQL-backed reads. Project/service/domain-claim/route-intent, volume-declaration, snapshot/backup metadata, operation, typed command-lease, and command-result records are persisted; the Overview and Operations activity views read the durable event stream. Domain TXT verification performs DNS reads only; provider DNS writes, ACME/TLS, and live gateway routing remain unavailable. Storage writes create metadata only: no disk is provisioned or attached. Snapshot capture and restore queue typed, fenced operations against the assigned agent and report crash-consistent, path-free integrity metadata; artifacts remain local to that agent, and backup scheduling/off-host copy remain unavailable. Creating a Service persists configuration only; it does not schedule an Instance. Release and Instance reads are available, but `POST /services/{id}/releases` returns `412 failed_precondition` with the default disabled builder; no release operation is queued. Release activation, deployment health-check execution, rollback, and build logs are not available. Fleet, event, and operation reads are authenticated; generic retry/cancel and direct product-API dispatch return explicit unavailable errors.

Fleet provisioning uses `POST /api/v1/fleet/join-tokens`, `POST /api/v1/fleet/enroll`, `GET /api/v1/fleet/nodes`, `POST /api/v1/fleet/nodes/{node_id}/credentials`, and the admin-authenticated `GET /api/v1/fleet/capabilities` probe. Heartbeat and command/result traffic remain internal agent transport. See [`AGENT_ENROLLMENT_API.md`](AGENT_ENROLLMENT_API.md) and [`INSTALLATION_CONTRACT.md`](INSTALLATION_CONTRACT.md) for DTOs, TLS, credentials, recovery, and install order. The server does not install or start agents remotely. The panel root remains 503 until the Vue bundle is safe. Any KVM acceptance evidence remains separate.

For an install, place the DSN and admin Bearer in the two root-owned credential source files, install the systemd units, apply migrations with `porter-migrate.service`, then start `porter.service`. Do not set secrets in the environment or argv. For local plaintext development, explicitly set `PORTER_API_MODE=development` and bind to loopback; all Fleet token routes still require HTTPS. The Go server embeds only `porter/web/index.html`; it returns 503 until that file is a verified PostgreSQL-only, no-mock UI.

```sh
# Build the separate migration executable from repository root
make porter-migrate

# Apply using the systemd credential file and dedicated unit
sudo systemctl start porter-migrate.service

# Start only after migration succeeds
sudo systemctl start porter.service
```

## Remaining runtime and product acceptance gates

1. Continue the remaining control-plane stores, scheduler/reconciliation jobs, and full fleet lifecycle/audit work. Enrollment and authenticated HTTPS poll/result are mounted in the PG-only server, but real KVM/Firecracker acceptance, revocation UI, capability/liveness reconciliation, production monitoring, and remote-agent operational validation remain. Storage still needs a Firecracker-agent-backed disk provision/attach/detach path and off-host backup. Snapshot capture/restore is wired to the existing agent runtime and fake-runtime tests, but real KVM acceptance and production durability/recovery validation remain. The domain slice still needs provider DNS integration, gateway reconciliation, ACME/TLS, and real traffic tests. These gates block wider beta/1.0 acceptance; they do not enable SQLite fallback or dual writes.
2. Complete the disposable Firecracker builder, source pinning/build queue, rollout and rollback workers, placement/scheduling, and actual guest lifecycle operations. Do not treat a persisted Service or desired-instance count as a running workload.
3. Keep the embedded panel root unavailable until Vue provides a PostgreSQL-only, no-mock bundle with the protected capability probe and public Fleet routes. Complete route, authorization, and browser end-to-end tests before enabling its controls.
4. Build a deterministic SQLite export/import tool with dry-run, stable-ID mapping, table counts, checksums, and referential-integrity checks. Rehearse on a copy, take and verify a backup, pause writes for cutover, reconcile PostgreSQL, and rehearse rollback. Do not delete existing SQLite data before this gate passes.
5. Run the release acceptance suite from the migration plan, including scaling, rollback, domains/DNS, load balancing, crash-consistent backup/restore, fleet operations, and real logs/metrics/traces. PostgreSQL remains the sole runtime control-plane truth in this beta; keep SQLite limited to agent-local host identity, paths, and recovery state.

## Migration rules

1. Migration files are numbered, contiguous, forward-only SQL bodies. Do not put `BEGIN` or `COMMIT` statements in them; the runner owns transaction boundaries.
2. The runner takes a PostgreSQL session advisory lock, then applies each SQL body and inserts its version, filename, SHA-256 checksum, and apply time into `public.porter_control_plane_schema_migrations` in the same transaction.
3. Re-running the command is idempotent when recorded checksums match. If a previously applied file's name or checksum changes, or the database contains an unknown/gapped history, the runner fails closed instead of silently skipping or rewriting it.
4. A failed SQL body or ledger insert rolls back that migration transaction, leaving its version unapplied and safe to retry after fixing the cause. If the connection drops around commit, rerun the command: it reconciles against the committed ledger before applying anything again.
5. Apply only through the explicit `porter-migrate.service` unit, which reads `porter-postgres-dsn` via systemd `LoadCredential`. Never invoke migrations from active server startup. Do not point it at production or the legacy backend's database as a shortcut.
6. Opt-in PostgreSQL tests use dedicated disposable database-name prefixes and drop only the control-plane schema/ledger: migration runner (`porter_test_*`), resource store/credentials (`porter_test_store_*`), HTTP adapter (`porter_test_httpapi_*`), v2 agent API (`porter_test_agentapi_*`), and the mounted recovery flow (`PORTER_STORE_TEST_POSTGRES_DSN`, database prefix `porter_test_store_*`). They cover migration apply/retry, hash-only credentials and rotation grace, enrollment issue/expiry/replay and node binding, authenticated HTTPS heartbeat/poll/result flows, snapshot lifecycle receipt convergence and rollback-safe operation transactions, event/result transaction atomicity, append-only history, Fleet-node/event/operation keyset pagination, claim scope and persisted TXT proof, pre-proof route rejection, single-writer volume and snapshot/backup metadata constraints, operation idempotency, lease expiry/retry, stale result fencing, duplicate receipts, HTTP errors/auth, no-fallback routing, and explicit schema readiness.
7. Do not enable dual writes. A later cutover needs an explicit stable-ID import, backup, reconciliation, and rollback plan. Extend with new numbered migrations; do not rewrite an applied migration or copy legacy migrations wholesale.

## Deliberate omissions

The existing legacy migrations were inspected for shape and migration conventions, not reused as a schema bundle. Version 1 excludes legacy user/org/tenant/RBAC and RLS models, billing/usage/quotas, marketplace/templates, AI, workflows, and feature-specific tables. The small resource-event stream does not replace a full audit system; operations/commands, logs/metrics/traces, and retention controls need their own scoped design before use. Storage metadata does not provision or attach a disk or copy a snapshot off-host. The agent-backed snapshot path is crash-consistent-only, verifies checksums, and preserves its existing restore rollback journal; it rejects shared-writable volumes. Real KVM/production acceptance remains unavailable here.

The pgx dependency supports the explicit migration utility, root server API connection, and opt-in PostgreSQL tests. PostgreSQL stores only contract state; source build execution remains unavailable until a disposable Firecracker builder MicroVM has passed the migration plan's isolation, cleanup, cancellation, artifact-integrity, and fail-closed acceptance gate. Production instances remain Firecracker/KVM-only; there is no Docker/containerd or Kubernetes application runtime.
