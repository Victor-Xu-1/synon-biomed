#!/usr/bin/env bash
set -euo pipefail

if [[ "${SYNON_CLEAN_SOURCE_TEST:-}" != "1" ]]; then
	exec scripts/run-clean-source-test.sh scripts/install-release-test.sh "$@"
fi

tmp="$(mktemp -d /tmp/i.XXXXXX)"
IFS=$'\t' read -r product_slug product_version < <("${GO:-go}" run -buildvcs=false ./scripts/product-identity)
runtime_pid=""
passed=false
cleanup() {
	local rc=$?
	local cleanup_failed=0
	trap - EXIT
	if [[ -n "$runtime_pid" ]] && kill -0 "$runtime_pid" 2>/dev/null; then
		kill "$runtime_pid" 2>/dev/null || true
		wait "$runtime_pid" 2>/dev/null || true
	fi
	if [[ "$passed" == true || "${SYNON_KEEP_FAILURE_ARTIFACTS:-0}" != "1" ]]; then
		if ! rm -rf -- "$tmp"; then
			printf 'unable to remove install release test directory: %s\n' "$tmp" >&2 || true
			cleanup_failed=1
		fi
	else
		printf 'install release failure evidence preserved at %s\n' "$tmp" >&2 || true
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
install_dir="$tmp/install"

# An internally consistent legacy package is still the wrong product. Reject
# its identity before executing any binary supplied by the candidate archive.
mkdir -p "$tmp/legacy-package"
tar -xzf "$archive" -C "$tmp/legacy-package"
legacy_root="$(find "$tmp/legacy-package" -mindepth 1 -maxdepth 1 -type d | head -n 1)"
sed -i "s/\"version\": \"${product_version}\"/\"version\": \"4.0.2\"/" "$legacy_root/product-identity.json"
cat >"$legacy_root/synon-go" <<'EOF'
#!/usr/bin/env bash
: >"${SYNON_TEST_MARKER:?}"
exit 97
EOF
chmod +x "$legacy_root/synon-go"
tar -C "$tmp/legacy-package" -czf "$tmp/legacy-package.tar.gz" "$(basename "$legacy_root")"
if SYNON_TEST_MARKER="$tmp/legacy-binary-ran" ./scripts/install-release.sh \
	"$tmp/legacy-package.tar.gz" "$tmp/legacy-install" >/dev/null 2>&1; then
	echo "installer accepted a legacy product identity" >&2
	exit 1
fi
test ! -e "$tmp/legacy-binary-ran"
test ! -e "$tmp/legacy-install"

protected_home="$tmp/protected-home"
mkdir -p "$protected_home"
if HOME="$protected_home" ./scripts/install-release.sh "$archive" . >/dev/null 2>&1; then
	echo "installer accepted the current working directory" >&2
	exit 1
fi
if HOME="$protected_home" ./scripts/install-release.sh "$archive" "$protected_home" >/dev/null 2>&1; then
	echo "installer accepted HOME as the install directory" >&2
	exit 1
fi

outside="$tmp/outside"
mkdir -p "$outside"
ln -s "$outside" "$tmp/outside-link"
if HOME="$protected_home" ./scripts/install-release.sh "$archive" "$tmp/outside-link/install" >/dev/null 2>&1; then
	echo "installer accepted a path through a symbolic-link ancestor" >&2
	exit 1
fi
test ! -e "$outside/install"
ln -s "$protected_home" "$tmp/protected-home-link"
if HOME="$protected_home" ./scripts/install-release.sh "$archive" "$tmp/protected-home-link" >/dev/null 2>&1; then
	echo "installer accepted a symlink resolving to HOME" >&2
	exit 1
fi

./scripts/install-release.sh "$archive" "$install_dir"

"$install_dir/synon-go" --health-json | grep -Fq "\"version\":\"${product_version}\""
"$install_dir/synon-go" release-manifest verify --root "$install_dir" | grep -q '"valid": true'
if "$install_dir/synon-go" tui --help >/dev/null 2>&1; then
  echo "retired TUI command is still available" >&2
  exit 1
fi
default_model_plan="$(env -i PATH="$PATH" HOME="$tmp/home" "$install_dir/synon-go" model-smoke --target runner --plan --require-all --json)"
printf '%s' "$default_model_plan" | grep -q '"secretsRedacted": true'
printf '%s' "$default_model_plan" | grep -q '"provider": "workspace"'
printf '%s' "$default_model_plan" | grep -q '"status": "skipped_missing_config"'
env -i PATH="$PATH" HOME="$tmp/home" SYNON_RUNNER_ENABLED=true SYNON_RUNNER_PROVIDER=go_builtin \
	"$install_dir/synon-go" model-smoke --target runner --run --require-all --json | grep -q '"status": "passed"'
test -x "$install_dir/synon-go-live-im-smoke"
env -i PATH="$PATH" HOME="$tmp/home" "$install_dir/synon-go-live-im-smoke" --plan --json | grep -q '"status":"planned"'
test -f "$install_dir/README.md"
test -f "$install_dir/LICENSE"
test -f "$install_dir/.env.example"
test -f "$install_dir/docs/THIRD_PARTY.md"
test -f "$install_dir/docs/operations-runbook.md"
test -f "$install_dir/docs/frontend-third-party-licenses.json"
test -f "$install_dir/docs/non-web-asset-boundary.json"
test ! -e "$install_dir/docs/compatibility"
test ! -e "$install_dir/docs/full-product"
test ! -e "$install_dir/docs/provenance"
test ! -e "$install_dir/REWRITE_STATUS.json"
test -f "$install_dir/RELEASE_MANIFEST.json"
test -x "$install_dir/scripts/install-systemd-user.sh"
test ! -e "$install_dir/scripts/install-release.sh"
test ! -e "$install_dir/scripts/install-release.ps1"
test -x "$install_dir/scripts/release-path-policy.sh"
test -x "$install_dir/scripts/uninstall-systemd-user.sh"
test -x "$install_dir/scripts/backup-release.sh"
test -x "$install_dir/scripts/rollback-release.sh"
test -x "$install_dir/scripts/uninstall-release.sh"
test -f "$install_dir/scripts/manage-release.ps1"
test -f "$install_dir/scripts/release-identity-policy.ps1"
test -f "$install_dir/web/index.html"
test ! -e "$install_dir/scripts/optional/websearch.py"
test ! -e "$install_dir/tools"
test ! -e "$install_dir/go.mod"
test ! -e "$install_dir/go.sum"

# Prove the installed server and compiled Web assets start with a PATH that
# contains no Go, Bun, Node.js, npm, or Python executable.
runtime_home="$tmp/runtime-home"
runtime_path="$tmp/empty-runtime-path"
runtime_socket_dir="$tmp/r"
mkdir -p "$runtime_home" "$runtime_path" "$runtime_socket_dir"
chmod 700 "$runtime_socket_dir"
systemd_run_path="$(command -v systemd-run || true)"
if [[ -z "$systemd_run_path" ]]; then
	echo "systemd-run is required for the installed Linux runtime acceptance" >&2
	exit 1
fi
ln -s "$systemd_run_path" "$runtime_path/systemd-run"
runtime_ready=false
for port_offset in $(seq 0 31); do
	runtime_port=$((42000 + ($$ + port_offset) % 10000))
	: >"$tmp/runtime.log"
	env -i PATH="$runtime_path" HOME="$tmp/home" XDG_RUNTIME_DIR="$runtime_socket_dir" SYNON_HOME="$runtime_home" \
		SYNON_ADDRESS="127.0.0.1:$runtime_port" SYNON_RUNNER_ENABLED=false \
		SYNON_ENABLED_ADAPTERS= "$install_dir/synon-go" serve >"$tmp/runtime.log" 2>&1 &
	runtime_pid=$!
	for _ in $(seq 1 100); do
		if /usr/bin/curl --noproxy '*' --fail --silent "http://127.0.0.1:$runtime_port/api/health" >/dev/null 2>&1 && \
			/usr/bin/curl --noproxy '*' --fail --silent "http://127.0.0.1:$runtime_port/" | grep -q '<!doctype html>'; then
			runtime_ready=true
			break
		fi
		if ! kill -0 "$runtime_pid" 2>/dev/null; then
			break
		fi
		sleep 0.1
	done
	if [[ "$runtime_ready" == true ]]; then
		break
	fi
	if kill -0 "$runtime_pid" 2>/dev/null; then
		cat "$tmp/runtime.log" >&2
		echo "installed runtime stayed alive without becoming ready" >&2
		exit 1
	fi
	wait "$runtime_pid" 2>/dev/null || true
	runtime_pid=""
	if ! grep -Fq 'address already in use' "$tmp/runtime.log"; then
		cat "$tmp/runtime.log" >&2
		echo "installed runtime failed before becoming ready" >&2
		exit 1
	fi
done
if [[ "$runtime_ready" != true ]]; then
	cat "$tmp/runtime.log" >&2
	echo "installed runtime could not acquire a test port" >&2
	exit 1
fi
kill "$runtime_pid"
wait "$runtime_pid" 2>/dev/null || true
runtime_pid=""

tampered="$tmp/tampered"
mkdir -p "$tampered"
tar -xzf "$archive" -C "$tampered"
tampered_dir="$(find "$tampered" -mindepth 1 -maxdepth 1 -type d | head -n 1)"
printf 'tampered\n' >"$tampered_dir/README.md"
tar -C "$tampered" -czf "$tmp/tampered.tar.gz" "$(basename "$tampered_dir")"
if ./scripts/install-release.sh "$tmp/tampered.tar.gz" "$tmp/rejected" >/dev/null 2>&1; then
	echo "installer accepted a tampered release" >&2
	exit 1
fi
test ! -e "$tmp/rejected"

printf 'escape\n' >"$tmp/payload"
tar -C "$tmp" -czPf "$tmp/unsafe.tar.gz" --transform='s|payload|../escape|' payload
if ./scripts/install-release.sh "$tmp/unsafe.tar.gz" "$tmp/unsafe-install" >/dev/null 2>&1; then
	echo "installer accepted a path-traversal archive" >&2
	exit 1
fi
test ! -e "$tmp/unsafe-install"

mkdir -p "$tmp/link-source/package"
ln -s ../../escape "$tmp/link-source/package/unsafe-link"
tar -C "$tmp/link-source" -czf "$tmp/unsafe-link.tar.gz" package
if ./scripts/install-release.sh "$tmp/unsafe-link.tar.gz" "$tmp/unsafe-link-install" >/dev/null 2>&1; then
	echo "installer accepted a symbolic-link archive entry" >&2
	exit 1
fi
test ! -e "$tmp/unsafe-link-install"

passed=true
trap - EXIT
if ! rm -rf -- "$tmp"; then
	printf 'unable to remove install release test directory: %s\n' "$tmp" >&2
	exit 1
fi
echo "INSTALL_RELEASE_TEST=yes"
