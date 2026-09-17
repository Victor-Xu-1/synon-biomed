package agentruntime

import "strings"

type ToolResultOutcome string

const (
	ToolResultSucceeded   ToolResultOutcome = "succeeded"
	ToolResultFailed      ToolResultOutcome = "failed"
	ToolResultUnavailable ToolResultOutcome = "unavailable"
	ToolResultPartial     ToolResultOutcome = "partial"
)

func (outcome ToolResultOutcome) Failed() bool {
	return outcome != ToolResultSucceeded
}

// HardFailed reports whether the outcome is a definitive terminal failure.
// Partial outcomes carry successful results alongside per-file/per-source
// errors and are recoverable (the caller can retry only the failed subset).
// Unavailable outcomes are recoverable upstream failures. Only HardFailed
// outcomes must drive failed lifecycle events, failed-call guards, and
// terminal tool-batch state; evidence validation intentionally keeps using
// Failed() because partial results are not authoritative source evidence.
func (outcome ToolResultOutcome) HardFailed() bool {
	return outcome == ToolResultFailed
}

// ClassifyToolResult applies the closed response-envelope contract shared by
// the agent gateway, event stream, deferred approvals, audit trail, and
// evidence validation. It intentionally inspects only envelope fields and the
// documented source-status list, never arbitrary nested scientific payloads.
func ClassifyToolResult(value any) ToolResultOutcome {
	if provider, ok := value.(interface{ ToolResultEnvelope() map[string]any }); ok {
		return classifyToolResultMap(provider.ToolResultEnvelope(), 0)
	}
	result, ok := value.(map[string]any)
	if !ok {
		return ToolResultSucceeded
	}
	return classifyToolResultMap(result, 0)
}

// IsNonExecutingPreflight identifies a fail-closed admission result that
// prevented user code or an external operation from starting. The result must
// still classify as failed for evidence and model correction, but its tool
// lifecycle is a completed safety check rather than an execution failure.
func IsNonExecutingPreflight(value any) bool {
	result, ok := value.(map[string]any)
	if !ok || result["executed"] != false {
		return false
	}
	message, _ := result["message"].(string)
	if strings.TrimSpace(message) == "" {
		message, _ = result["error"].(string)
	}
	if result["preflight"] == true {
		return strings.TrimSpace(message) != ""
	}
	status, _ := result["status"].(string)
	if strings.TrimSpace(status) == "" {
		status, _ = result["code"].(string)
	}
	status = strings.ToLower(strings.TrimSpace(status))
	if !strings.HasSuffix(status, "_preflight_required") {
		return false
	}
	return strings.TrimSpace(message) != ""
}

// ToolFailureEventMessage returns a bounded, non-sensitive lifecycle summary.
// Full diagnostics remain in the durable tool result; only a conservative
// machine-readable code may leave that envelope for status UI and logs.
func ToolFailureEventMessage(value any) string {
	const generic = "tool result reported failure"
	result, ok := value.(map[string]any)
	if !ok {
		return generic
	}
	code, _ := result["code"].(string)
	if strings.TrimSpace(code) == "" {
		if failure, ok := result["error"].(map[string]any); ok {
			code, _ = failure["code"].(string)
		}
	}
	code = strings.TrimSpace(code)
	if code == "" || len(code) > 128 {
		return generic
	}
	for _, char := range code {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' ||
			char == '_' || char == '-' || char == '.' {
			continue
		}
		return generic
	}
	return generic + ": " + code
}

func classifyToolResultMap(result map[string]any, depth int) ToolResultOutcome {
	if result["sourceUnavailable"] == true {
		return ToolResultUnavailable
	}
	if failure, ok := result["failure"].(map[string]any); ok && failure["recoverable"] == true {
		switch normalizeToolOutcomeToken(failure["kind"]) {
		case "searchunavailable", "researchunavailable", "sourceunavailable", "unavailable":
			return ToolResultUnavailable
		}
	}
	// An explicit partial envelope proves that usable output exists alongside
	// failures and may therefore retain ok=false. A bare errors list is partial
	// only after the top-level hard-failure flags are ruled out; otherwise a
	// zero-output save failure would be mislabeled as successful partial work.
	if result["partial"] == true {
		return ToolResultPartial
	}
	if result["ok"] == false || result["success"] == false || result["isError"] == true {
		return ToolResultFailed
	}
	if toolResultNonEmptyList(result["errors"]) {
		return ToolResultPartial
	}
	if toolResultFailureValue(result["error"]) || toolResultFailureValue(result["failure"]) {
		return ToolResultFailed
	}
	for _, key := range []string{"status", "stopReason", "stop_reason"} {
		switch normalizeToolOutcomeToken(result[key]) {
		case "failed", "failure", "error":
			return ToolResultFailed
		case "unavailable", "sourceunavailable":
			return ToolResultUnavailable
		case "partial", "incomplete":
			return ToolResultPartial
		}
	}
	if sources, ok := result["sources"].([]any); ok && len(sources) > 0 {
		unavailable := 0
		for _, raw := range sources {
			source, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			switch normalizeToolOutcomeToken(source["status"]) {
			case "failed", "failure", "error", "unavailable", "sourceunavailable":
				unavailable++
			}
		}
		if unavailable == len(sources) {
			return ToolResultUnavailable
		}
		if unavailable > 0 {
			return ToolResultPartial
		}
	}
	if depth == 0 {
		if nested, ok := result["result"].(map[string]any); ok {
			return classifyToolResultMap(nested, depth+1)
		}
	}
	return ToolResultSucceeded
}

func toolResultFailureValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(typed) != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

func toolResultNonEmptyList(value any) bool {
	values, ok := value.([]any)
	return ok && len(values) > 0
}

func normalizeToolOutcomeToken(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.NewReplacer("_", "", "-", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(text)))
}
