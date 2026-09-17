# Transactional Outbox

This package is the delivery half of the workspace SQLite transactional outbox.
The persistence API lives in `internal/persistence/workspace/outbox.go` so a
domain mutation and `EnqueueOutboxTx` can use the same `*sql.Tx`.

## Delivery contract

- Event IDs are deterministic from `(topic, idempotency_key)` unless an explicit
  stable ID is supplied. Reusing an ID with different content fails.
- `attempt_count` increments atomically when an event is claimed. A process crash
  therefore consumes an attempt even when it occurs before ack or retry.
- Claims use SQLite's clock. Expired claims may be taken over; an expired final
  attempt is moved to `dead_letter` before another claim.
- `BatchSize` configures concurrent workers. Each worker claims one event only
  when it is ready to deliver it, so an event does not wait behind a slow member
  while its lease expires.
- Partition heads are ordered and selected fairly by completed-event count.
  A later event in one partition is not claimable while an earlier event is
  pending or inflight.
- Delivery is at least once. HTTP receivers must atomically deduplicate their
  side effect by `X-Synon-Event-ID` (also sent as `Idempotency-Key`). A successful
  downstream side effect followed by a lost response or process crash is sent
  again with the same event ID.
- HTTP redirects are not followed. Non-loopback plaintext HTTP and endpoint URL
  credentials are rejected.

## Bounds

| Field | Limit |
| --- | ---: |
| Event ID | 256 bytes |
| Idempotency key | 512 bytes |
| Topic / event type | 128 bytes each |
| Partition key | 512 bytes |
| Aggregate type / ID | 512 bytes each |
| JSON payload | 1 MiB |
| Headers | 128 entries, 64 KiB encoded total |
| Header name / value | 256 bytes / 8 KiB |
| Claim lease / retry backoff | 24 hours |
| Initial availability delay | 365 days |

Identifiers and header values reject control characters. Header names must use
the HTTP token character set.

HTTP mutation idempotency keys are a stricter application contract: 1-256
bytes, ASCII HTTP-token characters only. They are never trimmed or truncated.
The mutation result ledger is namespaced by owner, key and operation; a retry
with the same request hash replays the committed result, while changed content
returns HTTP 409.

## Realtime materialization

The `workspace.realtime` topic contains a versioned typed envelope. The
production dispatcher materializes its stable event ID into `realtime_events`
and only then publishes the compat and frame hubs. A process may crash after the
business commit or after materialization; either restart path converges on one
durable realtime row.

Routine claims use a persisted `claim_generation` and `claim_token`. Claim
retries with the same token reuse the same generation and event. Completion is
fenced by the complete claim tuple and records an idempotency fingerprint;
replaying identical content is a no-op while changed content is rejected.

## Integrated business mutations

- Project HTTP create, update and delete.
- Frame HTTP create, update and delete. Create/update include the frame journal
  row in the same transaction and fan it out after realtime materialization.
- Routine HTTP create, claim and completion.
- Production routine scheduler claim and completion, including tick-scoped
  `host.done` work/idle markers.
- Routine host configure and `host.done` mutations.
- Artifact/version create, streamed/binary writes, copy, user apply-edit,
  rename, priority, move and delete. Version writes enqueue
  `artifact_created` plus `lineage_ready`; frame-producing writes also append
  their frame journal row in the same transaction.
- Artifact folders and project notes, including all baseline folder and
  `note_update` events.
- Cloud artifact import through the same streamed Artifact transaction.
- Attachment create and finalize use a durable two-phase blob marker. Startup
  completes a committed staging-to-final rename before reference auditing.
  Finalize retries replay the first attachment result after upload rows are
  deleted. Chunk rows, upload timestamps and chunk markers commit together;
  startup validates referenced chunks and removes canceled/uncommitted upload
  directories.
- Annotation CRUD and Memory create/supersede enforce owner scope in their
  SQLite transaction. v1.1 defines no independent realtime event for these
  mutations, so no synthetic event is emitted. Apply-edit emits the Artifact
  events and carries annotations atomically.

## Remaining integration backlog

Each remaining mutation must move its existing write into a shared transaction
and enqueue its typed event before commit. A post-commit audit record or a
second direct publish does not close the dual-write window.

- Projects and frames: branch, cancel, tree cancel/delete, compatibility
  project/frame writes, resume dispatch, read cursors, bench state, runtime
  metadata, session journal mirroring and non-HTTP frame/realtime writes.
- Agents and skills: agent create/update/delete/enable, skill assignments and
  preferences, custom prompts, connector tombstones/exclusions/tool exclusions.
- Model and MCP configuration: model provider registration, MCP server CRUD,
  grants, assignments, OAuth state, directories, directory connectors, runtime
  state and health updates.
- Artifacts and execution: Compute inference/workbench Artifact result writes
  (owned by the Compute Outbox batch), standalone lineage/execution links,
  execution log and feedback.
- Attachments: v1.1 defines no realtime event for create/chunk/finalize/cancel;
  future attachment metadata/delete mutations must join this protocol when
  introduced.
- Memory and routines: future routine
  pause/resume/delete/reconfiguration paths outside `host.routine.configure`.
- Compute: inference/SSH provider mutations and probes, BYOC/GPU/BioNeMo settings,
  managed endpoints, jobs/logs/session enablement, usage lifecycle/results,
  reconciliation leases, poller claims, harvest state and pending termination.

Scheduler lease heartbeats and internal bookkeeping remain non-public. Claim and
completion lifecycle transitions are public `routine_update` events.
