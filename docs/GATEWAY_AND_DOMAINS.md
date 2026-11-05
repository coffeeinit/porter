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
  verification_token,
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

## Custom-domain verification

Custom domains stay `pending` and receive no gateway route until their DNS TXT
proof matches. Create one with `POST /v1/projects/{id}/domains`:

```json
{"app_id":"app_...","hostname":"www.example.com","kind":"custom"}
```

The response includes `verification_record` and `verification_token`. Add that
TXT record in the domain's DNS zone, and point the hostname to the Porter
gateway using the address or CNAME configured by the operator. Then call:

```text
POST /v1/domains/{id}/verify
```

Porter checks `_porter-verify.<hostname>` for the matching TXT value. A missing
or incorrect record returns `409`; once it matches, the domain becomes active
and Porter creates its host route. Platform-owned `PORTER_BASE_DOMAIN` and
preview hostnames are managed by Porter and do not need a customer TXT proof.
The manual `POST /v1/routes` endpoint also requires an active domain mapped to
the same app, so it cannot bypass verification.
Use ASCII DNS labels (encode international names as punycode). Existing domains
that were already active before this change are not automatically re-verified;
review those records before exposing the gateway to public traffic.

The gateway can use static TLS certificate/key files or optional HTTP-01 ACME
through `PORTER_ACME_DOMAINS`. The ACME path has not been validated against a
public domain. DNS-provider automation (including Cloudflare), DNS-01, wildcard
certificates, and production certificate-lifecycle acceptance remain unfinished.

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

The current implementation uses Go's `httputil.NewSingleHostReverseProxy` with weighted round-robin selection over healthy replicas. It normalizes hostnames, honors configured path prefixes, rejects non-private upstream IPs, reuses upstream connections, propagates request IDs, and records status/latency/bytes/replica. TLS termination is available through static certificate/key files or optional HTTP-01 autocert, but public ACME acceptance is unverified. Provider DNS automation, DNS-01/wildcard issuance, connection draining, rate limits, retries, and live WebSocket/streaming acceptance are not complete; this is not yet a production edge service.

The active UI now shows the TXT ownership record and value for pending custom domains and can call the existing verification endpoint. Operators still create the TXT and A/CNAME records in their DNS provider by hand; automatic Cloudflare or other provider connections are not implemented.

## Future gateway work

- DNS provider adapter interface, starting with Cloudflare, plus manual/external DNS mapping
- DNS-01 and wildcard certificates, plus public ACME lifecycle acceptance
- canary/A-B traffic policy with health-gated promotion
- WebSocket support
- rate limits
- connection draining and bounded upstream timeouts/recovery
- custom errors/redirects
- bandwidth charts
- trace waterfall
- Fly/Vercel-style global edge placement later

## Ephemeral clarification

Ephemeral does not mean rebuild-per-request.

It means local disk is not retained. The immutable image remains available and
can be restarted quickly. Static/React/small stateless sites can use ephemeral
mode in production.
