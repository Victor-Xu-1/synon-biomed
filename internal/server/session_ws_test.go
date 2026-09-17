package server

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	adapterfeishu "synon-go/internal/adapters/feishu"
	eventjournal "synon-go/internal/persistence/journal"
)

func TestLocalLegacySessionWebSocketReplaysAndReceivesAppendedSessionEvents(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	allowPairingForTest(t, srv, "feishu", "ou_session_ws")
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()

	first, err := srv.HandleFeishuInbound(context.Background(), adapterfeishu.InboundEvent{
		EventID: "evt-session-ws-1", MessageID: "om_session_ws_1", ChatID: "oc_session_ws", ChatType: "p2p",
		SenderOpenID: "ou_session_ws", MessageType: "text", Text: "live websocket replay",
		TaskTitle: "live websocket replay", DedupID: "evt-session-ws-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := first["sessionId"].(string)
	if sessionID != "im:feishu:oc_session_ws" {
		t.Fatalf("sessionId = %q body = %#v", sessionID, first)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := strings.Replace(httpServer.URL, "http://", "ws://", 1) + "/ws/" + url.PathEscape(sessionID) + "?lastEventId=0"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial session websocket: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	connected := readWSMessage(t, ctx, conn)
	if connected["type"] != "connected" || connected["sessionId"] != sessionID {
		t.Fatalf("connected = %#v", connected)
	}
	replayed := readWSMessage(t, ctx, conn)
	if replayed["type"] != "im_message" || replayed["role"] != "user" || !strings.Contains(replayed["text"].(string), "live websocket replay") || replayed["eventId"] != float64(1) {
		t.Fatalf("replayed = %#v", replayed)
	}
	second, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial second session websocket: %v", err)
	}
	defer second.Close(websocket.StatusNormalClosure, "done")
	if connected := readWSMessage(t, ctx, second); connected["type"] != "connected" {
		t.Fatalf("second connected=%#v", connected)
	}
	if replayed := readWSMessage(t, ctx, second); replayed["eventId"] != float64(1) {
		t.Fatalf("second replayed=%#v", replayed)
	}

	appended, err := srv.executeDirectToolGatewayResponse(context.Background(), "session_append", map[string]any{
		"sessionId": sessionID,
		"role":      "assistant",
		"message": map[string]any{
			"type": "assistant_message",
			"text": "assistant reply delivered over websocket",
		},
		"clientMessageId": "assistant-session-ws-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	result := appended["result"].(map[string]any)
	if result["delivered"] != true {
		t.Fatalf("session_append did not report live delivery: %#v", appended)
	}

	delivered := readWSMessage(t, ctx, conn)
	if delivered["type"] != "assistant_message" || delivered["role"] != "assistant" || delivered["text"] != "assistant reply delivered over websocket" || delivered["eventId"] != float64(2) {
		t.Fatalf("delivered = %#v", delivered)
	}
	secondDelivered := readWSMessage(t, ctx, second)
	if secondDelivered["type"] != "assistant_message" || secondDelivered["eventId"] != float64(2) {
		t.Fatalf("second delivered=%#v", secondDelivered)
	}

	writeWSMessage(t, ctx, conn, map[string]any{"type": "ping"})
	pong := readWSMessage(t, ctx, conn)
	if pong["type"] != "pong" {
		t.Fatalf("pong = %#v", pong)
	}
}

func TestSessionWebSocketHubDisconnectsOnlyLaggingClient(t *testing.T) {
	hub := newSessionWebSocketHub()
	fast := hub.Register("session")
	slow := hub.Register("session")
	select {
	case <-fast.done:
		t.Fatal("registering a second client closed the first")
	default:
	}
	for sequence := int64(1); sequence <= sessionWebSocketClientBuffer+1; sequence++ {
		if !hub.Send("session", eventjournal.Message{"type": "text_chunk", "sequence": sequence}) {
			t.Fatalf("send %d did not reach the fast client", sequence)
		}
		select {
		case <-fast.send:
		case <-time.After(time.Second):
			t.Fatalf("fast client missed sequence %d", sequence)
		}
	}
	select {
	case <-slow.done:
	default:
		t.Fatal("lagging client was not disconnected")
	}
	select {
	case <-fast.done:
		t.Fatal("fast client was disconnected with lagging client")
	default:
	}
}
