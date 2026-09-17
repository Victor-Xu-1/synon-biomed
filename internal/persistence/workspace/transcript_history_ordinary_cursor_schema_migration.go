package workspace

import (
	"context"
	"errors"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	transcriptHistoryOrdinaryCursorV34PreflightIdentity = "canonical-transcript-v33-ordinary-cursor-v1"
	transcriptHistoryOrdinaryCursorV34RuleSpec          = "synon.workspace.transcript-history-ordinary-cursor.v34"
)

var transcriptHistoryOrdinaryCursorV34Migration = versionedSchemaMigration{
	version:    34,
	name:       "transcript-ordinary-history-cursor",
	statements: transcriptstore.HistoryOrdinaryCursorV34Statements(),
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        transcriptSchemaNoopCallbackID,
		RuleSpec:          transcriptHistoryOrdinaryCursorV34RuleSpec,
		PreflightIdentity: transcriptHistoryOrdinaryCursorV34PreflightIdentity,
	},
}

func preflightTranscriptHistoryOrdinaryCursorV34(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	for _, object := range []string{
		"transcript_history_ordinary_cursor_map_v34",
		"transcript_history_ordinary_cursor_validate",
		"transcript_history_ordinary_cursor_immutable",
		"transcript_history_ordinary_cursor_delete_active",
	} {
		var count int
		if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, object).Scan(&count); err != nil {
			return errors.New("inspect transcript ordinary cursor cohort")
		}
		if count != 0 {
			return errors.New("transcript ordinary cursor cohort is polluted")
		}
	}
	if err := validateMigrationObjects(ctx, executor,
		transcriptstore.HistoryOrdinaryV33ObjectNames(), transcriptstore.HistoryOrdinaryV33CanonicalStatements()); err != nil {
		return errors.New("transcript ordinary history v33 cohort identity mismatch")
	}
	return nil
}
