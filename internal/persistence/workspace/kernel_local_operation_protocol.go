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

	transcriptstore "synon-go/internal/persistence/transcript"
)

// CommitKernelLocalOperationProtocolReceiptTx binds one terminal native tool
// result checkpoint to its immutable operation in the same transaction that
// appends the checkpoint. A committed receipt is the only settlement fence;
// terminal operation state alone remains replayable after a crash.
func (s *Store) CommitKernelLocalOperationProtocolReceiptTx(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	operationID string,
	event transcriptstore.Event,
) error {
	operationID = strings.TrimSpace(operationID)
	if s == nil || tx == nil || operationID == "" || event.EventID <= 0 || event.RunnerAttempt == nil {
		return errors.New("operation and terminal runner checkpoint are required")
	}
	operation, found, err := getKernelLocalOperationQuery(ctx, tx, operationID)
	if err != nil {
		return err
	}
	backgroundStarted, err := kernelLocalOperationProtocolBackgroundStarted(ctx, tx, operation)
	if err != nil {
		return err
	}
	if !found || (!kernelLocalOperationTerminal(operation.State) && !backgroundStarted) ||
		operation.StreamUID != event.StreamUID {
		return fmt.Errorf("kernel protocol operation state mismatch: %w", ErrKernelLocalOperationConflict)
	}
	if err := validateKernelLocalOperationCurrentAuthority(ctx, tx, operation, true); err != nil {
		return err
	}
	var eventType string
	var runnerAttempt int64
	var payloadJSON []byte
	var createdAt string
	if err := tx.QueryRowContext(ctx, `SELECT event_type,runner_attempt,payload_json,created_at
		FROM transcript_events WHERE stream_uid=? AND event_id=?`, event.StreamUID, event.EventID).Scan(
		&eventType, &runnerAttempt, &payloadJSON, &createdAt,
	); err != nil {
		return err
	}
	if eventType != "runner_checkpoint" || runnerAttempt != *event.RunnerAttempt || !bytes.Equal(payloadJSON, event.PayloadJSON) {
		return fmt.Errorf("kernel protocol checkpoint identity mismatch: %w", ErrKernelLocalOperationConflict)
	}
	var payload struct {
		Status     string          `json:"status"`
		ToolCallID string          `json:"toolCallId"`
		ToolPhase  string          `json:"toolPhase"`
		ToolResult json.RawMessage `json:"toolResult"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payloadJSON))
	if err := decoder.Decode(&payload); err != nil || decoder.Decode(&struct{}{}) == nil ||
		strings.TrimSpace(payload.ToolCallID) != operation.ToolCallID ||
		(payload.ToolPhase != "completed" && payload.ToolPhase != "failed") || len(payload.ToolResult) == 0 {
		return fmt.Errorf("kernel protocol checkpoint payload mismatch: %w", ErrKernelLocalOperationConflict)
	}
	canonicalResult, err := canonicalKernelLocalOperationProtocolJSON(payload.ToolResult)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(canonicalResult)
	resultSHA := hex.EncodeToString(digest[:])
	resultRef := ""
	if backgroundStarted {
		var projected map[string]any
		if err := json.Unmarshal(canonicalResult, &projected); err != nil ||
			strings.TrimSpace(kernelLocalOperationStringValue(projected["exec_id"])) != operation.ExecutionID ||
			strings.TrimSpace(kernelLocalOperationStringValue(projected["status"])) != "running" {
			return fmt.Errorf("kernel background protocol result mismatch: %w", ErrKernelLocalOperationConflict)
		}
	} else {
		materialization, found, err := getKernelToolResultMaterializationQuery(ctx, tx, operation.OperationID)
		if err != nil || !found || !bytes.Equal(canonicalResult, materialization.TerminalResultJSON) ||
			resultSHA != materialization.TerminalResultSHA256 {
			return fmt.Errorf("kernel terminal materialization mismatch: %w", ErrKernelLocalOperationConflict)
		}
		resultRef = materialization.ResultRef
	}
	var existingStream, existingToolCall, existingSHA, existingCreated string
	var existingResultRef sql.NullString
	var existingAttempt, existingEvent int64
	err = tx.QueryRowContext(ctx, `SELECT stream_uid,runner_attempt,event_id,tool_call_id,result_sha256,result_ref,created_at
		FROM kernel_local_operation_protocol_receipts WHERE operation_id=?`, operationID).Scan(
		&existingStream, &existingAttempt, &existingEvent, &existingToolCall, &existingSHA, &existingResultRef, &existingCreated,
	)
	if err == nil {
		if existingStream != event.StreamUID || existingAttempt != runnerAttempt || existingEvent != event.EventID ||
			existingToolCall != operation.ToolCallID || existingSHA != resultSHA || existingResultRef.String != resultRef ||
			existingCreated != createdAt {
			return ErrKernelLocalOperationConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO kernel_local_operation_protocol_receipts(
		operation_id,stream_uid,runner_attempt,event_id,tool_call_id,result_sha256,result_ref,created_at
	) VALUES(?,?,?,?,?,?,?,?)`, operationID, event.StreamUID, runnerAttempt, event.EventID,
		operation.ToolCallID, resultSHA, nullableKernelToolResultString(resultRef), createdAt)
	return err
}

func kernelLocalOperationProtocolBackgroundStarted(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	operation KernelLocalOperation,
) (bool, error) {
	if operation.State != KernelLocalOperationStateStarted {
		return false, nil
	}
	if kernelLocalOperationIsBackground(operation.InputJSON) {
		return true, nil
	}
	// A foreground call is converted to the same detached protocol when its
	// bounded wait expires. The immutable detached execution row—not a model
	// argument—is the authority proving that this started operation continues
	// under the executor/observer lifecycle.
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM kernel_detached_executions
		WHERE operation_id=? AND execution_id=? AND state IN
			('accepted','dispatch_committed','started','cancel_requested','terminal')`,
		operation.OperationID, operation.ExecutionID).Scan(&count); err != nil {
		return false, err
	}
	return count == 1, nil
}

func kernelLocalOperationIsBackground(raw []byte) bool {
	var input map[string]any
	if err := json.Unmarshal(raw, &input); err != nil {
		return false
	}
	background, _ := input["background"].(bool)
	return background
}

func kernelLocalOperationStringValue(value any) string {
	text, _ := value.(string)
	return text
}

func canonicalKernelLocalOperationProtocolJSON(raw []byte) ([]byte, error) {
	if len(raw) == 0 || len(raw) > 1<<20 || validateKernelLocalOperationJSON(raw) != nil {
		return nil, ErrKernelLocalOperationConflict
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil || decoder.Decode(&struct{}{}) == nil {
		return nil, ErrKernelLocalOperationConflict
	}
	canonical, err := json.Marshal(value)
	if err != nil || len(canonical) > 1<<20 {
		return nil, ErrKernelLocalOperationConflict
	}
	return canonical, nil
}
