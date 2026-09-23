# Synon Biomed Operations Runbook

This runbook targets the current Synon Biomed source and runtime contracts. The packaged release contract is
[`release-acceptance-contract.md`](release-acceptance-contract.md). A successful
build or a historical non-Web compatibility report is not sufficient to
authorize a release.

## Runtime contract

- A release contains native binaries, compiled `web/` assets, retained Skills/assets, installers, integrity metadata, an SBOM, license evidence, and provenance.
- Go, Bun, Node.js and npm are build-time requirements only. On Linux/WSL amd64,
  Windows x64, macOS Intel, and macOS Apple Silicon, first service startup
  provisions the required Python and R scientific runtimes from the verified
  platform-bound Conda catalog; later tasks reuse those immutable generations.
  Unsupported operating-system/architecture pairs fail closed before any
  installer process starts. Optional kernel and MCP sidecars declare their own
  runtimes.
- Runtime state is external to the release directory. Set `SYNON_HOME` to a dedicated state directory and preserve it across upgrades.
- On native Windows, the installer keeps its package cache under the current
  user's runtime state root (`p` beside `conda` by default) to reduce extraction
  path length. It does not require a nondefault short `SYNON_HOME`. Deep paths
  may still expose upstream installer or filesystem limits; if installation
  fails, preserve the actual installer error and inspect the configured state
  path before retrying. Do not change the package lock or move a live cache.
- Native Windows/macOS packages include the platform-bound Python/R installer
  assets, but the current kernel confinement boundary is Linux/WSL-only.
  Do not treat a successful native Python/R installation or its core-runtime
  status as evidence that scientific task execution is available on those
  platforms; use Linux/WSL until a separately verified native confinement
  implementation is delivered.
- Advanced deployments may set absolute `SYNON_CONDA_HOME` and
  `SYNON_CONDA_ENVS_PATH` overrides; otherwise both roots are derived from the
  current user's `SYNON_HOME`/platform data directory and validated at startup.
- The default listener is `127.0.0.1:8765`. A non-loopback listener is rejected unless `SYNON_LINK_AUTH_PASSWORD` is configured.
- The default operator username is `local`. There is no compiled-in password; set a unique deployment password.
- The production runner uses the active saved workspace model provider. `go_builtin` is a test/development authority only.

## Public source checkout

### Skills library

Settings → Skills lists installed built-in, imported, and personal skills together.
Search and the Research field selector refine the list; expand Filters to narrow
by source or enabled state. Personal drafts appear under All sources or Personal
when the enabled-state filter is All. Imported source update/removal controls
remain available under the Imported source filter.

Add skill is the single entry for the online market, GitHub import, file import,
and personal skill creation. The online market opens separately and does not
replace the library or reset its search and filters. Filtering never disables
or deletes a skill.

The catalog scrolls independently; its pagination remains at the bottom of the
workspace on full, filtered and final pages. Cards wrap full skill names and
summaries at native text size, and category icons use the declared catalog field
rather than name matching. The detail view uses the same localized summary and
category. Source identifies where a skill was loaded from; it is not an assertion
of authorship. Original Markdown instructions remain unchanged, and non-Markdown
files are shown as source text.

### Connectors

Settings → Connectors manages installed MCP services. Search uses the displayed
localized descriptions; the filter selects connected, attention-needed or custom
connectors. Cards preserve full names and descriptions, configuration, permissions
and enable/disable controls. Pagination stays below the independently scrolling
library. Wide desktop pages use four columns and three rows (12 connectors),
with rows sharing the available height. Shorter windows scroll without clipping
card text or controls; narrow windows keep the responsive layout. Usage and
configuration share one card footer. Connection status is a service health signal, not proof of a completed
scientific operation.

Add connector opens custom configuration, the optional local catalog or the online
market. Browsing these catalogs preserves the installed library's search and page.
Installation, credential authorization and permission changes still require their
own explicit actions; opening the catalog does not install or authorize anything.
Sync directory remains available next to the library filters.

### Storage workspace

Settings → Storage loads directory status, file placement rules, cloud connections,
and size scans independently. A large environment scan does not block the directory
controls. Refresh status reloads metadata; the usage panel's refresh explicitly
rescans. Usage reports are cached for five minutes and show their scan time.
Failed refreshes retain the previous values with an error; unknown sizes remain
unknown rather than appearing as zero.

Displayed sizes are logical file sizes, not allocated or reclaimable disk space.
On supported Unix and Windows hosts, hard links count once within each category.
Categories and individual environment rows can still share files. Environment
storage groups physical directories and retained generations by environment name;
it does not follow activation links or claim that every measured environment is
active. These details must not be summed as reclaimable storage. Available capacity refers to
the data volume, which may differ from the host volume in a VM or container.
Symlinks are not traversed. Partial scans are explicitly marked.
If any entry in a category cannot be measured, its `totalBytes` is `null` and
the UI excludes that unknown category from the measured subtotal. A successfully
scanned empty or absent category still reports zero.

The authenticated data-directory read accepts `includeUsage=false` for lightweight
status; its default full read retains the conservative per-entry migration copy
estimate. Disk and environment usage reads accept `refresh=true` to bypass a
cached report. Both query parameters accept only `true` or `false`.

Changing the directory remains a separate confirmed action with the existing
running-task, target, space and restart checks. Opening the dialog only estimates
copy capacity; it does not move files. Completing a migration while keeping its
source and permanently deleting the source use distinct confirmation dialogs.
Save rules continue to use relative paths below the data root. No data-layout
migration or new storage engine is required by this interface.

### Local scientific software

Settings → Scientific environments, immediately below Network, is the single
catalog for local scientific software preparation. It presents the required
Python/R core runtimes and the registered optional predownload environments as
categorized cards, including package specifications, observed readiness and
installation progress.
Search matches software specifications as well as environment names; category
and status filters also include newly registered catalog entries.
Storage retains usage accounting and links to this catalog.
Saving a selection starts preparation in the background through the same managed
environment controller used by tasks; opening the page or selection dialog does
not install anything. Unselecting a queued item prevents it from starting. A
preparing card has an explicit pause action that cancels the active preparation;
ready software has an explicit uninstall action that deactivates its managed
environment after confirmation. The page distinguishes queued, preparing,
paused, ready and failed states, refreshes while preparation is active, and
offers an explicit retry for selected failed items. A card download requires
confirmation, preserves the current selections, and uses the existing
preparation queue. Progress is shown only when reported by the installer, not estimated
from elapsed time. Ready means the managed environment passed its checks, not
that a scientific task or result has been validated.

Package caches and environment generations use the configured data/conda roots
shown in Storage. The default root is resolved per user from `SYNON_HOME` or
the platform user data directory; product code and release assets never embed a
developer's absolute path. The layout is stable and shared by all tasks:
`${SYNON_HOME}/conda/envs/<environment>` is the active pointer and
`${SYNON_HOME}/conda/envs/.generations/<environment>/<generation>` stores the
immutable generation. Reopening or restarting the application verifies and
reuses a matching generation instead of downloading it again. A task discovers
software through its existing tools and executes under the same managed
environment authority; task outputs remain in the task/project artifact
workflow, not in the software installation directory.
The authenticated `/api/preferences/scientific-runtimes` endpoint provides
status with GET, saves registered `enabled_ids` with PUT, and retries one selected
optional environment with POST `{ "id": "..." }`; the same POST can retry a
failed required core runtime, while core runtimes cannot be paused or
uninstalled. Mutations require the normal authenticated session and CSRF
protection.

### Source startup

The public repository is `Victor-Xu-1/synon-biomed`.
A source checkout does not itself designate a tagged or packaged GitHub
Release. Do not present a workflow artifact, a source archive, or `make build`
output as an installable product release. Use an Ubuntu/WSL source checkout
with Git, Go >=1.26, Node.js >=22.22 and <25, and npm:

```bash
git clone https://github.com/Victor-Xu-1/synon-biomed.git
cd synon-biomed
bash scripts/dev/install-source-cli.sh
synon start
```

The entry validates Go >=1.26, Node.js >=22.22 and <25, npm, and its required
Unix tools before starting. It reserves only numeric loopback ports 8766 and
8765, starts the existing backend and frontend source hosts, requires structured
healthy gateway responses from both routes, and then prints:

```text
READY_URL=http://127.0.0.1:8765/#/login
```

The one-time installer creates only the user-level `~/.local/bin/synon` symlink
and never edits system directories or shell startup files. If it prints a PATH
hint, run that `export PATH=...` line once in the current shell. From then on,
use `synon start`; its options are passed to the canonical source quickstart.

Open the printed URL exactly; prefer numeric `127.0.0.1` over `localhost` from Windows.
A fresh local state may redirect from `#/login` to the guided `#/onboarding`
flow; complete that first-use flow before entering the workbench. Use
`synon start --open-browser` only when the current
terminal has desktop-browser access. The browser action is never implicit. No
Web password is compiled into the repository.

### First-run scientific runtime preparation

On every supported native target (Linux/WSL amd64, Windows x64, macOS Intel,
and macOS Apple Silicon), the gateway starts its service-owned core supervisor
before optional scientific environments are prepared. Python and R are required
core runtimes: they are verified/provisioned once, exposed as ready only after their
interpreter and package smoke checks pass, and cannot be paused or uninstalled
from the optional-selection UI. If a core install fails, the status card offers
an explicit retry. The onboarding **Local software** tab records one host-level
selection for optional environments and immediately returns control to the
workspace; optional installation stays in the background queue. Optional
entries start unselected, so an upgraded host does not infer a large download
without a saved selection. Estimates are planning information rather than
validation ceilings: a registered environment is not rejected or disabled
solely because its resolved install grows beyond a previous estimate.

The required core contract is intentionally small and explicit:

| Required runtime | Baseline contract | Why it is required |
| --- | --- | --- |
| `synon-biomed-python` | Python 3.11, RDKit, py3Dmol and the bundled rendering helpers | Default molecular, structure and general scientific task path |
| `synon-biomed-r` | R 4.5, `data.table`, `ggplot2`, `jsonlite`, and `tidyverse` namespaces | Supported R analysis and shared report/data handling |

Shell, Java, GPU frameworks, docking engines, omics stacks and other large
specialized tools are not hidden additional prerequisites. They remain
optional, task-selected runtimes and are installed through the same managed
environment authority when a workflow requires them.

The catalog includes the following independently versioned environments in
addition to the common structure, 2D interaction, biomolecular electrostatics,
and Vina groups. Each entry
publishes its current estimated additional storage, and the complete current
catalog is approximately 5.7 GiB when all groups are selected.

| Optional domain | Estimated additional storage | Primary runtime scope |
| --- | ---: | --- |
| Biomolecular electrostatics | 700 MiB | APBS, PDB2PQR, PROPKA, and RDKit potential-map preparation |
| Drug chemistry and process calculations | 180 MiB | Thermo, Chemicals, ChemPy, ASE, table and public-record parsing |
| Molecular conversion and rapid quantum tools | 650 MiB | Open Babel, Dimorphite-DL, xTB, ASE |
| Classical QSAR and ADMET | 600 MiB | scikit-learn, XGBoost, LightGBM, SHAP, Optuna, Mordred |
| Clinical statistics and pharmacometrics | 220 MiB | survival analysis, classical statistics, nonlinear fitting, units, Excel |
| Single-cell and omics analysis | 720 MiB | Scanpy, AnnData, Harmony, Leiden, igraph |
| Pharmacogenomics command line | 300 MiB | SAMtools, BCFtools, BEDTools, Minimap2, SeqKit, pysam |
| Molecular dynamics and simulation | 560 MiB | MDAnalysis, MDTraj, ParmEd, OpenMM CPU |
| Medical imaging analysis | 420 MiB | pydicom, NiBabel, SimpleITK, scikit-image |
| Instrument and analytical data | 260 MiB | Allotropy, Pandas, OpenPyXL, PDFPlumber |

The estimates describe optional immutable environment storage after the
required Python/R generations are present; exact downloads vary by platform,
dependency resolution, and package-cache reuse. The required runtime catalog
is selected for Linux/WSL amd64, Windows x64, macOS Intel, or macOS Apple
Silicon and its explicit lock is checked against the host before micromamba
starts. There is no fixed
per-environment or aggregate rejection threshold. Sequential preparation,
bounded timeouts, finite retries, and explicit selection provide the resource
controls instead. No environment, wheel, or Conda package is written into the
Git checkout. A fresh supported host needs network access to its pinned package
sources once; subsequent starts and tasks verify and reuse the active
generations. Unsupported operating-system/architecture pairs fail closed with
`bundled_runtime_platform_unsupported`; a healthy gateway is not evidence that
the scientific runtimes are ready.

The structure viewer requests this runtime lazily when a protein, pocket, or
ligand surface is first enabled. PDB2PQR assigns AMBER protein charges after
PROPKA protonation at pH 7.4, RDKit assigns Gasteiger ligand charges with
explicit hydrogens, and APBS solves separate protein-only and ligand-only
linearized Poisson-Boltzmann maps at 0.15 M ionic strength. A multi-ligand view
uses one bounded request (at most eight keyed ligand mol blocks): the server
prepares and solves the protein once, then solves one ligand component per key
against the same sizing frame. The legacy single `ligand_mol_block` request and
response shape remain supported. Every component uses the same grid dimensions,
origin, spacing, and center. Request bytes, per-ligand and aggregate atoms,
aggregate grid values, raw and compressed output, execution time, concurrency,
and cached response size are independently bounded.

The gzip-compressed OpenDX maps are validated before use and decompressed with a
streaming byte ceiling in the browser: protein and pocket surfaces sample the
protein map, while ligand surfaces sample their keyed ligand maps, all with the
same fixed -5 to +5 kT/e color range. Non-protein `HETATM` records are not
silently included in the PDB2PQR protein calculation; the report warns that
they were excluded and identifies separately supplied ligand components. A
Mol* snapshot contributes each world-coordinate transform once, preserves
alternate-location, insertion, occupancy, and temperature-factor metadata,
maps distinct symmetry instances to distinct PDB chains, and fails closed when
an identifier or numeric field cannot be represented without truncation. A
failed, missing, oversized, or misaligned component calculation is shown as
unavailable; the viewer does not substitute the combined complex field,
residue categories, or a uniform surface as if they represented electrostatic
complementarity.

P2Rank pocket prediction is provisioned on demand rather than by the default
warmup set because its pinned archive is approximately 263 MiB and expands into
an additional Java-backed runtime. Source, capacity, profile-selection,
security, recovery, and two-structure acceptance procedures are documented in
[`engineering/p2rank-runtime.md`](engineering/p2rank-runtime.md).

Inspect `/api/health` and its `scientific_runtime_warmups` object for each
runtime's `waiting_for_selection`, `scheduled`, `preparing`, `retrying`,
`ready`, `failed`, `stopped`, or `disabled` state. Transient download/provider
failures use a finite retry schedule and do not make the Web gateway unhealthy.
The same selected runtime is attempted again after a service restart; no
alternate scientific engine is silently substituted. Native Windows packages
do not include Micromamba, so local scientific warmups report `disabled` there.

`Ctrl+C` or `synon stop` stops only the exact backend/frontend hosts created by
this invocation; `synon status` reports the verified owner.
The quickstart fails closed when either port is occupied, either child exits, a
health body is not the expected gateway response, or startup exceeds its bounded
timeout. It never finds and kills a process by port. Persistent source state and
logs default to:

```text
${XDG_STATE_HOME:-$HOME/.local/state}/synon-biomed-source-quickstart
```

Override that dedicated path with `--state-dir`. It must remain outside the
checkout. Vite dependencies remain in the ignored `frontend/node_modules` path;
all runtime data, logs, compiled backend candidates, locks, and PID metadata stay
in the external state directory.

For updates, stop the quickstart, use `git pull --ff-only`, and start it again.
Do not update a dirty checkout, and do not use this source flow as a substitute
for the verified archive installation lifecycle below.

## Release candidate and authorization boundary

[`governance/release-policy.json`](governance/release-policy.json) is static
source policy. It cannot grant current release or tag authorization. The full
quality workflow builds Linux and Windows archives once, writes
`RELEASE_CANDIDATE.json`, and uploads the complete candidate set under an
artifact name bound to the exact source SHA.
The source-tree digest hashes the clean commit's Git archive rather than
ambient filesystem permission bits, so the same commit verifies identically in
Windows-mounted and native Linux clean worktrees.

After downloading a candidate from its recorded workflow run, verify the
manifest, source revision, and every archive/checksum pair:

```bash
python3 scripts/quality/release_candidate_manifest.py verify \
  --repo . \
  --artifact-dir /absolute/external/candidate-directory \
  --run-id WORKFLOW_RUN_ID \
  --run-attempt WORKFLOW_RUN_ATTEMPT
```

Current release authorization is a short-lived HMAC-SHA256 receipt created and
stored outside the source tree after explicit user approval. Its key is an
external credential with owner-only permissions. Verify the receipt against
the exact candidate bytes:

```bash
python3 scripts/quality/release_receipt_gate.py \
  --repo . \
  --artifact-dir /absolute/external/candidate-directory \
  --receipt /absolute/external/authorization.json \
  --key-file /absolute/external/authorization.key
```

Neither command creates a tag or release. Promotion is controller-owned and
must additionally verify the full workflow conclusion, main SHA, unused receipt,
absent tag, isolated release credentials, and enabled GitHub Release
immutability. Promotion attaches these exact files without rebuilding.

## Download from GitHub Packages

GHCR carries the same Linux and Windows installation archives, checksums and
candidate manifest as the corresponding GitHub Release. It uses the OCI
artifact type `application/vnd.synon-biomed.release.v1`. This is an installation
bundle, **not a runnable Docker image**: the Linux execution runtime requires
a systemd user manager and host sandbox support. The existing native install
and upgrade procedures below remain authoritative.

Install [ORAS](https://oras.land/docs/installation) and select an actually
published version. Set `SYNON_RELEASE_VERSION` to that published tag before
running the example below:

```bash
export SYNON_RELEASE_VERSION=vX.Y.Z
mkdir -p /tmp/synon-download
oras pull "ghcr.io/victor-xu-1/synon-biomed:${SYNON_RELEASE_VERSION}" --output /tmp/synon-download
cd /tmp/synon-download
sha256sum --check "synon-biomed-${SYNON_RELEASE_VERSION#v}-linux-amd64.tar.gz.sha256"
sha256sum --check "synon-biomed-${SYNON_RELEASE_VERSION#v}-windows-amd64.tar.gz.sha256"
```

For automated deployment, pin `ghcr.io/victor-xu-1/synon-biomed@sha256:DIGEST`
from the package's publication result. Public downloads do not require a token.
GitHub may display a generic Docker pull command for this registry; use ORAS
for this artifact type. Do not extract or execute an archive before checking
its checksum and release manifest. The package transport never contains user
databases, runtime credentials or scientific-task output.

## Verify an archive

The installer verifies the package manifest before replacing an installation. For an extracted package:

```bash
./synon-go release-supply-chain verify --root .
./synon-go release-manifest verify --root .
./synon-go --health-json
```

Each archive is emitted with a sibling `.sha256` file. Verify that checksum
against a trusted release channel before installation.

The release-management script is part of the trust root. Run it from a trusted
source tree or the currently trusted installation, never from the archive being
validated. Linux and Windows managers compare the candidate identity exactly
with the manager tree's `product-identity.json` before executing candidate code,
creating a backup, moving an installation, or registering a service.
Bootstrap installers are intentionally excluded from release archives, so an
archive cannot supply the program that establishes its own initial trust.

The provenance is an in-toto/SLSA statement with SHA-256 source and binary subjects. It is not a cryptographic publisher signature; distribute archive checksums through a trusted channel.

## Linux or WSL installation

```bash
export SYNON_RELEASE_VERSION="${SYNON_RELEASE_VERSION:-vX.Y.Z}"
export SYNON_RELEASE_DIR="$HOME/.local/opt/synon-biomed"
./scripts/install-release.sh ./synon-biomed-${SYNON_RELEASE_VERSION#v}-linux-amd64.tar.gz "$SYNON_RELEASE_DIR"
"$SYNON_RELEASE_DIR/synon-go" --health-json
```

Install a hardened systemd user service:

```bash
"$SYNON_RELEASE_DIR/scripts/install-systemd-user.sh" "$SYNON_RELEASE_DIR" "$HOME/.local/state/synon-go"
```

The installer creates:

- `~/.config/systemd/user/synon-go.service` with mode `0600`.
- `~/.config/synon-go/synon-go.env` with mode `0600`.
- `~/.local/state/synon-go` with mode `0700`, unless another state directory was supplied.

Edit the protected environment file before exposing or enabling integrations:

```bash
editor "$HOME/.config/synon-go/synon-go.env"
systemctl --user restart synon-go.service
systemctl --user --no-pager --full status synon-go.service
```

Recommended minimum environment:

```dotenv
SYNON_ADDRESS=127.0.0.1:8765
SYNON_HOME=/home/USER/.local/state/synon-go
SYNON_LINK_AUTH_USERNAME=local
SYNON_LINK_AUTH_PASSWORD=replace-with-a-long-random-password
SYNON_RUNNER_ENABLED=true
SYNON_RUNNER_PROVIDER=workspace
```

The unit uses `ProtectSystem=strict`, `ProtectHome=read-only`, and a writable exception for `SYNON_HOME`. If an approved workflow must write another path, add only that path with a user-unit `ReadWritePaths=` override.

## Login and Web

Open `http://127.0.0.1:8765/`. Prefer the numeric loopback address from Windows
because `localhost` can select an IPv6 or forwarding path that does not reach
the WSL listener. Use the configured operator username and password. The
password is deployment state, not a package default.

Optional external sign-in is configured only through deployment environment
variables. Google uses Authorization Code with PKCE and server-side ID-token
verification:

```dotenv
SYNON_AUTH_PUBLIC_BASE_URL=https://biomed.example
SYNON_AUTH_GOOGLE_CLIENT_ID=google-oauth-client-id
SYNON_AUTH_GOOGLE_CLIENT_SECRET=google-oauth-client-secret
```

Register the exact callback `https://biomed.example/api/auth/oidc/google/callback`.
Loopback development may omit the client secret and use a PKCE public client;
remote HTTPS deployments require a confidential client secret. Provider tokens
are never returned to the renderer or persisted.

Sign in with Apple requires the Services ID, Team ID, Key ID, and a private key
provided by absolute file path or Base64:

```dotenv
SYNON_AUTH_APPLE_CLIENT_ID=com.synon.biomed.web
SYNON_AUTH_APPLE_TEAM_ID=apple-team-id
SYNON_AUTH_APPLE_KEY_ID=apple-key-id
SYNON_AUTH_APPLE_PRIVATE_KEY_FILE=/run/secrets/synon-apple-private-key.p8
# SYNON_AUTH_APPLE_PRIVATE_KEY_B64=base64-encoded-pem
SYNON_AUTH_PUBLIC_BASE_URL=https://biomed.example
```

Register `https://biomed.example/api/auth/oidc/apple/callback` in the Apple
Developer portal. Raw multiline PEM environment values are rejected; the key
and generated client-secret JWT remain deployment-only.

WeChat website login is a separate Open Platform application and requires the
same public HTTPS origin:

```dotenv
SYNON_AUTH_WECHAT_APP_ID=wechat-open-platform-app-id
SYNON_AUTH_WECHAT_APP_SECRET=wechat-open-platform-app-secret
SYNON_AUTH_PUBLIC_BASE_URL=https://biomed.example
```

Register `https://biomed.example/api/auth/oauth/wechat/callback` and the matching
authorized domain. The server validates the stable UnionID and never persists
provider tokens, OpenID, or AppSecret. Restart the service after changing any
provider setting, then verify `GET /api/auth/providers`; account security and
session revocation are available in the account settings page.

The Web client, REST API, WebSocket/SSE streams, message channels, and Synon
Link share the same Go runtime and durable state. TUI is outside the current
product scope and the installed CLI does not expose a `tui` subcommand.

## Model authority

1. Save and enable a model provider for the operator in workspace provider settings.
2. Keep `SYNON_RUNNER_PROVIDER=workspace` and `SYNON_RUNNER_ENABLED=true`.
3. Run the redacted offline readiness plan:

```bash
synon-go model-smoke --plan --require-all --json
synon-go doctor --json
```

Only run `synon-go model-smoke --target runner --run --require-all --json` with explicit authorization and known cost. A plan or built-in test response is not evidence that an external provider is reachable.

## Message channels

Set `SYNON_ENABLED_ADAPTERS` and the matching credentials for Feishu or WeChat. Telegram and DingTalk are not supported by the current runtime. An enabled channel fails startup when its inbound/outbound credential set is incomplete.

Inbound users must be paired before a task/session is created. Each accepted chat binds to `im:<platform>:<chat-id>`. Outbound route metadata and terminal delivery checkpoints are durable. WeChat context tokens are stored only in the encrypted secret vault and are hidden from public secret APIs.

Pairing is rechecked for every outbound delivery. Revocation cancels an
in-flight route, removes its persisted route/checkpoint, and deletes its
encrypted context token before later messages can be accepted.

After restart, the runtime replays an unfinished response from its last delivered terminal checkpoint. Delivery is at-least-once: a crash after the remote platform accepts a message but before the local checkpoint commits can produce a duplicate. Channel APIs do not provide a portable exactly-once transaction.

Plan a credentialed delivery without exposing credentials:

```bash
synon-go-live-im-smoke --plan --require-all --json
```

Run live delivery only in an authorized channel test environment.

## Final external acceptance

The repository-level P9 gate combines both production model targets and both
supported outbound channels. It never reads credentials from command-line values;
load them from a protected `SYNON_CONFIG` file, the encrypted Workbench state,
or a mode-0600 service environment file. The plan is redacted and makes no
external model or platform request; it does verify the local runtime login and
audit path:

```bash
python3 -B scripts/p9_external_acceptance.py \
  --mode plan \
  --synon-binary "$SYNON_RELEASE_DIR/synon-go" \
  --live-im-binary "$SYNON_RELEASE_DIR/synon-go-live-im-smoke" \
  --runtime-url http://127.0.0.1:8765 \
  --output "$HOME/.local/state/synon-go/p9-external-plan.json"
```

Exit code `3` means one or more model, channel, target, or runtime-audit
settings are absent. Do not proceed until the plan reports `status: ready`.
The runtime URL must be loopback. When Web password authentication is enabled,
the channel helper authenticates locally and establishes the required CSRF
session before sending any test message.

The live command can incur model cost and sends one smoke message to WeChat and
Feishu. Use an approved test destination, then provide the
exact one-shot authorization phrase:

```bash
export SYNON_EXTERNAL_TEST_AUTHORIZED=I_ACCEPT_NETWORK_COST_AND_MESSAGES
python3 -B scripts/p9_external_acceptance.py \
  --mode run \
  --synon-binary "$SYNON_RELEASE_DIR/synon-go" \
  --live-im-binary "$SYNON_RELEASE_DIR/synon-go-live-im-smoke" \
  --runtime-url http://127.0.0.1:8765 \
  --timeout-seconds 30 \
  --max-attempts 1 \
  --output "$HOME/.local/state/synon-go/p9-external-run.json"
unset SYNON_EXTERNAL_TEST_AUTHORIZED
```

Success requires live, non-empty runner and compact responses, successful
delivery on both supported platforms, and two redacted runtime audit records. The
combined report contains statuses and missing variable names only. The Python
wrapper is a source acceptance tool; installed production operation continues
to require only the two Go binaries.

## Upgrade

1. Verify and back up the current release.
2. Stop the service before taking a consistent state backup.
3. Back up `SYNON_HOME` separately from the release directory.
4. Install the new archive to the same release path; the installer validates a staging tree before atomic replacement.
5. Restart and verify health, login, model readiness, channel diagnostics, and one representative flow.

```bash
systemctl --user stop synon-go.service
./scripts/backup-release.sh "$SYNON_RELEASE_DIR" /secure/backup/synon-biomed-release
tar --xattrs --acls -C "$HOME/.local/state" -czf /secure/backup/synon-go-state.tar.gz synon-go
./scripts/install-release.sh ./new-release.tar.gz "$SYNON_RELEASE_DIR"
systemctl --user start synon-go.service
curl --noproxy '*' --fail --silent http://127.0.0.1:8765/api/health
```

Do not store state inside the release directory. Release rollback does not implicitly roll back state schemas.

## Rollback and uninstall

```bash
systemctl --user stop synon-go.service
./scripts/rollback-release.sh "$SYNON_RELEASE_DIR" /secure/backup/synon-biomed-release
systemctl --user start synon-go.service
```

Restore a state backup only when the target binary cannot read the upgraded schema and after preserving current state for diagnosis. The v1.1 migration CLI provides inspect, run, verify, activate, rollback-cutover, and rollback operations.

Remove the service before the release:

```bash
"$SYNON_RELEASE_DIR/scripts/uninstall-systemd-user.sh"
./scripts/uninstall-release.sh "$SYNON_RELEASE_DIR"
```

The service environment and `SYNON_HOME` are intentionally preserved. Delete them only after a separate retention decision and verified backup.

## Windows installation

```powershell
$env:SYNON_RELEASE_VERSION = if ($env:SYNON_RELEASE_VERSION) { $env:SYNON_RELEASE_VERSION } else { 'vX.Y.Z' }
$env:SYNON_RELEASE_DIR = if ($env:SYNON_RELEASE_DIR) { $env:SYNON_RELEASE_DIR } else { 'C:\Tools\SynonBiomed' }
powershell -ExecutionPolicy Bypass -File .\scripts\install-release.ps1 `
  -Archive ".\synon-biomed-$($env:SYNON_RELEASE_VERSION.TrimStart('v'))-windows-amd64.tar.gz" `
  -InstallDir $env:SYNON_RELEASE_DIR
$env:SYNON_RELEASE_DIR\synon-go.exe --health-json
$env:SYNON_RELEASE_DIR\synon-go.exe release-manifest verify `
  --root $env:SYNON_RELEASE_DIR
```

Use `scripts/manage-release.ps1` for backup, rollback, and uninstall. The package does not automatically install a Windows service; run interactively or register it with an operator-managed service account and an ACL-protected environment.

### Windows local runtime start and health

The one-shot `--health-json` command exits before opening configuration, state,
or a listener. Starting the actual workstation runtime is a separate operation.
Use a versioned install directory, a separate versioned state copy, and a log
directory outside both. This example intentionally binds only numeric loopback;
blank Web credentials are acceptable only for that strictly local deployment.

```powershell
$InstallDir = Join-Path $env:LOCALAPPDATA 'SynonBiomed\0.1.1-REVISION'
$StateDir = Join-Path $env:LOCALAPPDATA 'SynonBiomed\state\8765-REVISION'
$LogDir = Join-Path $env:LOCALAPPDATA 'SynonBiomed\logs\8765-REVISION'
$Binary = Join-Path $InstallDir 'synon-go.exe'
$Stdout = Join-Path $LogDir 'stdout.log'
$Stderr = Join-Path $LogDir 'stderr.log'

if (-not (Test-Path -LiteralPath $Binary -PathType Leaf)) { throw 'Release binary is missing' }
if (-not (Test-Path -LiteralPath $StateDir -PathType Container)) { throw 'State directory is missing' }
if (Get-NetTCPConnection -State Listen -LocalPort 8765 -ErrorAction SilentlyContinue) {
  throw 'Port 8765 is already owned; inspect it instead of replacing it'
}
New-Item -ItemType Directory -Path $LogDir -Force | Out-Null

$env:SYNON_HOME = $StateDir
$env:SYNON_ADDRESS = '127.0.0.1:8765'
$env:SYNON_LINK_AUTH_USERNAME = ''
$env:SYNON_LINK_AUTH_PASSWORD = ''
$env:SYNON_RUNNER_ENABLED = 'true'
$env:SYNON_RUNNER_PROVIDER = 'workspace'
$Process = Start-Process -FilePath $Binary -WorkingDirectory $InstallDir `
  -WindowStyle Hidden -RedirectStandardOutput $Stdout `
  -RedirectStandardError $Stderr -PassThru

$Deadline = [DateTime]::UtcNow.AddSeconds(30)
$Health = $null
do {
  $Process.Refresh()
  if ($Process.HasExited) { throw "Synon Biomed exited with code $($Process.ExitCode)" }
  try {
    $Health = Invoke-RestMethod -Uri 'http://127.0.0.1:8765/api/health' -TimeoutSec 2
    if ($Health.status -eq 'healthy' -and $Health.name -eq 'Synon Biomed' -and $Health.version -eq '0.1.1') {
      break
    }
  } catch {
    # A bounded readiness loop reports failure after the deadline.
  }
  Start-Sleep -Milliseconds 250
} while ([DateTime]::UtcNow -lt $Deadline)
if ($null -eq $Health -or $Health.status -ne 'healthy') {
  throw 'Synon Biomed did not become healthy within 30 seconds'
}
```

Do not use a copied or stale PID file as stop authority. Resolve the live
listener, verify that its executable is the expected installed binary, and only
then stop that exact process:

```powershell
$InstallDir = Join-Path $env:LOCALAPPDATA 'SynonBiomed\0.1.1-REVISION'
$ExpectedBinary = [IO.Path]::GetFullPath((Join-Path $InstallDir 'synon-go.exe'))
$Listener = Get-NetTCPConnection -State Listen -LocalPort 8765 -ErrorAction Stop |
  Select-Object -First 1
$Runtime = Get-CimInstance Win32_Process -Filter "ProcessId=$($Listener.OwningProcess)"
if (-not [string]::Equals(
    [IO.Path]::GetFullPath($Runtime.ExecutablePath),
    $ExpectedBinary,
    [StringComparison]::OrdinalIgnoreCase)) {
  throw "Port 8765 is not owned by the expected Synon Biomed release"
}
$RuntimePID = $Listener.OwningProcess
$StoppedProcess = Stop-Process -Id $RuntimePID -PassThru -ErrorAction Stop
$StoppedProcess | Wait-Process -Timeout 30 -ErrorAction Stop
if (Get-Process -Id $RuntimePID -ErrorAction SilentlyContinue) {
  throw 'Synon Biomed process did not exit within 30 seconds'
}
if (Get-NetTCPConnection -State Listen -LocalPort 8765 -ErrorAction SilentlyContinue) {
  throw 'Synon Biomed did not release port 8765'
}
```

### Windows state backup and rollback

Release rollback and state rollback are different authorities. Stop the
verified process before copying SQLite state, and never point an older binary at
state it is not proven to read. Preserve the current install and state for
diagnosis before switching to a previously verified pair.

```powershell
$StateDir = Join-Path $env:LOCALAPPDATA 'SynonBiomed\state\8765-REVISION'
$BackupRoot = Join-Path $env:LOCALAPPDATA 'SynonBiomed\backups'
$StateBackup = Join-Path $BackupRoot ('state-' + (Get-Date -Format 'yyyyMMdd-HHmmss'))
if (Test-Path -LiteralPath $StateBackup) { throw 'State backup target already exists' }
New-Item -ItemType Directory -Path $BackupRoot -Force | Out-Null
Copy-Item -LiteralPath $StateDir -Destination $StateBackup -Recurse

function Get-StateInventory([string] $Root) {
  $ResolvedRoot = [IO.Path]::GetFullPath((Resolve-Path -LiteralPath $Root).Path)
  Get-ChildItem -LiteralPath $ResolvedRoot -File -Recurse | ForEach-Object {
    $RelativePath = $_.FullName.Substring($ResolvedRoot.Length).
      TrimStart([char[]]@('\', '/')).Replace('\', '/')
    [pscustomobject]@{
      Path = $RelativePath
      Length = $_.Length
      SHA256 = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash
    }
  } | Sort-Object Path
}

$SourceInventory = @(Get-StateInventory $StateDir)
$BackupInventory = @(Get-StateInventory $StateBackup)
$SourceJSON = $SourceInventory | ConvertTo-Json -Depth 3 -Compress
$BackupJSON = $BackupInventory | ConvertTo-Json -Depth 3 -Compress
if ($SourceJSON -cne $BackupJSON) {
  throw 'State backup inventory does not match the stopped source state'
}
$BackupInventory | ConvertTo-Json -Depth 3 |
  Set-Content -LiteralPath ($StateBackup + '.inventory.json') -Encoding UTF8
```

The comparison above is executable evidence that the stopped source and backup
have identical relative paths, lengths, and SHA-256 values. Preserve the JSON
inventory beside the backup. Do not treat a backup created while the listener
was active, or one that fails this comparison, as rollback authority.

Stop the verified listener and confirm both process exit and port release before
every release-management action. The helper backs up, rolls back, or uninstalls
release files only; it intentionally does not select or downgrade user state.
Run exactly one of the following scenarios, never the three blocks in sequence.

Back up the current release before an in-place upgrade:

```powershell
$Manager = '.\scripts\manage-release.ps1'
$InstallDir = Join-Path $env:LOCALAPPDATA 'SynonBiomed\0.1.1-REVISION'
$ReleaseBackup = Join-Path $env:LOCALAPPDATA 'SynonBiomed\backups\release-REVISION'

powershell -ExecutionPolicy Bypass -File $Manager -Action Backup `
  -InstallDir $InstallDir -BackupDir $ReleaseBackup
```

Roll back an in-place installation using a previously verified backup created
before the failed upgrade. Do not use a backup of the same failed release:

```powershell
$Manager = '.\scripts\manage-release.ps1'
$InstallDir = Join-Path $env:LOCALAPPDATA 'SynonBiomed\0.1.1-current'
$PreviousReleaseBackup = Join-Path $env:LOCALAPPDATA 'SynonBiomed\backups\release-PREVIOUS'

powershell -ExecutionPolicy Bypass -File $Manager -Action Rollback `
  -InstallDir $InstallDir -BackupDir $PreviousReleaseBackup
```

Uninstall one stopped, non-current side-by-side release. External state is
preserved and must be handled separately:

```powershell
$Manager = '.\scripts\manage-release.ps1'
$RetiredInstallDir = Join-Path $env:LOCALAPPDATA 'SynonBiomed\0.1.1-RETIRED'

powershell -ExecutionPolicy Bypass -File $Manager -Action Uninstall `
  -InstallDir $RetiredInstallDir
```

For a side-by-side deployment, rollback means preserving the failed install,
logs, and state for diagnosis, then selecting the previously verified old
install/state/log trio and starting that trio directly. Do not run the in-place
Rollback action against the current side-by-side directory. For either layout,
verify package manifest, offline health, live health, login/local identity,
read-only SQLite integrity, expected migration count, and one official-Chrome
refresh flow. Release rollback never authorizes an older binary to open newer
state.

The package does not create a Scheduled Task or Windows service. Registering
autostart changes workstation policy and must be an explicit operator decision;
do not create it as an installer side effect. Until such a decision is made,
the verified manual start procedure above is the runtime recovery contract.

This release-package rule is separate from the optional canonical-source
development supervisor documented in `README.md`. That development-only task
keeps the WSL source host alive on 8765/8766 and is installed explicitly from
`scripts/dev/install-source-dev-supervisor.ps1`; it is never packaged or used as
authority for the Windows-native release described in this runbook.

After a Windows reboot, assume the product is stopped: confirm port 8765 is
free, re-run the manual start procedure, and repeat the live health check. Do
not enable a historical WSL keepalive task as authority for the Windows-native
runtime. A future Scheduled Task or service requires a separate reviewed change
that fixes the service account, directory ACLs, environment source, logs, stop
procedure, restart policy, and rollback procedure.

## Runtime capability acquisition

### Skill catalog presentation

Bundled Skill categories and localized UI summaries are maintained in
`skills/synonbiomed/catalog-ui.json`. English source descriptions and executable
instructions remain in each `SKILL.md`. The loader uses the nearest catalog
inside its configured root, so loading `skills` or `skills/synonbiomed` produces
the same bundled presentation. Missing or invalid metadata is reported as a
catalog diagnostic while the Skill remains available; an external Skill does
not borrow presentation merely because its name matches a bundled Skill.

After adding or renaming a bundled Skill, update this catalog and run
`go run ./scripts/skills-manifest --write`, then verify with
`go run ./scripts/skills-manifest --check` and `go test ./internal/skills`.
The catalog ships in the release package and its digest is included in the
asset manifest. Settings filters and localized descriptions consume the API
metadata directly; frontend source does not maintain a second Skill name map.

Filtered `host.mcp.list_methods(server)` discovery uses the same connector
identity resolver as execution. An exact connector ID takes precedence over
aliases; ambiguous aliases require an exact identity. The returned canonical
`server_filter` survives Python bridge filtering and pagination. Filtered
discovery consults only that connector, while existing owner, exclusion and
tool-grant checks still apply.

Remote HTTP MCP `tools/list`, `resources/list` and `prompts/list` may recover
one transport EOF/short-body EOF with a 250ms cancellable backoff. This retry
stays within the caller's context and is recorded as
`remote_mcp_metadata_retry`; it does not replay `tools/call`, initialization,
HTTP permission failures or application/schema errors. Transport errors retain
their machine-readable cause while public messages redact credentials.

Direct regression checks:

```bash
go test -race -timeout=0 ./internal/tools/mcpstdio -run 'TestRemoteMetadata|TestRemoteHTTP' -count=1
go test -race -timeout=0 ./internal/server ./internal/kernel -run 'TestKernelMCP|TestReplHostMCP|TestWorkspaceArtifactHandle|TestWorkspaceRecordDirectory' -count=1
```

Optional captured-array replay uses `SYNON_TEST_RECORD_SOURCE` with a selected
read-only JSON array path and `go test -v -timeout=0 ./internal/server -run
TestWorkspaceRecordDirectoryCapturedReplay -count=1`. No captured scientific
answer is embedded in production or used as a report-generation template.

When a running task lacks a Skill, MCP connector, or scientific package, the
OPERON prompt routes exactly once through the bundled
`capability-acquisition` Skill. The workflow must search local catalogs
first, select one external candidate only when necessary, and reuse the
existing authorities:

- GitHub Skills are installed through `host.skills.install`, which delegates
  to the existing Marketplace preview/import path and pins the resolved commit.
- Directory and custom MCP connectors are installed through
  `host.mcp.install`; credentials remain restricted to the credentialed
  Settings flow.
- Packages and environments remain owned exclusively by `manage_environments`
  and `manage_packages`; workloads execute through the selected Python, R, or
  Bash environment.

New GitHub Skill installs and custom MCP registrations always create a
`capability_install` approval. The originating kernel call waits for the user
decision, revalidates frame ownership, resumes the same call after approval,
and returns the existing Marketplace or MCP result. A denial is terminal for
that installation attempt and must not be bypassed through a shell, direct
download, another registry, or a replacement task. Enabling an already
reviewed directory connector remains an idempotent directory operation.

After installation, verify the Skill through `host.skills.list/read`, or the
MCP through `host.mcp.list`, catalog inspection, and one bounded read-only
call. Then resume the original blocked step with the same task identity and
artifact versions. Installation is an intermediate audited event, not task
completion.

### Hosted MCP credentials (BYOK)

Hosted scientific MCPs that require an account use a user-owned credential;
Synon Biomed does not bundle provider keys or run their compute workloads
locally. Open **Settings -> Connectors**, then choose **Configure** on the
connector. The configuration dialog separates the current connection state,
official application/login links, account authorization and API Key / Token
entry. Only supported methods are shown; provider account passwords are entered
on the provider's own login page, never in Synon Biomed. The current contracts are:

| Connector | Credential | Transport injection |
|---|---|---|
| Open Targets (Official) | None | Public hosted MCP endpoint |
| Om (OMTX) | Provider account via OAuth | OAuth bearer token |
| PatSnap Chemical Molecular | Open Platform API key | Server-side `apikey` query parameter |
| Inductive Bio | Provider account via OAuth | OAuth bearer token |
| Boltz API (Official) | OAuth sign-in or workspace API key | OAuth bearer token or `x-api-key` |
| Tamarind Bio | Tamarind API key | `x-api-key` request header |
| Adaptyv Cloud Lab | OAuth sign-in or Foundry token | OAuth bearer token or `Authorization: Bearer` |

Use the application link to obtain provider access. For a key, paste only the
credential value, without a header name or `Bearer` prefix, and select **Save
and check connection**. For OAuth, select **Sign in and authorize**, complete
the provider window, then refresh the dialog. Its status distinguishes missing
keys, required/rejected authorization, connection errors and successful
connections. A saved-key receipt is not proof of a successful connection. A
failed status reload is shown as unavailable rather than retaining a stale
success. Closing the dialog clears any unsaved key draft.

Provider application URLs are optional `credentialUrl` entries in the canonical
MCP upstream metadata. Older catalogs fall back to their provider homepage;
unsafe or malformed links are not rendered. No new authentication endpoint or
credential storage path is introduced.

API keys are trimmed, required to be a single line, limited to 16 KiB, and
stored in the encrypted owner-scoped secret store. Connector inventory, API
responses, logs, tool schemas, and model context expose only whether a key is
configured; the key value is never returned. Disconnecting a bundled hosted
connector deletes its stored API key and OAuth state. Replacing a key writes a
new encrypted value without pre-filling the old value into the browser.
OAuth access tokens are encrypted, bound to the connector identity and remote
origin, and removed when expired. The current flow does not retain refresh
tokens; authorize the connector again after provider token expiry.

Saving a key enables and probes the connector through the same runtime path
used by `host.mcp`. Authentication failures remain visible as connector health
errors; they are not converted to a successful state. Computational jobs,
experiment creation, quote acceptance, payment, and other physical or costly
actions use the `confirm` permission policy by default and require an explicit
approved policy override to run without a per-call prompt. Provider account,
quota, billing, data-handling, and commercial terms remain the user's
responsibility.

### Remote MCP connection timeouts

Public connectors such as Open Targets and Imaging Data Commons do not need
API keys. A TLS handshake or connection timeout is a network-path failure,
not evidence of missing credentials. If the deployment requires an outbound
proxy, set `SYNON_NETWORK_PROXY` (an operator-trusted HTTP proxy origin) in the
service's persistent startup environment, or set `network.proxy` in its startup
configuration, then restart that service. A proxy exported only in an interactive
shell does not configure an already-running service. The configured proxy must
be reachable from the service's actual host/container/WSL network namespace.

Keep HTTPS certificate verification, public-destination validation and
per-call timeouts enabled. Verify a fresh MCP catalog and a read-only tool result;
a successful HTTP response or a previously cached green status is insufficient.
The opt-in acceptance tests use the same trusted TLS/proxy and public-address
client boundary as the server. With the deployment's network environment set:

```sh
SYNON_RUN_REAL_REMOTE_MCP=1 go test ./internal/mcpdirectory -run 'TestOfficial(OpenTargetsHostedMCPCompletesRealEGFRResolution|IDCRemoteMCPExposesRealReadOnlyCatalog)' -count=1 -v
```

These checks query public data and do not configure credentials or submit paid
jobs. They require external network access and must not be reported as passing
when skipped or unavailable.

## Observability and diagnosis

Windows-native runtime checks:

```powershell
$Listener = Get-NetTCPConnection -State Listen -LocalPort 8765 -ErrorAction Stop |
  Select-Object -First 1
$Runtime = Get-CimInstance Win32_Process -Filter "ProcessId=$($Listener.OwningProcess)"
$Runtime | Select-Object ProcessId, ExecutablePath, CommandLine
Invoke-RestMethod -Uri 'http://127.0.0.1:8765/api/health' -TimeoutSec 5 |
  ConvertTo-Json -Depth 5
Get-Content -LiteralPath "$env:LOCALAPPDATA\SynonBiomed\logs\8765-REVISION\stdout.log" -Tail 100
Get-Content -LiteralPath "$env:LOCALAPPDATA\SynonBiomed\logs\8765-REVISION\stderr.log" -Tail 100
```

If no listener exists, inspect the versioned stdout/stderr logs and the most
recent process exit before restarting. Never paste unredacted environment
variables, credentials, model payloads, or user state into an issue.

```bash
systemctl --user --no-pager --full status synon-go.service
journalctl --user -u synon-go.service --since today --no-pager
curl --noproxy '*' --fail --silent http://127.0.0.1:8765/api/health
synon-go doctor --json
synon-go model-smoke --plan --json
```

Never paste the secret vault, environment file, channel tokens, model keys, or unredacted payloads into logs or issues. Use structured doctor and smoke outputs, which are designed to redact credentials.

## Failure triage

| Symptom | Check | Corrective action |
|---|---|---|
| `localhost:8765` times out from Windows | Open `http://127.0.0.1:8765/`; verify `/api/health` on that address | Use numeric loopback; then inspect WSL forwarding only if `127.0.0.1` also fails |
| Login rejected | Auth environment, service environment, browser cookies | Correct the protected environment and restart; do not assume a package password |
| Runner partial/unavailable | Saved provider, credential reference, model smoke plan | Repair the workspace provider; do not switch production to `go_builtin` |
| Channel starts inbound only | Enabled adapter diagnostics and outbound credentials | Configure missing robot/card/token values and restart |
| Repeated IM response after crash | Delivery checkpoint and termination time | Treat as at-least-once replay; verify remote delivery before manual retry |
| Service cannot write a workspace | systemd `ReadWritePaths` and `SYNON_HOME` | Add only the required absolute path through a unit override |
| Release install rejected | Manifest, provenance, archive traversal, binary health | Do not bypass validation; obtain a clean archive |
| Optional kernel/MCP unavailable | Optional asset manifest and declared runtime | Install that sidecar runtime separately; core remains native |

### File-tool feedback and structured source inspection

OpenAI-compatible streamed function arguments retain complete assembly before
attempting compatibility recovery. Recovery recognizes root-level object
snapshots only; it cannot promote an element of an array or an object inside an
unfinished parent to the full call. Unresolved shapes use the existing private
protocol-feedback path so the model can repair the call before tool execution.
`provider_tool_arguments_recovered` and `provider_tool_arguments_unresolved`
diagnostics report assembly strategy, byte/fragment counts, short digest and
boundary classes, never argument content. These diagnose transport fidelity,
not research quality or task-completion eligibility.

If a streamed tool response has invalid arguments and has not emitted visible
assistant content, the adapter can retry the unchanged model request through
its existing non-streaming JSON transport before any tool executes. This is one
transport recovery path with the provider's normal bounded network retries,
not another agent loop or a prompt rewrite. Successful streaming responses and
visible-content responses are not replayed. Cancellation remains authoritative;
if recovery also fails, the original private protocol feedback remains available.
`provider_tool_argument_transport_recovery` records the outcome, and both requests
remain in the ordinary provider audit without persisting argument content.

The first feedback for an externalized typed HTML/search/record result uses
the same reading view as a later `read_file` call. The immutable original
JSON, source hash, byte count and outcome remain authoritative. The descriptor's
`preview` contains a serialized reading page with a fixed `source_version_id`
and, when partial, exact `read_with` continuation arguments. It does not turn
search snippets or an abstract-only page into full-text evidence.

Typed HTML source views also preserve publisher-declared bibliographic metadata
(Highwire/BE Press, Dublin Core, PRISM, Open Graph) and standard document links.
These are quoted source data, not verified citations or fetched full texts.
Links and metadata follow the unchanged body so existing continuation offsets
remain valid. Relative destinations resolve against the document's first base
URL; executable URLs, user-info credentials, scripts, asset preloads and unrelated
head metadata are not promoted. Links do not trigger downloads or bypass access
controls. Raw field selection, source bytes and hashes remain unchanged.

The engine retains the original-prefix requirement by default. A server-owned
preview validator may attest a derived view only by deterministically rebuilding
it from the identical original result. Shape, hash, size, outcome, UTF-8 and
transport-budget checks remain engine-owned. Runtime text and model output cannot
select a validator. Live and resumed runs preserve the same reading page;
unknown data keeps its original-prefix preview and normal source-reading handle.
The typed view uses the existing serialized inline window, not an additional
task-duration, model-token, source-count or publication gate. A display-budget
failure preserves the raw source and logs `runner_source_feedback_fallback`.

An autonomous working plan returns its generated step identities and exposes
the already-authorized `update_step_status` tool during the same execution.
The same `generate_plan` tool can revise an autonomous plan using complete
replacement content. Revisions remain versions of one artifact. Exactly unchanged
scoped steps keep IDs and progress; changed work and dependent later phases get
new IDs without inheriting completion. Removed-step notes and research navigation
remain stored under their old IDs and are returned as retired research rather
than silently dropped. Identical content reuses the current version, concurrent progress
updates are serialized with revision writes, and late source events cannot rewind
the plan. Restart restores autonomous mode and actual progress notes rather than
turning an existing plan into a user-approval request. Explicitly reviewed plans
cannot be changed through this autonomous path. Plan tracks do not themselves
spawn delegated agents; the existing `host.delegate` runtime owns child execution.
In autonomous mode, content accompanied by a redundant `approve` flag remains
an unreviewed working-plan operation; it never creates user approval. Approval-only
calls and explicit review-mode calls retain the real user-consent boundary.
This does not add tools outside the task's captured permission authority or
require plan checkboxes before completion. Progress notes can change without a
status transition; omitted notes retain prior notes, while an explicit empty
string clears them. Identical status and note updates remain idempotent.
`observations`, `source_refs` and `follow_ups` use the same preserve/clear
semantics. They are model-recorded navigation state, not source authority or a
completion score. Every current investigation retains its full description in
the returned state, and open follow-ups remain available to later investigation
or synthesis even when the working plan is revised.

Completed governed source receipts are reconstructed from the immutable
logical-task Transcript and associated with the investigation that was active
between progress transitions. A transition returns only newly observed source
receipts, the updated investigation and compact root-execution navigation;
its durable source-event watermark prevents the whole plan and prior receipts
from being repeated on every model round. Large-result references distinguish an
inline result, a preview with an immutable `read_with` handle, and a later
requested read. These are material-navigation facts, not a claim that the source
was substantively sufficient or that the investigation succeeded.

Root execution navigation returns every investigation that is already
`in_progress`; when none is active it returns only the first pending item in the
model-authored plan order. This prevents a report-writing track from becoming as
salient as an earlier unfinished acquisition track merely because both were put
in one phase. It does not reject another valid step ID, change the durable plan,
force a step to complete, or prevent explicit parallel delegation.

Successful source-tool evidence remains byte-for-byte independent of that state.
For the next model request only, the engine attaches a compact JSON navigation
object for the active investigation to the same tool-role result; durable
lifecycle events retain the exact original result. This keeps a newly read source adjacent to the investigation it informs
without adding a trailing assistant turn, injecting model-authored notes as
system policy, or altering durable evidence hashes. Resume attaches the same
state projection to an existing tool result. Neither path counts sources,
elapsed time, plan sections or tool calls, and neither blocks final completion;
the model remains responsible for deciding which discovery warrants follow-up.

Arrays of objects use a `json-record-directory-display-lines` overview before
their complete JSON view. Columns come from the data, not topic-specific key
rules. Small scalar cells appear directly; nested collections and long strings
show their type/size in the overview. This is navigation, not replacement data:
the original bytes follow, pagination remains available, and RFC 6901 pointers
such as `/29/abstract` retrieve the complete original value. A 256-byte overview
cell layout does not limit stored strings or their selectable content. Explicit
JSON selections retain their existing full-value behavior.

If a caller passes an ordinary artifact identity in `version_id`, a unique
first version can be resolved and pinned. `source_version_id` identifies the
actual immutable read. Multi-version identities return
`artifact_version_required` with a pinned `read_with`, rather than silently
following a moving head. Unauthorized and unknown identities disclose no
version metadata. These rules also apply to internal working-plan artifacts.

An unqualified `read_file` of large JSON can return a `json-field-preview`
view. Its sections contain exact JSON pointers, source sizes, inline values
or explicitly partial value previews, and executable `read_with` arguments.
The allocation uses the serialized tool-result transport budget, not source
importance. A preview is not a complete document read. Use `json_pointer`
to select a full field; `offset` and `limit` paginate that selected view.
Explicit line-window reads retain their existing behavior for generic JSON.
Stored source bytes and artifact hashes do not change when a view is rendered.
String previews contain decoded source characters, not a partial JSON string
literal escaped a second time. `size_bytes` remains the original serialized
source-field size; `value_complete` describes the shown value, and the full
selection remains available through its original handle.

Version-backed reads also return an absolute `file_path` usable by this task's
analysis runtimes. `file_path_scope: original_source` means the path contains
the entire original file, even when the displayed view selects a JSON field
or a line window. The path is resolved through the existing `host.artifact_path`
materialization implementation into the task's `.synon-artifacts` cache;
display filenames and `/api/artifacts/` links are not computation paths.
Local workspace reads return their authorized absolute path through the same
response field. Path metadata is budgeted before preview allocation.

The materializer verifies source size/hash and publishes a read-only cache
copy atomically. Missing or damaged cache copies are rebuilt from the same
immutable version; original artifacts and user files are not overwritten.
`kernel_artifact_cache_rebuilt` records repaired cache copies without source
contents or absolute workspace paths. Symlinks, hardlinks and path traversal
remain rejected by the existing secure workspace file authority. A cache
failure returns `file_path_error` and `file_path_error_code`, not a fictitious
path, while the verified original remains readable through normal selection
and paging. Source-integrity failures are not reported as valid evidence.

Focused materialization verification:

```bash
go test -race -timeout=0 ./internal/server -run 'TestWorkspaceVersionReadProvides|TestWorkspaceReadLocationIsUsable|TestWorkspaceVersionReadKeepsEvidence|TestKernelMaterialization|TestKernelHostInspectionScopesArtifactsFramesPathsAndLineage|TestAgentWorkspaceFileToolsIsolateProjectsArtifactsAndHostGrants|TestWorkspaceJSON|TestWorkspaceReadBudget|TestWorkspaceLargeJSONRead' -count=1
```

Successful `edit_file` receipts include a bounded `file_view` of the current
file while the edit's workspace lock is held. `shown_bytes`, `size_bytes`
and `truncated` distinguish a full view from a prefix; `read_with` retains
the normal file-reading entry point. If feedback cannot be read, the receipt
reports `file_view_status` without misreporting an already durable write as
failed. This feedback does not infer append intent, reject intentional
replacements, initiate model calls, or rewrite published artifacts. When
report sections disappear, compare the recorded replacement arguments,
resulting view and artifact hash before attributing the loss to storage.

### Public-source character decoding

Successful typed HTTP HTML results use `html-readable-display-lines` in
`read_file` unless an explicit `json_pointer` is supplied. This is a static
HTML reading view, not a summary, a visual browser or an assertion that a
paper was fully read. Standard HTML parsing removes head assets and executable
content while retaining body text, table cell boundaries/spans, text scripts,
MathML and resolved source links. It does not execute JavaScript, evaluate CSS,
select content by domain/topic, or replace the stored HTML. Image references
remain references; interpreting a figure still requires an appropriate tool.

The result includes `source_url`, `source_complete`, `source_size_bytes` and
`raw_read_with`. `source_complete` describes HTTP acquisition; `truncated`
describes this reading page. Keep the version and use `next_offset` for later
display lines. An explicit `/result/body` JSON selection accesses the original
HTML. The materialized `file_path` continues to contain the complete original
JSON, never the reading projection. Long titles paginate with the text instead
of filling fixed metadata and preventing access to the body. HTTP failures
are not promoted into readable evidence.

Normalized search results use `search-results-display-lines`: every source
retains its original order, title, URL, snippet and `/result/sources/N` pointer.
Duplicate legacy result arrays and per-record trace metadata remain available
through raw selection rather than being repeated in each view. Retrieval and
backend diagnostics remain in the paginated view. `source_state: discovered`
does not imply full-text acquisition or reading. Empty, untyped and non-discovery
JSON continues through the generic JSON reader.

Skill discovery exposes the enabled, policy-filtered catalog identity index
alongside lexical preview metadata. Neither lexical scores nor fixed research
phrases automatically load Skill bodies. Explicit selections, executed Skills
and durable implementation-specific dependency resolution retain their existing
authority. This changes capability discovery data, not the user's request,
model/system prompt, tool registry, research Skill bodies or publication policy.

Focused reading/discovery checks:

```bash
go test -race -timeout=0 ./internal/httptext ./internal/tools/webfetch -count=1
go test -race -timeout=0 ./internal/server -run 'TestWorkspaceHTTPDocument|TestWorkspaceDocument|TestWorkspaceSearchView|TestWorkspaceJSON|TestWorkspaceReadBudget|TestRuntimeSkill|TestRuntimeImplementation' -count=1
```

For read-only replay of captured HTTP/search blobs, set
`SYNON_TEST_SOURCE_BLOBS` to the platform path-list-separated, explicitly selected
blob paths and run `go test -v -timeout=0 ./internal/server -run
TestWorkspaceDocumentCapturedSourceReplay -count=1`. This reports presentation
sizes and pagination, not scientific correctness or new-task acceptance.

Web fetching and HTTP search decode text through `internal/httptext` before
source delivery or HTML parsing. HTML uses the standard BOM/header/meta
charset precedence from `golang.org/x/net/html/charset`; other text uses an
explicit HTTP charset or UTF-8. `web_fetch.bytesRead` counts received source
bytes, not the size of the decoded UTF-8 text. A decode error preserves the
received bytes in `rawBodyBase64` with an explicit recoverable source status;
an ASCII prefix must never stand in for the complete document. Transport
errors keep their original status. Binary downloads keep their dedicated
handoff and destination-security policy.

Search snippets and bounded file-line previews retain whole UTF-8 code
points. A truncated preview is still only a preview: stored source bytes
are unchanged. DuckDuckGo HTML title/snippet pairs are read from their
result container so a missing snippet cannot shift the next result's text.

Focused verification (not a substitute for the real research task):

```bash
go test -race -timeout=0 ./internal/tools/webfetch ./internal/tools/websearch -count=1
go test -race -timeout=0 ./internal/server -run 'TestWorkspace(BoundedLine|LongUnicodeLine|LargeJSONRead|ReadBudget|JSON)|TestNormalizedWebSearch|TestWebResearch' -count=1
```
