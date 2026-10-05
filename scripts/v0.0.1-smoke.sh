#!/usr/bin/env bash
#
# Porter v0.0.1-alpha smoke test — "it boots".
#
# Proves the direct-Firecracker path works end to end on one box:
#   migrate -> register a golden image -> start server -> login ->
#   create project -> boot one microVM -> verify the guest is running.
#
# Requires root: Firecracker needs /dev/kvm and netmgr creates a TAP device.
# In WSL, run it from Windows as root (no sudo password needed):
#
#   wsl -u root -e bash /mnt/d/github/porter/backend/scripts/v0.0.1-smoke.sh
#
# Every external dependency is overridable; see the defaults below. The test is
# self-contained: it uses its own database, its own port pair, its own socket
# and image directories, and leaves the operator's running install untouched.
#
set -uo pipefail

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------
BACKEND_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_DIR="$(cd "$BACKEND_DIR/.." && pwd)"

SMOKE_DB="${PORTER_SMOKE_DB:-porter_smoke}"
PG_ADMIN_URL="${PORTER_SMOKE_PG_ADMIN_URL:-postgres://porter:porter@localhost:5433/postgres?sslmode=disable}"
APP_DB_URL="${PORTER_SMOKE_APP_DB_URL:-postgres://porter:porter@localhost:5433/$SMOKE_DB?sslmode=disable}"

API_PORT="${PORTER_SMOKE_API_PORT:-18080}"
GW_PORT="${PORTER_SMOKE_GW_PORT:-18081}"
# The control API is mounted under /api/v1/ (server.go wraps its mux with a
# StripPrefix). Bare paths are served by the dashboard's SPA fallback, which
# answers 200 with HTML — so the prefix is required, and the health check below
# insists on a JSON body so an HTML fallback can never look healthy.
API="http://127.0.0.1:$API_PORT/api/v1"

ADMIN_USER="${PORTER_SMOKE_ADMIN_USER:-admin}"
ADMIN_PASS="${PORTER_SMOKE_ADMIN_PASSWORD:-porter-smoke-admin}"
SECRET_KEY="${PORTER_SMOKE_SECRET_KEY:-smoke-secret-key-0123456789abcdef0123456789abcdef}"

IMAGE_NAME="${PORTER_SMOKE_IMAGE:-smoke-alpine}"
ROOTFS="${PORTER_SMOKE_ROOTFS:-$REPO_DIR/data/alpine-3.21.ext4}"
KERNEL="${PORTER_SMOKE_KERNEL:-$REPO_DIR/data/vmlinux}"

WORKDIR="${PORTER_SMOKE_WORKDIR:-/tmp/porter-smoke}"
BIN="${PORTER_SMOKE_BIN:-$WORKDIR/porter}"
BOOT_TIMEOUT="${PORTER_SMOKE_TIMEOUT:-120}"
KEEP="${PORTER_SMOKE_KEEP:-0}"

PROJECT_NAME="${PORTER_SMOKE_PROJECT:-smoke-demo}"

SERVER_PID=""
STEP=0
PASSED=0

# ---------------------------------------------------------------------------
# Output helpers
# ---------------------------------------------------------------------------
c_ok()   { printf '\033[32m%s\033[0m\n' "$*"; }
c_bad()  { printf '\033[31m%s\033[0m\n' "$*"; }
c_info() { printf '\033[36m%s\033[0m\n' "$*"; }

step() {
  STEP=$((STEP + 1))
  printf '\n\033[1m[%d] %s\033[0m\n' "$STEP" "$*"
}

ok()   { PASSED=$((PASSED + 1)); c_ok   "    PASS  $*"; }
fail() { c_bad "    FAIL  $*"; cleanup; exit 1; }
note() { printf '          %s\n' "$*"; }

jget() {
  # jget <json> <python-expression over d>
  python3 -c 'import json,sys
try:
    d=json.loads(sys.argv[1])
except Exception:
    print(""); sys.exit(0)
print(eval(sys.argv[2]))' "$1" "$2" 2>/dev/null
}

# replica_field <json> <field> reads one field from the first replica, accepting
# either a bare JSON array (what /replicas returns) or an object wrapping a
# "replicas"/"items" list.
replica_field() {
  python3 -c 'import json,sys
try:
    d = json.loads(sys.argv[1])
except Exception:
    print(""); raise SystemExit
if isinstance(d, list):
    r = d[0] if d else {}
elif isinstance(d, dict):
    lst = d.get("replicas") or d.get("items") or []
    r = (lst[0] if lst else (d.get("replica") or {}))
else:
    r = {}
print((r or {}).get(sys.argv[2], "") or "")' "$1" "$2" 2>/dev/null
}

cleanup() {
  if [ "$KEEP" = "1" ]; then
    c_info "PORTER_SMOKE_KEEP=1 — leaving the server running (pid ${SERVER_PID:-none}, API $API)"
    return
  fi
  if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill "$SERVER_PID" 2>/dev/null
    wait "$SERVER_PID" 2>/dev/null
  fi
  # Only touch firecracker processes this smoke run created: their API socket
  # lives under our own work directory.
  pkill -f "firecracker.*${WORKDIR}" 2>/dev/null
  return
}
trap cleanup EXIT

# ---------------------------------------------------------------------------
# Preflight
# ---------------------------------------------------------------------------
step "Preflight: host can run Firecracker microVMs"

[ "$(id -u)" = "0" ] || fail "must run as root (Firecracker + TAP creation). In WSL: wsl -u root -e bash $0"
ok "running as root"

[ -c /dev/kvm ] || fail "/dev/kvm is missing — no KVM, no microVMs"
[ -r /dev/kvm ] && [ -w /dev/kvm ] || fail "/dev/kvm is not readable+writable"
ok "/dev/kvm present and accessible"

command -v firecracker >/dev/null || fail "firecracker binary not found on PATH"
note "firecracker: $(firecracker --version 2>/dev/null | head -1)"
ok "firecracker installed"

command -v ip >/dev/null || fail "the 'ip' command is required to create TAP devices"
ok "networking tooling present (ip)"

[ -f "$ROOTFS" ] || fail "rootfs not found: $ROOTFS (set PORTER_SMOKE_ROOTFS)"
[ -f "$KERNEL" ] || fail "kernel not found: $KERNEL (set PORTER_SMOKE_KERNEL)"
note "rootfs: $ROOTFS ($(stat -c%s "$ROOTFS") bytes)"
note "kernel: $KERNEL ($(stat -c%s "$KERNEL") bytes)"
ok "boot artifacts present"

command -v psql >/dev/null || fail "psql is required to provision the smoke database"
command -v python3 >/dev/null || fail "python3 is required for JSON parsing"

mkdir -p "$WORKDIR" || fail "cannot create work dir $WORKDIR"

# ---------------------------------------------------------------------------
# Build (unless a binary was supplied)
# ---------------------------------------------------------------------------
step "Build the control plane binary"

if [ -x "$BIN" ] && [ "${PORTER_SMOKE_REBUILD:-0}" != "1" ]; then
  ok "using supplied binary $BIN"
else
  ( cd "$BACKEND_DIR" && go build -o "$BIN" ./cmd/porter ) || fail "go build ./cmd/porter failed"
  [ -x "$BIN" ] || fail "build produced no binary at $BIN"
  ok "built $BIN"
fi
note "$("$BIN" version 2>/dev/null || echo 'version unknown')"

# ---------------------------------------------------------------------------
# Database
# ---------------------------------------------------------------------------
step "Provision a dedicated PostgreSQL database"

psql "$PG_ADMIN_URL" -v ON_ERROR_STOP=1 -q \
  -c "DROP DATABASE IF EXISTS $SMOKE_DB" \
  -c "CREATE DATABASE $SMOKE_DB" >/dev/null 2>&1 \
  || fail "cannot create database $SMOKE_DB via $PG_ADMIN_URL"
ok "database $SMOKE_DB created"

PORTER_CONFIG="$BACKEND_DIR/porter.toml" PORTER_DATABASE_URL="$APP_DB_URL" \
  "$BIN" migrate >"$WORKDIR/migrate.log" 2>&1 \
  || { sed -n '1,20p' "$WORKDIR/migrate.log"; fail "porter migrate failed"; }
ok "migrations applied"

# ---------------------------------------------------------------------------
# Golden image
# ---------------------------------------------------------------------------
step "Register a golden image in the catalog"

rm -rf "$WORKDIR/images"
PORTER_CONFIG="$BACKEND_DIR/porter.toml" \
PORTER_IMAGES_DIR="$WORKDIR/images" \
  "$BIN" image add "$IMAGE_NAME" "$ROOTFS" "$KERNEL" >"$WORKDIR/image.log" 2>&1 \
  || { cat "$WORKDIR/image.log"; fail "porter image add failed"; }
grep -q '"status": "ready"' "$WORKDIR/images/$IMAGE_NAME.json" \
  || { cat "$WORKDIR/image.log"; fail "image is not 'ready'"; }
ok "image '$IMAGE_NAME' registered and validated"

# ---------------------------------------------------------------------------
# Server
# ---------------------------------------------------------------------------
step "Start the control plane"

SERVER_ENV=(
  "PORTER_CONFIG=$BACKEND_DIR/porter.toml"
  "PORTER_DATABASE_URL=$APP_DB_URL"
  "PORTER_LISTEN_ADDR=:$API_PORT"
  "PORTER_GATEWAY_LISTEN_ADDR=:$GW_PORT"
  "PORTER_BOOTSTRAP_ADMIN_PASSWORD=$ADMIN_PASS"
  "PORTER_SECRET_KEY=$SECRET_KEY"
  "PORTER_KERNEL_IMAGE=$KERNEL"
  "PORTER_ROOTFS_PATH=$ROOTFS"
  "PORTER_IMAGES_DIR=$WORKDIR/images"
  "PORTER_FIRECRACKER_API_SOCKET_DIR=$WORKDIR/firecracker"
  "PORTER_FIRECRACKER_SNAPSHOT_DIR=$WORKDIR/snapshots"
  "PORTER_LOGS_DIR=$WORKDIR/logs"
  "PORTER_VOLUMES_DIR=$WORKDIR/volumes"
  "PORTER_BASE_DOMAIN=smoke.local"
  "PORTER_LOG_LEVEL=info"
)

env "${SERVER_ENV[@]}" "$BIN" server >"$WORKDIR/server.log" 2>&1 &
SERVER_PID=$!
note "server pid $SERVER_PID, log $WORKDIR/server.log"

HEALTH=""
for _ in $(seq 1 60); do
  BODY="$(curl -fsS -m 2 "$API/health" 2>/dev/null)"
  # A JSON body is required: the SPA fallback returns HTML with 200.
  case "$BODY" in
    *'"status"'*) HEALTH="$BODY"; break ;;
  esac
  if ! kill -0 "$SERVER_PID" 2>/dev/null; then
    sed -n '1,40p' "$WORKDIR/server.log"
    fail "server exited during startup"
  fi
  sleep 1
done
[ -n "$HEALTH" ] || { sed -n '1,40p' "$WORKDIR/server.log"; fail "no JSON health response at $API/health — is the /api/v1 prefix right?"; }
note "health: $HEALTH"
ok "control plane healthy on $API"

# ---------------------------------------------------------------------------
# API flow: login -> csrf -> create project
# ---------------------------------------------------------------------------
step "Authenticate"

LOGIN="$(curl -fsS -m 5 -X POST "$API/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" 2>/dev/null)" \
  || { sed -n '1,40p' "$WORKDIR/server.log"; fail "login request failed"; }
TOKEN="$(jget "$LOGIN" "d.get('token','')")"
[ -n "$TOKEN" ] || fail "no token in login response: $LOGIN"
ok "logged in as $ADMIN_USER"

# /csrf is an authenticated route (auth=true in the route table), so the token
# from login is required even to read the CSRF token.
CSRF="$(jget "$(curl -fsS -m 5 "$API/csrf" -H "Authorization: Bearer $TOKEN")" "d.get('csrf_token','')")"
[ -n "$CSRF" ] || fail "no csrf_token from /csrf"
ok "obtained CSRF token"

AUTH=(-H "Authorization: Bearer $TOKEN" -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json')

step "Create a project from the golden image"

CREATE="$(curl -sS -m 30 -X POST "$API/projects" "${AUTH[@]}" \
  -d "{\"name\":\"$PROJECT_NAME\",\"image\":\"$IMAGE_NAME\",\"replicas\":1,\"vcpus\":1,\"mem_mib\":256}" 2>/dev/null)"
note "response: $(printf '%s' "$CREATE" | head -c 400)"

PROJECT_ID="$(jget "$CREATE" "d.get('id') or (d.get('project') or {}).get('id','')")"
if [ -z "$PROJECT_ID" ]; then
  PROJECT_ID="$(jget "$(curl -fsS -m 10 "$API/projects" -H "Authorization: Bearer $TOKEN")" \
    "[p['id'] for p in (d.get('projects') or d.get('items') or []) if p.get('name')=='$PROJECT_NAME'][0]")"
fi
[ -n "$PROJECT_ID" ] || fail "cannot determine project id after create"
ok "project '$PROJECT_NAME' created (id $PROJECT_ID)"

# ---------------------------------------------------------------------------
# The actual acceptance: one microVM boots
# ---------------------------------------------------------------------------
# The durable truth is the replica row, not the project status: bootReplica
# flips the replica to "running" from a goroutine once vmm.Boot returns, while
# /projects/{id}/status derives a summary that reads "running" as soon as the
# desired replica count exists. Asserting on project status alone would pass
# before the VM had done anything, so this gate polls the replica itself and
# requires BOTH state=running AND a real guest IP.
step "Wait for the microVM to boot (timeout ${BOOT_TIMEOUT}s)"

DEADLINE=$(( $(date +%s) + BOOT_TIMEOUT ))
STATE=""
GUEST_IP=""
REPLICAS=""
while [ "$(date +%s)" -lt "$DEADLINE" ]; do
  REPLICAS="$(curl -fsS -m 5 "$API/projects/$PROJECT_ID/replicas" -H "Authorization: Bearer $TOKEN" 2>/dev/null)"
  STATE="$(replica_field "$REPLICAS" state)"
  GUEST_IP="$(replica_field "$REPLICAS" ip_address)"
  case "$STATE" in
    running) [ -n "$GUEST_IP" ] && break ;;
    failed|error)
      note "replicas: $(printf '%s' "$REPLICAS" | head -c 500)"
      grep -iE 'firecracker|boot|error|fail|kvm|tap' "$WORKDIR/server.log" | tail -20
      fail "microVM entered state '$STATE'";;
  esac
  sleep 2
done

if [ "$STATE" != "running" ] || [ -z "$GUEST_IP" ]; then
  note "last replica state: '${STATE:-unknown}', ip: '${GUEST_IP:-none}'"
  note "replicas: $(printf '%s' "$REPLICAS" | head -c 500)"
  grep -iE 'firecracker|boot|error|fail|kvm|tap' "$WORKDIR/server.log" | tail -25
  fail "microVM did not reach state=running with a guest IP within ${BOOT_TIMEOUT}s"
fi
ok "replica state=running with guest ip $GUEST_IP"

FC_COUNT="$(pgrep -fc "firecracker.*${WORKDIR}" 2>/dev/null || echo 0)"
[ "$FC_COUNT" -ge 1 ] || {
  note "server log tail:"; tail -30 "$WORKDIR/server.log"
  fail "no firecracker process is running for this smoke run"
}
ok "$FC_COUNT firecracker process(es) running"

step "Verify the guest really booted"

# The kernel's own serial output is the strongest evidence: it is produced by
# the guest, not by Porter's state machine.
if grep -q 'Booting paravirtualized kernel on KVM' "$WORKDIR/server.log"; then
  ok "guest kernel reported 'Booting paravirtualized kernel on KVM'"
else
  note "no kernel boot banner in the server log"
fi
if grep -q 'smpboot: Total of 1 processors activated' "$WORKDIR/server.log"; then
  ok "guest kernel activated its CPU (smpboot)"
fi

note "replica ip: $GUEST_IP"
if ping -c 1 -W 2 "${GUEST_IP%%/*}" >/dev/null 2>&1; then
  ok "guest ${GUEST_IP%%/*} answers on the network"
else
  note "guest ${GUEST_IP%%/*} did not answer ping (a console-less alpine image need not run ICMP; boot is already proven by the kernel banner plus a live firecracker process)"
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
step "Result"
c_ok "v0.0.1-alpha smoke test PASSED — one Firecracker microVM booted end to end"
note "steps passed: $PASSED"
note "server log:   $WORKDIR/server.log"
note "migrate log:  $WORKDIR/migrate.log"
note "image log:    $WORKDIR/image.log"
note "project id:   $PROJECT_ID"
note "database:     $SMOKE_DB"
exit 0
