# Changelog

All notable changes to Porter are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Status labels used below follow SRS §66.13:
`IMPLEMENTED` · `PARTIAL` · `EXPERIMENTAL` · `PLANNED`.

## [Unreleased]

## [v0.0.1-alpha] — 2026-10-05

**"It boots."** The first release: the direct-Firecracker path works end to end on
one box, proven by a repeatable smoke test. The build, `go vet` and the full test
suite are green.

### The headline

`scripts/v0.0.1-smoke.sh` provisions a database, applies migrations, registers a
golden image, starts the control plane, authenticates, creates a project, and
boots one Firecracker microVM — verified three independent ways:

- the durable `replicas` row reports `state=running` with the guest IP assigned;
- a live `firecracker` process exists for that VM;
- the **guest's own kernel serial output** shows `Booting paravirtualized kernel on KVM`
  and `smpboot: Total of 1 processors activated`.

### Added

- `porter kernel set <url|path>` — installs the vmlinux used to boot microVMs.
  Downloads over http(s) or copies a local file, atomically, and refuses anything
  that is under 1 MiB or does not carry ELF/gzip/bzip2/xz/zstd/bzImage magic, so a
  truncated download or an HTML error page can never be registered as a kernel.
- `porter image add <name> <rootfs.ext4> <vmlinux>` — validates both artifacts and
  registers the pair in the on-disk image catalog the control plane resolves
  deployable images from (`api.applyImageManifest` reads `Catalog.All()`).
  Records the ext4/architecture detail and both SHA-256 digests.
- `scripts/v0.0.1-smoke.sh` — the release gate. Self-contained: its own database,
  port pair, socket/snapshot/log/volume directories; it never disturbs a running
  install. Every dependency is overridable via `PORTER_SMOKE_*`.
- `internal/api.auditAuth` — the missing auth+audit middleware. Composes the full
  chain (request ID → bearer → rate limit → CSRF → RBAC → idempotency) and writes
  exactly one `audit_events` row per state-changing request, **including denials**,
  which is what makes rejected operations attributable rather than invisible.
  Reads are not audited, so the table stays meaningful.
- `api.WakeProject` — the gateway's scale-to-zero wake path. Boots a project's
  stopped replica and reports explicitly when the project has no replica to wake,
  rather than pretending it woke something.
- `runtime` Pause / Resume / Reboot, wired through `FCClient.SetVMState`
  (Firecracker `PATCH /vm`) and `Engine`, so `api.VMRunner` is satisfied by real
  behaviour instead of a missing method.
- `.gitattributes` — pins the repository to LF and marks binary assets, so diffs
  stay trustworthy regardless of the platform running git.

### Changed

- **341 handler bodies now return an explicit `501 notImplemented`** instead of
  failing to compile. The route table is untouched: **every one of the 844 routes
  is still present**, and each stubbed one names its own route in the 501 body, so
  a client can tell "by design, not yet in this release" from a fault.
  `IMPLEMENTED` for the 12 v0.0.1 routes, `PLANNED` for the rest.
- `handlers_coolify.go` → `handlers_operator.go`. Porter is its own product; its
  own source no longer names competitor products as parity targets.
- `porter.toml` `kernel_image` / `rootfs_path` now point at `../data/*`, relative
  and portable, with `PORTER_KERNEL_IMAGE` / `PORTER_ROOTFS_PATH` overrides. They
  previously referenced `/mnt/d/github/porter1/references/*`, a path that stopped
  existing when the checkout was renamed.
- Default binary version tracks the tree (`v0.0.1-alpha`); the Makefile's
  `-ldflags` injection still wins.
- Live acceptance (`backend/tests`) is now opt-in via `PORTER_TEST_API_URL`
  instead of defaulting to `localhost:8080`, so an unrelated server on that port
  can no longer turn the unit gate red for environmental reasons.

### Fixed

- **Startup panic:** the route table contained `GET /tags/{tagName?}`. Go's
  `ServeMux` has no optional-wildcard syntax and panics at registration, so
  `porter server` could not start. Split into the literal `/tags` and the
  wildcard `/tags/{tagName}`.
- **Authorization regression:** `GET /org` had lost its read capability gate
  (`""` = any authenticated principal) and is now gated by `project.read`,
  restoring the contract pinned by `TestPermForRoute`.
- **Test suite could not compile:** `api_test.go` referenced a
  `canonicalDisruptionBudget` helper and two source-tracing tests read the
  no-longer-existing `handlers.go`. The helper is implemented against the exact
  semantics its table specifies (one bound only, non-negative whole numbers,
  unknown keys rejected rather than ignored), and the tracing tests now scan the
  package instead of one file, so they no longer depend on file layout.
- `auditAuth` no longer panics when the API is constructed without a store; an
  audit write must never be able to break the data path.

### Removed

- `internal/api/handlers.go.bak`, `recovered_block.txt`, `tmp-recover2.sh` —
  merge-conflict recovery artifacts, superseded by the per-domain handler split
  and preserved in git history.

### Known limitations

- 341 routes answer 501 by design (see the status table in
  `docs/v0.0.1-alpha.md`). The store layer they need does not exist: the API
  layer calls **296 store methods that were never written**, against roughly 23
  table domains that were never migrated. This was verified to be unrecoverable —
  neither this checkout nor the upstream `coffeeinit/porter` repository ever
  contained them.
- The guest boots but does not answer ICMP: the alpine image in `data/` has no
  init that configures its network. Booting is proven by the kernel's own output;
  workload-level networking is `PLANNED` for a later release.
- The authoritative DNS server could not bind `:53` in the smoke environment
  because the port was already in use; this is logged and non-fatal.

[v0.0.1-alpha]: https://github.com/coffeeinit/porter/releases/tag/v0.0.1-alpha
