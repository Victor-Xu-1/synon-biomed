package workspace

import (
	"context"
	"errors"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	transcriptHistoryCutoverV30PreflightIdentity = "canonical-transcript-v29-history-cutover-empty-v1"
	transcriptHistoryCutoverV30RuleSpec          = "synon.workspace.transcript-history-cutover.v30"
)

var transcriptHistoryCutoverV30Migration = versionedSchemaMigration{
	version:    30,
	name:       "transcript-history-cutover-ready-plan",
	statements: transcriptstore.HistoryCutoverV30Statements(),
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        transcriptSchemaNoopCallbackID,
		RuleSpec:          transcriptHistoryCutoverV30RuleSpec,
		PreflightIdentity: transcriptHistoryCutoverV30PreflightIdentity,
	},
}

func preflightTranscriptHistoryCutoverV30(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	for _, object := range transcriptstore.HistoryCutoverV30ObjectNames() {
		var count int
		if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, object).Scan(&count); err != nil {
			return errors.New("inspect transcript history cutover cohort")
		}
		if count != 0 {
			return errors.New("transcript history cutover cohort is polluted")
		}
	}
	if err := validateMigrationObjects(ctx, executor,
		transcriptstore.HistoryBackfillV29ObjectNames(), transcriptstore.HistoryBackfillV29Statements()); err != nil {
		return errors.New("transcript history backfill v29 cohort identity mismatch")
	}
	return nil
}
