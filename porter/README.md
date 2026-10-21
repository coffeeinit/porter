# `porter` — control plane

This Go project produces the `porter` binary.

It runs:

- admin API
- embedded admin UI
- SQLite store
- scheduler
- controllers/reconcilers
- fleet registry
- deployment/image catalog
- gateway

It does **not** touch host resources or call Firecracker directly. The local
`porter-agent` is the only privileged host component.

Planned layout:

```text
porter/
  cmd/porter/
  internal/api/
  internal/auth/
  internal/store/
  internal/model/
  internal/controller/
  internal/scheduler/
  internal/fleet/
  internal/deploy/
  internal/image/
  internal/gateway/
  migrations/
  web/
```

See [`../docs/ARCHITECTURE.md`](../docs/ARCHITECTURE.md) and
[`../docs/BACKEND_V1_PLAN.md`](../docs/BACKEND_V1_PLAN.md).

## Embedded fleet manager

The `porter` binary also contains the fleet manager role. It is a logical package
inside Porter, not a separate deployment:

- one-time join tokens
- agent enrollment
- mTLS certificate issuance/rotation
- long-lived agent WebSocket registry
- heartbeat/capacity aggregation
- command dispatch/ack/retry
- operation result relay
- optional WireGuard hub
- node lifecycle + agent upgrade coordination

Porter normally runs inside a MicroVM managed by `porter-agent`, with
`porter.db` on an agent-managed persistent volume. Each node runs only one
`porter-agent` with its own local SQLite.



