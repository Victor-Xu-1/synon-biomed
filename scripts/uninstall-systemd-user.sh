#!/usr/bin/env bash
set -euo pipefail

stop_service=true
if [[ "${1:-}" == "--no-stop" ]]; then
	stop_service=false
	shift
fi
if (( $# != 0 )); then
	echo "usage: uninstall-systemd-user.sh [--no-stop]" >&2
	exit 2
fi

config_home="${XDG_CONFIG_HOME:-$HOME/.config}"
unit_dir="${SYNON_SYSTEMD_USER_DIR:-$config_home/systemd/user}"
unit_name="${SYNON_SYSTEMD_UNIT_NAME:-synon-go.service}"
if [[ ! "$unit_name" =~ ^[A-Za-z0-9_.@-]+\.service$ ]]; then
	echo "invalid systemd user unit name: $unit_name" >&2
	exit 1
fi
unit_path="$(realpath -m "$unit_dir/$unit_name")"
systemctl_bin="${SYNON_SYSTEMCTL:-systemctl}"
if [[ ! -f "$unit_path" ]]; then
	echo "synon-go user service is not installed: $unit_path" >&2
	exit 1
fi
if ! grep -Fqx '# Managed biomedical runtime. Use uninstall-systemd-user.sh to remove this unit.' "$unit_path"; then
	echo "refusing to remove an unmanaged unit: $unit_path" >&2
	exit 1
fi

if [[ "$stop_service" == true ]]; then
	command -v "$systemctl_bin" >/dev/null 2>&1 || {
		echo "systemctl is required to stop the user service" >&2
		exit 1
	}
	"$systemctl_bin" --user disable --now "$unit_name"
fi
rm -f -- "$unit_path"
if [[ "$stop_service" == true ]]; then
	"$systemctl_bin" --user daemon-reload
	"$systemctl_bin" --user reset-failed "$unit_name" >/dev/null 2>&1 || true
fi

echo "synon-go user service removed; state and environment files were preserved"
