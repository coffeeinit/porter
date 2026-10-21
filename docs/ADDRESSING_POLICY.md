# Addressing Policy — Domains Only

## Rule

Porter users never see or manage:

- VM IPs
- VM ports
- host ports
- TAP names
- Firecracker sockets
- container ports

Users manage:

- domains
- preview URLs
- slugs
- deployment IDs
- replica IDs
- application names

## Public examples

Correct:

```text
edge-api.porter.local
pr-142-edge-api.porter.local
api.example.com
```

Incorrect:

```text
10.42.0.7
10.42.0.7:8080
192.168.1.10:3000
localhost:3000
```

## API/MCP/CLI rule

Public API responses and MCP tools MUST NOT include:

```text
guest_ip
ip_address
private_ip
port
host_port
tap_name
socket
```

Use:

```text
replica_id
deployment_id
hostname
preview_url
slug
status
health
```

## Gateway rule

Gateway request logs record:

```text
host
path
method
status
duration
bytes
replica_id
```

Not:

```text
ip
port
```

## UI rule

Node and replica pages show:

- node name
- region
- state
- capacity
- replica ID
- deployment ID
- health
- requested domain
- preview URL
- logs

They do not show VM IPs or ports.

## Internal implementation note

Private networking is still required internally:

```text
Gateway -> private route target -> Firecracker guest socket
Agent -> private IP/port
```

That is an implementation detail. It must never appear in:

- public API JSON
- MCP tool output
- UI
- CLI output
- user-facing logs
- docs/examples
- error messages

## Debugging exception

Admin-only doctor output MAY expose redacted internal diagnostics, but must:

- require an admin capability
- never be returned to normal project views
- never be passed to MCP/AI tools by default
