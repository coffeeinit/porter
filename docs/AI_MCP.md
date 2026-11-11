# AI access via MCP

Porter exposes an authenticated MCP endpoint. The human operator keeps the existing admin token. AI and automation clients can use separate, revocable bearer tokens with explicit capabilities and project, app, or node scopes.

## Endpoint and token management

MCP uses JSON-RPC 2.0 over HTTP:

```text
POST /mcp
Authorization: Bearer <admin-token-or-scoped-mcp-token>
Content-Type: application/json
```

Create a credential with the operator token. The response contains the raw bearer token once; Porter stores only its SHA-256 hash. Listing credentials returns metadata, not bearer values. Revoke a token by its returned ID.

```http
POST /v1/mcp/tokens
Authorization: Bearer <admin-token>
Content-Type: application/json
```

```json
{
  "name": "deploy-assistant",
  "capabilities": ["projects:read", "apps:read", "apps:deploy"],
  "project_ids": ["prj_..."],
  "app_ids": [],
  "node_ids": []
}
```

```text
GET    /v1/mcp/tokens
DELETE /v1/mcp/tokens/{id}
```

Capabilities are explicit. Project scopes also grant access to apps in that project; app scopes grant access only to the named apps. Node reads use `node_ids`. Use `"*"` only when the AI principal is meant to access every resource in that scope. The current policy reserves global operations, metrics, gateway, and image-catalog reads for the human operator; the scoped credential UI disables those options rather than implying they are available.

## Capabilities

The token issuer accepts these capability names:

- `projects:read`, `projects:create`
- `apps:read`, `apps:create`, `apps:deploy`, `apps:scale`, `apps:configure`, `apps:operate`
- `fleet:read`, `fleet:write`
- `operations:read`, `volumes:read`, `volumes:create`, `snapshots:read`, `snapshots:create`
- `logs:read`, `metrics:read`, `domains:read`, `domains:write`, `gateway:read`
- `images:read`, `images:build`, `images:write`

The MCP `tools/list` response includes only tools enabled by the caller's capabilities. Porter checks each `tools/call` again before dispatch; an unlisted or unknown tool is denied. Project, app, and node list results are filtered to the token's scope. AI tool calls record the principal ID, tool name, and outcome without storing tool arguments or results.

## Operator-only tools

Until Porter has an approval workflow, scoped AI tokens cannot create snapshots, attach or resize volumes, create public domains or preview routes, build or register images, inspect arbitrary host paths, change replica traffic weights, restore snapshots, or cordon/uncordon/drain nodes. These remain available to the human operator through the admin token. Do not grant the admin token to an AI client.

## Supported MCP methods

- `initialize`
- `notifications/initialized`
- `tools/list`
- `tools/call`

## Current limits

Tokens can have an optional expiry and can be created, listed, and revoked in the operator Settings UI. The raw token is shown once at creation. The regular REST API still requires the operator admin token; MCP scopes do not authorize direct REST calls. Approval queues, direct REST scopes, and more granular snapshot/log resource mapping remain follow-up work.

The removed `legacy/backend` RBAC resolver informed the capability-based approach, but Porter does not import its customer/reseller/team tenancy model or claim the legacy AI routes are active features.
