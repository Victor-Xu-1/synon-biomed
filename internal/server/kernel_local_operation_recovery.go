package server

import (
	"context"
	"errors"
	"strings"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

const kernelLocalOperationRecoveryBatchSize = 250

// RunKernelLocalOperationRecovery reconciles crash windows without replaying
// an uncertain side effect. A prepared operation whose old runner lease is
// gone returns to approved and admits a new runner revision. A started
// operation from an earlier server boot becomes outcome_unknown and requires
// evidence/manual reconciliation; it is never executed again automatically.
func (s *Server) RunKernelLocalOperationRecovery(ctx context.Context) error {
	if s == nil || s.workspaceStore == nil || s.kernelOperationBootID == "" {
		return errors.New("kernel local operation recovery authority is unavailable")
	}
	for {
		// Take the current wake generation before querying durable state. Any
		// committed transition after this point closes this exact channel, so
		// query-to-wait races cannot strand a recovery candidate.
		wake := s.workspaceStore.KernelRetentionWake()
		pending, err := s.workspaceStore.ListPendingKernelLocalOperations(ctx, kernelLocalOperationRecoveryBatchSize)
		if err != nil {
			return err
		}
		for _, operation := range pending {
			frame, found, frameErr := s.workspaceStore.GetFrame(operation.FrameID)
			if frameErr != nil {
				return frameErr
			}
			if !found || isFrameResumeTerminalStatus(frame.Status) {
				continue
			}
			policy, policyErr := s.workspaceStore.KernelLocalExecApprovalPolicy(
				ctx, operation.OwnerUserID, operation.ProjectID, operation.RootFrameID,
				operation.Tool, operation.Environment,
			)
			if policyErr != nil {
				return policyErr
			}
			scope, allowed := rememberedKernelLocalOperationScope(operation, policy)
			if !allowed {
				continue
			}
			resolved, resolveErr := s.workspaceStore.ResolveKernelLocalOperationApproval(ctx,
				workspace.ResolveKernelLocalOperationApprovalInput{
					OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
					ExpectedStateVersion: operation.StateVersion, ApprovalRequestID: operation.ApprovalRequestID,
					Approved: true, DecisionID: kernelLocalOperationDecisionID(
						operation.OwnerUserID, operation.OperationID, operation.ApprovalRequestID, true, scope,
					), Scope: scope, Source: "remembered", ActorID: "system:remembered-approval-recovery",
					AdmitRunnerRevision: true,
				})
			if resolveErr != nil && !errors.Is(resolveErr, workspace.ErrKernelLocalOperationStale) {
				return resolveErr
			}
			if resolveErr == nil {
				if err := s.wakeFrameResumeDispatchAfterKernelTransition(ctx, resolved); err != nil {
					return err
				}
			}
		}
		for {
			candidates, more, err := s.workspaceStore.ListKernelLocalOperationRecoveryCandidates(
				ctx, s.kernelOperationBootID, kernelLocalOperationRecoveryBatchSize,
			)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			for _, candidate := range candidates {
				if candidate.AttemptLive {
					continue
				}
				operation := candidate.Operation
				switch operation.State {
				case workspace.KernelLocalOperationStatePrepared:
					_, err = s.workspaceStore.ReclaimPreparedKernelLocalOperation(ctx,
						workspace.ReclaimPreparedKernelLocalOperationInput{
							OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
							ExpectedStateVersion: operation.StateVersion, RunnerID: operation.RunnerID,
							RunnerAttempt:     operation.RunnerAttempt,
							RunnerClaimSHA256: operation.RunnerClaimSHA256, BootID: operation.BootID,
							ReasonCode: "runner_restarted_before_start",
						})
				case workspace.KernelLocalOperationStateStarted:
					if operation.BootID == s.kernelOperationBootID {
						continue
					}
					_, err = s.workspaceStore.MarkKernelLocalOperationOutcomeUnknown(ctx,
						workspace.MarkKernelLocalOperationOutcomeUnknownInput{
							OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
							ExpectedStateVersion: operation.StateVersion, ExecutionID: operation.ExecutionID,
							CurrentBootID: s.kernelOperationBootID, ReasonCode: "service_restarted_after_start",
						})
				}
				if err != nil && !errors.Is(err, workspace.ErrKernelLocalOperationStale) {
					return err
				}
			}
			if !more {
				break
			}
		}
		for {
			candidates, more, err := s.workspaceStore.ListTerminalFrameKernelApprovalRecoveryCandidates(
				ctx, kernelLocalOperationRecoveryBatchSize,
			)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			for _, operation := range candidates {
				_, err = s.workspaceStore.ResolveKernelLocalOperationApproval(ctx,
					workspace.ResolveKernelLocalOperationApprovalInput{
						OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
						ExpectedStateVersion: operation.StateVersion, ApprovalRequestID: operation.ApprovalRequestID,
						Approved: false, DecisionID: "terminal-frame-recovery:" + operation.OperationID,
						Scope: "once", Source: "stale_reconcile", ActorID: "system:kernel-operation-recovery",
						ReasonCode: "terminal_frame_orphaned_approval",
					})
				if err != nil && !errors.Is(err, workspace.ErrKernelLocalOperationStale) {
					return err
				}
			}
			if !more {
				break
			}
		}
		for {
			batches, more, err := s.workspaceStore.ListTerminalFrameToolBatchRecoveryCandidates(
				ctx, kernelLocalOperationRecoveryBatchSize,
			)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			for _, batch := range batches {
				_, err = s.workspaceStore.ReconcileTerminalFrameToolCallBatch(ctx, batch.OwnerUserID, batch.BatchID)
				if err != nil && !errors.Is(err, workspace.ErrToolCallBatchStale) {
					return err
				}
			}
			if !more {
				break
			}
		}
		due, found, err := s.workspaceStore.NextKernelLocalOperationRecoveryDue(ctx, s.kernelOperationBootID)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if !found {
			select {
			case <-ctx.Done():
				return nil
			case <-wake:
			}
			continue
		}
		delay := time.Until(due)
		if delay <= 0 {
			continue
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		case <-wake:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
	}
}

func rememberedKernelLocalOperationScope(
	operation workspace.KernelLocalOperation,
	policy workspace.ApprovalPolicyDecision,
) (string, bool) {
	scope := strings.ToLower(strings.TrimSpace(policy.Scope))
	return scope, policy.Found && strings.EqualFold(strings.TrimSpace(policy.Tier), "allow") &&
		scope != "" && kernelLocalOperationApprovalScopeAllowed(operation.Tool, scope)
}

func (s *Server) KernelLocalOperationRecoveryEnabled() bool {
	return s != nil && s.workspaceStore != nil && s.kernelOperationBootID != ""
}
