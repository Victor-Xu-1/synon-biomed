#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
ROOT_DIR=$(cd "$SCRIPT_DIR/../.." && pwd -P)
BACKEND_ENTRY_INPUT=${SYNON_QUICKSTART_BACKEND_ENTRY:-$SCRIPT_DIR/source-backend-watch.sh}
FRONTEND_ENTRY_INPUT=${SYNON_QUICKSTART_FRONTEND_ENTRY:-$SCRIPT_DIR/source-frontend-host.sh}
STATE_INPUT=${XDG_STATE_HOME:-$HOME/.local/state}/synon-biomed-source-quickstart
BACKEND_PORT=8766
WEB_PORT=8765
STARTUP_TIMEOUT=300
OPEN_BROWSER=false
BACKEND_PID=''
FRONTEND_PID=''

usage() {
  cat <<'EOF'
Usage: scripts/dev/source-quickstart.sh [options]

Start the existing Synon Biomed source backend watcher and Vite frontend host.
This is a development checkout entry, not a packaged release installer.

Options:
  --open-browser       Open the verified login URL after both services are ready.
  --state-dir PATH     External state/log directory (default: XDG state directory).
  --backend-port PORT  Backend loopback port (default: 8766).
  --web-port PORT      Web loopback port (default: 8765).
  --timeout-seconds N  Bounded startup wait (default: 300).
  --help               Show this help.

Press Ctrl+C to stop only the two source hosts started by this command.
EOF
}

fail() {
  echo "[source-quickstart] $*" >&2
  exit 1
}

while (( $# > 0 )); do
  case "$1" in
    --open-browser) OPEN_BROWSER=true; shift ;;
    --state-dir|--backend-port|--web-port|--timeout-seconds)
      (( $# >= 2 )) || fail "$1 requires a value"
      case "$1" in
        --state-dir) STATE_INPUT=$2 ;;
        --backend-port) BACKEND_PORT=$2 ;;
        --web-port) WEB_PORT=$2 ;;
        --timeout-seconds) STARTUP_TIMEOUT=$2 ;;
      esac
      shift 2 ;;
    --help|-h) usage; exit 0 ;;
    *) fail "unknown option: $1" ;;
  esac
done

for value_name in BACKEND_PORT WEB_PORT; do
  value=${!value_name}
  [[ "$value" =~ ^[0-9]+$ ]] && (( value >= 1024 && value <= 65535 )) ||
    fail "${value_name,,} must be an integer from 1024 to 65535"
done
(( BACKEND_PORT != WEB_PORT )) || fail "backend and Web ports must be different"
[[ "$STARTUP_TIMEOUT" =~ ^[1-9][0-9]*$ ]] || fail "timeout_seconds must be a positive integer"

for command_name in bash curl flock realpath setsid sha256sum timeout; do
  command -v "$command_name" >/dev/null 2>&1 || fail "required command not found: $command_name"
done
GO_BIN=${GO:-go}
NPM_BIN=${NPM:-npm}
command -v "$GO_BIN" >/dev/null 2>&1 || fail "Go 1.26 or newer is required; executable not found: $GO_BIN"
command -v node >/dev/null 2>&1 || fail "Node.js >=22.22.0 and <25 is required; executable not found: node"
command -v "$NPM_BIN" >/dev/null 2>&1 || fail "npm is required; executable not found: $NPM_BIN"

go_version=$("$GO_BIN" env GOVERSION 2>/dev/null) || fail "unable to read Go version from: $GO_BIN"
if [[ ! "$go_version" =~ ^go1\.([0-9]+)(\.|$) ]] || (( 10#${BASH_REMATCH[1]:-0} < 26 )); then
  fail "Go 1.26 or newer is required; found: $go_version"
fi
node_version=$(node --version 2>/dev/null) || fail "unable to read Node.js version"
if [[ ! "$node_version" =~ ^v([0-9]+)\.([0-9]+)\. ]] ||
  (( 10#${BASH_REMATCH[1]:-0} < 22 || 10#${BASH_REMATCH[1]:-0} >= 25 )) ||
  (( 10#${BASH_REMATCH[1]:-0} == 22 && 10#${BASH_REMATCH[2]:-0} < 22 )); then
  fail "Node.js >=22.22.0 and <25 is required; found: $node_version"
fi
"$NPM_BIN" --version >/dev/null 2>&1 || fail "unable to run npm: $NPM_BIN"

BACKEND_ENTRY=$(realpath -e "$BACKEND_ENTRY_INPUT") || fail "backend source host not found: $BACKEND_ENTRY_INPUT"
FRONTEND_ENTRY=$(realpath -e "$FRONTEND_ENTRY_INPUT") || fail "frontend source host not found: $FRONTEND_ENTRY_INPUT"
[[ -f "$BACKEND_ENTRY" && -f "$FRONTEND_ENTRY" ]] || fail "source host entries must be regular files"

STATE_DIR=$(realpath -m "$STATE_INPUT") || fail "state path is invalid: $STATE_INPUT"
HOME_DIR=$(realpath -e "$HOME") || fail "home directory is unavailable"
if [[ "$STATE_DIR" == / || "$STATE_DIR" == "$HOME_DIR" ||
      "$STATE_DIR" == "$ROOT_DIR" || "$STATE_DIR" == "$ROOT_DIR/"* ||
      "$ROOT_DIR" == "$STATE_DIR/"* ]]; then
  fail "state directory must be a dedicated path outside the source tree: $STATE_DIR"
fi
mkdir -p "$STATE_DIR"
chmod 700 "$STATE_DIR"
STATE_DIR=$(realpath -e "$STATE_DIR") || fail "state directory is unavailable: $STATE_INPUT"
LOG_DIR="$STATE_DIR/logs"
mkdir -p "$LOG_DIR" "$STATE_DIR/data" "$STATE_DIR/backend-build" "$STATE_DIR/frontend-host"
chmod 700 "$LOG_DIR" "$STATE_DIR/data" "$STATE_DIR/backend-build" "$STATE_DIR/frontend-host"
BACKEND_LOG="$LOG_DIR/backend.log"
FRONTEND_LOG="$LOG_DIR/frontend.log"
: >"$BACKEND_LOG"
: >"$FRONTEND_LOG"

port_is_open() {
  timeout 1 bash -c "exec 3<>/dev/tcp/127.0.0.1/$1" >/dev/null 2>&1
}
for port in "$BACKEND_PORT" "$WEB_PORT"; do
  ! port_is_open "$port" || fail "port is already in use: 127.0.0.1:$port"
done

stop_owned() {
  local pid=$1
  local label=$2
  [[ -n "$pid" ]] || return 0
  if ! kill -0 "$pid" 2>/dev/null; then
    wait "$pid" 2>/dev/null || true
    return 0
  fi
  kill -TERM "$pid" 2>/dev/null || true
  for _ in $(seq 1 100); do
    kill -0 "$pid" 2>/dev/null || { wait "$pid" 2>/dev/null || true; return 0; }
    sleep .05
  done
  echo "[source-quickstart] $label did not stop after SIGTERM; sending SIGKILL to owned pid $pid" >&2
  kill -KILL "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
}

cleanup() {
  local recorded=''
  trap - EXIT INT TERM
  stop_owned "$FRONTEND_PID" frontend
  stop_owned "$BACKEND_PID" backend
  [[ ! -f "$STATE_DIR/quickstart.pid" ]] || recorded=$(<"$STATE_DIR/quickstart.pid")
  [[ "$recorded" != "$$" ]] || rm -f "$STATE_DIR/quickstart.pid"
}
shutdown() { exit 0; }
trap cleanup EXIT
trap shutdown INT TERM

exec {LOCK_FD}>"$STATE_DIR/quickstart.lock"
flock -n "$LOCK_FD" || fail "another quickstart owns this state directory: $STATE_DIR"
printf '%s\n' "$$" >"$STATE_DIR/quickstart.pid"

SYNON_SOURCE_ROOT="$ROOT_DIR" \
SYNON_ADDRESS="127.0.0.1:$BACKEND_PORT" \
SYNON_HOME="$STATE_DIR/data" \
SYNON_DEV_BUILD_DIR="$STATE_DIR/backend-build" \
GO="$GO_BIN" bash "$BACKEND_ENTRY" >"$BACKEND_LOG" 2>&1 &
BACKEND_PID=$!

SYNON_DEV_FRONTEND_SOURCE="$ROOT_DIR/frontend" \
SYNON_DEV_FRONTEND_RUNTIME_STATE="$STATE_DIR/frontend-host" \
SYNON_DEV_WEB_HOST=127.0.0.1 \
SYNON_DEV_WEB_PORT="$WEB_PORT" \
SYNON_DEV_BACKEND_URL="http://127.0.0.1:$BACKEND_PORT" \
NPM="$NPM_BIN" bash "$FRONTEND_ENTRY" >"$FRONTEND_LOG" 2>&1 &
FRONTEND_PID=$!

show_failure_log() {
  local label=$1
  local log_file=$2
  echo "[source-quickstart] last $label log lines ($log_file):" >&2
  tail -40 "$log_file" >&2 || true
}

health_is_gateway() {
  local body=$1
  node -e '
try {
  const value = JSON.parse(process.argv[1]);
  if (!value || Array.isArray(value) || typeof value !== "object" ||
      value.status !== "healthy" || value.service !== "gateway") {
    process.exitCode = 1;
  }
} catch (_error) {
  process.exitCode = 1;
}' "$body"
}

BACKEND_HEALTH_URL="http://127.0.0.1:$BACKEND_PORT/health"
WEB_HEALTH_URL="http://127.0.0.1:$WEB_PORT/api/health"
LOGIN_URL="http://127.0.0.1:$WEB_PORT/#/login"
deadline=$(( $(date +%s) + STARTUP_TIMEOUT ))
ready=false
while (( $(date +%s) <= deadline )); do
  if ! kill -0 "$BACKEND_PID" 2>/dev/null; then
    show_failure_log backend "$BACKEND_LOG"
    fail "backend exited before readiness"
  fi
  if ! kill -0 "$FRONTEND_PID" 2>/dev/null; then
    show_failure_log frontend "$FRONTEND_LOG"
    fail "frontend exited before readiness"
  fi
  backend_health=$(curl --silent --show-error --max-time 2 "$BACKEND_HEALTH_URL" 2>/dev/null || true)
  web_health=$(curl --silent --show-error --max-time 2 "$WEB_HEALTH_URL" 2>/dev/null || true)
  web_document=$(curl --silent --show-error --max-time 2 "http://127.0.0.1:$WEB_PORT/" 2>/dev/null || true)
  if health_is_gateway "$backend_health" && health_is_gateway "$web_health" &&
        [[ "$web_document" == *'<html'* && "$web_document" == *'id="root"'* ]]; then
    ready=true
    break
  fi
  sleep .2
done
if [[ "$ready" != true ]]; then
  show_failure_log backend "$BACKEND_LOG"
  show_failure_log frontend "$FRONTEND_LOG"
  fail "services did not become healthy within ${STARTUP_TIMEOUT}s"
fi

echo "READY_URL=$LOGIN_URL"
echo "BACKEND_HEALTH_URL=$BACKEND_HEALTH_URL"
echo "STATE_DIR=$STATE_DIR"
echo "BACKEND_LOG=$BACKEND_LOG"
echo "FRONTEND_LOG=$FRONTEND_LOG"

open_browser() {
  if grep -qi microsoft /proc/version 2>/dev/null && command -v powershell.exe >/dev/null 2>&1; then
    powershell.exe -NoProfile -NonInteractive -Command 'Start-Process -FilePath $args[0]' "$LOGIN_URL"
    echo "BROWSER_OPEN=windows-default"
  elif command -v xdg-open >/dev/null 2>&1; then
    xdg-open "$LOGIN_URL"
    echo "BROWSER_OPEN=xdg-default"
  elif [[ "$(uname -s)" == Darwin ]] && command -v open >/dev/null 2>&1; then
    open "$LOGIN_URL"
    echo "BROWSER_OPEN=macos-default"
  else
    return 1
  fi
}
if [[ "$OPEN_BROWSER" == true ]]; then
  open_browser || echo "BROWSER_OPEN=failed; open $LOGIN_URL manually" >&2
else
  echo "BROWSER_OPEN=not-requested; open $LOGIN_URL manually"
fi

echo '[source-quickstart] ready; press Ctrl+C to stop the source hosts'
while true; do
  sleep 1
  if ! kill -0 "$BACKEND_PID" 2>/dev/null; then
    show_failure_log backend "$BACKEND_LOG"
    fail "backend exited after readiness"
  fi
  if ! kill -0 "$FRONTEND_PID" 2>/dev/null; then
    show_failure_log frontend "$FRONTEND_LOG"
    fail "frontend exited after readiness"
  fi
done
