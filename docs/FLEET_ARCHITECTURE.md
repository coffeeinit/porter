# Fleet Architecture — Porter + Agent

## Two deployable projects

```text
porter                 global control plane + embedded fleet manager
porter-agent     per-node executor
```

The fleet manager is a **logical package inside Porter**, not a separate binary.
Porter exposes the UI/API, scheduler, enrollment endpoint, mTLS agent
connections, command relay, and optional WireGuard hub.

This keeps the deployment simple:

- one server process
- one main SQLite database
- one `porter-agent` process per node
- one local SQLite database per node

## Databases

| Database | Owner | Contents |
|---|---|---|
| `porter.db` | Porter | users, projects, apps, deployments, previews, replicas, images, nodes, certs, commands, overlay IPs, operations, events |
| `porter-agent.db` | Porter agent | local VMs, PID/socket/TAP, volumes, snapshots, local ops, log/metric metadata |

No shared database file. The agent never stores global projects or deployments.

## Responsibilities

### Porter

- admin API + UI
- projects/apps/deployments/previews/replicas
- scheduler + desired-state controllers
- image catalog
- gateway routes
- one-time join tokens
- agent enrollment + mTLS certificate issuance/rotation
- long-lived agent connection hub
- heartbeat/capacity aggregation
- command dispatch/ack/retry
- operation result relay
- optional WireGuard overlay hub
- node lifecycle + agent upgrade coordination

### Porter agent

- local SQLite actual-state ledger
- Firecracker lifecycle
- TAP/bridge/NAT
- volumes/snapshots
- serial logs
- local metrics
- reconciles local state with Porter commands
- owns per-VM systemd units

## Main flow

```text
Porter scheduler decides: replica R -> node N
  -> Porter fleet-manager package persists command
  -> sends vm.apply over agent WebSocket
  -> porter-agent persists desired spec in local SQLite
  -> agent starts Firecracker VM
  -> agent reports vm.state + operation.result
  -> Porter updates replica/microVM status
```

## Independent updates

Versions:

```text
porter               v1.0.0
porter-agent   v1.0.0
protocol             v1
```

Rules:

- Porter supports protocol `v1` and one previous minor agent.
- protocol version is exchanged in `hello`.
- unknown major version -> explicit rejection.
- both binaries expose `--version` and `/version`.

Agent upgrade flow:

1. Porter marks an upgrade job for agent version X.
2. Porter sends `agent.upgrade` with signed URL + SHA256.
3. Agent downloads, verifies, and stages the binary.
4. Agent stops accepting new VM placements; running VMs continue.
5. Agent atomically swaps binary and restarts.
6. Agent reconnects and reports version.
7. On failure, Porter triggers rollback.

VMs survive because each runs in its own transient systemd unit
(`porter-vm-<id>.service`, `KillMode=process`).

## Why not split the manager out now?

For V1 simplicity:

- one server binary is easier to install and debug
- agent connection churn is still isolated in its own package/goroutines
- the manager boundary can be split into a separate binary later without
  changing the agent protocol


## Control plane is also a MicroVM

The `porter` control plane is normally started by `porter-agent` as a special
local MicroVM with a persistent data volume. The agent port-forwards the host
HTTP/HTTPS ports to that VM.

```text
porter-agent
  -> porter-control-plane MicroVM
       -> UI/API/scheduler/fleet manager
       -> porter.db on persistent volume
  -> workload MicroVMs
```

This keeps one isolation rule everywhere: only the agent touches host
resources, and Porter itself has no host privileges.

See [`CONTROL_PLANE_AS_MICROVM.md`](CONTROL_PLANE_AS_MICROVM.md).
