# Porter

Porter is a self-hosted, MicroVM-native application platform.

## Two deployable projects

| Project | Binary | Responsibility |
|---|---|---|
| `porter/` | `porter` | control plane + UI + API + scheduler + embedded fleet manager |
| `porter-agent/` | `porter-agent` | the only privileged host agent; controls Firecracker, networking, storage, local state |

The shared wire contract lives in `protocol/`.

V1 uses a host-installed Porter service. The first host runs both Porter and
`porter-agent`; every additional host runs only `porter-agent`.

```text
Host machine
  |
  +-- porter-agent (privileged)
        |
        +-- porter (unprivileged host service)
        |     -> UI / API / scheduler / fleet manager
        |     -> porter.db on persistent storage
        |
        +-- workload MicroVMs
              -> customer applications
```

Only the agent touches host resources. Porter never controls Firecracker
directly.

## Why this is robust

- Porter cannot access KVM, Firecracker sockets, or TAP devices.
- The host attack surface is one small agent.
- Agent restarts do not kill VMs.
- Porter restarts keep persistent state and do not require host privileges.
- The same control-plane code works in host-service or MicroVM mode.
- Per-node SQLite prevents guessing local VM state from processes.

## Core control model

Porter is built around:

- desired state
- reconciliation
- capacity-aware scheduling
- cordon/drain
- rolling upgrades
- health checks and recovery

See [`docs/CONTROLLER_MODEL.md`](docs/CONTROLLER_MODEL.md).

## V1.0.0 scope

- one-command agent install / first-node bootstrap
- Porter as unprivileged host service (default) or optional MicroVM mode
- agent-managed persistent Porter data
- one-command fleet join
- per-agent SQLite actual-state store
- nodes, heartbeat, cordon/drain, agent upgrades
- projects -> apps -> deployments -> previews -> replicas -> microVMs
- Vercel-style preview URL per pull request
- immutable deployments, promote/rollback
- image catalog + OCI/rootfs deploy path
- Git build path
- scheduling, reconciliation, autoscaling, canary
- logs, metrics, doctor
- domains, gateway, TLS
- persistent volumes + snapshots
- embedded admin UI + CLI

**Deferred:** customers, billing, marketplace, AI operator, compliance audit,
Kubernetes API compatibility, HA/multi-region control plane.

## Quick start target

```bash
# fresh Linux/KVM host
curl -fsSL https://porter.example.com/install-agent.sh | sh

# Agent installs/starts Porter and registers this host as node 1.
# Then from the dashboard/CLI:
porter fleet join-token --ttl 15m

# additional nodes
curl -fsSL https://porter.example.com/install-agent.sh | sh -s -- \
  --server https://porter.example.com \
  --token <join-token>
```

## Repository layout

```text
porter/          # control plane
porter-agent/    # privileged host agent
protocol/        # shared wire types
docs/            # SRS, architecture, UI spec, plans
legacy/          # archived old implementation/prototypes/images
```

## Read first

- [`docs/SRS_V1.md`](docs/SRS_V1.md)
- [`docs/UI_SPEC_V1.md`](docs/UI_SPEC_V1.md)
- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)
- [`docs/CONTROLLER_MODEL.md`](docs/CONTROLLER_MODEL.md)
- [`docs/ADR-001-CONTROL-PLANE-PLACEMENT.md`](docs/ADR-001-CONTROL-PLANE-PLACEMENT.md)
- [`docs/CONTROL_PLANE_AS_MICROVM.md`](docs/CONTROL_PLANE_AS_MICROVM.md)
- [`docs/FLEET_ARCHITECTURE.md`](docs/FLEET_ARCHITECTURE.md)
- [`docs/FLEET_STATE.md`](docs/FLEET_STATE.md)
- [`docs/CONNECTIVITY.md`](docs/CONNECTIVITY.md)
- [`docs/PROTOCOL.md`](docs/PROTOCOL.md)
- [`docs/BACKEND_V1_PLAN.md`](docs/BACKEND_V1_PLAN.md)
- [`docs/ROBUSTNESS_AND_SCALE_PLAN.md`](docs/ROBUSTNESS_AND_SCALE_PLAN.md)
- [`docs/DEVOPS_FEATURE_GAPS.md`](docs/DEVOPS_FEATURE_GAPS.md)

## Core rules

1. Firecracker is the runtime boundary.
2. Only `porter-agent` touches host resources.
3. Porter can run as a host service or optional MicroVM.
4. `porter.db` is persistent; `porter-agent.db` is local actual state.
5. Agent connects outbound over mTLS WebSocket.
6. WireGuard is optional; WSS is the control baseline.
7. Every command is idempotent with an operation ID.
8. Previews are deployment-scoped and ephemeral.
9. No fake UI success; unimplemented features are labelled.


- [`docs/AGENT_MODEL.md`](docs/AGENT_MODEL.md)
- [`docs/PRE_IMPLEMENTATION_CHECKLIST.md`](docs/PRE_IMPLEMENTATION_CHECKLIST.md)

- [`docs/V1_IMPLEMENTATION_STATUS.md`](docs/V1_IMPLEMENTATION_STATUS.md)

- [`docs/AI_MCP.md`](docs/AI_MCP.md)

- [`docs/GUEST_NETWORK.md`](docs/GUEST_NETWORK.md)

- [`docs/IMAGE_BUILD_PIPELINE.md`](docs/IMAGE_BUILD_PIPELINE.md)

- [`docs/AUTOSCALE_AND_IDLE.md`](docs/AUTOSCALE_AND_IDLE.md)

- [`docs/PERSISTENCE_MODEL.md`](docs/PERSISTENCE_MODEL.md)

- [`docs/GATEWAY_AND_DOMAINS.md`](docs/GATEWAY_AND_DOMAINS.md)

- [`docs/FLEET_DETAIL_OBSERVABILITY.md`](docs/FLEET_DETAIL_OBSERVABILITY.md)

- [`docs/GATEWAY_SLICE_STATUS.md`](docs/GATEWAY_SLICE_STATUS.md)

- [`docs/REMAINING_WORK.md`](docs/REMAINING_WORK.md)

- [`docs/IMAGE_PIPELINE_STATUS.md`](docs/IMAGE_PIPELINE_STATUS.md)

- [`docs/GITHUB_COMPOSE_STATUS.md`](docs/GITHUB_COMPOSE_STATUS.md)

- [`docs/HANDOFF.md`](docs/HANDOFF.md)
- [`docs/NEXT_TASKS.md`](docs/NEXT_TASKS.md)

- [`docs/PLAN_AUDIT.md`](docs/PLAN_AUDIT.md)
- [`docs/PLAN_V2.md`](docs/PLAN_V2.md)

- [`docs/ROADMAP_v0.2_v0.3.md`](docs/ROADMAP_v0.2_v0.3.md)

- [`docs/FIRECRAB_REFERENCE.md`](docs/FIRECRAB_REFERENCE.md)

- [`docs/ADDRESSING_POLICY.md`](docs/ADDRESSING_POLICY.md)

- [`docs/E2E_HTTP_VERIFIED.md`](docs/E2E_HTTP_VERIFIED.md)
