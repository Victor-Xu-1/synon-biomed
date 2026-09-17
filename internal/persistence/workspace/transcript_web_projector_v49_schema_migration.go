package workspace

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	transcriptWebProjectorV49CallbackID        = "transcript-web-projector-v49-rebuild"
	transcriptWebProjectorV49PreflightIdentity = "transcript-web-projector-v48-upgrade-v1"
	transcriptWebProjectorV49RuleSpec          = "synon.workspace.transcript-web-projector.v49"
)

var transcriptWebProjectorV49Migration = versionedSchemaMigration{
	version: 49,
	name:    "transcript-web-projector-v4",
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        transcriptWebProjectorV49CallbackID,
		RuleSpec:          transcriptWebProjectorV49RuleSpec,
		PreflightIdentity: transcriptWebProjectorV49PreflightIdentity,
	},
}

func preflightTranscriptWebProjectorV49(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	var tableSQL string
	if err := executor.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema
		WHERE type='table' AND name='transcript_web_projection_state'`).Scan(&tableSQL); err != nil {
		return errors.New("inspect transcript Web projection state")
	}
	compact := strings.NewReplacer(" ", "", "\n", "", "\r", "", "\t", "").Replace(tableSQL)
	if !strings.Contains(compact, "CHECK(projector_versionIN(1,2,3))") {
		return errors.New("transcript Web projector v48 version constraint is required")
	}
	for _, staging := range []string{
		"transcript_web_projection_state_v48", "transcript_web_messages_v48",
		"transcript_web_message_identities_v48", "transcript_web_message_artifact_refs_v48",
	} {
		var count int
		if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, staging).Scan(&count); err != nil || count != 0 {
			return errors.New("transcript Web projector v49 migration staging is polluted")
		}
	}
	return nil
}

func migrateTranscriptWebProjectorV49(ctx context.Context, tx *sql.Tx) error {
	for _, statement := range transcriptstore.WebReadModelProjectorV49RebuildStatements() {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
