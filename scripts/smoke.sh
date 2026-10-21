#!/usr/bin/env bash
set -euo pipefail
BASE=http://127.0.0.1:18080
ADMIN=testadmin
RESP=$(curl -s -X POST "$BASE/v1/fleet/join-tokens" -H "Authorization: Bearer $ADMIN" -H "Content-Type: application/json" -d '{"ttl_seconds":600}')
echo "$RESP"
TOKEN=$(printf '%s' "$RESP" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
echo "TOKEN=${TOKEN:0:16}..."
rm -rf /tmp/porter-test/agent
mkdir -p /tmp/porter-test/agent
/tmp/porterbin/porter-agent join --server "$BASE" --token "$TOKEN" --name wsl-node-1 \
  --data-dir /tmp/porter-test/agent \
  --kernel /mnt/d/github/porter/legacy/porter-images/bases/alpine/3.21/out/vmlinux \
  --rootfs /mnt/d/github/porter/legacy/porter-images/bases/alpine/3.21/out/rootfs.ext4
nohup /tmp/porterbin/porter-agent run --data-dir /tmp/porter-test/agent >/tmp/porter-agent.log 2>&1 &
echo $! > /tmp/porter-agent.pid
sleep 4
cat /tmp/porter-agent.log
echo NODES
curl -s "$BASE/v1/nodes" -H "Authorization: Bearer $ADMIN"
echo