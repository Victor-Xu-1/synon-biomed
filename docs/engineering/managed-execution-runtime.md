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

## Runtime authority

| Module | Responsibility |
| --- | --- |
| `internal/server/agent_environment_management.go` | Exact model schemas, frame authority, foreground/background completion and notifications |
| `internal/server/agent_environment_resource_preflight.go` | Existing-environment reuse, task resource requirements, live CPU/memory/disk/accelerator snapshot and mutation admission |
| `internal/kernel/managed_environment.go` | Inventory, content-addressed generations, additive package resolution, health checks and atomic activation |
| `internal/server/agent_kernel.go` | Persistent Python/R/REPL/Bash execution, durable operation identity and restart recovery |
| `internal/server/agent_bash.go` | Bash envelope, exit receipt and package-manager denial |
| `internal/tools/shellops` | Shell safety checks and OS confinement primitives |

The compatibility-only pack executor used by specific internal product APIs is
not advertised to agents and is not part of this flow.

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

A failed tool call is retained as diagnostic evidence. The model may inspect
the selected Skill or documented interface and make at most one materially
corrected call. An equivalent second failure closes that target. Missing user
input, approval, credentials, or external state parks the task and waits; it
never starts an unbounded retry loop.

Closing a repeated target applies only to that operation path in the current
bounded execution unit. It does not terminate the logical task or consume a
task-wide repair budget. Recoverable interruptions persist the canonical input,
successful receipts, workspace state and checkpoint, then continue in another
execution unit without an attempt or age ceiling. Semantic no-progress uses a
capped scheduling backoff; execution-unit limits, transient transport retries
and recovery scanner page sizes are never interpreted as logical-task limits.
The recovery coordinator drains every deterministic scanner page so older
tasks and approved operations cannot be starved behind a busy first page.

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

## Acceptance

Changes to this flow require schema tests, manager mapping tests, immutable and
additive-resolution tests, foreground and background lifecycle tests, a real
kernel execution witness, a real Skill-script read-only mount witness, and an
isolated target-runtime task. Mock-only success is not sufficient.
