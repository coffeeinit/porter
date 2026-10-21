# V1 Implementation Status

## Working now

- `porter` Go module with embedded dashboard
- `porter-agent` Go module
- shared `protocol` module
- Porter SQLite schema and migrations
- admin token auth
- join-token creation
- agent enrollment with hashed one-time token
- node token auth
- agent heartbeat and capacity reporting
- node registry
- project/app/deployment/replica creation
- simple capacity scheduler
- durable operation ledger
- event stream storage
- command queue to agent
- real Firecracker lifecycle on WSL/Linux:
  - start `firecracker`
  - configure boot source, rootfs, machine config
  - send `InstanceStart`
  - real kernel boot observed
- agent local SQLite for VM state
- agent restart does not kill Firecracker VM
- Porter restart preserves SQLite state

## Verified in WSL Ubuntu

```text
porter server
porter-agent join
porter-agent run
create project/app/deployment
scheduler sends vm.apply
agent starts Firecracker
kernel boots on KVM
operation = succeeded
```

The Firecracker serial log showed:

```text
Booting paravirtualized kernel on KVM
smpboot: Total of 1 processors activated
```

## Partial / next

- embedded dashboard with fleet/projects/deploy/operations
- per-VM TAP + bridge + NAT implemented and verified on Linux/WSL root
- MCP tools endpoint implemented\n- volumes (create/attach, /dev/vdb drive) implemented and verified\n- snapshots (pause -> create -> resume) implemented and verified\n- logs ingestion/query implemented and verified\n- metrics samples/query implemented and verified\n- MCP tools extended for volumes/snapshots/logs/metrics\n- GitHub webhook ingestion implemented (build/deploy automation pending)\n- health: process liveness + VM state reporting implemented\n- autoscaling policy, scale-to-zero, idle timeout, wake API, stale command requeue implemented\n- persistence_mode (default persistent) + auto volume creation implemented\n- vCPU/memory/disk exposed in deploy form + passed to Firecracker\n- guest kernel ip= boot argument for guest networking\n- gateway foundation: host routes, reverse proxy, request logs\n- domain management + automatic <app>.<base-domain> routes\n- gateway stats and request log API\n- UI gateway/domain/requests panel\n- no public VM IP/port UX; domains only\n- Dockerfile/Docker/OCI -> rootfs.ext4 image build pipeline implemented and verified\n- guest-init injection into built rootfs\n- image build API + MCP tool\n- GitHub framework detection + generated Dockerfiles implemented\n- git clone branch/subdir build implemented\n- Compose primary-service adapter implemented
- no build/GitHub flow yet
- no preview URL/gateway/TLS yet
- no volumes/snapshots yet
- no rolling upgrade algorithm yet
- no cordon/drain API yet
- no agent self-update yet
- no metrics/log streaming API yet
- no tests beyond build/smoke yet
- no HA control plane

## Next implementation order

1. TAP/bridge/NAT + VM IP/MAC
2. image catalog + kernel/rootfs download/verification
3. replica reconciler with health checks
4. cordon/drain and rolling update
5. preview environments + gateway routing
6. volumes/snapshots
7. logs/metrics streaming
8. build/GitHub integration
9. hardening and E2E tests







