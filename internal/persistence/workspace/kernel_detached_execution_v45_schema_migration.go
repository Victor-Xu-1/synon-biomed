package workspace

import (
	"context"
	"errors"
)

const (
	kernelDetachedExecutionV45CallbackID        = "kernel-detached-execution-v45-noop"
	kernelDetachedExecutionV45PreflightIdentity = "kernel-detached-execution-v44-empty-upgrade-v1"
	kernelDetachedExecutionV45RuleSpec          = "synon.workspace.kernel-detached-execution-payload.v45"
	kernelDetachedExecutionV45EmptySHA256       = "44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a"
)

var kernelDetachedExecutionV45Migration = versionedSchemaMigration{
	version: 45,
	name:    "kernel-detached-execution-payload-authority",
	statements: []string{
		`ALTER TABLE kernel_execution_backends ADD COLUMN session_spec_json TEXT NOT NULL DEFAULT '{}' CHECK(
			json_valid(session_spec_json) AND json_type(session_spec_json)='object' AND
			length(CAST(session_spec_json AS BLOB)) BETWEEN 2 AND 1048576
		)`,
		`ALTER TABLE kernel_execution_backends ADD COLUMN session_spec_sha256 TEXT NOT NULL
			DEFAULT '44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a' CHECK(
				length(session_spec_sha256)=64 AND session_spec_sha256 NOT GLOB '*[^0-9a-f]*'
			)`,
		`ALTER TABLE kernel_detached_executions ADD COLUMN request_json TEXT NOT NULL DEFAULT '{}' CHECK(
			json_valid(request_json) AND json_type(request_json)='object' AND
			length(CAST(request_json AS BLOB)) BETWEEN 2 AND 1048576
		)`,
		`CREATE TRIGGER kernel_execution_backends_v45_payload_required BEFORE INSERT ON kernel_execution_backends
		WHEN NEW.session_spec_json='{}' OR NEW.session_spec_sha256='44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a'
		BEGIN SELECT RAISE(ABORT,'kernel execution backend session specification is required'); END`,
		`CREATE TRIGGER kernel_execution_backends_v45_payload_immutable BEFORE UPDATE ON kernel_execution_backends
		WHEN NEW.session_spec_json!=OLD.session_spec_json OR NEW.session_spec_sha256!=OLD.session_spec_sha256
		BEGIN SELECT RAISE(ABORT,'kernel execution backend session specification is immutable'); END`,
		`CREATE TRIGGER kernel_detached_executions_v45_payload_required BEFORE INSERT ON kernel_detached_executions
		WHEN NEW.request_json='{}'
		BEGIN SELECT RAISE(ABORT,'kernel detached execution request is required'); END`,
		`CREATE TRIGGER kernel_detached_executions_v45_payload_immutable BEFORE UPDATE ON kernel_detached_executions
		WHEN NEW.request_json!=OLD.request_json
		BEGIN SELECT RAISE(ABORT,'kernel detached execution request is immutable'); END`,
	},
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        kernelDetachedExecutionV45CallbackID,
		RuleSpec:          kernelDetachedExecutionV45RuleSpec,
		PreflightIdentity: kernelDetachedExecutionV45PreflightIdentity,
	},
}

func preflightKernelDetachedExecutionV45(ctx context.Context, executor schemaMigrationQueryExecutor) error {
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

	var rows int
	if err := executor.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM kernel_execution_backends) +
		(SELECT COUNT(*) FROM kernel_detached_executions) +
		(SELECT COUNT(*) FROM kernel_execution_result_receipts) +
		(SELECT COUNT(*) FROM kernel_execution_host_calls)`).Scan(&rows); err != nil {
		return errors.New("inspect kernel detached execution v44 rows")
	}
	if rows != 0 {
		return errors.New("kernel detached execution v44 upgrade requires an empty unpublished cohort")
	}

	var polluted int
	if err := executor.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM pragma_table_info('kernel_execution_backends')
			WHERE name IN ('session_spec_json','session_spec_sha256')) +
		(SELECT COUNT(*) FROM pragma_table_info('kernel_detached_executions') WHERE name='request_json') +
		(SELECT COUNT(*) FROM sqlite_schema WHERE name IN (
			'kernel_execution_backends_v45_payload_required',
			'kernel_execution_backends_v45_payload_immutable',
			'kernel_detached_executions_v45_payload_required',
			'kernel_detached_executions_v45_payload_immutable'
		))`).Scan(&polluted); err != nil {
		return errors.New("inspect kernel detached execution v45 cohort")
	}
	if polluted != 0 {
		return errors.New("kernel detached execution v45 cohort is polluted")
	}
	return nil
}
