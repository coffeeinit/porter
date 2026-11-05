# Porter test scripts

## Legacy E2E scripts are disabled

The `e2e-*.sh`, `network-smoke.sh`, `phase2-smoke.sh`, `live-wsl.sh`, and `smoke.sh` scripts are retired and fail closed. They used shared or fixed directories, fixed ports, broad `pkill` patterns, and/or incomplete health/restore assertions. Do not remove their guards or run the old bodies. The HTTP and snapshot scripts also shared the same work directory.

Portable verification is `make test`, `make vet`, the race tests in `.github/workflows/ci.yml`, the embedded UI syntax check, and the beta binary build. The legacy `make e2e-*` targets print a warning and fail rather than invoking those scripts.

## Firecracker KVM acceptance

`make kvm-acceptance` runs `TestKVMFirecrackerApplyStopAcceptance`. It requires a Linux host with an accessible `/dev/kvm`, a Firecracker binary, and readable kernel/rootfs artifacts in `PORTER_KVM_KERNEL` and `PORTER_KVM_ROOTFS`. `PORTER_FIRECRACKER_BIN` is optional and defaults to `firecracker`.

The GitHub Actions KVM lane runs on version tags, or on manual dispatch when `run_kvm_acceptance` is enabled. It requires a self-hosted runner labeled `porter-kvm` plus the repository variables `PORTER_KVM_KERNEL`, `PORTER_KVM_ROOTFS`, and optionally `PORTER_FIRECRACKER_BIN`.

This gate currently verifies a real Firecracker apply/stop lifecycle only. It does not establish end-to-end API-to-agent behavior, host-reboot recovery, guest-network readiness, isolation/jailer correctness, snapshot/backup recovery, DNS/TLS provisioning, or complete OpenTelemetry ingestion. A passing portable CI build is not a substitute for those release checks.
