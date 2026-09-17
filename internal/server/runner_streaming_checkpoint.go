package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"synon-go/internal/agentruntime"
	"synon-go/internal/kernelcontract"
	eventjournal "synon-go/internal/persistence/journal"

	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) checkpointChatContentDelta(
	ctx context.Context,
	options SessionRunnerChatOptions,
	run *sessionRunnerChatRun,
	delta string,
	index int,
) error {
	if run == nil || delta == "" {
		return nil
	}
	if ctx == nil {
		return errors.New("model content delta context is required")
	}
	phase := fmt.Sprintf("chat-delta-%06d", index)
	event, err := s.appendTranscriptRunnerEvent(ctx, run.Transcript, "content_delta", phase, map[string]any{
		"text": delta, "delta_index": index,
		"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(run.assistantSegmentOrdinal(), ""),
	})
	if err != nil {
		return err
	}
	if run.Transcript != nil {
		run.AssistantSegmentOrdinal = run.assistantSegmentOrdinal()
		run.AssistantSegmentHasContent = true
		run.AssistantSegmentContent.WriteString(delta)
		run.AfterEventID = maxInt64(run.AfterEventID, event.EventID)
		run.recordProviderAcceptedContent(delta, event)
		return nil
	}
	if s.sessionStore == nil || s.eventJournal == nil {
		return errors.New("session streaming persistence is not configured")
	}
	claim := sessionstore.RunnerMutationClaim{
		SessionID: run.SessionID, RunnerID: options.RunnerID, Attempt: run.Attempt, ClaimToken: run.ClaimToken,
	}
	session, err := s.sessionStore.ValidateRunnerClaim(claim, true)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	clientMessageID := runnerCommandClientMessageID(
		options.RunnerID, run.SessionID, run.Attempt, run.ClaimToken, phase,
	)
	if existing, found, err := s.existingRunnerEventByClientMessageID(
		run.SessionID, clientMessageID, "content_delta", options.RunnerID,
	); err != nil {
		return err
	} else if found {
		if stringValue(existing.Message["text"]) != delta {
			return fmt.Errorf("content delta %d replay does not match persisted text", index)
		}
		run.AfterEventID = maxInt64(run.AfterEventID, existing.EventID)
		run.AssistantSegmentHasContent = true
		return nil
	}
	message := eventjournal.Message{
		"type": "content_delta", "role": "assistant", "runnerId": options.RunnerID,
		"text": delta, "deltaIndex": index,
	}
	addRunnerLeaseAuditToMessage(message, session.Runner)
	if run.AfterEventID > 0 {
		message["afterEventId"] = run.AfterEventID
	}
	entry, _, err := s.eventJournal.AppendIdempotent(run.SessionID, message, eventjournal.Metadata{
		ClientMessageID: clientMessageID,
	})
	if err != nil {
		return err
	}
	if entry == nil {
		return errors.New("model content delta was not persisted")
	}
	if _, err := s.sessionStore.CheckpointRunner(sessionstore.CheckpointRunnerInput{
		Claim: claim, Status: "running", EventID: entry.EventID, RecordedAt: now,
	}); err != nil {
		return err
	}
	run.AfterEventID = entry.EventID
	run.AssistantSegmentHasContent = true
	run.AssistantSegmentContent.WriteString(delta)
	s.publishSessionEntry(entry)
	return nil
}

func (s *Server) checkpointChatModelToolCalls(options SessionRunnerChatOptions, run *sessionRunnerChatRun, calls []agentruntime.ToolCall) error {
	if run == nil || len(calls) == 0 {
		return nil
	}
	payload := make([]any, 0, len(calls))
	kernelCallCount := 0
	runtimeRecoveredCount := 0
	for _, originalCall := range calls {
		call := s.sanitizeManagedAskUserToolCallForCheckpoint(run, originalCall)
		name := strings.TrimSpace(call.Name)
		if canonical, err := canonicalRuntimeToolName(name); err == nil {
			name = canonical
		} else if name == "" {
			return err
		}
		arguments := call.Arguments
		if canonical, admitted := canonicalKernelCheckpointArguments(name, arguments); admitted {
			arguments = canonical
		}
		toolCallPayload := map[string]any{
			"id":        call.ID,
			"type":      "function",
			"name":      name,
			"arguments": decodeToolEventJSON(string(arguments)),
		}
		if call.RejectedBeforeExecution {
			toolCallPayload["rejectedBeforeExecution"] = true
		}
		if call.RuntimeRecovered {
			toolCallPayload["runtimeRecovered"] = true
			runtimeRecoveredCount++
		}
		payload = append(payload, toolCallPayload)
		if !call.RejectedBeforeExecution && kernelcontract.Admitted(name, arguments) {
			kernelCallCount++
		}
	}
	encodedBatch, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode model tool call batch: %w", err)
	}
	batchDigest := sha256.Sum256(encodedBatch)
	cacheKey := "chat-model-tool-calls-" + hex.EncodeToString(batchDigest[:])
	message := fmt.Sprintf("model selected %d tool call(s)", len(calls))
	if runtimeRecoveredCount == len(calls) {
		message = fmt.Sprintf("runtime recovered %d deterministic control transition(s)", len(calls))
	}
	checkpointInput := map[string]any{
		"sessionId":     run.SessionID,
		"runnerId":      options.RunnerID,
		"runnerAttempt": run.Attempt,
		"claimToken":    run.ClaimToken,
		"status":        "running",
		"message":       message,
		"afterEventId":  run.AfterEventID,
		"clientMessageId": runnerCommandClientMessageID(options.RunnerID, run.SessionID, run.Attempt, run.ClaimToken,
			cacheKey),
		"modelToolCalls": payload,
		"resumeCacheKey": cacheKey,
	}
	if run.Transcript != nil {
		payload := transcriptCheckpointPayload(checkpointInput)
		destinations := transcriptRunnerDestinations(run.Transcript)
		if len(run.requiredScientificCapabilitiesSnapshot()) > 0 {
			payload["visibility"] = "provisional"
			destinations = nil
		}
		var commitHook transcriptstore.RunnerCheckpointCommitHook
		var committedOperations []transcriptstore.RunnerCheckpointKernelOperationReceipt
		var committedBatch workspace.ToolCallBatch
		var committedItems []workspace.ToolCallBatchItem
		if s.workspaceStore == nil {
			return errors.New("workspace store is required for durable model tool-call batches")
		}
		if kernelCallCount > 0 && run.Transcript.Stream.Kind != transcriptstore.StreamKindFrameRef {
			return errors.New("durable kernel operations require a Frame transcript stream")
		}
		commitHook = func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, _ bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			batch, items, _, err := s.workspaceStore.CreateToolCallBatchForCheckpointTx(ctx, tx, event)
			if err != nil {
				return transcriptstore.RunnerCheckpointCommitReceipt{}, err
			}
			committedBatch, committedItems = batch, items
			receipt := transcriptstore.RunnerCheckpointCommitReceipt{ToolBatch: &transcriptstore.RunnerCheckpointToolBatchReceipt{
				BatchID: batch.BatchID, CallCount: batch.CallCount,
			}}
			if kernelCallCount > 0 {
				operations, err := s.workspaceStore.CreateKernelLocalOperationsForCheckpointTx(ctx, tx, event)
				if err != nil {
					return transcriptstore.RunnerCheckpointCommitReceipt{}, err
				}
				if len(operations) != kernelCallCount {
					return transcriptstore.RunnerCheckpointCommitReceipt{}, errors.New("durable kernel operation count does not match model tool-call batch")
				}
				receipt.KernelOperations = make([]transcriptstore.RunnerCheckpointKernelOperationReceipt, 0, len(operations))
				for _, operation := range operations {
					committed := transcriptstore.RunnerCheckpointKernelOperationReceipt{
						OperationID: operation.OperationID, Ordinal: operation.ToolCallOrdinal,
						ToolCallID: operation.ToolCallID, Tool: operation.Tool, InputSHA256: operation.InputSHA256,
						ApprovalRequestID:   operation.ApprovalRequestID,
						RequestedEventID:    "kernel-local-operation-requested:" + operation.OperationID,
						InitialStateVersion: operation.StateVersion,
					}
					receipt.KernelOperations = append(receipt.KernelOperations, committed)
					committedOperations = append(committedOperations, committed)
				}
			}
			return receipt, nil
		}
		_, err := s.checkpointTranscriptRunnerEventWithDestinationsAndHook(
			context.Background(), run.Transcript, transcriptstore.RunnerPhaseExecuting,
			cacheKey, payload, true, destinations, commitHook,
		)
		if err != nil {
			return err
		}
		if committedBatch.BatchID == "" || len(committedItems) != len(calls) {
			return errors.New("durable model tool-call batch was not committed")
		}
		claimedBatch, err := s.workspaceStore.ClaimToolCallBatch(context.Background(), workspace.ClaimToolCallBatchInput{
			Claim: run.Transcript.Claim, BatchID: committedBatch.BatchID,
			ExpectedStateVersion: committedBatch.StateVersion,
		})
		if err != nil {
			return err
		}
		if claimedBatch.BatchID != committedBatch.BatchID {
			return errors.New("durable model tool-call batch claim conflicts with its checkpoint")
		}
		if run.ToolBatchIDs == nil {
			run.ToolBatchIDs = make(map[string]string, len(committedItems))
			run.ToolBatchOrdinals = make(map[string]int64, len(committedItems))
		}
		for _, item := range committedItems {
			run.ToolBatchIDs[item.ToolCallID] = committedBatch.BatchID
			run.ToolBatchOrdinals[item.ToolCallID] = item.Ordinal
		}
		if len(committedOperations) > 0 {
			if run.KernelOperationIDs == nil {
				run.KernelOperationIDs = make(map[string]string, len(committedOperations))
			}
			for _, operation := range committedOperations {
				if existing := run.KernelOperationIDs[operation.ToolCallID]; existing != "" && existing != operation.OperationID {
					return errors.New("kernel operation tool-call identity conflicts with the committed checkpoint")
				}
				run.KernelOperationIDs[operation.ToolCallID] = operation.OperationID
			}
			if err := s.resolveCheckpointKernelOperationPolicies(
				context.Background(), run, committedOperations,
			); err != nil {
				return err
			}
			s.workspaceStore.NotifyKernelOperationChanged()
		}
		return nil
	}
	checkpoint, err := s.checkpointSessionRunner(checkpointInput)
	if err != nil {
		return err
	}
	if event, ok := checkpoint["event"].(*eventjournal.Entry); ok && event != nil {
		run.AfterEventID = event.EventID
	}
	return nil
}

// canonicalKernelCheckpointArguments applies the same narrow JSON-schema
// coercion used by the execution gateway before a kernel call becomes durable.
// Some provider adapters serialize boolean tool arguments as "true"/"false".
// If the checkpoint retained that transport spelling while execution used the
// normalized boolean, the operation ledger and executor would describe two
// different calls and the authorized operation could never start.
//
// Only declared kernel boolean fields are coerced, and the result is used only
// when the complete kernel contract accepts it. Invalid calls remain ordinary
// tool-batch items so the model still receives the real validation failure.
func canonicalKernelCheckpointArguments(name string, raw json.RawMessage) (json.RawMessage, bool) {
	if !kernelcontract.IsTool(name) || len(raw) == 0 {
		return raw, false
	}
	if kernelcontract.Admitted(name, raw) {
		return raw, true
	}
	var input map[string]any
	if err := json.Unmarshal(raw, &input); err != nil || input == nil {
		return raw, false
	}
	changed := false
	for _, key := range []string{"background", "fresh"} {
		text, found := input[key].(string)
		if !found {
			continue
		}
		value, err := strconv.ParseBool(strings.TrimSpace(text))
		if err != nil {
			continue
		}
		input[key] = value
		changed = true
	}
	if !changed {
		return raw, false
	}
	canonical, err := json.Marshal(input)
	if err != nil || !kernelcontract.Admitted(name, canonical) {
		return raw, false
	}
	return canonical, true
}

var errKernelLocalOperationPendingRecovery = errors.New("kernel local operation is pending durable recovery")

type kernelLocalOperationPendingRecoveryError struct {
	operationID string
	state       string
	cause       error
}

func (e *kernelLocalOperationPendingRecoveryError) Error() string {
	if e == nil || e.cause == nil {
		return errKernelLocalOperationPendingRecovery.Error()
	}
	return e.cause.Error()
}

func (e *kernelLocalOperationPendingRecoveryError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *kernelLocalOperationPendingRecoveryError) Is(target error) bool {
	return target == errKernelLocalOperationPendingRecovery
}

func (s *Server) kernelLocalOperationTerminalCheckpointDisposition(
	ctx context.Context,
	operation workspace.KernelLocalOperation,
	phase, message string,
) (bool, error) {
	if workspace.KernelLocalOperationIsTerminal(operation.State) {
		return true, nil
	}
	backgroundStarted, err := kernelLocalOperationBackgroundRequested(operation)
	if err != nil {
		return false, err
	}
	if backgroundStarted {
		return true, nil
	}
	if operation.State == workspace.KernelLocalOperationStateStarted &&
		s != nil && s.workspaceStore != nil && operation.ExecutionID != "" {
		execution, found, err := s.workspaceStore.GetDetachedKernelExecution(ctx, operation.ExecutionID)
		if err != nil {
			return false, err
		}
		if detachedKernelExecutionContinues(operation, execution, found) {
			cause := errors.New(strings.TrimSpace(message))
			if strings.TrimSpace(message) == "" || phase == "completed" {
				cause = errKernelLocalOperationPendingRecovery
			}
			return false, &kernelLocalOperationPendingRecoveryError{
				operationID: operation.OperationID,
				state:       operation.State,
				cause:       cause,
			}
		}
	}
	if phase == "failed" {
		cause := errors.New(strings.TrimSpace(message))
		if strings.TrimSpace(message) == "" {
			cause = errKernelLocalOperationPendingRecovery
		}
		return false, &kernelLocalOperationPendingRecoveryError{
			operationID: operation.OperationID,
			state:       operation.State,
			cause:       cause,
		}
	}
	return false, workspace.ErrKernelLocalOperationConflict
}

func kernelLocalOperationBackgroundRequested(operation workspace.KernelLocalOperation) (bool, error) {
	if operation.State != workspace.KernelLocalOperationStateStarted {
		return false, nil
	}
	var input map[string]any
	if err := json.Unmarshal(operation.InputJSON, &input); err != nil {
		return false, workspace.ErrKernelLocalOperationConflict
	}
	background, _ := input["background"].(bool)
	return background, nil
}

// runnerToolSourcePhase reports whether a tool checkpoint can serve as the
// durable source event for an artifact write. Main-run tool calls use "start";
// independent completion-review reads checkpoint as "verification_tool" and
// must also be admissible so oversized reviewer evidence can be externalized.
func runnerToolSourcePhase(phase string) bool {
	return phase == "start" || phase == "verification_tool"
}

func (s *Server) checkpointChatTool(options SessionRunnerChatOptions, run *sessionRunnerChatRun, status string, message string, toolCallID string, phase string, details ...map[string]any) error {
	if run == nil {
		return nil
	}
	if run.SuppressReviewCheckpoints && (phase == "verification" || phase == "verification_tool") {
		return nil
	}
	checkpointIdentity := chatToolCheckpointPhase(phase+"-"+status, toolCallID)
	checkpointInput := chatToolCheckpointInput(options, run, status, message, toolCallID, phase, details...)
	payload := transcriptCheckpointPayload(checkpointInput)
	if s.workspaceStore != nil && run.Transcript != nil && toolCallID != "" {
		origin, found, err := s.workspaceStore.ToolCallOriginAttempt(context.Background(), run.ToolBatchIDs[toolCallID], toolCallID)
		if err != nil {
			return err
		}
		if found {
			payload["toolOriginAttempt"] = origin
		}
	}
	destinations := transcriptRunnerDestinations(run.Transcript)
	if len(run.requiredScientificCapabilitiesSnapshot()) > 0 && !publicToolLifecycleCheckpoint(phase, details) {
		payload["visibility"] = "provisional"
		destinations = nil
	}
	operationID := strings.TrimSpace(run.KernelOperationIDs[toolCallID])
	kernelTerminal := false
	if operationID != "" && (phase == "completed" || phase == "failed") {
		if s.workspaceStore == nil || run.Transcript == nil {
			return errors.New("workspace and Transcript authority are required for durable kernel settlement")
		}
		operation, found, err := s.workspaceStore.GetKernelLocalOperation(
			context.Background(), run.Transcript.Stream.OwnerID, operationID,
		)
		if err != nil {
			return err
		}
		if !found || operation.StreamUID != run.Transcript.Stream.UID || operation.ToolCallID != toolCallID {
			return workspace.ErrKernelLocalOperationConflict
		}
		kernelTerminal, err = s.kernelLocalOperationTerminalCheckpointDisposition(
			context.Background(), operation, phase, message,
		)
		if err != nil {
			return err
		}
	}
	var commitHook transcriptstore.RunnerCheckpointCommitHook
	batchMutation, err := s.prepareRunnerToolBatchCheckpointMutation(
		context.Background(), run, toolCallID, phase,
	)
	if err != nil {
		if phase != "approval_resumed" && (errors.Is(err, workspace.ErrToolCallBatchConflict) || errors.Is(err, workspace.ErrToolCallBatchStale)) {
			fmt.Fprintf(os.Stderr, "[runner] tool batch checkpoint conflict for %s is tolerated: %v\n", toolCallID, err)
			batchMutation = nil
		} else {
			return err
		}
	}
	if batchMutation != nil || kernelTerminal {
		if s.workspaceStore == nil {
			return errors.New("workspace store is required for durable tool settlement")
		}
		commitHook = func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, _ bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			if batchMutation != nil {
				if err := batchMutation(ctx, tx, event); err != nil {
					if phase != "approval_resumed" && (errors.Is(err, workspace.ErrToolCallBatchConflict) || errors.Is(err, workspace.ErrToolCallBatchStale)) {
						fmt.Fprintf(os.Stderr, "[runner] tool batch commit conflict for %s is tolerated: %v\n", toolCallID, err)
					} else {
						return transcriptstore.RunnerCheckpointCommitReceipt{}, fmt.Errorf("commit tool batch result: %w", err)
					}
				}
			}
			if kernelTerminal {
				if err := s.workspaceStore.CommitKernelLocalOperationProtocolReceiptTx(ctx, tx, operationID, event); err != nil {
					return transcriptstore.RunnerCheckpointCommitReceipt{}, fmt.Errorf("commit kernel protocol receipt: %w", err)
				}
			}
			if err := ctx.Err(); err != nil {
				return transcriptstore.RunnerCheckpointCommitReceipt{}, err
			}
			return transcriptstore.RunnerCheckpointCommitReceipt{}, nil
		}
	}
	transcriptEvent, err := s.checkpointTranscriptRunnerEventWithDestinationsAndHook(
		context.Background(), run.Transcript, transcriptstore.RunnerPhaseExecuting,
		checkpointIdentity, payload, true, destinations, commitHook,
	)
	if err != nil {
		if errors.Is(err, transcriptstore.ErrEventConflict) {
			// A duplicate tool checkpoint with a divergent payload must not
			// terminate the runner: the first checkpoint is already durable and
			// the in-memory tool result remains authoritative for this turn.
			fmt.Fprintf(os.Stderr, "[runner] tool checkpoint event conflict for %s is tolerated: %v\n", toolCallID, err)
			return nil
		}
		return err
	}
	if runnerToolSourcePhase(phase) && strings.TrimSpace(toolCallID) != "" && transcriptEvent.EventID > 0 {
		if run.ToolSourceEventIDs == nil {
			run.ToolSourceEventIDs = map[string]int64{}
		}
		run.ToolSourceEventIDs[toolCallID] = transcriptEvent.EventID
	}
	if run.Transcript != nil {
		return nil
	}
	checkpoint, err := s.checkpointSessionRunner(checkpointInput)
	if err != nil {
		return err
	}
	if event, ok := checkpoint["event"].(*eventjournal.Entry); ok && event != nil {
		run.AfterEventID = event.EventID
	}
	return nil
}

// Scientific completion requirements govern conclusions, not whether the
// user can observe an admitted operation and its actual outcome. Model-call
// proposals and independent verification checkpoints retain their privacy.
func publicToolLifecycleCheckpoint(phase string, details []map[string]any) bool {
	if len(details) == 0 {
		return false
	}
	if strings.TrimSpace(stringValue(details[0]["toolName"])) == "" {
		return false
	}
	switch phase {
	case "start", "approval_resumed", "waiting", "completed", "failed", "blocked", "cancelled", "canceled", prestartToolFailurePhase:
		return true
	default:
		return strings.HasPrefix(phase, "progress-")
	}
}

func chatToolCheckpointInput(options SessionRunnerChatOptions, run *sessionRunnerChatRun, status string, message string, toolCallID string, phase string, details ...map[string]any) map[string]any {
	checkpointIdentity := chatToolCheckpointPhase(phase+"-"+status, toolCallID)
	checkpointInput := map[string]any{
		"sessionId":       run.SessionID,
		"runnerId":        options.RunnerID,
		"runnerAttempt":   run.Attempt,
		"claimToken":      run.ClaimToken,
		"status":          status,
		"message":         message,
		"afterEventId":    run.AfterEventID,
		"clientMessageId": runnerCommandClientMessageID(options.RunnerID, run.SessionID, run.Attempt, run.ClaimToken, checkpointIdentity),
		"toolCallId":      toolCallID,
		"toolPhase":       phase,
		"resumeCacheKey":  chatToolCheckpointPhase(phase, toolCallID),
	}
	if lifecyclePhase := sessionRunnerLifecyclePhase(run); lifecyclePhase != "" {
		checkpointInput["lifecyclePhase"] = lifecyclePhase
	}
	if len(details) > 0 {
		for key, value := range details[0] {
			if value != nil {
				checkpointInput[key] = value
			}
		}
	}
	return checkpointInput
}

func chatToolCheckpointPhase(phase string, toolCallID string) string {
	id := sanitizeRunnerPhasePart(toolCallID)
	if id == "" {
		id = "tool-call"
	}
	return "chat-tool-" + sanitizeRunnerPhasePart(phase) + "-" + id
}

func decodeToolEventJSON(raw string) any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
		return decoded
	}
	return raw
}

func sanitizeRunnerPhasePart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) > 80 {
		value = value[:80]
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '-' || r == '_':
			return r
		default:
			return '_'
		}
	}, value)
}

func readBoundedBody(reader io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		limit = defaultSessionRunnerOutputLimitBytes
	}
	limited := io.LimitReader(reader, limit+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("response exceeded %d bytes", limit)
	}
	return body, nil
}
func runnerValueContainsToolResult(value any) bool {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if runnerValueContainsToolResult(item) {
				return true
			}
		}
	case map[string]any:
		if strings.TrimSpace(stringValue(typed["type"])) == "tool_result" {
			return true
		}
		for _, key := range []string{"content", "parts", "children", "items"} {
			if runnerValueContainsToolResult(typed[key]) {
				return true
			}
		}
	}
	return false
}
