package transcript

import (
	"encoding/json"
	"math"
	"strings"
)

const (
	AssistantSegmentVersionV1          = 1
	AssistantSegmentMaxOrdinal   int64 = 1_000_000
	AssistantReplaceScopeAttempt       = "attempt"
	AssistantReplaceScopeSegment       = "segment"
)

type AssistantSegmentV1 struct {
	Ordinal      int64
	ReplaceScope string
}

func ParseAssistantSegmentV1(payload map[string]any) (AssistantSegmentV1, bool, error) {
	raw, present := payload["assistant_segment"]
	if !present {
		return AssistantSegmentV1{}, false, nil
	}
	object, ok := raw.(map[string]any)
	if !ok || len(object) < 2 || len(object) > 3 {
		return AssistantSegmentV1{}, true, ErrEventConflict
	}
	for key := range object {
		if key != "version" && key != "ordinal" && key != "replace_scope" {
			return AssistantSegmentV1{}, true, ErrEventConflict
		}
	}
	version, ok := exactAssistantSegmentInteger(object["version"])
	if !ok || version != AssistantSegmentVersionV1 {
		return AssistantSegmentV1{}, true, ErrEventConflict
	}
	ordinal, ok := exactAssistantSegmentInteger(object["ordinal"])
	if !ok || ordinal <= 0 || ordinal > AssistantSegmentMaxOrdinal {
		return AssistantSegmentV1{}, true, ErrEventConflict
	}
	replaceScope := ""
	if rawScope, present := object["replace_scope"]; present {
		replaceScope, ok = rawScope.(string)
		replaceScope = strings.TrimSpace(replaceScope)
		if !ok || (replaceScope != AssistantReplaceScopeAttempt && replaceScope != AssistantReplaceScopeSegment) {
			return AssistantSegmentV1{}, true, ErrEventConflict
		}
	}
	return AssistantSegmentV1{Ordinal: ordinal, ReplaceScope: replaceScope}, true, nil
}

func AssistantSegmentPayloadV1(ordinal int64, replaceScope string) map[string]any {
	payload := map[string]any{"version": AssistantSegmentVersionV1, "ordinal": ordinal}
	if strings.TrimSpace(replaceScope) != "" {
		payload["replace_scope"] = strings.TrimSpace(replaceScope)
	}
	return payload
}

func exactAssistantSegmentInteger(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case json.Number:
		integer, err := typed.Int64()
		return integer, err == nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || math.Trunc(typed) != typed || typed > math.MaxInt64 || typed < math.MinInt64 {
			return 0, false
		}
		return int64(typed), true
	default:
		return 0, false
	}
}
