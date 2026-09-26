package server

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// Preserve the exact selectors supplied by existing preflight authorities.
// Arbitrary payloads/arguments are not copied into the model repair context.
// Priority order keeps the next callable identity ahead of supporting context.
var agentRuntimePreflightDiagnosticFields = []string{
	"required_skill", "required_skills", "required_filter", "required_reads",
	"required_entrypoint", "skill", "download", "downloads",
	"next_tool", "execution_pack_id", "implementation", "selected_implementations",
	"implementation_identities", "required_capabilities", "requested_environment",
	"required_packages", "required_imports", "decision_required",
}

func agentRuntimePreflightDiagnostic(preflight map[string]any) string {
	diagnostic := map[string]any{
		"code":     firstNonEmpty(stringValue(preflight["status"]), stringValue(preflight["code"])),
		"message":  preflight["message"],
		"recovery": preflight["recovery"],
	}
	for _, key := range agentRuntimePreflightDiagnosticFields {
		if value, exists := preflight[key]; exists {
			diagnostic[key] = value
		}
	}
	if raw, err := json.Marshal(diagnostic); err == nil && len(raw) <= 1800 {
		return string(raw)
	}

	// Never cut serialized JSON or a callable identity. Bound explanatory prose
	// and omit whole lower-priority fields explicitly when the repair budget is
	// exceeded. The original preflight still governs admission and execution.
	diagnostic["diagnostic_truncated"] = true
	for key, limit := range map[string]int{"code": 128, "message": 256, "recovery": 512} {
		diagnostic[key] = boundedPreflightDiagnosticText(stringValue(diagnostic[key]), limit)
	}
	omitted := make([]string, 0, len(agentRuntimePreflightDiagnosticFields))
	for index := len(agentRuntimePreflightDiagnosticFields) - 1; ; index-- {
		if len(omitted) > 0 {
			diagnostic["omitted_fields"] = omitted
		}
		if raw, err := json.Marshal(diagnostic); err == nil && len(raw) <= 1800 {
			return string(raw)
		}
		if index < 0 {
			return `{"code":"preflight_diagnostic_encoding_failed","message":"Runtime preflight feedback could not be encoded within its budget.","diagnostic_truncated":true}`
		}
		key := agentRuntimePreflightDiagnosticFields[index]
		if _, exists := diagnostic[key]; exists {
			delete(diagnostic, key)
			omitted = append(omitted, key)
		}
	}
}

func boundedPreflightDiagnosticText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if raw, err := json.Marshal(value); err == nil && len(raw) <= limit {
		return value
	}
	end := min(len(value), limit-len("…")-2)
	if end == len(value) {
		_, size := utf8.DecodeLastRuneInString(value)
		end -= size
	}
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	for {
		candidate := value[:end] + "…"
		if raw, err := json.Marshal(candidate); err == nil && len(raw) <= limit {
			return candidate
		}
		_, size := utf8.DecodeLastRuneInString(value[:end])
		end -= size
	}
}
