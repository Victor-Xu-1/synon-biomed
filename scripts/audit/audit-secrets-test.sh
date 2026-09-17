#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

mkdir -p "$tmp/source/internal"
printf 'api_key = "example-placeholder-secret"\n' >"$tmp/source/internal/example.txt"
printf '%s\n' "apiKey: 'SYNON_AI_IMG_API_KEY'" >>"$tmp/source/internal/example.txt"
printf '%s\n' "apiKey: 'local-integration-key' // fixture" >>"$tmp/source/internal/example.txt"
python3 "$root/scripts/audit/audit-secrets.py" "$tmp/source" >/dev/null

printf '%s%s\n' "apiKey: 'real-looking-" "value-that-must-fail'" >"$tmp/source/internal/assigned.txt"
if python3 "$root/scripts/audit/audit-secrets.py" "$tmp/source" >"$tmp/out" 2>"$tmp/err"; then
  echo "audit-secrets allowed an assigned secret" >&2
  exit 1
fi
grep -Fq 'internal/assigned.txt:1: assigned-secret' "$tmp/err"
rm "$tmp/source/internal/assigned.txt"

printf '%s%s\n' 'token = "ghp_' 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJ"' >"$tmp/source/internal/leak.txt"
if python3 "$root/scripts/audit/audit-secrets.py" "$tmp/source" >"$tmp/out" 2>"$tmp/err"; then
  echo "audit-secrets allowed a GitHub token" >&2
  exit 1
fi
grep -Fq 'internal/leak.txt:1: github-token' "$tmp/err"
rm "$tmp/source/internal/leak.txt"

printf '%s%s\n' '-----BEGIN OPENSSH ' 'PRIVATE KEY-----' >"$tmp/source/internal/private.pem"
if python3 "$root/scripts/audit/audit-secrets.py" "$tmp/source" >"$tmp/out" 2>"$tmp/err"; then
  echo "audit-secrets allowed a private key" >&2
  exit 1
fi
grep -Fq 'internal/private.pem:1: private-key' "$tmp/err"

echo "audit-secrets-test: ok"
