package server

import (
	"encoding/json"
	"strings"

	"synon-go/internal/agentruntime"
	"synon-go/internal/memoryextract"
	"synon-go/internal/memorytools"
	eventjournal "synon-go/internal/persistence/journal"
)

func memoryExtractionMessages(messages []agentruntime.Message) []memoryextract.Message {
	result := make([]memoryextract.Message, 0, len(messages))
	for _, message := range messages {
		role := strings.TrimSpace(message.Role)
		blocks := make([]memoryextract.Block, 0, len(message.Parts)+len(message.ToolCalls)+1)
		if message.Content != "" {
			blocks = append(blocks, memoryExtractionTextBlock(message.Content))
		}
		for _, part := range message.Parts {
			switch part.Type {
			case agentruntime.ContentPartText:
				blocks = append(blocks, memoryExtractionTextBlock(part.Text))
			case agentruntime.ContentPartImage:
				diskReference := part.Media != nil && part.Media.Source.Type == agentruntime.MediaSourceFile
				blocks = append(blocks, memoryextract.Block{Type: memoryextract.BlockImage, DiskReference: diskReference})
			}
		}
		for _, call := range message.ToolCalls {
			blocks = append(blocks, memoryextract.Block{
				Type: memoryextract.BlockToolUse, ToolName: strings.TrimSpace(call.Name),
				ToolUseID: strings.TrimSpace(call.ID), ToolInput: memoryExtractionToolInput(call.Arguments),
			})
		}
		if role == "tool" {
			role = "user"
			content := memoryExtractionToolResultText(message)
			blocks = []memoryextract.Block{{
				Type: memoryextract.BlockToolResult, ToolUseID: strings.TrimSpace(message.ToolCallID),
				Text: content, ToolError: memoryExtractionToolResultError(content),
			}}
		}
		result = append(result, memoryextract.Message{Role: role, Content: blocks})
	}
	return result
}

// memoryExtractionMessagesFromJournalEntry projects the durable transcript,
// not the compacted Runner replay window. Normal conversation blocks are read
// directly so local-image references and tool-result error flags survive; the
// structured Runner checkpoint decoder remains the authority for model/tool
// checkpoint payloads.
func memoryExtractionMessagesFromJournalEntry(entry eventjournal.Entry) []memoryextract.Message {
	messageType := strings.ToLower(strings.TrimSpace(stringValue(entry.Message["type"])))
	if messageType == "runner_checkpoint" {
		return memoryExtractionCheckpointMessages(entry)
	}
	if strings.HasSuffix(messageType, "_delta") {
		return nil
	}
	switch messageType {
	case "status", "streaming_batch", "runner_started", "runner_finished", "tool_progress", "rolling_compact_status", "session_compact", "compact":
		return nil
	}
	role := strings.ToLower(strings.TrimSpace(stringValue(entry.Message["role"])))
	if role != "user" && role != "assistant" {
		return nil
	}
	if role == "assistant" && boolValue(entry.Message["partial"], false) {
		return nil
	}
	blocks := make([]memoryextract.Block, 0)
	rawBlocks, _ := entry.Message["content"].([]any)
	for _, rawBlock := range rawBlocks {
		block, ok := rawBlock.(map[string]any)
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(stringValue(block["type"]))) {
		case "text":
			text := firstNonEmpty(stringValue(block["text"]), stringValue(block["content"]))
			if text != "" {
				blocks = append(blocks, memoryExtractionTextBlock(text))
			}
		case "image":
			source, _ := block["source"].(map[string]any)
			sourceType := strings.ToLower(strings.TrimSpace(stringValue(source["type"])))
			diskReference := sourceType == "_disk_ref" || sourceType == string(agentruntime.MediaSourceFile)
			if !diskReference {
				diskReference = strings.TrimSpace(firstNonEmpty(
					stringValue(source["path"]), stringValue(block["path"]),
				)) != ""
			}
			blocks = append(blocks, memoryextract.Block{Type: memoryextract.BlockImage, DiskReference: diskReference})
		case "tool_use":
			name := strings.TrimSpace(stringValue(block["name"]))
			if name == "" {
				continue
			}
			blocks = append(blocks, memoryextract.Block{
				Type: memoryextract.BlockToolUse, ToolName: name,
				ToolUseID: strings.TrimSpace(stringValue(block["id"])),
				ToolInput: memoryExtractionJSONMap(block["input"]),
			})
		case "tool_result":
			content := runnerStructuredContentText(block["content"])
			toolError := boolValue(block["is_error"], false) || boolValue(block["error"], false) || memoryExtractionToolResultError(content)
			blocks = append(blocks, memoryextract.Block{
				Type: memoryextract.BlockToolResult,
				ToolUseID: strings.TrimSpace(firstNonEmpty(
					stringValue(block["tool_use_id"]), stringValue(block["toolCallId"]), stringValue(block["tool_call_id"]),
				)),
				Text: content, ToolError: toolError,
			})
		}
	}
	if len(blocks) == 0 {
		if text := runnerMessageText(entry.Message); text != "" {
			blocks = append(blocks, memoryExtractionTextBlock(text))
		}
	}
	if len(blocks) == 0 {
		return nil
	}
	return []memoryextract.Message{{Role: role, Content: blocks}}
}

func memoryExtractionCheckpointMessages(entry eventjournal.Entry) []memoryextract.Message {
	name := strings.TrimSpace(stringValue(entry.Message["toolName"]))
	phase := strings.ToLower(strings.TrimSpace(stringValue(entry.Message["toolPhase"])))
	callID := strings.TrimSpace(stringValue(entry.Message["toolCallId"]))
	if name == "" || phase == "" || callID == "" {
		return nil
	}
	switch phase {
	case "start", "started", "running":
		return []memoryextract.Message{{Role: "assistant", Content: []memoryextract.Block{{
			Type: memoryextract.BlockToolUse, ToolName: name, ToolUseID: callID,
			ToolInput: memoryExtractionJSONMap(entry.Message["toolInput"]),
		}}}}
	case "completed", "failed", "cancelled":
		content := compactJSONForRuntimeContext(entry.Message["toolResult"])
		if content == "" {
			content = firstNonEmpty(stringValue(entry.Message["message"]), stringValue(entry.Message["text"]))
		}
		return []memoryextract.Message{{Role: "user", Content: []memoryextract.Block{{
			Type: memoryextract.BlockToolResult, ToolUseID: callID, Text: content,
			ToolError: phase != "completed" || memoryExtractionToolResultError(content),
		}}}}
	default:
		return nil
	}
}

func memoryExtractionTextBlock(text string) memoryextract.Block {
	return memoryextract.Block{
		Type: memoryextract.BlockText, Text: text,
		HarnessNotice: strings.HasPrefix(text, "[System] Prior-turn ") && strings.Contains(text, "<persisted-output>\n"),
	}
}

func memoryExtractionToolInput(arguments json.RawMessage) map[string]any {
	if len(arguments) == 0 {
		return map[string]any{}
	}
	var input map[string]any
	if err := json.Unmarshal(arguments, &input); err != nil || input == nil {
		return map[string]any{}
	}
	return input
}

func memoryExtractionJSONMap(value any) map[string]any {
	raw, err := json.Marshal(value)
	if err != nil || string(raw) == "null" {
		return map[string]any{}
	}
	return memoryExtractionToolInput(raw)
}

func memoryExtractionToolResultText(message agentruntime.Message) string {
	if message.Content != "" {
		return message.Content
	}
	texts := make([]string, 0, len(message.Parts))
	for _, part := range message.Parts {
		if part.Type == agentruntime.ContentPartText {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, "")
}

func memoryExtractionToolResultError(content string) bool {
	if strings.Contains(content, memorytools.ErrMemoryWriteRejected.Error()) ||
		strings.Contains(content, memorytools.ErrMemoryClassifierUnavailable.Error()) {
		return true
	}
	var payload map[string]any
	if json.Unmarshal([]byte(content), &payload) != nil {
		return false
	}
	errorText, _ := payload["error"].(string)
	return strings.TrimSpace(errorText) != ""
}
