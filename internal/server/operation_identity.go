package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// stableServerOperationID derives an idempotency identity from the governed
// authority request rather than from a provider/model call id. Call ids are
// transport metadata and change when a task is resumed or a model retries;
// using them as execution identity creates parallel operations for one intent.
func stableServerOperationID(prefix, scope string, authorityInput any) (string, error) {
	raw, err := json.Marshal(authorityInput)
	if err != nil {
		return "", fmt.Errorf("operation authority is not serializable: %w", err)
	}
	digest := sha256.Sum256(append([]byte("synon-server-operation-v1\x00"+strings.TrimSpace(prefix)+"\x00"+strings.TrimSpace(scope)+"\x00"), raw...))
	return strings.TrimSpace(prefix) + "-" + hex.EncodeToString(digest[:12]), nil
}

// stableAuthorityInput removes transport and presentation fields before an
// authority identity is derived. The operation contract remains in the input;
// only fields that cannot change the requested work are omitted.
func stableAuthorityInput(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	result := make(map[string]any, len(input))
	for key, value := range input {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "human_description", "call_id", "tool_call_id", "operation_id", "notification_id", "background":
			continue
		}
		result[key] = stableAuthorityValue(value)
	}
	return result
}

func stableAuthorityValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, nested := range typed {
			result[key] = stableAuthorityValue(nested)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = stableAuthorityValue(item)
		}
		return result
	default:
		return value
	}
}
