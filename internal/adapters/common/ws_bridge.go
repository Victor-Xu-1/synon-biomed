package common

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type AttachmentRef struct {
	Type     string `json:"type"`
	Name     string `json:"name,omitempty"`
	Path     string `json:"path,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

type ServerMessage map[string]any

type MessageHandler func(ServerMessage)

type PermissionResponseOptions struct {
	Rule         string
	UpdatedInput map[string]any
	ToolUseID    string
}

type WsBridgeOptions struct {
	AuthToken            string
	ReconnectBaseDelay   time.Duration
	ReconnectMaxDelay    time.Duration
	HeartbeatInterval    time.Duration
	PongTimeout          time.Duration
	MissedHeartbeatLimit int
}

type WsBridge struct {
	serverURL string
	platform  string
	authToken string
	options   WsBridgeOptions

	mu        sync.Mutex
	sessions  map[string]*wsBridgeSession
	handlers  map[string]MessageHandler
	bindings  map[string]string
	destroyed bool
}

type wsBridgeSession struct {
	sessionID string
	conn      *websocket.Conn
	cancel    context.CancelFunc
	opened    chan struct{}
	done      chan struct{}
	sendMu    sync.Mutex
	queue     chan ServerMessage

	reconnectAttempts int
	activityMu        sync.Mutex
	lastActivityAt    time.Time
	missedHeartbeats  int
}

func NewWsBridge(serverURL string, platform string, options WsBridgeOptions) *WsBridge {
	options = normalizeWsBridgeOptions(options)
	return &WsBridge{
		serverURL: strings.TrimRight(serverURL, "/"),
		platform:  platform,
		authToken: strings.TrimSpace(options.AuthToken),
		options:   options,
		sessions:  map[string]*wsBridgeSession{},
		handlers:  map[string]MessageHandler{},
		bindings:  map[string]string{},
	}
}

func (b *WsBridge) ConnectSession(ctx context.Context, chatID string, sessionID string) bool {
	return b.connectSession(ctx, chatID, sessionID, 0)
}

func (b *WsBridge) connectSession(ctx context.Context, chatID string, sessionID string, reconnectAttempts int) bool {
	if b == nil || strings.TrimSpace(chatID) == "" || strings.TrimSpace(sessionID) == "" {
		return false
	}
	b.mu.Lock()
	if b.destroyed {
		b.mu.Unlock()
		return false
	}
	if existing := b.sessions[chatID]; existing != nil {
		b.mu.Unlock()
		return false
	}
	if b.bindings == nil {
		b.bindings = map[string]string{}
	}
	b.bindings[chatID] = sessionID
	b.mu.Unlock()

	headers := http.Header{}
	if b.authToken != "" {
		headers.Set("Authorization", "Bearer "+b.authToken)
	}
	if imMode := normalizeBridgeIMMode(b.platform); imMode != "" {
		headers.Set("X-Synon-IM-Mode", imMode)
	}
	headers.Set("X-Synon-User-Id", strings.TrimSpace(b.platform)+":"+strings.TrimSpace(chatID))
	conn, _, err := websocket.Dial(ctx, b.serverURL+"/ws/"+sessionID, &websocket.DialOptions{
		HTTPHeader: headers,
	})
	if err != nil {
		return false
	}
	sessionCtx, cancel := context.WithCancel(context.Background())
	session := &wsBridgeSession{
		sessionID:         sessionID,
		conn:              conn,
		cancel:            cancel,
		opened:            make(chan struct{}),
		done:              make(chan struct{}),
		queue:             make(chan ServerMessage, 64),
		reconnectAttempts: reconnectAttempts,
		lastActivityAt:    time.Now(),
	}
	close(session.opened)

	b.mu.Lock()
	if b.destroyed || b.bindings[chatID] != sessionID {
		b.mu.Unlock()
		cancel()
		_ = conn.Close(websocket.StatusNormalClosure, "session no longer requested")
		return false
	}
	if existing := b.sessions[chatID]; existing != nil {
		b.mu.Unlock()
		cancel()
		_ = conn.Close(websocket.StatusNormalClosure, "duplicate session")
		return false
	}
	b.sessions[chatID] = session
	b.mu.Unlock()

	go b.readLoop(sessionCtx, chatID, session)
	go b.handlerLoop(sessionCtx, chatID, session)
	go b.heartbeatLoop(sessionCtx, session)
	return true
}

func (b *WsBridge) SendUserMessage(chatID string, content string, attachments []AttachmentRef) bool {
	message := map[string]any{
		"type":    "user_message",
		"content": content,
	}
	if len(attachments) > 0 {
		message["attachments"] = attachments
	}
	return b.send(chatID, message)
}

func (b *WsBridge) SendGoalDriver(chatID string, content string, displayContent string) bool {
	message := map[string]any{
		"type":    "goal_driver",
		"content": content,
	}
	if strings.TrimSpace(displayContent) != "" {
		message["displayContent"] = strings.TrimSpace(displayContent)
	}
	return b.send(chatID, message)
}

func (b *WsBridge) SendInternalTask(chatID string, content string, taskKind string) bool {
	return b.send(chatID, map[string]any{
		"type":     "internal_task",
		"content":  content,
		"taskKind": taskKind,
	})
}

func (b *WsBridge) SendPermissionResponse(chatID string, requestID string, allowed bool, options PermissionResponseOptions) bool {
	message := map[string]any{
		"type":      "permission_response",
		"requestId": requestID,
		"allowed":   allowed,
	}
	if strings.TrimSpace(options.Rule) != "" {
		message["rule"] = strings.TrimSpace(options.Rule)
	}
	if options.UpdatedInput != nil {
		message["updatedInput"] = options.UpdatedInput
	}
	if strings.TrimSpace(options.ToolUseID) != "" {
		message["toolUseID"] = strings.TrimSpace(options.ToolUseID)
	}
	return b.send(chatID, message)
}

func (b *WsBridge) SendStopGeneration(chatID string) bool {
	return b.send(chatID, map[string]any{"type": "stop_generation"})
}

func (b *WsBridge) OnServerMessage(chatID string, handler MessageHandler) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.handlers == nil {
		b.handlers = map[string]MessageHandler{}
	}
	if handler == nil {
		delete(b.handlers, chatID)
		return
	}
	b.handlers[chatID] = handler
}

func (b *WsBridge) ResetSession(chatID string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	session := b.sessions[chatID]
	delete(b.sessions, chatID)
	delete(b.bindings, chatID)
	delete(b.handlers, chatID)
	b.mu.Unlock()
	if session == nil {
		return
	}
	_ = session.writeJSON(context.Background(), map[string]any{
		"type":   "release_session",
		"reason": "adapter reset session",
	})
	session.cancel()
	_ = session.conn.Close(websocket.StatusNormalClosure, "session reset")
}

func (b *WsBridge) HasSession(chatID string) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessions[chatID] != nil || b.handlers[chatID] != nil
}

func (b *WsBridge) IsSessionReady(chatID string) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	session := b.sessions[chatID]
	b.mu.Unlock()
	return session != nil
}

func (b *WsBridge) WaitForOpen(ctx context.Context, chatID string, timeout time.Duration) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	session := b.sessions[chatID]
	b.mu.Unlock()
	if session == nil {
		return false
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-session.opened:
		return true
	case <-session.done:
		return false
	case <-ctx.Done():
		return false
	case <-timer.C:
		return false
	}
}

func (b *WsBridge) Destroy() {
	if b == nil {
		return
	}
	b.mu.Lock()
	sessions := make([]*wsBridgeSession, 0, len(b.sessions))
	for _, session := range b.sessions {
		sessions = append(sessions, session)
	}
	b.sessions = map[string]*wsBridgeSession{}
	b.handlers = map[string]MessageHandler{}
	b.bindings = map[string]string{}
	b.destroyed = true
	b.mu.Unlock()
	for _, session := range sessions {
		session.cancel()
		_ = session.conn.Close(websocket.StatusNormalClosure, "bridge destroyed")
	}
}

func (b *WsBridge) send(chatID string, message map[string]any) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	session := b.sessions[chatID]
	b.mu.Unlock()
	if session == nil {
		return false
	}
	return session.writeJSON(context.Background(), message) == nil
}

func (b *WsBridge) readLoop(ctx context.Context, chatID string, session *wsBridgeSession) {
	var readErr error
	defer func() {
		close(session.done)
		if ctx.Err() == nil {
			b.handleSessionClosed(chatID, session, readErr)
		}
	}()
	for {
		_, data, err := session.conn.Read(ctx)
		if err != nil {
			readErr = err
			return
		}
		session.touch()
		var msg ServerMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if msg["type"] == "pong" {
			continue
		}
		select {
		case session.queue <- msg:
		case <-ctx.Done():
			return
		}
	}
}

func (b *WsBridge) heartbeatLoop(ctx context.Context, session *wsBridgeSession) {
	interval := b.options.HeartbeatInterval
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if !session.markHeartbeatTick(interval, b.options.PongTimeout, b.options.MissedHeartbeatLimit) {
				_ = session.conn.Close(websocket.StatusCode(4001), "pong timeout")
				return
			}
			if err := session.writeJSON(ctx, map[string]any{"type": "ping"}); err != nil {
				_ = session.conn.Close(websocket.StatusCode(4001), "heartbeat write failed")
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func (b *WsBridge) handleSessionClosed(chatID string, session *wsBridgeSession, err error) {
	status := websocket.CloseStatus(err)
	if status == websocket.StatusNormalClosure {
		b.removeSessionIfCurrent(chatID, session)
		return
	}
	attempt := session.reconnectAttempts + 1
	b.removeSessionIfCurrent(chatID, session)
	b.scheduleReconnect(chatID, session.sessionID, attempt)
}

func (b *WsBridge) removeSessionIfCurrent(chatID string, session *wsBridgeSession) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sessions[chatID] == session {
		delete(b.sessions, chatID)
	}
}

func (b *WsBridge) scheduleReconnect(chatID string, sessionID string, attempt int) {
	if b == nil {
		return
	}
	delay := reconnectDelay(b.options.ReconnectBaseDelay, b.options.ReconnectMaxDelay, attempt)
	timer := time.NewTimer(delay)
	go func() {
		defer timer.Stop()
		select {
		case <-timer.C:
			b.mu.Lock()
			shouldReconnect := !b.destroyed && b.bindings[chatID] == sessionID && b.sessions[chatID] == nil
			b.mu.Unlock()
			if !shouldReconnect {
				return
			}
			if !b.connectSession(context.Background(), chatID, sessionID, attempt) {
				b.scheduleReconnect(chatID, sessionID, attempt+1)
			}
		}
	}()
}

func (b *WsBridge) handlerLoop(ctx context.Context, chatID string, session *wsBridgeSession) {
	for {
		select {
		case msg := <-session.queue:
			handler := b.handlerFor(chatID)
			if handler != nil {
				handler(msg)
			}
		case <-ctx.Done():
			return
		}
	}
}

func (b *WsBridge) handlerFor(chatID string) MessageHandler {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.handlers[chatID]
}

func (s *wsBridgeSession) writeJSON(ctx context.Context, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.conn.Write(ctx, websocket.MessageText, raw)
}

func (s *wsBridgeSession) touch() {
	s.activityMu.Lock()
	defer s.activityMu.Unlock()
	s.lastActivityAt = time.Now()
	s.missedHeartbeats = 0
}

func (s *wsBridgeSession) markHeartbeatTick(interval time.Duration, timeout time.Duration, missedLimit int) bool {
	if missedLimit <= 0 {
		missedLimit = 3
	}
	s.activityMu.Lock()
	defer s.activityMu.Unlock()
	if s.lastActivityAt.IsZero() {
		s.lastActivityAt = time.Now()
	}
	if time.Since(s.lastActivityAt) > interval+timeout {
		s.missedHeartbeats++
	}
	return s.missedHeartbeats < missedLimit
}

func normalizeBridgeIMMode(platform string) string {
	switch strings.TrimSpace(strings.ToLower(platform)) {
	case "feishu", "wechat":
		return strings.TrimSpace(strings.ToLower(platform))
	default:
		return ""
	}
}

func normalizeWsBridgeOptions(options WsBridgeOptions) WsBridgeOptions {
	if options.ReconnectBaseDelay <= 0 {
		options.ReconnectBaseDelay = time.Second
	}
	if options.ReconnectMaxDelay <= 0 {
		options.ReconnectMaxDelay = 30 * time.Second
	}
	if options.ReconnectMaxDelay < options.ReconnectBaseDelay {
		options.ReconnectMaxDelay = options.ReconnectBaseDelay
	}
	if options.HeartbeatInterval == 0 {
		options.HeartbeatInterval = 15 * time.Second
	}
	if options.PongTimeout <= 0 {
		options.PongTimeout = 10 * time.Second
	}
	if options.MissedHeartbeatLimit <= 0 {
		options.MissedHeartbeatLimit = 3
	}
	return options
}

func reconnectDelay(base time.Duration, max time.Duration, attempt int) time.Duration {
	if attempt <= 1 {
		return base
	}
	delay := base
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= max {
			return max
		}
	}
	return delay
}
