#!/usr/bin/env bash
# Start a Porter host agent using credentials supplied by the operator.
set -euo pipefail

: "${PORTER_ENROLL_TOKEN:?Set PORTER_ENROLL_TOKEN in the environment}"
: "${PORTER_AGENT_CONTROL_TOKEN:?Set PORTER_AGENT_CONTROL_TOKEN in the environment}"
: "${PORTER_AGENT_PROXY_TOKEN:?Set PORTER_AGENT_PROXY_TOKEN in the environment}"
export PORTER_CONTROL_URL="${PORTER_CONTROL_URL:-http://127.0.0.1:8080}"

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PORTER_BIN="${PORTER_BIN:-${ROOT_DIR}/bin/porter}"
cd "$ROOT_DIR"

if [[ -x "$PORTER_BIN" ]]; then
  exec "$PORTER_BIN" agent
fi
exec go run ./cmd/porter agent
