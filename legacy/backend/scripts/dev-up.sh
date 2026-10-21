#!/usr/bin/env bash
#
# Porter local development — API and dashboard together, one command.
#
#   bash backend/scripts/dev-up.sh
#
# Starts:
#   - the control plane (default :8080, the /api/v1 API)
#   - the Vite dev server for the Vue dashboard (default :5173) with HMR,
#     proxying /api to the control plane
#
# So you edit a .vue file and the browser updates; you edit a .go file and the
# API is restarted for you. Ctrl-C stops both.
#
# This does NOT need root: it starts the API and the dashboard, not microVMs.
# Booting a guest needs root (Firecracker + TAP) — see v0.0.1-smoke.sh.
#
# Overrides:
#   PORTER_API_PORT=8080        control-plane port
#   PORTER_WEB_PORT=5173        dashboard dev-server port
#   PORTER_DATABASE_URL=...     default comes from backend/porter.toml
#   PORTER_API_TARGET=...       derived from PORTER_API_PORT automatically
#
set -uo pipefail

BACKEND_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$BACKEND_DIR" || exit 1

API_PORT="${PORTER_API_PORT:-8080}"
WEB_PORT="${PORTER_WEB_PORT:-5173}"
export PORTER_API_TARGET="${PORTER_API_TARGET:-http://127.0.0.1:${API_PORT}}"
export PORTER_WEB_PORT="$WEB_PORT"

BIN="${PORTER_DEV_BIN:-/tmp/porter-dev-bin/porter}"
LOG_DIR="${PORTER_DEV_LOGDIR:-/tmp/porter-dev-logs}"

c_info() { printf '\033[36m%s\033[0m\n' "$*"; }
c_ok()   { printf '\033[32m%s\033[0m\n' "$*"; }
c_warn() { printf '\033[33m%s\033[0m\n' "$*"; }
c_bad()  { printf '\033[31m%s\033[0m\n' "$*"; }

API_PID=""
WEB_PID=""

# Each service is started with setsid, so it leads its own process group. That
# matters for the dashboard: `npm run dev` spawns vite as a child, and killing
# npm alone leaves vite bound to the port. Killing the group takes both.
kill_group() {
  [ -n "$1" ] || return 0
  kill -- "-$1" 2>/dev/null || kill "$1" 2>/dev/null
  return 0
}

cleanup() {
  echo
  c_info "shutting down…"
  kill_group "$WEB_PID"
  kill_group "$API_PID"
  wait 2>/dev/null
  exit 0
}
trap cleanup INT TERM

mkdir -p "$LOG_DIR"
# Not /tmp/porter-dev directly: that path is the configured Firecracker socket
# directory and already exists as a directory, so a binary cannot be written
# there (this bit the first run of this script).
mkdir -p "$(dirname "$BIN")"

# ---------------------------------------------------------------------------
# 1. Database
# ---------------------------------------------------------------------------
c_info "== database =="
DB_URL="${PORTER_DATABASE_URL:-$(grep -E '^\s*url\s*=' porter.toml 2>/dev/null | head -1 | sed -E 's/.*"(.*)".*/\1/')}"
if [ -z "$DB_URL" ]; then
  DB_URL="postgres://porter:porter@localhost:5433/porter?sslmode=disable"
fi
export PORTER_DATABASE_URL="$DB_URL"

PG_HOSTPORT="$(printf '%s' "$DB_URL" | sed -E 's|.*@([^/]+)/.*|\1|')"
PG_HOST="${PG_HOSTPORT%%:*}"
PG_PORT="${PG_HOSTPORT##*:}"
if timeout 3 bash -c "cat < /dev/null > /dev/tcp/${PG_HOST}/${PG_PORT}" 2>/dev/null; then
  c_ok "  PostgreSQL reachable at ${PG_HOST}:${PG_PORT}"
else
  c_bad "  PostgreSQL NOT reachable at ${PG_HOST}:${PG_PORT}"
  c_warn "  Start it, e.g.:  docker start porter-dev-pg"
  c_warn "  (or: docker run -d --name porter-dev-pg -p 5433:5432 -e POSTGRES_USER=porter -e POSTGRES_PASSWORD=porter -e POSTGRES_DB=porter postgres:16-alpine)"
  exit 1
fi

# ---------------------------------------------------------------------------
# 2. Build + migrate
# ---------------------------------------------------------------------------
c_info "== build =="
go build -o "$BIN" ./cmd/porter || { c_bad "go build failed"; exit 1; }
c_ok "  $("$BIN" version)"

c_info "== migrate =="
"$BIN" migrate >"$LOG_DIR/migrate.log" 2>&1 \
  && c_ok "  schema up to date" \
  || { c_bad "  migrate failed:"; sed -n '1,20p' "$LOG_DIR/migrate.log"; exit 1; }

# ---------------------------------------------------------------------------
# 3. Dashboard deps
# ---------------------------------------------------------------------------
c_info "== dashboard =="
if [ ! -d web/node_modules ]; then
  c_warn "  installing web dependencies (first run)…"
  ( cd web && npm install --no-audit --no-fund ) || { c_bad "  npm install failed"; exit 1; }
fi

# ---------------------------------------------------------------------------
# 4. Control plane
# ---------------------------------------------------------------------------
c_info "== control plane =="
if timeout 2 bash -c "cat < /dev/null > /dev/tcp/127.0.0.1/${API_PORT}" 2>/dev/null; then
  c_bad "  port ${API_PORT} is already in use"
  c_warn "  Pick another, e.g.:  PORTER_API_PORT=18080 bash $0"
  c_warn "  (the dashboard proxy target follows PORTER_API_PORT automatically)"
  exit 1
fi

setsid env PORTER_LISTEN_ADDR=":${API_PORT}" "$BIN" server >"$LOG_DIR/api.log" 2>&1 &
API_PID=$!
note_pid() { printf '          %s\n' "$*"; }
note_pid "api pid $API_PID, log $LOG_DIR/api.log"

for _ in $(seq 1 40); do
  BODY="$(curl -fsS -m 2 "http://127.0.0.1:${API_PORT}/api/v1/health" 2>/dev/null)"
  case "$BODY" in *'"status"'*) break ;; esac
  if ! kill -0 "$API_PID" 2>/dev/null; then
    c_bad "  the API exited during startup:"; sed -n '1,30p' "$LOG_DIR/api.log"; exit 1
  fi
  sleep 0.5
done
c_ok "  API healthy: http://127.0.0.1:${API_PORT}/api/v1/health"
note_pid "admin user is 'admin'"
note_pid "PORTER_BOOTSTRAP_ADMIN_PASSWORD applies only when the admin is first"
note_pid "seeded — on an already-initialised database use your existing password."

# ---------------------------------------------------------------------------
# 5. Dashboard dev server
# ---------------------------------------------------------------------------
c_info "== dashboard dev server =="
setsid bash -c "cd '$BACKEND_DIR/web' && exec npm run dev" >"$LOG_DIR/web.log" 2>&1 &
WEB_PID=$!
note_pid "web pid $WEB_PID, log $LOG_DIR/web.log"

for _ in $(seq 1 40); do
  if curl -fsS -m 2 "http://127.0.0.1:${WEB_PORT}/" >/dev/null 2>&1; then break; fi
  if ! kill -0 "$WEB_PID" 2>/dev/null; then
    c_bad "  the dashboard dev server exited:"; sed -n '1,30p' "$LOG_DIR/web.log"; exit 1
  fi
  sleep 0.5
done

echo
c_ok "ready"
echo "  dashboard : http://127.0.0.1:${WEB_PORT}/     (HMR; /api proxied to the API)"
echo "  API       : http://127.0.0.1:${API_PORT}/api/v1"
echo "  logs      : ${LOG_DIR}/api.log , ${LOG_DIR}/web.log"
echo
echo "  Log in with the seeded admin (user 'admin'). Set the password before"
echo "  first boot with PORTER_BOOTSTRAP_ADMIN_PASSWORD if you have not already."
echo
c_info "Ctrl-C to stop both."

wait
