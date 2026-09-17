package workspace

import (
	"context"
	"errors"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	transcriptHistoryBackfillV29PreflightIdentity = "canonical-transcript-v28-history-backfill-empty-v1"
	transcriptHistoryBackfillV29RuleSpec          = "synon.workspace.transcript-history-backfill.v29"
)

var transcriptHistoryBackfillV29Migration = versionedSchemaMigration{
	version:    29,
	name:       "transcript-history-backfill-staging",
	statements: transcriptstore.HistoryBackfillV29Statements(),
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        transcriptSchemaNoopCallbackID,
		RuleSpec:          transcriptHistoryBackfillV29RuleSpec,
		PreflightIdentity: transcriptHistoryBackfillV29PreflightIdentity,
	},
}

func preflightTranscriptHistoryBackfillV29(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	for _, object := range transcriptstore.HistoryBackfillV29ObjectNames() {
		var count int
		if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, object).Scan(&count); err != nil {
			return errors.New("inspect transcript history backfill cohort")
		}
		if count != 0 {
			return errors.New("transcript history backfill cohort is polluted")
		}
	}
	if err := validateMigrationObjects(ctx, executor,
		transcriptstore.HistoryClassificationV27ObjectNames(), transcriptstore.HistoryClassificationV27Statements()); err != nil {
		return errors.New("transcript history classification v27 cohort identity mismatch")
	}
	if err := validateMigrationObjects(ctx, executor, notificationV28ObjectNames, notificationsV28Migration.statements); err != nil {
		return errors.New("durable notification v28 cohort identity mismatch")
	}
	return validateFrameIncarnationV26Shape(ctx, executor)
}

func validateMigrationObjects(
	ctx context.Context,
	executor schemaMigrationQueryExecutor,
	names []string,
	statements []string,
) error {
	if len(names) != len(statements) {
		return errors.New("migration object manifest is invalid")
	}
	for index, name := range names {
		var observed string
		if err := executor.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE name=?`, name).Scan(&observed); err != nil {
			return err
		}
		if normalizeMigrationObjectSQL(observed) != normalizeMigrationObjectSQL(statements[index]) {
			return errors.New("migration object identity mismatch")
		}
	}
	return nil
}

func normalizeMigrationObjectSQL(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}
