# `porter-agent` — host agent

This Go project produces the `porter-agent` binary.

One privileged agent runs on every Linux/KVM machine. It is the **only** component that controls Firecracker, TAP devices, NAT, local volumes, and local system state.

Responsibilities include one-time-token enrollment, host preflight, heartbeat/capacity reporting, command polling, Firecracker lifecycle, TAP/bridge/NAT networking, local volumes and snapshots, and a local SQLite state ledger.

## Snapshot and restore

The agent captures Firecracker state/memory, each VM's private rootfs, and an optional attached volume with file syncs, SHA-256 manifests, exclusive volume locks, and a durable restore journal. The contract is **crash-consistent only**; application freeze/thaw is not implemented. Snapshot files stay on the agent node and are not an off-host backup. Legacy VMs without verified private-rootfs ownership fail closed for snapshot/restore and must be recreated. Portable fake tests cover restore and rollback; real KVM recovery acceptance remains required.

## Agent transport today

The agent uses JSON request/response over HTTPS with its bearer node token. It sends heartbeats and polls for commands; there is no WebSocket/WSS stream. Remote plaintext HTTP is rejected; loopback HTTP is allowed for development. Enrollment uses its one-time join token and is not protected by the optional client-certificate gate.

The control plane can enable mutual TLS for the agent routes. When enabled, every agent certificate must be signed by the configured client CA and contain the URI SAN `spiffe://porter/node/<node-id>` matching the enrolled node. The node bearer token remains required as a second authentication factor. Certificate issuance, renewal, and rotation are operator-managed; Porter does not issue or rotate certificates.

Example with a private CA:

```bash
sudo ./bin/porter-agent join \
  --server https://porter.example.com \
  --token <join-token> \
  --name my-node \
  --data-dir /var/lib/porter-agent \
  --kernel /path/to/vmlinux \
  --rootfs /path/to/rootfs.ext4 \
  --server-ca /etc/porter/control-plane-ca.pem

# After enrollment, provision a client certificate for the returned node ID.
sudo ./bin/porter-agent run \
  --data-dir /var/lib/porter-agent \
  --network on \
  --server-ca /etc/porter/control-plane-ca.pem \
  --client-cert /etc/porter-agent/node.crt \
  --client-key /etc/porter-agent/node.key
```

The same paths can be set through `PORTER_AGENT_SERVER_CA`, `PORTER_AGENT_CLIENT_CERT`, and `PORTER_AGENT_CLIENT_KEY` in the agent service environment. Keep the private key readable only by the agent service account. Use a publicly trusted API certificate and omit `--server-ca` when no private server CA is needed.

On the control-plane host, set `PORTER_API_TLS_CERT` and `PORTER_API_TLS_KEY` to serve the API over HTTPS. Set `PORTER_AGENT_CLIENT_CA` to enable client-certificate verification and enforce mTLS on heartbeat, command polling, result, log, snapshot, and VM-state routes. Enrollment and admin routes remain outside the mTLS gate.

Detailed architecture: [`../docs/FLEET_ARCHITECTURE.md`](../docs/FLEET_ARCHITECTURE.md) and [`../docs/CONTROL_PLANE_AS_MICROVM.md`](../docs/CONTROL_PLANE_AS_MICROVM.md). The proposed WebSocket protocol is documented separately and is not implemented.
