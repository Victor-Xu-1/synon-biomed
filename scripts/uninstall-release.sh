#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
source "$script_dir/release-path-policy.sh"

allow_corrupt=false
if [[ "${1:-}" == "--allow-corrupt" ]]; then allow_corrupt=true; shift; fi
if [[ $# -ne 1 ]]; then
	echo "usage: uninstall-release.sh [--allow-corrupt] <install-dir>" >&2
	exit 2
fi

raw_install_dir="$1"
install_dir="$(synon_release_safe_path "$raw_install_dir" install)"
if [[ "$install_dir" == "$(dirname "$(realpath -m -- "${HOME:?HOME is required}")")" ]]; then
	echo "refusing unsafe install directory: $install_dir" >&2
	exit 1
fi
if [[ ! -d "$install_dir" || ! -f "$install_dir/RELEASE_MANIFEST.json" ]]; then
	echo "install directory is not a verified product release: $install_dir" >&2
	exit 1
fi
if [[ "$allow_corrupt" != true ]]; then
	if [[ ! -x "$install_dir/synon-go" ]]; then
		echo "release binary is missing; use --allow-corrupt after checking the path" >&2
		exit 1
	fi
	synon_release_assert_product_identity "$install_dir"
	"$install_dir/synon-go" release-manifest verify --root "$install_dir" >/dev/null
fi
synon_release_revalidate_path "$raw_install_dir" "$install_dir" install
rm -rf -- "$install_dir"
printf 'synon-go uninstalled from %s; external data directories were preserved\n' "$install_dir"
