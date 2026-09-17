package transcript

const HistoryCutoverContractID = "synon.transcript.history-cutover.v30"

var historyCutoverV30Statements = []string{
	`CREATE TABLE transcript_history_cutover_runs (
		cutover_id BLOB PRIMARY KEY CHECK(length(cutover_id) = 32),
		backfill_id BLOB NOT NULL
			REFERENCES transcript_history_backfill_runs(backfill_id) ON DELETE NO ACTION,
		supersedes_cutover_id BLOB
			REFERENCES transcript_history_cutover_runs(cutover_id) ON DELETE NO ACTION,
		stream_uid TEXT NOT NULL,
		owner_id TEXT NOT NULL,
		source_branch_id TEXT NOT NULL,
		source_generation INTEGER NOT NULL CHECK(source_generation > 0),
		source_through_publication_seq INTEGER NOT NULL CHECK(source_through_publication_seq >= 0),
		contract_version INTEGER NOT NULL CHECK(contract_version = 1),
		verification_sha256 BLOB NOT NULL CHECK(length(verification_sha256) = 32),
		lineage_sha256 BLOB NOT NULL CHECK(length(lineage_sha256) = 32),
		cursor_sha256 BLOB NOT NULL CHECK(length(cursor_sha256) = 32),
		shadow_sha256 BLOB NOT NULL CHECK(length(shadow_sha256) = 32),
		branch_count INTEGER NOT NULL CHECK(branch_count > 0),
		event_count INTEGER NOT NULL CHECK(event_count > 0),
		cursor_count INTEGER NOT NULL CHECK(cursor_count > 0),
		prior_read_authority TEXT NOT NULL CHECK(prior_read_authority = 'legacy_mixed_v1'),
		target_read_authority TEXT NOT NULL CHECK(target_read_authority = 'transcript_payload_v1'),
		status TEXT NOT NULL CHECK(status IN ('ready','superseded')),
		activated INTEGER NOT NULL CHECK(activated = 0),
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL,
		FOREIGN KEY(stream_uid,source_branch_id)
			REFERENCES transcript_branches(stream_uid,branch_id) ON DELETE NO ACTION
	)`,
	`CREATE TABLE transcript_history_cutover_events (
		cutover_id BLOB NOT NULL REFERENCES transcript_history_cutover_runs(cutover_id) ON DELETE CASCADE,
		target_event_key TEXT NOT NULL,
		source_event_id INTEGER,
		candidate_id BLOB CHECK(candidate_id IS NULL OR length(candidate_id) = 32),
		fact_kind TEXT NOT NULL CHECK(fact_kind IN ('existing','ask_user_prompt','ask_user_pending','ask_user_result')),
		target_publication_seq INTEGER NOT NULL CHECK(target_publication_seq > 0),
		client_message_id TEXT NOT NULL,
		event_type TEXT NOT NULL,
		runner_attempt INTEGER CHECK(runner_attempt IS NULL OR runner_attempt > 0),
		payload_json BLOB NOT NULL CHECK(json_valid(payload_json)),
		payload_sha256 BLOB NOT NULL CHECK(length(payload_sha256) = 32),
		created_at TIMESTAMP NOT NULL,
		PRIMARY KEY(cutover_id,target_event_key),
		UNIQUE(cutover_id,target_publication_seq),
		UNIQUE(cutover_id,client_message_id),
		CHECK(length(target_event_key) > 0),
		CHECK(length(client_message_id) > 0),
		CHECK(length(event_type) > 0)
	)`,
	`CREATE TABLE transcript_history_cutover_branches (
		cutover_id BLOB NOT NULL REFERENCES transcript_history_cutover_runs(cutover_id) ON DELETE CASCADE,
		source_branch_id TEXT NOT NULL,
		target_branch_id TEXT NOT NULL,
		parent_source_branch_id TEXT,
		parent_target_branch_id TEXT,
		kind TEXT NOT NULL CHECK(kind IN ('base','edit','answer')),
		source_fork_event_id INTEGER,
		target_fork_event_key TEXT,
		fork_point INTEGER NOT NULL CHECK(fork_point >= 0),
		client_mutation_id TEXT NOT NULL,
		request_sha256 BLOB NOT NULL CHECK(length(request_sha256) = 32),
		source_message_id TEXT NOT NULL,
		source_through_ordinal INTEGER NOT NULL CHECK(source_through_ordinal >= 0),
		source_through_publication_seq INTEGER NOT NULL CHECK(source_through_publication_seq >= 0),
		source_membership_sha256 BLOB NOT NULL CHECK(length(source_membership_sha256) = 32),
		target_membership_sha256 BLOB NOT NULL CHECK(length(target_membership_sha256) = 32),
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL,
		PRIMARY KEY(cutover_id,source_branch_id),
		UNIQUE(cutover_id,target_branch_id),
		FOREIGN KEY(cutover_id,parent_source_branch_id)
			REFERENCES transcript_history_cutover_branches(cutover_id,source_branch_id)
			ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED,
		FOREIGN KEY(cutover_id,parent_target_branch_id)
			REFERENCES transcript_history_cutover_branches(cutover_id,target_branch_id)
			ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED
	)`,
	`CREATE TABLE transcript_history_cutover_receipts (
		cutover_id BLOB NOT NULL REFERENCES transcript_history_cutover_runs(cutover_id) ON DELETE CASCADE,
		source_attempt INTEGER NOT NULL CHECK(source_attempt > 0),
		target_event_key TEXT NOT NULL,
		status TEXT NOT NULL CHECK(status IN ('completed','failed','cancelled')),
		finished_at TIMESTAMP NOT NULL,
		PRIMARY KEY(cutover_id,source_attempt),
		FOREIGN KEY(cutover_id,target_event_key)
			REFERENCES transcript_history_cutover_events(cutover_id,target_event_key) ON DELETE NO ACTION
	)`,
	`CREATE TABLE transcript_history_cutover_artifact_refs (
		cutover_id BLOB NOT NULL,
		target_event_key TEXT NOT NULL,
		ordinal INTEGER NOT NULL CHECK(ordinal > 0),
		artifact_id TEXT NOT NULL,
		version_id TEXT NOT NULL,
		relation TEXT NOT NULL CHECK(relation IN ('produced','consumed','cited','attached')),
		availability TEXT NOT NULL CHECK(availability IN ('available','deleted','missing')),
		created_at TIMESTAMP NOT NULL,
		PRIMARY KEY(cutover_id,target_event_key,artifact_id,version_id),
		UNIQUE(cutover_id,target_event_key,ordinal),
		FOREIGN KEY(cutover_id,target_event_key)
			REFERENCES transcript_history_cutover_events(cutover_id,target_event_key) ON DELETE NO ACTION
	)`,
	`CREATE TABLE transcript_history_cutover_branch_events (
		cutover_id BLOB NOT NULL,
		target_branch_id TEXT NOT NULL,
		target_ordinal INTEGER NOT NULL CHECK(target_ordinal > 0),
		target_event_key TEXT NOT NULL,
		source_ordinal INTEGER CHECK(source_ordinal IS NULL OR source_ordinal > 0),
		PRIMARY KEY(cutover_id,target_branch_id,target_ordinal),
		UNIQUE(cutover_id,target_branch_id,target_event_key),
		FOREIGN KEY(cutover_id,target_branch_id)
			REFERENCES transcript_history_cutover_branches(cutover_id,target_branch_id) ON DELETE CASCADE,
		FOREIGN KEY(cutover_id,target_event_key)
			REFERENCES transcript_history_cutover_events(cutover_id,target_event_key) ON DELETE NO ACTION
	)`,
	`CREATE TABLE transcript_history_cutover_cursor_map (
		cutover_id BLOB NOT NULL,
		source_branch_id TEXT NOT NULL,
		target_branch_id TEXT NOT NULL,
		source_generation INTEGER NOT NULL CHECK(source_generation > 0),
		source_through_publication_seq INTEGER NOT NULL CHECK(source_through_publication_seq >= 0),
		source_message_index INTEGER NOT NULL CHECK(source_message_index >= 0),
		source_ordinal INTEGER NOT NULL CHECK(source_ordinal > 0),
		stable_message_id TEXT NOT NULL,
		target_publication_seq INTEGER NOT NULL CHECK(target_publication_seq > 0),
		target_message_index INTEGER NOT NULL CHECK(target_message_index >= 0),
		PRIMARY KEY(cutover_id,source_branch_id,source_message_index),
		FOREIGN KEY(cutover_id,source_branch_id)
			REFERENCES transcript_history_cutover_branches(cutover_id,source_branch_id) ON DELETE CASCADE,
		FOREIGN KEY(cutover_id,target_branch_id)
			REFERENCES transcript_history_cutover_branches(cutover_id,target_branch_id) ON DELETE CASCADE,
		FOREIGN KEY(cutover_id,target_publication_seq)
			REFERENCES transcript_history_cutover_events(cutover_id,target_publication_seq) ON DELETE NO ACTION
	)`,
	`CREATE TABLE transcript_history_cutover_shadow_comparisons (
		cutover_id BLOB NOT NULL REFERENCES transcript_history_cutover_runs(cutover_id) ON DELETE CASCADE,
		source_branch_id TEXT NOT NULL,
		dimension TEXT NOT NULL CHECK(dimension IN ('stable_ids','branch_order','ask_user_states','terminal_facts','artifact_refs')),
		verdict TEXT NOT NULL CHECK(verdict = 'match'),
		legacy_sha256 BLOB NOT NULL CHECK(length(legacy_sha256) = 32),
		target_sha256 BLOB NOT NULL CHECK(length(target_sha256) = 32),
		compared_at TIMESTAMP NOT NULL,
		PRIMARY KEY(cutover_id,source_branch_id,dimension),
		FOREIGN KEY(cutover_id,source_branch_id)
			REFERENCES transcript_history_cutover_branches(cutover_id,source_branch_id) ON DELETE CASCADE,
		CHECK(legacy_sha256 = target_sha256)
	)`,
	`CREATE INDEX transcript_history_cutover_stream
		ON transcript_history_cutover_runs(stream_uid,source_branch_id,source_generation,cutover_id)`,
	`CREATE INDEX transcript_history_cutover_ready
		ON transcript_history_cutover_runs(backfill_id,status,updated_at,cutover_id)`,
	`CREATE INDEX transcript_history_cutover_cursor_target
		ON transcript_history_cutover_cursor_map(cutover_id,target_branch_id,target_message_index)`,
}

func HistoryCutoverV30Statements() []string {
	return append([]string(nil), historyCutoverV30Statements...)
}

func HistoryCutoverV30TableNames() []string {
	return []string{
		"transcript_history_cutover_runs", "transcript_history_cutover_events",
		"transcript_history_cutover_branches", "transcript_history_cutover_receipts",
		"transcript_history_cutover_artifact_refs", "transcript_history_cutover_branch_events",
		"transcript_history_cutover_cursor_map", "transcript_history_cutover_shadow_comparisons",
	}
}

func HistoryCutoverV30ObjectNames() []string {
	return append(HistoryCutoverV30TableNames(),
		"transcript_history_cutover_stream", "transcript_history_cutover_ready",
		"transcript_history_cutover_cursor_target",
	)
}
