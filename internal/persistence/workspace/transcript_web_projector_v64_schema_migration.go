package workspace

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	transcriptWebProjectorV64CallbackID        = "transcript-web-projector-v64-rebuild"
	transcriptWebProjectorV64PreflightIdentity = "transcript-web-projector-v64-upgrade-v1"
	transcriptWebProjectorV64RuleSpec          = "synon.workspace.transcript-web-projector.v64"
)

var transcriptWebProjectorV64Migration = versionedSchemaMigration{
	version: 64,
	name:    "transcript-web-projector-v8",
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        transcriptWebProjectorV64CallbackID,
		RuleSpec:          transcriptWebProjectorV64RuleSpec,
		PreflightIdentity: transcriptWebProjectorV64PreflightIdentity,
	},
}

func preflightTranscriptWebProjectorV64(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	var tableSQL string
	if err := executor.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema
		WHERE type='table' AND name='transcript_web_projection_state'`).Scan(&tableSQL); err != nil {
		return errors.New("inspect transcript Web projection state")
	}
	compact := strings.NewReplacer(" ", "", "\n", "", "\r", "", "\t", "").Replace(tableSQL)
	if !strings.Contains(compact, "CHECK(projector_versionIN(1,2,3,4,5,6,7))") {
		return errors.New("transcript Web projector v60 version constraint is required")
	}
	for _, staging := range []string{
		"transcript_web_projection_state_v63", "transcript_web_messages_v63",
		"transcript_web_message_identities_v63", "transcript_web_message_artifact_refs_v63",
	} {
		var count int
		if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, staging).Scan(&count); err != nil || count != 0 {
			return errors.New("transcript Web projector v64 migration staging is polluted")
		}
	}
	return nil
}

func migrateTranscriptWebProjectorV64(ctx context.Context, tx *sql.Tx) error {
	for _, statement := range transcriptstore.WebReadModelProjectorV64RebuildStatements() {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
