package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const (
	kernelStartupV69CallbackID        = "kernel-startup-v69-receipt-authority"
	kernelStartupV69PreflightIdentity = "kernel-startup-v69-backend-head-v1"
	kernelStartupV69RuleSpec          = "synon.workspace.kernel-startup.v69"
	kernelBackendProcessV68           = "CHECK(state='starting' OR (executor_pid IS NOT NULL AND executor_pid_start_ticks IS NOT NULL))"
	kernelBackendProcessV69           = "CHECK(state IN ('starting','stopped') OR (executor_pid IS NOT NULL AND executor_pid_start_ticks IS NOT NULL))"
)

var kernelStartupV69Migration = versionedSchemaMigration{
	version: 69, name: "kernel-startup-failure-receipts",
	identityV2: &schemaMigrationIdentityV2{CallbackID: kernelStartupV69CallbackID,
		PreflightIdentity: kernelStartupV69PreflightIdentity, RuleSpec: kernelStartupV69RuleSpec},
}

func preflightKernelStartupV69(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	var ddl string
	if err := executor.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type='table' AND name='kernel_execution_backends'`).Scan(&ddl); err != nil {
		return err
	}
	if strings.Count(ddl, kernelBackendProcessV68) != 1 {
		return errors.New("kernel startup migration requires the v68 process identity constraint")
	}
	var polluted, violations int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name IN ('kernel_execution_backends_v68','kernel_execution_startup_receipts')`).Scan(&polluted); err != nil || polluted != 0 {
		return errors.New("kernel startup migration staging is polluted")
	}
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		return errors.New("kernel startup migration requires a valid reference graph")
	}
	return nil
}

// The backend head remains the only execution authority. Receipts preserve the
// reason an unstarted generation stopped, without inventing a process identity.
func migrateKernelStartupV69(ctx context.Context, tx *sql.Tx) (returnErr error) {
	if err := preflightKernelStartupV69(ctx, tx); err != nil {
		return err
	}
	var ddl string
	var before int64
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE name='kernel_execution_backends'`).Scan(&ddl); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM kernel_execution_backends`).Scan(&before); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT sql FROM sqlite_schema WHERE tbl_name='kernel_execution_backends' AND type IN ('index','trigger') AND sql IS NOT NULL ORDER BY type,name`)
	if err != nil {
		return err
	}
	var objects []string
	for rows.Next() {
		var statement string
		if err := rows.Scan(&statement); err != nil {
			_ = rows.Close()
			return err
		}
		objects = append(objects, statement)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	var renamePolicy int
	if err := tx.QueryRowContext(ctx, `PRAGMA legacy_alter_table`).Scan(&renamePolicy); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA legacy_alter_table=ON`); err != nil {
		return err
	}
	defer func() {
		statement := `PRAGMA legacy_alter_table=OFF`
		if renamePolicy == 1 {
			statement = `PRAGMA legacy_alter_table=ON`
		}
		_, err := tx.ExecContext(context.WithoutCancel(ctx), statement)
		returnErr = errors.Join(returnErr, err)
	}()
	statements := []string{
		`ALTER TABLE kernel_execution_backends RENAME TO kernel_execution_backends_v68`,
		strings.Replace(ddl, kernelBackendProcessV68, kernelBackendProcessV69, 1),
		`INSERT INTO kernel_execution_backends SELECT * FROM kernel_execution_backends_v68`,
		`DROP TABLE kernel_execution_backends_v68`,
	}
	statements = append(statements, objects...)
	statements = append(statements,
		`CREATE INDEX kernel_execution_backends_starting_inventory ON kernel_execution_backends(backend_id) WHERE state='starting'`,
		`CREATE TABLE kernel_execution_startup_receipts (
			backend_id TEXT NOT NULL REFERENCES kernel_execution_backends(backend_id) ON DELETE CASCADE,
			backend_generation INTEGER NOT NULL CHECK(backend_generation>0),
			executor_instance_id TEXT NOT NULL CHECK(length(executor_instance_id)>0),
			stage TEXT NOT NULL CHECK(stage IN ('launch','runtime_discovery','session_spec','resource_domain','worker_start','worker_identity','control_socket','activation','process_exit')),
			created_at TEXT NOT NULL,
			PRIMARY KEY(backend_id,backend_generation)
		)`,
		`CREATE TRIGGER kernel_startup_receipt_owner BEFORE INSERT ON kernel_execution_startup_receipts
		WHEN NOT EXISTS(SELECT 1 FROM kernel_execution_backends b WHERE b.backend_id=NEW.backend_id
		AND b.backend_generation=NEW.backend_generation AND b.executor_instance_id=NEW.executor_instance_id AND b.state='starting')
		BEGIN SELECT RAISE(ABORT,'kernel startup receipt authority is stale'); END`,
		`CREATE TRIGGER kernel_startup_receipt_immutable BEFORE UPDATE ON kernel_execution_startup_receipts
		BEGIN SELECT RAISE(ABORT,'kernel startup receipt is immutable'); END`,
		`CREATE TRIGGER kernel_startup_terminal_receipt BEFORE UPDATE ON kernel_execution_backends
		WHEN OLD.state='starting' AND NEW.state='stopped' AND NOT EXISTS(
		SELECT 1 FROM kernel_execution_startup_receipts r WHERE r.backend_id=NEW.backend_id
		AND r.backend_generation=NEW.backend_generation AND r.executor_instance_id=NEW.executor_instance_id)
		BEGIN SELECT RAISE(ABORT,'kernel startup terminal receipt is required'); END`,
		`CREATE TRIGGER kernel_unstarted_backend_insert BEFORE INSERT ON kernel_execution_backends
		WHEN NEW.state!='starting' AND (NEW.executor_pid IS NULL OR NEW.executor_pid_start_ticks IS NULL)
		BEGIN SELECT RAISE(ABORT,'kernel terminal backend requires process identity'); END`,
		// A generation may only advance after its predecessor has a terminal
		// state. This retires the old starting-to-starting replacement loophole.
		`CREATE TRIGGER kernel_startup_generation_fence BEFORE UPDATE ON kernel_execution_backends
		WHEN NEW.backend_generation!=OLD.backend_generation AND NOT (
		OLD.state IN ('stopped','evidence_lost') AND NEW.state='starting'
		AND NEW.backend_generation=OLD.backend_generation+1)
		BEGIN SELECT RAISE(ABORT,'kernel predecessor is not terminal'); END`,
		`CREATE TRIGGER kernel_startup_process_fence BEFORE UPDATE ON kernel_execution_backends
		WHEN NEW.backend_generation=OLD.backend_generation AND OLD.executor_pid IS NOT NULL AND (
		NEW.executor_pid IS NOT OLD.executor_pid OR NEW.executor_pid_start_ticks IS NOT OLD.executor_pid_start_ticks)
		BEGIN SELECT RAISE(ABORT,'kernel executor process identity is immutable'); END`,
	)
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate kernel startup authority: %w", err)
		}
	}
	var after, violations int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM kernel_execution_backends`).Scan(&after); err != nil || before != after {
		return fmt.Errorf("kernel startup migration changed backend count: %d to %d: %v", before, after, err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		return fmt.Errorf("kernel startup migration broke references: %d: %v", violations, err)
	}
	return nil
}
