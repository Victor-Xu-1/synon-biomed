package server

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) handleCompatibilityFrameMessages(w http.ResponseWriter, r *http.Request, frame workspace.CompatibilityFrame, segments []string) {
	if len(segments) == 2 {
		if r.Method != http.MethodGet {
			writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		from := compatNonNegativeQuery(r, "from", 0)
		limit := compatPositiveQuery(r, "limit", defaultWebMessagePageSize, maxWebMessagePageSize)
		if s.transcriptWebReadModel != nil {
			readModel, active, err := s.activatedTranscriptWebReadModel(
				r.Context(), compatAgentUserID(r), frame.ID, "",
			)
			if err != nil {
				writeV11StoreError(w, err)
				return
			}
			if active {
				if from > readModel.fence.VisibleMessageCount {
					writeV11Detail(w, http.StatusBadRequest, "Message offset exceeds canonical history")
					return
				}
				page, found, err := s.transcriptWebReadModel.GetTranscriptWebMessageRange(
					r.Context(), transcriptstore.TranscriptWebMessagePageInput{
						OwnerID: compatAgentUserID(r), StreamUID: readModel.stream.UID,
						BranchID: readModel.snapshot.BranchID, BranchGeneration: readModel.snapshot.BranchGeneration,
						ThroughPublicationSequence: readModel.snapshot.ThroughPublicationSequence,
						SourceRevision:             readModel.fence.SourceRevision, From: &from, Limit: limit,
						AllowQuarantinedSnapshot: readModel.allowQuarantinedSnapshot,
					},
				)
				if err != nil {
					writeV11StoreError(w, transcriptWebReadModelServingError(err))
					return
				}
				if !found {
					writeV11StoreError(w, transcriptWebAuthorityUnavailableError())
					return
				}
				messages, err := transcriptWebReadModelMessageMaps(page.Messages)
				if err != nil {
					writeV11StoreError(w, err)
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{
					"frame_id": frame.ID, "from": page.From,
					"messages": transcriptCompatibilityMessages(messages), "total": page.Total,
				})
				return
			}
		}
		if messages, _, active, err := s.loadActivatedTranscriptWebHistory(
			r.Context(), compatAgentUserID(r), frame.ID, "",
		); err != nil {
			writeV11StoreError(w, err)
			return
		} else if active {
			messages = transcriptCompatibilityMessages(messages)
			if from > len(messages) {
				writeV11Detail(w, http.StatusBadRequest, "Message offset exceeds canonical history")
				return
			}
			start := from
			end := min(len(messages), start+limit)
			writeJSON(w, http.StatusOK, map[string]any{
				"frame_id": frame.ID, "from": start, "messages": messages[start:end], "total": len(messages),
			})
			return
		}
		page, err := s.workspaceStore.CompatibilityFrameMessages(frame.ID, from, limit)
		if err != nil {
			writeV11StoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"frame_id": frame.ID, "from": page.From, "messages": page.Messages, "total": page.Total,
		})
		return
	}
	if len(segments) != 3 || segments[2] != "locate" {
		writeV11Detail(w, http.StatusNotFound, "Frame message endpoint not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		messageUUID := strings.TrimSpace(r.URL.Query().Get("uuid"))
		if s.transcriptWebReadModel != nil {
			readModel, active, err := s.activatedTranscriptWebReadModel(
				r.Context(), compatAgentUserID(r), frame.ID, "",
			)
			if err != nil {
				writeV11StoreError(w, err)
				return
			}
			if active {
				index, found, err := s.transcriptWebReadModel.LocateTranscriptWebMessage(
					r.Context(), readModel.transcriptWebMessageIdentity(compatAgentUserID(r), messageUUID),
				)
				if err != nil {
					writeV11StoreError(w, transcriptWebReadModelServingError(err))
					return
				}
				var location any
				if found {
					location = index
				}
				writeJSON(w, http.StatusOK, map[string]any{
					"frame_id": frame.ID, "idx": location, "uuid": messageUUID,
				})
				return
			}
		}
		if messages, _, active, err := s.loadActivatedTranscriptWebHistory(
			r.Context(), compatAgentUserID(r), frame.ID, "",
		); err != nil {
			writeV11StoreError(w, err)
			return
		} else if active {
			messages = transcriptCompatibilityMessages(messages)
			writeJSON(w, http.StatusOK, map[string]any{
				"frame_id": frame.ID, "idx": exactTranscriptMessageIndex(messages, messageUUID), "uuid": messageUUID,
			})
			return
		}
		index, err := s.workspaceStore.LocateCompatibilityFrameMessage(frame.ID, messageUUID)
		if err != nil {
			writeV11StoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"frame_id": frame.ID, "idx": index, "uuid": messageUUID})
	case http.MethodPost:
		var input struct {
			UUIDs []string `json:"uuids"`
		}
		if err := decodeAgentCompatJSON(r, &input); err != nil {
			writeV11Detail(w, http.StatusBadRequest, "Invalid locate request: "+err.Error())
			return
		}
		if s.transcriptWebReadModel != nil {
			readModel, active, err := s.activatedTranscriptWebReadModel(
				r.Context(), compatAgentUserID(r), frame.ID, "",
			)
			if err != nil {
				writeV11StoreError(w, err)
				return
			}
			if active {
				locations := make([]map[string]any, 0, len(input.UUIDs))
				for _, messageUUID := range input.UUIDs {
					trimmed := strings.TrimSpace(messageUUID)
					index, found, err := s.transcriptWebReadModel.LocateTranscriptWebMessage(
						r.Context(), readModel.transcriptWebMessageIdentity(compatAgentUserID(r), trimmed),
					)
					if err != nil {
						writeV11StoreError(w, transcriptWebReadModelServingError(err))
						return
					}
					var location any
					if found {
						location = index
					}
					locations = append(locations, map[string]any{"idx": location, "uuid": messageUUID})
				}
				writeJSON(w, http.StatusOK, map[string]any{"frame_id": frame.ID, "locations": locations})
				return
			}
		}
		if messages, _, active, err := s.loadActivatedTranscriptWebHistory(
			r.Context(), compatAgentUserID(r), frame.ID, "",
		); err != nil {
			writeV11StoreError(w, err)
			return
		} else if active {
			messages = transcriptCompatibilityMessages(messages)
			locations := make([]map[string]any, 0, len(input.UUIDs))
			for _, messageUUID := range input.UUIDs {
				locations = append(locations, map[string]any{
					"idx": exactTranscriptMessageIndex(messages, strings.TrimSpace(messageUUID)), "uuid": messageUUID,
				})
			}
			writeJSON(w, http.StatusOK, map[string]any{"frame_id": frame.ID, "locations": locations})
			return
		}
		locations := make([]map[string]any, 0, len(input.UUIDs))
		for _, messageUUID := range input.UUIDs {
			index, err := s.workspaceStore.LocateCompatibilityFrameMessage(frame.ID, messageUUID)
			if err != nil {
				writeV11StoreError(w, err)
				return
			}
			locations = append(locations, map[string]any{"idx": index, "uuid": messageUUID})
		}
		writeJSON(w, http.StatusOK, map[string]any{"frame_id": frame.ID, "locations": locations})
	default:
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func (s *Server) handleCompatibilityBranchMessages(w http.ResponseWriter, r *http.Request, frame workspace.CompatibilityFrame, rawBranchID string) {
	if r.Method != http.MethodGet {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	branchID, err := url.PathUnescape(rawBranchID)
	if err != nil || strings.TrimSpace(branchID) == "" {
		writeV11Detail(w, http.StatusBadRequest, "Invalid branch id")
		return
	}
	if s.transcriptWebReadModel != nil {
		readModel, active, err := s.activatedTranscriptWebReadModel(
			r.Context(), compatAgentUserID(r), frame.ID, branchID,
		)
		if err != nil {
			if errors.Is(err, transcriptstore.ErrBranchStateStale) {
				writeV11Detail(w, http.StatusConflict, "Conversation branch changed; refresh and retry")
			} else {
				writeV11StoreError(w, err)
			}
			return
		}
		if active {
			from := compatNonNegativeQuery(r, "from", 0)
			limit := compatPositiveQuery(r, "limit", defaultWebMessagePageSize, maxWebMessagePageSize)
			if from > readModel.fence.VisibleMessageCount {
				writeV11Detail(w, http.StatusBadRequest, "Message offset exceeds canonical history")
				return
			}
			page, found, err := s.transcriptWebReadModel.GetTranscriptWebMessageRange(
				r.Context(), transcriptstore.TranscriptWebMessagePageInput{
					OwnerID: compatAgentUserID(r), StreamUID: readModel.stream.UID,
					BranchID: readModel.snapshot.BranchID, BranchGeneration: readModel.snapshot.BranchGeneration,
					ThroughPublicationSequence: readModel.snapshot.ThroughPublicationSequence,
					SourceRevision:             readModel.fence.SourceRevision, From: &from, Limit: limit,
					AllowQuarantinedSnapshot: readModel.allowQuarantinedSnapshot,
				},
			)
			if err != nil {
				writeV11StoreError(w, transcriptWebReadModelServingError(err))
				return
			}
			if !found {
				writeV11StoreError(w, transcriptWebAuthorityUnavailableError())
				return
			}
			messages, err := transcriptWebReadModelMessageMaps(page.Messages)
			if err != nil {
				writeV11StoreError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"frame_id": frame.ID, "branch_id": readModel.snapshot.BranchID,
				"from": page.From, "messages": transcriptCompatibilityMessages(messages), "total": page.Total,
			})
			return
		}
	}
	if messages, snapshot, active, err := s.loadActivatedTranscriptWebHistory(
		r.Context(), compatAgentUserID(r), frame.ID, branchID,
	); err != nil {
		writeV11StoreError(w, err)
		return
	} else if active {
		messages = transcriptCompatibilityMessages(messages)
		writeJSON(w, http.StatusOK, map[string]any{
			"frame_id": frame.ID, "branch_id": snapshot.BranchID, "generation": snapshot.BranchGeneration, "messages": messages,
		})
		return
	}
	messages, found, err := s.workspaceStore.GetCompatibilityBranchMessages(frame.ID, branchID)
	if err != nil {
		if strings.Contains(err.Error(), "branch id") {
			writeV11Detail(w, http.StatusBadRequest, "Invalid branch id")
			return
		}
		writeV11StoreError(w, err)
		return
	}
	if !found {
		writeV11Detail(w, http.StatusNotFound, "Branch "+branchID+" not found on frame "+frame.ID)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"frame_id": frame.ID, "branch_id": branchID, "messages": messages,
	})
}

func transcriptCompatibilityMessages(messages []map[string]any) []map[string]any {
	result := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		id := strings.TrimSpace(webString(message["id"]))
		clientID := strings.TrimSpace(webString(message["msg_id"]))
		if clientID == "" {
			clientID = id
		}
		role := "assistant"
		if webString(message["position"]) == "right" {
			role = "user"
		}
		compat := map[string]any{
			"id": id, "msg_id": clientID, "messageUuid": id, "message_uuid": id,
			"uuid": id, "_uuid": id, "clientMessageId": clientID,
			"role": role, "type": role + "_message", "created_at": message["created_at"],
		}
		content, _ := message["content"].(map[string]any)
		switch webString(message["type"]) {
		case "text":
			text := webString(content["content"])
			compat["text"] = text
			compat["content"] = []any{map[string]any{"type": "text", "text": text}}
		case "tool_call":
			callID := strings.TrimSpace(webString(content["call_id"]))
			name := strings.TrimSpace(webString(content["name"]))
			compatibility, _ := content["_frame_compat"].(map[string]any)
			toolUseMessageID := strings.TrimSpace(webString(compatibility["tool_use_message_id"]))
			if toolUseMessageID == "" {
				toolUseMessageID = id
			}
			compat["id"], compat["messageUuid"], compat["message_uuid"], compat["uuid"], compat["_uuid"] =
				toolUseMessageID, toolUseMessageID, toolUseMessageID, toolUseMessageID, toolUseMessageID
			compat["content"] = []any{map[string]any{
				"type": "tool_use", "id": callID, "name": name, "input": content["input"],
			}}
			result = append(result, compat)
			if compatibility["result_present"] == true {
				toolResultMessageID := strings.TrimSpace(webString(compatibility["tool_result_message_id"]))
				if toolResultMessageID == "" {
					toolResultMessageID = callID + ":result"
				}
				result = append(result, map[string]any{
					"id": toolResultMessageID, "msg_id": toolResultMessageID,
					"messageUuid": toolResultMessageID, "message_uuid": toolResultMessageID,
					"uuid": toolResultMessageID, "_uuid": toolResultMessageID,
					"clientMessageId": toolResultMessageID, "role": "user", "type": "user_message",
					"created_at": message["created_at"],
					"content": []any{map[string]any{
						"type": "tool_result", "tool_use_id": callID,
						"content": compatibility["result_output"], "is_error": true,
					}},
				})
			}
			continue
		default:
			compat["content"] = message["content"]
		}
		result = append(result, compat)
	}
	return result
}

func exactTranscriptMessageIndex(messages []map[string]any, id string) any {
	if index := exactTranscriptMessageIndexValue(messages, id); index >= 0 {
		return index
	}
	return nil
}

func exactTranscriptMessageIndexValue(messages []map[string]any, id string) int {
	id = strings.TrimSpace(id)
	if id == "" || strings.HasPrefix(id, "idx:") {
		return -1
	}
	for index, message := range messages {
		if webString(message["id"]) == id || webString(message["msg_id"]) == id {
			return index
		}
	}
	return -1
}
