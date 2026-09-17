#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT_DIR=$(cd "$SCRIPT_DIR/../.." && pwd)
MODE="${1:---quick}"
GO_BIN="${GO:-go}"
AUDIT_DIR="${SYNON_AUDIT_DIR:-${TMPDIR:-/tmp}/synon-go-audit}"

usage() {
  cat <<'USAGE'
Usage: scripts/audit/audit-agent-runtime.sh [--quick|--full]

  --quick   Run focused agent runtime parity tests and source diff checks.
  --full    Run the final package-level agent runtime audit matrix plus full Go tests.

Environment:
  GO                Go executable to use. Defaults to "go".
  SYNON_AUDIT_DIR   Directory for audit logs. Defaults to "${TMPDIR:-/tmp}/synon-go-audit".
USAGE
}

case "$MODE" in
  --quick|quick)
    MODE="quick"
    ;;
  --full|full)
    MODE="full"
    ;;
  -h|--help|help)
    usage
    exit 0
    ;;
  *)
    usage >&2
    exit 2
    ;;
esac

mkdir -p "$AUDIT_DIR"
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
LOG_FILE="$AUDIT_DIR/agent-runtime-$MODE-$STAMP.log"

run() {
  {
    echo
    echo "==> $*"
  } | tee -a "$LOG_FILE"
  "$@" 2>&1 | tee -a "$LOG_FILE"
}

cd "$ROOT_DIR"

{
  echo "agent-runtime-audit"
  echo "mode=$MODE"
  echo "root=$ROOT_DIR"
  echo "go=$GO_BIN"
  echo "started_at=$STAMP"
} | tee "$LOG_FILE"

if [[ "$MODE" == "quick" ]]; then
  run bash scripts/goal-run-doctor-gate-test.sh
  run bash scripts/audit/audit-source-clean-test.sh
  run bash scripts/goal-run-startup-test.sh
  run bash scripts/audit/audit-agent-runtime-full-gates-test.sh
  run "$GO_BIN" test -count=1 ./internal/server -run 'TestToolsAPIExecutesAgentRuntimeDoctorParityAudit|TestSessionRunnerChatInjectsProviderResumeCacheMarkers|TestToolsAPIDirectGatewayRunsHooksAndRuntimeAudit|TestSessionRunnerChatInjectsAllowedDynamicMCPContext|TestServerAgentRuntimeBridgeAsyncPostToolHookDoesNotBlockToolResult'
  run git diff --check
else
  run bash scripts/goal-run-doctor-gate-test.sh
  run bash scripts/audit/audit-source-clean-test.sh
  run bash scripts/goal-run-startup-test.sh
  run bash scripts/goal-run-strict-config-test.sh
  run "$GO_BIN" test -count=1 ./internal/agentruntime ./internal/plugins/host ./internal/tools/mcpstdio ./internal/tools/registry ./internal/server ./cmd/synon
  run "$GO_BIN" test -count=1 ./...
  run bash scripts/audit/audit-source-clean.sh
  run bash scripts/package-release-test.sh
  run bash scripts/install-release-test.sh
  run git diff --check
fi

{
  echo
  echo "completed_at=$(date -u +%Y%m%dT%H%M%SZ)"
  echo "log=$LOG_FILE"
} | tee -a "$LOG_FILE"
