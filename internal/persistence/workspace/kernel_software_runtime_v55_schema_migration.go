package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const (
	kernelSoftwareRuntimeV55CallbackID        = "kernel-software-runtime-v55-rebuild-operation-head"
	kernelSoftwareRuntimeV55PreflightIdentity = "kernel-software-runtime-operation-head-v1"
	kernelSoftwareRuntimeV55RuleSpec          = "synon.workspace.kernel-software-runtime.v55"
)

var kernelSoftwareRuntimeV55Migration = versionedSchemaMigration{
	version: 55,
	name:    "kernel-software-runtime-authority",
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        kernelSoftwareRuntimeV55CallbackID,
		RuleSpec:          kernelSoftwareRuntimeV55RuleSpec,
		PreflightIdentity: kernelSoftwareRuntimeV55PreflightIdentity,
	},
}

func preflightKernelSoftwareRuntimeV55(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	var tableSQL string
	if err := executor.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema
		WHERE type='table' AND name='kernel_local_operations'`).Scan(&tableSQL); err != nil {
		return errors.New("inspect kernel local operation head")
	}
	if !strings.Contains(tableSQL, "tool IN ('python','r','repl')") || strings.Contains(tableSQL, "software_runtime") {
		return errors.New("kernel local operation tool authority is not the expected v54 shape")
	}
	var polluted int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
		WHERE name='kernel_local_operations_v39'`).Scan(&polluted); err != nil || polluted != 0 {
		return errors.New("kernel software runtime migration staging is polluted")
	}
	var foreignKeyFailures int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&foreignKeyFailures); err != nil || foreignKeyFailures != 0 {
		return errors.New("kernel software runtime migration requires a valid foreign key graph")
	}
	return nil
}

func rebuildKernelLocalOperationHeadV55(ctx context.Context, tx *sql.Tx) (returnErr error) {
	if tx == nil {
		return errors.New("kernel software runtime migration transaction is required")
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA legacy_alter_table=ON`); err != nil {
		return errors.New("enable legacy table rename for kernel operation rebuild")
	}
	legacyRenameEnabled := true
	defer func() {
		if !legacyRenameEnabled {
			return
		}
		if _, err := tx.ExecContext(context.Background(), `PRAGMA legacy_alter_table=OFF`); err != nil && returnErr == nil {
			returnErr = errors.New("restore SQLite table rename policy")
		}
	}()

	var before int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM kernel_local_operations`).Scan(&before); err != nil {
		return errors.New("count kernel operations before rebuild")
	}
	for _, trigger := range []string{
		"kernel_local_operations_identity_immutable",
		"kernel_local_operations_state_transition_valid",
		"kernel_local_operations_confinement_assignment",
		"kernel_local_operations_admission_assignment",
		"kernel_local_operation_transition_initial",
		"kernel_local_operation_transition_append",
		"kernel_local_operations_delete_forbidden",
	} {
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER `+trigger); err != nil {
			return fmt.Errorf("drop kernel operation trigger %s: %w", trigger, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE kernel_local_operations RENAME TO kernel_local_operations_v39`); err != nil {
		return fmt.Errorf("stage kernel operation head: %w", err)
	}
	ddl := strings.Replace(
		kernelLocalOperationsDDL,
		"tool TEXT NOT NULL CHECK(tool IN ('python','r','repl'))",
		"tool TEXT NOT NULL CHECK(tool IN ('python','r','repl','software_runtime'))",
		1,
	)
	if ddl == kernelLocalOperationsDDL {
		return errors.New("kernel software runtime DDL replacement is unavailable")
	}
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("create kernel software operation head: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO kernel_local_operations SELECT * FROM kernel_local_operations_v39`); err != nil {
		return fmt.Errorf("copy kernel operation heads: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE kernel_local_operations_v39`); err != nil {
		return fmt.Errorf("remove staged kernel operation head: %w", err)
	}
	// Recreate the indexes and current trigger variants that were attached to
	// the replaced parent table. Child-table triggers remain in place and keep
	// referring to the canonical parent name under legacy rename semantics.
	for _, index := range []int{3, 4, 5, 6, 7, 8, 9, 11, 12} {
		if _, err := tx.ExecContext(ctx, kernelLocalOperationV39Migration.statements[index]); err != nil {
			return fmt.Errorf("restore kernel operation schema object %d: %w", index, err)
		}
	}
	if _, err := tx.ExecContext(ctx, runtimeAuditDeleteV46Migration.statements[2]); err != nil {
		return fmt.Errorf("restore scoped kernel operation deletion fence: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA legacy_alter_table=OFF`); err != nil {
		return errors.New("restore SQLite table rename policy")
	}
	legacyRenameEnabled = false

	var after int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM kernel_local_operations`).Scan(&after); err != nil || after != before {
		return fmt.Errorf("kernel operation row count changed during rebuild: before=%d after=%d", before, after)
	}
	var foreignKeyFailures int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&foreignKeyFailures); err != nil || foreignKeyFailures != 0 {
		return errors.New("kernel software runtime rebuild broke the foreign key graph")
	}
	var staged, triggers, indexes int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name='kernel_local_operations_v39'`).Scan(&staged); err != nil || staged != 0 {
		return errors.New("kernel software runtime migration left a staging table")
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
		WHERE type='trigger' AND tbl_name='kernel_local_operations'`).Scan(&triggers); err != nil || triggers != 7 {
		return fmt.Errorf("kernel operation trigger set is incomplete: %d", triggers)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
		WHERE type='index' AND tbl_name='kernel_local_operations' AND sql IS NOT NULL`).Scan(&indexes); err != nil || indexes != 3 {
		return fmt.Errorf("kernel operation index set is incomplete: %d", indexes)
	}
	var tableSQL string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema
		WHERE type='table' AND name='kernel_local_operations'`).Scan(&tableSQL); err != nil ||
		!strings.Contains(tableSQL, "'software_runtime'") {
		return errors.New("kernel software runtime tool constraint was not published")
	}
	return nil
}
