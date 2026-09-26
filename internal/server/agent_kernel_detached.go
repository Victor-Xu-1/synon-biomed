package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"synon-go/internal/agentruntime"
	kernelruntime "synon-go/internal/kernel"
	kerneldetached "synon-go/internal/kernel/detached"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/software/localcontainer"
)

const (
	detachedKernelControlTTL     = 2 * time.Minute
	detachedKernelControlRenewal = 45 * time.Second
)

type detachedKernelTerminalSettlementError struct {
	execution workspace.DetachedKernelExecution
	cause     error
}

func (e *detachedKernelTerminalSettlementError) Error() string {
	if e == nil || e.cause == nil {
		return "detached kernel terminal settlement failed"
	}
	return e.cause.Error()
}

func (e *detachedKernelTerminalSettlementError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (s *Server) executeDetachedAgentKernel(
	ctx context.Context,
	access workspace.KernelFrameAccess,
	identity *agentKernelContext,
	spec kernelruntime.SessionSpec,
	backendSession kernelruntime.BackendSessionRef,
	publicName string,
	input map[string]any,
	code string,
	workingDir string,
	background bool,
	executionTimeout time.Duration,
	outputLimitBytes int64,
	approvalCall agentruntime.ToolCall,
) (map[string]any, error) {
	if s == nil || s.workspaceStore == nil || s.kernelExecutionBackend == nil || identity == nil {
		return nil, errors.New("detached kernel execution authority is unavailable")
	}
	taskOwnedSession := localcontainer.IsEnvironmentName(spec.Environment)
	operation, err := s.authorizeAgentKernelLocalOperation(
		ctx, identity, approvalCall, publicName, input, spec.Environment,
	)
	if err != nil {
		return nil, s.releaseUnstartedDetachedSession(publicName, backendSession, err, taskOwnedSession)
	}
	if operation.ExecutionLogID != "" {
		return s.replayAgentKernelOperationResult(operation)
	}
	run, _ := ctx.Value(transcriptRunnerChatRunContextKey{}).(*sessionRunnerChatRun)
	if run == nil || run.Transcript == nil {
		return nil, s.releaseUnstartedDetachedSession(
			publicName, backendSession, errors.New("kernel local operation runner authority is unavailable"), taskOwnedSession,
		)
	}
	claim := run.Transcript.Claim
	if err := s.validateLiveTranscriptRunnerClaim(ctx, claim); err != nil {
		return nil, s.releaseUnstartedDetachedSession(publicName, backendSession, err, taskOwnedSession)
	}
	confinementSHA, err := agentKernelConfinementSHA256(spec)
	if err != nil {
		return nil, s.releaseUnstartedDetachedSession(publicName, backendSession, err, taskOwnedSession)
	}
	prepared, err := s.workspaceStore.PrepareKernelLocalOperation(ctx,
		workspace.PrepareKernelLocalOperationInput{
			OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
			ExpectedStateVersion: operation.StateVersion, Claim: claim,
			BootID: s.kernelOperationBootID, KernelID: backendSession.KernelID,
			KernelGeneration: backendSession.KernelGeneration, ConfinementSHA256: confinementSHA,
		})
	if err != nil {
		return nil, s.releaseUnstartedDetachedSession(publicName, backendSession, err, taskOwnedSession)
	}
	releasePrepared := func(cause error) (map[string]any, error) {
		releaseCtx, cancelRelease := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancelRelease()
		_, releaseErr := s.workspaceStore.ReleasePreparedKernelLocalOperation(
			releaseCtx, workspace.ReleasePreparedKernelLocalOperationInput{
				OwnerUserID: prepared.OwnerUserID, OperationID: prepared.OperationID,
				ExpectedStateVersion: prepared.StateVersion, Claim: claim,
				BootID: s.kernelOperationBootID, ReasonCode: "infrastructure_setup_interrupted",
			},
		)
		settledErr := cause
		if releaseErr != nil && !errors.Is(releaseErr, workspace.ErrKernelLocalOperationStale) {
			settledErr = errors.Join(cause, fmt.Errorf("release prepared kernel operation: %w", releaseErr))
		}
		return nil, s.releaseUnstartedDetachedSession(publicName, backendSession, settledErr, taskOwnedSession)
	}
	lease, err := s.kernelExecutionBackend.AcquireSessionControl(ctx, backendSession, detachedKernelControlTTL)
	if err != nil {
		return releasePrepared(err)
	}
	executionID := uuid.NewString()
	toolUseID := operation.ToolCallID
	var startedOperation workspace.KernelLocalOperation
	var execution workspace.DetachedKernelExecution
	err = s.withKernelHostGrantAdmission(access.UserID, func() error {
		var admissionErr error
		startedOperation, execution, admissionErr = s.workspaceStore.StartDetachedKernelLocalOperation(ctx,
			workspace.StartDetachedKernelLocalOperationInput{
				Start: workspace.StartKernelLocalOperationInput{
					OwnerUserID: prepared.OwnerUserID, OperationID: prepared.OperationID,
					ExpectedStateVersion: prepared.StateVersion, Claim: claim,
					BootID: s.kernelOperationBootID, ExecutionID: executionID,
				},
				BackendID: backendSession.BackendID, BackendGeneration: backendSession.BackendGeneration,
				ControllerEpoch: lease.Epoch, ControllerToken: lease.Token,
				Request: newDetachedKernelExecutionRequest(
					prepared, spec, executionID, code, workingDir, background, executionTimeout, outputLimitBytes,
				),
			})
		return admissionErr
	})
	if err != nil {
		return releasePrepared(err)
	}
	operation = startedOperation
	ref := kernelruntime.BackendExecutionRef{
		ExecutionID: execution.ExecutionID, OperationID: execution.OperationID,
		BackendID: execution.BackendID, BackendGeneration: execution.BackendGeneration,
		RequestSHA256: execution.RequestSHA256, ConfinementSHA256: execution.ConfinementSHA256,
	}
	started := kernelruntime.ExecutionStarted{
		ExecID: executionID, ToolUseID: toolUseID, KernelID: backendSession.KernelID,
		FrameID: access.Frame.ID, Language: spec.Language, Environment: spec.Environment,
		KernelKind: spec.KernelKind, Code: code, Origin: "agent", StartedAt: time.Now().UTC(),
	}
	_, err = s.kernelExecutionBackend.Start(ctx, ref, kernelruntime.BackendStartFence{
		ExecutionStateVersion: execution.StateVersion, ControllerEpoch: lease.Epoch,
		ControllerToken: lease.Token, DispatchSequence: 1,
	})
	if err != nil {
		cancelCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		cancelErr := s.deliverPersistedDetachedCancellation(cancelCtx, ref, lease)
		cancel()
		if cancelErr == nil {
			s.startDetachedAgentKernelObserver(
				context.Background(), access, spec, backendSession, started, ref, lease,
				outputLimitBytes, operation, &claim,
			)
			return nil, errors.New("kernel execution was cancelled before dispatch")
		}
		return nil, err
	}
	if background {
		if err := s.recordAgentKernelBackgroundStart(access, started); err != nil {
			return nil, fmt.Errorf("record detached kernel background execution: %w", err)
		}
		s.startDetachedAgentKernelObserver(
			context.Background(), access, spec, backendSession, started, ref, lease,
			outputLimitBytes, operation, &claim,
		)
		return map[string]any{
			"status": "running", "exec_id": executionID,
			"message": "Kernel cell is running in the background.",
		}, nil
	}
	waitBudget := s.agentKernelForegroundWaitBudget(access.Frame.RootFrameID)
	waitCtx := ctx
	var cancel context.CancelFunc
	if waitBudget > 0 {
		waitCtx, cancel = context.WithTimeout(ctx, waitBudget)
		defer cancel()
	}
	result, err := s.waitDetachedAgentKernelExecution(
		waitCtx, access, spec, backendSession, started, ref, lease,
		outputLimitBytes, &operation, &claim,
	)
	if err == nil {
		return result, nil
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	s.startDetachedAgentKernelObserver(
		context.Background(), access, spec, backendSession, started, ref, lease,
		outputLimitBytes, operation, &claim,
	)
	// Foreground calls retain their original protocol identity until the
	// detached supervisor commits the real terminal result. Returning a normal
	// "running" value here would let the engine checkpoint that provisional
	// value as the tool's immutable terminal receipt, so the model could never
	// observe the later computation result and might execute the same work again.
	return nil, &kernelLocalOperationPendingRecoveryError{
		operationID: operation.OperationID,
		state:       operation.State,
		cause: fmt.Errorf(
			"kernel foreground execution %s remains under detached supervision",
			executionID,
		),
	}
}

// releaseUnstartedDetachedSession closes a one-shot software executor whenever
// setup fails before a durable execution is accepted. Background Python/R
// sessions are persistent by design and remain untouched. This prevents an
// approval, validation, or persistence error from leaving an idle task-owned
// process tree behind until the global idle timeout.
func (s *Server) releaseUnstartedDetachedSession(
	publicName string,
	backendSession kernelruntime.BackendSessionRef,
	cause error,
	taskOwned ...bool,
) error {
	owned := strings.EqualFold(strings.TrimSpace(publicName), softwareRuntimeToolName)
	if len(taskOwned) > 0 && taskOwned[0] {
		owned = true
	}
	if !owned {
		return cause
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.closeDetachedTaskOwnedSession(cleanupCtx, backendSession); err != nil {
		return errors.Join(cause, fmt.Errorf("close unstarted task-owned software runtime session: %w", err))
	}
	return cause
}

// newDetachedKernelExecutionRequest derives every durable authority field from
// the admitted operation. ExecutionID identifies the physical execution;
// ToolCallID must remain the original model call identity so protocol replay,
// tool-batch settlement, and artifact ownership cannot fork into a second ID.
func newDetachedKernelExecutionRequest(
	operation workspace.KernelLocalOperation,
	spec kernelruntime.SessionSpec,
	executionID string,
	code string,
	workingDir string,
	background bool,
	executionTimeout time.Duration,
	outputLimitBytes int64,
) workspace.KernelDetachedExecutionRequestV1 {
	return workspace.KernelDetachedExecutionRequestV1{
		Version: 1, OperationID: operation.OperationID, ExecutionID: executionID,
		OwnerUserID: operation.OwnerUserID, ProjectID: operation.ProjectID,
		RootFrameID: operation.RootFrameID, RootFrameIncarnationID: operation.RootFrameIncarnationID,
		FrameID: operation.FrameID, FrameIncarnationID: operation.FrameIncarnationID,
		KernelID: operation.KernelID, KernelGeneration: operation.KernelGeneration,
		ToolCallID: operation.ToolCallID, ToolName: operation.Tool, Language: spec.Language,
		KernelKind: spec.KernelKind, Environment: operation.Environment, Code: code,
		WorkingDir: workingDir, Background: background, Origin: "agent",
		TimeoutMillis:    executionTimeout.Milliseconds(),
		OutputLimitBytes: outputLimitBytes,
	}
}

func (s *Server) deliverPersistedDetachedCancellation(
	ctx context.Context,
	ref kernelruntime.BackendExecutionRef,
	lease kernelruntime.BackendControlLease,
) error {
	execution, found, err := s.workspaceStore.GetDetachedKernelExecution(ctx, ref.ExecutionID)
	if err != nil {
		return err
	}
	if !found || execution.OperationID != ref.OperationID || execution.BackendID != ref.BackendID ||
		execution.BackendGeneration != ref.BackendGeneration ||
		execution.State != workspace.DetachedKernelExecutionStateCancelRequested ||
		execution.CancelRequestID == "" {
		return workspace.ErrDetachedKernelExecutionConflict
	}
	_, err = s.kernelExecutionBackend.Cancel(ctx, ref, lease, kernelruntime.BackendCancelRequest{
		CancelRequestID: execution.CancelRequestID, ExpectedVersion: execution.StateVersion,
	})
	return err
}

func (s *Server) observeDetachedAgentKernelExecution(
	ctx context.Context,
	access workspace.KernelFrameAccess,
	spec kernelruntime.SessionSpec,
	backendSession kernelruntime.BackendSessionRef,
	started kernelruntime.ExecutionStarted,
	ref kernelruntime.BackendExecutionRef,
	lease kernelruntime.BackendControlLease,
	outputLimitBytes int64,
	operation workspace.KernelLocalOperation,
	claim *transcriptstore.RunnerClaim,
) {
	var terminal *workspace.DetachedKernelExecution
	var retry uint64
	for {
		var err error
		if terminal == nil {
			_, err = s.waitDetachedAgentKernelExecution(
				ctx, access, spec, backendSession, started, ref, lease,
				outputLimitBytes, &operation, claim,
			)
			var settlementErr *detachedKernelTerminalSettlementError
			if errors.As(err, &settlementErr) {
				captured := settlementErr.execution
				terminal = &captured
				err = settlementErr.cause
			}
		} else {
			_, err = s.settleDetachedAgentKernelExecution(
				ctx, access, spec, backendSession, started, *terminal,
				outputLimitBytes, &operation, claim,
			)
		}
		if err == nil || errors.Is(err, context.Canceled) {
			return
		}
		if terminal == nil {
			if renewed, _, acquireErr := s.kernelExecutionBackend.AcquireControl(
				ctx, ref, detachedKernelControlTTL,
			); acquireErr == nil {
				lease = renewed
			}
		} else if retry == 0 || retry%60 == 0 {
			log.Printf("detached_kernel_terminal_settlement_retry execution=%s attempt=%d error=%v",
				ref.ExecutionID, retry+1, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
		if retry != ^uint64(0) {
			retry++
		}
	}
}

func (s *Server) reserveDetachedKernelObserver(
	ctx context.Context,
	executionID string,
) (context.Context, context.CancelFunc, bool) {
	if s == nil || strings.TrimSpace(executionID) == "" {
		return nil, nil, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	observerCtx, cancel := context.WithCancel(ctx)
	s.detachedKernelObserverMu.Lock()
	if s.detachedKernelObservers == nil {
		s.detachedKernelObservers = make(map[string]context.CancelFunc)
	}
	if s.detachedKernelObserversDraining {
		s.detachedKernelObserverMu.Unlock()
		cancel()
		return nil, nil, false
	}
	if _, exists := s.detachedKernelObservers[executionID]; exists {
		s.detachedKernelObserverMu.Unlock()
		cancel()
		return nil, nil, false
	}
	if len(s.detachedKernelObservers) == 0 {
		s.detachedKernelObserversDone = make(chan struct{})
	}
	s.detachedKernelObservers[executionID] = cancel
	s.detachedKernelObserverMu.Unlock()
	return observerCtx, cancel, true
}

func (s *Server) releaseDetachedKernelObserver(executionID string, cancel context.CancelFunc) {
	if s == nil {
		if cancel != nil {
			cancel()
		}
		return
	}
	s.detachedKernelObserverMu.Lock()
	if _, found := s.detachedKernelObservers[executionID]; found {
		delete(s.detachedKernelObservers, executionID)
		if len(s.detachedKernelObservers) == 0 && s.detachedKernelObserversDone != nil {
			close(s.detachedKernelObserversDone)
		}
	}
	s.detachedKernelObserverMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Server) beginDetachedKernelObserverShutdown() {
	if s == nil {
		return
	}
	s.detachedKernelObserverMu.Lock()
	s.detachedKernelObserversDraining = true
	cancels := make([]context.CancelFunc, 0, len(s.detachedKernelObservers))
	for _, cancel := range s.detachedKernelObservers {
		cancels = append(cancels, cancel)
	}
	s.detachedKernelObserverMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (s *Server) waitDetachedKernelObservers(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.detachedKernelObserverMu.Lock()
	if len(s.detachedKernelObservers) == 0 {
		s.detachedKernelObserverMu.Unlock()
		return nil
	}
	done := s.detachedKernelObserversDone
	s.detachedKernelObserverMu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) stopDetachedKernelObservers(ctx context.Context) error {
	s.beginDetachedKernelObserverShutdown()
	return s.waitDetachedKernelObservers(ctx)
}

func (s *Server) launchReservedDetachedKernelObserver(
	observerCtx context.Context,
	cancel context.CancelFunc,
	access workspace.KernelFrameAccess,
	spec kernelruntime.SessionSpec,
	backendSession kernelruntime.BackendSessionRef,
	started kernelruntime.ExecutionStarted,
	ref kernelruntime.BackendExecutionRef,
	lease kernelruntime.BackendControlLease,
	outputLimitBytes int64,
	operation workspace.KernelLocalOperation,
	claim *transcriptstore.RunnerClaim,
) {
	go func() {
		defer s.releaseDetachedKernelObserver(ref.ExecutionID, cancel)
		s.observeDetachedAgentKernelExecution(
			observerCtx, access, spec, backendSession, started, ref, lease,
			outputLimitBytes, operation, claim,
		)
	}()
}

func (s *Server) startDetachedAgentKernelObserver(
	ctx context.Context,
	access workspace.KernelFrameAccess,
	spec kernelruntime.SessionSpec,
	backendSession kernelruntime.BackendSessionRef,
	started kernelruntime.ExecutionStarted,
	ref kernelruntime.BackendExecutionRef,
	lease kernelruntime.BackendControlLease,
	outputLimitBytes int64,
	operation workspace.KernelLocalOperation,
	claim *transcriptstore.RunnerClaim,
) bool {
	observerCtx, cancel, reserved := s.reserveDetachedKernelObserver(ctx, ref.ExecutionID)
	if !reserved {
		return false
	}
	s.launchReservedDetachedKernelObserver(
		observerCtx, cancel, access, spec, backendSession, started, ref, lease,
		outputLimitBytes, operation, claim,
	)
	return true
}

func (s *Server) waitDetachedAgentKernelExecution(
	ctx context.Context,
	access workspace.KernelFrameAccess,
	spec kernelruntime.SessionSpec,
	backendSession kernelruntime.BackendSessionRef,
	started kernelruntime.ExecutionStarted,
	ref kernelruntime.BackendExecutionRef,
	lease kernelruntime.BackendControlLease,
	outputLimitBytes int64,
	operation *workspace.KernelLocalOperation,
	claim *transcriptstore.RunnerClaim,
) (map[string]any, error) {
	poll := time.NewTicker(250 * time.Millisecond)
	defer poll.Stop()
	renewal := time.NewTicker(detachedKernelControlRenewal)
	defer renewal.Stop()
	publishedStart := false
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-renewal.C:
			var err error
			lease, err = s.kernelExecutionBackend.RenewControl(ctx, lease, detachedKernelControlTTL)
			if err != nil {
				return nil, err
			}
		case <-poll.C:
			snapshot, err := s.kernelExecutionBackend.Probe(ctx, ref, lease)
			if err != nil {
				return nil, err
			}
			if !publishedStart && snapshot.StartedAt != nil {
				started.StartedAt = snapshot.StartedAt.UTC()
				s.publishAgentKernelEvent(access, started, "start", map[string]any{
					"source": started.Code, "started_at": started.StartedAt.Format(time.RFC3339Nano),
				})
				publishedStart = true
			}
			execution, found, getErr := s.workspaceStore.GetDetachedKernelExecution(ctx, ref.ExecutionID)
			if getErr != nil || !found {
				if getErr != nil {
					return nil, getErr
				}
				return nil, errors.New("detached kernel execution disappeared")
			}
			if execution.TerminalReceiptID == "" {
				continue
			}
			visible, settleErr := s.settleDetachedAgentKernelExecution(
				ctx, access, spec, backendSession, started, execution,
				outputLimitBytes, operation, claim,
			)
			if settleErr != nil {
				return nil, &detachedKernelTerminalSettlementError{
					execution: execution,
					cause:     settleErr,
				}
			}
			return visible, nil
		}
	}
}

func (s *Server) settleDetachedAgentKernelExecution(
	ctx context.Context,
	access workspace.KernelFrameAccess,
	spec kernelruntime.SessionSpec,
	backendSession kernelruntime.BackendSessionRef,
	started kernelruntime.ExecutionStarted,
	execution workspace.DetachedKernelExecution,
	outputLimitBytes int64,
	operation *workspace.KernelLocalOperation,
	claim *transcriptstore.RunnerClaim,
) (map[string]any, error) {
	receipt, found, err := s.workspaceStore.GetKernelExecutionResultReceipt(ctx, execution.TerminalReceiptID)
	if err != nil || !found {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("detached kernel result receipt is unavailable")
	}
	result, cleanupPath, err := kerneldetached.LoadExecutionResult(
		filepath.Join(s.fileRoot, "workspace", "kernel-result-spool"), receipt,
	)
	if err != nil {
		return nil, err
	}
	var files []kernelruntime.FileWrite
	var dropped []string
	if err := json.Unmarshal([]byte(receipt.FilesWrittenJSON), &files); err != nil {
		return nil, errors.New("detached kernel file result is invalid")
	}
	if err := json.Unmarshal([]byte(receipt.DroppedRootsJSON), &dropped); err != nil {
		return nil, errors.New("detached kernel dropped-root result is invalid")
	}
	var outcomeErr error
	if result.Error != "" {
		outcomeErr = errors.New(result.Error)
	}
	outcome := kernelruntime.ExecutionOutcome{
		Response: result.Response, Err: outcomeErr, TimedOut: result.TimedOut,
		FilesWritten: files, DroppedRoots: dropped, CellIndex: result.CellIndex,
		StartedAt: result.StartedAt, FinishedAt: result.FinishedAt, Generation: result.Generation,
	}
	started.StartedAt = result.StartedAt
	if operation != nil && strings.EqualFold(strings.TrimSpace(operation.Tool), softwareRuntimeToolName) {
		if err := s.closeDetachedTaskOwnedSession(ctx, backendSession); err != nil {
			return nil, fmt.Errorf("close task-owned software runtime session: %w", err)
		}
	}
	session := kernelruntime.EnsuredSession{ID: backendSession.KernelID, Reused: result.Reused}
	visible, err := s.finishAgentKernelExecution(
		context.Background(), access, spec, session, started, outcome,
		outputLimitBytes, operation, claim, &receipt, nil,
	)
	if err != nil {
		return nil, err
	}
	if operation != nil {
		wake, wakeDecisionErr := s.detachedSettlementRequiresResumeWake(ctx, claim)
		if wakeDecisionErr != nil {
			return nil, fmt.Errorf("resolve detached kernel resume ownership: %w", wakeDecisionErr)
		}
		if wake {
			wakeCtx, cancelWake := context.WithTimeout(context.Background(), 2*time.Second)
			wakeErr := s.wakeFrameResumeDispatchAfterKernelTransition(wakeCtx, *operation)
			cancelWake()
			if wakeErr != nil {
				return nil, fmt.Errorf("wake frame after detached kernel settlement: %w", wakeErr)
			}
		}
	}
	if cleanupPath != "" {
		clean := filepath.Clean(cleanupPath)
		root := filepath.Clean(filepath.Join(s.fileRoot, "workspace", "kernel-result-spool"))
		if filepath.Dir(clean) != root {
			return nil, errors.New("detached kernel result cleanup path is invalid")
		}
		if err := os.Remove(clean); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("remove settled detached kernel result: %w", err)
		}
	}
	return visible, nil
}

func (s *Server) detachedSettlementRequiresResumeWake(
	ctx context.Context,
	claim *transcriptstore.RunnerClaim,
) (bool, error) {
	if claim == nil {
		return true, nil
	}
	err := s.validateLiveTranscriptRunnerClaim(ctx, *claim)
	if err == nil {
		return false, nil
	}
	if errors.Is(err, transcriptstore.ErrClaimStale) {
		return true, nil
	}
	return false, err
}

// closeDetachedTaskOwnedSession is the terminal cleanup gate for one-shot
// software runtime sessions. The durable backend state makes this idempotent:
// a settlement retry observes an already-stopped executor as success instead
// of launching work again. Persistent Python/R/REPL sessions never call it.
func (s *Server) closeDetachedTaskOwnedSession(
	ctx context.Context,
	backendSession kernelruntime.BackendSessionRef,
) error {
	if s == nil || s.workspaceStore == nil || s.kernelExecutionBackend == nil {
		return errors.New("detached kernel cleanup authority is unavailable")
	}
	backend, found, err := s.workspaceStore.GetKernelExecutionBackend(ctx, backendSession.BackendID)
	if err != nil {
		return err
	}
	if !found || backend.BackendGeneration != backendSession.BackendGeneration {
		return workspace.ErrKernelExecutionBackendStale
	}
	if backend.State == workspace.KernelExecutionBackendStateStopped {
		return nil
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	lease, err := s.kernelExecutionBackend.AcquireSessionControl(
		cleanupCtx, backendSession, detachedKernelControlTTL,
	)
	if err != nil {
		return err
	}
	return s.kernelExecutionBackend.CloseSession(cleanupCtx, backendSession, lease)
}

func detachedKernelRequestFromExecution(execution workspace.DetachedKernelExecution) (workspace.KernelDetachedExecutionRequestV1, error) {
	request, err := workspace.DecodeKernelDetachedExecutionRequestV1(execution.RequestJSON)
	if err != nil || strings.TrimSpace(request.ExecutionID) != execution.ExecutionID {
		return workspace.KernelDetachedExecutionRequestV1{}, errors.New("detached kernel request conflicts with durable state")
	}
	return request, nil
}
