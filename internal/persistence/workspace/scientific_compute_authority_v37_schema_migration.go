package workspace

import (
	"context"
	"errors"
)

const (
	scientificComputeAuthorityV37CallbackID        = "scientific-compute-authority-v37-noop"
	scientificComputeAuthorityV37PreflightIdentity = "compute-workbench-jobs-v1"
	scientificComputeAuthorityV37RuleSpec          = "synon.workspace.scientific-compute-authority.v37"
	scientificComputeAdmissionsDDL                 = `CREATE TABLE scientific_compute_admissions (
		job_id TEXT PRIMARY KEY REFERENCES compute_workbench_jobs(job_id) ON DELETE RESTRICT,
		owner_user_id TEXT NOT NULL,
		project_id TEXT NOT NULL,
		frame_id TEXT NOT NULL,
		root_frame_id TEXT NOT NULL,
		stream_uid TEXT NOT NULL,
		stream_epoch INTEGER NOT NULL CHECK(stream_epoch>0),
		runner_claim_sha256 TEXT NOT NULL CHECK(length(runner_claim_sha256)=64),
		attempt INTEGER NOT NULL CHECK(attempt>0),
		generation INTEGER NOT NULL CHECK(generation>0),
		provider TEXT NOT NULL,
		environment TEXT NOT NULL,
		capability TEXT NOT NULL,
		engine TEXT NOT NULL,
		engine_version TEXT NOT NULL,
		measurement_parser TEXT NOT NULL,
		score_kind TEXT NOT NULL,
		score_unit TEXT NOT NULL,
		profile_manifest_sha256 TEXT NOT NULL CHECK(length(profile_manifest_sha256)=64),
		code_sha256 TEXT NOT NULL CHECK(length(code_sha256)=64),
		environment_sha256 TEXT NOT NULL CHECK(length(environment_sha256)=64),
		weights_sha256 TEXT CHECK(weights_sha256 IS NULL OR length(weights_sha256)=64),
		weights_required INTEGER NOT NULL CHECK(weights_required IN (0,1)),
		request_sha256 TEXT NOT NULL CHECK(length(request_sha256)=64),
		state TEXT NOT NULL CHECK(state IN ('admitted','completed','failed')),
		created_at TEXT NOT NULL,
		completed_at TEXT,
		UNIQUE(owner_user_id,project_id,frame_id,job_id,attempt,generation)
	) STRICT`
	scientificComputeAdmissionInputsDDL = `CREATE TABLE scientific_compute_admission_inputs (
		job_id TEXT NOT NULL REFERENCES scientific_compute_admissions(job_id) ON DELETE CASCADE,
		kind TEXT NOT NULL,
		artifact_id TEXT NOT NULL REFERENCES artifacts(id) ON DELETE RESTRICT,
		version_id TEXT NOT NULL REFERENCES artifact_versions(id) ON DELETE RESTRICT,
		sha256 TEXT NOT NULL CHECK(length(sha256)=64),
		PRIMARY KEY(job_id,kind),
		UNIQUE(job_id,version_id)
	) STRICT`
	scientificComputeAdmissionOutputsDDL = `CREATE TABLE scientific_compute_admission_outputs (
		job_id TEXT NOT NULL REFERENCES scientific_compute_admissions(job_id) ON DELETE CASCADE,
		kind TEXT NOT NULL,
		relative_path TEXT NOT NULL,
		PRIMARY KEY(job_id,kind),
		UNIQUE(job_id,relative_path)
	) STRICT`
	scientificComputeReceiptsDDL = `CREATE TABLE scientific_compute_receipts (
		job_id TEXT PRIMARY KEY REFERENCES scientific_compute_admissions(job_id) ON DELETE CASCADE,
		owner_user_id TEXT NOT NULL,
		project_id TEXT NOT NULL,
		frame_id TEXT NOT NULL,
		root_frame_id TEXT NOT NULL,
		stream_uid TEXT NOT NULL,
		stream_epoch INTEGER NOT NULL CHECK(stream_epoch>0),
		runner_claim_sha256 TEXT NOT NULL CHECK(length(runner_claim_sha256)=64),
		attempt INTEGER NOT NULL CHECK(attempt>0),
		generation INTEGER NOT NULL CHECK(generation>0),
		completion_authority_seq INTEGER NOT NULL CHECK(completion_authority_seq>0),
		measurement_parser TEXT NOT NULL,
		score_kind TEXT NOT NULL,
		score_value REAL NOT NULL,
		score_unit TEXT NOT NULL,
		payload_json TEXT NOT NULL,
		payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256)=64),
		completed_at TEXT NOT NULL,
		UNIQUE(owner_user_id,project_id,frame_id,job_id,attempt,generation),
		FOREIGN KEY(job_id,completion_authority_seq) REFERENCES scientific_compute_handle_authorities(job_id,authority_seq) ON DELETE RESTRICT
	) STRICT`
	scientificComputeReceiptOutputsDDL = `CREATE TABLE scientific_compute_receipt_outputs (
		job_id TEXT NOT NULL REFERENCES scientific_compute_receipts(job_id) ON DELETE CASCADE,
		kind TEXT NOT NULL,
		relative_path TEXT NOT NULL,
		sha256 TEXT NOT NULL CHECK(length(sha256)=64),
		size_bytes INTEGER NOT NULL CHECK(size_bytes>=0),
		PRIMARY KEY(job_id,kind),
		UNIQUE(job_id,relative_path)
	) STRICT`
	scientificComputeHandleAuthoritiesDDL = `CREATE TABLE scientific_compute_handle_authorities (
		job_id TEXT NOT NULL REFERENCES scientific_compute_admissions(job_id) ON DELETE CASCADE,
		authority_seq INTEGER NOT NULL CHECK(authority_seq>0),
		owner_user_id TEXT NOT NULL,
		project_id TEXT NOT NULL,
		frame_id TEXT NOT NULL,
		root_frame_id TEXT NOT NULL,
		stream_uid TEXT NOT NULL,
		stream_epoch INTEGER NOT NULL CHECK(stream_epoch>0),
		runner_id TEXT NOT NULL,
		runner_attempt INTEGER NOT NULL CHECK(runner_attempt>0),
		runner_claim_sha256 TEXT NOT NULL CHECK(length(runner_claim_sha256)=64),
		claimed_input_revision INTEGER NOT NULL CHECK(claimed_input_revision>0),
		resume_source TEXT NOT NULL CHECK(resume_source IN ('fresh','checkpoint','retry','user_input')),
		resume_checkpoint_sequence INTEGER NOT NULL CHECK(resume_checkpoint_sequence>=0),
		source_kind TEXT NOT NULL CHECK(source_kind IN ('submission','adoption')),
		previous_authority_seq INTEGER,
		created_at TEXT NOT NULL,
		PRIMARY KEY(job_id,authority_seq),
		UNIQUE(job_id,runner_attempt,runner_claim_sha256),
		FOREIGN KEY(stream_uid,runner_attempt) REFERENCES transcript_runner_attempts(stream_uid,attempt) ON DELETE RESTRICT,
		FOREIGN KEY(job_id,previous_authority_seq) REFERENCES scientific_compute_handle_authorities(job_id,authority_seq) ON DELETE RESTRICT,
		CHECK((authority_seq=1 AND source_kind='submission' AND previous_authority_seq IS NULL) OR
			(authority_seq>1 AND source_kind='adoption' AND previous_authority_seq=authority_seq-1))
	) STRICT`
	scientificComputeHandlesDDL = `CREATE TABLE scientific_compute_handles (
		job_id TEXT PRIMARY KEY REFERENCES scientific_compute_admissions(job_id) ON DELETE CASCADE,
		handle_sha256 TEXT NOT NULL CHECK(length(handle_sha256)=64),
		current_authority_seq INTEGER NOT NULL CHECK(current_authority_seq>0),
		state TEXT NOT NULL CHECK(state IN ('open','closed')),
		created_at TEXT NOT NULL,
		closed_at TEXT,
		UNIQUE(handle_sha256),
		FOREIGN KEY(job_id,current_authority_seq) REFERENCES scientific_compute_handle_authorities(job_id,authority_seq) ON DELETE RESTRICT,
		CHECK((state='open' AND closed_at IS NULL) OR (state='closed' AND closed_at IS NOT NULL))
	) STRICT`
)

var scientificComputeAuthorityV37Migration = versionedSchemaMigration{
	version: 37,
	name:    "scientific-compute-authority",
	statements: []string{
		scientificComputeAdmissionsDDL,
		scientificComputeAdmissionInputsDDL,
		scientificComputeAdmissionOutputsDDL,
		scientificComputeReceiptsDDL,
		scientificComputeReceiptOutputsDDL,
		scientificComputeHandleAuthoritiesDDL,
		scientificComputeHandlesDDL,
		`CREATE INDEX scientific_compute_admissions_runner_idx ON scientific_compute_admissions(owner_user_id,project_id,frame_id,stream_uid,stream_epoch,runner_claim_sha256,attempt,capability)`,
		`CREATE INDEX scientific_compute_receipts_owner_frame_idx ON scientific_compute_receipts(owner_user_id,project_id,frame_id,stream_uid,stream_epoch,runner_claim_sha256,attempt,completed_at,job_id)`,
		`CREATE INDEX scientific_compute_handle_authorities_runner_idx ON scientific_compute_handle_authorities(owner_user_id,project_id,frame_id,stream_uid,stream_epoch,runner_attempt,runner_claim_sha256,job_id,authority_seq)`,
		`CREATE TRIGGER scientific_compute_handle_authorities_immutable BEFORE UPDATE ON scientific_compute_handle_authorities BEGIN SELECT RAISE(ABORT,'scientific compute handle authority is immutable'); END`,
		`CREATE TRIGGER scientific_compute_handle_authorities_append_only BEFORE DELETE ON scientific_compute_handle_authorities BEGIN SELECT RAISE(ABORT,'scientific compute handle authority is append-only'); END`,
		`CREATE TRIGGER scientific_compute_handles_identity_immutable BEFORE UPDATE ON scientific_compute_handles WHEN NEW.job_id!=OLD.job_id OR NEW.handle_sha256!=OLD.handle_sha256 OR NEW.created_at!=OLD.created_at BEGIN SELECT RAISE(ABORT,'scientific compute handle identity is immutable'); END`,
	},
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        scientificComputeAuthorityV37CallbackID,
		RuleSpec:          scientificComputeAuthorityV37RuleSpec,
		PreflightIdentity: scientificComputeAuthorityV37PreflightIdentity,
	},
}

func preflightScientificComputeAuthorityV37(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	var count int
	if err := executor.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sqlite_schema
		WHERE type='table' AND name IN ('compute_workbench_jobs','compute_usage','artifacts','artifact_versions','transcript_streams','transcript_runner_attempts')`,
	).Scan(&count); err != nil {
		return errors.New("inspect scientific compute dependencies")
	}
	if count != 6 {
		return errors.New("compute and artifact schemas are required before scientific compute authority")
	}
	return nil
}
