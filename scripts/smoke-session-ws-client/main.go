package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/coder/websocket"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: smoke-session-ws-client <http-base-url>")
		os.Exit(2)
	}

	baseURL := strings.TrimRight(os.Args[1], "/")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sessionID := createSession(ctx, baseURL)
	wsURL := sessionWebSocketURL(baseURL, sessionID, 0)
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: http.Header{"X-Synon-User-Id": []string{"session-ws-smoke"}}})
	if err != nil {
		fmt.Fprintf(os.Stderr, "dial session websocket: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	connected := read(ctx, conn)
	if connected["type"] != "connected" || connected["sessionId"] != sessionID {
		fmt.Fprintf(os.Stderr, "unexpected connected message: %#v\n", connected)
		os.Exit(1)
	}
	replayed := read(ctx, conn)
	if replayed["type"] != "im_message" || replayed["role"] != "user" || replayed["eventId"] != float64(1) {
		fmt.Fprintf(os.Stderr, "unexpected replayed message: %#v\n", replayed)
		os.Exit(1)
	}
	text, _ := replayed["text"].(string)
	if !strings.Contains(text, "session websocket smoke") {
		fmt.Fprintf(os.Stderr, "unexpected replayed text: %#v\n", replayed)
		os.Exit(1)
	}

	appendSessionMessage(ctx, baseURL, sessionID)
	delivered := read(ctx, conn)
	if delivered["type"] != "assistant_message" || delivered["role"] != "assistant" || delivered["eventId"] != float64(2) {
		fmt.Fprintf(os.Stderr, "unexpected delivered message: %#v\n", delivered)
		os.Exit(1)
	}
	if delivered["text"] != "session websocket delivery ready" {
		fmt.Fprintf(os.Stderr, "unexpected delivered text: %#v\n", delivered)
		os.Exit(1)
	}

	write(ctx, conn, map[string]any{"type": "ping"})
	pong := read(ctx, conn)
	if pong["type"] != "pong" {
		fmt.Fprintf(os.Stderr, "unexpected pong: %#v\n", pong)
		os.Exit(1)
	}

	fmt.Println("SESSION_WEBSOCKET_API=yes")
}

func createSession(ctx context.Context, baseURL string) string {
	body := map[string]any{
		"schema": "2.0",
		"header": map[string]any{
			"event_id":   "evt-session-ws-smoke",
			"event_type": "im.message.receive_v1",
		},
		"event": map[string]any{
			"sender": map[string]any{"sender_id": map[string]any{"open_id": "ou_session_ws_smoke"}},
			"message": map[string]any{
				"message_id":   "om_session_ws_smoke",
				"chat_id":      "oc_session_ws_smoke",
				"chat_type":    "p2p",
				"message_type": "text",
				"content":      `{"text":"session websocket smoke"}`,
			},
		},
	}
	result := postJSON(ctx, baseURL+"/api/adapters/feishu/event", body)
	sessionID, _ := result["sessionId"].(string)
	if sessionID != "im:feishu:oc_session_ws_smoke" {
		fmt.Fprintf(os.Stderr, "unexpected session id: %#v\n", result)
		os.Exit(1)
	}
	return sessionID
}

func appendSessionMessage(ctx context.Context, baseURL string, sessionID string) {
	body := map[string]any{
		"input": map[string]any{
			"sessionId": sessionID,
			"role":      "assistant",
			"message": map[string]any{
				"type": "assistant_message",
				"text": "session websocket delivery ready",
			},
			"clientMessageId": "smoke-session-ws-append",
		},
	}
	result := postJSON(ctx, baseURL+"/api/tools/session_append/execute", body)
	payload, _ := result["result"].(map[string]any)
	if payload["delivered"] != true {
		fmt.Fprintf(os.Stderr, "session_append was not delivered: %#v\n", result)
		os.Exit(1)
	}
}

func postJSON(ctx context.Context, endpoint string, body map[string]any) map[string]any {
	raw, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		panic(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "post %s: %v\n", endpoint, err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		fmt.Fprintf(os.Stderr, "decode %s: %v\n", endpoint, err)
		os.Exit(1)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || result["ok"] == false {
		fmt.Fprintf(os.Stderr, "post %s failed: status=%d body=%#v\n", endpoint, resp.StatusCode, result)
		os.Exit(1)
	}
	return result
}

func sessionWebSocketURL(baseURL string, sessionID string, lastEventID int64) string {
	wsURL := baseURL
	wsURL = strings.TrimPrefix(wsURL, "http://")
	wsURL = strings.TrimPrefix(wsURL, "https://")
	scheme := "ws"
	if strings.HasPrefix(baseURL, "https://") {
		scheme = "wss"
	}
	return fmt.Sprintf("%s://%s/ws/%s?lastEventId=%d", scheme, wsURL, url.PathEscape(sessionID), lastEventID)
}

func read(ctx context.Context, conn *websocket.Conn) map[string]any {
	messageType, data, err := conn.Read(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read websocket: %v\n", err)
		os.Exit(1)
	}
	if messageType != websocket.MessageText {
		fmt.Fprintf(os.Stderr, "unexpected websocket message type: %v\n", messageType)
		os.Exit(1)
	}
	var message map[string]any
	if err := json.Unmarshal(data, &message); err != nil {
		fmt.Fprintf(os.Stderr, "decode websocket message %q: %v\n", data, err)
		os.Exit(1)
	}
	return message
}

func write(ctx context.Context, conn *websocket.Conn, message map[string]any) {
	raw, err := json.Marshal(message)
	if err != nil {
		panic(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, raw); err != nil {
		fmt.Fprintf(os.Stderr, "write websocket: %v\n", err)
		os.Exit(1)
	}
}
