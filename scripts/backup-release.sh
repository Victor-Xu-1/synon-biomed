#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
source "$script_dir/release-path-policy.sh"

if [[ $# -ne 2 ]]; then
	echo "usage: backup-release.sh <install-dir> <backup-dir>" >&2
	exit 2
fi

raw_install_dir="$1"
raw_backup_dir="$2"
install_dir="$(synon_release_safe_path "$raw_install_dir" install)"
backup_dir="$(synon_release_safe_path "$raw_backup_dir" backup)"
if [[ ! -x "$install_dir/synon-go" || ! -f "$install_dir/RELEASE_MANIFEST.json" ]]; then
	echo "install directory is not a verified product release: $install_dir" >&2
	exit 1
fi
synon_release_assert_product_identity "$install_dir"
"$install_dir/synon-go" release-manifest verify --root "$install_dir" >/dev/null
if [[ -e "$backup_dir" ]]; then
	echo "backup path already exists: $backup_dir" >&2
	exit 1
fi

parent="$(dirname "$backup_dir")"
name="$(basename "$backup_dir")"
mkdir -p "$parent"
synon_release_revalidate_path "$raw_install_dir" "$install_dir" install
synon_release_revalidate_path "$raw_backup_dir" "$backup_dir" backup
stage="$(mktemp -d "$parent/.${name}.stage.XXXXXX")"
synon_release_revalidate_managed_child "$parent" "$stage" backup-stage
cleanup() {
	local rc=$?
	local cleanup_failed=0
	trap - EXIT
	if [[ -n "${stage:-}" && -e "$stage" ]]; then
		if synon_release_revalidate_managed_child "$parent" "$stage" backup-stage; then
			if ! rm -rf -- "$stage"; then
				printf 'backup stage preserved for manual recovery: %s\n' "$stage" >&2 || true
				cleanup_failed=1
			fi
		else
			printf 'backup stage preserved for manual recovery: %s\n' "$stage" >&2 || true
			cleanup_failed=1
		fi
	fi
	if (( rc == 0 && cleanup_failed != 0 )); then rc=1; fi
	exit "$rc"
}
trap cleanup EXIT
cp -a "$install_dir"/. "$stage"/
synon_release_assert_product_identity "$stage"
"$stage/synon-go" release-manifest verify --root "$stage" >/dev/null
synon_release_revalidate_path "$raw_backup_dir" "$backup_dir" backup
synon_release_revalidate_managed_child "$parent" "$stage" backup-stage
mv "$stage" "$backup_dir"
stage=""
printf 'synon-go release backed up to %s\n' "$backup_dir"
