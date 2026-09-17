#!/usr/bin/env bash
set -Eeuo pipefail

readonly synon_user="${SYNON_DEV_WSL_USER:-victor_1}"
readonly synon_uid="$(id -u "$synon_user")"
readonly runtime_dir="/run/user/$synon_uid"
readonly units=(
  synon-biomed-v010-backend.service
  synon-biomed-v010-frontend.service
)

loginctl enable-linger "$synon_user" >/dev/null
systemctl start "user@${synon_uid}.service"

ready=false
for _ in $(seq 1 30); do
  if [[ -S "$runtime_dir/bus" ]] &&
    runuser -u "$synon_user" -- env XDG_RUNTIME_DIR="$runtime_dir" \
      systemctl --user start "${units[@]}"; then
    ready=true
    break
  fi
  sleep 1
done

if [[ "$ready" != true ]] ||
  ! runuser -u "$synon_user" -- env XDG_RUNTIME_DIR="$runtime_dir" \
    systemctl --user is-active --quiet "${units[@]}"; then
  echo "Synon Biomed source services did not become active" >&2
  exit 1
fi

exec /bin/sleep infinity
