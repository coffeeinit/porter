# Next Tasks

## Priority 1 — Guest app startup + health

- inject `porter-guest-init` into every built rootfs
- pass network/env via kernel args or MMDS
- start app entrypoint
- HTTP/TCP health probes
- health state drives gateway rotation

## Priority 2 — Finish gateway

- ACME/Let's Encrypt
- wildcard preview TLS
- WebSocket support
- canary by deployment version
- timeout/retry/rate limits
- bandwidth + trace charts

## Priority 3 — Multi-page UI

- Projects list
- Project detail tabs
- Deployment detail
- Replica detail
- Fleet + node detail
- Gateway
- Storage
- Observability
- Images
- Settings

## Priority 4 — Build hardening

- async builds with operation/progress
- build log streaming
- cancel build
- build cache
- builder MicroVM isolation
- multi-service Compose
- GitHub App/webhook builds

## Priority 5 — Persistence recovery

- host-file volume resize
- snapshot restore E2E
- backups to object store
- restore verification

## First command tomorrow

```bash
cd /mnt/d/github/porter
make test
make build
```
