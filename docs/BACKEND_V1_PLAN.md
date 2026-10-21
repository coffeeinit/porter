# Porter V1.0.0 Backend Plan

**Goal:** ship a simple, reliable Firecracker control plane with one-click fleet expansion.

**Shape:** two independently versioned Go projects plus a shared protocol package.

```text
porter/         control plane + API/UI + scheduler + embedded fleet manager
porter-agent/    per-node Firecracker executor + local SQLite
protocol/       shared wire contract
```

## Non-goals for V1.0.0

Do not build these yet:

- customers / organizations / teams / resellers
- products / plans / subscriptions / invoices / payments / usage billing
- marketplace / extensions / plugin SDK
- AI operator / MCP
- audit/compliance product
- Kubernetes API compatibility
- multi-region provider orchestration
- HA/distributed control plane

Keep an `events` table for operational history only.

## Phase 0 — Repository foundation

Deliverables:

- `porter/` and `porter-agent/` as separate Go modules.
- `protocol/` shared module.
- Root Makefile with `make porter`, `make porter-agent`, `make test`.
- Dockerfiles for both binaries.
- `install-agent.sh` one-line installer.
- `README.md` and docs.

Acceptance:

- `go build ./...` works in all modules.
- No dependency on legacy code.

## Phase 1 — Control-plane core

Build in `porter/`:

- SQLite schema and migrations
- single admin user + API token
- project/app/deployment/replica CRUD
- image catalog CRUD
- operation ledger
- event stream
- REST API
- auth middleware for admin and node tokens

Minimal API:

```text
GET  /healthz
POST /v1/auth/login
GET  /v1/projects
POST /v1/projects
GET  /v1/projects/{id}/apps
POST /v1/projects/{id}/apps
POST /v1/apps/{id}/deployments
GET  /v1/deployments/{id}
POST /v1/deployments/{id}/rollback
GET  /v1/replicas
POST /v1/replicas/{id}/scale
GET  /v1/fleet/nodes
POST /v1/fleet/join-tokens
POST /v1/fleet/enroll
GET  /v1/fleet/stream
GET  /v1/operations/{id}
GET  /v1/events
```

Acceptance:

- CRUD persists across restart.
- Every mutating action creates an operation.
- Unauthorized requests fail closed.

## Phase 2 — Fleet enrollment and heartbeat

Deferred beyond V1.0.0. V1 runs Porter as a host service. Keep the agent
`AgentClient` transport abstraction so this mode can be added later.

- `porter-agent join --server ... --token ...`
- local Ed25519 key generation
- enrollment request
- certificate storage under `/var/lib/porter-agent/`
- mTLS WebSocket client with reconnect/backoff
- hello + heartbeat
- host capacity detection (`/proc/cpuinfo`, `/proc/meminfo`, `statfs`, KVM check)

Build in `porter/`:

- join-token creation and hashing
- enrollment validation
- node registry
- stale node detector
- node UI list

Acceptance:

- One command adds a node.
- Node appears online within 5 seconds.
- Killing/restarting the agent reconnects automatically.
- Expired/used/replayed token fails with a clear error.

## Phase 3 — Firecracker runtime on the agent

Deferred beyond V1.0.0. V1 runs Porter as a host service. Keep the agent
`AgentClient` transport abstraction so this mode can be added later.

- host preflight: `/dev/kvm`, `firecracker`, kernel/rootfs availability
- Firecracker API client (Unix socket)
- VM create/configure/start/stop/delete
- TAP device and bridge setup
- NAT/iptables rules
- local VM state directory
- serial log capture
- observed-state reporting

Acceptance:

- Porter can create a VM on a joined node.
- VM boots kernel and appears `running`.
- VM stop/delete works and is idempotent.
- Agent restart does not orphan running VMs.

## Phase 4 — Scheduler and reconciliation

Build in `porter/`:

- scheduler: filter ready nodes, score free resources, persist placement
- NodeController
- ReplicaController
- DeploymentController
- OperationController
- stale-node handling and rescheduling

Acceptance:

- Scale app 1 -> 3 sends commands to nodes.
- Cordoned node gets no new placements.
- Node goes offline -> replicas marked failed and rescheduled if capacity exists.
- Duplicate commands do not create duplicate VMs.

## Phase 5 — Deploy path

Minimum V1 path:

```text
image reference or OCI upload
  -> digest
  -> rootfs.ext4 + kernel
  -> image catalog
  -> deployment
  -> replica
  -> scheduler
  -> fleet
  -> Firecracker
```

Git build can be Phase 5B if time-constrained.

Acceptance:

- Deploy a prebuilt OCI/rootfs image.
- Deployment shows exact stage/status.
- Rollback points replicas to previous image digest.
- Failed boot surfaces explicit error; no fake success.

## Phase 6 — Networking and gateway

- project network allocation
- per-VM IP/MAC
- host firewall/NAT rules
- Porter gateway routes by host header to healthy replicas
- domain + TLS later if it does not slow the core

Acceptance:

- Public HTTP request reaches a microVM.
- Unhealthy replica removed from rotation.
- Domain route survives replica restart.

## Phase 7 — Admin UI

Use one clean embedded UI, not two prototypes.

Core screens:

1. Login
2. Overview
3. Fleet
4. Fleet node detail
5. Projects
6. Project detail
7. Deployment detail
8. Replica detail
9. Images
10. Operations/events
11. Settings

No customer/billing/marketplace navigation in V1.

Acceptance:

- Every action calls the real API.
- UI shows operation progress rather than fake instant success.
- No dead buttons.

## Phase 8 — Hardening / v1.0.0

- request IDs and structured errors
- graceful shutdown
- API pagination
- SQLite backup/restore command
- rate limits on login/enroll
- mTLS certificate rotation
- upgrade path for `porter` and `porter-fleet`
- end-to-end test from empty DB -> joined node -> running microVM

Release gate:

```text
fresh Porter starts
  -> admin logs in
  -> generates join token
  -> fresh fleet node joins
  -> image registered
  -> project/app/deployment created
  -> scheduler places replica
  -> fleet boots Firecracker VM
  -> VM reports healthy
  -> gateway serves traffic
  -> restart Porter and fleet
  -> state reconciles
```

## Suggested implementation order

1. Protocol types
2. Porter SQLite schema + API shell
3. Fleet join + heartbeat
4. Porter node dashboard
5. Fleet Firecracker lifecycle
6. Porter scheduler + controllers
7. Deploy from image
8. Admin UI
9. Gateway
10. Hardening

## Team split

If worked in parallel:

- **Porter track:** API, SQLite, scheduler, controllers, UI.
- **Fleet track:** enrollment, host network/storage, Firecracker, local reconciler.
- **Protocol track:** envelope/types first, then expand with versioning.

## Success definition

A new operator can:

1. start one Porter binary,
2. join a fresh KVM node with one command,
3. deploy an app,
4. see a real Firecracker microVM serving traffic,
5. add a second node with one command and scale across both.

That is the V1.0.0 finish line.

## Fleet local SQLite decision

Fleet must have its own SQLite database. Firecracker has no daemon that can
enumerate VMs with complete configuration, so process scanning alone is not
sufficient.

Layers:

| Database | Location | Purpose |
|---|---|---|
| Porter SQLite | control plane | global desired state, users, projects, deployments, scheduler, node registry |
| Fleet SQLite | each node | local actual state, VM runtime metadata, volumes, snapshots, host operations |

Fleet local DB uses:

- `modernc.org/sqlite` — pure Go, no CGO
- WAL mode
- one writer goroutine
- bounded log/metric retention

Detailed schema: [`FLEET_STATE.md`](FLEET_STATE.md).

## Connectivity decision

Required baseline:

```text
Fleet -> outbound mTLS WebSocket :443 -> Porter
```

Optional overlay:

```text
Fleet nodes <-> WireGuard hub on Porter
```

Use the official Go packages:

- `golang.zx2c4.com/wireguard/wgctrl` for kernel WireGuard
- `golang.zx2c4.com/wireguard/device` for userspace fallback
- optionally `github.com/quic-go/quic-go` for a future QUIC control path

WireGuard is not required for the control channel and must not be presented as
universal NAT traversal. See [`CONNECTIVITY.md`](CONNECTIVITY.md).

## Kubernetes-inspired scope

Only these Kubernetes-like features are in V1:

- node registration and heartbeat
- capacity-aware scheduling
- desired replica count
- reconciliation
- cordon/drain
- rolling update / restart

Not in scope:

- Kubernetes API compatibility
- Pods, Deployments, Services, Ingress
- CRDs/operators
- CNI/CSI
- kubelet/kube-proxy
- Kubernetes RBAC

## Revised phase details

### Phase 2 additions — Fleet local DB

- open/create `fleet.db`
- store node identity and cert metadata
- persist every applied VM spec
- persist observed runtime state
- reconcile SQLite inventory with live Firecracker sockets on restart
- report orphaned VMs, never delete automatically

### Phase 3 additions — WireGuard overlay

- optional `[overlay] enabled`, `port`, `cidr` config
- Porter hub peer allocation
- Fleet peer config on enrollment
- kernel WireGuard via `wgctrl`, userspace fallback
- overlay health does not block WSS control path

## Dependency policy

Keep the dependency set small:

- HTTP/router: standard library
- SQLite: `modernc.org/sqlite`
- WebSocket: one maintained Go WebSocket library
- mTLS/certs: standard library
- WireGuard: official `golang.zx2c4.com/wireguard`
- charts/UI: frontend-only, not backend dependencies
- no Kubernetes client-go in V1

## Revised success definition

```text
fresh Porter starts
  -> admin logs in
  -> join token generated
  -> fresh KVM node joins using one command
  -> fleet creates its local SQLite DB
  -> fleet connects outbound over mTLS WSS
  -> image is registered
  -> project/app/deployment created
  -> scheduler places replica
  -> fleet boots Firecracker VM
  -> fleet records actual state locally
  -> Porter observes running state
  -> gateway serves traffic
  -> Porter and fleet restart
  -> both reconcile from their own durable state
```

## UI-driven backend additions (from prototype review)

The refined prototype adds Vercel-style flows that the first plan did not fully
capture. These are now part of V1:

### Resource additions

```text
previews       -- PR-scoped ephemeral deployment environments
domains        -- custom hostnames
routes         -- gateway hostname -> deployment mapping
certificates   -- TLS/ACME state
snapshots      -- volume/VM recovery points
env_vars       -- per-app/project environment
secrets        -- encrypted values
operations     -- already planned, now UI-visible
events         -- operational history, not compliance audit
```

### API additions

```text
GET/POST /v1/projects/{id}/previews
GET/DELETE /v1/previews/{id}
POST /v1/previews/{id}/redeploy
POST /v1/deployments/{id}/promote
POST /v1/deployments/{id}/rollback
POST /v1/deployments/{id}/traffic-split
POST /v1/deployments/{id}/abort-canary
GET/PUT  /v1/apps/{id}/autoscale-policy
GET/POST /v1/projects/{id}/domains
POST /v1/domains/{id}/verify
GET/POST /v1/projects/{id}/volumes
POST /v1/volumes/{id}/attach
POST /v1/volumes/{id}/detach
GET/POST /v1/projects/{id}/env
GET/POST /v1/projects/{id}/secrets
GET/POST /v1/images
GET/POST /v1/fleet/nodes
POST /v1/fleet/join-tokens
GET /v1/operations
GET /v1/events
```

### UI contract rule

Every UI action must map to a real API call and a durable operation. No button
may display a fake success. Features not implemented in V1 must be labelled
`Preview`, `Not Implemented`, or `Planned`.

See [`SRS_V1.md`](SRS_V1.md) and [`UI_SPEC_V1.md`](UI_SPEC_V1.md).





## Phase 3.5 — Deferred: control plane as a MicroVM

Deferred beyond V1.0.0. V1 runs Porter as a host service. Keep the agent
`AgentClient` transport abstraction so this mode can be added later.

- bootstrap mode when no Porter URL is configured
- create persistent control-plane data volume under
  `/var/lib/porter-agent/control-plane/`
- start `porter-control-plane` MicroVM from signed image
- wait for `/healthz`
- local bootstrap registration as first fleet node
- host port forward `:80/:443 -> Porter VM`
- restart/reconnect supervision
- health-gated control-plane update with rollback

Acceptance:

- Fresh host installs only `porter-agent`.
- Porter appears inside a MicroVM.
- Dashboard/API works through the agent's host port forward.
- Restarting the agent does not kill Porter or workload VMs.
- Restarting Porter keeps its SQLite data volume.


## Image/build pipeline update

Canonical artifact = OCI digest. Source adapters:

```text
microvm image  -> direct kernel + rootfs
Dockerfile     -> BuildKit -> OCI -> rootfs
compose        -> primary service -> OCI -> rootfs
OCI image      -> unpack -> rootfs
Docker image   -> OCI -> rootfs
GitHub repo    -> detect framework -> Dockerfile -> OCI -> rootfs
```

V1 framework detection: Laravel/PHP, React/Vite, Next.js, Node/Express/Nest,
static HTML. Compose is only a build input; it is never the runtime.

See [`IMAGE_BUILD_PIPELINE.md`](IMAGE_BUILD_PIPELINE.md).
