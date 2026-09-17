#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
TMP_DIR=$(mktemp -d)
host_pid=""

cleanup() {
  if [[ -n "$host_pid" ]] && kill -0 "$host_pid" 2>/dev/null; then
    kill -TERM "$host_pid" 2>/dev/null || true
    wait "$host_pid" 2>/dev/null || true
  fi
  if [[ -f "$TMP_DIR/fake-npm/workers" ]]; then
    while IFS= read -r worker_pid; do
      if [[ "$worker_pid" =~ ^[0-9]+$ ]] && kill -0 "$worker_pid" 2>/dev/null; then
        kill -KILL "$worker_pid" 2>/dev/null || true
      fi
    done <"$TMP_DIR/fake-npm/workers"
  fi
  case "$TMP_DIR" in
    /tmp/tmp.*) rm -rf -- "$TMP_DIR" ;;
    *) echo "refusing to remove unexpected frontend-host test path: $TMP_DIR" >&2 ;;
  esac
}
trap cleanup EXIT

source_frontend="$TMP_DIR/canonical-frontend"
state_root="$TMP_DIR/runtime-state"
fake_state="$TMP_DIR/fake-npm"
mkdir -p "$source_frontend/packages/desktop/src" "$fake_state"
printf '{"version":1}\n' >"$source_frontend/package.json"
printf '{"lockfileVersion":3}\n' >"$source_frontend/package-lock.json"
printf '{}\n' >"$source_frontend/packages/desktop/package.json"
printf 'export const ready = true;\n' >"$source_frontend/packages/desktop/src/app.ts"

event_count() {
  local event="$1"
  if [[ ! -f "$fake_state/events" ]]; then
    printf '0\n'
    return
  fi
  grep -c "^$event$" "$fake_state/events" || true
}

wait_for_event_count() {
  local event="$1"
  local expected="$2"
  local count
  for _ in $(seq 1 200); do
    count=$(event_count "$event")
    if (( count >= expected )); then
      return
    fi
    sleep 0.05
  done
  echo "timed out waiting for fake npm event $event count $expected" >&2
  cat "$fake_state/events" 2>/dev/null || true
  cat "$TMP_DIR/host.log" 2>/dev/null || true
  exit 1
}

FAKE_NPM_STATE="$fake_state" \
SYNON_DEV_FRONTEND_SOURCE="$source_frontend" \
SYNON_DEV_FRONTEND_RUNTIME_STATE="$state_root" \
NPM="$ROOT_DIR/scripts/dev/testdata/fake-npm.sh" \
  bash "$ROOT_DIR/scripts/dev/source-frontend-host.sh" >"$TMP_DIR/host.log" 2>&1 &
host_pid=$!

wait_for_event_count ci 1
wait_for_event_count run 1
grep -Fqx "$source_frontend" "$fake_state/ci-cwds"
grep -Fqx "$source_frontend" "$fake_state/run-cwds"
test ! -e "$state_root/source"
test ! -e "$state_root/node_modules"

# Ordinary source changes stay in the canonical tree and remain Vite's HMR
# responsibility; the host must neither copy nor restart for them.
printf 'export const ready = false;\n' >"$source_frontend/packages/desktop/src/app.ts"
sleep 0.3
[[ "$(event_count ci)" == "1" ]]
[[ "$(event_count run)" == "1" ]]
grep -Fq 'ready = false' "$source_frontend/packages/desktop/src/app.ts"

# A competing host using the same state root must fail without disturbing the
# active instance.
if FAKE_NPM_STATE="$fake_state" \
  SYNON_DEV_FRONTEND_SOURCE="$source_frontend" \
  SYNON_DEV_FRONTEND_RUNTIME_STATE="$state_root" \
  NPM="$ROOT_DIR/scripts/dev/testdata/fake-npm.sh" \
    bash "$ROOT_DIR/scripts/dev/source-frontend-host.sh" >"$TMP_DIR/second.log" 2>&1; then
  echo "a second frontend host acquired the same runtime lock" >&2
  exit 1
fi
grep -Fq 'already running' "$TMP_DIR/second.log"

# Dependency metadata changes stop the old process group, install from the
# canonical package authority, and then start one replacement.
printf '{"version":2}\n' >"$source_frontend/package.json"
wait_for_event_count ci 2
wait_for_event_count run 2
[[ "$(tail -n 1 "$fake_state/ci-cwds")" == "$source_frontend" ]]
[[ "$(tail -n 1 "$fake_state/run-cwds")" == "$source_frontend" ]]

kill -TERM "$host_pid"
wait "$host_pid"
host_pid=""
while IFS= read -r worker_pid; do
  if kill -0 "$worker_pid" 2>/dev/null; then
    echo "frontend process-group worker survived host shutdown: $worker_pid" >&2
    exit 1
  fi
done <"$fake_state/workers"
test ! -e "$state_root/host.pid"
test ! -e "$state_root/frontend.pgid"

# A failed dependency install must be diagnostic and must not launch Vite from
# a stale node_modules tree.
failure_source="$TMP_DIR/failure-frontend"
failure_state="$TMP_DIR/failure-state"
failure_fake_state="$TMP_DIR/failure-fake-npm"
mkdir -p "$failure_source/packages/desktop" \
  "$failure_source/node_modules/.bin" \
  "$failure_state" \
  "$failure_fake_state"
printf '{"version":1}\n' >"$failure_source/package.json"
printf '{"lockfileVersion":3}\n' >"$failure_source/package-lock.json"
printf '{}\n' >"$failure_source/packages/desktop/package.json"
printf '#!/usr/bin/env bash\nexit 0\n' >"$failure_source/node_modules/.bin/vite"
chmod 755 "$failure_source/node_modules/.bin/vite"
printf 'not-the-current-key\n' >"$failure_state/dependency-key"
: >"$failure_fake_state/fail-ci"
if FAKE_NPM_STATE="$failure_fake_state" \
  SYNON_DEV_FRONTEND_SOURCE="$failure_source" \
  SYNON_DEV_FRONTEND_RUNTIME_STATE="$failure_state" \
  NPM="$ROOT_DIR/scripts/dev/testdata/fake-npm.sh" \
    bash "$ROOT_DIR/scripts/dev/source-frontend-host.sh" >"$TMP_DIR/failure.log" 2>&1; then
  echo "frontend host hid a dependency installation failure" >&2
  exit 1
fi
grep -Fq 'dependency installation failed' "$TMP_DIR/failure.log"
test ! -e "$failure_fake_state/events"

echo "source-frontend-host-test: ok"
