#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
WORKFLOW="$ROOT_DIR/.github/workflows/quality.yml"

if [[ ! -f "$WORKFLOW" ]]; then
  echo "ERROR: quality workflow is missing" >&2
  exit 1
fi

(
  cd "$ROOT_DIR"
  go run -buildvcs=false ./scripts/quality/github-actions-gate \
    --repo . \
    --policy docs/governance/github-actions-pins.json >/dev/null
)

required_fragments=(
  'gofmt -l'
  'go vet ./...'
  'CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -buildvcs=false'
  'PYTHONDONTWRITEBYTECODE=1 python3 scripts/quality/runtime_test_shards.py'
  'PYTHONDONTWRITEBYTECODE=1 python3 scripts/quality/runtime_test_shards.py --race'
  'python3 -m unittest scripts.quality.test_runtime_test_shards'
  'matrix: ${{ fromJSON(needs.p0-quality.outputs.runtime-matrix) }}'
  'matrix: ${{ fromJSON(needs.p0-quality.outputs.race-matrix) }}'
  'fail-fast: false'
  'PYTHONDONTWRITEBYTECODE=1 python3 -m unittest scripts.quality.test_product_identity_gate scripts.quality.test_release_candidate_manifest scripts.quality.test_release_receipt_gate'
  'sudo apt-get install --yes bubblewrap'
  'sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0'
  'bwrap --unshare-user --disable-userns --unshare-ipc --unshare-uts'
  'assets/optional/mcp-servers/bio-tools/requirements-test.txt'
  'sudo "$runtime_python" -m pip install --break-system-packages --ignore-installed'
  'import anyio, httpx, jsonschema, lxml, mcp, pandas, requests, urllib3'
  'from PIL import Image'
  'npm run format:check'
  'python3 scripts/quality/agent_manifest.py --repo . --check'
  'python3 scripts/quality/module_topology_gate.py --repo .'
  'npm run test:packaged'
  'scripts/audit/audit_frontend_migration.py --root . --check --require-clean'
  'make env-example-test'
  'make audit-non-web-boundary'
  'make audit-non-web-boundary-test'
  'make source-backend-watch-test'
  'make source-frontend-host-test'
  'make external-acceptance-test'
  'make package-test'
  'make install-test'
  'make release-lifecycle-test'
  'scripts/package-windows-release-test.sh'
  'scripts/quality/release_candidate_manifest.py create'
  'scripts/quality/release_candidate_manifest.py verify'
  'synon-biomed-release-candidate-'
  'RELEASE_CANDIDATE.json'
  'candidate-manifest-sha256'
  'source-tree-digest-mode manifest-only'
  'go test -buildvcs=false ./scripts/quality/github-actions-gate'
  'go run -buildvcs=false ./scripts/quality/github-actions-gate --repo . --policy docs/governance/github-actions-pins.json'
  'actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7.0.1'
  'actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c # v8.0.1'
  'scripts/release-windows-test.ps1'
  './scripts/release-windows-environment-test.ps1'
  '0 2 * * *'
  'timezone: Asia/Shanghai'
)
for fragment in "${required_fragments[@]}"; do
  if ! grep -Fq "$fragment" "$WORKFLOW"; then
    echo "ERROR: quality workflow is missing required gate: $fragment" >&2
    exit 1
  fi
done

PYTHONDONTWRITEBYTECODE=1 python3 -m unittest scripts.quality.test_runtime_test_shards
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest scripts.quality.test_pr_fast_scope

PR_WORKFLOW="$ROOT_DIR/.github/workflows/quality-pr.yml"
for fragment in \
  '  pull_request:' \
  '  pr-quality:' \
  '  pr-go-tests:' \
  '  pr-frontend-tests:' \
  '  merge-gate:' \
  'name: Merge Gate / CI required' \
  'needs: [pr-frontend-tests, pr-go-tests, pr-quality]' \
  'needs.pr-quality.result' \
  'scripts/quality/pr_fast_scope.py' \
  'scripts/quality/pr_fast_scope.py --vet' \
  'needs: pr-quality'; do
  if ! grep -Fq "$fragment" "$PR_WORKFLOW"; then
    echo "ERROR: PR fast workflow is missing required gate: $fragment" >&2
    exit 1
  fi
done

MAIN_WORKFLOW="$ROOT_DIR/.github/workflows/quality-main.yml"
for fragment in \
  '  push:' \
  '      - main' \
  '  main-quality:' \
  '  main-go-tests:' \
  '  main-frontend-tests:' \
  '  main-integration-gate:' \
  'name: Main Integration / required' \
  'needs: [main-frontend-tests, main-go-tests, main-quality]' \
  'needs.main-quality.result' \
  'github.event.before' \
  'github.sha'; do
  if ! grep -Fq "$fragment" "$MAIN_WORKFLOW"; then
    echo "ERROR: main integration workflow is missing required gate: $fragment" >&2
    exit 1
  fi
done

if [[ "$(grep -Fc 'run: bash scripts/audit/audit-source-clean.sh' "$WORKFLOW")" -lt 4 ]]; then
  echo "ERROR: each test/release job must audit its own source residue" >&2
  exit 1
fi

grep -Fq 'scripts/audit/audit_frontend_migration.py --root . --check' "$ROOT_DIR/Makefile" || {
  echo "ERROR: Makefile frontend audit must bind the current repository root" >&2
  exit 1
}

grep -Fq 'NODE_OPTIONS="$${NODE_OPTIONS:---max-old-space-size=4096}"' "$ROOT_DIR/Makefile" || {
  echo "ERROR: Makefile frontend build must provide the verified 4 GiB Node heap default" >&2
  exit 1
}

grep -Fqx '  p0-quality:' "$WORKFLOW" || {
  echo "ERROR: quality workflow is missing the policy and static-analysis job" >&2
  exit 1
}
grep -Fqx '  frontend-quality:' "$WORKFLOW" || {
  echo "ERROR: quality workflow is missing the independently rerunnable frontend job" >&2
  exit 1
}
grep -Fqx '  runtime-quality:' "$WORKFLOW" || {
  echo "ERROR: quality workflow is missing the independently rerunnable runtime job" >&2
  exit 1
}
grep -Fqx '  linux-release:' "$WORKFLOW" || {
  echo "ERROR: quality workflow is missing the independently rerunnable Linux release job" >&2
  exit 1
}
grep -Fqx '  windows-release:' "$WORKFLOW" || {
  echo "ERROR: quality workflow is missing the Windows release job" >&2
  exit 1
}
grep -Fqx '  race-runtime:' "$WORKFLOW" || {
  echo "ERROR: quality workflow is missing the Linux race job" >&2
  exit 1
}
grep -Fq '    needs: [p0-quality, frontend-quality, runtime-quality]' "$WORKFLOW" || {
  echo "ERROR: Linux release validation must depend on all upstream Linux quality jobs" >&2
  exit 1
}
grep -Fq '    needs: [p0-quality, runtime-quality]' "$WORKFLOW" || {
  echo "ERROR: race validation must reuse the successful runtime gate" >&2
  exit 1
}
grep -Fq '    needs: [p0-quality, linux-release, race-runtime]' "$WORKFLOW" || {
  echo "ERROR: Windows release validation must depend on Linux quality and race" >&2
  exit 1
}
grep -Fq '    runs-on: windows-latest' "$WORKFLOW" || {
  echo "ERROR: Windows release validation must run on a native Windows runner" >&2
  exit 1
}

echo "ci-contract-test: ok"
