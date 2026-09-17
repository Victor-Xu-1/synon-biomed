package workspace

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	transcriptWebProjectorV48CallbackID        = "transcript-web-projector-v48-rebuild"
	transcriptWebProjectorV48PreflightIdentity = "transcript-web-projector-v47-upgrade-v1"
	transcriptWebProjectorV48RuleSpec          = "synon.workspace.transcript-web-projector.v48"
)

var transcriptWebProjectorV48Migration = versionedSchemaMigration{
	version: 48,
	name:    "transcript-web-projector-v3",
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        transcriptWebProjectorV48CallbackID,
		RuleSpec:          transcriptWebProjectorV48RuleSpec,
		PreflightIdentity: transcriptWebProjectorV48PreflightIdentity,
	},
}

func preflightTranscriptWebProjectorV48(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	var tableSQL string
	if err := executor.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema
		WHERE type='table' AND name='transcript_web_projection_state'`).Scan(&tableSQL); err != nil {
		return errors.New("inspect transcript Web projection state")
	}
	compact := strings.NewReplacer(" ", "", "\n", "", "\r", "", "\t", "").Replace(tableSQL)
	if !strings.Contains(compact, "CHECK(projector_versionIN(1,2))") {
		return errors.New("transcript Web projector v47 version constraint is required")
	}
	for _, staging := range []string{
		"transcript_web_projection_state_v47", "transcript_web_messages_v47",
		"transcript_web_message_identities_v47", "transcript_web_message_artifact_refs_v47",
	} {
		var count int
		if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, staging).Scan(&count); err != nil || count != 0 {
			return errors.New("transcript Web projector v48 migration staging is polluted")
		}
	}
	return nil
}

func migrateTranscriptWebProjectorV48(ctx context.Context, tx *sql.Tx) error {
	for _, statement := range transcriptstore.WebReadModelProjectorV48RebuildStatements() {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
