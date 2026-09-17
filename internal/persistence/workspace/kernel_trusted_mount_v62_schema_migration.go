package workspace

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
)

const (
	kernelTrustedMountV62CallbackID        = "kernel-trusted-mount-v62-backfill"
	kernelTrustedMountV62PreflightIdentity = "kernel-trusted-mount-v62-session-spec-v1"
	kernelTrustedMountV62RuleSpec          = "synon.workspace.kernel-trusted-readonly-mount.v62"
)

var kernelTrustedMountV62Migration = versionedSchemaMigration{
	version: 62,
	name:    "kernel-trusted-readonly-mount-backfill",
	identityV2: &schemaMigrationIdentityV2{
		CallbackID: kernelTrustedMountV62CallbackID, RuleSpec: kernelTrustedMountV62RuleSpec,
		PreflightIdentity: kernelTrustedMountV62PreflightIdentity,
	},
}

func preflightKernelTrustedMountV62(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	var table, columns, triggers int
	if err := executor.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='kernel_execution_backends'),
		(SELECT COUNT(*) FROM pragma_table_info('kernel_execution_backends')
			WHERE name IN ('session_spec_json','session_spec_sha256')),
		(SELECT COUNT(*) FROM sqlite_schema WHERE type='trigger' AND name IN (
			'kernel_execution_backends_v45_payload_immutable',
			'kernel_execution_backends_transition_valid'
		))`).Scan(&table, &columns, &triggers); err != nil {
		return errors.New("inspect trusted kernel mount migration authority")
	}
	if table != 1 || columns != 2 || triggers != 2 {
		return errors.New("trusted kernel mount migration authority is unavailable")
	}
	return nil
}

func backfillKernelTrustedMountV62(ctx context.Context, tx *sql.Tx) error {
	// A short-lived candidate once allocated kernel_generation>1 for a fresh
	// executor. Such rows can never become ready because each detached executor
	// owns one fresh Manager whose worker generation starts at one. They have no
	// detached execution evidence and are removed before restoring the canonical
	// generation-one session identity.
	if _, err := tx.ExecContext(ctx, `DELETE FROM kernel_execution_backends
		WHERE kernel_generation>1 AND state='starting' AND executor_pid IS NULL
		AND NOT EXISTS (
			SELECT 1 FROM kernel_detached_executions execution
			WHERE execution.backend_id=kernel_execution_backends.backend_id
		)`); err != nil {
		return err
	}

	type update struct{ backendID, specJSON, specSHA string }
	rows, err := tx.QueryContext(ctx, `SELECT backend_id,session_spec_json FROM kernel_execution_backends`)
	if err != nil {
		return err
	}
	updates := make([]update, 0)
	for rows.Next() {
		var backendID, raw string
		if err := rows.Scan(&backendID, &raw); err != nil {
			_ = rows.Close()
			return err
		}
		spec, err := DecodeKernelExecutionSessionSpecV1(raw)
		if err != nil {
			_ = rows.Close()
			return err
		}
		changed := false
		for index := range spec.Mounts {
			if !spec.Mounts[index].Trusted && legacyServerOwnedKernelMount(spec, spec.Mounts[index]) {
				spec.Mounts[index].Trusted = true
				changed = true
			}
		}
		if !changed {
			continue
		}
		_, canonical, digest, err := canonicalKernelExecutionSessionSpec(spec)
		if err != nil {
			_ = rows.Close()
			return err
		}
		updates = append(updates, update{backendID: backendID, specJSON: canonical, specSHA: digest})
	}
	if err := rows.Close(); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `DROP TRIGGER kernel_execution_backends_v45_payload_immutable`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DROP TRIGGER kernel_execution_backends_transition_valid`); err != nil {
		return err
	}
	for _, item := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE kernel_execution_backends
			SET session_spec_json=?,session_spec_sha256=? WHERE backend_id=?`,
			item.specJSON, item.specSHA, item.backendID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `CREATE TRIGGER kernel_execution_backends_v45_payload_immutable BEFORE UPDATE ON kernel_execution_backends
		WHEN NEW.session_spec_json!=OLD.session_spec_json OR NEW.session_spec_sha256!=OLD.session_spec_sha256
		BEGIN SELECT RAISE(ABORT,'kernel execution backend session specification is immutable'); END`); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `CREATE TRIGGER kernel_execution_backends_transition_valid BEFORE UPDATE ON kernel_execution_backends
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
BEGIN SELECT RAISE(ABORT,'kernel execution backend transition is invalid'); END`)
	return err
}

func legacyServerOwnedKernelMount(spec KernelExecutionSessionSpecV1, mount KernelExecutionMountSpecV1) bool {
	if mount.Writable || mount.Trusted || filepath.Base(mount.Path) != "skills" {
		return false
	}
	mountPath := filepath.Clean(mount.Path)
	for _, protected := range spec.ProtectedPaths {
		protectedPath := filepath.Clean(protected)
		if relative, err := filepath.Rel(protectedPath, mountPath); err == nil &&
			relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
		if filepath.Base(protectedPath) == "optional" && filepath.Base(filepath.Dir(protectedPath)) == "assets" &&
			mountPath == filepath.Join(filepath.Dir(filepath.Dir(protectedPath)), "skills") {
			return true
		}
	}
	return false
}
