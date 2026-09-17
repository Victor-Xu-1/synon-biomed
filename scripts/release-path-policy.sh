#!/usr/bin/env bash

# Resolve a release-management target without allowing the target, one of its
# existing ancestors, or the current working directory to be reached through a
# symbolic link. Callers must revalidate immediately before destructive moves.
synon_release_safe_path() {
	local raw_path="${1:-}"
	local purpose="${2:-release}"
	if [[ -z "$raw_path" ]]; then
		echo "refusing an empty $purpose path" >&2
		return 1
	fi

	local lexical_path resolved_path cwd_path home_path
	lexical_path="$(realpath -ms -- "$raw_path")"
	resolved_path="$(realpath -m -- "$raw_path")"
	cwd_path="$(realpath -m -- "$PWD")"
	home_path="$(realpath -m -- "${HOME:?HOME is required}")"
	if [[ "$lexical_path" == "/" || "$lexical_path" == "$cwd_path" || "$resolved_path" == "$home_path" ]]; then
		echo "refusing unsafe $purpose path: $resolved_path" >&2
		return 1
	fi
	if [[ "$lexical_path" != "$resolved_path" ]]; then
		echo "refusing $purpose path through a symbolic link: $lexical_path" >&2
		return 1
	fi
	printf '%s\n' "$resolved_path"
}

synon_release_revalidate_path() {
	local raw_path="$1"
	local expected_path="$2"
	local purpose="${3:-release}"
	local current_path
	current_path="$(synon_release_safe_path "$raw_path" "$purpose")" || return 1
	if [[ "$current_path" != "$expected_path" ]]; then
		echo "$purpose path changed during the operation" >&2
		return 1
	fi
}

synon_release_managed_child() {
	local parent_path="$1"
	local child_path="$2"
	local purpose="${3:-managed release}"
	local canonical_parent lexical_child resolved_child
	canonical_parent="$(realpath -m -- "$parent_path")"
	lexical_child="$(realpath -ms -- "$child_path")"
	resolved_child="$(realpath -m -- "$child_path")"
	if [[ "$lexical_child" != "$resolved_child" || "$(dirname -- "$resolved_child")" != "$canonical_parent" ]]; then
		echo "refusing unsafe generated $purpose path" >&2
		return 1
	fi
	printf '%s\n' "$resolved_child"
}

synon_release_revalidate_managed_child() {
	local parent_path="$1"
	local child_path="$2"
	local purpose="${3:-managed release}"
	local current_path
	current_path="$(synon_release_managed_child "$parent_path" "$child_path" "$purpose")" || return 1
	if [[ "$current_path" != "$child_path" ]]; then
		echo "generated $purpose path changed during the operation" >&2
		return 1
	fi
}

synon_release_trusted_identity_path() {
	local policy_dir trusted_identity
	policy_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)" || return 1
	trusted_identity="$policy_dir/../product-identity.json"
	if [[ ! -f "$trusted_identity" || -L "$trusted_identity" ]]; then
		echo "trusted product identity is unavailable" >&2
		return 1
	fi
	printf '%s\n' "$trusted_identity"
}

synon_release_assert_product_identity() {
	local candidate_root="$1"
	local trusted_identity candidate_identity
	trusted_identity="$(synon_release_trusted_identity_path)" || return 1
	candidate_identity="$candidate_root/product-identity.json"
	if [[ ! -f "$candidate_identity" || -L "$candidate_identity" ]]; then
		echo "release product identity is unavailable" >&2
		return 1
	fi
	if ! cmp -s -- "$trusted_identity" "$candidate_identity"; then
		echo "release product identity does not match the trusted authority" >&2
		return 1
	fi
}
