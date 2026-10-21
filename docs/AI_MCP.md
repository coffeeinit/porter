# AI Access via MCP

Porter exposes an authenticated MCP endpoint so AI agents use the same control
plane as humans and CLI automation.

## Endpoint

```text
POST /mcp
Authorization: Bearer <admin-or-ai-token>
Content-Type: application/json
```

Transport is JSON-RPC 2.0 over HTTP for V1. A stdio/SSE transport can be added
later without changing the tool contract.

## Supported methods

- `initialize`
- `notifications/initialized`
- `tools/list`
- `tools/call`

## Tools

| Tool | Purpose |
|---|---|
| `porter_list_nodes` | Fleet nodes and capacity |
| `porter_list_projects` | Projects |
| `porter_create_project` | Create project |
| `porter_create_app` | Create app |
| `porter_deploy` | Create deployment |
| `porter_scale_app` | Scale desired replicas |
| `porter_list_operations` | Recent operation ledger |\n| `porter_get_node` / `porter_node_metrics` / `porter_node_logs` / `porter_node_vms` | Fleet node detail |\n| `porter_cordon_node` / `porter_uncordon_node` / `porter_drain_node` | Node maintenance |\n| `porter_list_domains` / `porter_create_domain` | Domain routing |\n| `porter_gateway_stats` / `porter_gateway_requests` | Gateway traffic |\n| `porter_create_preview` | Automatic PR preview domain |

Every tool call goes through the same authorization and store layer as the REST
API. AI must not bypass policy or get a hidden superuser path.

## Example

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "tools/call",
  "params": {
    "name": "porter_create_project",
    "arguments": {"name": "ai-demo"}
  }
}
```

## Next

- scoped AI tokens distinct from admin tokens
- approval gates for destructive tools
- MCP resources for project/project topology
- streaming tool progress
- audit metadata on every AI call


