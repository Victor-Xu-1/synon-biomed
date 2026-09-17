package agentruntime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

// toolCallRejection is the model-facing equivalent of Codex's
// RespondToModel tool error. It closes the exact call ID without executing the
// tool, then leaves the next action to the model instead of imposing a private
// repair sequence in the Harness.
type toolCallRejection struct {
	Code       string
	Message    string
	Diagnostic string
	Recovery   string
	Retryable  bool
	Preflight  bool
}

const maxModelVisibleRejectionFamilyAttempts = 3

func collectToolCallRejections(
	calls []ToolCall,
	advertised []ToolSchema,
	admission ToolCallAdmissionChecker,
	preflight ToolCallPreflightDiagnostics,
) map[int]toolCallRejection {
	rejections := make(map[int]toolCallRejection)
	for index, call := range calls {
		if len(advertised) > 0 && !toolCallNameAdvertised(call.Name, advertised) {
			rejection := toolCallRejection{
				Code:      "unadvertised_tool",
				Message:   fmt.Sprintf("Tool %q is not available in the current model turn.", strings.TrimSpace(call.Name)),
				Recovery:  "Choose a tool from the schemas advertised in the current request, or continue without this tool.",
				Retryable: false,
			}
			// MCP method schemas are intentionally represented by load-on-demand
			// connector Skills while the only model-visible transport is REPL with
			// host.mcp. A provider may still copy a flattened mcp__ identity from
			// retrieved schema text. Repair that protocol slip privately so it does
			// not become a user-visible failed retrieval; never execute or advertise
			// the competing flattened route.
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(call.Name)), "mcp__") &&
				toolCallNameAdvertised("repl", advertised) {
				rejection.Code = "mcp_route_requires_repl"
				rejection.Recovery = "Call the advertised repl tool and invoke the selected method with host.mcp(server, method, input) inside that cell. Do not call the flattened mcp__ name as a model tool."
				rejection.Retryable = true
				rejection.Preflight = true
			}
			rejections[index] = rejection
			continue
		}
		if diagnostic := boundedToolCallDiagnostic(call.ProviderProtocolDiagnostic); diagnostic != "" {
			rejections[index] = toolCallRejection{
				Code:       "invalid_tool_arguments",
				Message:    "The model provider returned tool arguments that are not a valid JSON object.",
				Diagnostic: diagnostic,
				Recovery:   "Re-read the current tool schema and issue one corrected call, or choose another advertised route.",
				Retryable:  true,
				Preflight:  true,
			}
			continue
		}
		if preflight != nil {
			if diagnostic := boundedToolCallDiagnostic(preflight.ToolCallPreflightDiagnostic(call)); diagnostic != "" {
				rejection := rejectionFromDiagnostic(
					"tool_call_preflight_rejected",
					"The tool call was rejected before execution by the current runtime contract.",
					diagnostic,
				)
				rejection.Preflight = true
				rejections[index] = rejection
				continue
			}
		}
		if admission != nil && !admission.AdmitsToolCall(call) {
			diagnostic := ""
			if diagnostics, ok := admission.(ToolCallAdmissionDiagnostics); ok {
				diagnostic = boundedToolCallDiagnostic(diagnostics.ToolCallAdmissionDiagnostic(call))
			}
			rejection := rejectionFromDiagnostic(
				"invalid_tool_arguments",
				"The tool arguments do not satisfy the admitted schema.",
				diagnostic,
			)
			rejection.Preflight = true
			rejections[index] = rejection
		}
	}
	return rejections
}

func privatePreflightRepairMessages(
	calls []ToolCall,
	rejections map[int]toolCallRejection,
) ([]Message, bool, error) {
	if len(calls) == 0 || len(rejections) != len(calls) {
		return nil, false, nil
	}
	messages := make([]Message, 0, len(calls))
	for index, call := range calls {
		rejection, found := rejections[index]
		if !found || !rejection.Preflight || !rejection.Retryable {
			return nil, false, nil
		}
		message, err := modelVisibleToolRejectionMessage(call, rejection)
		if err != nil {
			return nil, false, err
		}
		messages = append(messages, message)
	}
	return messages, true, nil
}

func toolCallNameAdvertised(name string, advertised []ToolSchema) bool {
	wanted := runtimeToolSchemaKey(name)
	if wanted == "" {
		return false
	}
	for _, schema := range advertised {
		if runtimeToolSchemaKey(schema.Name) == wanted {
			return true
		}
	}
	return false
}

func boundedToolCallDiagnostic(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 1800 {
		return value[:1800]
	}
	return value
}

func rejectionFromDiagnostic(code, message, diagnostic string) toolCallRejection {
	rejection := toolCallRejection{
		Code:       code,
		Message:    message,
		Diagnostic: diagnostic,
		Recovery:   "Correct the call using the current tool schema, choose another available tool, or continue with the evidence already available.",
		Retryable:  true,
	}
	var decoded map[string]any
	if diagnostic == "" || json.Unmarshal([]byte(diagnostic), &decoded) != nil {
		return rejection
	}
	if value := firstNonEmpty(stringValueFromMap(decoded, "code"), stringValueFromMap(decoded, "status")); value != "" {
		rejection.Code = value
	}
	if value := stringValueFromMap(decoded, "message"); value != "" {
		rejection.Message = value
	}
	if value := stringValueFromMap(decoded, "recovery"); value != "" {
		rejection.Recovery = value
	}
	return rejection
}

func applyToolCallRejectionFamilyBudget(
	calls []ToolCall,
	rejections map[int]toolCallRejection,
	attempts map[string]int,
) {
	for index, rejection := range rejections {
		if index < 0 || index >= len(calls) || !rejection.Retryable {
			continue
		}
		family := toolCallRejectionFamily(calls[index], rejection)
		if family == "\x00" {
			continue
		}
		attempts[family]++
		if attempts[family] <= maxModelVisibleRejectionFamilyAttempts {
			continue
		}
		rejection.Retryable = false
		rejection.Recovery = "The same tool failure family exhausted its correction budget for this turn. Stop calling this tool for this task segment; choose another advertised route or continue with the evidence already available."
		rejections[index] = rejection
	}
}

// toolCallRejectionFamily identifies the rejected operation, not merely the
// transport used to express it. Multiplexers such as REPL can carry many
// independent host.mcp calls; counting every schema correction against one
// broad "repl + code" bucket disables valid routes after unrelated mistakes.
// The normalized diagnostic scope keeps repeated semantic failures bounded
// while allowing a genuinely corrected target or method to proceed.
func toolCallRejectionFamily(call ToolCall, rejection toolCallRejection) string {
	tool := runtimeToolSchemaKey(call.Name)
	code := strings.ToLower(strings.TrimSpace(rejection.Code))
	if tool == "" && code == "" {
		return "\x00"
	}
	scope := strings.ToLower(strings.TrimSpace(firstNonEmpty(rejection.Diagnostic, rejection.Message, rejection.Recovery)))
	if scope == "" {
		return tool + "\x00" + code
	}
	digest := sha256.Sum256([]byte(scope))
	return fmt.Sprintf("%s\x00%s\x00%x", tool, code, digest[:12])
}

func stringValueFromMap(value map[string]any, key string) string {
	text, _ := value[key].(string)
	return strings.TrimSpace(text)
}

func modelVisibleToolRejectionMessage(call ToolCall, rejection toolCallRejection) (Message, error) {
	payload := map[string]any{
		"ok":        false,
		"executed":  false,
		"retryable": rejection.Retryable,
		"code":      rejection.Code,
		"message":   rejection.Message,
		"recovery":  rejection.Recovery,
	}
	if rejection.Diagnostic != "" {
		var diagnostic any
		if json.Unmarshal([]byte(rejection.Diagnostic), &diagnostic) == nil {
			payload["diagnostic"] = diagnostic
		} else {
			payload["diagnostic"] = rejection.Diagnostic
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Message{}, err
	}
	return Message{Role: "tool", ToolCallID: call.ID, Content: string(encoded)}, nil
}

func (e Engine) executeToolCallRoundWithRejections(
	ctx context.Context,
	calls []ToolCall,
	rejections map[int]toolCallRejection,
	mediaPolicy MediaPolicy,
	mediaBytesUsed int64,
) (ToolBatchExecution, error) {
	result := ToolBatchExecution{MediaBytesUsed: mediaBytesUsed}
	pendingParts := []ContentPart{}
	for index, call := range calls {
		if rejection, rejected := rejections[index]; rejected {
			message, err := modelVisibleToolRejectionMessage(call, rejection)
			if err != nil {
				return result, err
			}
			result.Messages = append(result.Messages, message)
			result.NextOrdinal = index + 1
			if err := e.emitLifecycleEvent(ctx, Event{
				Type: EventToolCompleted, ToolName: call.Name, ToolCallID: call.ID,
				Message: rejection.Message, Arguments: string(call.Arguments), Result: message.Content,
				RejectedBeforeExecution: true,
			}); err != nil {
				return result, err
			}
			continue
		}
		toolMessage, toolErr := e.executeTool(ctx, call)
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
				return result, fmt.Errorf("%w; append tool media: %v", toolErr, mediaErr)
			}
			return result, toolErr
		}
		result.NextOrdinal = index + 1
		if toolMessage.terminal {
			result.Terminal = true
			break
		}
	}
	withMedia, nextMediaBytesUsed, err := appendPendingToolMedia(
		result.Messages, pendingParts, mediaPolicy, result.MediaBytesUsed,
	)
	result.Messages = withMedia
	result.MediaBytesUsed = nextMediaBytesUsed
	if err != nil {
		return result, err
	}
	return result, nil
}
