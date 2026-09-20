package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"synon-go/internal/agentruntime"
	"synon-go/internal/kernelcontract"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/toolcontract"
)

const prestartToolFailurePhase = "prestart_failed"

func (s *Server) resumePendingAgentToolCalls(
	ctx context.Context,
	options SessionRunnerChatOptions,
	run *sessionRunnerChatRun,
) (bool, error) {
	resumed := false
	if run.ToolBatchIDs == nil {
		run.ToolBatchIDs = map[string]string{}
	}
	if run.ToolBatchOrdinals == nil {
		run.ToolBatchOrdinals = map[string]int64{}
	}
	if run.KernelOperationIDs == nil {
		run.KernelOperationIDs = map[string]string{}
	}
	for {
		batches, err := s.workspaceStore.ListRunnableToolCallBatches(ctx, run.Transcript.Claim, 100)
		if err != nil {
			return resumed, err
		}
		if len(batches) == 0 {
			return resumed, nil
		}
		batch := batches[0]
		items, err := s.workspaceStore.ListToolCallBatchItems(ctx, batch.OwnerUserID, batch.BatchID)
		if err != nil {
			return resumed, err
		}
		if batch.NextOrdinal < 0 || batch.NextOrdinal >= int64(len(items)) || int64(len(items)) != batch.CallCount {
			fmt.Fprintf(os.Stderr, "[runner] skipping inconsistent tool batch %s: next_ordinal=%d items=%d call_count=%d\n",
				batch.BatchID, batch.NextOrdinal, len(items), batch.CallCount)
			return resumed, nil
		}
		item := items[batch.NextOrdinal]
		if item.Ordinal != batch.NextOrdinal {
			fmt.Fprintf(os.Stderr, "[runner] skipping tool batch %s: cursor ordinal %d does not match item ordinal %d\n",
				batch.BatchID, batch.NextOrdinal, item.Ordinal)
			return resumed, nil
		}
		run.ToolBatchIDs[item.ToolCallID] = batch.BatchID
		run.ToolBatchOrdinals[item.ToolCallID] = item.Ordinal
		if item.StartedEventID > 0 {
			if run.ToolSourceEventIDs == nil {
				run.ToolSourceEventIDs = map[string]int64{}
			}
			run.ToolSourceEventIDs[item.ToolCallID] = item.StartedEventID
		}
		isKernel := kernelcontract.Admitted(item.ToolName, item.ArgumentsJSON)
		if isKernel {
			operation, found, err := s.workspaceStore.GetKernelLocalOperationByToolCall(
				ctx, batch.OwnerUserID, batch.StreamUID, item.ToolCallID,
			)
			if err != nil {
				return resumed, err
			}
			if !found {
				return resumed, workspace.ErrKernelLocalOperationConflict
			}
			run.KernelOperationIDs[item.ToolCallID] = operation.OperationID
			if operation.State == workspace.KernelLocalOperationStatePendingApproval &&
				(item.State == workspace.ToolCallBatchItemStateWaiting ||
					item.State == workspace.ToolCallBatchItemStateRunning) {
				resolved, decision, resolveErr := s.resolvePendingKernelLocalOperationPolicy(
					ctx, run, run.Transcript.Claim, operation,
				)
				if resolveErr != nil {
					return resumed, resolveErr
				}
				operation = resolved
				if decision == "ask" {
					return resumed, &agentruntime.PauseError{
						Status: "awaiting_approval", Message: "Kernel local execution is awaiting approval.",
						Data: map[string]any{
							"operation_id": operation.OperationID, "request_id": operation.ApprovalRequestID,
							"state_version": operation.StateVersion, "tool_call_id": operation.ToolCallID,
						},
					}
				}
			}
			if operation.State == workspace.KernelLocalOperationStateStarted &&
				item.State == workspace.ToolCallBatchItemStateRunning {
				background, backgroundErr := kernelLocalOperationBackgroundRequested(operation)
				if backgroundErr != nil {
					return resumed, backgroundErr
				}
				if !background {
					// Foreground work is supervised by the live kernel observer and
					// therefore has no detached-execution row. Retain this runner until
					// the durable operation becomes terminal; treating the absent row as
					// failed recovery immediately reclaims the same batch in a hot loop.
					// KernelRetentionWake is the single event-driven wake authority.
					operation, err = s.waitForStartedKernelLocalOperation(ctx, operation)
					if err != nil {
						return resumed, err
					}
				} else {
					execution, found, executionErr := s.workspaceStore.GetDetachedKernelExecution(
						ctx, operation.ExecutionID,
					)
					if executionErr != nil {
						return resumed, executionErr
					}
					if detachedKernelExecutionContinues(operation, execution, found) {
						result := map[string]any{
							"status": "running", "exec_id": operation.ExecutionID,
							"message": "Kernel cell continues under detached execution supervision.",
						}
						if err := s.checkpointChatTool(options, run, "completed",
							fmt.Sprintf("tool %s continues in the background", item.ToolName),
							item.ToolCallID, "completed", map[string]any{
								"toolName": item.ToolName, "toolResult": result,
							}); err != nil {
							return resumed, err
						}
						resumed = true
						continue
					}
				}
			}
			if item.State != workspace.ToolCallBatchItemStatePending &&
				workspace.KernelLocalOperationIsTerminal(operation.State) {
				reconciled, reconcileErr := s.workspaceStore.ReconcileToolCallBatchFromKernelProtocolReceipt(
					ctx, workspace.ReconcileKernelToolCallBatchInput{
						Claim: run.Transcript.Claim, BatchID: batch.BatchID,
						Ordinal: item.Ordinal, OperationID: operation.OperationID,
					},
				)
				if reconcileErr != nil {
					return resumed, fmt.Errorf("reconcile terminal kernel tool batch %s: %w", batch.BatchID, reconcileErr)
				}
				if reconciled {
					if restoreErr := s.restoreTrustedScientificStateForReconciledKernelOperation(
						run, item, operation,
					); restoreErr != nil {
						return resumed, restoreErr
					}
					resumed = true
					continue
				}
				// A service restart or a pre-receipt runner failure can leave a
				// terminal kernel operation with its immutable result materialized
				// but without the protocol receipt that normally advances this
				// batch. Reconstruct that exact terminal tool result and commit the
				// missing receipt so a new user message is not blocked by stale work.
				recovered, recoverErr := s.reconcileTerminalKernelToolCallWithoutProtocolReceipt(
					ctx, options, run, item, operation,
				)
				if recoverErr != nil {
					return resumed, recoverErr
				}
				if recovered {
					resumed = true
					continue
				}
			}
			if item.State != workspace.ToolCallBatchItemStatePending {
				if operation.State == workspace.KernelLocalOperationStatePrepared {
					reclaimed, reclaimErr := s.workspaceStore.ReclaimPreparedKernelLocalOperation(
						ctx, workspace.ReclaimPreparedKernelLocalOperationInput{
							OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
							ExpectedStateVersion: operation.StateVersion, RunnerID: operation.RunnerID,
							RunnerAttempt:     operation.RunnerAttempt,
							RunnerClaimSHA256: operation.RunnerClaimSHA256, BootID: operation.BootID,
							ReasonCode: "runner_handoff_before_start",
						},
					)
					if reclaimErr == nil {
						operation = reclaimed
					} else if !errors.Is(reclaimErr, workspace.ErrKernelLocalOperationStale) {
						return resumed, reclaimErr
					}
				}
				return resumed, &kernelLocalOperationPendingRecoveryError{
					operationID: operation.OperationID,
					state:       operation.State,
					cause: fmt.Errorf(
						"kernel tool-call batch item %s is pending durable operation recovery for operation state %s",
						item.State, operation.State,
					),
				}
			}
		}
		resumingInterruptedReadOnlyCall := false
		approvedCallID := ""
		if item.State == workspace.ToolCallBatchItemStateRunning {
			recovered, recoverErr := s.recoverPersistedRunnerLargeToolResult(ctx, options, run, item)
			if recoverErr != nil {
				return resumed, recoverErr
			}
			if recovered {
				resumed = true
				continue
			}
			replaySafe, replayErr := s.runnerToolCallReplaySafeAfterInterruption(ctx, options, item)
			if replayErr != nil {
				return resumed, replayErr
			}
			if !replaySafe {
				result := map[string]any{"ok": false, "error": map[string]any{
					"code": "tool_outcome_unknown", "message": "Tool execution outcome is unavailable after runner recovery.",
				}}
				if err := s.checkpointChatTool(options, run, "failed", "tool outcome is unavailable after runner recovery",
					item.ToolCallID, "outcome_unknown", map[string]any{
						"toolName": item.ToolName, "toolInput": decodeToolEventJSON(string(item.ArgumentsJSON)), "toolResult": result,
					}); err != nil {
					return resumed, err
				}
				resumed = true
				continue
			}
			// The original start event remains authoritative. Execute the exact
			// immutable read-only call again and attach only its terminal result.
			resumingInterruptedReadOnlyCall = true
		}
		if item.State == workspace.ToolCallBatchItemStateWaiting {
			approvalResult, approvalState, approvalID, approvalFound, approvalErr := s.agentToolApprovalBatchResult(ctx, item)
			if approvalErr != nil {
				return resumed, approvalErr
			}
			if approvalFound {
				switch approvalState {
				case "approved":
					if err := s.checkpointChatTool(options, run, "running", "resuming approved tool", item.ToolCallID, "approval_resumed", map[string]any{"toolName": item.ToolName, "toolInput": decodeToolEventJSON(string(item.ArgumentsJSON))}); err != nil {
						return resumed, err
					}
					approvedCallID = approvalID
					resumingInterruptedReadOnlyCall = true
				case "pending", "executing":
					return resumed, &agentruntime.PauseError{
						Status: "awaiting_approval", Message: "waiting for the user-approved tool operation to settle",
						Data: map[string]any{
							"approval_kind": agentToolApprovalKind, "approval_id": approvalID,
							"request_id": approvalID, "frame_id": run.Transcript.Stream.FrameID,
							"tool_call_id": item.ToolCallID, "tool_name": item.ToolName,
						},
					}
				case "completed", "denied", "failed", "blocked":
					status, phase := "completed", "completed"
					if agentruntime.IsNonExecutingPreflight(approvalResult) {
						status, phase = "failed", prestartToolFailurePhase
					} else if agentruntime.ClassifyToolResult(approvalResult).HardFailed() {
						status, phase = "failed", "failed"
					}
					if err := s.checkpointChatTool(options, run, status,
						fmt.Sprintf("tool %s %s after user approval", item.ToolName, status),
						item.ToolCallID, phase, map[string]any{
							"toolName": item.ToolName, "toolResult": approvalResult,
						}); err != nil {
						return resumed, err
					}
					resumed = true
					continue
				default:
					return resumed, errors.New("agent tool approval state is invalid")
				}
			}
			if approvedCallID == "" && item.ToolName == generatePlanToolName {
				metadata, found, err := s.workspaceStore.GetFrameRuntimeMetadata(run.Transcript.Stream.FrameID)
				if err != nil {
					return resumed, err
				}
				if !found || strings.TrimSpace(stringValue(metadata.ContextData["_plan_tool_call_id"])) != item.ToolCallID ||
					strings.TrimSpace(stringValue(metadata.ContextData["_plan_artifact_id"])) == "" ||
					strings.TrimSpace(stringValue(metadata.ContextData["_plan_version_id"])) == "" {
					return resumed, errors.New("generated plan waiting item has no matching durable plan authority")
				}
				if !compatibilityPlanBool(metadata.ContextData["_plan_approved"]) {
					inputType, inputErr := s.transcriptStore.LatestRunnerInputEventType(
						ctx, run.Transcript.Stream.UID, run.Transcript.Stream.OwnerID,
					)
					if inputErr != nil || inputType != "user_input_response" {
						return resumed, errors.New("generated plan was resumed without a user plan response")
					}
					result := map[string]any{
						"ok": true, "status": "plan_presented",
						"artifact_id": stringValue(metadata.ContextData["_plan_artifact_id"]),
						"version_id":  stringValue(metadata.ContextData["_plan_version_id"]),
						"message":     "The user responded to the presented plan. Interpret that exact user message. If it explicitly approves without changes, call generate_plan with approve=true alone before execution; otherwise answer or revise and keep the plan unapproved.",
					}
					if err := s.checkpointChatTool(options, run, "completed",
						"generated plan presented", item.ToolCallID, "completed", map[string]any{
							"toolName": item.ToolName, "toolResult": result,
						}); err != nil {
						return resumed, err
					}
					resumed = true
					continue
				}
				result := map[string]any{
					"ok": true, "status": "approved",
					"artifact_id": stringValue(metadata.ContextData["_plan_artifact_id"]),
					"version_id":  stringValue(metadata.ContextData["_plan_version_id"]),
					"message":     "The user approved this exact plan artifact version. Continue its steps without generating another plan.",
				}
				if err := s.checkpointChatTool(options, run, "completed",
					"generated plan approved", item.ToolCallID, "completed", map[string]any{
						"toolName": item.ToolName, "toolResult": result,
					}); err != nil {
					return resumed, err
				}
				resumed = true
				continue
			}
			if canonical, ok := toolcontract.CanonicalAskUser(item.ToolName); ok && canonical == toolcontract.AskUser {
				terminal, found, err := s.transcriptStore.FindFrameAskUserTerminalResult(
					ctx, transcriptstore.FindFrameAskUserTerminalResultInput{
						OwnerID: batch.OwnerUserID, FrameID: run.Transcript.Stream.FrameID,
						StreamUID: batch.StreamUID, BranchID: batch.BranchID,
						BranchGeneration: batch.BranchGeneration, ToolUseID: item.ToolCallID,
					},
				)
				if err != nil {
					return resumed, err
				}
				if found {
					encoded, err := transcriptstore.EncodeAskUserResultV1(terminal.Result)
					if err != nil {
						return resumed, err
					}
					result := map[string]any{}
					decoder := json.NewDecoder(strings.NewReader(string(encoded)))
					decoder.UseNumber()
					if err := decoder.Decode(&result); err != nil || result == nil || decoder.Decode(&struct{}{}) == nil {
						return resumed, errors.New("AskUser terminal result is invalid")
					}
					status, phase := "completed", "completed"
					if terminal.Result.Status == transcriptstore.AskUserStatusCancelled {
						status, phase = "cancelled", "cancelled"
					}
					if err := s.checkpointChatTool(options, run, status,
						fmt.Sprintf("tool %s %s", item.ToolName, status), item.ToolCallID, phase,
						map[string]any{"toolName": item.ToolName, "toolResult": result},
					); err != nil {
						return resumed, err
					}
					resumed = true
					continue
				}
				input := map[string]any{}
				decoder := json.NewDecoder(strings.NewReader(string(item.ArgumentsJSON)))
				decoder.UseNumber()
				if err := decoder.Decode(&input); err != nil || input == nil || decoder.Decode(&struct{}{}) == nil {
					return resumed, errors.New("AskUser input is invalid during recovery")
				}
				questions, err := askUserQuestionsForPersistenceFromToolInput(input)
				if err != nil {
					return resumed, fmt.Errorf("recover AskUser questions: %w", err)
				}
				// The tool waiting checkpoint and batch item are committed before
				// the Frame pending-input card. If the runner is interrupted in that
				// narrow window, replay the exact admitted AskUser payload so the
				// current authority can atomically finish parking it. This preserves
				// the strict Frame/stream checks in parkTranscriptAskUser instead of
				// weakening them for recovery.
				return resumed, transcriptAskUserPause(
					run.Transcript.Stream.SessionID, item.ToolCallID, canonical, questions,
				)
			}
			if approvedCallID == "" {
				return resumed, &agentruntime.PauseError{
					Status: "awaiting_user_response", Message: "Tool call is awaiting its durable user response.",
					Data: map[string]any{"batch_id": batch.BatchID, "tool_call_id": item.ToolCallID, "ordinal": item.Ordinal},
				}
			}
		}
		if item.State != workspace.ToolCallBatchItemStatePending && !resumingInterruptedReadOnlyCall {
			return resumed, workspace.ErrToolCallBatchConflict
		}
		call := agentruntime.ToolCall{
			ID: item.ToolCallID, Name: item.ToolName, Arguments: append(json.RawMessage(nil), item.ArgumentsJSON...),
			Resumed: resumingInterruptedReadOnlyCall,
		}
		toolCtx := withTranscriptRunnerChatRun(ctx, run)
		toolCtx = withWorkspaceMCPSourceEvidenceCollector(toolCtx)
		engine := s.newAgentRuntimeEngineWithContext(toolCtx, options)
		if approvedCallID != "" {
			engine.Tools = s.approvedRunnerToolGateway(run, item, approvedCallID)
		}
		engine.OnEventError = func(event agentruntime.Event) error {
			err := s.checkpointSessionRunnerToolEvent(toolCtx, options, run, event)
			return sessionRunnerToolProgressPersistenceBestEffort(event, err)
		}
		execution, err := engine.ExecuteToolBatch(toolCtx, []agentruntime.ToolCall{call}, 0, agentruntime.MediaPolicy{}, 0)
		if err != nil {
			return resumed, err
		}
		if execution.NextOrdinal != 1 {
			return resumed, errors.New("durable tool-call batch did not advance its exact ordinal")
		}
		resumed = true
	}
}

func sessionRunnerToolProgressPersistenceBestEffort(event agentruntime.Event, err error) error {
	if err == nil || event.Type != agentruntime.EventToolProgress {
		return err
	}
	// A progress checkpoint is observability, not execution authority. Start and
	// terminal checkpoints remain strict; losing one progress sample must not
	// cancel the live tool and turn a transient store race into task recovery.
	fmt.Fprintf(os.Stderr, "[runner] tool progress checkpoint skipped after a transient persistence error (err_type=%T)\n", err)
	return nil
}

func detachedKernelExecutionContinues(
	operation workspace.KernelLocalOperation,
	execution workspace.DetachedKernelExecution,
	found bool,
) bool {
	return found && operation.State == workspace.KernelLocalOperationStateStarted &&
		operation.ExecutionID != "" && execution.ExecutionID == operation.ExecutionID &&
		execution.OperationID == operation.OperationID &&
		execution.State != workspace.DetachedKernelExecutionStateEvidenceLost
}

func (s *Server) reconcileTerminalKernelToolCallWithoutProtocolReceipt(
	ctx context.Context,
	options SessionRunnerChatOptions,
	run *sessionRunnerChatRun,
	item workspace.ToolCallBatchItem,
	operation workspace.KernelLocalOperation,
) (bool, error) {
	if s == nil || s.workspaceStore == nil || run == nil || run.Transcript == nil ||
		!workspace.KernelLocalOperationIsTerminal(operation.State) {
		return false, nil
	}
	var result map[string]any
	materialized, found, err := s.workspaceStore.GetKernelToolResultMaterialization(ctx, operation.OperationID)
	if err != nil {
		return false, err
	}
	if found {
		if err := json.Unmarshal(materialized.TerminalResultJSON, &result); err != nil || result == nil {
			return false, fmt.Errorf("durable kernel terminal result is invalid for %s", item.ToolCallID)
		}
	} else if operation.ExecutionLogID != "" {
		result, err = s.replayAgentKernelOperationResult(operation)
		if err != nil {
			result = nil
		}
	}
	if result == nil {
		// Preserve truth: the execution reached a durable terminal state, but
		// its visible payload is unavailable. This is recoverable input for the
		// model and must not be mistaken for a successful scientific result.
		result = map[string]any{
			"sourceUnavailable": true,
			"status":            "unavailable",
			"code":              "kernel_tool_result_unavailable_after_recovery",
			"message":           "The kernel reached a terminal state but its visible result was unavailable after runner recovery.",
			"recovery":          "record the missing evidence and continue with an independent reproducible step",
		}
	}
	outcome := agentruntime.ClassifyToolResult(result)
	status, phase, message := "completed", "completed", fmt.Sprintf("tool %s recovered from its durable kernel result", item.ToolName)
	if outcome.HardFailed() {
		status, phase = "failed", "failed"
		message = fmt.Sprintf("tool %s failed; its durable kernel result was recovered", item.ToolName)
	} else if outcome != agentruntime.ToolResultSucceeded {
		message = fmt.Sprintf("tool %s returned a recoverable %s result after runner recovery", item.ToolName, outcome)
	}
	details := map[string]any{
		"toolName": item.ToolName, "toolInput": decodeToolEventJSON(string(item.ArgumentsJSON)), "toolResult": result,
	}
	s.bindTrustedScientificCompletion(run, details, nil)
	if err := s.checkpointChatTool(options, run, status, message, item.ToolCallID, phase, details); err != nil {
		return false, err
	}
	return true, nil
}

// restoreTrustedScientificStateForReconciledKernelOperation reconstructs the
// exact server-authored terminal result after a protocol receipt was committed
// by an earlier runner. It updates only the current in-memory completion state;
// the immutable terminal checkpoint remains the durable source of truth.
func (s *Server) restoreTrustedScientificStateForReconciledKernelOperation(
	run *sessionRunnerChatRun,
	item workspace.ToolCallBatchItem,
	operation workspace.KernelLocalOperation,
) error {
	if s == nil || run == nil || !workspace.KernelLocalOperationIsTerminal(operation.State) {
		return nil
	}
	var (
		result map[string]any
		err    error
	)
	if operation.ExecutionLogID != "" {
		result, err = s.replayAgentKernelOperationResult(operation)
	} else {
		result, err = s.kernelLocalOperationPersistedResult(operation)
	}
	if err != nil {
		return fmt.Errorf("restore trusted scientific state for kernel operation %s: %w", operation.OperationID, err)
	}
	details := map[string]any{
		"toolName": item.ToolName, "toolInput": decodeToolEventJSON(string(item.ArgumentsJSON)), "toolResult": result,
	}
	s.bindTrustedScientificCompletion(run, details, nil)
	return nil
}

func (s *Server) checkpointSessionRunnerToolEvent(
	ctx context.Context,
	options SessionRunnerChatOptions,
	run *sessionRunnerChatRun,
	event agentruntime.Event,
) error {
	switch event.Type {
	case agentruntime.EventToolStarted:
		return s.checkpointChatTool(options, run, "running", fmt.Sprintf("tool %s started", event.ToolName), event.ToolCallID, "start", map[string]any{
			"toolName": event.ToolName, "toolInput": decodeToolEventJSON(event.Arguments),
		})
	case agentruntime.EventToolProgress:
		message := fmt.Sprintf("tool %s is still running (elapsed %s)", event.ToolName, event.Elapsed.Round(time.Second))
		if event.Elapsed <= 0 {
			message = fmt.Sprintf("tool %s is still running", event.ToolName)
		}
		phase := fmt.Sprintf("progress-%06d", event.ProgressOrdinal)
		details := map[string]any{
			"toolName":        event.ToolName,
			"toolInput":       decodeToolEventJSON(event.Arguments),
			"toolProgress":    true,
			"progressOrdinal": event.ProgressOrdinal,
			"elapsedMs":       event.Elapsed.Milliseconds(),
		}
		if event.Progress != nil {
			details["progress"] = publicToolProgressPayload(*event.Progress, event.Elapsed)
		}
		return s.checkpointChatTool(options, run, "running", message, event.ToolCallID, phase, details)
	case agentruntime.EventToolCompleted:
		toolResult := structuredOnboardingToolAuditProjection(event.ToolName, decodeToolEventJSON(event.Result))
		if resultMap, _ := toolResult.(map[string]any); agentKernelPreflightResult(resultMap) {
			if err := s.completeKernelOperationPreflightForToolEvent(run, event.ToolCallID, resultMap); err != nil {
				return err
			}
		}
		requestedInput, executedInput, hasExecutedInput := runnerToolEventInputs(event)
		details := map[string]any{
			"toolName": event.ToolName, "toolInput": requestedInput, "toolResult": toolResult,
		}
		if hasExecutedInput {
			details["executedToolInput"] = executedInput
		}
		if event.RejectedBeforeExecution {
			details["rejectedBeforeExecution"] = true
		}
		if capabilities := run.toolCapabilities(event.ToolName); len(capabilities) > 0 {
			details["toolCapabilities"] = capabilities
		}
		sourceAttestation := attachWorkspaceMCPSourceEvidenceCheckpoint(ctx, details, event.ToolCallID)
		if event.ToolName == "skill" || event.ToolName == "Skill" {
			details["requiredScientificCapabilities"] = run.requiredScientificCapabilitiesSnapshot()
		}
		s.bindTrustedScientificCompletion(run, details, sourceAttestation)
		phase := runnerCompletedToolCheckpointPhase(event, toolResult)
		status := "completed"
		if phase == prestartToolFailurePhase {
			// A tool rejected before execution is a closed lifecycle, but it is
			// not a successful execution. Keep the durable status and phase
			// consistent so every transcript consumer observes one terminal
			// state instead of quarantining the whole conversation.
			status = "failed"
		}
		if phase == "completed" && !event.RejectedBeforeExecution {
			run.activateToolCapability(event.ToolName, stringArrayValue(details["toolCapabilities"])...)
		}
		if err := s.checkpointChatTool(options, run, status, fmt.Sprintf("tool %s %s", event.ToolName, status), event.ToolCallID, phase, details); err != nil {
			return err
		}
		if phase == "completed" && runnerToolCompletionHasMaterialProgress(event.ToolName, toolResult) {
			run.recordMaterialProgress()
		}
		if event.ToolName == "wait_for_notification" {
			expected, err := agentKernelNotificationClaimCount(event.Result)
			if err != nil {
				return err
			}
			return s.ackAgentKernelNotificationClaim(ctx, options.SessionID, event.ToolCallID, expected)
		}
		return nil
	case agentruntime.EventToolFailed:
		message := event.Message
		if message == "" {
			message = fmt.Sprintf("tool %s failed", event.ToolName)
		}
		phase := "failed"
		if event.RejectedBeforeExecution {
			phase = prestartToolFailurePhase
		}
		requestedInput, executedInput, hasExecutedInput := runnerToolEventInputs(event)
		details := map[string]any{
			"toolName": event.ToolName, "toolInput": requestedInput,
			"toolResult": decodeToolEventJSON(event.Result), "rejectedBeforeExecution": event.RejectedBeforeExecution,
		}
		if hasExecutedInput {
			details["executedToolInput"] = executedInput
		}
		// The attestation identifies a governed MCP invocation even when its
		// terminal result failed. It does not qualify the failed result as
		// evidence; it only lets durable process recovery retain the attempt.
		attachWorkspaceMCPSourceEvidenceCheckpoint(ctx, details, event.ToolCallID)
		return s.checkpointChatTool(options, run, "failed", message, event.ToolCallID, phase, details)
	case agentruntime.EventToolPaused:
		message := event.Message
		if message == "" {
			message = fmt.Sprintf("tool %s is awaiting user input", event.ToolName)
		}
		return s.checkpointChatTool(options, run, "waiting", message, event.ToolCallID, "waiting", map[string]any{
			"toolName": event.ToolName, "toolInput": decodeToolEventJSON(event.Arguments),
			"toolResult": decodeToolEventJSON(event.Result),
		})
	}
	return nil
}

func attachWorkspaceMCPSourceEvidenceCheckpoint(
	ctx context.Context,
	details map[string]any,
	toolCallID string,
) *workspaceMCPSourceEvidenceAttestation {
	attestation, found := takeWorkspaceMCPSourceEvidence(ctx, toolCallID)
	if !found || details == nil {
		return nil
	}
	requestJSON := mustMarshalRawMessage(runnerCheckpointExecutedToolInput(details))
	resultJSON := mustMarshalRawMessage(details["toolResult"])
	details["schema"] = attestation.Schema
	details["evidenceClass"] = attestation.EvidenceClass
	details["connectorId"] = attestation.ConnectorID
	details["connectorSource"] = attestation.ConnectorSource
	details["inputSchemaSha256"] = attestation.InputSchemaSHA256
	details["readOnlyHint"] = attestation.ReadOnlyHint
	details["requestSha256"] = kernelMCPEvidenceSHA256(requestJSON)
	details["resultSha256"] = kernelMCPEvidenceSHA256(resultJSON)
	return &attestation
}

func runnerToolEventInputs(event agentruntime.Event) (requested any, executed any, hasExecuted bool) {
	requested = decodeToolEventJSON(event.Arguments)
	raw := strings.TrimSpace(event.ExecutedArguments)
	if raw == "" {
		return requested, nil, false
	}
	executed = decodeToolEventJSON(raw)
	requestedJSON, requestedErr := json.Marshal(requested)
	executedJSON, executedErr := json.Marshal(executed)
	if requestedErr != nil || executedErr != nil || string(requestedJSON) == string(executedJSON) {
		return requested, nil, false
	}
	return requested, executed, true
}

func runnerCheckpointExecutedToolInput(details map[string]any) any {
	if executed, present := details["executedToolInput"]; present {
		return executed
	}
	return details["toolInput"]
}

func runnerCompletedToolCheckpointPhase(event agentruntime.Event, toolResult any) string {
	if !event.RejectedBeforeExecution {
		return "completed"
	}
	result, _ := toolResult.(map[string]any)
	code := strings.ToLower(strings.TrimSpace(firstNonEmpty(
		stringValue(result["code"]), stringValue(result["status"]),
	)))
	// Ownership routing is a successful Harness decision: the requested user
	// prompt was deliberately not executed, and the model received a closed tool
	// result telling it to continue autonomously. Recording that private routing
	// decision as a failed public tool corrupts failure-rate metrics and can make
	// an otherwise recoverable completion unit terminally fail.
	if code == "agent_owned_decision" && result["executed"] == false {
		return "completed"
	}
	return prestartToolFailurePhase
}

type runnerToolBatchCheckpointMutation func(
	context.Context,
	*transcriptstore.ImmediateTransaction,
	transcriptstore.Event,
) error

func (s *Server) bindRunnableToolCallBatches(
	ctx context.Context,
	run *sessionRunnerChatRun,
) error {
	if s == nil || s.workspaceStore == nil || run == nil || run.Transcript == nil {
		return nil
	}
	batches, err := s.workspaceStore.ListRunnableToolCallBatches(ctx, run.Transcript.Claim, 100)
	if err != nil {
		return err
	}
	if run.ToolBatchIDs == nil {
		run.ToolBatchIDs = map[string]string{}
		run.ToolBatchOrdinals = map[string]int64{}
	}
	for _, batch := range batches {
		claimed, err := s.workspaceStore.ClaimToolCallBatch(ctx, workspace.ClaimToolCallBatchInput{
			Claim: run.Transcript.Claim, BatchID: batch.BatchID, ExpectedStateVersion: batch.StateVersion,
		})
		if err != nil {
			if errors.Is(err, workspace.ErrToolCallBatchConflict) || errors.Is(err, workspace.ErrToolCallBatchStale) {
				fmt.Fprintf(os.Stderr, "[runner] tool batch %s is not claimable at resume: %v; continuing\n", batch.BatchID, err)
				continue
			}
			return err
		}
		items, err := s.workspaceStore.ListToolCallBatchItems(ctx, claimed.OwnerUserID, claimed.BatchID)
		if err != nil {
			return err
		}
		for _, item := range items {
			if existing := run.ToolBatchIDs[item.ToolCallID]; existing != "" && existing != claimed.BatchID {
				fmt.Fprintf(os.Stderr, "[runner] tool call %s already bound to batch %s; skipping batch %s binding\n",
					item.ToolCallID, existing, claimed.BatchID)
				continue
			}
			run.ToolBatchIDs[item.ToolCallID] = claimed.BatchID
			run.ToolBatchOrdinals[item.ToolCallID] = item.Ordinal
			if item.StartedEventID > 0 {
				if run.ToolSourceEventIDs == nil {
					run.ToolSourceEventIDs = map[string]int64{}
				}
				run.ToolSourceEventIDs[item.ToolCallID] = item.StartedEventID
			}
		}
	}
	return nil
}

func (s *Server) prepareRunnerToolBatchCheckpointMutation(
	ctx context.Context,
	run *sessionRunnerChatRun,
	toolCallID, phase string,
) (runnerToolBatchCheckpointMutation, error) {
	if s == nil || s.workspaceStore == nil || run == nil || run.Transcript == nil {
		return nil, nil
	}
	toolCallID = strings.TrimSpace(toolCallID)
	batchID := strings.TrimSpace(run.ToolBatchIDs[toolCallID])
	ordinal, hasOrdinal := run.ToolBatchOrdinals[toolCallID]
	if toolCallID == "" || batchID == "" || !hasOrdinal {
		return nil, nil
	}
	batch, found, err := s.workspaceStore.GetToolCallBatch(ctx, run.Transcript.Stream.OwnerID, batchID)
	if err != nil {
		return nil, err
	}
	if !found {
		fmt.Fprintf(os.Stderr, "[runner] tool batch %s is missing for checkpoint %s; skipping durable mutation\n", batchID, toolCallID)
		return nil, nil
	}
	items, err := s.workspaceStore.ListToolCallBatchItems(ctx, run.Transcript.Stream.OwnerID, batchID)
	if err != nil {
		return nil, err
	}
	var item workspace.ToolCallBatchItem
	itemFound := false
	for _, candidate := range items {
		if candidate.Ordinal == ordinal && candidate.ToolCallID == toolCallID {
			item, itemFound = candidate, true
			break
		}
	}
	if !itemFound {
		fmt.Fprintf(os.Stderr, "[runner] tool batch %s item %d (%s) is missing; skipping durable mutation\n", batchID, ordinal, toolCallID)
		return nil, nil
	}
	if batch.NextOrdinal != ordinal {
		fmt.Fprintf(os.Stderr, "[runner] tool batch %s already advanced past ordinal %d (next=%d); skipping durable mutation\n",
			batchID, ordinal, batch.NextOrdinal)
		return nil, nil
	}
	claim := run.Transcript.Claim
	if strings.HasPrefix(phase, "progress-") {
		// Progress checkpoints are durable observability events only. The start
		// checkpoint already moved the batch item to running, and the terminal
		// checkpoint will settle it. Treating progress as an unsupported batch
		// transition interrupts every tool that outlives the progress interval.
		return nil, nil
	}
	switch phase {
	case "approval_resumed":
		return func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event) error {
			_, _, err := s.workspaceStore.ValidateToolCallBatchResumeTx(ctx, tx, workspace.StartToolCallBatchItemInput{
				Claim: claim, BatchID: batch.BatchID, Ordinal: item.Ordinal,
				ExpectedBatchStateVersion: batch.StateVersion, ExpectedItemStateVersion: item.StateVersion, StartedEvent: event,
			})
			return err
		}, nil
	case "start":
		if batch.State != workspace.ToolCallBatchStateReady || item.State != workspace.ToolCallBatchItemStatePending {
			fmt.Fprintf(os.Stderr, "[runner] tool batch %s start checkpoint for %s is already durable (batch=%s item=%s); skipping\n",
				batchID, toolCallID, batch.State, item.State)
			return nil, nil
		}
		return func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event) error {
			_, _, err := s.workspaceStore.StartToolCallBatchItemTx(ctx, tx, workspace.StartToolCallBatchItemInput{
				Claim: claim, BatchID: batch.BatchID, Ordinal: item.Ordinal,
				ExpectedBatchStateVersion: batch.StateVersion, ExpectedItemStateVersion: item.StateVersion,
				StartedEvent: event,
			})
			return err
		}, nil
	case "waiting":
		if batch.State != workspace.ToolCallBatchStateRunning || item.State != workspace.ToolCallBatchItemStateRunning {
			fmt.Fprintf(os.Stderr, "[runner] tool batch %s waiting checkpoint for %s is not actionable (batch=%s item=%s); skipping\n",
				batchID, toolCallID, batch.State, item.State)
			return nil, nil
		}
		return func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event) error {
			_, _, err := s.workspaceStore.WaitToolCallBatchItemTx(ctx, tx, workspace.WaitToolCallBatchItemInput{
				Claim: claim, BatchID: batch.BatchID, Ordinal: item.Ordinal,
				ExpectedBatchStateVersion: batch.StateVersion, ExpectedItemStateVersion: item.StateVersion,
				WaitingEvent: event,
			})
			return err
		}, nil
	case prestartToolFailurePhase:
		pendingRejection := batch.State == workspace.ToolCallBatchStateReady &&
			item.State == workspace.ToolCallBatchItemStatePending
		startedRejection := batch.State == workspace.ToolCallBatchStateRunning &&
			item.State == workspace.ToolCallBatchItemStateRunning
		// Approval resumption deliberately leaves the batch in waiting until
		// its exact receipt settles. A native rejection must close that same
		// transaction instead of recording an orphan terminal checkpoint.
		waitingRejection := batch.State == workspace.ToolCallBatchStateWaiting &&
			item.State == workspace.ToolCallBatchItemStateWaiting
		if !pendingRejection && !startedRejection && !waitingRejection {
			fmt.Fprintf(os.Stderr, "[runner] tool batch %s pre-start failure for %s is not actionable (batch=%s item=%s); skipping\n",
				batchID, toolCallID, batch.State, item.State)
			return nil, nil
		}
		return func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event) error {
			_, _, err := s.workspaceStore.FinishToolCallBatchItemTx(ctx, tx, workspace.FinishToolCallBatchItemInput{
				Claim: claim, BatchID: batch.BatchID, Ordinal: item.Ordinal,
				ExpectedBatchStateVersion: batch.StateVersion, ExpectedItemStateVersion: item.StateVersion,
				TerminalEvent: event, TerminalState: "failed", TerminalPhase: phase,
				RejectedBeforeExecution: true,
			})
			return err
		}, nil
	case "completed", "failed", "blocked", "cancelled", "outcome_unknown":
		if item.State == workspace.ToolCallBatchItemStateCompleted ||
			item.State == workspace.ToolCallBatchItemStateFailed ||
			item.State == workspace.ToolCallBatchItemStateBlocked ||
			item.State == workspace.ToolCallBatchItemStateCancelled ||
			item.State == workspace.ToolCallBatchItemStateOutcomeUnknown {
			fmt.Fprintf(os.Stderr, "[runner] tool batch %s terminal checkpoint for %s is already durable (item=%s); skipping\n",
				batchID, toolCallID, item.State)
			return nil, nil
		}
		if (batch.State != workspace.ToolCallBatchStateRunning && batch.State != workspace.ToolCallBatchStateWaiting) ||
			(item.State != workspace.ToolCallBatchItemStateRunning && item.State != workspace.ToolCallBatchItemStateWaiting) {
			fmt.Fprintf(os.Stderr, "[runner] tool batch %s terminal checkpoint for %s is not actionable (batch=%s item=%s); skipping\n",
				batchID, toolCallID, batch.State, item.State)
			return nil, nil
		}
		return func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event) error {
			_, _, err := s.workspaceStore.FinishToolCallBatchItemTx(ctx, tx, workspace.FinishToolCallBatchItemInput{
				Claim: claim, BatchID: batch.BatchID, Ordinal: item.Ordinal,
				ExpectedBatchStateVersion: batch.StateVersion, ExpectedItemStateVersion: item.StateVersion,
				TerminalEvent: event, TerminalState: phase,
			})
			return err
		}, nil
	default:
		return nil, errors.New("tool batch checkpoint phase is unsupported")
	}
}
