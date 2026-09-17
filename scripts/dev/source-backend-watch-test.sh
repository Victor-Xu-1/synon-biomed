#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
WATCHER="$SCRIPT_DIR/source-backend-watch.sh"
TMP_DIR=$(mktemp -d)
ROOT="$TMP_DIR/source"
STATE="$TMP_DIR/state"
BUILD_COUNT="$TMP_DIR/build-count"
START_COUNT="$TMP_DIR/start-count"
FAIL_NEXT_BUILD="$TMP_DIR/fail-next-build"
FAIL_NEXT_ASSET_VERIFY="$TMP_DIR/fail-next-asset-verify"
RUNTIME_HEALTH="$TMP_DIR/runtime-health.json"
WATCHER_PID=""

cleanup() {
  if [[ -n "$WATCHER_PID" ]] && kill -0 "$WATCHER_PID" 2>/dev/null; then
    kill -TERM "$WATCHER_PID" 2>/dev/null || true
    wait "$WATCHER_PID" 2>/dev/null || true
  fi
  rm -rf -- "$TMP_DIR"
}
trap cleanup EXIT INT TERM

mkdir -p \
  "$ROOT/cmd" \
  "$ROOT/internal" \
	"$ROOT/internal/testdata" \
	"$ROOT/internal/persistence/workspace" \
  "$ROOT/assets" \
  "$ROOT/skills" \
  "$ROOT/scripts/dev" \
  "$ROOT/scripts/product-identity" \
  "$STATE"
[[ ! -e "$ROOT/tools" ]] || { echo 'fixture unexpectedly contains retired root tools directory' >&2; exit 1; }
printf 'module example.invalid/source-backend-watch-test\n' >"$ROOT/go.mod"
printf 'package internal\n' >"$ROOT/internal/backend.go"
printf 'fixture v1\n' >"$ROOT/internal/testdata/input.txt"
printf 'package workspace\n' >"$ROOT/internal/persistence/workspace/versioned_schema.go"
printf 'package workspace\n' >"$ROOT/internal/persistence/workspace/example_schema_migration.go"
printf 'package workspace\n' >"$ROOT/internal/persistence/workspace/example_schema_migration_test.go"
printf '#!/usr/bin/env bash\n' >"$ROOT/scripts/dev/frontend-only.sh"
printf '{"active_session_runs":0,"runtime_draining":false}\n' >"$RUNTIME_HEALTH"

cat >"$TMP_DIR/fake-go" <<'FAKE_GO'
#!/usr/bin/env bash
set -Eeuo pipefail
if [[ "${1:-}" == "env" && "${2:-}" == "GOVERSION" ]]; then
  echo go1.26.5
  exit 0
fi
if [[ "${1:-}" == "run" ]]; then
  printf 'synon-biomed\tSynon Biomed\n'
  exit 0
fi
if [[ "${1:-}" != "build" ]]; then
  echo "unexpected fake go invocation: $*" >&2
  exit 2
fi
output=""
while (( $# > 0 )); do
  if [[ "$1" == "-o" ]]; then
    shift
    output="${1:-}"
    break
  fi
  shift
done
[[ -n "$output" ]] || { echo 'fake go build did not receive -o' >&2; exit 2; }
count=0
[[ ! -f "$FAKE_BUILD_COUNT" ]] || count=$(<"$FAKE_BUILD_COUNT")
printf '%s\n' "$((count + 1))" >"$FAKE_BUILD_COUNT"
if [[ -f "$FAKE_FAIL_NEXT_BUILD" ]]; then
  rm -f "$FAKE_FAIL_NEXT_BUILD"
  echo 'injected build failure' >&2
  exit 1
fi
cat >"$output" <<'FAKE_BACKEND'
#!/usr/bin/env bash
set -Eeuo pipefail
if [[ "${1:-}" == "assets" && "${2:-}" == "verify" ]]; then
  if [[ -f "$FAKE_FAIL_NEXT_ASSET_VERIFY" ]]; then
    rm -f "$FAKE_FAIL_NEXT_ASSET_VERIFY"
    echo 'injected asset verification failure' >&2
    exit 1
  fi
  exit 0
fi
count=0
[[ ! -f "$FAKE_START_COUNT" ]] || count=$(<"$FAKE_START_COUNT")
printf '%s\n' "$((count + 1))" >"$FAKE_START_COUNT"
trap 'exit 0' INT TERM USR1
while true; do sleep 0.1; done
FAKE_BACKEND
FAKE_GO
chmod 0755 "$TMP_DIR/fake-go"
ln -s "$TMP_DIR/fake-go" "$TMP_DIR/go"
cat >"$TMP_DIR/old-go" <<'OLD_GO'
#!/usr/bin/env bash
[[ "${1:-}" == env && "${2:-}" == GOVERSION ]] && { echo go1.25.9; exit; }
exit 2
OLD_GO
chmod 0755 "$TMP_DIR/old-go"
if GO="$TMP_DIR/old-go" SYNON_SOURCE_ROOT="$ROOT" bash "$WATCHER" >"$TMP_DIR/old-go.log" 2>&1; then
  echo 'unsupported Go version passed backend watcher preflight' >&2
  exit 1
fi
grep -Fq 'Go 1.26 or newer is required; found: go1.25.9' "$TMP_DIR/old-go.log" || {
  echo 'unsupported Go version was not diagnosed' >&2
  cat "$TMP_DIR/old-go.log" >&2
  exit 1
}

wait_for_value() {
  local file="$1"
  local expected="$2"
  for _ in $(seq 1 100); do
    if [[ -f "$file" ]] && [[ "$(<"$file")" == "$expected" ]]; then
      return 0
    fi
    sleep 0.05
  done
  echo "timed out waiting for $file=$expected" >&2
  return 1
}

FAKE_BUILD_COUNT="$BUILD_COUNT" \
FAKE_START_COUNT="$START_COUNT" \
FAKE_FAIL_NEXT_BUILD="$FAIL_NEXT_BUILD" \
FAKE_FAIL_NEXT_ASSET_VERIFY="$FAIL_NEXT_ASSET_VERIFY" \
SYNON_SOURCE_ROOT="$ROOT" \
SYNON_DEV_BUILD_DIR="$STATE" \
SYNON_DEV_POLL_SECONDS=0.05 \
SYNON_DEV_BUILD_RETRY_MIN_SECONDS=1 \
SYNON_DEV_BUILD_RETRY_MAX_SECONDS=2 \
SYNON_DEV_RUNTIME_HEALTH_FILE="$RUNTIME_HEALTH" \
PATH="$TMP_DIR:$PATH" \
  env -u GO bash "$WATCHER" >"$TMP_DIR/watcher.log" 2>&1 &
WATCHER_PID=$!

wait_for_value "$BUILD_COUNT" 1
wait_for_value "$START_COUNT" 1
[[ -s "$STATE/accepted-migration-digest" ]] || {
	echo 'initial verified build did not establish its migration digest' >&2
	exit 1
}

set +e
FAKE_BUILD_COUNT="$BUILD_COUNT" \
FAKE_START_COUNT="$START_COUNT" \
FAKE_FAIL_NEXT_BUILD="$FAIL_NEXT_BUILD" \
FAKE_FAIL_NEXT_ASSET_VERIFY="$FAIL_NEXT_ASSET_VERIFY" \
SYNON_SOURCE_ROOT="$ROOT" \
SYNON_DEV_BUILD_DIR="$STATE" \
SYNON_DEV_POLL_SECONDS=0.05 \
SYNON_DEV_BUILD_RETRY_MIN_SECONDS=1 \
SYNON_DEV_BUILD_RETRY_MAX_SECONDS=2 \
SYNON_DEV_RUNTIME_HEALTH_FILE="$RUNTIME_HEALTH" \
GO="$TMP_DIR/fake-go" \
  bash "$WATCHER" >"$TMP_DIR/second-watcher.log" 2>&1
second_status=$?
set -e
[[ "$second_status" == 73 ]] || {
  echo "second watcher status=$second_status, want 73" >&2
  exit 1
}
grep -q 'another watcher already owns this build directory' "$TMP_DIR/second-watcher.log" || {
  echo 'second watcher did not report the single-owner boundary' >&2
  exit 1
}
[[ "$(<"$BUILD_COUNT")" == 1 && "$(<"$START_COUNT")" == 1 ]] || {
  echo 'rejected watcher changed the active backend' >&2
  exit 1
}

# Go test files are verification inputs, not production binary or migration
# inputs. Editing one must not restart the user's live backend or require an
# operator to accept a new production schema digest.
printf '// migration test changed\n' >>"$ROOT/internal/persistence/workspace/example_schema_migration_test.go"
sleep 0.4
[[ "$(<"$BUILD_COUNT")" == 1 && "$(<"$START_COUNT")" == 1 ]] || {
	echo 'test-only migration change rebuilt or restarted the backend' >&2
	exit 1
}
[[ ! -e "$STATE/pending-migration-digest" ]] || {
	echo 'test-only migration change produced a pending production digest' >&2
	exit 1
}

# Go testdata directories are test fixtures and are ignored by production
# package builds. Editing a fixture must not rebuild or restart the live API.
printf 'fixture v2\n' >>"$ROOT/internal/testdata/input.txt"
sleep 0.4
[[ "$(<"$BUILD_COUNT")" == 1 && "$(<"$START_COUNT")" == 1 ]] || {
	echo 'testdata-only change rebuilt or restarted the backend' >&2
	exit 1
}

# Migration inputs must never hot-deploy into the active user database.  The
# watcher keeps the accepted binary until an independently verified exact
# digest is promoted by the operator.
printf '// migration changed\n' >>"$ROOT/internal/persistence/workspace/example_schema_migration.go"
for _ in $(seq 1 100); do
	[[ -s "$STATE/pending-migration-digest" ]] && break
	sleep 0.05
done
[[ -s "$STATE/pending-migration-digest" ]] || {
	echo 'migration change did not produce a pending digest' >&2
	exit 1
}
[[ "$(<"$BUILD_COUNT")" == 1 && "$(<"$START_COUNT")" == 1 ]] || {
	echo 'unaccepted migration change replaced the active backend' >&2
	exit 1
}
grep -q 'migration inputs changed; keeping the accepted backend' "$TMP_DIR/watcher.log" || {
	echo 'unaccepted migration change was not diagnosed' >&2
	exit 1
}

# A service restart while a production migration is pending must continue to
# serve the accepted binary, then resume the candidate build after an operator
# accepts the pending digest.
kill -TERM "$WATCHER_PID"
wait "$WATCHER_PID"
WATCHER_PID=""
FAKE_BUILD_COUNT="$BUILD_COUNT" \
FAKE_START_COUNT="$START_COUNT" \
FAKE_FAIL_NEXT_BUILD="$FAIL_NEXT_BUILD" \
FAKE_FAIL_NEXT_ASSET_VERIFY="$FAIL_NEXT_ASSET_VERIFY" \
SYNON_SOURCE_ROOT="$ROOT" \
SYNON_DEV_BUILD_DIR="$STATE" \
SYNON_DEV_POLL_SECONDS=0.05 \
SYNON_DEV_BUILD_RETRY_MIN_SECONDS=1 \
SYNON_DEV_BUILD_RETRY_MAX_SECONDS=2 \
SYNON_DEV_RUNTIME_HEALTH_FILE="$RUNTIME_HEALTH" \
GO="$TMP_DIR/fake-go" \
  bash "$WATCHER" >"$TMP_DIR/resumed-watcher.log" 2>&1 &
WATCHER_PID=$!
wait_for_value "$START_COUNT" 2
[[ "$(<"$BUILD_COUNT")" == 1 ]] || {
	echo 'restarted watcher built an unaccepted migration' >&2
	exit 1
}
cp "$STATE/pending-migration-digest" "$STATE/accepted-migration-digest"
wait_for_value "$BUILD_COUNT" 2
wait_for_value "$START_COUNT" 3

# Frontend supervision is outside the backend dependency graph and must not
# interrupt an active API process.
printf '# frontend host changed\n' >>"$ROOT/scripts/dev/frontend-only.sh"
sleep 0.4
[[ "$(<"$BUILD_COUNT")" == 2 ]] || {
  echo 'frontend-only scripts/dev change rebuilt the backend' >&2
  exit 1
}
[[ "$(<"$START_COUNT")" == 3 ]] || {
  echo 'frontend-only scripts/dev change restarted the backend' >&2
  exit 1
}

# A verified candidate must remain pending while a task is active. Once the
# runtime reports idle, SIGUSR1 closes the admission race and activates that
# exact already-built candidate without interrupting the task.
printf '{"active_session_runs":1,"runtime_draining":false}\n' >"$RUNTIME_HEALTH"
printf '// busy-runtime candidate changed\n' >>"$ROOT/internal/backend.go"
wait_for_value "$BUILD_COUNT" 3
sleep 0.3
[[ "$(<"$START_COUNT")" == 3 ]] || {
  echo 'candidate activated while a task was active' >&2
  exit 1
}
grep -q 'activation deferred until active tasks finish' "$TMP_DIR/resumed-watcher.log" || {
  echo 'busy-runtime activation was not diagnosed' >&2
  exit 1
}
printf '{"active_session_runs":0,"runtime_draining":false}\n' >"$RUNTIME_HEALTH"
wait_for_value "$START_COUNT" 4

# A source snapshot whose built binary cannot verify its live asset manifests
# must never replace the active child. The unchanged coherent digest is retried
# with backoff and promoted only after verification succeeds.
touch "$FAIL_NEXT_ASSET_VERIFY"
printf '// asset-coherence candidate changed\n' >>"$ROOT/internal/backend.go"
wait_for_value "$BUILD_COUNT" 4
[[ "$(<"$START_COUNT")" == 4 ]] || {
  echo 'asset-invalid candidate replaced the active backend' >&2
  exit 1
}
wait_for_value "$BUILD_COUNT" 5
wait_for_value "$START_COUNT" 5
grep -q 'candidate asset verification failed; the previous backend remains active' "$TMP_DIR/resumed-watcher.log" || {
  echo 'asset-invalid candidate was not diagnosed' >&2
  exit 1
}

# A failed backend build keeps the active child and retries the unchanged
# digest after a bounded delay instead of waiting for an unrelated file touch.
touch "$FAIL_NEXT_BUILD"
printf '// backend source changed\n' >>"$ROOT/internal/backend.go"
wait_for_value "$BUILD_COUNT" 6
[[ "$(<"$START_COUNT")" == 5 ]] || {
	echo 'failed candidate replaced the active backend' >&2
	exit 1
}
wait_for_value "$BUILD_COUNT" 7
wait_for_value "$START_COUNT" 6
grep -q 'retrying failed digest' "$TMP_DIR/resumed-watcher.log" || {
  echo 'failed digest was not scheduled for retry' >&2
  exit 1
}

kill -TERM "$WATCHER_PID"
wait "$WATCHER_PID"
WATCHER_PID=""

echo 'source-backend-watch-test: ok'
