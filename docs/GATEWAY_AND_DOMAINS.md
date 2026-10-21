# Gateway, Domains, and Public UX

## Rule

Users should never manage VM IPs or ports.

They manage:

```text
domain
preview URL
route
certificate
deployment
```

Internal VM IPs/ports are implementation details.

## Gateway architecture

```text
Internet
  -> Porter Gateway
       hostname / path routing
       -> healthy replica
       -> MicroVM private IP:port
```

The UI/API return:

```text
app.porter.local
pr-142-app.porter.local
custom.example.com
```

Never:

```text
10.42.0.7:8080
```

## Domain model

```text
domains(
  id,
  project_id,
  app_id,
  hostname,
  kind,       -- production | preview | custom
  status,     -- pending | active | failed
  created_at
)
```

Routes are derived from domains:

```text
hostname -> app -> healthy replicas
```

## API

```text
GET  /v1/domains
GET  /v1/projects/{id}/domains
POST /v1/projects/{id}/domains
POST /v1/domains/{id}/verify
GET  /v1/routes
POST /v1/routes
GET  /v1/gateway/stats
GET  /v1/gateway/requests
```

## Default domains

When an app is created and `PORTER_BASE_DOMAIN` is configured:

```text
<app-slug>.<base-domain>
```

Example:

```text
edge-api.porter.local
```

Preview:

```text
pr-142-edge-api.porter.local
```

## Gateway request pipeline

```text
request
  -> request ID
  -> host/path route
  -> select healthy replica
  -> proxy
  -> record status, latency, bytes, replica
  -> update app activity
  -> update autoscale signal
```

## Future gateway work

- TLS/ACME
- wildcard certificates
- canary weights
- WebSocket support
- rate limits
- custom errors/redirects
- bandwidth charts
- trace waterfall
- Fly/Vercel-style global edge placement later

## Ephemeral clarification

Ephemeral does not mean rebuild-per-request.

It means local disk is not retained. The immutable image remains available and
can be restarted quickly. Static/React/small stateless sites can use ephemeral
mode in production.
