package workspace

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	transcriptWebProjectorV66CallbackID        = "transcript-web-projector-v66-rebuild"
	transcriptWebProjectorV66PreflightIdentity = "transcript-web-projector-v66-upgrade-v1"
	transcriptWebProjectorV66RuleSpec          = "synon.workspace.transcript-web-projector.v66"
)

var transcriptWebProjectorV66Migration = versionedSchemaMigration{
	version: 66,
	name:    "transcript-web-projector-v10",
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        transcriptWebProjectorV66CallbackID,
		RuleSpec:          transcriptWebProjectorV66RuleSpec,
		PreflightIdentity: transcriptWebProjectorV66PreflightIdentity,
	},
}

func preflightTranscriptWebProjectorV66(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	var tableSQL string
	if err := executor.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema
		WHERE type='table' AND name='transcript_web_projection_state'`).Scan(&tableSQL); err != nil {
		return errors.New("inspect transcript Web projection state")
	}
	compact := strings.NewReplacer(" ", "", "\n", "", "\r", "", "\t", "").Replace(tableSQL)
	if !strings.Contains(compact, "CHECK(projector_versionIN(1,2,3,4,5,6,7,8,9))") {
		return errors.New("transcript Web projector v65 version constraint is required")
	}
	for _, staging := range []string{
		"transcript_web_projection_state_v65", "transcript_web_messages_v65",
		"transcript_web_message_identities_v65", "transcript_web_message_artifact_refs_v65",
	} {
		var count int
		if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, staging).Scan(&count); err != nil || count != 0 {
			return errors.New("transcript Web projector v66 migration staging is polluted")
		}
	}
	return nil
}

func migrateTranscriptWebProjectorV66(ctx context.Context, tx *sql.Tx) error {
	for _, statement := range transcriptstore.WebReadModelProjectorV66RebuildStatements() {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
