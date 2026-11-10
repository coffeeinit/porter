#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd -- "$SCRIPT_DIR/.." && pwd)
BIN=${1:-"$REPO_ROOT/bin/porter-agent"}
if [[ ! -x "$BIN" ]]; then
  echo "missing executable Porter Agent binary: $BIN" >&2
  echo "build it first with: make build" >&2
  exit 1
fi
sudo install -d -o root -g root -m 0755 /var/lib/porter-agent
sudo install -o root -g root -m 0755 "$BIN" /usr/local/bin/porter-agent
sudo install -o root -g root -m 0644 "$REPO_ROOT/deploy/systemd/porter-agent.service" /etc/systemd/system/porter-agent.service
sudo systemctl daemon-reload
echo "Agent binary and unit installed; the service was not enabled or started."
echo "Enrollment is unavailable in this checkout; the token-safe join flow is not implemented here."
echo "Do not run enrollment or supply a token in an argument or file. Wait for the TTY/--token-stdin CLI and route integration."
