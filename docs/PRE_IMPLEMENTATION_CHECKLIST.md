# Pre-Implementation Checklist

These decisions should be fixed before writing the V1 backend.

## 1. Deployment topology

- [x] Porter runs as an unprivileged host systemd service.
- [x] Every node runs the same `porter-agent`.
- [x] First host runs Porter + agent.
- [x] Additional hosts run agent only.
- [x] Porter inside a MicroVM is deferred.
- [x] Agent is the only privileged host component.

## 2. Repository and binaries

- [x] `porter/` Go module -> `porter` binary.
- [x] `porter-agent/` Go module -> `porter-agent` binary.
- [x] `protocol/` shared Go module.
- [ ] Root Makefile targets.
- [ ] systemd units for Porter and agent.
- [ ] install scripts.
- [ ] Dockerfiles (optional for V1).
- [ ] version/build metadata format.

## 3. Networking contract

- [ ] Porter default ports: `:8080` HTTP, `:443` HTTPS later.
- [ ] Agent outbound WebSocket path: `/v1/fleet/stream`.
- [ ] Local agent connection: Unix socket path.
- [ ] Remote agent TLS hostname and certificate model.
- [ ] Reverse-proxy/Caddy assumption or built-in TLS.
- [ ] Host bridge name and default CIDR.
- [ ] Per-node / per-project IPAM strategy.
- [ ] Gateway traffic path from host to VM.
- [ ] WireGuard optional port/CIDR.

## 4. Bootstrap contract

- [ ] First-host installer command.
- [ ] Where Porter config lives: `/etc/porter/porter.toml`.
- [ ] Where Porter data lives: `/var/lib/porter/porter.db`.
- [ ] Where agent config lives: `/etc/porter-agent/agent.toml`.
- [ ] Where agent data lives: `/var/lib/porter-agent/porter-agent.db`.
- [ ] First-node local bootstrap token mechanism.
- [ ] Admin password/API token generation and one-time display.
- [ ] Idempotent re-run behavior.
- [ ] Uninstall/cleanup behavior.

## 5. Identity and security

- [ ] Join token format and TTL.
- [ ] Ed25519 agent keypair.
- [ ] Client certificate issuance/rotation.
- [ ] Local agent auth (Unix socket permissions or mTLS).
- [ ] Admin auth: session cookie + API token.
- [ ] CSRF policy.
- [ ] Secret encryption key management.
- [ ] Rate limits.
- [ ] Signed agent binaries.
- [ ] Audit/event redaction rules.

## 6. Data model

- [ ] exact SQLite schema for `porter.db`.
- [ ] exact SQLite schema for `porter-agent.db`.
- [ ] migration framework.
- [ ] resource ID conventions.
- [ ] generation/observedGeneration fields.
- [ ] operation state machine.
- [ ] event schema.
- [ ] retention policies.

## 7. Runtime contract

- [ ] `VMApplySpec` fields.
- [ ] VM state machine.
- [ ] Firecracker API sequence.
- [ ] rootfs/kernel paths.
- [ ] TAP/network spec.
- [ ] volume attachment spec.
- [ ] healthcheck spec.
- [ ] serial log location.
- [ ] systemd unit naming.
- [ ] VM stop/delete semantics.
- [ ] crash recovery behavior.

## 8. Scheduler

- [ ] node capacity accounting formula.
- [ ] filter rules.
- [ ] score rules.
- [ ] placement persistence.
- [ ] cordon/drain semantics.
- [ ] reschedule on node failure.
- [ ] disruption budget.
- [ ] resource overcommit policy.

## 9. Controller model

- [ ] reconcile intervals.
- [ ] retry/backoff.
- [ ] operation idempotency.
- [ ] rolling update algorithm.
- [ ] health failure recovery.
- [ ] conditions vocabulary.
- [ ] max retry / crash-loop policy.

## 10. API surface P0

```text
GET  /healthz
POST /v1/auth/login
GET  /v1/projects
POST /v1/projects
GET  /v1/projects/{id}/apps
POST /v1/projects/{id}/apps
POST /v1/apps/{id}/deployments
GET  /v1/deployments/{id}
POST /v1/deployments/{id}/promote
POST /v1/deployments/{id}/rollback
POST /v1/replicas/{id}/scale
GET  /v1/fleet/nodes
POST /v1/fleet/join-tokens
POST /v1/fleet/enroll
GET  /v1/fleet/stream
GET  /v1/operations/{id}
GET  /v1/events
```

- [ ] request/response schemas.
- [ ] error format.
- [ ] pagination.
- [ ] auth requirements.
- [ ] OpenAPI generation.

## 11. Build/deploy path

- [ ] prebuilt OCI/rootfs deploy first.
- [ ] Git webhook + BuildKit later.
- [ ] image digest/caching.
- [ ] build logs.
- [ ] preview URL generation.
- [ ] deployment protection P1.
- [ ] rollback.

## 12. Observability

- [ ] structured logs.
- [ ] request IDs.
- [ ] operation events.
- [ ] agent logs.
- [ ] VM serial logs.
- [ ] metrics storage/retention.
- [ ] doctor output format.

## 13. Testing gate

- [ ] unit tests.
- [ ] SQLite migration tests.
- [ ] protocol compatibility tests.
- [ ] join-token replay tests.
- [ ] agent reconnect tests.
- [ ] VM lifecycle idempotency tests.
- [ ] Porter restart recovery test.
- [ ] host reboot recovery test.
- [ ] scheduler placement test.
- [ ] end-to-end fresh host test.

## 14. Documentation gate

- [x] SRS.
- [x] UI feature inventory.
- [x] architecture.
- [x] agent model.
- [x] control-plane placement ADR.
- [x] controller model.
- [x] protocol draft.
- [x] connectivity plan.
- [x] robustness plan.
- [ ] final API schema.
- [ ] final database schema.
- [ ] operations runbook.
