# Firecrab Reference Notes

Source reviewed: `SteelCrab/firecrab` / `coffeeinit/firecrab` README and public docs.

Firecrab is a strong single-host MicroVM platform reference. It is Rust-based and
focuses on one Linux host with a browser dashboard, CLI, REST API, helper daemon,
images, networks, storage, and a guest serial terminal.

Porter is Go-based, two-binary, and aims for a single control plane with multiple
fleet nodes. Firecrab is a reference for product shape and guest lifecycle, not a
codebase to copy.

## Ideas Porter should adopt

### 1. First-class MicroImage / MicroNetwork / MicroStorage
Firecrab makes these explicit resources:

```text
M2Image
MicroNetwork
MicroStorage
MicroVM
```

Porter currently has images, networks, volumes, and domains, but they are not all
first-class in the UI/API. Add:

```text
Image      -> immutable kernel/rootfs/OCI artifact
Network    -> project/private network + IPAM
Storage    -> volume class + volume instance
```

### 2. Guest network-ready signal
Firecrab marks a VM running only when the guest reports its network is ready.

Porter should add:

```text
vm.booting
vm.network_ready
vm.app_healthy
vm.running
```

The gateway should activate traffic only after `network_ready` and app health.

### 3. Browser serial terminal
Firecrab exposes a VM browser serial terminal.

Porter has serial logs but no terminal UI. Add:

- browser terminal via SSE/WebSocket
- serial input line for debug
- copy/download logs
- read-only until guest-vsock/agent exists

### 4. Installer + service management
Firecrab has `install.sh` and `firecrab service start|status|debug|stop`.

Porter needs:

```text
install-porter.sh
porter-agent join
porter-agent run
porter-agent service status
porter-agent service logs
```

### 5. Image verification
Firecrab verifies image integrity before creating a VM.

Porter should hard-fail on:

- missing kernel/rootfs
- checksum mismatch
- wrong architecture
- corrupt ext4
- unsupported entrypoint

### 6. CLI/REST/dashboard parity
Firecrab documents dashboard, CLI, and REST as equal interfaces.

Porter must keep:

- same API for UI/CLI/MCP
- no hidden privileged UI path
- operation IDs visible in all clients

### 7. Clear guest lifecycle docs
Adopt Firecrab's clear distinction between:

```text
host helper
control API
guest
image
network
storage
```

## Ideas to reject for Porter V1

- Rust rewrite. Porter is Go; keep it.
- macOS/Windows MicroVM hosting. V1 targets Linux/KVM.
- Single-host-only data model. Porter needs fleet/multi-node.
- Copying Firecrab code or naming.
- macOS microManager complexity before Linux is excellent.

## Mapping to Porter roadmap

### v0.1.3
- guest network-ready signal
- app HTTP health
- serial log viewer foundation

### v0.2.0
- first-class Image/Network/Storage APIs
- browser serial terminal
- image verification
- CLI basics
- one-command installer/handoff

### v0.3.0
- multi-node network/storage placement
- service management commands
- CLI/REST/dashboard parity
- docs/runbooks
