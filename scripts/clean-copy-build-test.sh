#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
go_bin="${GO:-go}"
IFS=$'\t' read -r product_slug product_version < <("$go_bin" run -buildvcs=false ./scripts/product-identity)
tmp=$(mktemp -d)
cleanup() {
  case "$tmp" in
    /tmp/tmp.*) rm -rf -- "$tmp" ;;
    *) echo "refusing to remove unexpected clean-copy path: $tmp" >&2; return 1 ;;
  esac
}
trap cleanup EXIT

copy="$tmp/source"
output="$tmp/output"
cache="$tmp/go-cache"
mkdir -p "$copy" "$output" "$cache"

source_archive="$tmp/source.tar"
git -C "$root" archive --format=tar HEAD >"$source_archive"
source_digest=$(bash "$root/scripts/source-tree-digest.sh" "$root")
archive_digest=$(sha256sum "$source_archive" | cut -d ' ' -f1)
if [[ "$source_digest" != "$archive_digest" ]]; then
  echo "source digest differs from the exact committed archive" >&2
  exit 1
fi
tar --same-permissions -xf "$source_archive" -C "$copy"

filesystem_digest() {
  tar -C "$1" \
    --sort=name \
    --mtime='@0' \
    --owner=0 \
    --group=0 \
    --numeric-owner \
    --exclude='./dist' \
    --exclude='./release' \
    --exclude='./runtime' \
    --exclude='./workspace' \
    --exclude='./uploads' \
    --exclude='./mcp-output' \
    --exclude='./users' \
    --exclude='./models' \
    --exclude='./vendor' \
    --exclude='./node_modules' \
    --exclude='*/node_modules' \
    --exclude='*/out' \
    --exclude='*/test-results' \
    --exclude='*/coverage' \
    --exclude='./.synon-go-audit' \
    -cf - . | sha256sum | cut -d ' ' -f1
}

if [[ -e "$copy/.git" ]]; then
  echo "clean source copy contains Git metadata" >&2
  exit 1
fi

copy_digest=$(filesystem_digest "$copy")

(
  cd "$copy"
  bash scripts/audit/audit-source-clean.sh
  env GOWORK=off GOCACHE="$cache" "$go_bin" build -mod=readonly -trimpath -buildvcs=false -o "$output/synon-go" ./cmd/synon
  env GOWORK=off GOCACHE="$cache" "$go_bin" build -mod=readonly -trimpath -buildvcs=false -o "$output/synon-go-live-im-smoke" ./scripts/smoke-live-im
)

"$output/synon-go" --health-json | grep -Fq "\"version\":\"${product_version}\""
"$output/synon-go-live-im-smoke" --plan --json | grep -Fq '"secretsRedacted":true'

after_digest=$(filesystem_digest "$copy")
if [[ "$copy_digest" != "$after_digest" ]]; then
  echo "clean-copy build modified its source tree" >&2
  exit 1
fi

echo "clean-copy-build-test: ok"
