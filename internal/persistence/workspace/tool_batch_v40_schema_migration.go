package workspace

import (
	"context"
	"errors"
)

const (
	toolCallBatchV40MigrationName     = "transcript-tool-call-batch-authority"
	toolCallBatchV40CallbackID        = "tool-call-batch-v40-noop"
	toolCallBatchV40PreflightIdentity = "transcript-tool-call-batch-empty-cohort-v1"
	toolCallBatchV40RuleSpec          = "synon.workspace.transcript-tool-call-batch.v40"
	toolCallBatchesDDL                = `CREATE TABLE transcript_tool_call_batches (
		batch_id TEXT PRIMARY KEY,
		owner_user_id TEXT NOT NULL,
		stream_uid TEXT NOT NULL REFERENCES transcript_streams(stream_uid) ON DELETE RESTRICT,
		branch_id TEXT NOT NULL,
		branch_generation INTEGER NOT NULL CHECK(branch_generation>0),
		source_event_id INTEGER NOT NULL CHECK(source_event_id>0),
		source_publication_seq INTEGER NOT NULL CHECK(source_publication_seq>0),
		source_runner_attempt INTEGER NOT NULL CHECK(source_runner_attempt>0),
		source_client_message_id TEXT NOT NULL,
		admitted_input_revision INTEGER NOT NULL CHECK(admitted_input_revision>0),
		call_count INTEGER NOT NULL CHECK(call_count>0),
		next_ordinal INTEGER NOT NULL DEFAULT 0 CHECK(next_ordinal>=0 AND next_ordinal<=call_count),
		state TEXT NOT NULL CHECK(state IN ('ready','running','waiting','settled','cancelled','outcome_unknown')),
		state_version INTEGER NOT NULL CHECK(state_version>0),
		waiting_ordinal INTEGER CHECK(waiting_ordinal IS NULL OR (waiting_ordinal>=0 AND waiting_ordinal<call_count)),
		runner_id TEXT,
		runner_attempt INTEGER CHECK(runner_attempt IS NULL OR runner_attempt>0),
		runner_claim_sha256 TEXT CHECK(runner_claim_sha256 IS NULL OR
			(length(runner_claim_sha256)=64 AND runner_claim_sha256 NOT GLOB '*[^0-9a-f]*')),
		reason_code TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		UNIQUE(stream_uid,source_event_id),
		UNIQUE(batch_id,stream_uid),
		FOREIGN KEY(stream_uid,source_runner_attempt,source_event_id)
			REFERENCES transcript_events(stream_uid,runner_attempt,event_id) ON DELETE RESTRICT,
		CHECK((runner_id IS NULL AND runner_attempt IS NULL AND runner_claim_sha256 IS NULL) OR
			(runner_id IS NOT NULL AND runner_attempt IS NOT NULL AND runner_claim_sha256 IS NOT NULL)),
		CHECK(
			(state='settled' AND next_ordinal=call_count AND waiting_ordinal IS NULL) OR
			(state='waiting' AND next_ordinal<call_count AND waiting_ordinal=next_ordinal) OR
			(state IN ('ready','running') AND next_ordinal<call_count AND waiting_ordinal IS NULL) OR
			(state IN ('cancelled','outcome_unknown') AND waiting_ordinal IS NULL)
		)
	) STRICT`
	toolCallBatchItemsDDL = `CREATE TABLE transcript_tool_call_items (
		batch_id TEXT NOT NULL,
		stream_uid TEXT NOT NULL,
		ordinal INTEGER NOT NULL CHECK(ordinal>=0),
		tool_call_id TEXT NOT NULL,
		tool_name TEXT NOT NULL,
		arguments_json TEXT NOT NULL CHECK(length(CAST(arguments_json AS BLOB))<=1048576),
		arguments_sha256 TEXT NOT NULL CHECK(length(arguments_sha256)=64 AND arguments_sha256 NOT GLOB '*[^0-9a-f]*'),
		state TEXT NOT NULL CHECK(state IN ('pending','running','waiting','completed','failed','blocked','cancelled','outcome_unknown')),
		state_version INTEGER NOT NULL CHECK(state_version>0),
		started_event_id INTEGER CHECK(started_event_id IS NULL OR started_event_id>0),
		waiting_event_id INTEGER CHECK(waiting_event_id IS NULL OR waiting_event_id>0),
		terminal_event_id INTEGER CHECK(terminal_event_id IS NULL OR terminal_event_id>0),
		terminal_result_sha256 TEXT CHECK(terminal_result_sha256 IS NULL OR
			(length(terminal_result_sha256)=64 AND terminal_result_sha256 NOT GLOB '*[^0-9a-f]*')),
		result_ref TEXT CHECK(result_ref IS NULL OR length(CAST(result_ref AS BLOB))<=4096),
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY(batch_id,ordinal),
		UNIQUE(batch_id,tool_call_id),
		FOREIGN KEY(batch_id,stream_uid)
			REFERENCES transcript_tool_call_batches(batch_id,stream_uid) ON DELETE RESTRICT,
		FOREIGN KEY(stream_uid,started_event_id)
			REFERENCES transcript_events(stream_uid,event_id) ON DELETE RESTRICT,
		FOREIGN KEY(stream_uid,waiting_event_id)
			REFERENCES transcript_events(stream_uid,event_id) ON DELETE RESTRICT,
		FOREIGN KEY(stream_uid,terminal_event_id)
			REFERENCES transcript_events(stream_uid,event_id) ON DELETE RESTRICT,
		CHECK(
			(state='pending' AND started_event_id IS NULL AND waiting_event_id IS NULL AND terminal_event_id IS NULL AND
				terminal_result_sha256 IS NULL AND result_ref IS NULL) OR
			(state='running' AND started_event_id IS NOT NULL AND waiting_event_id IS NULL AND terminal_event_id IS NULL AND
				terminal_result_sha256 IS NULL AND result_ref IS NULL) OR
			(state='waiting' AND started_event_id IS NOT NULL AND waiting_event_id IS NOT NULL AND terminal_event_id IS NULL AND
				terminal_result_sha256 IS NULL AND result_ref IS NULL) OR
			(state IN ('completed','failed','blocked','cancelled','outcome_unknown') AND terminal_event_id IS NOT NULL AND
				terminal_result_sha256 IS NOT NULL)
		)
	) STRICT`
)

var toolCallBatchV40Migration = versionedSchemaMigration{
	version: 40,
	name:    toolCallBatchV40MigrationName,
	statements: []string{
		toolCallBatchesDDL,
		toolCallBatchItemsDDL,
		`CREATE INDEX transcript_tool_call_batches_runnable_idx ON transcript_tool_call_batches(
			owner_user_id,stream_uid,branch_id,branch_generation,state,admitted_input_revision,
			source_publication_seq,batch_id
		) WHERE state IN ('ready','running','waiting')`,
		`CREATE INDEX transcript_tool_call_items_state_idx ON transcript_tool_call_items(batch_id,state,ordinal)`,
		`CREATE UNIQUE INDEX transcript_tool_call_items_started_event_unique
			ON transcript_tool_call_items(stream_uid,started_event_id) WHERE started_event_id IS NOT NULL`,
		`CREATE UNIQUE INDEX transcript_tool_call_items_waiting_event_unique
			ON transcript_tool_call_items(stream_uid,waiting_event_id) WHERE waiting_event_id IS NOT NULL`,
		`CREATE UNIQUE INDEX transcript_tool_call_items_terminal_event_unique
			ON transcript_tool_call_items(stream_uid,terminal_event_id) WHERE terminal_event_id IS NOT NULL`,
		`CREATE TRIGGER transcript_tool_call_batches_identity_immutable BEFORE UPDATE ON transcript_tool_call_batches
		WHEN NEW.batch_id!=OLD.batch_id OR NEW.owner_user_id!=OLD.owner_user_id OR NEW.stream_uid!=OLD.stream_uid OR
			NEW.branch_id!=OLD.branch_id OR NEW.branch_generation!=OLD.branch_generation OR
			NEW.source_event_id!=OLD.source_event_id OR NEW.source_publication_seq!=OLD.source_publication_seq OR
			NEW.source_runner_attempt!=OLD.source_runner_attempt OR NEW.source_client_message_id!=OLD.source_client_message_id OR
			NEW.admitted_input_revision!=OLD.admitted_input_revision OR NEW.call_count!=OLD.call_count OR
			NEW.created_at!=OLD.created_at
		BEGIN SELECT RAISE(ABORT,'tool call batch identity is immutable'); END`,
		`CREATE TRIGGER transcript_tool_call_batches_transition_valid BEFORE UPDATE ON transcript_tool_call_batches
		WHEN NEW.state_version!=OLD.state_version+1 OR NOT (
			(OLD.state='ready' AND NEW.state IN ('ready','running','cancelled','outcome_unknown')) OR
			(OLD.state='running' AND NEW.state IN ('running','ready','waiting','settled','cancelled','outcome_unknown')) OR
			(OLD.state='waiting' AND NEW.state IN ('waiting','ready','settled','cancelled','outcome_unknown'))
		)
		BEGIN SELECT RAISE(ABORT,'tool call batch transition is invalid'); END`,
		`CREATE TRIGGER transcript_tool_call_batches_cursor_valid BEFORE UPDATE ON transcript_tool_call_batches
		WHEN NOT (NEW.next_ordinal=OLD.next_ordinal OR
			(NEW.next_ordinal=OLD.next_ordinal+1 AND OLD.next_ordinal<OLD.call_count))
		BEGIN SELECT RAISE(ABORT,'tool call batch cursor transition is invalid'); END`,
		`CREATE TRIGGER transcript_tool_call_batch_items_identity_immutable BEFORE UPDATE ON transcript_tool_call_items
		WHEN NEW.batch_id!=OLD.batch_id OR NEW.stream_uid!=OLD.stream_uid OR NEW.ordinal!=OLD.ordinal OR
			NEW.tool_call_id!=OLD.tool_call_id OR NEW.tool_name!=OLD.tool_name OR
			NEW.arguments_json!=OLD.arguments_json OR NEW.arguments_sha256!=OLD.arguments_sha256 OR
			NEW.created_at!=OLD.created_at OR
			(OLD.started_event_id IS NOT NULL AND NEW.started_event_id IS NOT OLD.started_event_id) OR
			(OLD.waiting_event_id IS NOT NULL AND NEW.waiting_event_id IS NOT OLD.waiting_event_id) OR
			(OLD.terminal_event_id IS NOT NULL AND NEW.terminal_event_id IS NOT OLD.terminal_event_id) OR
			(OLD.terminal_result_sha256 IS NOT NULL AND NEW.terminal_result_sha256 IS NOT OLD.terminal_result_sha256) OR
			(OLD.result_ref IS NOT NULL AND NEW.result_ref IS NOT OLD.result_ref)
		BEGIN SELECT RAISE(ABORT,'tool call batch item identity is immutable'); END`,
		`CREATE TRIGGER transcript_tool_call_batch_items_transition_valid BEFORE UPDATE ON transcript_tool_call_items
		WHEN NEW.state_version!=OLD.state_version+1 OR NOT (
			(OLD.state='pending' AND NEW.state IN ('running','cancelled','outcome_unknown')) OR
			(OLD.state='running' AND NEW.state IN ('waiting','completed','failed','blocked','cancelled','outcome_unknown')) OR
			(OLD.state='waiting' AND NEW.state IN ('completed','failed','blocked','cancelled','outcome_unknown'))
		)
		BEGIN SELECT RAISE(ABORT,'tool call batch item transition is invalid'); END`,
		`CREATE TRIGGER transcript_tool_call_batches_delete_forbidden BEFORE DELETE ON transcript_tool_call_batches
		BEGIN SELECT RAISE(ABORT,'tool call batch authority is append-only'); END`,
		`CREATE TRIGGER transcript_tool_call_batch_items_delete_forbidden BEFORE DELETE ON transcript_tool_call_items
		BEGIN SELECT RAISE(ABORT,'tool call batch item authority is append-only'); END`,
	},
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        toolCallBatchV40CallbackID,
		RuleSpec:          toolCallBatchV40RuleSpec,
		PreflightIdentity: toolCallBatchV40PreflightIdentity,
	},
}

func preflightToolCallBatchV40(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	var dependencies int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
		WHERE type='table' AND name IN ('transcript_streams','transcript_events','transcript_branch_state','transcript_branch_events')`).Scan(&dependencies); err != nil {
		return errors.New("inspect tool call batch dependencies")
	}
	if dependencies != 4 {
		return errors.New("tool call batch dependencies are required")
	}
	var polluted int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
		WHERE name IN ('transcript_tool_call_batches','transcript_tool_call_items')`).Scan(&polluted); err != nil {
		return errors.New("inspect tool call batch cohort")
	}
	if polluted != 0 {
		return errors.New("tool call batch cohort is polluted")
	}
	return nil
}
