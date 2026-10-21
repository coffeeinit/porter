# Porter Plan v2 — Current Roadmap

**Status:** active plan after implementation audit  
**No code changes in this document.**  
See `PLAN_AUDIT.md` for old-vs-current corrections.

## Phase 1 — Gateway completion

Goal: public traffic works through domains only.

Required:

- ACME/Let's Encrypt
- wildcard preview TLS
- WebSocket proxy support
- health-aware LB with real health status
- canary by deployment version
- rate limits/timeouts/retries
- bandwidth and status charts
- trace waterfall

Acceptance:

```text
domain -> Gateway -> healthy VM -> app response
preview URL -> ephemeral VM -> response
no VM IP/port in UI/API
```

## Phase 2 — Guest app startup + health

Required:

- inject `porter-guest-init` into every image
- MMDS metadata for env/network
- app entrypoint handling
- HTTP/TCP health probes
- health state drives gateway rotation
- restart-on-failure

Acceptance:

```text
build app -> boot VM -> guest configures network
-> app starts -> HTTP health passes -> traffic enabled
```

## Phase 3 — Multi-page UI

Required pages:

```text
/
/projects
/projects/:id
/deployments/:id
/replicas/:id
/fleet
/fleet/:id
/gateway
/storage
/observability
/images
/settings
```

Required visuals:

- Vercel-style deployment timeline
- request/latency/error charts
- bandwidth in/out
- CPU/memory/disk per VM
- network trace waterfall
- live logs
- build logs

Acceptance:

- deep links work
- browser back/forward works
- no monolithic home page
- no external CDN dependency for core UI

## Phase 4 — Build pipeline hardening

Implemented now:

- Dockerfile/Docker/OCI/Git/Compose-primary

Remaining:

- async builds with operations
- build log streaming
- cancel build
- build cache
- builder MicroVM isolation
- SBOM/vulnerability scan/signing
- multi-service Compose
- GitHub App/webhook-triggered build

## Phase 5 — Persistence and recovery

Required:

- host-file volume resize
- snapshot restore E2E
- backup to object store
- restore verification
- volume orphan cleanup
- disaster recovery runbook

## Phase 6 — Production hardening

- CI and test gates
- rate limiting
- certificate rotation
- agent self-update
- Porter SQLite backup/restore
- migration tests
- security review
- benchmark at 50 nodes / 1000 VMs

## Order of work

1. Guest app startup + health
2. Gateway completion
3. Multi-page UI
4. Async builds + build logs
5. Persistence recovery
6. Production hardening
