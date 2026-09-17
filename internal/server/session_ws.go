package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/coder/websocket"

	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
)

const maxSessionWebSocketMessageBytes = 2 * 1024 * 1024
const sessionWebSocketClientBuffer = 64

type sessionWebSocketHub struct {
	mu      sync.Mutex
	nextID  uint64
	clients map[string]map[uint64]*sessionWebSocketClient
}

type sessionWebSocketClient struct {
	id        uint64
	send      chan eventjournal.Message
	done      chan struct{}
	closeOnce sync.Once
}

func newSessionWebSocketHub() *sessionWebSocketHub {
	return &sessionWebSocketHub{
		clients: make(map[string]map[uint64]*sessionWebSocketClient),
	}
}

func (h *sessionWebSocketHub) Register(sessionID string) *sessionWebSocketClient {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[sessionID] == nil {
		h.clients[sessionID] = make(map[uint64]*sessionWebSocketClient)
	}
	id := h.nextID
	h.nextID++
	client := &sessionWebSocketClient{
		id:   id,
		send: make(chan eventjournal.Message, sessionWebSocketClientBuffer),
		done: make(chan struct{}),
	}
	h.clients[sessionID][id] = client
	return client
}

func (h *sessionWebSocketHub) Unregister(sessionID string, client *sessionWebSocketClient) {
	if h == nil || client == nil {
		return
	}
	h.mu.Lock()
	if clients := h.clients[sessionID]; clients != nil && clients[client.id] == client {
		delete(clients, client.id)
		if len(clients) == 0 {
			delete(h.clients, sessionID)
		}
	}
	h.mu.Unlock()
	client.Close()
}

func (h *sessionWebSocketHub) Send(sessionID string, message eventjournal.Message) bool {
	if h == nil || strings.TrimSpace(sessionID) == "" || message == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	delivered := false
	clients := h.clients[sessionID]
	for id, client := range clients {
		if client.Enqueue(message) {
			delivered = true
			continue
		}
		delete(clients, id)
	}
	if len(clients) == 0 {
		delete(h.clients, sessionID)
	}
	return delivered
}

func (h *sessionWebSocketHub) CloseSession(sessionID string) int {
	if h == nil || strings.TrimSpace(sessionID) == "" {
		return 0
	}
	h.mu.Lock()
	clients := h.clients[sessionID]
	delete(h.clients, sessionID)
	h.mu.Unlock()
	for _, client := range clients {
		client.Close()
	}
	return len(clients)
}

func (c *sessionWebSocketClient) Enqueue(message eventjournal.Message) bool {
	if c == nil || message == nil {
		return false
	}
	select {
	case <-c.done:
		return false
	default:
	}
	select {
	case c.send <- message:
		return true
	case <-c.done:
		return false
	default:
		c.Close()
		return false
	}
}

func (c *sessionWebSocketClient) Close() {
	if c == nil {
		return
	}
	c.closeOnce.Do(func() {
		close(c.done)
	})
}

func (s *Server) handleSessionWebSocket(w http.ResponseWriter, r *http.Request) {
	if !s.requireLegacySessionSurface(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "session websocket only supports GET")
		return
	}
	if s.sessionStore == nil || s.eventJournal == nil {
		http.Error(w, "session store is not configured", http.StatusBadRequest)
		return
	}
	sessionID, ok := sessionIDFromWebSocketPath(r.URL.Path)
	if !ok {
		http.Error(w, "invalid session id", http.StatusBadRequest)
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	if _, found, err := s.sessionStore.Get(sessionID); err != nil {
		http.Error(w, "session lookup failed", http.StatusInternalServerError)
		return
	} else if !found {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	if s.workspaceStore != nil {
		frameContext, found, err := s.workspaceStore.GetFrameRealtimeContext(sessionID)
		if err != nil {
			http.Error(w, "session ownership lookup failed", http.StatusInternalServerError)
			return
		}
		if found && frameContext.UserID != userID {
			http.Error(w, "resource not found", http.StatusNotFound)
			return
		}
	}

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(maxSessionWebSocketMessageBytes)
	defer conn.Close(websocket.StatusNormalClosure, "closed")

	client := s.sessionSockets.Register(sessionID)
	defer s.sessionSockets.Unregister(sessionID, client)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		sessionWebSocketWriteLoop(ctx, conn, client)
		_ = conn.CloseNow()
	}()

	if !client.Enqueue(eventjournal.Message{"type": "connected", "sessionId": sessionID}) || !s.replaySessionWebSocketCursor(r, client, sessionID) {
		cancel()
		<-writeDone
		return
	}

	for {
		messageType, data, err := conn.Read(ctx)
		if err != nil {
			cancel()
			<-writeDone
			return
		}
		if messageType != websocket.MessageText {
			continue
		}
		var message map[string]any
		if err := json.Unmarshal(data, &message); err != nil {
			client.Enqueue(eventjournal.Message{"type": "error", "code": "PARSE_ERROR", "message": "invalid JSON message"})
			continue
		}
		if !s.handleSessionWebSocketMessage(sessionID, client, message) {
			cancel()
			<-writeDone
			return
		}
	}
}

func sessionWebSocketWriteLoop(ctx context.Context, conn *websocket.Conn, client *sessionWebSocketClient) {
	for {
		select {
		case message := <-client.send:
			if err := writeWebSocketJSON(ctx, conn, message); err != nil {
				client.Close()
				return
			}
		case <-client.done:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (s *Server) replaySessionWebSocketCursor(r *http.Request, client *sessionWebSocketClient, sessionID string) bool {
	rawCursor := strings.TrimSpace(r.URL.Query().Get("lastEventId"))
	if rawCursor == "" {
		return true
	}
	afterEventID, err := strconv.ParseInt(rawCursor, 10, 64)
	if err != nil {
		return client.Enqueue(eventjournal.Message{"type": "error", "code": "BAD_CURSOR", "message": "lastEventId must be an integer"})
	}
	entries, err := s.eventJournal.ReadAfter(sessionID, afterEventID, 500)
	if err != nil {
		return client.Enqueue(eventjournal.Message{"type": "error", "code": "REPLAY_FAILED", "message": err.Error()})
	}
	for _, entry := range entries {
		if !client.Enqueue(entry.Message) {
			return false
		}
	}
	return true
}

func (s *Server) handleSessionWebSocketMessage(sessionID string, client *sessionWebSocketClient, message map[string]any) bool {
	switch stringValue(message["type"]) {
	case "ping":
		client.Enqueue(eventjournal.Message{"type": "pong"})
	case "release_session":
		return false
	case "user_message":
		if err := s.appendMirroredClientWebSocketMessage(sessionID, "user", message); err != nil {
			client.Enqueue(eventjournal.Message{"type": "error", "code": "USER_MESSAGE_FAILED", "message": err.Error()})
		}
	case "goal_driver":
		if err := s.appendMirroredClientWebSocketMessage(sessionID, "user", message); err != nil {
			client.Enqueue(eventjournal.Message{"type": "error", "code": "GOAL_DRIVER_FAILED", "message": err.Error()})
		}
	case "internal_task":
		if err := s.appendMirroredClientWebSocketMessage(sessionID, "system", message); err != nil {
			client.Enqueue(eventjournal.Message{"type": "error", "code": "INTERNAL_TASK_FAILED", "message": err.Error()})
		}
	case "permission_response":
		if err := s.appendMirroredClientWebSocketMessage(sessionID, "user", message); err != nil {
			client.Enqueue(eventjournal.Message{"type": "error", "code": "PERMISSION_RESPONSE_FAILED", "message": err.Error()})
		}
	case "stop_generation":
		accepted, runnerID := s.stopActiveSessionRun(sessionID, stringValue(message["reason"]))
		message["accepted"] = accepted
		if runnerID != "" {
			message["runnerId"] = runnerID
		}
		if err := s.appendMirroredClientWebSocketMessage(sessionID, "system", message); err != nil {
			client.Enqueue(eventjournal.Message{"type": "error", "code": "STOP_GENERATION_FAILED", "message": err.Error()})
		}
	default:
		client.Enqueue(eventjournal.Message{"type": "error", "code": "UNKNOWN_TYPE", "message": fmt.Sprintf("unknown message type: %s", stringValue(message["type"]))})
	}
	return true
}

func (s *Server) appendClientWebSocketMessage(sessionID string, role string, message map[string]any) error {
	_, err := s.appendClientWebSocketMessageEntry(sessionID, role, message)
	return err
}

func (s *Server) appendMirroredClientWebSocketMessage(sessionID string, role string, message map[string]any) error {
	entry, err := s.appendClientWebSocketMessageEntry(sessionID, role, message)
	if err != nil {
		return err
	}
	return s.mirrorSessionEntryToWorkspaceFrame(entry)
}

func (s *Server) appendClientWebSocketMessageEntry(sessionID string, role string, message map[string]any) (*eventjournal.Entry, error) {
	if s.sessionStore == nil || s.eventJournal == nil {
		return nil, fmt.Errorf("session store is not configured")
	}
	if _, ok, err := s.sessionStore.Get(sessionID); err != nil {
		return nil, err
	} else if !ok {
		if err := s.sessionStore.Upsert(sessionstore.Session{
			ID:      sessionID,
			Title:   sessionID,
			WorkDir: s.fileRoot,
		}); err != nil {
			return nil, err
		}
	}
	journalMessage := eventjournal.Message{}
	for key, value := range message {
		journalMessage[key] = value
	}
	journalMessage["role"] = role
	if _, ok := journalMessage["type"].(string); !ok {
		journalMessage["type"] = "message"
	}
	entry, err := s.eventJournal.Append(sessionID, journalMessage, eventjournal.Metadata{
		ClientMessageID: stringValue(message["clientMessageId"]),
	})
	if err != nil {
		return nil, err
	}
	if err := s.sessionStore.AppendMessage(sessionID, role); err != nil {
		return nil, err
	}
	return entry, nil
}

func sessionIDFromWebSocketPath(path string) (string, bool) {
	sessionID := strings.TrimPrefix(path, "/ws/")
	if sessionID == "" || sessionID == path || strings.Contains(sessionID, "/") {
		return "", false
	}
	for _, char := range sessionID {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' {
			continue
		}
		switch char {
		case '-', '_', '.', ':', '@':
			continue
		default:
			return "", false
		}
	}
	return sessionID, true
}
