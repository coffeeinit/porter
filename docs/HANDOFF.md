# PORTER HANDOFF — 2026-11-05

**v1.0.0-beta status: blocked.** No real KVM recovery acceptance or full API-to-agent release E2E has passed on the current integration.

## Current handoff — 2026-11-05, 20:15 IST

This section is the authoritative resume point. The owner said **stop all** work; implementation and tests are stopped. Do not resume coding, commit, push, or open a PR unless the owner asks again. This documentation update is the only work performed after that stop request.

### Branch and published state

- Repository: `/workspace/porter-inspection`.
- Branch: `work/porter-foundation-plan`.
- Local `HEAD` and a fresh `git ls-remote` both resolve to `e0c3f395272faf7044cf249aa474af063175624a` on `coffeeinit/porter`.
- The local `origin/work/porter-foundation-plan` tracking ref is stale at `87396b0`; trust a fresh remote query, not that tracking ref.
- No Porter PR, tag, or release exists.
- The last reported ten-milestone ledger said 8/10 at the Buildkite setup push, but earlier counters were inconsistent. Reconstruct the count from confirmed commits before relying on the limit.

Published milestones after `19b041b` include PostgreSQL CI (`4680ac3`), lifecycle API/agent dispatch (`851adaf`, `f4f09ac`), gateway port-forward rebinding (`62aeed4`), agent result outbox (`d704faf`), fail-closed build guard (`a1bdf1a`), ACME domain authorization (`43cf8e5`), privacy-safe OTLP trace export (`de11c38`), node-bound optional mTLS (`87396b0`), and Buildkite config (`e0c3f39`). Earlier work includes private per-VM writable rootfs (`19b041b`) and gateway/domain hardening. Confirm each SHA against the remote before quoting it as published.

### CI and KVM status

- The owner’s Buildkite GitHub Actions Compatibility run showed all 5/5 jobs successful; the Firecracker KVM job was skipped. The “workflow token unavailable” message was an annotation, not a job failure. Leave workflow-authorized tokens disabled unless a test actually needs GitHub API access.
- A native Buildkite pipeline is present at `.buildkite/pipeline.yml`; setup notes are in `docs/BUILDKITE_SETUP.md`. Its KVM step is opt-in and requires the dedicated `porter-kvm` self-hosted queue.
- No KVM-capable machine or registered self-hosted runner was available. This sandbox has no `/dev/kvm`, Firecracker, or Jailer. Real VM lifecycle and recovery acceptance remain mandatory blockers; portable CI is not KVM evidence.
- The KVM target currently covers only a real Firecracker apply/stop check, not the full beta acceptance matrix.

### Uncommitted work at stop — preserve it

The working tree was dirty and nothing below was staged, committed, or pushed. Last reported checks on this exact working tree passed `make test`, `make vet`, race suites for the Go modules, UI syntax, beta binary/version assertions, formatting, and `git diff --check`. KVM acceptance did not run. Review the changes before any future staging; do not reset or clean them blindly.

Modified files: `docs/HANDOFF.md`, `docs/PROTOCOL.md`, `docs/SNAPSHOT_RESTORE_VERIFIED.md`, `porter-agent/README.md`, `porter-agent/cmd/porter-agent/main.go`, `porter-agent/cmd/porter-agent/outbox_test.go`, `porter-agent/internal/client/client.go`, `porter-agent/internal/runtime/runtime.go`, `porter-agent/internal/runtime/runtime_test.go`, `porter/README.md`, `porter/internal/api/images.go`, `porter/internal/api/mcp.go`, `porter/internal/api/phase2.go`, `porter/internal/store/snapshot_integrity_test.go`, `porter/internal/store/store.go`, `protocol/types.go`, and `scripts/README.md`.

Untracked files: `porter-agent/cmd/porter-agent/snapshot_dispatch_test.go`, `porter-agent/internal/runtime/disk_snapshot.go`, `porter-agent/internal/runtime/disk_snapshot_test.go`, and `porter/internal/api/snapshot_roundtrip_test.go`.

The snapshot/restore work now validates that authenticated snapshot metadata belongs to the reporting node and refuses to report restore success if the agent cannot persist its local VM ledger. It has fake-based API/SQLite and agent HTTP-dispatch tests. Its intended contract is crash consistency only; application-consistent snapshots are unsupported. The detailed gate and remaining KVM/fault-injection requirements are in `docs/SNAPSHOT_RESTORE_VERIFIED.md`.

Other uncommitted changes include agent result-outbox behavior and strengthened fail-closed image-build tests. Treat these as separate review units; do not publish them as a single undifferentiated patch.

## 1. What this repository is now

Porter is a host-installed Firecracker platform with two Go projects:

```text
porter/          control plane + API + UI + scheduler + embedded gateway
porter-agent/    privileged host agent + Firecracker runtime + local SQLite
protocol/        shared wire types
```

Porter runs as an unprivileged host systemd service. The agent is the only
component that touches host KVM, Firecracker, TAP, NAT, volumes, or guest IPs.

The old implementation, old docs, old prototypes, and old image catalog are in
`legacy/` and must not be imported.

## 2. Current implemented features

### Control plane
- SQLite store and migrations
- admin bearer-token auth
- join-token creation
- agent enrollment
- node token auth
- heartbeat and capacity
- projects/apps/deployments/replicas
- simple scheduler
- command queue
- operation ledger
- event history
- autoscaling policy
- idle timeout / scale-to-zero
- volume create/attach/resize
- snapshot records
- logs ingestion/query
- metrics samples/query
- domains and routes
- gateway request logs/stats
- image catalog records
- direct MicroVM image records
- Dockerfile/Docker/OCI/Git/Compose image build pipeline
- embedded single-page dashboard
- MCP HTTP endpoint and tools

### Agent
- one binary on every node
- join + enrollment
- local SQLite
- heartbeat
- HTTPS/JSON bearer polling; optional CA-verified mTLS with a node-bound certificate URI; no WebSocket stream or automatic certificate lifecycle
- command polling
- real Firecracker lifecycle
- TAP/bridge/NAT
- kernel `ip=` guest network boot args
- persistent `/dev/vdb` data drive
- crash-consistent snapshot capture of Firecracker state/memory, private rootfs, and optional attached volume; synced files, SHA-256 manifests, exclusive volume locks, and durable retry record
- snapshot restore command with staged disk replacement, rollback journal, and interrupted-restore recovery before VM adoption
- serial log shipping
- process liveness health reporting
- signed-update path not implemented yet

### Historical WSL checks (not current beta acceptance evidence)
- real Firecracker boot under KVM
- real TAP/bridge/NAT
- persistent volume attached as `/dev/vdb`
- snapshot create
- logs and metrics
- Dockerfile -> OCI tar -> rootfs.ext4
- guest-init binary injected into built rootfs
- MCP tools
- agent restart while VM survives
- Porter restart with SQLite state recovery

## 3. Quick start

### Build

```bash
cd /mnt/d/github/porter   # WSL path
make test
make build
```

Binaries:

```text
bin/porter
bin/porter-agent
bin/porter-guest-init
```

### Run Porter

```bash
PORTER_ADMIN_TOKEN=testadmin \
PORTER_DB=/tmp/porter/porter.db \
PORTER_ADDR=127.0.0.1:8080 \
PORTER_GATEWAY_ADDR=:8081 \
PORTER_BASE_DOMAIN=porter.local \
./bin/porter server
```

Open:

```text
http://127.0.0.1:8080/?token=testadmin
```

### Join a node

```bash
# create a join token through the UI/API first
sudo ./bin/porter-agent join \
  --server http://127.0.0.1:8080 \
  --token <join-token> \
  --name my-node \
  --data-dir /var/lib/porter-agent \
  --kernel /path/to/vmlinux \
  --rootfs /path/to/rootfs.ext4

sudo ./bin/porter-agent run \
  --data-dir /var/lib/porter-agent \
  --network on
```

TAP/bridge/NAT requires root or `CAP_NET_ADMIN`.

### WSL test assets

```text
/mnt/d/github/porter/legacy/porter-images/bases/alpine/3.21/out/vmlinux
/mnt/d/github/porter/legacy/porter-images/bases/alpine/3.21/out/rootfs.ext4
```

## 4. Code map

```text
porter/cmd/porter/main.go              server startup, tick loop, gateway listener
porter/internal/api/                  REST + MCP endpoints
porter/internal/store/store.go        SQLite schema and all persistence
porter/internal/gateway/gateway.go    host routing + weighted reverse proxy
porter/internal/imagebuilder/         Dockerfile/OCI/Git/Compose -> rootfs
porter/web/index.html                 embedded dashboard
porter/migrations?                    none yet; schema lives in store.go

porter-agent/cmd/porter-agent/        join/run agent
porter-agent/cmd/porter-guest-init/   guest network + entrypoint starter
porter-agent/internal/runtime/        Firecracker/TAP/NAT/snapshot
porter-agent/internal/store/          local SQLite
porter-agent/internal/client/         Porter API client

protocol/types.go                     shared wire structs
```

## 5. API map (current)

```text
GET  /healthz
GET  /mcp

POST /v1/fleet/join-tokens
POST /v1/fleet/enroll
POST /v1/fleet/heartbeat
GET  /v1/fleet/commands
POST /v1/fleet/result
POST /v1/fleet/logs
POST /v1/fleet/snapshot
POST /v1/fleet/vmstate

GET  /v1/nodes
GET  /v1/fleet/nodes/{id}
GET  /v1/fleet/nodes/{id}/metrics
GET  /v1/fleet/nodes/{id}/logs
GET  /v1/fleet/nodes/{id}/vms
POST /v1/fleet/nodes/{id}/cordon
POST /v1/fleet/nodes/{id}/uncordon
POST /v1/fleet/nodes/{id}/drain

GET/POST /v1/projects
GET/POST /v1/projects/{id}/apps
GET  /v1/apps
POST /v1/apps/{id}/deployments
POST /v1/apps/{id}/scale
GET/PUT /v1/apps/{id}/autoscale
POST /v1/apps/{id}/activity
POST /v1/apps/{id}/wake
POST /v1/apps/{id}/previews

GET/POST /v1/projects/{id}/volumes
POST /v1/volumes/{id}/attach
POST /v1/volumes/{id}/resize

GET/POST /v1/snapshots
POST /v1/snapshots/{id}/restore
GET  /v1/logs
GET  /v1/metrics

GET/POST /v1/domains
GET/POST /v1/routes
GET  /v1/gateway/stats
GET  /v1/gateway/requests

GET/POST /v1/images
POST /v1/images/build
POST /v1/images/detect
```

## 6. MCP

Endpoint:

```text
POST /mcp
Authorization: Bearer <admin-token>
```

Implemented tools include:

```text
porter_list_nodes
porter_list_projects
porter_create_project
porter_create_app
porter_deploy
porter_scale_app
porter_list_operations
porter_list_volumes
porter_create_volume
porter_attach_volume
porter_resize_volume
porter_list_snapshots
porter_create_snapshot
porter_restore_snapshot
porter_list_logs
porter_list_metrics
porter_get_autoscale
porter_set_autoscale
porter_touch_app
porter_wake_app
porter_list_domains
porter_create_domain
porter_gateway_stats
porter_gateway_requests
porter_create_preview
porter_get_node
porter_node_metrics
porter_node_logs
porter_node_vms
porter_cordon_node
porter_uncordon_node
porter_drain_node
porter_set_replica_weight
porter_list_images
porter_create_image
porter_build_image
porter_detect_framework
```

## 7. Current known limitations

- UI is still one large page, not routed pages.
- No live log streaming (only query).
- Gateway supports static cert/key TLS and optional HTTP-01 autocert; public
  ACME acceptance is unverified, and provider DNS/DNS-01/wildcard TLS are absent.
- Gateway is HTTP reverse proxy; no full WebSocket tuning yet.
- Guest-init is built and injectable but app startup inside the guest is not
  fully verified.
- Health checks are process liveness only; no HTTP/TCP guest probes.
- Image builds still use host Docker/Git, but REST, MCP, and webhook entry points
  fail closed by default until an isolated builder is configured.
- Image builds run asynchronously and expose logs/cancellation; those features
  do not make the unisolated build path safe for untrusted source.
- Compose supports only the primary service, not full stacks.
- Signed GitHub push webhooks pin the commit SHA and deduplicate deliveries;
  webhook builds remain disabled by default, and GitHub App auth is absent.
- Crash-consistent snapshot/restore now includes disk files, checksums, volume locks, and rollback-journal recovery. Portable fake tests pass; real KVM, host-restart, and API-to-agent recovery remain unverified. Application consistency is not promised.
- Volume resize applies to the host disk file and is blocked while a VM manifest still references the volume; real KVM resize acceptance remains open.
- The admin API can create and integrity-check local SQLite backups; database
  restore, off-host retention, encryption, and a full recovery drill are absent.
- Portable regression tests cover the API, store, gateway, builder, and agent;
  API-to-fake-agent restart acceptance and KVM validation remain open.
- Agent WebSocket streaming and automatic client-certificate issuance, renewal,
  rotation, and revocation workflows are not implemented.
- GitHub Actions runs portable tests, vet, race checks, the beta build, and UI
  JavaScript syntax validation; it does not cover KVM/provider acceptance.

## 8. Next tasks in order

1. Multi-page UI restructure
   - Projects list
   - Project detail tabs
   - Deployment detail
   - Replica detail
   - Fleet + node detail
   - Gateway
   - Storage
   - Observability

2. Build pipeline hardening
   - async build operations with progress
   - build log streaming
   - build cache
   - builder isolation

3. Guest app startup + health
   - inject `porter-guest-init` into all images
   - MMDS network/env metadata
   - app entrypoint
   - HTTP/TCP health probes

4. Gateway production
   - ACME
   - wildcard preview TLS
   - WebSocket support
   - canary by deployment version
   - bandwidth/trace charts

5. Persistence
   - host-file volume resize
   - snapshot restore verification
   - backups
   - restore tests

## 9. Common pitfalls

- PowerShell `Set-Content` can write UTF-8 BOM; Go mod/files may need BOM stripped.
- Makefile recipes must use real tabs.
- WSL root is required for TAP/bridge/iptables.
- Docker image `docker save` may produce OCI layout (`index.json`) instead of
  old Docker `manifest.json`; imagebuilder supports both.
- Firecracker snapshot requires paused VM; runtime pauses/resumes.
- Gateway exposes internal VM IPs only in logs/backend; UI must show domains only.

## 10. Suggested first action tomorrow

Open `docs/NEXT_TASKS.md`, start with the UI restructure, then build log streaming.


## Current handoff — 2026-11-11, 19:30 IST

**Status: NOT RELEASE-READY. Stop implementation here.** This entry is the current resume point and does not replace or erase earlier handoff history.

### Exact branch and integration state

- Target: `/workspace/porter-beta-0.0.1-beta`, branch `work/porter-0.0.1-beta`.
- Before this documentation-only update, the worktree was clean at `eee9a993e90faa747de9632ed181be6a6e7fa69f`.
- No candidate code was copied, cherry-picked, merged, or otherwise integrated into beta. No candidate/source branch was deleted or modified for this handoff. The referenced commits remain in their isolated local candidate worktrees.
- No push, PR, merge, or change to `main` was made. The beta worktree's `origin` points to the local `/workspace/porter-inspection` worktree, not to GitHub; do not use it to publish this branch.
- The earlier owner instruction “Do not push or merge” remains in force. The later request did not explicitly lift it. Do not integrate candidate code or publish anything unless the owner gives a clear, scoped approval.
- GitHub issue creation was attempted only as an access check and is blocked: `gh auth status` reports no authenticated GitHub host, and no session-scoped GitHub connector CLI or GitHub issue MCP is available. No issues or labels were created. Complete issue drafts are below for creation when authorized access is available.

### Preserved candidate commits — not integrated

| Work | Candidate commit(s) | Current interpretation |
| --- | --- | --- |
| PostgreSQL Fleet backend | `3738acb0cd6db03fc153fb0784279047b795c26d` | Candidate only; not beta-integrated. |
| Packaging/systemd units and migrator | `7fb7351289633406b55e30232983d4135a12f86e` | Candidate only; not proof of real installation or activation. |
| Migrator credential contract docs | `65f25cdad8fefc94b7a25fd6ce1ff5f9eff2d4bb` | Candidate documentation only. |
| Operator CLI security fix | `7b112b2308387670fa0584c459557c4de6fc7005` | Reviewed candidate only; not beta-integrated. |
| Fleet UI | `6b59f8234004415e6538c73c11fa48ca6e783666` | Reviewed/tested candidate only; the current UI worktree has a later local tip, `57e2d328df14e75093488ea59057dfb3eb62df70`. Neither is beta-integrated. |
| Agent enrollment recovery | `729670fea9aaee54b75b08b8b7325df42078616c` | Reviewed candidate only; not beta-integrated. |
| Installer chain | `31636e764adcb36c3580a4a16184eed58feea8df` → `df945bb8ca08e727028acb241eef5b2f9fcdadd7` → `4a4ae67964ebc991e1ba01d7a012143956f4532e` | Isolated installer candidates only; no production install acceptance. |

Do not import code from the Atlas/Metal/Cargo/Pilot/Pulse reference ZIPs.

### Evidence and remaining acceptance gaps

- The packaging/docs and CLI candidates had two review passes. The UI candidate passed review and a disposable served-panel/browser exercise, but its UI-issued join token was **not redeemed**. It is not installed or integrated acceptance.
- Corrected agent candidate `729670f` had two reviews and reviewer-substantiated paired evidence with backend `3738acb` from a fresh private PostgreSQL 16 run. Reviewers inspected the report/transcript; they did not rerun it. The run covered enrollment/recovery fault paths, heartbeat, and agent restart, but did not test a hard process death after persisting `submitted` and before the HTTP request. The conservative fence prevents replay and may require operator recovery. The logs include an earlier appended node line, and no binary hash was retained; see the run report and logs in `/workspace/porter-paired-acceptance-3738acb0-729670fe-20261111/`.
- A separate 15-check PostgreSQL provisioning run did not run migrations or activate services. Installer child `4a4ae67` received two reviews for its exact preflight change, but its candidate still has no tracked migrator unit, the migrator reads the DSN from its environment, and the installer stops before migration/activation. No real Ubuntu/Debian install or systemd acceptance was run.
- The install/register matrix reports that the full served UI token-redemption path, exact installed CLI/store contract, durable rows for two installed agents, full negative security/TLS matrix, and clean-host/systemd acceptance remain open or blocked. Candidate-level passes are not integration or release passes.
- Firecracker/KVM acceptance is a separate, untested follow-up. The install/register acceptance lane explicitly does not require `/dev/kvm`; it does not establish KVM, VM lifecycle, or snapshot/restore acceptance.
- Use the supplied matrix at `/home/ubuntu/upload/CzUpXDxrMahUMQyX3V03oK/INSTALL_REGISTER_ACCEPTANCE.md` as the detailed acceptance ledger. Do not describe this work as full MVP acceptance or release-ready.

### GitHub issue drafts

Create these as separate issues when repository write access is restored. The issue drafts intentionally distinguish implementation from real-host and KVM acceptance.

#### Draft 1 — Implement migrator credential consumption and synchronous activation

**Suggested labels:** `release-blocker`, `installer`, `security`

**Body:**

The installer/migrator candidate chain is not ready for a real install. In the reviewed `4a4ae67` child, the exact two-file preflight review passed, but the checkout has no tracked migrator systemd unit, the Go migrator still reads its DSN from the environment, and the installer stops before migration and service activation. Do not treat fake smoke tests or credential preflight as migration acceptance.

Implement the pinned credential contract: a dedicated `Type=oneshot` migration unit on supported systemd (>=247), explicit `LoadCredential` entries for the required DSN and admin-token credentials, and migrator code that reads credentials from `$CREDENTIALS_DIRECTORY` rather than argv or environment fallbacks. Keep the raw DSN in a root-owned `0600` file. The installer must fail closed if either required credential or the migration is unavailable, and must not enable/start the API before migration succeeds.

**Acceptance:** tracked unit passes `systemd-analyze verify`; a real disposable VM proves the migrator runs as `User=porter` and reads the systemd credentials; migrations complete before API activation; a missing credential or failed migration prevents API enable/start; protected sentinel values do not appear in process argv, environment, stdout/stderr, installer output, or journal. Keep fake smoke evidence separate from real-systemd evidence.

#### Draft 2 — Prove a clean Ubuntu/Debian install and systemd lifecycle

**Suggested labels:** `release-blocker`, `acceptance`, `installer`

**Body:**

After the migrator credential path is implemented, run the installer on a disposable, clean supported Ubuntu/Debian VM. Candidate review, shell syntax checks, fake smoke, and a PostgreSQL provisioning-only run do not prove host installation or service activation.

**Acceptance:** use the real installer, PostgreSQL, tracked migration unit, and systemd services; verify ownership/modes and installed environment/TLS configuration; run `systemd-analyze verify`; prove migration finishes before the API becomes active and failure leaves the API disabled/stopped; install and invoke the real `porter-agent`; capture redacted logs and a reproducible report. Do not claim this issue complete using fake units, a manually launched API, or an acceptance run that skips migrations or activation.

#### Draft 3 — Integrate and redeem Fleet-panel tokens through real agents

**Suggested labels:** `release-blocker`, `acceptance`, `fleet`

**Body:**

The reviewed Fleet UI/API/agent work is still in isolated candidate worktrees. The disposable browser run exercised token issuance and panel behavior, but did not redeem the UI-issued token. No source integration is authorized until the owner explicitly approves it.

Once integration is explicitly authorized, reconcile the reviewed backend `3738acb`, UI `6b59f82`, agent `729670f`, packaging, and CLI candidates; preserve their provenance and resolve overlaps before changing beta. Then run the install/register matrix against the real served UI, real server, real PostgreSQL, and installed agents.

**Acceptance:** the served panel issues `POST /api/v1/fleet/join-tokens` and displays the secret only in the active panel; redeem both same-host and remote-host tokens using the real hidden-TTY and protected-stdin CLI modes; verify two distinct nodes first persist as `offline` in PostgreSQL and appear in the UI; verify heartbeat moves them to `Ready`; verify persistence after the required restart checks; prove admin/join tokens are not leaked to URLs, storage, logs, or unintended request fields. Retain a redacted transcript and exact source SHAs. This issue does not authorize a push, merge, or release by itself.

#### Draft 4 — Close the v2 enrollment API, CLI, TLS, and recovery matrix

**Suggested labels:** `release-blocker`, `security`, `acceptance`

**Body:**

Complete the still-open rows in `INSTALL_REGISTER_ACCEPTANCE.md` on the final integrated source and real client/server path. Candidate-level transcript evidence is not a substitute for integrated acceptance.

**Acceptance:** finish API-01 full response/expiry/redirect assertions; CLI-API-01 HTTPS redirect and malformed-origin (`?`/`#`) rejection with no bearer forwarding; SEC-01 through SEC-07 credential/SQLite byte scans, generic replay/unknown-token errors, response/Location secret hygiene, legacy-route rejection, state-directory and hostname checks, and browser token separation; TLS-01 through TLS-04 trusted-CA positive control, wrong-CA, hostname-mismatch, and redirect checks before enrollment; REC-01/REC-02 server-restart/lost-response recovery and the sole handler-not-reached 404 retry exception. Never replay an ambiguous request or add a `not_consumed` marker without explicit owner approval. Record each matrix row as passed, failed, or not run, with evidence.

#### Draft 5 — Test the agent hard-crash boundary after `submitted`

**Suggested labels:** `agent`, `recovery`, `acceptance`

**Body:**

The corrected agent candidate intentionally fences ambiguous enrollment outcomes after persisting `submitted`, before sending the HTTP request. The optional hard-death probe was not run. Add a deterministic fault-injection test that terminates the process at this exact boundary, then restarts it.

**Acceptance:** prove the restarted agent makes zero enrollment requests and never replays the one-use token; verify persisted local state and hostname/time recovery metadata; document the operator recovery path and distinguish a request-not-sent crash from an ambiguous post-send outcome. Keep the current conservative no-replay behavior unless the owner approves a different protocol.

#### Draft 6 — Run separate real Firecracker/KVM beta acceptance

**Suggested labels:** `release-blocker`, `kvm`, `acceptance`

**Body:**

Firecracker/KVM was explicitly out of scope for install/register acceptance and remains untested in this handoff. Run the beta acceptance on a dedicated KVM-capable host/runner; do not infer it from portable CI, PostgreSQL tests, or agent enrollment.

**Acceptance:** record the host/kernel and Firecracker/Jailer versions; exercise real VM create/boot/stop and the beta-required lifecycle/recovery path, including crash-consistent snapshot/restore as required by the beta gates; retain logs and a redacted report. Mark unsupported checks as not run. Application-consistent snapshots are not promised. This remains separate from clean OS/systemd installation acceptance.

### Stop condition

This handoff is the stop point. Do not continue implementation, copy candidate code, delete candidate branches/worktrees, push, merge, or open a PR without the owner’s explicit authorization. The only remaining action from this execution is the local documentation-only commit; report its SHA and the six issue drafts to the coordinator.
