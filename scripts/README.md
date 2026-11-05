# Porter test scripts

## Legacy E2E scripts are disabled

The `e2e-*.sh`, `network-smoke.sh`, `phase2-smoke.sh`, `live-wsl.sh`, and `smoke.sh` scripts are retired and fail closed. They used shared or fixed directories, fixed ports, broad `pkill` patterns, and/or incomplete health/restore assertions. Do not remove their guards or run the old bodies. The HTTP and snapshot scripts also shared the same work directory.

Portable verification is `make test`, `make vet`, the race tests in `.github/workflows/ci.yml`, the embedded UI syntax check, and the beta binary build. The legacy `make e2e-*` targets print a warning and fail rather than invoking those scripts.

## Firecracker KVM acceptance

`make kvm-acceptance` runs `TestKVMFirecrackerApplyStopAcceptance`. It requires a Linux host with an accessible `/dev/kvm`, a Firecracker binary, and readable kernel/rootfs artifacts in `PORTER_KVM_KERNEL` and `PORTER_KVM_ROOTFS`. `PORTER_FIRECRACKER_BIN` is optional and defaults to `firecracker`.

The GitHub Actions KVM lane runs on version tags, or on manual dispatch when `run_kvm_acceptance` is enabled. It requires a self-hosted runner labeled `porter-kvm` plus the repository variables `PORTER_KVM_KERNEL`, `PORTER_KVM_ROOTFS`, and optionally `PORTER_FIRECRACKER_BIN`.

This gate currently verifies a real Firecracker apply/stop lifecycle only. It does not establish end-to-end API-to-agent behavior, host-reboot recovery, guest-network readiness, isolation/jailer correctness, snapshot/backup recovery, DNS/TLS provisioning, or complete OpenTelemetry ingestion. A passing portable CI build is not a substitute for those release checks.

## v1.0.0-beta status: blocked

Do not create a v1.0.0-beta tag or release until each required gate has an active, safe acceptance test and passes on the exact release commit. In particular:

- **VM lifecycle and fleet repair:** the KVM gate does not cover start, restart, scale, agent/host restart convergence, failed-replica repair, capacity recovery, or safe drain/evacuation.
- **Build isolation:** builds are fail-closed by default, but enabling the unisolated-build override is not a safe release solution. Isolation, resource limits, source confinement, archive limits, and cancellation need tests.
- **Agent security and convergence:** optional node-bound mTLS now protects HTTPS polling routes when configured, but polling is not streaming. Certificate lifecycle, disconnect/restart replay, and node convergence need end-to-end acceptance.
- **Domains and routing:** candidate commit `f06bf61` reports persisted TXT proof and fail-closed routing, but it is not integrated. DNS-provider propagation and public ACME issuance/renewal are not accepted release behavior; the PostgreSQL-backed domain test was not run.
- **Observability:** optional privacy-safe OTLP/HTTP trace export is implemented, but metrics/log export and end-to-end collector acceptance remain open.
- **Backup and restore:** crash-consistent only; application consistency is not promised. The agent now captures Firecracker state/memory plus each VM's private rootfs and optional attached volume, with syncs, checksums, exclusive volume locks, retryable records, and rollback-journal recovery. Portable fake tests pass, but no real Firecracker/KVM recovery or full API-to-agent acceptance has run. Keep the gate blocked until the real KVM and restart-recovery tests pass on the exact release commit. See [`snapshot and restore verification status`](../docs/SNAPSHOT_RESTORE_VERIFIED.md).

The legacy E2E scripts remain disabled because their old bodies used unsafe shared resources or incomplete assertions. Do not remove their fail-closed guards or treat historical script output as current acceptance evidence. Portable unit tests, race tests, and beta binary builds are necessary checks, not release approval.
