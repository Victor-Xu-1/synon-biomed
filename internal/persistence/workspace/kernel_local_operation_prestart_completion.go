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

type CompleteKernelLocalOperationPreflightInput struct {
	OwnerUserID          string
	OperationID          string
	ExpectedStateVersion int64
	ExpectedState        string
	Claim                transcriptstore.RunnerClaim
	ReasonCode           string
	TerminalResultJSON   json.RawMessage
}

// CompleteKernelLocalOperationPreflight commits a deterministic control result
// produced before a kernel process starts. A preflight can finish before a
// permission prompt is needed or after an already-approved operation resumes;
// both states are safe because the transition rejects any prepared, started, or
// externally executed operation.
func (s *Store) CompleteKernelLocalOperationPreflight(
	ctx context.Context,
	input CompleteKernelLocalOperationPreflightInput,
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
		(input.ExpectedState != KernelLocalOperationStatePendingApproval &&
			input.ExpectedState != KernelLocalOperationStateApproved) ||
		strings.TrimSpace(input.Claim.RunnerID) == "" || input.Claim.Attempt <= 0 ||
		strings.TrimSpace(input.Claim.ClaimToken) == "" {
		return KernelLocalOperation{}, errors.New("complete kernel preflight authority is required")
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
	preflightDecisionID := ""
	if input.ExpectedState == KernelLocalOperationStatePendingApproval {
		decisionDigest := sha256.Sum256([]byte(input.OperationID + "\x00" + input.ReasonCode))
		preflightDecisionID = "kernel-preflight-" + hex.EncodeToString(decisionDigest[:])[:24]
	}
	operation, err := s.transitionKernelLocalOperation(ctx, kernelLocalOperationTransitionInput{
		ownerUserID: input.OwnerUserID, operationID: input.OperationID,
		expectedState: input.ExpectedState, expectedVersion: input.ExpectedStateVersion,
		// The v39 protocol deliberately permits approved operations to terminate
		// as cancelled when no kernel side effect began. The tool result itself is
		// successful; cancelled here records that execution was intentionally
		// short-circuited by the control preflight.
		targetState: KernelLocalOperationStateCancelled, reasonCode: input.ReasonCode,
		setClause: `terminal_at=?,result_json=?,result_sha256=?`,
		setArgs:   []any{now, string(materialization.TerminalResultJSON), resultSHA},
		validate: func(tx *transcriptstore.ImmediateTransaction, operation KernelLocalOperation) error {
			if operation.ExecutionID != "" || operation.PreparedAt != nil || operation.StartedAt != nil {
				return ErrKernelLocalOperationStale
			}
			if input.ExpectedState == KernelLocalOperationStateApproved &&
				(operation.AdmittedInputRevision <= 0 || operation.AdmittedInputRevision > input.Claim.ClaimedInputRevision) {
				return ErrKernelLocalOperationStale
			}
			return validateKernelLocalOperationLiveClaim(ctx, tx, operation, input.Claim)
		},
		before: func(tx *transcriptstore.ImmediateTransaction, operation KernelLocalOperation) (string, []any, error) {
			if input.ExpectedState != KernelLocalOperationStatePendingApproval {
				return "", nil, nil
			}
			if err := removeKernelLocalOperationPendingProjection(ctx, tx, operation); err != nil {
				return "", nil, err
			}
			// This is an explicit runtime denial of execution, never an implicit
			// user approval. Crash recovery derives the immutable input revision
			// from SourceRunnerAttempt because no approval response was appended.
			return `approval_decision_id=?,approval_decision=?,approval_scope=?,
				approval_source=?,approval_actor_id=?,decided_at=?`,
				[]any{preflightDecisionID, "deny", "once", "policy", "system", now}, nil
		},
		idempotent: func(operation KernelLocalOperation) bool {
			if operation.ReasonCode != input.ReasonCode || operation.ExecutionID != "" ||
				!bytes.Equal(operation.ResultJSON, materialization.TerminalResultJSON) || operation.ResultSHA256 != resultSHA {
				return false
			}
			return input.ExpectedState != KernelLocalOperationStatePendingApproval ||
				(operation.ApprovalDecisionID == preflightDecisionID && operation.ApprovalDecision == "deny" &&
					operation.ApprovalSource == "policy" && operation.ApprovalActorID == "system")
		},
		after: func(tx *transcriptstore.ImmediateTransaction, operation KernelLocalOperation) error {
			terminalMaterialization := materialization
			terminalMaterialization.CreatedAt = operation.UpdatedAt
			if err := insertKernelToolResultMaterializationTx(ctx, tx, terminalMaterialization); err != nil {
				return err
			}
			if input.ExpectedState == KernelLocalOperationStatePendingApproval {
				if _, err := appendKernelLocalOperationApprovalResolvedEvent(ctx, s, tx, operation); err != nil {
					return err
				}
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
