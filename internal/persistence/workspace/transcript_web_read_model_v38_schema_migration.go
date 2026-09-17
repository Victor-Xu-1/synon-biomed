package workspace

import (
	"context"
	"errors"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	transcriptWebReadModelV38CallbackID        = "transcript-web-read-model-v38-noop"
	transcriptWebReadModelV38PreflightIdentity = "canonical-transcript-typed-history-v35"
	transcriptWebReadModelV38RuleSpec          = "synon.workspace.transcript-web-read-model.v38"
)

var transcriptWebReadModelV38Migration = versionedSchemaMigration{
	version:    38,
	name:       "transcript-web-read-model",
	statements: transcriptstore.WebReadModelV38Statements(),
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        transcriptWebReadModelV38CallbackID,
		RuleSpec:          transcriptWebReadModelV38RuleSpec,
		PreflightIdentity: transcriptWebReadModelV38PreflightIdentity,
	},
}

func preflightTranscriptWebReadModelV38(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	for _, object := range transcriptstore.WebReadModelV38ObjectNames() {
		var count int
		if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, object).Scan(&count); err != nil {
			return errors.New("inspect transcript Web read-model cohort")
		}
		if count != 0 {
			return errors.New("transcript Web read-model cohort is polluted")
		}
	}
	if err := validateMigrationObjects(ctx, executor,
		transcriptstore.BranchV25ObjectNames(), transcriptstore.BranchV25CanonicalStatements()); err != nil {
		return errors.New("transcript branch v25 cohort identity mismatch")
	}
	if err := validateMigrationObjects(ctx, executor,
		transcriptstore.PayloadGenesisV32CanonicalObjectNames(),
		transcriptstore.PayloadGenesisV32CanonicalStatements()); err != nil {
		return errors.New("transcript payload genesis v32 cohort identity mismatch")
	}
	if err := validateMigrationObjects(ctx, executor,
		transcriptstore.TypedHistoryBootstrapV35ObjectNames(),
		transcriptstore.TypedHistoryBootstrapV35CanonicalStatements()); err != nil {
		return errors.New("transcript typed history v35 cohort identity mismatch")
	}
	var ordinalViolations int
	if err := executor.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM (
			SELECT branch.stream_uid,branch.branch_id,COUNT(membership.ordinal) AS event_count,
				COALESCE(MIN(membership.ordinal),0) AS min_ordinal,
				COALESCE(MAX(membership.ordinal),0) AS max_ordinal
			FROM transcript_branches branch
			LEFT JOIN transcript_branch_events membership
				ON membership.stream_uid=branch.stream_uid AND membership.branch_id=branch.branch_id
			GROUP BY branch.stream_uid,branch.branch_id
		) WHERE (event_count=0 AND (min_ordinal<>0 OR max_ordinal<>0))
			OR (event_count>0 AND (min_ordinal<>1 OR max_ordinal<>event_count))`).Scan(&ordinalViolations); err != nil {
		return errors.New("inspect transcript branch ordinal continuity")
	}
	if ordinalViolations != 0 {
		return errors.New("transcript branch ordinal continuity is invalid")
	}
	var missingEvents int
	if err := executor.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM transcript_branch_events membership
		LEFT JOIN transcript_events event
			ON event.stream_uid=membership.stream_uid AND event.event_id=membership.event_id
		WHERE event.event_id IS NULL`).Scan(&missingEvents); err != nil {
		return errors.New("inspect transcript branch event integrity")
	}
	if missingEvents != 0 {
		return errors.New("transcript branch event integrity is invalid")
	}
	var duplicateEvents int
	if err := executor.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM (
			SELECT stream_uid,branch_id,event_id
			FROM transcript_branch_events
			GROUP BY stream_uid,branch_id,event_id HAVING COUNT(*)<>1
		)`).Scan(&duplicateEvents); err != nil {
		return errors.New("inspect transcript branch event uniqueness")
	}
	if duplicateEvents != 0 {
		return errors.New("transcript branch event uniqueness is invalid")
	}
	var publicationViolations int
	if err := executor.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM (
			SELECT event.publication_seq,
				LAG(event.publication_seq) OVER (
					PARTITION BY membership.stream_uid,membership.branch_id ORDER BY membership.ordinal
				) AS previous_publication_seq
			FROM transcript_branch_events membership
			JOIN transcript_events event
				ON event.stream_uid=membership.stream_uid AND event.event_id=membership.event_id
		) WHERE previous_publication_seq IS NOT NULL AND publication_seq<=previous_publication_seq`).Scan(&publicationViolations); err != nil {
		return errors.New("inspect transcript branch publication continuity")
	}
	if publicationViolations != 0 {
		return errors.New("transcript branch publication continuity is invalid")
	}
	return nil
}
