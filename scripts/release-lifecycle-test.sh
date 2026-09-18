#!/usr/bin/env bash
set -euo pipefail

tmp="$(mktemp -d)"
IFS=$'\t' read -r product_slug product_version < <("${GO:-go}" run -buildvcs=false ./scripts/product-identity)
passed=false
cleanup() {
	local rc=$?
	local cleanup_failed=0
	trap - EXIT
	if [[ "$passed" == true || "${SYNON_KEEP_FAILURE_ARTIFACTS:-0}" != "1" ]]; then
		if ! rm -rf -- "$tmp"; then
			printf 'unable to remove release lifecycle test directory: %s\n' "$tmp" >&2 || true
			cleanup_failed=1
		fi
	else
		printf 'release lifecycle failure evidence preserved at %s\n' "$tmp" >&2 || true
	fi
	if (( rc == 0 && cleanup_failed != 0 )); then rc=1; fi
	exit "$rc"
}
trap cleanup EXIT

archive="${SYNON_RELEASE_TEST_ARCHIVE:-}"
if [[ -z "$archive" ]]; then
	archive="$(./scripts/package-release.sh "$tmp/out" | tail -n 1)"
fi
archive="$(realpath -e "$archive")"
install="$tmp/install"
backup="$tmp/backup"
external_data="$tmp/user-data"
mkdir -p "$external_data"
printf 'preserve-me\n' >"$external_data/marker"

./scripts/install-release.sh "$archive" "$install" >/dev/null
original_sha="$(sha256sum "$install/synon-go" | cut -d' ' -f1)"
./scripts/backup-release.sh "$install" "$backup" >/dev/null

# Rollback authority is the trusted manager tree, never the backup candidate.
# A legacy identity must fail before its binary runs or the installation moves.
cp -a "$backup" "$tmp/legacy-backup"
sed -i "s/\"version\": \"${product_version}\"/\"version\": \"4.0.2\"/" "$tmp/legacy-backup/product-identity.json"
cat >"$tmp/legacy-backup/synon-go" <<'EOF'
#!/usr/bin/env bash
: >"${SYNON_TEST_MARKER:?}"
exit 97
EOF
chmod +x "$tmp/legacy-backup/synon-go"
if SYNON_TEST_MARKER="$tmp/legacy-rollback-binary-ran" \
	./scripts/rollback-release.sh "$install" "$tmp/legacy-backup" >/dev/null 2>&1; then
	echo "rollback accepted a legacy product identity" >&2
	exit 1
fi
test ! -e "$tmp/legacy-rollback-binary-ran"
test "$original_sha" = "$(sha256sum "$install/synon-go" | cut -d' ' -f1)"

# A failed stage activation must restore the only previous installation rather
# than deleting it from the EXIT trap.
fake_bin="$tmp/fake-bin"
mkdir -p "$fake_bin"
cat >"$fake_bin/mv" <<'EOF'
#!/usr/bin/env bash
if [[ "${1:-}" == *".rollback."* ]]; then exit 97; fi
if [[ "${SYNON_TEST_FAIL_INSTALL_ACTIVATION:-0}" == "1" && "${1:-}" == *".stage."* ]]; then exit 98; fi
if [[ "${SYNON_TEST_FAIL_INSTALL_RESTORE:-0}" == "1" && "${1:-}" == *".backup."* ]]; then exit 99; fi
if [[ "${SYNON_TEST_FAIL_ROLLBACK_RESTORE:-0}" == "1" && "${1:-}" == *".pre-rollback."* ]]; then exit 100; fi
exec /usr/bin/mv "$@"
EOF
chmod +x "$fake_bin/mv"
if PATH="$fake_bin:$PATH" ./scripts/rollback-release.sh "$install" "$backup" >/dev/null 2>&1; then
	echo "rollback unexpectedly accepted a failed stage activation" >&2
	exit 1
fi
test -x "$install/synon-go"
test "$original_sha" = "$(sha256sum "$install/synon-go" | cut -d' ' -f1)"
test -z "$(find "$tmp" -maxdepth 1 -name '.install.pre-rollback.*' -print -quit)"

# If both install activation and automatic restore fail, the previous release
# and its exact recovery path must remain available to an operator.
install_failure_log="$tmp/install-restore-failure.log"
if SYNON_TEST_FAIL_INSTALL_ACTIVATION=1 SYNON_TEST_FAIL_INSTALL_RESTORE=1 PATH="$fake_bin:$PATH" \
	./scripts/install-release.sh "$archive" "$install" >/dev/null 2>"$install_failure_log"; then
	echo "install unexpectedly accepted failed activation and recovery" >&2
	exit 1
fi
test ! -e "$install"
mapfile -t preserved_backups < <(find "$tmp" -mindepth 1 -maxdepth 1 -type d -name '.install.backup.*' -print)
test "${#preserved_backups[@]}" -eq 1
test -x "${preserved_backups[0]}/synon-go"
test "$original_sha" = "$(sha256sum "${preserved_backups[0]}/synon-go" | cut -d' ' -f1)"
grep -Fq "preserved at ${preserved_backups[0]}" "$install_failure_log"
/usr/bin/mv -- "${preserved_backups[0]}" "$install"

# The rollback path has the same fail-closed recovery guarantee.
rollback_failure_log="$tmp/rollback-restore-failure.log"
if SYNON_TEST_FAIL_ROLLBACK_RESTORE=1 PATH="$fake_bin:$PATH" \
	./scripts/rollback-release.sh "$install" "$backup" >/dev/null 2>"$rollback_failure_log"; then
	echo "rollback unexpectedly accepted failed activation and recovery" >&2
	exit 1
fi
test ! -e "$install"
mapfile -t preserved_previous < <(find "$tmp" -mindepth 1 -maxdepth 1 -type d -name '.install.pre-rollback.*' -print)
test "${#preserved_previous[@]}" -eq 1
test -x "${preserved_previous[0]}/synon-go"
test "$original_sha" = "$(sha256sum "${preserved_previous[0]}/synon-go" | cut -d' ' -f1)"
grep -Fq "preserved at ${preserved_previous[0]}" "$rollback_failure_log"
/usr/bin/mv -- "${preserved_previous[0]}" "$install"

# Generate and verify a real hardened user unit from the installed release.
runtime_state="$tmp/runtime state"
config_home="$tmp/config home"
SYNON_SYSTEMD_USER_DIR="$tmp/systemd-user" XDG_CONFIG_HOME="$config_home" \
	"$install/scripts/install-systemd-user.sh" --no-start "$install" "$runtime_state" >/dev/null
unit="$tmp/systemd-user/synon-go.service"
test -f "$unit"
test "$(stat -c '%a' "$unit")" = "600"
env_file="$config_home/synon-go/synon-go.env"
test "$(stat -c '%a' "$env_file")" = "600"
escaped_runtime_state="${runtime_state// /\\x20}"
escaped_env_file="${env_file// /\\x20}"
grep -Fq "ExecStart=$install/synon-go serve" "$unit"
grep -Fq "WorkingDirectory=$install" "$unit"
grep -Fq "EnvironmentFile=-$escaped_env_file" "$unit"
grep -Fq "ReadWritePaths=$escaped_runtime_state" "$unit"
systemd-analyze verify "$unit" >/dev/null
SYNON_SYSTEMD_USER_DIR="$tmp/systemd-user" XDG_CONFIG_HOME="$config_home" \
	"$install/scripts/uninstall-systemd-user.sh" --no-stop >/dev/null
test ! -e "$unit"
test -f "$env_file"
test -d "$runtime_state"

# A rejected upgrade must leave the current installation byte-identical.
mkdir -p "$tmp/tampered"
tar -xzf "$archive" -C "$tmp/tampered"
tampered_root="$(find "$tmp/tampered" -mindepth 1 -maxdepth 1 -type d)"
printf 'tampered\n' >>"$tampered_root/README.md"
tar -C "$tmp/tampered" -czf "$tmp/tampered.tar.gz" "$(basename "$tampered_root")"
if ./scripts/install-release.sh "$tmp/tampered.tar.gz" "$install" >/dev/null 2>&1; then
	echo "tampered upgrade unexpectedly succeeded" >&2
	exit 1
fi
test "$original_sha" = "$(sha256sum "$install/synon-go" | cut -d' ' -f1)"

# Rollback replaces a corrupted installation from a verified backup.
printf 'corrupt\n' >>"$install/README.md"
./scripts/rollback-release.sh "$install" "$backup" >/dev/null
"$install/synon-go" release-manifest verify --root "$install" >/dev/null
test "$original_sha" = "$(sha256sum "$install/synon-go" | cut -d' ' -f1)"

# A normal upgrade can replace an existing release atomically.
./scripts/install-release.sh "$archive" "$install" >/dev/null
"$install/synon-go" --health-json | grep -Fq "\"version\":\"${product_version}\""

# Corrupt releases require an explicit override and never remove external data.
printf 'corrupt\n' >>"$install/README.md"
if ./scripts/uninstall-release.sh "$install" >/dev/null 2>&1; then
	echo "normal uninstall accepted a corrupt release" >&2
	exit 1
fi
test -d "$install"
./scripts/uninstall-release.sh --allow-corrupt "$install" >/dev/null
test ! -e "$install"
test "$(cat "$external_data/marker")" = "preserve-me"

passed=true
trap - EXIT
if ! rm -rf -- "$tmp"; then
	printf 'unable to remove release lifecycle test directory: %s\n' "$tmp" >&2
	exit 1
fi
echo "RELEASE_LIFECYCLE_TEST=yes"
