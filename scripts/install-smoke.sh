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
LOCAL_DSN='host=/var/run/postgresql user=porter dbname=porter sslmode=disable'
USER_MARKER="$WORK/porter-user-created"
BAD_USER_MARKER="$WORK/bad-porter-user-created"
SUDO_ARGS_LOG="$WORK/sudo-args.log"
USERADD_LOG="$WORK/useradd.log"
SYSTEMCTL_LOG="$WORK/systemctl.log"
PG_LOG="$WORK/postgres.log"
PACKAGE_LOG="$WORK/package.log"
: > "$SUDO_ARGS_LOG"
: > "$USERADD_LOG"
: > "$SYSTEMCTL_LOG"
: > "$PG_LOG"
: > "$PACKAGE_LOG"

cp -- "$ROOT/scripts/install-porter.sh" "$FIXTURE_ROOT/scripts/install-porter.sh"
cp -- "$ROOT/scripts/install-agent.sh" "$FIXTURE_ROOT/scripts/install-agent.sh"
cp -- "$ROOT/deploy/porter.env.example" "$FIXTURE_ROOT/deploy/porter.env.example"
cp -- "$ROOT/deploy/systemd/porter-agent.service" "$FIXTURE_ROOT/deploy/systemd/porter-agent.service"
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
if [[ ${1:-} == --version ]]; then
  printf 'systemd %s (smoke fake)\n' "${PORTER_INSTALL_TEST_SYSTEMD_VERSION:-247}"
  exit 0
fi
echo "blocked fake systemd action in installer smoke" >&2
exit 97
SYSTEMCTL
chmod 0755 "$MOCK_BIN/systemctl"

cat > "$MOCK_BIN/pg_isready" <<'PGREADY'
#!/usr/bin/env bash
set -euo pipefail
printf 'pg_isready ' >> "$PORTER_INSTALL_TEST_PG_LOG"
printf '%s ' "$@" >> "$PORTER_INSTALL_TEST_PG_LOG"
printf '\n' >> "$PORTER_INSTALL_TEST_PG_LOG"
[[ ${PORTER_INSTALL_TEST_PG_READY:-1} == 1 ]]
PGREADY
chmod 0755 "$MOCK_BIN/pg_isready"

cat > "$MOCK_BIN/psql" <<'PSQL'
#!/usr/bin/env bash
set -euo pipefail
printf 'psql ' >> "$PORTER_INSTALL_TEST_PG_LOG"
printf '%s ' "$@" >> "$PORTER_INSTALL_TEST_PG_LOG"
printf '\n' >> "$PORTER_INSTALL_TEST_PG_LOG"
all_args="$*"
if [[ "$all_args" == *pg_auth_members* ]]; then
  printf '%s\n' "${PORTER_INSTALL_TEST_PG_MEMBERSHIP:-safe}"
elif [[ "$all_args" == *pg_database* ]]; then
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
CREATEDB
chmod 0755 "$MOCK_BIN/createdb"

for package_manager in apt-get apt dnf yum zypper; do
  cat > "$MOCK_BIN/$package_manager" <<'PACKAGE'
#!/usr/bin/env bash
set -euo pipefail
printf '%s ' "$(basename "$0")" >> "$PORTER_INSTALL_TEST_PACKAGE_LOG"
printf '%q ' "$@" >> "$PORTER_INSTALL_TEST_PACKAGE_LOG"
printf '\n' >> "$PORTER_INSTALL_TEST_PACKAGE_LOG"
echo "blocked package operation in installer smoke" >&2
exit 98
PACKAGE
  chmod 0755 "$MOCK_BIN/$package_manager"
done

cat > "$MOCK_BIN/sudo" <<'SUDO'
#!/usr/bin/env bash
set -euo pipefail
printf '%q ' "$@" >> "$PORTER_INSTALL_TEST_SUDO_ARGS_LOG"
printf '\n' >> "$PORTER_INSTALL_TEST_SUDO_ARGS_LOG"
if [[ -n ${PORTER_ADMIN_TOKEN:-} || -n ${PORTER_POSTGRES_DSN:-} ||
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
}
run_installer() {
  local target_root=$1 input=$2 output=$3 marker=${4:-$USER_MARKER}
  PORTER_INSTALL_TEST_MODE=1 \
  PORTER_INSTALL_TEST_ROOT="$target_root" \
  PORTER_INSTALL_TEST_USER_MARKER="$marker" \
  PATH="$MOCK_BIN:$PATH" \
    bash "$FIXTURE_ROOT/scripts/install-porter.sh" "$FIXTURE_ROOT/bin/porter" >"$output" 2>&1 <<<"$input"
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

# A missing migration executable or unit must fail before prompts, files,
# account creation, or PostgreSQL calls.
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

cp "$WORK/porter.service.valid" "$FIXTURE_ROOT/deploy/systemd/porter.service"
sed -i '/^User=porter$/a User=other' "$FIXTURE_ROOT/deploy/systemd/porter.service"
expect_preflight_rejection duplicate-user "$WORK/duplicate-user-root" "$WORK/duplicate-user.log" \
  'duplicate or conflicting directive'

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

PG_NOT_READY_ROOT="$WORK/postgres-not-ready-root"
mkdir -p "$PG_NOT_READY_ROOT"
reset_logs
set +e
PORTER_INSTALL_TEST_PG_READY=0 run_installer "$PG_NOT_READY_ROOT" "" "$WORK/postgres-not-ready.log"
pg_ready_status=$?
set -e
if [[ $pg_ready_status -eq 0 ]] || ! grep -Fq 'local PostgreSQL is not ready' "$WORK/postgres-not-ready.log"; then
  echo "installer did not fail closed when local PostgreSQL was unavailable" >&2
  cat "$WORK/postgres-not-ready.log" >&2
  exit 1
fi
assert_empty_target "$PG_NOT_READY_ROOT"
assert_preflight_side_effects_absent "$WORK/postgres-not-ready.log"

PG_MEMBERSHIP_ROOT="$WORK/postgres-membership-root"
mkdir -p "$PG_MEMBERSHIP_ROOT"
reset_logs
set +e
PORTER_INSTALL_TEST_PG_MEMBERSHIP=unsafe run_installer "$PG_MEMBERSHIP_ROOT" "" "$WORK/postgres-membership.log"
pg_membership_status=$?
set -e
if [[ $pg_membership_status -eq 0 ]] || ! grep -Fq 'existing porter PostgreSQL role has memberships' "$WORK/postgres-membership.log"; then
  echo "installer did not reject an unsafe PostgreSQL role membership" >&2
  cat "$WORK/postgres-membership.log" >&2
  exit 1
fi
assert_empty_target "$PG_MEMBERSHIP_ROOT"
assert_preflight_side_effects_absent "$WORK/postgres-membership.log"

# Valid synthetic units proceed only in the sandbox. The fake PostgreSQL
# commands model role/database setup; fake systemd permits --version only.
TEST_ROOT="$WORK/valid-root"
mkdir -p "$TEST_ROOT"
reset_logs
valid_input=$(printf '%s\n%s\n' "$TOKEN" "$TOKEN")
set +e
PORTER_POSTGRES_DSN=dsn-inherited-sentinel \
PORTER_POSTGRES_API_DSN=dsn-inherited-sentinel \
DATABASE_URL=database-url-inherited-sentinel \
POSTGRES_DSN=dsn-inherited-sentinel \
CUSTOM_DSN=dsn-inherited-sentinel \
PGPASSWORD=pg-password-inherited-sentinel \
PGPASSFILE=/tmp/pg-password-inherited-sentinel \
PGTARGETSESSIONATTRS=primary \
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
if [[ "$ENV_CONTENT" == *PORTER_POSTGRES_DSN=* || "$ENV_CONTENT" == *PORTER_ADMIN_TOKEN=* ]]; then
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
if grep -Eq 'daemon-reload|enable|start|restart|stop' "$SYSTEMCTL_LOG"; then
  echo "installer attempted systemd activation or reload" >&2
  cat "$SYSTEMCTL_LOG" >&2
  exit 1
fi
if grep -Eq '(^|[[:space:]])(apt-get|apt|dnf|yum|zypper)([[:space:]]|$)' "$SUDO_ARGS_LOG" || [[ -s "$PACKAGE_LOG" ]]; then
  echo "installer attempted package operations" >&2
  exit 1
fi
assert_no_secret_leaks "$WORK/valid-install.log" "$SUDO_ARGS_LOG" "$USERADD_LOG" "$PG_LOG" "$SYSTEMCTL_LOG" "$PACKAGE_LOG"
assert_no_postgres_env_in_sudo

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
   ! grep -Fq 'pending Coder-owned credential readers' "$ROOT/scripts/install-porter.sh"; then
  echo "installer does not keep production activation fail-closed for Coder-owned blockers" >&2
  exit 1
fi
if ! grep -Fq 'Never add them here or pass them in argv' "$ROOT/deploy/porter.env.example"; then
  echo "example config does not clearly prohibit credential env/argv fallbacks" >&2
  exit 1
fi

printf '%s\n' "install smoke passed: canonical temporary root; fail-fast preflight with no prompt/write/DB mutation/package/user/systemd action; synthetic unit fixtures only (not real Coder unit evidence); inherited DSN cleanup; root:root 0600 credentials; temp cleanup; fake least-privilege PostgreSQL setup; migrator binary staging; no real DB/package/systemd operation or activation"
