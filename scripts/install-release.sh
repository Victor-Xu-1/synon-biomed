#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
source "$script_dir/release-path-policy.sh"

umask 022

if [[ $# -ne 2 ]]; then
	echo "usage: install-release.sh <synon-go-archive.tar.gz> <install-dir>" >&2
	exit 2
fi

archive="$1"
raw_install_dir="$2"
install_dir="$(synon_release_safe_path "$raw_install_dir" install)"
tmp="$(mktemp -d)"
stage=""
backup=""
cleanup() {
	local rc=$?
	local cleanup_failed=0
	trap - EXIT
	if ! rm -rf -- "$tmp"; then
		printf 'unable to remove install temporary directory: %s\n' "$tmp" >&2 || true
		cleanup_failed=1
	fi
	if [[ -n "$stage" && -e "$stage" ]]; then
		if [[ -n "${parent:-}" ]] && synon_release_revalidate_managed_child "$parent" "$stage" install-stage; then
			if ! rm -rf -- "$stage"; then
				printf 'install stage preserved for manual recovery: %s\n' "$stage" >&2 || true
				cleanup_failed=1
			fi
		else
			printf 'install stage preserved for manual recovery: %s\n' "$stage" >&2 || true
			cleanup_failed=1
		fi
	fi
	if [[ -n "$backup" && -e "$backup" ]]; then
		printf 'previous installation preserved for manual recovery: %s\n' "$backup" >&2 || true
		cleanup_failed=1
	fi
	if (( rc == 0 && cleanup_failed != 0 )); then rc=1; fi
	exit "$rc"
}
trap cleanup EXIT

restore_backup() {
	if [[ -z "$backup" || ! -e "$backup" ]]; then return 0; fi
	if [[ -e "$install_dir" ]] ||
		! synon_release_revalidate_path "$raw_install_dir" "$install_dir" install ||
		! synon_release_revalidate_managed_child "$parent" "$backup" install-backup; then
		printf 'unable to restore previous installation automatically; preserved at %s\n' "$backup" >&2
		return 1
	fi
	if ! mv "$backup" "$install_dir"; then
		printf 'unable to restore previous installation automatically; preserved at %s\n' "$backup" >&2
		return 1
	fi
	backup=""
}

if [[ ! -f "$archive" ]]; then
	echo "archive not found: $archive" >&2
	exit 1
fi

while IFS= read -r entry; do
	if [[ -z "$entry" || "$entry" == /* || "$entry" == *\\* || "/$entry/" == *"/../"* ]]; then
		echo "archive contains an unsafe path: $entry" >&2
		exit 1
	fi
done < <(tar -tzf "$archive")
while IFS= read -r detail; do
	case "${detail:0:1}" in
	-|d) ;;
	*)
		echo "archive contains a non-regular entry: $detail" >&2
		exit 1
		;;
	esac
done < <(LC_ALL=C tar -tvzf "$archive")

tar --no-same-owner --no-same-permissions -xzf "$archive" -C "$tmp"
package_dir="$(find "$tmp" -mindepth 1 -maxdepth 1 -type d | head -n 1)"
if [[ -z "$package_dir" || ! -x "$package_dir/synon-go" ]]; then
	echo "archive does not contain a runnable synon-go package" >&2
	exit 1
fi
if [[ "$(find "$tmp" -mindepth 1 -maxdepth 1 | wc -l)" -ne 1 ]]; then
	echo "archive must contain exactly one package directory" >&2
	exit 1
fi

synon_release_assert_product_identity "$package_dir"
"$package_dir/synon-go" release-manifest verify --root "$package_dir" >/dev/null
"$package_dir/synon-go" --health-json >/dev/null

parent="$(dirname "$install_dir")"
name="$(basename "$install_dir")"
mkdir -p "$parent"
synon_release_revalidate_path "$raw_install_dir" "$install_dir" install
stage="$(mktemp -d "$parent/.${name}.stage.XXXXXX")"
synon_release_revalidate_managed_child "$parent" "$stage" install-stage
cp -a "$package_dir"/. "$stage"/
synon_release_assert_product_identity "$stage"
"$stage/synon-go" release-manifest verify --root "$stage" >/dev/null

if [[ -e "$install_dir" ]]; then
	synon_release_revalidate_path "$raw_install_dir" "$install_dir" install
	backup="$parent/.${name}.backup.$(date +%s).$$"
	synon_release_revalidate_managed_child "$parent" "$backup" install-backup
	mv "$install_dir" "$backup"
fi
synon_release_revalidate_path "$raw_install_dir" "$install_dir" install
synon_release_revalidate_managed_child "$parent" "$stage" install-stage
if ! mv "$stage" "$install_dir"; then
	restore_backup || true
	exit 1
fi
stage=""

if ! synon_release_assert_product_identity "$install_dir" || ! "$install_dir/synon-go" --health-json >/dev/null; then
	synon_release_revalidate_path "$raw_install_dir" "$install_dir" install
	rm -rf "$install_dir"
	restore_backup || true
	exit 1
fi
if [[ -n "$backup" && -e "$backup" ]]; then
	synon_release_revalidate_managed_child "$parent" "$backup" install-backup
	rm -rf -- "$backup"
	backup=""
fi
printf 'synon-go installed to %s\n' "$install_dir"
