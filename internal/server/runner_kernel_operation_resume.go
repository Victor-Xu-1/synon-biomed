package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"synon-go/internal/agentruntime"
	workspace "synon-go/internal/persistence/workspace"
)

// resumeApprovedAgentKernelOperations settles durable local tool calls before
// the runner asks the model for another response. This closes the protocol
// gap after an approval/restart: the original assistant tool call receives one
// exact durable tool result instead of being regenerated or guessed.
func (s *Server) resumeApprovedAgentKernelOperations(
	ctx context.Context,
	options SessionRunnerChatOptions,
	run *sessionRunnerChatRun,
) (bool, error) {
	if s == nil || s.workspaceStore == nil || run == nil || run.Transcript == nil {
		return false, nil
	}
	if err := s.bindRunnableToolCallBatches(ctx, run); err != nil {
		return false, err
	}
	resumed := false
	for {
		operations, err := s.workspaceStore.ListRunnableKernelLocalOperations(ctx, run.Transcript.Claim, 100)
		if err != nil {
			return resumed, err
		}
		if len(operations) == 0 {
			break
		}
		madeProgress := false
		for _, operation := range operations {
			if err := ctx.Err(); err != nil {
				return resumed, err
			}
			if operation.State == workspace.KernelLocalOperationStateStarted {
				operation, err = s.waitForStartedKernelLocalOperation(ctx, operation)
				if err != nil {
					return resumed, err
				}
			}
			if run.KernelOperationIDs == nil {
				run.KernelOperationIDs = make(map[string]string, len(operations))
			}
			if existing := run.KernelOperationIDs[operation.ToolCallID]; existing != "" && existing != operation.OperationID {
				return resumed, errors.New("kernel operation tool-call identity conflicts with the active claim")
			}
			run.KernelOperationIDs[operation.ToolCallID] = operation.OperationID
			batchID := strings.TrimSpace(run.ToolBatchIDs[operation.ToolCallID])
			batchOrdinal, hasBatchOrdinal := run.ToolBatchOrdinals[operation.ToolCallID]
			if batchID == "" || !hasBatchOrdinal {
				return resumed, errors.New("kernel operation is not bound to a durable tool-call batch")
			}
			batch, found, err := s.workspaceStore.GetToolCallBatch(ctx, operation.OwnerUserID, batchID)
			if err != nil {
				return resumed, err
			}
			if !found {
				return resumed, workspace.ErrToolCallBatchConflict
			}
			if batch.NextOrdinal != batchOrdinal {
				continue
			}
			items, err := s.workspaceStore.ListToolCallBatchItems(ctx, batch.OwnerUserID, batch.BatchID)
			if err != nil {
				return resumed, err
			}
			var batchItem workspace.ToolCallBatchItem
			itemFound := false
			for _, item := range items {
				if item.Ordinal == batchOrdinal && item.ToolCallID == operation.ToolCallID {
					batchItem, itemFound = item, true
					break
				}
			}
			if !itemFound {
				return resumed, workspace.ErrToolCallBatchConflict
			}
			needsStartCheckpoint := true
			switch {
			case batch.State == workspace.ToolCallBatchStateReady &&
				batchItem.State == workspace.ToolCallBatchItemStatePending:
			case batch.State == workspace.ToolCallBatchStateRunning &&
				batchItem.State == workspace.ToolCallBatchItemStateRunning:
				// A pre-start infrastructure failure leaves the original start
				// checkpoint authoritative. Re-execution resumes the same item;
				// it must not manufacture a second start event.
				needsStartCheckpoint = false
			case batch.State == workspace.ToolCallBatchStateWaiting &&
				batchItem.State == workspace.ToolCallBatchItemStateWaiting:
			default:
				return resumed, workspace.ErrToolCallBatchConflict
			}
			decodeInput := func(raw []byte) (map[string]any, error) {
				decoder := json.NewDecoder(bytes.NewReader(raw))
				decoder.UseNumber()
				input := map[string]any{}
				if err := decoder.Decode(&input); err != nil || input == nil || decoder.Decode(&struct{}{}) == nil {
					return nil, errors.New("kernel operation input is invalid")
				}
				return input, nil
			}
			input, err := decodeInput(operation.InputJSON)
			if err != nil {
				return resumed, err
			}
			// Execution uses the admitted canonical operation input, while public
			// lifecycle checkpoints retain the immutable model-call arguments from
			// the durable batch. Admission may remove explicit default values; using
			// that normalized input in a resumed start would make one tool call look
			// like two conflicting calls to the transcript projector.
			checkpointInput, err := decodeInput(batchItem.ArgumentsJSON)
			if err != nil {
				return resumed, errors.New("kernel operation source tool input is invalid")
			}
			call := agentruntime.ToolCall{
				ID: operation.ToolCallID, Name: operation.Tool,
				Arguments: append(json.RawMessage(nil), operation.InputJSON...),
			}
			var result map[string]any
			if operation.State == workspace.KernelLocalOperationStateApproved {
				// Recovery is driven by the durable operation, not by the
				// in-memory/session alias that happened to create it. A resumed
				// Frame can legitimately have a different session identifier after
				// a restart; the operation's Frame authority is the canonical
				// boundary for re-establishing the kernel identity.
				identity := s.resolveAgentKernelContext(ctx, operation.FrameID)
				if identity == nil {
					return resumed, errors.New("kernel operation resume identity is unavailable")
				}
				if needsStartCheckpoint {
					if err := s.checkpointChatTool(options, run, "running",
						fmt.Sprintf("tool %s resumed", operation.Tool), call.ID, "start", map[string]any{
							"toolName": operation.Tool, "toolInput": checkpointInput, "recoveryReplay": true,
						}); err != nil {
						return resumed, err
					}
				}
				recoveryTimeout, timeoutErr := kernelLocalOperationRecoveryTimeout(operation.Tool, input)
				if timeoutErr != nil {
					return resumed, timeoutErr
				}
				operationCtx := ctx
				cancelOperation := func() {}
				if recoveryTimeout > 0 {
					operationCtx, cancelOperation = context.WithTimeoutCause(
						ctx, recoveryTimeout, errSessionRunnerKernelRecoveryDeadline,
					)
				}
				recoveryCtx := withTranscriptRunnerChatRun(operationCtx, run)
				result, err = s.executeAgentKernelToolWithApproval(
					recoveryCtx, identity, call, operation.Tool, input,
					options.OutputLimitBytes, options.AllowedTools,
				)
				if recoveryTimeout > 0 && err != nil && errors.Is(operationCtx.Err(), context.DeadlineExceeded) {
					err = sessionRunnerKernelRecoveryTimeout{
						timeout: recoveryTimeout,
						cause:   errSessionRunnerKernelRecoveryDeadline,
					}
				}
				cancelOperation()
				if err == nil && agentKernelPreflightResult(result) {
					// A worker-side preflight runs inside the ordinary kernel lifecycle
					// and may have already completed the operation before execute returns.
					// Reconcile against the current durable head instead of attempting a
					// second transition from this loop's stale approved snapshot.
					current, handled, reconcileErr := s.reconcileKernelOperationPreflightResult(
						ctx, run, operation.ToolCallID, result,
					)
					if reconcileErr != nil {
						return resumed, reconcileErr
					}
					if !handled {
						return resumed, workspace.ErrKernelLocalOperationConflict
					}
					operation = current
				}
				if err != nil {
					current, found, loadErr := s.workspaceStore.GetKernelLocalOperation(
						ctx, operation.OwnerUserID, operation.OperationID,
					)
					if loadErr != nil {
						return resumed, loadErr
					}
					if !found {
						return resumed, workspace.ErrKernelLocalOperationConflict
					}
					operation = current
					if operation.State == workspace.KernelLocalOperationStateApproved &&
						!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) &&
						!errors.Is(err, ErrGenerationStopped) {
						result = agentRuntimeToolErrorValue(err)
						terminalResult, encodeErr := json.Marshal(result)
						if encodeErr != nil {
							return resumed, encodeErr
						}
						operation, encodeErr = s.workspaceStore.FailApprovedKernelLocalOperation(
							ctx, workspace.FailApprovedKernelLocalOperationInput{
								OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
								ExpectedStateVersion: operation.StateVersion, Claim: run.Transcript.Claim,
								ReasonCode: "execution_preflight_failed", TerminalResultJSON: terminalResult,
							},
						)
						if encodeErr != nil {
							return resumed, encodeErr
						}
						if checkpointErr := s.checkpointChatTool(options, run, "failed",
							fmt.Sprintf("tool %s failed: %v", operation.Tool, err), call.ID, "failed", map[string]any{
								"toolName": operation.Tool, "toolInput": checkpointInput, "toolResult": result,
							}); checkpointErr != nil {
							return resumed, checkpointErr
						}
						resumed = true
						madeProgress = true
						continue
					}
					if checkpointErr := s.checkpointChatTool(options, run, "failed",
						fmt.Sprintf("tool %s failed: %v", operation.Tool, err), call.ID, "failed", map[string]any{
							"toolName": operation.Tool, "toolInput": checkpointInput,
							"toolResult": result,
						}); checkpointErr != nil {
						return resumed, errors.Join(err, checkpointErr)
					}
					return resumed, err
				}
				var found bool
				operation, found, err = s.workspaceStore.GetKernelLocalOperation(
					ctx, operation.OwnerUserID, operation.OperationID,
				)
				if err != nil {
					return resumed, err
				}
				if !found || operation.State == workspace.KernelLocalOperationStateApproved ||
					operation.State == workspace.KernelLocalOperationStatePrepared {
					return resumed, &kernelLocalOperationPendingRecoveryError{
						operationID: operation.OperationID,
						state:       operation.State,
						cause: fmt.Errorf(
							"kernel operation execution is pending durable recovery in state %s",
							operation.State,
						),
					}
				}
				foregroundPending, pendingErr := recoveredKernelForegroundResultPending(operation, result)
				if pendingErr != nil {
					return resumed, pendingErr
				}
				if foregroundPending {
					// A foreground wait window is only a UI responsiveness boundary.
					// Keep the authoritative runner lease and heartbeat alive while the
					// detached execution progresses. Releasing the lease here lets every
					// runner worker reclaim the same checkpoint and creates a recovery
					// loop even though the original operation is healthy. The durable
					// operation transition is the wake signal, so this wait has no task
					// duration ceiling and never re-executes the tool call.
					operation, err = s.waitForStartedKernelLocalOperation(ctx, operation)
					if err != nil {
						return resumed, err
					}
				}
				if workspace.KernelLocalOperationIsTerminal(operation.State) {
					if err := s.ensureKernelToolResultMaterialization(
						withTranscriptRunnerChatRun(ctx, run), operation, options.OutputLimitBytes,
					); err != nil {
						return resumed, err
					}
					result, err = s.kernelLocalOperationPersistedResult(operation)
					if err != nil {
						return resumed, err
					}
				}
			} else {
				if workspace.KernelLocalOperationIsTerminal(operation.State) {
					if err := s.ensureKernelToolResultMaterialization(
						withTranscriptRunnerChatRun(ctx, run), operation, options.OutputLimitBytes,
					); err != nil {
						return resumed, err
					}
				}
				result, err = s.kernelLocalOperationPersistedResult(operation)
				if err != nil {
					return resumed, err
				}
			}
			status, phase := recoveredKernelToolCheckpointStatus(operation, result)
			details := map[string]any{
				"toolName": operation.Tool, "toolInput": checkpointInput, "toolResult": result,
			}
			s.bindTrustedScientificCompletion(run, details, nil)
			if err := s.checkpointChatTool(options, run, status,
				fmt.Sprintf("tool %s %s", operation.Tool, status), call.ID, phase, details); err != nil {
				return resumed, fmt.Errorf("commit resumed kernel protocol result: %w", err)
			}
			resumed = true
			madeProgress = true
		}
		if !madeProgress {
			break
		}
	}
	continued, err := s.resumePendingAgentToolCalls(ctx, options, run)
	return resumed || continued, err
}

// waitForStartedKernelLocalOperation retains one runner owner while an exact
// durable foreground operation is still executing. KernelRetentionWake is
// captured before every read, closing the query-to-wait race without polling.
// Process drain or user cancellation cancels ctx; a restarted runner then
// re-enters this wait against the same operation identity.
func (s *Server) waitForStartedKernelLocalOperation(
	ctx context.Context,
	operation workspace.KernelLocalOperation,
) (workspace.KernelLocalOperation, error) {
	if s == nil || s.workspaceStore == nil || strings.TrimSpace(operation.OperationID) == "" ||
		strings.TrimSpace(operation.OwnerUserID) == "" {
		return workspace.KernelLocalOperation{}, errors.New("started kernel operation wait authority is unavailable")
	}
	for {
		wake := s.workspaceStore.KernelRetentionWake()
		current, found, err := s.workspaceStore.GetKernelLocalOperation(
			ctx, operation.OwnerUserID, operation.OperationID,
		)
		if err != nil {
			return workspace.KernelLocalOperation{}, err
		}
		if !found || current.StreamUID != operation.StreamUID ||
			current.ToolCallID != operation.ToolCallID {
			return workspace.KernelLocalOperation{}, workspace.ErrKernelLocalOperationConflict
		}
		if workspace.KernelLocalOperationIsTerminal(current.State) {
			return current, nil
		}
		if current.State != workspace.KernelLocalOperationStateStarted {
			return workspace.KernelLocalOperation{}, fmt.Errorf(
				"started kernel operation changed to unsupported state %s", current.State,
			)
		}
		select {
		case <-ctx.Done():
			return workspace.KernelLocalOperation{}, context.Cause(ctx)
		case <-wake:
		}
	}
}

func recoveredKernelToolCheckpointStatus(
	operation workspace.KernelLocalOperation,
	result map[string]any,
) (string, string) {
	succeeded := agentruntime.ClassifyToolResult(result) == agentruntime.ToolResultSucceeded
	if operation.State == workspace.KernelLocalOperationStateStarted ||
		(operation.State == workspace.KernelLocalOperationStateCompleted && succeeded) ||
		(operation.State == workspace.KernelLocalOperationStateCancelled &&
			agentKernelPreflightStatus(operation.ReasonCode) && succeeded) {
		return "completed", "completed"
	}
	return "failed", "failed"
}

// recoveredKernelForegroundResultPending distinguishes a foreground execution
// that merely exceeded the bounded UI wait from a model-requested background
// execution. Both return a provisional running value, but only the explicit
// background call may publish that value as its protocol result. Foreground
// work must wait for the observer's durable terminal materialization.
func recoveredKernelForegroundResultPending(
	operation workspace.KernelLocalOperation,
	result map[string]any,
) (bool, error) {
	if operation.State != workspace.KernelLocalOperationStateStarted {
		return false, nil
	}
	if strings.TrimSpace(stringValue(result["status"])) != "running" {
		return false, errors.New("kernel operation execution remained started without a background result")
	}
	backgroundRequested, err := kernelLocalOperationBackgroundRequested(operation)
	if err != nil {
		return false, err
	}
	return !backgroundRequested, nil
}

// kernelLocalOperationRecoveryTimeout preserves the ordinary bounded recovery
// floor while letting software_runtime own its declared wall-clock budget.
// Provisioning and execution can each consume timeout_seconds, so recovery
// must cover both phases plus the detached-runtime settlement grace. This is
// provider-independent orchestration: the selected software provider and
// immutable plan remain unchanged throughout recovery.
func kernelLocalOperationRecoveryTimeout(tool string, input map[string]any) (time.Duration, error) {
	budget := defaultSessionRunnerKernelRecoveryTimeout
	if strings.TrimSpace(tool) != softwareRuntimeToolName {
		return budget, nil
	}
	request, err := decodeSoftwareRuntimeRequest(input)
	if err != nil {
		return 0, err
	}
	if request.TimeoutSeconds == 0 {
		return 0, nil
	}
	declared := time.Duration(request.TimeoutSeconds) * time.Second
	softwareBudget := 2*declared + softwareRuntimeExecutionTimeoutGrace
	if softwareBudget > budget {
		budget = softwareBudget
	}
	return budget, nil
}

func agentKernelPreflightResult(result map[string]any) bool {
	return agentKernelPreflightReasonCode(result) != ""
}

// agentKernelPreflightReasonCode recognizes the same non-executing correction
// after either its original server result or the failed-call guard's bounded
// terminal envelope. The guard preserves executed=false but promotes status to
// code; durable kernel settlement must therefore consult both fields or a
// rejected call remains approved and is executed again on resume.
func agentKernelPreflightReasonCode(result map[string]any) string {
	if result == nil {
		return ""
	}
	if status := strings.TrimSpace(stringValue(result["status"])); agentKernelPreflightStatus(status) {
		return status
	}
	if !boolValue(result["executed"], true) {
		if code := strings.TrimSpace(stringValue(result["code"])); agentKernelPreflightStatus(code) {
			return code
		}
	}
	return ""
}

func (s *Server) completeKernelOperationPreflightForToolEvent(
	run *sessionRunnerChatRun,
	toolCallID string,
	result map[string]any,
) error {
	_, _, err := s.reconcileKernelOperationPreflightResult(
		context.Background(), run, toolCallID, result,
	)
	return err
}

// reconcileKernelOperationPreflightResult is the single settlement path for
// non-executing server preflights and worker-side preflights that traversed the
// normal kernel lifecycle. It always reloads the durable operation head: an
// already-terminal operation is authoritative and must never be transitioned
// again from an older in-memory state version.
func (s *Server) reconcileKernelOperationPreflightResult(
	ctx context.Context,
	run *sessionRunnerChatRun,
	toolCallID string,
	result map[string]any,
) (workspace.KernelLocalOperation, bool, error) {
	if !agentKernelPreflightResult(result) || run == nil {
		return workspace.KernelLocalOperation{}, false, nil
	}
	operationID := strings.TrimSpace(run.KernelOperationIDs[toolCallID])
	if operationID == "" {
		return workspace.KernelLocalOperation{}, false, nil
	}
	if s == nil || s.workspaceStore == nil || run.Transcript == nil {
		return workspace.KernelLocalOperation{}, false,
			errors.New("workspace and Transcript authority are required for kernel preflight settlement")
	}
	operation, found, err := s.workspaceStore.GetKernelLocalOperation(
		ctx, run.Transcript.Stream.OwnerID, operationID,
	)
	if err != nil {
		return workspace.KernelLocalOperation{}, false, err
	}
	if !found || operation.StreamUID != run.Transcript.Stream.UID || operation.ToolCallID != toolCallID {
		return workspace.KernelLocalOperation{}, false, workspace.ErrKernelLocalOperationConflict
	}
	if workspace.KernelLocalOperationIsTerminal(operation.State) {
		return operation, true, nil
	}
	if operation.State != workspace.KernelLocalOperationStatePendingApproval &&
		operation.State != workspace.KernelLocalOperationStateApproved {
		return workspace.KernelLocalOperation{}, false, workspace.ErrKernelLocalOperationConflict
	}
	terminalResult, err := json.Marshal(result)
	if err != nil {
		return workspace.KernelLocalOperation{}, false, err
	}
	operation, err = s.workspaceStore.CompleteKernelLocalOperationPreflight(
		ctx, workspace.CompleteKernelLocalOperationPreflightInput{
			OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
			ExpectedStateVersion: operation.StateVersion, ExpectedState: operation.State,
			Claim: run.Transcript.Claim, ReasonCode: agentKernelPreflightReasonCode(result),
			TerminalResultJSON: terminalResult,
		},
	)
	if err != nil {
		return workspace.KernelLocalOperation{}, false, err
	}
	return operation, true, nil
}

func agentKernelPreflightStatus(status string) bool {
	status = strings.TrimSpace(status)
	if strings.HasSuffix(status, "_preflight_required") {
		return true
	}
	return status == "governed_compute_required"
}

func (s *Server) kernelLocalOperationPersistedResult(operation workspace.KernelLocalOperation) (map[string]any, error) {
	if s != nil && s.workspaceStore != nil {
		materialized, found, err := s.workspaceStore.GetKernelToolResultMaterialization(
			context.Background(), operation.OperationID,
		)
		if err != nil {
			return nil, err
		}
		if found {
			decoder := json.NewDecoder(bytes.NewReader(materialized.TerminalResultJSON))
			decoder.UseNumber()
			result := map[string]any{}
			if err := decoder.Decode(&result); err != nil || result == nil || decoder.Decode(&struct{}{}) == nil {
				return nil, errors.New("kernel operation materialized result is invalid")
			}
			return result, nil
		}
	}
	return nil, errors.New("kernel operation terminal materialization is unavailable")
}
