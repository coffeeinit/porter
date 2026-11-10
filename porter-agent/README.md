# `porter-agent` — host agent

This Go project produces the `porter-agent` binary. One privileged agent runs on every Linux/KVM machine. It is the only component that controls Firecracker, TAP devices, NAT, local volumes, and local system state.

Responsibilities include one-time-token enrollment, host preflight, heartbeat/capacity reporting, command polling, Firecracker lifecycle, TAP/bridge/NAT networking, local volumes and snapshots, and a local SQLite state ledger.

## Snapshot and restore

The agent captures Firecracker state/memory, each VM's private rootfs, and an optional attached volume with file syncs, SHA-256 manifests, exclusive volume locks, and a durable restore journal. The contract is **crash-consistent only**; application freeze/thaw is not implemented. Snapshot files stay on the agent node and are not an off-host backup. Legacy VMs without verified private-rootfs ownership fail closed for snapshot/restore and must be recreated. Portable fake tests cover restore and rollback; real-KVM recovery acceptance remains required.

## Join and agent transport

Join requires HTTPS, verifies the control-plane certificate and hostname, and does not follow redirects. By default, the command prompts for the one-time token from `/dev/tty` with echo disabled and restores terminal echo on exit or interruption. Automation may opt in with the non-secret `--token-stdin` switch, which reads one line from stdin only. Token arguments and token-file options are rejected. Never put a token in an argument, URL, environment variable, file, shell history, or log. The token is sent only as the `Authorization: Bearer` header to `POST /api/v1/fleet/enroll`; the JSON body contains only `protocol_version: "v2"` and `hostname`. `--hostname` is optional and defaults to the system hostname; the selected hostname is included in every enrollment request and is not stored in SQLite or the service config.

The returned node bearer is written atomically to `node-credential` as a separate mode-0600 file inside a root-owned mode-0700 state directory. `agent-service.json` is also root-owned mode 0600 and is created before token input; SQLite's `config` table contains exactly `node_id`, `server_url`, and `credential_path`. Runtime paths stay in the protected service config, root-owned service environment file, or supported run flags, never the enrollment config row. A new state directory is created with mode 0700; an existing one is validated as-is, and unsafe permissions or ownership are rejected without changing them. The service refuses to start until the credential and ready config are both present and consistent.

`scripts/install-agent.sh` installs a root-owned, non-secret `/etc/porter/porter-agent.env` template unless one already exists. It carries `PORTER_AGENT_STATE_DIR`, `PORTER_AGENT_KERNEL`, `PORTER_AGENT_ROOTFS`, and optional TLS file paths; systemd loads it for the running agent. For a manual join, pass desired state/kernel/rootfs paths as flags, or export those `PORTER_AGENT_*` variables in the join process. Explicit flags override environment defaults. The installer does not pre-create or repair the state directory.

The client never retries enrollment automatically. An explicit `404`, or a `503` with exactly one handler-set `X-Porter-Enrollment-Outcome: not-consumed` header, confirms that the token was not consumed; a later user-invoked `join` may retry with the same token. A bare, unmarked, or malformed `503` is not safe to replay. The current backend candidate does not emit the 503 marker, so its 503 responses remain unknown until the handler implements that guarantee.

For a transport failure, an invalid/lost success response, or any other response that may follow handler execution, the outcome is unknown and the CLI blocks replay. Do not retry that token. An operator must find the Fleet row by the exact hostname and enrollment time, verify the exact node ID, and rotate credentials through admin Fleet; stop if the match is missing or ambiguous. Then verify an authenticated heartbeat. This client does not perform server-side credential rotation or revocation.

After enrollment, the agent uses only the PostgreSQL-backed v2 agent routes over HTTPS with `Authorization: Bearer <node_token>` and `X-Porter-Node-ID: <node_id>`; redirects are not followed. Heartbeat and telemetry POSTs return 204, command polling is `GET /internal/agent/commands?limit=20`, and result POSTs return 204. Logs and metrics are telemetry batches; snapshots and VM/state operations are `CommandV2` commands and result receipts. There are no separate v2 log, snapshot, or VM-state endpoints and no WebSocket/WSS stream. Enrollment is not protected by the optional client-certificate gate.

The control plane can enable mutual TLS for the agent routes. When enabled, every agent certificate must be signed by the configured client CA and contain the URI SAN `spiffe://porter/node/<node-id>` matching the enrolled node. The node bearer token remains required as a second authentication factor. Certificate issuance, renewal, and rotation are operator-managed; Porter does not issue or rotate certificates.

Example join with a private server CA:

```bash
sudo ./bin/porter-agent join --server https://porter.example.com \
  --hostname my-node \
  --state-dir /var/lib/porter-agent \
  --kernel /path/to/vmlinux \
  --rootfs /path/to/rootfs.ext4 \
  --server-ca /etc/porter/control-plane-ca.pem
# Enter the one-time token at the hidden prompt; do not pass it as an option.
```

After enrollment, provision a client certificate for the returned node ID. Run manually with the matching paths, or let systemd load the TLS settings from `/etc/porter/porter-agent.env`:

```bash
sudo ./bin/porter-agent run \
  --state-dir /var/lib/porter-agent \
  --network on \
  --server-ca /etc/porter/control-plane-ca.pem \
  --client-cert /etc/porter-agent/node.crt \
  --client-key /etc/porter-agent/node.key
```

A manual join can also use exported `PORTER_AGENT_SERVER_CA`, `PORTER_AGENT_CLIENT_CERT`, and `PORTER_AGENT_CLIENT_KEY` values or the matching flags. The join flow records selected certificate paths in the protected service config; it never stores private key contents. Keep the private key readable only by the agent service account. Use a publicly trusted API certificate and omit `--server-ca` when no private server CA is needed.

On the control-plane host, set `PORTER_API_TLS_CERT` and `PORTER_API_TLS_KEY` to serve the API over HTTPS. Set `PORTER_AGENT_CLIENT_CA` to enable client-certificate verification on the agent heartbeat, telemetry, command, and result routes. Snapshot and VM/state operations use the command and result routes. Enrollment and admin routes remain outside the mTLS gate.

Detailed architecture: [`../docs/FLEET_ARCHITECTURE.md`](../docs/FLEET_ARCHITECTURE.md) and [`../docs/CONTROL_PLANE_AS_MICROVM.md`](../docs/CONTROL_PLANE_AS_MICROVM.md). The proposed WebSocket protocol is documented separately and is not implemented.
