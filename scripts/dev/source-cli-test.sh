#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
TMP_DIR=$(mktemp -d)
START_PID=''
SLEEP_PID=''

cleanup() {
  for pid in "$START_PID" "$SLEEP_PID"; do
    if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
      kill -TERM "$pid" 2>/dev/null || true
      wait "$pid" 2>/dev/null || true
    fi
  done
  rm -rf -- "$TMP_DIR"
}
trap cleanup EXIT INT TERM

SOURCE_ROOT="$TMP_DIR/source checkout with spaces"
mkdir -p "$SOURCE_ROOT/scripts/dev" "$TMP_DIR/home/.local/bin" "$TMP_DIR/conflict/.local/bin" "$TMP_DIR/conflict-link/.local/bin"
cp "$SCRIPT_DIR/synon" "$SOURCE_ROOT/scripts/dev/synon"
cp "$SCRIPT_DIR/install-source-cli.sh" "$SOURCE_ROOT/scripts/dev/install-source-cli.sh"
chmod 0755 "$SOURCE_ROOT/scripts/dev/synon" "$SOURCE_ROOT/scripts/dev/install-source-cli.sh"
cat >"$SOURCE_ROOT/scripts/dev/source-quickstart.sh" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
state="${SYNON_SOURCE_STATE_DIR:-$HOME/.local/state/synon-biomed-source-quickstart}"
for ((index = 1; index <= $#; index++)); do
  argument=${!index}
  case "$argument" in
    --state-dir) ((index++)); state=${!index} ;;
    --state-dir=*) state=${argument#--state-dir=} ;;
  esac
done
mkdir -p "$state"
exec {lock_fd}>"$state/quickstart.lock"
flock -n "$lock_fd"
printf '%s\n' "$$" >"$state/quickstart.pid"
trap 'rm -f "$state/quickstart.pid"; exit 0' INT TERM
printf 'start-args=%s\n' "$*" >"$state/start-args"
while true; do sleep .05; done
EOF
chmod 0755 "$SOURCE_ROOT/scripts/dev/source-quickstart.sh"

HOME="$TMP_DIR/home" PATH="/usr/bin:/bin" \
  bash "$SOURCE_ROOT/scripts/dev/install-source-cli.sh" >"$TMP_DIR/install.log"
grep -Fq '[synon-install] installed:' "$TMP_DIR/install.log"
grep -Fq 'export PATH="/tmp/' "$TMP_DIR/install.log"
[[ ! -e "$TMP_DIR/home/.bashrc" ]]
[[ "$(realpath -e "$TMP_DIR/home/.local/bin/synon")" == "$SOURCE_ROOT/scripts/dev/synon" ]]

HOME="$TMP_DIR/home" PATH="/usr/bin:/bin" \
  bash "$SOURCE_ROOT/scripts/dev/install-source-cli.sh" >"$TMP_DIR/install-again.log"
grep -Fq '[synon-install] already installed:' "$TMP_DIR/install-again.log"

HOME="$TMP_DIR/conflict" PATH="/usr/bin:/bin" \
  printf 'existing\n' >"$TMP_DIR/conflict/.local/bin/synon"
if HOME="$TMP_DIR/conflict" PATH="/usr/bin:/bin" \
  bash "$SOURCE_ROOT/scripts/dev/install-source-cli.sh" >"$TMP_DIR/conflict.log" 2>&1; then
  echo 'installer replaced an existing command' >&2
  exit 1
fi
grep -Fq 'refusing to replace an existing non-Synon command' "$TMP_DIR/conflict.log"

ln -s /bin/sh "$TMP_DIR/conflict-link/.local/bin/synon"
if HOME="$TMP_DIR/conflict-link" PATH="/usr/bin:/bin" \
  bash "$SOURCE_ROOT/scripts/dev/install-source-cli.sh" >"$TMP_DIR/conflict-link.log" 2>&1; then
  echo 'installer replaced an existing symlink' >&2
  exit 1
fi
grep -Fq 'refusing to replace an existing non-Synon command' "$TMP_DIR/conflict-link.log"

STATE_DIR="$TMP_DIR/state"
mkdir -p "$STATE_DIR"
HOME="$TMP_DIR/home" SYNON_SOURCE_STATE_DIR="$STATE_DIR" \
  "$TMP_DIR/home/.local/bin/synon" start --timeout-seconds 4 >"$TMP_DIR/start.log" 2>&1 &
START_PID=$!
for _ in $(seq 1 100); do
  [[ -f "$STATE_DIR/quickstart.pid" ]] && break
  sleep .02
done
grep -Fq "start-args=--state-dir $STATE_DIR --timeout-seconds 4" "$STATE_DIR/start-args"
status=$(HOME="$TMP_DIR/home" SYNON_SOURCE_STATE_DIR="$STATE_DIR" \
  "$TMP_DIR/home/.local/bin/synon" status)
[[ "$status" == RUNNING* ]]
HOME="$TMP_DIR/home" SYNON_SOURCE_STATE_DIR="$STATE_DIR" \
  "$TMP_DIR/home/.local/bin/synon" stop >"$TMP_DIR/stop.log"
wait "$START_PID"
START_PID=''
grep -Fq 'source hosts stopped' "$TMP_DIR/stop.log"
status=$(HOME="$TMP_DIR/home" SYNON_SOURCE_STATE_DIR="$STATE_DIR" \
  "$TMP_DIR/home/.local/bin/synon" status)
[[ "$status" == STOPPED* ]]

STATE_EQ="$TMP_DIR/state with spaces"
HOME="$TMP_DIR/home" SYNON_SOURCE_STATE_DIR="$STATE_EQ" \
  "$TMP_DIR/home/.local/bin/synon" start --state-dir="$STATE_EQ" --timeout-seconds 4 >"$TMP_DIR/start-equals.log" 2>&1 &
START_PID=$!
for _ in $(seq 1 100); do
  [[ -f "$STATE_EQ/quickstart.pid" ]] && break
  sleep .02
done
grep -Fq "start-args=--state-dir=$STATE_EQ --timeout-seconds 4" "$STATE_EQ/start-args"
status=$(HOME="$TMP_DIR/home" SYNON_SOURCE_STATE_DIR="$STATE_EQ" \
  "$TMP_DIR/home/.local/bin/synon" status)
[[ "$status" == RUNNING* ]]
HOME="$TMP_DIR/home" SYNON_SOURCE_STATE_DIR="$STATE_EQ" \
  "$TMP_DIR/home/.local/bin/synon" stop >"$TMP_DIR/stop-equals.log"
wait "$START_PID"
START_PID=''
grep -Fq 'source hosts stopped' "$TMP_DIR/stop-equals.log"

mkdir -p "$TMP_DIR/identity-state"
: >"$TMP_DIR/identity-state/quickstart.lock"
sleep 30 &
SLEEP_PID=$!
printf '%s\n' "$SLEEP_PID" >"$TMP_DIR/identity-state/quickstart.pid"
if HOME="$TMP_DIR/home" SYNON_SOURCE_STATE_DIR="$TMP_DIR/identity-state" \
  "$TMP_DIR/home/.local/bin/synon" stop >"$TMP_DIR/identity.log" 2>&1; then
  echo 'stop signalled an unverified process' >&2
  exit 1
fi
grep -Fq 'refusing to signal unverified quickstart PID' "$TMP_DIR/identity.log"
kill -0 "$SLEEP_PID"
kill -TERM "$SLEEP_PID"
wait "$SLEEP_PID" 2>/dev/null || true
SLEEP_PID=''

mkdir -p "$TMP_DIR/disguised-state"
cat >"$TMP_DIR/disguised.sh" <<EOF
#!/usr/bin/env bash
exec -a "$SOURCE_ROOT/scripts/dev/source-quickstart.sh --state-dir $TMP_DIR/disguised-state" sleep 30
EOF
chmod 0755 "$TMP_DIR/disguised.sh"
"$TMP_DIR/disguised.sh" &
SLEEP_PID=$!
: >"$TMP_DIR/disguised-state/quickstart.lock"
printf '%s\n' "$SLEEP_PID" >"$TMP_DIR/disguised-state/quickstart.pid"
if HOME="$TMP_DIR/home" SYNON_SOURCE_STATE_DIR="$TMP_DIR/disguised-state" \
  "$TMP_DIR/home/.local/bin/synon" stop >"$TMP_DIR/disguised.log" 2>&1; then
  echo 'stop signalled a disguised process without the lock' >&2
  exit 1
fi
grep -Fq 'refusing to signal unverified quickstart PID' "$TMP_DIR/disguised.log"
kill -0 "$SLEEP_PID"
kill -TERM "$SLEEP_PID"
wait "$SLEEP_PID" 2>/dev/null || true
SLEEP_PID=''

ROOT_DIR=$(cd "$SCRIPT_DIR/../.." && pwd -P)
make -C "$ROOT_DIR" -n source-cli-install | grep -Fq 'bash scripts/dev/install-source-cli.sh'
make -C "$ROOT_DIR" -n source-cli-test | grep -Fq 'bash scripts/dev/source-cli-test.sh'
grep -Fq 'synon start' "$ROOT_DIR/README.md"
grep -Fq 'synon start' "$ROOT_DIR/docs/operations-runbook.md"

echo 'source CLI tests passed'
