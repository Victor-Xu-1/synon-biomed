package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	compactWebToolInputBytes       = 1024
	compactWebToolOutputBytes      = 2048
	compactWebToolDescriptionBytes = 512
)

func compactWebConversationMessagePage(r *http.Request, messages []map[string]any) []map[string]any {
	if r == nil || !strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("content_mode")), "compact") {
		return messages
	}
	compacted := make([]map[string]any, len(messages))
	for index, message := range messages {
		compacted[index] = compactWebConversationMessage(message)
	}
	return compacted
}

func compactWebConversationMessage(message map[string]any) map[string]any {
	if webString(message["type"]) != "tool_call" {
		return message
	}
	content, ok := message["content"].(map[string]any)
	if !ok {
		return message
	}
	// Ask User history is an interaction contract, not diagnostic tool output.
	// Its structured question/options and exact answer are required to render,
	// edit, and recover every completed clarification round. The Ask User schema
	// already bounds these fields, so returning the lossless message does not
	// create an unbounded compact-history response.
	if _, askUser := transcriptstore.CanonicalAskUserToolNameV1(webString(content["name"])); askUser {
		return message
	}
	nextContent := make(map[string]any, len(content)+1)
	for key, value := range content {
		nextContent[key] = value
	}
	originalSize := 0
	truncated := false
	for _, field := range []struct {
		name  string
		limit int
	}{
		{name: "description", limit: compactWebToolDescriptionBytes},
		{name: "input", limit: compactWebToolInputBytes},
		{name: "output", limit: compactWebToolOutputBytes},
	} {
		value, found := nextContent[field.name]
		if !found {
			continue
		}
		preview, size, clipped := compactWebConversationValue(value, field.limit)
		originalSize += size
		nextContent[field.name] = preview
		truncated = truncated || clipped
	}
	if _, hasInput := nextContent["input"]; hasInput {
		if args, found := nextContent["args"]; found {
			raw, _ := json.Marshal(args)
			originalSize += len(raw)
			delete(nextContent, "args")
			truncated = true
		}
	} else if args, found := nextContent["args"]; found {
		preview, size, clipped := compactWebConversationValue(args, compactWebToolInputBytes)
		originalSize += size
		nextContent["args"] = preview
		truncated = truncated || clipped
	}
	if !truncated {
		return message
	}
	compactMetadata := map[string]any{
		"truncated": true, "original_size": originalSize,
	}
	if humanDescription := compactWebConversationHumanDescription(content); humanDescription != "" {
		compactMetadata["human_description"] = humanDescription
	}
	if resultCount, ok := compactWebConversationResultCount(content["output"]); ok {
		compactMetadata["result_count"] = resultCount
	}
	nextContent["_compact"] = compactMetadata
	next := make(map[string]any, len(message))
	for key, value := range message {
		next[key] = value
	}
	next["content"] = nextContent
	return next
}

func compactWebConversationValue(value any, limit int) (any, int, bool) {
	raw, err := json.Marshal(value)
	if err != nil {
		return value, 0, false
	}
	if len(raw) <= limit {
		return value, len(raw), false
	}
	if text, ok := value.(string); ok {
		if structured, found := compactWebConversationJSONText(text, limit); found {
			return structured, len(raw), true
		}
		return compactWebConversationText(text, limit), len(raw), true
	}
	return compactWebConversationStructuredValue(value, limit), len(raw), true
}

func compactWebConversationText(value string, limit int) string {
	if encoded, err := json.Marshal(value); err == nil && len(encoded) <= limit {
		return value
	}
	suffix := "…"
	runes := []rune(value)
	low, high := 0, len(runes)
	for low < high {
		middle := low + (high-low+1)/2
		candidate := string(runes[:middle]) + suffix
		encoded, err := json.Marshal(candidate)
		if err == nil && len(encoded) <= limit {
			low = middle
		} else {
			high = middle - 1
		}
	}
	return string(runes[:low]) + suffix
}

func compactWebConversationJSONText(value string, limit int) (string, bool) {
	if !json.Valid([]byte(value)) {
		return "", false
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if decoder.Decode(&decoded) != nil || (decoded == nil) {
		return "", false
	}
	if _, object := decoded.(map[string]any); !object {
		if _, array := decoded.([]any); !array {
			return "", false
		}
	}
	low, high := 2, max(2, limit)
	best := "{}"
	if _, array := decoded.([]any); array {
		best = "[]"
	}
	for low <= high {
		middle := low + (high-low)/2
		preview, err := json.Marshal(compactWebConversationStructuredValue(decoded, middle))
		if err != nil {
			return "", false
		}
		wrapped, err := json.Marshal(string(preview))
		if err == nil && len(wrapped) <= limit {
			best = string(preview)
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	return best, true
}

func compactWebConversationStructuredValue(value any, limit int) any {
	switch typed := value.(type) {
	case map[string]any:
		result := map[string]any{}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.SliceStable(keys, func(left, right int) bool {
			if keys[left] == "human_description" {
				return true
			}
			if keys[right] == "human_description" {
				return false
			}
			return keys[left] < keys[right]
		})
		for _, key := range keys {
			current, _ := json.Marshal(result)
			encodedKey, _ := json.Marshal(key)
			remaining := limit - len(current) - len(encodedKey) - 2
			if remaining < 2 {
				continue
			}
			result[key] = compactWebConversationStructuredValue(typed[key], remaining)
			encoded, err := json.Marshal(result)
			if err != nil || len(encoded) > limit {
				delete(result, key)
			}
		}
		return result
	case []any:
		result := make([]any, 0, min(len(typed), 100))
		for _, candidate := range typed {
			current, _ := json.Marshal(result)
			remaining := limit - len(current) - 1
			if remaining < 2 {
				break
			}
			result = append(result, compactWebConversationStructuredValue(candidate, remaining))
			encoded, err := json.Marshal(result)
			if err != nil || len(encoded) > limit {
				result = result[:len(result)-1]
				break
			}
		}
		return result
	case string:
		return compactWebConversationText(typed, max(4, limit))
	default:
		return value
	}
}

func compactWebConversationHumanDescription(content map[string]any) string {
	for _, field := range []string{"input", "args"} {
		input, ok := content[field].(map[string]any)
		if !ok {
			continue
		}
		description, ok := input["human_description"].(string)
		description = strings.TrimSpace(description)
		if !ok || description == "" || len(description) > compactWebToolDescriptionBytes {
			continue
		}
		valid := true
		for _, character := range description {
			if character < 0x20 && character != '\t' {
				valid = false
				break
			}
		}
		if valid {
			return description
		}
	}
	return ""
}
