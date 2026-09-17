package memoryextract

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"synon-go/internal/memorypolicy"
)

var recalledWhitespace = regexp.MustCompile(`\s+`)

func ParseEmitted(raw map[string]any, knownIDs map[string]struct{}, maxPerKind int) Operations {
	operations := Operations{Append: []AppendOperation{}, Replace: []ReplaceOperation{}, Remove: []string{}}
	for _, value := range cappedArray(raw["append"], maxPerKind) {
		object, ok := value.(map[string]any)
		if !ok {
			continue
		}
		text := strings.TrimSpace(stringValue(object["text"]))
		if text == "" {
			continue
		}
		operations.Append = append(operations.Append, AppendOperation{
			Text: text, Evidence: normalizedEvidence(stringValue(object["evidence"])),
			Entity: optionalString(object["entity"]), Category: optionalString(object["category"]),
		})
	}

	replacements := make(map[string]ReplaceOperation)
	replacementOrder := make([]string, 0)
	for _, value := range cappedArray(raw["replace"], maxPerKind) {
		object, ok := value.(map[string]any)
		if !ok {
			continue
		}
		id := stringValue(object["id"])
		text := strings.TrimSpace(stringValue(object["text"]))
		if text == "" {
			continue
		}
		if _, known := knownIDs[id]; !known {
			continue
		}
		if _, exists := replacements[id]; !exists {
			replacementOrder = append(replacementOrder, id)
		}
		evidence := ""
		if truthy(object["evidence"]) {
			evidence = normalizedEvidence(stringValue(object["evidence"]))
		}
		replacements[id] = ReplaceOperation{ID: id, Text: text, Evidence: evidence}
	}

	removed := make(map[string]struct{})
	for _, value := range cappedArray(raw["remove"], maxPerKind) {
		id := stringValue(value)
		if _, known := knownIDs[id]; !known {
			continue
		}
		operations.Remove = append(operations.Remove, id)
		removed[id] = struct{}{}
	}
	for _, id := range replacementOrder {
		if _, removeWins := removed[id]; !removeWins {
			operations.Replace = append(operations.Replace, replacements[id])
		}
	}
	return operations
}

func ParseEmittedJSON(value []byte, knownIDs map[string]struct{}, maxPerKind int) (Operations, error) {
	var raw map[string]any
	if err := json.Unmarshal(value, &raw); err != nil {
		return Operations{}, fmt.Errorf("decode emitted memories: %w", err)
	}
	return ParseEmitted(raw, knownIDs, maxPerKind), nil
}

func DropRecalledOperations(operations Operations, bodies []string) Operations {
	return DropMatchingOperations(operations, bodies)
}

func DropMatchingOperations(operations Operations, bodies []string) Operations {
	normalizedBodies := make([]string, 0, len(bodies))
	for _, body := range bodies {
		normalized := normalizeRecallMatch(body)
		if memorypolicy.UTF16Length(normalized) >= 20 {
			normalizedBodies = append(normalizedBodies, normalized)
		}
	}
	if len(normalizedBodies) == 0 || len(operations.Append) == 0 && len(operations.Replace) == 0 {
		return operations
	}
	matches := func(value string) bool {
		normalized := normalizeRecallMatch(value)
		if memorypolicy.UTF16Length(normalized) < 20 {
			return false
		}
		for _, body := range normalizedBodies {
			if strings.Contains(body, normalized) || strings.Contains(normalized, body) {
				return true
			}
		}
		return false
	}
	filtered := Operations{
		Append:  make([]AppendOperation, 0, len(operations.Append)),
		Replace: make([]ReplaceOperation, 0, len(operations.Replace)),
		Remove:  append([]string(nil), operations.Remove...),
	}
	for _, operation := range operations.Append {
		if !matches(operation.Text) {
			filtered.Append = append(filtered.Append, operation)
		}
	}
	for _, operation := range operations.Replace {
		if !matches(operation.Text) {
			filtered.Replace = append(filtered.Replace, operation)
		}
	}
	return filtered
}

func normalizeRecallMatch(value string) string {
	value = strings.NewReplacer("≡", "=", "∶", ":", "‑", "-").Replace(value)
	return strings.TrimSpace(strings.ToLower(recalledWhitespace.ReplaceAllString(value, " ")))
}

func cappedArray(value any, limit int) []any {
	array, ok := value.([]any)
	if !ok || limit == 0 {
		return nil
	}
	if limit < 0 {
		limit = len(array) + limit
		if limit <= 0 {
			return nil
		}
	}
	if len(array) > limit {
		return array[:limit]
	}
	return array
}

func arrayLength(value any) int {
	array, ok := value.([]any)
	if !ok {
		return 0
	}
	return len(array)
}

func normalizedEvidence(value string) string {
	switch value {
	case "stated", "observed", "inferred":
		return value
	default:
		return "inferred"
	}
}

func optionalString(value any) string {
	if !truthy(value) {
		return ""
	}
	return stringValue(value)
}

func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case float64:
		return typed != 0 && !math.IsNaN(typed)
	case float32:
		return typed != 0 && !math.IsNaN(float64(typed))
	case int:
		return typed != 0
	case int64:
		return typed != 0
	default:
		return true
	}
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 32)
	case bool:
		return strconv.FormatBool(typed)
	case map[string]any:
		return "[object Object]"
	case []any:
		parts := make([]string, len(typed))
		for index, item := range typed {
			parts[index] = stringValue(item)
		}
		return strings.Join(parts, ",")
	default:
		return fmt.Sprint(typed)
	}
}
