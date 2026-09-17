#!/usr/bin/env bash
set -euo pipefail

root_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

mkdir -p "$tmp/source/internal"
git -C "$tmp/source" init -q -b main
git -C "$tmp/source" config user.name source-digest-test
git -C "$tmp/source" config user.email source-digest@example.invalid
git -C "$tmp/source" config core.fileMode false
printf 'package fixture\n' >"$tmp/source/internal/main.go"
printf '/dist/\n' >"$tmp/source/.gitignore"
git -C "$tmp/source" add .
git -C "$tmp/source" commit -qm 'initial source'

first=$(bash "$root_dir/scripts/source-tree-digest.sh" "$tmp/source")
touch -d '@123456789' "$tmp/source/internal/main.go"
chmod 777 "$tmp/source/internal/main.go"
second=$(bash "$root_dir/scripts/source-tree-digest.sh" "$tmp/source")
if [[ "$first" != "$second" ]]; then
  echo "source digest changed because of worktree-only time or permission projection" >&2
  exit 1
fi

mkdir -p "$tmp/source/dist"
printf 'ignored build output\n' >"$tmp/source/dist/binary"
third=$(bash "$root_dir/scripts/source-tree-digest.sh" "$tmp/source")
if [[ "$second" != "$third" ]]; then
  echo "source digest included ignored build output" >&2
  exit 1
fi

git -C "$tmp/source" update-index --chmod=+x internal/main.go
git -C "$tmp/source" commit -qm 'make source executable'
fourth=$(bash "$root_dir/scripts/source-tree-digest.sh" "$tmp/source")
if [[ "$third" == "$fourth" ]]; then
  echo "source digest ignored committed executable-mode change" >&2
  exit 1
fi

printf 'source change\n' >>"$tmp/source/internal/main.go"
if bash "$root_dir/scripts/source-tree-digest.sh" "$tmp/source" >/dev/null 2>&1; then
  echo "source digest accepted a dirty tracked file" >&2
  exit 1
fi
git -C "$tmp/source" add internal/main.go
git -C "$tmp/source" commit -qm 'change source content'
fifth=$(bash "$root_dir/scripts/source-tree-digest.sh" "$tmp/source")
if [[ "$fourth" == "$fifth" ]]; then
  echo "source digest ignored committed source content change" >&2
  exit 1
fi

git clone -q --no-local "$tmp/source" "$tmp/clone"
clone_digest=$(bash "$root_dir/scripts/source-tree-digest.sh" "$tmp/clone")
if [[ "$fifth" != "$clone_digest" ]]; then
  echo "source digest changed across clean clones" >&2
  exit 1
fi

printf 'untracked source\n' >"$tmp/source/untracked.txt"
if bash "$root_dir/scripts/source-tree-digest.sh" "$tmp/source" >/dev/null 2>&1; then
  echo "source digest accepted an untracked source file" >&2
  exit 1
fi

echo "source-tree-digest-test: ok"
