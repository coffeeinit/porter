#!/usr/bin/env bash
set -euo pipefail
ROOT=/mnt/d/github/porter
WORK=/tmp/porter-e2e-api
B=http://127.0.0.1:18103
ADMIN=testadmin
rm -rf "$WORK"; mkdir -p "$WORK/src"
pkill -f '/tmp/porterbin/porter server' 2>/dev/null || true
sleep 1
cd "$ROOT/porter" && go build -buildvcs=false -o /tmp/porterbin/porter ./cmd/porter
nohup env PORTER_ADMIN_TOKEN=$ADMIN PORTER_DB=$WORK/porter.db PORTER_ADDR=127.0.0.1:18103 PORTER_GATEWAY_ADDR=off PORTER_BASE_DOMAIN=porter.local PORTER_BACKUP_DIR=$WORK/backups /tmp/porterbin/porter server >"$WORK/porter.log" 2>&1 &
for i in $(seq 1 30); do curl -sf "$B/healthz" >/dev/null && break; sleep 1; done
A=(-H "Authorization: Bearer $ADMIN" -H 'Content-Type: application/json')
jget(){ python3 -c "import json,sys; print(json.load(sys.stdin).get('$1',''))"; }
PROJ=$(curl -sS -X POST "$B/v1/projects" "${A[@]}" -d '{"name":"api-e2e"}')
PID=$(printf '%s' "$PROJ" | jget id)
[ -n "$PID" ] || { echo "project create failed"; exit 1; }
APP=$(curl -sS -X POST "$B/v1/projects/$PID/apps" "${A[@]}" -d '{"name":"web","image":"built://demo","port":80,"vcpus":1,"memory_mb":256,"replicas":1,"persistence_mode":"ephemeral"}')
AID=$(printf '%s' "$APP" | jget id)
[ -n "$AID" ] || { echo "app create failed"; exit 1; }
curl -sS -X POST "$B/v1/apps/$AID/deployments" "${A[@]}" -d '{"image":"built://demo","replicas":1}' >/dev/null
curl -sS "$B/v1/apps/$AID/deployments" "${A[@]}" | grep -q 'dep_' || { echo "deployments list failed"; exit 1; }
curl -sS -X POST "$B/v1/apps/$AID/env" "${A[@]}" -d '{"key":"FOO","value":"bar","secret":false}' >/dev/null
curl -sS "$B/v1/apps/$AID/env" "${A[@]}" | grep -q 'FOO' || { echo "env failed"; exit 1; }
curl -sS -X DELETE "$B/v1/apps/$AID/env/FOO" "${A[@]}" >/dev/null
curl -sS -X PUT "$B/v1/apps/$AID/autoscale" "${A[@]}" -d '{"enabled":true,"min_replicas":1,"max_replicas":3,"cpu_target":70,"idle_timeout_sec":300,"scale_to_zero":true}' >/dev/null
curl -sS "$B/v1/apps/$AID/autoscale" "${A[@]}" | grep -q 'max_replicas' || { echo "autoscale failed"; exit 1; }
VOL=$(curl -sS -X POST "$B/v1/projects/$PID/volumes" "${A[@]}" -d '{"name":"data","size_mb":64}')
VID=$(printf '%s' "$VOL" | jget id)
[ -n "$VID" ] || { echo "volume create failed"; exit 1; }
curl -sS "$B/v1/projects/$PID/volumes" "${A[@]}" | grep -q "$VID" || { echo "volume list failed"; exit 1; }
curl -sS -X POST "$B/v1/volumes/$VID/attach" "${A[@]}" -d "{\"app_id\":\"$AID\"}" >/dev/null
curl -sS "$B/v1/domains" "${A[@]}" | grep -q 'web.porter.local' || { echo "auto domain failed"; exit 1; }
curl -sS "$B/v1/routes" "${A[@]}" | grep -q 'web.porter.local' || { echo "route failed"; exit 1; }
curl -sS "$B/v1/gateway/stats" "${A[@]}" | grep -q 'requests' || { echo "gateway stats failed"; exit 1; }
curl -sS "$B/v1/gateway/requests" "${A[@]}" >/dev/null
curl -sS "$B/v1/traces" "${A[@]}" >/dev/null
curl -sS "$B/v1/logs" "${A[@]}" >/dev/null
curl -sS "$B/v1/metrics" "${A[@]}" >/dev/null
IMG=$(curl -sS -X POST "$B/v1/images" "${A[@]}" -d '{"name":"direct-demo","source_type":"microvm","source_ref":"local","kernel_ref":"/tmp/vmlinux","rootfs_ref":"/tmp/rootfs.ext4"}')
curl -sS "$B/v1/images" "${A[@]}" | grep -q 'direct-demo' || { echo "image list failed"; exit 1; }
mkdir -p "$WORK/src"; echo '{"dependencies":{"react":"18"}}' > "$WORK/src/package.json"
curl -sS -X POST "$B/v1/images/detect" "${A[@]}" -d "{\"path\":\"$WORK/src\"}" | grep -q 'react' || { echo "detect failed"; exit 1; }
BK=$(curl -sS -X POST "$B/v1/system/backup" "${A[@]}")
BP=$(printf '%s' "$BK" | jget backup_path)
[ -n "$BP" ] || { echo "backup failed: $BK"; exit 1; }
curl -sS -X POST "$B/v1/system/backup/verify" "${A[@]}" -d "{\"path\":\"$BP\"}" | grep -q 'ok' || { echo "backup verify failed"; exit 1; }
MCP=$(curl -sS -X POST "$B/mcp" "${A[@]}" -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}')
echo "$MCP" | grep -q 'porter_create_project' || { echo "mcp tools failed"; exit 1; }
echo "$MCP" | grep -q 'porter_list_traces' || { echo "mcp trace tool missing"; exit 1; }
echo "API_E2E_OK"