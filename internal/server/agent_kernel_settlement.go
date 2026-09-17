package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	kernelruntime "synon-go/internal/kernel"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

const (
	kernelSettlementAdmissionMinRetry = 100 * time.Millisecond
	kernelSettlementAdmissionMaxRetry = 30 * time.Second
)

// enqueueBackgroundAgentKernelExecution transfers ownership of a completed
// background cell to the durable workspace outbox. Full execution output is
// staged in execution_log/materialization; the outbox contains only stable
// identities and digests so delivery can be retried across process restarts.
func (s *Server) enqueueBackgroundAgentKernelExecution(
	ctx context.Context,
	access workspace.KernelFrameAccess,
	spec kernelruntime.SessionSpec,
	session kernelruntime.EnsuredSession,
	started kernelruntime.ExecutionStarted,
	outcome kernelruntime.ExecutionOutcome,
	outputLimitBytes int64,
	operation *workspace.KernelLocalOperation,
	claim *transcriptstore.RunnerClaim,
	inputArtifacts []agentKernelInputArtifactReceipt,
) error {
	logInput, result, _ := prepareAgentKernelExecution(access, spec, session, started, outcome)
	bindAgentKernelInputArtifactAccessReceipts(&logInput, result, nil, inputArtifacts)
	result["cell_index"] = outcome.CellIndex
	duration := outcome.FinishedAt.Sub(started.StartedAt).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	input := workspace.EnqueueKernelBackgroundSettlementInput{
		ExecutionLog: logInput, Reused: session.Reused,
		DroppedRoots: append([]string(nil), outcome.DroppedRoots...), DurationMS: duration,
		OutputLimitBytes: outputLimitBytes, ToolID: started.ToolUseID,
	}
	if operation != nil {
		if claim == nil {
			return errors.New("kernel local operation claim is unavailable")
		}
		terminalState, reasonCode := kernelLocalOperationTerminalResultStatus(result)
		materialized, err := s.materializeAgentKernelTerminalResult(ctx, *operation, result, outputLimitBytes)
		if err != nil {
			return fmt.Errorf("materialize durable background kernel result: %w", err)
		}
		input.Operation = &workspace.KernelBackgroundSettlementOperationInput{
			Operation: *operation, Claim: *claim, TerminalState: terminalState, ReasonCode: reasonCode,
			TerminalResultJSON: materialized.JSON, TerminalResultRef: materialized.ResultRef,
		}
	} else {
		input.ResultCode = stringValue(result["code"])
	}
	input.Notification = workspace.CreateNotificationInput{
		ID: uuid.NewSHA1(uuid.NameSpaceOID,
			[]byte("kernel-cell-result:"+access.Frame.ID+":"+started.ExecID)).String(),
		SenderFrameID: access.Frame.ID, RecipientFrameID: access.Frame.ID,
		RootFrameID: access.Frame.RootFrameID, OwnerUserID: access.UserID,
		NotificationType: "cell_result",
	}
	_, err := s.workspaceStore.EnqueueKernelBackgroundSettlement(ctx, input)
	return err
}

func waitKernelSettlementAdmissionRetry(ctx context.Context, attempt uint64) error {
	delay := kernelSettlementAdmissionRetryDelay(attempt)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func kernelSettlementAdmissionRetryDelay(attempt uint64) time.Duration {
	if attempt >= 9 {
		return kernelSettlementAdmissionMaxRetry
	}
	delay := kernelSettlementAdmissionMinRetry << attempt
	if delay > kernelSettlementAdmissionMaxRetry {
		return kernelSettlementAdmissionMaxRetry
	}
	return delay
}

func logKernelSettlementAdmissionAttempt(attempt uint64) bool {
	ordinal := attempt + 1
	return ordinal <= 4 || ordinal&(ordinal-1) == 0 || ordinal%120 == 0
}

func validateAgentKernelToolInput(publicName string, input map[string]any) error {
	if publicName == softwareRuntimeToolName {
		_, err := decodeSoftwareRuntimeRequest(input)
		return err
	}
	allowed := map[string]struct{}{}
	switch publicName {
	case "python", "r":
		allowed = map[string]struct{}{"code": {}, "environment": {}, "working_dir": {}, "background": {}, "human_description": {}}
	case "bash":
		allowed = map[string]struct{}{"command": {}, "environment": {}, "working_dir": {}, "background": {}, "human_description": {}}
	case "repl":
		allowed = map[string]struct{}{"code": {}, "working_dir": {}, "background": {}, "fresh": {}, "human_description": {}}
	default:
		return errors.New("unsupported kernel tool")
	}
	for key := range input {
		if _, ok := allowed[key]; !ok {
			return errors.New("kernel tool input contains unsupported fields")
		}
	}
	if publicName == "bash" {
		command, ok := input["command"].(string)
		if !ok {
			return errors.New("bash command must be a string")
		}
		if err := validateAgentBashCommand(command); err != nil {
			return err
		}
	} else if _, ok := input["code"].(string); !ok {
		return errors.New("kernel code must be a string")
	}
	if publicName == "python" || publicName == "r" || publicName == "bash" {
		environment, ok := input["environment"].(string)
		if !ok {
			return errors.New(publicName + " environment must be a string")
		}
		if !kernelruntime.ValidEnvironmentName(environment) {
			return errors.New(publicName + " environment must be 1-100 bytes using letters, digits, dot, underscore, or dash")
		}
	}
	if publicName == "bash" {
		if err := validateManagedEnvironmentHumanDescription(stringValue(input["human_description"])); err != nil {
			return err
		}
	}
	return nil
}

// A local manager handle becoming terminal does not mean its durable
// settlement has finished. Keep the pre-submission reservation until then.
func (s *Server) launchReservedAgentKernelExecutionObserver(
	ctx context.Context, observerID string, cancel context.CancelFunc, observe func(context.Context),
) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer s.releaseDetachedKernelObserver(observerID, cancel)
		observe(ctx)
	}()
	return done
}

func (s *Server) observeAgentKernelExecution(
	resultContext context.Context,
	access workspace.KernelFrameAccess,
	spec kernelruntime.SessionSpec,
	session kernelruntime.EnsuredSession,
	started kernelruntime.ExecutionStarted,
	handle *kernelruntime.ExecutionHandle,
	closeAfterExecution bool,
	outputLimitBytes int64,
	operation *workspace.KernelLocalOperation,
	claim *transcriptstore.RunnerClaim,
	inputArtifactCollector *agentKernelInputArtifactCollector,
) {
	var outcome kernelruntime.ExecutionOutcome
	var ok bool
	select {
	case outcome, ok = <-handle.Done():
	case <-resultContext.Done():
		// Stop the exact local child, then retain its terminal receipt. Refresh
		// cannot finish until background executions acknowledge persistence.
		if s.kernelManager != nil {
			s.kernelManager.InterruptSession(spec.FrameID, spec.FrameIncarnationID, spec.RootFrameIncarnationID, started.ExecID)
		}
		outcome, ok = <-handle.Done()
	}
	if closeAfterExecution {
		if err := s.closeAgentKernel(session.ID); err != nil {
			log.Printf("close task-owned kernel %s before background result commit: %v", session.ID, err)
			return
		}
	}
	if !ok {
		return
	}
	var attempt uint64
	for {
		// Retain one bounded terminal receipt write when drain cancels the
		// observer. Never acknowledge a background execution before it commits.
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(resultContext), 5*time.Second)
		persistErr := s.enqueueBackgroundAgentKernelExecution(
			persistCtx, access, spec, session, started, outcome, outputLimitBytes, operation, claim,
			inputArtifactCollector.snapshot(),
		)
		cancel()
		if persistErr == nil {
			handle.AcknowledgePersistence()
			return
		}
		if errors.Is(persistErr, workspace.ErrWorkspaceStoreClosed) || s.isDraining() {
			log.Printf("stage background kernel settlement %s abandoned: %v", started.ExecID, persistErr)
			return
		}
		if logKernelSettlementAdmissionAttempt(attempt) {
			log.Printf("stage background kernel settlement %s attempt %d: %v", started.ExecID, attempt+1, persistErr)
		}
		if err := waitKernelSettlementAdmissionRetry(resultContext, attempt); err != nil {
			log.Printf("stage background kernel settlement %s stopped by lifecycle context: %v", started.ExecID, err)
			return
		}
		if attempt != ^uint64(0) {
			attempt++
		}
	}
}

func (s *Server) closeAgentKernel(kernelID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if s == nil || s.kernelManager == nil {
		return errors.New("kernel manager is unavailable")
	}
	return s.kernelManager.CloseKernel(ctx, kernelID)
}

func (s *Server) finishAgentKernelExecution(
	ctx context.Context,
	access workspace.KernelFrameAccess,
	spec kernelruntime.SessionSpec,
	session kernelruntime.EnsuredSession,
	started kernelruntime.ExecutionStarted,
	outcome kernelruntime.ExecutionOutcome,
	outputLimitBytes int64,
	operation *workspace.KernelLocalOperation,
	claim *transcriptstore.RunnerClaim,
	detachedReceipt *workspace.KernelExecutionResultReceipt,
	inputArtifacts []agentKernelInputArtifactReceipt,
) (map[string]any, error) {
	logInput, result, eventPayload := prepareAgentKernelExecution(access, spec, session, started, outcome)
	bindAgentKernelInputArtifactAccessReceipts(&logInput, result, eventPayload, inputArtifacts)
	if operation != nil {
		if operation.Tool == softwareRuntimeToolName {
			softwareResult, err := softwareRuntimeVisibleExecutionResult(
				operation.InputJSON, operation.Environment, spec.RuntimeGeneration, logInput.Record.Source, result,
			)
			if err != nil {
				return nil, fmt.Errorf("validate software runtime result: %w", err)
			}
			result = softwareResult
		}
		if detachedReceipt != nil && strings.TrimSpace(detachedReceipt.Outcome) == workspace.KernelExecutionResultCancelled {
			normalizeCancelledAgentKernelTerminalResult(result, operation.Tool)
		}
		if claim == nil && detachedReceipt == nil {
			return nil, errors.New("kernel local operation claim is unavailable")
		}
		terminalState, reasonCode := kernelLocalOperationTerminalResultStatus(result)
		// Bind the exact visible result before hashing/materializing it. Adding
		// cell_index after materialization makes trusted replay reject approved
		// detached kernel results as a large-result infrastructure failure even
		// when the result is small enough to remain inline.
		result["cell_index"] = outcome.CellIndex
		materialized, err := s.materializeAgentKernelTerminalResult(ctx, *operation, result, outputLimitBytes)
		if err != nil {
			return nil, fmt.Errorf("materialize durable kernel result: %w", err)
		}
		settlementBootID := s.kernelOperationBootID
		if detachedReceipt != nil {
			settlementBootID = operation.BootID
		}
		finishInput := workspace.FinishKernelLocalOperationInput{
			OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
			ExpectedStateVersion: operation.StateVersion,
			BootID:               settlementBootID, ExecutionID: started.ExecID,
			TerminalState: terminalState, ReasonCode: reasonCode, ExecutionLog: logInput,
			TerminalResultJSON: materialized.JSON, TerminalResultRef: materialized.ResultRef,
		}
		finishInput.Notification, err = detachedAgentKernelCompletionNotification(
			access, started, *operation, materialized.JSON, outputLimitBytes,
		)
		if err != nil {
			return nil, err
		}
		if claim != nil {
			finishInput.Claim = *claim
		}
		var finished workspace.FinishKernelLocalOperationResult
		if detachedReceipt != nil {
			finished, err = s.workspaceStore.FinishDetachedKernelLocalOperation(
				context.Background(), finishInput, detachedReceipt.ReceiptID,
			)
		} else {
			finished, err = s.workspaceStore.FinishKernelLocalOperation(context.Background(), finishInput)
		}
		if err != nil {
			return nil, fmt.Errorf("commit durable kernel result: %w", err)
		}
		*operation = finished.Operation
		if finished.NotificationEvent.ID != "" {
			if publishErr := s.publishWorkspaceEvent(finished.NotificationEvent); publishErr != nil {
				log.Printf("publish detached background kernel result %s: %v", finished.NotificationEvent.ID, publishErr)
			}
		}
	} else {
		record, err := s.workspaceStore.SaveExecutionLog(logInput)
		if err != nil {
			return nil, err
		}
		result["cell_index"] = record.CellIndex
	}
	s.publishAgentKernelEvent(access, started, "done", eventPayload)
	if spec.Language == "r" && operation == nil {
		visible := map[string]any{
			"stdout":    result["stdout"],
			"stderr":    result["stderr"],
			"exit_code": 0,
		}
		if stringValue(result["exit_status"]) != "ok" {
			visible["exit_code"] = 1
		}
		if stringValue(result["exit_status"]) == "cancelled" {
			visible["cancelled"] = true
		}
		return visible, nil
	}
	return result, nil
}

func decodeMaterializedAgentKernelResult(raw json.RawMessage) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var result map[string]any
	if err := decoder.Decode(&result); err != nil || result == nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, errors.New("materialized kernel result is invalid")
	}
	return result, nil
}

func agentKernelCellResultNotification(
	access workspace.KernelFrameAccess,
	started kernelruntime.ExecutionStarted,
	result map[string]any,
	outputLimitBytes int64,
) *workspace.CreateNotificationInput {
	return &workspace.CreateNotificationInput{
		ID: uuid.NewSHA1(uuid.NameSpaceOID,
			[]byte("kernel-cell-result:"+access.Frame.ID+":"+started.ExecID)).String(),
		SenderFrameID: access.Frame.ID, RecipientFrameID: access.Frame.ID,
		RootFrameID: access.Frame.RootFrameID, OwnerUserID: access.UserID,
		NotificationType: "cell_result",
		Payload:          agentKernelCellResultPayload(started, result, outputLimitBytes),
	}
}

func detachedAgentKernelCompletionNotification(
	access workspace.KernelFrameAccess,
	started kernelruntime.ExecutionStarted,
	operation workspace.KernelLocalOperation,
	materialized json.RawMessage,
	outputLimitBytes int64,
) (*workspace.CreateNotificationInput, error) {
	background, err := kernelLocalOperationBackgroundRequested(operation)
	if err != nil {
		return nil, fmt.Errorf("resolve kernel background result delivery: %w", err)
	}
	if !background {
		return nil, nil
	}
	authoritative, err := decodeMaterializedAgentKernelResult(materialized)
	if err != nil {
		return nil, errors.New("materialized background kernel result is invalid")
	}
	return agentKernelCellResultNotification(access, started, authoritative, outputLimitBytes), nil
}

// normalizeCancelledAgentKernelTerminalResult keeps both initial settlement and
// durable replay aligned with the terminal cancellation authority. A governed
// software harness can emit its own failure receipt while SIGINT is unwinding;
// that inner envelope must not override the executor receipt or the already
// committed operation state.
func normalizeCancelledAgentKernelTerminalResult(result map[string]any, tool string) {
	if result == nil {
		return
	}
	result["ok"] = false
	result["status"] = "cancelled"
	result["exit_status"] = "cancelled"
	result["retryable"] = true
	if strings.EqualFold(strings.TrimSpace(tool), softwareRuntimeToolName) {
		result["code"] = "software_runtime_cancelled"
		result["message"] = "The governed software operation was cancelled and its task-owned process tree was stopped."
		result["recovery"] = "inspect_existing_outputs_and_resume_the_task; rerun_only_if_required_results_are_missing_or_invalid"
	}
}

func kernelLocalOperationTerminalStatus(exitStatus string) (string, string) {
	switch strings.TrimSpace(exitStatus) {
	case "ok":
		return workspace.KernelLocalOperationStateCompleted, "execution_completed"
	case "cancelled":
		return workspace.KernelLocalOperationStateCancelled, "execution_cancelled"
	default:
		return workspace.KernelLocalOperationStateFailed, "execution_failed"
	}
}

func kernelLocalOperationTerminalResultStatus(result map[string]any) (string, string) {
	// A Python code preflight is an executed control step whose authoritative
	// result tells the agent how to repair the same environment or choose an
	// approved alternative. No user code ran, but the tool turn did complete.
	// Persisting it as a failed physical execution conflicts with the detached
	// executor's immutable completed receipt and strands the tool batch in
	// recovery. Keep ordinary execution failures terminal-failed below.
	if agentKernelPreflightResult(result) {
		return workspace.KernelLocalOperationStateCompleted, "execution_completed"
	}
	return kernelLocalOperationTerminalStatus(stringValue(result["exit_status"]))
}

func prepareAgentKernelExecution(
	access workspace.KernelFrameAccess,
	spec kernelruntime.SessionSpec,
	session kernelruntime.EnsuredSession,
	started kernelruntime.ExecutionStarted,
	outcome kernelruntime.ExecutionOutcome,
) (workspace.SaveExecutionLogInput, map[string]any, map[string]any) {
	stderr, exitStatus := kernelOutcomeStatus(outcome)
	source := started.Code
	bashExitCode := -1
	if spec.KernelKind == "bash" {
		command, commandErr := agentBashCommandFromWrapper(started.Code)
		if commandErr != nil {
			exitStatus = "error"
			if stderr != "" {
				stderr += "\n"
			}
			stderr += commandErr.Error()
		} else {
			source = command
		}
		var terminalErr error
		stderr, exitStatus, bashExitCode, terminalErr = normalizeAgentBashTerminal(stderr, exitStatus)
		if terminalErr != nil {
			if stderr != "" {
				stderr += "\n"
			}
			stderr += terminalErr.Error()
			exitStatus = "error"
		}
	}
	recovery := ""
	if outcome.TimedOut {
		if stderr != "" {
			stderr += "\n"
		}
		stderr += "Kernel execution exceeded its bounded wall-clock deadline and was stopped."
		if recovery != "" {
			recovery += "\n"
		}
		recovery += "Narrow the input or split the analysis into bounded steps. Inspect only the authorized workspace or exact prior-tool paths; use background=true for a legitimately long Python, R, or Bash computation."
	}
	policyWrites := agentKernelWorkspacePolicyWrites(outcome.FilesWritten)
	policyOnly := false
	if len(policyWrites) > 0 {
		policyRecovery := agentKernelWorkspacePolicyRecovery(policyWrites)
		policyOnly = exitStatus == "ok"
		if stderr != "" {
			stderr += "\n"
		}
		stderr += policyRecovery
		if policyOnly {
			exitStatus = "error"
		}
		if recovery != "" {
			recovery += "\n"
		}
		recovery += policyRecovery
	}
	codePreflight, codePreflightErr := agentKernelPythonCodePreflight(spec, outcome, exitStatus)
	if codePreflightErr != nil {
		exitStatus = "error"
		if stderr != "" {
			stderr += "\n"
		}
		stderr += codePreflightErr.Error()
	} else if codePreflight != nil {
		recovery = strings.TrimSpace(stringValue(codePreflight["recovery"]))
	}
	duration := outcome.FinishedAt.Sub(started.StartedAt).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	logInput := workspace.SaveExecutionLogInput{
		Record: workspace.ExecutionLogRecord{
			ID: started.ExecID, FrameID: spec.FrameID, CellIndex: outcome.CellIndex,
			KernelID: session.ID, KernelKind: spec.KernelKind, CondaEnv: spec.Environment,
			Language: spec.Language, Source: source, Stdout: outcome.Response.Stdout,
			Stderr: stderr, ExitStatus: exitStatus, Origin: "agent", ExecutedAt: started.StartedAt,
			FilesWritten: outcome.FilesWritten, ErrorLine: kernelErrorLine(outcome.Response.Trace),
		},
		ExpectedOwnerID: access.UserID, ExpectedProjectID: access.Frame.ProjectID,
		ExpectedFrameIncarnationID:     access.Frame.IncarnationID,
		ExpectedRootFrameIncarnationID: access.RootFrameIncarnationID,
	}
	result := map[string]any{
		"ok": exitStatus == "ok", "exec_id": started.ExecID, "tool_use_id": started.ToolUseID,
		// Kernel reuse is an execution-efficiency fact, not an idempotent tool
		// receipt. Keeping it under the generic reused field made every later REPL
		// cell look like a repeated read even when it executed new work.
		"kernel_id": session.ID, "kernel_kind": spec.KernelKind, "kernel_reused": session.Reused,
		"stdout": outcome.Response.Stdout, "stderr": stderr, "exit_status": exitStatus,
		"cell_index": outcome.CellIndex, "files_written": outcome.FilesWritten,
		"dropped_roots": outcome.DroppedRoots,
	}
	if environment := strings.TrimSpace(spec.Environment); environment != "" {
		result["environment"] = environment
	}
	if generation := strings.TrimSpace(spec.RuntimeGeneration); generation != "" {
		result["environment_generation"] = generation
	}
	if codePreflight != nil {
		result["ok"] = false
		result["status"] = "code_preflight_required"
		result["executed"] = false
		result["preflight"] = codePreflight
		result["message"] = stringValue(codePreflight["message"])
	}
	if outcome.TimedOut {
		result["timed_out"] = true
		result["code"] = "kernel_execution_timeout"
	}
	if environmentCode := agentKernelEnvironmentFailureCode(exitStatus, stderr); environmentCode != "" {
		applyAgentKernelFailureRecovery(result, environmentCode)
		recovery = stringValue(result["recovery"])
	}
	if spec.KernelKind == "bash" {
		result["exit_code"] = bashExitCode
		if bashExitCode > 0 && result["code"] == nil {
			result["code"] = "bash_nonzero_exit"
		}
	}
	if _, classified := result["code"]; !classified {
		if code := agentKernelExecutionFailureCode(spec.Language, exitStatus, stderr); code != "" {
			applyAgentKernelFailureRecovery(result, code)
			recovery = stringValue(result["recovery"])
		}
	}
	if len(policyWrites) > 0 {
		result["workspace_policy_violations"] = agentKernelWorkspacePolicyPayload(policyWrites)
		if policyOnly {
			// Keep the execution log truthful (exit_status=error), while exposing
			// the model-facing envelope as a recoverable partial result. The engine
			// can continue the same task and the violation remains fully recorded.
			result["partial"] = true
			result["status"] = "partial"
			result["recoverable"] = true
		}
	}
	eventPayload := map[string]any{
		"cell_id": started.ExecID, "stdout": outcome.Response.Stdout, "stderr": stderr,
		"cancelled": exitStatus == "cancelled", "duration_ms": duration,
		"files_written": outcome.FilesWritten, "dropped_roots": outcome.DroppedRoots,
	}
	if spec.KernelKind == "bash" {
		eventPayload["exit_code"] = bashExitCode
	}
	if codePreflight != nil {
		eventPayload["status"] = "code_preflight_required"
		eventPayload["executed"] = false
		eventPayload["preflight"] = codePreflight
	}
	if len(policyWrites) > 0 {
		eventPayload["workspace_policy_violations"] = agentKernelWorkspacePolicyPayload(policyWrites)
	}
	if recovery != "" {
		result["recovery"] = recovery
		eventPayload["recovery"] = recovery
	}
	return logInput, result, eventPayload
}
