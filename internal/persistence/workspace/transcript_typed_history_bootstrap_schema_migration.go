package workspace

import (
	"context"
	"errors"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	transcriptTypedHistoryBootstrapV35PreflightIdentity = "canonical-transcript-v34-typed-bootstrap-v1"
	transcriptTypedHistoryBootstrapV35RuleSpec          = "synon.workspace.transcript-typed-history-bootstrap.v35"
)

var transcriptTypedHistoryBootstrapV35Migration = versionedSchemaMigration{
	version:    35,
	name:       "transcript-typed-history-bootstrap",
	statements: transcriptstore.TypedHistoryBootstrapV35Statements(),
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        transcriptSchemaNoopCallbackID,
		RuleSpec:          transcriptTypedHistoryBootstrapV35RuleSpec,
		PreflightIdentity: transcriptTypedHistoryBootstrapV35PreflightIdentity,
	},
}

func preflightTranscriptTypedHistoryBootstrapV35(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	for _, object := range transcriptstore.TypedHistoryBootstrapV35ObjectNames() {
		var count int
		if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, object).Scan(&count); err != nil {
			return errors.New("inspect transcript typed history bootstrap cohort")
		}
		if count != 0 {
			return errors.New("transcript typed history bootstrap cohort is polluted")
		}
	}
	v33Names := transcriptstore.HistoryOrdinaryV33ObjectNames()
	v33Statements := transcriptstore.HistoryOrdinaryV33CanonicalStatements()
	retainedNames := make([]string, 0, len(v33Names)-1)
	retainedStatements := make([]string, 0, len(v33Statements)-1)
	for index, name := range v33Names {
		if name != "transcript_history_ordinary_cursor_map" {
			retainedNames = append(retainedNames, name)
			retainedStatements = append(retainedStatements, v33Statements[index])
		}
	}
	if err := validateMigrationObjects(ctx, executor, retainedNames, retainedStatements); err != nil {
		return errors.New("transcript ordinary history v33 cohort identity mismatch")
	}
	if err := validateMigrationObjects(ctx, executor,
		transcriptstore.HistoryOrdinaryCursorV34ObjectNames(),
		transcriptstore.HistoryOrdinaryCursorV34CanonicalStatements()); err != nil {
		return errors.New("transcript ordinary cursor v34 cohort identity mismatch")
	}
	if err := validateMigrationObjects(ctx, executor,
		transcriptstore.PayloadGenesisV32CanonicalObjectNames(),
		transcriptstore.PayloadGenesisV32CanonicalStatements()); err != nil {
		return errors.New("transcript payload genesis v32 cohort identity mismatch")
	}
	return nil
}
