#!/usr/bin/env bash
set -euo pipefail
ROOT=/mnt/d/github/porter
WORK=/tmp/porter-e2e-github
B=http://127.0.0.1:18104
ADMIN=testadmin
rm -rf "$WORK"; mkdir -p "$WORK/repo"
cat > "$WORK/repo/Dockerfile" <<'EOF'
FROM nginx:alpine
RUN echo github-webhook-ok > /usr/share/nginx/html/index.html
EOF
cd "$WORK/repo"
git init -q
git config user.email e2e@porter.local
git config user.name e2e
git add Dockerfile
git commit -qm init
git branch -m main
pkill -f '/tmp/porterbin/porter server' 2>/dev/null || true
sleep 1
cd "$ROOT/porter" && go build -buildvcs=false -o /tmp/porterbin/porter ./cmd/porter
nohup env PORTER_ADMIN_TOKEN=$ADMIN PORTER_DB=$WORK/porter.db PORTER_ADDR=127.0.0.1:18104 PORTER_GATEWAY_ADDR=off PORTER_BASE_DOMAIN=porter.local PORTER_GUEST_INIT=/tmp/porterbin/porter-guest-init PORTER_KERNEL_IMAGE=/mnt/d/github/porter/legacy/porter-images/bases/alpine/3.21/out/vmlinux PORTER_IMAGE_WORKDIR=$WORK/work /tmp/porterbin/porter server >"$WORK/porter.log" 2>&1 &
for i in $(seq 1 30); do curl -sf "$B/healthz" >/dev/null && break; sleep 1; done
A=(-H "Authorization: Bearer $ADMIN" -H 'Content-Type: application/json')
PROJ=$(curl -sS -X POST "$B/v1/projects" "${A[@]}" -d '{"name":"git-e2e"}')
PID=$(printf '%s' "$PROJ" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
REPO="$WORK/repo"
APP=$(curl -sS -X POST "$B/v1/projects/$PID/apps" "${A[@]}" -d "{\"name\":\"git-app\",\"image\":\"built://pending\",\"repo\":\"$REPO\",\"branch\":\"main\",\"port\":80,\"vcpus\":1,\"memory_mb\":256,\"replicas\":1,\"persistence_mode\":\"ephemeral\"}")
AID=$(printf '%s' "$APP" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
echo "APP=$AID"
curl -sS -X POST "$B/hooks/github" -H 'Content-Type: application/json' -H 'X-GitHub-Event: push' -d "{\"ref\":\"refs/heads/main\",\"repository\":{\"clone_url\":\"$REPO\",\"html_url\":\"$REPO\"}}" >/dev/null
for i in $(seq 1 120); do
  DEPS=$(curl -sS "$B/v1/apps/$AID/deployments" "${A[@]}")
  if echo "$DEPS" | grep -q 'dep_'; then echo "GITHUB_DEPLOY_OK"; echo "$DEPS"; break; fi
  sleep 2
done
IMG=$(curl -sS "$B/v1/images" "${A[@]}")
echo "$IMG" | grep -q '"source_type":"git"' && echo "GITHUB_IMAGE_OK"
echo "== build logs =="
IMID=$(echo "$IMG" | python3 -c 'import json,sys; print(json.load(sys.stdin)[0]["id"])')
curl -sS "$B/v1/images/$IMID/logs" "${A[@]}" | head -c 1200
echo