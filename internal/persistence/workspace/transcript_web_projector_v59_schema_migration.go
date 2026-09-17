package workspace

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	transcriptWebProjectorV59CallbackID        = "transcript-web-projector-v59-rebuild"
	transcriptWebProjectorV59PreflightIdentity = "transcript-web-projector-v58-upgrade-v1"
	transcriptWebProjectorV59RuleSpec          = "synon.workspace.transcript-web-projector.v59"
)

var transcriptWebProjectorV59Migration = versionedSchemaMigration{
	version: 59,
	name:    "transcript-web-projector-v6",
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        transcriptWebProjectorV59CallbackID,
		RuleSpec:          transcriptWebProjectorV59RuleSpec,
		PreflightIdentity: transcriptWebProjectorV59PreflightIdentity,
	},
}

func preflightTranscriptWebProjectorV59(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	var tableSQL string
	if err := executor.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema
		WHERE type='table' AND name='transcript_web_projection_state'`).Scan(&tableSQL); err != nil {
		return errors.New("inspect transcript Web projection state")
	}
	compact := strings.NewReplacer(" ", "", "\n", "", "\r", "", "\t", "").Replace(tableSQL)
	if !strings.Contains(compact, "CHECK(projector_versionIN(1,2,3,4,5))") {
		return errors.New("transcript Web projector v58 version constraint is required")
	}
	for _, staging := range []string{
		"transcript_web_projection_state_v58", "transcript_web_messages_v58",
		"transcript_web_message_identities_v58", "transcript_web_message_artifact_refs_v58",
	} {
		var count int
		if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, staging).Scan(&count); err != nil || count != 0 {
			return errors.New("transcript Web projector v59 migration staging is polluted")
		}
	}
	return nil
}

func migrateTranscriptWebProjectorV59(ctx context.Context, tx *sql.Tx) error {
	for _, statement := range transcriptstore.WebReadModelProjectorV59RebuildStatements() {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
