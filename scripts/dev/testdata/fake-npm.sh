#!/usr/bin/env bash
set -euo pipefail

if [[ -z "${FAKE_NPM_STATE:-}" ]]; then
  echo "FAKE_NPM_STATE is required" >&2
  exit 2
fi
mkdir -p "$FAKE_NPM_STATE"

case "${1:-}" in
  --version)
    echo "10.0.0-test"
    ;;
  ci)
    if [[ -e "$FAKE_NPM_STATE/fail-ci" ]]; then
      echo "configured fake npm ci failure" >&2
      exit 42
    fi
    mkdir -p node_modules/.bin
    printf '#!/usr/bin/env bash\nexit 0\n' >node_modules/.bin/vite
    chmod 755 node_modules/.bin/vite
    printf 'ci\n' >>"$FAKE_NPM_STATE/events"
    printf '%s\n' "$PWD" >>"$FAKE_NPM_STATE/ci-cwds"
    ;;
  run)
    if [[ "${2:-}" != "dev" ]]; then
      echo "unexpected fake npm run target: ${2:-}" >&2
      exit 2
    fi
    printf 'run\n' >>"$FAKE_NPM_STATE/events"
    printf '%s\n' "$PWD" >>"$FAKE_NPM_STATE/run-cwds"
    sleep 600 &
    worker_pid=$!
    printf '%s\n' "$worker_pid" >>"$FAKE_NPM_STATE/workers"
    trap 'exit 0' INT TERM
    wait "$worker_pid"
    ;;
  *)
    echo "unexpected fake npm command: $*" >&2
    exit 2
    ;;
esac
