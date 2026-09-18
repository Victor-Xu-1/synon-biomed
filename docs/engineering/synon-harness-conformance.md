# Synon Harness conformance

## Single authorities

The public operation timeline follows
[`tool-stream-presentation-contract.md`](./tool-stream-presentation-contract.md).
That contract is the only grouping and typed-detail authority; raw protocol
batches and generic input/output fallbacks do not define the public layout.

- Model, direct HTTP, approved-resume and internal system tool calls enter one
  `internal/toolgateway` pipeline. Its immutable order is normalization,
  snapshot/schema admission, bounded pre-execution validation, durable failure budget,
  review scope, pre-tool hooks, post-hook revalidation, permission, source
  activity recording, execution, materialization, post-tool hooks and durable audit.
  Short-circuit outcomes may advance only to post-tool hooks or audit.
- Every registered Tool carries its runtime capabilities and one
  `direct`, `deferred`, or `hidden` exposure. The registration is the ceiling:
  a turn may narrow it, but a Skill or injected schema cannot promote it. One turn-scoped authority rejects
  conflicting duplicate schemas, builds the initial model projection, and
  admits later Skill/plan control exposure from that same snapshot. Task prose,
  plan titles, filenames, and recovery branches cannot register or promote a
  scientific executor.
- Every chat runner cycle enters one `internal/sessionrunner` machine:
  claim, recovery, context, snapshot, provider/tool rounds,
  verification, completion, paused or terminal settlement. Provider and tool
  may alternate; business code cannot jump backward to context or create a
  second recovery loop. Existing durable preparation and tool checkpoints
  carry the current lifecycle phase rather than publishing a competing event
  stream.
- `ask_user` exposes one direct question with 2–4 options. It is model-judged
  and used only for a material unresolved user-owned choice; it is never a
  mandatory root-task intake ritual. The first substantial compute-environment
  or scientific-engine setup is preflighted before mutation; when two or more
  live viable configurations have material trade-offs, the model asks once.
  A compatible ready environment and a still-valid answered route are reused,
  while routine reversible tool choices continue autonomously.
- `generate_plan` accepts phases, parallel delegations, desired outputs,
  feasibility, and approve-only textual approval. `update_step_status` is the
  only plan-progress mutation and supports `in_progress`, `completed`,
  `blocked`, and `skipped`. Explicit `plan_mode` is the sole mandatory-planning
  authority. It injects the plan rules and rejects at most three prose
  completions before allowing the bounded fallback, without manufacturing a
  provider-specific tool choice or token budget.
  A plan activates only its progress control; research, computation, docking,
  installation, and design Tools remain owned by their Tool/Skill capability
  contracts. Autonomous plans remain navigation: a pending step does not force
  a status-only model round, and one explicit completion may record its
  findings directly. A user-reviewed plan retains its explicit start and
  terminal transitions.
- `manage_environments` owns list/create/delete/register. `manage_packages`
  owns install/uninstall/list and immutable forks.
- Scientific execution uses persistent `python`, `r`, `bash`, and scoped
  `repl`. Remote compute is a distinct provider/job authority, not a second
  local package-management route.
- The scientific runtime keeps one Kernel Manager, one managed-environment
  Manager, one artifact/lineage authority and one Agent Engine. Their source is
  separated by catalog, execution, policy, settlement, mutation, validation,
  protocol, tool-batch and materialization responsibility; those files do not
  register alternate workers, package managers, completion decisions or model
  loops.
- REVIEWER and BOOKMARKER are fixed hidden jobs spawned by one completion
  checkpoint authority.

## Discovery and MCP

When the model judges a Skill useful, its discovery path is:

`search_skills → skill → repl/host.mcp`

That path is advisory and model-directed. An already advertised root tool is
not revoked merely because no Skill has been loaded, and loading one Skill does
not revoke other tools in the same immutable turn snapshot.
Deferred Tools become visible only when the selected Skill declares their exact
identity. Hidden operations never become model-visible, even if an older Skill
or restored schema still names one. Skill selection and a loaded Skill's next
executable action remain model-owned; metadata does not force a parallel
`Skill → connector → source` workflow. Tool capabilities, exposure and successful activation are retained in
runner checkpoints and compaction continuity so recovery cannot fall back to a
prompt-keyword route.

The first successful Skill invocation returns one complete authoritative
contract plus a bounded activation receipt. Repeating the same Skill with the
same arguments in the same logical task is idempotent: it returns a compact
`already_loaded`/`reused` receipt, does not rematerialize or repersist the
contract, and is restored from durable completed checkpoints after recovery.
`search_skills` annotates matches already active in the task while preserving
other candidates for later independent stages. A different Skill or materially
different arguments remain loadable, so loop prevention cannot freeze a
multi-stage workflow.

For an exact query whose Skill is already active, the model-facing search
result retains candidate identities but omits repeated previews and schemas.
Any tool round composed only of explicit `reused=true` receipts or typed
`synon.tool_effect.v1 state=unchanged` effects adds one short, private
convergence instruction to the next model
sample: continue from the successful evidence, choose a materially different
action only when needed, or finish. This instruction does not remove tools or
block a different call, and it is never projected into the conversation.
`idempotent=true` describes replay safety and is not by itself proof that the
current invocation made no progress.

The portable control-kernel catalog helper returns server-name strings from
`host.mcp.list_servers()` and method dictionaries from
`host.mcp.list_methods(server)`. The first output can therefore be passed to
the second without a private adapter or guessed object field.

`web_search` is a default canonical root tool for discovering current public
sources when an exact URL is unknown. `web_fetch` is the bounded reader for a
chosen URL. Both remain model-selected and independently executable; neither
is hidden behind Skill discovery or a task-keyword route.
Search-result locators are retained as provenance for the URLs they actually
return; deeper claims still require the bounded reader or another governed
source-evidence tool, so discovery is useful without becoming a hidden second
research path. Every source result also carries a bounded model-only material
projection with exact JSON locators, excerpt hashes and optional read
candidates. It remains in the Tool role. After compaction, one task-wide
projection is rebuilt from the immutable Transcript independently of plan-step
kind; its system fallback contains only explicitly untrusted source metadata,
identities and read handles, never raw source excerpts. The prompt snapshot
stores the exact prepared projection manifest rather than duplicating source
content.
Scholarly providers retain their complete returned abstract, DOI/PMID/PMCID,
publisher, journal, author and publication-date precision in the same search
result authority; the 600-byte snippet remains presentation only. A complete
provider abstract is recorded as `abstract_record`, while an ordinary result
snippet remains `discovered`. Optional publication bounds, newest-first order
and cursors are applied only by providers whose native contracts support them;
each provider reports applied and unsupported constraints, its own known total
and its opaque continuation. Candidate counts never claim exhaustive coverage.
`web_fetch` records requested and final URLs, redirect hops, retrieval time,
selected response headers and a hash of returned response bytes. HTTP `Date`,
`Last-Modified` and website chrome dates are not promoted to publication dates.
The existing immutable large-result and `read_file` authority presents HTML
publisher metadata and JATS abstract/body/sections/tables/captions/references;
the raw response remains independently readable and the view is rebuilt from
those exact bytes before entering model context.

The retained compound `web_research` operation remains available only to
explicit TaskRun and direct compatibility consumers. Its registration is
`hidden`; it cannot be advertised by a selected Skill. Historical continuation
records that point at it are translated at the compatibility boundary into one
observable `web_search` or `web_fetch` action before the outer Engine resumes.

The retired ToolSearch route and duplicate flattened MCP schemas are not
advertised to the scientific Session Runner. Connected methods resolve through
the live scoped `repl/host.mcp` bridge using the shared `mcpdirectory → mcpstdio`
pool, schema validation, approval policy, credentials,
timeouts, process containment, and audit trail. HTTP/internal compatibility
surfaces may still resolve an exact MCP method through that same pool, but they
are not a second model-visible connector path. Historical Skill and AskUser
names remain readable only at explicit inbound compatibility boundaries.

The Linux root-agent fixed set contains the same 28 conditional Harness tools
as the pinned reference inventory. Fixed jobs receive only their typed output
tool. Legacy aliases, `ToolSearch`, the retired software pack runtime,
top-level image/delegation shortcuts, and domain-specific download or docking
tools are absent from new model snapshots.

## Task lifecycle

1. A new root task starts directly unless the model identifies a material
   unresolved user-owned decision. An ordinary or fully specified task does
   not call `ask_user`; a first substantial environment or engine selection is
   an exception only when live preflight proves multiple viable configurations
   with material trade-offs and no still-valid prior answer exists.
2. The canonical task identity remains unchanged through planning, approval,
   execution, pause, restart, review, and continuation.
3. A plan may be approved by the UI or by an explicit user message. Text
   approval must be interpreted by the model and recorded with
   `generate_plan(approve=true)` before execution.
4. Every explicitly reviewed plan step must reach a terminal status before
   completion. Autonomous plan statuses remain durable navigation and do not
   become a second completion authority.
5. Repeated identical operations are bounded inside one execution unit;
   external blockers wait for user or external state instead of polling. The
   logical task itself has no attempt, continuation, age or duration ceiling:
   recoverable interruptions persist a checkpoint and resume with capped
   no-progress backoff. Recovery scans drain all deterministic pages, so a page
   size cannot starve older tasks. A quarantined Web projection is retried only
   after its canonical source coordinates, branch generation, or projector
   version changes, never because a timer elapsed over unchanged input.
   A missing model configuration is a typed waiting state, not a failed task:
   its exact runner checkpoint and resume dispatch remain parked without a
   timeout until model selection or explicit continue wakes the same task.
   Resumable and automatic are separate durable properties: the interruption
   payload records `auto_resume=false`, so ordinary lease recovery and global
   recovery scans cannot mistake a user-owned model wait for unattended work.
6. Diagnostic tasks preserve raw failure evidence and never force a pass.
7. `wait_for_notification` accepts an omitted display label and caps one wait
   at 1800 seconds; a timeout returns durable pending-work state so the model
   can decide whether to wait again, change course, or ask the user.
8. A new user message after a terminal runner attempt starts a fresh attempt
   and supersedes obsolete resume-dispatch records. A typed answer to an active
   `ask_user` checkpoint continues that same attempt instead.
9. A controlled interruption with a durable recovery contract keeps the same
   visible tool lifecycle open until its recovered terminal checkpoint arrives.
   Non-recoverable interruptions still settle unmatched tools explicitly, so
   the Web projection never invents a failure before a valid recovered result.
10. Python syntax is compiled before user code executes. A syntax defect returns
    a non-executing `code_preflight_required` correction and leaves the persistent
    kernel healthy instead of recording a failed scientific operation.
11. The stdlib-only `repl` rejects third-party imports before creating a kernel
    operation and returns `code_preflight_required`; scientific packages must
    run through `manage_environments` plus the managed `python` tool.
12. Approval, AskUser, plan and recovery input responses advance transport
    revisions without creating a new scientific-task boundary. Loaded Skills and
    verified receipts remain scoped to the latest canonical task intent until a
    genuinely newer user task begins. An explicit continue, repair or resume
    message reuses prior answered decisions instead of opening another
    AskUser round.
13. A live runner owns synchronous kernel policy admission. Kernel recovery may
    resolve pending approval only after that source runner is no longer live, so
    ordinary full-access calls never invalidate their own claim through a racing
    recovery worker.
14. Tool progress checkpoints are durable best-effort observability. Start and
    terminal checkpoints remain strict execution authority; one transient progress
    persistence error is logged and cannot interrupt the live tool.
15. The model may load a matching Skill when its instructions or an external
    schema are needed. Root tools remain governed by the exact advertised
    snapshot and their own typed input contracts; there is no Harness-imposed
    `search_skills → skill → tool` sequence.
16. A request sent with `plan_mode=true` keeps the ordinary model-visible tool
    set and model-judged AskUser behavior; it does not impose a fixed tool
    sequence, search count, or task classifier. If the model attempts to finish
    with prose while no matching approved plan exists, one bounded durable stop
    hook returns that candidate to the same turn for correction. Provider-supplied
    plan `version`, IDs, agent assignment and step status are stripped at
    admission because those fields are runtime-owned; the resulting nested v3
    plan still passes strict artifact validation.
17. Each claimed conversation is a Runner Pool isolation boundary. A local
    cycle failure may settle or recover that conversation, but it cannot cancel
    sibling workers or inject `context canceled` into unrelated long tasks.
    Only an unclaimed global infrastructure failure restarts the pool.
18. Recoverable tool protocol failures follow one model-visible path. An
    unadvertised name, invalid admitted arguments, or bounded pre-execution
    rejection closes the exact call ID with `executed=false`, records a failed
    durable lifecycle without allocating a kernel execution authority, and
    returns the diagnostic to the next model sample. The model may correct the
    call, select another advertised tool, or continue with available evidence;
    the Harness does not run hidden private repair rounds or quarantine the
    entire tool schema after one malformed call. The third rejection in the
    same tool-name/error-code family is returned as `retryable=false` with a
    directive to abandon that path for the current segment, so superficial
    argument changes cannot create an unbounded correction loop. Every
    `executed=false` result is also `ok=false`; a stale `edit_file` may use one
    successful `read_file` as inspection evidence and retry once, after which
    the same file/error family closes even if `old_string` keeps changing.
    A successful mutator that explicitly reports `changed=false`,
    `unchanged=true`, or only unchanged artifact versions does not advance the
    mutation epoch. A complete before/after file hash also ignores a
    timestamp-only rewrite; oversize, incomplete or dropped scans remain
    conservative. An explicit empty `files_written` runtime report does not
    reopen a failed artifact call, but it also does not classify the whole
    Python/R/REPL action as no-progress because kernel variables, stdout, jobs
    or other non-file effects may still be valid. Repeating an exact mutator
    that authoritatively reports no effect is rejected before execution as
    `repeated_non_progressing_tool_call`; a materially changed mutation or
    another capability remains available.
    A typed Tool effect records whether this invocation changed control,
    material, workspace or external state. Idempotency remains an independent
    property, so a safe first execution cannot be mislabeled as a no-progress
    replay.
    Provider adapters preserve a valid call ID/name whose argument payload is
    malformed by substituting a safe empty object plus an internal diagnostic;
    the Engine then returns `invalid_tool_arguments` to that call ID. Only a
    missing/duplicate call ID or similarly unpairable payload is a fatal model
    protocol invariant.
19. Non-executing Python/code preflight results are `ok=false`. Artifact save
    is partial only when at least one artifact succeeded; zero published
    artifacts plus structural validation errors is a hard model-visible
    failure. Claim-to-source binding and retrieval-depth gaps preserve the
    saved bytes and authored source identities, and return a non-blocking
    `evidence_binding_advisory` with machine-readable grading
    (`source_authenticity`, `retrieval_depth`, `source_tier`, and
    `semantic_support`) instead of `completion_pending`. The former
    `unsupported_evidence_references` identity is retained only as a legacy
    diagnostic field, not as the current warning verdict.
    A valid artifact version is retained before current-batch evidence and
    consistency feedback is returned. Each distinct finding set carries a
    stable `quality_snapshot_id`; whitespace-only rewrites, reordered findings
    and timestamp changes cannot manufacture a new issue, while changed source
    identity, field scope or value produces a new snapshot. A known DOI paired
    with the exact retrieved title of a different DOI is distinguished from an
    unknown claim binding. Neither case creates `completion_pending`.
    These advisories cannot open an inline repair window or force a source/edit
    route before terminal validation. One
    `synon.runner_recovery.v1` state retains the exact validator detail,
    required state transition, capability-selection flag and unchanged-repeat
    protection without generating domain-specific repair prose. A terminal
    `evidence_source_locator_missing` structural failure adds one bounded
    `synon.artifact-repair.v1` requirement containing the artifact path, row,
    accepted locator kinds and semantic-change rule. The outer gateway then
    advances through available artifact-read, source-discovery,
    source-locator-read, artifact-edit and publication capabilities. The model
    still owns every query, source choice and byte change; an unavailable source
    route advances to editing so the unsupported row can be removed rather than
    retried forever. When an inline
    draft warning is followed by a clean artifact save, the gateway selects
    `tool_choice=none` for the next round so the immutable completion validator
    rechecks the candidate before any additional provider-selected mutation.
    `none` is an Engine-enforced protocol state: that model request carries an
    empty Tool catalog, and a provider-emitted Tool call is discarded and
    privately resampled instead of reaching the gateway.
20. Durable correction checkpoints are Codex-style stop-hook feedback. They
    preserve the reason, affected state and acceptance condition and never
    classify task prose into a domain, filename, tool identity or task-specific
    workflow. A machine-owned structural condition may require the next generic
    capability transition, but the model chooses its arguments and scientific
    content from current evidence. Architecture tests reject task-specific
    routing strings and require graceful fallback when a capability is absent.
21. Scientific source and execution admission does not branch on pharmaceutical
    domain words such as formulation, pharmacokinetics, binding mode or
    docking. Source lineage activates from selected Skill/Tool capabilities or
    successful durable source receipts; model-selected computation and its real
    tool results determine the execution route. User prose, plan titles and
    filenames cannot activate a separate scientific path.
22. Skill discovery has one loading route. Explicitly selected Skills enter the
    initial turn context; a bounded deterministic task-text pre-scan may expose
    candidate names and descriptions, while only the model-selected `skill`
    call loads a body. Automatic Skill-body merging and competing prompt
    injection paths are absent. Onboarding Skill switches persist availability
    preferences only; they are not copied into a fixed per-conversation
    selection that would inject the entire enabled catalog before the first
    task.
23. Completion evidence follows the canonical task contract rather than a
    universal scientific gate. When the user explicitly limits both execution
    and the final response to named tool operations, only successful receipts
    for those exact operations can complete the task; a failed receipt or any
    unrequested tool keeps the contract unsatisfied. Merely using
    `search_skills` or `skill` inside a substantive scientific workflow never
    weakens its authoritative-source, governed-compute, artifact, or delivery
    requirements. Tool failures inside such workflows remain local evidence:
    the model observes the concrete failure, makes a materially different
    repair, and continues the same logical task from its durable checkpoint.
24. Plan progression preserves model action ownership. A request to start a
    later step may advance only plan control; its notes, observations, source
    references and follow-ups cannot be rebound to the currently active step.
    A request naming any currently actionable step resolves to that step's
    canonical ID and preserves its payload. When the model explicitly marks a
    research step complete, unobserved query languages and source-proposed
    follow-ups remain visible as `required=false` quality advisories; they do
    not reopen the step, force Tool choice, or enter protocol recovery. A new
    `deep` or `systematic` step with no bound source-reader receipt instead
    remains `in_progress` with a `synon.research-source-read.v1` capability
    requirement and bounded locators from its discovery receipts. Search
    snippets and search-returned abstract records remain discovery in this
    transition; the model selects which source to read, may bind an existing
    qualified receipt, and may truthfully mark the step blocked/skipped when no
    authoritative material is available. Previously completed legacy steps are
    not reopened retroactively. The same bounded entity and topic relevance
    matcher gates both generic Web reads and structured article records: a valid
    DOI/PMID/PMCID that resolves to an unrelated article remains a real source
    receipt but cannot satisfy the active investigation. Whole-file PHP/ERB
    script templates join Jinja, Mustache and format fields in the unresolved
    deliverable check; the check is scoped to the entire text artifact so a
    rendered report may still contain a fenced code example. Legacy continuations without an explicit flag retain their
    required semantics until executed or explicitly completed. Autonomous
    plans remain revisable navigation and progress data: a pending autonomous
    step does not force `update_step_status`; an explicit completion retains
    its model-authored findings without an administrative start round. Only a plan explicitly
    reviewed and approved by the user makes unfinished steps a
    final-completion blocker. `generate_plan(approve=true)` remains a pure
    control transition; plan-shape normalization cannot add draft content to
    that approve-only call.

## Artifacts and compute

- `save_artifacts` validates lineage, format and structured scientific
  references. Model-authored reports and readable deliverables retain citation
  diagnostics as quality grading without turning absence of a current-task
  binding into a scientific truth verdict or publication gate. Raw `.log`,
  `.stdout`, and `.stderr` execution evidence remains
  byte-faithful, requires an exact current-frame file-write path and SHA-256
  receipt, and is not reinterpreted as narrative citation text.
  The explicit `files` array owns the save set. Destination keys can recover an
  omitted compatibility file only when they are file-like; classifier labels
  such as `snapshot` and `working_data` never become file paths.
  `destination=snapshot` retains versions; `working_data` retains only the
  newest content while preserving pruned version metadata for lineage.
- An optional working-data document with schema
  `synon.artifact-table-contract.v1` declares one bounded record set,
  identity fields (including regimen/time when applicable), compared fields,
  and exact per-artifact header mappings. It lets differently labelled
  Markdown/CSV tables share an explicit comparison authority without adding
  internal columns to user output. Without this declaration, matching row
  counts or translated labels never establish row identity. Existing inferred
  table comparison remains a compatibility boundary for already matching
  schemas and is not extended with new scientific header aliases.
- The terminal completion gate compares the final response with the current
  produced-artifact set. It rejects missing referenced outputs, contradictory
  tables or environment claims, and a structured final identity that omits all
  matching authoritative source-artifact identities while substituting another
  identity in the same namespace. Explicit output-format aliases are
  canonicalized before staging and completion checks (`Markdown` and `.md`
  identify the same deliverable), so omitting a failed link from the final prose
  cannot turn requested working data into a completed delivery. Model-authored
  `artifact:` URI links are non-canonical presentation and reduce to their
  visible labels; only the immutable-version projector emits final download
  links. Explicit primary-plus-comparator reporting
  remains valid.
- Host and network access use explicit grants. Host deletion moves items to the
  system Trash and never performs a permanent delete.
- SSH command, SCP, durable submit/wait/cancel, task-wide concurrency, provider
  notes, and the scoped `host.compute` SDK share the same compute stores.
- Provider SDK and inference kernels run inside the same audited Linux
  confinement boundary with credentials delivered only over inherited file
  descriptors and egress restricted by an exact host/port proxy. Modal and
  SSH/Slurm submissions persist their input digest and recover pending/staging
  work idempotently after restart; ambiguous remote identities fail closed.
- Structured MCP results retain connector-canonical fields byte-for-byte;
  audit receipts never diverge from the source payload. The Python host bridge
  may add deterministic in-memory convenience fields such as component-derived
  gene aliases after the audited RPC completes; these aliases never alter the
  persisted evidence bytes.
- The Python bridge accepts common discovery spellings
  `host.mcp.list_servers()`/`list_methods()` by projecting the same cached,
  authority-scoped MCP catalog. Its bounded `read_file` compatibility module
  resolves immutable artifact versions through `host.artifact_path`; it cannot
  open an unapproved host path or bypass the 16 MiB text limit.
- Kernel source is immutable after durable admission. Python resolves an
  immutable artifact explicitly with `host.artifact_path`; R and Bash consume
  task-relative workspace files. Presentation markers and artifact display URLs
  remain byte-for-byte text when a report or data file writes them, so the
  runtime never rewrites approved source and invalidates its execution identity.
- The AutoDock Vina pack derives a co-crystal docking center from the verified
  reference-ligand coordinates when explicit center values are absent. This
  keeps receptor preparation, box derivation, docking, and validation in one
  governed path instead of ad-hoc parser cells. Its optional output directory
  remains task-relative and is rejected if it escapes the workspace.
- The canonical RDKit analog generator always preserves the verified parent's
  complete heavy-atom graph. The redundant model-authored required-core SMARTS
  input was removed, eliminating tautomer/aromaticity spelling failures without
  weakening the structural invariant. Its machine validation uses the shared
  top-level `overall_pass`, `errors`, and per-check boolean contract.
- The canonical Modal Skill contains the complete published environment set,
  including the pinned OpenMM/CUDA molecular-dynamics image; environments are
  definitions under the same Provider authority, not separate submission paths.
- The control-kernel SDK includes capabilities, credentials, read-only scoped
  SQL, execution inspection/interruption, findings, local stats, model
  endpoints, structured output, artifacts, lineage, delegation, Skills,
  agents, archive, MCP, and compute.
- Managed Python exposes the same local `host.view_image` path as the reference
  Harness. Live MCP App tools use `host.app` and the ticketed viewer broker;
  stable connector IDs and normalized names resolve through the same MCP pool,
  and editable Ketcher saves create ordinary immutable Artifact versions rather
  than a second persistence path.
- Bundled MCP subprocesses receive one host-resolved X.509 posture and optional
  CA bundle at the single stdio spawn boundary. The posture refreshes every six
  minutes and fails closed to strict mode on invalid overrides or detection
  errors.
- Explicit network grants reach analysis kernels only through the host CONNECT
  policy relay. The same configured upstream proxy is reused by server, MCP,
  and kernel egress; direct kernel sockets remain confined.
  Full-access tasks grant public HTTPS destinations without the restricted-mode
  built-in domain deny defaults. Explicit operator denies, destination address
  validation, TLS verification and process isolation still apply. Closing the
  proxy releases both sides of active transfers, including stalled connections.
  Public file transfers have no fixed product-sized byte ceiling or total
  wall-clock deadline. Streaming disk checks retain a safety reserve and
  account for staging and publication copies; unknown content length is not
  treated as an infinite storage request. Atomic staging, validator-bound
  Range/If-Range resume, finite retries and SHA-256 verification remain in the
  same transfer path. Waiting for another writer is cancellable.
  Default R environment repair uses the existing installer supervisor instead
  of a separate wall-clock-limited process path. Native subprocesses inherit
  slow-network defaults: pip uses a socket-idle timeout, mamba's minimum-rate
  rejection is disabled, and R uses its maximum representable positive timeout
  (R does not accept zero or infinity). Explicit runtime configuration still
  takes precedence. These defaults do not rewrite task-authored timeouts or
  claim resumability for third-party code that does not implement it.

Execution effect preparation and registered-entrypoint admission use the same
Shell grammar. A static single-command argv may contain comments, continued
lines and trailing newlines; substitutions, redirections, pipelines and extra
commands cannot inherit that entrypoint's authority. Runtime-owned arguments
are appended to the parsed argv and quoted as data, not concatenated after a
comment. Native R parsing accepts omitted call arguments and array indices;
partial parse failures retain witnessed effects and explicit uncertainty.
Python API witnesses use native from-import semantics for submodules and do
not treat scoped/optional imports or rebound names as proven missing APIs.
Import initialization failures remain unresolved, distinct from absence.

## Streaming and user visibility

Provider content is persisted incrementally. Private reasoning is never shown;
a reasoning-only interval produces one localized activity state. Waiting for a
question, plan decision, permission, background completion, model selection,
failure, and success remain distinct. While a plan is awaiting a decision, the
composer stays available for approval, questions, or requested revisions.
Older transcript pages, delayed history-rebase events, and remote replayed user
rows never override explicit user scrolling; only the local optimistic user
row can force a post-submit jump to the bottom. Conversation detail caches are
scoped by authenticated owner as well as conversation ID.
Approval cards project only scopes accepted by the matching backend request
kind. Agent-tool approvals expose `once` and, when rememberable, `always`;
one-shot requests cannot select a persistent scope, and denial is normalized
as a scope-free one-shot decision rather than a grant.

External Skill catalogs load in parallel with a bounded per-source deadline.
If every source is unavailable, the market reaches a stable retryable state
instead of serially waiting or showing a false empty catalog. MCP Registry
search uses one same-origin backend proxy with bounded query, timeout, response
size, status, and JSON validation.

The stable frontend IPC export is a facade over responsibility-specific
conversation, runtime, file, provider, database, preview and operations
clients. Conversation rendering delegates draft/mobile composer state and
message projection to independent controllers; runtime operations remain one
controller with separate plan/review presenters. The durable stdout store is
owned under the runtime service directory and is not reimplemented in a view.
Composer text, local/workspace files, exact generated-artifact versions,
fixed Skills and turn-scoped MCP connectors are one typed per-conversation
draft. Unsent drafts are versioned and isolated by authenticated owner and
conversation so reloads and A-to-B-to-A navigation restore only the matching
identity set. Direct send, queue, retry, `@` selection, `/Skill` selection and
artifact “add to chat” preserve that same identity set; filenames or rendered
Markdown never substitute for an artifact/version or connector ID.

## Product identity

Runtime prompts, model-visible Skills, UI, logs, health output, and generated
artifacts identify only Synon Biomed. Historical protocol identifiers may
remain only where required to read or safely reap pre-migration state; new
resources use Synon names.

## Verification boundary

Contract, persistence, kernel-host, confined provider proxy, SSH/Slurm,
approval-resume, plan approval, artifact-retention and source-routing behavior
are verified by their corresponding executable tests. Local protocol tests do
not establish successful calls to credentialed external models or compute
accounts. Release requirements are defined in the
[operator contract](../release-acceptance-contract.md); each release must carry
evidence for its exact source revision and applicable external prerequisites.
