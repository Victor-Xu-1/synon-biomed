#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
BASELINE_ROOT="${SYNON_V11_ROOT:-/home/victor_1/synonbiomed-v1.1}"
SCENARIO=""
OUTPUT=""
PORT="38990"
STARTUP_TIMEOUT_SECONDS="30"
BUN_BIN="${BUN_BIN:-}"
ORACLE_BIN="${SYNON_ORACLE_BIN:-}"

usage() {
  cat <<'USAGE'
Usage: scripts/compat/capture-v11.sh \
  --scenario <repository-relative-scenario.json> \
  --output <repository-relative-capture.json> \
  [--baseline-root <read-only-v1.1-root>] \
  [--port <loopback-port>] \
  [--startup-timeout <seconds>]

The script executes the shipped v1.1 Bun bundle directly with an isolated /tmp
data directory. It never calls the baseline run_serve.sh or patch script.
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
  --baseline-root)
    BASELINE_ROOT="${2:-}"
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

BASELINE_ROOT=$(realpath "$BASELINE_ROOT")
SERVER_ENTRY="$BASELINE_ROOT/runtime/server/synonbiomed.bundle.js"
ASSETS_ROOT="$BASELINE_ROOT/runtime/assets"
CONFIG_PATH="$BASELINE_ROOT/synonbiomed.config.toml"
for required in "$SERVER_ENTRY" "$ASSETS_ROOT" "$CONFIG_PATH"; do
  if [[ ! -e "$required" ]]; then
    echo "ERROR: missing v1.1 baseline component $required" >&2
    exit 1
  fi
done

if [[ -z "$BUN_BIN" ]]; then
  if command -v bun >/dev/null 2>&1; then
    BUN_BIN=$(command -v bun)
  elif [[ -x "$HOME/.bun/bin/bun" ]]; then
    BUN_BIN="$HOME/.bun/bin/bun"
  else
    echo "ERROR: Bun is required only to execute the read-only v1.1 baseline" >&2
    exit 1
  fi
fi

if ss -ltn "( sport = :$PORT )" | grep -q ":$PORT"; then
  echo "ERROR: loopback port $PORT is already in use" >&2
  exit 1
fi

STATE_DIR=$(mktemp -d "/tmp/synon-v11-oracle.XXXXXX")
USER_HOME="$STATE_DIR/user-home"
SERVER_LOG="$STATE_DIR/server.log"
ORACLE_CONFIG="$STATE_DIR/synonbiomed.config.toml"
SERVER_PID=""
mkdir -p "$USER_HOME/.config"

if ! awk -v conda_home="$STATE_DIR/conda" '
  BEGIN { replaced = 0 }
  /^[[:space:]]*conda_home[[:space:]]*=/ {
    printf "conda_home = \"%s\"\n", conda_home
    replaced++
    next
  }
  { print }
  END { if (replaced != 1) exit 42 }
' "$CONFIG_PATH" >"$ORACLE_CONFIG"; then
  echo "ERROR: baseline config must contain exactly one paths.conda_home assignment" >&2
  exit 1
fi

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
  /tmp/synon-v11-oracle.*)
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

export NO_PROXY="${NO_PROXY:-localhost,127.0.0.1,::1}"
export no_proxy="${no_proxy:-$NO_PROXY}"
export PYTHONPATH="$ASSETS_ROOT/skills/cheminfo-render${PYTHONPATH:+:$PYTHONPATH}"

SERVER_ENV=(
  "HOME=$USER_HOME"
  "XDG_CONFIG_HOME=$USER_HOME/.config"
  "SYNON_SESSION_DIR=$STATE_DIR/session"
  "SYNON_CONDA_HOME=$STATE_DIR/conda"
  "SYNON_ASSETS_ROOT=$ASSETS_ROOT"
  "SYNON_LLM_BASE_URL=${SYNON_V11_LLM_BASE_URL:-http://127.0.0.1:$PORT/synon-llm}"
  "SYNON_LLM_API_KEY=synon-local-core"
)
SERVER_ARGS=(
  "$SERVER_ENTRY" serve
  --config "$ORACLE_CONFIG"
  --assets-root "$ASSETS_ROOT"
  --host 127.0.0.1
  --port "$PORT"
)
if [[ "${SYNON_V11_ORACLE_DEFAULT_DATA_DIR:-false}" != "true" ]]; then
  SERVER_ENV+=("SYNON_DATA_DIR=$STATE_DIR")
  SERVER_ARGS+=(--data-dir "$STATE_DIR")
fi
env "${SERVER_ENV[@]}" "$BUN_BIN" "${SERVER_ARGS[@]}" >"$SERVER_LOG" 2>&1 &
SERVER_PID=$!

ready=0
for _ in $(seq 1 $((STARTUP_TIMEOUT_SECONDS * 4))); do
  if curl --noproxy localhost,127.0.0.1 --connect-timeout 1 --max-time 2 -fsS "http://127.0.0.1:$PORT/health" >/dev/null 2>&1; then
    ready=1
    break
  fi
  if ! kill -0 "$SERVER_PID" >/dev/null 2>&1; then
    echo "ERROR: v1.1 baseline exited before readiness" >&2
    tail -100 "$SERVER_LOG" >&2
    exit 1
  fi
  sleep 0.25
done
if ((ready == 0)); then
  echo "ERROR: v1.1 baseline did not become ready in $STARTUP_TIMEOUT_SECONDS seconds" >&2
  tail -100 "$SERVER_LOG" >&2
  exit 1
fi

cd "$ROOT_DIR"
if [[ -n "$ORACLE_BIN" ]]; then
  "$ORACLE_BIN" contracts capture-http \
    --root "$ROOT_DIR" \
    --scenario "$SCENARIO" \
    --base-url "http://127.0.0.1:$PORT" \
    --runtime synonbiomed-v1.1 \
    --output "$OUTPUT" \
    --force
else
  if ! command -v go >/dev/null 2>&1; then
    for candidate in "$HOME/.local/go-1.26.0/bin" "$HOME/.local/go-1.22.5/bin"; do
      if [[ -x "$candidate/go" ]]; then
        export PATH="$candidate:$PATH"
        break
      fi
    done
  fi
  if ! command -v go >/dev/null 2>&1; then
    echo "ERROR: Go is required to run the development capture CLI" >&2
    exit 1
  fi
  go run ./cmd/synon contracts capture-http \
    --root "$ROOT_DIR" \
    --scenario "$SCENARIO" \
    --base-url "http://127.0.0.1:$PORT" \
	  --runtime synonbiomed-v1.1 \
	  --output "$OUTPUT" \
	  --force
fi
