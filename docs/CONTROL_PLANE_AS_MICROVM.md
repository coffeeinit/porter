# Control Plane as a MicroVM (Deferred)

## Core idea

**Deferred:** V1.0.0 runs Porter as an unprivileged host systemd service. This document is kept as a future hardening option only.

The host only runs `porter-agent`, the privileged executor.

```text
Host machine
  |
  +-- porter-agent (root / narrow capabilities)
        |
        +-- systemd unit: porter-control-plane.service
        |     -> Firecracker MicroVM
        |        -> porter API/UI/scheduler/fleet manager
        |        -> persistent volume: porter.db
        |
        +-- systemd units: porter-vm-<id>.service
              -> customer application MicroVMs
```

This gives one uniform rule:

> Every workload, including Porter itself, is a Firecracker MicroVM controlled
> by an agent.

## Benefits

- Porter cannot access host KVM, Firecracker sockets, or host files directly.
- The host attack surface is the agent only.
- Control-plane upgrades are just MicroVM image/volume updates.
- Same isolation model for Porter and customer workloads.
- Local first node works out of the box.
- Remote fleet nodes follow the same agent protocol.

## Bootstrap sequence

```text
1. Operator installs porter-agent on a fresh KVM host.
2. Agent starts and runs preflight checks.
3. If no Porter URL is configured, agent enters bootstrap mode.
4. Agent pulls the signed Porter control-plane image.
5. Agent creates the Porter data volume.
6. Agent starts the Porter control-plane MicroVM.
7. Agent waits for Porter /healthz.
8. Agent registers itself as the first fleet node using a local bootstrap key.
9. Agent starts a host port forward to Porter's API/UI.
10. Operator opens the dashboard and creates a join token for more nodes.
```

The bootstrap key is local-only and one-time. It is never printed in logs.

## Port forwarding

The control-plane MicroVM has a private IP on the host bridge.

The agent forwards:

```text
host :443  -> control-plane VM :8080
host :80   -> control-plane VM :8080
```

Remote agents connect outbound to `wss://porter.example.com/v1/fleet/stream`.
The host agent forwards that traffic to the Porter MicroVM. Remote nodes still
make only outbound connections.

## Persistent data

Porter's SQLite database must not live inside the ephemeral control-plane
rootfs.

```text
host path:  /var/lib/porter-agent/control-plane/data/
VM mount:   /var/lib/porter/data/
DB:         /var/lib/porter/data/porter.db
```

The agent owns the volume attachment and persists its identity in the local
`porter-agent.db`.

## Failure handling

### Control-plane VM crashes
Agent detects the stopped VM and restarts it with the same data volume.

### Agent restarts
Control-plane and workload VMs keep running in their own systemd units.
Agent reconnects to Porter and reconciles local state.

### Host reboots
Agent starts first, then starts the control-plane VM, then reconciles all
recorded workload VMs.

### Control-plane VM unhealthy
Agent reports `control_plane_unhealthy`. It can restart the VM. If repeated
failures occur, it keeps the data volume and surfaces an explicit doctor error.

## Updating Porter

1. Agent receives/reads a signed control-plane image reference.
2. Agent takes a SQLite backup/snapshot.
3. Agent starts a new Porter MicroVM with the new image and the same data volume.
4. New VM must pass `/healthz` and a schema compatibility check.
5. Agent switches the host port forward to the new VM.
6. Old VM is stopped after the new one is healthy.
7. On failure, switch back to the old VM and keep the previous image.

## Network path for remote agents

```text
remote agent -- outbound WSS:443 --> host agent -- local forward --> Porter VM
```

This means Porter does not need direct host network privileges, and remote nodes
never need inbound ports.

## What the agent must never do

- expose an unauthenticated shell
- let Porter write directly to host paths
- mount the host root filesystem into Porter
- pass Firecracker API sockets into Porter
- trust an unsigned control-plane image
- delete a data volume automatically

## V1 scope

P0:

- agent bootstrap mode
- control-plane VM supervision
- persistent data volume
- host port forward
- local bootstrap registration
- restart/reconnect reconciliation

P1:

- signed control-plane image updates
- automatic backup before update
- health-gated switchover
- rollback to previous Porter image


