#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
SCENARIO=""
OUTPUT=""
PORT="38991"
STARTUP_TIMEOUT_SECONDS="30"

usage() {
  cat <<'USAGE'
Usage: scripts/compat/capture-go.sh \
  --scenario <repository-relative-scenario.json> \
  --output <repository-relative-capture.json> \
  [--port <loopback-port>] \
  [--startup-timeout <seconds>]

The script builds the current Go source, starts it with isolated temporary
state, captures the HTTP scenario, and always terminates the candidate process.
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
  --scenario)
    SCENARIO="${2:-}"
    shift 2
    ;;
  --output)
    OUTPUT="${2:-}"
    shift 2
    ;;
  --port)
    PORT="${2:-}"
    shift 2
    ;;
  --startup-timeout)
    STARTUP_TIMEOUT_SECONDS="${2:-}"
    shift 2
    ;;
  --help|-h)
    usage
    exit 0
    ;;
  *)
    echo "ERROR: unknown argument $1" >&2
    usage >&2
    exit 2
    ;;
  esac
done

if [[ -z "$SCENARIO" || -z "$OUTPUT" ]]; then
  echo "ERROR: --scenario and --output are required" >&2
  usage >&2
  exit 2
fi
if [[ ! "$PORT" =~ ^[0-9]+$ ]] || ((PORT < 1024 || PORT > 65535)); then
  echo "ERROR: --port must be an integer from 1024 through 65535" >&2
  exit 2
fi
if [[ ! "$STARTUP_TIMEOUT_SECONDS" =~ ^[0-9]+$ ]] || ((STARTUP_TIMEOUT_SECONDS < 1 || STARTUP_TIMEOUT_SECONDS > 300)); then
  echo "ERROR: --startup-timeout must be an integer from 1 through 300" >&2
  exit 2
fi
if ! command -v go >/dev/null 2>&1; then
  for candidate in "$HOME/.local/go-1.26.0/bin" "$HOME/.local/go-1.22.5/bin"; do
    if [[ -x "$candidate/go" ]]; then
      export PATH="$candidate:$PATH"
      break
    fi
  done
fi
if ! command -v go >/dev/null 2>&1; then
  echo "ERROR: Go is required to build the candidate runtime" >&2
  exit 1
fi
if ss -ltn "( sport = :$PORT )" | grep -q ":$PORT"; then
  echo "ERROR: loopback port $PORT is already in use" >&2
  exit 1
fi

STATE_DIR=$(mktemp -d "/tmp/synon-go-oracle.XXXXXX")
HOME_DIR="$STATE_DIR/home"
USER_HOME="$STATE_DIR/user-home"
SERVER_BIN="$STATE_DIR/synon-go"
SERVER_LOG="$STATE_DIR/server.log"
SERVER_PID=""
mkdir -p "$HOME_DIR" "$USER_HOME/.config"

cleanup() {
  local exit_code=$?
  if [[ -n "$SERVER_PID" ]] && kill -0 "$SERVER_PID" >/dev/null 2>&1; then
    kill -TERM "$SERVER_PID" >/dev/null 2>&1 || true
    for _ in $(seq 1 40); do
      if ! kill -0 "$SERVER_PID" >/dev/null 2>&1; then
        break
      fi
      sleep 0.1
    done
    if kill -0 "$SERVER_PID" >/dev/null 2>&1; then
      kill -KILL "$SERVER_PID" >/dev/null 2>&1 || true
    fi
    wait "$SERVER_PID" >/dev/null 2>&1 || true
  fi
  case "$STATE_DIR" in
  /tmp/synon-go-oracle.*)
    rm -rf -- "$STATE_DIR"
    ;;
  *)
    echo "ERROR: refusing to remove unexpected state path $STATE_DIR" >&2
    exit 1
    ;;
  esac
  exit "$exit_code"
}
trap cleanup EXIT INT TERM

cd "$ROOT_DIR"
go build -trimpath -o "$SERVER_BIN" ./cmd/synon

SERVER_ENV=(
  "HOME=$USER_HOME"
  "XDG_CONFIG_HOME=$USER_HOME/.config"
  "SYNON_ADDRESS=127.0.0.1:$PORT"
  "SYNON_RUNNER_ENABLED=${SYNON_GO_RUNNER_ENABLED:-false}"
)
if [[ "${SYNON_GO_ORACLE_DEFAULT_DATA_DIR:-false}" != "true" ]]; then
  SERVER_ENV+=("SYNON_HOME=$HOME_DIR")
fi
env "${SERVER_ENV[@]}" "$SERVER_BIN" serve >"$SERVER_LOG" 2>&1 &
SERVER_PID=$!

ready=0
for _ in $(seq 1 $((STARTUP_TIMEOUT_SECONDS * 4))); do
  if curl --noproxy localhost,127.0.0.1 --connect-timeout 1 --max-time 2 -fsS "http://127.0.0.1:$PORT/health" >/dev/null 2>&1; then
    ready=1
    break
  fi
  if ! kill -0 "$SERVER_PID" >/dev/null 2>&1; then
    echo "ERROR: Go candidate exited before readiness" >&2
    tail -100 "$SERVER_LOG" >&2
    exit 1
  fi
  sleep 0.25
done
if ((ready == 0)); then
  echo "ERROR: Go candidate did not become ready in $STARTUP_TIMEOUT_SECONDS seconds" >&2
  tail -100 "$SERVER_LOG" >&2
  exit 1
fi

"$SERVER_BIN" contracts capture-http \
  --root "$ROOT_DIR" \
  --scenario "$SCENARIO" \
  --base-url "http://127.0.0.1:$PORT" \
  --runtime synon-go-v4.0.2 \
  --output "$OUTPUT" \
  --force
