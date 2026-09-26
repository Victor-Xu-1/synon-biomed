# Tool stream presentation contract

This document defines the single public projection for task operations. Tool
protocol payloads remain authoritative evidence, but the conversation renders a
typed, stable view instead of one generic input/output card.

## Disclosure levels

1. **Timeline** — assistant explanation and compact operation rows. A row shows
   status, a task-specific human description, one short public context value,
   and a result count or progress state.
2. **Stage group** — adjacent operations from one coherent work stage share a
   neutral carrier with a summary and step count. Expanding it reveals the
   ordered child operations without losing their individual states.
3. **Operation detail** — clicking a child row reveals type-specific fields.
   Routine transport fields and empty sections are omitted.
4. **Evidence output** — large code, command output, method guidance, lists, or
   structured evidence has its own disclosure inside the operation detail. It
   is mounted lazily, preserves the scroll anchor, and never replaces already
   published text.

Disclosure defaults are type-owned rather than inferred from payload size.
All ordinary rows use one allow-listed subject projection: file operations show
leaf filenames, package operations show requested packages, and research or
compute operations show their available public query or subject. Rejected or
missing human descriptions do not discard these typed facts. Host paths,
credentials, internal environment identities and arbitrary code/output remain
excluded. For code without a safe description or named subject, only language
and line count are shown. Collapsed group headers reuse these same subjects;
they do not generate another narrative or own another execution state.

Live progress keeps the operation subject beside the observed phase. Unknown
phases use a neutral processing label. Phase-only percentages are explicitly
qualified; installer-process completion is not environment readiness. Unknown
transfer totals retain observed byte counts without inventing a percentage or
remaining size. Terminal status continues to come from the authoritative tool
result, not the progress percentage.
Milestones are displayed as completed/total steps, not a prediction of elapsed
time or work volume. Download percentages explicitly refer to transferred
bytes, while elapsed time explicitly refers to this tool step. Environment
reuse and accepted background execution are distinct from a newly completed
installation.

Intermediate prose should add an observed finding or uncertainty and explain
the next action's purpose in one or two short sentences. Length guidance is a
style target, never a minimum that forces invented facts or blocks execution.
The single primary-model communication contract owns this guidance; no second
summary generator expands or rewrites published progress.

Analysis code/commands and method guidance are visible at operation-detail
level; plan-step descriptions stay beside their titles in the shared plan
card. File bodies/diffs, collection contents, and execution output are
third-level disclosures. Collections begin collapsed even
when short, matching the same predictable interaction for packages, artifacts,
providers, inputs and scientific records.

## Type-specific content

| Public type | Compact row | Operation detail | Evidence output |
| --- | --- | --- | --- |
| Plan | planning objective and step count | plan summary, numbered step titles, per-step descriptions, feasibility/confidence when present | no generic raw payload |
| Search | query and result count | query, source titles, complete URLs, optional relevance snippets | no provider envelope or MCP name |
| Retrieval | selected source and retrieval state | source URL/domain, title, identifier and available publication metadata | sanitized retrieved text or structured record summary |
| Inspection | inspected task object and output size | task-relative file/data identifier and safe selectors | sanitized preview, table/text excerpt, or image preview |
| Analysis / command | scientific action and output size/progress | language or analysis context plus the task code/command when the user expands it | stdout/stderr or structured scientific result, separately collapsible |
| Method guidance | method name and ready state | method title, intended scope and the relevant guide | formatted guidance; product prompts and internal routing remain hidden |
| Environment | preparation action and state | public mode, language/version, requested dependencies and resource recommendation | installed/available components and diagnostic summary; private environment IDs are omitted |
| Compute | resource action and current state | provider display name, purpose, environment, scheduler, transfer direction, timeout and requested inputs/outputs | available providers, job state, transfer size, task code/command and stdout/stderr; private job and host identities are omitted |
| Project memory | lookup/write action and match count | public memory scope, category, query, facts being added or corrected | matching facts and write counts; memory IDs and project/frame identities are omitted |
| Access | requested capability and approval state | public domain or task-relative/leaf location, requested mode and task purpose | granted locations or recovery state; host-account identity and absolute private paths are omitted |
| File edit | task-relative target and state | target plus sanitized written content or before/after content | public mutation result or validation summary |
| Artifact save | deliverable count | filenames, types, sizes and user-facing links/previews | save/validation summary; storage paths, checksums and internal IDs are omitted |
| Failure | failed action | user-readable cause class and recovery path | sanitized task diagnostic when useful; never secrets, stack dumps or product internals |

The typed registry is also the authority for structural differences:

Execution infrastructure fields such as cell/kernel identifiers, transport
backend names, reuse flags and raw exit envelopes are never public scientific
evidence. Domain fields such as dataset/accession, PDB ID, ligand, score,
sample/result counts and deliverable filenames remain available.

Retrieval receipts are translated into task language rather than exposing HTTP
plumbing: for example, full-text availability, DOI, public source, canonical
source link and a readable unavailability reason may be shown, while status
codes and backend names remain hidden.

| Public type | Identity header | Generic output disclosure | Default inner structure |
| --- | --- | --- | --- |
| Plan | omitted because the compact row already names the plan | omitted | summary paragraph, numbered steps with inline descriptions, confidence assessment |
| Search | omitted by the dedicated source renderer | omitted | query followed by source title, complete URL, and optional snippet |
| Method guidance | method and public method name | omitted | formatted guidance directly in the first expanded panel |
| Analysis / command | language and safe environment label | enabled | task code or command, then a separately collapsible result |
| Environment | public operation and language/version | enabled | requested components/resources, then available components or diagnostics |
| Compute | public compute operation and provider | enabled | resource metadata or code/command, then job/transfer/output evidence |
| Project memory | public scope and category | enabled | query or proposed fact changes, then matching facts or mutation counts |
| Access | public domain/location and purpose | enabled | requested scope followed by the granted or recoverable outcome |
| Retrieval / inspection | public operation and task object/source | enabled when evidence exists | safe selectors/metadata, then retrieved or inspected evidence |
| File / artifact | public operation and task-relative target | enabled when evidence exists | public file names/content followed by save or validation evidence |
| Generic | neutral operation name | enabled when evidence exists | only allow-listed public fields and sanitized evidence |

The application does not infer a second visual policy from a protocol batch,
connector name, or historical message shape. Every public operation first maps
through this registry and then uses the same detail projection for live events,
reconnect hydration, and history replay.

## Canonical tool coverage

The public registry covers the complete root-tool contract and the task-owned
compute surface. A new root tool cannot rely on the generic card as its intended
design; it must either join one of these public types or declare that a separate
status surface owns it.

Dynamic MCP methods use the same typed registry. After the type-specific fields
are projected, any remaining safe scalar or collection parameters are rendered
with human-readable labels. This keeps new scientific methods auditable without
adding one UI component per method. Secrets, runtime identities, protocol
fields, private paths, raw envelopes and product implementation terms remain
excluded. The supplemental projection is a fallback inside the same detail
model, not a second card or compatibility renderer.

| Public type | Exact tool roots | Dynamic classification |
| --- | --- | --- |
| Plan | `generate_plan` | none; plans never infer from an arbitrary name |
| Search | `web_search` and retained display aliases | MCP or task tools whose method contains `search`, `query`, `lookup`, or `find` |
| Retrieval | `web_fetch`, `fetch_article_fulltext`, `download_public_scientific_file` | methods containing `fetch`, `download`, or `open_url` |
| Inspection | `read_file`, `read_artifact_lineage` | methods containing `read`, `get`, `inspect`, `describe`, `resolve`, or `select` |
| Analysis / command | `python`, `r`, `bash`/`powershell`, `repl`, `code_execution`, `operon` | methods containing `analyze`, `calculate`, `cluster`, `compute`, `dock`, `fit`, `generate`, `model`, `plot`, `predict`, `render`, `score`, `simulate`, `visualize`, or `align` |
| Method guidance | `search_skills`, `skill` and retained loader aliases | none; an MCP method is evidence, not a Skill card |
| Environment | `manage_environments`, `manage_packages` | none |
| Compute | `list_compute`, `compute_details`, `ask_about_compute`, `compute_provider`, `ssh`, `scp`, `submit_job`, `wait_job`, `cancel_job`, `running_jobs`, `set_concurrency_limit` | task-owned compute methods must use this explicit family |
| Project memory | `read_memory`, `search_memory`, `write_memory` | none |
| Access | `list_host_grants`, `request_host_access`, `request_network_access` | none |
| File | `edit_file`, `delete_host_files` and retained write/edit aliases | methods containing `write`, `edit`, `patch`, `delete`, `remove`, or `update_file` |
| Artifact | `save_artifacts`, `list_artifacts` | methods containing `save`, `export`, or `publish` |

`ask_user` is rendered only by the dedicated question/history cards.
`update_step_status`, `wait_for_notification`, and execution boundaries are
rendered only through the task status/plan authority. Fixed reviewer,
bookmarker, summarizer, onboarding and work-item control tools are not public
task operations and are omitted. These exclusions prevent two UI components
from competing to describe the same state.

Search and retrieval deliberately differ. Search expands to the query and a
list of source titles, complete public URLs and snippets. Retrieval expands to
the selected URL/identifier and preserves the retrieved text, download receipt,
file metadata or diagnostic output behind the evidence disclosure. A retrieval
is never reduced to a second search-results list.

A search with no usable source still uses the dedicated search renderer: it
shows the query and a truthful empty result with a recovery suggestion. It must
not fall through to generic protocol diagnostics or reveal backend/provider
implementation names.

Compact history carries the authoritative aggregate count separately from the
byte-limited output preview as `_compact.result_count`. Provider-level counts
inside diagnostic arrays are never timeline authority: one provider may return
zero while another supplies the final source set. Full history remains the
detail authority and is loaded on demand; compaction therefore reduces payload
size without changing the visible result count.

## Grouping boundaries

- Real assistant prose is always a hard boundary. It explains a changed
  finding, decision, uncertainty, or method; routine operations do not require
  filler text.
- Plans are standalone.
- Search, retrieval, and inspection may merge as one research stage.
- Method selection and environment preparation may merge as one setup stage.
- Access requests, environment preparation and compute discovery may merge as
  one setup stage when no assistant explanation separates them.
- Analysis, inspection, file work, and artifact saving may merge as one
  execution stage. Inspection intentionally overlaps research and execution.
- Compute execution and project-memory writes may merge with execution;
  project-memory reads/searches may merge with research. They do not bridge a
  search directly into computation.
- Search and computation do not merge across a silent semantic transition.
- One shared stage family must remain valid across every operation in a group;
  an inspection or compute row that belongs to two families cannot bridge two
  otherwise incompatible stages through pairwise adjacency.
- Active groups are expanded. Completed short groups remain easy to inspect;
  large completed groups collapse without discarding child detail.

The merge matrix is intentionally small:

| Left stage | May merge with | Must split from |
| --- | --- | --- |
| Setup | method guidance, environment, access, compute discovery | research, computation, plan |
| Execution | analysis, inspection, compute execution, memory write, file work, artifact save | search, setup, plan |
| Research | search, retrieval, inspection, memory read/search | computation, setup, plan |
| Plan | nothing | every other stage |

A kernel cell that only dispatches one typed connector operation and the
connector's durable child result is one public operation. The child's typed
payload, the wrapper's task-specific description, and its readable stdout or
file evidence are retained in that single operation; kernel IDs, dispatch code,
and the transport wrapper itself are not rendered. Compact-history expansion
loads both durable records and enriches the existing row instead of replacing
already visible evidence.

A protocol-level `tool_group` is not trusted as a presentation group. Every
child operation is classified, and the shared-family rule is evaluated across
all children. A batch containing retrieval and computation therefore cannot be
attached to an earlier research group merely because retrieval happened to be
its first child.

## Public-data boundary

### Single-source public communication

Only the primary model produces assistant prose. There is no secondary narration
request or periodic summary writer. The observer records byte counts and typed
publication decisions without generating text or storing response bodies.

The model-facing tool response contract declares `public_progress` as a string
separate from executable arguments. Its schema requires a nonempty update at
the start and after at least four settled operations and 90 seconds without
public prose. Routine rounds allow an empty string; optional structured updates
have a 30-second minimum interval and require a settled operation. Cadence
metadata is scoped to stream, branch and input revision and survives recovery.
The primary model produces both the explanation and its action in one request;
no summarization request is added. One parallel batch publishes at most one
structured update. Valid native tool-round prose takes precedence, avoiding a
duplicate explanation. Existing native streaming remains available.

The adapter removes this field before tool admission/execution. Invalid or
missing progress never disables an otherwise valid scientific operation.
Interrupted responses cannot publish their structured update. Progress uses
the existing durable text stream, while a no-tool response remains a final
candidate subject to all completion checks. Historical progress-envelope
decoding remains supported, but new model instructions use only the declared
response field; no second narrator or alternate tool gateway is introduced.

A completed response with tool calls can contain a short public preamble even
when its proposed tool requires private protocol repair. The publication owner
must explicitly approve that preamble; tool-choice, schema, admission and final
validation remain unchanged. Incomplete responses and final-looking or
report-sized drafts are not promoted to progress. Progress is bounded to 300
Unicode characters without clipping the underlying model response.

Required-tool rounds retain candidate text under the model-response byte budget
until the tool-choice contract can be checked. Adjacent content fragments are
coalesced into bounded chunks, with complete UTF-8 strings and typed marker order
preserved. Provider token fragmentation is not an event-count lifetime for the
response or task. Explicit public progress still uses its own immediate validated
publication path; coalescing grants no new publication or execution authority.

Live text retains its durable publication coordinate. A loaded history page
carries the publication range already represented by its text snapshots. An
increment within that covered range cannot be appended again, and an older live
copy cannot outrank its authoritative snapshot merely because it is longer.
Identical words from a genuinely new publication remain valid. This is a
snapshot/increment reconciliation rule, not content-based duplicate suppression.

Durability is not publication permission. An incomplete provider response is
stored as a versioned `provider_private_candidate` runner checkpoint, bound to
the existing continuation digest, branch, attempt, and assistant segment fences.
Its UTF-8 chunks never become `content_delta` events solely because the provider
was interrupted. Resumption restores these private bytes before completion
validation; public progress requires an explicit progress or tool boundary.
Readers retain support for earlier continuations containing public deltas.
An older reader that does not understand private candidate checkpoints cannot
resume these new continuations; deployment rollback must preserve a compatible
reader or drain the affected continuations first.

Scientific completion requirements do not hide admitted operation lifecycles.
Model-proposed calls remain private until the runner emits a real start or an
authoritative outcome. Start, progress and settlement enrich the same call
identity, including failures. Matching descriptions alone never establish a
retry or recovery relationship between different calls.

Bash output is forwarded as bytes become available, with incremental UTF-8
decoding across pipe reads. A small output chunk must not wait for a full buffer
or process exit. Process activity and output are observations, not proof of
scientific progress or successful completion. Historical immutable command
envelopes remain verifiable, but newly generated envelopes use only the current
streaming implementation.

Historical assistant rows containing only orphan protocol delimiters are
excluded at the shared renderer/virtual-list visibility boundary. User input
and scientific notation remain intact; no durable task evidence is deleted.

Expanded task evidence may include the query, task code, task command, method
guidance, and scientific output needed for audit. Before rendering, the
projection removes credentials, personal absolute paths, product source paths,
system prompts, runtime-policy terms, protocol envelopes, stack traces, and
storage identities. The compact timeline never displays raw JSON or raw logs.

## Stability

### Approval and detached operation ownership

Approving a runner-owned call records authorization; the approval endpoint does
not execute it. The claimed runner resumes the exact persisted call through
the normal Engine progress observer and gateway. A one-shot execution claim
precedes side effects, and its durable result precedes protocol settlement.
Repeated decisions cannot launch another execution. Ordinary tools cannot
approve their own requests. A recovered mutation without a result is explicitly
outcome-unknown, never silently replayed. The original admitted tool attempt
remains the public coordinate across runner handoffs.

Agent-tool approval decisions enter through the frame's dedicated `resolve-input` API, not
the ordinary `SendMessage` tool. The submitted response `message` supplies the
approval reason unchanged apart from surrounding whitespace; requiring a
reason never permits a fabricated fallback. Approval and denial both retain
this explanation. Remembered decisions use the existing exact-input store and
its list/revoke operations; revocation makes the next matching operation ask
again without executing it.

Detached environment work uses the existing environment supervisor and shared
bounded progress collector. Its scoped `tool_operation_observation` events join
the same Transcript journal, outbox and tool-row projection. They may report
only the admitted operation's observed progress/result, not model text or new
protocol receipts. Admission is deterministic, owner/branch-bound and exclusive;
an accepted background receipt cannot overwrite a faster terminal observation.
Task completion does not fabricate completion of detached work. Service restart
settles an abandoned observer as outcome-unknown and publishes a recovery
notification, leaving environment inspection available before any retry.

The progress collector publishes the first observation immediately, coalesces
changed observations at one-second intervals, retains a thirty-second quiet
heartbeat, and flushes the latest observed phase before returning.
Unknown transfer totals stay unknown; no percentage or ETA is extrapolated.
Deployment rollback must retain a reader that understands approval-resume and
operation-observation events once these records have been written.

- Chinese-task progress uses the same language publication boundary for native
  tool preambles and decoded structured progress, including required-tool
  rounds. Clearly English narration is held before publication and faithfully
  localized using the configured model without tools or executable arguments.
  Code, URLs, filenames, numeric literals and scientific identifiers are
  validated for preservation. The original tool identity and arguments remain
  authoritative; failed localization is audited and cannot rerun a valid
  action. Already-published history is not rewritten. Normal Chinese progress
  does not require an extra model request.
- Durable event order is the display order.
- The live continuation indicator is inside the latest content, never a separate
  footer/status row. Between operations it occupies the final tool row's icon
  slot; the row's result text and durable status remain unchanged. New content
  receives the indicator and the previous row restores its ordinary outcome
  icon. A collapsed group shows it in its header; prose shows it inside its
  message container. Existing runtime authority and realtime connectivity own
  activity, not a local timer or a previous tool's label. Active tools retain
  their own indicators. Paused, approval/input waiting, disconnected, terminal
  and historical windows have no continuation animation; their existing task
  controls retain the authoritative status. No durable message is rewritten.
- Streaming text is append-only; tool settlement enriches the existing row and
  cannot retract neighboring assistant text.
- Text replay identity is the conversation, durable segment ID and publication
  sequence, never the text string. Subscription deduplication survives metadata
  refreshes for the same conversation. The message merge boundary also retains
  applied publication ranges, including holes for unseen delayed publications;
  a larger observed sequence is not permission to discard an unseen lower one.
  History coverage retires already represented live copies and their receipt
  ranges. A delayed publication for an existing durable segment updates its
  original row instead of creating another row after an intervening tool.
  Identical text from a different segment or publication remains distinct.
- A compact/history row is immutable once published. Lazy hydration of the full
  durable record may add operation-detail fields, collections and evidence, but
  the row label, short context and result summary continue to come from the
  original projected item. Expanding a row therefore cannot change “41 lines
  of output” into “Completed”, rename the action, or make its query disappear.
- Expanding or collapsing detail preserves the current scroll anchor.
- Reconnect and history hydration reproduce the same groups, labels, terminal
  states, and evidence without a second projection path.

## Visual system

- Conversation content uses one 56rem outer rail with 40px desktop message
  insets, yielding the shared 816px prose/tool measure. Tool rows, expanded
  details, user messages and artifact trays use that same axis. The composer
  uses a 24px rail inset (848px at the desktop maximum) so its
  surface slightly frames, rather than narrows, the transcript.
- Compact operation rows use 13 px type, 22 px line height, 5/10/5/6 px
  padding, an 8 px radius, and the primary task-text color.
- Group carriers use a 14 px radius, 4/6 px padding, and a neutral surface
  derived from the active theme. Detail panels retain a 30 px left inset,
  a 10 px radius and a subtle border/shadow; nested surfaces use theme tokens.
- Assistant prose uses 15 px type with a 25 px line height, respecting user
  font-size preferences. Detail titles use 12 px medium/semibold type; field
  values and explanatory text use 13 px regular sans-serif. Code uses one
  12.5 px/21 px monospace stack. Nested disclosures never progressively shrink
  essential text. Ordinary UI copy is not italicized; authored emphasis remains.
- Search query/title text uses readable sans-serif, with 12 px source metadata.
  Muted metadata uses the theme's secondary color; scientific content remains
  primary text. On narrow screens, nested label/value rows stack vertically.
- Approval and Ask User cards share the same theme and typography hierarchy.
  The visible approval button includes the selected scope. Submission promises
  stay connected to the controller, so in-flight responses disable conflicting
  actions rather than cancelling and replacing the previous decision. Error
  feedback is sanitized and retains user answers for explicit retry.
- Summary rows and recursively expanded data share one private-field policy;
  internal environment/runtime generation identities are never scientific
  evidence and do not appear in either projection.
- Detail panels may scroll only for genuinely large code, output, or result
  evidence. Plans never create a nested scroll surface; each step keeps its
  description visible in the expanded plan.
- Live and historical groups start expanded to show compact operation rows;
  each operation's detail card and evidence output start collapsed. Status and
  result counts continue to update in compact rows.
  Starting work, receiving output, appending operations and settling a tool
  never expand a disclosure. Only user interaction changes its state; manual
  choices survive virtual-list remounts within the conversation and branch.
