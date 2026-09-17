#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT_DIR=$(cd "$SCRIPT_DIR/.." && pwd)
GO_BIN="${GO:-go}"
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

pick_port() {
  local port
  for port in $(seq 18800 18880); do
    if command -v ss >/dev/null 2>&1; then
      if [[ -z "$(ss -H -ltn "sport = :$port")" ]]; then
        echo "$port"
        return 0
      fi
    elif ! timeout 1 bash -c "echo >/dev/tcp/127.0.0.1/$port" >/dev/null 2>&1; then
      echo "$port"
      return 0
    fi
  done
  echo "ERROR: no free port found for strict goal-run test" >&2
  return 1
}

BIN="$TMP_DIR/synon-go"
(
  cd "$ROOT_DIR"
  "$GO_BIN" build -buildvcs=false -o "$BIN" ./cmd/synon
)

PORT=$(pick_port)
OUT="$TMP_DIR/goal-run-strict.out"
SYNON_GOAL_HOME="$TMP_DIR/home" \
SYNON_GOAL_PORT="$PORT" \
  bash "$SCRIPT_DIR/goal-run-strict-config.sh" "$BIN" >"$OUT"

grep -q 'doctor-gate: .*strict=1' "$OUT"
grep -q 'model-runner pass' "$OUT"
grep -q 'compact-memory pass' "$OUT"
grep -q 'Goal run finished.' "$OUT"

echo "goal-run-strict-config-test: ok"
