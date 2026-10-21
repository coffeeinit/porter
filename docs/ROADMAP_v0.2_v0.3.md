# Roadmap v0.2.0 → v0.3.0

**Direction:** MicroVM platform with Vercel/Coolify-like developer experience.  
**Explicitly out of scope:** customers, billing, subscriptions, resellers, marketplace, compliance audit.

## Working method

One small vertical slice at a time:

1. implement
2. run locally in WSL
3. verify the exact acceptance path
4. commit
5. tag a patch release if stable

No big-bang feature bundles.

---

# v0.1.x — stabilization

## v0.1.3 — first true app runtime

Goal:

```text
built image -> boot VM -> guest-init -> app starts
-> HTTP health passes -> Porter healthy -> gateway routes
```

Tasks:

- build a simple HTTP app image
- boot it with `init=/sbin/porter-guest-init`
- configure guest network
- start app entrypoint
- HTTP probe from agent
- Porter marks replica healthy
- gateway serves app by domain
- document exact test command

Acceptance:

```bash
curl http://<preview-domain>/   # returns app response
```

This is the most important next milestone.

---

# v0.2.0 — real deployable platform

Only features needed for a Coolify/Vercel-like single-operator platform.

## 1. Runtime & deploy
- working app startup
- HTTP/TCP health checks
- restart on failure
- deployment logs
- rollback to previous image
- immutable deployment records

## 2. Build
- async image builds
- build log streaming/storage
- build status: queued/building/ready/failed
- cancel build
- build cache
- GitHub repo build
- Dockerfile, OCI, Docker, Compose-primary, framework detection

## 3. UI
Routed pages:

```text
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

Core Vercel-style visuals:

- deployment timeline
- request rate
- p50/p95/p99 latency
- error rate
- bandwidth in/out
- CPU/memory per replica
- build logs
- serial logs
- health timeline

## 4. Gateway
- preview domains
- custom domains
- ACME/Let's Encrypt
- wildcard preview TLS
- health-aware LB
- WebSocket support
- basic canary by deployment
- rate limits/timeouts

## 5. Config
- env vars
- secrets
- project settings
- persistence mode UI
- vCPU/memory/disk/volume settings

## v0.2.0 acceptance

One operator can:

```text
connect GitHub repo
  -> detect framework
  -> build image
  -> deploy MicroVM
  -> pass health
  -> serve on a domain
  -> view logs/metrics
  -> rollback
```

---

# v0.3.0 — production-ready single operator

## 1. Reliability
- snapshot restore E2E
- host-file volume resize
- backups + restore verification
- node drain/reschedule
- rolling deployment
- canary promotion
- failed build rollback
- agent reconnect recovery
- control-plane SQLite backup/restore

## 2. Developer experience
- CLI:
  ```text
  porter deploy
  porter logs
  porter scale
  porter rollback
  porter fleet
  ```
- one-click templates: Laravel, Next.js, React, Node, static
- GitHub webhook-triggered deploy
- preview cleanup/TTL
- env/secrets rotation
- API tokens

## 3. Fleet operations
- node detail UI
- node metrics charts
- node logs
- cordon/drain UI
- agent version/upgrade page
- capacity/cost estimate (non-billing)

## 4. Observability
- per-VM CPU/memory/disk/network
- gateway bandwidth
- request trace waterfall
- live logs
- alerts: node down, deploy failed, cert expiring
- doctor page

## v0.3.0 acceptance

One operator can:

```text
add node
  -> deploy real app
  -> update app
  -> rollback
  -> scale
  -> snapshot
  -> restore
  -> backup
  -> observe
  -> upgrade agent
```

---

# Order of work

1. v0.1.3 — app boot + HTTP health + gateway
2. v0.2.0a — routed UI skeleton
3. v0.2.0b — async builds + build logs
4. v0.2.0c — ACME + custom domains + preview TLS
5. v0.2.0d — env/secrets + rollback + persistence UI
6. v0.3.0a — backups/restore + snapshot verification
7. v0.3.0b — CLI + GitHub webhook + templates
8. v0.3.0c — observability/traces/alerts
9. v0.3.0d — fleet upgrades + hardening

## First task tomorrow

v0.1.3 only:

```text
build simple HTTP app image
-> boot with guest-init
-> app responds
-> health becomes healthy
-> gateway serves domain
```
