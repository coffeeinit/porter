# Why Guest Networking Is Needed

TAP/bridge/NAT only builds the **host side** of the network path.

```text
Internet -> host bridge -> TAP -> Firecracker virtio-net -> guest kernel
```

The guest kernel still needs to:

- bring up `eth0`
- assign the allocated IP (e.g. `10.42.0.2/16`)
- set the default route via `10.42.0.1`
- configure DNS
- start the application listening on the expected port

Without guest-side setup, the VM can boot but the application is unreachable.

## Options

1. **Guest agent / cloud-init** configures networking on boot.
2. **Bake network config into the image** using kernel command line or init script.
3. **DHCP server on the bridge** gives the guest an IP automatically.
4. **MMDS metadata** lets a guest agent retrieve IP/gateway/env.

## V1 recommendation

- Porter allocates guest IP/MAC.
- Agent creates TAP/bridge/NAT.
- Guest image runs a tiny `porter-guest-init` on first boot that:
  - reads MMDS or kernel args
  - configures `eth0`, route, DNS
  - starts the workload entrypoint

Guest networking is required for real HTTP traffic and health checks. Host-side
TAP/NAT alone only provides the plumbing.
