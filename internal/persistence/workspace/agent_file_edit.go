package workspace

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

const agentFileEditMutationOperation = "agent_workspace_edit_v1"

type AgentFileEditReceipt struct {
	State          string `json:"state"`
	ExecutionID    string `json:"execution_id"`
	FrameID        string `json:"frame_id"`
	DisplayPath    string `json:"display_path"`
	OriginalSHA256 string `json:"original_sha256"`
	FinalSHA256    string `json:"final_sha256"`
	Created        bool   `json:"created"`
	BytesWritten   int64  `json:"bytes_written"`
}

type AgentFileEditReceiptInput struct {
	OwnerID           string
	IdempotencyKey    string
	RequestSHA256     string
	Receipt           AgentFileEditReceipt
	ExecutionLogInput SaveExecutionLogInput
}

func (s *Store) GetAgentFileEditReceipt(
	ctx context.Context,
	ownerID string,
	idempotencyKey string,
	requestSHA256 string,
) (AgentFileEditReceipt, bool, error) {
	if s == nil || s.db == nil {
		return AgentFileEditReceipt{}, false, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var receipt AgentFileEditReceipt
	found, err := s.lookupMutationResult(ctx, ownerID, idempotencyKey, agentFileEditMutationOperation, requestSHA256, &receipt)
	if err != nil {
		return AgentFileEditReceipt{}, false, err
	}
	if found {
		if err := validateAgentFileEditReceipt(receipt); err != nil {
			return AgentFileEditReceipt{}, false, err
		}
	}
	return receipt, found, nil
}

func (s *Store) PrepareAgentFileEditReceipt(
	ctx context.Context,
	input AgentFileEditReceiptInput,
) (AgentFileEditReceipt, bool, error) {
	if s == nil || s.db == nil {
		return AgentFileEditReceipt{}, false, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateAgentFileEditReceiptInput(input); err != nil {
		return AgentFileEditReceipt{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentFileEditReceipt{}, false, err
	}
	defer tx.Rollback()
	var existing AgentFileEditReceipt
	found, err := lookupMutationResultTx(
		ctx, tx, input.OwnerID, input.IdempotencyKey, agentFileEditMutationOperation, input.RequestSHA256, &existing,
	)
	if err != nil {
		return AgentFileEditReceipt{}, false, err
	}
	if found {
		if err := validateAgentFileEditReceipt(existing); err != nil {
			return AgentFileEditReceipt{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return AgentFileEditReceipt{}, false, err
		}
		return existing, false, nil
	}
	receipt := input.Receipt
	receipt.State = "prepared"
	if err := insertMutationResultTx(
		ctx, tx, input.OwnerID, input.IdempotencyKey, agentFileEditMutationOperation, input.RequestSHA256, receipt, s.now().UTC(),
	); err != nil {
		return AgentFileEditReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return AgentFileEditReceipt{}, false, err
	}
	return receipt, true, nil
}

func (s *Store) CompleteAgentFileEditReceipt(
	ctx context.Context,
	input AgentFileEditReceiptInput,
) (ExecutionLogRecord, AgentFileEditReceipt, error) {
	if s == nil || s.db == nil {
		return ExecutionLogRecord{}, AgentFileEditReceipt{}, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateAgentFileEditReceiptInput(input); err != nil {
		return ExecutionLogRecord{}, AgentFileEditReceipt{}, err
	}
	prepared, err := s.prepareExecutionLog(input.ExecutionLogInput)
	if err != nil {
		return ExecutionLogRecord{}, AgentFileEditReceipt{}, err
	}
	if prepared.record.ID != input.Receipt.ExecutionID || prepared.record.FrameID != input.Receipt.FrameID {
		return ExecutionLogRecord{}, AgentFileEditReceipt{}, errors.New("agent file edit execution identity is inconsistent")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ExecutionLogRecord{}, AgentFileEditReceipt{}, err
	}
	defer tx.Rollback()
	var receipt AgentFileEditReceipt
	found, err := lookupMutationResultTx(
		ctx, tx, input.OwnerID, input.IdempotencyKey, agentFileEditMutationOperation, input.RequestSHA256, &receipt,
	)
	if err != nil {
		return ExecutionLogRecord{}, AgentFileEditReceipt{}, err
	}
	if !found || validateAgentFileEditReceipt(receipt) != nil {
		return ExecutionLogRecord{}, AgentFileEditReceipt{}, errors.New("agent file edit receipt is unavailable")
	}
	if !sameAgentFileEditReceipt(receipt, input.Receipt) {
		return ExecutionLogRecord{}, AgentFileEditReceipt{}, errors.New("agent file edit receipt conflicts with completion")
	}
	if receipt.State == "completed" {
		record, err := scanExecutionLog(tx.QueryRowContext(ctx, executionLogSelect+`
			WHERE log.frame_id = ? AND log.id = ?`, receipt.FrameID, receipt.ExecutionID))
		if err != nil {
			return ExecutionLogRecord{}, AgentFileEditReceipt{}, err
		}
		if err := tx.Commit(); err != nil {
			return ExecutionLogRecord{}, AgentFileEditReceipt{}, err
		}
		return record, receipt, nil
	}
	record, err := saveExecutionLogTx(ctx, tx, prepared)
	if err != nil {
		return ExecutionLogRecord{}, AgentFileEditReceipt{}, err
	}
	receipt.State = "completed"
	raw, err := json.Marshal(receipt)
	if err != nil {
		return ExecutionLogRecord{}, AgentFileEditReceipt{}, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE mutation_result_ledger SET result_json=?
		WHERE owner_user_id=? AND idempotency_key=? AND operation=? AND request_hash=?`,
		string(raw), input.OwnerID, input.IdempotencyKey, agentFileEditMutationOperation, input.RequestSHA256,
	)
	if err != nil {
		return ExecutionLogRecord{}, AgentFileEditReceipt{}, err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return ExecutionLogRecord{}, AgentFileEditReceipt{}, errors.New("agent file edit receipt completion was not persisted")
	}
	if err := tx.Commit(); err != nil {
		return ExecutionLogRecord{}, AgentFileEditReceipt{}, err
	}
	return record, receipt, nil
}

func validateAgentFileEditReceiptInput(input AgentFileEditReceiptInput) error {
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.RequestSHA256 = strings.ToLower(strings.TrimSpace(input.RequestSHA256))
	if input.OwnerID == "" || input.IdempotencyKey == "" || !validAgentFileEditDigest(input.RequestSHA256) {
		return errors.New("agent file edit receipt owner, identity, and request digest are required")
	}
	return validateAgentFileEditReceipt(input.Receipt)
}

func validateAgentFileEditReceipt(receipt AgentFileEditReceipt) error {
	if receipt.State != "" && receipt.State != "prepared" && receipt.State != "completed" {
		return errors.New("agent file edit receipt state is invalid")
	}
	if strings.TrimSpace(receipt.ExecutionID) == "" || strings.TrimSpace(receipt.FrameID) == "" ||
		strings.TrimSpace(receipt.DisplayPath) == "" || receipt.BytesWritten < 0 ||
		!validAgentFileEditDigest(receipt.FinalSHA256) ||
		(receipt.OriginalSHA256 != "absent" && !validAgentFileEditDigest(receipt.OriginalSHA256)) {
		return errors.New("agent file edit receipt is invalid")
	}
	return nil
}

func validAgentFileEditDigest(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func sameAgentFileEditReceipt(left, right AgentFileEditReceipt) bool {
	return left.ExecutionID == right.ExecutionID && left.FrameID == right.FrameID &&
		left.DisplayPath == right.DisplayPath && left.OriginalSHA256 == right.OriginalSHA256 &&
		left.FinalSHA256 == right.FinalSHA256 && left.Created == right.Created && left.BytesWritten == right.BytesWritten
}
