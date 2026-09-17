#!/usr/bin/env bash
set -euo pipefail

root="${1:-.}"
root=$(cd "$root" && pwd -P)

if ! git -C "$root" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  echo "source tree digest requires a Git worktree" >&2
  exit 1
fi
top=$(git -C "$root" rev-parse --show-toplevel)
top=$(cd "$top" && pwd -P)
if [[ "$top" != "$root" ]]; then
  echo "source tree digest must run at the repository root" >&2
  exit 1
fi
if [[ -n "$(git -C "$root" status --porcelain=v1 --untracked-files=normal)" ]]; then
  echo "source tree digest requires a clean committed revision" >&2
  exit 1
fi
if git -C "$root" ls-files --stage | awk '$1 == "160000" { found = 1 } END { exit !found }'; then
  echo "source tree digest does not accept unresolved submodule content" >&2
  exit 1
fi

commit=$(git -C "$root" rev-parse --verify 'HEAD^{commit}')
git -C "$root" archive --format=tar "$commit" | sha256sum | cut -d ' ' -f1
