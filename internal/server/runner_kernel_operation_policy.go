package server

import (
	"context"
	"errors"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

// resolveCheckpointKernelOperationPolicies closes the policy gap between the
// atomic model-tool-call checkpoint and execution. A full-access or deny
// decision must be durable before the runner can yield; otherwise progress
// incorrectly depends on a connected frontend polling the confirmation API.
func (s *Server) resolveCheckpointKernelOperationPolicies(
	ctx context.Context,
	run *sessionRunnerChatRun,
	receipts []transcriptstore.RunnerCheckpointKernelOperationReceipt,
) error {
	if len(receipts) == 0 {
		return nil
	}
	if s == nil || s.workspaceStore == nil || run == nil || run.Transcript == nil {
		return errors.New("kernel checkpoint approval policy authority is unavailable")
	}
	for _, receipt := range receipts {
		operation, found, err := s.workspaceStore.GetKernelLocalOperation(
			ctx, run.Transcript.Stream.OwnerID, receipt.OperationID,
		)
		if err != nil {
			return err
		}
		if !found || operation.StreamUID != run.Transcript.Stream.UID ||
			operation.ToolCallID != receipt.ToolCallID ||
			operation.State != workspace.KernelLocalOperationStatePendingApproval {
			return workspace.ErrKernelLocalOperationConflict
		}
		_, decision, err := s.resolvePendingKernelLocalOperationPolicy(
			ctx, run, run.Transcript.Claim, operation,
		)
		if err != nil {
			return err
		}
		switch decision {
		case "allow", "ask", "deny":
		default:
			return errors.New("kernel checkpoint approval policy decision is invalid")
		}
	}
	return nil
}
