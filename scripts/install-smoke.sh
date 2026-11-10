#!/usr/bin/env bash
set -euo pipefail
set +x

ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
if [[ ! -x /usr/bin/sudo ]]; then
  echo "install smoke requires sudo for root-ownership verification" >&2
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
SUDO_ARGS_LOG="$WORK/sudo-args.log"
USERADD_LOG="$WORK/useradd.log"
SYSTEMCTL_LOG="$WORK/systemctl.log"
: > "$SUDO_ARGS_LOG"
: > "$USERADD_LOG"
: > "$SYSTEMCTL_LOG"

# The fixture includes the installer, example config, current server unit, and
# fake build artifacts. Do not create or copy the pending migration unit.
cp -- "$ROOT/scripts/install-porter.sh" "$FIXTURE_ROOT/scripts/install-porter.sh"
cp -- "$ROOT/deploy/porter.env.example" "$FIXTURE_ROOT/deploy/porter.env.example"
cp -- "$ROOT/deploy/systemd/porter.service" "$FIXTURE_ROOT/deploy/systemd/porter.service"
cat > "$FIXTURE_ROOT/bin/porter" <<'BIN'
#!/usr/bin/env sh
exit 0
BIN
cat > "$FIXTURE_ROOT/bin/porter-migrate" <<'BIN'
#!/usr/bin/env sh
exit 0
BIN
chmod 0755 "$FIXTURE_ROOT/bin/porter" "$FIXTURE_ROOT/bin/porter-migrate"
if [[ -e "$FIXTURE_ROOT/deploy/systemd/porter-migrate.service" ]]; then
  echo "smoke fixture must not create the Coder-owned migration unit" >&2
  exit 1
fi

cat > "$MOCK_BIN/getent" <<'GETENT'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$#" == 2 && "$1" == passwd && "$2" == porter ]]; then
  if [[ -f "$PORTER_INSTALL_TEST_USER_MARKER" ]]; then
    printf '%s\n' 'porter:x:997:997:Porter service:/var/lib/porter:/usr/sbin/nologin'
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

cat > "$MOCK_BIN/systemctl" <<'SYSTEMCTL'
#!/usr/bin/env bash
set -euo pipefail
printf '%q ' "$@" >> "$PORTER_INSTALL_TEST_SYSTEMCTL_LOG"
printf '\n' >> "$PORTER_INSTALL_TEST_SYSTEMCTL_LOG"
echo "blocked fake systemctl invocation in installer smoke" >&2
exit 97
SYSTEMCTL
chmod 0755 "$MOCK_BIN/systemctl"

cat > "$MOCK_BIN/sudo" <<'SUDO'
#!/usr/bin/env bash
set -euo pipefail
printf '%q ' "$@" >> "$PORTER_INSTALL_TEST_SUDO_ARGS_LOG"
printf '\n' >> "$PORTER_INSTALL_TEST_SUDO_ARGS_LOG"
if [[ -n ${PORTER_ADMIN_TOKEN:-} ]]; then
  echo "admin token environment variable reached a privileged command" >&2
  exit 98
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
  install|test|chown|chmod|grep|awk|stat|cat|sh)
    ;;
  *)
    echo "blocked unexpected privileged command in installer smoke" >&2
    exit 99
    ;;
esac

# Every absolute path passed to a real privileged command must stay within the
# isolated temporary root, smoke workspace, or copied fixture repository.
for arg in "$@"; do
  if [[ "$arg" == /* ]]; then
    case "$arg" in
      "$PORTER_INSTALL_TEST_ROOT"|"$PORTER_INSTALL_TEST_ROOT"/*|"$PORTER_INSTALL_TEST_WORK"/*|"$PORTER_INSTALL_TEST_REPO_ROOT"/*) ;;
      *)
        echo "blocked privileged path outside isolated roots" >&2
        exit 100
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

# Prove the systemctl name resolves to the fake and that the fake fails closed;
# clear the probe record so installer assertions require zero real/fake calls.
set +e
PATH="$MOCK_BIN:$PATH" \
PORTER_INSTALL_TEST_SYSTEMCTL_LOG="$SYSTEMCTL_LOG" \
  systemctl daemon-reload >/dev/null 2>&1
systemctl_probe_status=$?
set -e
if [[ $systemctl_probe_status -ne 97 || ! -s "$SYSTEMCTL_LOG" ]]; then
  echo "fake systemctl guard did not intercept the probe" >&2
  exit 1
fi
: > "$SYSTEMCTL_LOG"

run_installer() {
  local target_root=$1
  local input=$2
  local output=$3
  PORTER_ADMIN_TOKEN=untrusted-env-sentinel \
  PORTER_INSTALL_TEST_MODE=1 \
  PORTER_INSTALL_TEST_ROOT="$target_root" \
  PORTER_INSTALL_TEST_USER_MARKER="$USER_MARKER" \
  PORTER_INSTALL_TEST_SUDO_ARGS_LOG="$SUDO_ARGS_LOG" \
  PORTER_INSTALL_TEST_USERADD_LOG="$USERADD_LOG" \
  PORTER_INSTALL_TEST_SYSTEMCTL_LOG="$SYSTEMCTL_LOG" \
  PORTER_INSTALL_TEST_MOCK_BIN="$MOCK_BIN" \
  PORTER_INSTALL_TEST_WORK="$WORK" \
  PORTER_INSTALL_TEST_REPO_ROOT="$FIXTURE_ROOT" \
  PATH="$MOCK_BIN:$PATH" \
    bash "$FIXTURE_ROOT/scripts/install-porter.sh" "$FIXTURE_ROOT/bin/porter" >"$output" 2>&1 <<<"$input"
}

assert_no_token_in_files() {
  local file contents
  for file in "$@"; do
    [[ -f "$file" ]] || continue
    contents=$(<"$file")
    if [[ "$contents" == *"$TOKEN"* ]]; then
      echo "admin token leaked to output or privileged command arguments" >&2
      return 1
    fi
  done
}

assert_no_systemctl_calls() {
  if [[ -s "$SYSTEMCTL_LOG" ]]; then
    echo "installer invoked systemctl; only the fake probe is allowed" >&2
    cat "$SYSTEMCTL_LOG" >&2
    return 1
  fi
}

assert_no_database_or_package_commands() {
  if grep -Eq '(^|[[:space:]])(apt-get|psql|pg_isready|createdb|runuser|systemctl)([[:space:]]|$)' "$SUDO_ARGS_LOG"; then
    echo "installer attempted package, database, runuser, or systemd work before unit validation" >&2
    return 1
  fi
}

TEST_ROOT="$WORK/root"
mkdir -p "$TEST_ROOT"
set +e
run_installer "$TEST_ROOT" "$TOKEN
$TOKEN" "$WORK/install-output.log"
install_status=$?
set -e
if [[ $install_status -eq 0 ]]; then
  echo "installer did not fail closed for the missing migration unit" >&2
  exit 1
fi
if ! grep -Fq 'missing required migration unit source: deploy/systemd/porter-migrate.service' "$WORK/install-output.log"; then
  cat "$WORK/install-output.log" >&2
  echo "installer did not report the missing Coder-owned migration unit" >&2
  exit 1
fi

ENV_FILE="$TEST_ROOT/etc/porter/porter.env"
ADMIN_FILE="$TEST_ROOT/etc/porter/porter-admin-token"
POSTGRES_FILE="$TEST_ROOT/etc/porter/porter-postgres-dsn"
if [[ $(stat -c '%u:%g:%a' "$ENV_FILE") != "0:0:600" ]]; then
  echo "porter.env is not root:root mode 0600" >&2
  exit 1
fi
if [[ $(stat -c '%u:%g:%a' "$ADMIN_FILE") != "0:0:600" ]]; then
  echo "admin credential is not root:root mode 0600" >&2
  exit 1
fi
if [[ $(stat -c '%u:%g:%a' "$POSTGRES_FILE") != "0:0:600" ]]; then
  echo "PostgreSQL credential is not root:root mode 0600" >&2
  exit 1
fi
ADMIN_CONTENT=$(/usr/bin/sudo cat "$ADMIN_FILE")
POSTGRES_CONTENT=$(/usr/bin/sudo cat "$POSTGRES_FILE")
ENV_CONTENT=$(/usr/bin/sudo cat "$ENV_FILE")
if [[ "$ADMIN_CONTENT" != "$TOKEN" ]]; then
  echo "admin credential does not contain exactly the supplied raw token" >&2
  exit 1
fi
if [[ "$POSTGRES_CONTENT" != "$LOCAL_DSN" ]]; then
  echo "PostgreSQL credential is not the expected single raw local DSN" >&2
  exit 1
fi
if [[ "$ENV_CONTENT" == *PORTER_ADMIN_TOKEN=* || "$ENV_CONTENT" == *PORTER_POSTGRES_DSN=* || "$ENV_CONTENT" == *PORTER_POSTGRES_API_DSN=* ]]; then
  echo "porter.env contains an admin token or DSN environment assignment" >&2
  exit 1
fi
if [[ $(stat -c '%a' "$TEST_ROOT/var/lib/porter") != "750" ]]; then
  echo "Porter state directory mode is not 0750" >&2
  exit 1
fi
if [[ ! -x "$TEST_ROOT/usr/local/bin/porter" ]]; then
  echo "installer did not stage the Porter binary under the isolated root" >&2
  exit 1
fi
if [[ -e "$TEST_ROOT/etc/systemd/system/porter.service" || -e "$TEST_ROOT/etc/systemd/system/porter-migrate.service" ]]; then
  echo "installer staged a unit despite the missing Coder migration unit" >&2
  exit 1
fi
if ! grep -q -- '--system.*--user-group' "$USERADD_LOG"; then
  echo "installer did not create the porter system account safely through the fake" >&2
  exit 1
fi
SUDO_ARGS_CONTENT=$(<"$SUDO_ARGS_LOG")
if [[ "$SUDO_ARGS_CONTENT" == *"$TEST_ROOT$TEST_ROOT"* ]]; then
  echo "installer doubled the isolated test-root prefix" >&2
  exit 1
fi
assert_no_token_in_files "$WORK/install-output.log" "$SUDO_ARGS_LOG" "$USERADD_LOG" "$SYSTEMCTL_LOG"
for log in "$WORK/install-output.log" "$SUDO_ARGS_LOG" "$USERADD_LOG" "$SYSTEMCTL_LOG"; do
  if grep -Fq "$LOCAL_DSN" "$log"; then
    echo "PostgreSQL DSN leaked to output or command logs" >&2
    exit 1
  fi
done
assert_no_systemctl_calls
assert_no_database_or_package_commands

# Existing admin credential permissions are repaired without prompting or
# printing the token; the missing-unit guard still prevents activation.
PORTER_INSTALL_TEST_ROOT="$TEST_ROOT" \
PORTER_INSTALL_TEST_WORK="$WORK" \
PORTER_INSTALL_TEST_REPO_ROOT="$FIXTURE_ROOT" \
PORTER_INSTALL_TEST_SUDO_ARGS_LOG="$SUDO_ARGS_LOG" \
PORTER_INSTALL_TEST_USERADD_LOG="$USERADD_LOG" \
PORTER_INSTALL_TEST_USER_MARKER="$USER_MARKER" \
PORTER_INSTALL_TEST_SYSTEMCTL_LOG="$SYSTEMCTL_LOG" \
PORTER_INSTALL_TEST_MOCK_BIN="$MOCK_BIN" \
  "$MOCK_BIN/sudo" chmod 0644 "$ADMIN_FILE"
set +e
PORTER_INSTALL_TEST_MODE=1 \
PORTER_INSTALL_TEST_ROOT="$TEST_ROOT" \
PORTER_INSTALL_TEST_USER_MARKER="$USER_MARKER" \
PORTER_INSTALL_TEST_SUDO_ARGS_LOG="$SUDO_ARGS_LOG" \
PORTER_INSTALL_TEST_USERADD_LOG="$USERADD_LOG" \
PORTER_INSTALL_TEST_SYSTEMCTL_LOG="$SYSTEMCTL_LOG" \
PORTER_INSTALL_TEST_MOCK_BIN="$MOCK_BIN" \
PORTER_INSTALL_TEST_WORK="$WORK" \
PORTER_INSTALL_TEST_REPO_ROOT="$FIXTURE_ROOT" \
PATH="$MOCK_BIN:$PATH" \
  bash "$FIXTURE_ROOT/scripts/install-porter.sh" "$FIXTURE_ROOT/bin/porter" >"$WORK/existing-config-output.log" 2>&1 </dev/null
second_status=$?
set -e
if [[ $second_status -eq 0 ]] || ! grep -Fq 'missing required migration unit source' "$WORK/existing-config-output.log"; then
  echo "existing-config run did not stop at the missing-unit guard" >&2
  exit 1
fi
if [[ $(stat -c '%u:%g:%a' "$ADMIN_FILE") != "0:0:600" ]]; then
  echo "installer did not repair admin credential permissions" >&2
  exit 1
fi
assert_no_token_in_files "$WORK/existing-config-output.log" "$SUDO_ARGS_LOG" "$SYSTEMCTL_LOG"
assert_no_systemctl_calls
assert_no_database_or_package_commands

# Legacy DSN environment variables are rejected without echoing their values.
LEGACY_ROOT="$WORK/legacy-env"
mkdir -p "$LEGACY_ROOT/etc/porter"
printf '%s\n' 'PORTER_POSTGRES_DSN=not-a-real-secret' > "$LEGACY_ROOT/etc/porter/porter.env"
chmod 0600 "$LEGACY_ROOT/etc/porter/porter.env"
set +e
run_installer "$LEGACY_ROOT" "" "$WORK/legacy-env-output.log"
legacy_status=$?
set -e
if [[ $legacy_status -eq 0 ]] || ! grep -Fq 'must not be environment assignments in porter.env' "$WORK/legacy-env-output.log"; then
  echo "installer did not reject a legacy DSN environment assignment" >&2
  exit 1
fi
if grep -Fq 'not-a-real-secret' "$WORK/legacy-env-output.log" "$SUDO_ARGS_LOG"; then
  echo "legacy DSN value leaked to output or privileged arguments" >&2
  exit 1
fi
assert_no_systemctl_calls
assert_no_database_or_package_commands

# Token confirmation mismatch fails before unit activation and never logs token.
BAD_ROOT="$WORK/bad-token"
mkdir -p "$BAD_ROOT"
set +e
run_installer "$BAD_ROOT" "$TOKEN
not-the-same-token" "$WORK/token-mismatch-output.log"
token_mismatch_status=$?
set -e
if [[ $token_mismatch_status -eq 0 ]] || ! grep -Fq 'admin token confirmation did not match' "$WORK/token-mismatch-output.log"; then
  echo "installer accepted a mismatched admin-token confirmation" >&2
  exit 1
fi
if [[ -e "$BAD_ROOT/etc/porter/porter-admin-token" ]]; then
  echo "installer wrote an admin credential after mismatched confirmation" >&2
  exit 1
fi
assert_no_token_in_files "$WORK/token-mismatch-output.log" "$SUDO_ARGS_LOG" "$USERADD_LOG" "$SYSTEMCTL_LOG"
assert_no_systemctl_calls
assert_no_database_or_package_commands

# Static checks cover the uncommitted credential unit contract without claiming
# real LoadCredential isolation or a successful migration/activation sequence.
CONTROL_INSTALLER="$ROOT/scripts/install-porter.sh"
TEMPLATE="$ROOT/deploy/porter.env.example"
if ! grep -Fq 'deploy/systemd/porter-migrate.service' "$CONTROL_INSTALLER" || \
   ! grep -Fq 'deploy/systemd/porter.service' "$CONTROL_INSTALLER" || \
   ! grep -Fq 'LoadCredential=porter-admin-token:/etc/porter/porter-admin-token' "$CONTROL_INSTALLER" || \
   ! grep -Fq 'LoadCredential=porter-postgres-dsn:/etc/porter/porter-postgres-dsn' "$CONTROL_INSTALLER" || \
   ! grep -Fq 'systemd 247 or newer is required' "$CONTROL_INSTALLER"; then
  echo "installer is missing the exact credential-unit or systemd version guard" >&2
  exit 1
fi
if grep -Eq '^[[:space:]]*(PORTER_ADMIN_TOKEN|PORTER_POSTGRES(_API)?_DSN)=' "$TEMPLATE"; then
  echo "example environment template contains a credential assignment" >&2
  exit 1
fi
if [[ -e "$ROOT/deploy/systemd/porter-migrate.service" ]]; then
  echo "the candidate unexpectedly contains a Coder-owned migration unit" >&2
  exit 1
fi
if grep -Eq 'systemctl[[:space:]]+(enable|start)' "$CONTROL_INSTALLER"; then
  echo "activation orchestration was added before credential units/tests landed" >&2
  exit 1
fi

# The separate agent candidate is not copied or edited. This branch must not
# advertise its future token-safe command before the CLI is integrated.
AGENT_INSTALLER="$ROOT/scripts/install-agent.sh"
if grep -Eq -- '--token([ =]|$)|http://|porter-agent join --server' "$AGENT_INSTALLER"; then
  echo "agent installer advertises an unavailable or unsafe enrollment command" >&2
  exit 1
fi
if ! grep -Fq 'Enrollment is unavailable in this checkout' "$AGENT_INSTALLER" || \
   ! grep -Fq 'Do not run enrollment or supply a token in an argument or file' "$AGENT_INSTALLER" || \
   ! grep -Fq 'TTY/--token-stdin CLI and route integration' "$AGENT_INSTALLER"; then
  echo "agent installer does not clearly block unavailable enrollment" >&2
  exit 1
fi

printf '%s\n' "install smoke passed: isolated root/account fakes, root:root 0600 raw admin/DB credential files, no secret in env/args/logs, path-prefix check, fake systemctl guard, missing-unit fail-closed; no real DB/packages/systemd or credential-isolation claim"
