package workspace

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	transcriptWebProjectorV67CallbackID        = "transcript-web-projector-v67-rebuild"
	transcriptWebProjectorV67PreflightIdentity = "transcript-web-projector-v67-upgrade-v1"
	transcriptWebProjectorV67RuleSpec          = "synon.workspace.transcript-web-projector.v67"
)

var transcriptWebProjectorV67Migration = versionedSchemaMigration{
	version: 67,
	name:    "transcript-web-projector-v11",
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        transcriptWebProjectorV67CallbackID,
		RuleSpec:          transcriptWebProjectorV67RuleSpec,
		PreflightIdentity: transcriptWebProjectorV67PreflightIdentity,
	},
}

func preflightTranscriptWebProjectorV67(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	var tableSQL string
	if err := executor.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema
		WHERE type='table' AND name='transcript_web_projection_state'`).Scan(&tableSQL); err != nil {
		return errors.New("inspect transcript Web projection state")
	}
	compact := strings.NewReplacer(" ", "", "\n", "", "\r", "", "\t", "").Replace(tableSQL)
	if !strings.Contains(compact, "CHECK(projector_versionIN(1,2,3,4,5,6,7,8,9,10))") {
		return errors.New("transcript Web projector v66 version constraint is required")
	}
	for _, staging := range []string{
		"transcript_web_projection_state_v66", "transcript_web_messages_v66",
		"transcript_web_message_identities_v66", "transcript_web_message_artifact_refs_v66",
	} {
		var count int
		if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, staging).Scan(&count); err != nil || count != 0 {
			return errors.New("transcript Web projector v67 migration staging is polluted")
		}
	}
	return nil
}

func migrateTranscriptWebProjectorV67(ctx context.Context, tx *sql.Tx) error {
	for _, statement := range transcriptstore.WebReadModelProjectorV67RebuildStatements() {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
