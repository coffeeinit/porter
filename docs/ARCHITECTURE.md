# Porter V1 Architecture

## 1. Design goals

Porter V1 must be easier to operate than the legacy backend:

- Two independently versioned binaries: `porter` and `porter-agent`.
- Two SQLite databases: `porter.db` on the server and `porter-agent.db` per node.
- No Redis, message broker, external scheduler, or Kubernetes dependency.
- Fleet enrollment in one command.
- Agents connect outbound to Porter; no inbound control port on nodes.
- The host agent stores local actual state and is safe to restart.
- Every long-running action becomes a durable operation with observable state.
- Porter owns global desired state; each agent owns host-local actual state.

## 2. Two deployable Go projects + shared protocol

### `porter/` — control plane + embedded fleet manager

Owns:

- admin authentication, API, embedded admin UI
- runs as a MicroVM managed by `porter-agent`
- SQLite schema and migrations (`porter.db`)
- projects, apps, deployments, previews, replicas
- scheduler and desired-state controllers
- image catalog, domains, routes, gateway
- one-time join tokens and agent enrollment
- mTLS certificate issuance/rotation
- long-lived agent connection registry
- heartbeat/capacity aggregation
- command dispatch/ack/retry
- optional WireGuard overlay hub
- agent upgrade coordination

Must not:

- run customer workloads
- call Firecracker directly

### `porter-agent/` — node agent

Owns:

- control-plane MicroVM bootstrap and supervision on the first host
- host port forward to the control-plane MicroVM
- local node identity/keypair
- mTLS connection to Porter
- heartbeat and capacity reporting
- Firecracker process lifecycle
- TAP/bridge/network namespace setup
- rootfs/ext4 volume handling
- local SQLite actual-state ledger (`porter-agent.db`)
- local reconciliation and operation result reporting

Must not:

- own global scheduling decisions
- store global project/user data
- expose an unauthenticated control port

### `protocol/` — shared contract

Contains only wire types and validation helpers:

- message envelope
- node hello/heartbeat
- VM desired-state spec
- VM observed-state report
- operation results
- log/metric frames

Do not put business logic in `protocol/`.

## 3. Data ownership

### `porter.db`

```text
users
api_tokens
projects
apps
deployments
previews
replicas
microvms
volumes
images
domains
routes
certificates
nodes
node_certificates
join_tokens
heartbeats
commands
overlay_ips
agent_versions
upgrade_jobs
operations
events
```

### `porter-agent.db` (one per node)

```text
node_identity
vms
vm_runtime
volumes
snapshots
operations
logs_meta
metrics_ring
```

No `customers`, `organizations`, `teams`, `plans`, `subscriptions`,
`invoices`, `payments`, `audit_events`, or marketplace tables in V1.

## 4. Runtime chain

```text
Project
  -> App
    -> Deployment (image digest + config)
      -> Replica (desired count)
        -> MicroVM (actual Firecracker instance on a node)
```

Replica states:

```text
pending
scheduling
provisioning
booting
running
degraded
stopping
stopped
failed
deleting
deleted
```

Node states:

```text
joining
ready
cordoned
draining
offline
```

Operation states:

```text
queued
running
succeeded
failed
cancelled
```

## 5. Scheduling

V1 scheduler is deliberately simple:

1. Filter nodes: `ready`, not cordoned, enough free CPU/memory/disk, matches image architecture.
2. Score nodes: least allocated first, then most free memory, then stable node ID.
3. Persist placement and create a `microvms` row in `provisioning`.
4. Send `vm.apply` command to the chosen fleet node.
5. Fleet reports observed state; control-plane controller updates `microvms` and `replicas`.

No taints/affinity/topology spread in V1. Labels can be stored for future use.

## 6. Reconciliation

Control-plane controllers run on a fixed tick (default 5s):

- NodeController: mark stale nodes offline, cancel placement, reschedule replicas if possible.
- ReplicaController: ensure each replica has a microVM or a pending operation.
- DeploymentController: progress rollout only from observed healthy replicas.
- OperationController: expire stuck operations and surface explicit errors.

Fleet reconciliation runs on a fixed tick (default 3s) and after every command:

- Compare local Firecracker processes against Porter's last desired VM set.
- Start missing VMs, stop extra VMs, report state.
- Never delete a VM just because a single command was missed; desired state wins.

## 7. Fleet join flow

```text
Admin                        Porter                         New node
  |                            |                               |
  |-- create join token ------>|                               |
  |                            |<-- POST /v1/fleet/enroll -----|
  |                            |    token + public key         |
  |                            |--- signed client cert ------->|
  |                            |<== mTLS WebSocket ============|
  |                            |<-- hello + heartbeat ---------|
  |<-- node appears in UI -----|                               |
```

Requirements:

- Join token is one-time, expiring, and hashed at rest.
- Node generates its own Ed25519 keypair locally.
- Porter signs a short-lived client certificate after validating the token.
- Node identity is the public-key fingerprint, not IP/hostname.
- Re-enrollment uses a new token.
- Revocation means deleting the node record and refusing its certificate.

## 8. Communication

Use a long-lived outbound WebSocket over mTLS:

- Fleet -> Porter: `hello`, `heartbeat`, `vm.state`, `operation.result`, `logs`, `metrics`.
- Porter -> Fleet: `vm.apply`, `vm.stop`, `vm.delete`, `snapshot.create`, `snapshot.restore`, `node.cordon`.

Every message:

```json
{
  "id": "msg_...",
  "type": "vm.apply",
  "node_id": "node_...",
  "operation_id": "op_...",
  "sent_at": "2026-...",
  "payload": {}
}
```

Rules:

- `operation_id` is idempotency key.
- Fleet acknowledges receipt, then reports progress/result.
- Porter retries commands with the same `operation_id`.
- Messages are small JSON in V1; protobuf can be added later without changing semantics.

## 9. Security boundary

- Control plane runs unprivileged where possible.
- Fleet runs as root or with narrow `CAP_NET_ADMIN`/`CAP_SYS_ADMIN` for Firecracker/TAP.
- Jailer is optional for developer hosts, recommended for production.
- Fleet never accepts inbound commands except the outbound WebSocket connection.
- Secrets are encrypted at rest in SQLite and only sent to the target node when needed.
- No hidden privileged AI/automation path.

## 10. Networking and storage

Networking V1:

- One bridge per control-plane cluster, e.g. `porter0`.
- One TAP device per microVM.
- Per-project `/24` or per-VM `/30` (choose one and keep it consistent).
- NAT for outbound traffic.
- Gateway runs in `porter`, not on every fleet node.

Storage V1:

- Rootfs is ephemeral and created from an image.
- Persistent volume = one ext4 sparse file on the fleet node.
- Attach as `/dev/vdb`.
- Snapshot = copy/snapshot file + metadata.
- No distributed storage in V1.

## 11. What is intentionally excluded from V1.0.0

- Customer accounts, teams, resellers
- Billing, subscriptions, invoices, payment adapters
- Marketplace, extensions, third-party integrations
- AI operator/MCP
- Full audit/compliance retention/export
- Kubernetes API compatibility
- Multi-region provider orchestration
- HA control plane / distributed SQLite
- Live migration

## 12. Fleet local SQLite

Each Fleet node owns one SQLite file, default:

```text
/var/lib/porter-fleet/fleet.db
```

Use a pure-Go SQLite driver (`modernc.org/sqlite`) so Fleet remains a single
static binary with no CGO dependency. Enable WAL mode for safe concurrent reads
and writes.

Local tables:

```text
node_identity      -- node id, cert fingerprint, enrollment metadata
vms                -- vm id, replica id, project/app ids, desired spec JSON
vm_runtime         -- pid, api socket, tap name, ip, mac, state, last error
volumes            -- local volume id, path, size, attached vm
snapshots          -- local snapshot files and checksums
operations         -- operation id, command, state, attempts, result
logs               -- bounded ring buffer index or file offsets
metrics            -- bounded recent samples
```

Rules:

- Porter is still the source of truth for **global desired state**.
- Fleet SQLite is the source of truth for **what exists on this host**.
- On boot, Fleet loads its local VM inventory and checks each VM against its
  Firecracker API socket and PID.
- If a VM is missing locally, Fleet reports `failed`; Porter decides whether to
  reschedule.
- If Fleet has a VM Porter does not know about, mark it orphaned and require an
  explicit cleanup operation; never delete silently.
- Use WAL mode, `busy_timeout`, and one writer goroutine.

This avoids trying to infer all VM configuration from `ps`/`/proc`. Process
scanning is only a backup check, not the source of truth.

## 13. Remote connectivity and WireGuard

### Baseline: outbound mTLS WebSocket

Fleet always initiates the control connection:

```text
wss://porter.example.com/v1/fleet/stream
```

Benefits:

- no inbound port on Fleet nodes
- NAT/private-network friendly
- works through HTTP proxies when configured
- easy one-command enrollment
- reconnect with exponential backoff

### Optional: built-in WireGuard overlay

For private node-to-node traffic and management isolation, Porter can act as a
WireGuard hub.

Possible Go packages:

| Package | Use |
|---|---|
| `golang.zx2c4.com/wireguard` | Official userspace WireGuard implementation/library |
| `golang.zx2c4.com/wireguard/wgctrl` | Configure kernel WireGuard interfaces |
| `golang.zx2c4.com/wireguard/device` | Embedded userspace device when kernel support is unavailable |
| `github.com/quic-go/quic-go` | Optional future multiplexed control transport over UDP |
| `github.com/hashicorp/yamux` | Optional stream multiplexing over a single connection |

Recommended V1 behavior:

- Control channel always uses mTLS WebSocket over TCP 443.
- WireGuard is optional and enabled by config.
- Prefer kernel WireGuard via `wgctrl`; fall back to userspace `wireguard-go`.
- Porter exposes one WireGuard UDP endpoint.
- Fleet nodes receive a private overlay IP from Porter.
- If WireGuard cannot connect, the control plane still works over WSS.

Do not claim WireGuard provides universal NAT traversal. It needs a reachable
endpoint or port forwarding. If a future requirement is truly zero-inbound-port
peer-to-peer networking, use a relay/DERP-style design or integrate
Tailscale/Headscale as an optional extension rather than forcing it into V1.

## 14. Kubernetes-inspired scope only

In V1, "Kubernetes-like" means:

- nodes register and heartbeat
- scheduler places replicas on nodes
- desired replica count is reconciled
- cordon/drain
- rolling updates

It does not mean Kubernetes compatibility. Do not add:

- Kubernetes API server or CRDs
- Pods/Deployments/Services/Ingress objects
- CNI/CSI/CRI interfaces
- Kubernetes RBAC compatibility
- kubelet/kube-proxy behavior
- multi-cluster federation










## 15. Control-plane-in-MicroVM rule

The recommended production topology is:

```text
host
  -> porter-agent (privileged)
  -> porter-control-plane MicroVM (API/UI/scheduler/fleet manager)
  -> workload MicroVMs
```

The agent owns the control-plane data volume and supervises the control-plane
VM. Porter never receives host privileges. See
[`CONTROL_PLANE_AS_MICROVM.md`](CONTROL_PLANE_AS_MICROVM.md).
