# Porter Fleet Protocol — current HTTP v1 and future stream proposal

## Implemented transport

Current Porter binaries use request/response JSON over HTTP(S). The agent enrolls with a one-time join token, then sends heartbeats and polls for commands using its node bearer token. There is no WebSocket/WSS stream or frame envelope in the current transport; the shared `protocol.Envelope` type is not used by these HTTP handlers.

Remote control-plane URLs must use HTTPS. HTTP is allowed only for loopback development. API TLS terminates in the Porter API process; a TLS-terminating proxy in front of Porter cannot satisfy the agent client-certificate check unless it passes the TLS connection through.

Every enrolled-agent request uses `Authorization: Bearer <node-token>` and `X-Porter-Node-ID: <node-id>`. Optional mTLS adds a verified client certificate; it does not replace the bearer token. When `PORTER_AGENT_CLIENT_CA` is configured, the API requires mTLS on the agent routes below. The client certificate must be signed by that CA and carry the URI SAN `spiffe://porter/node/<node-id>` matching the node-ID header. Enrollment and admin routes are not covered by this gate.

| Method | Path | Purpose | mTLS when enabled |
|---|---|---|---|
| `POST` | `/v1/fleet/enroll` | Exchange a one-time join token for a node ID and node token | No |
| `POST` | `/v1/fleet/heartbeat` | Report node capacity and VM count | Yes |
| `GET` | `/v1/fleet/commands` | Poll for queued commands | Yes |
| `POST` | `/v1/fleet/result` | Report a command result | Yes |
| `POST` | `/v1/fleet/logs` | Submit VM log lines | Yes |
| `POST` | `/v1/fleet/snapshot` | Submit snapshot metadata | Yes |
| `POST` | `/v1/fleet/vmstate` | Report VM state and health | Yes |

### Enrollment payload

The current request fields are defined by `protocol.EnrollRequest`:

```json
{
  "token": "penr_...",
  "hostname": "node-1",
  "os": "linux",
  "arch": "amd64",
  "cpus": 8,
  "memory_mb": 16384,
  "disk_mb": 120000
}
```

A successful response contains only the node identity and bearer token:

```json
{
  "node_id": "node_...",
  "node_token": "..."
}
```

Porter does not issue a client certificate as part of enrollment. If mTLS is enabled, an operator must provision a certificate for the returned node ID before starting the agent's authenticated fleet loop.

### Heartbeats and command polling

A heartbeat uses the current `protocol.Heartbeat` fields:

```json
{
  "node_id": "node_...",
  "cpus_free": 6,
  "memory_free_mb": 12288,
  "disk_free_mb": 90000,
  "vm_count": 2,
  "at": "2026-11-05T12:00:00Z"
}
```

The command endpoint returns the queued `protocol.Command` records. Results use `protocol.CommandResult` (`command_id`, `operation_id`, `node_id`, `attempt`, `success`, and optional `error` or `state`). Agent API payloads are JSON; the Go definitions in `protocol/types.go` are authoritative for fields not shown here.

### Enabling mTLS

On the control-plane host, set `PORTER_API_TLS_CERT` and `PORTER_API_TLS_KEY` to enable HTTPS. Also set `PORTER_AGENT_CLIENT_CA` to require verified node certificates on heartbeat, command polling, result, log, snapshot, and VM-state requests. Invalid or incomplete certificate configuration fails startup.

On the agent, provide the control-plane trust root as `--server-ca` or `PORTER_AGENT_SERVER_CA` when it is not in the system trust store. Provide its identity as `--client-cert` and `--client-key`, or `PORTER_AGENT_CLIENT_CERT` and `PORTER_AGENT_CLIENT_KEY`. Certificate issuance, rotation, revocation workflows, and WebSocket streaming are not implemented.

## Future WebSocket stream proposal — not implemented

The rest of this section is a design sketch only. These message names and payload examples are not accepted by current Porter binaries.

A future agent-initiated outbound mTLS WebSocket could use a versioned envelope containing message ID, type, node ID, operation ID, timestamp, and payload. Candidate messages include `hello`, `heartbeat`, `vm.state`, `operation.result`, `vm.apply`, `vm.stop`, `vm.delete`, `snapshot.create`, and `snapshot.restore`. The stream would need durable command state, acknowledgements, deadlines, bounded retries, idempotent operations, reconnect backoff, and replay/deduplication after disconnect.

If implemented, node certificates should remain node-specific and revocable, with explicit renewal and emergency-revocation behavior. The control plane must continue to send authenticated operations to the agent; it must never access or proxy a Firecracker Unix socket over the network. Optional WireGuard overlay networking is separate from the agent control channel.
