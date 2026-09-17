package server

import (
	"encoding/json"
	"math"
)

// compactWebConversationResultCount preserves the tool's authoritative result
// count outside a byte-truncated output preview. Compact history is a transport
// optimization; it must not change a successful search with results into a
// visible zero-result operation merely because a nested provider diagnostic
// appears earlier in the JSON text.
func compactWebConversationResultCount(output any) (int, bool) {
	value := output
	if text, ok := output.(string); ok {
		if err := json.Unmarshal([]byte(text), &value); err != nil {
			return 0, false
		}
	}

	// Prefer the actual public collection before provider-level diagnostics.
	// A federated search can return 38 deduplicated sources while one backend's
	// returnedResults counter is only 7; using that nested counter makes the
	// compact timeline contradict the full disclosure.
	for _, path := range [][]string{
		{"result", "sources"},
		{"data", "sources"},
		{"sources"},
	} {
		if collection, ok := valueAtJSONPath(value, path).([]any); ok {
			return len(collection), true
		}
	}

	for _, path := range [][]string{
		{"result", "returnedResults"},
		{"result", "results_returned"},
		{"result", "retrieved"},
		{"result", "retrieval", "returned"},
		{"result", "diagnostics", "returnedResults"},
		{"result", "diagnostics", "n_returned"},
		{"result", "diagnostics", "n_retrieved"},
		{"data", "returnedResults"},
		{"data", "retrieval", "returned"},
		{"data", "diagnostics", "returnedResults"},
		{"returnedResults"},
		{"results_returned"},
		{"retrieved"},
		{"retrieval", "returned"},
		{"diagnostics", "returnedResults"},
	} {
		if count, ok := compactWebConversationNonNegativeInt(valueAtJSONPath(value, path)); ok {
			return count, true
		}
	}

	for _, path := range [][]string{
		{"result", "records"},
		{"data", "records"},
		{"records"},
		{"result", "targets"},
		{"data", "targets"},
		{"targets"},
		{"result", "compounds"},
		{"data", "compounds"},
		{"compounds"},
		{"result", "entries"},
		{"data", "entries"},
		{"entries"},
		{"result", "structures"},
		{"data", "structures"},
		{"structures"},
		{"result", "datasets"},
		{"data", "datasets"},
		{"datasets"},
		{"result", "variants"},
		{"data", "variants"},
		{"variants"},
		{"result", "items"},
		{"data", "items"},
		{"items"},
		{"result", "results"},
		{"data", "results"},
		{"results"},
		{"result", "articles"},
		{"data", "articles"},
		{"articles"},
		{"result", "trials"},
		{"data", "trials"},
		{"trials"},
		{"result", "studies"},
		{"data", "studies"},
		{"studies"},
		{"result", "pmids"},
		{"data", "pmids"},
		{"pmids"},
	} {
		if collection, ok := valueAtJSONPath(value, path).([]any); ok {
			return len(collection), true
		}
	}
	return 0, false
}

func valueAtJSONPath(value any, path []string) any {
	current := value
	for _, key := range path {
		record, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = record[key]
	}
	return current
}

func compactWebConversationNonNegativeInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		if typed >= 0 {
			return typed, true
		}
	case int64:
		if typed >= 0 && typed <= int64(math.MaxInt) {
			return int(typed), true
		}
	case float64:
		if typed >= 0 && typed <= float64(math.MaxInt) && math.Trunc(typed) == typed {
			return int(typed), true
		}
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil && parsed >= 0 && parsed <= int64(math.MaxInt) {
			return int(parsed), true
		}
	}
	return 0, false
}
