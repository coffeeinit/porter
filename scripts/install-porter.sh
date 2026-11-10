#!/usr/bin/env bash
set -euo pipefail
# Never allow caller-requested shell tracing to expose secrets read below.
set +x
unset PORTER_ADMIN_TOKEN

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd -- "$SCRIPT_DIR/.." && pwd)
BIN=${1:-"$REPO_ROOT/bin/porter"}
MIGRATOR_BIN="$REPO_ROOT/bin/porter-migrate"
MIGRATOR_UNIT="$REPO_ROOT/deploy/systemd/porter-migrate.service"
PORTER_UNIT="$REPO_ROOT/deploy/systemd/porter.service"
if [[ ! -x "$BIN" ]]; then
  echo "missing executable Porter binary: $BIN" >&2
  echo "build it first with: make build" >&2
  exit 1
fi
if [[ ! -x "$MIGRATOR_BIN" ]]; then
  echo "missing required PostgreSQL migration binary: $MIGRATOR_BIN" >&2
  echo "build it first with: make build" >&2
  exit 1
fi

# The test-only root is accepted only in explicit test mode. The smoke harness
# uses fake account/systemd commands and never contacts PostgreSQL.
INSTALL_ROOT=""
if [[ ${PORTER_INSTALL_TEST_MODE:-0} == 1 ]]; then
  INSTALL_ROOT=${PORTER_INSTALL_TEST_ROOT:?PORTER_INSTALL_TEST_ROOT is required in test mode}
  if [[ "$INSTALL_ROOT" != /* || "$INSTALL_ROOT" == "/" ]]; then
    echo "PORTER_INSTALL_TEST_ROOT must be a non-root absolute path" >&2
    exit 1
  fi
  INSTALL_ROOT=${INSTALL_ROOT%/}
elif [[ -n ${PORTER_INSTALL_TEST_ROOT:-} ]]; then
  echo "PORTER_INSTALL_TEST_ROOT is only valid with PORTER_INSTALL_TEST_MODE=1" >&2
  exit 1
fi

target_path() {
  printf '%s%s' "$INSTALL_ROOT" "$1"
}

PORTER_DIR=$(target_path /etc/porter)
ENV_FILE=$(target_path /etc/porter/porter.env)
ADMIN_CREDENTIAL=$(target_path /etc/porter/porter-admin-token)
POSTGRES_CREDENTIAL=$(target_path /etc/porter/porter-postgres-dsn)
STATE_DIR=$(target_path /var/lib/porter)
SYSTEMD_DIR=$(target_path /etc/systemd/system)

if ! getent passwd porter >/dev/null; then
  sudo useradd --system --user-group --home-dir "$STATE_DIR" --no-create-home --shell /usr/sbin/nologin porter
fi
if ! getent passwd porter >/dev/null || ! getent group porter >/dev/null; then
  echo "the unprivileged porter system user and group must exist" >&2
  exit 1
fi

sudo install -d -o root -g root -m 0755 "$PORTER_DIR" "$SYSTEMD_DIR"
sudo install -d -o porter -g porter -m 0750 "$STATE_DIR"
sudo install -d -o root -g root -m 0755 "$(target_path /usr/local/bin)"
sudo install -o root -g root -m 0755 "$BIN" "$(target_path /usr/local/bin/porter)"

if sudo test -L "$ENV_FILE"; then
  echo "$ENV_FILE must not be a symbolic link" >&2
  exit 1
fi
if ! sudo test -e "$ENV_FILE"; then
  sudo install -o root -g root -m 0600 "$REPO_ROOT/deploy/porter.env.example" "$ENV_FILE"
elif ! sudo test -f "$ENV_FILE"; then
  echo "$ENV_FILE must be a regular file" >&2
  exit 1
fi
sudo chown root:root "$ENV_FILE"
sudo chmod 0600 "$ENV_FILE"
if sudo grep -Eq '^[[:space:]]*(PORTER_ADMIN_TOKEN|PORTER_POSTGRES(_API)?_DSN)[[:space:]]*=' "$ENV_FILE"; then
  echo "admin tokens and PostgreSQL DSNs must not be environment assignments in porter.env" >&2
  exit 1
fi

# The DSN is a raw one-line credential, not an EnvironmentFile entry. For the
# local default, deliver the fixed non-secret value through stdin only.
LOCAL_POSTGRES_DSN='host=/var/run/postgresql user=porter dbname=porter sslmode=disable'
if sudo test -L "$POSTGRES_CREDENTIAL"; then
  echo "$POSTGRES_CREDENTIAL must not be a symbolic link" >&2
  exit 1
fi
if ! sudo test -e "$POSTGRES_CREDENTIAL"; then
  printf '%s\n' "$LOCAL_POSTGRES_DSN" | sudo sh -c '
    set -eu
    set +x
    path=$1
    [ ! -e "$path" ] || { echo "PostgreSQL credential source appeared during install" >&2; exit 1; }
    IFS= read -r dsn || { echo "missing PostgreSQL credential input" >&2; exit 1; }
    [ -n "$dsn" ] || { echo "empty PostgreSQL credential input" >&2; exit 1; }
    dir=${path%/*}
    tmp=$(mktemp "$dir/.porter-postgres-dsn.XXXXXX")
    printf "%s\\n" "$dsn" > "$tmp"
    chown root:root "$tmp"
    chmod 0600 "$tmp"
    mv -f "$tmp" "$path"
  ' sh "$POSTGRES_CREDENTIAL"
elif ! sudo test -f "$POSTGRES_CREDENTIAL"; then
  echo "$POSTGRES_CREDENTIAL must be a regular file" >&2
  exit 1
fi
sudo chown root:root "$POSTGRES_CREDENTIAL"
sudo chmod 0600 "$POSTGRES_CREDENTIAL"
CREDENTIAL_META=$(sudo stat -c '%u:%g:%a' "$POSTGRES_CREDENTIAL")
if [[ "$CREDENTIAL_META" != "0:0:600" ]] || ! sudo test -s "$POSTGRES_CREDENTIAL"; then
  echo "unsafe or empty PostgreSQL credential source (expected root:root 0600)" >&2
  exit 1
fi
if ! sudo awk 'END { if (NR != 1) exit 1 }' "$POSTGRES_CREDENTIAL"; then
  echo "PostgreSQL credential source must contain exactly one raw DSN line" >&2
  exit 1
fi
if sudo grep -q '^PORTER_POSTGRES_DSN=' "$POSTGRES_CREDENTIAL"; then
  echo "PostgreSQL credential source must contain a raw DSN, not an environment assignment" >&2
  exit 1
fi

has_valid_admin_token() {
  sudo test -f "$ADMIN_CREDENTIAL" &&
    sudo awk 'END { if (NR != 1) exit 1 }' "$ADMIN_CREDENTIAL" &&
    sudo grep -Eq '^[A-Za-z0-9_-]{32,}$' "$ADMIN_CREDENTIAL"
}

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
  # The token travels only on stdin to the privileged writer; it is not an
  # argument, exported environment variable, URL, EnvironmentFile, or log.
  printf '%s\n' "$ADMIN_TOKEN" | sudo sh -c '
    set -eu
    set +x
    path=$1
    tmp=$(mktemp "${path}.tmp.XXXXXX")
    if ! IFS= read -r token; then
      rm -f "$tmp"
      echo "missing admin credential input" >&2
      exit 1
    fi
    case "$token" in
      ""|*[!A-Za-z0-9_-]*) rm -f "$tmp"; echo "invalid admin credential input" >&2; exit 1 ;;
    esac
    [ "${#token}" -ge 32 ] || { rm -f "$tmp"; echo "admin credential is too short" >&2; exit 1; }
    printf "%s\\n" "$token" > "$tmp"
    chown root:root "$tmp"
    chmod 0600 "$tmp"
    mv -f "$tmp" "$path"
  ' sh "$ADMIN_CREDENTIAL"
}

if sudo test -L "$ADMIN_CREDENTIAL"; then
  echo "$ADMIN_CREDENTIAL must not be a symbolic link" >&2
  exit 1
fi
if ! sudo test -e "$ADMIN_CREDENTIAL"; then
  read_admin_token
  write_admin_token
fi
unset ADMIN_TOKEN confirm 2>/dev/null || true
if ! sudo test -f "$ADMIN_CREDENTIAL"; then
  echo "$ADMIN_CREDENTIAL must be a regular file" >&2
  exit 1
fi
sudo chown root:root "$ADMIN_CREDENTIAL"
sudo chmod 0600 "$ADMIN_CREDENTIAL"
ADMIN_META=$(sudo stat -c '%u:%g:%a' "$ADMIN_CREDENTIAL")
if [[ "$ADMIN_META" != "0:0:600" ]] || ! has_valid_admin_token; then
  echo "admin credential is missing, invalid, or unsafe; refusing activation" >&2
  exit 1
fi

# Require Coder's exact unit sources and credential contract. This installer
# never creates or edits either systemd unit.
ADMIN_CREDENTIAL_DIRECTIVE='LoadCredential=porter-admin-token:/etc/porter/porter-admin-token'
POSTGRES_CREDENTIAL_DIRECTIVE='LoadCredential=porter-postgres-dsn:/etc/porter/porter-postgres-dsn'
if [[ ! -f "$MIGRATOR_UNIT" ]]; then
  echo "missing required migration unit source: deploy/systemd/porter-migrate.service; refusing migration or activation" >&2
  exit 1
fi
if [[ ! -f "$PORTER_UNIT" ]]; then
  echo "missing required server unit source: deploy/systemd/porter.service; refusing activation" >&2
  exit 1
fi
for required in 'Type=oneshot' 'User=porter' 'Group=porter' "$POSTGRES_CREDENTIAL_DIRECTIVE" 'ExecStart=/usr/local/bin/porter-migrate apply'; do
  if ! grep -Fqx "$required" "$MIGRATOR_UNIT"; then
    echo "migration unit is missing a required credential contract line; refusing activation" >&2
    exit 1
  fi
done
for required in 'User=porter' 'Group=porter' "$ADMIN_CREDENTIAL_DIRECTIVE" "$POSTGRES_CREDENTIAL_DIRECTIVE"; do
  if ! grep -Fqx "$required" "$PORTER_UNIT"; then
    echo "server unit is missing a required credential contract line; refusing activation" >&2
    exit 1
  fi
done
if grep -Eq '^[[:space:]]*Environment=.*(PORTER_ADMIN_TOKEN|PORTER_POSTGRES(_API)?_DSN)' "$MIGRATOR_UNIT" "$PORTER_UNIT"; then
  echo "tokens and database DSNs must not be passed through unit environment variables" >&2
  exit 1
fi

if [[ ${PORTER_INSTALL_TEST_MODE:-0} == 1 ]]; then
  echo "Install smoke mode: raw credential paths and permissions validated; package, database, migration, and systemd actions skipped."
  exit 0
fi

SYSTEMD_VERSION=$(systemctl --version 2>/dev/null | awk 'NR == 1 { print $2 }' || true)
if [[ ! "$SYSTEMD_VERSION" =~ ^[0-9]+$ ]] || (( SYSTEMD_VERSION < 247 )); then
  echo "systemd 247 or newer is required for LoadCredential; refusing activation without a fallback" >&2
  exit 1
fi

echo "Credential-backed migration and service activation remain blocked until Coder's unit and credential/redaction tests land." >&2
echo "No PostgreSQL migration or Porter service activation was attempted." >&2
exit 1
