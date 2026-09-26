# Managed scientific execution

## Purpose

Synon Biomed exposes one model-facing software flow:

1. preflight the proposed dependency set and resource shape with
   `manage_environments(mode="preflight", ...)` or
   `manage_packages(mode="preflight", ...)`;
2. reuse the compatible environment returned by that inventory, or create one
   with `manage_environments(mode="create", ...)`;
3. add a missing dependency only with
   `manage_packages(mode="install", ...)`;
4. run `python`, `r`, or `bash` with the returned environment name.

Preflight ranks compatible generations deterministically and returns one
`recommended_environment` with the exact matched package versions. Only a
bounded alternative-name window enters model context; the full local catalog
count remains available without serializing every environment name. Reuse is a
feasible route even when a new local mutation would be blocked by disk or other
resource constraints.

The preflight result also declares `setup_state`, `reuse_preferred`, and
`requires_new_environment`. A compatible ready environment is the default
continuation and does not reopen an earlier route decision. Before the first
substantial compute environment or scientific engine is configured, the model
asks once only when live preflight leaves multiple viable configurations with
material scientific, resource, cost, data-boundary, or deliverable trade-offs.
Small reversible tool choices and a single valid route continue without an
intake ritual. A later task reuses the compatible verified environment unless
the user requests a change or new evidence makes it incompatible.

Create and install remain safe when the model omits an explicit preflight call:
the server runs the same inventory and machine-resource check internally before
either mutation. A resource mismatch returns a decision request without
starting an installer.

No other model-visible installer, package resolver, arbitrary execution pack,
or shell installation path exists.

## Installation readiness and reuse

The service-owned scientific bootstrap has one authority for the required
`synon-biomed-python` and `synon-biomed-r` runtimes. It resolves the managed
root from the current user's `SYNON_HOME` (falling back to the platform user
data directory), stores active pointers below `conda/envs/`, and stores
content-addressed generations below `conda/envs/.generations/`. The root is
configuration-derived; no developer checkout path is part of the contract.
Both runtimes are provisioned from verified catalog/explicit-lock assets at
startup, smoke-tested, and then made available to every later task. A later
task or service restart inventories and verifies the active generation first,
so it reuses the existing installation rather than downloading a duplicate.
Optional warmups use the same managed environment supervisor and root but are
opt-in selections.

An installer transaction is successful only when process creation and every
package link-script exit succeed. The controlled native asset records its
official source revision, narrow source change, locked build dependencies,
licenses and checksum under `assets/optional/micromamba/`. Package scripts are
never replayed manually by a parallel repair path. Package inventory consumes
the pinned installer's structured `packages` envelope, not console text.

Publication validates the actual interpreter and requested runtime imports for
Python and R. R inventory derives namespaces from package DESCRIPTION paths
and data-package link metadata, verifies the physical payload, and loads the
required namespaces in that environment. Task text and scientific engine names
do not participate. Import output is bounded; third-party messages or arbitrary
JSON cannot substitute for a successful verifier exit and its own receipt.
Interpreter launchers and package hooks retain normalized absolute OS utility
search paths; caller secrets and user R libraries are not inherited.

Receipts carry an installation-validation revision, which also changes the
immutable Conda generation identity. Legacy hook-dependent generations remain
on disk for evidence and existing workers, but are not advertised or recovered
as verified environments. The existing preparation/publication authority
creates a new generation and atomically activates it after all checks pass.
Failures and cancellation never activate staging content. Restart recovery
revalidates the published receipt instead of rerunning completed installation.
Health-cache identity includes prefix, language, resolved packages and import
witnesses; a weaker check cannot satisfy a stronger request. Reuse validates
the current request's imports before returning success.

## Runtime authority

| Module | Responsibility |
| --- | --- |
| `internal/server/agent_environment_management.go` | Exact model schemas, frame authority, resource and implementation admission |
| `internal/server/agent_environment_resource_preflight.go` | Existing-environment reuse, task resource requirements, live CPU/memory/disk/accelerator snapshot and mutation admission |
| `internal/kernel/managed_environment.go` | Inventory, content-addressed generations, additive package resolution, health checks and atomic activation |
| `internal/server/agent_kernel.go` | Persistent Python/R/REPL/Bash execution, durable operation identity and restart recovery |
| `internal/server/managed_operation.go` | Typed environment-operation adapters and task-owned cancellation through the existing durable outbox |
| `internal/server/agent_bash.go` | Bash envelope, exit receipt and package-manager denial |
| `internal/tools/shellops` | Shell safety checks and OS confinement primitives |

The compatibility-only pack executor used by specific internal product APIs is
not advertised to agents and is not part of this flow.

### Process lifecycle and recovery

Linux process inspection has one identity and membership implementation shared
by cancellation, resource observation and detached-executor recovery. A PID and
matching start time are necessary but not sufficient for liveness: an exited,
unreaped process cannot retain execution ownership even if its last heartbeat
is recent. A stopped but still-live process is not treated as an exited one.
Worker identity is captured at launch and cannot silently rebind to a different
process when inspected later.

Cancellation is bound before process launch starts the runtime's context
watcher; it is never replaced after launch. On Linux a publication barrier
keeps cancellation from observing an unfinished launch identity. Windows keeps
the existing Job Object supervisor's pre-launch cancellation hook, including
root termination when cancellation precedes Job assignment.

Interrupt and forced termination use the same signal path. It discovers children
of every worker thread, including descendants that create another process group
or session; child identities are rechecked before signals are delivered. An
ordinary interrupt preserves sandbox supervisors so a cooperative interpreter
can return its cancellation receipt and keep its existing variables. Existing
timeout/grace handling owns escalation when the interpreter cannot settle.
Observation display and traversal limits do not truncate cancellation targets.
Unrelated workers are not members of the target process tree or process group.
Only the contiguous sandbox-launcher ancestry receives supervisor treatment;
an ordinary child does not gain signal protection by using the same name.

## Environment invariants

- Existing-environment inventory and a live machine-resource snapshot precede
  every create or install mutation, including direct mutation calls.
- A compatible ready generation is reused without invoking an installer.
- An infeasible local request returns `resource_choice_required`; it does not
  install, silently reduce the requested workload, or select a remote provider.
- Names and package specifications are bounded and validated.
- Installation is immutable: a new generation is verified before activation.
- Package installation is additive. Existing resolved package records must be
  byte-for-byte preserved; incompatible version changes require a new
  environment.
- Conda mutation uses `--freeze-installed`; pip resolution is checked after
  installation before publication.
- A failed staging generation is never activated.
- Every execution call names its environment explicitly.
- Python source imports and accessed imported attributes are checked against
  that exact environment before the public tool-start boundary. Deterministic
  API drift therefore enters the private correction turn rather than becoming
  a failed execution. The verifier derives targets from Python syntax and the
  live environment; no package- or task-specific removed-API blacklist exists.
- Bash cannot invoke pip, conda, mamba, micromamba, apt, brew,
  `install.packages`, or equivalent mutation commands.

## Failure and retry contract

### Persistence contention and task lifetime

SQLite `BUSY` and `LOCKED` (including their extended driver codes) retain their
transient classification. They are not schema-loss or stale-owner evidence.
Admission retries remain local to the scheduler worker with capped, cancellable
backoff. Claimed task errors do not cancel unrelated conversations. A genuine
supervisor interruption preserves its typed cause and resumes the same durable
attempt instead of publishing a business cancellation.

Lease checks use the clock after acquiring the database write authority, not
before waiting for it. Runner heartbeat retries stay inside the current lease
and a bounded renewal deadline; they do not mutate the claim shared with other
goroutines. Losing the lease never grants an old worker permission to settle a
successor's task. Database and context failures remain distinguishable from
actual ownership loss.

The detached executor reconciles canonical frame/root terminal state and
incarnation through the existing cancellation ledger. Completed, failed,
cancelled and replaced tasks retire only their own backend generation; waiting
for user input or recoverable runner interruption does not retire a live task.
Reconciliation includes accepted work not yet present in the process-local
active map. Dispatch is fenced before queued cancellation, acknowledgements
precede backend retirement, and late result settlement never rewrites the
original task failure. A controller restart does not erase this obligation.
Acknowledged requests remain in the reconciliation inventory until their result
receipt commits; a lock or crash between those writes cannot strand queued work.

Cancellation and outcome persistence serialize at the executor owner. A
provider-level cancellation is recorded as `provider_cancel`, with no invented
operating-system termination signal. Native signal receipts retain their actual
signal. Both use the same terminal writer, recovery consumer and cancellation
ledger; there is no alternate task-cleanup executor.
For containers, task cancellation wins over simultaneous executor detachment.
The provider must verify the same container's physical terminal state after
stopping it. Unavailable control or unconfirmed exit retains recovery evidence
and is never converted into a successful cancellation receipt.

Workspace migration 68 expands the internal cancellation constraint using the
existing transactional migration authority. It preserves execution rows,
terminal receipts, payload/identity fences and foreign-key relationships.
Migration failure rolls back the schema and journal together. Older binaries
cannot open the upgraded schema: rollback requires the pre-upgrade database
backup and matching binary, with explicit reconciliation of any later writes.

A failed tool call remains diagnostic evidence. The existing tool gateway owns
retry admission and recovery identity; execution receipts do not add another
failure-budget authority. Kernel read-open witnesses are bounded dependency
telemetry, not proof of full content reading or scientific evidence. Python
cells report observed task-file opens, including failed open attempts. Opaque
native and subprocess reads are not inferred from program text. Adapters without
observations do not claim complete dependency coverage. Missing user input,
approval or credentials remains an explicit blocker.

Closing a repeated target applies only to that operation path in the current
bounded execution unit. It does not terminate the logical task or consume a
task-wide repair budget. Recoverable interruptions persist the canonical input,
successful receipts, workspace state and checkpoint, then continue in another
execution unit without an attempt or age ceiling. Semantic no-progress uses a
capped scheduling backoff; execution-unit limits, transient transport retries
and recovery scanner page sizes are never interpreted as logical-task limits.
The recovery coordinator drains every deterministic scanner page so older
tasks and approved operations cannot be starved behind a busy first page.

## Physical execution and background operations

### Bounded memory, continuous execution

On the Linux detached executor, cgroup v2 and systemd 254 or newer are required
for delegated workload placement. The single executor service retains its
admitted `MemoryMax` and bounded swap allowance. Its `control` subgroup owns
supervision and receipt persistence; the worker and its descendants are placed
atomically in the sibling `workload` subgroup through the existing process
launcher. The workload hard ceiling leaves 128 MiB inside the same total budget
for supervision. Only the workload initially receives the 85% soft threshold.
Unsupported delegation fails launch explicitly; it does not select another
launcher or take control of the caller's cgroup.

The executor samples workload memory, full memory-stall time, user CPU time and
its active execution's output sequence every five seconds. A bounded thirty-second
window with at least 80% full memory-stall time and no useful CPU or output
progress permits one relaxation of `memory.high`, no further than the already-
admitted workload hard ceiling. Brief reclaim fluctuations do not restart this
lossless-relief window; separate windows never accumulate isolated peaks.
Relief is written and verified by the existing supervisor without forking a
control command into the pressured workload. Destructive recovery retains a
stricter two-minute consecutive severe-stall window before the same execution
handle requests recovery through the normal worker owner. Neither is a task-
duration limit.
Productive silent computation, output progress, transient peaks, stale samples,
counter resets and unavailable telemetry do not consume a recovery window.

Resource recovery settles only after the physical worker has exited. Captured
output and completed workspace files remain available; interpreter memory is
explicitly lost, not described as a resumable memory snapshot. The existing
durable result records `resource_pressure`, and the shared result adapter emits
`kernel_memory_pressure`, `recoverable: true` and `retry_unchanged: false` for
Python, R and Bash. Existing tool-admission consumers own subsequent retry
decisions; the resource supervisor does not start a replacement computation.
No task-wide failure or duration ceiling is introduced.
Checkpoints must have been written by the workload; the runtime cannot recreate
unwritten scientific state or correct an algorithm on the model's behalf.

The existing compute observation exposes optional resource measurements bound
to the same execution identity and sampling time as its process tree. Stale,
unbound or completed observations do not retain a current pressure label. The
panel shows observed group usage, its hard budget and memory-stall ratio, not a
predicted completion time or per-process RSS. An embedded worker can share its
containing resource group with other processes; its measurements do not imply
exclusive ownership or authority to change that group. This native Linux mechanism does not claim pressure
control of container-provider or non-Linux resource domains.

The detached executor retires its backend when the physical worker exits, even
if the control socket remains reachable. Outstanding receipts drain through the
existing settlement authority before a successor can take ownership. Replacement
generations report interpreter-memory reset; durable files are not discarded or
represented as reconstructed variables.

Environment and container preparation share typed operation adapters for both
foreground and background execution. Background admission commits an immutable,
owner- and frame-incarnation-bound request before returning its notification ID.
The existing outbox dispatcher renews its claim for healthy long operations;
claim loss or task cancellation cancels the operation context. Resume does not
re-authorize a request cancelled earlier. Result notification and claim settlement
commit atomically, and an expired or stale owner cannot start or publish work.
Queue claims and side-effect admission are recorded separately. One claim can
admit its operation once; future claims reconcile domain receipts. Immutable
installer and registration receipts are checked before target-existence checks
or preparation side effects. Deactivation atomically moves the admitted active
pointer into an operation-bound receipt, so retrying an old deletion cannot
deactivate a later reactivation, even of the same content generation. Terminal
cancellation waits for the last owned installer's cleanup; a new waiter never
joins a cancelled, draining operation. Shared work with other live waiters is
not killed when one waiter leaves.

For transcript-backed calls, admission creates the existing progress observer
and the durable operation in one SQLite transaction. Observation identity does
not contain a runner claim token. Progress writes validate the current outbox
lease and derive their sequence from retained events; previous owners cannot
overwrite successor progress. Terminal progress, result notification and claim
settlement commit or roll back together. The same public history row and event
API display the lifecycle without a second execution or publication route.

Service drain stops the dispatcher and its local work but leaves admitted
operations recoverable; it is not user cancellation. A new service continues
the durable observation sequence and reconciles domain receipts. Legacy
boot-bound observers without a durable owner retain only their existing
unknown-outcome recovery; that recovery neither executes work nor applies to
new durable operations. Progress writes are bounded and best-effort, while
terminal observation remains part of authoritative result settlement.

Foreground results, background notifications and history reconstruction use
one result-binding consumer. The durable receipt carries the source/target
environment relationship and implementation identity, not a second controller.

Environment binding recovery publishes readiness only after the complete read
has succeeded. Concurrent consumers wait for the same attempt; failures remain
retryable and are returned to the caller rather than cached as an empty success.

## Durable public-file acquisition

Public scientific files have one acquisition authority: the dedicated public
file download tool. Bash transfer clients are deferred before execution so
they cannot create a second lifecycle that bypasses provenance, persistence,
or recovery.

- Partial bytes and a digest-only request identity are kept in private runtime
  state, outside the task workspace and user-visible artifact projections.
- A canceled call or service restart closes the active connection but retains
  synchronized partial bytes. The next logically identical call resumes from
  the durable byte count only when a strong ETag or valid Last-Modified value,
  `If-Range`, and the returned `Content-Range` agree.
- If the remote server ignores the range, changes the validator, or cannot
  prove byte identity, the same authority truncates its private partial and
  safely restarts from byte zero. It never appends uncertain bytes.
- Large transfers have no wall-clock completion deadline. A transfer is
  considered unhealthy only when its body produces no bytes for the configured
  idle interval or the task is explicitly canceled.
- Completed content is hashed and validated before the existing atomic
  artifact/workspace publication path runs. The private partial is removed
  only after publication succeeds.
- The operator-selected network proxy is passed into this authority explicitly;
  it does not independently read mutable process proxy variables.

## Compute process observation

The existing kernel resource inventory is the sole process-observation source.
`GET /api/kernels` and the frame-scoped kernel inventory may include
`execution_observation` on each authorized kernel:

- `execution_id` binds a sample to `current_cell_tag`;
- `sampled_at` is the UTC resource-sample time;
- `status` is `observed`, `partial`, or `unavailable`;
- `processes` contains PID, parent PID, process-start identity, program basename,
  name source (`executable` or `process_name`), and OS process state. Arguments,
  environment values, and executable paths are not included.

On Linux, one bounded process-tree walk supplies both resource counters and
program names. It visits children of all worker threads, validates parent/start
identities, fences root PID reuse, and reports partial observation when a process
vanishes, inspection is denied, or limits are reached. At most 64 process records
are returned; traversal has a 4096-process/thread budget. Names obtained only
from process metadata are explicitly distinguished from executable names.
Other platforms currently report unavailable process observation rather than
inferring programs from submitted code or environment names.

Local workers retain the PID start identity captured before their reaper starts.
Cell transitions, generation changes, closing workers, and reaped processes
invalidate attribution during sampling. Detached workers use their persisted
PID identity and revalidate the monotonic execution receipt after sampling.
Observation cannot create, restart, cancel, or otherwise control an execution.

The compute panel displays distinct observed leaf programs, with the complete
sampled tree available in details. The environment and submitted source are
separate labels, never substitutes for the running program. Samples older than
15 seconds, more than 5 seconds in the future, or belonging to a different cell
are not presented as current. Missing data and incomplete samples remain
explicit; an observation failure does not stop the underlying computation.

The focused browser regression starts an isolated SQLite store, real confined
kernel and HTTP API, and the production compute panel in project Playwright.
It requires Linux, Python, Node, installed frontend dependencies, and the
Playwright browser matching the lockfile. It never uses an installed service,
user account, model configuration or browser profile:

```sh
SYNON_TEST_EXECUTION_OBSERVATION_BROWSER=1 go test ./internal/server \
  -run '^TestKernelExecutionObservationRealBrowser$' -count=1 -v -timeout=180s
```

Screenshots are written outside the repository to a temporary directory, or to
the existing directory selected by `SYNON_OBSERVATION_TEST_ARTIFACTS`.

## Acceptance

### Execution preparation and startup ownership

`internal/executionprep` is the single static effect-preparation authority used
before tool admission and immediately before execution. It parses Bash grammar
and native Python/R syntax without evaluating task source. Resolved subprocesses,
embedded-language calls and workspace script entries use the same bounded plan.
Script reads are confined to the task root and carry content digests; each check
reads the current body rather than trusting a stale path-only decision.

Known package mutations use the existing managed environment authority. Known
public file acquisitions use the existing durable download authority, including
its response/status/content checks and atomic publication. Ordinary computation,
HTTP API requests, authenticated transfers and local-service requests are not
blanket-disabled. A static plan cannot prove arbitrary dynamic source behavior:
unresolved parsers, dynamic entries and analysis-budget exhaustion are unknown,
not successful verification and not reasons to invent or execute a replacement
scientific workflow. Runtime confinement and verified environment publication
remain authoritative.

Bash's protocol supervisor uses the configured control Python while child
commands use the selected environment's executable path. A native-only R
environment therefore does not need Python merely to run `Rscript` via Bash.
Python analysis itself still requires Python in the selected environment.

Detached executors claim their PID/start identity before runtime discovery.
Startup failures atomically record a generation-fenced stage receipt and a
terminal backend state (workspace schema 69). An elapsed readiness wait is not
evidence of process death. A replacement must wait for the existing physical
owner to release the session; unstarted and previously ready backends share the
same terminal-to-starting recreation path. Startup receipts contain no user
source, process arguments, environment values or credentials.

Focused regressions:

```sh
go test ./internal/kernel -run '^TestExecutionPreparation|^TestBashSessionSeparates' -count=1
go test ./internal/persistence/workspace -run '^TestKernelStartup|^TestKernelExecutionBackend' -count=1
go test ./internal/kernel/detached -run '^TestExecutorStartupFailure|^TestStartingLiveExecutor|^TestPredecessorBackend' -count=1
```

### Installation phase closure

Creation, immutable package mutation, and registered-environment forks execute
pip installation stages through one planner and supervised process boundary.
Only declared stages require their installer runtime: native-only R creation
does not require Python, while a mixed runtime provisions the Python/pip stage
dependencies before execution. Adding a pip stage to an existing native runtime
provisions missing dependencies in the unpublished successor, preserving source
interpreter pins and the additive-resolution check. A stage failure or cancellation
cannot dispatch the next stage or publish a ready generation.

All pip stages use the target prefix's executable search path. A successful
Conda generation binds ordered pip replay phases, including their package
sources and build options, to its generation digest. Later immutable mutations
replay those phases against the recorded package versions before installing the
new request. This preserves distinct wheel/index sources across successive
installs; a change to a saved source invalidates the generation receipt.
Generations created before replay phases were recorded retain their package
inventory and active pointer. During the first mutation, their pinned pip
packages are restored with caller-supplied source hints. If a source build
needs a package already present in that inventory, restoration installs that
provider first and records the successful phase order in the successor. An
unavailable source or ambiguous build provider fails without publishing a new
generation. Registered forks still use their existing separately validated
installation path.
Pip uninstall removes the requested distribution from future replay phases and
verifies its absence in the successor before activation. A retained package
that pulls the removed distribution back causes the mutation to fail while the
old active generation remains available.
Mutation preflight obtains the execution language from the source environment;
the package manager chosen for a stage does not redefine that language.

R package witnesses distinguish installed package metadata (`Meta/package.rds`)
from resource-only DESCRIPTION trees. Declared but missing installed metadata is
an error, and selected packages still undergo a real namespace-load witness.

Progress milestones are displayed as step counts, not estimated overall work
percentages. Only observed counters produce percentages. Terminal tool results
replace live phase presentation and expose redacted terminal diagnostics.

Focused real creation and presentation checks (isolated from running tasks):

```sh
SYNON_TEST_REAL_R_CREATE=1 go test ./internal/kernel \
  -run '^TestManagedEnvironmentRealRCreatePublishExecute$' -count=1 -v -timeout=10m
cd frontend
node tests/web-e2e/toolPhaseLifecycle.browser.mjs
```

The R check downloads packages into disposable state, invokes the public creation
boundary, executes a real worker request, and verifies generation reuse after
manager restart. The browser check uses the production component with controlled
state transitions; it does not start a model or scientific task.

Changes to this flow require schema tests, manager mapping tests, immutable and
additive-resolution tests, foreground and background lifecycle tests, a real
kernel execution witness, a real Skill-script read-only mount witness, and an
isolated target-runtime task. Mock-only success is not sufficient.
