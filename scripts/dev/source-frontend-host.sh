#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT_DIR=$(cd "$SCRIPT_DIR/../.." && pwd)
SOURCE_FRONTEND_INPUT=${SYNON_DEV_FRONTEND_SOURCE:-$ROOT_DIR/frontend}
STATE_ROOT_INPUT=${SYNON_DEV_FRONTEND_RUNTIME_STATE:-${XDG_STATE_HOME:-$HOME/.local/state}/synon-biomed-source-dev/frontend-host}
NPM_BIN=${NPM:-npm}
# This supervisor is the canonical source-development entry, not a generic
# Vite invocation.  Keep one deterministic loopback authority when callers do
# not supply an isolated test port, while still allowing every value to be
# overridden explicitly for focused browser runs.
SYNON_DEV_WEB_HOST=${SYNON_DEV_WEB_HOST:-127.0.0.1}
SYNON_DEV_WEB_PORT=${SYNON_DEV_WEB_PORT:-8765}
SYNON_DEV_BACKEND_URL=${SYNON_DEV_BACKEND_URL:-http://127.0.0.1:8766}
export SYNON_DEV_WEB_HOST SYNON_DEV_WEB_PORT SYNON_DEV_BACKEND_URL
PACKAGE_PATHS=(
  package.json
  package-lock.json
  packages/desktop/package.json
)

fail() {
  echo "[source-frontend] $*" >&2
  exit 1
}

for required_command in flock node realpath setsid sha256sum; do
  command -v "$required_command" >/dev/null 2>&1 || fail "required command not found: $required_command"
done
command -v "$NPM_BIN" >/dev/null 2>&1 || fail "npm executable not found: $NPM_BIN"

SOURCE_FRONTEND=$(realpath -e "$SOURCE_FRONTEND_INPUT") || fail "canonical frontend does not exist: $SOURCE_FRONTEND_INPUT"
[[ -d "$SOURCE_FRONTEND" ]] || fail "canonical frontend is not a directory: $SOURCE_FRONTEND"

mkdir -p "$STATE_ROOT_INPUT"
chmod 700 "$STATE_ROOT_INPUT"
STATE_ROOT=$(realpath -e "$STATE_ROOT_INPUT") || fail "runtime state does not exist: $STATE_ROOT_INPUT"
if [[ "$STATE_ROOT" == "/" || "$STATE_ROOT" == "$SOURCE_FRONTEND" || "$STATE_ROOT" == "$SOURCE_FRONTEND/"* || "$SOURCE_FRONTEND" == "$STATE_ROOT/"* ]]; then
  fail "runtime state must be outside the canonical frontend: $STATE_ROOT"
fi

package_files=()
validate_package_files() {
  local relative_path
  package_files=()
  for relative_path in "${PACKAGE_PATHS[@]}"; do
    if [[ ! -f "$SOURCE_FRONTEND/$relative_path" || -L "$SOURCE_FRONTEND/$relative_path" ]]; then
      echo "[source-frontend] canonical package metadata is missing or symbolic: $SOURCE_FRONTEND/$relative_path" >&2
      return 1
    fi
    package_files+=("$SOURCE_FRONTEND/$relative_path")
  done
}
validate_package_files || exit 1

LOCK_FILE="$STATE_ROOT/host.lock"
HOST_PID_FILE="$STATE_ROOT/host.pid"
FRONTEND_PGID_FILE="$STATE_ROOT/frontend.pgid"
DEPENDENCY_KEY_FILE="$STATE_ROOT/dependency-key"

exec {LOCK_FD}>"$LOCK_FILE"
if ! flock -n "$LOCK_FD"; then
  existing_pid="unknown"
  if [[ -f "$HOST_PID_FILE" ]]; then
    existing_pid=$(tr -d '\r\n' <"$HOST_PID_FILE")
  fi
  echo "[source-frontend] another host is already running (pid=$existing_pid, state=$STATE_ROOT)" >&2
  exit 73
fi

atomic_write() {
  local destination="$1"
  local value="$2"
  local temporary="$destination.tmp.$$"
  printf '%s\n' "$value" >"$temporary"
  mv -f "$temporary" "$destination"
}

remove_owned_pid_file() {
  local path="$1"
  local expected="$2"
  local observed=""
  if [[ -f "$path" ]]; then
    observed=$(tr -d '\r\n' <"$path")
  fi
  if [[ "$observed" == "$expected" ]]; then
    rm -f "$path"
  fi
}

atomic_write "$HOST_PID_FILE" "$$"

dependency_key() {
  validate_package_files || return 1
  {
    local index
    for index in "${!PACKAGE_PATHS[@]}"; do
      printf '%s\n' "${PACKAGE_PATHS[$index]}"
      sha256sum "${package_files[$index]}"
    done
    node --version
    "$NPM_BIN" --version
  } | sha256sum | cut -d ' ' -f1
}

installed_dependency_key() {
  if [[ -f "$DEPENDENCY_KEY_FILE" ]]; then
    tr -d '\r\n' <"$DEPENDENCY_KEY_FILE"
  fi
}

ensure_dependencies() {
  local before_key
  local after_key
  local recorded_key
  before_key=$(dependency_key) || return 1
  recorded_key=$(installed_dependency_key)
  if [[ "$before_key" == "$recorded_key" && -x "$SOURCE_FRONTEND/node_modules/.bin/vite" ]]; then
    return 0
  fi

  echo "[source-frontend] installing dependencies in canonical source: $SOURCE_FRONTEND"
  if ! (cd "$SOURCE_FRONTEND" && "$NPM_BIN" ci --ignore-scripts); then
    echo "[source-frontend] dependency installation failed; Vite was not started with stale dependencies" >&2
    return 1
  fi
  if [[ ! -x "$SOURCE_FRONTEND/node_modules/.bin/vite" ]]; then
    echo "[source-frontend] dependency installation completed without node_modules/.bin/vite" >&2
    return 1
  fi

  after_key=$(dependency_key) || return 1
  if [[ "$after_key" != "$before_key" ]]; then
    echo "[source-frontend] dependency metadata changed during npm ci; retrying from the new canonical state" >&2
    return 75
  fi
  atomic_write "$DEPENDENCY_KEY_FILE" "$after_key"
}

prepare_dependencies() {
  local attempt
  local status
  for attempt in 1 2 3; do
    if ensure_dependencies; then
      return 0
    else
      status=$?
    fi
    if (( status != 75 )); then
      return "$status"
    fi
  done
  echo "[source-frontend] dependency metadata kept changing during npm ci; refusing an unstable start" >&2
  return 1
}

launcher_pid=""
frontend_pgid=""

process_group_exists() {
  local pgid="$1"
  kill -0 -- "-$pgid" 2>/dev/null
}

stop_frontend() {
  local candidate_pgid=""
  local observed_session=""
  if [[ -z "$frontend_pgid" && -n "$launcher_pid" ]]; then
    # Shutdown can arrive after the session leader publishes its PID but before
    # start_frontend has consumed it. Recover that identity before waiting on
    # the setsid launcher, otherwise a fast caller can strand the whole group.
    for _ in $(seq 1 100); do
      if [[ -f "$FRONTEND_PGID_FILE" ]]; then
        candidate_pgid=$(tr -d '\r\n' <"$FRONTEND_PGID_FILE")
        if [[ "$candidate_pgid" =~ ^[0-9]+$ ]] && process_group_exists "$candidate_pgid"; then
          frontend_pgid="$candidate_pgid"
          break
        fi
      fi
      kill -0 "$launcher_pid" 2>/dev/null || break
      sleep 0.02
    done
  fi
  if [[ -n "$frontend_pgid" ]] && [[ "$frontend_pgid" =~ ^[0-9]+$ ]]; then
    observed_session=$(ps -o sid= -p "$frontend_pgid" 2>/dev/null | tr -d ' ' || true)
    if [[ -n "$observed_session" && "$observed_session" != "$frontend_pgid" ]]; then
      echo "[source-frontend] refusing to signal pid $frontend_pgid because it is not its recorded session leader" >&2
    elif process_group_exists "$frontend_pgid"; then
      kill -TERM -- "-$frontend_pgid" 2>/dev/null || true
      for _ in $(seq 1 50); do
        process_group_exists "$frontend_pgid" || break
        sleep 0.1
      done
      if process_group_exists "$frontend_pgid"; then
        echo "[source-frontend] frontend process group $frontend_pgid ignored SIGTERM; sending SIGKILL" >&2
        kill -KILL -- "-$frontend_pgid" 2>/dev/null || true
      fi
    fi
  elif [[ -n "$launcher_pid" ]] && kill -0 "$launcher_pid" 2>/dev/null; then
    echo "[source-frontend] stopping launcher before a process-group identity was published" >&2
    kill -TERM "$launcher_pid" 2>/dev/null || true
  fi
  if [[ -n "$launcher_pid" ]]; then
    wait "$launcher_pid" 2>/dev/null || true
    launcher_pid=""
  fi
  if [[ -n "$frontend_pgid" ]]; then
    remove_owned_pid_file "$FRONTEND_PGID_FILE" "$frontend_pgid"
    frontend_pgid=""
  fi
}

cleanup_started=false
cleanup() {
  if [[ "$cleanup_started" == "true" ]]; then
    return
  fi
  cleanup_started=true
  stop_frontend
  remove_owned_pid_file "$HOST_PID_FILE" "$$"
}

shutdown() {
  trap - EXIT INT TERM HUP
  cleanup
  exit 0
}

trap cleanup EXIT
trap shutdown INT TERM HUP

start_frontend() {
  local supervisor_script
  rm -f "$FRONTEND_PGID_FILE"
  supervisor_script=$(cat <<'SUPERVISOR'
set -Eeuo pipefail
host_pid="$1"
npm_bin="$2"
watcher_script="$3"
pgid_file="$4"
shift 4
package_files=("$@")
supervisor_pid=$BASHPID
temporary="$pgid_file.tmp.$supervisor_pid"
printf '%s\n' "$supervisor_pid" >"$temporary"
mv -f "$temporary" "$pgid_file"

npm_pid=""
watchdog_pid=""
watcher_pid=""
terminate_group() {
  trap '' INT TERM HUP USR1
  kill -TERM -- "-$supervisor_pid" 2>/dev/null || true
  wait "$npm_pid" 2>/dev/null || true
  wait "$watchdog_pid" 2>/dev/null || true
  wait "$watcher_pid" 2>/dev/null || true
}
shutdown_supervisor() {
  terminate_group
  exit 143
}
restart_supervisor() {
  terminate_group
  exit 75
}
trap shutdown_supervisor INT TERM HUP
trap restart_supervisor USR1

(
  while kill -0 "$host_pid" 2>/dev/null; do
    sleep 0.25
  done
  kill -TERM "$supervisor_pid" 2>/dev/null || true
) &
watchdog_pid=$!

node "$watcher_script" "$host_pid" "$supervisor_pid" "${package_files[@]}" &
watcher_pid=$!
"$npm_bin" run dev &
npm_pid=$!
completed_pid=""
status=0
wait -n -p completed_pid "$npm_pid" "$watcher_pid" || status=$?
if [[ "$completed_pid" == "$npm_pid" ]]; then
  npm_pid=""
else
  watcher_pid=""
  if (( status == 0 )); then
    status=70
  fi
  echo "[source-frontend] dependency watcher exited unexpectedly with status $status" >&2
fi
terminate_group
exit "$status"
SUPERVISOR
)

  (
    cd "$SOURCE_FRONTEND"
    exec setsid --fork --wait bash -c "$supervisor_script" _ \
      "$$" "$NPM_BIN" "$SCRIPT_DIR/frontend-dependency-watch.mjs" \
      "$FRONTEND_PGID_FILE" "${package_files[@]}"
  ) &
  launcher_pid=$!

  for _ in $(seq 1 100); do
    if [[ -f "$FRONTEND_PGID_FILE" ]]; then
      frontend_pgid=$(tr -d '\r\n' <"$FRONTEND_PGID_FILE")
      if [[ "$frontend_pgid" =~ ^[0-9]+$ ]] && process_group_exists "$frontend_pgid"; then
        echo "[source-frontend] Vite process group $frontend_pgid started from $SOURCE_FRONTEND"
        return 0
      fi
    fi
    if ! kill -0 "$launcher_pid" 2>/dev/null; then
      wait "$launcher_pid" 2>/dev/null || true
      launcher_pid=""
      echo "[source-frontend] frontend process group exited before startup completed" >&2
      return 1
    fi
    sleep 0.02
  done

  echo "[source-frontend] timed out waiting for the frontend process group" >&2
  stop_frontend
  return 1
}

prepare_dependencies

while true; do
  active_key=$(dependency_key)
  start_frontend

  # Close the install-to-watch race without scanning ordinary source files.
  if [[ "$(dependency_key)" != "$active_key" ]]; then
    echo "[source-frontend] dependency metadata changed before the watcher became active; restarting"
    stop_frontend
    prepare_dependencies
    continue
  fi

  frontend_status=0
  wait "$launcher_pid" || frontend_status=$?
  launcher_pid=""
  if [[ -n "$frontend_pgid" ]]; then
    remove_owned_pid_file "$FRONTEND_PGID_FILE" "$frontend_pgid"
    frontend_pgid=""
  fi
  if (( frontend_status == 75 )); then
    prepare_dependencies
    continue
  fi
  if (( frontend_status != 0 )); then
    echo "[source-frontend] Vite process group exited with status $frontend_status" >&2
  fi
  exit "$frontend_status"
done
