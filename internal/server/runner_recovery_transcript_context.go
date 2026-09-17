package server

import (
	"encoding/json"
	"fmt"
	"strings"
	eventjournal "synon-go/internal/persistence/journal"
)

func recoveredToolTranscriptContext(entries []eventjournal.Entry, afterEventID int64) string {
	lines := make([]string, 0, 12)
	for _, entry := range entries {
		if afterEventID > 0 && entry.EventID <= afterEventID {
			continue
		}
		if strings.TrimSpace(stringValue(entry.Message["type"])) != "runner_checkpoint" {
			continue
		}
		toolName := strings.TrimSpace(stringValue(entry.Message["toolName"]))
		if toolName == "" {
			continue
		}
		// Checkpoints with a toolPhase are projected as native provider tool
		// messages; only legacy pre-protocol tool checkpoints (toolName without
		// toolPhase/modelToolCalls) need advisory recovery prose.
		if strings.TrimSpace(stringValue(entry.Message["toolPhase"])) != "" ||
			compactJSONForRuntimeContext(entry.Message["modelToolCalls"]) != "" {
			continue
		}
		line := fmt.Sprintf("event %d: tool=%s", entry.EventID, toolName)
		if status := strings.TrimSpace(stringValue(entry.Message["status"])); status != "" {
			line += " status=" + status
		}
		if callID := strings.TrimSpace(stringValue(entry.Message["toolCallId"])); callID != "" {
			line += " call=" + callID
		}
		if input := compactJSONForRuntimeContext(entry.Message["toolInput"]); input != "" {
			line += " input=" + input
		}
		if result := compactJSONForRuntimeContext(entry.Message["toolResult"]); result != "" {
			line += " result=" + result
		}
		lines = append(lines, line)
	}
	const maxToolContextEvents = 20
	if len(lines) > maxToolContextEvents {
		lines = lines[len(lines)-maxToolContextEvents:]
	}
	if len(lines) == 0 {
		return ""
	}
	return "Recovered tool transcript:\n" + strings.Join(lines, "\n")
}

func compactJSONForRuntimeContext(value any) string {
	if value == nil {
		return ""
	}
	if text := strings.TrimSpace(stringValue(value)); text != "" {
		return text
	}
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func latestCompactModelContext(entries []eventjournal.Entry) (string, int64, bool) {
	var summary string
	var eventID int64
	for _, entry := range entries {
		if !isCompactJournalEntry(entry) {
			continue
		}
		candidate := compactSummaryFromMessage(entry.Message)
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		summary = strings.TrimSpace(candidate)
		eventID = entry.EventID
	}
	return summary, eventID, summary != ""
}

func isCompactJournalEntry(entry eventjournal.Entry) bool {
	eventType := strings.TrimSpace(stringValue(entry.Message["type"]))
	if eventType == "session_compact" || eventType == "Compact" || eventType == "compact" {
		return true
	}
	return eventType == "runner_checkpoint" &&
		strings.EqualFold(strings.TrimSpace(stringValue(entry.Message["toolPhase"])), "auto_compact") &&
		strings.TrimSpace(compactSummaryFromMessage(entry.Message)) != ""
}

func compactSummaryFromMessage(message eventjournal.Message) string {
	if modelContext, ok := message["modelContext"].(map[string]any); ok {
		if summary := strings.TrimSpace(stringValue(modelContext["summary"])); summary != "" {
			return summary
		}
	}
	if checkpoint, ok := message["checkpoint"].(map[string]any); ok {
		if modelContext, ok := checkpoint["modelContext"].(map[string]any); ok {
			if summary := strings.TrimSpace(stringValue(modelContext["summary"])); summary != "" {
				return summary
			}
		}
		if summary := strings.TrimSpace(stringValue(checkpoint["summary"])); summary != "" {
			return summary
		}
	}
	return strings.TrimSpace(stringValue(message["summary"]))
}

func runnerMessageText(message eventjournal.Message) string {
	for _, key := range []string{"text", "content"} {
		if text := strings.TrimSpace(stringValue(message[key])); text != "" {
			return text
		}
	}
	for _, key := range []string{"content", "parts", "attachments"} {
		if text := runnerStructuredContentText(message[key]); text != "" {
			return text
		}
	}
	if nested, ok := message["message"].(map[string]any); ok {
		for _, key := range []string{"text", "content"} {
			if text := strings.TrimSpace(stringValue(nested[key])); text != "" {
				return text
			}
		}
		for _, key := range []string{"content", "parts", "attachments"} {
			if text := runnerStructuredContentText(nested[key]); text != "" {
				return text
			}
		}
	}
	return ""
}

func runnerStructuredContentText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := runnerStructuredContentText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		return runnerContentBlockText(typed)
	default:
		return ""
	}
}

func runnerContentBlockText(block map[string]any) string {
	blockType := strings.ToLower(strings.TrimSpace(firstNonEmpty(
		stringValue(block["type"]),
		stringValue(block["tag"]),
		stringValue(block["kind"]),
	)))
	for _, key := range []string{"text", "content"} {
		if text := strings.TrimSpace(stringValue(block[key])); text != "" {
			return text
		}
	}
	switch blockType {
	case "text", "plain_text", "input_text", "markdown", "mrkdwn":
		return ""
	case "tool_use":
		name := strings.TrimSpace(firstNonEmpty(stringValue(block["name"]), stringValue(block["tool_name"])))
		identifier := strings.TrimSpace(firstNonEmpty(stringValue(block["id"]), stringValue(block["tool_use_id"])))
		input := compactJSONForRuntimeContext(block["input"])
		return strings.TrimSpace(fmt.Sprintf("[tool use %s id=%s input=%s]", name, identifier, input))
	case "tool_result":
		identifier := strings.TrimSpace(firstNonEmpty(stringValue(block["tool_use_id"]), stringValue(block["id"])))
		content := compactJSONForRuntimeContext(block["content"])
		return strings.TrimSpace(fmt.Sprintf("[tool result id=%s content=%s]", identifier, content))
	case "image", "img", "image_url", "input_image":
		return runnerMediaBlockLabel("image", block,
			"image_url", "url", "imageKey", "image_key", "filePath", "file_path", "path")
	case "file", "attachment", "input_file":
		return runnerMediaBlockLabel("file", block,
			"file_name", "fileName", "name", "filename", "title", "file_key", "fileKey", "path", "url")
	case "audio", "input_audio":
		return runnerMediaBlockLabel("audio", block, "name", "file_name", "url", "path")
	case "video":
		return runnerMediaBlockLabel("video", block, "name", "file_name", "url", "path")
	}
	for _, key := range []string{"children", "content", "elements", "items"} {
		if text := runnerStructuredContentText(block[key]); text != "" {
			return text
		}
	}
	if blockType != "" {
		return "[" + blockType + " content]"
	}
	return ""
}

func runnerMediaBlockLabel(kind string, block map[string]any, keys ...string) string {
	label := ""
	for _, key := range keys {
		value := block[key]
		if nested, ok := value.(map[string]any); ok {
			for _, nestedKey := range []string{"url", "path", "name", "file_name", "fileName"} {
				if text := strings.TrimSpace(stringValue(nested[nestedKey])); text != "" {
					label = text
					break
				}
			}
		} else if text := strings.TrimSpace(stringValue(value)); text != "" {
			label = text
		}
		if label != "" {
			break
		}
	}
	if label == "" {
		return "[" + kind + "]"
	}
	return "[" + kind + ": " + label + "]"
}
