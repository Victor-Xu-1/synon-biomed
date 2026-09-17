package workspace

import (
	"context"
	"errors"
)

const (
	kernelDetachedExecutionV44CallbackID        = "kernel-detached-execution-v44-noop"
	kernelDetachedExecutionV44PreflightIdentity = "kernel-detached-execution-empty-cohort-v1"
	kernelDetachedExecutionV44RuleSpec          = "synon.workspace.kernel-detached-execution.v44"
)

var kernelDetachedExecutionV44Migration = versionedSchemaMigration{
	version: 44,
	name:    "kernel-detached-execution-authority",
	statements: []string{
		`CREATE TABLE kernel_execution_backends (
			backend_id TEXT PRIMARY KEY,
			owner_user_id TEXT NOT NULL,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
			root_frame_id TEXT NOT NULL REFERENCES frames(id) ON DELETE RESTRICT,
			root_frame_incarnation_id TEXT NOT NULL,
			frame_id TEXT NOT NULL REFERENCES frames(id) ON DELETE RESTRICT,
			frame_incarnation_id TEXT NOT NULL,
			kernel_id TEXT NOT NULL,
			kernel_generation INTEGER NOT NULL CHECK(kernel_generation>0),
			protocol_version INTEGER NOT NULL CHECK(protocol_version=1),
			executor_instance_id TEXT NOT NULL,
			machine_boot_id TEXT NOT NULL,
			executor_pid INTEGER CHECK(executor_pid IS NULL OR executor_pid>0),
			executor_pid_start_ticks INTEGER CHECK(executor_pid_start_ticks IS NULL OR executor_pid_start_ticks>0),
			worker_pid INTEGER CHECK(worker_pid IS NULL OR worker_pid>0),
			worker_pid_start_ticks INTEGER CHECK(worker_pid_start_ticks IS NULL OR worker_pid_start_ticks>0),
			worker_pgid INTEGER CHECK(worker_pgid IS NULL OR worker_pgid>0),
			cgroup_path TEXT NOT NULL DEFAULT '' CHECK(length(CAST(cgroup_path AS BLOB))<=4096),
			socket_path TEXT NOT NULL CHECK(length(CAST(socket_path AS BLOB)) BETWEEN 1 AND 4096),
			backend_generation INTEGER NOT NULL CHECK(backend_generation>0),
			state TEXT NOT NULL CHECK(state IN ('starting','ready','draining','stopped','evidence_lost')),
			state_version INTEGER NOT NULL CHECK(state_version>0),
			heartbeat_sequence INTEGER NOT NULL DEFAULT 0 CHECK(heartbeat_sequence>=0),
			heartbeat_at TEXT,
			controller_epoch INTEGER NOT NULL DEFAULT 0 CHECK(controller_epoch>=0),
			controller_token_sha256 BLOB CHECK(controller_token_sha256 IS NULL OR length(controller_token_sha256)=32),
			controller_lease_expires_at TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE(owner_user_id,frame_id,kernel_id,kernel_generation),
			CHECK((controller_token_sha256 IS NULL AND controller_lease_expires_at IS NULL) OR
				(controller_epoch>0 AND controller_token_sha256 IS NOT NULL AND controller_lease_expires_at IS NOT NULL)),
			CHECK(state='starting' OR (executor_pid IS NOT NULL AND executor_pid_start_ticks IS NOT NULL))
		) STRICT`,
		`CREATE TABLE kernel_detached_executions (
			execution_id TEXT PRIMARY KEY,
			operation_id TEXT NOT NULL UNIQUE REFERENCES kernel_local_operations(operation_id) ON DELETE RESTRICT,
			backend_id TEXT NOT NULL REFERENCES kernel_execution_backends(backend_id) ON DELETE RESTRICT,
			backend_generation INTEGER NOT NULL CHECK(backend_generation>0),
			request_sha256 TEXT NOT NULL CHECK(length(request_sha256)=64 AND request_sha256 NOT GLOB '*[^0-9a-f]*'),
			confinement_sha256 TEXT NOT NULL CHECK(length(confinement_sha256)=64 AND confinement_sha256 NOT GLOB '*[^0-9a-f]*'),
			state TEXT NOT NULL CHECK(state IN (
				'accepted','dispatch_committed','started','cancel_requested','terminal','evidence_lost'
			)),
			state_version INTEGER NOT NULL CHECK(state_version>0),
			dispatch_sequence INTEGER NOT NULL DEFAULT 0 CHECK(dispatch_sequence>=0),
			accepted_at TEXT NOT NULL,
			dispatch_committed_at TEXT,
			request_written_at TEXT,
			worker_started_at TEXT,
			last_observation_sequence INTEGER NOT NULL DEFAULT 0 CHECK(last_observation_sequence>=0),
			stdout_tail TEXT NOT NULL DEFAULT '' CHECK(length(CAST(stdout_tail AS BLOB))<=262144),
			cancel_request_id TEXT,
			cancel_requested_at TEXT,
			cancel_ack_sequence INTEGER CHECK(cancel_ack_sequence IS NULL OR cancel_ack_sequence>0),
			cancel_ack_at TEXT,
			cancel_signal TEXT CHECK(cancel_signal IS NULL OR cancel_signal IN ('dequeue','sigint','sigterm','sigkill')),
			terminal_receipt_id TEXT UNIQUE REFERENCES kernel_execution_result_receipts(receipt_id) ON DELETE RESTRICT,
			reason_code TEXT NOT NULL DEFAULT '' CHECK(length(CAST(reason_code AS BLOB))<=256),
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			CHECK((cancel_request_id IS NULL AND cancel_requested_at IS NULL) OR
				(cancel_request_id IS NOT NULL AND cancel_requested_at IS NOT NULL)),
			CHECK((cancel_ack_sequence IS NULL AND cancel_ack_at IS NULL AND cancel_signal IS NULL) OR
				(cancel_request_id IS NOT NULL AND cancel_ack_sequence IS NOT NULL AND cancel_ack_at IS NOT NULL AND cancel_signal IS NOT NULL)),
			CHECK(
				(state='accepted' AND dispatch_committed_at IS NULL AND request_written_at IS NULL AND worker_started_at IS NULL AND terminal_receipt_id IS NULL) OR
				(state='dispatch_committed' AND dispatch_committed_at IS NOT NULL AND worker_started_at IS NULL AND terminal_receipt_id IS NULL) OR
				(state='started' AND dispatch_committed_at IS NOT NULL AND request_written_at IS NOT NULL AND worker_started_at IS NOT NULL AND terminal_receipt_id IS NULL) OR
				(state='cancel_requested' AND cancel_request_id IS NOT NULL AND terminal_receipt_id IS NULL) OR
				(state='terminal' AND terminal_receipt_id IS NOT NULL) OR
				(state='evidence_lost' AND terminal_receipt_id IS NULL AND reason_code!='')
			)
		) STRICT`,
		`CREATE TABLE kernel_execution_result_receipts (
			receipt_id TEXT PRIMARY KEY,
			execution_id TEXT NOT NULL UNIQUE REFERENCES kernel_detached_executions(execution_id) ON DELETE RESTRICT,
			operation_id TEXT NOT NULL UNIQUE REFERENCES kernel_local_operations(operation_id) ON DELETE RESTRICT,
			backend_id TEXT NOT NULL REFERENCES kernel_execution_backends(backend_id) ON DELETE RESTRICT,
			backend_generation INTEGER NOT NULL CHECK(backend_generation>0),
			terminal_sequence INTEGER NOT NULL CHECK(terminal_sequence>0),
			outcome TEXT NOT NULL CHECK(outcome IN ('completed','failed','cancelled')),
			result_json TEXT NOT NULL CHECK(json_valid(result_json) AND length(CAST(result_json AS BLOB))<=1048576),
			result_ref TEXT CHECK(result_ref IS NULL OR (
				length(CAST(result_ref AS BLOB))<=4096 AND result_ref GLOB 'artifact-version:*'
			)),
			result_sha256 TEXT NOT NULL CHECK(length(result_sha256)=64 AND result_sha256 NOT GLOB '*[^0-9a-f]*'),
			execution_log_sha256 TEXT CHECK(execution_log_sha256 IS NULL OR (
				length(execution_log_sha256)=64 AND execution_log_sha256 NOT GLOB '*[^0-9a-f]*'
			)),
			exit_code INTEGER,
			termination_signal TEXT NOT NULL DEFAULT '' CHECK(length(CAST(termination_signal AS BLOB))<=64),
			interrupted INTEGER NOT NULL CHECK(interrupted IN (0,1)),
			timed_out INTEGER NOT NULL CHECK(timed_out IN (0,1)),
			started_at TEXT NOT NULL,
			finished_at TEXT NOT NULL,
			files_written_json TEXT NOT NULL DEFAULT '[]' CHECK(
				json_valid(files_written_json) AND json_type(files_written_json)='array' AND length(CAST(files_written_json AS BLOB))<=1048576
			),
			dropped_roots_json TEXT NOT NULL DEFAULT '[]' CHECK(
				json_valid(dropped_roots_json) AND json_type(dropped_roots_json)='array' AND length(CAST(dropped_roots_json AS BLOB))<=262144
			),
			created_at TEXT NOT NULL
		) STRICT`,
		`CREATE TABLE kernel_execution_host_calls (
			execution_id TEXT NOT NULL REFERENCES kernel_detached_executions(execution_id) ON DELETE RESTRICT,
			host_call_id TEXT NOT NULL,
			ordinal INTEGER NOT NULL CHECK(ordinal>=0 AND ordinal<32),
			method TEXT NOT NULL CHECK(length(CAST(method AS BLOB)) BETWEEN 1 AND 256),
			request_json TEXT NOT NULL CHECK(json_valid(request_json) AND length(CAST(request_json AS BLOB))<=1048576),
			request_sha256 TEXT NOT NULL CHECK(length(request_sha256)=64 AND request_sha256 NOT GLOB '*[^0-9a-f]*'),
			state TEXT NOT NULL CHECK(state IN ('pending','executing','completed','failed','outcome_unknown')),
			state_version INTEGER NOT NULL CHECK(state_version>0),
			claim_epoch INTEGER CHECK(claim_epoch IS NULL OR claim_epoch>0),
			claim_token_sha256 BLOB CHECK(claim_token_sha256 IS NULL OR length(claim_token_sha256)=32),
			claim_expires_at TEXT,
			result_json TEXT CHECK(result_json IS NULL OR (json_valid(result_json) AND length(CAST(result_json AS BLOB))<=1048576)),
			result_ref TEXT CHECK(result_ref IS NULL OR length(CAST(result_ref AS BLOB))<=4096),
			result_sha256 TEXT CHECK(result_sha256 IS NULL OR (
				length(result_sha256)=64 AND result_sha256 NOT GLOB '*[^0-9a-f]*'
			)),
			reason_code TEXT NOT NULL DEFAULT '' CHECK(length(CAST(reason_code AS BLOB))<=256),
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			terminal_at TEXT,
			PRIMARY KEY(execution_id,host_call_id),
			UNIQUE(execution_id,ordinal),
			CHECK((claim_epoch IS NULL AND claim_token_sha256 IS NULL AND claim_expires_at IS NULL) OR
				(claim_epoch IS NOT NULL AND claim_token_sha256 IS NOT NULL AND claim_expires_at IS NOT NULL)),
			CHECK((state IN ('pending','executing') AND result_json IS NULL AND result_ref IS NULL AND result_sha256 IS NULL AND terminal_at IS NULL) OR
				(state IN ('completed','failed','outcome_unknown') AND result_json IS NOT NULL AND result_sha256 IS NOT NULL AND terminal_at IS NOT NULL))
		) STRICT`,
		`CREATE UNIQUE INDEX kernel_execution_backends_generation_unique
			ON kernel_execution_backends(backend_id,backend_generation)`,
		`CREATE INDEX kernel_execution_backends_recovery_due
			ON kernel_execution_backends(state,heartbeat_at,controller_lease_expires_at,backend_id)`,
		`CREATE UNIQUE INDEX kernel_detached_executions_one_active_per_backend
			ON kernel_detached_executions(backend_id) WHERE state IN ('accepted','dispatch_committed','started','cancel_requested')`,
		`CREATE INDEX kernel_detached_executions_recovery_due
			ON kernel_detached_executions(state,updated_at,execution_id)`,
		`CREATE INDEX kernel_execution_host_calls_recovery_due
			ON kernel_execution_host_calls(state,claim_expires_at,execution_id,ordinal)`,
		`CREATE TRIGGER kernel_execution_backends_identity_immutable BEFORE UPDATE ON kernel_execution_backends
		WHEN NEW.backend_id!=OLD.backend_id OR NEW.owner_user_id!=OLD.owner_user_id OR NEW.project_id!=OLD.project_id OR
			NEW.root_frame_id!=OLD.root_frame_id OR NEW.root_frame_incarnation_id!=OLD.root_frame_incarnation_id OR
			NEW.frame_id!=OLD.frame_id OR NEW.frame_incarnation_id!=OLD.frame_incarnation_id OR NEW.kernel_id!=OLD.kernel_id OR
			NEW.kernel_generation!=OLD.kernel_generation OR NEW.protocol_version!=OLD.protocol_version OR
			NEW.executor_instance_id!=OLD.executor_instance_id OR NEW.machine_boot_id!=OLD.machine_boot_id OR
			NEW.backend_generation!=OLD.backend_generation OR NEW.socket_path!=OLD.socket_path OR NEW.created_at!=OLD.created_at
		BEGIN SELECT RAISE(ABORT,'kernel execution backend identity is immutable'); END`,
		`CREATE TRIGGER kernel_execution_backends_transition_valid BEFORE UPDATE ON kernel_execution_backends
		WHEN NEW.state_version!=OLD.state_version+1 OR NOT (
			(OLD.state='starting' AND NEW.state IN ('starting','ready','stopped','evidence_lost')) OR
			(OLD.state='ready' AND NEW.state IN ('ready','draining','stopped','evidence_lost')) OR
			(OLD.state='draining' AND NEW.state IN ('draining','stopped','evidence_lost'))
		)
		BEGIN SELECT RAISE(ABORT,'kernel execution backend transition is invalid'); END`,
		`CREATE TRIGGER kernel_detached_executions_identity_immutable BEFORE UPDATE ON kernel_detached_executions
		WHEN NEW.execution_id!=OLD.execution_id OR NEW.operation_id!=OLD.operation_id OR NEW.backend_id!=OLD.backend_id OR
			NEW.backend_generation!=OLD.backend_generation OR NEW.request_sha256!=OLD.request_sha256 OR
			NEW.confinement_sha256!=OLD.confinement_sha256 OR NEW.accepted_at!=OLD.accepted_at OR NEW.created_at!=OLD.created_at
		BEGIN SELECT RAISE(ABORT,'kernel detached execution identity is immutable'); END`,
		`CREATE TRIGGER kernel_detached_executions_transition_valid BEFORE UPDATE ON kernel_detached_executions
		WHEN NEW.state_version!=OLD.state_version+1 OR NOT (
			(OLD.state='accepted' AND NEW.state IN ('dispatch_committed','cancel_requested','terminal','evidence_lost')) OR
			(OLD.state='dispatch_committed' AND NEW.state IN ('dispatch_committed','started','cancel_requested','terminal','evidence_lost')) OR
			(OLD.state='started' AND NEW.state IN ('started','cancel_requested','terminal','evidence_lost')) OR
			(OLD.state='cancel_requested' AND NEW.state IN ('cancel_requested','terminal','evidence_lost'))
		)
		BEGIN SELECT RAISE(ABORT,'kernel detached execution transition is invalid'); END`,
		`CREATE TRIGGER kernel_execution_result_receipts_immutable BEFORE UPDATE ON kernel_execution_result_receipts
		BEGIN SELECT RAISE(ABORT,'kernel execution result receipt is immutable'); END`,
		`CREATE TRIGGER kernel_execution_result_receipts_delete_forbidden BEFORE DELETE ON kernel_execution_result_receipts
		BEGIN SELECT RAISE(ABORT,'kernel execution result receipt is append-only'); END`,
		`CREATE TRIGGER kernel_execution_host_calls_identity_immutable BEFORE UPDATE ON kernel_execution_host_calls
		WHEN NEW.execution_id!=OLD.execution_id OR NEW.host_call_id!=OLD.host_call_id OR NEW.ordinal!=OLD.ordinal OR
			NEW.method!=OLD.method OR NEW.request_json!=OLD.request_json OR NEW.request_sha256!=OLD.request_sha256 OR
			NEW.created_at!=OLD.created_at
		BEGIN SELECT RAISE(ABORT,'kernel execution host call identity is immutable'); END`,
		`CREATE TRIGGER kernel_execution_host_calls_transition_valid BEFORE UPDATE ON kernel_execution_host_calls
		WHEN NEW.state_version!=OLD.state_version+1 OR NOT (
			(OLD.state='pending' AND NEW.state IN ('executing','failed','outcome_unknown')) OR
			(OLD.state='executing' AND NEW.state IN ('executing','completed','failed','outcome_unknown'))
		)
		BEGIN SELECT RAISE(ABORT,'kernel execution host call transition is invalid'); END`,
	},
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        kernelDetachedExecutionV44CallbackID,
		RuleSpec:          kernelDetachedExecutionV44RuleSpec,
		PreflightIdentity: kernelDetachedExecutionV44PreflightIdentity,
	},
}

func preflightKernelDetachedExecutionV44(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	var dependencies int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
		WHERE type='table' AND name IN (
			'projects','frames','kernel_local_operations','kernel_local_operation_materializations'
		)`).Scan(&dependencies); err != nil {
		return errors.New("inspect kernel detached execution dependencies")
	}
	if dependencies != 4 {
		return errors.New("kernel detached execution dependencies are required")
	}
	var polluted int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name IN (
		'kernel_execution_backends','kernel_detached_executions','kernel_execution_result_receipts',
		'kernel_execution_host_calls','kernel_execution_backends_generation_unique',
		'kernel_execution_backends_recovery_due','kernel_detached_executions_one_active_per_backend',
		'kernel_detached_executions_recovery_due','kernel_execution_host_calls_recovery_due',
		'kernel_execution_backends_identity_immutable','kernel_execution_backends_transition_valid',
		'kernel_detached_executions_identity_immutable','kernel_detached_executions_transition_valid',
		'kernel_execution_result_receipts_immutable','kernel_execution_result_receipts_delete_forbidden',
		'kernel_execution_host_calls_identity_immutable','kernel_execution_host_calls_transition_valid'
	)`).Scan(&polluted); err != nil {
		return errors.New("inspect kernel detached execution cohort")
	}
	if polluted != 0 {
		return errors.New("kernel detached execution cohort is polluted")
	}
	return nil
}
