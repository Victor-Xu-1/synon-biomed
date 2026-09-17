package workspace

import (
	"context"
	"errors"
	"strings"
)

const (
	toolBatchV57CallbackID        = "tool-batch-v57-noop"
	toolBatchV57PreflightIdentity = "tool-batch-prestart-failure-v1"
	toolBatchV57RuleSpec          = "synon.workspace.tool-batch-prestart-failure.v57"
)

var toolBatchV57Migration = versionedSchemaMigration{
	version: 57,
	name:    "tool-batch-model-visible-prestart-failure",
	statements: []string{
		`DROP TRIGGER transcript_tool_call_batches_transition_valid`,
		`CREATE TRIGGER transcript_tool_call_batches_transition_valid BEFORE UPDATE ON transcript_tool_call_batches
		WHEN NEW.state_version!=OLD.state_version+1 OR NOT (
			(OLD.state='ready' AND NEW.state IN ('ready','running','settled','cancelled','outcome_unknown')) OR
			(OLD.state='running' AND NEW.state IN ('running','ready','waiting','settled','cancelled','outcome_unknown')) OR
			(OLD.state='waiting' AND NEW.state IN ('waiting','ready','settled','cancelled','outcome_unknown'))
		)
		BEGIN SELECT RAISE(ABORT,'tool call batch transition is invalid'); END`,
		`DROP TRIGGER transcript_tool_call_batch_items_transition_valid`,
		`CREATE TRIGGER transcript_tool_call_batch_items_transition_valid BEFORE UPDATE ON transcript_tool_call_items
		WHEN NEW.state_version!=OLD.state_version+1 OR NOT (
			(OLD.state='pending' AND NEW.state IN ('running','failed','cancelled','outcome_unknown')) OR
			(OLD.state='running' AND NEW.state IN ('waiting','completed','failed','blocked','cancelled','outcome_unknown')) OR
			(OLD.state='waiting' AND NEW.state IN ('completed','failed','blocked','cancelled','outcome_unknown'))
		)
		BEGIN SELECT RAISE(ABORT,'tool call batch item transition is invalid'); END`,
	},
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        toolBatchV57CallbackID,
		RuleSpec:          toolBatchV57RuleSpec,
		PreflightIdentity: toolBatchV57PreflightIdentity,
	},
}

func preflightToolBatchV57(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	var batchTrigger, itemTrigger string
	if err := executor.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema
		WHERE type='trigger' AND name='transcript_tool_call_batches_transition_valid'`).Scan(&batchTrigger); err != nil {
		return errors.New("inspect tool batch transition trigger")
	}
	if err := executor.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema
		WHERE type='trigger' AND name='transcript_tool_call_batch_items_transition_valid'`).Scan(&itemTrigger); err != nil {
		return errors.New("inspect tool batch item transition trigger")
	}
	if strings.Contains(batchTrigger, "'settled'") && strings.Contains(itemTrigger, "'pending' AND NEW.state IN ('running','failed'") {
		return errors.New("tool batch pre-start failure transition is already installed")
	}
	if !strings.Contains(batchTrigger, "OLD.state='ready'") || !strings.Contains(itemTrigger, "OLD.state='pending'") {
		return errors.New("tool batch transition schema is unavailable")
	}
	return nil
}
