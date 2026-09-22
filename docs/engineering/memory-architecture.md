# Memory architecture

Synon Biomed implements one workspace memory lifecycle. It does not maintain a parallel session-summary memory, file mirror, or runtime-KV memory ledger.

## Authorities

| Concern | Authority |
| --- | --- |
| Runtime tuning | `internal/memoryconfig` and `internal/memorypolicy` |
| User/project/session enablement | workspace user settings, project auto-recall setting, exact session `memory_mode` |
| Automatic extraction preference | user-scoped control-plane setting `memory.autoExtractionEnabled`, inheriting the deployment capability default |
| Durable facts and categories | workspace SQLite memory tables |
| Interactive mutations | claimed `write_memory` transcript receipt |
| Automatic recall | `Store.RecallMemories` |
| Explicit search | `Store.SearchMemories` over the authenticated user's full pool |
| Prompt rendering | `internal/memoryprompt` |
| Post-completion extraction | `internal/memoryextract` plus `memoryExtractionRuntime` |
| Logical-task recall stability | durable runner task memory snapshot |
| Conversation continuity | transcript replay and compaction, not a second memory store |

## Turn lifecycle

1. Resolve the authenticated user, project, root frame and source frame from persisted session/frame state.
2. Apply the user, project and exact `memory_mode` gates. Recall and manual management use the master setting; post-completion extraction additionally uses the independent automatic-memory preference.
3. Render workspace memory rules and bounded profile/category facts into a system message.
4. Rank automatic recall with BM25/Jaccard reciprocal-rank fusion, project boost, staleness decay, cross-project quotas and frame isolation.
5. Inject recall as a harness-owned user block immediately before the real user message. Persist served IDs so retries and child tasks do not repeatedly surface the same facts.
6. Seal that context to the logical task so a lease recovery cannot drift to newly written facts.
7. After a durable completed root turn, run one detached, drainable extraction task. Project the durable transcript, honor compaction cursors, suppress recalled-text feedback, classify mutations, and apply scoped appends/replacements/removals.

## Isolation and safety

- `memory_mode: "off"` disables tools, prompt recall and extraction. Other spellings and non-string values are not silently normalized into policy decisions.
- Turning off automatic memory stops only post-completion extraction. It does not hide existing memories or create a parallel recall path.
- Project memory disablement suppresses automatic recall and extraction; explicit tools remain available when user/session policy permits.
- Frame facts are visible only to the trusted root frame and are deleted with that conversation.
- Explicit search can find another project owned by the same authenticated user, using the explicit-search contract; automatic recall admits cross-project rows only through bounded quotas.
- User-authored facts cannot be replaced or removed by agents or extractors.
- Extractor successors use compare-and-swap supersession; stale model output cannot overwrite a newer fact.
- Prompt-injection classification, literal preservation, secret redaction, hostile-memory sanitization and owner/scope validation are enforced before persistence or rendering.

## Removed competing path

The legacy `autoMemoryEnabled` session-summary path, runtime-KV `memory`/`memory-schedule` namespaces, generated Markdown mirror, schedule pruning, token thresholds and duplicate provider call were removed. Durable transcript compaction remains the sole conversation-continuity mechanism; workspace memory remains the sole cross-session fact mechanism.

## Focused verification

The affected test surface is:

- `go test ./internal/memoryconfig ./internal/memorypolicy ./internal/memoryclassifier ./internal/memoryprompt ./internal/memorytools ./internal/memoryextract`
- `go test ./internal/persistence/workspace -run 'Memory|WorkspaceExtraction|WorkspaceExtractor'`
- `go test ./internal/persistence/transcript -run Memory`
- `go test ./internal/server -run 'Memory|Workspace|RunnerTaskMemory|SessionRunnerModulesBindOnePhaseMachine'`
- `vitest run --project node tests/unit/synonbiomed/synonBiomedMemory.test.ts`
- `vitest run --project dom tests/unit/synonbiomed/SynonBiomedMemoryManager.dom.test.tsx`

Release acceptance additionally requires a real configured model to complete one root scientific task, create a durable memory, recall it in a fresh session, preserve it across reload/compaction, and prove that an opted-out session and another user cannot receive it.
