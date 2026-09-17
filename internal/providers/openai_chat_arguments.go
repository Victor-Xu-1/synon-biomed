package providers

import (
	"encoding/json"
	"log"
	"strings"
)

func normalizeOpenAIChatStreamArguments(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if normalized, ok := normalizeProviderToolArgumentsObject(raw); ok {
		return string(normalized), true
	}
	return lastCompleteOpenAIChatStreamJSONValue(raw)
}

// Preserve true deltas first. Only after both complete assemblies have been
// checked may recovery examine root-level snapshots. An inner object must
// never outrank a valid complete assembly or stand in for the original call.
func resolveOpenAIChatStreamArguments(parts []string, merged string) (string, bool) {
	candidates := []string{}
	if len(parts) > 0 {
		candidates = append(candidates, strings.Join(parts, ""))
	}
	if len(candidates) == 0 || merged != candidates[0] {
		candidates = append(candidates, merged)
	}
	for index, candidate := range candidates {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		if normalized, ok := normalizeProviderToolArgumentsObject(candidate); ok {
			if index > 0 {
				log.Printf("provider_tool_arguments_recovered assembly=merged %s", openAIChatStreamArgumentsDiagnostic(candidates[0], len(parts)))
			}
			return string(normalized), true
		}
	}
	for _, candidate := range candidates {
		if normalized, ok := lastCompleteOpenAIChatStreamJSONValue(candidate); ok {
			log.Printf("provider_tool_arguments_recovered assembly=root_snapshot %s", openAIChatStreamArgumentsDiagnostic(candidate, len(parts)))
			return normalized, true
		}
	}
	if len(candidates) > 0 {
		log.Printf("provider_tool_arguments_unresolved %s", openAIChatStreamArgumentsDiagnostic(candidates[0], len(parts)))
	}
	return "", false
}

// Single linear scan of lexical root boundaries. Objects inside arrays,
// strings or unfinished parents are never extraction candidates. The existing
// stream byte bound still applies; no reverse scan or candidate-count cutoff
// can silently discard an outer value in a sufficiently detailed argument.
func lastCompleteOpenAIChatStreamJSONValue(raw string) (string, bool) {
	stack := make([]byte, 0)
	start, end := -1, -1
	inString, escaped := false, false
	best := ""
	for index := 0; index < len(raw); index++ {
		b := raw[index]
		if inString {
			if escaped {
				escaped = false
			} else if b == '\\' {
				escaped = true
			} else if b == '"' {
				inString = false
			}
			continue
		}
		if b == '"' {
			inString = true
			continue
		}
		if b == '{' || b == '[' {
			if len(stack) == 0 {
				if end >= 0 && strings.TrimSpace(raw[end:index]) != "" {
					return "", false
				}
				start = index
			}
			stack = append(stack, b)
			continue
		}
		if b != '}' && b != ']' {
			continue
		}
		if len(stack) == 0 {
			continue
		} // Some legacy snapshots repeat their closing suffix.
		top := stack[len(stack)-1]
		if (top == '{' && b != '}') || (top == '[' && b != ']') {
			return "", false
		}
		stack = stack[:len(stack)-1]
		if len(stack) != 0 {
			continue
		}
		candidate := raw[start : index+1]
		if candidate[0] != '{' || !json.Valid([]byte(candidate)) {
			return "", false
		}
		best, end = candidate, index+1
	}
	if len(stack) != 0 || best == "" || strings.ContainsAny(raw[end:], "[{") {
		return "", false
	}
	return best, true
}
