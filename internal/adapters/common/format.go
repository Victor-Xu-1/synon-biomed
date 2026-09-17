package common

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

// SplitMessage splits long IM text while preserving paragraph, line, sentence,
// and word boundaries where possible.
func SplitMessage(text string, limit int) []string {
	if limit <= 0 || len(text) <= limit {
		return []string{text}
	}

	var chunks []string
	remaining := text
	for len(remaining) > 0 {
		if len(remaining) <= limit {
			chunks = append(chunks, remaining)
			break
		}

		window := remaining[:limit]
		splitAt := strings.LastIndex(window, "\n\n")
		if splitAt <= 0 {
			splitAt = strings.LastIndex(window, "\n")
		}
		if splitAt <= 0 {
			splitAt = strings.LastIndex(window, ". ")
		}
		if splitAt <= 0 {
			splitAt = strings.LastIndex(window, " ")
		}
		if splitAt <= 0 {
			splitAt = limit
		}

		if splitAt < len(remaining) && (remaining[splitAt] == '\n' || remaining[splitAt] == '.') {
			splitAt++
		}

		chunks = append(chunks, strings.TrimRightFunc(remaining[:splitAt], unicode.IsSpace))
		remaining = strings.TrimLeftFunc(remaining[splitAt:], unicode.IsSpace)
	}
	return chunks
}

func FormatToolUse(toolName string, input any) string {
	if summary := formatToolSummary(toolName, input); summary != "" {
		return fmt.Sprintf("🔧 %s  %s", toolName, summary)
	}
	return fmt.Sprintf("🔧 %s\n%s", toolName, TruncateInput(input, 200))
}

func FormatPermissionRequest(toolName string, input any, requestID string) string {
	return fmt.Sprintf("🔐 需要权限确认 [%s]\n工具: %s\n%s", requestID, toolName, TruncateInput(input, 300))
}

func TruncateInput(input any, maxLen int) string {
	var text string
	switch value := input.(type) {
	case string:
		text = value
	default:
		data, err := json.MarshalIndent(input, "", "  ")
		if err != nil {
			return "(unserializable)"
		}
		text = string(data)
	}
	return truncateRunes(text, maxLen)
}

func EscapeMarkdownV2(text string) string {
	var builder strings.Builder
	for _, r := range text {
		switch r {
		case '_', '*', '[', ']', '(', ')', '~', '`', '>', '#', '+', '-', '=', '|', '{', '}', '.', '!', '\\':
			builder.WriteRune('\\')
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

func formatToolSummary(toolName string, input any) string {
	inputMap, ok := input.(map[string]any)
	if !ok {
		return ""
	}

	switch toolName {
	case "Bash":
		if desc, ok := stringField(inputMap, "description"); ok {
			return desc
		}
		if command, ok := stringField(inputMap, "command"); ok {
			return truncateRunes(command, 120)
		}
	case "Read", "Edit", "Write":
		if filePath, ok := stringField(inputMap, "file_path"); ok {
			return shortPath(filePath)
		}
	case "Grep":
		pattern, hasPattern := stringField(inputMap, "pattern")
		path, hasPath := stringField(inputMap, "path")
		if hasPattern && hasPath {
			return fmt.Sprintf("%q in %s", truncateRunes(pattern, 60), shortPath(path))
		}
		if hasPattern {
			return fmt.Sprintf("%q", truncateRunes(pattern, 60))
		}
	case "Glob":
		if pattern, ok := stringField(inputMap, "pattern"); ok {
			return fmt.Sprintf("%q", pattern)
		}
	case "Skill":
		if skill, ok := stringField(inputMap, "skill"); ok {
			return skill
		}
	case "Agent":
		if desc, ok := stringField(inputMap, "description"); ok {
			return desc
		}
	case "WebFetch":
		if url, ok := stringField(inputMap, "url"); ok {
			return truncateRunes(url, 120)
		}
	case "WebSearch":
		if query, ok := stringField(inputMap, "query"); ok {
			return fmt.Sprintf("%q", truncateRunes(query, 80))
		}
	}
	return ""
}

func stringField(input map[string]any, key string) (string, bool) {
	value, ok := input[key]
	if !ok {
		return "", false
	}
	text, ok := value.(string)
	return text, ok && text != ""
}

func shortPath(filePath string) string {
	parts := strings.Split(filePath, "/")
	if len(parts) > 3 {
		return ".../" + strings.Join(parts[len(parts)-3:], "/")
	}
	return filePath
}

func truncateRunes(text string, maxLen int) string {
	if maxLen < 0 {
		maxLen = 0
	}
	runes := []rune(text)
	if len(runes) <= maxLen {
		return text
	}
	return string(runes[:maxLen]) + "..."
}
