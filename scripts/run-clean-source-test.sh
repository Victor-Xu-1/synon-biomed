#!/usr/bin/env bash
set -euo pipefail

if (( $# == 0 )); then
	echo "usage: $0 <repository-relative-test-script> [args...]" >&2
	exit 2
fi
if [[ "${SYNON_CLEAN_SOURCE_TEST:-}" == "1" ]] ||
	! git rev-parse --is-inside-work-tree >/dev/null 2>&1 ||
	[[ -z "$(git status --porcelain --untracked-files=normal)" ]]; then
	SYNON_CLEAN_SOURCE_TEST=1 exec bash "$@"
fi

tmp="$(mktemp -d)"
cleanup() {
	case "$tmp" in
	/tmp/tmp.*) rm -rf -- "$tmp" ;;
	*) echo "refusing to remove unexpected clean-test path: $tmp" >&2 ;;
	esac
}
trap cleanup EXIT

snapshot="$tmp/source-snapshot"
mkdir -p "$snapshot"
chmod --reference=. "$snapshot"
tar \
	--exclude='./.git' --exclude='./node_modules' --exclude='*/node_modules' \
	--exclude='*/out' --exclude='*/test-results' --exclude='*/playwright-report' \
	--exclude='*/coverage' --exclude='./release' --exclude='./release-windows' \
	-cf - . | tar --same-permissions -C "$snapshot" -xf -
(
	cd "$snapshot"
	git init -q
	git config user.name 'Synon Release Test'
	git config user.email 'release-test@localhost.invalid'
	git add -A
	git commit -q -m 'test: clean release snapshot'
	SYNON_CLEAN_SOURCE_TEST=1 bash "$@"
)
