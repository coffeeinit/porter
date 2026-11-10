# Porter 0.0.1-beta change log

Status: in progress; this is not beta-ready. This branch is the single integration target for reviewed work. No PR to `main` or merge is authorized until the release gates pass.

Target: `coffeeinit/porter:work/porter-0.0.1-beta`

## 2026-11-10 — Branch bootstrap

- Status: candidate branch only; no feature integration in this entry.
- Rationale: create one traceable home for reviewed 0.0.1-beta changes while keeping the dirty Track A and user-approved migration worktrees untouched.
- Exact base: `59f311a7bc1c338ff779cd35b99afe4afc82a0ff` (direct parent `0f773461fe6b0e7eea6c6d23f47e7938ab2d810b`; preserves `e0c3f39` Buildkite beta pipeline and `0f77346` crash-consistent restore).
- Bootstrap commit SHA: recorded in the next changelog-only commit because a Git commit cannot contain its own final SHA.
- Scope: this log and branch-status record only; no application code or dirty-path work included.
- Files: `docs/BETA_CHANGELOG.md`.
- Git author identity: `coffeeinit <coffeeinit@users.noreply.github.com>`.
- Implementing agent: Porter Agent (coordinating the branch bootstrap).
- Reviewer: not yet reviewed; this is documentation-only branch setup.
- Owner approval: owner authorized the single branch to be created and pushed as an in-progress candidate; not beta-ready.
- Checks: pending commit-level `git show --check`, clean-worktree check, remote-ref recheck, and push confirmation.

### Open release gates

- PostgreSQL must be the sole global control-plane source of truth; production startup, API, gateway/ACME, CLI, and background loops must have no global SQLite fallback. Agent-local SQLite may remain only as a minimal recovery journal.
- Stable instance identity and positive monotonic generation must fence commands, results, and telemetry end to end; one active instance per replica must be enforced.
- Startup must require an explicit nonblank admin token in development and production and never generate or log it.
- BuildKit must run only in an isolated disposable Firecracker builder guest; no host Docker/container runtime or host-side BuildKit fallback. Actual builder and artifact acceptance on KVM remains required.
- Agent enrollment, recovery journal permissions, restart/reconcile behavior, and local Firecracker socket ownership need end-to-end validation.
- Lifecycle, placement, replica repair, drain, autoscale/wake, health-gated rollout, private networking, domains/DNS/TLS, and load balancing need working PostgreSQL-backed flows and acceptance coverage.
- Backup/restore is crash-consistent only; checksums, exclusive writable-volume ownership, rollback, and a complete recovery drill are required. Application-consistent snapshots are not promised.
- Vue must be the active truthful product UI; rerun tests/build and verify its routes against the final API contract.
- Real KVM/Firecracker/Jailer acceptance is mandatory. TLS/DNS/ACME, proxy firewall restrictions, migration/import/rollback rehearsal, and release hardening remain open.

Portable tests alone do not establish beta readiness. Do not merge to `main` until every applicable release gate is evidenced.
