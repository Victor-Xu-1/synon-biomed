package workspace

import (
	"context"
	"database/sql"
	"errors"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	transcriptHistoryActivationV31PreflightIdentity = "canonical-transcript-v30-history-activation-empty-v1"
	transcriptHistoryActivationV31RuleSpec          = "synon.workspace.transcript-history-activation.v31"
	transcriptHistoryActivationV31CallbackID        = "transcript-frame-authority-backfill-v1"
)

var transcriptHistoryActivationV31Migration = versionedSchemaMigration{
	version:    31,
	name:       "transcript-history-serving-activation",
	statements: transcriptstore.HistoryActivationV31Statements(),
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        transcriptHistoryActivationV31CallbackID,
		RuleSpec:          transcriptHistoryActivationV31RuleSpec,
		PreflightIdentity: transcriptHistoryActivationV31PreflightIdentity,
	},
}

func backfillTranscriptFrameAuthorityV31(ctx context.Context, tx *sql.Tx) error {
	if tx == nil {
		return errors.New("transcript frame authority migration transaction is required")
	}
	var invalid int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM transcript_streams stream
		LEFT JOIN frames frame ON frame.id=stream.frame_id AND frame.project_id=stream.project_id
		LEFT JOIN projects project ON project.id=frame.project_id
		WHERE stream.kind='frame_ref' AND (
			stream.session_id='' OR frame.id IS NULL OR project.id IS NULL OR
			project.user_id!=stream.owner_id OR frame.root_frame_id!=stream.root_frame_id
		)`).Scan(&invalid); err != nil {
		return errors.New("inspect frame transcript authority")
	}
	if invalid != 0 {
		return errors.New("frame transcript authority is inconsistent")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO transcript_frame_authority(
		owner_id,session_id,active_stream_uid,active_epoch,authority_generation,
		read_authority,write_authority,activation_id,updated_at)
		SELECT stream.owner_id,stream.session_id,stream.stream_uid,stream.epoch,1,
			'legacy_mixed_v1','legacy_frame_ref_v1',NULL,stream.updated_at
		FROM transcript_streams stream
		WHERE stream.kind='frame_ref' AND NOT EXISTS (
			SELECT 1 FROM transcript_streams newer
			WHERE newer.kind='frame_ref' AND newer.owner_id=stream.owner_id
				AND newer.session_id=stream.session_id AND newer.epoch>stream.epoch
		)`); err != nil {
		return errors.New("backfill frame transcript authority")
	}
	var streams, authorities int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM (
		SELECT owner_id,session_id FROM transcript_streams WHERE kind='frame_ref' GROUP BY owner_id,session_id
	)`).Scan(&streams); err != nil {
		return errors.New("count frame transcript streams")
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcript_frame_authority`).Scan(&authorities); err != nil {
		return errors.New("count frame transcript authorities")
	}
	if streams != authorities {
		return errors.New("frame transcript authority backfill is incomplete")
	}
	return nil
}

func preflightTranscriptHistoryActivationV31(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	for _, object := range transcriptstore.HistoryActivationV31ObjectNames() {
		var count int
		if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, object).Scan(&count); err != nil {
			return errors.New("inspect transcript history activation cohort")
		}
		if count != 0 {
			return errors.New("transcript history activation cohort is polluted")
		}
	}
	if err := validateMigrationObjects(ctx, executor,
		transcriptstore.HistoryCutoverV30ObjectNames(), transcriptstore.HistoryCutoverV30Statements()); err != nil {
		return errors.New("transcript history cutover v30 cohort identity mismatch")
	}
	return nil
}
