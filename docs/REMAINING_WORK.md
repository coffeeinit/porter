# Remaining Production Work — Current

## Completed

- gateway host routing
- health-aware weighted replica selection
- automatic preview domain API
- optional TLS cert/key listener
- request logs, bytes, latency
- activity tracking from gateway
- fleet node detail APIs, metrics, logs, VMs
- cordon / uncordon / drain
- MCP tools for gateway, domains, fleet detail, previews
- guest-init binary
- image catalog records
- Dockerfile/Docker/OCI/Git/Compose-primary image build
- guest-init injection into built rootfs
- volume resize API
- replica traffic weight API
- snapshot restore endpoint + agent restore command

## Not complete

### Image/build
- async builds + operations
- build log streaming
- build cache
- builder MicroVM isolation
- multi-service Compose
- GitHub App/webhook-triggered builds
- SBOM/vulnerability scan/signing

### UI
- true multi-page routing
- project/deployment/replica detail pages
- Vercel-style charts/traces
- live log streaming

### Networking/health
- real ACME automation
- wildcard TLS
- HTTP/TCP guest health probes
- guest app startup verification

### Persistence
- host-file volume resize
- snapshot restore verification
- backup to object store
- restore verification
- disaster recovery

## Next implementation order

1. Guest app startup + real health checks
2. Gateway completion: ACME, wildcard TLS, WebSocket, canary
3. Multi-page UI
4. Async builds + build logs
5. Backup/restore verification
