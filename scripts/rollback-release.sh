#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
source "$script_dir/release-path-policy.sh"

if [[ $# -ne 2 ]]; then
	echo "usage: rollback-release.sh <install-dir> <backup-dir>" >&2
	exit 2
fi

raw_install_dir="$1"
raw_backup_dir="$2"
install_dir="$(synon_release_safe_path "$raw_install_dir" install)"
backup_dir="$(synon_release_safe_path "$raw_backup_dir" backup)"
if [[ ! -x "$backup_dir/synon-go" || ! -f "$backup_dir/RELEASE_MANIFEST.json" ]]; then
	echo "backup directory is not a verified product release: $backup_dir" >&2
	exit 1
fi
synon_release_assert_product_identity "$backup_dir"
"$backup_dir/synon-go" release-manifest verify --root "$backup_dir" >/dev/null

parent="$(dirname "$install_dir")"
name="$(basename "$install_dir")"
mkdir -p "$parent"
synon_release_revalidate_path "$raw_install_dir" "$install_dir" install
synon_release_revalidate_path "$raw_backup_dir" "$backup_dir" backup
stage="$(mktemp -d "$parent/.${name}.rollback.XXXXXX")"
synon_release_revalidate_managed_child "$parent" "$stage" rollback-stage
previous=""
cleanup() {
	local rc=$?
	local cleanup_failed=0
	trap - EXIT
	if [[ -n "${stage:-}" && -e "$stage" ]]; then
		if synon_release_revalidate_managed_child "$parent" "$stage" rollback-stage; then
			if ! rm -rf -- "$stage"; then
				printf 'rollback stage preserved for manual recovery: %s\n' "$stage" >&2 || true
				cleanup_failed=1
			fi
		else
			printf 'rollback stage preserved for manual recovery: %s\n' "$stage" >&2 || true
			cleanup_failed=1
		fi
	fi
	if [[ -n "${previous:-}" && -e "$previous" ]]; then
		printf 'previous installation preserved for manual recovery: %s\n' "$previous" >&2 || true
		cleanup_failed=1
	fi
	if (( rc == 0 && cleanup_failed != 0 )); then rc=1; fi
	exit "$rc"
}
trap cleanup EXIT

restore_previous() {
	if [[ -z "$previous" || ! -e "$previous" ]]; then return 0; fi
	if [[ -e "$install_dir" ]] ||
		! synon_release_revalidate_path "$raw_install_dir" "$install_dir" install ||
		! synon_release_revalidate_managed_child "$parent" "$previous" previous-install; then
		printf 'unable to restore previous installation automatically; preserved at %s\n' "$previous" >&2
		return 1
	fi
	if ! mv "$previous" "$install_dir"; then
		printf 'unable to restore previous installation automatically; preserved at %s\n' "$previous" >&2
		return 1
	fi
	previous=""
}
cp -a "$backup_dir"/. "$stage"/
synon_release_assert_product_identity "$stage"
"$stage/synon-go" release-manifest verify --root "$stage" >/dev/null
if [[ -e "$install_dir" ]]; then
	synon_release_revalidate_path "$raw_install_dir" "$install_dir" install
	previous="$parent/.${name}.pre-rollback.$(date +%s).$$"
	synon_release_revalidate_managed_child "$parent" "$previous" previous-install
	mv "$install_dir" "$previous"
fi
synon_release_revalidate_path "$raw_install_dir" "$install_dir" install
synon_release_revalidate_managed_child "$parent" "$stage" rollback-stage
if ! mv "$stage" "$install_dir"; then
	restore_previous || true
	exit 1
fi
stage=""
if ! synon_release_assert_product_identity "$install_dir" || ! "$install_dir/synon-go" --health-json >/dev/null || ! "$install_dir/synon-go" release-manifest verify --root "$install_dir" >/dev/null; then
	if synon_release_revalidate_path "$raw_install_dir" "$install_dir" install; then
		rm -rf -- "$install_dir"
	fi
	if restore_previous; then
		echo "rollback candidate failed validation; previous install restored" >&2
	else
		echo "rollback candidate failed validation; manual recovery is required" >&2
	fi
	exit 1
fi
if [[ -n "$previous" && -e "$previous" ]]; then
	synon_release_revalidate_managed_child "$parent" "$previous" previous-install
	rm -rf -- "$previous"
	previous=""
fi
printf 'synon-go rolled back from %s to %s\n' "$backup_dir" "$install_dir"
