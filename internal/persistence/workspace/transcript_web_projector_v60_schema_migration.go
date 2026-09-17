package workspace

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	transcriptWebProjectorV60CallbackID        = "transcript-web-projector-v60-rebuild"
	transcriptWebProjectorV60PreflightIdentity = "transcript-web-projector-v59-upgrade-v1"
	transcriptWebProjectorV60RuleSpec          = "synon.workspace.transcript-web-projector.v60"
)

var transcriptWebProjectorV60Migration = versionedSchemaMigration{
	version: 60,
	name:    "transcript-web-projector-v7",
	identityV2: &schemaMigrationIdentityV2{
		CallbackID: transcriptWebProjectorV60CallbackID, RuleSpec: transcriptWebProjectorV60RuleSpec,
		PreflightIdentity: transcriptWebProjectorV60PreflightIdentity,
	},
}

func preflightTranscriptWebProjectorV60(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	var tableSQL string
	if err := executor.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema
		WHERE type='table' AND name='transcript_web_projection_state'`).Scan(&tableSQL); err != nil {
		return errors.New("inspect transcript Web projection state")
	}
	compact := strings.NewReplacer(" ", "", "\n", "", "\r", "", "\t", "").Replace(tableSQL)
	if !strings.Contains(compact, "CHECK(projector_versionIN(1,2,3,4,5,6))") {
		return errors.New("transcript Web projector v59 version constraint is required")
	}
	for _, staging := range []string{
		"transcript_web_projection_state_v59", "transcript_web_messages_v59",
		"transcript_web_message_identities_v59", "transcript_web_message_artifact_refs_v59",
	} {
		var count int
		if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, staging).Scan(&count); err != nil || count != 0 {
			return errors.New("transcript Web projector v60 migration staging is polluted")
		}
	}
	return nil
}

func migrateTranscriptWebProjectorV60(ctx context.Context, tx *sql.Tx) error {
	for _, statement := range transcriptstore.WebReadModelProjectorV60RebuildStatements() {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
