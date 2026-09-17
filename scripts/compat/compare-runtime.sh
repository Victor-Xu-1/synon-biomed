#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
SCENARIO=""
BASELINE=""
CANDIDATE=""
ORACLE_BIN="${SYNON_ORACLE_BIN:-}"

while [[ $# -gt 0 ]]; do
  case "$1" in
  --scenario)
    SCENARIO="${2:-}"
    shift 2
    ;;
  --baseline)
    BASELINE="${2:-}"
    shift 2
    ;;
  --candidate)
    CANDIDATE="${2:-}"
    shift 2
    ;;
  --help|-h)
    echo "Usage: scripts/compat/compare-runtime.sh --scenario <path> --baseline <path> --candidate <path>"
    exit 0
    ;;
  *)
    echo "ERROR: unknown argument $1" >&2
    exit 2
    ;;
  esac
done

if [[ -z "$SCENARIO" || -z "$BASELINE" || -z "$CANDIDATE" ]]; then
  echo "ERROR: --scenario, --baseline, and --candidate are required" >&2
  exit 2
fi

cd "$ROOT_DIR"
if [[ -n "$ORACLE_BIN" ]]; then
  exec "$ORACLE_BIN" contracts compare \
    --root "$ROOT_DIR" \
    --scenario "$SCENARIO" \
    --baseline "$BASELINE" \
    --candidate "$CANDIDATE"
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
  echo "ERROR: Go is required to run the development compare CLI" >&2
  exit 1
fi
exec go run ./cmd/synon contracts compare \
  --root "$ROOT_DIR" \
  --scenario "$SCENARIO" \
  --baseline "$BASELINE" \
  --candidate "$CANDIDATE"
