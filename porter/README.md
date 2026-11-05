# `porter` — control plane

This Go project produces the `porter` binary. It runs the admin API and embedded UI, SQLite store, scheduler/controllers, fleet registry, deployment and image catalog, and gateway. It does not touch host resources or call Firecracker directly; the local `porter-agent` is the only privileged host component.

## Fleet transport today

The current agent protocol is JSON over request/response HTTP(S): one-time-token enrollment, bearer-authenticated heartbeats, command polling, results, logs, snapshots, and VM-state reports. It does not use a WebSocket stream.

To serve the API over HTTPS, set `PORTER_API_TLS_CERT` and `PORTER_API_TLS_KEY`. To require verified agent client certificates on the agent fleet routes, also set `PORTER_AGENT_CLIENT_CA`. Client certificates must be signed by that CA and contain the URI SAN `spiffe://porter/node/<node-id>` matching `X-Porter-Node-ID`. The node bearer token remains required. Enrollment and admin routes are not subject to the agent mTLS gate.

Certificate issuance, renewal, revocation workflows, and rotation are operator-managed and are not implemented by Porter. See [`../docs/PROTOCOL.md`](../docs/PROTOCOL.md) for the implemented HTTP routes and the clearly labeled future stream proposal.

## Snapshot metadata

Snapshot records include SHA-256 manifests for Firecracker state/memory, the private rootfs, and any attached volume. The accepted contract is **crash-consistent only**, not application-consistent. Artifact bytes remain on the agent node; the control plane stores metadata and does not provide off-host retention. Real KVM recovery and full API-to-agent acceptance are still required before the beta gate can pass.

## Project layout

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
  web/
```

See [`../docs/ARCHITECTURE.md`](../docs/ARCHITECTURE.md) and [`../docs/BACKEND_V1_PLAN.md`](../docs/BACKEND_V1_PLAN.md).
