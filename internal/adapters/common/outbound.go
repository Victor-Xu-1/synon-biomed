package common

import "strings"

type OutboundChunk struct {
	Kind      string `json:"kind"`
	Text      string `json:"text,omitempty"`
	Complete  bool   `json:"complete,omitempty"`
	ToolName  string `json:"toolName,omitempty"`
	ToolUseID string `json:"toolUseId,omitempty"`
	RequestID string `json:"requestId,omitempty"`
}

func BuildOutboundChunks(message ServerMessage) []OutboundChunk {
	if len(message) == 0 {
		return nil
	}
	switch strings.TrimSpace(stringValue(message["type"])) {
	case "content_delta", "assistant_message":
		return textChunk("text", firstStringValue(message, "text", "content", "message"))
	case "reasoning_delta":
		return textChunk("reasoning", firstStringValue(message, "text", "content", "message"))
	case "tool_use":
		toolName := firstStringValue(message, "toolName", "name")
		text := FormatToolUse(toolName, message["input"])
		return []OutboundChunk{{
			Kind:      "tool",
			Text:      text,
			ToolName:  toolName,
			ToolUseID: firstStringValue(message, "toolUseID", "toolUseId"),
		}}
	case "tool_use_complete", "tool_result":
		toolName := firstStringValue(message, "toolName", "name")
		text := strings.TrimSpace(firstStringValue(message, "message", "text", "content"))
		if text == "" {
			text = "工具完成"
		}
		if toolName != "" {
			text = toolName + ": " + text
		}
		return []OutboundChunk{{
			Kind:      "tool",
			Text:      text,
			ToolName:  toolName,
			ToolUseID: firstStringValue(message, "toolUseID", "toolUseId"),
		}}
	case "permission_request":
		toolName := firstStringValue(message, "toolName", "name")
		requestID := firstStringValue(message, "requestId", "requestID")
		return []OutboundChunk{{
			Kind:      "permission",
			Text:      FormatPermissionRequest(toolName, message["input"], requestID),
			ToolName:  toolName,
			ToolUseID: firstStringValue(message, "toolUseID", "toolUseId"),
			RequestID: requestID,
		}}
	case "error":
		text := firstStringValue(message, "message", "error", "text", "content")
		if text == "" {
			text = "未知错误"
		}
		return []OutboundChunk{{Kind: "error", Text: text}}
	case "message_complete":
		return []OutboundChunk{{Kind: "complete", Complete: true}}
	default:
		return nil
	}
}

func textChunk(kind string, text string) []OutboundChunk {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	return []OutboundChunk{{Kind: kind, Text: text}}
}

func firstStringValue(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(stringValue(values[key])); value != "" {
			return value
		}
	}
	return ""
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
