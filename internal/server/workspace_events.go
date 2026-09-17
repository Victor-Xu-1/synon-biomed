package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	workspace "synon-go/internal/persistence/workspace"
)

const workspaceEventSubscriberBuffer = 64

type workspaceEventHub struct {
	mu          sync.Mutex
	nextID      uint64
	subscribers map[uint64]chan workspace.FrameEvent
}

func newWorkspaceEventHub() *workspaceEventHub {
	return &workspaceEventHub{subscribers: make(map[uint64]chan workspace.FrameEvent)}
}

func (h *workspaceEventHub) Subscribe() (<-chan workspace.FrameEvent, func()) {
	if h == nil {
		closed := make(chan workspace.FrameEvent)
		close(closed)
		return closed, func() {}
	}
	h.mu.Lock()
	id := h.nextID
	h.nextID++
	channel := make(chan workspace.FrameEvent, workspaceEventSubscriberBuffer)
	h.subscribers[id] = channel
	h.mu.Unlock()
	return channel, func() {
		h.mu.Lock()
		if current, ok := h.subscribers[id]; ok && current == channel {
			delete(h.subscribers, id)
			close(channel)
		}
		h.mu.Unlock()
	}
}

func (h *workspaceEventHub) Publish(event workspace.FrameEvent) int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	disconnected := 0
	for id, channel := range h.subscribers {
		select {
		case channel <- event:
		default:
			delete(h.subscribers, id)
			close(channel)
			disconnected++
		}
	}
	h.mu.Unlock()
	return disconnected
}

func (s *Server) handleWorkspaceEvents(w http.ResponseWriter, r *http.Request) {
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	frameID := strings.TrimSpace(r.URL.Query().Get("frame_id"))
	if frameID == "" {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "frame_id is required"})
		return
	}
	if !workspaceEventFrameOwned(w, r, store, frameID) {
		return
	}
	after, err := workspaceEventCursor(r)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	events, err := store.ListFrameEvents(frameID, after, workspaceEventLimit(r))
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "events": events})
}

func (s *Server) handleWorkspaceEventStream(w http.ResponseWriter, r *http.Request) {
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	frameID := strings.TrimSpace(r.URL.Query().Get("frame_id"))
	if frameID == "" {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "frame_id is required"})
		return
	}
	if !workspaceEventFrameOwned(w, r, store, frameID) {
		return
	}
	cursor, err := workspaceEventCursor(r)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "streaming is not supported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	channel, unsubscribe := s.workspaceEvents.Subscribe()
	defer unsubscribe()
	for {
		events, err := store.ListFrameEvents(frameID, cursor, 1000)
		if err != nil {
			return
		}
		for _, event := range events {
			if !writeWorkspaceSSE(w, event) {
				return
			}
			cursor = event.Sequence
		}
		flusher.Flush()
		break
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case event, open := <-channel:
			if !open {
				return
			}
			if event.FrameID != frameID || event.Sequence <= cursor {
				continue
			}
			if !writeWorkspaceSSE(w, event) {
				return
			}
			cursor = event.Sequence
			flusher.Flush()
		}
	}
}

func workspaceEventFrameOwned(w http.ResponseWriter, r *http.Request, store *workspace.Store, frameID string) bool {
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return false
	}
	frame, found, err := store.GetFrame(frameID)
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return false
	}
	if !found {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "resource not found"})
		return false
	}
	return workspaceProjectOwned(w, store, frame.ProjectID, userID)
}

func workspaceEventCursor(r *http.Request) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("after_sequence"))
	if raw == "" {
		raw = strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	}
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("invalid event cursor %q", raw)
	}
	return value, nil
}

func workspaceEventLimit(r *http.Request) int {
	value, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || value <= 0 {
		return 100
	}
	if value > 1000 {
		return 1000
	}
	return value
}

func writeWorkspaceSSE(w http.ResponseWriter, event workspace.FrameEvent) bool {
	payload, err := json.Marshal(event)
	if err != nil {
		return false
	}
	_, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.Sequence, event.Type, payload)
	return err == nil
}
