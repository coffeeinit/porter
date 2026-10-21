# PORTER HANDOFF — 2026-10-18

## 1. What this repository is now

Porter is a host-installed Firecracker platform with two Go projects:

```text
porter/          control plane + API + UI + scheduler + embedded gateway
porter-agent/    privileged host agent + Firecracker runtime + local SQLite
protocol/        shared wire types
```

Porter runs as an unprivileged host systemd service. The agent is the only
component that touches host KVM, Firecracker, TAP, NAT, volumes, or guest IPs.

The old implementation, old docs, old prototypes, and old image catalog are in
`legacy/` and must not be imported.

## 2. Current implemented features

### Control plane
- SQLite store and migrations
- admin bearer-token auth
- join-token creation
- agent enrollment
- node token auth
- heartbeat and capacity
- projects/apps/deployments/replicas
- simple scheduler
- command queue
- operation ledger
- event history
- autoscaling policy
- idle timeout / scale-to-zero
- volume create/attach/resize
- snapshot records
- logs ingestion/query
- metrics samples/query
- domains and routes
- gateway request logs/stats
- image catalog records
- direct MicroVM image records
- Dockerfile/Docker/OCI/Git/Compose image build pipeline
- embedded single-page dashboard
- MCP HTTP endpoint and tools

### Agent
- one binary on every node
- join + enrollment
- local SQLite
- heartbeat
- command polling
- real Firecracker lifecycle
- TAP/bridge/NAT
- kernel `ip=` guest network boot args
- persistent `/dev/vdb` data drive
- snapshot create (pause/resume)
- snapshot restore command
- serial log shipping
- process liveness health reporting
- signed-update path not implemented yet

### Verified live in WSL Ubuntu
- real Firecracker boot under KVM
- real TAP/bridge/NAT
- persistent volume attached as `/dev/vdb`
- snapshot create
- logs and metrics
- Dockerfile -> OCI tar -> rootfs.ext4
- guest-init binary injected into built rootfs
- MCP tools
- agent restart while VM survives
- Porter restart with SQLite state recovery

## 3. Quick start

### Build

```bash
cd /mnt/d/github/porter   # WSL path
make test
make build
```

Binaries:

```text
bin/porter
bin/porter-agent
bin/porter-guest-init
```

### Run Porter

```bash
PORTER_ADMIN_TOKEN=testadmin \
PORTER_DB=/tmp/porter/porter.db \
PORTER_ADDR=127.0.0.1:8080 \
PORTER_GATEWAY_ADDR=:8081 \
PORTER_BASE_DOMAIN=porter.local \
./bin/porter server
```

Open:

```text
http://127.0.0.1:8080/?token=testadmin
```

### Join a node

```bash
# create a join token through the UI/API first
sudo ./bin/porter-agent join \
  --server http://127.0.0.1:8080 \
  --token <join-token> \
  --name my-node \
  --data-dir /var/lib/porter-agent \
  --kernel /path/to/vmlinux \
  --rootfs /path/to/rootfs.ext4

sudo ./bin/porter-agent run \
  --data-dir /var/lib/porter-agent \
  --network on
```

TAP/bridge/NAT requires root or `CAP_NET_ADMIN`.

### WSL test assets

```text
/mnt/d/github/porter/legacy/porter-images/bases/alpine/3.21/out/vmlinux
/mnt/d/github/porter/legacy/porter-images/bases/alpine/3.21/out/rootfs.ext4
```

## 4. Code map

```text
porter/cmd/porter/main.go              server startup, tick loop, gateway listener
porter/internal/api/                  REST + MCP endpoints
porter/internal/store/store.go        SQLite schema and all persistence
porter/internal/gateway/gateway.go    host routing + weighted reverse proxy
porter/internal/imagebuilder/         Dockerfile/OCI/Git/Compose -> rootfs
porter/web/index.html                 embedded dashboard
porter/migrations?                    none yet; schema lives in store.go

porter-agent/cmd/porter-agent/        join/run agent
porter-agent/cmd/porter-guest-init/   guest network + entrypoint starter
porter-agent/internal/runtime/        Firecracker/TAP/NAT/snapshot
porter-agent/internal/store/          local SQLite
porter-agent/internal/client/         Porter API client

protocol/types.go                     shared wire structs
```

## 5. API map (current)

```text
GET  /healthz
GET  /mcp

POST /v1/fleet/join-tokens
POST /v1/fleet/enroll
POST /v1/fleet/heartbeat
GET  /v1/fleet/commands
POST /v1/fleet/result
POST /v1/fleet/logs
POST /v1/fleet/snapshot
POST /v1/fleet/vmstate

GET  /v1/nodes
GET  /v1/fleet/nodes/{id}
GET  /v1/fleet/nodes/{id}/metrics
GET  /v1/fleet/nodes/{id}/logs
GET  /v1/fleet/nodes/{id}/vms
POST /v1/fleet/nodes/{id}/cordon
POST /v1/fleet/nodes/{id}/uncordon
POST /v1/fleet/nodes/{id}/drain

GET/POST /v1/projects
GET/POST /v1/projects/{id}/apps
GET  /v1/apps
POST /v1/apps/{id}/deployments
POST /v1/apps/{id}/scale
GET/PUT /v1/apps/{id}/autoscale
POST /v1/apps/{id}/activity
POST /v1/apps/{id}/wake
POST /v1/apps/{id}/previews

GET/POST /v1/projects/{id}/volumes
POST /v1/volumes/{id}/attach
POST /v1/volumes/{id}/resize

GET/POST /v1/snapshots
POST /v1/snapshots/{id}/restore
GET  /v1/logs
GET  /v1/metrics

GET/POST /v1/domains
GET/POST /v1/routes
GET  /v1/gateway/stats
GET  /v1/gateway/requests

GET/POST /v1/images
POST /v1/images/build
POST /v1/images/detect
```

## 6. MCP

Endpoint:

```text
POST /mcp
Authorization: Bearer <admin-token>
```

Implemented tools include:

```text
porter_list_nodes
porter_list_projects
porter_create_project
porter_create_app
porter_deploy
porter_scale_app
porter_list_operations
porter_list_volumes
porter_create_volume
porter_attach_volume
porter_resize_volume
porter_list_snapshots
porter_create_snapshot
porter_restore_snapshot
porter_list_logs
porter_list_metrics
porter_get_autoscale
porter_set_autoscale
porter_touch_app
porter_wake_app
porter_list_domains
porter_create_domain
porter_gateway_stats
porter_gateway_requests
porter_create_preview
porter_get_node
porter_node_metrics
porter_node_logs
porter_node_vms
porter_cordon_node
porter_uncordon_node
porter_drain_node
porter_set_replica_weight
porter_list_images
porter_create_image
porter_build_image
porter_detect_framework
```

## 7. Current known limitations

- UI is still one large page, not routed pages.
- No live log streaming (only query).
- Gateway has no ACME; TLS only via cert/key files.
- Gateway is HTTP reverse proxy; no full WebSocket tuning yet.
- Guest-init is built and injectable but app startup inside the guest is not
  fully verified.
- Health checks are process liveness only; no HTTP/TCP guest probes.
- Image builds use host Docker, not an isolated builder MicroVM.
- Build runs synchronously; no build log stream or build cache.
- Compose supports only the primary service, not full stacks.
- GitHub auth is local clone based; no GitHub App/webhook-triggered build.
- Snapshot restore endpoint/agent command added, but not yet E2E-verified.
- Volume resize updates metadata; host file resize not yet applied.
- Backups are not implemented.
- Tests are minimal: imagebuilder detection/compose tests only.
- No CI.

## 8. Next tasks in order

1. Multi-page UI restructure
   - Projects list
   - Project detail tabs
   - Deployment detail
   - Replica detail
   - Fleet + node detail
   - Gateway
   - Storage
   - Observability

2. Build pipeline hardening
   - async build operations with progress
   - build log streaming
   - build cache
   - builder isolation

3. Guest app startup + health
   - inject `porter-guest-init` into all images
   - MMDS network/env metadata
   - app entrypoint
   - HTTP/TCP health probes

4. Gateway production
   - ACME
   - wildcard preview TLS
   - WebSocket support
   - canary by deployment version
   - bandwidth/trace charts

5. Persistence
   - host-file volume resize
   - snapshot restore verification
   - backups
   - restore tests

## 9. Common pitfalls

- PowerShell `Set-Content` can write UTF-8 BOM; Go mod/files may need BOM stripped.
- Makefile recipes must use real tabs.
- WSL root is required for TAP/bridge/iptables.
- Docker image `docker save` may produce OCI layout (`index.json`) instead of
  old Docker `manifest.json`; imagebuilder supports both.
- Firecracker snapshot requires paused VM; runtime pauses/resumes.
- Gateway exposes internal VM IPs only in logs/backend; UI must show domains only.

## 10. Suggested first action tomorrow

Open `docs/NEXT_TASKS.md`, start with the UI restructure, then build log streaming.
