package server

import (
	"context"
	"errors"
	"strings"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

const kernelLocalExecRecoveryBatchSize = 128

// RunKernelLocalExecApprovalRecovery settles approvals only after their exact
// runner attempt loses authority. It uses committed-state wakes and the nearest
// lease deadline, so recovery is independent of browser reads and has no poll.
func (s *Server) RunKernelLocalExecApprovalRecovery(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.workspaceStore == nil {
		<-ctx.Done()
		return nil
	}
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()
	for {
		if context.Cause(ctx) != nil {
			return nil
		}
		if err := s.ensureApprovedKernelLocalOperationResumeDispatches(ctx); err != nil {
			return err
		}
		candidates, more, err := s.workspaceStore.ListKernelLocalExecApprovalRecoveryCandidates(
			ctx, kernelLocalExecRecoveryBatchSize,
		)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
		now := time.Now().UTC()
		var nearest time.Time
		settled := false
		for _, candidate := range candidates {
			if candidate.RunnerLive {
				policy, policyErr := s.workspaceStore.KernelLocalExecApprovalPolicy(
					ctx, candidate.Resolution.OwnerUserID, candidate.Resolution.ProjectID,
					candidate.Resolution.RootFrameID, candidate.Resolution.Tool, candidate.Resolution.Environment,
				)
				if policyErr != nil {
					return policyErr
				}
				if remembered, ok := rememberedKernelLocalExecApproval(candidate.Resolution, policy); ok {
					_, event, created, resolveErr := s.workspaceStore.ResolveKernelLocalExecApproval(ctx, remembered)
					if resolveErr != nil {
						return resolveErr
					}
					if created {
						settled = true
						if err := s.publishWorkspaceEvent(event); err != nil {
							return err
						}
						if err := s.publishWebConfirmationRemovals(event.FrameID,
							[]string{remembered.RequestID}, "processing"); err != nil {
							return err
						}
					}
					continue
				}
			}
			if s.kernelLocalExecWaiterIsActive(candidate.Resolution) {
				continue
			}
			if candidate.RunnerLive {
				if nearest.IsZero() || candidate.LeaseExpiresAt.Before(nearest) {
					nearest = candidate.LeaseExpiresAt
				}
				continue
			}
			_, event, created, resolveErr := s.workspaceStore.ResolveKernelLocalExecApproval(ctx, candidate.Resolution)
			if errors.Is(resolveErr, workspace.ErrKernelLocalExecApprovalRunnerLive) {
				continue
			}
			if resolveErr != nil {
				return resolveErr
			}
			if !created {
				continue
			}
			settled = true
			if err := s.publishWorkspaceEvent(event); err != nil {
				return err
			}
			if err := s.publishWebConfirmationRemovals(event.FrameID,
				[]string{candidate.Resolution.RequestID}, "processing"); err != nil {
				return err
			}
		}
		if settled || more {
			continue
		}
		var deadline <-chan time.Time
		if !nearest.IsZero() {
			delay := nearest.Sub(now)
			if delay < 0 {
				delay = 0
			}
			timer.Reset(delay)
			deadline = timer.C
		}
		select {
		case <-ctx.Done():
			return nil
		case <-s.workspaceStore.KernelRetentionWake():
		case <-deadline:
		}
		if deadline != nil && !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
}

func rememberedKernelLocalExecApproval(
	resolution workspace.KernelLocalExecApprovalResolutionInput,
	policy workspace.ApprovalPolicyDecision,
) (workspace.KernelLocalExecApprovalResolutionInput, bool) {
	if !policy.Found || !strings.EqualFold(strings.TrimSpace(policy.Tier), "allow") ||
		!kernelLocalOperationApprovalScopeAllowed(resolution.Tool, policy.Scope) {
		return workspace.KernelLocalExecApprovalResolutionInput{}, false
	}
	resolution.Approved = true
	resolution.Scope = strings.ToLower(strings.TrimSpace(policy.Scope))
	resolution.RequireStaleRunner = false
	return resolution, resolution.Scope != ""
}

// ensureApprovedKernelLocalOperationResumeDispatches repairs the restart
// boundary where a user approval committed successfully after the original
// runner lease disappeared, but no frame_resumed dispatch survived to wake a
// new runner. It is idempotent and never authorizes, installs, or executes a
// second operation path.
func (s *Server) ensureApprovedKernelLocalOperationResumeDispatches(ctx context.Context) error {
	operations, err := s.workspaceStore.ListApprovedKernelLocalOperations(ctx, kernelLocalExecRecoveryBatchSize)
	if err != nil {
		return err
	}
	for _, operation := range operations {
		resolution := workspace.KernelLocalExecApprovalResolutionInput{
			OwnerUserID: operation.OwnerUserID, ProjectID: operation.ProjectID, FrameID: operation.FrameID,
			FrameIncarnationID: operation.FrameIncarnationID, RootFrameID: operation.RootFrameID,
			RootIncarnationID: operation.RootFrameIncarnationID, RequestID: operation.ApprovalRequestID,
			Tool: operation.Tool, Environment: operation.Environment, InputSHA256: operation.InputSHA256,
			StreamUID: operation.StreamUID, RunnerID: operation.RunnerID, RunnerAttempt: operation.RunnerAttempt,
			ClaimTokenSHA256: operation.RunnerClaimSHA256, KernelID: operation.KernelID,
			ExpectedGeneration: uint64(operation.KernelGeneration), Approved: true, Scope: operation.ApprovalScope,
		}
		if s.kernelLocalExecWaiterIsActive(resolution) {
			continue
		}
		attemptLive, err := s.kernelLocalOperationRunnerAttemptLive(ctx, operation)
		if err != nil {
			return err
		}
		if attemptLive {
			continue
		}
		frame, found, err := s.workspaceStore.GetFrame(operation.FrameID)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		if frame.Status != workspace.FrameStatusProcessing && frame.Status != "running" {
			continue
		}
		dispatch, found, err := s.workspaceStore.GetCompatibilityFrameResumeDispatchByFrame(operation.FrameID)
		if err != nil {
			return err
		}
		if found && kernelLocalExecRecoveryDispatchActive(dispatch.Status) {
			// Approval may commit while the current dispatch is still claimed. Its
			// first wake is then intentionally a no-op, after which the runner
			// requeues itself with a bounded recovery backstop. Reassert the wake
			// from committed approved state so that backstop never becomes a
			// user-visible stall.
			if err := s.wakeFrameResumeDispatchAfterKernelTransition(ctx, operation); err != nil {
				return err
			}
			continue
		}
		// CreateAutoResumeDispatch is the single creation authority. It creates
		// a fresh dispatch when the lookup above found only terminal history.
		resume, err := s.workspaceStore.CreateAutoResumeDispatch(
			operation.FrameID, operation.RootFrameID, operation.ProjectID, frame.AgentName, "kernel_approval_wake",
		)
		if err != nil || resume.Event == nil {
			if err != nil {
				return err
			}
			continue
		}
		if err := s.registerFrameResumeDispatch(resume.Event.ID, defaultFrameResumeReservationTTL); err != nil {
			return err
		}
		if err := s.publishWorkspaceEvent(*resume.Event); err != nil {
			return err
		}
	}
	return nil
}

func kernelLocalExecRecoveryDispatchActive(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "registered", "claimed", "blocked":
		return true
	default:
		return false
	}
}

func (s *Server) kernelLocalOperationRunnerAttemptLive(
	ctx context.Context,
	operation workspace.KernelLocalOperation,
) (bool, error) {
	if s != nil && s.hasActiveSessionRun(operation.FrameID) {
		return true, nil
	}
	if s == nil || s.transcriptStore == nil || operation.RunnerAttempt <= 0 ||
		strings.TrimSpace(operation.StreamUID) == "" || strings.TrimSpace(operation.OwnerUserID) == "" {
		return false, nil
	}
	state, err := s.transcriptStore.GetRunnerRuntimeState(
		ctx, operation.StreamUID, operation.OwnerUserID, operation.RunnerAttempt,
	)
	if err != nil {
		return false, err
	}
	return state.Status == "running" &&
		strings.TrimSpace(state.RunnerID) == strings.TrimSpace(operation.RunnerID) &&
		state.ExpiresAt.After(time.Now().UTC()), nil
}

func (s *Server) KernelLocalExecApprovalRecoveryEnabled() bool {
	return s != nil && s.workspaceStore != nil
}
