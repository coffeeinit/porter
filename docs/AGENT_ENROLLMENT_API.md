# Fleet enrollment and agent API

The beta control plane exposes Fleet provisioning through PostgreSQL-backed `/api/v1/fleet` routes. Internal handler paths are not part of the UI, installer, or operator-script contract. PostgreSQL is the only server-side source of truth; there is no SQLite or legacy `/v1` fallback.

## Public Fleet routes

| Method and path | Authentication | Result |
| --- | --- | --- |
| `GET /api/v1/fleet/capabilities` | Operator Bearer | HTTPS-only `200`, `no-store`, exact capability flags for mounted routes |
| `POST /api/v1/fleet/join-tokens` | Operator Bearer | HTTPS-only `201`, one-use token, `no-store` |
| `POST /api/v1/fleet/enroll` | One-time join Bearer | HTTPS-only `201`, node identity and node credential once, `no-store` |
| `GET /api/v1/fleet/nodes` | Operator Bearer | Paginated Fleet rows persisted in PostgreSQL |
| `POST /api/v1/fleet/nodes/{node_id}/credentials` | Operator Bearer | HTTPS-only `201`, rotated node credential once, `no-store` |

All these routes reject a `token` query parameter. The capability, issue, and rotation routes return `401` without or with an invalid operator Bearer. `POST /api/v1/fleet/enroll` accepts only a valid join Bearer; the operator token is not an enrollment token. Credential rotation accepts no body or exactly `{}` with `Content-Type: application/json`; it has no input fields and rejects unknown fields.

The capability response contains exactly these booleans:

```json
{
  "join_token_issuance": true,
  "agent_enrollment": true,
  "node_credential_rotation": true
}
```

Each flag is derived from a handler actually mounted at the public route. Missing handlers are reported as `false`; if the capability handler is absent, its route returns `404`. The UI must enable setup controls only after HTTPS, valid operator authentication, a `200` response, and all three flags set to `true`. Any auth failure, `404`, `426`, `503`, network error, invalid DTO, or false flag must disable the controls and clear the in-memory operator token.

## Issue a join token

`POST /api/v1/fleet/join-tokens` accepts the operator Bearer and this JSON body:

```json
{
  "target": "remote_host",
  "control_plane_url": "https://porter.example.com"
}
```

`target` must be `same_host` or `remote_host`. `control_plane_url` must be an HTTPS origin without a path, query, fragment, or user information. A successful request returns `201 Created`:

```json
{
  "id": "enr_…",
  "token": "ptenr_…",
  "target": "remote_host",
  "control_plane_url": "https://porter.example.com",
  "expires_at": "2026-11-10T10:40:00Z"
}
```

The token contains 256 random bits, expires after ten minutes, and can be redeemed once. PostgreSQL stores only its SHA-256 hash; there is no list or secret-recovery endpoint. Keep it only in the active setup session. Never put it in a URL, process argument, log, analytics event, or persistent UI storage.

## Enroll a node and protect its identity

`POST /api/v1/fleet/enroll` accepts the join token in `Authorization: Bearer <join-token>` and this JSON body:

```json
{
  "protocol_version": "v2",
  "hostname": "porter-worker-01"
}
```

A successful request returns `201 Created` with `protocol_version`, opaque `node_id`, one-time `node_token`, enrollment target, HTTPS control-plane URL, credential generation, and initial `offline` state. In one PostgreSQL transaction, the server consumes the join token, creates the Fleet row, stores only the node-token SHA-256 hash, and writes audit events. Unknown, expired, and replayed join tokens return the same generic `401` response. The server never logs either token.

The agent client contract is `sudo porter-agent join --server https://<control-plane>`. It prompts through hidden `/dev/tty` input by default; optional `--token-stdin` reads exactly one token line. Do not add token argv or token-file flags. Verify the server certificate. Persist the opaque node ID and node token only in protected agent-local state; on Unix, credential files must be mode `0600`. Never pass the node token in argv, environment, URLs, logs, or telemetry. Bind every agent request to `X-Porter-Node-ID` and its node Bearer.

The mounted TLS-only agent routes are `POST /internal/agent/heartbeat`, `GET /internal/agent/commands`, `POST /internal/agent/telemetry`, and `POST /internal/agent/results`. Each requires a valid node Bearer plus matching `X-Porter-Node-ID`. A heartbeat moves a persisted Fleet node from `offline` to `ready` and updates `last_seen_at`. A result or telemetry body with a different node identity returns `403`; absent, invalid, wrong-node, expired, or stale node credentials return `401`. If `PORTER_AGENT_CLIENT_CA` is configured, post-enrollment routes additionally require a verified certificate whose `spiffe://porter/node/<node_id>` identity matches the request.

Do not automatically retry enrollment after an ambiguous or lost `201`: the join token has already been consumed. Unknown, expired, and replayed tokens must stay indistinguishable.

## Recover after a lost enrollment response

1. Do not retry enrollment.
2. In Fleet, find exactly one offline row matching the expected hostname and creation time. Use its opaque ID. If the match is ambiguous, stop.
3. Call `POST /api/v1/fleet/nodes/{id}/credentials` for that exact ID and securely deliver the returned token to the intended host.
4. Persist it in the protected agent-local credential file and send an authenticated heartbeat.
5. Confirm the same Fleet ID is `ready` with a recent `last_seen_at`.

Credential rotation increments the generation and returns the new token once. The previous credential remains valid for five minutes by default, up to a configured maximum of thirty minutes; it is rejected after its stored expiry. There is no immediate revoke/delete endpoint in this flow.

## PostgreSQL and systemd requirements

The server and migrator receive `/etc/porter/porter-postgres-dsn` and `/etc/porter/porter-admin-token` only through systemd `LoadCredential` (systemd 247+). Source files must be `root:root`, mode `0600`. Both `porter.service` and `porter-migrate.service` load both credentials. Runtime processes have no `PORTER_POSTGRES_DSN` or `PORTER_ADMIN_TOKEN` environment/argv fallback. Keep `porter.env` for non-secret settings only. See [`INSTALLATION_CONTRACT.md`](INSTALLATION_CONTRACT.md) for exact installer order and unit contents.

Build and run `porter-migrate.service` before `porter.service`. The server verifies the complete schema through migration 0012 and fails closed if any credential, connection, or migration is missing. It never applies migrations at startup. Remote listeners require API TLS. Plaintext enrollment, issue, rotation, and agent v2 requests return `426 Upgrade Required`.

Legacy `/v1` paths return a `410 Gone` JSON error with a `req_…` request ID. The prior `/internal/admin/...` issue and `/internal/agent/enroll` paths are not public provisioning routes. The internal heartbeat/commands/telemetry/results paths remain agent transport only.

## Embedded UI gate

The server embeds `porter/web/index.html` through `porter/web/web.go`. The root returns `200` only for a bundle declaring `<meta name="porter-api-contract" content="postgres-only">`, using the capability and public Fleet routes, and containing no `/v1` calls, demo/sample fallback, or persistent token storage. Until the Vue-owned bundle passes that gate, `/` visibly returns `503 Service Unavailable`; the legacy UI is not shown as a fallback. The current lane does not claim the panel is usable until the UI track supplies and verifies that bundle.

The PostgreSQL API tests use disposable databases. No real Firecracker/KVM acceptance is implied by these API checks; keep KVM evidence separate.
