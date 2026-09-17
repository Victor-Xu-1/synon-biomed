#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT_DIR=$(cd "$SCRIPT_DIR/.." && pwd)
BIN="${1:-$ROOT_DIR/dist/synon-go}"
HOME_DIR=${SYNON_GOAL_HOME:-/tmp/synon-go-goal-run}
PORT=${SYNON_GOAL_PORT:-8765}
HOST=${SYNON_GOAL_HOST:-127.0.0.1}
BASE="http://$HOST:$PORT"
CURL=(curl --noproxy 127.0.0.1,localhost --connect-timeout 2 --max-time 15 -fsS)

if [[ ! -x "$BIN" ]]; then
  echo "ERROR: binary missing or not executable: $BIN" >&2
  exit 1
fi

HOME_DIR=$(realpath -m "$HOME_DIR")
if [[ "$HOME_DIR" == "/" || "$HOME_DIR" == "$ROOT_DIR" ]]; then
	echo "ERROR: refusing unsafe goal-run home: $HOME_DIR" >&2
	exit 1
fi

rm -rf "$HOME_DIR"
mkdir -p "$HOME_DIR"
export SYNON_HOME="$HOME_DIR"
export SYNON_ADDRESS="$HOST:$PORT"

mkdir -p "$HOME_DIR/logs"
PID_FILE="$HOME_DIR/synon-go-goal.pid"
LOG_FILE="$HOME_DIR/synon-go-goal.log"

cleanup() {
  if [[ -s "$PID_FILE" ]]; then
    local pid
    pid=$(cat "$PID_FILE")
    if kill -0 "$pid" >/dev/null 2>&1; then
      kill "$pid" || true
      for _ in {1..40}; do
        sleep 0.1
        if ! kill -0 "$pid" >/dev/null 2>&1; then
          break
        fi
      done
      if kill -0 "$pid" >/dev/null 2>&1; then
        kill -9 "$pid" || true
      fi
    fi
  fi
}
trap cleanup EXIT

(
  cd "$ROOT_DIR"
  "$BIN" >"$LOG_FILE" 2>&1 &
  echo $! > "$PID_FILE"
)

for i in {1..80}; do
  if "${CURL[@]}" "$BASE/health" >/dev/null 2>&1; then
    break
  fi
  pid=$(cat "$PID_FILE")
  if ! kill -0 "$pid" >/dev/null 2>&1; then
    echo "ERROR: runtime exited before becoming healthy." >&2
    echo "--- synon-go-goal log ---" >&2
    tail -n 80 "$LOG_FILE" >&2
    exit 1
  fi
  sleep 0.25
  if [[ $i -eq 80 ]]; then
    echo "ERROR: server not healthy after startup." >&2
    echo "--- synon-go-goal log ---"
    tail -n 80 "$LOG_FILE"
    exit 1
  fi
done

echo "Goal Run: runtime startup ok"
echo "binary=$BIN"
echo "home=$HOME_DIR"
echo "base=$BASE"

echo "health:"
"${CURL[@]}" "$BASE/health" | tee "$HOME_DIR/health.json"

echo
echo "tools:"
TOOL_COUNT=$("${CURL[@]}" "$BASE/api/tools" | tee "$HOME_DIR/tools.json" | jq '.tools | length')
echo "tool_count=$TOOL_COUNT"
if [[ "$TOOL_COUNT" -lt 1 ]]; then
  echo "ERROR: tool registry is empty." >&2
  exit 1
fi

echo
echo "agent-runtime-doctor-all:"
"${CURL[@]}" -H "Content-Type: application/json" -d '{"input":{"scope":"all"}}' \
  "$BASE/api/tools/AgentRuntimeDoctor/execute" | tee "$HOME_DIR/doctor-all.json"

PASS=$(jq '.result.summary.pass // 0' "$HOME_DIR/doctor-all.json")
PARTIAL=$(jq '.result.summary.partial // 0' "$HOME_DIR/doctor-all.json")
MISSING=$(jq '.result.summary.missing // 0' "$HOME_DIR/doctor-all.json")
FAIL=$(jq '.result.summary.fail // 0' "$HOME_DIR/doctor-all.json")

echo
echo "doctor-summary:$PASS/$((PASS + PARTIAL + MISSING + FAIL)) pass partial missing fail=$PASS $PARTIAL $MISSING $FAIL"
bash "$SCRIPT_DIR/goal-run-doctor-gate.sh" "$HOME_DIR/doctor-all.json"

echo
echo "areas:"
jq -r '.result.areas[] | "\(.name) \(.status)"' "$HOME_DIR/doctor-all.json"

if jq -e '.result.summary.missing > 0' "$HOME_DIR/doctor-all.json" >/dev/null 2>&1; then
  echo "missing-areas:"
  jq -r '.result.areas[] | select(.status=="missing") | .name' "$HOME_DIR/doctor-all.json"
fi

echo
echo "scope-doctors:"
for scope in query-engine model-runner tool-gateway permissions hooks skills sessions compact-memory taskrun-agent mcp synon-link-im; do
  SCOPE_RESULT=$("${CURL[@]}" -H "Content-Type: application/json" -d "{\"input\":{\"scope\":\"$scope\"}}" \
    "$BASE/api/tools/AgentRuntimeDoctor/execute" )
  STATUS=$(echo "$SCOPE_RESULT" | jq -r '.result.summary.status? // .result.areas[0].status // "n/a"')
  echo "  ${scope}: ${STATUS}"
done

echo "Goal run finished."
