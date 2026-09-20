package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
)

// ExecuteToolBatch executes calls in their original model order starting at
// startOrdinal. It is shared by the live Engine loop and durable runner
// recovery so resumed work cannot drift onto a second execution path.
func (e Engine) ExecuteToolBatch(
	ctx context.Context,
	calls []ToolCall,
	startOrdinal int,
	mediaPolicy MediaPolicy,
	mediaBytesUsed int64,
) (ToolBatchExecution, error) {
	result := ToolBatchExecution{NextOrdinal: startOrdinal, MediaBytesUsed: mediaBytesUsed}
	if startOrdinal < 0 || startOrdinal > len(calls) {
		return result, errors.New("tool batch resume ordinal is invalid")
	}
	pendingParts := []ContentPart{}
	result.NoProgress = startOrdinal < len(calls)
	for ordinal := startOrdinal; ordinal < len(calls); ordinal++ {
		toolMessage, toolErr := e.executeTool(ctx, calls[ordinal])
		result.NoProgress = result.NoProgress && toolMessage.noProgress
		if toolMessage.Role != "" {
			pendingParts = append(pendingParts, toolMessage.pending...)
			toolMessage.pending = nil
			result.Messages = append(result.Messages, toolMessage)
		}
		if toolErr != nil {
			withMedia, nextMediaBytesUsed, mediaErr := appendPendingToolMedia(
				result.Messages, pendingParts, mediaPolicy, result.MediaBytesUsed,
			)
			result.Messages = withMedia
			result.MediaBytesUsed = nextMediaBytesUsed
			if mediaErr != nil {
				return result, errors.Join(toolErr, mediaErr)
			}
			return result, toolErr
		}
		result.NextOrdinal = ordinal + 1
		if toolMessage.terminal {
			result.Terminal = true
			break
		}
	}
	withMedia, nextMediaBytesUsed, mediaErr := appendPendingToolMedia(
		result.Messages, pendingParts, mediaPolicy, result.MediaBytesUsed,
	)
	result.Messages = withMedia
	result.MediaBytesUsed = nextMediaBytesUsed
	if mediaErr != nil {
		return result, mediaErr
	}
	return result, nil
}

func validateToolCallBatch(calls []ToolCall, maxCalls int) error {
	if maxCalls > 0 && len(calls) > maxCalls {
		return &ToolCallBatchLimitError{Limit: maxCalls, Received: len(calls)}
	}
	seen := make(map[string]struct{}, len(calls))
	for index, call := range calls {
		callID := strings.TrimSpace(call.ID)
		if callID == "" || callID != call.ID {
			return &ToolCallBatchValidationError{Index: index, Code: "invalid_id"}
		}
		if _, exists := seen[callID]; exists {
			return &ToolCallBatchValidationError{Index: index, Code: "duplicate_id"}
		}
		seen[callID] = struct{}{}
	}
	return nil
}

func appendPendingToolMedia(messages []Message, pending []ContentPart, policy MediaPolicy, used int64) ([]Message, int64, error) {
	if len(pending) == 0 {
		return messages, used, nil
	}
	policy = normalizeMediaPolicy(policy)
	if used >= policy.MaxTotalBytes {
		return messages, used, fmt.Errorf("media content exceeds total limit of %d bytes", policy.MaxTotalBytes)
	}
	mediaMessage := Message{Role: "user", Parts: append([]ContentPart{{
		Type: ContentPartText, Text: ToolMediaContextNotice,
	}}, pending...)}
	normalized, err := NormalizeModelRequestMedia(ModelRequest{Messages: []Message{mediaMessage}, MediaPolicy: policy})
	if err != nil {
		return messages, used, err
	}
	added := modelMessageMediaBytes(normalized.Messages)
	if added > policy.MaxTotalBytes-used {
		return messages, used, fmt.Errorf("media content exceeds total limit of %d bytes", policy.MaxTotalBytes)
	}
	return append(messages, normalized.Messages[0]), used + added, nil
}

func modelMessageMediaBytes(messages []Message) int64 {
	var total int64
	for _, message := range messages {
		for _, part := range message.Parts {
			if part.Media != nil {
				total += int64(len(part.Media.Source.Data))
			}
		}
	}
	return total
}

func (e Engine) executeTool(ctx context.Context, call ToolCall) (Message, error) {
	if call.ID == "" {
		call.ID = firstNonEmpty(call.Name, "invalid_tool_call")
	}
	if !call.Resumed {
		if err := e.emit(Event{Type: EventToolStarted, ToolName: call.Name, ToolCallID: call.ID, Arguments: string(call.Arguments)}); err != nil {
			return Message{}, err
		}
	}
	value := map[string]any{"ok": false}
	if err := validateToolCall(call); err != nil {
		value["error"] = err.Error()
		message, messageErr := e.toolResultMessage(ctx, call, value, ClassifyToolResult(value))
		message.noProgress = true
		if messageErr != nil {
			return message, messageErr
		}
		if eventErr := e.emitLifecycleEvent(ctx, Event{Type: EventToolFailed, ToolName: call.Name, ToolCallID: call.ID, Message: err.Error(), Arguments: string(call.Arguments), Result: message.Content}); eventErr != nil {
			return message, eventErr
		}
		return message, nil
	}
	if e.Tools == nil {
		value["error"] = "agent runtime tool gateway is required"
		message, messageErr := e.toolResultMessage(ctx, call, value, ClassifyToolResult(value))
		if messageErr != nil {
			return message, messageErr
		}
		if eventErr := e.emitLifecycleEvent(ctx, Event{Type: EventToolFailed, ToolName: call.Name, ToolCallID: call.ID, Message: value["error"].(string), Arguments: string(call.Arguments), Result: message.Content}); eventErr != nil {
			return message, eventErr
		}
		return message, nil
	}
	result, err := e.executeToolGatewayWithProgress(ctx, call)
	if err != nil {
		var pause *PauseError
		if errors.As(err, &pause) {
			for key, item := range pause.Data {
				value[key] = item
			}
			value["status"] = pause.Status
			value["message"] = pause.Error()
			message, messageErr := e.toolResultMessage(ctx, call, value, ClassifyToolResult(value))
			if messageErr != nil {
				return message, messageErr
			}
			if eventErr := e.emitLifecycleEvent(ctx, Event{Type: EventToolPaused, ToolName: call.Name, ToolCallID: call.ID, Message: pause.Error(), Arguments: string(call.Arguments), Result: message.Content}); eventErr != nil {
				return message, eventErr
			}
			return message, pause
		}
		// Cancellation is an infrastructure/control-plane boundary, not a tool
		// business failure. A draining backend must leave the durable call open
		// so the next runner can recover that same call without a red failure.
		if contextErr := context.Cause(ctx); contextErr != nil {
			return Message{}, contextErr
		}
		value["error"] = err.Error()
		message, messageErr := e.toolResultMessage(ctx, call, value, ClassifyToolResult(value))
		if messageErr != nil {
			return message, messageErr
		}
		if eventErr := e.emitLifecycleEvent(ctx, Event{Type: EventToolFailed, ToolName: call.Name, ToolCallID: call.ID, Message: err.Error(), Arguments: string(call.Arguments), Result: message.Content}); eventErr != nil {
			return message, eventErr
		}
		return message, nil
	}
	executedArguments := strings.TrimSpace(string(result.ExecutedArguments))
	if executedArguments == "" {
		executedArguments = string(call.Arguments)
	} else {
		var executedInput map[string]any
		if json.Unmarshal(result.ExecutedArguments, &executedInput) != nil || executedInput == nil {
			return Message{}, errors.New("tool gateway returned invalid executed arguments")
		}
	}
	executedCall := call
	executedCall.Arguments = append(json.RawMessage(nil), []byte(executedArguments)...)
	outcome := ClassifyToolResult(result.Value)
	var message Message
	var messageErr error
	if result.Materialized != nil {
		messageErr = e.validateTrustedMaterializedToolResult(executedCall, result.Value, outcome, *result.Materialized)
		if messageErr == nil {
			message = Message{Role: "tool", ToolCallID: call.ID, Content: string(result.Materialized.JSON)}
		}
	} else {
		message, messageErr = e.toolResultMessage(ctx, executedCall, result.Value, outcome)
	}
	if messageErr != nil {
		return message, messageErr
	}
	message.pending = append([]ContentPart(nil), result.Parts...)
	message.terminal = result.Terminal && outcome == ToolResultSucceeded
	message.noProgress = toolResultReportsNoProgress(result.Value)
	eventType := EventToolCompleted
	eventMessage := ""
	rejectedBeforeExecution := IsNonExecutingPreflight(result.Value)
	if outcome.HardFailed() && !rejectedBeforeExecution {
		eventType = EventToolFailed
		eventMessage = ToolFailureEventMessage(result.Value)
	}
	originalResult := message.Content
	modelResult, modelResultErr := AttachToolResultModelContext(originalResult, result.ModelContext)
	if modelResultErr != nil {
		return message, modelResultErr
	}
	if err := e.emitLifecycleEvent(ctx, Event{
		Type: eventType, ToolName: call.Name, ToolCallID: call.ID, Message: eventMessage,
		Arguments: string(call.Arguments), ExecutedArguments: executedArguments,
		Result: originalResult, RejectedBeforeExecution: rejectedBeforeExecution,
	}); err != nil {
		return message, err
	}
	message.Content = modelResult
	return message, nil
}

// AttachToolResultModelContext preserves ordinary object fields whenever
// possible. A collision or non-object result is wrapped without dropping data.
// The returned encoding is a transient provider view; callers retain content
// as the durable evidence authority.
func AttachToolResultModelContext(content string, modelContext any) (string, error) {
	if modelContext == nil {
		return content, nil
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.UseNumber()
	var toolResult any
	if err := decoder.Decode(&toolResult); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return "", errors.New("tool result model context requires one JSON result")
	}
	contextJSON, err := json.Marshal(modelContext)
	if err != nil {
		return "", fmt.Errorf("encode tool result model context: %w", err)
	}
	contextDecoder := json.NewDecoder(strings.NewReader(string(contextJSON)))
	contextDecoder.UseNumber()
	var contextValue any
	if err := contextDecoder.Decode(&contextValue); err != nil {
		return "", fmt.Errorf("decode tool result model context: %w", err)
	}
	const contextKey = "_synon_model_context"
	if object, ok := toolResult.(map[string]any); ok {
		if _, collision := object[contextKey]; !collision {
			object[contextKey] = contextValue
			encoded, err := json.Marshal(object)
			return string(encoded), err
		}
	}
	encoded, err := json.Marshal(map[string]any{
		"tool_result": toolResult, contextKey: contextValue,
	})
	return string(encoded), err
}

// toolResultReportsNoProgress is intentionally envelope-based so the engine
// stays independent from any particular source connector. A gateway marks a
// result that did not change authoritative state with one of the documented
// boolean fields; inspect maps and conventional typed fields directly so
// custom MarshalJSON values are never marshalled a second time on the
// large-result path.
func toolResultReportsNoProgress(value any) bool {
	if provider, ok := value.(interface{ ToolResultEnvelope() map[string]any }); ok {
		value = provider.ToolResultEnvelope()
	}
	if envelope, ok := value.(map[string]any); ok {
		outcome := ClassifyToolResult(envelope)
		if envelope["executed"] == false || outcome.HardFailed() || outcome == ToolResultUnavailable {
			return true
		}
		return envelope["reused"] == true || toolResultExplicitlyUnchanged(envelope) || toolResultExplicitlyNoMutation(envelope)
	}
	reflected := reflect.ValueOf(value)
	for reflected.IsValid() && reflected.Kind() == reflect.Pointer {
		if reflected.IsNil() {
			return false
		}
		reflected = reflected.Elem()
	}
	if !reflected.IsValid() || reflected.Kind() != reflect.Struct {
		return false
	}
	field := reflected.FieldByName("Reused")
	if field.IsValid() && field.Kind() == reflect.Bool {
		return field.Bool()
	}
	return false
}
