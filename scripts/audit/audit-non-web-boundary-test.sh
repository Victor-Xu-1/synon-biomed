#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
TMP_DIR=$(mktemp -d)
cleanup() {
  case "$TMP_DIR" in
    /tmp/tmp.*) rm -rf -- "$TMP_DIR" ;;
    *) echo "refusing to remove unexpected boundary-test path: $TMP_DIR" >&2 ;;
  esac
}
trap cleanup EXIT

FIXTURE="$TMP_DIR/project"
mkdir -p "$FIXTURE/docs" "$FIXTURE/frontend/node_modules/example" \
  "$FIXTURE/frontend/out" \
  "$FIXTURE/scripts/audit" \
  "$FIXTURE/scripts/dev" \
  "$FIXTURE/skills/synonbiomed/skill-creator/assets" \
  "$FIXTURE/skills/synonbiomed/skill-creator/eval-viewer"
cp "$ROOT_DIR/docs/non-web-asset-boundary.json" "$FIXTURE/docs/non-web-asset-boundary.json"
printf '{}\n' >"$FIXTURE/frontend/package.json"
printf '{}\n' >"$FIXTURE/frontend/SOURCE_IMPORT_MANIFEST.json"
printf '{}\n' >"$FIXTURE/frontend/MIGRATION_MANIFEST.json"
printf 'generated dependency\n' >"$FIXTURE/frontend/node_modules/example/index.js"
printf 'generated output\n' >"$FIXTURE/frontend/out/index.js"
printf 'export {};\n' >"$FIXTURE/scripts/audit/reference_harness_inventory.mjs"
printf 'export {};\n' >"$FIXTURE/scripts/dev/frontend-dependency-watch.mjs"
printf 'export {};\n' >"$FIXTURE/scripts/generate-conda-runtime-lock.mjs"
printf 'export {};\n' >"$FIXTURE/scripts/generate-conda-runtime-lock.test.mjs"
printf '<html></html>\n' \
  >"$FIXTURE/skills/synonbiomed/skill-creator/assets/eval_review.html"
printf '<html></html>\n' \
  >"$FIXTURE/skills/synonbiomed/skill-creator/eval-viewer/viewer.html"

bash "$ROOT_DIR/scripts/audit/audit-non-web-boundary.sh" "$FIXTURE" >/dev/null

ln -s "$FIXTURE" "$TMP_DIR/project-link"
bash "$ROOT_DIR/scripts/audit/audit-non-web-boundary.sh" "$TMP_DIR/project-link" >/dev/null

printf 'export const legacy = true;\n' >"$FIXTURE/legacy.ts"
if bash "$ROOT_DIR/scripts/audit/audit-non-web-boundary.sh" "$FIXTURE" >/dev/null 2>&1; then
  echo "ERROR: boundary audit allowed Web-like source outside frontend/" >&2
  exit 1
fi
if bash "$ROOT_DIR/scripts/audit/audit-non-web-boundary.sh" "$TMP_DIR/project-link" >/dev/null 2>&1; then
  echo "ERROR: boundary audit allowed Web-like source through a symlinked checkout" >&2
  exit 1
fi
rm "$FIXTURE/legacy.ts"

mkdir -p "$FIXTURE/webapp"
printf '<html></html>\n' >"$FIXTURE/webapp/index.html"
if bash "$ROOT_DIR/scripts/audit/audit-non-web-boundary.sh" "$FIXTURE" >/dev/null 2>&1; then
  echo "ERROR: boundary audit allowed a second root-level Web shell" >&2
  exit 1
fi
rm -rf "$FIXTURE/webapp"

rm "$FIXTURE/frontend/MIGRATION_MANIFEST.json"
if bash "$ROOT_DIR/scripts/audit/audit-non-web-boundary.sh" "$FIXTURE" >/dev/null 2>&1; then
  echo "ERROR: boundary audit allowed incomplete frontend provenance" >&2
  exit 1
fi

echo "audit-non-web-boundary-test: ok"
