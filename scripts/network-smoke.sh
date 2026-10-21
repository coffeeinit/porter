#!/usr/bin/env bash
set -euo pipefail
B=http://127.0.0.1:18082
A=testadmin
rm -rf /tmp/porter-net
mkdir -p /tmp/porter-net/agent
pkill -f '/tmp/porterbin/porter server' 2>/dev/null || true
pkill -f '/tmp/porterbin/porter-agent run' 2>/dev/null || true
pkill -f 'firecracker --api-sock /tmp/porter-net' 2>/dev/null || true
sleep 1
nohup env PORTER_ADMIN_TOKEN=$A PORTER_DB=/tmp/porter-net/porter.db PORTER_ADDR=127.0.0.1:18082 /tmp/porterbin/porter server >/tmp/porter-net/porter.log 2>&1 &
sleep 2
RESP=$(curl -s -X POST "$B/v1/fleet/join-tokens" -H "Authorization: Bearer $A" -H "Content-Type: application/json" -d '{"ttl_seconds":600}')
TOKEN=$(printf '%s' "$RESP" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
/tmp/porterbin/porter-agent join --server "$B" --token "$TOKEN" --name net-node-1 \
 --data-dir /tmp/porter-net/agent \
 --kernel /mnt/d/github/porter/legacy/porter-images/bases/alpine/3.21/out/vmlinux \
 --rootfs /mnt/d/github/porter/legacy/porter-images/bases/alpine/3.21/out/rootfs.ext4
nohup /tmp/porterbin/porter-agent run --data-dir /tmp/porter-net/agent --network on >/tmp/porter-net/agent.log 2>&1 &
sleep 4
P=$(curl -s -X POST "$B/v1/projects" -H "Authorization: Bearer $A" -H "Content-Type: application/json" -d '{"name":"netdemo"}')
PID=$(printf '%s' "$P" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
AP=$(curl -s -X POST "$B/v1/projects/$PID/apps" -H "Authorization: Bearer $A" -H "Content-Type: application/json" -d '{"name":"web","image":"base://alpine","replicas":1,"port":8080,"vcpus":1,"memory_mb":256}')
AID=$(printf '%s' "$AP" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
curl -s -X POST "$B/v1/apps/$AID/deployments" -H "Authorization: Bearer $A" -H "Content-Type: application/json" -d '{"image":"base://alpine","replicas":1}' >/dev/null
sleep 8
echo '--- bridge ---'
ip -br addr show porter0 || true
echo '--- tap ---'
ip -br link show | grep '^pt' || true
echo '--- firecracker ---'
pgrep -a firecracker || true
echo '--- operations ---'
curl -s "$B/v1/operations" -H "Authorization: Bearer $A"
echo
echo '--- agent log ---'
cat /tmp/porter-net/agent.log