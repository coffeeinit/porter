# Firecracker upstream audit

**As of 2026-11-05.** This is a Porter-relevant upstream capability review, not a verified upgrade comparison. Porter baseline `7adbfc983f4df15770cbbecc32a88d17e9be236b` invokes an unpinned `firecracker` binary. **Porter has no Firecracker version pin, so its deployed version, build, and feature set are unknown.** The official release list still shows v1.17.0 as latest, and GitHub's merged-PR search for dates after 2026-11-04 returned no results. [1, 2, 20]

## What Firecracker does—and does not—provide

Firecracker is the per-microVM virtual machine monitor (VMM). Its host API configures and controls a VM: CPU and memory, kernel/root filesystem, network and block devices, snapshots, logging, and metrics. It can create a microVM when Porter’s local agent asks its VMM API to do so. [3]

Firecracker is **not** a Kubernetes-style control plane or application platform. It does not provide fleet membership/heartbeats, placement and scheduling, desired-state reconciliation, multi-node autoscaling, scale-to-zero/wake-on-request, image builds or registry workflows, application routing/gateway, public DNS/TLS/domain management, or an end-to-end distributed tracing system. Its VMM metrics and logs are useful telemetry, but they are not distributed traces. Those are Porter control-plane, agent, builder, and gateway responsibilities; instrument those components (for example, with OpenTelemetry) and correlate them with VM IDs and Firecracker metrics. [3, 4]

## Latest stable release: v1.17.0

The official release page lists **Firecracker v1.17.0**, published 2026-09-10, as the latest stable release; no later stable tag was listed on 2026-11-05. [1, 2]

Relevant additions and fixes:

- **Kernel, CPU, and boot:** official guest Linux 6.18 support; automatic x86_64 `bzImage` detection (uncompressed `vmlinux` remains recommended where boot time and memory matter); AWS Graviton5 onboarding; and host-kernel/KVM-gated ARM implementation-ID and CPU-template fixes. These are conditional platform capabilities, not general OS certification. [2, 5]
- **Block, memory, and devices:** virtio device reset; virtio-block block-size/topology reporting; opt-in discard for writable `Sync` drives; bounded memory-hotplug inputs; and virtio-mem snapshot mapping and partial-operation fixes. Reset is not guaranteed for vhost-user-block or Async virtio-block. [2, 6, 7]
- **Snapshots:** transparent huge pages and snapshot-load page-mode selection. `sync_snapshot_files=false` can reduce snapshot-create wait but weakens crash durability for VM state and guest-memory files; retain the default durable behavior unless the trade-off is accepted. [2, 8]
- **Isolation and observability:** aarch64 jailer ownership-race fix; optional metrics `id` and operator-defined `properties`; and SIGPIPE logging/deadlock fixes. Firecracker still relies on a correctly configured Linux host; the release does not prove Porter currently enables the jailer, seccomp, cgroups, or namespaces. [2, 9, 10]
- **Networking/vsock:** host-to-guest vsock delivery fixes after pause/resume and event-loop/throughput improvements, with a single-vCPU/newer-Intel performance caveat. [2, 11]

## Merged to `main`, but not in a tagged release

These changes were merged to upstream `main` by the audit date but are **not in v1.17.0**. Do not assume Porter’s unpinned runtime includes them; first pin and inspect the actual artifact. [12–17]

- **#6312 — threaded virtio-block/multiqueue (merged Oct 28):** adds optional `num_queues`; each queue can use a worker thread, and values above one enable multiqueue. This is block-I/O parallelism, not VM scheduling or autoscaling. Treat it as opt-in and benchmark real workloads before enabling it. [12]
- **#6370 — dirty-page completion when a block worker exits (merged Nov 3):** closes a worker shutdown/drop failure that can abort Firecracker during dirty-page tracking. The reported reproducer requires threaded queues, the Async I/O engine, and dirty tracking together. This fix is important before Porter combines those options, but it is not a substitute for snapshot/recovery tests. [13]
- **#6331 — MMDS TCP retransmission correction (merged Nov 4):** retransmits from the oldest unacknowledged response byte so packet loss or multi-queue traffic does not lose metadata responses. Relevant only if Porter enables MMDS. [14]
- **#6372 — vhost `update_config` panic guard (merged to `main` Nov 3):** checks whether the guest has activated the device instead of asserting during an early configuration-update window. GitHub’s page header still labels this PR “open,” but its timeline records the merge to `main`; treat the timeline as the merge evidence and retain this UI inconsistency in mind. [15]
- **#6354 — shared saved virtio-device state across PCI/MMIO (merged Nov 2):** consolidates internal persisted-state structures to streamline snapshot code. It is a refactor, not a new Porter-facing API or feature. [16]
- **#6341 — secret-free VM KVM checks:** merged to `feature/secret-hiding`, **not `main`**. It consolidates required KVM capability checks, including `KVM_CAP_USERFAULT` for restoring a secret-free VM from a snapshot. Do not advertise or enable this capability based on that PR until it reaches a tagged release or a deliberately verified build. [17]

## Host and guest compatibility

Firecracker is a VMM, not a guest OS distribution. Upstream documents Linux hosts on x86_64 or aarch64 with KVM and read/write access to `/dev/kvm`. The v1.17.0 kernel policy lists Linux 5.10, 6.1, and 6.18 host/guest combinations, subject to release-specific support dates and feature requirements. This is not a promise that arbitrary distributions, kernel configurations, or non-Linux guests work. The host kernel/KVM/architecture determine whether the VMM starts and host capabilities work; the guest kernel and drivers determine guest-visible devices. [18, 19]

## Porter integration actions

1. **Pin and report the runtime:** select an exact Firecracker release/build, record artifact digest and version, and verify the executable in the deployed agent rather than resolving an unspecified `firecracker` from `PATH`. Ship the matching jailer and statically linked Firecracker binary together. v1.17.0 is the current stable candidate, subject to target-host validation. [2, 9]
2. **Keep orchestration in Porter:** implement heartbeat freshness, scheduling, reconciliation, scaling, gateway/domain/DNS/TLS, and trace propagation in Porter. Do not treat Firecracker’s per-VM API or metrics as fleet control or distributed tracing. [3, 4]
3. **Verify host containment:** validate `/dev/kvm` access, chroot, uid/gid drop, cgroup version/limits, namespaces, resource limits, and production seccomp behavior from actual deployment evidence; do not infer these from an API call. [9, 10, 18]
4. **Gate optional features:** test only the block, discard, reset, huge-page, snapshot durability, vsock, MMDS, and CPU-template paths Porter actually configures. Keep `sync_snapshot_files=true` unless a durability trade-off is accepted. Do not combine threaded multiqueue, Async block I/O, and dirty-page tracking on an unverified build. [2, 6–8, 11, 12, 13]
5. **Keep KVM-gated/branch-only behavior disabled:** do not advertise secret-free guests/snapshot restore unless the deployed build and actual host KVM capabilities satisfy the required checks. #6341 is not merged to `main`. [17]

## References

1. [Official Firecracker releases](https://github.com/firecracker-microvm/firecracker/releases)
2. [Firecracker v1.17.0 release notes](https://github.com/firecracker-microvm/firecracker/releases/tag/v1.17.0)
3. [Firecracker repository README and API capability summary](https://github.com/firecracker-microvm/firecracker)
4. [Firecracker metrics documentation](https://github.com/firecracker-microvm/firecracker/blob/main/docs/metrics.md)
5. [PR #6116: ARM writable implementation-ID registers](https://github.com/firecracker-microvm/firecracker/pull/6116); [PR #6172: CLIDR host-kernel gate](https://github.com/firecracker-microvm/firecracker/pull/6172); [PR #6120: CPU-template MSR ordering](https://github.com/firecracker-microvm/firecracker/pull/6120); [PR #6145: Graviton5](https://github.com/firecracker-microvm/firecracker/pull/6145)
6. [PR #6285: virtio reset limitations](https://github.com/firecracker-microvm/firecracker/pull/6285); [block-device documentation](https://github.com/firecracker-microvm/firecracker/blob/main/docs/block.md); [memory hotplug documentation](https://github.com/firecracker-microvm/firecracker/blob/main/docs/memory-hotplug.md)
7. [PR #6076: hotplug input bounds](https://github.com/firecracker-microvm/firecracker/pull/6076); [PR #6174: virtio-mem snapshot mapping](https://github.com/firecracker-microvm/firecracker/pull/6174); [PR #6176: virtio-mem operation accounting](https://github.com/firecracker-microvm/firecracker/pull/6176)
8. [Huge-pages documentation](https://github.com/firecracker-microvm/firecracker/blob/main/docs/hugepages.md); [snapshot support documentation](https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/snapshot-support.md); [PR #6109: snapshot fsync control](https://github.com/firecracker-microvm/firecracker/pull/6109)
9. [PR #5956: aarch64 jailer TOCTOU fix](https://github.com/firecracker-microvm/firecracker/pull/5956); [jailer documentation](https://github.com/firecracker-microvm/firecracker/blob/main/docs/jailer.md)
10. [Seccomp documentation](https://github.com/firecracker-microvm/firecracker/blob/main/docs/seccomp.md); [PR #5983: metrics identity/tags](https://github.com/firecracker-microvm/firecracker/pull/5983); [PR #6086](https://github.com/firecracker-microvm/firecracker/pull/6086) and [PR #6143](https://github.com/firecracker-microvm/firecracker/pull/6143): SIGPIPE logging fixes
11. [Network setup documentation](https://github.com/firecracker-microvm/firecracker/blob/main/docs/network-setup.md); [vsock documentation](https://github.com/firecracker-microvm/firecracker/blob/main/docs/vsock.md)
12. [PR #6312: threaded virtio-block/multiqueue](https://github.com/firecracker-microvm/firecracker/pull/6312)
13. [PR #6370: worker dirty-page completion](https://github.com/firecracker-microvm/firecracker/pull/6370)
14. [PR #6331: MMDS retransmission](https://github.com/firecracker-microvm/firecracker/pull/6331)
15. [PR #6372: vhost `update_config` panic guard](https://github.com/firecracker-microvm/firecracker/pull/6372)
16. [PR #6354: shared persisted device state](https://github.com/firecracker-microvm/firecracker/pull/6354)
17. [PR #6341: secret-free VM KVM capability checks](https://github.com/firecracker-microvm/firecracker/pull/6341)
18. [Firecracker Getting Started](https://github.com/firecracker-microvm/firecracker/blob/main/docs/getting-started.md)
19. [v1.17.0 kernel policy](https://github.com/firecracker-microvm/firecracker/blob/v1.17.0/docs/kernel-policy.md)
20. [Merged-PR search after 2026-11-04](https://github.com/firecracker-microvm/firecracker/pulls?q=is%3Apr+is%3Amerged+merged%3A%3E2026-11-04+sort%3Aupdated-desc) (checked 2026-11-05; no results)
