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
  Implementation evidence checks apply to typed implementation choices, not
  unrelated input or parameter questions while an environment choice is
  pending. Two evidence-backed alternatives are sufficient; no extra option
  quota requires inventing an engine. Answering an input question does not
  authorize installation or resolve a pending implementation choice.
  Environment resource preflight binds categorical requirements to the exact
  implementation's catalog Skill, not the union of previously loaded Skills.
  Its `capability_contract_skill` names that authority; an empty value means
  no implementation capability contract was established. Unknown or composite
  names do not inherit capabilities from task history. Host feasibility does
  not attest scientific readiness, select an implementation, or remove any
  outstanding task-level capability or downstream evidence requirement.
  Registry singleton selection requires one exact primary route covering all
  required scientific capabilities. Coverage follows the primary pack and its
  registered controlled-input resolvers; alternatives for the same input group
  contribute their common capabilities, never the union of mutually exclusive
  routes. Selecting the primary preserves every task requirement and does not
  authorize an auxiliary execution. Its input-source choice, receipt and pack
  validation still apply. Unlinked, unselected transitive or ambiguous pack
  identities cannot be invented into a complete route. Multiple complete routes
  still require the existing selection decision; discovery is not readiness or
  execution evidence.
  When full coverage includes an auxiliary stage, unnamed environment requests
  retain the existing exact-implementation requirement; they cannot silently
  provision the primary environment. Primary selection reports the outstanding
  capabilities and available resolver choices separately from authorization.
  The same registered route coverage validates primary options when multiple
  routes exist; their individual preflight and choice requirements remain.
  A registry-bound auxiliary resolver uses only the typed `evidence_resolver`
  selection. An existing typed group/Skill/implementation tuple is matched
  directly; conflicting or malformed tuples cannot be rewritten from prose,
  and exact tuples do not need an engine-name mention in their display text.
  Checkpoint and execution normalization remove both public and
  nested primary-selection fields from that option so re-parsing cannot turn
  it back into a primary engine choice. An explicit implementation takes
  precedence over comparative prose; an unregistered composite or version is
  not silently rebound to an engine mentioned inside its name or description.
  Primary and auxiliary selections share one event-ordered replay scope. A
  changed primary retires older auxiliary selections from active authority,
  not from history; re-confirming the same primary preserves them. Current
  primary selection supersedes loaded-Skill discovery history. When only an
  answered resolver remains in bounded replay, its exact, uniquely registered
  parent can be restored; ambiguous parentage cannot select an engine, and
  catalog-wide recovery cannot override an existing primary selection.
  The accepted closed-option label can restore absent implementation metadata
  only through exact registered identities and declared primary/resolver edges.
  Unknown labels, independent-engine composites and free-text answers do not
  select a route. Explicit typed identities and resolver tuples take precedence.
  Answer acceptance and fenced typed-answer replay use the same reconciler;
  historical question and answer records are never rewritten.
  Concurrent answers for one controlled input group cannot authorize conflicting
  resolver routes. The shared continuation constraint is checked before the
  response batch writes grants or answers, leaving conflicts correctable in the
  pending card. Historical same-event conflicts remain visible to validation;
  a later explicit answer replaces that group's active choice without rewriting
  its history. Map iteration never selects a route.
  Externalized tool-result references are recognized independently of history
  preview compaction. Timeline normalization and lazy detail loading share the
  canonical artifact/version route decoder. Version-bound search-view counts
  describe discovered records, not completed source reading; an unhydrated
  result with no reliable count is pending detail, not an empty search. The
  existing authenticated detail loader and paged large-result surface remain
  the only disclosure path, including during loading, failures and retries.
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
Retrieval summaries and details use the same bounded receipt decoder for native
camel-case fields and existing snake-case records. An explicit unavailable
source is shown as unavailable even when its error page was read completely;
response byte counts are not evidence acquisition. The source-status notice
does not rewrite transport completion or turn a recoverable source failure into
a terminal task failure. Downloaded body content is not status metadata.
The same unavailable outcome does not count as semantic progress at tool
settlement or durable replay: receiving an unavailable-source response cannot
clear a previously closed no-progress action. Successful and partial usable
results retain their existing progress semantics.
Repeated correction counts are folded in constant space from the same complete,
fenced transcript projection, then carried as private claim-bound replay data.
The provider history window does not reset this count. A newly typed correction
obligation or a new task starts a new scope; unrelated successful tools,
clarification responses and protocol repair do not erase an unresolved one.
The derived aggregate is not model-authored data or another persisted authority.
An interruption's scheduler reason and its original correction cause
are separate fields in the same atomic checkpoint. Exhaustion labels do not
replace the repair scope or erase the failed attempt from replay accounting.
The versioned cause separates its bounded UTF-8 display from a complete typed
condition. Reference diagnostics retain every category and exact identifier;
review issues retain claims, message coordinates, severity/verdict, immutable
versions, evidence references and quoted evidence. Unfinished plan steps bind
their IDs and plan version; visual findings bind image versions and digests.
Semantic identity treats diagnostic sets as unordered and duplicate-insensitive,
but preserves meaningful identifier case/internal whitespace and every evidence
field. A review's human-description summary is presentation when explicit issues
exist; summary-only historical conditions remain distinct. Exact content IDs
also retain presentation and ordering for immutable reads.
Complete conditions use bounded chunks in the same Transcript transaction as
the interruption and lease release. The existing event-size limit is unchanged;
missing, reordered or corrupt chunks cannot hydrate a condition. Chunk storage
does not consume the provider's checkpoint seed window. Full recovery data is a
private, nonserialized projection, not additional model history or another store.
The context and task contract carry the bounded display and exact condition ID.
The existing `read_file` tool accepts `recovery_condition_id` as an exclusive
read source, with line windows and `json_pointer` selections. It validates the
live owner/task/input/lease, preserves JSON escaping in paged diagnostic values,
and reports per-page coverage. Reading a condition is control-plane inspection,
not new scientific evidence, a changed artifact or a reset of the repair budget.
Novel diagnostic windows advance the tool loop; repeating a cached window does
not manufacture progress. A versioned selection cursor is derived from native
read receipts across execution-unit boundaries. Bounded replay retains its last
receipt with the original protocol closure and ordinary replay-size checks.
Coverage records delivered contiguous lines, not comprehension or correctness;
owner, current input and active branch still fence access to the condition.
Repair classifiers consume the complete condition; display counts, identifier
lengths and row-preview limits do not truncate their requirement set.
Old checkpoints without complete data remain explicit legacy evidence. The
reader does not invent clipped fields or assert equality with a new complete
condition. The compatibility journal adapter retains supplied causes, while its
existing storage limits remain separate from canonical Transcript chunking.
Preserving a cause does not reopen an unchanged failed strategy. Correction
counts do not bound the logical task: current admission derives closed routes
from complete canonical conditions and receipts, with explicit material-input
changes required for re-evaluation. The retired correction-budget reason is
eligible through the existing dispatcher without rewriting old checkpoints;
user-decision, external-blocker and terminal/cancellation boundaries remain.
Reader versions must understand the cause/condition contracts before resuming
their checkpoints; rollback must preserve all original events.
Response-presentation validation uses this same correction authority. A failed
inline language conversion distinguishes upstream failures from empty output,
wrong language, changed protected literals and unexpected tool proposals.
Provider errors retain their identity; cancellation cannot release an original
tool call. A validated original action is never replaced by a translator action.
Optional progress blocks keep their protocol bounds but are not execution
prerequisites. Malformed native blocks or embedded envelopes are drained
without publication; a complete valid action proposal still reaches ordinary
schema, permission and admission checks. Metadata diagnostics record the
discard without exposing private reasoning. Cancellation, transport failures,
total response limits and downstream publication errors cannot be discarded
as presentation errors. This applies to streaming/nonstreaming and every
response language; no correction budget is spent on optional narration.
Pre-tool progress cannot establish the proposed operation's completion.
Completion-shaped narration is withheld until the same task has a prior
successful execution or explicit reuse receipt; failed, partial, unavailable,
decision-required and rejected-before-execution receipts grant no authority.
This preserves useful observations from earlier completed work without letting
the current proposal self-certify. Independently validated final responses
retain their authority. This language check is not a scientific truth detector.
Final-language conversion first uses the existing faithful conversion. If
that rejects a candidate containing protected literals, a single different
route binds those literals to host-owned slots, validates each slot exactly
once and restores original bytes. The primary action is never regenerated;
invented or duplicated protected literals also fail validation. Both
conversion costs count and presentation calls do not request hidden reasoning.
An unusable final candidate remains distinct from optional progress. It
preserves completed receipts through the existing typed correction/backoff
authority; persistent inability to produce a valid final answer remains an
explicit correctness blocker, not an endless retry or false completion.
Terminal settlement chooses a safe machine reason before localizing public
detail. The existing terminal event carries that reason atomically; frame
details, persisted message history and live notifications project the same
identity. Stop-hook, cleanup and response-persistence failures replace earlier
causes when they determine the final outcome. Public labels are closed,
localized categories, never provider payloads or filesystem paths.
No-progress call deduplication uses the same complete semantic execution
fingerprint as live retry guards and durable recovery. Human-facing argument
previews remain bounded, but their clipping, JSON key order and presentation
labels never decide whether two execution identities are the same.
Recent closed-route previews are not the admission set. During no-progress
recovery, a single cancellable, fenced transcript scan matches the proposed
batch against exact completed idempotent receipts, typed terminal admission
rejections and explicit closure checkpoints. Matching state is bounded by the
proposal, not history size. Failed or unavailable results are not implicitly
completed evidence; a rejected route is not described as successful execution.
An explicit `executed=false` decision or admission result cannot prove material
progress, satisfy a correction action, or close an attempted execution route.
Previously emitted `correction_route_closed` and
`durable_no_progress_route_closed` replies are derived guard decisions, not new
attempts; replay does not compound them into a permanent closure. During an
active correction, an `ask_user` question supported only by the original task
objective and no current typed decision authority returns an agent-owned
recovery result without parking the task. A separately evidenced scientific,
parameter, cost, resource, or permission choice remains available.
Task/obligation changes and material progress retain their existing reset
semantics. An authority read failure never falls back to the recent preview.
Tool preflight receives one proposed batch and the execution context before
any model-response or tool-start publication. Diagnostics remain bound to exact
call indices in the existing rejection/repair path. Cancellation, unavailable
preflight authority and invalid diagnostic indices stop admission as errors;
they are not converted into a model-selection retry or a tool receipt. The
server applies its existing validators through this single batch entry point.
Model-facing preflight feedback preserves the exact required Skill, schema
filter, evidence reads and registered implementation selectors. Only reviewed
diagnostic fields cross this projection; raw arguments and arbitrary payload
fields do not. The bounded projection always emits valid JSON and never cuts
an identity in half. Oversized explanatory text or omitted supporting fields
are explicitly marked, without weakening the original execution precondition.
Registered software acquisition is checked at admission and the final download
boundary. An exact catalog URL retains its filename and checksum contract even
when the model omits or malforms those fields. For a selected implementation or
evidence resolver, alternative release/source archives of the same upstream
repository do not replace the registered asset. Unrelated data, repositories
and source-document pages keep their ordinary access checks. Corrected calls
use the existing owner-scoped, checksum-verified download cache and transfer
service; there is no second acquisition executor. Constant Python f-string
URLs are resolved by the non-executing AST preparation pass so inline downloads
cannot escape that same durable acquisition route merely through formatting.
Closed acquisition routes retain the exact registered correction. A repeated
Skill route may return its current registered entrypoint only after verifying
the task-scoped bundle against the source digest; guessed paths and modified
assets are not callable authority. Such feedback remains non-executing and
does not reset the no-progress state.
Implementation-owned launcher inspection also follows one literal working
directory prefix and task-local extensionless scripts with recognized shebangs.
The same shell grammar resolves the invocation; bounded root-confined reads
prevent a symlink or printed filename from supplying foreign source authority.
An identified launcher remains subject to the existing canonical execution-pack
gate, which returns the verified current entrypoint when available. No software
name or alternate executor is introduced by this source inspection.
Execution-source preparation inherits the task context while retaining its
eight-second operation budget; a shorter parent deadline or cancellation is
not extended by preparation or converted into permission to execute.
For bounded binary reads, the durable download handoff retains the canonical
request URL only when a contiguous transport redirect receipt connects it to
the final response. The original executed WebFetch input binds redirect hosts.
A successful HTML source may additionally authorize an exact structured file
link on an HTTPS sibling host only when both hosts share the same effective
registrable domain; unrelated external links, arbitrary prose URLs and
connector claims cannot widen that source authority. The same exact link
remains recognizable after compaction when a host-generated
`html-readable-display-lines` read is bound to the immutable large-result
version and source-body SHA; ordinary artifact text and mismatched versions do
not inherit download authority. The existing downloader
re-requests the canonical URL, allowing signed
destinations to rotate within the attested host set while retaining TLS,
public-address checks, bounded redirects, resume validation and atomic staging.
Externalized partial receipts keep their owner, size and content-hash checks;
failed results do not acquire download authority.
PDF text is streamed from the existing passive extractor into the canonical
serialized-budgeted line window. Default reads retain the original document
attachment; explicit `offset`/`limit` reads select extracted text without
resending visual media. `next_offset` advances through whole-document text,
while selected-page visual reads expose a separate whole-text read input.
Text windows do not claim visual coverage. Extraction failure is explicit,
cancellation reaps the extractor, and staging copies the authorized file handle
instead of reopening a potentially replaced pathname. Originals are unchanged.
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
4. Current-task executable plans retain unfinished obligations at final
   settlement. They remain revisable navigation, not an independent scientific
   quality verdict; reasoning-only work never acquires an execution quota.
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
   Repeated attempts against the same typed condition without material progress
   also park the same queue record with `waitingFor=recovery_condition`.
   Completed evidence, the logical goal and resumable checkpoint remain intact.
   New durable user/material evidence, model selection, a newer recovery contract
   or explicit continue can wake it. Administrative progress and elapsed time
   cannot. Live events and reloaded conversation state both show paused, not
   running or failed. Healthy execution and historical counts from an older
   recovery contract do not trigger this wait.
   Route quarantines are scoped to the recovery contract that evaluated them.
   An upgraded contract reevaluates admission while retaining successful
   execution receipts, immutable results, task ownership and permission checks.
   A guard's own rejection cannot create a new attempted-action receipt.
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
    persistence error is logged and cannot interrupt the live tool. Public
    installer and download progress shows observed bytes, remaining bytes,
    transfer rate, phase, and elapsed time without milestone counts or derived
    percentages. A planned transfer size alone never proves bytes were received.
15. The model may load a matching Skill when its instructions or an external
    schema are needed. Root tools remain governed by the exact advertised
    snapshot and their own typed input contracts; there is no Harness-imposed
    `search_skills → skill → tool` sequence.
16. A request sent with `plan_mode=true` keeps the ordinary model-visible tool
    set and model-judged AskUser behavior; it does not impose a fixed tool
    sequence, search count, or task classifier. If the model attempts to finish
    with prose while no matching approved plan exists, a bounded execution-unit
    stop hook returns that candidate for correction. Exhausting that unit
    preserves a resumable plan-approval obligation, never a successful result.
    Approval and tool exposure use the same artifact/task-input revision/content
    binding; an old approval cannot hide plan generation for new input. Provider-supplied
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
    entire tool schema after a malformed call. A repeated rejection in the
    same tool/target/diagnostic family eventually exhausts its private correction
    window, while the capability stays available for genuinely corrected inputs.
    Admission alone and unrelated successes do not reset that family. A
    non-executing rejection is `ok=false`; a successful policy or decision
    preflight may instead be `ok=true, executed=false`, which still does not
    attest that the requested action ran. A stale `edit_file` may use one
    successful `read_file` as inspection evidence and retry once, after which
    the same file/error family closes even if `old_string` keeps changing.
    Persisted native edit conditions also preflight changed invalid variants
    through the same content-type, structural and replacement validators;
    exact valid corrections remain eligible after continuation or restart.
    A native edit rejection also releases a named editor-only correction choice
    to required-tool selection. Other admitted inspection, acquisition and
    execution tools remain available to repair the cause; they do not discharge
    the outstanding validation obligation. Reads and unrelated edits do not
    reset the failed target, and the same invalid input stays preflight-blocked.
    Approval resumption preserves the waiting batch until the terminal receipt
    commits. A non-executing rejection settles that waiting item atomically
    with its failed checkpoint, without replaying the approved mutation or
    terminating the logical task. Recoverable gateway outcomes are normalized
    to the existing approval terminal states; precise diagnostics and partial
    results remain in the original receipt. Unknown or missing receipts do not
    acquire execution authority.
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
    failure. Every partial save carries the same correction code and keeps the
    next model turn tool-required until a later save settles the correction;
    a saved sibling file cannot make a missing requested deliverable look
    complete. Claim-to-source binding and retrieval-depth gaps preserve the
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
20. Durable correction checkpoints provide typed recovery feedback. They
    preserve the reason, affected state and acceptance condition and never
    classify task prose into a domain, filename, tool identity or task-specific
    workflow. A machine-owned structural condition may require the next generic
    capability transition, but the model chooses its arguments and scientific
    content from current evidence. Complete Transcript route membership is
    evaluated with cancellable bounded-memory passes, not bounded display or
    replay windows. Canonical identical conditions and explicitly identified
    unchanged materials close a rejected route, not a tool or logical task.
    Architecture tests reject task-specific
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
    requirements. When the user explicitly requires a read-back or re-read of
    a concrete output filename, completion requires a successful `read_file`
    receipt after that file's latest successful edit or save; an earlier,
    unrelated, failed or non-executing read cannot satisfy the obligation.
    If the user explicitly asks the final answer to report the first failure
    code, that answer must include a bounded machine code from the first failed
    durable tool receipt; later success does not erase or summarize it away.
    The Web history projection preserves only those explicitly requested codes
    that match the task-bound durable failure receipt; unrelated runtime
    identifiers remain behind the normal public-message boundary.
    Tool failures inside such workflows remain local evidence:
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
    plans remain revisable navigation and progress data during execution, but
    every current-task-bound executable plan now retains unfinished, blocked, and skipped steps at
    final settlement. The runner continues the same logical task from its
    checkpoint instead of publishing a progress summary as whole-task success.
    A plan `execution` step declares an `execution_tool` from the task's captured
    tool authority. A unique successful current-task receipt can bind without
    an extra status-only start round; explicit restart and plan-version fences
    still apply. A receipt cannot complete two distinct steps. An old plan with
    an unknown executor can bind an explicit `execution_ref` only to a registered
    execution pack with frame-owned execution provenance and verified output
    digests. Candidate references come from that same receipt scan, never model
    claims. Rejected preflight, failed process, nonzero exit code, an unrelated
    tool, or a status update alone cannot prove execution. Reasoning-only work
    does not acquire an artificial tool-call quota. When an existing `ask_user`
    decision occurs between completed and open plan steps, its question carries
    a bounded server-derived progress snapshot. The optional `stage_progress`
    is validated and persisted on the existing AskUser v1 prompt, including
    plan identity, counts, and at most 12 displayed steps per state; model
    input cannot supply it. Answering resumes the same logical task and its
    open steps. A duration alone never creates a question or terminates work.
    `generate_plan(approve=true)` remains a pure
    control transition; plan-shape normalization cannot add draft content to
    that approve-only call.

## Artifacts and compute

- Receipt-bound managed execution snapshots remain authoritative when their
  mutable workspace aliases are missing or replaced. Missing and empty aliases
  are restored; occupied files and foreign links remain intact and do not gain
  snapshot authority. Kernel protection and artifact publication resolve the
  same verified snapshot. Superseded alias backups are removed only after
  matching the receipt, and a corrupt snapshot cannot grant output ownership.
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
  Host grants do not override protected kernel namespaces. A conflicting mount
  remains fail-closed; helper startup diagnostics expose only a bounded stage
  and error code, never raw paths, arguments or environment values. Existing
  user grants are not silently revoked to make execution start.
  Host-grant creation, picker and mode changes validate the kernel's existing
  namespace policy before persistence, including canonical symlink targets.
  Historical conflicting grants remain visible and explicitly revocable;
  execution admission reports the conflict before starting a worker. A narrow
  task directory is allowed when it does not overlap a protected namespace.
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
  Connection/header and response-read inactivity are separate budgets, not a
  total download lifetime. The positive-second network configuration fields
  `response_header_timeout_seconds` (60), `read_idle_timeout_seconds` (30),
  `transfer_idle_timeout_seconds` (300) and `search_timeout_seconds` (90)
  have matching `SYNON_NETWORK_..._SECONDS` environment overrides. The search
  timeout bounds one query, including all providers and retry waits; a caller's
  shorter deadline or cancellation always wins. Binary Web reads sample a
  bounded prefix and retain their canonical download handoff.
  Search receipts distinguish completed-empty, relevance-filtered, HTTP,
  connection, header-timeout and body-idle outcomes and retain actual attempts.
  Transient status retries honor Retry-After without shortening server advice.
  Full transfers count consecutive attempts without additional retained bytes
  of the same validated representation, not lifetime connections. Repeated
  replacement representations do not reset that budget. A deferred retry keeps
  its not-before time and private partial file across service reconstruction;
  its receipt reports retained bytes, resumability and the actual failure.
  Managed installers consume the same explicit proxy configuration as the
  service. Detached installers restore it from the claimed durable session
  specification. Ambient proxy variables cannot override that route, and
  analysis processes do not inherit installer proxy credentials.
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
commands cannot inherit that entrypoint's authority. One redundant leading
`cd` is removed only when its resolved target is the exact task workspace and
the remaining command already matches one registered execution-pack entrypoint;
different directories, activation commands and any additional operation remain
fail-closed. Runtime-owned arguments
are appended to the parsed argv and quoted as data, not concatenated after a
comment. Python preparation tracks witnessed HTTP response chunks through
iterator bindings, file contexts and called helper parameters into file writes
or stream copies. These facts use the same durable-download requirement as
direct response-body writes; they do not execute or rewrite user source.
Streaming API inspection without a file sink, rebound local data and unresolved
values do not by themselves establish file acquisition. Authenticated/private
transfers are not rerouted to the public downloader. This is bounded static
effect preparation, not exhaustive program analysis or a sandbox guarantee.
Native R parsing accepts omitted call arguments and array indices;
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
Pending transcript projection is a message-synchronization state, not evidence
of active execution. Only a successful completed task awaiting its displayed
answer uses the finalizing capsule. Failed, cancelled, paused, waiting-input and
newer active phases retain their authoritative label, icon and available actions;
failure-message hydration and explicit retry remain intact.
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
