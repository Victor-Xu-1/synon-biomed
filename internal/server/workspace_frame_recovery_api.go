package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	kernelruntime "synon-go/internal/kernel"
	eventjournal "synon-go/internal/persistence/journal"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

type frameStreamingBuffer struct {
	FrameID      string               `json:"frameId"`
	Active       bool                 `json:"active"`
	AfterEventID int64                `json:"afterEventId"`
	LastEventID  int64                `json:"lastEventId"`
	Content      string               `json:"content"`
	Thinking     string               `json:"thinking"`
	Entries      []eventjournal.Entry `json:"entries"`
	UpdatedAt    string               `json:"updatedAt,omitempty"`
}

type compatibilityStreamingBuffer struct {
	FrameID    string                     `json:"frame_id"`
	Text       string                     `json:"text"`
	Thinking   string                     `json:"thinking"`
	ToolStdout []kernelruntime.ExecStream `json:"tool_stdout"`
}

const (
	compatibilityStreamingRetainedFrames  = 8
	compatibilityStreamingReadBatch       = 500
	compatibilityStreamingRetainedEntries = 500
)

type frameStreamingCacheEntry struct {
	cursor   eventjournal.ReadCursor
	buffer   frameStreamingBuffer
	lastUsed uint64
}

func (s *Server) handleCompatibilityFrameStreaming(w http.ResponseWriter, r *http.Request, frame workspace.CompatibilityFrame) {
	if r.Method != http.MethodGet {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	if frame.IsHidden {
		writeV11Detail(w, http.StatusNotFound, "Frame "+frame.ID+" not found")
		return
	}
	buffer, err := s.compatibilityStreamingBuffer(frame.ID)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, buffer)
}

func (s *Server) handleCompatibilityFrameStreamingBatch(w http.ResponseWriter, r *http.Request, frame workspace.CompatibilityFrame) {
	if r.Method != http.MethodPost {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	if frame.IsHidden {
		writeV11Detail(w, http.StatusNotFound, "Frame "+frame.ID+" not found")
		return
	}
	var input struct{}
	if r.Body != nil && r.ContentLength != 0 {
		if err := decodeAgentCompatJSON(r, &input); err != nil {
			writeV11Detail(w, http.StatusBadRequest, "Invalid streaming buffer request: "+err.Error())
			return
		}
	}
	rootFrameID := firstNonEmpty(frame.RootFrameID, frame.ID)
	rootFrames, err := s.workspaceStore.ListActiveFramesForRoot(frame.ProjectID, rootFrameID)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	frameIDs := make(map[string]struct{}, len(rootFrames))
	for _, candidate := range rootFrames {
		frameIDs[candidate.ID] = struct{}{}
	}
	if s.kernelManager != nil {
		for _, session := range s.kernelManager.ListSessionKernels(rootFrameID) {
			if _, exists := frameIDs[session.FrameID]; exists {
				continue
			}
			candidate, found, err := s.workspaceStore.GetCompatibilityFrame(session.FrameID)
			if err != nil {
				writeV11StoreError(w, err)
				return
			}
			if !found || candidate.IsHidden || candidate.ProjectID != frame.ProjectID ||
				firstNonEmpty(candidate.RootFrameID, candidate.ID) != rootFrameID {
				continue
			}
			frameIDs[candidate.ID] = struct{}{}
			rootFrames = append(rootFrames, candidate.Frame)
		}
	}
	sort.Slice(rootFrames, func(i, j int) bool {
		if rootFrames[i].RootSequence != rootFrames[j].RootSequence {
			return rootFrames[i].RootSequence < rootFrames[j].RootSequence
		}
		if !rootFrames[i].CreatedAt.Equal(rootFrames[j].CreatedAt) {
			return rootFrames[i].CreatedAt.Before(rootFrames[j].CreatedAt)
		}
		return rootFrames[i].ID < rootFrames[j].ID
	})
	buffers := make([]compatibilityStreamingBuffer, 0)
	for _, candidate := range rootFrames {
		buffer, err := s.frameStreamingBuffer(candidate.ID)
		if err != nil {
			writeV11StoreError(w, err)
			return
		}
		streams := []kernelruntime.ExecStream{}
		if s.kernelManager != nil {
			streams = s.kernelManager.ListExecStreams(candidate.ID)
		}
		if (buffer.Active && (buffer.Content != "" || buffer.Thinking != "")) || len(streams) > 0 {
			buffers = append(buffers, compatibilityStreamingBuffer{
				FrameID: candidate.ID, Text: buffer.Content, Thinking: buffer.Thinking,
				ToolStdout: streams,
			})
		}
	}
	payload := map[string]any{"root_frame_id": rootFrameID, "buffers": buffers}
	if revision, found, err := s.compatibilityStreamingHistoryRevision(
		r.Context(), compatAgentUserID(r), frame.ID,
	); err != nil {
		writeV11StoreError(w, err)
		return
	} else if found {
		payload["history_revision"] = revision
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) compatibilityStreamingHistoryRevision(
	ctx context.Context,
	ownerID, sessionID string,
) (int64, bool, error) {
	if s == nil || s.transcriptStore == nil {
		return 0, false, nil
	}
	authority, found, err := s.transcriptStore.GetFrameAuthorityBySession(ctx, ownerID, sessionID)
	if err != nil || !found {
		return 0, false, err
	}
	if !authority.TranscriptPayloadActive() {
		return 0, false, nil
	}
	stream, found, err := s.transcriptStore.GetFrameStreamBySession(ctx, ownerID, sessionID)
	if err != nil || !found {
		return 0, false, err
	}
	if stream.UID != authority.ActiveStreamUID || stream.Epoch != authority.ActiveEpoch {
		return 0, false, transcriptstore.ErrEventConflict
	}
	snapshot, err := s.transcriptStore.GetProjectionSnapshot(ctx, stream.UID, ownerID)
	if err != nil {
		return 0, false, err
	}
	return snapshot.ThroughPublicationSequence, true, nil
}

func (s *Server) compatibilityStreamingBuffer(frameID string) (*compatibilityStreamingBuffer, error) {
	buffer, err := s.frameStreamingBuffer(frameID)
	if err != nil {
		return nil, err
	}
	streams := []kernelruntime.ExecStream{}
	if s.kernelManager != nil {
		streams = s.kernelManager.ListExecStreams(frameID)
	}
	text, thinking := "", ""
	if buffer.Active {
		text, thinking = buffer.Content, buffer.Thinking
	}
	if text == "" && thinking == "" && len(streams) == 0 {
		return nil, nil
	}
	return &compatibilityStreamingBuffer{
		FrameID: frameID, Text: text, Thinking: thinking, ToolStdout: streams,
	}, nil
}

func (s *Server) handleWorkspaceFrameStreaming(w http.ResponseWriter, r *http.Request, store *workspace.Store, frameID string) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	if _, found, err := store.GetFrame(frameID); err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	} else if !found {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "frame not found"})
		return
	}
	buffer, err := s.frameStreamingBuffer(frameID)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "buffer": buffer})
}

func (s *Server) handleWorkspaceFrameStreamingBatch(w http.ResponseWriter, r *http.Request, store *workspace.Store) {
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	var input struct {
		IDs []string `json:"ids"`
	}
	if err := decodeWorkspaceJSON(r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if len(input.IDs) > 500 {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "streaming batch is limited to 500 frame ids"})
		return
	}
	buffers := make(map[string]any, len(input.IDs))
	for _, rawID := range input.IDs {
		frameID := strings.TrimSpace(rawID)
		if frameID == "" {
			continue
		}
		if _, found, err := store.GetFrame(frameID); err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		} else if !found {
			buffers[frameID] = nil
			continue
		}
		buffer, err := s.frameStreamingBuffer(frameID)
		if err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		buffers[frameID] = buffer
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "buffers": buffers})
}

func (s *Server) frameStreamingBuffer(frameID string) (*frameStreamingBuffer, error) {
	if s.eventJournal == nil {
		return nil, errors.New("session event journal is not configured")
	}
	s.streamingCacheMu.Lock()
	defer s.streamingCacheMu.Unlock()
	if s.streamingCache == nil {
		s.streamingCache = make(map[string]*frameStreamingCacheEntry)
	}
	state := s.streamingCache[frameID]
	if state == nil {
		state = &frameStreamingCacheEntry{buffer: frameStreamingBuffer{FrameID: frameID, Entries: []eventjournal.Entry{}}}
		s.streamingCache[frameID] = state
	}
	for reset := 0; ; {
		entries, cursor, atEnd, err := s.eventJournal.ReadFromCursor(
			frameID, state.cursor, compatibilityStreamingReadBatch,
		)
		if errors.Is(err, eventjournal.ErrReadCursorStale) && reset == 0 {
			state.cursor = eventjournal.ReadCursor{}
			state.buffer = frameStreamingBuffer{FrameID: frameID, Entries: []eventjournal.Entry{}}
			reset++
			continue
		}
		if err != nil {
			return nil, err
		}
		state.cursor = cursor
		for _, entry := range entries {
			applyFrameStreamingEntry(&state.buffer, entry)
		}
		if atEnd {
			break
		}
	}
	s.streamingCacheClock++
	state.lastUsed = s.streamingCacheClock
	s.trimFrameStreamingCache(frameID)
	buffer := state.buffer
	buffer.Entries = append([]eventjournal.Entry(nil), state.buffer.Entries...)

	// Legacy Session state is mutable outside the append-only journal. Apply it
	// after the cached projection so it never becomes a second cache authority.
	if s.sessionStore != nil {
		session, found, err := s.sessionStore.Get(frameID)
		if err == nil && found {
			if session.LastRole == "user" {
				buffer.Active = true
			}
			if session.Runner != nil && !frameRunnerStatusTerminal(session.Runner.Status) {
				buffer.Active = true
			}
		}
	}
	// Transcript-backed runners persist content deltas in the canonical
	// Transcript instead of the legacy EventJournal. When the legacy journal
	// has no content for an active frame, fall back to the verified transcript
	// projection so the v1.1 streaming-buffer contract remains observable for
	// live text and for refresh/reconnect recovery.
	if buffer.Content == "" && buffer.Thinking == "" && s.transcriptStore != nil && s.workspaceStore != nil {
		if transcriptContent, transcriptActive, err := s.transcriptStreamingFallback(frameID); err == nil && transcriptActive {
			if buffer.Content == "" && transcriptContent != "" {
				buffer.Content = transcriptContent
				buffer.Active = true
			}
		}
	}
	return &buffer, nil
}

// transcriptStreamingFallback reads the current visible assistant text from
// the verified Transcript Web projection for frames whose runner persists
// deltas through the Transcript authority. It returns the last assistant
// message body and whether the frame currently has a transcript-backed stream.
func (s *Server) transcriptStreamingFallback(frameID string) (string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), transcriptStreamingFallbackTimeout)
	defer cancel()
	// Resolve the authoritative owner through the workspace frame context;
	// both local accounts and registered Web accounts are supported.
	frameContext, ctxFound, ctxErr := s.workspaceStore.GetFrameRealtimeContext(frameID)
	if ctxErr != nil || !ctxFound {
		return "", false, nil
	}
	stream, found, err := s.transcriptStore.GetFrameStreamBySession(ctx, frameContext.UserID, frameID)
	if err != nil || !found {
		return "", false, nil
	}
	if s.transcriptWebReadModel == nil {
		return "", false, nil
	}
	snapshot, err := s.transcriptStore.GetProjectionSnapshot(ctx, stream.UID, stream.OwnerID)
	if err != nil || snapshot.ThroughPublicationSequence == 0 {
		return "", false, nil
	}
	state, messages, found, err := s.transcriptWebReadModel.GetTranscriptWebProjectionCheckpoint(
		ctx, stream.OwnerID, stream.UID, snapshot.BranchID,
	)
	if err != nil {
		return "", false, nil
	}
	if !found || state.ThroughPublicationSequence == 0 {
		return "", false, nil
	}
	content := ""
	for index := len(messages) - 1; index >= 0 && content == ""; index-- {
		message := messages[index]
		if !message.Visible || len(message.MessageJSON) == 0 {
			continue
		}
		var envelope struct {
			Type    string `json:"type"`
			Content struct {
				Content string `json:"content"`
			} `json:"content"`
		}
		if json.Unmarshal(message.MessageJSON, &envelope) != nil || envelope.Type != "text" {
			continue
		}
		if strings.TrimSpace(envelope.Content.Content) != "" {
			content = envelope.Content.Content
			break
		}
	}
	return content, content != "", nil
}

const (
	transcriptStreamingFallbackTimeout      = 2 * time.Second
	transcriptStreamingFallbackMessageLimit = 50
)

func applyFrameStreamingEntry(buffer *frameStreamingBuffer, entry eventjournal.Entry) {
	if entry.EventID > buffer.LastEventID {
		buffer.LastEventID = entry.EventID
		buffer.UpdatedAt = entry.CreatedAt
	}
	messageType := strings.TrimSpace(stringValue(entry.Message["type"]))
	role := strings.TrimSpace(stringValue(entry.Message["role"]))
	switch {
	case role == "user":
		buffer.AfterEventID = entry.EventID
		buffer.Content = ""
		buffer.Thinking = ""
		buffer.Entries = buffer.Entries[:0]
		buffer.Active = true
	case messageType == "content_delta":
		chunk := firstNonEmpty(stringValue(entry.Message["text"]), stringValue(entry.Message["delta"]), stringValue(entry.Message["content"]), stringValue(entry.Message["chunk"]))
		if firstNonEmpty(stringValue(entry.Message["block_type"]), stringValue(entry.Message["blockType"])) == "thinking" {
			buffer.Thinking += chunk
		} else {
			buffer.Content += chunk
		}
		appendFrameStreamingEntry(buffer, entry)
		buffer.Active = true
	case messageType == "thinking_delta":
		buffer.Thinking += firstNonEmpty(stringValue(entry.Message["text"]), stringValue(entry.Message["delta"]), stringValue(entry.Message["content"]), stringValue(entry.Message["chunk"]))
		appendFrameStreamingEntry(buffer, entry)
		buffer.Active = true
	case role == "assistant" && (messageType == "message" || messageType == "assistant_message"):
		buffer.Content = runnerMessageText(entry.Message)
		appendFrameStreamingEntry(buffer, entry)
	case messageType == "runner_finished":
		buffer.Active = false
	}
}

func appendFrameStreamingEntry(buffer *frameStreamingBuffer, entry eventjournal.Entry) {
	buffer.Entries = append(buffer.Entries, entry)
	if extra := len(buffer.Entries) - compatibilityStreamingRetainedEntries; extra > 0 {
		copy(buffer.Entries, buffer.Entries[extra:])
		buffer.Entries = buffer.Entries[:compatibilityStreamingRetainedEntries]
	}
}

func (s *Server) trimFrameStreamingCache(retainFrameID string) {
	for len(s.streamingCache) > compatibilityStreamingRetainedFrames {
		oldestID := ""
		oldestUse := ^uint64(0)
		for frameID, state := range s.streamingCache {
			if frameID == retainFrameID || state.lastUsed >= oldestUse {
				continue
			}
			oldestID, oldestUse = frameID, state.lastUsed
		}
		if oldestID == "" {
			return
		}
		delete(s.streamingCache, oldestID)
	}
}

func frameRunnerStatusTerminal(status string) bool {
	switch strings.TrimSpace(status) {
	case "completed", "failed", "cancelled", "canceled", "stopped":
		return true
	default:
		return false
	}
}

func (s *Server) handleWorkspaceCompactionArchive(w http.ResponseWriter, r *http.Request, store *workspace.Store, frameID, rawIndex string) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	if _, found, err := store.GetFrame(frameID); err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	} else if !found {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "frame not found"})
		return
	}
	index, err := strconv.Atoi(rawIndex)
	if err != nil || index < 0 {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "compaction archive index must be non-negative"})
		return
	}
	if err := s.reconcileCompactionArchives(frameID); err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	archives, err := store.ListCompactionArchives(frameID)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	archive, found, err := store.GetCompactionArchive(frameID, index)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if !found {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "compaction archive not found"})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "archive": archive, "archiveCount": len(archives)})
}

func (s *Server) handleWorkspaceCompactionMessages(w http.ResponseWriter, r *http.Request, store *workspace.Store, frameID string) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	if _, found, err := store.GetFrame(frameID); err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	} else if !found {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "frame not found"})
		return
	}
	if err := s.reconcileCompactionArchives(frameID); err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	archives, err := store.ListCompactionArchives(frameID)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	through := len(archives) - 1
	if len(archives) > 0 {
		through = archives[len(archives)-1].CompactionIndex
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("through")); raw != "" {
		through, err = strconv.Atoi(raw)
		if err != nil || through < 0 {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "through must be a non-negative integer"})
			return
		}
	}
	messages, err := store.ListCompactionMessages(frameID, through)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"ok": true, "frame_id": frameID, "messages": messages,
		"archive_count": len(archives), "through": through,
	})
}

func (s *Server) handleWorkspaceCrossSessionReferences(w http.ResponseWriter, r *http.Request, store *workspace.Store, rootFrameID string) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	root, found, err := store.GetFrame(rootFrameID)
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if !found || root.RootFrameID != root.ID {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "root frame not found"})
		return
	}
	frames, err := store.ListFrames(root.ProjectID, 1000, 0)
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	internalIDs := map[string]bool{}
	rootFrames := make([]workspace.Frame, 0)
	for _, frame := range frames {
		if frame.RootFrameID == root.ID {
			internalIDs[frame.ID] = true
			rootFrames = append(rootFrames, frame)
		}
	}
	references := make([]map[string]any, 0)
	seen := map[string]bool{}
	for _, frame := range rootFrames {
		events, err := store.ListFrameEvents(frame.ID, 0, 1000)
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		for _, event := range events {
			candidates := map[string]string{}
			collectFrameReferenceFields(event.Payload, candidates)
			keys := make([]string, 0, len(candidates))
			for key := range candidates {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, field := range keys {
				targetID := strings.TrimSpace(candidates[field])
				if targetID == "" || internalIDs[targetID] {
					continue
				}
				key := field + "\x00" + targetID
				if seen[key] {
					continue
				}
				seen[key] = true
				references = append(references, map[string]any{
					"sourceFrameId": frame.ID, "eventId": event.ID,
					"eventType": event.Type, "field": field, "targetId": targetID,
				})
			}
		}
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "rootFrameId": root.ID, "references": references})
}

func collectFrameReferenceFields(value any, output map[string]string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			switch key {
			case "targetSessionId", "sourceSessionId", "parentSessionId", "referencedSessionId", "targetFrameId":
				if target := strings.TrimSpace(fmt.Sprint(item)); target != "" {
					output[key] = target
				}
			default:
				collectFrameReferenceFields(item, output)
			}
		}
	case []any:
		for _, item := range typed {
			collectFrameReferenceFields(item, output)
		}
	}
}
