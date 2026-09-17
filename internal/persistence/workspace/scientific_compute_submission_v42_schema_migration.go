package workspace

import (
	"context"
	"errors"
)

const (
	scientificComputeSubmissionV42CallbackID        = "scientific-compute-submission-v42-noop"
	scientificComputeSubmissionV42PreflightIdentity = "scientific-compute-submission-recovery-v1"
	scientificComputeSubmissionV42RuleSpec          = "synon.workspace.scientific-compute-submission.v42"
	scientificComputeSubmissionsDDL                 = `CREATE TABLE scientific_compute_submissions (
		job_id TEXT PRIMARY KEY REFERENCES scientific_compute_admissions(job_id) ON DELETE RESTRICT,
		request_sha256 TEXT NOT NULL CHECK(length(request_sha256)=64 AND request_sha256 NOT GLOB '*[^0-9a-f]*'),
		handle_sha256 TEXT NOT NULL CHECK(length(handle_sha256)=64 AND handle_sha256 NOT GLOB '*[^0-9a-f]*'),
		handle_authority_seq INTEGER NOT NULL CHECK(handle_authority_seq>0),
		submission_id TEXT NOT NULL UNIQUE,
		recovery_generation INTEGER NOT NULL CHECK(recovery_generation>0),
		provider TEXT NOT NULL,
		install_id TEXT NOT NULL,
		image_ref TEXT NOT NULL,
		runtime_generation TEXT NOT NULL,
		attempt_timeout_seconds INTEGER NOT NULL CHECK(attempt_timeout_seconds>0),
		harvest_margin_seconds INTEGER NOT NULL CHECK(harvest_margin_seconds>=0),
		termination_grace_seconds INTEGER NOT NULL CHECK(termination_grace_seconds>=0),
		staging_timeout_seconds INTEGER NOT NULL CHECK(staging_timeout_seconds>0),
		archive_storage_path TEXT NOT NULL UNIQUE CHECK(archive_storage_path GLOB 'scientific-submissions/*'),
		archive_sha256 TEXT NOT NULL CHECK(length(archive_sha256)=64 AND archive_sha256 NOT GLOB '*[^0-9a-f]*'),
		archive_size_bytes INTEGER NOT NULL CHECK(archive_size_bytes>0),
		state TEXT NOT NULL CHECK(state IN ('prepared','creating','bound','submitted','failed','closed')),
		sandbox_id TEXT,
		sandbox_deadline_at TEXT,
		create_started_at TEXT,
		submitted_at TEXT,
		failed_at TEXT,
		archive_released_at TEXT,
		updated_at TEXT NOT NULL,
		outbox_event_id TEXT NOT NULL UNIQUE REFERENCES workspace_outbox(event_id) ON DELETE RESTRICT,
		FOREIGN KEY(job_id,handle_authority_seq) REFERENCES scientific_compute_handle_authorities(job_id,authority_seq) ON DELETE RESTRICT,
		CHECK((state='prepared' AND sandbox_id IS NULL AND sandbox_deadline_at IS NULL AND create_started_at IS NULL AND submitted_at IS NULL) OR
			(state='creating' AND sandbox_id IS NULL AND sandbox_deadline_at IS NOT NULL AND create_started_at IS NOT NULL AND submitted_at IS NULL) OR
			(state='bound' AND sandbox_id IS NOT NULL AND sandbox_deadline_at IS NOT NULL AND submitted_at IS NULL) OR
			(state='submitted' AND sandbox_id IS NOT NULL AND sandbox_deadline_at IS NOT NULL AND submitted_at IS NOT NULL) OR
			(state IN ('failed','closed'))),
		CHECK((state='failed' AND failed_at IS NOT NULL) OR (state<>'failed'))
	) STRICT`
)

var scientificComputeSubmissionV42Migration = versionedSchemaMigration{
	version: 42,
	name:    "scientific-compute-submission-recovery",
	statements: []string{
		blobCommitSchema,
		scientificComputeSubmissionsDDL,
		`CREATE INDEX scientific_compute_submissions_recovery_idx
			ON scientific_compute_submissions(state,updated_at,job_id)`,
		`CREATE TRIGGER scientific_compute_submissions_identity_immutable
			BEFORE UPDATE ON scientific_compute_submissions
			WHEN NEW.job_id!=OLD.job_id OR NEW.request_sha256!=OLD.request_sha256 OR
				NEW.handle_sha256!=OLD.handle_sha256 OR NEW.handle_authority_seq!=OLD.handle_authority_seq OR
				NEW.submission_id!=OLD.submission_id OR NEW.provider!=OLD.provider OR
				NEW.install_id!=OLD.install_id OR NEW.image_ref!=OLD.image_ref OR
				NEW.runtime_generation!=OLD.runtime_generation OR
				NEW.attempt_timeout_seconds!=OLD.attempt_timeout_seconds OR
				NEW.harvest_margin_seconds!=OLD.harvest_margin_seconds OR
				NEW.termination_grace_seconds!=OLD.termination_grace_seconds OR
				NEW.staging_timeout_seconds!=OLD.staging_timeout_seconds OR
				NEW.archive_storage_path!=OLD.archive_storage_path OR NEW.archive_sha256!=OLD.archive_sha256 OR
				NEW.archive_size_bytes!=OLD.archive_size_bytes OR NEW.outbox_event_id!=OLD.outbox_event_id
			BEGIN SELECT RAISE(ABORT,'scientific compute submission identity is immutable'); END`,
	},
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        scientificComputeSubmissionV42CallbackID,
		RuleSpec:          scientificComputeSubmissionV42RuleSpec,
		PreflightIdentity: scientificComputeSubmissionV42PreflightIdentity,
	},
}

func preflightScientificComputeSubmissionV42(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	var dependencies int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
		WHERE type='table' AND name IN (
			'scientific_compute_admissions','scientific_compute_handle_authorities',
			'scientific_compute_handles','workspace_outbox'
		)`).Scan(&dependencies); err != nil {
		return errors.New("inspect scientific compute submission dependencies")
	}
	if dependencies != 4 {
		return errors.New("scientific compute authority and outbox schemas are required")
	}
	var polluted int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
		WHERE name IN ('scientific_compute_submissions','scientific_compute_submissions_recovery_idx',
			'scientific_compute_submissions_identity_immutable')`).Scan(&polluted); err != nil {
		return errors.New("inspect scientific compute submission cohort")
	}
	if polluted != 0 {
		return errors.New("scientific compute submission cohort is polluted")
	}
	return nil
}
