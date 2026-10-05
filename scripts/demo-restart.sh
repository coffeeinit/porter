#!/usr/bin/env bash
# Build and run the Porter demo server in the foreground.
# Supply database/admin/encryption settings through the environment.
set -euo pipefail

: "${PORTER_DATABASE_URL:?Set PORTER_DATABASE_URL in the environment}"
: "${PORTER_BOOTSTRAP_ADMIN_PASSWORD:?Set PORTER_BOOTSTRAP_ADMIN_PASSWORD in the environment}"
: "${PORTER_SECRET_KEY:?Set PORTER_SECRET_KEY in the environment}"

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PORTER_BIN="${PORTER_DEMO_BIN:-/tmp/porter-demo.bin}"
mkdir -p "$(dirname "$PORTER_BIN")"
cd "$ROOT_DIR"
go build -o "$PORTER_BIN" ./cmd/porter
exec "$PORTER_BIN" server
