#!/usr/bin/env bash
set -euo pipefail
BIN=${1:-./bin/porter-agent}
if [[ ! -x "$BIN" ]]; then echo "missing $BIN; run make build" >&2; exit 1; fi
sudo install -d -m 0755 /var/lib/porter-agent
sudo install -m 0755 "$BIN" /usr/local/bin/porter-agent
sudo install -m 0644 deploy/systemd/porter-agent.service /etc/systemd/system/porter-agent.service
sudo systemctl daemon-reload
sudo systemctl enable porter-agent
echo "Agent installed. Join with:"
echo "  sudo porter-agent join --server https://<porter-host> --token <join-token> --data-dir /var/lib/porter-agent --kernel /var/lib/porter-agent/vmlinux --rootfs /var/lib/porter-agent/rootfs.ext4"
echo "Then: sudo systemctl start porter-agent"
