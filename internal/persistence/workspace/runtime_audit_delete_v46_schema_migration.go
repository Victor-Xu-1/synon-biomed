package workspace

import (
	"context"
	"errors"
)

const (
	runtimeAuditDeleteV46CallbackID        = "runtime-audit-delete-v46-noop"
	runtimeAuditDeleteV46PreflightIdentity = "runtime-audit-delete-v45-upgrade-v1"
	runtimeAuditDeleteV46RuleSpec          = "synon.workspace.runtime-audit-delete.v46"
)

var runtimeAuditDeleteV46Migration = versionedSchemaMigration{
	version: 46,
	name:    "runtime-audit-scoped-deletion",
	statements: []string{
		`CREATE TABLE workspace_runtime_delete_scopes (
			scope_id TEXT PRIMARY KEY,
			owner_user_id TEXT NOT NULL,
			project_id TEXT NOT NULL,
			root_frame_id TEXT NOT NULL,
			created_at TEXT NOT NULL,
			UNIQUE(owner_user_id,project_id,root_frame_id)
		) STRICT`,
		`DROP TRIGGER kernel_local_operations_delete_forbidden`,
		`CREATE TRIGGER kernel_local_operations_delete_forbidden BEFORE DELETE ON kernel_local_operations
		WHEN NOT EXISTS (
			SELECT 1 FROM workspace_runtime_delete_scopes scope
			WHERE scope.owner_user_id=OLD.owner_user_id AND scope.project_id=OLD.project_id
				AND scope.root_frame_id=OLD.root_frame_id
		)
		BEGIN SELECT RAISE(ABORT,'kernel local operation head is append-only'); END`,
		`DROP TRIGGER kernel_local_operation_transitions_append_only`,
		`CREATE TRIGGER kernel_local_operation_transitions_append_only BEFORE DELETE ON kernel_local_operation_transitions
		WHEN NOT EXISTS (
			SELECT 1 FROM kernel_local_operations operation
			JOIN workspace_runtime_delete_scopes scope
				ON scope.owner_user_id=operation.owner_user_id AND scope.project_id=operation.project_id
				AND scope.root_frame_id=operation.root_frame_id
			WHERE operation.operation_id=OLD.operation_id
		)
		BEGIN SELECT RAISE(ABORT,'kernel local operation transition is append-only'); END`,
		`DROP TRIGGER kernel_local_operation_protocol_receipts_append_only`,
		`CREATE TRIGGER kernel_local_operation_protocol_receipts_append_only BEFORE DELETE ON kernel_local_operation_protocol_receipts
		WHEN NOT EXISTS (
			SELECT 1 FROM kernel_local_operations operation
			JOIN workspace_runtime_delete_scopes scope
				ON scope.owner_user_id=operation.owner_user_id AND scope.project_id=operation.project_id
				AND scope.root_frame_id=operation.root_frame_id
			WHERE operation.operation_id=OLD.operation_id
		)
		BEGIN SELECT RAISE(ABORT,'kernel local operation protocol receipt is append-only'); END`,
		`DROP TRIGGER kernel_local_operation_materialization_delete_forbidden`,
		`CREATE TRIGGER kernel_local_operation_materialization_delete_forbidden
		BEFORE DELETE ON kernel_local_operation_materializations
		WHEN NOT EXISTS (
			SELECT 1 FROM kernel_local_operations operation
			JOIN workspace_runtime_delete_scopes scope
				ON scope.owner_user_id=operation.owner_user_id AND scope.project_id=operation.project_id
				AND scope.root_frame_id=operation.root_frame_id
			WHERE operation.operation_id=OLD.operation_id
		)
		BEGIN SELECT RAISE(ABORT,'kernel tool result materialization is append-only'); END`,
		`DROP TRIGGER kernel_execution_result_receipts_delete_forbidden`,
		`CREATE TRIGGER kernel_execution_result_receipts_delete_forbidden BEFORE DELETE ON kernel_execution_result_receipts
		WHEN NOT EXISTS (
			SELECT 1 FROM kernel_local_operations operation
			JOIN workspace_runtime_delete_scopes scope
				ON scope.owner_user_id=operation.owner_user_id AND scope.project_id=operation.project_id
				AND scope.root_frame_id=operation.root_frame_id
			WHERE operation.operation_id=OLD.operation_id
		)
		BEGIN SELECT RAISE(ABORT,'kernel execution result receipt is append-only'); END`,
		`DROP TRIGGER transcript_tool_call_batches_delete_forbidden`,
		`CREATE TRIGGER transcript_tool_call_batches_delete_forbidden BEFORE DELETE ON transcript_tool_call_batches
		WHEN NOT EXISTS (
			SELECT 1 FROM transcript_streams stream
			JOIN workspace_runtime_delete_scopes scope
				ON scope.owner_user_id=stream.owner_id AND scope.project_id=stream.project_id
				AND scope.root_frame_id=stream.root_frame_id
			WHERE stream.stream_uid=OLD.stream_uid
		)
		BEGIN SELECT RAISE(ABORT,'tool call batch authority is append-only'); END`,
		`DROP TRIGGER transcript_tool_call_batch_items_delete_forbidden`,
		`CREATE TRIGGER transcript_tool_call_batch_items_delete_forbidden BEFORE DELETE ON transcript_tool_call_items
		WHEN NOT EXISTS (
			SELECT 1 FROM transcript_streams stream
			JOIN workspace_runtime_delete_scopes scope
				ON scope.owner_user_id=stream.owner_id AND scope.project_id=stream.project_id
				AND scope.root_frame_id=stream.root_frame_id
			WHERE stream.stream_uid=OLD.stream_uid
		)
		BEGIN SELECT RAISE(ABORT,'tool call batch item authority is append-only'); END`,
		`DROP TRIGGER scientific_compute_handle_authorities_append_only`,
		`CREATE TRIGGER scientific_compute_handle_authorities_append_only
		BEFORE DELETE ON scientific_compute_handle_authorities
		WHEN NOT EXISTS (
			SELECT 1 FROM workspace_runtime_delete_scopes scope
			WHERE scope.owner_user_id=OLD.owner_user_id AND scope.project_id=OLD.project_id
				AND scope.root_frame_id=OLD.root_frame_id
		)
		BEGIN SELECT RAISE(ABORT,'scientific compute handle authority is append-only'); END`,
	},
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        runtimeAuditDeleteV46CallbackID,
		RuleSpec:          runtimeAuditDeleteV46RuleSpec,
		PreflightIdentity: runtimeAuditDeleteV46PreflightIdentity,
	},
}

func preflightRuntimeAuditDeleteV46(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	var dependencies int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name IN (
		'kernel_local_operations','kernel_local_operation_transitions','kernel_local_operation_protocol_receipts',
		'kernel_local_operation_materializations','kernel_execution_result_receipts',
		'transcript_tool_call_batches','transcript_tool_call_items','scientific_compute_handle_authorities',
		'kernel_local_operations_delete_forbidden','kernel_local_operation_transitions_append_only',
		'kernel_local_operation_protocol_receipts_append_only',
		'kernel_local_operation_materialization_delete_forbidden',
		'kernel_execution_result_receipts_delete_forbidden','transcript_tool_call_batches_delete_forbidden',
		'transcript_tool_call_batch_items_delete_forbidden','scientific_compute_handle_authorities_append_only'
	)`).Scan(&dependencies); err != nil {
		return errors.New("inspect runtime audit deletion dependencies")
	}
	if dependencies != 16 {
		return errors.New("runtime audit deletion dependencies are required")
	}
	var polluted int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
		WHERE name='workspace_runtime_delete_scopes'`).Scan(&polluted); err != nil {
		return errors.New("inspect runtime audit deletion cohort")
	}
	if polluted != 0 {
		return errors.New("runtime audit deletion cohort is polluted")
	}
	return nil
}
