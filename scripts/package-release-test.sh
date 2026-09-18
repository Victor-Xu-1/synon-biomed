#!/usr/bin/env bash
set -euo pipefail

if [[ $# -gt 0 && ( $# -ne 1 || "$1" != "--readme-contract-only" ) ]]; then
	echo "usage: scripts/package-release-test.sh [--readme-contract-only]" >&2
	exit 2
fi

if [[ "${1:-}" != "--readme-contract-only" && "${SYNON_CLEAN_SOURCE_TEST:-}" != "1" ]]; then
	exec scripts/run-clean-source-test.sh scripts/package-release-test.sh "$@"
fi

tmp="$(mktemp -d)"
IFS=$'\t' read -r product_slug product_version < <("${GO:-go}" run -buildvcs=false ./scripts/product-identity)
passed=false
cleanup() {
	local rc=$?
	local cleanup_failed=0
	trap - EXIT
	if [[ "$passed" == true || "${SYNON_KEEP_FAILURE_ARTIFACTS:-0}" != "1" ]]; then
		if ! rm -rf -- "$tmp"; then
			printf 'unable to remove package release test directory: %s\n' "$tmp" >&2 || true
			cleanup_failed=1
		fi
	else
		printf 'package release failure evidence preserved at %s\n' "$tmp" >&2 || true
	fi
	if (( rc == 0 && cleanup_failed != 0 )); then rc=1; fi
	exit "$rc"
}
trap cleanup EXIT

validate_packaged_readme() {
	python3 - "$1" <<'PY'
import re
import sys
from pathlib import Path

source = Path(sys.argv[1]).read_text(encoding="utf-8")
text = re.sub(r"\s+", " ", source).lower()
if "full web product | certified" in text:
    raise SystemExit("packaged README overclaims full Web certification")
for external in (
    "agents.md", "rewrite_status.json", "source-materials/",
    "docs/governance/development/", "docs/governance/history/",
    "docs/governance/harness-goal.md", "docs/governance/repository-state.md",
    "docs/governance/synon-biomed-optimization-constraints.md",
    "docs/quality-testing/", "docs/compatibility/goals/",
):
    if external in text:
        raise SystemExit("packaged README links to external engineering material: " + external)
PY
}

verify_packaged_readme_contract() {
	test -f "$1"
	grep -Fxq '# Synon Biomed' "$1"
	if ! cmp -s README.md "$1"; then
		echo "packaged README differs from the current source README" >&2
		exit 1
	fi
	validate_packaged_readme "$1"
	for external in \
		docs/governance/harness-goal.md \
		docs/governance/development/new-policy.md \
		docs/quality-testing/new-task.md \
		docs/compatibility/goals/new-goal.md \
		docs/governance/history/new-cleanup.json \
		AGENTS.md \
		REWRITE_STATUS.json; do
		cp "$1" "$tmp/README.md.validation"
		printf '\n[Internal engineering material](%s)\n' "$external" >>"$tmp/README.md.validation"
		if validate_packaged_readme "$tmp/README.md.validation" >/dev/null 2>&1; then
			echo "package README validator allowed external engineering link: $external" >&2
			exit 1
		fi
	done
}

if [[ "${1:-}" == "--readme-contract-only" ]]; then
	cp README.md "$tmp/README.md.candidate"
	verify_packaged_readme_contract "$tmp/README.md.candidate"
	passed=true
	echo "package-release-readme-contract-test: ok"
	exit 0
fi

./scripts/package-release.sh "$tmp/out"

archive="$(find "$tmp/out" -maxdepth 1 -type f -name "${product_slug}-v${product_version}-*.tar.gz" | head -n 1)"
if [[ -z "$archive" ]]; then
	echo "release archive missing" >&2
	exit 1
fi
test -f "$archive.sha256"
(cd "$(dirname "$archive")" && sha256sum -c "$(basename "$archive").sha256") >/dev/null

mkdir -p "$tmp/unpack"
tar --same-permissions -xzf "$archive" -C "$tmp/unpack"
package_dir="$(find "$tmp/unpack" -mindepth 1 -maxdepth 1 -type d | head -n 1)"
if [[ -z "$package_dir" ]]; then
	echo "release package directory missing" >&2
	exit 1
fi

SYNON_WEB_ASSETS="$package_dir/web" ./scripts/package-release.sh "$tmp/out-repeated" >/dev/null
repeated_archive="$(find "$tmp/out-repeated" -maxdepth 1 -type f -name "${product_slug}-v${product_version}-*.tar.gz" | head -n 1)"
if ! cmp -s "$archive" "$repeated_archive"; then
	echo "release archive is not reproducible" >&2
	exit 1
fi
if ! cmp -s "$archive.sha256" "$repeated_archive.sha256"; then
	echo "release archive checksum is not reproducible" >&2
	exit 1
fi

"$package_dir/synon-go" --health-json | grep -Fq "\"version\":\"${product_version}\""
"$package_dir/synon-go" serve --health-json | grep -Fq "\"version\":\"${product_version}\""
model_smoke_plan="$(env -i PATH="$PATH" HOME="$tmp/home" "$package_dir/synon-go" model-smoke --plan --require-all --json)"
printf '%s' "$model_smoke_plan" | grep -q '"secretsRedacted": true'
printf '%s' "$model_smoke_plan" | grep -q '"name": "runner"'
printf '%s' "$model_smoke_plan" | grep -q '"name": "compact"'
printf '%s' "$model_smoke_plan" | grep -q '"provider": "workspace"'
printf '%s' "$model_smoke_plan" | grep -q '"status": "skipped_missing_config"'
model_smoke_builtin="$(env -i PATH="$PATH" HOME="$tmp/home" SYNON_RUNNER_ENABLED=true SYNON_RUNNER_PROVIDER=go_builtin "$package_dir/synon-go" model-smoke --target runner --run --require-all --json)"
printf '%s' "$model_smoke_builtin" | grep -q '"status": "passed"'
printf '%s' "$model_smoke_builtin" | grep -q '"requiresNetwork": false'
"$package_dir/synon-go" assets list --root "$package_dir" | grep -q '"name": "kernel-compute"'
"$package_dir/synon-go" assets list --root "$package_dir" | grep -q '"name": "ketcher-chemistry"'
"$package_dir/synon-go" assets list --root "$package_dir" | grep -q '"name": "synon-link"'
"$package_dir/synon-go" assets list --root "$package_dir" | grep -q '"name": "micromamba"'
source_asset_count="$("$package_dir/synon-go" assets verify --root "$PWD" |
	python3 -c 'import json,sys; print(json.load(sys.stdin)["checked"])')"
package_asset_report="$("$package_dir/synon-go" assets verify --root "$package_dir")"
printf '%s' "$package_asset_report" | grep -q '"valid": true'
package_asset_count="$(printf '%s' "$package_asset_report" |
	python3 -c 'import json,sys; print(json.load(sys.stdin)["checked"])')"
test "$package_asset_count" = "$source_asset_count"
"$package_dir/synon-go" release-manifest verify --root "$package_dir" | grep -q '"valid": true'
test -x "$package_dir/synon-go-live-im-smoke"
live_im_plan="$(env -i PATH="$PATH" HOME="$tmp/home" "$package_dir/synon-go-live-im-smoke" --plan --require-all --json)"
printf '%s' "$live_im_plan" | grep -q '"status":"planned"'
printf '%s' "$live_im_plan" | grep -q '"enabled":\[\]'
printf '%s' "$live_im_plan" | grep -q '"secretsRedacted":true'
configured_live_im_plan="$(env -i PATH="$PATH" HOME="$tmp/home" "$package_dir/synon-go-live-im-smoke" --plan --json --config "$PWD/scripts/testdata/live-im-config.json")"
printf '%s' "$configured_live_im_plan" | grep -q '"enabled":\["wechat","feishu"\]'
printf '%s' "$configured_live_im_plan" | grep -q '"runtimeAuditConfigured":true'
for secret in wechat-package-secret feishu-package-secret; do
	if printf '%s' "$configured_live_im_plan" | grep -Fq "$secret"; then
		echo "live IM plan leaked config secret: $secret" >&2
		exit 1
	fi
done
verify_packaged_readme_contract "$package_dir/README.md"
test -f "$package_dir/LICENSE"
cmp LICENSE "$package_dir/LICENSE"
cmp COMMERCIAL-LICENSE.md "$package_dir/COMMERCIAL-LICENSE.md"
cmp frontend/LICENSE "$package_dir/docs/licenses/frontend/LICENSE"
test -f "$package_dir/product-identity.json"
cmp product-identity.json "$package_dir/product-identity.json"
test -f "$package_dir/.env.example"
test -f "$package_dir/docs/THIRD_PARTY.md"
test -f "$package_dir/docs/operations-runbook.md"
test -f "$package_dir/docs/release-acceptance-contract.md"
grep -Fxq '# Synon Biomed Release Acceptance Contract' \
	"$package_dir/docs/release-acceptance-contract.md"
grep -Fq 'never represents current release authorization' \
	"$package_dir/docs/release-acceptance-contract.md"
test -f "$package_dir/docs/frontend-third-party-licenses.json"
test -f "$package_dir/docs/non-web-asset-boundary.json"
test -f "$package_dir/docs/licenses/aioncore-logos/LICENSE"
test -f "$package_dir/docs/licenses/aioncore-logos/SOURCE.md"
test -f "$package_dir/docs/licenses/synon-2d-interaction-runtime/NOTICE.md"
test -f "$package_dir/docs/licenses/synon-scientific-runtime-warmups/NOTICE.md"
grep -Fq '020a27a77aeb2b5be7a2b4380aab8ae2686311c4' "$package_dir/docs/licenses/aioncore-logos/SOURCE.md"
test -f "$package_dir/RELEASE_MANIFEST.json"
test -f "$package_dir/SBOM.spdx.json"
test -f "$package_dir/THIRD_PARTY_LICENSES.json"
test -f "$package_dir/PROVENANCE.intoto.json"
grep -Fq "pkg:generic/${product_slug}@${product_version}" "$package_dir/PROVENANCE.intoto.json"
"$package_dir/synon-go" release-supply-chain verify --root "$package_dir" | grep -q '"valid": true'
test ! -e "$package_dir/docs/compatibility"
test ! -e "$package_dir/docs/full-product"
test ! -e "$package_dir/docs/provenance"
test ! -e "$package_dir/REWRITE_STATUS.json"
test -f "$package_dir/skills/synonbiomed/literature-review/SKILL.md"
test -f "$package_dir/assets/synonbiomed/agents.manifest.json"
test -f "$package_dir/assets/synonbiomed/agents/operon/metadata.yaml"
test -f "$package_dir/assets/optional/mcp-servers/bio-tools.manifest.json"
test -f "$package_dir/assets/optional/mcp-servers/bio-tools/run_server.py"
test -f "$package_dir/assets/optional/mcp-servers/ketcher-chemistry.manifest.json"
test -f "$package_dir/assets/optional/mcp-servers/ketcher-chemistry/NOTICE"
test -f "$package_dir/assets/optional/mcp-servers/ketcher-chemistry/widget/index.html.gz"
test "$(stat -c %s "$package_dir/assets/optional/mcp-servers/ketcher-chemistry/widget/index.html.gz")" -lt 10485760
printf '{"jsonrpc":"2.0","id":1,"method":"tools/list"}\n' |
	"$package_dir/synon-go" mcp-ketcher |
	grep -q '"name":"open_sketcher"'
test -f "$package_dir/assets/optional/kernel-compute.manifest.json"
test -f "$package_dir/assets/optional/kernels/kernel_worker.py"
test -f "$package_dir/assets/optional/micromamba/LICENSE"
test "$("$package_dir/assets/optional/micromamba/linux-x86_64/micromamba" --version)" = "2.9.0"
# The existing assets-verify check above is the single checksum authority.
test -f "$package_dir/assets/optional/micromamba/BUILD.md"
test -f "$package_dir/assets/optional/micromamba/DEPENDENCY-NOTICES.txt"
test -f "$package_dir/assets/synon-link/manifest.json"
test -f "$package_dir/assets/synon-link/synon-link-extension-v0.6.10.zip"
test -f "$package_dir/web/index.html"
find "$package_dir/web" -type f -name '*.js' -print -quit | grep -q .
find "$package_dir/web" -type f -name '*.css' -print -quit | grep -q .
printf '2cb251ad8aedf940f6d3a11b41b74ed55f9b912ab48257a21789b78a1ca0c90b  %s\n' "$package_dir/assets/synon-link/synon-link-extension-v0.6.10.zip" | sha256sum -c -
test ! -e "$package_dir/scripts/install-release.sh"
test ! -e "$package_dir/scripts/install-release.ps1"
test -f "$package_dir/scripts/release-identity-policy.ps1"
test -x "$package_dir/scripts/install-systemd-user.sh"
test -x "$package_dir/scripts/release-path-policy.sh"
test -x "$package_dir/scripts/uninstall-systemd-user.sh"
test -x "$package_dir/scripts/backup-release.sh"
test -x "$package_dir/scripts/rollback-release.sh"
test -x "$package_dir/scripts/uninstall-release.sh"
test -f "$package_dir/scripts/manage-release.ps1"
test ! -e "$package_dir/scripts/optional/websearch.py"
test ! -e "$package_dir/tools"
test ! -e "$package_dir/go.mod"
test ! -e "$package_dir/go.sum"
test ! -e "$package_dir/assets/synonbiomed/sqlite-migrations"
test ! -e "$package_dir/assets/synonbiomed/sqlite-migrations.manifest.json"

for required in \
	product-self-knowledge alphafold2 boltz diffdock drug-discovery-pipeline \
	proteinmpnn rfdiffusion-nim aidd-expert computational-chem-expert \
	dmpk-expert medchem-expert structural-biology-expert; do
	if ! grep -Fq "/$required/" "$package_dir/RELEASE_MANIFEST.json"; then
		echo "release package is missing required bundled domain asset: $required" >&2
		exit 1
	fi
done
if ! grep -R -Fq '"get_admet"' "$package_dir/assets/optional/mcp-servers/bio-tools"; then
	echo "release package is missing the bundled ChEMBL get_admet tool" >&2
	exit 1
fi

python3 - "$package_dir/RELEASE_MANIFEST.json" <<'PY'
import json
import sys
from pathlib import Path

manifest = json.loads(Path(sys.argv[1]).read_text())
assert manifest["schemaVersion"] == 4, "new packages must use the current manifest schema"
assert manifest["integrity"] == "sha256", "package integrity must remain explicit"
assert manifest["fileCount"] == len(manifest["files"]) > 0, "file inventory must be complete"
assert "coverage" not in manifest, "new packages must not embed historical engineering scores"
assert "releaseEligible" not in manifest, "a package cannot grant its own release authorization"
PY
grep -q '"bun": false' "$package_dir/RELEASE_MANIFEST.json"
grep -q '"node": false' "$package_dir/RELEASE_MANIFEST.json"
grep -q '"go": false' "$package_dir/RELEASE_MANIFEST.json"
grep -q '"python": false' "$package_dir/RELEASE_MANIFEST.json"

if find "$package_dir" -type d ! -path "$package_dir/docs/licenses/frontend" \( -name .git -o -name node_modules -o -name desktop -o -name frontend -o -name webapp -o -name web-ui -o -name dist -o -name models -o -name vendor -o -name users -o -name workspace -o -name runtime -o -name uploads -o -name mcp-output -o -name test-results \) -print | grep -q .; then
	echo "release package contains banned directory" >&2
	exit 1
fi

if find "$package_dir" -type f -name '*.go' -print | grep -q .; then
	echo "release package contains Go source" >&2
	exit 1
fi

if find "$package_dir" -type f \( -name '.env' -o -name '.mcp.json' -o -name '.synon.json' -o -iname '*token*' -o -iname '*.db' -o -iname '*.sqlite' -o -iname '*.sqlite3' -o -iname '*.pid' -o -iname '*.log' \) -print | grep -q .; then
	echo "release package contains banned file" >&2
	exit 1
fi

for personal_path in '/home/victor_1' 'C:\Users\Victor'; do
	if grep -IRFq "$personal_path" "$package_dir"; then
		echo "release package contains a personal machine path: $personal_path" >&2
		exit 1
	fi
done

package_web_files=$(find "$package_dir" -type f ! -path "$package_dir/web/*" \( \
  -iname '*.html' -o -iname '*.htm' -o -iname '*.css' \
  -o -iname '*.scss' -o -iname '*.sass' -o -iname '*.less' \
  -o -iname '*.js' -o -iname '*.mjs' -o -iname '*.cjs' \
  -o -iname '*.jsx' -o -iname '*.ts' -o -iname '*.tsx' \
  -o -iname '*.vue' -o -iname '*.svelte' -o -iname '*.astro' \
\) -printf '%P\n' | sort)
expected_package_web_files=$(printf '%s\n' \
  'skills/synonbiomed/skill-creator/assets/eval_review.html' \
  'skills/synonbiomed/skill-creator/eval-viewer/viewer.html' | sort)
if [[ "$package_web_files" != "$expected_package_web_files" ]]; then
  echo "release package contains unclassified Web-like files" >&2
  diff -u <(printf '%s\n' "$expected_package_web_files") <(printf '%s\n' "$package_web_files") >&2 || true
  exit 1
fi

if find "$package_dir/web" -type l -print | grep -q .; then
	echo "release package Web assets contain symbolic links" >&2
	exit 1
fi

zip_files="$(find "$package_dir" -type f -name '*.zip' -print)"
if [[ "$zip_files" != "$package_dir/assets/synon-link/synon-link-extension-v0.6.10.zip" ]]; then
	echo "release package must contain only the pinned Synon Link zip" >&2
	printf '%s\n' "$zip_files" >&2
	exit 1
fi

large_files="$(find "$package_dir" -type f ! -name synon-go -size +10M -printf '%P\n' | sort)"
expected_large_files="$(printf '%s\n' \
	'assets/optional/micromamba/linux-x86_64/micromamba' \
	'synon-go-live-im-smoke' | sort)"
if [[ "$large_files" != "$expected_large_files" ]]; then
	echo "release package contains an unclassified file larger than 10MB" >&2
	printf '%s\n' "$large_files" >&2
	exit 1
fi

if [[ -n "${SYNON_RELEASE_TEST_ARCHIVE:-}" ]]; then
	cache_path="$(realpath -m "$SYNON_RELEASE_TEST_ARCHIVE")"
	mkdir -p "$(dirname "$cache_path")"
	cp "$archive" "$cache_path"
	cp "$archive.sha256" "$cache_path.sha256"
fi

passed=true
trap - EXIT
if ! rm -rf -- "$tmp"; then
	printf 'unable to remove package release test directory: %s\n' "$tmp" >&2
	exit 1
fi
echo "RELEASE_PACKAGE_TEST=yes"
