#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
source "$script_dir/release-path-policy.sh"

start_service=true
if [[ "${1:-}" == "--no-start" ]]; then
	start_service=false
	shift
fi
if (( $# < 1 || $# > 3 )); then
	echo "usage: install-systemd-user.sh [--no-start] <install-dir> [state-dir] [env-file]" >&2
	exit 2
fi

install_dir="$(realpath -e "$1")"
state_dir="${2:-${XDG_STATE_HOME:-$HOME/.local/state}/synon-go}"
config_home="${XDG_CONFIG_HOME:-$HOME/.config}"
env_file="${3:-$config_home/synon-go/synon-go.env}"
unit_dir="${SYNON_SYSTEMD_USER_DIR:-$config_home/systemd/user}"
unit_name="${SYNON_SYSTEMD_UNIT_NAME:-synon-go.service}"
if [[ ! "$unit_name" =~ ^[A-Za-z0-9_.@-]+\.service$ ]]; then
	echo "invalid systemd user unit name: $unit_name" >&2
	exit 1
fi
unit_path="$unit_dir/$unit_name"
systemctl_bin="${SYNON_SYSTEMCTL:-systemctl}"
systemd_analyze_bin="${SYNON_SYSTEMD_ANALYZE:-systemd-analyze}"

if [[ ! -x "$install_dir/synon-go" || ! -f "$install_dir/RELEASE_MANIFEST.json" ]]; then
	echo "install directory is not a verified product release: $install_dir" >&2
	exit 1
fi
synon_release_assert_product_identity "$install_dir"
"$install_dir/synon-go" release-manifest verify --root "$install_dir" >/dev/null

state_dir="$(realpath -m "$state_dir")"
env_file="$(realpath -m "$env_file")"
unit_dir="$(realpath -m "$unit_dir")"
for path in "$state_dir" "$env_file" "$unit_dir"; do
	if [[ "$path" == "/" || "$path" == "$HOME" ]]; then
		echo "refusing unsafe managed service path: $path" >&2
		exit 1
	fi
done

systemd_env_quote() {
	local value="$1"
	value="${value//\\/\\\\}"
	value="${value//\"/\\\"}"
	printf '"%s"' "$value"
}

systemd_escape_path() {
	local value="$1" output="" character hex index
	local LC_ALL=C
	if [[ "$value" != /* || "$value" == *$'\n'* || "$value" == *$'\r'* ]]; then
		echo "systemd path must be an absolute single-line path" >&2
		return 1
	fi
	for ((index = 0; index < ${#value}; index++)); do
		character="${value:index:1}"
		case "$character" in
		[A-Za-z0-9_./:-]) output+="$character" ;;
		%) output+='%%' ;;
		*)
			printf -v hex '%02x' "'$character"
			output+="\\x$hex"
			;;
		esac
	done
	printf '%s' "$output"
}

mkdir -p "$state_dir" "$(dirname "$env_file")" "$unit_dir"
chmod 700 "$state_dir" "$(dirname "$env_file")"
if [[ ! -e "$env_file" ]]; then
	cat >"$env_file" <<EOF
# Product user service environment. Keep this file mode 0600.
SYNON_ADDRESS=127.0.0.1:8765
SYNON_HOME=$(systemd_env_quote "$state_dir")
EOF
fi
chmod 600 "$env_file"

binary_escaped="$(systemd_escape_path "$install_dir/synon-go")"
workdir_escaped="$(systemd_escape_path "$install_dir")"
env_escaped="$(systemd_escape_path "$env_file")"
state_escaped="$(systemd_escape_path "$state_dir")"
cat >"$unit_path" <<EOF
# Managed biomedical runtime. Use uninstall-systemd-user.sh to remove this unit.
[Unit]
Description=Managed biomedical agent runtime
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
ExecStart=$binary_escaped serve
WorkingDirectory=$workdir_escaped
EnvironmentFile=-$env_escaped
Restart=on-failure
RestartSec=3s
TimeoutStopSec=20s
KillMode=mixed
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths=$state_escaped
UMask=0077
LimitNOFILE=65536

[Install]
WantedBy=default.target
EOF
chmod 600 "$unit_path"

if command -v "$systemd_analyze_bin" >/dev/null 2>&1; then
	"$systemd_analyze_bin" verify "$unit_path" >/dev/null
fi
if [[ "$start_service" == true ]]; then
	command -v "$systemctl_bin" >/dev/null 2>&1 || {
		echo "systemctl is required to start the user service" >&2
		exit 1
	}
	"$systemctl_bin" --user daemon-reload
	"$systemctl_bin" --user enable --now "$unit_name"
	"$systemctl_bin" --user --no-pager --full status "$unit_name"
fi

echo "synon-go user service installed at $unit_path using state $state_dir"
