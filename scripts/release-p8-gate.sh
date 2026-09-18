#!/usr/bin/env bash
set -euo pipefail

if ! command -v powershell.exe >/dev/null 2>&1 || ! command -v wslpath >/dev/null 2>&1; then
	echo "native Windows PowerShell is required for the P8 release gate" >&2
	exit 1
fi

tmp="$(mktemp -d)"
IFS=$'\t' read -r product_slug product_version < <("${GO:-go}" run -buildvcs=false ./scripts/product-identity)
passed=false
cleanup() {
	local rc=$?
	trap - EXIT
	if [[ "$passed" == true ]]; then
		rm -rf -- "$tmp"
	else
		printf 'P8 failure evidence preserved at %s\n' "$tmp" >&2
	fi
	exit "$rc"
}
trap cleanup EXIT

archive="$tmp/${product_slug}-v${product_version}-linux-amd64.tar.gz"
SYNON_KEEP_FAILURE_ARTIFACTS=1 SYNON_RELEASE_TEST_ARCHIVE="$archive" \
	bash scripts/package-release-test.sh 2>&1 | tee "$tmp/package-release-test.log"
test -f "$archive"
test -f "$archive.sha256"
SYNON_KEEP_FAILURE_ARTIFACTS=1 SYNON_RELEASE_TEST_ARCHIVE="$archive" \
	bash scripts/install-release-test.sh 2>&1 | tee "$tmp/install-release-test.log"
SYNON_KEEP_FAILURE_ARTIFACTS=1 SYNON_RELEASE_TEST_ARCHIVE="$archive" \
	bash scripts/release-lifecycle-test.sh 2>&1 | tee "$tmp/release-lifecycle-test.log"

mkdir -p "$tmp/linux-unpack"
tar --same-permissions -xzf "$archive" -C "$tmp/linux-unpack"
linux_package="$(find "$tmp/linux-unpack" -mindepth 1 -maxdepth 1 -type d -print -quit)"
test -f "$linux_package/web/index.html"
SYNON_KEEP_FAILURE_ARTIFACTS=1 SYNON_WEB_ASSETS="$linux_package/web" \
	bash scripts/package-windows-release-test.sh "$tmp/windows" \
	2>&1 | tee "$tmp/package-windows-release-test.log"
windows_archive="$(find "$tmp/windows" -maxdepth 1 -type f -name "${product_slug}-v${product_version}-windows-amd64.tar.gz" -print -quit)"
test -f "$windows_archive"
test -f "$windows_archive.sha256"

windows_log="$tmp/windows-release-test.log"
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$(wslpath -w "$PWD/scripts/release-windows-test.ps1")" -Archive "$(wslpath -w "$windows_archive")" -RepoRoot "$(wslpath -w "$PWD")" 2>&1 | tee "$windows_log"
if ! tr -d '\r' <"$windows_log" | grep -Fxq 'WINDOWS_RELEASE_TEST=yes'; then
	echo "native Windows lifecycle did not emit its success marker" >&2
	exit 1
fi

passed=true
trap - EXIT
case "$tmp" in
/tmp/tmp.*) rm -rf -- "$tmp" ;;
*) echo "refusing to remove unexpected P8 path: $tmp" >&2; exit 1 ;;
esac
echo "P8_RELEASE_GATE=yes"
