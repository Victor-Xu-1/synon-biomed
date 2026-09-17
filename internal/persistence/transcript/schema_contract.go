package transcript

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const (
	SchemaContractID                = "synon.transcript.v23"
	ArtifactAssociationContractID   = "synon.transcript.artifacts.v24"
	BranchLineageContractID         = "synon.transcript.branches.v25"
	HistoryClassificationContractID = "synon.transcript.history-classification.v27"
)

var schemaV23Statements = []string{
	`CREATE TABLE transcript_streams (
		stream_uid TEXT PRIMARY KEY,
		owner_id TEXT NOT NULL,
		external_id TEXT NOT NULL,
		session_id TEXT NOT NULL DEFAULT '',
		kind TEXT NOT NULL CHECK(kind IN ('frame_ref','taskrun','standalone')),
		project_id TEXT NOT NULL DEFAULT '',
		root_frame_id TEXT NOT NULL DEFAULT '',
		frame_id TEXT NOT NULL DEFAULT '',
		epoch INTEGER NOT NULL CHECK(epoch > 0),
		input_revision INTEGER NOT NULL DEFAULT 0,
		consumed_input_revision INTEGER NOT NULL DEFAULT 0,
		next_event_id INTEGER NOT NULL DEFAULT 1,
		next_publication_seq INTEGER NOT NULL DEFAULT 1,
		next_checkpoint_sequence INTEGER NOT NULL DEFAULT 1,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL,
		UNIQUE(owner_id, external_id, epoch)
	)`,
	`CREATE UNIQUE INDEX transcript_session_epoch
		ON transcript_streams(owner_id,session_id,epoch) WHERE session_id!=''`,
	`CREATE TABLE transcript_runner_attempts (
		stream_uid TEXT NOT NULL REFERENCES transcript_streams(stream_uid) ON DELETE CASCADE,
		attempt INTEGER NOT NULL,
		runner_id TEXT NOT NULL,
		claim_token_sha256 BLOB NOT NULL,
		claimed_input_revision INTEGER NOT NULL,
		resume_source TEXT NOT NULL CHECK(resume_source IN ('fresh','checkpoint','retry','user_input')),
		resume_checkpoint_sequence INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL CHECK(status IN ('running','completed','failed','cancelled','reclaimed')),
		phase TEXT NOT NULL CHECK(phase IN ('claimed','planning','executing','waiting_user','waiting_approval','waiting_external','terminal')),
		phase_sequence INTEGER NOT NULL DEFAULT 1,
		last_checkpoint_sequence INTEGER NOT NULL DEFAULT 0,
		claimed_at TIMESTAMP NOT NULL,
		expires_at TIMESTAMP NOT NULL,
		finished_event_id INTEGER,
		finished_at TIMESTAMP,
		PRIMARY KEY(stream_uid, attempt),
		FOREIGN KEY(stream_uid,attempt,finished_event_id)
			REFERENCES transcript_events(stream_uid,runner_attempt,event_id),
		CHECK(
			(status='running' AND phase!='terminal' AND finished_event_id IS NULL AND finished_at IS NULL) OR
			(status IN ('completed','failed','cancelled') AND phase='terminal' AND finished_event_id IS NOT NULL AND finished_at IS NOT NULL) OR
			(status='reclaimed' AND phase='terminal' AND finished_event_id IS NOT NULL AND finished_at IS NOT NULL)
		)
	)`,
	`CREATE UNIQUE INDEX transcript_one_running_attempt
		ON transcript_runner_attempts(stream_uid) WHERE status='running'`,
	`CREATE TABLE transcript_events (
		stream_uid TEXT NOT NULL REFERENCES transcript_streams(stream_uid) ON DELETE CASCADE,
		event_id INTEGER NOT NULL,
		publication_seq INTEGER NOT NULL,
		client_message_id TEXT NOT NULL,
		event_type TEXT NOT NULL,
		source TEXT NOT NULL CHECK(source IN ('frame_ref','payload')),
		runner_attempt INTEGER,
		payload_json BLOB,
		frame_event_id TEXT,
		created_at TIMESTAMP NOT NULL,
		PRIMARY KEY(stream_uid, event_id),
		UNIQUE(stream_uid, publication_seq),
		UNIQUE(stream_uid, client_message_id),
		UNIQUE(stream_uid, runner_attempt, event_id),
		FOREIGN KEY(stream_uid,runner_attempt) REFERENCES transcript_runner_attempts(stream_uid,attempt) ON DELETE CASCADE,
		CHECK((source='frame_ref' AND frame_event_id IS NOT NULL AND payload_json IS NULL) OR
			(source='payload' AND frame_event_id IS NULL AND payload_json IS NOT NULL))
	)`,
	`CREATE TABLE transcript_runner_receipts (
		stream_uid TEXT NOT NULL,
		attempt INTEGER NOT NULL,
		event_id INTEGER NOT NULL,
		status TEXT NOT NULL CHECK(status IN ('completed','failed','cancelled','reclaimed')),
		finished_at TIMESTAMP NOT NULL,
		PRIMARY KEY(stream_uid, attempt),
		FOREIGN KEY(stream_uid,attempt,event_id)
			REFERENCES transcript_events(stream_uid,runner_attempt,event_id) ON DELETE CASCADE
	)`,
	`CREATE TABLE transcript_runner_checkpoints (
		stream_uid TEXT NOT NULL,
		checkpoint_sequence INTEGER NOT NULL,
		runner_attempt INTEGER NOT NULL,
		event_id INTEGER NOT NULL,
		phase TEXT NOT NULL CHECK(phase IN ('planning','executing','waiting_user','waiting_approval','waiting_external')),
		resumable INTEGER NOT NULL CHECK(resumable IN (0,1)),
		created_at TIMESTAMP NOT NULL,
		PRIMARY KEY(stream_uid,checkpoint_sequence),
		UNIQUE(stream_uid,runner_attempt,event_id),
		FOREIGN KEY(stream_uid,runner_attempt,event_id)
			REFERENCES transcript_events(stream_uid,runner_attempt,event_id) ON DELETE CASCADE
	)`,
	`CREATE TABLE transcript_delivery_routes (
		stream_uid TEXT NOT NULL REFERENCES transcript_streams(stream_uid) ON DELETE CASCADE,
		destination TEXT NOT NULL,
		current_generation INTEGER NOT NULL CHECK(current_generation > 0),
		status TEXT NOT NULL CHECK(status IN ('active','revoked')),
		updated_at TIMESTAMP NOT NULL,
		PRIMARY KEY(stream_uid,destination)
	)`,
	`CREATE TABLE transcript_delivery_intents (
		stream_uid TEXT NOT NULL,
		publication_seq INTEGER NOT NULL,
		destination TEXT NOT NULL,
		route_generation INTEGER NOT NULL,
		status TEXT NOT NULL CHECK(status IN ('pending','inflight','delivered','failed','revoked')),
		attempt_count INTEGER NOT NULL DEFAULT 0,
		claim_token_sha256 BLOB,
		claimed_by TEXT,
		lease_expires_at TIMESTAMP,
		next_attempt_at TIMESTAMP,
		last_error_code TEXT NOT NULL DEFAULT '',
		last_retry_after_ns INTEGER NOT NULL DEFAULT 0,
		last_max_attempts INTEGER NOT NULL DEFAULT 0,
		delivered_at TIMESTAMP,
		updated_at TIMESTAMP NOT NULL,
		PRIMARY KEY(stream_uid,publication_seq,destination,route_generation),
		FOREIGN KEY(stream_uid,publication_seq) REFERENCES transcript_events(stream_uid,publication_seq) ON DELETE CASCADE
	)`,
	`CREATE INDEX transcript_delivery_ready
		ON transcript_delivery_intents(destination,status,next_attempt_at,lease_expires_at,stream_uid,publication_seq)`,
}

var schemaV23TableNames = []string{
	"transcript_streams",
	"transcript_runner_attempts",
	"transcript_events",
	"transcript_runner_receipts",
	"transcript_runner_checkpoints",
	"transcript_delivery_routes",
	"transcript_delivery_intents",
}

var schemaV23ObjectNames = append(append([]string(nil), schemaV23TableNames...),
	"transcript_session_epoch",
	"transcript_one_running_attempt",
	"transcript_delivery_ready",
)

var artifactV24Statements = []string{
	`CREATE TABLE transcript_artifact_commits (
		stream_uid TEXT NOT NULL,
		runner_attempt INTEGER NOT NULL,
		source_event_id INTEGER NOT NULL,
		ordinal INTEGER NOT NULL,
		artifact_id TEXT NOT NULL,
		version_id TEXT NOT NULL,
		relation TEXT NOT NULL CHECK(relation IN ('produced','consumed','cited','attached')),
		bound_event_id INTEGER,
		created_at TIMESTAMP NOT NULL,
		PRIMARY KEY(stream_uid,runner_attempt,source_event_id,artifact_id,version_id),
		UNIQUE(stream_uid,runner_attempt,source_event_id,ordinal),
		FOREIGN KEY(stream_uid,runner_attempt,source_event_id)
			REFERENCES transcript_events(stream_uid,runner_attempt,event_id) ON DELETE RESTRICT,
		FOREIGN KEY(stream_uid,runner_attempt,bound_event_id)
			REFERENCES transcript_events(stream_uid,runner_attempt,event_id) ON DELETE RESTRICT
	)`,
	`CREATE INDEX transcript_artifact_commits_pending
		ON transcript_artifact_commits(stream_uid,runner_attempt,bound_event_id,source_event_id,ordinal)`,
	`CREATE INDEX transcript_artifact_commits_version
		ON transcript_artifact_commits(artifact_id,version_id,stream_uid,runner_attempt,source_event_id)`,
	`CREATE TABLE transcript_artifact_refs (
		stream_uid TEXT NOT NULL,
		runner_attempt INTEGER NOT NULL,
		source_event_id INTEGER NOT NULL,
		ordinal INTEGER NOT NULL,
		artifact_id TEXT NOT NULL,
		version_id TEXT NOT NULL,
		relation TEXT NOT NULL CHECK(relation IN ('produced','consumed','cited','attached')),
		availability TEXT NOT NULL CHECK(availability IN ('available','deleted','missing')),
		created_at TIMESTAMP NOT NULL,
		PRIMARY KEY(stream_uid,runner_attempt,source_event_id,artifact_id,version_id),
		UNIQUE(stream_uid,runner_attempt,source_event_id,ordinal),
		FOREIGN KEY(stream_uid,runner_attempt,source_event_id)
			REFERENCES transcript_events(stream_uid,runner_attempt,event_id) ON DELETE RESTRICT
	)`,
	`CREATE INDEX transcript_artifact_refs_source
		ON transcript_artifact_refs(stream_uid,runner_attempt,source_event_id,ordinal)`,
	`CREATE INDEX transcript_artifact_refs_version
		ON transcript_artifact_refs(artifact_id,version_id,stream_uid,runner_attempt,source_event_id)`,
}

var branchV25Statements = []string{
	`CREATE TABLE transcript_branches (
		stream_uid TEXT NOT NULL REFERENCES transcript_streams(stream_uid) ON DELETE CASCADE,
		branch_id TEXT NOT NULL,
		parent_branch_id TEXT,
		fork_event_id INTEGER,
		fork_point INTEGER NOT NULL CHECK(fork_point >= 0),
		kind TEXT NOT NULL CHECK(kind IN ('base','edit','answer')),
		client_mutation_id TEXT NOT NULL,
		request_sha256 BLOB NOT NULL CHECK(length(request_sha256)=32),
		source_message_id TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL,
		PRIMARY KEY(stream_uid,branch_id),
		UNIQUE(stream_uid,client_mutation_id),
		FOREIGN KEY(stream_uid,parent_branch_id)
			REFERENCES transcript_branches(stream_uid,branch_id)
			ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED,
		FOREIGN KEY(stream_uid,fork_event_id)
			REFERENCES transcript_events(stream_uid,event_id)
			ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED,
		CHECK(length(branch_id)=11 AND branch_id GLOB 'br_[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]'),
		CHECK(length(client_mutation_id)>0),
		CHECK(
			(kind='base' AND parent_branch_id IS NULL AND fork_event_id IS NULL AND fork_point=0) OR
			(kind IN ('edit','answer') AND parent_branch_id IS NOT NULL AND fork_event_id IS NOT NULL AND length(source_message_id)>0)
		)
	)`,
	`CREATE TABLE transcript_branch_state (
		stream_uid TEXT PRIMARY KEY REFERENCES transcript_streams(stream_uid) ON DELETE CASCADE,
		active_branch_id TEXT NOT NULL,
		generation INTEGER NOT NULL CHECK(generation > 0),
		updated_at TIMESTAMP NOT NULL,
		FOREIGN KEY(stream_uid,active_branch_id)
			REFERENCES transcript_branches(stream_uid,branch_id)
			ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED
	)`,
	`CREATE TABLE transcript_branch_events (
		stream_uid TEXT NOT NULL,
		branch_id TEXT NOT NULL,
		ordinal INTEGER NOT NULL CHECK(ordinal > 0),
		event_id INTEGER NOT NULL,
		PRIMARY KEY(stream_uid,branch_id,ordinal),
		UNIQUE(stream_uid,branch_id,event_id),
		FOREIGN KEY(stream_uid,branch_id)
			REFERENCES transcript_branches(stream_uid,branch_id) ON DELETE CASCADE,
		FOREIGN KEY(stream_uid,event_id)
			REFERENCES transcript_events(stream_uid,event_id)
			ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED
	)`,
	`CREATE INDEX transcript_branch_events_event
		ON transcript_branch_events(stream_uid,event_id,branch_id)`,
}

var historyClassificationV27Statements = []string{
	`CREATE TABLE transcript_history_classification_runs (
		run_id BLOB PRIMARY KEY CHECK(length(run_id) = 32),
		stream_uid TEXT NOT NULL,
		branch_id TEXT NOT NULL,
		branch_generation INTEGER NOT NULL CHECK(branch_generation > 0),
		contract_version INTEGER NOT NULL CHECK(contract_version = 1),
		through_ordinal INTEGER NOT NULL CHECK(through_ordinal >= 0),
		through_publication_seq INTEGER NOT NULL CHECK(through_publication_seq >= 0),
		source_sha256 BLOB NOT NULL CHECK(length(source_sha256) = 32),
		scan_status TEXT NOT NULL CHECK(scan_status IN ('complete','blocked')),
		scan_reason TEXT NOT NULL CHECK(scan_reason IN ('none','source_unavailable','branch_stale','contract_violation')),
		candidate_count INTEGER NOT NULL CHECK(candidate_count >= 0),
		native_count INTEGER NOT NULL CHECK(native_count >= 0),
		eligible_count INTEGER NOT NULL CHECK(eligible_count >= 0),
		poison_count INTEGER NOT NULL CHECK(poison_count >= 0),
		conflict_count INTEGER NOT NULL CHECK(conflict_count >= 0),
		classified_at TIMESTAMP NOT NULL,
		UNIQUE(stream_uid,branch_id,branch_generation,contract_version,through_ordinal,through_publication_seq,source_sha256),
		CHECK(candidate_count = native_count + eligible_count + poison_count + conflict_count),
		FOREIGN KEY(stream_uid,branch_id)
			REFERENCES transcript_branches(stream_uid,branch_id) ON DELETE CASCADE
	)`,
	`CREATE TABLE transcript_history_classification_candidates (
		run_id BLOB NOT NULL REFERENCES transcript_history_classification_runs(run_id) ON DELETE CASCADE,
		candidate_id BLOB NOT NULL CHECK(length(candidate_id) = 32),
		first_ordinal INTEGER NOT NULL CHECK(first_ordinal > 0),
		tool_use_id TEXT NOT NULL,
		tool_event_id INTEGER CHECK(tool_event_id > 0),
		result_event_id INTEGER CHECK(result_event_id > 0),
		runner_attempt INTEGER CHECK(runner_attempt > 0),
		disposition TEXT NOT NULL CHECK(disposition IN ('native_v1','eligible','poison','conflict')),
			reason_code TEXT NOT NULL CHECK(reason_code IN (
			'native_v1','structured_legacy','missing_runner_attempt','ambiguous_prose',
			'invalid_questions','answer_key_mismatch','malformed_fact','incomplete_pair','incomplete_lineage',
			'duplicate_fact','origin_conflict'
		)),
		state_json BLOB CHECK(state_json IS NULL OR json_valid(state_json)),
		evidence_json BLOB NOT NULL CHECK(json_valid(evidence_json)),
		evidence_sha256 BLOB NOT NULL CHECK(length(evidence_sha256) = 32),
		PRIMARY KEY(run_id,candidate_id)
	)`,
	`CREATE TABLE transcript_history_shadow_comparisons (
		run_id BLOB NOT NULL REFERENCES transcript_history_classification_runs(run_id) ON DELETE CASCADE,
		dimension TEXT NOT NULL CHECK(dimension IN ('stable_ids','branch_order','ask_user_states','terminal_facts','artifact_refs')),
		verdict TEXT NOT NULL CHECK(verdict IN ('match','mismatch','blocked')),
		legacy_sha256 BLOB NOT NULL CHECK(length(legacy_sha256) = 32),
		candidate_sha256 BLOB CHECK(candidate_sha256 IS NULL OR length(candidate_sha256) = 32),
		reason_code TEXT NOT NULL CHECK(reason_code IN ('none','canonical_unavailable','legacy_unavailable','digest_mismatch','not_compared')),
		evidence_json BLOB NOT NULL CHECK(json_valid(evidence_json)),
		compared_at TIMESTAMP NOT NULL,
		PRIMARY KEY(run_id,dimension),
		CHECK((verdict='blocked' AND candidate_sha256 IS NULL) OR
			(verdict IN ('match','mismatch') AND candidate_sha256 IS NOT NULL))
	)`,
	`CREATE INDEX transcript_history_classification_runs_latest
		ON transcript_history_classification_runs(stream_uid,branch_id,contract_version,through_publication_seq DESC,run_id)`,
	`CREATE INDEX transcript_history_classification_findings
		ON transcript_history_classification_candidates(disposition,reason_code,run_id,candidate_id)`,
}

func SchemaContractStatements() []string {
	statements := SchemaV23Statements()
	statements = append(statements, artifactV24Statements...)
	statements = append(statements, branchV25Statements...)
	statements = append(statements, historyClassificationV27Statements...)
	statements = append(statements, historyBackfillV29Statements...)
	statements = append(statements, historyCutoverV30Statements...)
	statements = append(statements, historyActivationV31Statements...)
	statements = append(statements, payloadGenesisV32Statements...)
	statements = append(statements, historyOrdinaryV33Statements...)
	statements = append(statements, historyOrdinaryCursorV34Statements...)
	statements = append(statements, typedHistoryBootstrapV35Statements...)
	return append(statements, webReadModelV38Statements...)
}

func SchemaV23Statements() []string {
	return append([]string(nil), schemaV23Statements...)
}

func ArtifactV24Statements() []string {
	return append([]string(nil), artifactV24Statements...)
}

func BranchV25Statements() []string {
	return append([]string(nil), branchV25Statements...)
}

func BranchV25ObjectNames() []string {
	return []string{
		"transcript_branches",
		"transcript_branch_state",
		"transcript_branch_events",
		"transcript_branch_events_event",
	}
}

func BranchV25CanonicalStatements() []string {
	return BranchV25Statements()
}

func HistoryClassificationV27Statements() []string {
	return append([]string(nil), historyClassificationV27Statements...)
}

func SchemaV23TableNames() []string {
	return append([]string(nil), schemaV23TableNames...)
}

func SchemaV23ObjectNames() []string {
	return append([]string(nil), schemaV23ObjectNames...)
}

func SchemaV24TableNames() []string {
	return append(SchemaV23TableNames(), "transcript_artifact_commits", "transcript_artifact_refs")
}

func SchemaV24ObjectNames() []string {
	return append(SchemaV23ObjectNames(),
		"transcript_artifact_commits", "transcript_artifact_commits_pending", "transcript_artifact_commits_version",
		"transcript_artifact_refs", "transcript_artifact_refs_source", "transcript_artifact_refs_version",
	)
}

func SchemaV25TableNames() []string {
	return append(SchemaV24TableNames(), "transcript_branches", "transcript_branch_state", "transcript_branch_events")
}

func SchemaV25ObjectNames() []string {
	return append(SchemaV24ObjectNames(),
		"transcript_branches", "transcript_branch_state", "transcript_branch_events", "transcript_branch_events_event",
	)
}

func HistoryClassificationV27TableNames() []string {
	return []string{
		"transcript_history_classification_runs",
		"transcript_history_classification_candidates",
		"transcript_history_shadow_comparisons",
	}
}

func HistoryClassificationV27ObjectNames() []string {
	return append(HistoryClassificationV27TableNames(),
		"transcript_history_classification_runs_latest", "transcript_history_classification_findings",
	)
}

func SchemaContractSHA256() string {
	digest := sha256.Sum256([]byte(strings.Join(SchemaContractStatements(), "\x00")))
	return hex.EncodeToString(digest[:])
}

func SchemaV23SHA256() string {
	digest := sha256.Sum256([]byte(strings.Join(schemaV23Statements, "\x00")))
	return hex.EncodeToString(digest[:])
}

func ArtifactV24SHA256() string {
	digest := sha256.Sum256([]byte(strings.Join(artifactV24Statements, "\x00")))
	return hex.EncodeToString(digest[:])
}

func BranchV25SHA256() string {
	digest := sha256.Sum256([]byte(strings.Join(branchV25Statements, "\x00")))
	return hex.EncodeToString(digest[:])
}
