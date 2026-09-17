package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const (
	kernelProviderCancelV68CallbackID        = "kernel-provider-cancel-v68-rebuild-execution"
	kernelProviderCancelV68PreflightIdentity = "kernel-provider-cancel-v68-execution-head-v1"
	kernelProviderCancelV68RuleSpec          = "synon.workspace.kernel-provider-cancel.v68"
	kernelCancelSignalsV67                   = "cancel_signal IN ('dequeue','sigint','sigterm','sigkill')"
	kernelCancelSignalsV68                   = "cancel_signal IN ('dequeue','sigint','sigterm','sigkill','provider_cancel')"
)

var kernelProviderCancelV68Migration = versionedSchemaMigration{
	version: 68,
	name:    "kernel-provider-cancellation-authority",
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        kernelProviderCancelV68CallbackID,
		PreflightIdentity: kernelProviderCancelV68PreflightIdentity,
		RuleSpec:          kernelProviderCancelV68RuleSpec,
	},
}

func preflightKernelProviderCancelV68(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	var ddl string
	if err := executor.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type='table' AND name='kernel_detached_executions'`).Scan(&ddl); err != nil {
		return fmt.Errorf("inspect detached execution cancellation authority: %w", err)
	}
	if strings.Count(ddl, kernelCancelSignalsV67) != 1 || strings.Contains(ddl, "'provider_cancel'") {
		return errors.New("detached execution cancellation constraint is not the expected v67 shape")
	}
	var staging, foreignKeys int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name='kernel_detached_executions_v67'`).Scan(&staging); err != nil || staging != 0 {
		return errors.New("provider cancellation migration staging is polluted")
	}
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&foreignKeys); err != nil || foreignKeys != 0 {
		return errors.New("provider cancellation migration requires a valid foreign key graph")
	}
	return nil
}

// Rebuild only the execution head. The dedicated migration connection disables
// FK enforcement outside this transaction; legacy rename leaves all receipt and
// host-call references bound to the authoritative name, not the staging table.
func migrateKernelProviderCancelV68(ctx context.Context, tx *sql.Tx) (returnErr error) {
	if err := preflightKernelProviderCancelV68(ctx, tx); err != nil {
		return err
	}
	var ddl string
	var before int64
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type='table' AND name='kernel_detached_executions'`).Scan(&ddl); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM kernel_detached_executions`).Scan(&before); err != nil {
		return err
	}
	// Retain the installed indexes and triggers exactly, including payload and
	// identity fences introduced after the original table migration.
	rows, err := tx.QueryContext(ctx, `SELECT sql FROM sqlite_schema WHERE tbl_name='kernel_detached_executions'
		AND type IN ('index','trigger') AND sql IS NOT NULL ORDER BY type,name`)
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
	err = rows.Err()
	err = errors.Join(err, rows.Close())
	if err != nil {
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
		`ALTER TABLE kernel_detached_executions RENAME TO kernel_detached_executions_v67`,
		strings.Replace(ddl, kernelCancelSignalsV67, kernelCancelSignalsV68, 1),
		`INSERT INTO kernel_detached_executions SELECT * FROM kernel_detached_executions_v67`,
		`DROP TABLE kernel_detached_executions_v67`,
	}
	for _, statement := range append(statements, objects...) {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("rebuild provider cancellation authority: %w", err)
		}
	}
	var after, violations int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM kernel_detached_executions`).Scan(&after); err != nil || after != before {
		return fmt.Errorf("provider cancellation migration changed execution count: %d to %d: %v", before, after, err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		return fmt.Errorf("provider cancellation migration broke execution references: %d: %v", violations, err)
	}
	return nil
}
