#!/usr/bin/env bash
set -euo pipefail
set +x

ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
if [[ ! -x /usr/bin/sudo ]]; then
  echo "install smoke requires sudo for temporary-root ownership checks" >&2
  exit 1
fi

WORK=$(mktemp -d)
cleanup() {
  if [[ -n ${WORK:-} && -d $WORK ]]; then
    /usr/bin/sudo chown -R "$(id -u):$(id -g)" "$WORK" 2>/dev/null || true
    rm -rf "$WORK"
  fi
}
trap cleanup EXIT
MOCK_BIN="$WORK/mock-bin"
FIXTURE_ROOT="$WORK/repo"
mkdir -p "$MOCK_BIN" "$FIXTURE_ROOT/scripts" "$FIXTURE_ROOT/deploy/systemd" "$FIXTURE_ROOT/bin"
TOKEN=$(python3 -c 'import secrets; print(secrets.token_urlsafe(36))')
valid_input=$(printf '%s\n%s\n' "$TOKEN" "$TOKEN")
LOCAL_DSN='host=/var/run/postgresql user=porter dbname=porter sslmode=disable'
USER_MARKER="$WORK/porter-user-created"
BAD_USER_MARKER="$WORK/bad-porter-user-created"
SUDO_ARGS_LOG="$WORK/sudo-args.log"
USERADD_LOG="$WORK/useradd.log"
SYSTEMCTL_LOG="$WORK/systemctl.log"
PG_LOG="$WORK/postgres.log"
PACKAGE_LOG="$WORK/package.log"
EVENT_LOG="$WORK/events.log"
AGENT_INSTALL_LOG="$WORK/agent-install.log"
: > "$SUDO_ARGS_LOG"
: > "$USERADD_LOG"
: > "$SYSTEMCTL_LOG"
: > "$PG_LOG"
: > "$PACKAGE_LOG"
: > "$EVENT_LOG"
: > "$AGENT_INSTALL_LOG"

cp -- "$ROOT/scripts/install-porter.sh" "$FIXTURE_ROOT/scripts/install-porter.sh"
cp -- "$ROOT/scripts/install-agent.sh" "$FIXTURE_ROOT/scripts/install-agent.sh"
cp -- "$ROOT/deploy/porter.env.example" "$FIXTURE_ROOT/deploy/porter.env.example"
cp -- "$ROOT/deploy/systemd/porter-agent.service" "$FIXTURE_ROOT/deploy/systemd/porter-agent.service"
cp -- "$ROOT/deploy/porter-agent.env.example" "$FIXTURE_ROOT/deploy/porter-agent.env.example"
for binary in porter porter-migrate porter-agent; do
  cat > "$FIXTURE_ROOT/bin/$binary" <<'BIN'
#!/usr/bin/env sh
exit 0
BIN
  chmod 0755 "$FIXTURE_ROOT/bin/$binary"
done

# Synthetic units exercise the installer parser only. They are not Coder-owned
# unit sources and are not evidence that any real unit has been reviewed.
write_valid_units() {
  cat > "$FIXTURE_ROOT/deploy/systemd/porter-migrate.service" <<'UNIT'
[Unit]
Description=Porter database migration
After=postgresql.service

[Service]
Type=oneshot
User=porter
Group=porter
LoadCredential=porter-admin-token:/etc/porter/porter-admin-token
LoadCredential=porter-postgres-dsn:/etc/porter/porter-postgres-dsn
ExecStart=/usr/local/bin/porter-migrate apply
UNIT
  cat > "$FIXTURE_ROOT/deploy/systemd/porter.service" <<'UNIT'
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
}

cat > "$MOCK_BIN/getent" <<'GETENT'
#!/usr/bin/env bash
set -euo pipefail
marker=${PORTER_INSTALL_TEST_USER_MARKER:-}
if [[ "$#" == 2 && "$1" == passwd && "$2" == porter ]]; then
  if [[ -n "$marker" && -f "$marker" ]]; then
    home="${PORTER_INSTALL_TEST_ROOT:-/var/lib/porter}/var/lib/porter"
    if [[ ${PORTER_INSTALL_TEST_BAD_ACCOUNT:-0} == 1 ]]; then
      printf 'porter:x:0:0:bad account:%s:/bin/sh\n' "$home"
    else
      printf 'porter:x:997:997:Porter service:%s:/usr/sbin/nologin\n' "$home"
    fi
    exit 0
  fi
  exit 2
fi
if [[ "$#" == 2 && "$1" == group && "$2" == porter ]]; then
  if [[ -n "$marker" && -f "$marker" ]]; then
    if [[ ${PORTER_INSTALL_TEST_BAD_ACCOUNT:-0} == 1 ]]; then
      printf '%s\n' 'porter:x:0:'
    else
      printf '%s\n' 'porter:x:997:'
    fi
    exit 0
  fi
  exit 2
fi
echo "blocked unexpected getent lookup in installer smoke" >&2
exit 96
GETENT
chmod 0755 "$MOCK_BIN/getent"

cat > "$MOCK_BIN/systemctl" <<'SYSTEMCTL'
#!/usr/bin/env bash
set -euo pipefail
printf '%q ' "$@" >> "$PORTER_INSTALL_TEST_SYSTEMCTL_LOG"
printf '\n' >> "$PORTER_INSTALL_TEST_SYSTEMCTL_LOG"
printf 'systemctl %s\n' "$*" >> "$PORTER_INSTALL_TEST_EVENT_LOG"
if [[ ${1:-} == --version ]]; then
  printf 'systemd %s (smoke fake)\n' "${PORTER_INSTALL_TEST_SYSTEMD_VERSION:-247}"
  exit 0
fi
if [[ "$*" == 'enable --now postgresql.service' ]]; then
  exit 0
fi
if [[ ${PORTER_INSTALL_TEST_AGENT_INSTALL_PROBE:-0} == 1 && "$*" == daemon-reload ]]; then
  exit 0
fi
echo "blocked non-PostgreSQL fake systemd action in installer smoke" >&2
exit 97
SYSTEMCTL
chmod 0755 "$MOCK_BIN/systemctl"
cat > "$MOCK_BIN/pg_isready" <<'PGREADY'
#!/usr/bin/env bash
set -euo pipefail
count=0
if [[ -f "$PORTER_INSTALL_TEST_PG_READY_COUNT" ]]; then
  read -r count < "$PORTER_INSTALL_TEST_PG_READY_COUNT"
fi
count=$((count + 1))
printf '%s\n' "$count" > "$PORTER_INSTALL_TEST_PG_READY_COUNT"
printf 'pg_isready ' >> "$PORTER_INSTALL_TEST_PG_LOG"
printf '%s ' "$@" >> "$PORTER_INSTALL_TEST_PG_LOG"
printf '\n' >> "$PORTER_INSTALL_TEST_PG_LOG"
printf 'pg_isready %s\n' "$*" >> "$PORTER_INSTALL_TEST_EVENT_LOG"
if [[ ${PORTER_INSTALL_TEST_PG_READY:-1} != 1 ]]; then
  exit 1
fi
[[ $count -ge ${PORTER_INSTALL_TEST_PG_READY_AFTER:-1} ]]
PGREADY
chmod 0755 "$MOCK_BIN/pg_isready"

cat > "$MOCK_BIN/psql" <<'PSQL'
#!/usr/bin/env bash
set -euo pipefail
printf 'psql ' >> "$PORTER_INSTALL_TEST_PG_LOG"
printf '%s ' "$@" >> "$PORTER_INSTALL_TEST_PG_LOG"
printf '\n' >> "$PORTER_INSTALL_TEST_PG_LOG"
printf 'psql %s\n' "$*" >> "$PORTER_INSTALL_TEST_EVENT_LOG"
all_args="$*"
if [[ "$all_args" == *pg_auth_members* ]]; then
  if [[ "$all_args" != *'m.roleid = r.oid'* || "$all_args" != *'m.member = r.oid'* ]]; then
    echo "fake psql rejected incomplete role membership query" >&2
    exit 96
  fi
  case ${PORTER_INSTALL_TEST_PG_MEMBERSHIP:-safe} in
    safe) printf '%s\n' safe ;;
    member|roleid) printf '%s\n' unsafe ;;
    *) echo "unknown fake role membership scenario" >&2; exit 96 ;;
  esac
elif [[ "$all_args" == *pg_database* ]]; then
  if [[ "$all_args" != *"d.datname = 'porter'"* ||
        "$all_args" != *'r.oid = d.datdba'* ||
        "$all_args" != *"r.rolname = 'porter'"* ||
        "$all_args" != *'d.datistemplate = false'* ]]; then
    echo "fake psql rejected incomplete database identity/owner query" >&2
    exit 96
  fi
  printf '%s\n' "${PORTER_INSTALL_TEST_PG_DATABASE:-absent}"
fi
PSQL
chmod 0755 "$MOCK_BIN/psql"

cat > "$MOCK_BIN/createdb" <<'CREATEDB'
#!/usr/bin/env bash
set -euo pipefail
printf 'createdb ' >> "$PORTER_INSTALL_TEST_PG_LOG"
printf '%s ' "$@" >> "$PORTER_INSTALL_TEST_PG_LOG"
printf '\n' >> "$PORTER_INSTALL_TEST_PG_LOG"
printf 'createdb %s\n' "$*" >> "$PORTER_INSTALL_TEST_EVENT_LOG"
CREATEDB
chmod 0755 "$MOCK_BIN/createdb"
cat > "$MOCK_BIN/sleep" <<'SLEEP'
#!/usr/bin/env bash
set -euo pipefail
[[ "$#" == 1 && "$1" == 1 ]]
SLEEP
chmod 0755 "$MOCK_BIN/sleep"
for package_manager in apt-get apt dnf yum zypper; do
  cat > "$MOCK_BIN/$package_manager" <<'PACKAGE'
#!/usr/bin/env bash
set -euo pipefail
printf '%s ' "$(basename "$0")" >> "$PORTER_INSTALL_TEST_PACKAGE_LOG"
printf '%q ' "$@" >> "$PORTER_INSTALL_TEST_PACKAGE_LOG"
printf '\n' >> "$PORTER_INSTALL_TEST_PACKAGE_LOG"
printf '%s %s\n' "$(basename "$0")" "$*" >> "$PORTER_INSTALL_TEST_EVENT_LOG"
if [[ $(basename "$0") == apt-get && "$*" == 'install -y postgresql' ]]; then
  if [[ ${PORTER_INSTALL_TEST_APT_FAIL:-0} == 1 ]]; then
    exit 98
  fi
  if [[ -n ${PORTER_INSTALL_TEST_INSTALLED_MARKER:-} ]]; then
    : > "$PORTER_INSTALL_TEST_INSTALLED_MARKER"
  fi
  exit 0
fi
echo "blocked unsupported package operation in installer smoke" >&2
exit 98
PACKAGE
  chmod 0755 "$MOCK_BIN/$package_manager"
done

cat > "$MOCK_BIN/sudo" <<'SUDO'
#!/usr/bin/env bash
set -euo pipefail
printf '%q ' "$@" >> "$PORTER_INSTALL_TEST_SUDO_ARGS_LOG"
printf '\n' >> "$PORTER_INSTALL_TEST_SUDO_ARGS_LOG"
if [[ -v ADMIN_TOKEN || -v confirm || -n ${PORTER_ADMIN_TOKEN:-} || -n ${PORTER_POSTGRES_DSN:-} ||
      -n ${PORTER_POSTGRES_API_DSN:-} || -n ${DATABASE_URL:-} || -n ${DATABASE_DSN:-} ||
      -n ${PORTER_DATABASE_URL:-} || -n ${POSTGRES_URL:-} || -n ${POSTGRES_DSN:-} ||
      -n ${CUSTOM_DSN:-} || -n ${PGPASSWORD:-} || -n ${PGPASSFILE:-} || -n ${PGTARGETSESSIONATTRS:-} ]]; then
  echo "credential or PostgreSQL environment reached a privileged command" >&2
  exit 98
fi

if [[ ${PORTER_INSTALL_TEST_AGENT_PROBE:-0} == 1 ]]; then
  echo "blocked agent installer privileged-command probe" >&2
  exit 97
fi
if [[ ${PORTER_INSTALL_TEST_AGENT_INSTALL_PROBE:-0} == 1 ]]; then
  case ${1:-} in
    test)
      if [[ ${2:-} == -L && ${3:-} == /etc/porter ]]; then
        [[ ${PORTER_INSTALL_TEST_AGENT_CONFIG_SYMLINK:-0} == 1 ]]
        exit $?
      fi
      if [[ ${3:-} == /etc/porter && ${2:-} == -e ]]; then
        [[ ${PORTER_INSTALL_TEST_AGENT_CONFIG_DIR_EXISTS:-${PORTER_INSTALL_TEST_AGENT_ENV_EXISTS:-0}} == 1 ]]
        exit $?
      fi
      if [[ ${3:-} == /etc/porter && ${2:-} == -d ]]; then
        [[ ${PORTER_INSTALL_TEST_AGENT_CONFIG_DIR_EXISTS:-${PORTER_INSTALL_TEST_AGENT_ENV_EXISTS:-0}} == 1 ]]
        exit $?
      fi
      if [[ ${2:-} == -L && ${3:-} == /etc/porter/porter-agent.env ]]; then
        [[ ${PORTER_INSTALL_TEST_AGENT_ENV_SYMLINK:-0} == 1 ]]
        exit $?
      fi
      if [[ ${3:-} == /etc/porter/porter-agent.env && ${2:-} == -e ]]; then
        [[ ${PORTER_INSTALL_TEST_AGENT_ENV_EXISTS:-0} == 1 ]]
        exit $?
      fi
      if [[ ${3:-} == /etc/porter/porter-agent.env && ${2:-} == -f ]]; then
        [[ ${PORTER_INSTALL_TEST_AGENT_ENV_EXISTS:-0} == 1 &&
           ${PORTER_INSTALL_TEST_AGENT_ENV_REGULAR:-1} == 1 ]]
        exit $?
      fi
      echo "blocked unexpected agent destination test" >&2
      exit 98
      ;;
    install)
      printf '%q ' "$@" >> "$PORTER_INSTALL_TEST_AGENT_INSTALL_LOG"
      printf '\n' >> "$PORTER_INSTALL_TEST_AGENT_INSTALL_LOG"
      exit 0
      ;;
    stat)
      if [[ ${2:-} == -c && ${3:-} == '%u:%g:%a' && ${4:-} == /etc/porter/porter-agent.env ]]; then
        printf '%s\n' "${PORTER_INSTALL_TEST_AGENT_ENV_META:-0:0:600}"
        exit 0
      fi
      echo "blocked unexpected agent metadata check" >&2
      exit 98
      ;;
    systemctl)
      exec "$PORTER_INSTALL_TEST_MOCK_BIN/systemctl" "${@:2}"
      ;;
    *)
      echo "blocked unexpected agent installer command" >&2
      exit 98
      ;;
  esac
fi

if [[ ${1:-} == -u && ${2:-} == postgres ]]; then
  command_name=${3:-}
  case "$command_name" in
    pg_isready|psql|createdb)
      exec "$PORTER_INSTALL_TEST_MOCK_BIN/$command_name" "${@:4}"
      ;;
    *)
      echo "blocked unexpected PostgreSQL command in installer smoke" >&2
      exit 99
      ;;
  esac
fi
case ${1:-} in
  systemctl|*/systemctl)
    exec "$PORTER_INSTALL_TEST_MOCK_BIN/systemctl" "${@:2}"
    ;;
  useradd)
    printf '%q ' "$@" >> "$PORTER_INSTALL_TEST_USERADD_LOG"
    printf '\n' >> "$PORTER_INSTALL_TEST_USERADD_LOG"
    : > "$PORTER_INSTALL_TEST_USER_MARKER"
    exit 0
    ;;
  apt-get|apt|dnf|yum|zypper)
    exec "$PORTER_INSTALL_TEST_MOCK_BIN/$1" "${@:2}"
    ;;
  install|test|realpath|stat|chown|chmod|grep|awk|cat|sh)
    ;;
  *)
    echo "blocked unexpected privileged command in installer smoke" >&2
    exit 100
    ;;
esac

# Every absolute path passed to a real privileged helper must stay within the
# temporary root, smoke workspace, or copied fixture repository.
for arg in "$@"; do
  if [[ "$arg" == /* ]]; then
    case "$arg" in
      "$PORTER_INSTALL_TEST_ROOT"|"$PORTER_INSTALL_TEST_ROOT"/*|"$PORTER_INSTALL_TEST_WORK"/*|"$PORTER_INSTALL_TEST_REPO_ROOT"/*) ;;
      *)
        echo "blocked privileged path outside isolated roots" >&2
        exit 101
        ;;
    esac
  fi
done

args=("$@")
if [[ ${args[0]:-} == install ]]; then
  test_user=$(id -un)
  test_group=$(id -gn)
  for i in "${!args[@]}"; do
    if [[ ${args[$i]} == porter && $i -gt 0 && ${args[$((i - 1))]} == -o ]]; then
      args[$i]=$test_user
    elif [[ ${args[$i]} == porter && $i -gt 0 && ${args[$((i - 1))]} == -g ]]; then
      args[$i]=$test_group
    fi
  done
fi
exec /usr/bin/sudo "${args[@]}"
SUDO
chmod 0755 "$MOCK_BIN/sudo"

export PORTER_INSTALL_TEST_SYSTEMCTL_LOG="$SYSTEMCTL_LOG"
export PORTER_INSTALL_TEST_PG_LOG="$PG_LOG"
export PORTER_INSTALL_TEST_PACKAGE_LOG="$PACKAGE_LOG"
export PORTER_INSTALL_TEST_EVENT_LOG="$EVENT_LOG"
export PORTER_INSTALL_TEST_AGENT_INSTALL_LOG="$AGENT_INSTALL_LOG"
export PORTER_INSTALL_TEST_PG_READY_COUNT="$WORK/pg-ready-count"
export PORTER_INSTALL_TEST_SUDO_ARGS_LOG="$SUDO_ARGS_LOG"
export PORTER_INSTALL_TEST_USERADD_LOG="$USERADD_LOG"
export PORTER_INSTALL_TEST_MOCK_BIN="$MOCK_BIN"
export PORTER_INSTALL_TEST_WORK="$WORK"
export PORTER_INSTALL_TEST_REPO_ROOT="$FIXTURE_ROOT"

reset_logs() {
  : > "$SUDO_ARGS_LOG"
  : > "$USERADD_LOG"
  : > "$SYSTEMCTL_LOG"
  : > "$PG_LOG"
  : > "$PACKAGE_LOG"
  : > "$EVENT_LOG"
  : > "$AGENT_INSTALL_LOG"
  rm -f "$PORTER_INSTALL_TEST_PG_READY_COUNT"
}
command() {
  if [[ ${1:-} == -v && -n ${PORTER_INSTALL_TEST_MISSING_COMMAND:-} &&
        ${2:-} == "$PORTER_INSTALL_TEST_MISSING_COMMAND" &&
        ! -e ${PORTER_INSTALL_TEST_INSTALLED_MARKER:-/nonexistent/porter-smoke-marker} ]]; then
    return 1
  fi
  builtin command "$@"
}
export -f command
run_installer() {
  local target_root=$1 input=$2 output=$3 marker=${4:-$USER_MARKER}
  PORTER_INSTALL_TEST_MODE=1 \
  PORTER_INSTALL_TEST_ROOT="$target_root" \
  PORTER_INSTALL_TEST_USER_MARKER="$marker" \
  PATH="$MOCK_BIN:$PATH" \
    bash "$FIXTURE_ROOT/scripts/install-porter.sh" "$FIXTURE_ROOT/bin/porter" >"$output" 2>&1 <<<"$input"
}
event_line() {
  local needle=$1
  awk -v needle="$needle" 'index($0, needle) { print NR; exit }' "$EVENT_LOG"
}
assert_event_before() {
  local first=$1 second=$2 first_line second_line
  first_line=$(event_line "$first")
  second_line=$(event_line "$second")
  if [[ -z "$first_line" || -z "$second_line" ]] || (( first_line >= second_line )); then
    echo "expected event order not found: $first before $second" >&2
    cat "$EVENT_LOG" >&2
    return 1
  fi
}
assert_no_porter_writes() {
  local target_root=$1 output=$2
  if [[ -s "$USERADD_LOG" ]] ||
     grep -Eq '(^|[[:space:]])(install|chown|chmod|useradd)([[:space:]]|$)' "$SUDO_ARGS_LOG" ||
     grep -Eq 'CREATE ROLE|ALTER ROLE|createdb|ALTER DATABASE|REVOKE|GRANT|DROP DATABASE' "$PG_LOG"; then
    echo "local PostgreSQL failure proceeded to a Porter write" >&2
    cat "$output" "$SUDO_ARGS_LOG" "$USERADD_LOG" "$PG_LOG" >&2
    return 1
  fi
  assert_empty_target "$target_root"
}

assert_empty_target() {
  local target_root=$1
  if find "$target_root" -mindepth 1 -print -quit | grep -q .; then
    echo "preflight failure wrote into temporary root: $target_root" >&2
    find "$target_root" -mindepth 1 -print >&2
    return 1
  fi
}
assert_preflight_side_effects_absent() {
  local output=$1
  if grep -Eq 'Read the admin token|Admin token \(' "$output"; then
    echo "preflight failure prompted for a secret" >&2
    return 1
  fi
  if [[ -s "$USERADD_LOG" || -s "$PACKAGE_LOG" ]]; then
    echo "preflight failure invoked user or package commands" >&2
    return 1
  fi
  if grep -Eq '(^|[[:space:]])(install|chown|chmod|useradd|createdb|apt-get|apt|dnf|yum|zypper)([[:space:]]|$)' "$SUDO_ARGS_LOG" || \
     grep -Eq 'createdb|CREATE ROLE|ALTER DATABASE|REVOKE|GRANT|DROP DATABASE' "$PG_LOG"; then
    echo "preflight failure attempted a mutation or package operation" >&2
    cat "$SUDO_ARGS_LOG" >&2
    return 1
  fi
  if grep -Eq 'daemon-reload|enable|start|restart|stop' "$SYSTEMCTL_LOG"; then
    echo "preflight failure attempted a systemd action" >&2
    return 1
  fi
}
assert_no_secret_leaks() {
  local file contents
  for file in "$@"; do
    [[ -f "$file" ]] || continue
    contents=$(<"$file")
    if [[ "$contents" == *"$TOKEN"* || "$contents" == *"dsn-inherited-sentinel"* ||
          "$contents" == *"database-url-inherited-sentinel"* || "$contents" == *"pg-password-inherited-sentinel"* ]]; then
      echo "secret or inherited PostgreSQL value leaked to output or command logs" >&2
      return 1
    fi
  done
}
assert_no_postgres_env_in_sudo() {
  if grep -q 'credential or PostgreSQL environment reached' "$WORK"/*.log 2>/dev/null; then
    echo "an inherited PostgreSQL variable reached a privileged command" >&2
    return 1
  fi
}
expect_preflight_rejection() {
  local label=$1 target_root=$2 output=$3 expected=$4 marker=${5:-$USER_MARKER} allow_config=${6:-0}
  mkdir -p "$target_root"
  reset_logs
  set +e
  run_installer "$target_root" "" "$output" "$marker"
  local status=$?
  set -e
  if [[ $status -eq 0 ]] || ! grep -Fq "$expected" "$output"; then
    echo "expected preflight rejection did not occur: $label" >&2
    cat "$output" >&2
    return 1
  fi
  if [[ "$allow_config" == 1 ]]; then
    if [[ $(find "$target_root" -mindepth 1 -print | wc -l) -ne 3 ||
          $(<"$target_root/etc/porter/porter.env") != 'CUSTOM_DSN=dsn-inherited-sentinel' ]]; then
      echo "preflight changed or added files beside the existing unsafe config" >&2
      find "$target_root" -mindepth 1 -print >&2
      return 1
    fi
  else
    assert_empty_target "$target_root"
  fi
  assert_preflight_side_effects_absent "$output"
  assert_no_secret_leaks "$output" "$SUDO_ARGS_LOG" "$USERADD_LOG" "$PG_LOG" "$SYSTEMCTL_LOG" "$PACKAGE_LOG"
}

# A non-canonical/symlink test root is rejected before unit parsing or writes.
mkdir -p "$WORK/canonical-target"
ln -s "$WORK/canonical-target" "$WORK/root-symlink"
reset_logs
set +e
PORTER_INSTALL_TEST_MODE=1 PORTER_INSTALL_TEST_ROOT="$WORK/root-symlink" \
  PATH="$MOCK_BIN:$PATH" bash "$FIXTURE_ROOT/scripts/install-porter.sh" "$FIXTURE_ROOT/bin/porter" \
  >"$WORK/symlink-root.log" 2>&1 </dev/null
symlink_root_status=$?
set -e
if [[ $symlink_root_status -eq 0 ]] || ! grep -Fq 'canonical child' "$WORK/symlink-root.log"; then
  echo "installer accepted a symlink test root" >&2
  cat "$WORK/symlink-root.log" >&2
  exit 1
fi
assert_empty_target "$WORK/canonical-target"
assert_preflight_side_effects_absent "$WORK/symlink-root.log"

mkdir -p "$WORK/world-writable-root"
chmod 0777 "$WORK/world-writable-root"
reset_logs
set +e
PORTER_INSTALL_TEST_MODE=1 PORTER_INSTALL_TEST_ROOT="$WORK/world-writable-root" \
  PATH="$MOCK_BIN:$PATH" bash "$FIXTURE_ROOT/scripts/install-porter.sh" "$FIXTURE_ROOT/bin/porter" \
  >"$WORK/world-writable-root.log" 2>&1 </dev/null
world_writable_status=$?
set -e
if [[ $world_writable_status -eq 0 ]] || ! grep -Fq 'must not be group- or world-writable' "$WORK/world-writable-root.log"; then
  echo "installer accepted a group/world-writable test root" >&2
  cat "$WORK/world-writable-root.log" >&2
  exit 1
fi
assert_empty_target "$WORK/world-writable-root"
assert_preflight_side_effects_absent "$WORK/world-writable-root.log"

# A missing application/migration executable or unit must fail before prompts, files,
# account creation, or PostgreSQL calls.
MISSING_PORTER="$FIXTURE_ROOT/bin/porter"
mv "$MISSING_PORTER" "$WORK/porter.bin"
expect_preflight_rejection missing-porter-binary "$WORK/missing-porter-root" "$WORK/missing-porter.log" \
  'missing required executable Porter binary'
mv "$WORK/porter.bin" "$MISSING_PORTER"

MISSING_MIGRATOR="$FIXTURE_ROOT/bin/porter-migrate"
mv "$MISSING_MIGRATOR" "$WORK/porter-migrate.bin"
expect_preflight_rejection missing-migrator-binary "$WORK/missing-migrator-root" "$WORK/missing-migrator.log" \
  'missing required executable PostgreSQL migration binary'
mv "$WORK/porter-migrate.bin" "$MISSING_MIGRATOR"

MISSING_ROOT="$WORK/missing-unit-root"
mkdir -p "$MISSING_ROOT"
expect_preflight_rejection missing-migration-unit "$MISSING_ROOT" "$WORK/missing-unit.log" \
  'missing required migration unit source: deploy/systemd/porter-migrate.service'

# Strict synthetic-unit checks reject conflicts and every credential fallback.
write_valid_units
cp "$FIXTURE_ROOT/deploy/systemd/porter.service" "$WORK/porter.service.valid"
cp "$FIXTURE_ROOT/deploy/systemd/porter-migrate.service" "$WORK/porter-migrate.service.valid"

MISSING_MIGRATOR_ADMIN_ROOT="$WORK/missing-migrator-admin-root"
mkdir -p "$MISSING_MIGRATOR_ADMIN_ROOT"
sed -i '/^LoadCredential=porter-admin-token:\/etc\/porter\/porter-admin-token$/d' \
  "$FIXTURE_ROOT/deploy/systemd/porter-migrate.service"
expect_preflight_rejection missing-migrator-admin-credential "$MISSING_MIGRATOR_ADMIN_ROOT" \
  "$WORK/missing-migrator-admin.log" 'expected exactly one admin and one PostgreSQL LoadCredential'
cp "$WORK/porter-migrate.service.valid" "$FIXTURE_ROOT/deploy/systemd/porter-migrate.service"

MISSING_SERVER_UNIT_ROOT="$WORK/missing-server-unit-root"
mkdir -p "$MISSING_SERVER_UNIT_ROOT"
mv "$FIXTURE_ROOT/deploy/systemd/porter.service" "$WORK/porter.service"
expect_preflight_rejection missing-server-unit "$MISSING_SERVER_UNIT_ROOT" "$WORK/missing-server-unit.log" \
  'missing required server unit source: deploy/systemd/porter.service'
mv "$WORK/porter.service" "$FIXTURE_ROOT/deploy/systemd/porter.service"

# An unsafe installation target is rejected before package, account, config,
# or token writes, while leaving the pre-existing symlink untouched.
UNSAFE_PATH_ROOT="$WORK/unsafe-target-path-root"
mkdir -p "$UNSAFE_PATH_ROOT/etc" "$WORK/escaped-target"
ln -s "$WORK/escaped-target" "$UNSAFE_PATH_ROOT/etc/porter"
reset_logs
set +e
run_installer "$UNSAFE_PATH_ROOT" "" "$WORK/unsafe-target-path.log"
unsafe_path_status=$?
set -e
if [[ $unsafe_path_status -eq 0 ]] ||
   ! grep -Fq 'installation path must not be a symbolic link' "$WORK/unsafe-target-path.log" ||
   [[ ! -L "$UNSAFE_PATH_ROOT/etc/porter" ]] ||
   find "$WORK/escaped-target" -mindepth 1 -print -quit | grep -q .; then
  echo "installer did not safely reject a symlinked installation target" >&2
  cat "$WORK/unsafe-target-path.log" >&2
  exit 1
fi
assert_preflight_side_effects_absent "$WORK/unsafe-target-path.log"

cp "$WORK/porter.service.valid" "$FIXTURE_ROOT/deploy/systemd/porter.service"
sed -i '/^User=porter$/a User=other' "$FIXTURE_ROOT/deploy/systemd/porter.service"
expect_preflight_rejection duplicate-user "$WORK/duplicate-user-root" "$WORK/duplicate-user.log" \
  'duplicate or conflicting directive'

cp "$WORK/porter.service.valid" "$FIXTURE_ROOT/deploy/systemd/porter.service"
sed -i 's/^Type=simple$/Type=oneshot/' "$FIXTURE_ROOT/deploy/systemd/porter.service"
expect_preflight_rejection server-oneshot-type "$WORK/server-oneshot-root" "$WORK/server-oneshot.log" \
  'unit is missing exactly one required contract directive'

cp "$WORK/porter.service.valid" "$FIXTURE_ROOT/deploy/systemd/porter.service"
sed -i 's|EnvironmentFile=/etc/porter/porter.env|EnvironmentFile=/tmp/alternate-secret.env|' "$FIXTURE_ROOT/deploy/systemd/porter.service"
expect_preflight_rejection alternate-envfile "$WORK/alternate-env-root" "$WORK/alternate-env.log" \
  'unexpected EnvironmentFile fallback'

cp "$WORK/porter.service.valid" "$FIXTURE_ROOT/deploy/systemd/porter.service"
sed -i '/^User=porter$/a Environment=PORTER_ADMIN_TOKEN=fallback' "$FIXTURE_ROOT/deploy/systemd/porter.service"
expect_preflight_rejection environment-fallback "$WORK/environment-root" "$WORK/environment.log" \
  'Environment fallback is forbidden'

cp "$WORK/porter.service.valid" "$FIXTURE_ROOT/deploy/systemd/porter.service"
sed -i '/^LoadCredential=porter-admin-token:/a LoadCredentialEncrypted=porter-admin-token:/etc/porter/porter-admin-token' \
  "$FIXTURE_ROOT/deploy/systemd/porter.service"
expect_preflight_rejection encrypted-credential-fallback "$WORK/encrypted-credential-root" \
  "$WORK/encrypted-credential.log" 'unsupported alternate credential source'

cp "$WORK/porter.service.valid" "$FIXTURE_ROOT/deploy/systemd/porter.service"
sed -i '/^ExecStart=\/usr\/local\/bin\/porter server$/a ExecStart=/usr/local/bin/porter server --admin-token=fallback' "$FIXTURE_ROOT/deploy/systemd/porter.service"
expect_preflight_rejection argv-fallback "$WORK/argv-root" "$WORK/argv.log" \
  'duplicate or conflicting directive'

cp "$WORK/porter.service.valid" "$FIXTURE_ROOT/deploy/systemd/porter.service"
sed -i '/^ExecStart=\/usr\/local\/bin\/porter server$/d' "$FIXTURE_ROOT/deploy/systemd/porter.service"
sed -i '/^\[Install\]$/a ExecStart=/usr/local/bin/porter server' "$FIXTURE_ROOT/deploy/systemd/porter.service"
expect_preflight_rejection execstart-wrong-section "$WORK/wrong-section-root" "$WORK/wrong-section.log" \
  'service directive outside Service'

cp "$WORK/porter.service.valid" "$FIXTURE_ROOT/deploy/systemd/porter.service"
cp "$WORK/porter-migrate.service.valid" "$FIXTURE_ROOT/deploy/systemd/porter-migrate.service"

# An unsafe pre-existing config or system account also fails before prompting.
CONFIG_ROOT="$WORK/config-root"
mkdir -p "$CONFIG_ROOT/etc/porter"
printf '%s\n' 'CUSTOM_DSN=dsn-inherited-sentinel' > "$CONFIG_ROOT/etc/porter/porter.env"
chmod 0600 "$CONFIG_ROOT/etc/porter/porter.env"
expect_preflight_rejection credential-in-envfile "$CONFIG_ROOT" "$WORK/config.log" \
  'must not be environment assignments in porter.env' "$USER_MARKER" 1

BAD_ACCOUNT_ROOT="$WORK/bad-account-root"
touch "$BAD_USER_MARKER"
PORTER_INSTALL_TEST_BAD_ACCOUNT=1 expect_preflight_rejection unsafe-system-account "$BAD_ACCOUNT_ROOT" "$WORK/bad-account.log" \
  'existing porter account must be a non-root system account' "$BAD_USER_MARKER"
rm -f "$BAD_USER_MARKER"

VERSION_ROOT="$WORK/old-systemd-root"
mkdir -p "$VERSION_ROOT"
reset_logs
set +e
PORTER_INSTALL_TEST_SYSTEMD_VERSION=246 run_installer "$VERSION_ROOT" "" "$WORK/old-systemd.log"
version_status=$?
set -e
if [[ $version_status -eq 0 ]] || ! grep -Fq 'systemd 247 or newer is required' "$WORK/old-systemd.log"; then
  echo "installer did not reject an unsupported systemd version" >&2
  cat "$WORK/old-systemd.log" >&2
  exit 1
fi
assert_empty_target "$VERSION_ROOT"
assert_preflight_side_effects_absent "$WORK/old-systemd.log"

# A malformed or unconfirmed token must be rejected before package installation,
# PostgreSQL service changes, role/database work, or any persistent Porter writes.
for token_case in malformed mismatch; do
  TOKEN_ROOT="$WORK/token-$token_case-root"
  mkdir -p "$TOKEN_ROOT"
  reset_logs
  if [[ "$token_case" == malformed ]]; then
    token_input=$'short-token\nshort-token\n'
    expected_token_error='admin token must contain at least 32 URL-safe characters'
  else
    token_input=$(printf '%s\n%s\n' "$TOKEN" "${TOKEN}mismatch")
    expected_token_error='admin token confirmation did not match'
  fi
  set +e
  run_installer "$TOKEN_ROOT" "$token_input" "$WORK/token-$token_case.log"
  token_status=$?
  set -e
  if [[ $token_status -eq 0 ]] || ! grep -Fq "$expected_token_error" "$WORK/token-$token_case.log" ||
     [[ -s "$PACKAGE_LOG" || -s "$PG_LOG" || -s "$USERADD_LOG" ]] ||
     grep -Eq '^enable |^start |^restart ' "$SYSTEMCTL_LOG"; then
    echo "installer did not reject the $token_case admin token before local PostgreSQL changes" >&2
    cat "$WORK/token-$token_case.log" "$PACKAGE_LOG" "$PG_LOG" "$SYSTEMCTL_LOG" >&2
    exit 1
  fi
  assert_empty_target "$TOKEN_ROOT"
  if grep -Fq -- 'short-token' "$WORK/token-$token_case.log" "$SUDO_ARGS_LOG" "$PG_LOG" "$SYSTEMCTL_LOG" "$PACKAGE_LOG" ||
     [[ $(<"$WORK/token-$token_case.log") == *"$TOKEN"* ]]; then
    echo "admin token input appeared in output or privileged-command logs" >&2
    exit 1
  fi
done

BAD_ADMIN_ROOT="$WORK/bad-admin-credential-root"
mkdir -p "$BAD_ADMIN_ROOT/etc/porter"
printf '%s\n' "$TOKEN" > "$BAD_ADMIN_ROOT/etc/porter/porter-admin-token"
/usr/bin/sudo chown root:root "$BAD_ADMIN_ROOT/etc/porter/porter-admin-token"
/usr/bin/sudo chmod 0644 "$BAD_ADMIN_ROOT/etc/porter/porter-admin-token"
reset_logs
set +e
run_installer "$BAD_ADMIN_ROOT" "" "$WORK/bad-admin-credential.log"
bad_admin_status=$?
set -e
if [[ $bad_admin_status -eq 0 ]] ||
   ! grep -Fq 'existing admin credential must be root:root mode 0600' "$WORK/bad-admin-credential.log" ||
   [[ -s "$PACKAGE_LOG" || -s "$PG_LOG" ]] ||
   grep -Eq '^enable |^start |^restart ' "$SYSTEMCTL_LOG" ||
   grep -Eq 'Read the admin token|Admin token \(' "$WORK/bad-admin-credential.log"; then
  echo "installer did not fail closed on an unsafe existing admin credential before PostgreSQL changes" >&2
  cat "$WORK/bad-admin-credential.log" "$PACKAGE_LOG" "$PG_LOG" "$SYSTEMCTL_LOG" >&2
  exit 1
fi
assert_no_secret_leaks "$WORK/bad-admin-credential.log" "$SUDO_ARGS_LOG" "$PG_LOG" "$SYSTEMCTL_LOG" "$PACKAGE_LOG"

PG_NOT_READY_ROOT="$WORK/postgres-not-ready-root"
mkdir -p "$PG_NOT_READY_ROOT"
reset_logs
set +e
PORTER_INSTALL_TEST_PG_READY=0 run_installer "$PG_NOT_READY_ROOT" "$valid_input" "$WORK/postgres-not-ready.log"
pg_ready_status=$?
set -e
if [[ $pg_ready_status -eq 0 ]] || ! grep -Fq 'local PostgreSQL is not ready after starting the service' "$WORK/postgres-not-ready.log"; then
  echo "installer did not fail closed when local PostgreSQL was unavailable" >&2
  cat "$WORK/postgres-not-ready.log" >&2
  exit 1
fi
if ! grep -Fq 'Read the admin token twice from test-only stdin' "$WORK/postgres-not-ready.log"; then
  echo "installer did not collect the admin token before local PostgreSQL startup" >&2
  cat "$WORK/postgres-not-ready.log" >&2
  exit 1
fi
assert_no_porter_writes "$PG_NOT_READY_ROOT" "$WORK/postgres-not-ready.log"
if [[ $(wc -l < "$PG_LOG") -ne 30 || -s "$PACKAGE_LOG" ]] ||
   ! grep -Fq 'enable --now postgresql.service' "$SYSTEMCTL_LOG"; then
  echo "installer did not start PostgreSQL and poll readiness before stopping" >&2
  cat "$SYSTEMCTL_LOG" "$PG_LOG" "$PACKAGE_LOG" >&2
  exit 1
fi

for membership_case in member roleid; do
  PG_MEMBERSHIP_ROOT="$WORK/postgres-membership-$membership_case-root"
  mkdir -p "$PG_MEMBERSHIP_ROOT"
  reset_logs
  set +e
  PORTER_INSTALL_TEST_PG_MEMBERSHIP="$membership_case" \
    run_installer "$PG_MEMBERSHIP_ROOT" "$valid_input" "$WORK/postgres-membership-$membership_case.log"
  pg_membership_status=$?
  set -e
  if [[ $pg_membership_status -eq 0 ]] ||
     ! grep -Fq 'existing porter PostgreSQL role has memberships' "$WORK/postgres-membership-$membership_case.log"; then
    echo "installer did not reject the $membership_case side of PostgreSQL role membership" >&2
    cat "$WORK/postgres-membership-$membership_case.log" >&2
    exit 1
  fi
  assert_no_porter_writes "$PG_MEMBERSHIP_ROOT" "$WORK/postgres-membership-$membership_case.log"
  if ! grep -Fq 'm.roleid = r.oid' "$PG_LOG" || ! grep -Fq 'm.member = r.oid' "$PG_LOG" ||
     grep -Eq 'CREATE ROLE|ALTER ROLE|createdb|ALTER DATABASE|REVOKE|GRANT' "$PG_LOG"; then
    echo "installer changed PostgreSQL or omitted a membership direction for $membership_case" >&2
    cat "$PG_LOG" >&2
    exit 1
  fi
done

PG_UNSAFE_DB_ROOT="$WORK/postgres-unsafe-database-root"
mkdir -p "$PG_UNSAFE_DB_ROOT"
reset_logs
set +e
PORTER_INSTALL_TEST_PG_DATABASE=unsafe \
  run_installer "$PG_UNSAFE_DB_ROOT" "$valid_input" "$WORK/postgres-unsafe-database.log"
pg_database_status=$?
set -e
if [[ $pg_database_status -eq 0 ]] ||
   ! grep -Fq 'existing local porter database identity/owner is not validated' "$WORK/postgres-unsafe-database.log"; then
  echo "installer did not reject an existing porter database with an unvalidated owner" >&2
  cat "$WORK/postgres-unsafe-database.log" >&2
  exit 1
fi
assert_no_porter_writes "$PG_UNSAFE_DB_ROOT" "$WORK/postgres-unsafe-database.log"
if ! grep -Fq 'd.datdba' "$PG_LOG" ||
   grep -Eq 'CREATE ROLE|ALTER ROLE|createdb|ALTER DATABASE|REVOKE|GRANT' "$PG_LOG"; then
  echo "installer altered a database before validating its identity and owner" >&2
  cat "$PG_LOG" >&2
  exit 1
fi

# Valid synthetic units proceed only in the sandbox. Fake apt, systemd, and
# PostgreSQL commands model local setup; no host package, service, or DB is used.
TEST_ROOT="$WORK/valid-root"
mkdir -p "$TEST_ROOT"
reset_logs
set +e
PORTER_POSTGRES_DSN=dsn-inherited-sentinel \
PORTER_POSTGRES_API_DSN=dsn-inherited-sentinel \
DATABASE_URL=database-url-inherited-sentinel \
POSTGRES_DSN=dsn-inherited-sentinel \
CUSTOM_DSN=dsn-inherited-sentinel \
PGPASSWORD=pg-password-inherited-sentinel \
PGPASSFILE=/tmp/pg-password-inherited-sentinel \
PGTARGETSESSIONATTRS=primary \
PORTER_INSTALL_TEST_PG_READY_AFTER=3 \
  run_installer "$TEST_ROOT" "$valid_input" "$WORK/valid-install.log"
valid_status=$?
set -e
if [[ $valid_status -ne 0 ]]; then
  cat "$WORK/valid-install.log" >&2
  echo "valid isolated installer smoke did not complete" >&2
  exit 1
fi

ENV_FILE="$TEST_ROOT/etc/porter/porter.env"
ADMIN_FILE="$TEST_ROOT/etc/porter/porter-admin-token"
POSTGRES_FILE="$TEST_ROOT/etc/porter/porter-postgres-dsn"
if [[ $(stat -c '%u:%g:%a' "$ENV_FILE") != '0:0:600' ||
      $(stat -c '%u:%g:%a' "$ADMIN_FILE") != '0:0:600' ||
      $(stat -c '%u:%g:%a' "$POSTGRES_FILE") != '0:0:600' ]]; then
  echo "environment or raw credential files do not have root:root mode 0600" >&2
  exit 1
fi
if [[ $(/usr/bin/sudo cat "$ADMIN_FILE") != "$TOKEN" ]]; then
  echo "admin credential does not contain exactly the supplied token" >&2
  exit 1
fi
if [[ $(/usr/bin/sudo cat "$POSTGRES_FILE") != "$LOCAL_DSN" ]]; then
  echo "PostgreSQL credential does not contain the expected raw local DSN" >&2
  exit 1
fi
if [[ $(stat -c '%a' "$TEST_ROOT/var/lib/porter") != 750 ]]; then
  echo "Porter state directory mode is not 0750" >&2
  exit 1
fi
if [[ ! -x "$TEST_ROOT/usr/local/bin/porter" || ! -x "$TEST_ROOT/usr/local/bin/porter-migrate" ]]; then
  echo "installer did not stage both required binaries" >&2
  exit 1
fi
ENV_CONTENT=$(/usr/bin/sudo cat "$ENV_FILE")
if [[ "$ENV_CONTENT" == *PORTER_POSTGRES_DSN=* || "$ENV_CONTENT" == *PORTER_ADMIN_TOKEN=* ||
      "$ENV_CONTENT" != *'PORTER_ADDR=127.0.0.1:8080'* ]]; then
  echo "example environment file contains a credential assignment" >&2
  exit 1
fi
if ! grep -Fq 'CREATE ROLE porter LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS' "$PG_LOG" ||
   ! grep -Fq 'createdb --owner=porter porter' "$PG_LOG" ||
   ! grep -Fq 'ALTER DATABASE porter OWNER TO porter' "$PG_LOG" ||
   ! grep -Fq 'REVOKE CREATE ON SCHEMA public FROM PUBLIC' "$PG_LOG"; then
  echo "fake PostgreSQL log does not show least-privilege role/database provisioning" >&2
  cat "$PG_LOG" >&2
  exit 1
fi
if [[ -e "$TEST_ROOT/etc/systemd/system/porter.service" || -e "$TEST_ROOT/etc/systemd/system/porter-migrate.service" ]]; then
  echo "installer staged a Coder-owned unit unexpectedly" >&2
  exit 1
fi
if compgen -G "$TEST_ROOT/etc/porter/*.tmp.*" >/dev/null || compgen -G "$TEST_ROOT/etc/porter/.porter-postgres-dsn.*" >/dev/null; then
  echo "temporary credential file was left behind" >&2
  exit 1
fi
if [[ -s "$USERADD_LOG" ]] && ! grep -q -- '--system.*--user-group' "$USERADD_LOG"; then
  echo "Porter account was not created as a system account with a private group" >&2
  exit 1
fi
if [[ $(grep -c '^--version ' "$SYSTEMCTL_LOG") -ne 1 ||
      $(grep -c '^enable --now postgresql.service ' "$SYSTEMCTL_LOG") -ne 1 ||
      $(wc -l < "$SYSTEMCTL_LOG") -ne 2 ]]; then
  echo "installer attempted an unexpected systemd action beyond starting PostgreSQL" >&2
  cat "$SYSTEMCTL_LOG" >&2
  exit 1
fi
if [[ -s "$PACKAGE_LOG" ]]; then
  echo "installer installed packages even though all PostgreSQL commands were available" >&2
  exit 1
fi
if [[ $(<"$PORTER_INSTALL_TEST_PG_READY_COUNT") -ne 3 ]]; then
  echo "installer did not retry PostgreSQL readiness until the fake server was ready" >&2
  cat "$PG_LOG" >&2
  exit 1
fi
assert_event_before 'systemctl enable --now postgresql.service' 'pg_isready -q'
assert_event_before 'pg_isready -q' 'pg_auth_members'
assert_event_before 'pg_auth_members' 'd.datdba'
assert_event_before 'd.datdba' 'CREATE ROLE porter LOGIN'
assert_event_before 'CREATE ROLE porter LOGIN' 'createdb --owner=porter porter'
assert_no_secret_leaks "$WORK/valid-install.log" "$SUDO_ARGS_LOG" "$USERADD_LOG" "$PG_LOG" "$SYSTEMCTL_LOG" "$PACKAGE_LOG"
assert_no_postgres_env_in_sudo

TRACE_ROOT="$WORK/xtrace-root"
mkdir -p "$TRACE_ROOT"
reset_logs
set +e
PORTER_INSTALL_TEST_MODE=1 \
PORTER_INSTALL_TEST_ROOT="$TRACE_ROOT" \
PORTER_INSTALL_TEST_USER_MARKER="$USER_MARKER" \
PATH="$MOCK_BIN:$PATH" \
  bash -x "$FIXTURE_ROOT/scripts/install-porter.sh" "$FIXTURE_ROOT/bin/porter" \
  >"$WORK/xtrace-install.log" 2>&1 <<<"$valid_input"
xtrace_status=$?
set -e
if [[ $xtrace_status -ne 0 ]]; then
  echo "traced fake installer run failed" >&2
  cat "$WORK/xtrace-install.log" >&2
  exit 1
fi
assert_no_secret_leaks "$WORK/xtrace-install.log" "$SUDO_ARGS_LOG" "$USERADD_LOG" "$PG_LOG" "$SYSTEMCTL_LOG" "$PACKAGE_LOG"

# Existing databases are changed only when the fake catalog validates the
# porter name, porter owner, and non-template identity; do not recreate them.
OWNED_DB_ROOT="$WORK/postgres-owned-database-root"
mkdir -p "$OWNED_DB_ROOT"
reset_logs
set +e
PORTER_INSTALL_TEST_PG_DATABASE=owned \
  run_installer "$OWNED_DB_ROOT" "$valid_input" "$WORK/postgres-owned-database.log"
owned_db_status=$?
set -e
if [[ $owned_db_status -ne 0 ]] || grep -Fq 'createdb ' "$PG_LOG" ||
   ! grep -Fq 'd.datdba' "$PG_LOG" || ! grep -Fq "r.rolname = 'porter'" "$PG_LOG" ||
   ! grep -Fq 'ALTER DATABASE porter OWNER TO porter' "$PG_LOG"; then
  echo "installer did not safely reuse the owner-validated porter database" >&2
  cat "$WORK/postgres-owned-database.log" "$PG_LOG" >&2
  exit 1
fi

# Missing PostgreSQL commands trigger only the fake apt install, after all
# fail-fast checks and before the fake service/readiness/database sequence.
PKG_ROOT="$WORK/postgresql-package-root"
mkdir -p "$PKG_ROOT"
reset_logs
rm -f "$WORK/postgresql-installed"
set +e
PORTER_INSTALL_TEST_MISSING_COMMAND=psql \
PORTER_INSTALL_TEST_INSTALLED_MARKER="$WORK/postgresql-installed" \
  run_installer "$PKG_ROOT" "$valid_input" "$WORK/postgresql-package.log"
package_status=$?
set -e
if [[ $package_status -ne 0 || ! -f "$WORK/postgresql-installed" ||
      $(wc -l < "$PACKAGE_LOG") -ne 1 ]] ||
   ! grep -Fq 'apt-get install -y postgresql' "$PACKAGE_LOG"; then
  echo "installer did not install PostgreSQL through the fake apt-get when a required command was missing" >&2
  cat "$WORK/postgresql-package.log" "$PACKAGE_LOG" >&2
  exit 1
fi
assert_event_before 'apt-get install -y postgresql' 'systemctl enable --now postgresql.service'
assert_event_before 'systemctl enable --now postgresql.service' 'pg_isready -q'
assert_event_before 'pg_isready -q' 'CREATE ROLE porter LOGIN'
assert_no_secret_leaks "$WORK/postgresql-package.log" "$SUDO_ARGS_LOG" "$PG_LOG" "$SYSTEMCTL_LOG" "$PACKAGE_LOG"

# A supplied advanced external raw DSN must skip local packages, services, and
# role/database work. The fake credential is not connected to or disclosed.
EXTERNAL_ROOT="$WORK/external-dsn-root"
mkdir -p "$EXTERNAL_ROOT/etc/porter"
printf '%s\n' 'postgresql://porter:external-example@db.invalid/porter?sslmode=require' \
  > "$EXTERNAL_ROOT/etc/porter/porter-postgres-dsn"
chmod 0600 "$EXTERNAL_ROOT/etc/porter/porter-postgres-dsn"
reset_logs
set +e
run_installer "$EXTERNAL_ROOT" "$valid_input" "$WORK/external-dsn.log"
external_status=$?
set -e
if [[ $external_status -ne 0 || -s "$PACKAGE_LOG" || -s "$PG_LOG" ]] ||
   grep -Eq '^enable --now|^start |^restart ' "$SYSTEMCTL_LOG"; then
  echo "external DSN mode attempted local PostgreSQL provisioning" >&2
  cat "$WORK/external-dsn.log" "$PACKAGE_LOG" "$PG_LOG" "$SYSTEMCTL_LOG" >&2
  exit 1
fi
assert_no_secret_leaks "$WORK/external-dsn.log" "$SUDO_ARGS_LOG" "$PG_LOG" "$SYSTEMCTL_LOG" "$PACKAGE_LOG"

# A direct agent-installer probe proves its first privileged child receives no
# inherited PostgreSQL DSN/password variables; the fake sudo blocks the action.
reset_logs
set +e
env PORTER_INSTALL_TEST_AGENT_PROBE=1 \
  PORTER_POSTGRES_DSN=dsn-inherited-sentinel \
  PORTER_POSTGRES_API_DSN=dsn-inherited-sentinel \
  DATABASE_URL=database-url-inherited-sentinel \
  POSTGRES_DSN=dsn-inherited-sentinel \
  CUSTOM_DSN=dsn-inherited-sentinel \
  PGPASSWORD=pg-password-inherited-sentinel \
  PGPASSFILE=/tmp/pg-password-inherited-sentinel \
  PGTARGETSESSIONATTRS=primary \
  PATH="$MOCK_BIN:$PATH" \
  bash "$FIXTURE_ROOT/scripts/install-agent.sh" "$FIXTURE_ROOT/bin/porter-agent" \
  >"$WORK/agent-probe.log" 2>&1
agent_status=$?
set -e
if [[ $agent_status -ne 97 ]] || grep -Fq 'credential or PostgreSQL environment reached' "$WORK/agent-probe.log"; then
  echo "agent installer did not clear inherited PostgreSQL environment before sudo" >&2
  cat "$WORK/agent-probe.log" >&2
  exit 1
fi
assert_no_secret_leaks "$WORK/agent-probe.log" "$SUDO_ARGS_LOG"

# Agent install smoke uses a privileged-command fake only. A missing template
# is reported and skipped; a present one is installed once without overwriting.
AGENT_ENV_TEMPLATE="$FIXTURE_ROOT/deploy/porter-agent.env.example"
rm -f "$AGENT_ENV_TEMPLATE"
reset_logs
set +e
PORTER_INSTALL_TEST_AGENT_INSTALL_PROBE=1 \
PORTER_INSTALL_TEST_AGENT_CONFIG_SYMLINK=1 \
  PATH="$MOCK_BIN:$PATH" bash "$FIXTURE_ROOT/scripts/install-agent.sh" "$FIXTURE_ROOT/bin/porter-agent" \
  >"$WORK/agent-install-symlink-dir.log" 2>&1
agent_symlink_dir_status=$?
set -e
if [[ $agent_symlink_dir_status -eq 0 ]] ||
   ! grep -Fq 'agent configuration directory must not be a symbolic link' "$WORK/agent-install-symlink-dir.log" ||
   [[ -s "$AGENT_INSTALL_LOG" ]]; then
  echo "agent installer did not reject a symlinked /etc/porter before writes" >&2
  cat "$WORK/agent-install-symlink-dir.log" "$AGENT_INSTALL_LOG" >&2
  exit 1
fi

reset_logs
set +e
PORTER_INSTALL_TEST_AGENT_INSTALL_PROBE=1 \
  PATH="$MOCK_BIN:$PATH" bash "$FIXTURE_ROOT/scripts/install-agent.sh" "$FIXTURE_ROOT/bin/porter-agent" \
  >"$WORK/agent-install-no-template.log" 2>&1
agent_missing_template_status=$?
set -e
if [[ $agent_missing_template_status -ne 0 ]] ||
   ! grep -Fq 'agent environment template is absent; no agent environment file was installed' "$WORK/agent-install-no-template.log" ||
   ! grep -Fq '/etc/porter' "$AGENT_INSTALL_LOG" ||
   grep -Fq '/etc/porter/porter-agent.env' "$AGENT_INSTALL_LOG" ||
   grep -Fq '/var/lib/porter-agent' "$AGENT_INSTALL_LOG"; then
  echo "agent installer did not safely skip an absent environment template" >&2
  cat "$WORK/agent-install-no-template.log" "$AGENT_INSTALL_LOG" >&2
  exit 1
fi
if ! grep -Fq -- 'sudo porter-agent join --server https://<control-plane> --state-dir /var/lib/porter-agent' "$WORK/agent-install-no-template.log" ||
   ! grep -Fq '/dev/tty with echo disabled by default' "$WORK/agent-install-no-template.log" ||
   ! grep -Fq 'append --token-stdin' "$WORK/agent-install-no-template.log" ||
   grep -Eq '(^|[[:space:]])--token([=[:space:]]|$)|--data-dir|--hostname' "$WORK/agent-install-no-template.log" ||
   grep -Fq -- "$TOKEN" "$WORK/agent-install-no-template.log" ||
   grep -Eiq '(kernel|rootfs).*(installed|provisioned)' "$WORK/agent-install-no-template.log" ||
   grep -Fq '/var/lib/porter-agent' "$AGENT_INSTALL_LOG" ||
   grep -Eq '^enable |^start |^restart ' "$SYSTEMCTL_LOG"; then
  echo "agent installer printed an unsafe or activated join command" >&2
  cat "$WORK/agent-install-no-template.log" "$SYSTEMCTL_LOG" >&2
  exit 1
fi

cp -- "$ROOT/deploy/porter-agent.env.example" "$AGENT_ENV_TEMPLATE"
reset_logs
set +e
PORTER_INSTALL_TEST_AGENT_INSTALL_PROBE=1 \
  PATH="$MOCK_BIN:$PATH" bash "$FIXTURE_ROOT/scripts/install-agent.sh" "$FIXTURE_ROOT/bin/porter-agent" \
  >"$WORK/agent-install-template.log" 2>&1
agent_template_status=$?
set -e
if [[ $agent_template_status -ne 0 ]] ||
   ! grep -Fq -- "$AGENT_ENV_TEMPLATE /etc/porter/porter-agent.env" "$AGENT_INSTALL_LOG"; then
  echo "agent installer did not stage the present env template through the fake privileged helper" >&2
  cat "$WORK/agent-install-template.log" "$AGENT_INSTALL_LOG" >&2
  exit 1
fi

for agent_env_case in symlink nonregular mode owner; do
  reset_logs
  agent_env_override=()
  case "$agent_env_case" in
    symlink) agent_env_override=(PORTER_INSTALL_TEST_AGENT_ENV_SYMLINK=1); expected_agent_env_error='agent environment destination must not be a symbolic link' ;;
    nonregular) agent_env_override=(PORTER_INSTALL_TEST_AGENT_ENV_REGULAR=0); expected_agent_env_error='agent environment destination must be a regular file' ;;
    mode) agent_env_override=(PORTER_INSTALL_TEST_AGENT_ENV_META=0:0:644); expected_agent_env_error='must be root:root mode 0600' ;;
    owner) agent_env_override=(PORTER_INSTALL_TEST_AGENT_ENV_META=1000:0:600); expected_agent_env_error='must be root:root mode 0600' ;;
  esac
  set +e
  env "${agent_env_override[@]}" PORTER_INSTALL_TEST_AGENT_INSTALL_PROBE=1 \
    PORTER_INSTALL_TEST_AGENT_ENV_EXISTS=1 PATH="$MOCK_BIN:$PATH" \
    bash "$FIXTURE_ROOT/scripts/install-agent.sh" "$FIXTURE_ROOT/bin/porter-agent" \
    >"$WORK/agent-env-$agent_env_case.log" 2>&1
  agent_env_status=$?
  set -e
  if [[ $agent_env_status -eq 0 ]] ||
     ! grep -Fq "$expected_agent_env_error" "$WORK/agent-env-$agent_env_case.log" ||
     [[ -s "$AGENT_INSTALL_LOG" || -s "$SYSTEMCTL_LOG" ]]; then
    echo "agent installer did not fail safely for an existing $agent_env_case environment file" >&2
    cat "$WORK/agent-env-$agent_env_case.log" "$AGENT_INSTALL_LOG" "$SYSTEMCTL_LOG" >&2
    exit 1
  fi
done

reset_logs
set +e
PORTER_INSTALL_TEST_AGENT_INSTALL_PROBE=1 \
PORTER_INSTALL_TEST_AGENT_ENV_EXISTS=1 \
  PATH="$MOCK_BIN:$PATH" bash "$FIXTURE_ROOT/scripts/install-agent.sh" "$FIXTURE_ROOT/bin/porter-agent" \
  >"$WORK/agent-install-existing-env.log" 2>&1
agent_existing_env_status=$?
set -e
if [[ $agent_existing_env_status -ne 0 ]] ||
   ! grep -Fq 'Existing agent environment file left unchanged' "$WORK/agent-install-existing-env.log" ||
   grep -Fq -- "$AGENT_ENV_TEMPLATE /etc/porter/porter-agent.env" "$AGENT_INSTALL_LOG"; then
  echo "agent installer overwrote an existing environment file" >&2
  cat "$WORK/agent-install-existing-env.log" "$AGENT_INSTALL_LOG" >&2
  exit 1
fi
rm -f "$AGENT_ENV_TEMPLATE"

# Source assertions guard secret cleanup and fail-closed behavior. The parser
# checks above use only synthetic unit fixtures and provide no real-unit evidence.
if ! grep -Fq 'trap cleanup_secret_memory EXIT' "$ROOT/scripts/install-porter.sh" ||
   [[ $(grep -Fc 'trap cleanup EXIT' "$ROOT/scripts/install-porter.sh") -lt 2 ]] ||
   ! grep -Fq "trap 'exit 130' INT" "$ROOT/scripts/install-porter.sh" ||
   ! grep -Fq 'NOBYPASSRLS' "$ROOT/scripts/install-porter.sh" ||
   ! grep -Fq 'Environment fallback is forbidden' "$ROOT/scripts/install-porter.sh" ||
   ! grep -Fq 'service directive outside Service' "$ROOT/scripts/install-porter.sh"; then
  echo "installer is missing secret cleanup or strict credential safeguards" >&2
  exit 1
fi
if ! grep -Fq 'No PostgreSQL migration or Porter service activation was attempted.' "$ROOT/scripts/install-porter.sh" ||
   ! grep -Fq 'real-host systemd acceptance verifies credential delivery, migration ordering, and fail-closed behavior' "$ROOT/scripts/install-porter.sh"; then
  echo "installer does not keep production activation fail-closed pending real-host acceptance" >&2
  exit 1
fi
if ! grep -Fq 'Production requires API TLS' "$ROOT/deploy/porter.env.example" ||
   ! grep -Fq 'Loopback HTTP and development mode are for' "$ROOT/deploy/porter.env.example" ||
   ! grep -Fq 'Only non-secret settings belong in this file' "$ROOT/deploy/porter.env.example" ||
   ! grep -Fq 'systemd LoadCredential' "$ROOT/deploy/porter.env.example" ||
   ! grep -Fq '# PORTER_API_TLS_CERT=/etc/porter/tls/server.crt' "$ROOT/deploy/porter.env.example" ||
   ! grep -Fq '# PORTER_API_TLS_KEY=/etc/porter/tls/server.key' "$ROOT/deploy/porter.env.example" ||
   ! grep -Fq '# PORTER_API_MODE=development' "$ROOT/deploy/porter.env.example" ||
   grep -Eq '^[[:space:]]*(export[[:space:]]+)?(PORTER_DB|PORTER_ADMIN_TOKEN)[[:space:]]*=' "$ROOT/deploy/porter.env.example"; then
  echo "example config has unsafe secret placeholders or is missing the reviewed TLS/dev comments" >&2
  exit 1
fi
if ! grep -Fq 'PORTER_AGENT_STATE_DIR=/var/lib/porter-agent' "$ROOT/deploy/porter-agent.env.example" ||
   ! grep -Fq 'PORTER_AGENT_KERNEL=/var/lib/porter-agent/vmlinux' "$ROOT/deploy/porter-agent.env.example" ||
   ! grep -Fq 'PORTER_AGENT_ROOTFS=/var/lib/porter-agent/rootfs.ext4' "$ROOT/deploy/porter-agent.env.example" ||
   ! grep -Fq '# PORTER_AGENT_SERVER_CA=/etc/porter/tls/server-ca.pem' "$ROOT/deploy/porter-agent.env.example" ||
   ! grep -Fq '# PORTER_AGENT_CLIENT_CERT=/etc/porter/tls/agent.crt' "$ROOT/deploy/porter-agent.env.example" ||
   ! grep -Fq '# PORTER_AGENT_CLIENT_KEY=/etc/porter/tls/agent.key' "$ROOT/deploy/porter-agent.env.example" ||
   ! grep -Fq 'deploy/systemd/porter-agent.service loads this file as its EnvironmentFile' "$ROOT/deploy/porter-agent.env.example" ||
   ! grep -Fq 'root:root mode 0600' "$ROOT/deploy/porter-agent.env.example" ||
   grep -Eq '^[[:space:]]*(export[[:space:]]+)?PORTER_AGENT_(SERVER_CA|CLIENT_CERT|CLIENT_KEY)[[:space:]]*=' "$ROOT/deploy/porter-agent.env.example" ||
   ! grep -Fqx 'EnvironmentFile=-/etc/porter/porter-agent.env' "$ROOT/deploy/systemd/porter-agent.service"; then
  echo "agent environment example, systemd unit, and installer safety contract do not match" >&2
  exit 1
fi

printf '%s\n' "install smoke passed: admin-token validation before fake local PostgreSQL changes; synthetic unit fixtures only (not real Coder unit evidence); fake apt/systemd/PostgreSQL ordering and readiness retries; trace/secret-leak checks; external-DSN bypass; agent env-file type/owner/mode gating and non-overwrite; safe join guidance; no real DB/package/systemd operation, Porter activation, or migration"
