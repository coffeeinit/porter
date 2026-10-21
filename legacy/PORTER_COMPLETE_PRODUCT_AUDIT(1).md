# Porter Complete Product Audit and Implementation Roadmap

**Unified report:** SRS coverage, platform completeness, API/manual audit, and detailed API product-workflow gap matrix.

This master file consolidates the project audit documents into one delivery artifact. The source sections are preserved below so the report can be reviewed or shared as a single file.

---

## 1. SRS Coverage Report

# Porter Backend — SRS Verification and Coverage Report

**Verification date:** 2026-10-03  
**Source inputs:** `SRS.md`, `MISSING.md`, uploaded Porter repository  
**Status convention:** `Implemented` means present in source and covered by tests or a compile-verified path; `Partial` means a working slice exists but an SRS acceptance path is incomplete; `Planned` means the audit found no complete implementation.

## Verification performed

- Installed Go 1.25.0, compatible with the module’s declared Go version.
- Built the frontend with `npm ci && npm run build`, producing `web/dist` for Go embedding.
- Ran `gofmt` on changed files.
- Ran `go test ./...`: **all 64 packages passed**.
- Built the production binary with `go build -trimpath -ldflags '-s -w' -o porter ./cmd/porter`.
- Executed `porter --version`: **v1.0.0-beta**.
- Counted **179 Go test files** in the packaged source tree.
- Verified migrations through **0038**, including the `pg_trgm` log-search index.

## Implemented backend areas

### Runtime and infrastructure

- Firecracker-oriented runtime abstraction and VM lifecycle operations: boot, stop, exec, console, snapshots, restore/verify, retention, migrate planning, ephemeral runs, and health rollout.
- Node registration/agent connectivity, heartbeat monitoring, capability discovery, scheduling, placements, failover intents, and resource/capacity checks.
- KVM/Firecracker/BuildKit preflight checks are present; a physical KVM-metal end-to-end run still requires a suitable host.
- Cgroup sampling, VM disk gauges, startup metrics, tracing, and telemetry paths exist.

### Control plane and API

- Go HTTP control plane with embedded dashboard assets.
- Authentication/session/token paths, API keys, LDAP live bind/JIT login, RBAC, scoped capabilities, CSRF, request IDs, ETags, pagination, idempotency, and audit middleware.
- Project, deployment, replica, node, server, network, DNS/domain, TLS, storage, backup, logs, metrics, traces, events, alerts, incidents, operations, cron, environment, secret, and service routes are present.
- Route permission coverage tests protect the registered API surface.

### Build, deployment, and networking

- Git webhook → build intent, Dockerfile/OCI/prebuilt artifact flows, BuildKit integration, deployment records/logs, rollout/rollback strategies, health checks, preview URLs, branch preview routing, gateway routing, domain verification, Cloudflare integration, and ACME HTTP-01 wiring.
- Compose stack fan-out, marketplace database templates, engine defaults in migration 0035, and persistent service configuration are present.

### Data and commercial foundations

- PostgreSQL migrations 0001–0038, RLS/tenant tables, project settings, operation/task ledger, deployments, replicas, logs, usage events, plans/prices/subscriptions, quotas, credits/invoice preview, incidents, auxiliary VMs, and audit records.
- Billing preview and usage metering exist. Actual payment/money movement is intentionally not implemented.

### AI, safety, and operations

- MCP HTTP facade with search/describe/call, approvals, scoped identity, audit, execution denylist/judgment gate, secret isolation, ptyd sessions, agent mode, and durable operation states.
- Shutdown has bounded control-server/gateway shutdown; some watcher goroutines still rely on context cancellation rather than explicit joins.

## Disruption budgets — completed in this pass

- Existing pure policy type supports `min_available` and `max_unavailable`, validation, and `CanEvict` decisions.
- Existing autoscale policy helper calculates remaining allowance from desired, healthy, and in-flight replica state.
- Existing host failover path gates migration operations per project/service workload and retries skipped replicas on later reconciliation.
- Added authenticated API endpoints:
  - `GET /projects/{projectId}/settings/disruption-budget`
  - `PUT /projects/{projectId}/settings/disruption-budget`
- Added strict validation: unknown fields, negative values, fractional values, and setting both bounds are rejected.
- Added canonical integer persistence through the existing durable `project_settings` table.
- Added API canonicalization tests; existing resource, autoscale, and controller tests cover policy and failover behavior.

## Partial or remaining SRS gaps

The repository is **not yet a complete implementation of every SRS sentence**. The following items remain and should not be represented as complete:

1. **KVM-metal proof:** requires execution on a real supported Linux/KVM host.
2. **Guest image baking:** guestprep installs distro packages but does not yet bake Porter agent/ptyd binaries into every guest image.
3. **MCP provider executors:** the MCP facade/catalog exists; GitHub/Google/etc. provider adapters are not complete.
4. **Billing money movement:** plans, subscriptions, metering, quotas, and invoice preview exist; payment adapter, charge, refund, and dunning execution do not.
5. **Static-site build path:** the build matrix detects static projects, but the static engine still asks for a Dockerfile/prebuilt OCI artifact rather than producing a native static artifact.
6. **Preview TTL/teardown:** preview creation exists; automatic expiry/cleanup needs a complete controller path and lifecycle tests.
7. **PR lifecycle events/comments:** GitHub push handling exists; PR open/sync/reopen/comment workflows are not complete.
8. **Multi-region scheduling and richer affinity:** node region/zone fields and basic allowed-region filtering exist, but full affinity/anti-affinity/topology spreading is incomplete.
9. **Network policy semantics:** project firewall rules exist; Kubernetes-like selector/namespace policy semantics are not complete.
10. **Anycast and edge runtime:** not implemented; Cloudflare integration is DNS/tunnel-oriented.
11. **Storage pools, HA/fencing, and live migration:** snapshots/restore and stop-and-copy migration exist; these advanced Proxmox/Fly-style capabilities remain incomplete.
12. **Explicit shutdown joins:** several watcher paths still depend on cancellation without join handles.
13. **Resource/types duplication and runtime mode overlap:** the audit identifies architectural cleanup still possible, although the current packages compile and tests pass.
14. **Thin CLI and command split:** Porter currently uses one binary with `server`, `agent`, `ptyd`, `migrate`, and utility modes; separate thin CLI/daemon binaries are not present by design.
15. **Full production acceptance suite:** the repository has broad unit/contract tests, but the complete SRS critical E2E list requires live PostgreSQL, Firecracker, networking, DNS/TLS, and node-failure environments.

## Bottom line

The uploaded backend is a substantial, compiling Porter control-plane implementation with broad working slices and a passing automated test suite. It is **not honest to call every SRS capability complete** until the remaining items above—especially live KVM E2E, guest baking, provider executors, payment execution, and full production acceptance—are implemented and verified in their required environments.


---

## 2. Platform Completeness Audit


## Slice 3 completed: image catalog visibility and dedicated Images UI

The backend already exposed `GET /images`, but it only used a startup snapshot for filesystem manifests, did not consistently merge persisted golden images in search/detail reads, and the frontend had no dedicated Images route. `imagecatalog.Catalog` now reloads `data/images/*.json` on API reads; the image API merges and deduplicates on-disk manifests with persisted database images; search and detail include both sources and IDs. The UI now has `/images` in the sidebar with catalog search, status/type/resource metadata, refresh, and custom ZIP or OCI/Docker image upload support.

Expected existing storage layout remains compatible: `ImagesDir` points at `data/images`, `CustomImagesDir` at `data/custom`, `GuestBasesDir` at `data/bases`, and volumes at `data/volumes`. The backend still requires a valid rootfs plus Firecracker-compatible kernel before an uploaded image is registered as bootable.


---

## 3. API and Internal Feature Manual Audit

# Porter API and Internal Feature Manual Audit

**Review date:** 2026-10-03  
**Review style:** source-level/manual review requested by the user; no new test suite was used for this review.

## Overall result

The backend has a single API route table with **399 declared HTTP routes** across `api.go` and `feature_*.go`. Every declaration points to a concrete `*API` handler method. The route loop applies the authenticated wrapper to protected routes and the request-ID wrapper to intentionally public routes.

The project is substantially wired, but not every SRS feature is fully implemented. Several handlers explicitly report unsupported or planned behavior; those are documented below rather than being treated as complete.

## Manual request flow checked

1. `cmd/porter` loads configuration and starts `server.Run`.
2. `server.Run` performs startup prerequisite checks, creates the PostgreSQL store, applies migrations, seeds the admin, constructs the runtime engine, controllers, task runner, API, and listeners.
3. `API.Routes` registers the complete `apiRoutes` table with Go `ServeMux` method/path patterns.
4. Protected routes pass through:
   - request ID assignment;
   - bearer-token resolution in this order: JWT, DB session, persisted API key;
   - optional rate limiting;
   - CSRF validation for state-changing requests;
   - route permission lookup and scoped RBAC evaluation;
   - idempotency replay for mutating requests;
   - the feature handler.
5. Public operational routes are limited to health/version/auth/bootstrap/webhook/heartbeat-style flows and are individually validated in their handlers.

## Feature groups manually confirmed as wired

- Authentication, sessions, API keys, JWT/JWKS, LDAP login, user/account, orgs, groups, RBAC roles and assignments.
- Projects, compose, environments, settings, secrets, domains/DNS, Git, hooks, cron, alerts, incidents, drains, redirects, analytics, observability, firewall, cache, volumes, exports/imports.
- Deployments, rollout/checks, rollback, replicas/VM compatibility endpoints, snapshots, restore, SSH metadata/certificates, console, exec, logs, metrics, traffic.
- Build and image/catalog endpoints, OCI upload, guest bases, kernel build, MCP facade, Cloudflare/tunnels, quick shares, email, services/templates, backups, capacity policies.
- Servers/nodes, enrollment tokens, token-authenticated heartbeats, placements, host prerequisites/runtime, operations, billing/usage, preview/auxiliary VM flows.
- Autoscaling, health checks, controller reconciliation, failover, and disruption-budget persistence/API.

## Corrections made during manual review

### 1. SSE `/events` was bypassing API authentication

Before correction, `Routes` registered `GET /events` directly with `a.hub`, while all project/org event endpoints were protected. This exposed the global event stream without authentication or `event.read` RBAC.

**Correction:** `/events` is now a route-table entry with `event.read`, `auth=true`, and a thin `handleEvents` method. It receives the same auth, CSRF/read handling, request ID, RBAC, and audit path as other internal endpoints.

### 2. Logout did not revoke the login token

Before correction, `/auth/logout` and `/logout` were public and only returned `{ "status": "logged out" }`; the opaque token issued by login remained valid.

**Correction:** both logout aliases now require authentication. Logout revokes an opaque API-key token by token hash and also attempts session revocation. JWTs are self-contained and remain client-invalidated/short-lived, as expected for JWT semantics.

### 3. Node heartbeat is public only at the HTTP wrapper, not in authorization effect

`POST /servers/{id}/heartbeat` is intentionally registered without user auth because nodes are not users. The handler requires the enrollment-issued `X-Porter-Node-Token` (or bearer form), validates it against the server ID, and rejects missing/invalid tokens with 401. This is correct for machine identity.

### 4. Webhook and enrollment routes are intentionally public

- `POST /hooks/github` is public because GitHub cannot use a Porter user session; the handler validates the configured webhook signature/shape.
- `POST /nodes/enroll` is public because the one-time enrollment token in the request body is the credential; the store consumes it atomically and returns a one-time node token.

## Explicitly incomplete handlers found by manual review

These are not missing route declarations; they are registered handlers whose current behavior intentionally reports a gap:

- `PATCH /users/me` returns HTTP 501: profile updates are not implemented in beta-dev.
- `POST /auth/password/forgot` and `POST /auth/password/reset` return an explicit unsupported response because no recovery provider/token store is configured.
- `POST /auth/signup` returns a single-tenant-admin note rather than creating a normal account; additional users are created through the protected user API.
- `GET /auth/jwks` returns 503 when JWT signing is not configured.
- Email sending returns an explicit configuration error when SMTP is not configured.
- VPS static-IP pinning reports an explicit planned/KVM-metal limitation when no allocation exists.
- Server detail responses explicitly label missing region/zone/host-metric fields as planned rather than fabricating values.
- Several runtime operations correctly return errors when Firecracker, guest agent, kernel/rootfs, or host prerequisites are absent.

## Manual verdict

**API wiring:** broadly complete and internally consistent.  
**Authentication/RBAC path:** complete for route-table endpoints after the SSE correction.  
**Logout behavior:** corrected to revoke opaque credentials.  
**Runtime feature completeness:** not complete for account recovery/profile mutation, payment execution, some provider integrations, static-site native builds, advanced multi-region scheduling, and live KVM/Firecracker operations.

The next implementation phase should prioritize the explicit unsupported handlers according to product priority, rather than adding duplicate route declarations.


---

## 4. Expanded Product Gap Matrix and API Workflow Catalog



## 8. Second-pass API and internal-feature inventory

A second pass compared the complete route table in `internal/api/api.go`, all `feature_*.go` modules, controller/server wiring, explicit unsupported responses, and frontend API consumers. This found additional gaps that were not sufficiently explicit in the first matrix.

### 8.1 Backend API surface exists but is not a complete product contract

Porter exposes a broad API surface—approximately 350 registered method/path entries—but many endpoints are isolated feature slices rather than complete user journeys. A registered route is not counted as implemented unless its persistence, runtime behavior, authorization boundary, and UI or documented integration are complete.

Additional API-level gaps:

- **Account/profile:** `PATCH /users/me` and `DELETE /users/me` currently return an explicit unsupported/501 response for profile updates/deletion. API-key management exists under `/users/me/api-keys`, but there is no UI or complete service-account/API-key lifecycle screen.
- **Authentication lifecycle:** signup, password reset, LDAP login, token minting, session inspection, and JWKS endpoints exist, but the shipped UI only implements username/password login. Email verification, password-reset completion, MFA/security settings, session/device management, and recovery UX are absent.
- **Organization administration:** organization create/current-org patch/delete/transfer/events routes exist, but the UI loads only `/orgs/default`; there is no organization switcher, create/transfer/delete workflow, ownership handoff UX, or tenant context visibly bound to every action.
- **Teams and roles:** groups, roles, permissions, project members, invites, and scoped assignments have API routes, but there is no complete team/group hierarchy UI, invite acceptance flow, tenant-owned role editor, service identity, or API-key administration. Existing Team UI is only a narrow org-member/RBAC-assignment view.
- **Compose/import:** `/projects/compose`, compose validation/preview, project export/import, Git import/branches, hooks, deployment protection, OIDC, functions, and build-machine settings are API-visible but have no complete corresponding browser flow.
- **Project settings:** general, build, checks, rollout, framework, Git, LFS, ignore-command, networking, advanced, passport, microfrontends, retention, and security APIs exist, but the product UI has no unified project settings workspace. This prevents users from configuring the behavior that the backend advertises.
- **Environment management:** create/patch/delete environments, branch, domain, and environment range endpoints exist, but the UI only expands/read-displays project data and does not manage environment lifecycle or preview promotion.
- **Deployment controls:** build creation, Git deployment, build logs/streams, deployment checks, rollout, promotion, rollback, source, and OG metadata routes exist, but no complete source-to-build-to-health-to-promotion UI is wired. The existing deployment list is primarily inspection and has the `__all__` project-id bug documented above.
- **Operations and observability:** drains, alerts, firewall rules/events/stats/whitelist, cache statistics/purge, traffic search/clear, analytics usage/timeseries/paths/status codes/bandwidth/requests/invocations, and Web Vitals endpoints exist but are absent from the navigation or only partially represented by read-only pages. There is no alert acknowledgment/incident workflow or operator-facing policy center.
- **Networking:** project network creation/listing exists, but there is no complete network lifecycle, subnet/IP allocation UI, firewall policy editor, port-forward/port-publish control, private-network attachment, or provider-neutral DNS record management screen.
- **Cron/jobs:** a project cron API and history/run actions exist, but the UI does not provide a complete project-scoped job lifecycle with execution logs, retry policy, concurrency policy, secret isolation, or job result history tied to operations.
- **Infrastructure administration:** host overview/ports/kernel/prerequisites/runtime, global VM controls, guest-base listing, server enrollment/heartbeat/SSH/removal, roles/permissions, and node operations exist as API fragments. There is no complete provider/region/zone/node-pool inventory, node cordon/drain/maintenance/replace flow, or customer-owned server onboarding wizard.
- **Persistent VM product mode:** `/vms` compatibility routes provide lifecycle/read/log/metrics/SSH/exec/console primitives, but there is no persistent VM creation API with an explicit mode, no VM workspace route in the UI, and no custom browser desktop/file-manager/session layer. The current VM API is primarily a compatibility view over application replicas.
- **Images and guest bases:** `/guest-bases`, `/images/base`, `/images/base/readiness`, `/images/ml`, image stats/prune, and custom image upload exist, but `/images/ml` is currently an alias of image search rather than an ML-image feature; there is no base-image lifecycle, compatibility diagnostics, registry credential management, or deploy-from-image action.
- **Billing and usage:** global and project usage/analytics endpoints exist, but they do not form a complete customer/org/subscription/payment/invoice graph. There is no seller/reseller workspace, payment method flow, invoice finalization, refunds/credits, quota/overage policy UI, or usage export/audit flow.

### 8.2 Explicit unsupported or misleading success responses

The second pass found several API responses that can look successful while the requested feature is not actually available:

- VM SSH endpoints return HTTP 200 metadata such as `ssh unsupported until the guest-vsock agent is enabled` rather than providing a working SSH certificate/session flow.
- VM exec endpoints return HTTP 200 `exec unsupported` responses instead of executing through the guest agent.
- Profile mutation returns HTTP 501 with an explicit beta-dev message.
- Event streaming returns HTTP 503 when the hub is not wired; there is no UI connection/retry surface.
- Some runtime/build/feature-unavailable paths return an accepted/deferred or status object without a durable operation record and without a controller that will complete it.
- Cloudflare quick-share support is an optional gateway primitive, not a complete domain/provider integration. There is no UI/provider lifecycle for enabling, listing, revoking, or billing Cloudflare-backed routes.
- `internal/store/vps.go` and related VPS code explicitly describe real-host operations as planned/deferred. Therefore the current VPS layer should not be treated as the persistent user-managed VM product requested by the target design.

### 8.3 API-to-UI coverage verdict after the second pass

The API is broad, but the frontend is currently a dashboard over selected read paths rather than a complete control plane. The most important additional UI gaps are:

1. No persistent VM workspace or custom desktop UI.
2. No source deployment wizard.
3. No application lifecycle/scale/autoscale controls from AppDetail.
4. No project settings workspace despite dozens of settings endpoints.
5. No compose/environment/hooks/protection/OIDC/functions UI.
6. No certificate/TLS management UI.
7. No firewall/cache/alerts/drains/analytics/Web Vitals operator UI.
8. No customer/seller/payment/usage administration UI.
9. No node pool/provider/region/maintenance/drain UI.
10. No frontend contract, type-check, or browser E2E test suite.

### 8.4 Second-pass priority additions

Add these to the implementation backlog in dependency order:

- **P0:** Make unsupported operations return explicit non-success semantics consistently; introduce an operation resource and link every long-running API mutation to it.
- **P0:** Add canonical tenant ownership checks to every project/group/volume/VM/environment/member route, independent of the caller-supplied org header/body.
- **P0:** Add an explicit persistent-VM resource/mode and a dedicated VM workspace API contract; do not present application-replica compatibility routes as a complete VPS feature.
- **P1:** Implement a unified Project Settings, Source Deployments, Environments, and Operations UI against the existing API surface.
- **P1:** Implement guest-agent-backed terminal/SSH/exec/file-manager operations with real session authorization and audit records.
- **P1:** Implement provider/node administration, domain/TLS, firewall, alerts, cache, analytics, and network lifecycle screens.
- **P1:** Implement organization switching, teams/invitations, service accounts/API keys, billing accounts, usage dimensions, invoices, payments, and seller/customer administration.
- **P2:** Add OpenAPI/generated client contracts, frontend type checking, browser E2E tests, and route-to-UI coverage checks so new backend APIs cannot silently remain unreachable.

### 8.5 Audit evidence and scope

This section was produced by re-inventorying the route table and comparing it with `feature_*.go`, controller/server registration in `internal/server/server.go`, explicit unsupported/deferred markers, and all `web/src/views` API calls. It supplements—not replaces—the detailed area findings above. No source code was changed during this second-pass documentation audit.


## 9. Detailed API product-workflow coverage and creation map

This section maps the registered API to the product workflows Porter is expected to provide. It deliberately separates **route creation** from **working product creation**. A route is only considered complete when it has: (1) authenticated and tenant-safe input, (2) durable persistence, (3) controller/runtime execution where needed, (4) observable operation status, (5) failure/retry behavior, and (6) a usable UI or documented automation path.

### 9.1 Product creation workflow matrix

| Product workflow | API entry points | What the current API creates | Missing connection before it is a working product flow | Status |
|---|---|---|---|---|
| **User/account creation** | `POST /auth/signup`, `POST /users`, `PATCH /users/me` | User/session records and login credentials | Email verification, profile lifecycle, MFA/security settings, API-key/service-account UX, and consistent account recovery | Partial |
| **Organization creation** | `POST /orgs`, `PATCH /orgs/current`, `POST /orgs/transfer` | Organization and membership records | Canonical target-org authorization, organization switcher, ownership transfer UX, tenant-bound token context | Partial / high risk |
| **Team/group creation** | `POST /groups`, group-project association routes, org member routes | Groups, memberships, scoped assignments | Real teams/team_members lifecycle, invitation acceptance, ownership checks on every group/project ID, tenant-owned roles | Partial / high risk |
| **Project/application creation** | `POST /projects`, `POST /projects/compose` | Project record, image/resource settings, sometimes initial replicas | Persistent/ephemeral mode, source/provider configuration, durable operation, scheduler reservation, health-gated readiness, project settings UI | Partial |
| **Environment creation** | `POST /projects/{projectId}/environments` | Environment record | Branch/deployment association, preview lifecycle, environment-specific domains/secrets, UI controls, ownership verification | Partial |
| **Deployment creation** | `POST /projects/{projectId}/deployments`, `POST /projects/{projectId}/deployments/git` | Deployment/build records and source metadata | Build completion must create deployment replicas, use `BuildID` correctly, wait for healthy, then promote traffic | Partial / high risk |
| **Build creation** | `POST /projects/{projectId}/builds`, `/builds/run` | Build row and build log activity | Durable worker operation, cancellation/retry, isolated BuildKit execution result, artifact-to-replica handoff | Partial |
| **Image creation** | `POST /images/custom`, `POST /images/custom/oci` | Rootfs/kernel artifacts and `GoldenImage` row | Registry pull/auth, digest/platform model, hardened conversion, real kernel/Firecracker readiness, deploy-from-image action | Partial |
| **Persistent VM creation** | Current `/vms` compatibility routes and project/replica creation | Application-replica-like VM records | Explicit VM resource and `persistent` mode, root disk/volume lifecycle, user VM workspace, guest agent, quotas and billing | Missing as product workflow |
| **Replica creation** | Project create, scale, replica start/batch start, service scale | VM/process attempts and project VM IDs | Durable cardinality reconciler, placement reservation, operation ID, failure cleanup, observed health | Partial / high risk |
| **Node/server creation** | `POST /servers`, enrollment-token flows, heartbeat | Server/node records and reports | Provider/region/node-pool model, real capability admission, KVM/Firecracker validation, cordon/drain/replace, placement integration | Partial |
| **Volume creation** | `POST /volumes`, project volume route | Host volume directory and metadata | Attach/detach, correct `data.img` Firecracker path, capacity/quota admission, resize semantics, delete protection | Partial / high risk |
| **Backup creation** | Backup schedule creation and task scan; snapshot routes | Schedule/snapshot descriptor activity | Immutable backup artifact, ObjectStore, checksum/encryption, backup row, restore verification, cross-node destination | Missing |
| **Domain creation** | `POST /projects/{projectId}/domains`, verification routes | Domain row and DNS probe result | Durable verification state, certificate issuance/binding, gateway activation, DNS record management, renewal | Partial / high risk |
| **Network/firewall creation** | Network and firewall rule POST routes | Network/rule records and some gateway state | Full attachment/routing lifecycle, private networking, port-forwarding, enforcement, UI and node integration | Partial |
| **Alert/drain creation** | Alert/drain routes | Alert/drain records | Evaluator/notification delivery, actionable incident lifecycle, task integration, UI acknowledgement and testing | Partial |
| **Subscription/billing creation** | Plan/subscription/invoice preview routes | Plan and preview subscription/invoice data | Customer account, payment method, invoice finalization, usage dimensions, payments, refunds, credits, dunning, seller administration | Partial / preview only |

### 9.2 API connection requirements by workflow stage

Every creation endpoint should follow this state sequence rather than performing direct runtime mutation inside the HTTP request:

```text
request validation
  → canonical tenant/resource ownership check
  → idempotency key lookup
  → durable desired resource
  → durable operation record
  → quota/entitlement check
  → scheduler placement reservation
  → controller/runtime execution
  → observed state and conditions
  → health/readiness gate
  → gateway/DNS exposure
  → usage/billing events
  → UI event/task progress
```

The current code generally implements only a subset of this sequence. In particular, project creation and scale paths frequently jump from request validation directly to `Boot`/`Stop`; build endpoints can finish at an image row without creating healthy deployment replicas; snapshot schedules can finish at local descriptors without immutable backup objects; and billing endpoints can finish at a preview without a commercial transaction.

### 9.3 API family status table

| API family | Route families present | Durable data present | Runtime/controller present | UI workflow | Main product gap |
|---|---|---|---|---|---|
| Auth/account | `/auth/*`, `/users*` | Yes, partial | Session/auth middleware | Login only | Profile, recovery, MFA, API keys, account security |
| Organizations/teams | `/orgs*`, `/groups*`, `/roles*`, `/permissions` | Yes | RBAC evaluator | Narrow Team page | Cross-tenant ownership, teams/invites, org switcher |
| Projects/apps | `/projects*` | Yes | Partial replica/controller | Read/create image project | Persistent VM mode, settings, operation convergence |
| Environments | `/environments*` | Yes | Partial | No management UI | Branch/preview/domain lifecycle |
| Git/build/deploy | `/git/*`, `/builds*`, `/deployments*`, `/rollouts` | Yes | Build/controller split | Inspection only | Source-to-healthy deployment |
| Runtime replicas | `/replicas*`, `/vms*` | Partial | Firecracker plus partial controller | Read-only mostly | Durable workload reconciler, process observation, VM workspace |
| Images/guest bases | `/images*`, `/guest-bases` | Yes, partial | OCI conversion | Images page | Registry/platform/digest/readiness/deploy action |
| Storage/backup | `/volumes*`, snapshots/backups | Partial | Snapshot task only | Storage/schedule pages | Correct attach, ObjectStore, restore product |
| Domains/TLS/DNS | `/domains*`, project DNS | Partial | Gateway/base ACME | Domains page | Custom TLS lifecycle and activation |
| Networks/firewall/cache | `/networks*`, `/firewall*`, `/cache*` | Partial | Partial gateway/host paths | Mostly read-only | Enforcement, attachment, complete controls |
| Logs/metrics/analytics | `/logs`, `/metrics`, `/traffic`, analytics | Partial | Heartbeat/partial collectors | Logs/Metrics | Real resource telemetry, alerting, traceability |
| Nodes/servers | `/servers*`, host endpoints | Yes, partial | Heartbeat/controller | Node inventory | Admission, capacity, placement, drain, repair |
| Billing/usage | `/billing/*`, `/usage*`, analytics | Preview/partial | Partial meters | Read-only billing | Commercial lifecycle and metered product |
| Operations/events | `/operations`, `/events` | Partial | TaskRunner limited | Polling Tasks page | Universal operation executor, SSE/UI linkage |

### 9.4 Creation endpoints that require immediate implementation attention

The following endpoint families are present but should not be considered complete until the adjacent control-plane work is implemented:

1. `POST /projects` and `POST /projects/compose`: create a durable workload specification and operation, not only a project plus immediate boot attempts.
2. `POST /projects/{projectId}/deployments/git`: link the build and deployment, provision replicas after successful build, and health-gate promotion.
3. `POST /projects/{projectId}/builds` and `/builds/run`: return an operation ID, persist worker state, support retry/cancel, and publish immutable artifact metadata.
4. `PATCH /projects/{projectId}/scale` and replica batch operations: update desired cardinality and let a reconciler converge it; do not directly mutate only the process and project ID list.
5. `POST /images/custom/oci`: validate archive bounds/digests/platform/kernel compatibility and make the resulting image deployable through a user-visible action.
6. `POST /volumes` and resize/delete routes: create a real block artifact, attach/detach safely, enforce quota/capacity, and protect attached data.
7. Snapshot/restore and schedule routes: create immutable, selectable recovery points and verify restore before marking success.
8. Domain add/verify routes: persist verification, issue/bind certificates, and activate gateway routing only after policy checks.
9. `POST /servers`: enroll a real node with capabilities proven from the host, then make it eligible for scheduler placement.
10. Billing/subscription endpoints: move from plan preview to customer/org subscription, metering, invoice, payment, and lifecycle records.

### 9.5 Complete registered route inventory

The following inventory is generated from `internal/api/api.go` and is included so future implementation work can map each API route to a product workflow. Routes with the same family are grouped by their first path segment; the handler name is included as the internal implementation anchor.

#### `/auth` route family (9 routes)

| Method | Path | Permission | Auth | Handler | Source line |
|---|---|---|---|---|---:|
| `POST` | `/auth/login` | `public` | `false` | `handleLogin` | 521 |
| `POST` | `/auth/logout` | `public` | `true` | `handleLogout` | 523 |
| `POST` | `/auth/signup` | `public` | `false` | `handleSignup` | 525 |
| `POST` | `/auth/password/forgot` | `public` | `false` | `handlePasswordForgot` | 526 |
| `POST` | `/auth/password/reset` | `public` | `false` | `handlePasswordReset` | 527 |
| `GET` | `/auth/jwks` | `public` | `false` | `handleJWKS` | 528 |
| `POST` | `/auth/ldap/login` | `public` | `false` | `handleLDAPLogin` | 529 |
| `POST` | `/auth/token` | `public` | `true` | `handleMintToken` | 530 |
| `GET` | `/auth/session` | `public` | `true` | `handleSession` | 531 |

#### `/csrf` route family (1 routes)

| Method | Path | Permission | Auth | Handler | Source line |
|---|---|---|---|---|---:|
| `GET` | `/csrf` | `public` | `true` | `handleCSRFToken` | 512 |

#### `/events` route family (1 routes)

| Method | Path | Permission | Auth | Handler | Source line |
|---|---|---|---|---|---:|
| `GET` | `/events` | `event.read` | `true` | `handleEvents` | 516 |

#### `/feedback` route family (2 routes)

| Method | Path | Permission | Auth | Handler | Source line |
|---|---|---|---|---|---:|
| `POST` | `/feedback` | `feedback.write` | `true` | `handleFeedback` | 518 |
| `GET` | `/feedback` | `feedback.read` | `true` | `handleListFeedback` | 519 |

#### `/global` route family (2 routes)

| Method | Path | Permission | Auth | Handler | Source line |
|---|---|---|---|---|---:|
| `GET` | `/global/analytics` | `analytics.read` | `true` | `handleGlobalAnalytics` | 773 |
| `GET` | `/global/analytics/timeseries` | `analytics.read` | `true` | `handleGlobalAnalyticsTimeseries` | 774 |

#### `/groups` route family (8 routes)

| Method | Path | Permission | Auth | Handler | Source line |
|---|---|---|---|---|---:|
| `GET` | `/groups` | `group.create` | `true` | `handleListGroups` | 560 |
| `POST` | `/groups` | `group.create` | `true` | `handleCreateGroup` | 561 |
| `GET` | `/groups/{groupId}` | `project.read` | `true` | `handleGetGroup` | 562 |
| `PATCH` | `/groups/{groupId}` | `group.update` | `true` | `handlePatchGroup` | 563 |
| `DELETE` | `/groups/{groupId}` | `group.delete` | `true` | `handleDeleteGroup` | 564 |
| `GET` | `/groups/{groupId}/projects` | `project.read` | `true` | `handleGroupProjects` | 565 |
| `POST` | `/groups/{groupId}/projects/{projectId}` | `project.write` | `true` | `handleAddGroupProject` | 566 |
| `DELETE` | `/groups/{groupId}/projects/{projectId}` | `project.write` | `true` | `handleRemoveGroupProject` | 567 |

#### `/guest-bases` route family (1 routes)

| Method | Path | Permission | Auth | Handler | Source line |
|---|---|---|---|---|---:|
| `GET` | `/guest-bases` | `project.read` | `true` | `handleListGuestBases` | 791 |

#### `/health` route family (1 routes)

| Method | Path | Permission | Auth | Handler | Source line |
|---|---|---|---|---|---:|
| `GET` | `/health` | `public` | `false` | `handleHealth` | 513 |

#### `/healthz` route family (1 routes)

| Method | Path | Permission | Auth | Handler | Source line |
|---|---|---|---|---|---:|
| `GET` | `/healthz` | `public` | `false` | `handleHealthz` | 514 |

#### `/host` route family (5 routes)

| Method | Path | Permission | Auth | Handler | Source line |
|---|---|---|---|---|---:|
| `GET` | `/host/overview` | `metric.read` | `true` | `handleHostOverview` | 831 |
| `GET` | `/host/ports` | `metric.read` | `true` | `handleHostPorts` | 832 |
| `GET` | `/host/kernel` | `metric.read` | `true` | `handleHostKernel` | 833 |
| `GET` | `/host/prerequisites` | `metric.read` | `true` | `handleHostPrerequisites` | 834 |
| `GET` | `/host/runtime` | `metric.read` | `true` | `handleRuntimeConfig` | 835 |

#### `/images` route family (11 routes)

| Method | Path | Permission | Auth | Handler | Source line |
|---|---|---|---|---|---:|
| `GET` | `/images` | `project.read` | `true` | `handleListImages` | 793 |
| `GET` | `/images/base` | `project.read` | `true` | `handleBaseImage` | 794 |
| `GET` | `/images/base/readiness` | `project.read` | `true` | `handleBaseImageReadiness` | 795 |
| `POST` | `/images/custom` | `image.upload` | `true` | `handleUploadCustomImage` | 796 |
| `POST` | `/images/custom/oci` | `image.upload` | `true` | `handleUploadCustomOCIImage` | 801 |
| `GET` | `/images/search` | `project.read` | `true` | `handleImageSearch` | 802 |
| `GET` | `/images/{reference}` | `project.read` | `true` | `handleGetImage` | 803 |
| `DELETE` | `/images/{reference}` | `project.delete` | `true` | `handleDeleteImage` | 804 |
| `POST` | `/images/prune` | `cache.purge` | `true` | `handlePruneImages` | 805 |
| `GET` | `/images/stats` | `project.read` | `true` | `handleImageStats` | 806 |
| `GET` | `/images/ml` | `project.read` | `true` | `handleImageSearch` | 807 |

#### `/login` route family (1 routes)

| Method | Path | Permission | Auth | Handler | Source line |
|---|---|---|---|---|---:|
| `POST` | `/login` | `public` | `false` | `handleLogin` | 522 |

#### `/logout` route family (1 routes)

| Method | Path | Permission | Auth | Handler | Source line |
|---|---|---|---|---|---:|
| `POST` | `/logout` | `public` | `true` | `handleLogout` | 524 |

#### `/logs` route family (1 routes)

| Method | Path | Permission | Auth | Handler | Source line |
|---|---|---|---|---|---:|
| `GET` | `/logs` | `log.read` | `true` | `handleDaemonLogs` | 837 |

#### `/org` route family (2 routes)

| Method | Path | Permission | Auth | Handler | Source line |
|---|---|---|---|---|---:|
| `GET` | `/org` | `project.read` | `true` | `handleGetOrg` | 557 |
| `PATCH` | `/org` | `org.settings` | `true` | `handlePatchOrg` | 558 |

#### `/orgs` route family (13 routes)

| Method | Path | Permission | Auth | Handler | Source line |
|---|---|---|---|---|---:|
| `GET` | `/orgs` | `project.read` | `true` | `handleListOrgs` | 543 |
| `GET` | `/orgs/default` | `project.read` | `true` | `handleDefaultOrg` | 544 |
| `POST` | `/orgs` | `org.member.role` | `true` | `handleCreateOrg` | 545 |
| `GET` | `/orgs/current` | `project.read` | `true` | `handleGetCurrentOrg` | 546 |
| `PATCH` | `/orgs/current` | `org.settings` | `true` | `handlePatchCurrentOrg` | 547 |
| `DELETE` | `/orgs/current` | `org.settings` | `true` | `handleDeleteCurrentOrg` | 548 |
| `GET` | `/orgs/members` | `member.list` | `true` | `handleListOrgMembers` | 549 |
| `POST` | `/orgs/members` | `org.member.add` | `true` | `handleAddOrgMember` | 550 |
| `PATCH` | `/orgs/members/{username}` | `org.member.role` | `true` | `handlePatchOrgMember` | 551 |
| `DELETE` | `/orgs/members/{username}` | `org.member.remove` | `true` | `handleRemoveOrgMember` | 552 |
| `GET` | `/orgs/audit` | `org.audit` | `true` | `handleOrgAudit` | 553 |
| `POST` | `/orgs/transfer` | `org.transfer` | `true` | `handleOrgTransfer` | 554 |
| `GET` | `/orgs/events` | `event.read` | `true` | `handleOrgEvents` | 555 |

#### `/overview` route family (1 routes)

| Method | Path | Permission | Auth | Handler | Source line |
|---|---|---|---|---|---:|
| `GET` | `/overview` | `project.read` | `true` | `handleOverview` | 809 |

#### `/permissions` route family (1 routes)

| Method | Path | Permission | Auth | Handler | Source line |
|---|---|---|---|---|---:|
| `GET` | `/permissions` | `org.audit` | `true` | `handleListPermissions` | 860 |

#### `/projects` route family (203 routes)

| Method | Path | Permission | Auth | Handler | Source line |
|---|---|---|---|---|---:|
| `GET` | `/projects` | `project.list` | `true` | `handleListProjects` | 569 |
| `POST` | `/projects` | `project.create` | `true` | `handleCreateProject` | 570 |
| `POST` | `/projects/compose` | `project.create` | `true` | `handleCreateComposeProject` | 571 |
| `GET` | `/projects/{projectId}` | `project.read` | `true` | `handleGetProject` | 572 |
| `PATCH` | `/projects/{projectId}` | `project.rename` | `true` | `handlePatchProject` | 573 |
| `DELETE` | `/projects/{projectId}` | `project.delete` | `true` | `handleDeleteProject` | 574 |
| `POST` | `/projects/{projectId}/redeploy` | `project.deploy` | `true` | `handleRedeployProject` | 575 |
| `GET` | `/projects/{projectId}/scale` | `replica.list` | `true` | `handleGetScale` | 576 |
| `PATCH` | `/projects/{projectId}/scale` | `project.scale` | `true` | `handleScale` | 577 |
| `GET` | `/projects/{projectId}/healthcheck` | `project.read` | `true` | `handleGetHealthcheck` | 578 |
| `PUT` | `/projects/{projectId}/healthcheck` | `project.settings` | `true` | `handlePutHealthcheck` | 579 |
| `GET` | `/projects/{projectId}/autoscale` | `project.read` | `true` | `handleGetAutoscale` | 580 |
| `PUT` | `/projects/{projectId}/autoscale` | `project.settings` | `true` | `handlePutAutoscale` | 581 |
| `POST` | `/projects/{projectId}/restart` | `project.restart` | `true` | `handleRestartProject` | 582 |
| `GET` | `/projects/{projectId}/env` | `env.list` | `true` | `handleListEnv` | 583 |
| `POST` | `/projects/{projectId}/env` | `env.set` | `true` | `handleSetEnv` | 584 |
| `POST` | `/projects/{projectId}/env/bulk` | `env.set` | `true` | `handleSetEnvBulk` | 585 |
| `PATCH` | `/projects/{projectId}/env/{envId}` | `env.set` | `true` | `handlePatchEnv` | 586 |
| `DELETE` | `/projects/{projectId}/env/{envId}` | `env.set` | `true` | `handleDeleteEnv` | 587 |
| `GET` | `/projects/{projectId}/secrets` | `secret.list` | `true` | `handleListSecrets` | 588 |
| `POST` | `/projects/{projectId}/secrets` | `secret.create` | `true` | `handleCreateSecret` | 589 |
| `DELETE` | `/projects/{projectId}/secrets/{secretId}` | `secret.delete` | `true` | `handleDeleteSecret` | 590 |
| `GET` | `/projects/{projectId}/domains` | `domain.list` | `true` | `handleListDomains` | 591 |
| `POST` | `/projects/{projectId}/domains` | `domain.add` | `true` | `handleAddDomain` | 592 |
| `GET` | `/projects/{projectId}/domains/records` | `domain.list` | `true` | `handleDomainRecords` | 593 |
| `GET` | `/projects/{projectId}/domains/{domainId}` | `domain.list` | `true` | `handleGetDomain` | 594 |
| `DELETE` | `/projects/{projectId}/domains/{domainId}` | `domain.remove` | `true` | `handleDeleteDomain` | 595 |
| `POST` | `/projects/{projectId}/domains/{domainId}/verify` | `domain.verify` | `true` | `handleVerifyDomain` | 596 |
| `POST` | `/projects/{projectId}/domains/{domainId}/reverify` | `domain.verify` | `true` | `handleVerifyDomain` | 597 |
| `GET` | `/projects/{projectId}/dns` | `domain.list` | `true` | `handleProjectDNS` | 598 |
| `GET` | `/projects/{projectId}/dns/records` | `domain.list` | `true` | `handleProjectDNS` | 599 |
| `GET` | `/projects/{projectId}/compose` | `project.read` | `true` | `handleGetCompose` | 600 |
| `PUT` | `/projects/{projectId}/compose` | `project.import` | `true` | `handlePutCompose` | 601 |
| `POST` | `/projects/{projectId}/compose/validate` | `project.import` | `true` | `handleValidateCompose` | 602 |
| `GET` | `/projects/{projectId}/compose/preview` | `project.read` | `true` | `handleComposePreview` | 603 |
| `GET` | `/projects/{projectId}/logs` | `log.read` | `true` | `handleProjectLogs` | 604 |
| `GET` | `/projects/{projectId}/logs/stream` | `log.read` | `true` | `handleProjectLogStream` | 605 |
| `GET` | `/projects/{projectId}/metrics` | `metric.read` | `true` | `handleProjectMetrics` | 606 |
| `GET` | `/projects/{projectId}/traffic` | `traffic.read` | `true` | `handleProjectTraffic` | 607 |
| `GET` | `/projects/{projectId}/events` | `event.read` | `true` | `handleProjectEvents` | 608 |
| `GET` | `/projects/{projectId}/pool` | `replica.list` | `true` | `handlePoolStatus` | 609 |
| `POST` | `/projects/{projectId}/pool/drain` | `project.settings` | `true` | `handlePoolDrain` | 610 |
| `GET` | `/projects/{projectId}/status` | `project.read` | `true` | `handleProjectStatus` | 611 |
| `GET` | `/projects/{projectId}/liveness` | `project.read` | `true` | `handleProjectLiveness` | 612 |
| `GET` | `/projects/{projectId}/replicas` | `replica.list` | `true` | `handleListReplicas` | 613 |
| `POST` | `/projects/{projectId}/replicas/batch/start` | `replica.start` | `true` | `handleReplicaBatchStart` | 614 |
| `POST` | `/projects/{projectId}/replicas/batch/stop` | `replica.stop` | `true` | `handleReplicaBatchStop` | 615 |
| `GET` | `/projects/{projectId}/replicas/{n}` | `replica.list` | `true` | `handleGetReplica` | 616 |
| `POST` | `/projects/{projectId}/replicas/{n}/start` | `replica.start` | `true` | `handleReplicaStart` | 617 |
| `POST` | `/projects/{projectId}/replicas/{n}/stop` | `replica.stop` | `true` | `handleReplicaStop` | 618 |
| `POST` | `/projects/{projectId}/replicas/{n}/restart` | `replica.restart` | `true` | `handleReplicaRestart` | 619 |
| `POST` | `/projects/{projectId}/replicas/{n}/snapshot` | `replica.snapshot` | `true` | `handleReplicaSnapshot` | 620 |
| `POST` | `/projects/{projectId}/replicas/{n}/restore` | `replica.restore` | `true` | `handleReplicaRestore` | 621 |
| `POST` | `/projects/{projectId}/replicas/{n}/recover` | `replica.restore` | `true` | `handleReplicaRestore` | 622 |
| `DELETE` | `/projects/{projectId}/replicas/{n}` | `replica.delete` | `true` | `handleReplicaDelete` | 623 |
| `GET` | `/projects/{projectId}/replicas/{n}/logs` | `log.read` | `true` | `handleReplicaLogs` | 624 |
| `GET` | `/projects/{projectId}/replicas/{n}/metrics` | `metric.read` | `true` | `handleReplicaMetrics` | 625 |
| `GET` | `/projects/{projectId}/replicas/{n}/traffic` | `traffic.read` | `true` | `handleReplicaTraffic` | 626 |
| `GET` | `/projects/{projectId}/replicas/{n}/health` | `replica.list` | `true` | `handleReplicaHealth` | 627 |
| `GET` | `/projects/{projectId}/replicas/{n}/ssh-info` | `ssh.connect` | `true` | `handleSSHInfo` | 628 |
| `POST` | `/projects/{projectId}/replicas/{n}/ssh-cert` | `ssh.connect` | `true` | `handleSSHCert` | 629 |
| `POST` | `/projects/{projectId}/replicas/{n}/exec` | `replica.exec` | `true` | `handleReplicaExec` | 630 |
| `GET` | `/projects/{projectId}/replicas/{n}/console` | `console.open` | `true` | `handleReplicaConsole` | 631 |
| `GET` | `/projects/{projectId}/deployments` | `deployment.list` | `true` | `handleListDeployments` | 632 |
| `POST` | `/projects/{projectId}/deployments` | `deployment.create` | `true` | `handleCreateDeployment` | 633 |
| `GET` | `/projects/{projectId}/deployments/upload` | `deployment.create` | `true` | `handleDeploymentUpload` | 634 |
| `GET` | `/projects/{projectId}/deployments/{deployId}` | `deployment.list` | `true` | `handleGetDeployment` | 635 |
| `GET` | `/projects/{projectId}/deployments/{deployId}/checks` | `deployment.list` | `true` | `handleGetDeploymentChecks` | 636 |
| `PUT` | `/projects/{projectId}/deployments/{deployId}/checks` | `deployment.create` | `true` | `handleUpsertDeploymentChecks` | 637 |
| `PATCH` | `/projects/{projectId}/deployments/{deployId}/checks/{checkName}` | `deployment.create` | `true` | `handleSetDeploymentCheck` | 638 |
| `PUT` | `/projects/{projectId}/deployments/{deployId}/rollout` | `deployment.promote` | `true` | `handleSetDeploymentRollout` | 639 |
| `GET` | `/projects/{projectId}/deployments/{deployId}/logs` | `log.read` | `true` | `handleDeploymentLogs` | 640 |
| `POST` | `/projects/{projectId}/deployments/{deployId}/promote` | `deployment.promote` | `true` | `handlePromoteDeployment` | 641 |
| `POST` | `/projects/{projectId}/deployments/{deployId}/rollback` | `deployment.rollback` | `true` | `handleRollbackDeployment` | 642 |
| `DELETE` | `/projects/{projectId}/deployments/{deployId}` | `deployment.rollback` | `true` | `handleDeleteDeployment` | 643 |
| `GET` | `/projects/{projectId}/deployments/{deployId}/source` | `deployment.list` | `true` | `handleDeploymentSource` | 644 |
| `GET` | `/projects/{projectId}/deployments/{deployId}/og` | `deployment.list` | `true` | `handleDeploymentOG` | 645 |
| `GET` | `/projects/{projectId}/settings/general` | `project.read` | `true` | `handleGetGeneral` | 646 |
| `PATCH` | `/projects/{projectId}/settings/general` | `project.settings` | `true` | `handlePatchGeneral` | 647 |
| `POST` | `/projects/{projectId}/avatar` | `project.avatar` | `true` | `handleSetAvatar` | 648 |
| `POST` | `/projects/{projectId}/transfer` | `project.transfer` | `true` | `handleTransferProject` | 649 |
| `GET` | `/projects/{projectId}/settings/build` | `project.read` | `true` | `handleGetBuild` | 650 |
| `PUT` | `/projects/{projectId}/settings/build` | `project.settings` | `true` | `handlePutBuild` | 651 |
| `GET` | `/projects/{projectId}/settings/checks` | `project.read` | `true` | `handleGetChecks` | 652 |
| `POST` | `/projects/{projectId}/settings/checks` | `project.settings` | `true` | `handlePutChecks` | 653 |
| `GET` | `/projects/{projectId}/settings/rollout` | `project.read` | `true` | `handleGetRollout` | 654 |
| `PUT` | `/projects/{projectId}/settings/rollout` | `project.settings` | `true` | `handlePutRollout` | 655 |
| `GET` | `/projects/{projectId}/settings/build-machine` | `project.read` | `true` | `handleGetBuildMachine` | 656 |
| `PUT` | `/projects/{projectId}/settings/build-machine` | `project.settings` | `true` | `handlePutBuildMachine` | 657 |
| `POST` | `/projects/{projectId}/settings/ignore-command` | `git.settings` | `true` | `handleSetIgnoreCommand` | 658 |
| `GET` | `/projects/{projectId}/settings/framework` | `project.read` | `true` | `handleGetFramework` | 659 |
| `GET` | `/projects/{projectId}/environments` | `project.read` | `true` | `handleListEnvironments` | 660 |
| `POST` | `/projects/{projectId}/environments` | `project.settings` | `true` | `handleCreateEnvironment` | 661 |
| `GET` | `/projects/{projectId}/environments/available` | `project.read` | `true` | `handleEnvironmentsAvailable` | 662 |
| `GET` | `/projects/{projectId}/environments/{envId}` | `project.read` | `true` | `handleGetEnvironment` | 663 |
| `PATCH` | `/projects/{projectId}/environments/{envId}` | `project.settings` | `true` | `handlePatchEnvironment` | 664 |
| `DELETE` | `/projects/{projectId}/environments/{envId}` | `project.settings` | `true` | `handleDeleteEnvironment` | 665 |
| `POST` | `/projects/{projectId}/environments/{envId}/branch` | `project.settings` | `true` | `handleEnvBranch` | 666 |
| `POST` | `/projects/{projectId}/environments/{envId}/domain` | `project.settings` | `true` | `handleEnvDomain` | 667 |
| `GET` | `/projects/{projectId}/environments/{envId}/range` | `project.read` | `true` | `handleEnvRange` | 668 |
| `GET` | `/projects/{projectId}/settings/git` | `project.read` | `true` | `handleGetGit` | 669 |
| `PUT` | `/projects/{projectId}/settings/git` | `git.settings` | `true` | `handlePutGit` | 670 |
| `POST` | `/projects/{projectId}/settings/git/sync` | `git.settings` | `true` | `handleGitSync` | 671 |
| `PATCH` | `/projects/{projectId}/settings/git/toggles` | `git.settings` | `true` | `handleGitToggles` | 672 |
| `GET` | `/projects/{projectId}/settings/git/lfs` | `project.read` | `true` | `handleGetGitLFS` | 673 |
| `PUT` | `/projects/{projectId}/settings/git/lfs` | `git.settings` | `true` | `handlePutGitLFS` | 674 |
| `GET` | `/projects/{projectId}/hooks` | `project.read` | `true` | `handleListHooks` | 675 |
| `POST` | `/projects/{projectId}/hooks` | `hook.create` | `true` | `handleCreateHook` | 676 |
| `DELETE` | `/projects/{projectId}/hooks/{hookId}` | `hook.delete` | `true` | `handleDeleteHook` | 677 |
| `POST` | `/projects/{projectId}/hooks/{hookId}/trigger` | `hook.trigger` | `true` | `handleTriggerHook` | 678 |
| `GET` | `/projects/{projectId}/settings/deployment-protection` | `project.read` | `true` | `handleGetProtection` | 679 |
| `PUT` | `/projects/{projectId}/settings/deployment-protection` | `project.settings` | `true` | `handlePutProtection` | 680 |
| `GET` | `/projects/{projectId}/settings/oidc` | `project.read` | `true` | `handleGetOIDC` | 681 |
| `PUT` | `/projects/{projectId}/settings/oidc` | `project.settings` | `true` | `handlePutOIDC` | 682 |
| `GET` | `/projects/{projectId}/settings/functions` | `git.settings` | `true` | `handleGetFunctions` | 683 |
| `PUT` | `/projects/{projectId}/settings/functions` | `git.settings` | `true` | `handlePutFunctions` | 684 |
| `GET` | `/projects/{projectId}/crons` | `cron.create` | `true` | `handleListCrons` | 685 |
| `POST` | `/projects/{projectId}/crons` | `cron.create` | `true` | `handleCreateCron` | 686 |
| `GET` | `/projects/{projectId}/crons/history` | `cron.update` | `true` | `handleCronHistory` | 687 |
| `GET` | `/projects/{projectId}/crons/{cronId}` | `cron.create` | `true` | `handleGetCron` | 688 |
| `PATCH` | `/projects/{projectId}/crons/{cronId}` | `cron.update` | `true` | `handlePatchCron` | 689 |
| `DELETE` | `/projects/{projectId}/crons/{cronId}` | `cron.delete` | `true` | `handleDeleteCron` | 690 |
| `POST` | `/projects/{projectId}/crons/{cronId}/run` | `cron.run` | `true` | `handleRunCron` | 691 |
| `GET` | `/projects/{projectId}/members` | `member.list` | `true` | `handleListProjectMembers` | 692 |
| `POST` | `/projects/{projectId}/members` | `member.invite` | `true` | `handleAddProjectMember` | 693 |
| `GET` | `/projects/{projectId}/members/{username}` | `member.list` | `true` | `handleGetProjectMember` | 694 |
| `PATCH` | `/projects/{projectId}/members/{username}` | `member.role` | `true` | `handlePatchProjectMember` | 695 |
| `DELETE` | `/projects/{projectId}/members/{username}` | `member.remove` | `true` | `handleRemoveProjectMember` | 696 |
| `POST` | `/projects/{projectId}/members/invite` | `member.invite` | `true` | `handleInviteMember` | 697 |
| `GET` | `/projects/{projectId}/drains` | `project.settings` | `true` | `handleListDrains` | 698 |
| `POST` | `/projects/{projectId}/drains` | `drain.create` | `true` | `handleCreateDrain` | 699 |
| `DELETE` | `/projects/{projectId}/drains/{drainId}` | `drain.delete` | `true` | `handleDeleteDrain` | 700 |
| `POST` | `/projects/{projectId}/drains/{drainId}/test` | `drain.create` | `true` | `handleTestDrain` | 701 |
| `GET` | `/projects/{projectId}/alerts` | `project.read` | `true` | `handleListAlerts` | 702 |
| `POST` | `/projects/{projectId}/alerts` | `alert.create` | `true` | `handleCreateAlert` | 703 |
| `GET` | `/projects/{projectId}/alerts/{alertId}` | `project.read` | `true` | `handleGetAlert` | 704 |
| `PATCH` | `/projects/{projectId}/alerts/{alertId}` | `alert.update` | `true` | `handlePatchAlert` | 705 |
| `DELETE` | `/projects/{projectId}/alerts/{alertId}` | `alert.delete` | `true` | `handleDeleteAlert` | 706 |
| `POST` | `/projects/{projectId}/alerts/{alertId}/silence` | `alert.silence` | `true` | `handleSilenceAlert` | 707 |
| `POST` | `/projects/{projectId}/alerts/{alertId}/unsilence` | `alert.silence` | `true` | `handleUnsilenceAlert` | 708 |
| `GET` | `/projects/{projectId}/settings/security` | `project.read` | `true` | `handleGetSecurity` | 709 |
| `PUT` | `/projects/{projectId}/settings/security` | `project.settings` | `true` | `handlePutSecurity` | 710 |
| `GET` | `/projects/{projectId}/settings/retention` | `project.read` | `true` | `handleGetRetention` | 711 |
| `PUT` | `/projects/{projectId}/settings/retention` | `project.settings` | `true` | `handlePutRetention` | 712 |
| `GET` | `/projects/{projectId}/settings/networking` | `project.network` | `true` | `handleGetNetworking` | 713 |
| `PUT` | `/projects/{projectId}/settings/networking` | `project.network` | `true` | `handlePutNetworking` | 714 |
| `GET` | `/projects/{projectId}/settings/advanced` | `project.read` | `true` | `handleGetAdvanced` | 715 |
| `PUT` | `/projects/{projectId}/settings/advanced` | `project.settings` | `true` | `handlePutAdvanced` | 716 |
| `GET` | `/projects/{projectId}/settings/passport` | `project.read` | `true` | `handleGetPassport` | 717 |
| `PUT` | `/projects/{projectId}/settings/passport` | `project.settings` | `true` | `handlePutPassport` | 718 |
| `GET` | `/projects/{projectId}/settings/microfrontends` | `project.read` | `true` | `handleGetMicrofrontends` | 719 |
| `PUT` | `/projects/{projectId}/settings/microfrontends` | `project.settings` | `true` | `handlePutMicrofrontends` | 720 |
| `GET` | `/projects/{projectId}/redirects` | `project.read` | `true` | `handleListRedirects` | 721 |
| `POST` | `/projects/{projectId}/redirects` | `redirect.create` | `true` | `handleCreateRedirect` | 722 |
| `DELETE` | `/projects/{projectId}/redirects/{redirectId}` | `redirect.delete` | `true` | `handleDeleteRedirect` | 723 |
| `PUT` | `/projects/{projectId}/redirects/bulk` | `redirect.create` | `true` | `handleBulkRedirects` | 724 |
| `GET` | `/projects/{projectId}/analytics/usage` | `analytics.read` | `true` | `handleAnalyticsUsage` | 725 |
| `GET` | `/projects/{projectId}/analytics/usage/timeseries` | `analytics.read` | `true` | `handleAnalyticsTimeseries` | 726 |
| `GET` | `/projects/{projectId}/analytics/paths` | `analytics.read` | `true` | `handleAnalyticsPaths` | 727 |
| `GET` | `/projects/{projectId}/analytics/status-codes` | `analytics.read` | `true` | `handleAnalyticsStatusCodes` | 728 |
| `GET` | `/projects/{projectId}/analytics/bandwidth` | `analytics.read` | `true` | `handleAnalyticsBandwidth` | 729 |
| `GET` | `/projects/{projectId}/analytics/requests` | `analytics.read` | `true` | `handleAnalyticsRequests` | 730 |
| `GET` | `/projects/{projectId}/analytics/invocations` | `analytics.read` | `true` | `handleAnalyticsInvocations` | 731 |
| `GET` | `/projects/{projectId}/observability/web-vitals` | `webvital.read` | `true` | `handleWebVitals` | 732 |
| `POST` | `/projects/{projectId}/observability/web-vitals/beacon` | `webvital.read` | `true` | `handleWebVitalsBeacon` | 733 |
| `GET` | `/projects/{projectId}/observability/web-vitals/timeseries` | `webvital.read` | `true` | `handleWebVitalsTimeseries` | 734 |
| `GET` | `/projects/{projectId}/observability/lcp` | `webvital.read` | `true` | `handleAnalyticsUsage` | 735 |
| `GET` | `/projects/{projectId}/observability/cls` | `webvital.read` | `true` | `handleAnalyticsUsage` | 736 |
| `GET` | `/projects/{projectId}/observability/fid` | `webvital.read` | `true` | `handleAnalyticsUsage` | 737 |
| `GET` | `/projects/{projectId}/firewall/rules` | `project.read` | `true` | `handleListFirewallRules` | 738 |
| `POST` | `/projects/{projectId}/firewall/rules` | `firewall.create` | `true` | `handleCreateFirewallRule` | 739 |
| `GET` | `/projects/{projectId}/firewall/rules/{ruleId}` | `project.read` | `true` | `handleGetFirewallRule` | 740 |
| `DELETE` | `/projects/{projectId}/firewall/rules/{ruleId}` | `firewall.delete` | `true` | `handleDeleteFirewallRule` | 741 |
| `PATCH` | `/projects/{projectId}/firewall/rules/{ruleId}` | `firewall.update` | `true` | `handlePatchFirewallRule` | 742 |
| `GET` | `/projects/{projectId}/firewall/events` | `traffic.read` | `true` | `handleFirewallEvents` | 743 |
| `GET` | `/projects/{projectId}/firewall/stats` | `traffic.read` | `true` | `handleFirewallStats` | 744 |
| `POST` | `/projects/{projectId}/firewall/whitelist` | `firewall.create` | `true` | `handleFirewallWhitelist` | 745 |
| `GET` | `/projects/{projectId}/cache/stats` | `cache.stats` | `true` | `handleCacheStats` | 746 |
| `POST` | `/projects/{projectId}/cache/purge` | `cache.purge` | `true` | `handleCachePurge` | 747 |
| `POST` | `/projects/{projectId}/cache/purge/path` | `cache.purge` | `true` | `handleCachePurgePath` | 748 |
| `GET` | `/projects/{projectId}/volumes` | `volume.read` | `true` | `handleListVolumes` | 749 |
| `POST` | `/projects/{projectId}/volumes` | `volume.create` | `true` | `handleCreateVolume` | 750 |
| `GET` | `/projects/{projectId}/volumes/{volumeId}` | `volume.read` | `true` | `handleGetVolume` | 751 |
| `DELETE` | `/projects/{projectId}/volumes/{volumeId}` | `volume.delete` | `true` | `handleDeleteVolume` | 752 |
| `POST` | `/projects/{projectId}/volumes/{volumeId}/resize` | `volume.resize` | `true` | `handleResizeVolume` | 753 |
| `GET` | `/projects/{projectId}/volumes/{volumeId}/usage` | `volume.read` | `true` | `handleVolumeUsage` | 754 |
| `POST` | `/projects/{projectId}/export` | `project.export` | `true` | `handleExportProject` | 755 |
| `POST` | `/projects/{projectId}/import` | `project.import` | `true` | `handleImportProject` | 756 |
| `PUT` | `/projects/{projectId}/ssh` | `ssh.toggle` | `true` | `handleSSHToggle` | 757 |
| `POST` | `/projects/{projectId}/git/import` | `git.import` | `true` | `handleGitImport` | 758 |
| `POST` | `/projects/{projectId}/deployments/git` | `build.create` | `true` | `handleDeployGit` | 759 |
| `GET` | `/projects/{projectId}/builds` | `build.create` | `true` | `handleListBuilds` | 760 |
| `POST` | `/projects/{projectId}/builds` | `build.create` | `true` | `handleCreateBuild` | 761 |
| `POST` | `/projects/{projectId}/builds/run` | `build.create` | `true` | `handleCreateBuild` | 762 |
| `GET` | `/projects/{projectId}/builds/{buildId}/logs` | `log.read` | `true` | `handleBuildLogs` | 763 |
| `GET` | `/projects/{projectId}/builds/{buildId}/logs/stream` | `log.read` | `true` | `handleBuildLogStream` | 764 |
| `GET` | `/projects/{projectId}/git/branches` | `git.import` | `true` | `handleGitBranches` | 765 |
| `GET` | `/projects/{projectId}/rollouts` | `deployment.list` | `true` | `handleListRollouts` | 766 |
| `GET` | `/projects/{projectId}/services` | `project.read` | `true` | `handleListServices` | 767 |
| `GET` | `/projects/{projectId}/services/{serviceName}` | `project.read` | `true` | `handleGetService` | 768 |
| `POST` | `/projects/{projectId}/services/{serviceName}/scale` | `project.scale` | `true` | `handleScaleService` | 769 |
| `GET` | `/projects/{projectId}/networks` | `project.network` | `true` | `handleListNetworks` | 770 |
| `POST` | `/projects/{projectId}/networks` | `project.network` | `true` | `handleCreateNetwork` | 771 |

#### `/replicas` route family (2 routes)

| Method | Path | Permission | Auth | Handler | Source line |
|---|---|---|---|---|---:|
| `GET` | `/replicas` | `replica.list` | `true` | `handleGlobalReplicas` | 781 |
| `GET` | `/replicas/{replicaId}` | `replica.list` | `true` | `handleGlobalReplica` | 782 |

#### `/roles` route family (9 routes)

| Method | Path | Permission | Auth | Handler | Source line |
|---|---|---|---|---|---:|
| `GET` | `/roles` | `org.audit` | `true` | `handleListRoles` | 850 |
| `POST` | `/roles` | `org.member.role` | `true` | `handleCreateRole` | 851 |
| `GET` | `/roles/{roleId}` | `org.audit` | `true` | `handleGetRole` | 852 |
| `PATCH` | `/roles/{roleId}` | `org.member.role` | `true` | `handlePatchRole` | 853 |
| `DELETE` | `/roles/{roleId}` | `org.member.role` | `true` | `handleDeleteRole` | 854 |
| `GET` | `/roles/{roleId}/permissions` | `org.audit` | `true` | `handleGetRolePermissions` | 855 |
| `PUT` | `/roles/{roleId}/permissions` | `org.member.role` | `true` | `handleSetRolePermissions` | 856 |
| `POST` | `/roles/{roleId}/permissions/{permissionId}` | `org.member.role` | `true` | `handleAddRolePermission` | 857 |
| `DELETE` | `/roles/{roleId}/permissions/{permissionId}` | `org.member.role` | `true` | `handleRemoveRolePermission` | 858 |

#### `/servers` route family (6 routes)

| Method | Path | Permission | Auth | Handler | Source line |
|---|---|---|---|---|---:|
| `GET` | `/servers` | `server.register` | `true` | `handleListServers` | 843 |
| `POST` | `/servers` | `server.register` | `true` | `handleRegisterServer` | 844 |
| `GET` | `/servers/{id}` | `server.register` | `true` | `handleGetServer` | 845 |
| `POST` | `/servers/{id}/heartbeat` | `server.register` | `false` | `handleServerHeartbeat` | 846 |
| `GET` | `/servers/{id}/ssh` | `server.register` | `true` | `handleServerSSH` | 847 |
| `DELETE` | `/servers/{id}` | `server.remove` | `true` | `handleDeleteServer` | 848 |

#### `/traffic` route family (3 routes)

| Method | Path | Permission | Auth | Handler | Source line |
|---|---|---|---|---|---:|
| `GET` | `/traffic` | `traffic.read` | `true` | `handleAllTraffic` | 839 |
| `DELETE` | `/traffic` | `cache.purge` | `true` | `handleClearTraffic` | 840 |
| `GET` | `/traffic/search` | `traffic.read` | `true` | `handleTrafficSearch` | 841 |

#### `/usage` route family (4 routes)

| Method | Path | Permission | Auth | Handler | Source line |
|---|---|---|---|---|---:|
| `GET` | `/usage` | `analytics.read` | `true` | `handleUsage` | 776 |
| `GET` | `/usage/bandwidth` | `analytics.read` | `true` | `handleUsageBandwidth` | 777 |
| `GET` | `/usage/requests` | `analytics.read` | `true` | `handleUsageRequests` | 778 |
| `GET` | `/usage/timeseries` | `analytics.read` | `true` | `handleGlobalAnalyticsTimeseries` | 779 |

#### `/users` route family (9 routes)

| Method | Path | Permission | Auth | Handler | Source line |
|---|---|---|---|---|---:|
| `GET` | `/users/me` | `public` | `true` | `handleMe` | 533 |
| `PATCH` | `/users/me` | `public` | `true` | `handlePatchMe` | 534 |
| `DELETE` | `/users/me` | `public` | `true` | `handleDeleteMe` | 535 |
| `GET` | `/users/me/api-keys` | `apikey.create` | `true` | `handleListAPIKeys` | 536 |
| `POST` | `/users/me/api-keys` | `apikey.create` | `true` | `handleCreateAPIKey` | 537 |
| `DELETE` | `/users/me/api-keys/{keyId}` | `apikey.delete` | `true` | `handleDeleteAPIKey` | 538 |
| `GET` | `/users` | `user.list` | `true` | `handleListUsers` | 539 |
| `POST` | `/users` | `user.create` | `true` | `handleCreateUser` | 540 |
| `DELETE` | `/users/{username}` | `user.delete` | `true` | `handleDeleteUser` | 541 |

#### `/version` route family (1 routes)

| Method | Path | Permission | Auth | Handler | Source line |
|---|---|---|---|---|---:|
| `GET` | `/version` | `public` | `false` | `handleVersion` | 515 |

#### `/vms` route family (19 routes)

| Method | Path | Permission | Auth | Handler | Source line |
|---|---|---|---|---|---:|
| `GET` | `/vms` | `replica.list` | `true` | `handleListAllVMs` | 811 |
| `GET` | `/vms/{replicaId}` | `replica.list` | `true` | `handleGetVMCompat` | 812 |
| `POST` | `/vms/{replicaId}/start` | `replica.start` | `true` | `handleReplicaStartByID` | 813 |
| `POST` | `/vms/{replicaId}/stop` | `replica.stop` | `true` | `handleReplicaStopByID` | 814 |
| `POST` | `/vms/{replicaId}/restart` | `replica.restart` | `true` | `handleReplicaRestartByID` | 815 |
| `POST` | `/vms/{replicaId}/snapshot` | `replica.snapshot` | `true` | `handleReplicaSnapshotByID` | 816 |
| `POST` | `/vms/{replicaId}/restore` | `replica.restore` | `true` | `handleReplicaRestoreByID` | 817 |
| `POST` | `/vms/{replicaId}/recover` | `replica.restore` | `true` | `handleReplicaRestoreByID` | 818 |
| `DELETE` | `/vms/{replicaId}` | `replica.delete` | `true` | `handleVMCompatDelete` | 819 |
| `GET` | `/vms/{replicaId}/domains` | `domain.list` | `true` | `handleVMCompatDomains` | 820 |
| `GET` | `/vms/{replicaId}/logs` | `log.read` | `true` | `handleReplicaLogsByID` | 821 |
| `GET` | `/vms/{replicaId}/logs/stream` | `log.read` | `true` | `handleReplicaLogStream` | 822 |
| `GET` | `/vms/{replicaId}/metrics` | `metric.read` | `true` | `handleReplicaMetricsByID` | 823 |
| `GET` | `/vms/{replicaId}/traffic` | `traffic.read` | `true` | `handleReplicaTrafficByID` | 824 |
| `GET` | `/vms/{replicaId}/health` | `replica.list` | `true` | `handleReplicaHealthByID` | 825 |
| `GET` | `/vms/{replicaId}/ssh-info` | `ssh.connect` | `true` | `handleSSHInfoByID` | 826 |
| `POST` | `/vms/{replicaId}/ssh-cert` | `ssh.connect` | `true` | `handleSSHCertByID` | 827 |
| `POST` | `/vms/{replicaId}/exec` | `replica.exec` | `true` | `handleReplicaExecByID` | 828 |
| `GET` | `/vms/{replicaId}/console` | `console.open` | `true` | `handleReplicaConsoleByID` | 829 |

#### `/volumes` route family (6 routes)

| Method | Path | Permission | Auth | Handler | Source line |
|---|---|---|---|---|---:|
| `GET` | `/volumes` | `volume.read` | `true` | `handleListVolumes` | 784 |
| `POST` | `/volumes` | `volume.create` | `true` | `handleCreateVolume` | 785 |
| `GET` | `/volumes/{volumeId}` | `volume.read` | `true` | `handleGetVolume` | 786 |
| `DELETE` | `/volumes/{volumeId}` | `volume.delete` | `true` | `handleDeleteVolume` | 787 |
| `POST` | `/volumes/{volumeId}/resize` | `volume.resize` | `true` | `handleResizeVolume` | 788 |
| `GET` | `/volumes/{volumeId}/usage` | `volume.read` | `true` | `handleVolumeUsage` | 789 |

### 9.6 API documentation maintenance rule

For every new API endpoint, update the product workflow matrix with its creation/lifecycle purpose, persistence target, controller/runtime side effect, authorization scope, idempotency behavior, operation status, UI consumer, and acceptance test. A route should not be marked complete solely because it compiles or returns JSON. This rule is especially important for persistent VMs, deployment creation, scaling, backups, billing, DNS/TLS, and enterprise tenancy where a superficially successful response can otherwise hide a missing product workflow.


---

## 5. V1 Release Readiness: Additional Missing Work

The feature audit identifies product gaps. This section adds the release-engineering work required before Porter can be honestly released as a working V1. Passing Go tests and the frontend build is not sufficient; the production path must be installable, secure, recoverable, observable, and acceptance-tested on a real Firecracker host.

### 5.1 Freeze the V1 scope

The current design covers a large platform: PaaS deployments, persistent VPS-style VMs, OCI/Docker conversion, Kubernetes-style orchestration, cPanel-like administration, organizations, billing, DNS, Cloudflare integration, and private-server operation. A release candidate needs a written contract separating **V1 required**, **V1 preview**, and **post-V1**.

A credible V1 required vertical slice is:

```text
install Porter on one supported Linux host
  → initialize PostgreSQL and migrations
  → validate KVM/Firecracker/kernel/rootfs/network prerequisites
  → create organization and project
  → create ephemeral deployment or persistent VM
  → reserve capacity and boot MicroVM
  → connect guest agent and pass health/readiness
  → expose a domain or local endpoint
  → view logs/metrics/events
  → stop/restart/recover
  → snapshot and restore
  → enforce tenant/quota boundaries
  → record usage
```

Multi-node scheduling, external registry pulls, payments, custom DNS authority, Cloudflare adapters, advanced analytics, and broad enterprise administration should be marked preview unless their complete flows pass these gates.

### 5.2 Installation and deployment packaging

Before release, Porter needs a supported installation contract for local servers, rented servers, and private MNC environments. Required work includes:

- Publish a supported Linux distribution, kernel, architecture, and `/dev/kvm` matrix.
- Provide a reproducible installer or container/VM deployment package with pinned versions.
- Install and validate Firecracker, kernel, rootfs tooling, TAP/network privileges, cgroups, PostgreSQL, and host utilities.
- Define config/data/log/cert/backup directories and permissions.
- Add first-run initialization for migrations, roles, base images, secrets, and admin account.
- Add forward migrations and documented upgrade/rollback behavior.
- Add backup-before-upgrade and restore procedures.
- Add systemd/service supervision, graceful shutdown, restart policy, and log rotation.
- Verify clean installation, restart, upgrade, and restore on a fresh supported host.

### 5.3 Real Firecracker acceptance environment

The current suite is mainly unit, fake-socket, filesystem, or in-memory coverage. A release requires a privileged acceptance environment with Linux `/dev/kvm`, a pinned Firecracker binary, a valid compatible kernel/rootfs, TAP networking, cgroups, Firecracker privilege isolation, a guest agent/vsock service, PostgreSQL migrations, and real volume/snapshot directories.

Required acceptance tests:

1. Create, boot, health-check, stop, restart, and delete a real VM.
2. Boot a deployment workload and route traffic through the gateway.
3. Boot a persistent VM, write data, restart it, and verify persistence.
4. Create and attach a volume, verify the actual Firecracker drive path, detach/reattach, and verify data.
5. Create two snapshots with different contents and restore either without overwriting the other.
6. Kill Firecracker and verify Degraded/Recovering/restart behavior.
7. Restart Porter and verify desired/runtime state reconciliation.
8. Reject creation when capacity, kernel, rootfs, KVM, or network prerequisites are unavailable.
9. Prove tenant A cannot read or mutate tenant B's projects, VMs, volumes, logs, domains, images, operations, or billing.
10. Prove duplicate requests with the same idempotency key do not create duplicate resources.

### 5.4 Security release gates

Before exposing Porter to real users, complete the following:

- Replace caller-controlled organization/project identifiers with server-side ownership resolution.
- Review every route permission and parent-resource check, including compatibility routes.
- Require authorization, CSRF protection where applicable, audit records, and idempotency semantics for destructive operations.
- Stop returning HTTP 200 for unsupported SSH/exec/runtime operations; return explicit error semantics and visible UI status.
- Validate production secret keys and prevent unsafe startup configuration.
- Ensure secrets never appear in logs, builds, image metadata, events, backups, or errors.
- Implement guest-agent authentication, short-lived SSH certificates, session authorization, command audit, and revocation.
- Enforce Firecracker isolation, path confinement, archive limits, symlink safety, image digest verification, and upload quotas.
- Add rate limits for login, signup, reset, image upload, build, VM creation, exec, and public gateway routes.
- Add session rotation, password policy, optional MFA, scoped API keys, expiry, and revocation.
- Add dependency, Go vulnerability, image, secret, and license scans in CI.
- Define tenant data retention, deletion, export, and privacy behavior.

### 5.5 Data, migration, and correctness gates

- Test migrations from an empty database and from the latest production-like schema.
- Add foreign keys or explicit ownership checks for project/org/VM/volume/domain/backup/build/deployment relationships.
- Add transaction boundaries for creation, deletion, scale, volume attach, snapshot, restore, and billing events.
- Add operation records containing status, progress, errors, retries, timestamps, actor, tenant, and idempotency key.
- Add reconciliation after process restart and partial failure at every creation stage.
- Give artifacts and snapshots immutable identities; never use only VM ID as a recovery filename.
- Add indexes and cursor pagination for every list API.
- Add cleanup jobs for logs, builds, images, snapshots, operations, sessions, and temporary artifacts.
- Add consistency checks for project membership, VM rows, runtime processes, placements, IPs, volumes, and gateway backends.

### 5.6 Observability and operations gates

A V1 operator must be able to determine what happened without reading source code. Required capabilities are structured logs containing request/operation/tenant/project/VM/node IDs; durable audit/events for login, mutations, failures, recovery, billing, and destructive actions; real CPU, memory, disk, network, VM, guest-agent, Firecracker, and host-capacity metrics; alerts for failed boot, degraded VM, node loss, storage exhaustion, certificate expiry, backup failure, capacity exhaustion, and quota denial; readiness endpoints that separate database, KVM, Firecracker, storage, gateway, and controller state; visible operation progress/retry; and runbooks for failed deployments, stuck VMs, lost nodes, bad images, failed restores, expired certificates, full disks, and database recovery.

### 5.7 API and client release gates

- Publish an OpenAPI or equivalent versioned API contract.
- Generate or centrally type the frontend API client.
- Define stable error envelopes, status semantics, pagination, filtering, cursors, and rate-limit headers.
- Add compatibility tests for every critical create/lifecycle route.
- Add browser E2E tests for login, organization/project creation, image selection, deployment, persistent VM, lifecycle controls, logs, volume, snapshot/restore, domain, and billing/usage views.
- Verify every registered mutation route is reachable in the UI or explicitly documented as automation/admin-only.
- Add a check that flags new API routes without a documented workflow, permission, persistence target, operation ID, and acceptance test.

### 5.8 Commercial and support readiness

If Porter will be rented to customers or used by sellers/MNCs, V1 also needs customer/account ownership, plans and entitlements, quota/overage/suspension behavior, meters for VM uptime/CPU/RAM/disk/bandwidth/builds/snapshots/backups/domains/seats, invoice finalization, payment-provider integration, failed-payment handling, credits/refunds/tax/audit, seller or private-provider administration, customer export/deletion, terms/privacy/acceptable-use/abuse/incident procedures, and release/support/status documentation.

### 5.9 V1 release decision

Based on the complete audit, Porter is **not yet V1 release-ready**. The minimum release blockers are:

1. Durable workload and operation reconciliation.
2. Explicit ephemeral versus persistent VM resource model.
3. Real single-node Firecracker/KVM/guest-agent integration tests.
4. Correct volume attachment and immutable snapshot/backup restore.
5. Source-to-healthy deployment completion.
6. Tenant ownership enforcement and security review.
7. Persistent VM workspace with terminal/file/log/lifecycle controls.
8. Real metrics, audit, operation, and failure-recovery visibility.
9. Install/upgrade/backup packaging for a supported clean host.
10. API contract and browser acceptance coverage.

Until these gates pass, the safe label is **Porter development preview / technical foundation**, not a complete production IaaS/PaaS V1.


---

## 6. Delivery Strategy: Working v0.1 First, Full Porter Platform After

The requested delivery has two levels and they should not be confused:

### v0.1 target: a real working proof

The first release should prove that Porter is not only an API prototype. It must run a minimal Firecracker image-based VM and deploy a small GitHub project through the real build/deploy path.

#### v0.1 supported scope

1. Run Porter on one supported Linux/KVM host.
2. Apply PostgreSQL migrations and create the first administrator/organization.
3. Validate Firecracker, KVM, kernel, rootfs, networking, storage, and guest-agent prerequisites.
4. Register or build one known-good minimal Firecracker guest image.
5. Create a VM/project from that image.
6. Allocate a network identity and reserve CPU/RAM capacity.
7. Boot the MicroVM through the production `VMManager`/`FCClient` path.
8. Start the guest agent and prove health/readiness.
9. View VM state, logs, metrics, and events through the API and UI.
10. Stop, restart, and delete the VM without orphaned runtime, database, IP, or volume state.
11. Attach one persistent volume, write test data, restart the VM, and verify the data remains.
12. Create a GitHub deployment from a public or configured private repository.
13. Clone an exact commit, detect a supported Dockerfile or minimal build input, and run the build inside the isolated BuildKit MicroVM path.
14. Produce an immutable rootfs/artifact record and link it to the deployment.
15. Create deployment replicas from the artifact, boot them, run a health check, and expose the healthy service through the gateway.
16. Show GitHub commit, build logs, deployment state, VM state, health, and gateway URL in the UI.
17. Reject failed builds and unhealthy deployments without promoting traffic.
18. Run a rollback or redeploy to the previous known-good artifact.

#### v0.1 deliberately excluded or preview-only

The following should not block the first working proof if clearly labeled preview: multi-node scheduling, automatic node migration, public registry pulls, complex Compose stacks, payment execution, reseller billing, custom certificate automation, Cloudflare provider lifecycle, advanced Kubernetes policies, full cPanel desktop features, and multi-region failover.

#### v0.1 acceptance flow

```text
Fresh supported Linux/KVM host
  → install/configure Porter
  → run migrations and bootstrap admin
  → validate prerequisites
  → register minimal Firecracker image
  → create project/VM
  → reserve capacity and boot VM
  → guest agent health = ready
  → API/UI show running VM
  → attach volume and verify persistence
  → GitHub push or manual commit deploy
  → clone exact commit
  → BuildKit MicroVM build
  → immutable artifact linked to Deployment
  → boot deployment replica
  → health check passes
  → gateway promotes traffic
  → UI shows build/deploy/log/health state
  → stop/restart/redeploy/rollback succeeds
```

The v0.1 release should include a repeatable acceptance script that fails if any stage is skipped. It must use a real PostgreSQL database and real Firecracker/KVM, not only unit fakes or a fake Unix socket.

### Full Porter target after v0.1

After the proof works, the broader roadmap continues toward the complete unified platform:

- Persistent user-managed VM mode sharing the same MicroVM lifecycle as deployment workloads.
- Custom browser VM workspace with terminal, SSH, file manager, upload/download, logs, networking, snapshots, and lifecycle controls.
- Durable workload reconciler with desired/observed state, conditions, generations, idempotency, operations, placement, recovery, and autostop.
- Multi-node scheduler with capacity reservations, node admission, regions, zones, pools, taints, drain, replacement, and migration.
- GitHub/GitLab/provider integrations, repository selection, webhooks, branch previews, PR events, build cancellation, retries, and deployment promotion.
- Dockerfile, Compose, OCI layout, Docker-save, and registry-reference workflows with platform selection, digests, credentials, security scanning, and provenance.
- ObjectStore-backed snapshots/backups with encryption, checksums, immutable versions, retention, restore verification, and cross-node recovery.
- Domains, DNS, certificates, ACME HTTP-01/DNS-01, wildcard certificates, gateway policy, and optional Cloudflare integration.
- Kubernetes-style replicas, autoscaling, disruption budgets, health/readiness, rolling updates, topology spreading, and idle autostop/wake.
- Frappe-style organizations, teams, invitations, tenant-owned roles, service identities, audit, customer accounts, plans, entitlements, usage, invoices, payments, credits, refunds, and seller/private-provider administration.
- Local-server, rented-server, and MNC/private-cloud installation and provider/node management.
- Complete UI coverage, generated API contracts, browser E2E tests, install/upgrade tooling, observability, security scanning, and operational runbooks.

### Implementation order for the working proof

#### Phase A — Runtime proof

Create one canonical v0.1 workload model and operation path. Remove direct API-only VM mutations from the proof path. Persist desired state, operation state, VM state, node/placement, IP, and volume attachment. Add real host prerequisite validation and production-path Firecracker integration tests.

#### Phase B — Minimal guest image

Create a reproducible minimal guest image containing the required kernel/rootfs layout, init process, networking, and Porter guest agent. Pin the image and kernel checksums. Validate boot, agent registration, health, logs, and graceful shutdown on the target host.

#### Phase C — GitHub build/deploy proof

Fix the deployment/build linkage so `Deployment.BuildID` is authoritative. When a build succeeds, persist the immutable artifact, create deployment replicas, wait for health, and only then update gateway traffic. Add a real GitHub webhook/manual trigger acceptance path and failed-build/unhealthy rollback tests.

#### Phase D — v0.1 UI and release package

Expose only the proven flow in the UI: image/project creation, VM status, start/stop/restart, volume, GitHub source, build logs, deployment status, health, gateway URL, and rollback. Package the installer/configuration, acceptance script, supported-host guide, troubleshooting guide, and release notes.

### v0.1 release decision rule

Porter can be labeled **v0.1 working preview** only when the same documented acceptance script passes on a clean supported Firecracker/KVM host for both:

1. a minimal image-based VM lifecycle; and
2. a GitHub source-to-healthy deployment lifecycle.

The current repository has many of the required primitives, but this end-to-end proof has not yet been demonstrated in this sandbox. The full platform audit remains the source of truth for the work after v0.1.


---

## 7. Direct Firecracker Runtime Clarification

Porter does **not** need Docker to run customer MicroVMs. Docker/OCI is only an optional **input/build compatibility format**. The customer workload runtime is Firecracker directly:

```text
Dockerfile / OCI / Compose / Git source (optional input)
  → isolated build/conversion step
  → rootfs.ext4 + compatible kernel
  → direct Firecracker process
  → guest agent/vsock
  → Porter networking, storage, gateway, logs, and billing
```

A Docker daemon must not sit in the runtime path for pure MicroVM workloads.

### Jailer is optional, not Docker

Firecracker's Jailer is a companion process for constraining a Firecracker process. It is not a container engine and it does not turn the workload into a Docker container. Jailer commonly helps with:

- dropping privileges to a dedicated UID/GID;
- entering a per-VM chroot or controlled working directory;
- applying cgroups and resource boundaries;
- reducing filesystem visibility;
- applying process/resource limits and a narrower execution environment;
- making the Firecracker process boundary easier to supervise.

Porter may either:

1. use Jailer with direct Firecracker; or
2. run Firecracker directly and implement an equivalent Porter supervisor/security boundary.

For the requested pure-MicroVM architecture, option 2 is valid, but it must not mean running Firecracker as an unrestricted root process. The direct mode must provide, at minimum:

- one isolated Unix API socket and runtime directory per VM;
- a dedicated VM process identity or equivalent privilege drop;
- no host filesystem access except explicitly staged kernel/rootfs/volume/socket paths;
- cgroup v2 CPU, memory, process, and I/O limits;
- seccomp and capability minimization where supported by the host setup;
- network namespace/TAP ownership and controlled bridge/firewall rules;
- read-only or validated kernel/rootfs inputs and safe volume paths;
- bounded image/archive extraction before runtime;
- PID/process-exit supervision and cleanup of sockets, TAP devices, IP allocations, mounts, and database rows;
- authenticated guest-agent/vsock communication;
- audit records for exec, console, SSH, file, network, and lifecycle actions.

Therefore the v0.1 acceptance requirement is updated from “Firecracker plus Jailer” to:

> **Firecracker plus Jailer or an equivalently isolated Porter runtime supervisor.**

Docker is not a runtime prerequisite for v0.1. Docker/Podman is only needed if the chosen build-input tests use Dockerfiles or Docker-save archives; the production MicroVM boot, lifecycle, networking, storage, and guest execution path remains direct Firecracker.


---

## 8. Implemented Direct-Firecracker Hardening and Conversion Package Slice

This slice adds the requested separation between pure MicroVM runtime and Docker/OCI compatibility inputs.

### Direct Firecracker Jailer support

- Added `internal/jailer`, a typed Go package that validates VM IDs, absolute API socket paths, Firecracker binary paths, and cgroup version before constructing Jailer arguments.
- Integrated the package into the production runtime's `jailArgs` builder.
- Preserved direct Firecracker mode when Jailer is disabled or no Jailer binary is configured.
- Added unit tests for valid arguments and unsafe input rejection.
- Jailer remains optional; the direct mode is still the Porter runtime model, while production multi-tenant deployments can enable Jailer or an equivalent supervisor.

### Standalone Docker/OCI/Compose conversion package

- Added `cmd/porter-convert`, a separate buildable CLI release:
  - `-mode oci`: OCI-layout or Docker-save archive to Firecracker `rootfs.ext4`;
  - `-mode dockerfile`: Dockerfile to OCI through `buildctl`, then ext4 conversion;
  - `-mode compose`: supported Compose YAML to a deterministic MicroVM service/dependency/network plan.
- The converter never invokes Docker or Podman and does not require a host container daemon.
- Dockerfile and Compose are compatibility input formats only. BuildKit is intended to run inside its own Firecracker builder MicroVM; customer workloads run as direct Firecracker MicroVMs.
- Added `conversion/README.md`, updated `builder/README.md`, and changed `scripts/build-builder-image.sh`/Makefile to accept a supplied BuildKit OCI archive instead of building through Docker/Podman.
- Added `make convert` and `make jailer-test` targets.

### Verification

The new slice was formatted and verified with:

```text
go test ./internal/jailer ./internal/runtime ./internal/buildkit ./internal/compose
 go build -trimpath -o /tmp/porter-convert ./cmd/porter-convert
 go build -trimpath -o /tmp/porter-builder-image ./cmd/porter-builder-image
 bash -n scripts/build-builder-image.sh
```

This is a verified code/package slice, not yet a real KVM acceptance run; the current sandbox has no `/dev/kvm`, Firecracker binary, or Jailer binary.
