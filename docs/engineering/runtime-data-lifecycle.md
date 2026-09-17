# Runtime data lifecycle

Synon Biomed has one task-history authority and one bounded operational-state
store:

- `workspace/synonbiomed-v1.1.sqlite` owns projects, frames, canonical
  transcript messages, execution evidence, tool records, memories, and artifact
  lineage.
- `runtime-state.sqlite` owns non-conversation operational state such as
  approvals, connector projections, short-lived audits, and usage counters.
  Each key is one SQLite row; updates never rewrite unrelated entries.
- `logs/server-YYYYMMDD.log` is a diagnostic stream, not task history.

The server performs its first lifecycle sweep five minutes after startup and
then once every 24 hours. It logs a compact receipt only when rows were removed.

| Data class | Retention | Protection |
|---|---:|---|
| Unreferenced content snapshots | 1 hour | Hashes referenced by artifact provenance remain |
| Terminal realtime replay rows | 1 hour | Active roots and history activation fences remain |
| Ephemeral artifacts | 1 day | Active roots and all non-ephemeral/final artifacts remain |
| Resolved queued input | 7 days | Queued or delivering input remains |
| Delivered transport receipts | 1 hour | Undelivered rows and referenced compute submissions remain |
| Dead letters and operational audits | 7 days | Pending/inflight transport and active state remain |
| Completed compute usage and usage audits | 30 days | Running compute remains |
| Superseded memories | 90 days | Active memory and safe supersession chains remain |
| Daily operational logs | 7 days | Current files remain private mode `0600` |
| Schema rollback snapshots | newest 2 | A newly created migration backup is retained |

Deleting a task or project removes its SQLite child records, realtime replay
cohort, outbox cohort, artifact blobs, compatibility session/journal projection,
and scoped runtime-state entries. The deletion notification is enqueued only
after the retired transport cohort has been removed, so it is the sole event
that survives the transaction.

The lifecycle never applies a fixed age limit to active tasks. It does not
delete conversation messages, execution logs attached to a retained frame,
final artifacts, user uploads, active memory, unresolved approvals, pending
outbox events, credentials, settings, or account data.

Historical runtime reset and deployment remain integration-controller actions.
The controller must stop the service, verify the resolved data root, preserve
configuration and credentials as directed, remove the approved runtime-history
inventory, start from the integrated revision, and verify fresh SQLite creation,
foreign-key integrity, API health, browser navigation, task execution, restart,
and a post-restart lifecycle sweep. Source files are never part of that reset.
