package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"unicode/utf8"

	runtimekv "synon-go/internal/persistence/runtimekv"
)

// Keep the agent-facing runtime_list result small enough that a diagnostic
// listing does not become a durable copy of a large runtime value in every
// subsequent model request. runtime_get remains the exact-value read path.
const agentRuntimeListValuePreviewBytes = 2048

func compactAgentRuntimeListToolResponse(response any) any {
	payload, ok := response.(map[string]any)
	if !ok {
		return response
	}
	entries, ok := payload["entries"].([]runtimekv.Entry)
	if !ok {
		return response
	}

	compacted := make([]runtimekv.Entry, len(entries))
	changed := false
	for index, entry := range entries {
		compacted[index] = entry
		value, valueChanged := compactAgentRuntimeListValue(entry)
		if valueChanged {
			compacted[index].Value = value
			changed = true
		}
	}
	if !changed {
		return response
	}

	result := make(map[string]any, len(payload)+1)
	for key, value := range payload {
		result[key] = value
	}
	result["entries"] = compacted
	result["valuePolicy"] = "Large runtime_list values are bounded previews; use runtime_get with readWith.namespace and readWith.key for the exact value."
	return result
}

func compactAgentRuntimeListValue(entry runtimekv.Entry) (any, bool) {
	encoded, err := json.Marshal(entry.Value)
	if err != nil || len(encoded) <= agentRuntimeListValuePreviewBytes {
		return entry.Value, false
	}

	preview := encoded[:agentRuntimeListPreviewLength(encoded)]
	digest := sha256.Sum256(encoded)
	return map[string]any{
		"truncated":     true,
		"valueBytes":    len(encoded),
		"sha256":        hex.EncodeToString(digest[:]),
		"preview":       string(preview),
		"previewBytes":  len(preview),
		"previewFormat": "jsonPrefix",
		"readWith": map[string]any{
			"tool":      "runtime_get",
			"namespace": entry.Namespace,
			"key":       entry.Key,
		},
	}, true
}

func agentRuntimeListPreviewLength(encoded []byte) int {
	limit := len(encoded)
	if limit > agentRuntimeListValuePreviewBytes {
		limit = agentRuntimeListValuePreviewBytes
	}
	for limit > 0 && !utf8.Valid(encoded[:limit]) {
		limit--
	}
	return limit
}
