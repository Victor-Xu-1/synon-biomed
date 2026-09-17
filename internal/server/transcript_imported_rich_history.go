package server

import (
	"fmt"

	transcriptstore "synon-go/internal/persistence/transcript"
)

type transcriptImportedRichAction struct {
	append    bool
	index     int
	id        string
	messageID string
	role      string
	text      string
	callID    string
	name      string
	input     map[string]any
	output    string
	isError   bool
}

type transcriptImportedRichTool struct {
	index         int
	id, messageID string
	callID, name  string
	input         map[string]any
	resolved      bool
}

type transcriptImportedRichHistoryState struct {
	enabled bool
	tools   map[string]transcriptImportedRichTool
}

func transcriptImportedRichHasOnlyKeys(record map[string]any, allowed ...string) bool {
	if record == nil || len(record) > len(allowed) {
		return false
	}
	keys := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		keys[key] = struct{}{}
	}
	for key := range record {
		if _, ok := keys[key]; !ok {
			return false
		}
	}
	return true
}

func newTranscriptImportedRichHistoryState(enabled bool) *transcriptImportedRichHistoryState {
	return &transcriptImportedRichHistoryState{enabled: enabled, tools: map[string]transcriptImportedRichTool{}}
}

func (s *transcriptImportedRichHistoryState) consume(
	projected transcriptstore.ProjectedEvent,
	payload map[string]any,
	nextIndex int,
) ([]transcriptImportedRichAction, bool, error) {
	if !s.enabled {
		return nil, false, nil
	}
	role := ""
	switch projected.Event.Type {
	case "history_user_message":
		role = "user"
	case "history_assistant_message":
		role = "assistant"
	case "history_system_message":
		role = "system"
	default:
		return nil, false, nil
	}
	if projected.Event.Source != transcriptstore.EventSourcePayload || projected.Event.RunnerAttempt != nil {
		return nil, true, transcriptstore.ErrEventConflict
	}
	payloadRole, roleOK := transcriptToolExactString(payload["role"])
	blocks, blocksOK := payload["content"].([]any)
	if !roleOK || payloadRole != role || !blocksOK || len(blocks) == 0 {
		return nil, true, transcriptstore.ErrEventConflict
	}
	actions := make([]transcriptImportedRichAction, 0, len(blocks))
	for blockIndex, rawBlock := range blocks {
		block, ok := rawBlock.(map[string]any)
		if !ok {
			return nil, true, transcriptstore.ErrEventConflict
		}
		blockType, typeOK := transcriptToolExactString(block["type"])
		if !typeOK {
			return nil, true, transcriptstore.ErrEventConflict
		}
		switch blockType {
		case "text":
			if !transcriptImportedRichHasOnlyKeys(block, "type", "text") {
				return nil, true, transcriptstore.ErrEventConflict
			}
			text, ok := block["text"].(string)
			if !ok {
				return nil, true, transcriptstore.ErrEventConflict
			}
			if role == "system" || text == "" {
				continue
			}
			identity := fmt.Sprintf("transcript-history:%s:%d:%d", projected.Event.StreamUID, projected.Event.EventID, blockIndex)
			actions = append(actions, transcriptImportedRichAction{
				append: true, index: nextIndex, id: identity, messageID: identity, role: role, text: text,
			})
			nextIndex++
		case "tool_use":
			if role != "assistant" || !transcriptImportedRichHasOnlyKeys(block, "type", "id", "name", "input") {
				return nil, true, transcriptstore.ErrEventConflict
			}
			callID, callOK := transcriptToolExactString(block["id"])
			name, nameOK := transcriptToolExactString(block["name"])
			input, inputOK := block["input"].(map[string]any)
			_, askUser := transcriptstore.CanonicalAskUserToolNameV1(name)
			if !callOK || !nameOK || !inputOK || askUser {
				return nil, true, transcriptstore.ErrEventConflict
			}
			if _, duplicate := s.tools[callID]; duplicate {
				return nil, true, transcriptstore.ErrEventConflict
			}
			identity := "transcript-history-tool:" + projected.Event.StreamUID + ":" + callID
			record := transcriptImportedRichTool{
				index: nextIndex, id: identity, messageID: identity,
				callID: callID, name: name, input: input,
			}
			s.tools[callID] = record
			actions = append(actions, transcriptImportedRichAction{
				append: true, index: nextIndex, id: identity, messageID: identity,
				callID: callID, name: name, input: input,
			})
			nextIndex++
		case "tool_result":
			if role != "user" || !transcriptImportedRichHasOnlyKeys(block, "type", "tool_use_id", "content", "is_error") {
				return nil, true, transcriptstore.ErrEventConflict
			}
			callID, callOK := transcriptToolExactString(block["tool_use_id"])
			output, outputOK := block["content"].(string)
			isError := false
			if raw, found := block["is_error"]; found {
				var boolOK bool
				isError, boolOK = raw.(bool)
				if !boolOK {
					return nil, true, transcriptstore.ErrEventConflict
				}
			}
			record, found := s.tools[callID]
			if !callOK || !outputOK || !found || record.resolved {
				return nil, true, transcriptstore.ErrEventConflict
			}
			record.resolved = true
			s.tools[callID] = record
			actions = append(actions, transcriptImportedRichAction{
				index: record.index, id: record.id, messageID: record.messageID,
				callID: record.callID, name: record.name, input: record.input, output: output, isError: isError,
			})
		default:
			return nil, true, transcriptstore.ErrEventConflict
		}
	}
	return actions, true, nil
}

func (s *transcriptImportedRichHistoryState) validate() error {
	for _, tool := range s.tools {
		if !tool.resolved {
			return transcriptstore.ErrEventConflict
		}
	}
	return nil
}

func transcriptImportedRichMessage(
	action transcriptImportedRichAction,
	sessionID string,
	createdAt int64,
) map[string]any {
	if action.callID == "" {
		position := "left"
		if action.role == "user" {
			position = "right"
		}
		return map[string]any{
			"id": action.id, "msg_id": action.messageID, "conversation_id": sessionID,
			"type": "text", "position": position, "status": "finish", "created_at": createdAt,
			"content": map[string]any{"content": action.text}, "artifact_refs": []map[string]any{},
		}
	}
	status := "running"
	messageStatus := "work"
	content := map[string]any{
		"call_id": action.callID, "name": action.name, "args": action.input, "input": action.input,
	}
	if !action.append {
		status = "completed"
		messageStatus = "finish"
		content["output"] = action.output
		if action.isError {
			status = "error"
			messageStatus = "error"
			content["error"] = action.output
		}
	}
	content["status"] = status
	return map[string]any{
		"id": action.id, "msg_id": action.messageID, "conversation_id": sessionID,
		"type": "tool_call", "position": "left", "status": messageStatus,
		"created_at": createdAt, "content": content,
	}
}
