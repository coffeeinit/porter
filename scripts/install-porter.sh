#!/usr/bin/env bash
set -euo pipefail
BIN=${1:-./bin/porter}
if [[ ! -x "$BIN" ]]; then echo "missing $BIN; run make build" >&2; exit 1; fi
sudo install -d -m 0755 /etc/porter /var/lib/porter
sudo install -m 0755 "$BIN" /usr/local/bin/porter
if [[ ! -f /etc/porter/porter.env ]]; then sudo cp deploy/porter.env.example /etc/porter/porter.env; fi
sudo install -m 0644 deploy/systemd/porter.service /etc/systemd/system/porter.service
sudo systemctl daemon-reload
sudo systemctl enable --now porter
echo "Porter started. Check: systemctl status porter"
