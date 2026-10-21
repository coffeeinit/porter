#!/usr/bin/env bash
set -euo pipefail
ROOT=/mnt/d/github/porter
WORK=/tmp/porter-e2e-mcp
B=http://127.0.0.1:18102
ADMIN=testadmin
rm -rf "$WORK"; mkdir -p "$WORK"
pkill -f '/tmp/porterbin/porter server' 2>/dev/null || true
sleep 1
cd "$ROOT/porter" && go build -buildvcs=false -o /tmp/porterbin/porter ./cmd/porter
nohup env PORTER_ADMIN_TOKEN=$ADMIN PORTER_DB=$WORK/porter.db PORTER_ADDR=127.0.0.1:18102 PORTER_GATEWAY_ADDR=off /tmp/porterbin/porter server >"$WORK/porter.log" 2>&1 &
for i in $(seq 1 30); do curl -sf "$B/healthz" >/dev/null && break; sleep 1; done
auth=(-H "Authorization: Bearer $ADMIN" -H 'Content-Type: application/json')
init=$(curl -sS -X POST "$B/mcp" "${auth[@]}" -d '{"jsonrpc":"2.0","id":1,"method":"initialize"}')
echo "$init" | grep -q '"protocolVersion"' || { echo "initialize failed: $init"; exit 1; }
tools=$(curl -sS -X POST "$B/mcp" "${auth[@]}" -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}')
echo "$tools" | grep -q 'porter_create_project' || { echo "tools/list missing create_project"; exit 1; }
call=$(curl -sS -X POST "$B/mcp" "${auth[@]}" -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"porter_create_project","arguments":{"name":"mcp-e2e"}}}')
echo "$call" | grep -q 'mcp-e2e' || { echo "tools/call failed: $call"; exit 1; }
echo "MCP_E2E_OK"