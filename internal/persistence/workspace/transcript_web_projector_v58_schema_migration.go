package workspace

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	transcriptWebProjectorV58CallbackID        = "transcript-web-projector-v58-rebuild"
	transcriptWebProjectorV58PreflightIdentity = "transcript-web-projector-v57-upgrade-v1"
	transcriptWebProjectorV58RuleSpec          = "synon.workspace.transcript-web-projector.v58"
)

var transcriptWebProjectorV58Migration = versionedSchemaMigration{
	version: 58,
	name:    "transcript-web-projector-v5",
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        transcriptWebProjectorV58CallbackID,
		RuleSpec:          transcriptWebProjectorV58RuleSpec,
		PreflightIdentity: transcriptWebProjectorV58PreflightIdentity,
	},
}

func preflightTranscriptWebProjectorV58(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	var tableSQL string
	if err := executor.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema
		WHERE type='table' AND name='transcript_web_projection_state'`).Scan(&tableSQL); err != nil {
		return errors.New("inspect transcript Web projection state")
	}
	compact := strings.NewReplacer(" ", "", "\n", "", "\r", "", "\t", "").Replace(tableSQL)
	if !strings.Contains(compact, "CHECK(projector_versionIN(1,2,3,4))") {
		return errors.New("transcript Web projector v57 version constraint is required")
	}
	for _, staging := range []string{
		"transcript_web_projection_state_v57", "transcript_web_messages_v57",
		"transcript_web_message_identities_v57", "transcript_web_message_artifact_refs_v57",
	} {
		var count int
		if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, staging).Scan(&count); err != nil || count != 0 {
			return errors.New("transcript Web projector v58 migration staging is polluted")
		}
	}
	return nil
}

func migrateTranscriptWebProjectorV58(ctx context.Context, tx *sql.Tx) error {
	for _, statement := range transcriptstore.WebReadModelProjectorV58RebuildStatements() {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
