package agentruntime

import (
	"bytes"
	"encoding/json"
	"strings"
)

const maxNoProgressRecoveryCallLabels = 8

// Failure feedback is already carried by the exact native tool receipt. Never
// describe rejected/failed or decision-required actions as successful reusable
// work merely because they made no progress. A compact reuse receipt is a
// successful idempotent result even though it truthfully reports executed=false.
func noProgressReceiptsAreSuccessful(messages []Message) bool {
	found := false
	for _, message := range messages {
		if message.Role != "tool" {
			continue
		}
		found = true
		var value any
		if json.Unmarshal([]byte(message.Content), &value) != nil || ClassifyToolResult(value) != ToolResultSucceeded {
			return false
		}
		if object, ok := value.(map[string]any); ok && object["executed"] == false {
			if object["reused"] != true && !toolResultExplicitlyUnchanged(object) && !toolResultExplicitlyNoMutation(object) {
				return false
			}
		}
	}
	return found
}

func appendNoProgressRecoveryCalls(history []ToolCall, calls []ToolCall) []ToolCall {
	seen := make(map[string]struct{}, len(history)+len(calls))
	for _, call := range history {
		seen[noProgressRecoveryCallKey(call)] = struct{}{}
	}
	for _, call := range calls {
		key := noProgressRecoveryCallKey(call)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		history = append(history, call)
		if len(history) > maxNoProgressRecoveryCallLabels {
			history = append([]ToolCall(nil), history[len(history)-maxNoProgressRecoveryCallLabels:]...)
		}
	}
	return history
}

func noProgressRecoveryCallKey(call ToolCall) string {
	name := strings.TrimSpace(call.Name)
	if name == "" {
		return ""
	}
	// Display clipping cannot define execution identity. Use the same complete
	// semantic fingerprint as the live failed-call guard and durable recovery.
	return ExecutionCallFingerprint(name, call.Arguments)
}

func compactNoProgressArguments(arguments json.RawMessage) string {
	trimmed := bytes.TrimSpace(arguments)
	if len(trimmed) == 0 {
		return "{}"
	}
	buffer := bytes.Buffer{}
	if json.Compact(&buffer, trimmed) == nil {
		trimmed = buffer.Bytes()
	}
	const maxBytes = 240
	if len(trimmed) > maxBytes {
		trimmed = append(append([]byte(nil), trimmed[:maxBytes]...), '.', '.', '.')
	}
	return string(trimmed)
}

func idempotentToolRoundRecoveryInstruction(calls []ToolCall) string {
	labels := make([]string, 0, len(calls))
	for _, call := range calls {
		name := strings.TrimSpace(call.Name)
		if name == "" {
			continue
		}
		labels = append(labels, name+"("+compactNoProgressArguments(call.Arguments)+")")
	}
	closed := "the completed operations already in context"
	if len(labels) > 0 {
		closed = strings.Join(labels, "; ")
	}
	return "The latest tool round returned only idempotent receipts and produced no new evidence. " +
		"These completed operations are closed for this execution: " + closed + ". " +
		"Reuse their successful results and do not repeat them with cosmetic argument changes. " +
		"Choose a materially different action only when the task still requires new evidence; otherwise provide the requested final answer now."
}
