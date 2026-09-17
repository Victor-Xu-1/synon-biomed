package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"synon-go/internal/synonlink"
)

const maxSynonLinkWebSocketMessageBytes = 20 * 1024 * 1024

func (s *Server) handleSynonLinkWebSocket(w http.ResponseWriter, r *http.Request) {
	userID := resolveUserID(r, nil)
	if userID == "" {
		http.Error(w, "userId is required", http.StatusBadRequest)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	conn.SetReadLimit(maxSynonLinkWebSocketMessageBytes)
	defer conn.Close(websocket.StatusNormalClosure, "closed")

	ctx := r.Context()
	_ = writeWebSocketJSON(ctx, conn, map[string]any{
		"type":       "connected",
		"serverTime": time.Now().UTC().UnixMilli(),
		"userId":     userID,
	})

	delivery := make(chan synonlink.Command, 16)
	accessEvents := make(chan synonlink.AccessEvent, 16)
	done := make(chan struct{})
	defer close(done)

	var clientID string
	defer func() {
		if clientID != "" {
			s.synonLink.Disconnect(clientID)
		}
	}()

	go func() {
		for {
			select {
			case <-done:
				return
			case command := <-delivery:
				if command.ID == "" {
					continue
				}
				_ = writeWebSocketJSON(ctx, conn, command)
			case event := <-accessEvents:
				if event.Event == "" {
					continue
				}
				_ = writeWebSocketJSON(ctx, conn, event)
			}
		}
	}()

	for {
		messageType, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if messageType != websocket.MessageText {
			continue
		}
		var message map[string]any
		if err := json.Unmarshal(data, &message); err != nil {
			_ = writeWebSocketJSON(ctx, conn, map[string]any{"type": "error", "message": "invalid JSON message"})
			continue
		}
		switch stringValue(message["type"]) {
		case "hello":
			client := clientFromHello(userID, message)
			if err := s.synonLink.Attach(client, delivery); err != nil {
				_ = writeWebSocketJSON(ctx, conn, map[string]any{"type": "error", "message": err.Error()})
				continue
			}
			if err := s.synonLink.AttachAccessEvents(userID, client.ID, accessEvents); err != nil {
				_ = writeWebSocketJSON(ctx, conn, map[string]any{"type": "error", "message": err.Error()})
				continue
			}
			clientID = client.ID
			registered := s.synonLink.ListClients(userID)
			var ackClient synonlink.Client
			for _, item := range registered {
				if item.ID == client.ID {
					ackClient = item
					break
				}
			}
			_ = writeWebSocketJSON(ctx, conn, map[string]any{"type": "hello_ack", "client": ackClient})
		case "command_progress":
			_ = s.synonLink.RecordProgress(
				clientID,
				stringValue(message["commandId"]),
				stringValue(message["status"]),
				stringValue(message["summary"]),
				mapValue(message["progress"]),
			)
		case "command_result":
			if okValue, ok := message["ok"].(bool); ok && !okValue {
				_ = s.synonLink.CompleteCommand(clientID, stringValue(message["commandId"]), map[string]any{
					"error": stringValue(message["error"]),
					"ok":    false,
				})
				continue
			}
			_ = s.synonLink.CompleteCommand(clientID, stringValue(message["commandId"]), mapValue(message["result"]))
		case "pong":
			// The current Go service updates liveness through progress/result paths.
		}
	}
}

func writeWebSocketJSON(ctx context.Context, conn *websocket.Conn, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, data)
}

func clientFromHello(userID string, message map[string]any) synonlink.Client {
	kind := stringValue(message["clientKind"])
	if kind == "" {
		kind = "browser"
	}
	version := stringValue(message["extensionVersion"])
	if version == "" {
		version = stringValue(message["agentVersion"])
	}
	return synonlink.Client{
		ID:               stringValue(message["clientId"]),
		UserID:           userID,
		DeviceID:         stringValue(message["deviceId"]),
		Name:             stringValue(message["name"]),
		Kind:             kind,
		Version:          version,
		Browser:          stringValue(message["browser"]),
		Platform:         stringValue(message["platform"]),
		Capabilities:     stringSlice(message["capabilities"]),
		SupportedActions: stringSlice(message["supportedActions"]),
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func stringSlice(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func mapValue(value any) map[string]any {
	if value == nil {
		return nil
	}
	result, _ := value.(map[string]any)
	return result
}
