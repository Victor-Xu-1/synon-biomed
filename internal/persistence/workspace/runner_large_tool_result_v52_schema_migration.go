package workspace

import (
	"context"
	"errors"
)

const (
	runnerLargeToolResultV52CallbackID        = "runner-large-tool-result-v52-authorized-delete"
	runnerLargeToolResultV52PreflightIdentity = "runner-large-tool-result-v50-delete-trigger-v1"
	runnerLargeToolResultV52RuleSpec          = "synon.workspace.runner-large-tool-result.v52"
)

// Runner-large tool results are immutable runtime evidence during normal
// operation. Project/frame deletion is the explicit lifecycle boundary that
// may retire this evidence, and it is authorized by the same scoped deletion
// cohort used for the other append-only runtime records.
var runnerLargeToolResultV52Migration = versionedSchemaMigration{
	version: 52,
	name:    "runner-large-tool-result-authorized-delete",
	statements: []string{
		`DROP TRIGGER runner_large_tool_result_delete_forbidden`,
		`CREATE TRIGGER runner_large_tool_result_delete_forbidden
			BEFORE DELETE ON runner_large_tool_results
			WHEN NOT EXISTS (
				SELECT 1 FROM workspace_runtime_delete_scopes scope
				WHERE scope.owner_user_id=OLD.owner_user_id AND scope.project_id=OLD.project_id
					AND scope.root_frame_id=OLD.root_frame_id
			)
			BEGIN SELECT RAISE(ABORT,'runner large tool result is append-only'); END`,
	},
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        runnerLargeToolResultV52CallbackID,
		RuleSpec:          runnerLargeToolResultV52RuleSpec,
		PreflightIdentity: runnerLargeToolResultV52PreflightIdentity,
	},
}

func preflightRunnerLargeToolResultV52(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	var dependencies int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
		WHERE type='table' AND name IN ('runner_large_tool_results','workspace_runtime_delete_scopes')`).Scan(&dependencies); err != nil {
		return errors.New("inspect runner large tool result deletion dependencies")
	}
	if dependencies != 2 {
		return errors.New("runner large tool result deletion dependencies are required")
	}
	var triggerCount int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
		WHERE type='trigger' AND name='runner_large_tool_result_delete_forbidden'
		AND sql LIKE '%workspace_runtime_delete_scopes%'`).Scan(&triggerCount); err != nil {
		return errors.New("inspect runner large tool result deletion trigger")
	}
	if triggerCount != 0 {
		return errors.New("runner large tool result deletion trigger is already scoped")
	}
	return nil
}
