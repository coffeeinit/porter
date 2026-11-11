#!/usr/bin/env bash
set -euo pipefail
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

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd -- "$SCRIPT_DIR/.." && pwd)
BIN=${1:-"$REPO_ROOT/bin/porter-agent"}
AGENT_ENV_TEMPLATE="$REPO_ROOT/deploy/porter-agent.env.example"
AGENT_ENV_FILE=/etc/porter/porter-agent.env
if [[ ! -x "$BIN" ]]; then
  echo "missing executable Porter Agent binary: $BIN" >&2
  echo "build it first with: make build" >&2
  exit 1
fi
if [[ -L "$AGENT_ENV_TEMPLATE" || ( -e "$AGENT_ENV_TEMPLATE" && ! -f "$AGENT_ENV_TEMPLATE" ) ]]; then
  echo "agent environment template must be a regular non-symlink file: $AGENT_ENV_TEMPLATE" >&2
  exit 1
fi
if sudo test -L /etc/porter; then
  echo "agent configuration directory must not be a symbolic link: /etc/porter" >&2
  exit 1
fi
if sudo test -e /etc/porter && ! sudo test -d /etc/porter; then
  echo "agent configuration path must be a directory: /etc/porter" >&2
  exit 1
fi
AGENT_ENV_EXISTS=0
if [[ -f "$AGENT_ENV_TEMPLATE" ]]; then
  if sudo test -L "$AGENT_ENV_FILE"; then
    echo "agent environment destination must not be a symbolic link: $AGENT_ENV_FILE" >&2
    exit 1
  fi
  if sudo test -e "$AGENT_ENV_FILE"; then
    if ! sudo test -f "$AGENT_ENV_FILE"; then
      echo "agent environment destination must be a regular file: $AGENT_ENV_FILE" >&2
      exit 1
    fi
    AGENT_ENV_EXISTS=1
  fi
fi
sudo install -d -o root -g root -m 0755 /etc/porter
if [[ -f "$AGENT_ENV_TEMPLATE" ]]; then
  if (( ! AGENT_ENV_EXISTS )); then
    sudo install -o root -g root -m 0600 "$AGENT_ENV_TEMPLATE" "$AGENT_ENV_FILE"
  else
    echo "Existing agent environment file left unchanged: $AGENT_ENV_FILE"
  fi
else
  echo "agent environment template is absent; no agent environment file was installed." >&2
fi
sudo install -o root -g root -m 0755 "$BIN" /usr/local/bin/porter-agent
sudo install -o root -g root -m 0644 "$REPO_ROOT/deploy/systemd/porter-agent.service" /etc/systemd/system/porter-agent.service
sudo systemctl daemon-reload
echo "Agent binary and unit installed; the service was not enabled or started."
echo "Enrollment is not available through a safe CLI in this checkout; do not join yet."
echo "Once a hidden-prompt join CLI is available, use:"
echo "  sudo /usr/local/bin/porter-agent join --server <PORTER_URL> --state-dir /var/lib/porter-agent [--hostname <HOSTNAME>]"
echo "The join flow must read the token with terminal echo disabled; never put it in a command or file."
