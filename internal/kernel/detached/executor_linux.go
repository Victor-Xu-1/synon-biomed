//go:build linux

package detached

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	kernelruntime "synon-go/internal/kernel"
	"synon-go/internal/kernelcontract"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/software/localcontainer"
	"synon-go/internal/sqliteutil"
)

const (
	defaultExecutorHeartbeatInterval  = 10 * time.Second
	defaultExecutorSocketReadyTimeout = 5 * time.Second
	defaultExecutorIdleTimeout        = 15 * time.Minute
	defaultExecutorDrainTimeout       = 30 * time.Second
	defaultExecutorDrainPollInterval  = 100 * time.Millisecond
	maxOutcomeCommitAttempts          = 8
	minHeartbeatContentionRetryDelay  = 100 * time.Millisecond
	maxHeartbeatContentionRetryDelay  = 5 * time.Second
)

// Executor owns one physical kernel session independently of the Web service.
// SQLite remains the sole dispatch and terminal authority; the Unix socket is
// only a fenced control plane and never carries executable payloads.
type Executor struct {
	cancellationMu     sync.Mutex
	Store              *workspace.Store
	Manager            *kernelruntime.Manager
	CondaHome          string
	BackendID          string
	BackendGeneration  int64
	ExecutorInstanceID string
	SocketPath         string
	ResultSpoolDir     string
	HeartbeatInterval  time.Duration
	IdleTimeout        time.Duration

	mu       sync.Mutex
	activeWG sync.WaitGroup
	session  kernelruntime.EnsuredSession
	// workerGeneration is process-local Manager authority. It intentionally
	// differs from the durable logical kernel generation after an executor or
	// controller restart; the latter remains in SQLite request/fence records.
	workerGeneration uint64
	active           map[string]*activeExecution
	lifetimeDone     <-chan struct{}
	workspaceDir     string
	shutdown         chan struct{}
	fatalErr         chan error
	lastActivity     time.Time
	draining         bool
	memoryDomain     *executorMemoryDomain
	once             sync.Once
}

type activeExecution struct {
	kernel          *kernelruntime.ExecutionHandle
	cancelContainer context.CancelFunc
}

func (e *Executor) Run(ctx context.Context) (runReturnErr error) {
	if ctx == nil || e == nil || e.Store == nil || e.Manager == nil {
		return errors.New("detached kernel executor configuration is incomplete")
	}
	e.BackendID = strings.TrimSpace(e.BackendID)
	e.ExecutorInstanceID = strings.TrimSpace(e.ExecutorInstanceID)
	e.SocketPath = strings.TrimSpace(e.SocketPath)
	if e.BackendID == "" || e.BackendGeneration <= 0 || e.ExecutorInstanceID == "" || e.SocketPath == "" {
		return errors.New("detached kernel executor identity is incomplete")
	}
	if e.HeartbeatInterval <= 0 {
		e.HeartbeatInterval = defaultExecutorHeartbeatInterval
	}
	if e.HeartbeatInterval < time.Second || e.HeartbeatInterval > time.Minute {
		return errors.New("detached kernel executor heartbeat interval is invalid")
	}
	if e.IdleTimeout <= 0 {
		e.IdleTimeout = defaultExecutorIdleTimeout
	}
	if e.IdleTimeout < time.Second || e.IdleTimeout > 24*time.Hour {
		return errors.New("detached kernel executor idle timeout is invalid")
	}
	backend, found, err := e.Store.GetKernelExecutionBackend(ctx, e.BackendID)
	if err != nil || !found {
		if err != nil {
			return err
		}
		return errors.New("detached kernel backend is unavailable")
	}
	if backend.BackendGeneration != e.BackendGeneration ||
		backend.ExecutorInstanceID != e.ExecutorInstanceID || backend.SocketPath != e.SocketPath ||
		backend.State != workspace.KernelExecutionBackendStateStarting {
		return workspace.ErrKernelExecutionBackendStale
	}
	if err := e.ClaimStartup(ctx); err != nil {
		return err
	}
	startupStage := "session_spec"
	activated := false
	defer func() {
		if !activated && runReturnErr != nil {
			runReturnErr = e.RecordStartupFailure(startupStage, runReturnErr)
		}
	}()
	spec, err := workspace.DecodeKernelExecutionSessionSpecV1(backend.SessionSpecJSON)
	if err != nil {
		return err
	}
	startupStage = "resource_domain"
	e.memoryDomain, err = prepareExecutorMemoryDomain(e.BackendID, e.BackendGeneration)
	if err != nil {
		return err
	}
	if e.memoryDomain != nil {
		defer e.memoryDomain.root.Close()
		if err = e.Manager.ConfigureWorkerResourceDomain(e.memoryDomain.directory); err != nil {
			return err
		}
	}
	startupStage = "worker_start"
	session, err := e.Manager.EnsureSession(kernelSessionSpec(spec))
	if err != nil {
		return fmt.Errorf("ensure detached kernel session: %w", err)
	}
	startupStage = "worker_identity"
	workerGeneration, err := detachedExecutorWorkerGeneration(
		backend.KernelID, backend.KernelGeneration, session.ID, session.Worker.Generation(),
	)
	if err != nil {
		_ = e.Manager.CloseKernel(context.Background(), session.ID)
		return err
	}
	workerIdentity, err := session.Worker.ProcessIdentity()
	if err != nil {
		_ = e.Manager.CloseKernel(context.Background(), session.ID)
		return err
	}
	executorStartTicks, err := kernelruntime.CurrentProcessStartTicks()
	if err != nil {
		_ = e.Manager.CloseKernel(context.Background(), session.ID)
		return err
	}
	cgroupPath, err := currentUnifiedCgroupPath()
	if err != nil {
		_ = e.Manager.CloseKernel(context.Background(), session.ID)
		return err
	}
	e.mu.Lock()
	e.session = session
	e.workerGeneration = workerGeneration
	e.active = map[string]*activeExecution{}
	e.workspaceDir = filepath.Clean(spec.WorkspaceDir)
	e.shutdown = make(chan struct{})
	e.fatalErr = make(chan error, 1)
	e.lastActivity = time.Now().UTC()
	e.draining = false
	e.mu.Unlock()

	// The supervisor context owns the physical executor lifetime, but a caller
	// context ending is a handoff signal, not permission to kill accepted work.
	// Run observes ctx below, fences new admission, and drains durable receipts
	// before it cancels this independent control context.
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.mu.Lock()
	e.lifetimeDone = runCtx.Done()
	e.mu.Unlock()
	startupStage = "control_socket"
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- (Server{SocketPath: e.SocketPath, Handler: e, MaxConcurrent: 32, IdleTimeout: 30 * time.Second}).Serve(runCtx)
	}()
	if err := waitForTrustedSocket(runCtx, e.SocketPath, defaultExecutorSocketReadyTimeout, serverErr); err != nil {
		_ = e.Manager.CloseKernel(context.Background(), session.ID)
		return fmt.Errorf("start detached kernel control server: %w", err)
	}
	startupStage = "activation"
	backend, err = e.Store.ActivateKernelExecutionBackend(runCtx, workspace.ActivateKernelExecutionBackendInput{
		BackendID: e.BackendID, BackendGeneration: e.BackendGeneration,
		ExecutorInstanceID: e.ExecutorInstanceID, ExecutorPID: int64(os.Getpid()),
		ExecutorPIDStartTicks: executorStartTicks, WorkerPID: int64(workerIdentity.PID),
		WorkerPIDStartTicks: workerIdentity.StartTicks, WorkerPGID: int64(workerIdentity.PGID),
		CgroupPath: cgroupPath, HeartbeatSequence: 1,
	})
	if err != nil {
		_ = e.Manager.CloseKernel(context.Background(), session.ID)
		return err
	}
	activated = true
	heartbeatErr := make(chan error, 1)
	go func() { heartbeatErr <- e.runHeartbeat(runCtx, backend.HeartbeatSequence) }()
	cancellationErr := make(chan error, 1)
	go func() { cancellationErr <- e.runCancellationReconciler(runCtx) }()
	pressureDone := make(chan struct{})
	go func() { defer close(pressureDone); e.runMemoryPressureSupervisor(runCtx) }()
	go func() {
		if err := e.runIdleReconciler(runCtx); err != nil {
			e.reportFatal(err)
		}
	}()

	var runErr error
	serverFinished := false
	settlementFailed := false
	select {
	case <-ctx.Done():
		runErr = context.Cause(ctx)
	case <-e.shutdown:
	case <-session.Worker.Stopped():
		runErr = session.Worker.TerminalError()
	case err := <-serverErr:
		runErr = err
		serverFinished = true
	case err := <-heartbeatErr:
		runErr = err
	case err := <-cancellationErr:
		runErr = err
	case err := <-e.fatalErr:
		runErr = err
		settlementFailed = true
	}
	// Withdraw new dispatch before draining outstanding receipts. A surviving
	// control socket is not evidence that its physical worker is reusable, but
	// already accepted work remains owned until its durable receipt settles.
	e.beginDraining()
	drainCtx, drainCancel := context.WithTimeout(context.Background(), defaultExecutorDrainTimeout)
	drainErr := e.waitForDurableDrain(drainCtx)
	drainCancel()
	if drainErr != nil {
		settlementFailed = true
		runErr = errors.Join(runErr, drainErr)
	}
	e.requestShutdown()
	cancel()
	<-pressureDone
	if !serverFinished {
		select {
		case err := <-serverErr:
			runErr = errors.Join(runErr, err)
		case <-time.After(5 * time.Second):
			runErr = errors.Join(runErr, errors.New("detached kernel control server did not stop"))
		}
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer closeCancel()
	closeErr := e.Manager.CloseKernel(closeCtx, session.ID)
	settled := make(chan struct{})
	go func() {
		e.activeWG.Wait()
		close(settled)
	}()
	evidenceLost := settlementFailed || closeErr != nil
	select {
	case <-settled:
	case <-time.After(10 * time.Second):
		evidenceLost = true
	}
	_, finishErr := e.Store.FinishKernelExecutionBackend(context.Background(), workspace.FinishKernelExecutionBackendInput{
		BackendID: e.BackendID, BackendGeneration: e.BackendGeneration,
		ExecutorInstanceID: e.ExecutorInstanceID, EvidenceLost: evidenceLost,
	})
	if errors.Is(runErr, context.Canceled) {
		runErr = nil
	}
	return errors.Join(runErr, closeErr, finishErr)
}

func (e *Executor) HandleDetachedKernelCommand(ctx context.Context, request CommandRequest) CommandResponse {
	if e == nil || e.Store == nil || e.Manager == nil || request.BackendID != e.BackendID ||
		request.BackendGeneration != e.BackendGeneration {
		return commandResponseError(request.RequestID, "stale_authority", "detached kernel command authority is stale")
	}
	if err := e.validateController(ctx, request); err != nil {
		return commandResponseError(request.RequestID, "stale_authority", "detached kernel command authority is stale")
	}
	e.mu.Lock()
	e.lastActivity = time.Now().UTC()
	e.mu.Unlock()
	switch request.Command {
	case CommandProbe:
		execution, found, err := e.Store.GetDetachedKernelExecution(ctx, request.ExecutionID)
		if err != nil || !found || execution.BackendID != e.BackendID || execution.BackendGeneration != e.BackendGeneration {
			return commandResponseError(request.RequestID, "not_found", "detached kernel execution is unavailable")
		}
		response := commandResponseOK(request.RequestID)
		snapshot := detachedExecutionSnapshot(execution)
		response.Snapshot = &snapshot
		return response
	case CommandDispatch:
		// Durable acceptance is the admission fence. An accepted execution can
		// still be dispatched while this generation drains because the state
		// transition to draining is serialized with acceptance in SQLite; no new
		// accepted execution can appear after that transition. This closes the
		// service-handoff window without allowing a second operation to enter.
		return e.dispatch(ctx, request)
	case CommandCancel:
		return e.cancelExecution(ctx, request)
	case CommandClose:
		e.beginDraining()
		return commandResponseOK(request.RequestID)
	default:
		return commandResponseError(request.RequestID, "unsupported", "detached kernel command is unsupported")
	}
}

// AfterDetachedKernelCommand is called after the close acknowledgement is on
// the wire. Shutting down from the handler itself can close the Unix socket
// before the controller receives its successful response, turning a clean
// handoff into a spurious context deadline.
func (e *Executor) AfterDetachedKernelCommand(_ context.Context, request CommandRequest, response CommandResponse) {
	if request.Command == CommandClose && response.OK {
		e.requestShutdownIfDrained(context.Background())
	}
}

func (e *Executor) dispatch(ctx context.Context, request CommandRequest) CommandResponse {
	execution, err := e.Store.MarkKernelExecutionRequestWritten(ctx, workspace.MarkKernelExecutionRequestWrittenInput{
		KernelDetachedExecutionControlInput: workspace.KernelDetachedExecutionControlInput{
			ExecutionID: request.ExecutionID, BackendGeneration: request.BackendGeneration,
			ControllerEpoch: request.ControllerEpoch, ControllerToken: request.ControllerToken,
			ExpectedVersion: request.ExpectedVersion,
		},
		DispatchSequence: request.DispatchSequence,
	})
	if err != nil {
		return commandResponseError(request.RequestID, "dispatch_conflict", "detached kernel dispatch conflicts with durable state")
	}
	durableRequest, err := workspace.DecodeKernelDetachedExecutionRequestV1(execution.RequestJSON)
	if err != nil || durableRequest.ExecutionID != request.ExecutionID || durableRequest.HostCallMethods != nil && len(durableRequest.HostCallMethods) > 0 {
		return commandResponseError(request.RequestID, "request_invalid", "detached kernel execution request is unavailable")
	}
	e.mu.Lock()
	if _, exists := e.active[request.ExecutionID]; exists {
		e.mu.Unlock()
		return e.dispatchResponse(request, execution)
	}
	if localcontainer.IsEnvironmentName(durableRequest.Environment) {
		e.mu.Unlock()
		if err := e.dispatchContainerExecution(ctx, durableRequest, execution); err != nil {
			log.Printf("submit detached container execution %s: %v", durableRequest.ExecutionID, err)
			if commitErr := e.commitSubmitFailure(durableRequest, err); commitErr != nil {
				e.reportFatal(commitErr)
			}
			return commandResponseError(request.RequestID, "submit_failed", "detached container execution could not start")
		}
		return e.dispatchResponse(request, execution)
	}
	handle, submitErr := e.Manager.Submit(kernelruntime.SubmitRequest{
		KernelID: durableRequest.KernelID, ExpectedGeneration: e.workerGeneration,
		OwnerID: durableRequest.OwnerUserID, ProjectID: durableRequest.ProjectID,
		FrameID: durableRequest.FrameID, FrameIncarnationID: durableRequest.FrameIncarnationID,
		RootFrameIncarnationID: durableRequest.RootFrameIncarnationID,
		KernelKind:             durableRequest.KernelKind, Language: durableRequest.Language,
		Environment: durableRequest.Environment, ExecID: durableRequest.ExecutionID,
		ToolUseID: durableRequest.ToolCallID, ToolName: durableRequest.ToolName,
		Code: durableRequest.Code, WorkingDir: durableRequest.WorkingDir,
		Background: durableRequest.Background, Fresh: durableRequest.Fresh, Origin: durableRequest.Origin,
		Timeout: detachedExecutionTimeout(durableRequest.TimeoutMillis),
	})
	if submitErr == nil {
		e.active[request.ExecutionID] = &activeExecution{kernel: handle}
		e.activeWG.Add(1)
	}
	e.mu.Unlock()
	if submitErr != nil {
		log.Printf("submit detached kernel execution %s: %v", durableRequest.ExecutionID, submitErr)
		if workerErr := e.session.Worker.TerminalError(); workerErr != nil {
			log.Printf("detached kernel worker stopped before execution %s: %v", durableRequest.ExecutionID, workerErr)
			e.beginDraining()
		}
		if err := e.commitSubmitFailure(durableRequest, submitErr); err != nil {
			e.reportFatal(err)
		}
		return commandResponseError(request.RequestID, "submit_failed", "detached kernel execution could not start")
	}
	go e.observeExecution(durableRequest, handle)
	return e.dispatchResponse(request, execution)
}

func detachedExecutorWorkerGeneration(
	durableKernelID string,
	durableKernelGeneration int64,
	processKernelID string,
	processWorkerGeneration uint64,
) (uint64, error) {
	if strings.TrimSpace(durableKernelID) == "" || durableKernelGeneration <= 0 ||
		processKernelID != durableKernelID || processWorkerGeneration == 0 {
		return 0, workspace.ErrKernelExecutionBackendConflict
	}
	return processWorkerGeneration, nil
}

func detachedExecutionTimeout(milliseconds int64) time.Duration {
	if milliseconds <= 0 {
		return 0
	}
	return time.Duration(milliseconds) * time.Millisecond
}

func (e *Executor) dispatchResponse(request CommandRequest, execution workspace.DetachedKernelExecution) CommandResponse {
	response := commandResponseOK(request.RequestID)
	committed := execution.AcceptedAt
	if execution.DispatchCommittedAt != nil {
		committed = *execution.DispatchCommittedAt
	}
	response.Dispatch = &kernelruntime.BackendDispatchReceipt{
		ExecutionID: execution.ExecutionID, DispatchSequence: execution.DispatchSequence,
		StateVersion: execution.StateVersion, DispatchCommittedAt: committed,
	}
	return response
}

func (e *Executor) observeExecution(request workspace.KernelDetachedExecutionRequestV1, handle *kernelruntime.ExecutionHandle) {
	defer e.activeWG.Done()
	defer func() {
		e.mu.Lock()
		delete(e.active, request.ExecutionID)
		e.lastActivity = time.Now().UTC()
		e.mu.Unlock()
		// A cancellation reconciler may have published draining while this
		// observer was settling. Re-check after removing the last local handle;
		// checking before deletion would leave a drained executor alive forever.
		e.requestShutdownIfDrained(context.Background())
	}()
	startedAt := time.Now().UTC()
	started, startedOK := <-handle.Started()
	if startedOK && started.ExecID != "" {
		startedAt = started.StartedAt.UTC()
		if execution, found, err := e.Store.GetDetachedKernelExecution(context.Background(), request.ExecutionID); err == nil && found {
			_, _ = e.Store.MarkKernelExecutionStarted(context.Background(), workspace.MarkKernelExecutionStartedInput{
				ExecutionID: request.ExecutionID, BackendGeneration: e.BackendGeneration,
				ExecutorInstanceID: e.ExecutorInstanceID, ExpectedVersion: execution.StateVersion,
				ObservationSequence: execution.LastObservationSequence + 1,
			})
		}
	}
	outcome, ok := <-handle.Done()
	if !ok {
		outcome = kernelruntime.ExecutionOutcome{Err: errors.New("kernel execution closed without outcome"), StartedAt: startedAt, FinishedAt: time.Now().UTC()}
	}
	if outcome.StartedAt.IsZero() {
		outcome.StartedAt = startedAt
	}
	if outcome.FinishedAt.IsZero() || outcome.FinishedAt.Before(outcome.StartedAt) {
		outcome.FinishedAt = time.Now().UTC()
	}
	if err := e.persistOutcome(request, outcome, startedAt); err != nil {
		e.reportFatal(err)
		return
	}
	// Background handles remain registered until their durable receipt commits.
	// Release that identity only after persistence so a long task can execute
	// many background units without accumulating stale manager state.
	handle.AcknowledgePersistence()
}

func (e *Executor) dispatchContainerExecution(
	ctx context.Context,
	request workspace.KernelDetachedExecutionRequestV1,
	execution workspace.DetachedKernelExecution,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	draining := e.draining
	e.mu.Unlock()
	// A request can cross the drain boundary only after the durable admission
	// transaction has written its dispatch request. New work cannot be accepted
	// once the backend is draining, while already-accepted work must still be
	// allowed to reach its provider and settle its receipt.
	if draining && execution.RequestWrittenAt == nil {
		return errors.New("kernel executor is draining")
	}
	if request.ToolName != kernelcontract.PythonTool && request.ToolName != kernelcontract.BashTool {
		return errors.New("container-backed execution requires Python or Bash authority")
	}
	manager, err := localcontainer.New(e.Manager, e.CondaHome)
	if err != nil {
		return err
	}
	environment, found, err := manager.Resolve(ctx, request.Environment)
	if err != nil {
		return err
	}
	if !found || environment.Status != "ready" {
		return errors.New("container environment is unavailable")
	}
	source := request.Code
	if request.ToolName == kernelcontract.BashTool {
		source, err = kernelcontract.BashCommandFromWrapper(request.Code)
		if err != nil {
			return fmt.Errorf("validate container Bash authority: %w", err)
		}
	}
	executionCtx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	if e.draining {
		e.mu.Unlock()
		cancel()
		return errors.New("kernel executor is draining")
	}
	if _, exists := e.active[request.ExecutionID]; exists {
		e.mu.Unlock()
		cancel()
		return nil
	}
	lifetimeDone := e.lifetimeDone
	if lifetimeDone == nil {
		lifetimeDone = make(chan struct{})
	}
	e.active[request.ExecutionID] = &activeExecution{cancelContainer: cancel}
	e.activeWG.Add(1)
	e.mu.Unlock()

	if execution, current, storeErr := e.Store.GetDetachedKernelExecution(context.Background(), request.ExecutionID); storeErr == nil && current {
		if _, markErr := e.Store.MarkKernelExecutionStarted(context.Background(), workspace.MarkKernelExecutionStartedInput{
			ExecutionID: request.ExecutionID, BackendGeneration: e.BackendGeneration,
			ExecutorInstanceID: e.ExecutorInstanceID, ExpectedVersion: execution.StateVersion,
			ObservationSequence: execution.LastObservationSequence + 1,
		}); markErr != nil {
			e.mu.Lock()
			delete(e.active, request.ExecutionID)
			e.mu.Unlock()
			e.activeWG.Done()
			cancel()
			return markErr
		}
	}
	go e.observeContainerExecution(executionCtx, lifetimeDone, request, source, manager, environment)
	return nil
}

func (e *Executor) observeContainerExecution(
	executionCtx context.Context,
	lifetimeDone <-chan struct{},
	request workspace.KernelDetachedExecutionRequestV1,
	source string,
	manager *localcontainer.Manager,
	environment localcontainer.Environment,
) {
	defer e.activeWG.Done()
	defer func() {
		e.mu.Lock()
		delete(e.active, request.ExecutionID)
		e.lastActivity = time.Now().UTC()
		e.mu.Unlock()
		e.requestShutdownIfDrained(context.Background())
	}()
	startedAt := time.Now().UTC()
	tracker := kernelruntime.NewWorkspaceChangeTracker(e.workspaceDir, request.WorkingDir)
	result, err := manager.RunExecution(executionCtx, lifetimeDone, environment, localcontainer.ExecutionRequest{
		ExecutionID: request.ExecutionID, ToolName: request.ToolName, Code: source,
		Workspace: e.workspaceDir, WorkingDir: request.WorkingDir,
	})
	if errors.Is(err, localcontainer.ErrExecutorDetached) {
		// The Docker daemon owns the still-running container. Leave the durable
		// execution unsettled so the successor executor resumes the exact identity
		// instead of manufacturing a terminal failure during service shutdown.
		return
	}
	if errors.Is(err, localcontainer.ErrExecutionOutcomeUnconfirmed) {
		// Keep the durable request/ack and original identity for the existing
		// recovery owner. Neither cancellation nor process failure is proven.
		e.reportFatal(err)
		return
	}
	filesWritten, droppedRoots := tracker.Finish()
	finishedAt := result.FinishedAt
	if finishedAt.IsZero() || finishedAt.Before(startedAt) {
		finishedAt = time.Now().UTC()
	}
	if !result.StartedAt.IsZero() {
		startedAt = result.StartedAt
	}
	outcome := kernelruntime.ExecutionOutcome{
		Response: kernelruntime.Response{
			ID: request.ExecutionID, Stdout: result.Stdout, Stderr: result.Stderr,
			Interrupted: result.Interrupted,
			Trace: map[string]any{
				"provider": localcontainer.ProviderID, "container_id": result.ContainerID,
				"container_name": result.ContainerName, "image_id": result.ImageID, "resumed": result.Resumed,
			},
		},
		Err: err, FilesWritten: filesWritten, DroppedRoots: droppedRoots,
		StartedAt: startedAt, FinishedAt: finishedAt, Generation: e.workerGeneration,
	}
	outcome.Response.Stderr = containerExecutionProtocolStderr(request.ToolName, outcome.Response.Stderr, result.ExitCode)
	if err == nil && result.ExitCode != 0 && !result.Interrupted {
		outcome.Response.Error = fmt.Sprintf("container process exited with status %d", result.ExitCode)
	}
	if persistErr := e.persistOutcome(request, outcome, startedAt); persistErr != nil {
		e.reportFatal(persistErr)
	}
}

func containerExecutionProtocolStderr(toolName, stderr string, exitCode int) string {
	if toolName != kernelcontract.BashTool {
		return stderr
	}
	return stderr + "\n" + kernelcontract.BashExitPrefix + strconv.Itoa(exitCode) + "\n"
}

func (e *Executor) commitSubmitFailure(request workspace.KernelDetachedExecutionRequestV1, submitErr error) error {
	now := time.Now().UTC()
	return e.persistOutcome(request, kernelruntime.ExecutionOutcome{
		Err: submitErr, StartedAt: now, FinishedAt: now,
	}, now)
}

func (e *Executor) persistOutcome(
	request workspace.KernelDetachedExecutionRequestV1,
	outcome kernelruntime.ExecutionOutcome,
	startedAt time.Time,
) error {
	var lastErr error
	for attempt := 1; attempt <= maxOutcomeCommitAttempts; attempt++ {
		// Completion cannot overtake an in-flight provider cancellation before
		// its durable acknowledgement is visible to terminal classification.
		e.cancellationMu.Lock()
		err := e.commitOutcome(request, outcome, startedAt)
		e.cancellationMu.Unlock()
		if err == nil {
			return nil
		} else {
			lastErr = err
			log.Printf("persist detached kernel result %s attempt %d: %v", request.ExecutionID, attempt, err)
		}
		if attempt < maxOutcomeCommitAttempts {
			time.Sleep(outcomeCommitRetryDelay(attempt))
		}
	}
	return fmt.Errorf("persist detached kernel result %s after %d attempts: %w",
		request.ExecutionID, maxOutcomeCommitAttempts, lastErr)
}

func outcomeCommitRetryDelay(attempt int) time.Duration {
	delay := 250 * time.Millisecond
	for step := 1; step < attempt && delay < 4*time.Second; step++ {
		delay *= 2
	}
	if delay > 4*time.Second {
		return 4 * time.Second
	}
	return delay
}

func (e *Executor) reportFatal(err error) {
	if err == nil || e == nil || e.fatalErr == nil {
		return
	}
	select {
	case e.fatalErr <- err:
	default:
	}
}

func (e *Executor) commitOutcome(
	request workspace.KernelDetachedExecutionRequestV1,
	outcome kernelruntime.ExecutionOutcome,
	startedAt time.Time,
) error {
	execution, found, err := e.Store.GetDetachedKernelExecution(context.Background(), request.ExecutionID)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("detached kernel execution disappeared before terminal receipt")
	}
	if execution.TerminalReceiptID != "" {
		return nil
	}
	cancelledByAuthority, terminationSignal := detachedCancellationOutcome(execution)
	if cancelledByAuthority {
		// The durable request and executor acknowledgement are the terminal
		// classification authority. A process-group SIGINT may end the worker
		// before its protocol response reports Interrupted; that is still a
		// successful cancellation, not an execution failure.
		outcome.Response.Interrupted = true
	}
	result := ExecutionResultV1{
		Version: 1, ExecutionID: request.ExecutionID, ToolCallID: request.ToolCallID,
		KernelID: request.KernelID, KernelKind: request.KernelKind, Language: request.Language,
		Environment: request.Environment, Reused: e.session.Reused || outcome.CellIndex > 1, Response: outcome.Response,
		TimedOut:  outcome.TimedOut,
		CellIndex: outcome.CellIndex, StartedAt: startedAt.UTC(), FinishedAt: outcome.FinishedAt.UTC(),
		Generation: outcome.Generation,
	}
	if outcome.Err != nil && !cancelledByAuthority {
		result.Error = detachedExecutionError(outcome.Err)
	}
	resultJSON, resultRef, err := e.materializeExecutorResult(result)
	if err != nil {
		return err
	}
	filesWritten := outcome.FilesWritten
	if filesWritten == nil {
		filesWritten = []kernelruntime.FileWrite{}
	}
	droppedRoots := outcome.DroppedRoots
	if droppedRoots == nil {
		droppedRoots = []string{}
	}
	filesJSON, filesErr := json.Marshal(filesWritten)
	droppedJSON, droppedErr := json.Marshal(droppedRoots)
	if filesErr != nil || droppedErr != nil {
		return errors.New("encode detached kernel result file lists")
	}
	digest := sha256.Sum256(resultJSON)
	outcomeState := workspace.KernelExecutionResultCompleted
	if outcome.Response.Interrupted || outcome.TimedOut {
		outcomeState = workspace.KernelExecutionResultCancelled
	} else if outcome.Err != nil || outcome.Response.Error != "" {
		outcomeState = workspace.KernelExecutionResultFailed
	}
	_, _, err = e.Store.CommitKernelExecutionResult(context.Background(), workspace.CommitKernelExecutionResultInput{
		ReceiptID: "kernel-receipt-" + uuid.NewString(), ExecutionID: request.ExecutionID,
		BackendGeneration: e.BackendGeneration, ExecutorInstanceID: e.ExecutorInstanceID,
		TerminalSequence: execution.LastObservationSequence + 1, Outcome: outcomeState,
		ResultJSON: string(resultJSON), ResultRef: resultRef, ResultSHA256: hex.EncodeToString(digest[:]),
		Interrupted: outcome.Response.Interrupted, TimedOut: outcome.TimedOut,
		TerminationSignal: terminationSignal,
		StartedAt:         startedAt.UTC(), FinishedAt: outcome.FinishedAt.UTC(),
		FilesWrittenJSON: string(filesJSON), DroppedRootsJSON: string(droppedJSON),
	})
	return err
}

func detachedExecutionError(err error) string {
	const generic = "kernel execution failed"
	if err == nil {
		return ""
	}
	detail := strings.Join(strings.Fields(err.Error()), " ")
	if detail == "" {
		return generic
	}
	runes := []rune(detail)
	if len(runes) > 512 {
		detail = string(runes[:512]) + "…"
	}
	return generic + ": " + detail
}

func (e *Executor) materializeExecutorResult(result ExecutionResultV1) ([]byte, string, error) {
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, "", errors.New("encode detached kernel result")
	}
	if len(encoded) <= 1048576 {
		return encoded, "", nil
	}
	spoolDir := filepath.Clean(strings.TrimSpace(e.ResultSpoolDir))
	if !filepath.IsAbs(spoolDir) {
		return nil, "", errors.New("detached kernel result spool is unavailable")
	}
	if err := os.MkdirAll(spoolDir, 0o700); err != nil {
		return nil, "", errors.New("prepare detached kernel result spool")
	}
	if info, err := os.Stat(spoolDir); err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, "", errors.New("detached kernel result spool is not private")
	}
	fullDigest := sha256.Sum256(encoded)
	fullSHA := hex.EncodeToString(fullDigest[:])
	finalPath := filepath.Join(spoolDir, fullSHA+".json")
	if existing, err := os.ReadFile(finalPath); err == nil {
		existingDigest := sha256.Sum256(existing)
		if subtle.ConstantTimeCompare(existingDigest[:], fullDigest[:]) != 1 {
			return nil, "", errors.New("detached kernel result spool conflicts with existing content")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, "", errors.New("inspect detached kernel result spool")
	} else {
		temporary, createErr := os.CreateTemp(spoolDir, ".kernel-result-*.tmp")
		if createErr != nil {
			return nil, "", errors.New("create detached kernel result spool")
		}
		temporaryPath := temporary.Name()
		committed := false
		defer func() {
			_ = temporary.Close()
			if !committed {
				_ = os.Remove(temporaryPath)
			}
		}()
		if chmodErr := temporary.Chmod(0o600); chmodErr != nil {
			return nil, "", errors.New("secure detached kernel result spool")
		}
		if _, writeErr := temporary.Write(encoded); writeErr != nil {
			return nil, "", errors.New("write detached kernel result spool")
		}
		if syncErr := temporary.Sync(); syncErr != nil {
			return nil, "", errors.New("sync detached kernel result spool")
		}
		if closeErr := temporary.Close(); closeErr != nil {
			return nil, "", errors.New("close detached kernel result spool")
		}
		if renameErr := os.Rename(temporaryPath, finalPath); renameErr != nil {
			return nil, "", errors.New("publish detached kernel result spool")
		}
		committed = true
	}
	pointer, err := json.Marshal(struct {
		Version     int    `json:"version"`
		ExecutionID string `json:"execution_id"`
		Kind        string `json:"kind"`
		SHA256      string `json:"sha256"`
		Bytes       int    `json:"bytes"`
	}{Version: 1, ExecutionID: result.ExecutionID, Kind: "kernel-spool-v1", SHA256: fullSHA, Bytes: len(encoded)})
	if err != nil {
		return nil, "", errors.New("encode detached kernel result pointer")
	}
	return pointer, "kernel-spool-sha256:" + fullSHA, nil
}

func (e *Executor) runIdleReconciler(ctx context.Context) error {
	interval := e.IdleTimeout / 4
	if interval < time.Second {
		interval = time.Second
	}
	if interval > 30*time.Second {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-ticker.C:
			e.mu.Lock()
			if executorShouldReapIdle(e.lastActivity, len(e.active), now.UTC(), e.IdleTimeout) {
				e.draining = true
				e.mu.Unlock()
				e.requestShutdownIfDrained(ctx)
				return nil
			}
			e.mu.Unlock()
		}
	}
}

func executorShouldReapIdle(lastActivity time.Time, activeCount int, now time.Time, timeout time.Duration) bool {
	if activeCount != 0 || timeout <= 0 || lastActivity.IsZero() {
		return false
	}
	return !now.Before(lastActivity) && now.Sub(lastActivity) >= timeout
}

func (e *Executor) beginDraining() {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.draining = true
	e.mu.Unlock()
}

func (e *Executor) requestShutdown() {
	if e == nil || e.shutdown == nil {
		return
	}
	e.once.Do(func() { close(e.shutdown) })
}

// requestShutdownIfDrained closes the control loop only after both the local
// observer set and the durable accepted set are empty. The durable query is
// the authority across restart; the in-memory map only prevents closing while
// this process is still writing a receipt.
func (e *Executor) requestShutdownIfDrained(ctx context.Context) {
	if e == nil || e.Store == nil {
		return
	}
	e.mu.Lock()
	draining := e.draining
	localActive := len(e.active)
	e.mu.Unlock()
	if !draining || localActive != 0 {
		return
	}
	durable, err := e.Store.CountNonterminalKernelExecutions(ctx, e.BackendID, e.BackendGeneration)
	if err == nil && durable == 0 {
		e.requestShutdown()
	}
}

func (e *Executor) waitForDurableDrain(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(defaultExecutorDrainPollInterval)
	defer ticker.Stop()
	for {
		e.mu.Lock()
		localActive := len(e.active)
		e.mu.Unlock()
		durable, err := e.Store.CountNonterminalKernelExecutions(ctx, e.BackendID, e.BackendGeneration)
		if err != nil {
			return fmt.Errorf("inspect detached kernel drain: %w", err)
		}
		if localActive == 0 && durable == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("detached kernel drain did not settle: local_active=%d durable_active=%d: %w", localActive, durable, ctx.Err())
		case <-ticker.C:
		}
	}
}

func (e *Executor) validateController(ctx context.Context, request CommandRequest) error {
	backend, found, err := e.Store.GetKernelExecutionBackend(ctx, request.BackendID)
	if err != nil || !found || backend.BackendGeneration != request.BackendGeneration ||
		backend.ControllerEpoch != request.ControllerEpoch || backend.ControllerLeaseExpiresAt == nil ||
		!backend.ControllerLeaseExpiresAt.After(time.Now().UTC()) ||
		(backend.State != workspace.KernelExecutionBackendStateReady && backend.State != workspace.KernelExecutionBackendStateDraining) {
		return workspace.ErrKernelExecutionBackendStale
	}
	digest := sha256.Sum256([]byte(request.ControllerToken))
	if subtle.ConstantTimeCompare(backend.ControllerTokenSHA256, digest[:]) != 1 {
		return workspace.ErrKernelExecutionBackendStale
	}
	return nil
}

func (e *Executor) runHeartbeat(ctx context.Context, sequence int64) error {
	return runHeartbeatLoop(ctx, e.HeartbeatInterval, sequence, func(expectedSequence int64) error {
		_, err := e.Store.HeartbeatKernelExecutionBackend(ctx, workspace.HeartbeatKernelExecutionBackendInput{
			BackendID: e.BackendID, BackendGeneration: e.BackendGeneration,
			ExecutorInstanceID: e.ExecutorInstanceID, ExpectedHeartbeatSequence: expectedSequence,
		})
		return err
	})
}

func runHeartbeatLoop(ctx context.Context, interval time.Duration, sequence int64, heartbeat func(int64) error) error {
	if ctx == nil || heartbeat == nil || interval <= 0 {
		return errors.New("detached kernel heartbeat loop is incomplete")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			nextSequence := sequence + 1
			delay := minHeartbeatContentionRetryDelay
			for {
				err := heartbeat(nextSequence)
				if err == nil {
					sequence = nextSequence
					// A successful heartbeat callback may simultaneously close the
					// executor context. Observe that lifecycle boundary before a queued
					// ticker event can schedule one extra heartbeat under load.
					if ctx.Err() != nil {
						return nil
					}
					break
				}
				if !sqliteutil.IsTransientContention(err) {
					return err
				}
				log.Printf("detached kernel heartbeat delayed by transient SQLite contention sequence=%d retry_after=%s: %v", nextSequence, delay, err)
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					if !timer.Stop() {
						<-timer.C
					}
					return nil
				case <-timer.C:
				}
				if delay < maxHeartbeatContentionRetryDelay {
					delay *= 2
					if delay > maxHeartbeatContentionRetryDelay {
						delay = maxHeartbeatContentionRetryDelay
					}
				}
			}
		}
	}
}

func detachedExecutionSnapshot(execution workspace.DetachedKernelExecution) kernelruntime.BackendExecutionSnapshot {
	return kernelruntime.BackendExecutionSnapshot{
		ExecutionID: execution.ExecutionID, State: execution.State, StateVersion: execution.StateVersion,
		DispatchSequence: execution.DispatchSequence, LastObservationSequence: execution.LastObservationSequence,
		StdoutTail: execution.StdoutTail, CancelRequestID: execution.CancelRequestID,
		TerminalReceiptID: execution.TerminalReceiptID, ReasonCode: execution.ReasonCode,
		StartedAt:  execution.WorkerStartedAt,
		ObservedAt: execution.UpdatedAt.UTC(),
	}
}

func kernelSessionSpec(spec workspace.KernelExecutionSessionSpecV1) kernelruntime.SessionSpec {
	mounts := make([]kernelruntime.WorkerMount, 0, len(spec.Mounts))
	for _, mount := range spec.Mounts {
		mounts = append(mounts, restoredKernelWorkerMount(mount))
	}
	return kernelruntime.SessionSpec{
		KernelID: spec.KernelID, OwnerID: spec.OwnerUserID, ProjectID: spec.ProjectID,
		RootFrameID: spec.RootFrameID, RootFrameIncarnationID: spec.RootFrameIncarnationID,
		FrameID: spec.FrameID, FrameIncarnationID: spec.FrameIncarnationID,
		AgentName: spec.AgentName, DelegateName: spec.DelegateName, KernelKind: spec.KernelKind,
		Language: spec.Language, Environment: spec.Environment, RuntimeGeneration: spec.RuntimeGeneration,
		WorkspaceDir: spec.WorkspaceDir, Mounts: mounts, ProtectedPaths: append([]string(nil), spec.ProtectedPaths...),
		EgressAllowedDomains: append([]string(nil), spec.EgressAllowedDomains...),
		EgressDeniedDomains:  append([]string(nil), spec.EgressDeniedDomains...),
		CABundle:             spec.CABundle,
		UpstreamProxy:        spec.UpstreamProxy,
		Fresh:                spec.Fresh,
	}
}

func restoredKernelWorkerMount(mount workspace.KernelExecutionMountSpecV1) kernelruntime.WorkerMount {
	if mount.Trusted {
		return kernelruntime.TrustedReadOnlyDirectoryMount(mount.Path)
	}
	return kernelruntime.WorkerMount{Path: mount.Path, Writable: mount.Writable}
}

func waitForTrustedSocket(ctx context.Context, path string, timeout time.Duration, serverErr <-chan error) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := validateTrustedUnixSocket(path); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-serverErr:
			if err == nil {
				return errors.New("detached kernel control server stopped before readiness")
			}
			return err
		case <-deadline.C:
			return errors.New("detached kernel executor socket did not become ready")
		case <-ticker.C:
		}
	}
}
