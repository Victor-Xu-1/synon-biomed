package transcript

const TypedHistoryBootstrapContractID = "synon.transcript.typed-history-bootstrap.v35"

const typedHistoryBootstrapReceiptsV35Statement = `CREATE TABLE transcript_typed_history_bootstrap_receipts (
	bootstrap_id BLOB PRIMARY KEY CHECK(length(bootstrap_id)=32),
	genesis_id BLOB NOT NULL UNIQUE,
	stream_uid TEXT NOT NULL UNIQUE,
	owner_id TEXT NOT NULL,
	session_id TEXT NOT NULL,
	frame_incarnation_id TEXT NOT NULL CHECK(length(frame_incarnation_id)>0),
	epoch INTEGER NOT NULL CHECK(epoch=1),
	active_branch_id TEXT NOT NULL,
	branch_generation INTEGER NOT NULL CHECK(branch_generation=1),
	authority_generation INTEGER NOT NULL CHECK(authority_generation=1),
	history_kind TEXT NOT NULL CHECK(history_kind='ordinary_rich_v1'),
	source_through_sequence INTEGER NOT NULL CHECK(source_through_sequence>0),
	source_row_count INTEGER NOT NULL CHECK(source_row_count>0),
	history_event_count INTEGER NOT NULL CHECK(history_event_count=source_row_count),
	branch_event_count INTEGER NOT NULL CHECK(branch_event_count=history_event_count),
	text_block_count INTEGER NOT NULL CHECK(text_block_count>=0),
	tool_use_count INTEGER NOT NULL CHECK(tool_use_count>0),
	tool_result_count INTEGER NOT NULL CHECK(tool_result_count=tool_use_count),
	ask_user_prompt_count INTEGER NOT NULL CHECK(ask_user_prompt_count=0),
	ask_user_result_count INTEGER NOT NULL CHECK(ask_user_result_count=0),
	attempt_count INTEGER NOT NULL CHECK(attempt_count=0),
	runner_receipt_count INTEGER NOT NULL CHECK(runner_receipt_count=0),
	checkpoint_count INTEGER NOT NULL CHECK(checkpoint_count=0),
	artifact_commit_count INTEGER NOT NULL CHECK(artifact_commit_count=0),
	artifact_ref_count INTEGER NOT NULL CHECK(artifact_ref_count=0),
	source_snapshot_sha256 BLOB NOT NULL CHECK(length(source_snapshot_sha256)=32),
	history_sha256 BLOB NOT NULL CHECK(length(history_sha256)=32),
	materialized_sha256 BLOB NOT NULL CHECK(length(materialized_sha256)=32),
	status TEXT NOT NULL CHECK(status='active'),
	created_at TIMESTAMP NOT NULL,
	UNIQUE(bootstrap_id,stream_uid,epoch,authority_generation),
	FOREIGN KEY(genesis_id,stream_uid,epoch,authority_generation)
		REFERENCES transcript_payload_genesis_receipts(
			genesis_id,stream_uid,epoch,authority_generation) ON DELETE NO ACTION,
	FOREIGN KEY(stream_uid,owner_id,session_id,epoch)
		REFERENCES transcript_streams(stream_uid,owner_id,session_id,epoch) ON DELETE NO ACTION,
	FOREIGN KEY(stream_uid,active_branch_id)
		REFERENCES transcript_branches(stream_uid,branch_id) ON DELETE NO ACTION
)`

const typedHistoryBootstrapValidateV35Statement = `CREATE TRIGGER transcript_typed_history_bootstrap_validate
	BEFORE INSERT ON transcript_typed_history_bootstrap_receipts
	WHEN NOT EXISTS (
		SELECT 1 FROM transcript_payload_genesis_receipts genesis
		JOIN transcript_streams stream ON stream.stream_uid=genesis.stream_uid
		JOIN transcript_branch_state branch ON branch.stream_uid=stream.stream_uid
		JOIN frames frame ON frame.id=stream.frame_id AND frame.project_id=stream.project_id
		JOIN projects project ON project.id=frame.project_id
		WHERE genesis.genesis_id=NEW.genesis_id AND genesis.stream_uid=NEW.stream_uid
			AND genesis.owner_id=NEW.owner_id AND genesis.session_id=NEW.session_id
			AND genesis.epoch=NEW.epoch AND genesis.authority_generation=NEW.authority_generation
			AND genesis.source_kind='frame_import' AND genesis.status='active'
			AND genesis.source_event_count=NEW.source_row_count
			AND genesis.source_sha256=NEW.source_snapshot_sha256
			AND stream.kind='frame_ref' AND stream.owner_id=NEW.owner_id
			AND stream.session_id=NEW.session_id AND stream.epoch=NEW.epoch
			AND project.user_id=NEW.owner_id AND frame.incarnation_id=NEW.frame_incarnation_id
			AND branch.active_branch_id=NEW.active_branch_id
			AND branch.generation=NEW.branch_generation
			AND NEW.history_event_count=(SELECT COUNT(*) FROM transcript_events event
				WHERE event.stream_uid=NEW.stream_uid AND event.source='payload'
					AND event.runner_attempt IS NULL AND event.frame_event_id IS NULL)
			AND NEW.branch_event_count=(SELECT COUNT(*) FROM transcript_branch_events member
				WHERE member.stream_uid=NEW.stream_uid AND member.branch_id=NEW.active_branch_id)
			AND 0=(SELECT COUNT(*) FROM transcript_runner_attempts attempt
				WHERE attempt.stream_uid=NEW.stream_uid)
			AND 0=(SELECT COUNT(*) FROM transcript_runner_receipts receipt
				WHERE receipt.stream_uid=NEW.stream_uid)
			AND 0=(SELECT COUNT(*) FROM transcript_runner_checkpoints checkpoint
				WHERE checkpoint.stream_uid=NEW.stream_uid)
			AND 0=(SELECT COUNT(*) FROM transcript_artifact_commits commit_row
				WHERE commit_row.stream_uid=NEW.stream_uid)
			AND 0=(SELECT COUNT(*) FROM transcript_artifact_refs ref
				WHERE ref.stream_uid=NEW.stream_uid)
			AND NOT EXISTS (SELECT 1 FROM transcript_frame_authority authority
				WHERE authority.owner_id=NEW.owner_id AND authority.session_id=NEW.session_id)
	)
	BEGIN SELECT RAISE(ABORT,'typed history bootstrap authority mismatch'); END`

const typedHistoryBootstrapImmutableV35Statement = `CREATE TRIGGER transcript_typed_history_bootstrap_immutable
	BEFORE UPDATE ON transcript_typed_history_bootstrap_receipts
	BEGIN SELECT RAISE(ABORT,'typed history bootstrap receipt is immutable'); END`

const typedHistoryBootstrapDeleteActiveV35Statement = `CREATE TRIGGER transcript_typed_history_bootstrap_delete_active
	BEFORE DELETE ON transcript_typed_history_bootstrap_receipts
	WHEN EXISTS (
		SELECT 1 FROM transcript_frame_authority authority
		WHERE authority.active_stream_uid=OLD.stream_uid AND authority.active_epoch=OLD.epoch
			AND authority.genesis_id=OLD.genesis_id
			AND authority.read_authority='transcript_payload_v1'
			AND authority.write_authority='transcript_payload_v1'
	)
	BEGIN SELECT RAISE(ABORT,'active typed history bootstrap receipt is immutable'); END`

var typedHistoryBootstrapV35Statements = []string{
	typedHistoryBootstrapReceiptsV35Statement,
	typedHistoryBootstrapValidateV35Statement,
	typedHistoryBootstrapImmutableV35Statement,
	typedHistoryBootstrapDeleteActiveV35Statement,
}

func TypedHistoryBootstrapV35Statements() []string {
	return append([]string(nil), typedHistoryBootstrapV35Statements...)
}

func TypedHistoryBootstrapV35ObjectNames() []string {
	return []string{
		"transcript_typed_history_bootstrap_receipts",
		"transcript_typed_history_bootstrap_validate",
		"transcript_typed_history_bootstrap_immutable",
		"transcript_typed_history_bootstrap_delete_active",
	}
}

func TypedHistoryBootstrapV35CanonicalStatements() []string {
	return append([]string(nil), typedHistoryBootstrapV35Statements...)
}
