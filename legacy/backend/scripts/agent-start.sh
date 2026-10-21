#!/usr/bin/env bash
# Start the porter agent (node identity: enroll via API, tokened heartbeat).
set -e
ET=$(cat /home/pappu/porter-agent-run/enroll.txt)
export PORTER_ENROLL_TOKEN="$ET"
export PORTER_CONTROL_URL='http://localhost:8080'
export PORTER_AGENT_CONTROL_TOKEN='agent-ctrl-demo-token-2026'
export PORTER_AGENT_PROXY_TOKEN='agent-proxy-demo-token-2026'
export PORTER_AGENT_ID='wsl-node-live'
pkill -f 'porter.agent' 2>/dev/null || true
sleep 1
cd /mnt/d/github/porter/backend
setsid nohup /home/pappu/porter-agent agent > /home/pappu/porter-agent-run/agent.log 2>&1 < /dev/null &
sleep 15
tail -4 /home/pappu/porter-agent-run/agent.log
