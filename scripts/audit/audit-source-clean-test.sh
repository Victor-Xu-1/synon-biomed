#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

FIXTURE="$TMP_DIR/project"
mkdir -p "$FIXTURE/scripts/audit"
mkdir -p "$FIXTURE/scripts/quality"
mkdir -p "$FIXTURE/internal/persistence/workspace"
mkdir -p "$FIXTURE/internal/logoassets"
mkdir -p "$FIXTURE/.github"
mkdir -p "$FIXTURE/docs"
mkdir -p "$FIXTURE/docs/compatibility"
mkdir -p "$FIXTURE/docs/compatibility/goals"
mkdir -p "$FIXTURE/docs/governance"
mkdir -p "$FIXTURE/docs/full-product"
mkdir -p "$FIXTURE/docs/provenance"
mkdir -p "$FIXTURE/frontend"
mkdir -p "$FIXTURE/skills/synonbiomed"
cp "$ROOT_DIR/scripts/audit/audit-source-clean.sh" "$FIXTURE/scripts/audit/audit-source-clean.sh"
cp "$ROOT_DIR/scripts/audit/audit-secrets.py" "$FIXTURE/scripts/audit/audit-secrets.py"
cp "$ROOT_DIR/scripts/audit/audit-non-web-boundary.sh" "$FIXTURE/scripts/audit/audit-non-web-boundary.sh"
cp "$ROOT_DIR/scripts/audit/audit-non-web-boundary-test.sh" \
  "$FIXTURE/scripts/audit/audit-non-web-boundary-test.sh"
cp "$ROOT_DIR/scripts/ci-contract-test.sh" "$FIXTURE/scripts/ci-contract-test.sh"
cp "$ROOT_DIR/scripts/env-example-test.sh" "$FIXTURE/scripts/env-example-test.sh"
mkdir -p "$FIXTURE/scripts/dev"
for dev_script in \
  frontend-dependency-watch.mjs \
  source-frontend-host.sh \
  source-frontend-host-test.sh; do
  cp "$ROOT_DIR/scripts/dev/$dev_script" "$FIXTURE/scripts/dev/$dev_script"
done
mkdir -p "$FIXTURE/scripts/dev/testdata"
cp "$ROOT_DIR/scripts/dev/testdata/fake-npm.sh" \
  "$FIXTURE/scripts/dev/testdata/fake-npm.sh"
cp "$ROOT_DIR/scripts/generate-conda-runtime-lock.mjs" \
  "$FIXTURE/scripts/generate-conda-runtime-lock.mjs"
cp "$ROOT_DIR/scripts/generate-conda-runtime-lock.test.mjs" \
  "$FIXTURE/scripts/generate-conda-runtime-lock.test.mjs"
cp "$ROOT_DIR/scripts/p9_external_acceptance.py" "$FIXTURE/scripts/p9_external_acceptance.py"
cp "$ROOT_DIR/scripts/p9_external_acceptance_test.py" \
  "$FIXTURE/scripts/p9_external_acceptance_test.py"
cp "$ROOT_DIR/scripts/audit/audit_frontend_migration.py" "$FIXTURE/scripts/audit/audit_frontend_migration.py"
cp "$ROOT_DIR/scripts/verify-artifact-provenance.py" \
  "$FIXTURE/scripts/verify-artifact-provenance.py"
cp "$ROOT_DIR/scripts/quality/release_contract_io.py" \
  "$FIXTURE/scripts/quality/release_contract_io.py"
cp "$ROOT_DIR/scripts/quality/release_candidate_manifest.py" \
  "$FIXTURE/scripts/quality/release_candidate_manifest.py"
cp "$ROOT_DIR/scripts/quality/release_receipt_gate.py" \
  "$FIXTURE/scripts/quality/release_receipt_gate.py"
cp "$ROOT_DIR/.env.example" "$FIXTURE/.env.example"
cp "$ROOT_DIR/.dockerignore" "$FIXTURE/.dockerignore"
cp "$ROOT_DIR/.github/release.yml" "$FIXTURE/.github/release.yml"
cp "$ROOT_DIR/COMMERCIAL-LICENSE.md" "$FIXTURE/COMMERCIAL-LICENSE.md"
cp "$ROOT_DIR/LICENSE" "$FIXTURE/LICENSE"
cp "$ROOT_DIR/identity.go" "$FIXTURE/identity.go"
cp "$ROOT_DIR/identity_test.go" "$FIXTURE/identity_test.go"
cp "$ROOT_DIR/product-identity.json" "$FIXTURE/product-identity.json"
cp "$ROOT_DIR/README.md" "$FIXTURE/README.md"
cp "$ROOT_DIR/internal/logoassets/LICENSE" "$FIXTURE/internal/logoassets/LICENSE"
cp "$ROOT_DIR/internal/logoassets/SOURCE.md" "$FIXTURE/internal/logoassets/SOURCE.md"
cp "$ROOT_DIR/docs/THIRD_PARTY.md" "$FIXTURE/docs/THIRD_PARTY.md"
cp "$ROOT_DIR/docs/operations-runbook.md" "$FIXTURE/docs/operations-runbook.md"
cp "$ROOT_DIR/docs/release-acceptance-contract.md" \
  "$FIXTURE/docs/release-acceptance-contract.md"
cp "$ROOT_DIR/docs/non-web-asset-boundary.json" "$FIXTURE/docs/non-web-asset-boundary.json"
cp "$ROOT_DIR/docs/governance/web-api-required-paths.json" "$FIXTURE/docs/governance/web-api-required-paths.json"
cp "$ROOT_DIR/docs/governance/versioning.md" \
  "$FIXTURE/docs/governance/versioning.md"
cp "$ROOT_DIR/docs/governance/release-policy.json" \
  "$FIXTURE/docs/governance/release-policy.json"
cp "$ROOT_DIR/docs/provenance/workbench-source-migration.json" \
  "$FIXTURE/docs/provenance/workbench-source-migration.json"
for frontend_file in \
  LICENSE \
  MIGRATION_MANIFEST.json \
  SOURCE_IMPORT_MANIFEST.json \
  THIRD_PARTY_LICENSES.json \
  package-lock.json \
  package.json \
  vite.config.ts; do
  cp "$ROOT_DIR/frontend/$frontend_file" "$FIXTURE/frontend/$frontend_file"
done

touch "$FIXTURE/unclassified-notes.txt"
if (cd "$FIXTURE" && bash scripts/audit/audit-source-clean.sh >"$TMP_DIR/unclassified-root.out" 2>"$TMP_DIR/unclassified-root.err"); then
  echo "ERROR: audit-source-clean allowed an unclassified top-level source file" >&2
  exit 1
fi
grep -Fq './unclassified-notes.txt' "$TMP_DIR/unclassified-root.err"
rm "$FIXTURE/unclassified-notes.txt"

mkdir -p "$FIXTURE/build/scientific/autodock-vina"
if (cd "$FIXTURE" && bash scripts/audit/audit-source-clean.sh >"$TMP_DIR/obsolete-build.out" 2>"$TMP_DIR/obsolete-build.err"); then
  echo "ERROR: audit-source-clean allowed the retired scientific build tree" >&2
  exit 1
fi
grep -Fq './build' "$TMP_DIR/obsolete-build.err"
rm -rf "$FIXTURE/build"

expect_rejected_path() {
  local relative_path="$1"
  local path_kind="$2"
  local case_name
  case_name=$(printf '%s' "$relative_path" | tr '/.~' '___')

  if [[ "$path_kind" == "dir" ]]; then
    mkdir -p "$FIXTURE/$relative_path"
  else
    mkdir -p "$(dirname "$FIXTURE/$relative_path")"
    touch "$FIXTURE/$relative_path"
  fi

  if (
    cd "$FIXTURE"
    bash scripts/audit/audit-source-clean.sh >"$TMP_DIR/$case_name.out" 2>"$TMP_DIR/$case_name.err"
  ); then
    echo "ERROR: audit-source-clean allowed $relative_path" >&2
    exit 1
  fi

  if ! grep -Fq "./$relative_path" "$TMP_DIR/$case_name.err"; then
    echo "ERROR: audit-source-clean did not report $relative_path" >&2
    cat "$TMP_DIR/$case_name.err" >&2
    exit 1
  fi

  rm -rf "$FIXTURE/$relative_path"
}

(
  cd "$FIXTURE"
  bash scripts/audit/audit-source-clean.sh >/dev/null
)

# Every removed collaboration/evidence file is forbidden from returning. These
# are negative boundary checks, not dependencies on an operator archive.
for external_path in \
  "docs/compatibility/current-report.json" \
  "docs/compatibility/v1.1-behavior-evidence.json" \
  "docs/compatibility/v1.1-realtime-contract-coverage.json" \
  "docs/compatibility/v1.1-reuse-manifest.json" \
  "docs/compatibility/v1.1-service-contract-coverage.json" \
  "AGENTS.md" \
  ".github/pull_request_template.md" \
  "docs/governance/development/00-并行开发统一约束.md" \
  "docs/governance/development/01-开发线程工作规范.md" \
  "docs/governance/development/02-主协调线程PR并线验收规范.md" \
  "docs/governance/development/03-Synon-Biomed-Harness优化与长任务验收规范.md" \
  "docs/governance/development/04-工程质量与Agent协作规范.md" \
  "docs/governance/development/05-接入AGENTS与GitHub执行指南.md" \
  "docs/governance/development/README-并行开发约束.md" \
  "docs/governance/product-identity-release-authorization.json" \
  "docs/governance/repository-state.md" \
  "docs/governance/synon-biomed-optimization-constraints.md" \
  "docs/governance/harness-goal.md" \
  "docs/governance/history/release-cleanup-2026-07-29.json" \
  "docs/engineering/synon-harness-acceptance.json" \
  "docs/engineering/synon-runtime-capability-ledger.json" \
  "docs/engineering/synon-harness-migration-audit.md" \
  "docs/compatibility/recovered-work-audit.md" \
  "docs/compatibility/v1.1-reuse-review.md" \
  "docs/compatibility/goals/README.md" \
  "docs/compatibility/goals/non-web-runtime-goal.md" \
  "docs/compatibility/goals/full-product-superiority-goal.md" \
  "docs/full-product/p0-report.md" \
  "docs/full-product/p4-control-surface-audit.md" \
  "docs/full-product/p5-scientific-workbench-audit.md" \
  "docs/full-product/p6-runtime-data-kernel-audit.md" \
  "docs/full-product/p7-retained-capabilities-audit.md" \
  "docs/full-product/p8-release-operations-audit.md" \
  "docs/full-product/p9-final-acceptance-audit.md" \
  "docs/quality-testing/DESIGN_QA.md" \
  "docs/quality-testing/MCP_ONLINE_UPGRADE_ACCEPTANCE_2026-08-30.md" \
  "docs/quality-testing/MCP_SMALL_MOLECULE_DESIGN_ADDENDUM_2026-08-30.md" \
  "docs/quality-testing/REAL_TASK_ACCEPTANCE_2026-08-12.md" \
  "docs/quality-testing/TEST_ISSUES.md" \
  "docs/quality-testing/TOOL_FAILURE_LEDGER_2026-08-07.md" \
  "docs/quality-testing/artifact-sidebar-compact-design-qa.md" \
  "docs/quality-testing/compute-panel-design-qa.md" \
  "docs/quality-testing/interaction-diagram-design-qa.md" \
  "docs/quality-testing/structure-preview-toolbar-design-qa.md" \
  "docs/quality-testing/tool-stream-visual-qa.md" \
  "REWRITE_STATUS.json" \
  "docs/compatibility/evidence/rewrite-status-legacy.json" \
  "docs/compatibility/gaps/agent-connector-management.md" \
  "docs/compatibility/gaps/agent-profile-management.md" \
  "docs/compatibility/gaps/compute-inference-provider-probe.md" \
  "docs/compatibility/gaps/frame-session-core.md" \
  "docs/compatibility/gaps/get-agents.md" \
  "docs/compatibility/gaps/health-check.md" \
  "docs/compatibility/gaps/provider-runtime-authority.md" \
  "docs/compatibility/gaps/vm-resources-desktop-contract.md" \
  "docs/compatibility/gaps/vm-restart-running-frames-desktop-contract.md"; do
  expect_rejected_path "$external_path" file
done

for required_generator in \
  scripts/generate-conda-runtime-lock.mjs \
  scripts/generate-conda-runtime-lock.test.mjs; do
  rm "$FIXTURE/$required_generator"
  case_name=$(basename "$required_generator" | tr '.-' '__')
  if (cd "$FIXTURE" && bash scripts/audit/audit-source-clean.sh >"$TMP_DIR/$case_name.out" 2>"$TMP_DIR/$case_name.err"); then
    echo "ERROR: audit-source-clean allowed a source tree without $required_generator" >&2
    exit 1
  fi
  grep -Fq "$required_generator" "$TMP_DIR/$case_name.err"
  cp "$ROOT_DIR/$required_generator" "$FIXTURE/$required_generator"
done

chmod 0644 "$FIXTURE/scripts/audit/audit-source-clean.sh"
if (cd "$FIXTURE" && bash scripts/audit/audit-source-clean.sh >"$TMP_DIR/non-executable.out" 2>"$TMP_DIR/non-executable.err"); then
  echo "ERROR: audit-source-clean allowed a non-executable shell script" >&2
  exit 1
fi
grep -Fq 'shell scripts are not executable' "$TMP_DIR/non-executable.err"
grep -Fq 'scripts/audit/audit-source-clean.sh' "$TMP_DIR/non-executable.err"
chmod 0755 "$FIXTURE/scripts/audit/audit-source-clean.sh"

git -C "$FIXTURE" init -q
git -C "$FIXTURE" add scripts/dev/source-frontend-host.sh
git -C "$FIXTURE" update-index --chmod=-x scripts/dev/source-frontend-host.sh
if (cd "$FIXTURE" && bash scripts/audit/audit-source-clean.sh >"$TMP_DIR/index-mode.out" 2>"$TMP_DIR/index-mode.err"); then
  echo "ERROR: audit-source-clean allowed a non-executable Git index shell script" >&2
  exit 1
fi
grep -Fq 'Git index shell scripts are not executable' "$TMP_DIR/index-mode.err"
grep -Fq './scripts/dev/source-frontend-host.sh' "$TMP_DIR/index-mode.err"
git -C "$FIXTURE" update-index --chmod=+x scripts/dev/source-frontend-host.sh

rm "$FIXTURE/.env.example"
if (cd "$FIXTURE" && bash scripts/audit/audit-source-clean.sh >"$TMP_DIR/missing-env-example.out" 2>"$TMP_DIR/missing-env-example.err"); then
  echo "ERROR: audit-source-clean allowed a source tree without .env.example" >&2
  exit 1
fi
grep -Fq '.env.example' "$TMP_DIR/missing-env-example.err"
cp "$ROOT_DIR/.env.example" "$FIXTURE/.env.example"

rm "$FIXTURE/LICENSE"
if (cd "$FIXTURE" && bash scripts/audit/audit-source-clean.sh >"$TMP_DIR/missing-license.out" 2>"$TMP_DIR/missing-license.err"); then
  echo "ERROR: audit-source-clean allowed a source tree without LICENSE" >&2
  exit 1
fi
grep -Fq 'LICENSE' "$TMP_DIR/missing-license.err"
cp "$ROOT_DIR/LICENSE" "$FIXTURE/LICENSE"

rm "$FIXTURE/COMMERCIAL-LICENSE.md"
if (cd "$FIXTURE" && bash scripts/audit/audit-source-clean.sh >"$TMP_DIR/missing-commercial-license.out" 2>"$TMP_DIR/missing-commercial-license.err"); then
  echo "ERROR: audit-source-clean allowed a source tree without COMMERCIAL-LICENSE.md" >&2
  exit 1
fi
grep -Fq 'COMMERCIAL-LICENSE.md' "$TMP_DIR/missing-commercial-license.err"
cp "$ROOT_DIR/COMMERCIAL-LICENSE.md" "$FIXTURE/COMMERCIAL-LICENSE.md"

rm "$FIXTURE/product-identity.json"
if (cd "$FIXTURE" && bash scripts/audit/audit-source-clean.sh >"$TMP_DIR/missing-product-identity.out" 2>"$TMP_DIR/missing-product-identity.err"); then
  echo "ERROR: audit-source-clean allowed a source tree without product identity" >&2
  exit 1
fi
grep -Fq 'product-identity.json' "$TMP_DIR/missing-product-identity.err"
cp "$ROOT_DIR/product-identity.json" "$FIXTURE/product-identity.json"

(
  cd "$FIXTURE"
  touch .env.local
  if bash scripts/audit/audit-source-clean.sh >"$TMP_DIR/env-local.out" 2>"$TMP_DIR/env-local.err"; then
    echo "ERROR: audit-source-clean allowed .env.local" >&2
    exit 1
  fi
)

if ! grep -q './.env.local' "$TMP_DIR/env-local.err"; then
  echo "ERROR: audit-source-clean did not report .env.local" >&2
  cat "$TMP_DIR/env-local.err" >&2
  exit 1
fi

rm "$FIXTURE/.env.local"
mkdir "$FIXTURE/workspace"
if (cd "$FIXTURE" && bash scripts/audit/audit-source-clean.sh >"$TMP_DIR/workspace.out" 2>"$TMP_DIR/workspace.err"); then
	echo "ERROR: audit-source-clean allowed root runtime workspace" >&2
	exit 1
fi
grep -q './workspace' "$TMP_DIR/workspace.err"
rmdir "$FIXTURE/workspace"

mkdir -p "$FIXTURE/assets/component/node_modules"
if (cd "$FIXTURE" && bash scripts/audit/audit-source-clean.sh >"$TMP_DIR/node-modules.out" 2>"$TMP_DIR/node-modules.err"); then
	echo "ERROR: audit-source-clean allowed nested node_modules" >&2
	exit 1
fi
grep -q './assets/component/node_modules' "$TMP_DIR/node-modules.err"
rm -rf "$FIXTURE/assets/component/node_modules"

mkdir -p "$FIXTURE/internal/capabilities/markush-support"
touch "$FIXTURE/internal/capabilities/markush-support/admet_tool.go"
touch "$FIXTURE/internal/capabilities/markush-support/pcc_knowledge_briefing.txt"
if ! (
  cd "$FIXTURE"
  bash scripts/audit/audit-source-clean.sh >"$TMP_DIR/capability-names.out" 2>"$TMP_DIR/capability-names.err"
); then
  echo "ERROR: audit-source-clean rejected files based only on capability names" >&2
  cat "$TMP_DIR/capability-names.err" >&2
  exit 1
fi
rm -rf "$FIXTURE/internal/capabilities"

expect_rejected_path "README.md.orig" file
expect_rejected_path "patch.rej" file
expect_rejected_path "notes.md~" file
expect_rejected_path ".superpowers" dir
expect_rejected_path "docs/superpowers" dir
expect_rejected_path "docs/legacy-runtime-goal.md" file
expect_rejected_path "docs/governance/development/new-policy.md" file
expect_rejected_path "docs/quality-testing/new-task-record.md" file
expect_rejected_path "docs/compatibility/goals/new-roadmap.md" file
expect_rejected_path "docs/compatibility/evidence/new-capture.json" file
expect_rejected_path "docs/full-product/new-progress.json" file
expect_rejected_path "docs/governance/history/new-cleanup.json" file
expect_rejected_path "internal/testmodule/__pycache__" dir
expect_rejected_path "internal/testmodule/cache.pyc" file
expect_rejected_path ".pytest_cache" dir
expect_rejected_path "test-results" dir
expect_rejected_path ".DS_Store" file

expect_rejected_path "assets/unclassified/archive.zip" file

mkdir -p "$FIXTURE/assets/classified"
archive_path="$FIXTURE/assets/classified/archive.zip"
printf 'classified archive payload\n' >"$archive_path"
archive_hash=$(sha256sum "$archive_path" | cut -d ' ' -f1)
archive_bytes=$(wc -c <"$archive_path" | tr -d ' ')
printf '{\n  "files": [\n    {"path": "archive.zip", "sha256": "%s", "bytes": %s}\n  ]\n}\n' \
  "$archive_hash" "$archive_bytes" >"$FIXTURE/assets/classified/manifest.json"
if ! (
  cd "$FIXTURE"
  bash scripts/audit/audit-source-clean.sh >"$TMP_DIR/classified-archive.out" 2>"$TMP_DIR/classified-archive.err"
); then
  echo "ERROR: audit-source-clean rejected a hash-classified archive" >&2
  cat "$TMP_DIR/classified-archive.err" >&2
  exit 1
fi

printf 'tampered\n' >>"$archive_path"
if (
  cd "$FIXTURE"
  bash scripts/audit/audit-source-clean.sh >"$TMP_DIR/tampered-archive.out" 2>"$TMP_DIR/tampered-archive.err"
); then
  echo "ERROR: audit-source-clean allowed a tampered classified archive" >&2
  exit 1
fi
grep -Fq './assets/classified/archive.zip' "$TMP_DIR/tampered-archive.err"

rm -rf "$FIXTURE/assets/classified"
mkdir -p "$FIXTURE/assets/parent-provenance/linux-x86_64"
nested_archive="$FIXTURE/assets/parent-provenance/linux-x86_64/runtime.zip"
printf 'nested classified archive payload\n' >"$nested_archive"
nested_hash=$(sha256sum "$nested_archive" | cut -d ' ' -f1)
nested_bytes=$(wc -c <"$nested_archive" | tr -d ' ')
printf '{\n  "files": [\n    {"path": "linux-x86_64/runtime.zip", "sha256": "%s", "bytes": %s}\n  ]\n}\n' \
  "$nested_hash" "$nested_bytes" >"$FIXTURE/assets/parent-provenance/manifest.json"
if ! (
  cd "$FIXTURE"
  bash scripts/audit/audit-source-clean.sh >"$TMP_DIR/nested-classified.out" 2>"$TMP_DIR/nested-classified.err"
); then
  echo "ERROR: audit-source-clean rejected an ancestor-classified archive" >&2
  cat "$TMP_DIR/nested-classified.err" >&2
  exit 1
fi

printf 'tampered\n' >>"$nested_archive"
if (
  cd "$FIXTURE"
  bash scripts/audit/audit-source-clean.sh >"$TMP_DIR/nested-tampered.out" 2>"$TMP_DIR/nested-tampered.err"
); then
  echo "ERROR: audit-source-clean allowed a tampered ancestor-classified archive" >&2
  exit 1
fi
grep -Fq './assets/parent-provenance/linux-x86_64/runtime.zip' \
  "$TMP_DIR/nested-tampered.err"

rm -rf "$FIXTURE/assets/parent-provenance"
mkdir -p "$FIXTURE/assets/sibling-pack/payload"
sibling_archive="$FIXTURE/assets/sibling-pack/payload/runtime.gz"
printf 'sibling manifest payload\n' >"$sibling_archive"
sibling_hash=$(sha256sum "$sibling_archive" | cut -d ' ' -f1)
sibling_bytes=$(wc -c <"$sibling_archive" | tr -d ' ')
printf '{\n  "files": [\n    {"path": "payload/runtime.gz", "sha256": "%s", "bytes": %s}\n  ]\n}\n' \
  "$sibling_hash" "$sibling_bytes" >"$FIXTURE/assets/sibling-pack.manifest.json"
if ! (
  cd "$FIXTURE"
  bash scripts/audit/audit-source-clean.sh >"$TMP_DIR/sibling-classified.out" 2>"$TMP_DIR/sibling-classified.err"
); then
  echo "ERROR: audit-source-clean rejected a sibling-manifest asset pack" >&2
  cat "$TMP_DIR/sibling-classified.err" >&2
  exit 1
fi

printf 'tampered\n' >>"$sibling_archive"
if (
  cd "$FIXTURE"
  bash scripts/audit/audit-source-clean.sh >"$TMP_DIR/sibling-tampered.out" 2>"$TMP_DIR/sibling-tampered.err"
); then
  echo "ERROR: audit-source-clean allowed a tampered sibling-manifest asset" >&2
  exit 1
fi
grep -Fq './assets/sibling-pack/payload/runtime.gz' "$TMP_DIR/sibling-tampered.err"

echo "audit-source-clean-test: ok"
