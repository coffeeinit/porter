# Porter focused implementation plan

**Baseline:** `7adbfc983f4df15770cbbecc32a88d17e9be236b`
**Status:** Working V1 plan for the owner's stated product goal; deployment defaults remain explicit until verified.
**Worktree:** Changes are committed to `work/porter-foundation-plan` only; no upstream PR until V1 gates pass.

Porter is a self-hosted, Kubernetes-inspired platform for Firecracker microVM workloads—not a container platform and not Kubernetes API-compatible. Its user hierarchy is **project/group → app/workload → deployment/release → replica → Firecracker microVM**, placed on agent-managed host nodes. A project is a grouping boundary, not a billing tenant. Preserve the current agent/runtime where it works; close correctness and safety gaps before adding dashboard-only controls.

## Product boundary

**In scope:** project/group organization; apps as desired-state workloads; multiple replicas as separate Firecracker microVMs; healthy replacement and traffic failover; fresh-capacity placement, reconciliation, safe drain and resource-aware horizontal autoscaling; Dockerfile/OCI/Docker and a documented Compose subset as *build/import inputs only*; immutable releases, promotion/rollback, and previews; an inbuilt Go reverse-proxy/load-balancer and traffic telemetry; custom and platform domains, DNS-provider adapters (Cloudflare and a provider interface, plus manual DNS mapping), TLS/ACME lifecycle; idle stop and wake-on-request; storage, retention, recovery; and a polished live operator UX with inbuilt OpenTelemetry-style metrics, logs, and traces. Grafana must not be required; optional OTLP export/integration can be added for operators who want external tools. MCP access is for explicitly scoped AI/automation principals. The owner does not want hosted starter templates or managed databases—users configure and run their own workloads.

**Out of scope for this project:** SaaS billing; customer/reseller accounts; multi-tenant organization RBAC; marketplace; fully autonomous AI execution without scoped permissions or required approvals; Kubernetes API compatibility; and roadmap work focused on cloning or integrating unrelated AGPLv3 self-hosted applications. Porter still needs basic separation of operator, node-agent, scoped CI credentials, and AI/MCP principals; that is not SaaS multi-tenant RBAC.

## Reference-project direction

Use the owner's shortlist as source-level pattern references: Overalls and E2B Runtime for multi-host/isolated execution; FireCrab and nibrunner for the single-host VM/app workflow; Flintlock, Brigade, and Battery for lifecycle, scheduling, and pre-warmed capacity; MicroVM.nix for declarative machine definitions; and Ephemera, Fireagent, Appstrate, and Husker for focused comparison only. The uploaded notes explicitly say the list is not exhaustive and some projects need maintenance verification. These are evaluation targets, not dependencies or claims that Porter owns their features. The root [`porternew_observability.html`](../porternew_observability.html) is the owner's detailed 1,697-line Alpine UX concept; it declares mock mode and has no API client. Its seeded records, random charts, fake request/log streams, and action toasts are not live data or working operations. Use its project→release→replica drill-down, fleet, observability, and AI-operator layouts as design references. V1 treats a project as the workload grouping boundary; the concept has no separate group/team workflow. Validate the mock's conflicting primary/canary flags and traffic weights before implementing those policies. The active UI is `porter/web/index.html`; do not transplant fake success. Keep canary/A-B, AI execution, guest console, migration, snapshots, certificate actions, and other unwired controls preview-only until backend acceptance tests exist.

## Inbuilt observability target — OpenTelemetry, no Grafana requirement

Use OpenTelemetry (OTel/OTLP) conventions to instrument the Porter control plane, agent, gateway, image/deploy lifecycle and host/microVM health. The product itself must ingest/query/store useful metrics, logs and traces and render them in the embedded Porter UI; do not require a separate Grafana installation. Optional OTLP export to Grafana or another observability stack can be a later operator choice. Application-level spans are available when customer workloads send OTLP to Porter; platform and gateway telemetry must work without app instrumentation. Link resource attributes such as service, project, release, replica, node and environment, and propagate trace/span context across ingress→gateway→guest where the application supports it.

The active code already has node metric history, gateway request/stat records, and UI charts. Its `/v1/traces` endpoint currently returns gateway request summaries with a request ID; it is not a distributed trace/span store. These are separate features—not a unified OTel pipeline or production-ready retention system. Define an authenticated OTLP/HTTP and/or gRPC ingest contract, internal storage/query API, time range and filter semantics, percentiles/histograms, correlated structured logs and trace waterfalls, retention/downsampling, label-cardinality limits, secret/PII redaction, and collector/storage outage behavior before advertising the suite. **Exit:** a clean install shows node, app, release, replica and gateway health/traffic plus correlated logs/traces in the Porter UI, with no Grafana process; OTLP export is optional and tests verify ingest→query→UI correlation, accuracy, authorization, retention and outage behavior.

## Authorization: reuse capability behavior, not legacy tenancy

The repository does contain RBAC code under `legacy/backend`: `internal/rbac/rbac.go` is a storage-agnostic capability resolver with ancestor inheritance and deny-overrides-allow, and its focused package tests pass with `GOWORK=off`. The legacy API has a route-to-permission table, but it is a separate Go 1.25/PostgreSQL application. Its scoped schema includes reseller/org/team tenancy; its assignment handler currently restricts grants to `principal_type=user`; its MCP handler records the signed-in user as the actor; and its AI-agent management routes return `501`. These are not active microVM-service features.

The active `porter` service now has separate, revocable MCP bearer tokens, hashed at rest, with expiry, per-tool capabilities and project/app/node scopes; list results are filtered to those scopes, unknown tools fail closed, calls record principal/tool/outcome without arguments, and the operator UI can create/revoke tokens and reveal a new token once. The human operator's admin path and node machine identity remain separate. Host-impacting tools stay operator-only until an approval workflow exists. Direct REST scopes and an approval queue remain open; this is not a full inherited-role/deny-overrides resolver. Do not import legacy reseller/customer/org tables or imply that the legacy AI-agent endpoints are active.

## Communication model: secure stream, local Firecracker socket

The current agent still sends JSON over request/response HTTP: it posts heartbeats and fetches commands on separate two-second ticks. The agent now rejects plaintext remote control-plane URLs and allows HTTP only to loopback for development. Standard HTTPS certificate verification is enabled. There is still no mTLS agent identity/stream; bearer tokens over HTTPS are a partial transport hardening, not the production target.

**Recommended target:** agent-initiated, long-lived **outbound mTLS WebSocket** to the control plane. The node opens the connection, so Porter does not need to expose an inbound port on each node. Send capacity/health leases and command acknowledgements over the stream; the control plane pushes queued work instead of making the agent fetch commands every two seconds. This follows the useful Kubernetes pattern—kubelets authenticate outward to the API server, maintain leases, and watch for changes—without claiming Kubernetes API compatibility or copying its protocol.

The analogy is specifically about secure node-to-control-plane communication, streamed change notifications, and heartbeat leases: see Kubernetes' [node/control-plane communication guidance](https://kubernetes.io/docs/concepts/architecture/control-plane-node-communication/), [API watch model](https://kubernetes.io/docs/reference/using-api/api-concepts/), and [Lease heartbeat model](https://kubernetes.io/docs/concepts/architecture/leases/). Kubernetes also documents separate API-server-to-kubelet paths for logs, exec, and port-forwarding; Porter can avoid an exposed node API by sending telemetry over its agent-initiated stream. This is a design pattern, not protocol compatibility.

The stream must be backed by durable control-plane state, not treated as the source of truth. Use versioned envelopes, node certificate identity, command/operation IDs, deadlines, explicit accepted/started/completed acknowledgements, bounded retries, idempotent handlers, reconnect backoff with jitter, and replay/deduplication after disconnect. Enroll with a short-lived, one-use token; issue revocable node-specific certificates; validate the server certificate; document certificate rotation and emergency revocation. Missing, expired, or revoked identity fails closed. Do not add an insecure plaintext fallback. If streaming cannot land in one step, a temporary HTTPS long-poll may be used only with mTLS and explicit removal criteria.

**Firecracker control is local, not networked:** the existing agent launches Firecracker with a per-VM Unix-domain API socket and sends its API requests over that socket. Keep that fast local boundary. The control plane sends an authenticated operation to the agent; it must not open or proxy Firecracker sockets over the network. Restrict socket and VM-directory ownership/permissions, validate VM IDs and paths, and let only the local agent manage Firecracker. Network traffic remains for agent-to-control-plane control messages, guest networking, and application traffic.

## Release blockers and constraints

1. **No hardware verification here:** this sandbox has no `/dev/kvm`, `firecracker`, or `jailer`. VM launch, guest readiness, NAT, isolation, and snapshot tests require a KVM-capable CI/host.
2. **Legacy E2E scripts are disabled:** they used host-specific paths, shared `/tmp` state, broad `pkill`, and incomplete assertions. The current opt-in KVM lane only applies and stops one real Firecracker VM; a safe portable API-to-agent integration harness and broader KVM acceptance are still open.
3. **Build isolation is not proven:** image API/MCP builds and signed GitHub webhooks now fail closed by default because the builder still calls host Docker/Git. `PORTER_ENABLE_UNISOLATED_BUILDS=true` is an explicit temporary override for trusted sources only; it is not a sandbox. The imagebuilder now has adversarial path/archive checks and verified SHA checkout, but builder resource isolation, secret boundaries, egress controls, full OCI validation, and host-canary tests remain open.
4. **Control-loop gaps block promises:** a controller tick now marks expired-heartbeat nodes offline and new placement rejects nodes older than 15 seconds. Heartbeats preserve cordon/drain intent; accounting includes active replica transitions and releases capacity after successful deletion. The system still does not recover replicas/capacity after controller or agent restart, provide lease-based reservations, complete safe drain, or repair all lost-node workloads. Command-result identity/leases and healthy-only gateway selection are covered, but these are not full reconciliation.
5. **Host security/data guarantees are incomplete:** agent privilege/Jailer profile, encrypted-at-rest secrets, network egress policy, backup/restore scope, and volume behavior need explicit acceptance criteria.
6. **Provider access is operator-owned:** GHCR, GitHub Actions, public DNS, ACME, and off-host backup tests need disposable credentials/zones/accounts. Use fakes for normal CI and opt-in live integration tests.
7. **Scale/performance claims are TBD:** choose and measure tested node/VM limits, cold-start latency, RPO/RTO, and retention before publishing targets.

## Firecracker upstream baseline

The [upstream capability audit](FIRECRACKER_UPSTREAM_AUDIT.md) finds Firecracker v1.17.0 (2026-09-10) as the latest stable release as of 2026-11-05, with no merged PR found after Nov 4. Porter invokes `firecracker` without a version pin, so the actual binary and its feature set are unknown; this is not a proven upgrade delta. Use v1.17.0 as the first pinned candidate only after its host/kernel/KVM requirements pass on Porter’s target systems, and ship the matching jailer and Firecracker binaries together.

Evaluate the released additions selectively: guest Linux 6.18 and x86_64 bzImage support; metrics identity/tags; optional virtio-block geometry/discard; memory hotplug validation; snapshot page-mode and durability controls; and vsock fixes. Keep merged-but-unreleased threaded virtio-block, its dirty-page worker fix, MMDS, vhost and snapshot-state changes disabled until they land in a tagged release or Porter deliberately adopts a verified main build. Firecracker is a per-VM VMM, not Porter’s fleet scheduler, autoscaler, gateway, or distributed tracing system.

## Proposed defaults to use while implementing

- First supported topology: one Linux/KVM host, host-service control plane, local agent; add agent-only fleet nodes after single-node recovery works.
- Firecracker is the only customer application runtime. Docker/OCI are build/import formats, never a container-runtime fallback.
- Pin Firecracker and its matching jailer to exact, recorded artifacts; report version and digest and verify `/dev/kvm`, host architecture, and kernel before claiming support.
- Prefer BuildKit behind an isolated builder interface; host-Docker builds remain disabled by default. The temporary environment override is for trusted sources only, never an untrusted-build boundary.
- V1 prioritizes HTTP applications. Compose is translated from an explicitly reviewed/selected service and supported fields; it is never run on the host with `docker compose up`.
- Target CI flow: GitHub Actions builds and pushes an immutable OCI digest (GHCR supported), then calls Porter with a scoped token. Signed push webhooks now pin the `after` commit SHA, deduplicate deliveries, and create a release without changing the active app image before promotion; the direct Git builder/API and CI-to-digest flow still need full pinning and verification.
- Scale-to-zero initially terminates the microVM to release RAM. A persistent volume survives; ephemeral VM/root scratch does not. The gateway coalesces concurrent wake requests and applies a bounded wait/retry response. Add memory snapshots only after measuring their storage cost and restore safety.
- Porter owns internal service discovery and host-based gateway routes. Custom domains stay pending until TXT ownership is proved; the UI now displays the TXT record and can request verification. Add provider adapters (Cloudflare first, extensible), manual DNS mapping, TLS/ACME issuance/renewal, and wildcard support behind operator-managed secrets. Do not make self-hosted authoritative public DNS a V1 dependency.

## Workable delivery order and exit tests

### 0. Confirm the safety contract

Record supported Linux/kernel/architecture, one-host topology, threat model, network egress defaults, volume semantics, and KVM CI environment. Keep undecided items marked TBD. **Exit:** README, service files, installer, and tests describe the same supported path.

### 1. Repair existing integration seams and establish regression tests — started

Regression-tested slices now include heartbeat preservation of cordon/drain intent; active-replica CPU/memory accounting and deletion release; stale-node offline marking and fresh-node placement; command-result node/operation/attempt binding and leases; masked secrets; healthy-only weighted gateway selection; per-release ready gating; GitHub webhook signature/size/delivery checks and commit-SHA pinning; archive/path checks; and UI fixtures only in anonymous demo mode. MCP credentials are separate, hashed, revocable and expiring, with per-tool capabilities, project/app/node scopes, filtered results, audit records, and operator UI management. Agent `vm.apply` reuses a matching live VM on replay, rejects identity collisions, and reports local persistence failures. The live UI now uses actual project app counts, lets operators select an app and inspect deployments/replicas, masks secret values, and shows the custom-domain TXT challenge. Gateway path-prefix behavior and private-upstream validation have unit tests. **Still open:** controller/agent restart recovery, replica repair, lease-based capacity recovery, safe drain, broader command idempotency, isolated builds, provider DNS/TLS, and end-to-end KVM validation.

### 2. Secure, durable control-plane/agent channel

Remote agent URLs now require HTTPS (plaintext is loopback-only), but the agent still uses two-second heartbeat/command polling and bearer identity. Replace this with an outbound mTLS WebSocket stream; add typed/versioned messages, certificate enrollment/rotation/revocation, durable atomic command claim/ack, operation correlation, idempotency, replay, backpressure, deadlines, and reconnect behavior. Keep the agent as the only Firecracker-socket client. `vm.apply` replay protection exists; other command kinds and controller restart replay remain open. **Exit:** TLS tests reject wrong, absent, expired and revoked credentials; disconnect/reconnect, duplicates, late results and controller restart produce one correct durable operation.

### 3. Reconciliation and resource truth

Heartbeat expiry now marks nodes offline on the controller tick, and placement rejects nodes older than 15 seconds; capacity accounting follows active replica transitions, but reservations are not leased and lost-node capacity/replicas are not fully repaired. Add desired/observed generations and conditions, agent startup inventory, operation recovery and metric timestamps. **Exit:** control-plane and agent restarts converge or expose a stable blocked reason without duplicate VMs; heartbeats cannot override maintenance intent; lost-node replicas are repaired only after safe capacity is confirmed.

### 4. Harden agent execution and the builder boundary

Retain current local Firecracker UDS behavior; enforce Jailer/seccomp/cgroup/least-privilege policy, safe VM/socket paths, guest readiness, and explicit egress. The builder now rejects unsafe archive/path inputs, but still invokes host Docker/Git; API, MCP and webhook build entry points are disabled unless an operator explicitly opts in for trusted sources. Replace that escape hatch with an isolated BuildKit/Moby builder using CPU/memory/disk/time limits, no host Docker socket, no secret access, and controlled egress. Verify OCI manifests, selected architecture, blob digests, symlinks, whiteouts, and workspace containment. **Exit:** sandbox and adversarial tests show no host canary access, secret leakage, or path escape.

### 5. Complete fleet scheduling and lifecycle

Current placement checks heartbeat freshness and free CPU/memory; accounting tracks active replica transitions. Complete allocatable disk, architecture/capability checks, concurrent capacity reservations, safe drain/shutdown, offline repair and agent restart inventory. **Exit:** deterministic multi-node tests cover concurrent scheduling, insufficient capacity, heartbeat loss, maintenance state, restart and drain with local volumes.

### 6. Complete build-to-release inputs

Signed GitHub push webhooks now require a 40-character commit SHA, claim delivery IDs once, and use detached SHA checkout; deployment VM specs resolve kernel/rootfs from the immutable image record. The active release does not have its app-wide image reference changed before the new replicas pass health. Direct Git builds still accept moving refs, and isolated Dockerfile, OCI/GHCR digest, private-registry and reviewed Compose workflows are not complete. **Exit:** fixtures cover moving-branch pinning, corrupt/multi-platform OCI input, private registry auth, Compose ambiguity, unsupported-field errors, source/rootfs digests, and mocked CI-to-deploy flow.

### 7. Readiness-gated rollout, routes, domains, and DNS

Deploys are serialized per app and promote only after the desired number of replicas report healthy; gateway selection uses healthy replicas, honors path prefixes, validates private upstream IPs, and records request metrics. Custom domains remain pending until the `_porter-verify.<hostname>` TXT proof is visible; the live UI now displays the record and can verify it. DNS-provider integration (Cloudflare first, pluggable adapters), manual mapping guidance, TLS/ACME issuance/renewal, wildcard certificates, canary/A-B traffic controls and robust rollback UX remain open. **Exit:** domains cannot route before proof, unhealthy replicas never receive traffic, and live DNS/ACME tests are opt-in.

### 8. Automatic stop/start, autoscale, storage, and retention

Autoscale policy fields and activity/wake endpoints exist, but their end-to-end behavior is not verified as a production autoscaler. Add timestamped workload/gateway metrics, min/max, cooldown, hysteresis, stale-data handling, request coalescing at zero replicas, bounded readiness waits, and retryable wake failures. Specify persistent-volume semantics separately from disposable VM/root state; add quotas and reference-aware garbage collection. **Exit:** fake-clock tests verify stable scaling; gateway tests cover wake success/failure, concurrency, timeout and retention; storage tests never remove referenced artifacts.

### 9. Operator UX and release gates

The large `porternew_observability.html` concept is mock-only. The active UI now uses live API data for project app counts, app selection, release/replica detail, custom-domain TXT proof, and MCP credential management; secret values are masked and demo fixtures stay anonymous-only. The UI labels gateway request records accurately; distributed OpenTelemetry spans and the unified ingest/store/query system are not implemented. Build that inbuilt OTel-compatible system and embedded metrics/logs/traces dashboards; Grafana must remain optional. The concept's canary/A-B, preview, traffic, SSL, logs, analytics, console and operation views are not all live product features. Continue to align CLI/API/docs/installers; add doctor, bounded retention, backup/restore and upgrade/rollback. Run a portable API integration suite in CI and expand the self-hosted KVM release lane beyond its current apply/stop smoke test. **Exit:** clean-host install, no-Grafana telemetry acceptance, full KVM E2E, backup/restore drill, and reproducible tests for every advertised capability.

## Verification matrix

- **Every change:** `make test`, `make vet`, `git diff --check`, focused regression tests; race tests for shared control-plane/store paths.
- **Portable integration:** API → SQLite/store → durable operation → fake agent/runtime → gateway, including auth, duplicates, retry, cancellation, reconnect, restart, and failure injection.
- **Security:** wrong/revoked node identity, replayed command results, archive traversal/symlink/digest fuzzing, secret-marker scans, build canary/egress tests, webhook signature/replay checks.
- **KVM lane:** current gate launches and stops one real Firecracker VM over local UDS; full release acceptance must also cover Jailer, guest readiness, network isolation, snapshots, and resource ceilings.
- **Opt-in provider lane:** GHCR/GitHub Actions, DNS provider, ACME, and off-host backup/restore with disposable credentials.

A unit test, successful build, or historical E2E note is not proof of a hardware- or provider-dependent feature. Report status as **Verified**, **Preview**, **Planned**, or **Not implemented** based on that feature's acceptance test.

## Current verification

- Baseline before changes: `make test` passed on commit `7adbfc9`.
- Latest branch verification: `make test`, `make vet`, `make build`, `go test -race ./internal/store ./internal/api ./internal/gateway ./internal/imagebuilder`, `node --check` on the current live UI script, and `git diff --check` all passed. Focused tests cover fresh-node placement, node state, MCP scopes/token expiry, duplicate `vm.apply`, signed/deduplicated webhooks, commit-SHA checkout from a local Git repo, build default-off guard, per-release kernel/rootfs resolution, TXT domain proof, healthy replica selection, and gateway host/path/trace handling. This does not replace portable API→agent integration or KVM testing.
- Firecracker upstream release and merged-PR findings are documented in `docs/FIRECRACKER_UPSTREAM_AUDIT.md`; as of 2026-11-05, v1.17.0 remains the latest stable tag and no PR is listed as merged after Nov 4. Porter’s exact binary version remains unknown until pinning and runtime inspection.
- Firecracker/KVM E2E not run here: `/dev/kvm` and the Firecracker binary are absent in this sandbox. Legacy destructive E2E scripts are now disabled; the GitHub KVM job requires a configured self-hosted runner and kernel/rootfs repository variables.
