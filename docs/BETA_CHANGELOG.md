# Porter 0.0.1-beta change log

Status: in progress; this is not beta-ready. This is the single integration branch for reviewed work. No PR to `main` or merge is authorized until the release gates pass.

Target: `coffeeinit/porter:work/porter-0.0.1-beta`

## 2026-11-10 — Branch bootstrap

- Status: candidate branch only; no feature integration in this entry.
- Rationale: create one traceable home for reviewed 0.0.1-beta changes while keeping the dirty Track A and user-approved migration worktrees untouched.
- Exact base: `59f311a7bc1c338ff779cd35b99afe4afc82a0ff` (direct parent `0f773461fe6b0e7eea6c6d23f47e7938ab2d810b`; preserves `e0c3f39` Buildkite beta pipeline and `0f77346` crash-consistent restore).
- Bootstrap commit SHA: `fc1d6825e801f0088c20b74a9534a4fd0605869f`.
- Scope: this log and branch-status record only; no application code or dirty-path work included.
- Files: `docs/BETA_CHANGELOG.md`.
- Git author identity: `coffeeinit <coffeeinit@users.noreply.github.com>`.
- Implementing agent: Porter Agent (branch bootstrap).
- Reviewer: not yet reviewed; documentation-only branch setup.
- Owner approval: owner authorized this single branch to be created and pushed as an in-progress candidate; it is not beta-ready.
- First published branch tip: `6b8fb7a4b02c46f21e1aeefd5bcd1fc9db745143`; GitHub reported the new ref and `git ls-remote` matched this exact SHA. Branch URL: `https://github.com/coffeeinit/porter/tree/work/porter-0.0.1-beta`.
- Checks: `git diff --check` and `git show --check` passed; the separate bootstrap worktree was clean. The remote ref was absent before creation, and the first push was non-forced. This changelog update will advance the same branch normally.

## 2026-11-10 — BuildKit boundary candidate review (not integrated)

- Candidate commit: `cd22211693eebac4f88cc12b4b18518863ab5a30`, direct child of the approved base `59f311a7bc1c338ff779cd35b99afe4afc82a0ff`.
- Scope: fail-closed BuildKit/OCI boundary only; not a working builder or bootable kernel/rootfs artifact pipeline. No host Docker, Podman, containerd, runc, shell, or host BuildKit fallback was found.
- Implementer: Developer (`G9CqmiepH6KGSQ6XyCem5M`); separate work task `uS3XIT9xgSWZsXPCBJD6CE`.
- Independent reviewer: `8Tz8Rf2S2HqfDemDUYNEXB`.
- Tests reported by the implementer: `make test`, `make vet`, `go test -race ./internal/buildruntime`, `git show --check` passed. Independent reviewer also passed `go test ./internal/buildruntime`, `go test ./...` in `porter-agent`, and `go vet ./...`.
- Review findings: P2 cleanup may lose the destroy window when Stop consumes a shared deadline; partial launch may leak if Launch returns `(nil, err)`; `/dev/null` can pass the character-device-only KVM preflight. P3 docs overstate timeout/cancellation coverage; temp-filesystem tests fail under world-writable `/tmp` because they depend on checkout ancestry.
- Decision: do not integrate yet. Developer is fixing the findings; independent re-review is required before this candidate can be considered.
- Limitations: `/dev/kvm`, Firecracker, Jailer, `buildkitd`, `buildctl`, and an approved guest image are unavailable here. No real BuildKit, registry, Firecracker, or boot acceptance ran; real KVM/builder acceptance remains open.

### Open release gates

- PostgreSQL must be the sole global control-plane source of truth; production startup, API, gateway/ACME, CLI, and background loops must have no global SQLite fallback. Agent-local SQLite may remain only as a minimal recovery journal.
- Stable instance identity and positive monotonic generation must fence commands, results, and telemetry end to end; one active instance per replica must be enforced.
- Startup must require an explicit nonblank admin token in development and production and never generate or log it.
- BuildKit must run only in an isolated disposable Firecracker builder guest; no host Docker/container-runtime or host-side BuildKit fallback. Real builder and artifact acceptance on KVM remains required.
- Agent enrollment, recovery journal permissions, restart/reconcile behavior, and local Firecracker socket ownership need end-to-end validation.
- Lifecycle, placement, replica repair, drain, autoscale/wake, health-gated rollout, private networking, domains/DNS/TLS, and load balancing need working PostgreSQL-backed flows and acceptance coverage.
- Backup/restore is crash-consistent only; checksums, exclusive writable-volume ownership, rollback, and a complete recovery drill are required. Application-consistent snapshots are not promised.
- Vue must be the active truthful product UI; rerun tests/build and verify its routes against the final API contract.
- Real KVM/Firecracker/Jailer acceptance is mandatory. TLS/DNS/ACME, proxy firewall restrictions, migration/import/rollback rehearsal, and release hardening remain open.

Portable tests alone do not establish beta readiness. Do not merge to `main` until every applicable release gate is evidenced.
