# `porter-agent` — host agent

This Go project produces the `porter-agent` binary.

One privileged agent runs on every Linux/KVM machine. It is the **only**
component that controls Firecracker, TAP devices, NAT, local volumes, and local
system state.

Responsibilities:

- bootstrap/join with a one-time token
- host preflight: KVM, Firecracker, cgroups, bridge/TAP support
- outbound mTLS WebSocket to Porter
- heartbeat and capacity reporting
- Firecracker microVM lifecycle
- TAP/bridge/NAT networking
- local ext4 volumes and snapshots
- local SQLite actual-state ledger
- host port forwarding to the Porter control-plane MicroVM
- control-plane MicroVM supervision on the bootstrap host
- signed self-update and VM-preserving restarts

No global scheduler. No inbound unauthenticated port. Per-VM systemd units keep
workloads alive across agent restarts.

Detailed design: [`../docs/FLEET_ARCHITECTURE.md`](../docs/FLEET_ARCHITECTURE.md)
and [`../docs/CONTROL_PLANE_AS_MICROVM.md`](../docs/CONTROL_PLANE_AS_MICROVM.md).
