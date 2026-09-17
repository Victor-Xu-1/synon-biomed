#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

FIXTURE="$TMP_DIR/project"
MARKERS="$TMP_DIR/markers"
mkdir -p "$FIXTURE/scripts/audit" "$TMP_DIR/bin"
cp "$ROOT_DIR/scripts/audit/audit-agent-runtime.sh" "$FIXTURE/scripts/audit/audit-agent-runtime.sh"

write_marker_script() {
  local path="$1"
  local name="$2"
  cat >"$path" <<SH
#!/usr/bin/env bash
echo "$name" >> "$MARKERS"
SH
  chmod +x "$path"
}

write_marker_script "$FIXTURE/scripts/goal-run-doctor-gate-test.sh" "goal-run-doctor-gate-test"
write_marker_script "$FIXTURE/scripts/audit/audit-source-clean-test.sh" "audit-source-clean-test"
write_marker_script "$FIXTURE/scripts/goal-run-startup-test.sh" "goal-run-startup-test"
write_marker_script "$FIXTURE/scripts/goal-run-strict-config-test.sh" "goal-run-strict-config-test"
write_marker_script "$FIXTURE/scripts/audit/audit-source-clean.sh" "audit-source-clean"
write_marker_script "$FIXTURE/scripts/package-release-test.sh" "package-release-test"
write_marker_script "$FIXTURE/scripts/install-release-test.sh" "install-release-test"

cat >"$TMP_DIR/bin/go" <<SH
#!/usr/bin/env bash
echo "go \$*" >> "$MARKERS"
exit 0
SH
cat >"$TMP_DIR/bin/git" <<SH
#!/usr/bin/env bash
echo "git \$*" >> "$MARKERS"
exit 0
SH
chmod +x "$TMP_DIR/bin/go" "$TMP_DIR/bin/git"

(
  cd "$FIXTURE"
  PATH="$TMP_DIR/bin:$PATH" GO="$TMP_DIR/bin/go" SYNON_AUDIT_DIR="$TMP_DIR/audit" bash scripts/audit/audit-agent-runtime.sh --full >/dev/null
)

require_marker() {
  local marker="$1"
  if ! grep -qxF "$marker" "$MARKERS"; then
    echo "ERROR: full audit did not run $marker" >&2
    echo "markers:" >&2
    cat "$MARKERS" >&2
    exit 1
  fi
}

require_marker "goal-run-doctor-gate-test"
require_marker "audit-source-clean-test"
require_marker "goal-run-startup-test"
require_marker "goal-run-strict-config-test"
require_marker "audit-source-clean"
require_marker "package-release-test"
require_marker "install-release-test"

echo "audit-agent-runtime-full-gates-test: ok"
