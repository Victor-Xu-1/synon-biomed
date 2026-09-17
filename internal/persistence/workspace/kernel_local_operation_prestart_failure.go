package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

type FailApprovedKernelLocalOperationInput struct {
	OwnerUserID          string
	OperationID          string
	ExpectedStateVersion int64
	Claim                transcriptstore.RunnerClaim
	ReasonCode           string
	TerminalResultJSON   json.RawMessage
}

// FailApprovedKernelLocalOperation records a deterministic failure that
// happened before the approved operation reached the prepared boundary. No
// kernel side effect can have started in this state, so the exact failure is
// safe to materialize and return to the model instead of entering uncertain
// execution recovery.
func (s *Store) FailApprovedKernelLocalOperation(
	ctx context.Context,
	input FailApprovedKernelLocalOperationInput,
) (KernelLocalOperation, error) {
	input.OwnerUserID = strings.TrimSpace(input.OwnerUserID)
	input.OperationID = strings.TrimSpace(input.OperationID)
	reason, err := normalizeKernelLocalOperationReason(input.ReasonCode)
	if err != nil {
		return KernelLocalOperation{}, err
	}
	input.ReasonCode = reason
	if s == nil || ctx == nil || input.OwnerUserID == "" || input.OperationID == "" ||
		input.ExpectedStateVersion <= 0 || input.ReasonCode == "" ||
		strings.TrimSpace(input.Claim.RunnerID) == "" || input.Claim.Attempt <= 0 ||
		strings.TrimSpace(input.Claim.ClaimToken) == "" {
		return KernelLocalOperation{}, errors.New("complete approved kernel failure authority is required")
	}
	materialization, err := normalizeKernelToolResultMaterialization(
		input.OperationID, input.TerminalResultJSON, "", "", "native_v41", s.now().UTC(),
	)
	if err != nil {
		return KernelLocalOperation{}, err
	}
	digest := sha256.Sum256(materialization.TerminalResultJSON)
	resultSHA := hex.EncodeToString(digest[:])
	now := s.now().UTC().Format(time.RFC3339Nano)
	operation, err := s.transitionKernelLocalOperation(ctx, kernelLocalOperationTransitionInput{
		ownerUserID: input.OwnerUserID, operationID: input.OperationID,
		expectedState: KernelLocalOperationStateApproved, expectedVersion: input.ExpectedStateVersion,
		targetState: KernelLocalOperationStateCancelled, reasonCode: input.ReasonCode,
		setClause: `terminal_at=?,result_json=?,result_sha256=?`,
		setArgs:   []any{now, string(materialization.TerminalResultJSON), resultSHA},
		validate: func(tx *transcriptstore.ImmediateTransaction, operation KernelLocalOperation) error {
			if operation.ExecutionID != "" || operation.PreparedAt != nil || operation.StartedAt != nil ||
				operation.AdmittedInputRevision <= 0 || operation.AdmittedInputRevision > input.Claim.ClaimedInputRevision {
				return ErrKernelLocalOperationStale
			}
			return validateKernelLocalOperationLiveClaim(ctx, tx, operation, input.Claim)
		},
		idempotent: func(operation KernelLocalOperation) bool {
			return operation.ReasonCode == input.ReasonCode && operation.ExecutionID == "" &&
				bytes.Equal(operation.ResultJSON, materialization.TerminalResultJSON) && operation.ResultSHA256 == resultSHA
		},
		after: func(tx *transcriptstore.ImmediateTransaction, operation KernelLocalOperation) error {
			terminalMaterialization := materialization
			terminalMaterialization.CreatedAt = operation.UpdatedAt
			if err := insertKernelToolResultMaterializationTx(ctx, tx, terminalMaterialization); err != nil {
				return err
			}
			_, err := appendKernelLocalOperationTerminalEvent(ctx, s, tx, operation, operation.UpdatedAt)
			return err
		},
	})
	if err == nil {
		s.signalKernelRetentionWake()
	}
	return operation, err
}
