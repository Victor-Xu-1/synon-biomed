package transcript

const HistoryBackfillContractID = "synon.transcript.history-backfill.v29"

var historyBackfillV29Statements = []string{
	`CREATE TABLE transcript_history_backfill_runs (
		backfill_id BLOB PRIMARY KEY CHECK(length(backfill_id) = 32),
		classification_run_id BLOB NOT NULL UNIQUE
			REFERENCES transcript_history_classification_runs(run_id) ON DELETE NO ACTION,
		stream_uid TEXT NOT NULL,
		branch_id TEXT NOT NULL,
		branch_generation INTEGER NOT NULL CHECK(branch_generation > 0),
		contract_version INTEGER NOT NULL CHECK(contract_version = 1),
		source_sha256 BLOB NOT NULL CHECK(length(source_sha256) = 32),
		staging_sha256 BLOB NOT NULL CHECK(length(staging_sha256) = 32),
		candidate_count INTEGER NOT NULL CHECK(candidate_count > 0),
		created_at TIMESTAMP NOT NULL,
		UNIQUE(backfill_id,classification_run_id),
		UNIQUE(stream_uid,branch_id,branch_generation,classification_run_id),
		FOREIGN KEY(stream_uid,branch_id)
			REFERENCES transcript_branches(stream_uid,branch_id) ON DELETE NO ACTION
	)`,
	`CREATE TABLE transcript_history_backfill_candidates (
		backfill_id BLOB NOT NULL,
		classification_run_id BLOB NOT NULL,
		candidate_id BLOB NOT NULL CHECK(length(candidate_id) = 32),
		candidate_ordinal INTEGER NOT NULL CHECK(candidate_ordinal > 0),
		tool_use_id TEXT NOT NULL,
		runner_attempt INTEGER NOT NULL CHECK(runner_attempt > 0),
		tool_event_id INTEGER NOT NULL CHECK(tool_event_id > 0),
		result_event_id INTEGER NOT NULL CHECK(result_event_id > 0),
		tool_frame_event_id TEXT NOT NULL,
		result_frame_event_id TEXT NOT NULL,
		prompt_client_message_id TEXT NOT NULL,
		pending_client_message_id TEXT NOT NULL,
		terminal_client_message_id TEXT NOT NULL,
		prompt_json BLOB NOT NULL CHECK(json_valid(prompt_json)),
		pending_json BLOB NOT NULL CHECK(json_valid(pending_json)),
		result_json BLOB NOT NULL CHECK(json_valid(result_json)),
		payload_sha256 BLOB NOT NULL CHECK(length(payload_sha256) = 32),
		evidence_sha256 BLOB NOT NULL CHECK(length(evidence_sha256) = 32),
		created_at TIMESTAMP NOT NULL,
		PRIMARY KEY(backfill_id,candidate_id),
		UNIQUE(backfill_id,candidate_ordinal),
		UNIQUE(backfill_id,prompt_client_message_id),
		UNIQUE(backfill_id,pending_client_message_id),
		UNIQUE(backfill_id,terminal_client_message_id),
		CHECK(length(prompt_client_message_id) > 0),
		CHECK(length(pending_client_message_id) > 0),
		CHECK(length(terminal_client_message_id) > 0),
		CHECK(prompt_client_message_id <> pending_client_message_id),
		CHECK(prompt_client_message_id <> terminal_client_message_id),
		CHECK(pending_client_message_id <> terminal_client_message_id),
		FOREIGN KEY(backfill_id,classification_run_id)
			REFERENCES transcript_history_backfill_runs(backfill_id,classification_run_id) ON DELETE CASCADE,
		FOREIGN KEY(classification_run_id,candidate_id)
			REFERENCES transcript_history_classification_candidates(run_id,candidate_id) ON DELETE NO ACTION
	)`,
	`CREATE TABLE transcript_history_backfill_cursor_map (
		backfill_id BLOB NOT NULL,
		candidate_id BLOB NOT NULL CHECK(length(candidate_id) = 32),
		legacy_ordinal INTEGER NOT NULL CHECK(legacy_ordinal > 0),
		stable_message_id TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL,
		PRIMARY KEY(backfill_id,legacy_ordinal),
		FOREIGN KEY(backfill_id,candidate_id)
			REFERENCES transcript_history_backfill_candidates(backfill_id,candidate_id) ON DELETE CASCADE
	)`,
	`CREATE INDEX transcript_history_backfill_stream
		ON transcript_history_backfill_runs(stream_uid,branch_id,branch_generation,backfill_id)`,
	`CREATE INDEX transcript_history_backfill_candidate_source
		ON transcript_history_backfill_candidates(classification_run_id,candidate_id,backfill_id)`,
}

func HistoryBackfillV29Statements() []string {
	return append([]string(nil), historyBackfillV29Statements...)
}

func HistoryBackfillV29TableNames() []string {
	return []string{
		"transcript_history_backfill_runs",
		"transcript_history_backfill_candidates",
		"transcript_history_backfill_cursor_map",
	}
}

func HistoryBackfillV29ObjectNames() []string {
	return append(HistoryBackfillV29TableNames(),
		"transcript_history_backfill_stream", "transcript_history_backfill_candidate_source",
	)
}
