package workspace

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	transcriptWebProjectorV47CallbackID        = "transcript-web-projector-v47-rebuild"
	transcriptWebProjectorV47PreflightIdentity = "transcript-web-projector-v46-upgrade-v1"
	transcriptWebProjectorV47RuleSpec          = "synon.workspace.transcript-web-projector.v47"
)

var transcriptWebProjectorV47Migration = versionedSchemaMigration{
	version: 47,
	name:    "transcript-web-projector-v2",
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        transcriptWebProjectorV47CallbackID,
		RuleSpec:          transcriptWebProjectorV47RuleSpec,
		PreflightIdentity: transcriptWebProjectorV47PreflightIdentity,
	},
}

func preflightTranscriptWebProjectorV47(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	var tableSQL string
	if err := executor.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema
		WHERE type='table' AND name='transcript_web_projection_state'`).Scan(&tableSQL); err != nil {
		return errors.New("inspect transcript Web projection state")
	}
	compact := strings.NewReplacer(" ", "", "\n", "", "\r", "", "\t", "").Replace(tableSQL)
	if !strings.Contains(compact, "CHECK(projector_version=1)") &&
		!strings.Contains(compact, "CHECK(projector_versionIN(1,2))") {
		return errors.New("transcript Web projector version constraint is unsupported")
	}
	for _, staging := range []string{
		"transcript_web_projection_state_v38", "transcript_web_messages_v38",
		"transcript_web_message_identities_v38", "transcript_web_message_artifact_refs_v38",
	} {
		var count int
		if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, staging).Scan(&count); err != nil || count != 0 {
			return errors.New("transcript Web projector migration staging is polluted")
		}
	}
	return nil
}

func migrateTranscriptWebProjectorV47(ctx context.Context, tx *sql.Tx) error {
	for _, statement := range transcriptstore.WebReadModelProjectorV47RebuildStatements() {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
