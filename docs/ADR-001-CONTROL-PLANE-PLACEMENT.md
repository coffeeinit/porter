# ADR-001: Control Plane Placement

## Status

Accepted for V1.0.0.

## Decision

Porter runs as an **unprivileged host-installed Go service**.

It is not run inside a MicroVM in V1.

```text
Host
  ├── porter       (unprivileged systemd service)
  │     -> UI/API/scheduler/fleet manager/SQLite
  └── porter-agent (privileged)
        -> Firecracker, TAP, NAT, volumes, local SQLite
```

The first host runs both Porter and `porter-agent`. Every additional host runs
only `porter-agent`.

## Why

- simplest install and upgrade path
- direct HTTPS endpoint for remote agents
- easy SQLite backup/restore
- easier debugging and operations
- no nested virtualization
- no host port-forward chain
- no control-plane VM bootstrap/rollback complexity
- Porter remains unprivileged and never controls Firecracker directly

Porter is trusted infrastructure. The important isolation boundary is between
**workloads** and the host, not between Porter and the host.

## Rules

- `porter-agent` is the only privileged host component.
- Porter has no KVM, Firecracker socket, TAP, or host root filesystem access.
- Porter and local agent communicate over a private Unix socket or localhost mTLS.
- Remote agents connect outbound to Porter over WSS.
- Porter restarts must not kill workloads.
- `porter.db` and `porter-agent.db` are separate SQLite databases.

## Deferred

Running Porter inside a MicroVM is deferred. It may return later as an optional
hardened mode, but it must not complicate V1.
