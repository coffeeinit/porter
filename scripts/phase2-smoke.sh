#!/usr/bin/env bash
set -euo pipefail
B=http://127.0.0.1:18083
A=testadmin
rm -rf /tmp/porter-p2
mkdir -p /tmp/porter-p2/agent
pkill -f '/tmp/porterbin/porter server' 2>/dev/null || true
pkill -f '/tmp/porterbin/porter-agent run' 2>/dev/null || true
pkill -f 'firecracker --api-sock /tmp/porter-p2' 2>/dev/null || true
sleep 1
nohup env PORTER_ADMIN_TOKEN=$A PORTER_DB=/tmp/porter-p2/porter.db PORTER_ADDR=127.0.0.1:18083 /tmp/porterbin/porter server >/tmp/porter-p2/porter.log 2>&1 &
sleep 2
RESP=$(curl -s -X POST "$B/v1/fleet/join-tokens" -H "Authorization: Bearer $A" -H "Content-Type: application/json" -d '{"ttl_seconds":600}')
TOKEN=$(printf '%s' "$RESP" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
/tmp/porterbin/porter-agent join --server "$B" --token "$TOKEN" --name p2-node \
 --data-dir /tmp/porter-p2/agent \
 --kernel /mnt/d/github/porter/legacy/porter-images/bases/alpine/3.21/out/vmlinux \
 --rootfs /mnt/d/github/porter/legacy/porter-images/bases/alpine/3.21/out/rootfs.ext4
nohup /tmp/porterbin/porter-agent run --data-dir /tmp/porter-p2/agent --network on >/tmp/porter-p2/agent.log 2>&1 &
sleep 4
P=$(curl -s -X POST "$B/v1/projects" -H "Authorization: Bearer $A" -H "Content-Type: application/json" -d '{"name":"p2"}')
PID=$(printf '%s' "$P" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
VOL=$(curl -s -X POST "$B/v1/projects/$PID/volumes" -H "Authorization: Bearer $A" -H "Content-Type: application/json" -d '{"name":"data","size_mb":64}')
VID=$(printf '%s' "$VOL" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
AP=$(curl -s -X POST "$B/v1/projects/$PID/apps" -H "Authorization: Bearer $A" -H "Content-Type: application/json" -d '{"name":"web","image":"base://alpine","replicas":1,"port":8080,"vcpus":1,"memory_mb":256}')
AID=$(printf '%s' "$AP" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
curl -s -X POST "$B/v1/volumes/$VID/attach" -H "Authorization: Bearer $A" -H "Content-Type: application/json" -d "{\"app_id\":\"$AID\"}" >/dev/null
curl -s -X POST "$B/v1/apps/$AID/deployments" -H "Authorization: Bearer $A" -H "Content-Type: application/json" -d '{"image":"base://alpine","replicas":1}' >/dev/null
sleep 8
VMID=$(python3 -c "import sqlite3;c=sqlite3.connect('/tmp/porter-p2/porter.db');print(c.execute('select vm_id from replicas limit 1').fetchone()[0])")
echo VMID=$VMID
echo '--- volumes ---'; curl -s "$B/v1/projects/$PID/volumes" -H "Authorization: Bearer $A"; echo
echo '--- volume file ---'; ls -lh /tmp/porter-p2/agent/vms/volumes/ 2>/dev/null || true
echo '--- operations after deploy ---'; curl -s "$B/v1/operations" -H "Authorization: Bearer $A"; echo
SNAP=$(curl -s -X POST "$B/v1/snapshots" -H "Authorization: Bearer $A" -H "Content-Type: application/json" -d "{\"vm_id\":\"$VMID\"}"); echo SNAP=$SNAP
sleep 4
echo '--- snapshots ---'; curl -s "$B/v1/snapshots" -H "Authorization: Bearer $A"; echo
echo '--- logs ---'; curl -s "$B/v1/logs?limit=5" -H "Authorization: Bearer $A" | head -c 1200; echo
echo '--- metrics ---'; curl -s "$B/v1/metrics?limit=5" -H "Authorization: Bearer $A"; echo
echo '--- mcp tools ---'; curl -s -X POST "$B/mcp" -H "Authorization: Bearer $A" -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | grep -o 'porter_[a-z_]*' | sort -u