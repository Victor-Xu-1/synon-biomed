package workspace

import (
	"context"
	"errors"
)

const (
	kernelResultSpoolV63CallbackID        = "kernel-result-spool-v63-noop"
	kernelResultSpoolV63PreflightIdentity = "kernel-result-receipt-v44-required"
	kernelResultSpoolV63RuleSpec          = "synon.workspace.kernel-result-spool.v63"
)

var kernelResultSpoolV63Migration = versionedSchemaMigration{
	version: 63,
	name:    "kernel-detached-result-spool-reference",
	statements: []string{
		`CREATE TABLE kernel_execution_result_spool_refs (
			receipt_id TEXT PRIMARY KEY REFERENCES kernel_execution_result_receipts(receipt_id) ON DELETE CASCADE,
			result_ref TEXT NOT NULL CHECK(
				length(result_ref)=84 AND result_ref GLOB 'kernel-spool-sha256:*'
				AND substr(result_ref,21) NOT GLOB '*[^0-9a-f]*'
			),
			created_at TEXT NOT NULL
		) STRICT`,
		`CREATE TRIGGER kernel_execution_result_spool_refs_immutable
			BEFORE UPDATE ON kernel_execution_result_spool_refs
			BEGIN SELECT RAISE(ABORT,'kernel execution result spool reference is immutable'); END`,
	},
	identityV2: &schemaMigrationIdentityV2{
		CallbackID: kernelResultSpoolV63CallbackID, RuleSpec: kernelResultSpoolV63RuleSpec,
		PreflightIdentity: kernelResultSpoolV63PreflightIdentity,
	},
}

func preflightKernelResultSpoolV63(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	var count int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
		WHERE type='table' AND name='kernel_execution_result_receipts'`).Scan(&count); err != nil {
		return errors.New("inspect detached kernel result receipt authority")
	}
	if count != 1 {
		return errors.New("detached kernel result receipt authority is unavailable")
	}
	return nil
}
