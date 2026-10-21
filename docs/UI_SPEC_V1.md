# Porter V1 UI Specification & Feature Inventory

**Source:** extracted from `legacy/prototypes/porter-control-plane-refined(1).html`
(the superset; `porternew.html` is the earlier smaller draft).

Priority: `P0` V1 required, `P1` next, `P2` preview, `LATER` planned.

## 1. Global shell

Prototype elements:

- Porter logo / workspace breadcrumb
- top-level nav: Projects, Fleet, Observability, AI Operator
- Recent Events dropdown
- Search / `Ctrl/Cmd+K` command palette
- `+ Enroll` and `Deploy` buttons
- runtime mode toggle (`mock` / `firecracker`)
- demo banner
- operator sidebar:
  - Workspace / `porter-control-plane`
  - Platform: Overview, Projects, Fleet, Observability, AI Operator
  - Infrastructure: Networks & Gateway, Storage & Backups, Security & IAM,
    Automation, Porter Doctor
  - Business: Customers & Teams, Usage & Billing, Audit & Events
  - control-plane status card: API/DB latency, scheduler synced
- bottom-right preview toolbar: Comments, Edit Mode, Draft Mode, Feature Flags
- toasts, command palette, keyboard shortcuts modal

V1 mapping: keep Projects, Fleet, Observability, Doctor, Images, Operations.
Hide or mark `LATER`: AI Operator, Customers, Billing, Security/IAM, Automation.

## 2. Projects list

Prototype: project rows/cards, state badge, replica count, deployment count,
primary domain, open/create/delete project.

V1 fields:

```text
id, name, slug, status, replicas_desired, replicas_running,
latest_deployment_id, primary_domain, preview_count,
created_at, updated_at
```

Actions: `GET/POST /v1/projects`, `GET/DELETE /v1/projects/{id}`.

## 3. Project detail — global actions

Prototype action bar: Deploy/Redeploy, Restart, Add Replica, Configure Scaling,
Delete Project, Danger Zone warning.

Release flow:

```text
1 PR preview MicroVM
2 Deployment
3 Replica group
4 Autoscale policy
```

Project tabs:

1. Overview
2. Replica groups
3. Deployments
4. Traffic & Scaling
5. Config & Storage
6. Logs
7. Metrics & Usage
8. Activity
9. Settings

## 4. Project overview

Widgets: Active Replicas, Deployments, Request Rate + p99, Storage,
SLO & Error Budget (healthy, uptime, target, error budget, burn rate),
project actions, release flow.

Help text: Add Replica adds MicroVMs immediately; autoscaling is the reusable
min/max policy deciding when to do that automatically.

V1: replicas running/desired, latest deployment, requests/latency if available,
storage summary, honest disabled states for unimplemented actions.

## 5. Replica groups

- deployments vs replicas explanation
- groups by deployment
- primary marker
- replica cards
- replica detail navigation
- desired/running/healthy counts
- version/commit
- scale group / restart group

## 6. Deployments

Prototype table: ID, Commit, Duration, Replicas, State, Action.
Expandable row shows attached runtime and serving replicas. Actions: Rollback,
Redeploy, Promote. Archived deployment shows "no active replicas".

Preview environment example:

```text
edge-api-pr142.porter.app - commit 7f3c8b2
READY - 1 preview MicroVM - comments + toolbar enabled - Open preview
```

Statuses: `queued, building, deploying, ready, failed, rolled_back, archived`.

## 7. Preview deployments

A preview is an ephemeral MicroVM created from a pull request and attached to
the deployment it validates. It is not a second production control plane.

Fields:

```text
id, deployment_id, project_id, pull_request, branch, commit,
status, preview_url, replica_count, ttl, protection_mode
```

Actions: open, redeploy, delete, comment, toolbar/feature flags.
Protection: Vercel-style auth, password, trusted IP allowlist.

V1: preview URL + ephemeral MicroVM + cleanup (P0); protection (P1);
comments/toolbar full flow (LATER).

## 8. Traffic & Scaling

### Canary

- stable version
- canary target version
- weight slider (1%..100%)
- apply
- promote to 100%
- abort
- health status

API concept:

```text
POST /v1/deployments/{id}/traffic-split {canary_weight}
POST /v1/deployments/{id}/promote
POST /v1/deployments/{id}/abort-canary
```

### A/B test (`P2`)

- variant A/B selectors
- split B
- start/conclude

### Autoscaling

- min replicas
- max replicas
- CPU trigger
- idle suspend toggle
- suspend after seconds
- save policy

API: `GET/PUT /v1/apps/{id}/autoscale-policy`.

### Domains & LB

- load-balancing algorithm
- health path
- TLS/ACME status
- certificate list + expiry + renew all
- DNS records table
- add domain + CNAME verification

## 9. Config & Storage

### Volumes

Prototype: persistent volumes, mount `/data`, filesystem `ext4`, size `50 GB`,
persistence `Persistent`, Detach, snapshots Restore/Delete, empty state.

V1: attach/detach, resize later, snapshots P1; ephemeral rootfs is never
presented as persistent.

### Env & Secrets

- key/value table
- environment scope
- secret masking
- copy env for local dev
- encrypted at rest; never show secrets in logs/events

## 10. Logs

Prototype: project logs, live streaming indicator, search/filter, empty state.

V1: SSE/WebSocket stream, filters, bounded retention, explicit error when
logging is unavailable.

## 11. Metrics & Usage

Prototype: Core Web Vitals (LCP, INP, CLS, FCP, TTFB), request rate chart,
boot-to-ready latency, 30d retention, top projects/endpoints.

V1: platform/VM metrics from fleet, gateway request metrics; web vitals and cost
estimate are P2/non-billing.

## 12. Activity

Prototype table: Actor, Action, Resource, Outcome, When.

V1: operational events only, not the deferred compliance-audit product.

## 13. Project settings

Prototype: deployment protection (Vercel auth, password, trusted IPs) and
build/deploy framework/version settings.

V1: delete confirmation, preview protection P1, framework detection P1,
no team/SSO settings.

## 14. Replica detail

Header banners:

- Primary: actions control the entire replica group.
- Secondary: removable independently.

Status: `RUNNING · 14.8ms`; primary star.

Action bar: Scale Down, Lifecycle & Doctor, Restart ACPI,
Remove this Replica.

Tabs: Overview, Traffic, Metrics & Usage, Network Trace,
Logs & Console, Storage, Env, Lifecycle, Doctor.

### Overview

Fields: microVM identity/name, state, primary flag, boot readiness/health
latency, observed generation, project, deployment, host, private IP, kernel,
Firecracker socket, version, placement, uptime, traffic weight, specs,
sibling replicas, recent activity.

### Lifecycle & Doctor

Health verified before traffic; 10/10 checks.

### Traffic

Request rate, dropped requests, Rx/Tx, p99/p50.

### Metrics & Usage

CPU %, memory used/limit, ballooning, block I/O read/write, CPU chart,
request latency, cost estimate (P2), uptime.

### Network Trace

TAP name, bridge, MAC, eBPF forwarding state, MTU/TSO/CSO.
Waterfall: Edge GW -> Bridge, Bridge -> Guest, Guest -> App, with timings.

### Logs & Console

Serial + app logs; filters all/http/kernel; live badge.
Interactive console (ttyS0) preview is read-only until guest-vsock exists.
The UI must label not-implemented honestly.

### Storage

Volume `/dev/vdb -> /data`, virtio-blk sparse, mounted, capacity/filesystem/
persistence/mount/I/O mode, snapshots restore/delete.

### Env

Replica env vars and copy-env-for-local-dev.

### Lifecycle actions

- graceful reboot
- Firecracker pause
- snapshot recovery point
- live migration (P2 preview)
- primary cannot be removed; use Scale Down
- secondary can be removed
- irreversible removal warning: drain, terminate, ephemeral storage deleted

### Doctor

Subsystem checks: boot latency, virtio-net, virtio-balloon, virtio-blk,
SSH/exec, interactive console. States: PASS / Preview / Not Implemented.

## 15. Fleet — nodes list

Prototype: nodes sub-tab, 100% online, total vCPUs/RAM/MicroVMs, region filter,
columns status/region/IP/disk/memory/CPU/inspect, Fleet Doctor, Operation Inbox.

V1: node list from enrolled agents, online/offline/cordoned/draining, capacity
and pressure, inspect node.

## 16. Node detail

Tabs: Overview & Actions, Live Metrics, Hosted MicroVMs, Network, Doctor,
Logs, Agent Config.

Overview: IP, region, role, uptime, kernel, firecracker version, CPU, disk.
Actions: restart agent, cordon, drain, migrate VMs (P2 preview).
Connectivity: heartbeat RTT.
Network: porter0 bridge, subnet, gateway, MTU, egress NAT, eBPF isolation.
Scheduling placement description.
Hardware: KVM device, Firecracker, Jailer, cgroups, bridges, agent version,
heartbeat RTT, sensors.
Live telemetry: CPU, memory, VMs, cache, Rx/Tx.
Hosted MicroVMs table: replica, project, IP, specs, inspect.
Agent config: enrollment command, `/etc/porter/agent.toml`.

## 17. Platform admin overview

Prototype: healthy workloads, fleet capacity, requests/hour, MRR/customer count
(LATER), intent-to-healthy flow:
1. Source / GitHub
2. Build / BuildKit artifact
3. Runtime / boot MicroVM + network/storage
4. Operate / route traffic, health, reconcile
Recent operations, policy posture, audit coverage.

V1: project/fleet/request/recent-operation cards; omit MRR/customer data.

## 18. Networks & Gateway admin

- gateway status: healthy, route count, errors
- IPAM usage
- TLS certificates valid
- egress NAT/eBPF
- gateway routes: hostname -> deployment target, protocol, TLS, status
- add route

## 19. Storage & Backups admin

- persistent block size/attached volumes
- object storage (P2)
- latest backup, RPO
- storage classes: Ephemeral, Persistent block, Object store, Backup
- attach volume

V1: persistent volumes, snapshots/backups; object store optional.

## 20. Security & IAM admin (`LATER`)

Prototype: policy checks, members/roles, secrets encrypted, action classes
(read automatic, low-risk policy, production approval, destructive explicit
approval), SSO/SAML, MFA, production deploy protection, AI uses human IAM.

V1: admin token only, secret encryption, destructive confirmations; no SSO,
teams, or compliance audit.

## 21. Automation & Workflows (`LATER`)

Prototype: active workflows, create workflow, triggers (GitHub push, schedule,
capacity alert), policies, last run, status. Not implemented in V1.

## 22. Doctor admin

Deterministic diagnostics: run all checks, platform readiness, check table
(component/target/detail/status), useful without AI.

V1: control plane, SQLite, fleet connectivity, node KVM/Firecracker, image/kernel
presence, gateway/DNS/TLS, scheduler.

## 23. Observability

Prototype: time range 1h/6h/24h, total requests, p99 latency, error rate, active
projects, throughput chart, top projects, top endpoints, live request log.

V1: control-plane + gateway + fleet metrics; live request log P1; web vitals P2.

## 24. AI Operator (`LATER`)

Prototype: scoped role, inspect via `/api/discover`, safety gates,
intent/risk/current/proposed/cost/approval/steps, reject/approve and execute,
outcome/result, audit trail. Not implemented in V1.

## 25. Modals and wizards

1. Add Replicas: quantity 1..5, current/after, target deployment, placement
   spread, cost increase (P2).
2. Scale Down Group: remove count, current/after, primary never removed,
   minimum 1.
3. Remove Replica: irreversible warning, drain/terminate, ephemeral storage
   deleted.
4. Deploy Wizard: service name/repo/branch; repository detected/framework/
   Dockerfile; base image/port/vCPU/memory; review/deploy.
5. Enroll Server: hostname, IP, region, one-line enroll command.
6. Add Volume: name, mount, size GB.
7. Add Domain: domain, CNAME instructions, add & verify.
8. Shortcuts.
9. Command Palette.
10. Preview toolbar: comments, edit mode, draft mode, feature flags.

## 26. Data the backend must provide

Nodes:

```text
id, hostname, ip, region, role, state, cordoned,
cpus, memory_mb, memory_used_mb, disk_gb, disk_free_gb,
vms_count, kernel, firecracker_version, agent_version,
heartbeat_at, overlay_connected
```

Projects:

```text
id, name, description, status, replicas_running, replicas_desired,
latest_deployment_id, primary_domain, preview_count, created_at
```

Deployments:

```text
id, project_id, app_id, commit_hash, commit_message, author,
image_digest, state, duration_seconds, created_at,
route_weight, preview_id
```

Previews:

```text
id, project_id, deployment_id, pr_number, branch, commit,
url, status, replica_count, expires_at, protection_mode
```

Replicas/MicroVMs:

```text
id, project_id, deployment_id, node_id, name, state, ip, mac,
vcpus, memory_mb, kernel, firecracker_socket, uptime_seconds,
version_tag, traffic_weight, is_primary, volume_ids
```

Volumes:

```text
id, project_id, name, size_gb, mount_path, filesystem,
state, attached_replica_id
```

Domains/routes/certs:

```text
hostname, deployment_id, protocol, tls_status, cert_expiry, dns_records
```

Operations/events:

```text
id, actor, action, resource, outcome, timestamp, detail
```

## 27. Implementation priority

### P0
- trimmed global shell
- projects list/detail
- project overview
- deployments + preview URL + rollback/promote
- replica groups + scale
- fleet list + one-click enroll
- operation inbox / events
- logs
- basic node detail

### P1
- canary traffic
- autoscaling
- domains/LB/TLS
- volumes/snapshots
- env/secrets
- full replica detail tabs
- full node detail tabs
- doctor
- observability

### P2
- A/B test
- network trace waterfall
- web vitals
- cost estimate
- preview toolbar/comments
- live migration preview
- object storage

### LATER
- AI operator
- automation/workflows
- customers/teams
- billing
- security/IAM
- full audit/compliance
