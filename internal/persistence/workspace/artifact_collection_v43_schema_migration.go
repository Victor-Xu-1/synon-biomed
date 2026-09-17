package workspace

import (
	"context"
	"errors"
)

const (
	artifactCollectionV43CallbackID        = "artifact-collection-v43-noop"
	artifactCollectionV43PreflightIdentity = "artifact-project-index-v1"
	artifactCollectionV43RuleSpec          = "synon.workspace.artifact-collection-index.v43"
)

var artifactCollectionV43Migration = versionedSchemaMigration{
	version: 43,
	name:    "artifact-project-collection-index",
	statements: []string{
		`CREATE INDEX artifacts_project_current_idx
			ON artifacts(project_id,updated_at DESC,id DESC,current_version_number)`,
	},
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        artifactCollectionV43CallbackID,
		RuleSpec:          artifactCollectionV43RuleSpec,
		PreflightIdentity: artifactCollectionV43PreflightIdentity,
	},
}

func preflightArtifactCollectionV43(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	var artifactsTable int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
		WHERE type='table' AND name='artifacts'`).Scan(&artifactsTable); err != nil {
		return errors.New("inspect artifact collection dependency")
	}
	if artifactsTable != 1 {
		return errors.New("artifact collection schema is required")
	}
	var polluted int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
		WHERE name='artifacts_project_current_idx'`).Scan(&polluted); err != nil {
		return errors.New("inspect artifact collection index")
	}
	if polluted != 0 {
		return errors.New("artifact collection index cohort is polluted")
	}
	return nil
}
