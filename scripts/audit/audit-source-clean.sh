#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT_DIR=$(cd "$SCRIPT_DIR/../.." && pwd)

cd "$ROOT_DIR"

# Local engineering policies and historical task records are external material,
# not clone/build inputs. Keep their retired paths from returning to source.
for external_directory in \
  docs/governance/development \
  docs/quality-testing \
  docs/compatibility/goals \
  docs/compatibility/evidence \
  docs/full-product \
  docs/governance/history; do
  external_residue=""
  if [[ -L "$external_directory" || -f "$external_directory" ]]; then
    external_residue="$external_directory"
  elif [[ -d "$external_directory" ]]; then
    external_residue=$(find "$external_directory" \( -type f -o -type l \) -print -quit)
  fi
  if [[ -n "$external_residue" ]]; then
    echo "ERROR: external engineering material found in source: ./$external_residue" >&2
    exit 1
  fi
done

external_engineering_paths=(
  "AGENTS.md"
  ".github/pull_request_template.md"
  "docs/governance/development/00-并行开发统一约束.md"
  "docs/governance/development/01-开发线程工作规范.md"
  "docs/governance/development/02-主协调线程PR并线验收规范.md"
  "docs/governance/development/03-Synon-Biomed-Harness优化与长任务验收规范.md"
  "docs/governance/development/04-工程质量与Agent协作规范.md"
  "docs/governance/development/05-接入AGENTS与GitHub执行指南.md"
  "docs/governance/development/README-并行开发约束.md"
  "docs/governance/product-identity-release-authorization.json"
  "docs/governance/repository-state.md"
  "docs/governance/synon-biomed-optimization-constraints.md"
  "docs/governance/harness-goal.md"
  "docs/governance/history/release-cleanup-2026-07-29.json"
  "docs/engineering/synon-harness-acceptance.json"
  "docs/engineering/synon-runtime-capability-ledger.json"
  "docs/engineering/synon-harness-migration-audit.md"
  "docs/compatibility/recovered-work-audit.md"
  "docs/compatibility/v1.1-reuse-review.md"
  "docs/compatibility/goals/README.md"
  "docs/compatibility/goals/non-web-runtime-goal.md"
  "docs/compatibility/goals/full-product-superiority-goal.md"
  "docs/full-product/p0-report.md"
  "docs/full-product/p4-control-surface-audit.md"
  "docs/full-product/p5-scientific-workbench-audit.md"
  "docs/full-product/p6-runtime-data-kernel-audit.md"
  "docs/full-product/p7-retained-capabilities-audit.md"
  "docs/full-product/p8-release-operations-audit.md"
  "docs/full-product/p9-final-acceptance-audit.md"
  "docs/quality-testing/DESIGN_QA.md"
  "docs/quality-testing/MCP_ONLINE_UPGRADE_ACCEPTANCE_2026-08-30.md"
  "docs/quality-testing/MCP_SMALL_MOLECULE_DESIGN_ADDENDUM_2026-08-30.md"
  "docs/quality-testing/REAL_TASK_ACCEPTANCE_2026-08-12.md"
  "docs/quality-testing/TEST_ISSUES.md"
  "docs/quality-testing/TOOL_FAILURE_LEDGER_2026-08-07.md"
  "docs/quality-testing/artifact-sidebar-compact-design-qa.md"
  "docs/quality-testing/compute-panel-design-qa.md"
  "docs/quality-testing/interaction-diagram-design-qa.md"
  "docs/quality-testing/structure-preview-toolbar-design-qa.md"
  "docs/quality-testing/tool-stream-visual-qa.md"
  "REWRITE_STATUS.json"
  "docs/compatibility/current-report.json"
  "docs/compatibility/v1.1-behavior-evidence.json"
  "docs/compatibility/v1.1-realtime-contract-coverage.json"
  "docs/compatibility/v1.1-reuse-manifest.json"
  "docs/compatibility/v1.1-service-contract-coverage.json"
  "docs/compatibility/evidence/rewrite-status-legacy.json"
  "docs/compatibility/gaps/agent-connector-management.md"
  "docs/compatibility/gaps/agent-profile-management.md"
  "docs/compatibility/gaps/compute-inference-provider-probe.md"
  "docs/compatibility/gaps/frame-session-core.md"
  "docs/compatibility/gaps/get-agents.md"
  "docs/compatibility/gaps/health-check.md"
  "docs/compatibility/gaps/provider-runtime-authority.md"
  "docs/compatibility/gaps/vm-resources-desktop-contract.md"
  "docs/compatibility/gaps/vm-restart-running-frames-desktop-contract.md"
)
for external_path in "${external_engineering_paths[@]}"; do
  if [[ -e "$external_path" || -L "$external_path" ]]; then
    echo "ERROR: external engineering material found in source: ./$external_path" >&2
    exit 1
  fi
done

allowed_root_entries=(
  .dockerignore
  .env.example
  .git
  .gitattributes
  .github
  .gitignore
  COMMERCIAL-LICENSE.md
  CODE_OF_CONDUCT.md
  CONTRIBUTING.md
  LICENSE
  Makefile
  README.md
  SECURITY.md
  assets
  cmd
  docs
  frontend
  go.mod
  go.sum
  identity.go
  identity_test.go
  internal
  product-identity.json
  scripts
  skills
  tools
)
unexpected_root_entries=""
while IFS= read -r entry; do
  allowed=false
  for expected in "${allowed_root_entries[@]}"; do
    if [[ "$entry" == "$expected" ]]; then
      allowed=true
      break
    fi
  done
  if [[ "$allowed" != "true" ]]; then
    unexpected_root_entries+="./$entry"$'\n'
  fi
done < <(find . -mindepth 1 -maxdepth 1 -printf '%f\n' | sort)
if [[ -n "$unexpected_root_entries" ]]; then
  echo "ERROR: unclassified top-level source entries found" >&2
  printf '%s' "$unexpected_root_entries" >&2
  exit 1
fi

required_source_files=(
  .dockerignore
  .env.example
  .github/release.yml
  COMMERCIAL-LICENSE.md
  CODE_OF_CONDUCT.md
  CONTRIBUTING.md
  LICENSE
  SECURITY.md
  identity.go
  identity_test.go
  product-identity.json
  docs/THIRD_PARTY.md
  docs/operations-runbook.md
  docs/release-acceptance-contract.md
  docs/non-web-asset-boundary.json
  docs/governance/web-api-required-paths.json
  docs/provenance/workbench-source-migration.json
  docs/governance/versioning.md
  docs/governance/release-policy.json
  internal/logoassets/LICENSE
  internal/logoassets/SOURCE.md
  scripts/audit/audit-non-web-boundary.sh
  scripts/audit/audit-non-web-boundary-test.sh
  scripts/ci-contract-test.sh
  scripts/dev/frontend-dependency-watch.mjs
  scripts/dev/source-frontend-host.sh
  scripts/dev/source-frontend-host-test.sh
  scripts/dev/testdata/fake-npm.sh
  scripts/env-example-test.sh
  scripts/generate-conda-runtime-lock.mjs
  scripts/generate-conda-runtime-lock.test.mjs
  scripts/p9_external_acceptance.py
  scripts/p9_external_acceptance_test.py
  scripts/audit/audit_frontend_migration.py
  scripts/verify-artifact-provenance.py
  scripts/quality/release_contract_io.py
  scripts/quality/release_candidate_manifest.py
  scripts/quality/release_receipt_gate.py
  frontend/LICENSE
  frontend/MIGRATION_MANIFEST.json
  frontend/SOURCE_IMPORT_MANIFEST.json
  frontend/THIRD_PARTY_LICENSES.json
  frontend/package-lock.json
  frontend/package.json
  frontend/vite.config.ts
)

missing_required_files=""
for required_file in "${required_source_files[@]}"; do
  if [[ ! -f "$required_file" || -L "$required_file" ]]; then
    missing_required_files+="$required_file"$'\n'
  fi
done
if [[ -n "$missing_required_files" ]]; then
  echo "ERROR: required source or release files are missing" >&2
  printf '%s' "$missing_required_files" >&2
  exit 1
fi

scan() {
  find . \
    -path './.git' -prune -o \
    "$@" \
    -print | sort
}

fail_if_any() {
  local label="$1"
  local output="$2"
  if [[ -n "$output" ]]; then
    echo "ERROR: $label" >&2
    echo "$output" >&2
    exit 1
  fi
}

non_executable_shell_scripts=$(find . \
  -path './.git' -prune -o \
  -name node_modules -prune -o \
  -type f -name '*.sh' ! -perm /111 -print | sort)
fail_if_any "shell scripts are not executable" \
  "$non_executable_shell_scripts"

indexed_non_executable_shell_scripts=""
if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  while IFS= read -r -d '' record; do
    mode=${record%% *}
    path=${record#*$'\t'}
    if [[ "$mode" != "100755" ]]; then
      indexed_non_executable_shell_scripts+="./$path"$'\n'
    fi
  done < <(git ls-files -z --stage -- '*.sh')
  indexed_non_executable_shell_scripts=${indexed_non_executable_shell_scripts%$'\n'}
fi
fail_if_any "Git index shell scripts are not executable" \
  "$indexed_non_executable_shell_scripts"

blocked_state=$(find . \
  -path './.git' -prune -o \
  \( \
    -name node_modules -o \
    -name out -o \
    -name coverage -o \
    -name .synon-go-audit -o \
    -path './dist' -o \
    -path './release' -o \
    -path './vendor' -o \
    -path './models' -o \
    -path './users' -o \
    -path './runtime' -o \
    -path './workspace' -o \
    -path './uploads' -o \
    -path './mcp-output' \
  \) -print -prune | sort)
fail_if_any "blocked generated/runtime directories found" "$blocked_state"

construction_residue=$(scan \( \
  -name '.superpowers' -o \
  -path './docs/superpowers' -o \
  -name '__pycache__' -o \
  -name '.pytest_cache' -o \
  -name '.mypy_cache' -o \
  -name '.ruff_cache' -o \
  -name '.cache' -o \
  -name 'test-results' -o \
  -name '*.orig' -o \
  -name '*.rej' -o \
  -name '*~' -o \
  -name '*.swp' -o \
  -name '*.swo' -o \
  -name '*.pyc' -o \
  -name '*.pyo' -o \
  -name '.DS_Store' -o \
  -name 'Thumbs.db' -o \
  -name '.coverage' \
\))
fail_if_any "construction, cache, or editor residue found" "$construction_residue"

duplicate_goal_docs=$(find ./docs -type f -iname '*goal*.md' -print | sort)
fail_if_any "external engineering Goal documents found in source" "$duplicate_goal_docs"

sensitive_files=$(scan \( \
  -name '.env' -o \
  \( -name '.env.*' ! -name '.env.example' \) -o \
  -name '.mcp.json' -o \
  -name '.synon.json' -o \
  -name '*.token' -o \
  -name '*.pid' -o \
  -name '*.log' -o \
  -name '*.db' -o \
  -name '*.sqlite' -o \
  -name '*.sqlite3' \
\))
fail_if_any "sensitive or local runtime files found" "$sensitive_files"

python3 scripts/audit/audit-secrets.py .

artifact_has_provenance() {
  local artifact="$1"
  python3 scripts/verify-artifact-provenance.py "$ROOT_DIR" "$artifact" >/dev/null 2>&1
}

unclassified_artifacts=$(
  while IFS= read -r -d '' artifact; do
    if ! artifact_has_provenance "$artifact"; then
      printf '%s\n' "$artifact"
    fi
  done < <(
    find . \
      -path './.git' -prune -o \
      -type f \
      \( \
        -size +10M -o \
        -iname '*.zip' -o \
        -iname '*.tar' -o \
        -iname '*.tgz' -o \
        -iname '*.tar.gz' -o \
        -iname '*.7z' -o \
        -iname '*.rar' -o \
        -iname '*.gz' -o \
        -iname '*.bz2' -o \
        -iname '*.xz' -o \
        -iname '*.whl' -o \
        -iname '*.wasm' -o \
        -iname '*.exe' -o \
        -iname '*.dll' -o \
        -iname '*.dylib' -o \
        -iname '*.so' \
      \) \
      -print0
  )
)
fail_if_any "large, archive, or binary files without matching local provenance found" "$unclassified_artifacts"

echo "source-clean-audit: ok"
