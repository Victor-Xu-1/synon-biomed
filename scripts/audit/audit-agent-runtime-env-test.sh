#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

FIXTURE="$TMP_DIR/project"
mkdir -p "$FIXTURE/scripts/audit" "$TMP_DIR/bin"
cp "$ROOT_DIR/scripts/audit/audit-agent-runtime.sh" "$FIXTURE/scripts/audit/audit-agent-runtime.sh"
cp "$ROOT_DIR/scripts/goal-run-doctor-gate.sh" "$FIXTURE/scripts/goal-run-doctor-gate.sh"
cp "$ROOT_DIR/scripts/goal-run-doctor-gate-test.sh" "$FIXTURE/scripts/goal-run-doctor-gate-test.sh"

for fixture_script in \
  scripts/audit/audit-source-clean-test.sh \
  scripts/goal-run-startup-test.sh \
  scripts/audit/audit-agent-runtime-full-gates-test.sh; do
  printf '#!/usr/bin/env bash\nexit 0\n' >"$FIXTURE/$fixture_script"
  chmod +x "$FIXTURE/$fixture_script"
done

cat >"$TMP_DIR/bin/go" <<'SH'
#!/usr/bin/env bash
exit 0
SH
cat >"$TMP_DIR/bin/git" <<'SH'
#!/usr/bin/env bash
exit 0
SH
chmod +x "$TMP_DIR/bin/go" "$TMP_DIR/bin/git"

(
  cd "$FIXTURE"
  PATH="$TMP_DIR/bin:$PATH" GO="$TMP_DIR/bin/go" bash scripts/audit/audit-agent-runtime.sh --quick >/dev/null
)

if [[ -d "$FIXTURE/.synon-go-audit" ]]; then
  echo "ERROR: default audit run wrote local artifacts into the project root" >&2
  exit 1
fi

log_count=$(find "${TMPDIR:-/tmp}/synon-go-audit" -type f -name 'agent-runtime-quick-*.log' 2>/dev/null | wc -l | tr -d ' ')
if [[ "$log_count" -lt 1 ]]; then
  echo "ERROR: default audit run did not write a log under the system temp audit directory" >&2
  exit 1
fi

echo "audit-agent-runtime-env-test: ok"
