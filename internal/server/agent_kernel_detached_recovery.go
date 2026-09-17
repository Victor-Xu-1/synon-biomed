package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

const (
	detachedKernelRecoveryBatchSize       = 128
	detachedKernelRecoveryPollInterval    = 2 * time.Second
	detachedKernelRecoveryRetryInterval   = 5 * time.Second
	detachedKernelBackendLivenessDeadline = 45 * time.Second
)

// RunDetachedKernelExecutionRecovery reattaches the Web read/projection layer
// to executor-owned work after a service restart. Physical execution never
// moves back into the Web process: active work is observed through the backend
// protocol and terminal work is settled from its immutable SQLite receipt.
func (s *Server) RunDetachedKernelExecutionRecovery(ctx context.Context) error {
	if s == nil || s.workspaceStore == nil || s.transcriptStore == nil || s.kernelExecutionBackend == nil {
		return errors.New("detached kernel recovery authority is unavailable")
	}
	for {
		wake := s.workspaceStore.KernelRetentionWake()
		candidates, more, err := s.workspaceStore.ListDetachedKernelExecutionRecoveryCandidates(
			ctx, s.kernelOperationBootID, detachedKernelRecoveryBatchSize,
		)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		retry := more
		for _, candidate := range candidates {
			if err := s.recoverDetachedKernelExecution(ctx, candidate); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				retry = true
				// One malformed or temporarily un-settleable detached execution
				// must not take the recovery supervisor (and therefore the whole
				// service) out of health. Retain the durable candidate for a bounded
				// retry, record the exact identity and continue with this batch.
				log.Printf("detached kernel recovery candidate retained for retry execution=%s operation=%s: %v",
					candidate.Execution.ExecutionID, candidate.Operation.OperationID, err)
			}
		}
		if more {
			continue
		}
		waitInterval := detachedKernelRecoveryPollInterval
		if retry {
			waitInterval = detachedKernelRecoveryRetryInterval
		}
		timer := time.NewTimer(waitInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil
		case <-wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
	}
}

func (s *Server) recoverDetachedKernelExecution(
	ctx context.Context,
	candidate workspace.DetachedKernelExecutionRecoveryCandidate,
) error {
	execution := candidate.Execution
	operation := candidate.Operation
	request, err := detachedKernelRequestFromExecution(execution)
	if err != nil {
		return err
	}
	access, found, err := s.workspaceStore.GetKernelFrameAccessContext(ctx, request.FrameID)
	if err != nil {
		return err
	}
	if !found || access.UserID != request.OwnerUserID || access.Frame.ProjectID != request.ProjectID ||
		access.Frame.RootFrameID != request.RootFrameID || access.RootFrameIncarnationID != request.RootFrameIncarnationID ||
		access.Frame.IncarnationID != request.FrameIncarnationID {
		return workspace.ErrDetachedKernelExecutionConflict
	}
	backend, found, err := s.workspaceStore.GetKernelExecutionBackend(ctx, execution.BackendID)
	if err != nil {
		return err
	}
	if !found || backend.BackendGeneration != execution.BackendGeneration ||
		backend.OwnerUserID != request.OwnerUserID || backend.ProjectID != request.ProjectID ||
		backend.RootFrameID != request.RootFrameID || backend.FrameID != request.FrameID ||
		backend.KernelID != request.KernelID || backend.KernelGeneration != request.KernelGeneration {
		return workspace.ErrDetachedKernelExecutionConflict
	}
	persistedSpec, err := workspace.DecodeKernelExecutionSessionSpecV1(backend.SessionSpecJSON)
	if err != nil {
		return err
	}
	spec := recoveredKernelSessionSpec(persistedSpec)
	backendSession := kernelruntime.BackendSessionRef{
		BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
		KernelID: backend.KernelID, KernelGeneration: backend.KernelGeneration,
		ExecutorInstanceID: backend.ExecutorInstanceID, SocketPath: backend.SocketPath,
	}
	started := kernelruntime.ExecutionStarted{
		ExecID: execution.ExecutionID, ToolUseID: request.ToolCallID, KernelID: request.KernelID,
		FrameID: request.FrameID, Language: request.Language, Environment: request.Environment,
		KernelKind: request.KernelKind, Code: request.Code, Origin: request.Origin,
	}
	if execution.WorkerStartedAt != nil {
		started.StartedAt = execution.WorkerStartedAt.UTC()
	} else {
		started.StartedAt = execution.AcceptedAt.UTC()
	}
	if request.Background {
		if err := s.recordAgentKernelBackgroundStart(access, started); err != nil {
			return fmt.Errorf("restore detached kernel background execution: %w", err)
		}
	}
	ref := kernelruntime.BackendExecutionRef{
		ExecutionID: execution.ExecutionID, OperationID: execution.OperationID,
		BackendID: execution.BackendID, BackendGeneration: execution.BackendGeneration,
		RequestSHA256: execution.RequestSHA256, ConfinementSHA256: execution.ConfinementSHA256,
	}
	observerCtx, cancelObserver, reserved := s.reserveDetachedKernelObserver(ctx, execution.ExecutionID)
	if !reserved {
		return nil
	}
	launchedObserver := false
	defer func() {
		if !launchedObserver {
			s.releaseDetachedKernelObserver(execution.ExecutionID, cancelObserver)
		}
	}()
	if execution.State == workspace.DetachedKernelExecutionStateTerminal {
		_, err := s.settleDetachedAgentKernelExecution(
			ctx, access, spec, backendSession, started, execution,
			request.OutputLimitBytes, &operation, nil,
		)
		if err != nil {
			return fmt.Errorf("settle detached kernel execution %s operation %s: %w",
				execution.ExecutionID, operation.OperationID, err)
		}
		return nil
	}
	// A terminal receipt is immutable computation evidence and remains
	// authoritative after its task-owned executor has stopped. Only an
	// unfinished execution whose backend stopped without a receipt has an
	// unknown outcome. Checking backend state before terminal settlement made
	// successful cancellation and one-shot software cleanup look evidence-lost
	// during restart recovery.
	if backend.State == workspace.KernelExecutionBackendStateStopped ||
		backend.State == workspace.KernelExecutionBackendStateEvidenceLost {
		return s.settleDetachedKernelEvidenceLost(ctx, execution, operation)
	}
	lease, _, err := s.kernelExecutionBackend.AcquireControl(ctx, ref, detachedKernelControlTTL)
	if err != nil {
		settled, reconcileErr := s.reconcileUnreachableDetachedKernelBackend(ctx, backend, execution, operation)
		if reconcileErr != nil {
			return errors.Join(err, reconcileErr)
		}
		if settled {
			return nil
		}
		return err
	}
	if execution.State == workspace.DetachedKernelExecutionStateCancelRequested {
		if err := s.deliverPersistedDetachedCancellation(ctx, ref, lease); err != nil {
			return err
		}
		s.launchReservedDetachedKernelObserver(
			observerCtx, cancelObserver, access, spec, backendSession, started, ref, lease,
			request.OutputLimitBytes, operation, nil,
		)
		launchedObserver = true
		return nil
	}
	if execution.State == workspace.DetachedKernelExecutionStateAccepted ||
		execution.State == workspace.DetachedKernelExecutionStateDispatchCommitted {
		if _, err := s.kernelExecutionBackend.Start(ctx, ref, kernelruntime.BackendStartFence{
			ExecutionStateVersion: execution.StateVersion, ControllerEpoch: lease.Epoch,
			ControllerToken: lease.Token, DispatchSequence: 1,
		}); err != nil {
			return err
		}
	}
	s.launchReservedDetachedKernelObserver(
		observerCtx, cancelObserver, access, spec, backendSession, started, ref, lease,
		request.OutputLimitBytes, operation, nil,
	)
	launchedObserver = true
	return nil
}

func (s *Server) reconcileUnreachableDetachedKernelBackend(
	ctx context.Context,
	backend workspace.KernelExecutionBackend,
	execution workspace.DetachedKernelExecution,
	operation workspace.KernelLocalOperation,
) (bool, error) {
	dead, err := detachedKernelBackendDefinitivelyDead(
		backend, time.Now().UTC(), kernelruntime.ProcessIdentityAlive,
	)
	if err != nil {
		return false, err
	}
	if !dead {
		return false, nil
	}
	if _, err := s.workspaceStore.FinishKernelExecutionBackend(ctx,
		workspace.FinishKernelExecutionBackendInput{
			BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
			ExecutorInstanceID: backend.ExecutorInstanceID, EvidenceLost: true,
		}); err != nil && !errors.Is(err, workspace.ErrKernelExecutionBackendStale) {
		return false, err
	}
	if err := s.settleDetachedKernelEvidenceLost(ctx, execution, operation); err != nil {
		return false, err
	}
	return true, nil
}

// detachedKernelBackendDefinitivelyDead uses the executor process identity as
// the strongest local liveness signal. A known-dead PID is conclusive and must
// not leave the UI falsely running for the heartbeat grace period. The grace
// remains for remote/legacy backends without a process identity, while a live
// process is never declared dead merely because a heartbeat is delayed during
// a long CPU- or I/O-bound operation.
func detachedKernelBackendDefinitivelyDead(
	backend workspace.KernelExecutionBackend,
	now time.Time,
	probe func(pid int64, startTicks int64) (bool, error),
) (bool, error) {
	if backend.ExecutorPID > 0 && backend.ExecutorPIDStartTicks > 0 && probe != nil {
		alive, err := probe(backend.ExecutorPID, backend.ExecutorPIDStartTicks)
		if err != nil {
			return false, err
		}
		return !alive, nil
	}
	return detachedKernelBackendLivenessExpired(backend, now), nil
}

func detachedKernelBackendLivenessExpired(backend workspace.KernelExecutionBackend, now time.Time) bool {
	lastLiveness := backend.UpdatedAt.UTC()
	if backend.HeartbeatAt != nil {
		lastLiveness = backend.HeartbeatAt.UTC()
	}
	return !lastLiveness.IsZero() && now.UTC().Sub(lastLiveness) >= detachedKernelBackendLivenessDeadline
}

func (s *Server) settleDetachedKernelEvidenceLost(
	ctx context.Context,
	execution workspace.DetachedKernelExecution,
	operation workspace.KernelLocalOperation,
) error {
	if _, err := s.workspaceStore.MarkKernelExecutionEvidenceLost(ctx,
		workspace.MarkKernelExecutionEvidenceLostInput{
			ExecutionID: execution.ExecutionID, BackendGeneration: execution.BackendGeneration,
			ReasonCode: "detached_backend_evidence_lost",
		}); err != nil {
		return err
	}
	_, err := s.workspaceStore.MarkKernelLocalOperationOutcomeUnknown(ctx,
		workspace.MarkKernelLocalOperationOutcomeUnknownInput{
			OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
			ExpectedStateVersion: operation.StateVersion, ExecutionID: operation.ExecutionID,
			CurrentBootID: s.kernelOperationBootID,
			ReasonCode:    "detached_backend_evidence_lost",
		})
	return err
}

func recoveredKernelSessionSpec(input workspace.KernelExecutionSessionSpecV1) kernelruntime.SessionSpec {
	mounts := make([]kernelruntime.WorkerMount, 0, len(input.Mounts))
	for _, mount := range input.Mounts {
		if mount.Trusted {
			mounts = append(mounts, kernelruntime.TrustedReadOnlyDirectoryMount(mount.Path))
			continue
		}
		mounts = append(mounts, kernelruntime.WorkerMount{Path: mount.Path, Writable: mount.Writable})
	}
	return kernelruntime.SessionSpec{
		KernelID: input.KernelID, OwnerID: input.OwnerUserID, ProjectID: input.ProjectID,
		FrameID: input.FrameID, FrameIncarnationID: input.FrameIncarnationID,
		RootFrameID: input.RootFrameID, RootFrameIncarnationID: input.RootFrameIncarnationID,
		AgentName: input.AgentName, DelegateName: input.DelegateName, KernelKind: input.KernelKind,
		Language: input.Language, Environment: input.Environment, RuntimeGeneration: input.RuntimeGeneration,
		WorkspaceDir: input.WorkspaceDir, Mounts: mounts,
		ProtectedPaths:       append([]string(nil), input.ProtectedPaths...),
		EgressAllowedDomains: append([]string(nil), input.EgressAllowedDomains...),
		EgressDeniedDomains:  append([]string(nil), input.EgressDeniedDomains...),
		CABundle:             input.CABundle, UpstreamProxy: input.UpstreamProxy, Fresh: input.Fresh,
	}
}

func (s *Server) DetachedKernelExecutionRecoveryEnabled() bool {
	return s != nil && s.workspaceStore != nil && s.transcriptStore != nil && s.kernelExecutionBackend != nil
}
