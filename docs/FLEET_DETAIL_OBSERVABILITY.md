# Fleet Detail & Observability

Each fleet node has a detail view:

```text
GET /v1/fleet/nodes/{id}
GET /v1/fleet/nodes/{id}/metrics
GET /v1/fleet/nodes/{id}/logs
GET /v1/fleet/nodes/{id}/vms
POST /v1/fleet/nodes/{id}/cordon
POST /v1/fleet/nodes/{id}/uncordon
POST /v1/fleet/nodes/{id}/drain
```

## UI

Node detail shows:

- status / arch / region
- CPU free vs total
- memory free vs total
- hosted MicroVMs
- node metrics history chart
- node logs
- cordon/drain actions later wired into buttons

## MCP tools

```text
porter_get_node
porter_node_metrics
porter_node_logs
porter_node_vms
porter_cordon_node
porter_uncordon_node
porter_drain_node
```

## Data sources

- agent heartbeat -> metrics samples
- agent log shipper -> node logs
- replica rows -> hosted VMs
- node table -> capacity/state

## Next

- per-VM CPU/memory/network charts
- live log streaming
- network waterfall trace
- bandwidth chart
- drain-safe eviction with rescheduling
