package workspace

import (
	"context"
	"errors"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	transcriptHistoryOrdinaryV33PreflightIdentity = "canonical-transcript-v32-ordinary-history-empty-v1"
	transcriptHistoryOrdinaryV33RuleSpec          = "synon.workspace.transcript-history-ordinary.v33"
)

var transcriptHistoryOrdinaryV33Migration = versionedSchemaMigration{
	version:    33,
	name:       "transcript-ordinary-history-authority",
	statements: transcriptstore.HistoryOrdinaryV33Statements(),
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        transcriptSchemaNoopCallbackID,
		RuleSpec:          transcriptHistoryOrdinaryV33RuleSpec,
		PreflightIdentity: transcriptHistoryOrdinaryV33PreflightIdentity,
	},
}

func preflightTranscriptHistoryOrdinaryV33(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	for _, object := range []string{
		"transcript_history_ordinary_cutover_runs",
		"transcript_history_ordinary_cursor_map",
		"transcript_history_activation_receipts_v33",
		"transcript_history_activation_freeze_ordinary",
		"transcript_history_activation_immutable",
	} {
		var count int
		if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, object).Scan(&count); err != nil {
			return errors.New("inspect transcript ordinary history cohort")
		}
		if count != 0 {
			return errors.New("transcript ordinary history cohort is polluted")
		}
	}
	retainedNames, retainedStatements := transcriptstore.HistoryActivationV31RetainedObjects()
	if err := validateMigrationObjects(ctx, executor, retainedNames, retainedStatements); err != nil {
		return errors.New("transcript history activation v31 cohort identity mismatch")
	}
	if err := validateMigrationObjects(ctx, executor,
		transcriptstore.PayloadGenesisV32CanonicalObjectNames(), transcriptstore.PayloadGenesisV32CanonicalStatements()); err != nil {
		return errors.New("transcript payload genesis v32 cohort identity mismatch")
	}
	return nil
}
