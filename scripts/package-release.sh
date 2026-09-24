#!/usr/bin/env bash
set -euo pipefail

umask 022

out_dir="${1:-release}"
go_bin="${GO:-go}"
npm_bin="${NPM:-npm}"
IFS=$'\t' read -r product_slug version < <(env -u GOOS -u GOARCH "$go_bin" run -buildvcs=false ./scripts/product-identity)
if [[ ! "$product_slug" =~ ^[a-z0-9]+(-[a-z0-9]+)*$ || ! "$version" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
	echo "unable to derive product identity" >&2
	exit 1
fi
host_goos="$(env -u GOOS -u GOARCH "$go_bin" env GOOS)"
host_goarch="$(env -u GOOS -u GOARCH "$go_bin" env GOARCH)"
goos="${GOOS:-$host_goos}"
goarch="${GOARCH:-$host_goarch}"
package_name="${product_slug}-v${version}-${goos}-${goarch}"
source_date_epoch="${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct 2>/dev/null || printf '0')}"
source_uri="${SYNON_SOURCE_URI:-pkg:generic/${product_slug}@${version}}"
source_revision="unversioned"
source_dirty="true"

if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
	source_revision=$(git rev-parse HEAD)
	if [[ -z "$(git status --porcelain --untracked-files=normal)" ]]; then
		source_dirty="false"
	fi
fi
if [[ "$source_dirty" == "true" ]]; then
	echo "formal release requires a clean source revision" >&2
	exit 1
fi
source_sha256=$(bash scripts/source-tree-digest.sh .)

if [[ ! "$source_date_epoch" =~ ^[0-9]+$ ]]; then
	echo "SOURCE_DATE_EPOCH must be a non-negative Unix timestamp" >&2
	exit 1
fi

binary_suffix=""
if [[ "$goos" == "windows" ]]; then
	binary_suffix=".exe"
fi

tmp="$(mktemp -d)"
cleanup() {
	case "$tmp" in
	/tmp/tmp.*)
		rm -rf -- "$tmp"
		;;
	*)
		echo "refusing to remove unexpected release temp path: $tmp" >&2
		return 1
		;;
	esac
}
trap cleanup EXIT

mkdir -p "$out_dir"
pkg="$tmp/$package_name"
mkdir -p "$pkg/scripts"
mkdir -p "$pkg/skills"
mkdir -p "$pkg/assets"
mkdir -p "$pkg/docs"

web_assets="${SYNON_WEB_ASSETS:-}"
if [[ -z "$web_assets" ]]; then
	frontend_build="$tmp/frontend-build"
	mkdir -p "$frontend_build"
	tar -C frontend \
		--exclude='./node_modules' --exclude='*/node_modules' \
		--exclude='*/out' --exclude='*/test-results' \
		--exclude='*/playwright-report' --exclude='*/coverage' \
		-cf - . | tar -C "$frontend_build" -xf -
	(
		cd "$frontend_build"
		"$npm_bin" ci --ignore-scripts
		NODE_OPTIONS="--max-old-space-size=4096" "$npm_bin" run build
	)
	web_assets="$frontend_build/out/renderer"
fi
if [[ ! -f "$web_assets/index.html" ]]; then
	echo "Web assets are missing index.html: $web_assets" >&2
	exit 1
fi
if find "$web_assets" -type l -print -quit | grep -q .; then
	echo "Web assets must not contain symbolic links: $web_assets" >&2
	exit 1
fi
mkdir -p "$pkg/web"
cp -R "$web_assets"/. "$pkg/web/"

target_binary="$pkg/synon-go${binary_suffix}"
target_live_smoke="$pkg/synon-go-live-im-smoke${binary_suffix}"
CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" "$go_bin" build -trimpath -buildvcs=false -ldflags='-s -w -buildid=' -o "$target_binary" ./cmd/synon
CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" "$go_bin" build -trimpath -buildvcs=false -ldflags='-s -w -buildid=' -o "$target_live_smoke" ./scripts/smoke-live-im

cp README.md "$pkg/README.md"
cp product-identity.json "$pkg/product-identity.json"
cp LICENSE "$pkg/LICENSE"
cp COMMERCIAL-LICENSE.md "$pkg/COMMERCIAL-LICENSE.md"
cp .env.example "$pkg/.env.example"
cp docs/THIRD_PARTY.md "$pkg/docs/THIRD_PARTY.md"
cp docs/operations-runbook.md "$pkg/docs/operations-runbook.md"
cp docs/release-acceptance-contract.md "$pkg/docs/release-acceptance-contract.md"
cp frontend/THIRD_PARTY_LICENSES.json "$pkg/docs/frontend-third-party-licenses.json"
cp docs/non-web-asset-boundary.json "$pkg/docs/non-web-asset-boundary.json"
mkdir -p "$pkg/docs/licenses/aioncore-logos"
mkdir -p "$pkg/docs/licenses/frontend"
cp frontend/LICENSE "$pkg/docs/licenses/frontend/LICENSE"
cp -R docs/licenses/. "$pkg/docs/licenses/"
cp internal/logoassets/LICENSE "$pkg/docs/licenses/aioncore-logos/LICENSE"
cp internal/logoassets/SOURCE.md "$pkg/docs/licenses/aioncore-logos/SOURCE.md"
cp -R skills/. "$pkg/skills/"
cp -R assets/. "$pkg/assets/"
# Keep only the native installer and Conda lock catalog for the target package.
# The source checkout retains all platform assets, but a release must not ship
# or accidentally select a foreign executable or package lock.
runtime_platform=""
case "$goos/$goarch" in
	linux/amd64) runtime_platform="linux-x86_64" ;;
	windows/amd64) runtime_platform="windows-x86_64" ;;
	darwin/amd64) runtime_platform="darwin-x86_64" ;;
	darwin/arm64) runtime_platform="darwin-arm64" ;;
esac
if [[ -z "$runtime_platform" ]]; then
	echo "unsupported native scientific runtime target: $goos/$goarch" >&2
	exit 1
fi
installer_root="$pkg/assets/optional/micromamba"
conda_catalog_root="$pkg/assets/optional/conda-runtimes"
installer_name="micromamba"
if [[ "$goos" == "windows" ]]; then installer_name="micromamba.exe"; fi
test -f "$installer_root/$runtime_platform/$installer_name" || {
	echo "native micromamba asset is missing for $runtime_platform" >&2
	exit 1
}
if [[ "$runtime_platform" == "linux-x86_64" ]]; then
	test -f "$installer_root/manifest.json"
	test -f "$conda_catalog_root/manifest.json"
else
	test -f "$installer_root/$runtime_platform/manifest.json"
	test -f "$conda_catalog_root/$runtime_platform/manifest.json"
	rm -f -- "$installer_root/manifest.json" "$conda_catalog_root/manifest.json"
fi
find "$installer_root" -mindepth 1 -maxdepth 1 -type d ! -name "$runtime_platform" -exec rm -rf -- {} +
if [[ "$runtime_platform" == "linux-x86_64" ]]; then
	find "$conda_catalog_root" -mindepth 1 -maxdepth 1 -type d \
		\( -name '*-x86_64' -o -name 'darwin-arm64' \) ! -name "$runtime_platform" -exec rm -rf -- {} +
else
	find "$conda_catalog_root" -mindepth 1 -maxdepth 1 -type d \
		! -name "$runtime_platform" -exec rm -rf -- {} +
fi
cp scripts/release-path-policy.sh "$pkg/scripts/release-path-policy.sh"
cp scripts/release-identity-policy.ps1 "$pkg/scripts/release-identity-policy.ps1"
cp scripts/install-systemd-user.sh "$pkg/scripts/install-systemd-user.sh"
cp scripts/uninstall-systemd-user.sh "$pkg/scripts/uninstall-systemd-user.sh"
cp scripts/backup-release.sh "$pkg/scripts/backup-release.sh"
cp scripts/rollback-release.sh "$pkg/scripts/rollback-release.sh"
cp scripts/uninstall-release.sh "$pkg/scripts/uninstall-release.sh"
cp scripts/manage-release.ps1 "$pkg/scripts/manage-release.ps1"
chmod +x "$target_binary" "$target_live_smoke" \
	"$pkg/scripts/release-path-policy.sh" "$pkg/scripts/backup-release.sh" \
	"$pkg/scripts/rollback-release.sh" "$pkg/scripts/uninstall-release.sh" \
	"$pkg/scripts/install-systemd-user.sh" "$pkg/scripts/uninstall-systemd-user.sh"

manifest_tool="$target_binary"
if [[ "$goos" != "$host_goos" || "$goarch" != "$host_goarch" ]]; then
	manifest_tool="$tmp/synon-go-manifest-tool"
	env -u GOOS -u GOARCH CGO_ENABLED=0 "$go_bin" build -trimpath -buildvcs=false -ldflags='-s -w -buildid=' -o "$manifest_tool" ./cmd/synon
fi

module_cache=$(env -u GOOS -u GOARCH "$go_bin" env GOMODCACHE)
go_root=$(env -u GOOS -u GOARCH "$go_bin" env GOROOT)
supply_chain_args=(
	release-supply-chain create
	--root "$pkg"
	--goos "$goos"
	--goarch "$goarch"
	--binaries "synon-go${binary_suffix},synon-go-live-im-smoke${binary_suffix}"
	--module-cache "$module_cache"
	--go-root "$go_root"
	--license-overrides "docs/licenses"
	--source-uri "$source_uri"
	--source-revision "$source_revision"
	--source-sha256 "$source_sha256"
)
if [[ "$source_dirty" == "true" ]]; then
	supply_chain_args+=(--source-dirty)
fi
SOURCE_DATE_EPOCH="$source_date_epoch" "$manifest_tool" "${supply_chain_args[@]}" >/dev/null
"$manifest_tool" release-supply-chain verify --root "$pkg" >/dev/null

SOURCE_DATE_EPOCH="$source_date_epoch" "$manifest_tool" release-manifest create \
	--root "$pkg" --goos "$goos" --goarch "$goarch" --require-supply-chain >/dev/null
"$manifest_tool" release-manifest verify --root "$pkg" >/dev/null

archive="$out_dir/$package_name.tar.gz"
tar -C "$tmp" --sort=name --mtime="@$source_date_epoch" --owner=0 --group=0 --numeric-owner \
	-cf - "$package_name" | gzip -n >"$archive"
(
	cd "$out_dir"
	sha256sum "$(basename "$archive")" >"$(basename "$archive").sha256"
)
printf '%s\n' "$archive"
