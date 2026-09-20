package server

import (
	"context"

	"errors"
	"fmt"
	"log"

	"strings"
	"time"

	"github.com/google/uuid"

	"synon-go/internal/agentruntime"
	"synon-go/internal/executionprep"
	kernelruntime "synon-go/internal/kernel"
	"synon-go/internal/kernelcontract"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/software"
	"synon-go/internal/software/localcontainer"
)

func (s *Server) executeAgentKernelTool(
	ctx context.Context,
	identity *agentKernelContext,
	name string,
	input map[string]any,
	allowedToolSets ...[]string,
) (map[string]any, error) {
	return s.executeAgentKernelToolWithLimit(ctx, identity, name, input, defaultSessionRunnerOutputLimitBytes, allowedToolSets...)
}

func (s *Server) executeAgentKernelToolWithLimit(
	ctx context.Context,
	identity *agentKernelContext,
	name string,
	input map[string]any,
	outputLimitBytes int64,
	allowedToolSets ...[]string,
) (map[string]any, error) {
	return s.executeAgentKernelToolInternal(ctx, identity, nil, name, input, outputLimitBytes, allowedToolSets...)
}

func (s *Server) executeAgentKernelToolWithApproval(
	ctx context.Context,
	identity *agentKernelContext,
	call agentruntime.ToolCall,
	name string,
	input map[string]any,
	outputLimitBytes int64,
	allowedToolSets ...[]string,
) (map[string]any, error) {
	return s.executeAgentKernelToolInternal(ctx, identity, &call, name, input, outputLimitBytes, allowedToolSets...)
}

func (s *Server) executeAgentKernelToolInternal(
	ctx context.Context,
	identity *agentKernelContext,
	approvalCall *agentruntime.ToolCall,
	name string,
	input map[string]any,
	outputLimitBytes int64,
	allowedToolSets ...[]string,
) (map[string]any, error) {
	if identity == nil {
		return nil, errors.New("kernel execution identity is unavailable")
	}
	publicName := strings.ToLower(strings.TrimSpace(name))
	if publicName == "operon" {
		publicName = "repl"
	}
	if err := validateAgentKernelToolInput(publicName, input); err != nil {
		return nil, err
	}
	if publicName == softwareRuntimeToolName && approvalCall == nil {
		return nil, errors.New("software runtime requires a durable agent tool-call authority")
	}
	access, err := s.validateKernelHostIdentity(ctx, identity.access)
	if err != nil {
		return nil, err
	}
	// Durable approval is bound to the exact model-authored call. Execution-only
	// compatibility normalization must never replace that immutable authority.
	// Keeping these values separate matches the Harness boundary used for every
	// other confinement transform (working directory, artifact references, and
	// runtime launcher generation).
	authorityInput, input := agentKernelAuthorityAndExecutionInputs(publicName, input)
	observation := executionprep.ObservationFromContext(ctx)
	if observation != nil {
		source := stringValue(input["code"])
		if publicName == "bash" {
			source = stringValue(input["command"])
		}
		if !observation.Matches(publicName, source) {
			return executionprep.ObservationDeclined("diagnostic_source_binding_mismatch"), nil
		}
	}
	containerBacked := localcontainer.IsEnvironmentName(strings.TrimSpace(stringValue(input["environment"])))
	if observation != nil && containerBacked {
		return executionprep.ObservationDeclined("diagnostic_runtime_contract_unavailable"), nil
	}
	preflight := agentExecutionPreparationPreflight(ctx, publicName, input, identity, s.kernelManager)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if preflight != nil {
		return preflight, nil
	}
	if preflight := agentKernelOptionalFormatterPreflight(publicName, input); preflight != nil {
		return preflight, nil
	}
	if preflight := agentKernelPythonFileHandleShadowPreflight(publicName, input); preflight != nil {
		return preflight, nil
	}
	if preflight := agentKernelLargeToolResultPathPreflight(publicName, input); preflight != nil {
		return preflight, nil
	}
	if preflight := agentKernelMCPCatalogCallPreflight(publicName, input); preflight != nil {
		return preflight, nil
	}
	workspaceDir, err := s.ensureAgentWorkspaceRoot(identity)
	if err != nil {
		return nil, errors.New("kernel workspace authority is unavailable")
	}
	input, workingDir, err := s.normalizeAgentKernelWorkingDirInput(access.UserID, workspaceDir, input)
	if err != nil {
		return nil, err
	}
	kind, language, environment := "analysis", "python", ""
	code := ""
	executionTimeout := defaultAgentKernelExecutionTimeout
	taskOwnedKernel := publicName == softwareRuntimeToolName || containerBacked
	if taskOwnedKernel && s.kernelExecutionBackend == nil {
		code := "software_runtime_unavailable"
		message := "software runtime detached execution backend is unavailable; refusing an inline fallback"
		recovery := "wait_for_the_governed_detached_runtime_to_recover_then_retry_the_same_call"
		if containerBacked {
			code = "container_runtime_unavailable"
			message = "container execution requires the durable detached backend; refusing an inline or host-shell fallback"
			recovery = "restore_the_durable_executor_then_resume_the_same_container_environment_and_call"
		}
		return nil, software.NewOperationError(
			code, message, recovery,
			true,
		)
	}
	var preauthorizedOperation *workspace.KernelLocalOperation
	if publicName == softwareRuntimeToolName {
		selectedComputeProvider, selectionErr := s.resolveSelectedComputeProvider(ctx, access.UserID, access.Frame.RootFrameID)
		if selectionErr != nil {
			return nil, softwareRuntimeFailureMessage(selectionErr)
		}
		selectedSoftwareProvider, selectionErr := softwareRuntimeProviderForComputeSelection(selectedComputeProvider)
		if selectionErr != nil {
			return nil, softwareRuntimeFailureMessage(selectionErr)
		}
		if requestedProvider := strings.TrimSpace(stringValue(input["provider"])); requestedProvider != "" && requestedProvider != selectedSoftwareProvider {
			return nil, softwareRuntimeFailureMessage(fmt.Errorf(
				"requested software provider %q conflicts with the dialogue compute selection %q",
				requestedProvider, selectedComputeProvider,
			))
		}
		// Provision and generate the immutable launcher from the exact
		// model-authored request stored in the durable operation. The resolved
		// absolute working directory is an execution confinement detail passed
		// separately to the kernel; hashing it into the launcher would split the
		// approval authority from the execution authority for every relative cwd.
		controller, plan, resolveErr := s.resolveSoftwareRuntime(ctx, authorityInput)
		if resolveErr != nil {
			return nil, softwareRuntimeFailureMessage(resolveErr)
		}
		plan, resolveErr = durableSoftwareRuntimePlan(plan)
		if resolveErr != nil {
			return nil, softwareRuntimeFailureMessage(resolveErr)
		}
		environment = plan.Environment
		if plan.Request.TimeoutSeconds > 0 {
			executionTimeout = time.Duration(plan.Request.TimeoutSeconds)*time.Second + softwareRuntimeExecutionTimeoutGrace
		} else {
			executionTimeout = 0
		}
		if approvalCall != nil {
			operation, authorizeErr := s.authorizeAgentKernelLocalOperation(
				ctx, identity, *approvalCall, publicName, authorityInput, environment,
			)
			if authorizeErr != nil {
				return nil, authorizeErr
			}
			if operation.ExecutionLogID != "" {
				return s.replayAgentKernelOperationResult(operation)
			}
			preauthorizedOperation = &operation
		}
		operationID := "direct-" + plan.RequestDigest
		if approvalCall != nil {
			operationID = approvalCall.ID
		}
		_, code, err = s.provisionSoftwareRuntime(ctx, controller, plan, operationID)
		if err != nil {
			return nil, softwareRuntimeFailureMessage(err)
		}
	} else if publicName == "bash" {
		command := input["command"].(string)
		code, err = agentBashPythonWrapper(command)
		if err == nil && observation != nil {
			code, err = kernelcontract.BashObservationPythonWrapper(command, observation)
		}
		if err != nil {
			return nil, err
		}
		environment = input["environment"].(string)
		kind = "bash"
	} else {
		code = input["code"].(string)
		if strings.TrimSpace(code) == "" {
			return nil, errors.New("kernel code must not be empty")
		}
		if rawEnvironment, found := input["environment"]; found {
			environment = rawEnvironment.(string)
		}
	}
	if containerBacked && approvalCall != nil {
		operation, authorizeErr := s.authorizeAgentKernelLocalOperation(
			ctx, identity, *approvalCall, publicName, authorityInput, environment,
		)
		if authorizeErr != nil {
			return nil, authorizeErr
		}
		if operation.ExecutionLogID != "" {
			return s.replayAgentKernelOperationResult(operation)
		}
		preauthorizedOperation = &operation
	}
	if containerBacked {
		// A selected container is externally owned by Docker and recovered through
		// its deterministic durable identity. It has no implicit wall-clock limit;
		// explicit task cancellation remains authoritative.
		executionTimeout = 0
	}
	allowedTools := []string(nil)
	if len(allowedToolSets) > 0 {
		allowedTools = allowedToolSets[0]
	}
	if (publicName == "python" || publicName == "bash") && environment == "" {
		return nil, errors.New(publicName + " environment must not be empty")
	}
	if publicName == "r" {
		kind, language = "r", "r"
		if environment == "" {
			return nil, errors.New("r environment must not be empty")
		}
	}
	if publicName == "repl" {
		kind, environment = "operon", "repl"
	}
	if boundary := s.managedExecutionEnvironmentBoundary(ctx, publicName, input, environment); boundary != nil {
		return boundary, nil
	}
	if (publicName == "python" || publicName == "bash") && environment == agentKernelManagedPythonEnvironment {
		if s.kernelManager == nil {
			return nil, errors.New("managed Python scientific runtime is unavailable")
		}
		if err := s.kernelManager.EnsureManagedPythonEnvironment(ctx); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			return nil, errors.New("managed Python scientific runtime provisioning failed")
		}
	}
	runtimeGeneration := ""
	if (publicName == "python" || publicName == "bash") && environment == agentKernelManagedPythonEnvironment {
		runtimeGeneration, err = s.kernelManager.ManagedPythonActiveGeneration()
		if err != nil {
			return nil, errors.New("managed Python scientific runtime generation is unavailable")
		}
	}
	if runtimeGeneration == "" && agentKernelEnvironmentRequiresManagedGeneration(publicName, environment) {
		// A custom environment is never allowed to fall through to the worker
		// launcher without an immutable managed generation. In particular, a
		// partially constructed Server (or a restart during dependency
		// provisioning) may not have a kernel manager yet; treat that as the
		// same non-executing readiness state instead of starting a doomed
		// operation that later enters recovery.
		if s.kernelManager == nil {
			return agentKernelEnvironmentReadinessPreflight(nil), nil
		}
		generation, found, generationErr := s.kernelManager.ManagedEnvironmentActiveGeneration(environment)
		if generationErr != nil {
			return agentKernelEnvironmentReadinessPreflight(generationErr), nil
		}
		if !found || strings.TrimSpace(generation) == "" {
			return agentKernelEnvironmentReadinessPreflight(nil), nil
		}
		runtimeGeneration = generation
	}
	s.hostGrantKernelMu.Lock()
	hostGrantAuthorityLocked := true
	defer func() {
		if hostGrantAuthorityLocked {
			s.hostGrantKernelMu.Unlock()
		}
	}()
	if s.hostGrantKernelFences[access.UserID] {
		return nil, errors.New("kernel host access is fenced until isolation can be re-established")
	}
	if err := s.ensureAgentKernelManagedDirectories(); err != nil {
		return nil, err
	}
	executionIdentity := *identity
	executionIdentity.workspaceDir = workspaceDir
	identity = &executionIdentity
	background, err := optionalKernelBoolean(input, "background")
	if err != nil {
		return nil, err
	}
	fresh, err := optionalKernelBoolean(input, "fresh")
	if err != nil {
		return nil, err
	}
	if fresh && publicName != "repl" {
		return nil, errors.New("fresh is available only for repl")
	}
	excludePackID := ""
	if engine, found := s.canonicalManagedExecutionPack(publicName, input); found {
		excludePackID = engine.ExecutionPack.ID
	}
	spec, err := s.agentKernelSessionSpec(ctx, access, identity.workspaceDir, agentKernelSessionRuntime{
		PublicName: publicName, Kind: kind, Language: language,
		Environment: environment, RuntimeGeneration: runtimeGeneration,
		ExcludePackID: excludePackID,
	})
	if err != nil {
		return nil, err
	}
	if taskOwnedKernel {
		if preauthorizedOperation == nil {
			return nil, errors.New("task-owned runtime durable operation authority is unavailable")
		}
		spec.KernelID = softwareRuntimeKernelID(preauthorizedOperation.OperationID)
	}
	if approvalCall != nil && s.kernelExecutionBackend != nil && publicName != "repl" &&
		agentKernelUsesDetachedExecution(publicName, background) {
		// Executor provisioning is infrastructure preparation, not the admitted
		// scientific side effect. Approval resumes intentionally replace the
		// parked request context; keep this bounded setup alive across that handoff
		// so an executor cannot be launched and then abandoned while the durable
		// operation remains approved. Software-runtime work always uses this path,
		// including foreground calls, so a Web/service restart cannot turn a
		// physically running computation into an outcome_unknown inline process.
		// The live runner claim is revalidated before PrepareKernelLocalOperation
		// and no user code starts on a stale claim.
		setupCtx, setupCancel := detachedKernelSetupContext(ctx, executionTimeout)
		defer setupCancel()
		backendSession, ensureErr := s.kernelExecutionBackend.EnsureSession(setupCtx, spec)
		if ensureErr != nil {
			return nil, ensureErr
		}
		if run, _ := setupCtx.Value(transcriptRunnerChatRunContextKey{}).(*sessionRunnerChatRun); run == nil || run.Transcript == nil {
			return nil, s.releaseUnstartedDetachedSession(
				publicName, backendSession, errors.New("kernel local operation runner authority is unavailable after backend setup"), taskOwnedKernel,
			)
		} else if err := s.validateLiveTranscriptRunnerClaim(setupCtx, run.Transcript.Claim); err != nil {
			return nil, s.releaseUnstartedDetachedSession(publicName, backendSession, err, taskOwnedKernel)
		}
		s.hostGrantKernelMu.Unlock()
		hostGrantAuthorityLocked = false
		return s.executeDetachedAgentKernel(setupCtx, access, identity, spec, backendSession,
			publicName, authorityInput, code, workingDir, background, executionTimeout, outputLimitBytes, *approvalCall)
	}
	execID := uuid.NewString()
	toolUseID := "agent-" + execID
	observerID := "local:" + execID
	observerCtx, observerCancel, reserved := s.reserveDetachedKernelObserver(
		durableAgentKernelResultContext(ctx, toolUseID), observerID,
	)
	if !reserved {
		return nil, ErrRuntimeDraining
	}
	// Reserve before any local submission. Shutdown may begin while a
	// foreground caller is handing off, but it cannot reject an already-owned
	// terminal receipt or close its store before that receipt has settled.
	observerOwned := true
	defer func() {
		if observerOwned {
			s.releaseDetachedKernelObserver(observerID, observerCancel)
		}
	}()
	var session kernelruntime.EnsuredSession
	ownedKernel := false
	defer func() {
		if ownedKernel {
			if closeErr := s.closeAgentKernel(session.ID); closeErr != nil {
				log.Printf("close task-owned kernel %s: %v", session.ID, closeErr)
			}
		}
	}()
	if fresh {
		spec.KernelID = "kernel-fresh-" + uuid.NewString()
		spec.Fresh = true
		worker, startErr := s.kernelManager.StartFreshSession(spec)
		if startErr != nil {
			return nil, startErr
		}
		session = kernelruntime.EnsuredSession{ID: spec.KernelID, Worker: worker}
		ownedKernel = true
	} else {
		session, err = s.kernelManager.EnsureSession(spec)
		if err != nil {
			return nil, err
		}
		ownedKernel = taskOwnedKernel
	}
	s.hostGrantKernelMu.Unlock()
	hostGrantAuthorityLocked = false
	expectedGeneration := session.Worker.Generation()
	if expectedGeneration == 0 {
		return nil, errors.New("kernel generation is unavailable")
	}
	var hostCalls *kernelruntime.HostCallPolicy
	inputArtifactCollector := newAgentKernelInputArtifactCollector()
	if publicName == "repl" {
		outerToolUseID := ""
		if approvalCall != nil {
			outerToolUseID = approvalCall.ID
		}
		executionBinding := kernelTranscriptExecutionBindingFromContext(
			ctx, outerToolUseID, session.ID, expectedGeneration, session.Worker,
		)
		// A foreground wait may detach without cancelling the already-authorized
		// execution. Host-call authority therefore belongs to the execution
		// identity, not to the caller's shorter foreground wait context.
		policyContext := context.Background()
		if reviewerScope := sessionReviewerEvidenceScopeFromContext(ctx); reviewerScope != nil {
			policyContext = withSessionReviewerEvidenceScope(policyContext, reviewerScope)
		}
		hostCalls = s.agentKernelHostCallPolicy(
			policyContext, access, identity.workspaceDir, allowedTools, fresh, executionBinding,
		)
	} else if publicName == "python" {
		hostCalls = s.kernelHostCallPolicy(context.Background(), access, identity.workspaceDir)
		hostCalls.AllowedMethods = []string{"host.artifact_path"}
	}
	inputArtifactCollector.wrap(s, access, hostCalls)
	submitRequest := kernelruntime.SubmitRequest{
		KernelID: session.ID, ExpectedGeneration: expectedGeneration,
		OwnerID: spec.OwnerID, ProjectID: spec.ProjectID, FrameID: spec.FrameID,
		FrameIncarnationID: spec.FrameIncarnationID, RootFrameIncarnationID: spec.RootFrameIncarnationID,
		KernelKind: kind, Language: language, Environment: environment,
		ExecID: execID, ToolUseID: toolUseID, ToolName: publicName, Code: code, Origin: "agent",
		WorkingDir: workingDir, Background: background, Fresh: fresh,
		Timeout: executionTimeout, HostCalls: hostCalls,
		Observation: observation,
	}
	if observation != nil {
		submitRequest.ObservationCodeSHA256 = executionprep.SourceSHA256(code)
	}
	var handle *kernelruntime.ExecutionHandle
	var kernelOperation *workspace.KernelLocalOperation
	var kernelOperationClaim *transcriptstore.RunnerClaim
	if approvalCall != nil {
		operation := workspace.KernelLocalOperation{}
		if preauthorizedOperation != nil {
			operation = *preauthorizedOperation
		} else {
			operation, err = s.authorizeAgentKernelLocalOperation(
				ctx, identity, *approvalCall, publicName, authorityInput, environment,
			)
			if err != nil {
				return nil, err
			}
		}
		if operation.ExecutionLogID != "" {
			return s.replayAgentKernelOperationResult(operation)
		}
		kernelOperation = &operation
		confinementSHA, err := agentKernelConfinementSHA256(spec)
		if err != nil {
			return nil, err
		}
		gate := make(chan error, 1)
		gatedRequest := submitRequest
		gatedRequest.StartAuthorization = gate
		preparedHandle, err := s.kernelManager.Submit(gatedRequest)
		if err != nil {
			return nil, err
		}
		reject := func(rejection error) error {
			gate <- rejection
			close(gate)
			outcome := <-preparedHandle.Done()
			if !outcome.Dequeued || outcome.Err == nil {
				return errors.Join(rejection, errors.New("kernel execution authorization gate did not dequeue cleanly"))
			}
			return rejection
		}
		run, _ := ctx.Value(transcriptRunnerChatRunContextKey{}).(*sessionRunnerChatRun)
		if run == nil || run.Transcript == nil {
			return nil, reject(errors.New("kernel local operation runner authority is unavailable"))
		}
		preparedOperation, err := s.workspaceStore.PrepareKernelLocalOperation(ctx,
			workspace.PrepareKernelLocalOperationInput{
				OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
				ExpectedStateVersion: operation.StateVersion, Claim: run.Transcript.Claim,
				BootID: s.kernelOperationBootID, KernelID: session.ID,
				KernelGeneration: int64(expectedGeneration), ConfinementSHA256: confinementSHA,
			})
		if err != nil {
			return nil, reject(err)
		}
		startedOperation, err := s.workspaceStore.StartKernelLocalOperation(ctx,
			workspace.StartKernelLocalOperationInput{
				OwnerUserID: preparedOperation.OwnerUserID, OperationID: preparedOperation.OperationID,
				ExpectedStateVersion: preparedOperation.StateVersion, Claim: run.Transcript.Claim,
				BootID: s.kernelOperationBootID, ExecutionID: execID,
			})
		if err != nil {
			return nil, reject(err)
		}
		kernelOperation = &startedOperation
		claim := run.Transcript.Claim
		kernelOperationClaim = &claim
		handle = preparedHandle
		gate <- nil
		close(gate)
	}
	if handle == nil {
		if approvalCall != nil {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			run, _ := ctx.Value(transcriptRunnerChatRunContextKey{}).(*sessionRunnerChatRun)
			if run == nil || run.Transcript == nil {
				return nil, errors.New("kernel local execution runner authority is unavailable before submit")
			}
			if err := s.validateLiveTranscriptRunnerClaim(ctx, run.Transcript.Claim); err != nil {
				return nil, err
			}
			if expectedGeneration == 0 || session.Worker.Generation() != expectedGeneration {
				return nil, errors.New("kernel generation changed before approved execution submit")
			}
		}
		var err error
		handle, err = s.kernelManager.Submit(submitRequest)
		if err != nil {
			return nil, err
		}
	}
	started, earlyOutcome, startErr := awaitAgentKernelExecutionStart(ctx, handle.Started(), handle.Done())
	if startErr != nil {
		s.kernelManager.InterruptSession(spec.FrameID, spec.FrameIncarnationID, spec.RootFrameIncarnationID, execID)
		select {
		case <-handle.Done():
		case <-time.After(6 * time.Second):
		}
		return nil, agentKernelContextCause(ctx)
	}
	if earlyOutcome != nil {
		if kernelOperation == nil {
			if earlyOutcome.ObservationRefused {
				_, exitStatus := kernelOutcomeStatus(*earlyOutcome)
				return agentKernelObservationRefusal(*earlyOutcome, exitStatus)
			}
			if earlyOutcome.Err != nil {
				return nil, earlyOutcome.Err
			}
			return nil, errors.New("kernel execution ended before start")
		}
		if started.ExecID == "" {
			started = agentKernelSyntheticExecutionStart(submitRequest, *earlyOutcome)
		}
		return s.finishAgentKernelExecution(ctx, access, spec, session, started, *earlyOutcome, outputLimitBytes,
			kernelOperation, kernelOperationClaim, nil, inputArtifactCollector.snapshot())
	}
	s.publishAgentKernelEvent(access, started, "start", map[string]any{"source": started.Code, "started_at": started.StartedAt.UTC().Format(time.RFC3339Nano)})
	startObserver := func() <-chan struct{} {
		closeAfterExecution := ownedKernel
		ownedKernel = false
		observerOwned = false
		return s.launchReservedAgentKernelExecutionObserver(
			observerCtx, observerID, observerCancel,
			func(observerCtx context.Context) {
				s.observeAgentKernelExecution(observerCtx, access, spec, session, started, handle,
					closeAfterExecution, outputLimitBytes, kernelOperation, kernelOperationClaim, inputArtifactCollector)
			},
		)
	}
	if background {
		if err := s.recordAgentKernelBackgroundStart(access, started); err != nil {
			s.kernelManager.InterruptSession(spec.FrameID, spec.FrameIncarnationID, spec.RootFrameIncarnationID, execID)
			select {
			case <-handle.Done():
			case <-time.After(6 * time.Second):
			}
			handle.AcknowledgePersistence()
			return nil, errors.New("kernel background result channel is unavailable")
		}
		startObserver()
		return map[string]any{
			"status": "running", "exec_id": execID,
			"message": "Kernel cell is running in the background.",
		}, nil
	}
	foregroundWaitTimeout := s.agentKernelForegroundWaitBudget(access.Frame.RootFrameID)
	var foregroundTimer *time.Timer
	var foregroundTimerC <-chan time.Time
	if foregroundWaitTimeout > 0 {
		foregroundTimer = time.NewTimer(foregroundWaitTimeout)
		foregroundTimerC = foregroundTimer.C
		defer foregroundTimer.Stop()
	}
	var outcome kernelruntime.ExecutionOutcome
	select {
	case outcome = <-handle.Done():
	case <-ctx.Done():
		if s.kernelManager != nil {
			s.kernelManager.CancelHostCalls(spec.FrameID, spec.FrameIncarnationID, spec.RootFrameIncarnationID, execID)
		}
		cause := agentKernelContextCause(ctx)
		if agentKernelCallerRequiresExecutionStop(ctx) {
			// A user stop and a runtime drain are lifecycle commands, not short
			// foreground-wait cancellations. Never detach their computation. A
			// user stop settles the exact durable execution as cancelled; a drain
			// leaves the started operation recoverable by the next process.
			if errors.Is(cause, ErrGenerationStopped) {
				if err := s.recordAgentKernelBackgroundStart(access, started); err != nil {
					s.kernelManager.InterruptSession(spec.FrameID, spec.FrameIncarnationID, spec.RootFrameIncarnationID, execID)
					select {
					case <-handle.Done():
					case <-time.After(6 * time.Second):
					}
					return nil, errors.Join(cause, errors.New("kernel cancellation result channel is unavailable"))
				}
				settled := startObserver()
				s.kernelManager.InterruptSession(spec.FrameID, spec.FrameIncarnationID, spec.RootFrameIncarnationID, execID)
				select {
				case <-settled:
				case <-time.After(6 * time.Second):
				}
				return nil, cause
			}

			s.kernelManager.InterruptSession(spec.FrameID, spec.FrameIncarnationID, spec.RootFrameIncarnationID, execID)
			select {
			case <-handle.Done():
			case <-time.After(6 * time.Second):
			}
			return nil, cause
		}
		if err := s.recordAgentKernelBackgroundStart(access, started); err != nil {
			s.kernelManager.InterruptSession(spec.FrameID, spec.FrameIncarnationID, spec.RootFrameIncarnationID, execID)
			return nil, errors.New("kernel detached result channel is unavailable")
		}
		startObserver()
		return map[string]any{
			"status": "running", "exec_id": execID,
			"message": "Kernel cell continues in the background after the caller stopped waiting.",
		}, nil
	case <-observerCtx.Done():
		if err := s.recordAgentKernelBackgroundStart(access, started); err != nil {
			s.kernelManager.InterruptSession(spec.FrameID, spec.FrameIncarnationID, spec.RootFrameIncarnationID, execID)
			return nil, errors.Join(ErrRuntimeDraining, err)
		}
		startObserver()
		return nil, ErrRuntimeDraining
	case <-foregroundTimerC:
		if err := s.recordAgentKernelBackgroundStart(access, started); err != nil {
			s.kernelManager.InterruptSession(spec.FrameID, spec.FrameIncarnationID, spec.RootFrameIncarnationID, execID)
			return nil, errors.New("kernel detached result channel is unavailable")
		}
		startObserver()
		return map[string]any{
			"status": "running", "exec_id": execID,
			"message": "Kernel cell exceeded the foreground wait window and continues in the background.",
		}, nil
	}
	if ownedKernel {
		if err := s.closeAgentKernel(session.ID); err != nil {
			return nil, fmt.Errorf("close task-owned kernel before result commit: %w", err)
		}
		ownedKernel = false
	}
	return s.finishAgentKernelExecution(ctx, access, spec, session, started, outcome, outputLimitBytes,
		kernelOperation, kernelOperationClaim, nil, inputArtifactCollector.snapshot())
}
