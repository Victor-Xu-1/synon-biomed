#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

cat >"$TMP_DIR/failing-runtime" <<'SCRIPT'
#!/usr/bin/env bash
echo "intentional startup failure" >&2
exit 12
SCRIPT
chmod +x "$TMP_DIR/failing-runtime"

set +e
SYNON_GOAL_HOME="$TMP_DIR/home" SYNON_GOAL_PORT=18999 \
	timeout 4 bash "$ROOT_DIR/scripts/goal-run.sh" "$TMP_DIR/failing-runtime" \
	>"$TMP_DIR/out" 2>"$TMP_DIR/err"
status=$?
set -e

if [[ $status -eq 0 || $status -eq 124 ]]; then
	echo "goal-run did not fail promptly when the runtime exited: status=$status" >&2
	exit 1
fi
grep -q 'runtime exited before becoming healthy' "$TMP_DIR/err"
grep -q 'intentional startup failure' "$TMP_DIR/err"

echo "goal-run-startup-test: ok"
