package workspace

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	transcriptWebProjectorV65CallbackID        = "transcript-web-projector-v65-rebuild"
	transcriptWebProjectorV65PreflightIdentity = "transcript-web-projector-v65-upgrade-v1"
	transcriptWebProjectorV65RuleSpec          = "synon.workspace.transcript-web-projector.v65"
)

var transcriptWebProjectorV65Migration = versionedSchemaMigration{
	version: 65,
	name:    "transcript-web-projector-v9",
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        transcriptWebProjectorV65CallbackID,
		RuleSpec:          transcriptWebProjectorV65RuleSpec,
		PreflightIdentity: transcriptWebProjectorV65PreflightIdentity,
	},
}

func preflightTranscriptWebProjectorV65(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	var tableSQL string
	if err := executor.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema
		WHERE type='table' AND name='transcript_web_projection_state'`).Scan(&tableSQL); err != nil {
		return errors.New("inspect transcript Web projection state")
	}
	compact := strings.NewReplacer(" ", "", "\n", "", "\r", "", "\t", "").Replace(tableSQL)
	if !strings.Contains(compact, "CHECK(projector_versionIN(1,2,3,4,5,6,7,8))") {
		return errors.New("transcript Web projector v64 version constraint is required")
	}
	for _, staging := range []string{
		"transcript_web_projection_state_v64", "transcript_web_messages_v64",
		"transcript_web_message_identities_v64", "transcript_web_message_artifact_refs_v64",
	} {
		var count int
		if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, staging).Scan(&count); err != nil || count != 0 {
			return errors.New("transcript Web projector v65 migration staging is polluted")
		}
	}
	return nil
}

func migrateTranscriptWebProjectorV65(ctx context.Context, tx *sql.Tx) error {
	for _, statement := range transcriptstore.WebReadModelProjectorV65RebuildStatements() {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
