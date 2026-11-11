# Porter PostgreSQL install contract

## Required platform and secrets

The installer must require systemd 247 or newer before installing or starting Porter. Both executables fail closed if systemd does not provide `CREDENTIALS_DIRECTORY`; there is no environment-variable, command-line, or SQLite fallback.

Create these source files as `root:root`, mode `0600`:

| Secret | Source file | Unit credential name |
| --- | --- | --- |
| PostgreSQL connection string | `/etc/porter/porter-postgres-dsn` | `porter-postgres-dsn` |
| Operator admin Bearer | `/etc/porter/porter-admin-token` | `porter-admin-token` |

`porter.service` must run as `User=porter` and load both files with:

```ini
LoadCredential=porter-admin-token:/etc/porter/porter-admin-token
LoadCredential=porter-postgres-dsn:/etc/porter/porter-postgres-dsn
UnsetEnvironment=PORTER_POSTGRES_DSN PORTER_ADMIN_TOKEN
```

`porter-migrate.service` must run as `User=porter` and load both credentials:

```ini
LoadCredential=porter-admin-token:/etc/porter/porter-admin-token
LoadCredential=porter-postgres-dsn:/etc/porter/porter-postgres-dsn
UnsetEnvironment=PORTER_POSTGRES_DSN PORTER_ADMIN_TOKEN
```

Both services receive both secret files only through `$CREDENTIALS_DIRECTORY`; the server and migrator read the PostgreSQL DSN there, and the server also reads the admin token there. The migrator is supplied the admin token as part of this service contract. Keep `/etc/porter/porter.env` for non-secret settings only. Do not put either credential in an environment file, process environment, command arguments, logs, URLs, or shell history. Errors must remain generic and must not echo secret contents.

## Install and start order

1. Check the systemd version and fail before installation if it is below 247.
2. Create the two root-owned credential source files with mode `0600`; do not print their contents.
3. Install `porter`, `porter-migrate`, `porter.service`, and `porter-migrate.service`. Build the migration binary with `make porter-migrate` or the root `make build` target.
4. Keep TLS enabled for remote API access. Missing credentials, an unreachable database, or a missing migration must stop startup without applying schema changes automatically.
5. Run `systemctl start porter-migrate.service` and verify it succeeds before starting or restarting `porter.service`. The migrator applies migrations explicitly and is safe to rerun against the intended database.
6. Start `porter.service`. It must connect to the same PostgreSQL database, verify schema through migration 0012, then serve the PostgreSQL API.

The current `scripts/install-porter.sh` is not yet compliant: it installs only the server binary/unit and does not install the migrator unit or create these credential files. The installer track must update it before treating a shell install as accepted.

## Protected Fleet API contract

Every Fleet route below uses HTTPS. Issue, list, rotation, and the capability probe require the operator Bearer in the `Authorization` header. Enrollment accepts only the short-lived, single-use join Bearer. The issue, enroll, rotate, and capability-probe routes reject query parameters; tokens are never accepted in a URL.

`GET /api/v1/fleet/capabilities` requires a valid admin Bearer and returns `200 OK`, `Cache-Control: no-store`, and exactly this DTO when handlers are mounted:

```json
{
  "join_token_issuance": true,
  "agent_enrollment": true,
  "node_credential_rotation": true
}
```

The flags are derived from the public mux registrations. An absent handler must produce `false`; if the capability handler itself is absent, the route returns `404`. Missing or invalid admin auth returns `401`; plaintext HTTP returns `426`. The UI must enable token controls only after an authenticated `200` with all three flags true. A `401`, `403`, `404`, `426`, `503`, network error, malformed/missing flag, or any false flag disables controls and clears the in-memory admin credential.

The public setup routes are:

| Method and path | Auth | Result |
| --- | --- | --- |
| `POST /api/v1/fleet/join-tokens` | Admin Bearer | One-use, ten-minute join token; `201`, `no-store` |
| `POST /api/v1/fleet/enroll` | One-time join Bearer | Creates the PostgreSQL Fleet row and returns the node credential once; `201`, `no-store` |
| `GET /api/v1/fleet/nodes` | Admin Bearer | PostgreSQL-backed Fleet rows; supports pagination |
| `POST /api/v1/fleet/nodes/{node_id}/credentials` | Admin Bearer | Rotates a node credential; `201`, `no-store` |

Credential rotation has no input fields. It accepts an absent body or exactly `{}` with `Content-Type: application/json` for browser-client compatibility; all nonempty fields and query parameters are rejected.

Issue and rotation requests without or with an invalid admin Bearer return `401`. Unknown, expired, and replayed join tokens return the same generic `401`. The database stores only SHA-256 token hashes, never plaintext. Legacy `/v1` routes return `410`; no request may fall back to SQLite or demo data.

## Browser and agent secret handling

The admin token UI must use a password input. The token stays only in JavaScript memory for the active tab and is sent only in the `Authorization` header for capability, issue, Fleet-list, and rotation requests. Clear it on success/logout, `401`, `403`, `503`, network failure, and page unload. Do not use local/session storage, URLs, browser history, console output, analytics, or persistent UI state for it.

The agent join client contract is `sudo porter-agent join --server https://<control-plane>`. It prompts for the one-time token through hidden `/dev/tty` input by default. An optional `--token-stdin` reads exactly one token line from stdin. Do not add token argv or token-file flags. After enrollment, store the node ID and node credential in protected agent-local state (credential file mode `0600` on Unix); never put the node token in argv, environment, logs, URLs, or telemetry. Do not retry an enrollment when the `201` response may have been lost; recover through the exact Fleet row and a deliberate credential rotation.

## Embedded panel gate

The Go server embeds `porter/web/index.html` through `porter/web/web.go`. The server serves that root only when the bundle declares `<meta name="porter-api-contract" content="postgres-only">`, uses the protected capability and public Fleet routes, and contains no legacy `/v1` calls, demo/sample fallback, or persistent token storage. Otherwise `/` returns a clear `503 Service Unavailable` page and serves none of the embedded legacy assets. Keep the panel unavailable until the Vue-owned candidate is verified; do not make the root appear usable by showing mock rows.
