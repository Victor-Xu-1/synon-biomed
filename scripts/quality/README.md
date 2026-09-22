# Repository quality tools

These scripts enforce the current Synon Biomed repository boundaries.
They are verification gates: passing one gate proves only the contract named by
that gate, not complete product acceptance.

## Run the maintained test suite

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover \
  -s scripts/quality -p 'test_*.py'
```

## Content authorities

Verify the content-addressed bundled Agent manifest:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 scripts/quality/agent_manifest.py \
  --repo . --check
```

Use `--write` only after reviewing Agent metadata or capability changes.

Verify the kernel/compute asset manifest and real interpreter protocol:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 scripts/quality/test_kernel_manifest.py
PYTHONDONTWRITEBYTECODE=1 python3 scripts/quality/kernel_manifest.py
PYTHONDONTWRITEBYTECODE=1 python3 scripts/quality/test_kernel_worker_protocol.py
```

The manifest generator's `--write` mode updates checksums, not redistribution
permissions. The protocol tests use POSIX pipes and signals; an installed
Matplotlib runtime enables the additional real plotting check.

Verify dependency-manager ownership and locked manifests:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 scripts/quality/dependency_authority.py \
  --repo . --spec docs/governance/dependency-authority.json
```

Clean-clone verification prefetches the locked Go modules with at most three
attempts. Transient transport failures are retried with bounded backoff; final
failure remains fatal, and signed module-download URLs are not echoed:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 scripts/quality/go_module_download.py \
  --attempts 3 --timeout-seconds 300
```

The clean-clone gate also installs the pinned MCP Python test requirements into
the user-site of its private external HOME. MCP subprocesses inherit that HOME
through the existing safe environment allowlist, while PATH, the repository,
and the real user Python installation remain unchanged.

Verify the canonical module layout, banned duplicate roots, and growth ceilings:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 scripts/quality/module_topology_gate.py \
  --repo .
```

Verify product identity and every checked-in projection:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 scripts/quality/product_identity_gate.py \
  --repo . --mode candidate
```

`--mode release` deliberately remains blocked after validating the static
release policy, because tracked source cannot grant current release or tag
authorization. The external release controller must validate a signed receipt
against the exact candidate manifest and artifacts.

## Checkout and runtime portability

Reject tracked paths that collide or cannot be checked out on supported
case-insensitive Windows filesystems:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 scripts/quality/portable_paths.py --repo .
```

Run an argv-only command set against an exact clean local clone:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 scripts/quality/clean_clone_gate.py \
  --source . \
  --sha "$(git rev-parse HEAD)" \
  --commands docs/governance/templates/clean-clone-command-set.json \
  --temp-root /absolute/private-temp-root \
  --output /absolute/new-clean-clone-result.json
```

The clean-clone gate never consumes uncommitted files. Use `--validate-only`
to validate a command-set document without cloning or executing it.

After building a candidate binary, probe the real `/health` startup contract in
an isolated temporary runtime:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 scripts/quality/startup_smoke_gate.py \
  --binary ./path/to/synon --timeout-seconds 30
```

The binary path must be relative to the current directory. The gate selects a
free loopback port, validates the JSON health response, stops the process, and
reports only bounded hashes and byte counts for captured output.

## Read-only repository evidence

Capture a redacted repository snapshot and derive a hygiene report outside the
source tree:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 scripts/quality/repository_state.py snapshot \
  --repo /absolute/repository \
  --label hygiene-audit \
  --output /absolute/private-evidence/snapshot.json

PYTHONDONTWRITEBYTECODE=1 python3 scripts/quality/repository_state.py hygiene-report \
  --snapshot /absolute/private-evidence/snapshot.json \
  --output /absolute/private-evidence/hygiene-report.json
```

Snapshots classify paths and withhold credential or user-data content. They do
not authorize deletion, staging, integration, release, or deployment.

## CI relationship


Pull requests use `.github/workflows/quality-pr.yml`: policy/static checks,
changed Go packages plus their production and test-import dependency closure,
and the frontend scope when frontend files or product identity changed. A Go
dependency version/checksum change selects its actual module consumers, including
external indirection and test-only imports. Toolchain/module/replacement authority
changes or unclassified package-external inputs retain complete-graph coverage.
Declared verification-only workflow changes run mandatory CI contract checks.
Documentation-only changes do not run product tests.
Before frontend dependency installation, the fast path verifies the same
reviewed migration manifest used by full CI. Run it from a clean candidate or
clone; stale provenance fails before expensive frontend checks. Do not delete
unowned local dependencies or build output to force this condition.
Go tests share the existing inventory/evidence executor in one to four bounded,
disjoint partitions and batches of at most 64 top-level tests. All discovered
tests and subtests remain covered; every partition's failure propagates to the
required aggregate. Distinct artifacts retain complete planned/executed inventories.

The authoritative full workflow is `.github/workflows/quality.yml`. It runs by
manual dispatch and daily at 02:00 in the explicit `Asia/Shanghai` timezone on
the default branch, combining these gates with frontend type/lint/test/build checks,
Go formatting, vet and race tests, source/security audits, clean-copy builds,
and release lifecycle verification. Routine PR review uses the required scoped
checks; comprehensive qualification remains the scheduled/manual full workflow,
not a claim inferred from a fast check. Native Windows release validation first
tests its environment isolation, then performs the complete installed lifecycle.
A promotion of an exact verified revision does not repeat checks merely because
the branch name changed. Changed inputs require fresh applicable evidence.
This split does not waive configured required checks or release qualification.
Non-embedded runtime assets use exact declarations in
`runtime_input_scope.json` to select their Go owners and every production/test
import consumer. Missing declarations or unavailable owners retain conservative
coverage. These declarations cannot exempt runtime tests or replace the scheduled
full release matrix. Installer build inputs retain the same runtime consumers
as the resulting installer, including the asset-integrity and environment tests.
GitHub's scheduled execution can be delayed by runner/platform load. When hosted
execution is unavailable, local evidence must identify the exact revision,
environment, tested scope and missing gates under a distinct local status; it
must not be represented as a completed GitHub-hosted workflow or as full quality
success when only focused checks ran.
