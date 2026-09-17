#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}
ROOT_DIR=$(cd "$ROOT_DIR" && pwd -P)
BOUNDARY="$ROOT_DIR/docs/non-web-asset-boundary.json"

if [[ ! -f "$BOUNDARY" || -L "$BOUNDARY" ]]; then
  echo "ERROR: non-Web asset boundary manifest is missing or is a symbolic link" >&2
  exit 1
fi

python3 - "$BOUNDARY" <<'PY'
import json
import sys

path = sys.argv[1]
with open(path, "r", encoding="utf-8") as handle:
    document = json.load(handle)
expected = [
    "skills/synonbiomed/skill-creator/assets/eval_review.html",
    "skills/synonbiomed/skill-creator/eval-viewer/viewer.html",
    "assets/synon-link/synon-link-extension-v0.6.10.zip",
    "assets/optional/mcp-servers/ketcher-chemistry/widget/index.html.gz",
]
actual = [item.get("path") for item in document.get("allowedVisualAssets", [])]
expected_tooling = [
    "scripts/audit/reference_harness_inventory.mjs",
    "scripts/dev/frontend-dependency-watch.mjs",
    "scripts/generate-conda-runtime-lock.mjs",
    "scripts/generate-conda-runtime-lock.test.mjs",
]
actual_tooling = [item.get("path") for item in document.get("allowedToolingFiles", [])]
if document.get("schemaVersion") != 1 or actual != expected or actual_tooling != expected_tooling:
    raise SystemExit("ERROR: non-Web asset boundary manifest does not match the audited allowlist")
PY

for required_frontend_file in \
  frontend/package.json \
  frontend/SOURCE_IMPORT_MANIFEST.json \
  frontend/MIGRATION_MANIFEST.json; do
  if [[ ! -f "$ROOT_DIR/$required_frontend_file" || -L "$ROOT_DIR/$required_frontend_file" ]]; then
    echo "ERROR: the sole product frontend is missing $required_frontend_file" >&2
    exit 1
  fi
done

# The product Web tree is audited separately from all standalone visual assets.
actual_non_product_web_files=$(find "$ROOT_DIR" \
  -path "$ROOT_DIR/.git" -prune -o \
  -path "$ROOT_DIR/frontend" -prune -o \
  -type d \( \
    -name node_modules -o -name out -o -name coverage -o \
    -name test-results -o -name playwright-report -o -name dist \
  \) -prune -o \
  -type f \( \
    -iname '*.html' -o -iname '*.htm' -o -iname '*.css' \
    -o -iname '*.scss' -o -iname '*.sass' -o -iname '*.less' \
    -o -iname '*.js' -o -iname '*.mjs' -o -iname '*.cjs' \
    -o -iname '*.jsx' -o -iname '*.ts' -o -iname '*.tsx' \
    -o -iname '*.vue' -o -iname '*.svelte' -o -iname '*.astro' \
  \) -printf '%P\n' | sort)
expected_web_files=$(printf '%s\n' \
  'scripts/audit/reference_harness_inventory.mjs' \
  'scripts/dev/frontend-dependency-watch.mjs' \
  'scripts/generate-conda-runtime-lock.mjs' \
  'scripts/generate-conda-runtime-lock.test.mjs' \
  'skills/synonbiomed/skill-creator/assets/eval_review.html' \
  'skills/synonbiomed/skill-creator/eval-viewer/viewer.html' | sort)
if [[ "$actual_non_product_web_files" != "$expected_web_files" ]]; then
  echo "ERROR: source contains unclassified Web-like files outside frontend/" >&2
  diff -u <(printf '%s\n' "$expected_web_files") <(printf '%s\n' "$actual_non_product_web_files") >&2 || true
  exit 1
fi

banned_directories=$(find "$ROOT_DIR" -mindepth 1 -maxdepth 1 \
  -type d \( -name desktop -o -name web -o -name webapp -o -name web-ui \) \
  -printf '%P\n' | sort)
if [[ -n "$banned_directories" ]]; then
  echo "ERROR: a second root-level product Web shell is present" >&2
  printf '%s\n' "$banned_directories" >&2
  exit 1
fi

echo "audit-non-web-boundary: ok"
