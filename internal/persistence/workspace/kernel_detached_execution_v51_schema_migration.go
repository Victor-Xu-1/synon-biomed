package workspace

import (
	"context"
	"errors"
)

const (
	kernelDetachedExecutionV51CallbackID        = "kernel-detached-execution-v51-recreate-triggers"
	kernelDetachedExecutionV51PreflightIdentity = "kernel-detached-execution-v51-backend-recreate-v1"
	kernelDetachedExecutionV51RuleSpec          = "synon.workspace.kernel-detached-execution-backend-recreate.v51"
)

// The v44 triggers treated every field of kernel_execution_backends as
// immutable and forbade terminal rows from transitioning back to 'starting'.
// After a service restart the evidence_lost row must be reusable in place:
// the historical kernel_detached_executions rows keep their old
// backend_generation references, while the live session row advances to a
// fresh executor generation. Only that recreation path may mutate
// executor_instance_id/machine_boot_id/socket_path/backend_generation.
var kernelDetachedExecutionV51Migration = versionedSchemaMigration{
	version: 51,
	name:    "kernel-detached-execution-backend-recreate",
	statements: []string{
		`DROP TRIGGER kernel_execution_backends_identity_immutable`,
		`DROP TRIGGER kernel_execution_backends_transition_valid`,
		`CREATE TRIGGER kernel_execution_backends_identity_immutable BEFORE UPDATE ON kernel_execution_backends
WHEN NEW.backend_id!=OLD.backend_id OR NEW.owner_user_id!=OLD.owner_user_id OR NEW.project_id!=OLD.project_id OR
	NEW.root_frame_id!=OLD.root_frame_id OR NEW.root_frame_incarnation_id!=OLD.root_frame_incarnation_id OR
	NEW.frame_id!=OLD.frame_id OR NEW.frame_incarnation_id!=OLD.frame_incarnation_id OR NEW.kernel_id!=OLD.kernel_id OR
	NEW.kernel_generation!=OLD.kernel_generation OR NEW.protocol_version!=OLD.protocol_version OR
	NEW.created_at!=OLD.created_at
	OR (
		NEW.backend_generation!=OLD.backend_generation+1
		AND (NEW.backend_generation!=OLD.backend_generation OR NEW.executor_instance_id!=OLD.executor_instance_id OR
			NEW.machine_boot_id!=OLD.machine_boot_id OR NEW.socket_path!=OLD.socket_path)
	)
BEGIN SELECT RAISE(ABORT,'kernel execution backend identity is immutable'); END`,
		`CREATE TRIGGER kernel_execution_backends_transition_valid BEFORE UPDATE ON kernel_execution_backends
WHEN (NEW.state_version!=OLD.state_version+1 AND NOT (
		OLD.state IN ('stopped','evidence_lost') AND NEW.state='starting'
		AND NEW.backend_generation=OLD.backend_generation+1 AND NEW.state_version=1
	)) OR NOT (
	(OLD.state='starting' AND NEW.state IN ('starting','ready','stopped','evidence_lost')) OR
	(OLD.state='ready' AND NEW.state IN ('ready','draining','stopped','evidence_lost')) OR
	(OLD.state='draining' AND NEW.state IN ('draining','stopped','evidence_lost')) OR
	(OLD.state='stopped' AND NEW.state='starting') OR
	(OLD.state='evidence_lost' AND NEW.state='starting')
)
BEGIN SELECT RAISE(ABORT,'kernel execution backend transition is invalid'); END`,
	},
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        kernelDetachedExecutionV51CallbackID,
		RuleSpec:          kernelDetachedExecutionV51RuleSpec,
		PreflightIdentity: kernelDetachedExecutionV51PreflightIdentity,
	},
}

func preflightKernelDetachedExecutionV51(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	var tables int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name IN (
		'kernel_execution_backends','kernel_detached_executions',
		'kernel_execution_result_receipts','kernel_execution_host_calls'
	)`).Scan(&tables); err != nil {
		return errors.New("inspect kernel detached execution v44 tables")
	}
	if tables != 4 {
		return errors.New("kernel detached execution v44 tables are required")
	}
	var identityTriggers, transitionTriggers int
	if err := executor.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM sqlite_schema WHERE type='trigger' AND name='kernel_execution_backends_identity_immutable'),
		(SELECT COUNT(*) FROM sqlite_schema WHERE type='trigger' AND name='kernel_execution_backends_transition_valid')
	`).Scan(&identityTriggers, &transitionTriggers); err != nil {
		return errors.New("inspect kernel detached execution backend triggers")
	}
	if identityTriggers != 1 || transitionTriggers != 1 {
		return errors.New("kernel detached execution backend triggers are required")
	}
	var alreadyUpgraded int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
		WHERE type='trigger' AND name='kernel_execution_backends_transition_valid'
		AND sql LIKE '%backend_generation+1%'`).Scan(&alreadyUpgraded); err != nil {
		return errors.New("inspect kernel detached execution v51 cohort")
	}
	if alreadyUpgraded != 0 {
		return errors.New("kernel detached execution v51 cohort is polluted")
	}
	return nil
}
