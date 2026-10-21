# Porter V1 Software Requirements Specification

**Status:** draft for V1.0.0  
**Source of truth for UI intent:** [`UI_SPEC_V1.md`](UI_SPEC_V1.md)  
**Architecture:** [`ARCHITECTURE.md`](ARCHITECTURE.md)  
**Delivery plan:** [`BACKEND_V1_PLAN.md`](BACKEND_V1_PLAN.md)

---

## 1. Product definition

Porter is a self-hosted Firecracker control plane with a Vercel-style application
workflow and a lightweight fleet of Kubernetes-inspired node agents.

```text
Project
  -> Deployment (immutable release)
    -> Preview environment (optional, PR-scoped, ephemeral)
    -> Replica group
      -> Replica
        -> MicroVM
```

The primary UX is:

```text
Connect source -> review build plan -> deploy -> preview -> promote -> scale
```

The primary infrastructure UX is:

```text
Enroll node -> fleet appears -> scheduler places replicas -> observe/repair
```

## 2. V1 scope and priority labels

Priority labels:

- `P0` — required for V1.0.0
- `P1` — required for a complete V1 if time permits
- `P2` — prototype/visible but explicitly preview
- `LATER` — not part of V1; must not block core

### In scope

| Area | Priority |
|---|---|
| Admin login + API token | P0 |
| Fleet one-command join | P0 |
| Fleet SQLite local state | P0 |
| Nodes, heartbeat, cordon/drain | P0 |
| Projects, apps, deployments, replicas, microVMs | P0 |
| Deploy from OCI/rootfs image | P0 |
| Git source + build plan detection | P1 |
| Preview environment per pull request with unique URL | P0 |
| Deployment promotion/rollback | P0 |
| Replica scale up/down | P0 |
| Logs | P0 |
| Basic metrics | P1 |
| Domains + gateway routes + TLS | P1 |
| Persistent volumes + snapshots | P1 |
| Env vars + secrets | P1 |
| Canary traffic split | P1 |
| A/B test | P2 |
| Autoscaling min/max/CPU policy | P1 |
| Fleet doctor + node detail | P1 |
| Project/replica doctor | P1 |
| Observability dashboard | P1 |
| AI operator | LATER |
| Workflows/automation | LATER |
| Customers/teams/resellers | LATER |
| Billing/subscriptions/invoices | LATER |
| Full audit/compliance product | LATER |

### Explicit non-goals

- Kubernetes API compatibility
- Kubernetes Pods/Deployments/Services/Ingress objects
- CRDs, CNI, CSI, kubelet, kube-proxy
- Docker-as-runtime
- Pass-through customer shell or unrestricted AI shell
- Customers, organizations, teams, billing in V1

## 3. Roles

V1 has three principals:

1. **Admin/operator** — browser/CLI user; full control plane access.
2. **Fleet node** — machine identity authenticated with mTLS.
3. **Automation token** — scoped API token for CLI/CI.

No customer/team/reseller RBAC in V1.

## 4. Global navigation requirements

The UI SHALL expose:

- Projects
- Fleet
- Observability
- Doctor
- Images
- Operations/events
- Settings

The following navigation items MAY be visible only as `LATER` placeholders and
MUST NOT expose fake success:

- AI Operator
- Customers
- Billing
- Marketplace

Global behaviors:

- command palette (`Ctrl/Cmd+K`)
- recent events dropdown
- deploy button
- enroll node button
- toasts
- operation progress drawer
- current org/workspace label may be hidden in V1

## 5. Projects

### 5.1 Project list

Each project row/card SHALL show:

- name
- status (`running`, `degraded`, `stopped`, `failed`)
- active replicas
- current deployment
- latest preview count
- domain / primary URL
- last activity

Project actions:

- create project
- open project
- delete project (destructive confirmation)

### 5.2 Project detail layout

Project navigation tabs:

1. Overview
2. Replica groups
3. Deployments
4. Traffic & Scaling
5. Config & Storage
6. Logs
7. Metrics & Usage
8. Activity
9. Settings

Preview UI SHALL be deployment-scoped, not a separate top-level control plane.
A preview is an ephemeral MicroVM attached to the deployment it validates.

## 6. Project overview

The overview SHALL show:

- active replicas
- deployment count/state
- request rate + p99
- storage attachment state
- SLO/error budget (P1)
- project actions:
  - Redeploy (new release)
  - Restart (in-place reboot)
  - Add Replica
  - Configure Scaling
  - Delete Project
- release-flow visualization:
  - PR preview MicroVM
  - Deployment
  - Replica group
  - Autoscale policy

The UI MUST explain the difference between:

- **Project** — logical application
- **Deployment** — immutable release
- **Replica** — one running instance/MicroVM

## 7. Deployments

Deployment requirements:

- immutable: image digest + config + commit
- status: `queued`, `building`, `deploying`, `ready`, `failed`, `rolled_back`
- step-level progress, never a generic spinner
- expandable row showing attached replicas
- attached preview environment for a PR:
  - preview URL
  - commit
  - status
  - preview MicroVM count
  - open-preview action
  - comments + toolbar for preview deployments
- actions:
  - promote
  - rollback
  - redeploy
  - freeze
- deploy wizard:
  1. source (Git repo/branch or image)
  2. detected framework/Dockerfile/build plan
  3. base image, port, vCPU, memory
  4. review
  5. deploy
- preview lifecycle:
  - created from PR
  - unique URL
  - ephemeral
  - deleted on PR close/merge or TTL
  - never represented as production

## 8. Replica groups and scaling

- A deployment owns a replica group.
- A replica is one Firecracker MicroVM.
- Primary replica is protected from direct removal.
- Scale up/down changes desired count immediately.
- Scale down removes from tail; primary stays.
- Autoscaling policy defines min/max/CPU trigger and optional idle suspend.

Required operations:

```text
scale_up
scale_down
restart
pause
resume
snapshot
restore
delete_secondary_replica
cordon/drain at node level
```

## 9. Traffic and scaling

Tabs required:

### Canary
- stable version
- canary version
- weight slider
- promote to 100%
- abort

### A/B test (P2)
- variant A/B selectors
- traffic split
- start/conclude

### Autoscaling
- min replicas
- max replicas
- CPU trigger
- idle suspend policy
- save policy

### Domains and load balancing
- load-balancing algorithm
- health path
- TLS/ACME state
- certificate list + expiry + renew
- DNS records table (type/name/value/TTL/status)
- add domain + CNAME verification

## 10. Config and storage

### Volumes
- list volumes
- attach/detach
- mount path
- filesystem
- size
- persistence flag
- snapshots (restore/delete)

### Environment and secrets
- env var list
- secret values masked
- environment scope
- copy env for local dev
- secrets encrypted at rest

## 11. Logs, metrics, traces

### Project logs
- live stream
- search/filter
- no matching state

### Metrics and usage
- Core Web Vitals (LCP, INP, CLS, FCP, TTFB)
- request rate chart
- boot-to-ready latency
- retention label

### Replica metrics
- CPU
- memory + ballooning
- block I/O
- request latency
- cost estimate (P2, non-billing)
- uptime

### Network trace waterfall
- TAP name
- MAC
- eBPF forwarding state
- MTU
- edge gateway -> bridge -> guest -> app timings

### Console
- serial log stream
- interactive exec only when guest-vsock agent exists
- MUST label read-only preview honestly

## 12. Fleet and nodes

### Fleet list
- online percentage
- total vCPUs, RAM, MicroVMs
- region filter
- status
- IP
- disk/memory/CPU pressure
- inspect node

### Fleet doctor
- platform-wide diagnostics
- verified / preview / not-implemented legend
- operation inbox

### Node detail tabs
1. Overview & actions
2. Live metrics
3. Hosted MicroVMs
4. Network
5. Doctor
6. Logs
7. Agent config

Node actions:

- restart agent
- cordon
- drain
- migrate VMs (P2 preview)
- inspect hardware
- copy enrollment command

Node info:

- hostname, IP, region, role, uptime
- kernel, Firecracker version, jailer
- cgroups, bridges, agent version
- heartbeat RTT
- KVM device
- CPU/RAM/disk
- network interfaces, egress/NAT/eBPF
- scheduled VMs

### Enroll modal
- hostname
- IP
- region
- one-line install command

## 13. Platform admin

P1 admin surfaces:

- control-plane overview
- networks and gateway
- storage and backups
- doctor

LATER surfaces:

- customers/teams
- billing
- security/IAM
- automation/workflows
- AI operator

Platform overview SHALL show:

- healthy workloads
- fleet capacity
- requests/hour
- recent operations
- policy posture (only when implemented)

## 14. Networking and domains

- gateway routes resolve to a deployment, never directly to a node
- gateway status + route count + errors
- IPAM usage
- TLS certificate validity
- egress/NAT mode
- add route
- domain verification via CNAME
- TLS automatic via ACME

## 15. Storage and backups

- ephemeral rootfs / scratch
- persistent block volumes
- object storage (P2)
- backup copies (P1)
- storage classes
- latest backup state
- RPO target
- attach volume

## 16. Doctor and honest status

Every feature MUST display one of:

- Verified
- Preview
- Not Implemented
- Planned

The UI MUST NOT pretend a preview/planned feature is production-ready.

Doctor must work without AI and return:

```text
check
status
severity
observed
expected
cause
action
evidence
```

## 17. Operations and events

All mutation actions SHALL create an operation:

```text
queued -> running -> succeeded | failed | cancelled
```

The UI SHALL show:

- operation ID
- actor
- target resource
- progress
- result/error
- retry/cancel where safe

Operational events are not the deferred compliance-audit product.

## 18. Preview deployments

Preview requirements:

- tied to a PR and a deployment
- unique hostname, e.g. `app-pr142.porter.app`
- one or more ephemeral MicroVMs
- comments/toolbar enabled
- deployment protection options:
  - Vercel-style authentication
  - password protection
  - trusted IP allowlist
- open-preview action
- automatic cleanup on merge/close or TTL

## 19. Settings

Project settings:

- deployment protection
- build/deploy framework detection
- runtime version selection
- delete project

Node settings:

- agent config
- cordon/drain
- restart agent

## 20. Backend resource contract

Every major resource SHALL expose:

```text
id
metadata
spec
status
conditions
generation
observed_generation
created_at
updated_at
```

Resources:

```text
nodes
projects
apps
deployments
previews
replicas
microvms
volumes
snapshots
domains
routes
certificates
images
operations
events
```

## 21. Invariants

1. Firecracker is the runtime boundary.
2. OCI/Docker is an artifact format, never the runtime.
3. Porter SQLite is global desired state.
4. Fleet SQLite is local actual state.
5. Fleet connects outbound over mTLS WSS.
6. WireGuard is optional and never the only control path.
7. All commands are idempotent with operation IDs.
8. Previews are deployment-scoped and ephemeral.
9. Kubernetes-like scaling only; no Kubernetes compatibility.
10. No customers/billing/compliance audit in V1.
11. UI, CLI, and automation use the same API.
12. The UI must show honest implementation status.

## 22. Fleet component split

Porter SHALL ship two independently versioned binaries:

| Binary | Role |
|---|---|
| `porter` | global desired state, API/UI, scheduler, embedded fleet manager |
| `porter-porter-agent` | per-node Firecracker executor with local SQLite |

Requirements:

- The fleet manager is a logical package inside Porter, not a separate binary.
- Porter agents connect outbound only to Porter.
- Porter exposes enrollment and the agent WebSocket endpoint.
- Each binary exposes its version.
- Porter and agents can be upgraded independently.
- Porter supports protocol v1 and one previous minor agent version.
- Agent upgrades are signed, staged, health-checked, and rollback-capable.
- Per-VM systemd units keep workloads alive across agent restarts.



## 23. Deployment topology

V1.0.0 SHALL run Porter as an unprivileged host-installed Go service.

The first host SHALL run:

- `porter` as a systemd service
- `porter-agent` as the privileged host executor

Additional hosts SHALL run only `porter-agent`.

Porter has no KVM, Firecracker socket, TAP, or host root filesystem access.
Running Porter inside a MicroVM is deferred and MUST NOT block V1.

## 24. Image sources and build pipeline

Porter SHALL use OCI image digests as the canonical deployable artifact.

All supported sources SHALL normalize to OCI or a direct MicroVM image:

- direct `vmlinux + rootfs.ext4`
- Dockerfile -> BuildKit -> OCI
- docker-compose primary service -> OCI
- OCI image -> rootfs.ext4
- Docker image -> OCI -> rootfs.ext4
- GitHub repo -> framework detection -> generated Dockerfile -> OCI

Docker Compose SHALL NOT be the runtime. It is only a build/orchestration input.
V1 supports a single primary service from Compose. Full multi-service stacks are P1.

Framework detection SHALL be explicit and SHALL support at least Laravel/PHP,
React/Vite, Next.js, generic Node/Express/Nest, and static sites. If uncertain,
Porter SHALL ask for a Dockerfile or framework choice rather than guessing.

See [`IMAGE_BUILD_PIPELINE.md`](IMAGE_BUILD_PIPELINE.md).

## 25. Addressing policy

Public surfaces SHALL use domains, preview URLs, slugs, and resource IDs only.

Public API/MCP/UI/CLI/logs MUST NOT expose VM IPs, ports, TAP names, or
Firecracker sockets. Internal private IP/port usage is an implementation detail.
