#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
GATE="$SCRIPT_DIR/goal-run-doctor-gate.sh"
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

write_doctor() {
  local path="$1"
  local pass="$2"
  local partial="$3"
  local missing="$4"
  local fail="$5"
  cat >"$path" <<JSON
{"result":{"summary":{"pass":$pass,"partial":$partial,"missing":$missing,"fail":$fail},"areas":[{"name":"query-engine","status":"pass"},{"name":"model-runner","status":"partial"},{"name":"broken","status":"fail"},{"name":"absent","status":"missing"}]}}
JSON
}

expect_success() {
  local name="$1"
  shift
  if ! "$@" >"$TMP_DIR/$name.out" 2>"$TMP_DIR/$name.err"; then
    echo "expected success for $name" >&2
    cat "$TMP_DIR/$name.err" >&2 || true
    exit 1
  fi
}

expect_failure() {
  local name="$1"
  shift
  if "$@" >"$TMP_DIR/$name.out" 2>"$TMP_DIR/$name.err"; then
    echo "expected failure for $name" >&2
    cat "$TMP_DIR/$name.out" >&2 || true
    exit 1
  fi
}

write_doctor "$TMP_DIR/pass-with-partial.json" 9 2 0 0
write_doctor "$TMP_DIR/all-pass.json" 11 0 0 0
write_doctor "$TMP_DIR/missing.json" 9 1 1 0
write_doctor "$TMP_DIR/fail.json" 9 1 0 1

expect_success "default-allows-partial" bash "$GATE" "$TMP_DIR/pass-with-partial.json"
expect_failure "default-blocks-missing" bash "$GATE" "$TMP_DIR/missing.json"
expect_failure "default-blocks-fail" bash "$GATE" "$TMP_DIR/fail.json"
expect_failure "strict-blocks-partial" env SYNON_GOAL_STRICT=1 bash "$GATE" "$TMP_DIR/pass-with-partial.json"
expect_success "strict-allows-explicit-external-partial" env SYNON_GOAL_STRICT=1 SYNON_GOAL_ALLOWED_PARTIAL=model-runner bash "$GATE" "$TMP_DIR/pass-with-partial.json"
expect_failure "strict-rejects-different-allowed-partial" env SYNON_GOAL_STRICT=1 SYNON_GOAL_ALLOWED_PARTIAL=synon-link-im bash "$GATE" "$TMP_DIR/pass-with-partial.json"
expect_success "strict-all-pass" env SYNON_GOAL_STRICT=1 bash "$GATE" "$TMP_DIR/all-pass.json"

echo "goal-run-doctor-gate-test: ok"
