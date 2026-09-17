package workspace

import (
	"context"
	"errors"
)

const (
	frameIncarnationV26PreflightIdentity = "canonical-transcript-v25-frame-incarnation-v1"
	frameIncarnationV26RuleSpec          = "synon.workspace.frame-incarnation-authority.v26"
)

var frameIncarnationV26Migration = versionedSchemaMigration{
	version: 26,
	name:    "frame-incarnation-authority",
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        transcriptSchemaNoopCallbackID,
		RuleSpec:          frameIncarnationV26RuleSpec,
		PreflightIdentity: frameIncarnationV26PreflightIdentity,
	},
	statements: []string{
		`ALTER TABLE frames ADD COLUMN incarnation_id TEXT NOT NULL DEFAULT ''`,
		`UPDATE frames SET incarnation_id=lower(hex(randomblob(16))) WHERE incarnation_id=''`,
		`CREATE UNIQUE INDEX frames_incarnation_id_unique ON frames(incarnation_id) WHERE incarnation_id<>''`,
		`UPDATE workspace_outbox AS outbox
			SET payload_json=json_set(payload_json,'$.frameIncarnationId',(
				SELECT frame.incarnation_id FROM frame_events AS event
				JOIN frames AS frame ON frame.id=event.frame_id
				WHERE event.id=json_extract(outbox.payload_json,'$.frameEventId')
			))
			WHERE outbox.topic='workspace.realtime'
				AND json_valid(outbox.payload_json)
				AND COALESCE(json_extract(outbox.payload_json,'$.frameEventId'),'')<>''
				AND COALESCE(json_extract(outbox.payload_json,'$.frameIncarnationId'),'')=''
				AND EXISTS(
					SELECT 1 FROM frame_events AS event JOIN frames AS frame ON frame.id=event.frame_id
					WHERE event.id=json_extract(outbox.payload_json,'$.frameEventId')
				)`,
		`UPDATE workspace_outbox AS outbox
			SET status='dead_letter',claim_owner=NULL,claim_token=NULL,claimed_at_ms=NULL,
				lease_expires_at_ms=NULL,last_error='pre_v26_frame_source_unavailable',
				dead_lettered_at_ms=COALESCE(dead_lettered_at_ms,occurred_at_ms)
			WHERE outbox.topic='workspace.realtime'
				AND outbox.status IN ('pending','inflight')
				AND json_valid(outbox.payload_json)
				AND COALESCE(json_extract(outbox.payload_json,'$.frameEventId'),'')<>''
				AND NOT EXISTS(
					SELECT 1 FROM frame_events AS event JOIN frames AS frame ON frame.id=event.frame_id
					WHERE event.id=json_extract(outbox.payload_json,'$.frameEventId')
				)`,
	},
}

func preflightFrameIncarnationV26(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	for _, table := range []string{"frames", "frame_events", "workspace_outbox"} {
		var count int
		if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
			return errors.New("inspect frame incarnation preflight")
		}
		if count != 1 {
			return errors.New("frame incarnation migration requires canonical v25 workspace tables")
		}
	}
	return nil
}
