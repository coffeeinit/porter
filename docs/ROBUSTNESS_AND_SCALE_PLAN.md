# Robustness, Failure Handling, and Scale Plan

## 1. Non-negotiable architectural rule

Only `porter-agent` touches host resources.

Porter runs as an unprivileged host systemd service in V1. Builders and customer
apps run as Firecracker MicroVMs. Porter has no KVM, host filesystem, TAP, or
Firecracker socket access.

```text
porter-agent (privileged)
  -> porter (unprivileged host service)
  -> workload VMs (unprivileged)
  -> builder VMs (optional)
```

## 2. Failure matrix

| Failure | Expected behavior |
|---|---|
| Porter process crashes | systemd restarts it; SQLite data persists |
| Agent crashes | VMs continue; agent restarts and reconciles from `porter-agent.db` |
| Host reboots | agent starts first, then Porter, then workload VMs |
| Agent loses network | VMs continue; reconnect with backoff; ops queued |
| Porter loses network | agent keeps VMs; state reported on reconnect |
| Porter DB locked | WAL + busy_timeout; API returns explicit 503/retry |
| Porter data volume full | doctor error; no silent writes |
| Invalid Porter upgrade | health check fails; systemd/package rollback |
| Node disk full | scheduler stops placement; explicit condition on node |

## 3. Bootstrap must be idempotent

First-host installer:

1. install `porter` + `porter-agent`
2. create config and data directory
3. start `porter`, wait for `/healthz`
4. start `porter-agent`, enroll as first node
5. print dashboard URL and admin setup once

Re-running the installer must reuse existing config, SQLite, and certificates.

## 4. State recovery

Agent startup:

1. open `porter-agent.db`
2. scan `porter-vm-*` systemd units
3. verify Firecracker sockets/PIDs
4. reconcile local state
5. reconnect to Porter and report actual state

Porter startup:

1. open `porter.db`
2. run migrations
3. load desired state
4. reconcile replicas through agents

Mismatch is repaired by reconciliation, never panic.

## 5. Scalability

V1: single Porter host, 1–20 nodes, 100–500 MicroVMs.
V1.1: scheduler indexes, batch reconcile, bounded agent queues.
V2: extract fleet-manager package into separate binary if agent connections
become a bottleneck. Agent protocol stays unchanged.
V2+: HA control plane, external/shared DB, leader election.

## 6. Security hardening

- narrow agent capabilities where possible
- jailer/seccomp for production workloads
- signed agent images/binaries
- mTLS certificate rotation
- one-time join tokens hashed at rest
- encrypted secrets + redaction
- no host mounts into Porter
- network isolation/egress policy
- rate limits on login/enroll
- destructive-action confirmation

## 7. Required tests

- protocol compatibility
- join token expiry/replay/revocation
- agent reconnect and command replay
- Porter restart with running VMs
- host reboot recovery
- scheduler placement + cordon/drain
- idempotent VM apply/stop/delete
- preview TTL cleanup
- rollback health gate
- SQLite backup/restore
- secrets never logged

## 8. Action plan

1. two Go modules + protocol types
2. Porter API/SQLite/UI shell
3. Agent install, preflight, local SQLite
4. Join/heartbeat/mTLS
5. Firecracker lifecycle + TAP/NAT
6. Scheduler + controllers + cordon/drain
7. Deploy/preview/rollback
8. Logs/metrics/doctor
9. Hardening and E2E
