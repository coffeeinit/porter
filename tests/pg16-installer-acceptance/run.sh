#!/usr/bin/env bash
set -euo pipefail
set +x

# Standalone acceptance harness for the exact installer commit reported by Go Dev.
# All PostgreSQL work is on a private, disposable PG16 cluster under /tmp.
# The installer runs only with PORTER_INSTALL_TEST_MODE=1, a temporary root,
# and the command-interception seam used by scripts/install-smoke.sh.

readonly EXPECTED_SHA="31636e764adcb36c3580a4a16184eed58feea8df"
readonly ARTIFACT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly SOURCE_REPO="${PORTER_INSTALL_SOURCE_REPO:-$(git -C "$ARTIFACT_DIR" rev-parse --show-toplevel)}"
readonly PG_BINDIR="$(pg_config --bindir)"
readonly RUNUSER="/usr/sbin/runuser"
readonly REAL_SUDO="/usr/bin/sudo"

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
PASS_COUNT=0
pass() { PASS_COUNT=$((PASS_COUNT + 1)); printf 'PASS: %s\n' "$*"; }

for cmd in git pg_config pg_lsclusters python3 tar; do
  command -v "$cmd" >/dev/null 2>&1 || fail "required command missing: $cmd"
done
for cmd in initdb pg_ctl postgres psql createdb pg_isready; do
  [[ -x "$PG_BINDIR/$cmd" ]] || fail "PG16 command missing: $PG_BINDIR/$cmd"
done
"$PG_BINDIR/postgres" --version | grep -Eq 'PostgreSQL\) 16\.' || fail "PG16 server binary required"
"$PG_BINDIR/psql" --version | grep -Eq 'PostgreSQL\) 16\.' || fail "PG16 psql required"
"$REAL_SUDO" -n true >/dev/null 2>&1 || fail "passwordless sudo is required for a postgres-owned disposable cluster"
getent passwd postgres >/dev/null || fail "postgres OS account is required"
git -C "$SOURCE_REPO" rev-parse --git-dir >/dev/null 2>&1 || fail "source Git repository not found: $SOURCE_REPO"
git -C "$SOURCE_REPO" cat-file -e "$EXPECTED_SHA^{commit}" 2>/dev/null || fail "expected source commit not present: $EXPECTED_SHA"

WORK=$(mktemp -d /tmp/porter-installer-pg16.XXXXXX)
chmod 0711 "$WORK"
readonly WORK
readonly CLUSTERS_BEFORE="$WORK/clusters-before"
# Capture the registered cluster inventory before starting anything. The private
# cluster below is not registered through pg_createcluster and uses no host socket.
pg_lsclusters --no-header > "$CLUSTERS_BEFORE"
readonly TMPBASE="$WORK/tmp"
readonly FIXTURE="$WORK/repo"
readonly MOCK_BIN="$WORK/mock-bin"
readonly PGDATA="$WORK/pgdata"
readonly PGSOCKET="$WORK/socket"
readonly PGPORT="$(python3 -c 'import secrets; print(55000 + secrets.randbelow(10000))')"

PG_STARTED=0
BLOCKED_COUNT=0
cleanup() {
  local rc=$?
  trap - EXIT
  if [[ "$PG_STARTED" == 1 && -d "$PGDATA" ]]; then
    "$REAL_SUDO" -n -u postgres "$PG_BINDIR/pg_ctl" -D "$PGDATA" -m fast -w stop >/dev/null 2>&1 || true
  fi
  if [[ -n "${CLUSTERS_BEFORE:-}" && -f "$CLUSTERS_BEFORE" ]]; then
    rm -f -- "$CLUSTERS_BEFORE"
  fi
  if [[ -n "${WORK:-}" && "$WORK" == /tmp/porter-installer-pg16.* && -d "$WORK" ]]; then
    "$REAL_SUDO" -n rm -rf -- "$WORK" >/dev/null 2>&1 || true
  fi
  exit "$rc"
}
trap cleanup EXIT

mkdir -m 0700 "$TMPBASE" "$MOCK_BIN" "$FIXTURE"
mkdir -p "$FIXTURE/scripts" "$FIXTURE/deploy/systemd" "$FIXTURE/bin"
# Extract only the exact candidate inputs from Git objects; never checkout or
# write into the candidate worktree.
git -C "$SOURCE_REPO" archive "$EXPECTED_SHA" \
  scripts/install-porter.sh deploy/porter.env.example deploy/systemd/porter.service \
  | tar -x -C "$FIXTURE"
for binary in porter porter-migrate; do
  printf '#!/bin/sh\nexit 0\n' > "$FIXTURE/bin/$binary"
  chmod 0755 "$FIXTURE/bin/$binary"
done

# These are the same synthetic parser fixtures used by the candidate smoke test.
# They only allow the PG provisioning branch to be reached; they are not reviewed
# backend-owned production units and cannot prove migration/service ordering.
cat > "$FIXTURE/deploy/systemd/porter-migrate.service" <<'UNIT'
[Unit]
Description=Porter database migration
After=postgresql.service
[Service]
Type=oneshot
User=porter
Group=porter
LoadCredential=porter-postgres-dsn:/etc/porter/porter-postgres-dsn
ExecStart=/usr/local/bin/porter-migrate apply
UNIT
cat > "$FIXTURE/deploy/systemd/porter.service" <<'UNIT'
[Unit]
Description=Porter control plane
After=network-online.target
Wants=network-online.target
[Service]
Type=simple
User=porter
Group=porter
EnvironmentFile=/etc/porter/porter.env
LoadCredential=porter-admin-token:/etc/porter/porter-admin-token
LoadCredential=porter-postgres-dsn:/etc/porter/porter-postgres-dsn
ExecStart=/usr/local/bin/porter server
Restart=always
NoNewPrivileges=true
[Install]
WantedBy=multi-user.target
UNIT

# Private PG16 cluster data and socket directories are owned by postgres.
"$REAL_SUDO" -n install -d -o postgres -g postgres -m 0700 "$PGDATA" "$PGSOCKET"
"$REAL_SUDO" -n -u postgres "$PG_BINDIR/initdb" -D "$PGDATA" \
  --username=postgres --encoding=UTF8 --no-locale --auth-local=trust --auth-host=reject >/dev/null
"$REAL_SUDO" -n -u postgres "$PG_BINDIR/pg_ctl" -D "$PGDATA" \
  -l "$PGDATA/server.log" \
  -o "-p $PGPORT -k $PGSOCKET -c listen_addresses='' -c unix_socket_permissions=0700" \
  -w start >/dev/null
PG_STARTED=1

# Shared environment used by the safe test-seam wrappers. Installer calls to
# systemctl/apt are intercepted; postgres client commands are directed only to
# the private socket and the real PG16 binaries.
export PORTER_ACCEPTANCE_WORK="$WORK"
export PORTER_ACCEPTANCE_FIXTURE="$FIXTURE"
export PORTER_ACCEPTANCE_MOCK_BIN="$MOCK_BIN"
export PORTER_ACCEPTANCE_PG_BINDIR="$PG_BINDIR"
export PORTER_ACCEPTANCE_PG_SOCKET="$PGSOCKET"
export PORTER_ACCEPTANCE_PG_PORT="$PGPORT"
export PORTER_ACCEPTANCE_REAL_SUDO="$REAL_SUDO"

cat > "$MOCK_BIN/systemctl" <<'SYSTEMCTL'
#!/usr/bin/env bash
set -euo pipefail
printf 'systemctl %s\n' "$*" >> "$PORTER_INSTALL_TEST_SYSTEMCTL_LOG"
if [[ "${1:-}" == --version ]]; then
  printf 'systemd 247 (acceptance test shim; no host systemd call)\n'
  exit 0
fi
if [[ "$*" == 'enable --now postgresql.service' ]]; then
  # Private PG16 is already running under pg_ctl; never call host systemd.
  exit 0
fi
printf 'blocked unexpected systemctl operation: %s\n' "$*" >&2
exit 97
SYSTEMCTL
chmod 0755 "$MOCK_BIN/systemctl"

cat > "$MOCK_BIN/apt-get" <<'APT'
#!/usr/bin/env bash
printf 'blocked apt-get %q\n' "$*" >> "$PORTER_INSTALL_TEST_PACKAGE_LOG"
printf 'blocked: installer must not invoke apt-get in this harness\n' >&2
exit 98
APT
chmod 0755 "$MOCK_BIN/apt-get"

cat > "$MOCK_BIN/getent" <<'GETENT'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$#" == 2 && "$1" == passwd && "$2" == porter ]]; then
  if [[ -f "$PORTER_INSTALL_TEST_USER_MARKER" ]]; then
    printf 'porter:x:997:997:Porter service:%s/var/lib/porter:/usr/sbin/nologin\n' "$PORTER_INSTALL_TEST_ROOT"
    exit 0
  fi
  exit 2
fi
if [[ "$#" == 2 && "$1" == group && "$2" == porter ]]; then
  if [[ -f "$PORTER_INSTALL_TEST_USER_MARKER" ]]; then
    printf '%s\n' 'porter:x:997:'
    exit 0
  fi
  exit 2
fi
exec /usr/bin/getent "$@"
GETENT
chmod 0755 "$MOCK_BIN/getent"

cat > "$MOCK_BIN/sudo" <<'SUDO'
#!/usr/bin/env bash
set -euo pipefail
for secret_var in PORTER_ADMIN_TOKEN PORTER_POSTGRES_DSN PORTER_POSTGRES_API_DSN DATABASE_URL DATABASE_DSN POSTGRES_URL POSTGRES_DSN PGPASSWORD PGPASSFILE; do
  if [[ -n "${!secret_var-}" ]]; then
    printf 'blocked secret-bearing environment variable at privileged boundary: %s\n' "$secret_var" >&2
    exit 98
  fi
done
printf '%q ' "$@" >> "$PORTER_INSTALL_TEST_SUDO_ARGS_LOG"
printf '\n' >> "$PORTER_INSTALL_TEST_SUDO_ARGS_LOG"

if [[ "${1:-}" == -u && "${2:-}" == postgres ]]; then
  command_name=${3:-}
  case "$command_name" in
    psql|pg_isready|createdb) ;;
    *) printf 'blocked unexpected postgres command: %s\n' "$command_name" >&2; exit 99 ;;
  esac
  printf 'client=%s host=%s port=%s\n' "$command_name" "$PORTER_ACCEPTANCE_PG_SOCKET" "$PORTER_ACCEPTANCE_PG_PORT" >> "$PORTER_INSTALL_TEST_PG_CLIENT_LOG"
  exec "$PORTER_ACCEPTANCE_REAL_SUDO" -n -u postgres -- /usr/bin/env -i \
    HOME=/var/lib/postgresql PATH="$PORTER_ACCEPTANCE_PG_BINDIR:/usr/bin:/bin" \
    PGHOST="$PORTER_ACCEPTANCE_PG_SOCKET" PGPORT="$PORTER_ACCEPTANCE_PG_PORT" \
    PGUSER=postgres "$PORTER_ACCEPTANCE_PG_BINDIR/$command_name" "${@:4}"
fi

case "${1:-}" in
  systemctl|*/systemctl)
    exec "$PORTER_ACCEPTANCE_MOCK_BIN/systemctl" "${@:2}"
    ;;
  useradd)
    printf '%q ' "$@" >> "$PORTER_INSTALL_TEST_USERADD_LOG"
    printf '\n' >> "$PORTER_INSTALL_TEST_USERADD_LOG"
    : > "$PORTER_INSTALL_TEST_USER_MARKER"
    exit 0
    ;;
  apt-get|apt|dnf|yum|zypper)
    printf 'blocked package manager %q %q\n' "$1" "${2:-}" >> "$PORTER_INSTALL_TEST_PACKAGE_LOG"
    exit 98
    ;;
  install|test|realpath|stat|chown|chmod|grep|awk|cat|sh)
    ;;
  *)
    printf 'blocked unexpected privileged command: %s\n' "${1:-}" >&2
    exit 100
    ;;
esac

# For actual privileged filesystem helpers, every absolute path must remain in
# the private temp workspace or the exact-SHA fixture repository.
args=("$@")
for i in "${!args[@]}"; do
  if [[ "${args[$i]}" == /* ]]; then
    case "${args[$i]}" in
      "$PORTER_INSTALL_TEST_ROOT"|"$PORTER_INSTALL_TEST_ROOT"/*|"$PORTER_ACCEPTANCE_WORK"/*|"$PORTER_ACCEPTANCE_FIXTURE"/*) ;;
      *) printf 'blocked privileged path outside test-owned roots: %s\n' "${args[$i]}" >&2; exit 101 ;;
    esac
  fi
  if [[ "${args[$i]}" == porter && $i -gt 0 ]]; then
    prev=$((i - 1))
    case "${args[$prev]}" in
      -o|-g) args[$i]=$(id -un) ;;
    esac
  fi
done
exec /usr/bin/sudo -n "${args[@]}"
SUDO
chmod 0755 "$MOCK_BIN/sudo"

# Test-side psql always runs as postgres with a clean environment and the private
# socket. It cannot accidentally fall back to a registered cluster or TCP port.
pg_psql() {
  "$REAL_SUDO" -n -u postgres -- /usr/bin/env -i \
    HOME=/var/lib/postgresql PATH="$PG_BINDIR:/usr/bin:/bin" \
    PGHOST="$PGSOCKET" PGPORT="$PGPORT" PGUSER=postgres \
    "$PG_BINDIR/psql" --no-psqlrc --set ON_ERROR_STOP=1 "$@"
}
pg_value() {
  local database=$1 sql=$2
  pg_psql --dbname "$database" --tuples-only --no-align --command "$sql" | tr -d '\r\n'
}
assert_value() {
  local label=$1 expected=$2 actual=$3
  [[ "$actual" == "$expected" ]] || fail "$label expected '$expected', got '$actual'"
}

run_installer() {
  local label=$1 expected_status=$2 expected_text=$3
  local install_root="$TMPBASE/root-$label"
  local output="$WORK/$label.out"
  local marker="$WORK/$label.user-created"
  local systemctl_log="$WORK/$label.systemctl.log"
  local package_log="$WORK/$label.package.log"
  local pg_client_log="$WORK/$label.pg-clients.log"
  local sudo_log="$WORK/$label.sudo.log"
  local useradd_log="$WORK/$label.useradd.log"
  local token rc
  mkdir -m 0700 "$install_root"
  : > "$systemctl_log"; : > "$package_log"; : > "$pg_client_log"
  : > "$sudo_log"; : > "$useradd_log"
  token=$(python3 -c 'import secrets; print(secrets.token_urlsafe(36))')
  set +e
  printf '%s\n%s\n' "$token" "$token" | \
    env -u PORTER_ADMIN_TOKEN -u PORTER_POSTGRES_DSN -u PORTER_POSTGRES_API_DSN \
      -u DATABASE_URL -u DATABASE_DSN -u POSTGRES_URL -u POSTGRES_DSN \
      -u PGPASSWORD -u PGPASSFILE -u PGHOST -u PGHOSTADDR -u PGPORT -u PGDATABASE -u PGUSER \
      -u PGSSLMODE -u PGOPTIONS -u PGSERVICE -u PGSERVICEFILE \
      TMPDIR="$TMPBASE" PATH="$MOCK_BIN:$PG_BINDIR:/usr/bin:/bin" \
      PORTER_INSTALL_TEST_MODE=1 PORTER_INSTALL_TEST_ROOT="$install_root" \
      PORTER_INSTALL_TEST_MOCK_BIN="$MOCK_BIN" \
      PORTER_INSTALL_TEST_USER_MARKER="$marker" \
      PORTER_INSTALL_TEST_SYSTEMCTL_LOG="$systemctl_log" \
      PORTER_INSTALL_TEST_PACKAGE_LOG="$package_log" \
      PORTER_INSTALL_TEST_PG_CLIENT_LOG="$pg_client_log" \
      PORTER_INSTALL_TEST_SUDO_ARGS_LOG="$sudo_log" \
      PORTER_INSTALL_TEST_USERADD_LOG="$useradd_log" \
      PORTER_ACCEPTANCE_WORK="$WORK" PORTER_ACCEPTANCE_FIXTURE="$FIXTURE" \
      PORTER_ACCEPTANCE_MOCK_BIN="$MOCK_BIN" \
      PORTER_ACCEPTANCE_PG_BINDIR="$PG_BINDIR" \
      PORTER_ACCEPTANCE_PG_SOCKET="$PGSOCKET" PORTER_ACCEPTANCE_PG_PORT="$PGPORT" \
      bash "$FIXTURE/scripts/install-porter.sh" "$FIXTURE/bin/porter" > "$output" 2>&1
  rc=${PIPESTATUS[1]}
  set -e
  if [[ "$expected_status" == success ]]; then
    [[ "$rc" == 0 ]] || { cat "$output" >&2; fail "$label installer exit=$rc, expected success"; }
  else
    [[ "$rc" != 0 ]] || { cat "$output" >&2; fail "$label installer unexpectedly succeeded"; }
    grep -Fq -- "$expected_text" "$output" || { cat "$output" >&2; fail "$label did not report expected refusal: $expected_text"; }
  fi
  if [[ -n "$expected_text" && "$expected_status" == success ]]; then
    grep -Fq -- "$expected_text" "$output" || fail "$label missing expected success text"
  fi
  if grep -Fq -- "$token" "$output" "$sudo_log" "$useradd_log"; then
    fail "$label leaked its admin token to output or privileged command arguments"
  fi
  if [[ -s "$package_log" ]]; then cat "$package_log" >&2; fail "$label attempted package-manager work"; fi
  if grep -Eq 'systemctl .*porter|systemctl daemon-reload|systemctl enable --now porter' "$systemctl_log"; then
    fail "$label attempted Porter service activation in test mode"
  fi
  if ! grep -Fq "host=$PGSOCKET port=$PGPORT" "$pg_client_log"; then
    fail "$label did not direct real PostgreSQL clients to the disposable cluster"
  fi
  if [[ "$expected_status" == success ]]; then
    [[ -f "$install_root/etc/porter/porter-postgres-dsn" ]] || fail "$label did not stage the local PostgreSQL credential in its temp root"
    [[ "$(stat -c '%a' "$install_root/etc/porter/porter-postgres-dsn")" == 600 ]] || fail "$label PostgreSQL credential is not mode 0600"
    [[ "$(stat -c '%a' "$install_root/etc/porter/porter-admin-token")" == 600 ]] || fail "$label admin credential is not mode 0600"
  fi
  PASS_COUNT=$((PASS_COUNT + 1))
  printf 'PASS: installer case %s (exit=%s; local root, PG socket %s/%s)\n' "$label" "$rc" "$PGSOCKET" "$PGPORT"
  LAST_SYSTEMCTL_LOG=$systemctl_log
  LAST_PG_CLIENT_LOG=$pg_client_log
  LAST_OUTPUT=$output
}

# First, demonstrate production mode’s source-unit blocker using the exact commit
# without synthetic units. This must fail before PostgreSQL, systemctl, or apt.
NO_UNIT_FIXTURE="$WORK/no-unit-repo"
cp -a "$FIXTURE" "$NO_UNIT_FIXTURE"
rm -f "$NO_UNIT_FIXTURE/deploy/systemd/porter-migrate.service"
NO_UNIT_ROOT="$TMPBASE/no-unit-root"
mkdir -m 0700 "$NO_UNIT_ROOT"
NO_UNIT_LOG="$WORK/no-unit.out"
NO_UNIT_SYSTEMCTL_LOG="$WORK/no-unit.systemctl.log"
NO_UNIT_PACKAGE_LOG="$WORK/no-unit.package.log"
NO_UNIT_PG_LOG="$WORK/no-unit.pg.log"
: > "$NO_UNIT_SYSTEMCTL_LOG"; : > "$NO_UNIT_PACKAGE_LOG"; : > "$NO_UNIT_PG_LOG"
set +e
env TMPDIR="$TMPBASE" PATH="$MOCK_BIN:$PG_BINDIR:/usr/bin:/bin" \
  PORTER_INSTALL_TEST_MODE=1 PORTER_INSTALL_TEST_ROOT="$NO_UNIT_ROOT" \
  PORTER_INSTALL_TEST_MOCK_BIN="$MOCK_BIN" \
  PORTER_INSTALL_TEST_SYSTEMCTL_LOG="$NO_UNIT_SYSTEMCTL_LOG" \
  PORTER_INSTALL_TEST_PACKAGE_LOG="$NO_UNIT_PACKAGE_LOG" \
  PORTER_INSTALL_TEST_PG_CLIENT_LOG="$NO_UNIT_PG_LOG" \
  bash "$NO_UNIT_FIXTURE/scripts/install-porter.sh" "$NO_UNIT_FIXTURE/bin/porter" > "$NO_UNIT_LOG" 2>&1
no_unit_rc=$?
set -e
[[ "$no_unit_rc" != 0 ]] || fail "source without porter-migrate.service did not fail closed"
grep -Fq 'missing required migration unit source' "$NO_UNIT_LOG" || { cat "$NO_UNIT_LOG" >&2; fail "missing migration-unit refusal was not reported"; }
[[ ! -s "$NO_UNIT_SYSTEMCTL_LOG" && ! -s "$NO_UNIT_PACKAGE_LOG" && ! -s "$NO_UNIT_PG_LOG" ]] || fail "missing-unit fail-closed check performed systemd/package/PG actions"
pass "exact source fails closed when Coder-owned porter-migrate.service is absent, before PG/systemd/package work"

# Fresh local-provisioning path: actual role/database SQL against private PG16.
run_installer fresh success 'Install smoke mode:'
FRESH_SYSTEMCTL_LOG=$LAST_SYSTEMCTL_LOG
FRESH_OUTPUT=$LAST_OUTPUT
assert_value 'porter role flags' safe "$(pg_value postgres "SELECT CASE WHEN rolcanlogin AND NOT rolsuper AND NOT rolcreatedb AND NOT rolcreaterole AND NOT rolinherit AND NOT rolreplication AND NOT rolbypassrls THEN 'safe' ELSE 'unsafe' END FROM pg_roles WHERE rolname='porter'")"
assert_value 'porter database owner/template' 'porter:false' "$(pg_value postgres "SELECT r.rolname || ':' || d.datistemplate::text FROM pg_database d JOIN pg_roles r ON r.oid=d.datdba WHERE d.datname='porter'")"
assert_value 'porter CONNECT' t "$(pg_value postgres "SELECT has_database_privilege('porter','porter','CONNECT')")"
assert_value 'PUBLIC database grants' 0 "$(pg_value postgres "SELECT count(*) FROM pg_database d CROSS JOIN LATERAL aclexplode(COALESCE(d.datacl,acldefault('d',d.datdba))) a WHERE d.datname='porter' AND a.grantee=0")"
assert_value 'porter public-schema USAGE/CREATE' t "$(pg_value porter "SELECT has_schema_privilege('porter','public','USAGE') AND has_schema_privilege('porter','public','CREATE')")"
assert_value 'PUBLIC public-schema CREATE' 0 "$(pg_value porter "SELECT count(*) FROM pg_namespace n CROSS JOIN LATERAL aclexplode(COALESCE(n.nspacl,acldefault('n',n.nspowner))) a WHERE n.nspname='public' AND a.grantee=0 AND a.privilege_type='CREATE'")"
printf 'INFO: PG16 public schema owner is %s; installer contract grants Porter USAGE/CREATE and does not transfer schema ownership.\n' "$(pg_value porter "SELECT pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname='public'")"
pass 'least-privilege porter role, porter-owned database, PUBLIC revocations, and schema grants verified on real PG16'

# Accepted pre-existing DB owned by porter: installer normalizes PUBLIC rights.
pg_psql --dbname postgres --command 'GRANT ALL PRIVILEGES ON DATABASE porter TO PUBLIC' >/dev/null
pg_psql --dbname porter --command 'GRANT CREATE ON SCHEMA public TO PUBLIC' >/dev/null
run_installer owned-existing success 'Install smoke mode:'
assert_value 'owned existing DB owner/template' 'porter:false' "$(pg_value postgres "SELECT r.rolname || ':' || d.datistemplate::text FROM pg_database d JOIN pg_roles r ON r.oid=d.datdba WHERE d.datname='porter'")"
assert_value 'owned DB PUBLIC privileges after normalization' 0 "$(pg_value postgres "SELECT count(*) FROM pg_database d CROSS JOIN LATERAL aclexplode(COALESCE(d.datacl,acldefault('d',d.datdba))) a WHERE d.datname='porter' AND a.grantee=0")"
assert_value 'owned DB PUBLIC schema CREATE after normalization' 0 "$(pg_value porter "SELECT count(*) FROM pg_namespace n CROSS JOIN LATERAL aclexplode(COALESCE(n.nspacl,acldefault('n',n.nspowner))) a WHERE n.nspname='public' AND a.grantee=0 AND a.privilege_type='CREATE'")"
if grep -Fq 'client=createdb' "$LAST_PG_CLIENT_LOG"; then fail 'accepted existing porter DB was recreated'; fi
pass 'pre-existing porter-owned non-template database accepted and normalized without createdb'

# Membership direction 1: porter is a member of another role.
pg_psql --dbname postgres --command 'CREATE ROLE porter_parent NOLOGIN; GRANT porter_parent TO porter' >/dev/null
run_installer member-of-other rejected 'existing porter PostgreSQL role has memberships; refusing to alter it'
assert_value 'porter membership in another role preserved' 1 "$(pg_value postgres "SELECT count(*) FROM pg_auth_members m JOIN pg_roles p ON p.oid=m.member JOIN pg_roles r ON r.oid=m.roleid WHERE p.rolname='porter' AND r.rolname='porter_parent'")"
if grep -Eiq 'client=createdb|ALTER ROLE|ALTER DATABASE|REVOKE|GRANT ' "$LAST_PG_CLIENT_LOG"; then fail 'installer attempted provisioning after membership rejection'; fi
pg_psql --dbname postgres --command 'REVOKE porter_parent FROM porter; DROP ROLE porter_parent' >/dev/null
pass 'membership rejection verified when porter is the member'

# Membership direction 2: another role is a member of porter.
pg_psql --dbname postgres --command 'CREATE ROLE porter_child NOLOGIN; GRANT porter TO porter_child' >/dev/null
run_installer other-member-of-porter rejected 'existing porter PostgreSQL role has memberships; refusing to alter it'
assert_value 'membership in porter preserved' 1 "$(pg_value postgres "SELECT count(*) FROM pg_auth_members m JOIN pg_roles p ON p.oid=m.roleid JOIN pg_roles c ON c.oid=m.member WHERE p.rolname='porter' AND c.rolname='porter_child'")"
if grep -Eiq 'client=createdb|ALTER ROLE|ALTER DATABASE|REVOKE|GRANT ' "$LAST_PG_CLIENT_LOG"; then fail 'installer attempted provisioning after inverse membership rejection'; fi
pg_psql --dbname postgres --command 'REVOKE porter FROM porter_child; DROP ROLE porter_child' >/dev/null
pass 'membership rejection verified when another role is a member of porter'

# Unexpected pre-existing DB owner: refusal must precede role or DB mutation.
pg_psql --dbname postgres --command 'DROP DATABASE porter' >/dev/null
pg_psql --dbname postgres --command 'CREATE ROLE porter_owner_probe NOLOGIN; ALTER ROLE porter WITH SUPERUSER CREATEDB CREATEROLE INHERIT' >/dev/null
pg_psql --dbname postgres --command 'CREATE DATABASE porter OWNER porter_owner_probe' >/dev/null
run_installer foreign-owner rejected 'existing local porter database identity/owner is not validated; refusing to alter it'
assert_value 'unexpected DB owner preserved' porter_owner_probe "$(pg_value postgres "SELECT r.rolname FROM pg_database d JOIN pg_roles r ON r.oid=d.datdba WHERE d.datname='porter'")"
assert_value 'unsafe role flags not mutated on rejection' unsafe "$(pg_value postgres "SELECT CASE WHEN rolsuper AND rolcreatedb AND rolcreaterole AND rolinherit THEN 'unsafe' ELSE 'changed' END FROM pg_roles WHERE rolname='porter'")"
pass 'unexpected foreign-owned porter database refused before role/database normalization'

# Unexpected template DB: current contract accepts only a non-template DB.
pg_psql --dbname postgres --command 'DROP DATABASE porter' >/dev/null
pg_psql --dbname postgres --command 'ALTER ROLE porter NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS' >/dev/null
pg_psql --dbname postgres --command 'CREATE DATABASE porter OWNER porter' >/dev/null
pg_psql --dbname postgres --command 'ALTER DATABASE porter WITH IS_TEMPLATE true' >/dev/null
run_installer template-database rejected 'existing local porter database identity/owner is not validated; refusing to alter it'
assert_value 'template DB identity preserved' 'porter:true' "$(pg_value postgres "SELECT r.rolname || ':' || d.datistemplate::text FROM pg_database d JOIN pg_roles r ON r.oid=d.datdba WHERE d.datname='porter'")"
pass 'unexpected template porter database refused without alteration'

# Test-mode activation guard: only the PostgreSQL-service stub may be recorded.
if grep -Eq 'systemctl (enable --now porter|daemon-reload|start porter|restart porter)' "$FRESH_SYSTEMCTL_LOG"; then
  fail 'test-mode run attempted Porter service activation'
fi
if ! grep -Fq 'enable --now postgresql.service' "$FRESH_SYSTEMCTL_LOG"; then
  fail 'test-mode run did not exercise the intercepted PostgreSQL service seam'
fi
if ! grep -Fq 'migrations and Porter unit reload/activation were skipped' "$FRESH_OUTPUT"; then
  fail 'test-mode output did not disclose skipped migration/service activation'
fi
pass 'test mode does not activate Porter or run migrations; systemctl access stayed inside the stub'

# Positive ordering is explicitly blocked, not simulated: the exact commit omits
# the real backend-owned migration unit and its production server unit fails the
# credential validation contract. The installer’s production branch is fail-closed.
if ! git -C "$SOURCE_REPO" cat-file -e "$EXPECTED_SHA:deploy/systemd/porter-migrate.service" 2>/dev/null; then
  BLOCKED_COUNT=$((BLOCKED_COUNT + 1))
  printf 'BLOCKED: positive migration-before-service sequencing; exact SHA lacks deploy/systemd/porter-migrate.service and deliberately reports no migration or Porter activation until Coder-owned unit/readers/migrator integration lands.\n'
else
  printf 'BLOCKED: positive sequencing still requires a runnable Coder-owned migrator and reviewed service integration; this harness does not promote synthetic units to evidence.\n'
  BLOCKED_COUNT=$((BLOCKED_COUNT + 1))
fi

# Verify registered cluster inventory was not changed. All installer DB client
# calls were logged with the private Unix socket and random port above.
pg_lsclusters --no-header > "$WORK/clusters-after"
if ! cmp -s "$CLUSTERS_BEFORE" "$WORK/clusters-after"; then
  diff -u "$CLUSTERS_BEFORE" "$WORK/clusters-after" >&2 || true
  fail 'registered PostgreSQL cluster inventory changed during the harness'
fi
if ! grep -Fq "host=$PGSOCKET port=$PGPORT" "$WORK"/*.pg-clients.log 2>/dev/null; then
  fail 'no installer PostgreSQL client was observed on the private socket'
fi
if grep -E 'host=(/var/run/postgresql|localhost|127\.0\.0\.1|::1|) port=(5432|)' "$WORK"/*.pg-clients.log >/dev/null 2>&1; then
  fail 'a PostgreSQL installer client used a default or host cluster endpoint'
fi
pass 'registered PostgreSQL cluster inventory unchanged; installer PG clients used only the disposable PG16 Unix socket'

printf 'RESULT: %s executable checks passed; %s acceptance item(s) blocked.\n' "$PASS_COUNT" "$BLOCKED_COUNT"
if (( BLOCKED_COUNT > 0 )); then
  printf 'ACCEPTANCE STATUS: PARTIAL/BLOCKED (do not treat as full install acceptance).\n'
  exit 2
fi
printf 'ACCEPTANCE STATUS: PASS\n'
