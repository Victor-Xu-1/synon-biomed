package workspace

import (
	"context"
	"errors"
)

const (
	generatedPlanRetentionV61CallbackID        = "generated-plan-retention-v61-noop"
	generatedPlanRetentionV61PreflightIdentity = "generated-plan-retention-column-v1"
	generatedPlanRetentionV61RuleSpec          = "synon.workspace.generated-plan-retention.v61"
)

var generatedPlanRetentionV61Migration = versionedSchemaMigration{
	version: 61,
	name:    "generated-plan-internal-retention",
	statements: []string{
		`UPDATE artifacts
		SET retention_mode='working_data'
		WHERE retention_mode='snapshot'
		  AND length(id)=37
		  AND id GLOB 'plan-[0-9a-f]*'
		  AND name GLOB 'plan_[0-9a-f]*.json'`,
	},
	identityV2: &schemaMigrationIdentityV2{
		CallbackID: generatedPlanRetentionV61CallbackID, RuleSpec: generatedPlanRetentionV61RuleSpec,
		PreflightIdentity: generatedPlanRetentionV61PreflightIdentity,
	},
}

func preflightGeneratedPlanRetentionV61(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	var count int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('artifacts')
		WHERE name='retention_mode'`).Scan(&count); err != nil {
		return errors.New("inspect generated plan retention authority")
	}
	if count != 1 {
		return errors.New("generated plan retention authority is unavailable")
	}
	return nil
}
