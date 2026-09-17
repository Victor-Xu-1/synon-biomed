package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	kernelToolResultV41CallbackID        = "kernel-tool-result-v41-noop"
	kernelToolResultV41PreflightIdentity = "kernel-tool-result-materialization-v1"
	kernelToolResultV41RuleSpec          = "synon.workspace.kernel-tool-result.v41"
	kernelToolResultMaterializationsDDL  = `CREATE TABLE kernel_local_operation_materializations (
		operation_id TEXT PRIMARY KEY REFERENCES kernel_local_operations(operation_id) ON DELETE RESTRICT,
		terminal_result_json TEXT NOT NULL CHECK(
			json_valid(terminal_result_json) AND length(CAST(terminal_result_json AS BLOB))<=1048576
		),
		terminal_result_sha256 TEXT NOT NULL CHECK(
			length(terminal_result_sha256)=64 AND terminal_result_sha256 NOT GLOB '*[^0-9a-f]*'
		),
		result_ref TEXT CHECK(result_ref IS NULL OR (
			length(CAST(result_ref AS BLOB))<=4096 AND result_ref GLOB 'artifact-version:*'
		)),
		execution_log_sha256 TEXT CHECK(execution_log_sha256 IS NULL OR (
			length(execution_log_sha256)=64 AND execution_log_sha256 NOT GLOB '*[^0-9a-f]*'
		)),
		source TEXT NOT NULL CHECK(source IN ('native_v41','legacy_checkpoint','legacy_execution_log')),
		created_at TEXT NOT NULL,
		CHECK(result_ref IS NULL OR (
			json_type(terminal_result_json,'$.artifact_id')='text' AND
			json_type(terminal_result_json,'$.version_id')='text' AND
			result_ref='artifact-version:'||json_extract(terminal_result_json,'$.version_id')
		))
	) STRICT`
)

var kernelToolResultV41Migration = versionedSchemaMigration{
	version: 41,
	name:    "kernel-tool-result-materialization-authority",
	statements: []string{
		`DROP TRIGGER kernel_local_operation_protocol_receipts_immutable`,
		`DROP TRIGGER kernel_local_operation_protocol_receipts_append_only`,
		kernelToolResultMaterializationsDDL,
		`ALTER TABLE kernel_local_operation_protocol_receipts ADD COLUMN result_ref TEXT
			CHECK(result_ref IS NULL OR (length(CAST(result_ref AS BLOB))<=4096 AND result_ref GLOB 'artifact-version:*'))`,
		`CREATE INDEX kernel_local_operation_materializations_ref_idx
			ON kernel_local_operation_materializations(result_ref,operation_id) WHERE result_ref IS NOT NULL`,
		`CREATE TRIGGER kernel_local_operation_materialization_immutable
			BEFORE UPDATE ON kernel_local_operation_materializations
			BEGIN SELECT RAISE(ABORT,'kernel tool result materialization is immutable'); END`,
		`CREATE TRIGGER kernel_local_operation_materialization_delete_forbidden
			BEFORE DELETE ON kernel_local_operation_materializations
			BEGIN SELECT RAISE(ABORT,'kernel tool result materialization is append-only'); END`,
	},
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        kernelToolResultV41CallbackID,
		RuleSpec:          kernelToolResultV41RuleSpec,
		PreflightIdentity: kernelToolResultV41PreflightIdentity,
	},
}

// backfillKernelToolResultV41 promotes the exact tool result that was already
// delivered by a legacy protocol receipt. The receipt is the historical
// model-visible fact and therefore takes precedence over reconstruction from
// an execution log. Terminal operations without a receipt remain explicitly
// unsettled and are reconciled by the resumable server coordinator before they
// can be replayed.
func backfillKernelToolResultV41(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT operation.operation_id,operation.state,
		COALESCE(operation.result_sha256,''),receipt.stream_uid,receipt.event_id,
		receipt.result_sha256,event.payload_json,event.created_at
		FROM kernel_local_operation_protocol_receipts receipt
		JOIN kernel_local_operations operation ON operation.operation_id=receipt.operation_id
		JOIN transcript_events event ON event.stream_uid=receipt.stream_uid AND event.event_id=receipt.event_id
		WHERE operation.state IN ('completed','failed','cancelled','outcome_unknown')
		ORDER BY operation.operation_id`)
	if err != nil {
		return fmt.Errorf("read legacy kernel protocol receipts: %w", err)
	}
	type legacyReceipt struct {
		operationID, state, executionLogSHA, streamUID, receiptSHA, payloadJSON, createdAt string
		eventID                                                                            int64
	}
	receipts := []legacyReceipt{}
	for rows.Next() {
		var current legacyReceipt
		if err := rows.Scan(&current.operationID, &current.state, &current.executionLogSHA,
			&current.streamUID, &current.eventID, &current.receiptSHA, &current.payloadJSON,
			&current.createdAt); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan legacy kernel protocol receipt: %w", err)
		}
		receipts = append(receipts, current)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close legacy kernel protocol receipts: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate legacy kernel protocol receipts: %w", err)
	}
	for _, receipt := range receipts {
		var payload struct {
			ToolResult json.RawMessage `json:"toolResult"`
		}
		decoder := json.NewDecoder(strings.NewReader(receipt.payloadJSON))
		if decoder.Decode(&payload) != nil || decoder.Decode(&struct{}{}) == nil || len(payload.ToolResult) == 0 {
			return errors.New("legacy kernel protocol receipt payload is invalid")
		}
		canonical, err := canonicalKernelLocalOperationProtocolJSON(payload.ToolResult)
		if err != nil {
			return errors.New("legacy kernel protocol receipt result is invalid")
		}
		digest := sha256.Sum256(canonical)
		if receipt.receiptSHA != hex.EncodeToString(digest[:]) {
			return errors.New("legacy kernel protocol receipt result digest conflicts with checkpoint")
		}
		resultRef, err := toolCallBatchImmutableResultRef(canonical)
		if err != nil {
			return errors.New("legacy kernel protocol receipt result reference is invalid")
		}
		createdAt, err := time.Parse(time.RFC3339Nano, receipt.createdAt)
		if err != nil {
			return errors.New("legacy kernel protocol receipt timestamp is invalid")
		}
		executionLogSHA := receipt.executionLogSHA
		if receipt.state == KernelLocalOperationStateOutcomeUnknown {
			executionLogSHA = ""
		}
		materialization, err := normalizeKernelToolResultMaterialization(
			receipt.operationID, canonical, resultRef, executionLogSHA, "legacy_checkpoint", createdAt,
		)
		if err != nil || !bytes.Equal(materialization.TerminalResultJSON, canonical) {
			return errors.New("legacy kernel protocol receipt materialization is invalid")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO kernel_local_operation_materializations(
			operation_id,terminal_result_json,terminal_result_sha256,result_ref,execution_log_sha256,source,created_at
		) VALUES(?,?,?,?,?,?,?)`, materialization.OperationID, string(materialization.TerminalResultJSON),
			materialization.TerminalResultSHA256, nullableKernelToolResultString(materialization.ResultRef),
			nullableKernelToolResultString(materialization.ExecutionLogSHA256), materialization.Source,
			materialization.CreatedAt.Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("backfill legacy kernel tool result materialization: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE kernel_local_operation_protocol_receipts
			SET result_ref=? WHERE operation_id=?`, nullableKernelToolResultString(resultRef), receipt.operationID); err != nil {
			return fmt.Errorf("backfill legacy kernel protocol receipt reference: %w", err)
		}
	}
	for _, statement := range []string{
		`CREATE TRIGGER kernel_local_operation_protocol_receipts_immutable BEFORE UPDATE ON kernel_local_operation_protocol_receipts
		BEGIN SELECT RAISE(ABORT,'kernel local operation protocol receipt is immutable'); END`,
		`CREATE TRIGGER kernel_local_operation_protocol_receipts_append_only BEFORE DELETE ON kernel_local_operation_protocol_receipts
		BEGIN SELECT RAISE(ABORT,'kernel local operation protocol receipt is append-only'); END`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("restore kernel protocol receipt immutability: %w", err)
		}
	}
	return nil
}

func preflightKernelToolResultV41(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	var dependencies int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
		WHERE type='table' AND name IN (
			'kernel_local_operations','kernel_local_operation_protocol_receipts',
			'transcript_tool_call_batches','transcript_tool_call_items'
		)`).Scan(&dependencies); err != nil {
		return errors.New("inspect kernel tool result dependencies")
	}
	if dependencies != 4 {
		return errors.New("kernel tool result dependencies are required")
	}
	var polluted int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
		WHERE name='kernel_local_operation_materializations'`).Scan(&polluted); err != nil {
		return errors.New("inspect kernel tool result cohort")
	}
	if polluted != 0 {
		return errors.New("kernel tool result cohort is polluted")
	}
	rows, err := executor.QueryContext(ctx, `PRAGMA table_info(kernel_local_operation_protocol_receipts)`)
	if err != nil {
		return errors.New("inspect kernel protocol receipt columns")
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return errors.New("inspect kernel protocol receipt columns")
		}
		if name == "result_ref" {
			return errors.New("kernel tool result cohort is polluted")
		}
	}
	return rows.Err()
}
