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

## Product target (not a completion claim)

Projects/groups contain apps/workloads; each workload has immutable releases and one or more Firecracker microVM replicas, scheduled and reconciled across agent-managed hosts. Porter aims to provide healthy replica failover, autoscaling and wake-on-request, previews and release promotion/rollback, an inbuilt reverse proxy/load balancer, custom domains and DNS-provider/TLS lifecycle, and scoped MCP access. Inbuilt OpenTelemetry-style metrics, logs and traces must render in Porter’s own UI; Grafana is optional, not a prerequisite.

Docker/OCI/Compose are build/import inputs only; the customer runtime is Firecracker. Porter does not provide hosted starter templates or managed databases. See [`docs/IMPLEMENTATION_PLAN.md`](docs/IMPLEMENTATION_PLAN.md) for verified slices and open release blockers. The root [`porternew_observability.html`](porternew_observability.html) is a mock UI concept, not a live feature list; the active UI is `porter/web/index.html`.

Planned self-hosted deployment includes an operator UI/CLI, first-node bootstrap, joined agent nodes, SQLite-backed state, and host-installed control plane. Do not treat the plan as a claim that every listed capability is implemented or production-verified.

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

Remote agent connections require HTTPS; the agent rejects plaintext HTTP except to loopback addresses for local development. Standard TLS certificate verification is enabled. Mutual TLS is not implemented yet, so don't expose the control-plane API without a trusted HTTPS endpoint and appropriate network controls.

Image API/MCP builds and GitHub webhook builds are disabled by default because the current builder invokes host Docker/Git and is not isolated. `PORTER_ENABLE_UNISOLATED_BUILDS=true` is a temporary operator override for trusted sources only; do not use it as a security boundary for untrusted code.

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
5. Remote agent links require verified HTTPS; outbound mTLS WebSocket remains the target and is not implemented yet.
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
