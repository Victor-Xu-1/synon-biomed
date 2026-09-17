package server

import (
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func transcriptWebAssistantMessageType(payload map[string]any) string {
	if strings.EqualFold(strings.TrimSpace(firstNonEmpty(
		webString(payload["block_type"]), webString(payload["blockType"]),
	)), "thinking") {
		return "thinking"
	}
	return "text"
}

func transcriptWebAssistantContent(text, messageType string) map[string]any {
	content := map[string]any{"content": text}
	if messageType == "thinking" {
		content["status"] = "thinking"
	}
	return content
}

func transcriptWebAssistantMessageAcceptsPayload(message, payload map[string]any) bool {
	return strings.TrimSpace(webString(message["type"])) == transcriptWebAssistantMessageType(payload)
}

func transcriptWebSettleAssistantMessage(message map[string]any, status string) error {
	message["status"] = status
	if strings.TrimSpace(webString(message["type"])) != "thinking" {
		return nil
	}
	content, ok := message["content"].(map[string]any)
	if !ok {
		return transcriptstore.ErrEventConflict
	}
	content["status"] = "done"
	return nil
}
