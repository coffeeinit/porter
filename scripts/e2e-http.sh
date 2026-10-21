#!/usr/bin/env bash
set -euo pipefail
ROOT=/mnt/d/github/porter
B=http://127.0.0.1:18100
G=http://127.0.0.1:18101
ADMIN=testadmin
WORK=/tmp/porter-e2e-http
rm -rf "$WORK"
mkdir -p "$WORK/work" "$WORK/ctx"
cat > "$WORK/ctx/Dockerfile" <<'EOF'
FROM nginx:alpine
RUN echo porter-nginx-ok > /usr/share/nginx/html/index.html
EOF
pkill -f '/tmp/porterbin/porter server' 2>/dev/null || true
pkill -f '/tmp/porterbin/porter-agent run' 2>/dev/null || true
pkill -f 'firecracker --api-sock /tmp/porter-e2e-http' 2>/dev/null || true
sleep 1
cd "$ROOT/porter" && go build -buildvcs=false -o /tmp/porterbin/porter ./cmd/porter
cd "$ROOT/porter-agent" && go build -buildvcs=false -o /tmp/porterbin/porter-agent ./cmd/porter-agent
cd "$ROOT/porter-agent" && go build -buildvcs=false -o /tmp/porterbin/porter-guest-init ./cmd/porter-guest-init
nohup env PORTER_ADMIN_TOKEN=$ADMIN PORTER_DB=$WORK/porter.db PORTER_ADDR=127.0.0.1:18100 PORTER_GATEWAY_ADDR=127.0.0.1:18101 PORTER_BASE_DOMAIN=porter.local PORTER_GUEST_INIT=/tmp/porterbin/porter-guest-init PORTER_KERNEL_IMAGE=/mnt/d/github/porter/legacy/porter-images/bases/alpine/3.21/out/vmlinux PORTER_IMAGE_WORKDIR=$WORK/work /tmp/porterbin/porter server >"$WORK/porter.log" 2>&1 &
echo $! > "$WORK/porter.pid"
for i in $(seq 1 30); do curl -sf "$B/healthz" >/dev/null && break; sleep 1; done
curl -sf "$B/healthz" >/dev/null || { echo "Porter not healthy"; cat "$WORK/porter.log"; exit 1; }
echo "== Porter healthy =="
cat > "$WORK/build.json" <<EOF
{"name":"http-app","source_type":"dockerfile","context_dir":"$WORK/ctx","dockerfile":"$WORK/ctx/Dockerfile","size_mb":512}
EOF
BUILD=$(curl -sS -m 30 -X POST "$B/v1/images/build" -H "Authorization: Bearer $ADMIN" -H 'Content-Type: application/json' --data-binary @"$WORK/build.json")
echo "$BUILD" > "$WORK/build-response.json"
IMGID=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["image"]["id"])' "$WORK/build-response.json")
echo "== Build queued: $IMGID =="
for i in $(seq 1 120); do
  IMG=$(curl -sS "$B/v1/images/$IMGID" -H "Authorization: Bearer $ADMIN")
  ST=$(python3 -c 'import json,sys; print(json.load(sys.stdin).get("status",""))' <<<"$IMG")
  echo "build status: $ST"
  [ "$ST" = "ready" ] && break
  [ "$ST" = "failed" ] && { echo "$IMG"; exit 1; }
  sleep 2
done
echo "$IMG" > "$WORK/image.json"
ROOTFS=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["rootfs_ref"])' "$WORK/image.json")
echo "ROOTFS=$ROOTFS"
JOIN=$(curl -sS -X POST "$B/v1/fleet/join-tokens" -H "Authorization: Bearer $ADMIN" -H 'Content-Type: application/json' -d '{"ttl_seconds":900}')
JOIN_TOKEN=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])' <<<"$JOIN")
/tmp/porterbin/porter-agent join --server "$B" --token "$JOIN_TOKEN" --name e2e-node --data-dir "$WORK/agent" --kernel /mnt/d/github/porter/legacy/porter-images/bases/alpine/3.21/out/vmlinux --rootfs "$ROOTFS"
nohup /tmp/porterbin/porter-agent run --data-dir "$WORK/agent" --network on --init-path /sbin/porter-guest-init >"$WORK/agent.log" 2>&1 &
echo $! > "$WORK/agent.pid"
sleep 4
PROJECT=$(curl -sS -X POST "$B/v1/projects" -H "Authorization: Bearer $ADMIN" -H 'Content-Type: application/json' -d '{"name":"e2e"}')
PID=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])' <<<"$PROJECT")
APP=$(curl -sS -X POST "$B/v1/projects/$PID/apps" -H "Authorization: Bearer $ADMIN" -H 'Content-Type: application/json' -d '{"name":"http-app","image":"built://http-app","port":80,"vcpus":1,"memory_mb":256,"replicas":1,"persistence_mode":"ephemeral"}')
AID=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])' <<<"$APP")
DEP=$(curl -sS -X POST "$B/v1/apps/$AID/deployments" -H "Authorization: Bearer $ADMIN" -H 'Content-Type: application/json' -d '{"image":"built://http-app","replicas":1}')
echo "== Deployment =="; echo "$DEP"
echo "== waiting for health =="
for i in $(seq 1 60); do
  VMS=$(curl -sS "$B/v1/nodes" -H "Authorization: Bearer $ADMIN" || true)
  NODE=$(python3 -c 'import json,sys; d=json.load(sys.stdin); print(d[0]["id"] if d else "")' <<<"$VMS" 2>/dev/null || true)
  [ -n "$NODE" ] && VMLIST=$(curl -sS "$B/v1/fleet/nodes/$NODE/vms" -H "Authorization: Bearer $ADMIN" || true) || VMLIST='[]'
  echo "$VMLIST"
  if echo "$VMLIST" | grep -q '"health":"healthy"'; then echo "HEALTHY"; break; fi
  sleep 2
done
echo "== gateway response =="
curl -sS -m 10 -H 'Host: http-app.porter.local' "$G/" || true
echo
echo "== logs =="
curl -sS "$B/v1/logs?limit=20" -H "Authorization: Bearer $ADMIN" || true
echo
echo "== agent log =="; cat "$WORK/agent.log"
echo "== porter log =="; cat "$WORK/porter.log"