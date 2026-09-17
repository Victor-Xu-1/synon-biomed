#!/usr/bin/env bash
set -euo pipefail

if [[ "${SYNON_CLEAN_SOURCE_TEST:-}" != "1" ]]; then
	out_dir="$(realpath -m "${1:-release-windows}")"
	exec scripts/run-clean-source-test.sh scripts/package-windows-release-test.sh "$out_dir"
fi

out_dir="${1:-release-windows}"
tmp="$(mktemp -d)"
passed=false
cleanup() {
	local rc=$?
	local cleanup_failed=0
	trap - EXIT
	if [[ "$passed" == true || "${SYNON_KEEP_FAILURE_ARTIFACTS:-0}" != "1" ]]; then
		if ! rm -rf -- "$tmp"; then
			printf 'unable to remove Windows package test directory: %s\n' "$tmp" >&2 || true
			cleanup_failed=1
		fi
	else
		printf 'Windows package failure evidence preserved at %s\n' "$tmp" >&2 || true
	fi
	if (( rc == 0 && cleanup_failed != 0 )); then rc=1; fi
	exit "$rc"
}
trap cleanup EXIT

GOOS=windows GOARCH=amd64 ./scripts/package-release.sh "$tmp/first" >/dev/null
first="$(find "$tmp/first" -maxdepth 1 -type f -name 'synon-biomed-v0.1.1-windows-amd64.tar.gz' -print -quit)"
test -n "$first"
mkdir -p "$tmp/unpack"
tar --same-permissions -xzf "$first" -C "$tmp/unpack"
package_dir="$(find "$tmp/unpack" -mindepth 1 -maxdepth 1 -type d -print -quit)"
test -f "$package_dir/web/index.html"
test -f "$package_dir/synon-go.exe"
test -f "$package_dir/synon-go-live-im-smoke.exe"
test ! -e "$package_dir/assets/optional/micromamba"
if find "$package_dir" -type f ! -name synon-go.exe ! -name synon-go-live-im-smoke.exe -size +10M -print | grep -q .; then
	echo "Windows release contains an unclassified file larger than 10MB" >&2
	exit 1
fi

GOOS=windows GOARCH=amd64 SYNON_WEB_ASSETS="$package_dir/web" ./scripts/package-release.sh "$tmp/second" >/dev/null
second="$(find "$tmp/second" -maxdepth 1 -type f -name 'synon-biomed-v0.1.1-windows-amd64.tar.gz' -print -quit)"
if ! cmp -s "$first" "$second"; then
	echo "Windows release archive is not reproducible" >&2
	exit 1
fi
if ! cmp -s "$first.sha256" "$second.sha256"; then
	echo "Windows release archive checksum is not reproducible" >&2
	exit 1
fi

grep -q '"goos": "windows"' "$package_dir/RELEASE_MANIFEST.json"
grep -q '"goarch": "amd64"' "$package_dir/RELEASE_MANIFEST.json"
grep -q '"node": false' "$package_dir/RELEASE_MANIFEST.json"
grep -q '"bun": false' "$package_dir/RELEASE_MANIFEST.json"
mkdir -p "$out_dir"
cp "$first" "$out_dir/"
cp "$first.sha256" "$out_dir/"
result="$out_dir/$(basename "$first")"
passed=true
trap - EXIT
if ! rm -rf -- "$tmp"; then
	printf 'unable to remove Windows package test directory: %s\n' "$tmp" >&2
	exit 1
fi
echo "$result"
