package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

type KernelToolResultMaterialization struct {
	OperationID          string
	TerminalResultJSON   json.RawMessage
	TerminalResultSHA256 string
	ResultRef            string
	ExecutionLogSHA256   string
	Source               string
	CreatedAt            time.Time
}

type KernelToolResultBackfillCandidate struct {
	Operation KernelLocalOperation
}

type CommitLegacyKernelToolResultMaterializationInput struct {
	OperationID          string
	ExpectedState        string
	ExpectedStateVersion int64
	TerminalResultJSON   json.RawMessage
	TerminalResultRef    string
}

func normalizeKernelToolResultMaterialization(
	operationID string,
	terminalResultJSON []byte,
	resultRef, executionLogSHA, source string,
	createdAt time.Time,
) (KernelToolResultMaterialization, error) {
	operationID = strings.TrimSpace(operationID)
	resultRef = strings.TrimSpace(resultRef)
	executionLogSHA = strings.TrimSpace(executionLogSHA)
	source = strings.TrimSpace(source)
	canonical, err := canonicalToolCallBatchJSON(terminalResultJSON, false)
	if err != nil || operationID == "" || createdAt.IsZero() ||
		(source != "native_v41" && source != "legacy_checkpoint" && source != "legacy_execution_log") {
		return KernelToolResultMaterialization{}, errors.New("complete kernel tool result materialization is required")
	}
	detectedRef, err := toolCallBatchImmutableResultRef(canonical)
	if err != nil || detectedRef != resultRef {
		return KernelToolResultMaterialization{}, ErrKernelLocalOperationConflict
	}
	if executionLogSHA != "" && !validKernelToolResultSHA256(executionLogSHA) {
		return KernelToolResultMaterialization{}, ErrKernelLocalOperationConflict
	}
	digest := sha256.Sum256(canonical)
	return KernelToolResultMaterialization{
		OperationID: operationID, TerminalResultJSON: append(json.RawMessage(nil), canonical...),
		TerminalResultSHA256: hex.EncodeToString(digest[:]), ResultRef: resultRef,
		ExecutionLogSHA256: executionLogSHA, Source: source, CreatedAt: createdAt.UTC(),
	}, nil
}

func validKernelToolResultSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, current := range value {
		if (current < '0' || current > '9') && (current < 'a' || current > 'f') {
			return false
		}
	}
	return true
}

func sameKernelToolResultMaterialization(left, right KernelToolResultMaterialization) bool {
	return left.OperationID == right.OperationID &&
		bytes.Equal(left.TerminalResultJSON, right.TerminalResultJSON) &&
		left.TerminalResultSHA256 == right.TerminalResultSHA256 && left.ResultRef == right.ResultRef &&
		left.ExecutionLogSHA256 == right.ExecutionLogSHA256 && left.Source == right.Source &&
		left.CreatedAt.Equal(right.CreatedAt)
}

func insertKernelToolResultMaterializationTx(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	materialization KernelToolResultMaterialization,
) error {
	if ctx == nil || tx == nil {
		return errors.New("kernel tool result transaction is required")
	}
	if existing, found, err := getKernelToolResultMaterializationQuery(ctx, tx, materialization.OperationID); err != nil {
		return err
	} else if found {
		if !sameKernelToolResultMaterialization(existing, materialization) {
			return ErrKernelLocalOperationConflict
		}
		return nil
	}
	var operationState, executionLogID string
	if err := tx.QueryRowContext(ctx, `SELECT state,COALESCE(execution_log_id,'')
		FROM kernel_local_operations WHERE operation_id=?`, materialization.OperationID).Scan(
		&operationState, &executionLogID,
	); err != nil {
		return err
	}
	if !kernelLocalOperationTerminal(operationState) ||
		(executionLogID == "" && materialization.ExecutionLogSHA256 != "") ||
		(executionLogID != "" && materialization.ExecutionLogSHA256 == "") {
		return ErrKernelLocalOperationConflict
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO kernel_local_operation_materializations(
		operation_id,terminal_result_json,terminal_result_sha256,result_ref,execution_log_sha256,source,created_at
	) VALUES(?,?,?,?,?,?,?)`, materialization.OperationID, string(materialization.TerminalResultJSON),
		materialization.TerminalResultSHA256, nullableKernelToolResultString(materialization.ResultRef),
		nullableKernelToolResultString(materialization.ExecutionLogSHA256), materialization.Source,
		materialization.CreatedAt.Format(time.RFC3339Nano))
	return err
}

// ensureStagedKernelToolResultMaterializationTx stores the immutable v41 tool
// result while the operation is still started. The execution log is already
// durable at this point; the outbox envelope carries only its identity and
// hashes. The normal terminal insertion path remains strict and compatible.
func ensureStagedKernelToolResultMaterializationTx(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	materialization KernelToolResultMaterialization,
	executionID string,
) error {
	if ctx == nil || tx == nil || strings.TrimSpace(executionID) == "" {
		return errors.New("staged kernel tool result transaction is required")
	}
	if existing, found, err := getKernelToolResultMaterializationQuery(ctx, tx, materialization.OperationID); err != nil {
		return err
	} else if found {
		if !sameKernelToolResultMaterialization(existing, materialization) {
			return ErrKernelLocalOperationConflict
		}
		return nil
	}
	var state, operationExecutionID, executionLogID string
	if err := tx.QueryRowContext(ctx, `SELECT state,COALESCE(execution_id,''),COALESCE(execution_log_id,'')
		FROM kernel_local_operations WHERE operation_id=?`, materialization.OperationID).Scan(
		&state, &operationExecutionID, &executionLogID,
	); err != nil {
		return err
	}
	if state != KernelLocalOperationStateStarted || operationExecutionID != strings.TrimSpace(executionID) ||
		executionLogID != "" || materialization.ExecutionLogSHA256 == "" {
		return ErrKernelLocalOperationConflict
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO kernel_local_operation_materializations(
		operation_id,terminal_result_json,terminal_result_sha256,result_ref,execution_log_sha256,source,created_at
	) VALUES(?,?,?,?,?,?,?)`, materialization.OperationID, string(materialization.TerminalResultJSON),
		materialization.TerminalResultSHA256, nullableKernelToolResultString(materialization.ResultRef),
		nullableKernelToolResultString(materialization.ExecutionLogSHA256), materialization.Source,
		materialization.CreatedAt.Format(time.RFC3339Nano))
	return err
}

func nullableKernelToolResultString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func getKernelToolResultMaterializationQuery(
	ctx context.Context,
	queryer interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	},
	operationID string,
) (KernelToolResultMaterialization, bool, error) {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return KernelToolResultMaterialization{}, false, nil
	}
	var result KernelToolResultMaterialization
	var resultJSON []byte
	var resultRef, executionLogSHA sql.NullString
	var createdAt string
	err := queryer.QueryRowContext(ctx, `SELECT operation_id,terminal_result_json,terminal_result_sha256,
		result_ref,execution_log_sha256,source,created_at
		FROM kernel_local_operation_materializations WHERE operation_id=?`, operationID).Scan(
		&result.OperationID, &resultJSON, &result.TerminalResultSHA256,
		&resultRef, &executionLogSHA, &result.Source, &createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return KernelToolResultMaterialization{}, false, nil
	}
	if err != nil {
		return KernelToolResultMaterialization{}, false, err
	}
	result.ResultRef, result.ExecutionLogSHA256 = resultRef.String, executionLogSHA.String
	parsedAt, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return KernelToolResultMaterialization{}, false, ErrKernelLocalOperationConflict
	}
	validated, err := normalizeKernelToolResultMaterialization(
		result.OperationID, resultJSON, result.ResultRef, result.ExecutionLogSHA256, result.Source, parsedAt,
	)
	if err != nil || result.TerminalResultSHA256 != validated.TerminalResultSHA256 ||
		!bytes.Equal(resultJSON, validated.TerminalResultJSON) {
		return KernelToolResultMaterialization{}, false, ErrKernelLocalOperationConflict
	}
	return validated, true, nil
}

func (s *Store) GetKernelToolResultMaterialization(
	ctx context.Context,
	operationID string,
) (KernelToolResultMaterialization, bool, error) {
	if s == nil || s.db == nil || ctx == nil {
		return KernelToolResultMaterialization{}, false, errors.New("workspace store and context are required")
	}
	return getKernelToolResultMaterializationQuery(ctx, s.db, operationID)
}

// ListKernelToolResultBackfillCandidates returns only terminal v39 operations
// that have not yet delivered a protocol receipt. Receipt-backed rows are
// promoted atomically by migration v41; finding one without a materialization
// therefore signals corruption rather than a second runtime read path.
func (s *Store) ListKernelToolResultBackfillCandidates(
	ctx context.Context,
	limit int,
) ([]KernelToolResultBackfillCandidate, bool, error) {
	if s == nil || s.db == nil || ctx == nil {
		return nil, false, errors.New("workspace store and context are required")
	}
	if limit <= 0 || limit > 1000 {
		return nil, false, errors.New("kernel tool result backfill limit is invalid")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT operation.operation_id
		FROM kernel_local_operations operation
		LEFT JOIN kernel_local_operation_materializations materialization
			ON materialization.operation_id=operation.operation_id
		LEFT JOIN kernel_local_operation_protocol_receipts receipt
			ON receipt.operation_id=operation.operation_id
		WHERE operation.state IN ('completed','failed','cancelled','outcome_unknown')
			AND materialization.operation_id IS NULL
		ORDER BY operation.updated_at,operation.operation_id LIMIT ?`, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	operationIDs := []string{}
	for rows.Next() {
		var operationID string
		if err := rows.Scan(&operationID); err != nil {
			return nil, false, err
		}
		operationIDs = append(operationIDs, operationID)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(operationIDs) > limit
	if more {
		operationIDs = operationIDs[:limit]
	}
	result := make([]KernelToolResultBackfillCandidate, 0, len(operationIDs))
	for _, operationID := range operationIDs {
		operation, found, err := getKernelLocalOperationQuery(ctx, s.db, operationID)
		if err != nil {
			return nil, false, err
		}
		if !found {
			return nil, false, ErrKernelLocalOperationConflict
		}
		var receiptCount int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM kernel_local_operation_protocol_receipts
			WHERE operation_id=?`, operationID).Scan(&receiptCount); err != nil {
			return nil, false, err
		}
		if receiptCount != 0 {
			return nil, false, errors.New("receipt-backed kernel result is missing its v41 materialization")
		}
		if operation.State == KernelLocalOperationStateOutcomeUnknown {
			if len(operation.ResultJSON) == 0 || operation.ExecutionLogID != "" {
				return nil, false, ErrKernelLocalOperationConflict
			}
		} else if operation.ExecutionLogID == "" || operation.ResultSHA256 == "" ||
			operation.ExecutionLogRef != "execution-log:"+operation.ExecutionLogID {
			return nil, false, ErrKernelLocalOperationConflict
		}
		result = append(result, KernelToolResultBackfillCandidate{Operation: operation})
	}
	return result, more, nil
}

// CommitLegacyKernelToolResultMaterialization installs one reconstructed v39
// terminal result under the v41 authority. The operation head is revalidated
// in the same immediate transaction; no operation or protocol state is
// rewritten and an already committed value must match byte-for-byte.
func (s *Store) CommitLegacyKernelToolResultMaterialization(
	ctx context.Context,
	input CommitLegacyKernelToolResultMaterializationInput,
) (KernelToolResultMaterialization, error) {
	if s == nil || s.db == nil || ctx == nil {
		return KernelToolResultMaterialization{}, errors.New("workspace store and context are required")
	}
	input.OperationID = strings.TrimSpace(input.OperationID)
	input.ExpectedState = strings.TrimSpace(input.ExpectedState)
	if input.OperationID == "" || !kernelLocalOperationTerminal(input.ExpectedState) || input.ExpectedStateVersion <= 0 {
		return KernelToolResultMaterialization{}, errors.New("complete legacy kernel result identity is required")
	}
	repository, err := s.TranscriptRepository(ctx)
	if err != nil {
		return KernelToolResultMaterialization{}, err
	}
	var committed KernelToolResultMaterialization
	err = repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		operation, found, err := getKernelLocalOperationQuery(ctx, tx, input.OperationID)
		if err != nil {
			return err
		}
		if !found || operation.State != input.ExpectedState || operation.StateVersion != input.ExpectedStateVersion {
			return ErrKernelLocalOperationStale
		}
		var receiptCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM kernel_local_operation_protocol_receipts
			WHERE operation_id=?`, operation.OperationID).Scan(&receiptCount); err != nil {
			return err
		}
		if receiptCount != 0 {
			return ErrKernelLocalOperationConflict
		}
		executionLogSHA := operation.ResultSHA256
		if operation.State == KernelLocalOperationStateOutcomeUnknown {
			executionLogSHA = ""
		} else if operation.ExecutionLogID == "" || operation.ExecutionLogRef != "execution-log:"+operation.ExecutionLogID {
			return ErrKernelLocalOperationConflict
		}
		materialization, err := normalizeKernelToolResultMaterialization(
			operation.OperationID, input.TerminalResultJSON, input.TerminalResultRef,
			executionLogSHA, "legacy_execution_log", operation.UpdatedAt,
		)
		if err != nil {
			return err
		}
		existing, found, err := getKernelToolResultMaterializationQuery(ctx, tx, operation.OperationID)
		if err != nil {
			return err
		}
		if found {
			if !sameKernelToolResultMaterialization(existing, materialization) {
				return ErrKernelLocalOperationConflict
			}
			committed = existing
			return nil
		}
		if err := insertKernelToolResultMaterializationTx(ctx, tx, materialization); err != nil {
			return err
		}
		committed = materialization
		return nil
	})
	return committed, err
}
