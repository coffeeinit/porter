#!/usr/bin/env bash
set -euo pipefail
ROOT=/mnt/d/github/porter
LIVE=/var/lib/porter-live
B=http://127.0.0.1:18080
G=http://127.0.0.1:18081
ADMIN=porter-admin
TOKEN_FILE="$LIVE/admin-token.txt"
rm -rf "$LIVE"
mkdir -p "$LIVE/work" "$LIVE/ctx" "$LIVE/backups"
cat > "$LIVE/ctx/Dockerfile" <<'EOF'
FROM nginx:alpine
RUN echo porter-live-ok > /usr/share/nginx/html/index.html
EOF
pkill -f '/mnt/d/github/porter/bin/porter server' 2>/dev/null || true
pkill -f '/mnt/d/github/porter/bin/porter-agent run' 2>/dev/null || true
pkill -f 'firecracker --api-sock /var/lib/porter-live' 2>/dev/null || true
sleep 1
cd "$ROOT/porter" && go build -buildvcs=false -o "$ROOT/bin/porter" ./cmd/porter
cd "$ROOT/porter-agent" && go build -buildvcs=false -o "$ROOT/bin/porter-agent" ./cmd/porter-agent
cd "$ROOT/porter-agent" && go build -buildvcs=false -o "$ROOT/bin/porter-guest-init" ./cmd/porter-guest-init
printf '%s\n' "$ADMIN" > "$TOKEN_FILE"
nohup env PORTER_ADMIN_TOKEN=$ADMIN PORTER_DB="$LIVE/porter.db" PORTER_ADDR=0.0.0.0:18080 PORTER_GATEWAY_ADDR=0.0.0.0:18081 PORTER_BASE_DOMAIN=porter.local PORTER_GUEST_INIT="$ROOT/bin/porter-guest-init" PORTER_KERNEL_IMAGE="$ROOT/legacy/porter-images/bases/alpine/3.21/out/vmlinux" PORTER_IMAGE_WORKDIR="$LIVE/work" PORTER_BACKUP_DIR="$LIVE/backups" "$ROOT/bin/porter" server >"$LIVE/porter.log" 2>&1 &
echo $! > "$LIVE/porter.pid"
for i in $(seq 1 30); do curl -sf "$B/healthz" >/dev/null && break; sleep 1; done
curl -sf "$B/healthz" >/dev/null || { echo 'Porter failed to start'; cat "$LIVE/porter.log"; exit 1; }
A=(-H "Authorization: Bearer $ADMIN" -H 'Content-Type: application/json')
cat > "$LIVE/image.json" <<EOF
{"name":"live-nginx","source_type":"dockerfile","context_dir":"$LIVE/ctx","dockerfile":"$LIVE/ctx/Dockerfile","size_mb":512}
EOF
IMG=$(curl -sS -X POST "$B/v1/images/build" "${A[@]}" --data-binary @"$LIVE/image.json")
IMID=$(printf '%s' "$IMG" | python3 -c 'import json,sys; print(json.load(sys.stdin)["image"]["id"])')
for i in $(seq 1 120); do
  IM=$(curl -sS "$B/v1/images/$IMID" "${A[@]}")
  ST=$(printf '%s' "$IM" | python3 -c 'import json,sys; print(json.load(sys.stdin)["status"])')
  [ "$ST" = ready ] && break
  [ "$ST" = failed ] && { echo "$IM"; exit 1; }
  sleep 2
done
ROOTFS=$(printf '%s' "$IM" | python3 -c 'import json,sys; print(json.load(sys.stdin)["rootfs_ref"])')
JOIN=$(curl -sS -X POST "$B/v1/fleet/join-tokens" "${A[@]}" -d '{"ttl_seconds":86400}')
JOIN_TOKEN=$(printf '%s' "$JOIN" | python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])')
"$ROOT/bin/porter-agent" join --server "$B" --token "$JOIN_TOKEN" --name wsl-live --data-dir "$LIVE/agent" --kernel "$ROOT/legacy/porter-images/bases/alpine/3.21/out/vmlinux" --rootfs "$ROOTFS"
nohup "$ROOT/bin/porter-agent" run --data-dir "$LIVE/agent" --network on --init-path /sbin/porter-guest-init >"$LIVE/agent.log" 2>&1 &
echo $! > "$LIVE/agent.pid"
sleep 4
PROJ=$(curl -sS -X POST "$B/v1/projects" "${A[@]}" -d '{"name":"live-demo"}')
PID=$(printf '%s' "$PROJ" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
APP=$(curl -sS -X POST "$B/v1/projects/$PID/apps" "${A[@]}" -d '{"name":"live-app","image":"built://live-nginx","port":80,"vcpus":1,"memory_mb":256,"replicas":1,"persistence_mode":"ephemeral"}')
AID=$(printf '%s' "$APP" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
curl -sS -X POST "$B/v1/apps/$AID/deployments" "${A[@]}" -d '{"image":"built://live-nginx","replicas":1}' >/dev/null
for i in $(seq 1 90); do
  NODE=$(curl -sS "$B/v1/nodes" "${A[@]}" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d[0]["id"] if d else "")')
  [ -z "$NODE" ] && { sleep 2; continue; }
  VMS=$(curl -sS "$B/v1/fleet/nodes/$NODE/vms" "${A[@]}")
  echo "$VMS" | grep -q '"health":"healthy"' && break
  sleep 2
done
echo
echo '============================================'
echo ' PORTER LIVE WSL DEMO'
echo '============================================'
echo "Dashboard: http://localhost:18080/?token=$ADMIN"
echo "Admin token: $ADMIN"
echo "Gateway:  http://localhost:18081/"
echo "Domain:   http://live-app.porter.local:18081/"
echo
echo 'If your browser cannot resolve the domain, run this in Windows PowerShell as Administrator:'
echo '  Add-Content C:\Windows\System32\drivers\etc\hosts "127.0.0.1 live-app.porter.local"'
echo
echo 'Gateway check command:'
echo "  curl -H 'Host: live-app.porter.local' http://127.0.0.1:18081/"
echo
echo 'Files:'
echo "  $LIVE/porter.log"
echo "  $LIVE/agent.log"
echo "  $LIVE/porter.db"
echo '============================================'
curl -sS -H 'Host: live-app.porter.local' "$G/" || true
echo