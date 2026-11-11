#!/usr/bin/env bash
set -euo pipefail
# Never allow caller-requested shell tracing to expose secrets read below.
set +x
unset PORTER_ADMIN_TOKEN PORTER_POSTGRES_DSN PORTER_POSTGRES_API_DSN \
  DATABASE_URL DATABASE_DSN PORTER_DATABASE_URL POSTGRES_URL POSTGRES_DSN \
  PGHOST PGHOSTADDR PGPORT PGDATABASE PGUSER PGPASSWORD PGPASSFILE \
  PGSSLMODE PGREQUIRESSL PGCHANNELBINDING PGOPTIONS PGAPPNAME \
  PGCONNECT_TIMEOUT PGSSLCERT PGSSLKEY PGSSLROOTCERT PGSSLCRL PGSSLCRLDIR \
  PGSERVICE PGSERVICEFILE PGLOADBALANCEHOSTS
while IFS= read -r inherited_db_var; do
  case "$inherited_db_var" in
    PG*|*DSN) unset "$inherited_db_var" ;;
  esac
done < <(compgen -e)

cleanup_secret_memory() {
  unset ADMIN_TOKEN confirm PORTER_ADMIN_TOKEN PORTER_POSTGRES_DSN PORTER_POSTGRES_API_DSN \
    DATABASE_URL DATABASE_DSN PORTER_DATABASE_URL POSTGRES_URL POSTGRES_DSN \
    PGPASSWORD PGPASSFILE
}
trap cleanup_secret_memory EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd -- "$SCRIPT_DIR/.." && pwd)
BIN=${1:-"$REPO_ROOT/bin/porter"}
MIGRATOR_BIN="$REPO_ROOT/bin/porter-migrate"
MIGRATOR_UNIT="$REPO_ROOT/deploy/systemd/porter-migrate.service"
PORTER_UNIT="$REPO_ROOT/deploy/systemd/porter.service"

if [[ ! -f "$BIN" || ! -x "$BIN" || -L "$BIN" ]]; then
  echo "missing required executable Porter binary: $BIN" >&2
  echo "build it first with: make build" >&2
  exit 1
fi
if [[ ! -f "$MIGRATOR_BIN" || ! -x "$MIGRATOR_BIN" || -L "$MIGRATOR_BIN" ]]; then
  echo "missing required executable PostgreSQL migration binary: $MIGRATOR_BIN" >&2
  echo "the migration binary must be built and placed at bin/porter-migrate before install" >&2
  exit 1
fi

# Test roots are restricted to an existing, canonical child of the temporary
# directory, owned by the caller and not writable by other users.
INSTALL_ROOT=""
if [[ ${PORTER_INSTALL_TEST_MODE:-0} == 1 ]]; then
  requested_root=${PORTER_INSTALL_TEST_ROOT:?PORTER_INSTALL_TEST_ROOT is required in test mode}
  if [[ "$requested_root" != /* || "$requested_root" == "/" || ! -d "$requested_root" ]]; then
    echo "PORTER_INSTALL_TEST_ROOT must be an existing non-root absolute temporary directory" >&2
    exit 1
  fi
  temp_base=$(realpath -e -- "${TMPDIR:-/tmp}") || {
    echo "the temporary directory must exist in test mode" >&2
    exit 1
  }
  canonical_root=$(realpath -e -- "$requested_root") || {
    echo "PORTER_INSTALL_TEST_ROOT must resolve to an existing canonical directory" >&2
    exit 1
  }
  if [[ "$requested_root" != "$canonical_root" || "$canonical_root" == "$temp_base" || "$canonical_root" != "$temp_base/"* ]]; then
    echo "PORTER_INSTALL_TEST_ROOT must be a canonical child of the temporary directory" >&2
    exit 1
  fi
  root_owner=$(stat -c '%u' -- "$canonical_root")
  if [[ "$root_owner" != "$(id -u)" ]]; then
    echo "PORTER_INSTALL_TEST_ROOT must be owned by the invoking user" >&2
    exit 1
  fi
  root_mode=$(stat -c '%a' -- "$canonical_root")
  if [[ ! "$root_mode" =~ ^[0-7]{3,4}$ ]] || (( (8#$root_mode & 0022) != 0 )); then
    echo "PORTER_INSTALL_TEST_ROOT must not be group- or world-writable" >&2
    exit 1
  fi
  INSTALL_ROOT=$canonical_root
elif [[ -n ${PORTER_INSTALL_TEST_ROOT:-} ]]; then
  echo "PORTER_INSTALL_TEST_ROOT is only valid with PORTER_INSTALL_TEST_MODE=1" >&2
  exit 1
fi

# Clear inherited PostgreSQL connection variables before any privileged child
# command. The application DSN is supplied only through the credential file.
for forbidden_pg_var in PORTER_POSTGRES_DSN PORTER_POSTGRES_API_DSN DATABASE_URL DATABASE_DSN \
  PORTER_DATABASE_URL POSTGRES_URL POSTGRES_DSN PGPASSWORD PGPASSFILE; do
  if [[ -v $forbidden_pg_var ]]; then
    echo "inherited PostgreSQL DSN or password environment was not cleared" >&2
    exit 1
  fi
done

target_path() {
  printf '%s%s' "$INSTALL_ROOT" "$1"
}

PORTER_DIR=$(target_path /etc/porter)
ENV_FILE=$(target_path /etc/porter/porter.env)
ADMIN_CREDENTIAL=$(target_path /etc/porter/porter-admin-token)
POSTGRES_CREDENTIAL=$(target_path /etc/porter/porter-postgres-dsn)
STATE_DIR=$(target_path /var/lib/porter)
SYSTEMD_DIR=$(target_path /etc/systemd/system)
LOCAL_BIN_DIR=$(target_path /usr/local/bin)
LOCAL_POSTGRES_DSN='host=/var/run/postgresql user=porter dbname=porter sslmode=disable'

# Unit sources are owned by Coder. Require the credential contract before any
# host writes or secret prompts; this validates files but never installs them.
if [[ ! -f "$MIGRATOR_UNIT" || -L "$MIGRATOR_UNIT" ]]; then
  echo "missing required migration unit source: deploy/systemd/porter-migrate.service; refusing migration or activation" >&2
  exit 1
fi
if [[ ! -f "$PORTER_UNIT" || -L "$PORTER_UNIT" ]]; then
  echo "missing required server unit source: deploy/systemd/porter.service; refusing activation" >&2
  exit 1
fi

validate_unit() {
  local unit_file=$1 unit_kind=$2 required_line
  if ! awk -v kind="$unit_kind" '
    function fail(message) {
      printf "%s unit contract rejected at line %d: %s\n", kind, NR, message > "/dev/stderr"
      invalid = 1
    }
    {
      line = $0
      sub(/^[[:space:]]*/, "", line)
      sub(/[[:space:]]*$/, "", line)
      if (line == "" || line ~ /^[#;]/) next
      if (line ~ /\\$/) {
        fail("line continuation is not allowed")
        next
      }
      if (line ~ /^\[[^][]+\]$/) {
        section = substr(line, 2, length(line) - 2)
        if (sections[section]++) fail("duplicate section")
        next
      }
      if (section == "") {
        fail("directive outside a section")
        next
      }
      equal = index(line, "=")
      if (equal < 2) {
        fail("malformed directive")
        next
      }
      key = substr(line, 1, equal - 1)
      value = substr(line, equal + 1)
      sub(/[[:space:]]+$/, "", key)
      sub(/^[[:space:]]+/, "", value)
      if (key !~ /^[A-Za-z][A-Za-z0-9]*$/) {
        fail("malformed directive name")
        next
      }
      if ((key == "Type" || key == "User" || key == "Group" || key == "Environment" ||
           key == "EnvironmentFile" || key == "ExecStart") && section != "Service")
        fail("service directive outside Service")
      if (key ~ /^LoadCredential/ && key != "LoadCredential")
        fail("unsupported alternate credential source")
      if (key == "LoadCredential") {
        if (section != "Service") fail("LoadCredential must be in Service")
        if (kind == "server") {
          if (value == "porter-admin-token:/etc/porter/porter-admin-token") admin_credential++
          else if (value == "porter-postgres-dsn:/etc/porter/porter-postgres-dsn") postgres_credential++
          else fail("unexpected or fallback credential source")
        } else {
          if (value == "porter-postgres-dsn:/etc/porter/porter-postgres-dsn") postgres_credential++
          else fail("unexpected or fallback credential source")
        }
      } else if (directives[section SUBSEP key]++) {
        fail("duplicate or conflicting directive")
      }
      if (key == "Environment")
        fail("Environment fallback is forbidden")
      if (key == "EnvironmentFile") {
        envfile_count++
        if (section != "Service") fail("EnvironmentFile must be in Service")
        if (kind != "server" || value != "/etc/porter/porter.env")
          fail("unexpected EnvironmentFile fallback")
      }
      if (key == "PassEnvironment" || key == "UnsetEnvironment" || key == "ImportEnvironment" ||
          key == "SetCredential" || key == "SetCredentialEncrypted" ||
          key == "ImportCredential")
        fail("unsupported environment or credential fallback")
      if (key ~ /^Exec/ && key != "ExecStart")
        fail("additional command fallback is forbidden")
      if (key == "ExecStart") exec_count++
    }
    END {
      if (!sections["Service"]) fail("missing Service section")
      if (exec_count != 1) fail("expected exactly one ExecStart")
      if (kind == "server") {
        if (admin_credential != 1 || postgres_credential != 1)
          fail("expected exactly one admin and one PostgreSQL LoadCredential")
        if (envfile_count != 1) fail("expected exactly one approved EnvironmentFile")
      } else {
        if (postgres_credential != 1) fail("expected exactly one PostgreSQL LoadCredential")
        if (admin_credential != 0 || envfile_count != 0)
          fail("unexpected credential or EnvironmentFile fallback")
      }
      if (invalid) exit 1
    }
  ' "$unit_file"; then
    echo "unit is not compatible with the reviewed credential contract: $unit_file" >&2
    return 1
  fi

  local -a required_lines
  if [[ "$unit_kind" == server ]]; then
    required_lines=(
      'Type=simple'
      'User=porter'
      'Group=porter'
      'EnvironmentFile=/etc/porter/porter.env'
      'LoadCredential=porter-admin-token:/etc/porter/porter-admin-token'
      'LoadCredential=porter-postgres-dsn:/etc/porter/porter-postgres-dsn'
      'ExecStart=/usr/local/bin/porter server'
    )
  else
    required_lines=(
      'Type=oneshot'
      'User=porter'
      'Group=porter'
      'LoadCredential=porter-postgres-dsn:/etc/porter/porter-postgres-dsn'
      'ExecStart=/usr/local/bin/porter-migrate apply'
    )
  fi
  for required_line in "${required_lines[@]}"; do
    if [[ $(grep -Fxc -- "$required_line" "$unit_file") -ne 1 ]]; then
      echo "unit is missing exactly one required contract directive; refusing migration or activation: $unit_file" >&2
      return 1
    fi
  done
}
validate_unit "$MIGRATOR_UNIT" migrator
validate_unit "$PORTER_UNIT" server

SYSTEMD_VERSION=$(systemctl --version 2>/dev/null | awk 'NR == 1 { print $2 }' || true)
if [[ ! "$SYSTEMD_VERSION" =~ ^[0-9]+$ ]] || (( SYSTEMD_VERSION < 247 )); then
  echo "systemd 247 or newer is required for LoadCredential; refusing activation without a fallback" >&2
  exit 1
fi

preflight_target_path() {
  local path=$1 expected_type=$2 resolved
  if sudo test -L "$path"; then
    echo "installation path must not be a symbolic link: $path" >&2
    return 1
  fi
  if sudo test -e "$path"; then
    if [[ "$expected_type" == directory ]] && ! sudo test -d "$path"; then
      echo "installation path must be a directory: $path" >&2
      return 1
    fi
    if [[ "$expected_type" == file ]] && ! sudo test -f "$path"; then
      echo "installation path must be a regular file: $path" >&2
      return 1
    fi
  fi
  resolved=$(sudo realpath -m -- "$path") || {
    echo "could not safely resolve installation path: $path" >&2
    return 1
  }
  if [[ "$resolved" != "$path" ]]; then
    echo "installation path has a symbolic or non-canonical parent: $path" >&2
    return 1
  fi
}

# Validate every target before creating directories, repairing permissions, or
# asking for a token. realpath -m also catches links in existing parent paths.
preflight_target_path "$PORTER_DIR" directory
preflight_target_path "$ENV_FILE" file
preflight_target_path "$ADMIN_CREDENTIAL" file
preflight_target_path "$POSTGRES_CREDENTIAL" file
preflight_target_path "$STATE_DIR" directory
preflight_target_path "$SYSTEMD_DIR" directory
preflight_target_path "$LOCAL_BIN_DIR" directory
preflight_target_path "$(target_path /usr/local/bin/porter)" file
preflight_target_path "$(target_path /usr/local/bin/porter-migrate)" file

ENV_EXISTS=0
if sudo test -e "$ENV_FILE"; then
  ENV_EXISTS=1
  if sudo grep -Eq '^[[:space:]]*(export[[:space:]]+)?(PORTER_ADMIN_TOKEN|PORTER_DATABASE_URL|DATABASE_URL|POSTGRES_URL|[A-Z_][A-Z0-9_]*DSN|DSN|PG[A-Z0-9_]+)[[:space:]]*=' "$ENV_FILE"; then
    echo "admin tokens and PostgreSQL DSNs must not be environment assignments in porter.env" >&2
    exit 1
  fi
fi

ADMIN_EXISTS=0
if sudo test -e "$ADMIN_CREDENTIAL"; then
  ADMIN_EXISTS=1
  if ! sudo test -s "$ADMIN_CREDENTIAL" || \
     ! sudo awk 'END { if (NR != 1) exit 1 }' "$ADMIN_CREDENTIAL" || \
     ! sudo grep -Eq '^[A-Za-z0-9_-]{32,}$' "$ADMIN_CREDENTIAL"; then
    echo "admin credential is invalid; refusing installation" >&2
    exit 1
  fi
fi

POSTGRES_EXISTS=0
USE_LOCAL_POSTGRES=0
if sudo test -e "$POSTGRES_CREDENTIAL"; then
  POSTGRES_EXISTS=1
  if ! sudo test -s "$POSTGRES_CREDENTIAL" || \
     ! sudo awk 'END { if (NR != 1) exit 1 }' "$POSTGRES_CREDENTIAL" || \
     sudo grep -Eq '^[[:space:]]*(export[[:space:]]+)?(PORTER_DATABASE_URL|DATABASE_URL|POSTGRES_URL|[A-Z_][A-Z0-9_]*DSN|DSN|PG[A-Z0-9_]+)[[:space:]]*=' "$POSTGRES_CREDENTIAL"; then
    echo "PostgreSQL credential source must contain exactly one raw DSN line" >&2
    exit 1
  fi
  if sudo grep -Fqx -- "$LOCAL_POSTGRES_DSN" "$POSTGRES_CREDENTIAL"; then
    USE_LOCAL_POSTGRES=1
  fi
else
  USE_LOCAL_POSTGRES=1
fi

validate_porter_account() {
  local passwd_entry group_entry pw_name pw_pass pw_uid pw_gid pw_gecos pw_home pw_shell
  local gr_name gr_pass gr_gid gr_members
  passwd_entry=$(getent passwd porter || true)
  group_entry=$(getent group porter || true)
  if [[ -z "$passwd_entry" ]]; then
    if [[ -n "$group_entry" ]]; then
      IFS=: read -r gr_name gr_pass gr_gid gr_members <<< "$group_entry"
      if [[ "$gr_name" != porter || ! "$gr_gid" =~ ^[0-9]+$ ]] || (( gr_gid == 0 || gr_gid >= 1000 )); then
        echo "existing porter group must be a non-root system group" >&2
        return 1
      fi
    fi
    PORTER_ACCOUNT_EXISTS=0
    return 0
  fi
  if [[ -z "$group_entry" ]]; then
    echo "existing porter account has no porter primary group" >&2
    return 1
  fi
  IFS=: read -r pw_name pw_pass pw_uid pw_gid pw_gecos pw_home pw_shell <<< "$passwd_entry"
  IFS=: read -r gr_name gr_pass gr_gid gr_members <<< "$group_entry"
  if [[ "$pw_name" != porter || "$gr_name" != porter ||
        ! "$pw_uid" =~ ^[0-9]+$ || ! "$pw_gid" =~ ^[0-9]+$ || ! "$gr_gid" =~ ^[0-9]+$ ||
        "$pw_gid" != "$gr_gid" || "$pw_home" != "$STATE_DIR" || "$pw_shell" != /usr/sbin/nologin ]] ||
     (( pw_uid == 0 || pw_uid >= 1000 || pw_gid == 0 || pw_gid >= 1000 )); then
    echo "existing porter account must be a non-root system account with group porter, the expected home, and /usr/sbin/nologin" >&2
    return 1
  fi
  PORTER_ACCOUNT_EXISTS=1
}
PORTER_ACCOUNT_EXISTS=0
validate_porter_account

prepare_local_postgres() {
  local pg_command missing_commands=() attempt ready=0
  for pg_command in pg_isready psql createdb; do
    if ! command -v "$pg_command" >/dev/null 2>&1; then
      missing_commands+=("$pg_command")
    fi
  done
  if (( ${#missing_commands[@]} )); then
    if ! command -v apt-get >/dev/null 2>&1; then
      echo "apt-get is required to install missing local PostgreSQL commands: ${missing_commands[*]}" >&2
      return 1
    fi
    sudo apt-get install -y postgresql
    for pg_command in pg_isready psql createdb; do
      if ! command -v "$pg_command" >/dev/null 2>&1; then
        echo "required local PostgreSQL command is still missing after installing postgresql: $pg_command" >&2
        return 1
      fi
    done
  fi

  sudo systemctl enable --now postgresql.service
  for ((attempt = 1; attempt <= 30; attempt++)); do
    if sudo -u postgres pg_isready -q; then
      ready=1
      break
    fi
    if (( attempt < 30 )); then
      sleep 1
    fi
  done
  if (( ! ready )); then
    echo "local PostgreSQL is not ready after starting the service; refusing installation" >&2
    return 1
  fi

  local role_membership_check
  role_membership_check=$(sudo -u postgres psql --no-psqlrc --set ON_ERROR_STOP=1 --tuples-only --no-align \
    --dbname postgres --command "SELECT CASE WHEN EXISTS (SELECT 1 FROM pg_auth_members m JOIN pg_roles r ON (m.roleid = r.oid OR m.member = r.oid) WHERE r.rolname = 'porter') THEN 'unsafe' ELSE 'safe' END;")
  if [[ "$role_membership_check" != safe ]]; then
    echo "existing porter PostgreSQL role has memberships; refusing to alter it" >&2
    return 1
  fi

  local database_identity_check
  database_identity_check=$(sudo -u postgres psql --no-psqlrc --set ON_ERROR_STOP=1 --tuples-only --no-align \
    --dbname postgres --command "SELECT CASE WHEN NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'porter') THEN 'absent' WHEN EXISTS (SELECT 1 FROM pg_database d JOIN pg_roles r ON r.oid = d.datdba WHERE d.datname = 'porter' AND r.rolname = 'porter' AND d.datistemplate = false) THEN 'owned' ELSE 'unsafe' END;")
  case "$database_identity_check" in
    absent|owned) LOCAL_POSTGRES_DB_STATE=$database_identity_check ;;
    *)
      echo "existing local porter database identity/owner is not validated; refusing to alter it" >&2
      return 1
      ;;
  esac
}

provision_local_postgres() {
  local role_sql
  if [[ ${LOCAL_POSTGRES_DB_STATE:-} != absent && ${LOCAL_POSTGRES_DB_STATE:-} != owned ]]; then
    echo "local porter database was not validated before provisioning" >&2
    return 1
  fi
  role_sql="DO \$porter\$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'porter') THEN CREATE ROLE porter LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS; ELSE ALTER ROLE porter WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS; END IF; END \$porter\$;"
  sudo -u postgres psql --no-psqlrc --set ON_ERROR_STOP=1 --dbname postgres --command "$role_sql"
  if [[ "$LOCAL_POSTGRES_DB_STATE" == absent ]]; then
    sudo -u postgres createdb --owner=porter porter
  fi
  sudo -u postgres psql --no-psqlrc --set ON_ERROR_STOP=1 --dbname postgres \
    --command 'ALTER DATABASE porter OWNER TO porter; REVOKE ALL PRIVILEGES ON DATABASE porter FROM PUBLIC; GRANT CONNECT ON DATABASE porter TO porter;'
  sudo -u postgres psql --no-psqlrc --set ON_ERROR_STOP=1 --dbname porter \
    --command 'REVOKE CREATE ON SCHEMA public FROM PUBLIC; GRANT USAGE, CREATE ON SCHEMA public TO porter;'
}

if (( USE_LOCAL_POSTGRES )); then
  prepare_local_postgres
fi

read_admin_token() {
  local confirm
  if [[ -r /dev/tty && -t 0 ]]; then
    printf 'Admin token (32+ URL-safe characters): ' >/dev/tty
    IFS= read -r -s ADMIN_TOKEN </dev/tty || return 1
    printf '\nConfirm admin token: ' >/dev/tty
    IFS= read -r -s confirm </dev/tty || return 1
    printf '\n' >/dev/tty
  else
    printf 'Read the admin token twice from protected stdin (two lines).\n' >&2
    IFS= read -r ADMIN_TOKEN || return 1
    IFS= read -r confirm || return 1
  fi
  if [[ ! "$ADMIN_TOKEN" =~ ^[A-Za-z0-9_-]{32,}$ ]]; then
    echo "admin token must contain at least 32 URL-safe characters" >&2
    return 1
  fi
  if [[ "$ADMIN_TOKEN" != "$confirm" ]]; then
    echo "admin token confirmation did not match" >&2
    return 1
  fi
}

write_admin_token() {
  # The token travels only on stdin to a privileged writer and its temporary
  # file is removed on normal exit and on HUP/INT/TERM before the atomic rename.
  printf '%s\n' "$ADMIN_TOKEN" | sudo sh -c '
    set -eu
    set +x
    umask 077
    path=$1
    tmp=
    cleanup() { [ -z "$tmp" ] || rm -f -- "$tmp" || true; unset token dsn; }
    trap cleanup EXIT
    trap "exit 129" HUP
    trap "exit 130" INT
    trap "exit 143" TERM
    tmp=$(mktemp "${path}.tmp.XXXXXX")
    if ! IFS= read -r token; then
      echo "missing admin credential input" >&2
      exit 1
    fi
    case "$token" in
      ""|*[!A-Za-z0-9_-]*) echo "invalid admin credential input" >&2; exit 1 ;;
    esac
    [ "${#token}" -ge 32 ] || { echo "admin credential is too short" >&2; exit 1; }
    printf "%s\\n" "$token" > "$tmp"
    chown root:root "$tmp"
    chmod 0600 "$tmp"
    mv -f -- "$tmp" "$path"
    tmp=
  ' sh "$ADMIN_CREDENTIAL"
}

write_local_postgres_dsn() {
  printf '%s\n' "$LOCAL_POSTGRES_DSN" | sudo sh -c '
    set -eu
    set +x
    umask 077
    path=$1
    [ ! -e "$path" ] || { echo "PostgreSQL credential source appeared during install" >&2; exit 1; }
    IFS= read -r dsn || { echo "missing PostgreSQL credential input" >&2; exit 1; }
    [ -n "$dsn" ] || { echo "empty PostgreSQL credential input" >&2; exit 1; }
    dir=${path%/*}
    tmp=
    cleanup() { [ -z "$tmp" ] || rm -f -- "$tmp" || true; unset token dsn; }
    trap cleanup EXIT
    trap "exit 129" HUP
    trap "exit 130" INT
    trap "exit 143" TERM
    tmp=$(mktemp "$dir/.porter-postgres-dsn.XXXXXX")
    printf "%s\\n" "$dsn" > "$tmp"
    chown root:root "$tmp"
    chmod 0600 "$tmp"
    mv -f -- "$tmp" "$path"
    tmp=
  ' sh "$POSTGRES_CREDENTIAL"
}

# All read-only validation is complete. Only now may the installer request a
# token or modify the isolated/host filesystem.
if (( ! ADMIN_EXISTS )); then
  read_admin_token
fi

if (( USE_LOCAL_POSTGRES )); then
  provision_local_postgres
fi

if (( ! PORTER_ACCOUNT_EXISTS )); then
  sudo useradd --system --user-group --home-dir "$STATE_DIR" --no-create-home --shell /usr/sbin/nologin porter
fi
validate_porter_account

sudo install -d -o root -g root -m 0755 "$PORTER_DIR" "$SYSTEMD_DIR"
sudo install -d -o porter -g porter -m 0750 "$STATE_DIR"
sudo install -d -o root -g root -m 0755 "$LOCAL_BIN_DIR"
sudo install -o root -g root -m 0755 "$BIN" "$(target_path /usr/local/bin/porter)"
sudo install -o root -g root -m 0755 "$MIGRATOR_BIN" "$(target_path /usr/local/bin/porter-migrate)"

if (( ! ENV_EXISTS )); then
  sudo install -o root -g root -m 0600 "$REPO_ROOT/deploy/porter.env.example" "$ENV_FILE"
fi
sudo chown root:root "$ENV_FILE"
sudo chmod 0600 "$ENV_FILE"

if (( ! POSTGRES_EXISTS )); then
  write_local_postgres_dsn
fi
sudo chown root:root "$POSTGRES_CREDENTIAL"
sudo chmod 0600 "$POSTGRES_CREDENTIAL"
POSTGRES_META=$(sudo stat -c '%u:%g:%a' "$POSTGRES_CREDENTIAL")
if [[ "$POSTGRES_META" != "0:0:600" ]] || ! sudo test -s "$POSTGRES_CREDENTIAL" || \
   ! sudo awk 'END { if (NR != 1) exit 1 }' "$POSTGRES_CREDENTIAL"; then
  echo "unsafe or empty PostgreSQL credential source (expected root:root 0600)" >&2
  exit 1
fi
if (( ! ADMIN_EXISTS )); then
  write_admin_token
  unset ADMIN_TOKEN confirm 2>/dev/null || true
fi
sudo chown root:root "$ADMIN_CREDENTIAL"
sudo chmod 0600 "$ADMIN_CREDENTIAL"
ADMIN_META=$(sudo stat -c '%u:%g:%a' "$ADMIN_CREDENTIAL")
if [[ "$ADMIN_META" != "0:0:600" ]] || \
   ! sudo awk 'END { if (NR != 1) exit 1 }' "$ADMIN_CREDENTIAL" || \
   ! sudo grep -Eq '^[A-Za-z0-9_-]{32,}$' "$ADMIN_CREDENTIAL"; then
  echo "admin credential is missing, invalid, or unsafe; refusing activation" >&2
  exit 1
fi

if [[ ${PORTER_INSTALL_TEST_MODE:-0} == 1 ]]; then
  echo "Install smoke mode: temporary-root files and fake local PostgreSQL setup completed; migrations and Porter unit reload/activation were skipped."
  exit 0
fi

echo "Credential-backed migration and service activation remain blocked pending Coder-owned credential readers, redaction tests, and reviewed unit integration." >&2
echo "No PostgreSQL migration or Porter service activation was attempted." >&2
exit 1
