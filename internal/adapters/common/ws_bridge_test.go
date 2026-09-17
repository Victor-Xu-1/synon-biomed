package common

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestWsBridgeSendsUserMessagePermissionAndStopOverRealWebSocket(t *testing.T) {
	received := make(chan map[string]any, 3)
	server := newBridgeTestServer(t, func(_ *http.Request, conn *websocket.Conn) {
		for i := 0; i < 3; i++ {
			_, data, err := conn.Read(context.Background())
			if err != nil {
				return
			}
			var msg map[string]any
			if err := json.Unmarshal(data, &msg); err != nil {
				t.Errorf("decode websocket message: %v", err)
				return
			}
			received <- msg
		}
	})
	defer server.Close()

	bridge := NewWsBridge(serverURLToWS(server.URL), "feishu", WsBridgeOptions{})
	defer bridge.Destroy()
	if connected := bridge.ConnectSession(context.Background(), "chat-1", "sess-1"); !connected {
		t.Fatal("ConnectSession returned false for first connection")
	}
	if ok := bridge.WaitForOpen(context.Background(), "chat-1", time.Second); !ok {
		t.Fatal("websocket did not open")
	}
	if !bridge.SendUserMessage("chat-1", "hello", []AttachmentRef{{Type: "image", Name: "plot.png", Path: "/tmp/plot.png"}}) {
		t.Fatal("SendUserMessage returned false")
	}
	if !bridge.SendPermissionResponse("chat-1", "req-1", true, PermissionResponseOptions{
		Rule:         "always",
		UpdatedInput: map[string]any{"answer": "yes"},
		ToolUseID:    "tool-1",
	}) {
		t.Fatal("SendPermissionResponse returned false")
	}
	if !bridge.SendStopGeneration("chat-1") {
		t.Fatal("SendStopGeneration returned false")
	}

	user := mustReceiveBridgeMessage(t, received)
	if user["type"] != "user_message" || user["content"] != "hello" {
		t.Fatalf("user message = %+v", user)
	}
	attachments := user["attachments"].([]any)
	if len(attachments) != 1 || attachments[0].(map[string]any)["type"] != "image" {
		t.Fatalf("attachments = %+v", attachments)
	}
	permission := mustReceiveBridgeMessage(t, received)
	if permission["type"] != "permission_response" || permission["requestId"] != "req-1" || permission["allowed"] != true || permission["rule"] != "always" || permission["toolUseID"] != "tool-1" {
		t.Fatalf("permission message = %+v", permission)
	}
	stop := mustReceiveBridgeMessage(t, received)
	if stop["type"] != "stop_generation" {
		t.Fatalf("stop message = %+v", stop)
	}
}

func TestWsBridgeSendsAuthAndIMModeHeaders(t *testing.T) {
	headers := make(chan http.Header, 1)
	server := newBridgeTestServer(t, func(r *http.Request, conn *websocket.Conn) {
		headers <- r.Header.Clone()
		_ = conn.Close(websocket.StatusNormalClosure, "done")
	})
	defer server.Close()

	bridge := NewWsBridge(serverURLToWS(server.URL), "wechat", WsBridgeOptions{AuthToken: "secret"})
	defer bridge.Destroy()
	bridge.ConnectSession(context.Background(), "chat-auth", "sess-auth")
	if ok := bridge.WaitForOpen(context.Background(), "chat-auth", time.Second); !ok {
		t.Fatal("websocket did not open")
	}

	got := <-headers
	if got.Get("Authorization") != "Bearer secret" {
		t.Fatalf("authorization = %q", got.Get("Authorization"))
	}
	if got.Get("X-Synon-IM-Mode") != "wechat" {
		t.Fatalf("im mode = %q", got.Get("X-Synon-IM-Mode"))
	}
}

func TestWsBridgeSerializesServerHandlersPerChat(t *testing.T) {
	var serverConn *websocket.Conn
	ready := make(chan struct{})
	server := newBridgeTestServer(t, func(_ *http.Request, conn *websocket.Conn) {
		serverConn = conn
		close(ready)
		<-conn.CloseRead(context.Background()).Done()
	})
	defer server.Close()

	bridge := NewWsBridge(serverURLToWS(server.URL), "feishu", WsBridgeOptions{})
	defer bridge.Destroy()
	events := make([]string, 0)
	var mu sync.Mutex
	bridge.OnServerMessage("chat-serial", func(msg ServerMessage) {
		tag, _ := msg["tag"].(string)
		delay, _ := msg["delay"].(float64)
		mu.Lock()
		events = append(events, "start:"+tag)
		mu.Unlock()
		time.Sleep(time.Duration(delay) * time.Millisecond)
		mu.Lock()
		events = append(events, "end:"+tag)
		mu.Unlock()
	})
	bridge.ConnectSession(context.Background(), "chat-serial", "sess-serial")
	if ok := bridge.WaitForOpen(context.Background(), "chat-serial", time.Second); !ok {
		t.Fatal("websocket did not open")
	}
	<-ready
	for _, payload := range []string{
		`{"tag":"1","delay":40}`,
		`{"tag":"2","delay":5}`,
		`{"tag":"3","delay":5}`,
	} {
		if err := serverConn.Write(context.Background(), websocket.MessageText, []byte(payload)); err != nil {
			t.Fatalf("server write: %v", err)
		}
	}
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	want := []string{"start:1", "end:1", "start:2", "end:2", "start:3", "end:3"}
	if len(events) != len(want) {
		t.Fatalf("events = %#v", events)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("events = %#v", events)
		}
	}
}

func TestWsBridgeResetSessionReleasesAndRemovesSession(t *testing.T) {
	received := make(chan map[string]any, 1)
	server := newBridgeTestServer(t, func(_ *http.Request, conn *websocket.Conn) {
		_, data, err := conn.Read(context.Background())
		if err != nil {
			return
		}
		var msg map[string]any
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Errorf("decode release message: %v", err)
			return
		}
		received <- msg
	})
	defer server.Close()

	bridge := NewWsBridge(serverURLToWS(server.URL), "feishu", WsBridgeOptions{})
	bridge.ConnectSession(context.Background(), "chat-reset", "sess-reset")
	if ok := bridge.WaitForOpen(context.Background(), "chat-reset", time.Second); !ok {
		t.Fatal("websocket did not open")
	}
	bridge.ResetSession("chat-reset")
	defer bridge.Destroy()

	release := mustReceiveBridgeMessage(t, received)
	if release["type"] != "release_session" || release["reason"] != "adapter reset session" {
		t.Fatalf("release = %+v", release)
	}
	if bridge.HasSession("chat-reset") {
		t.Fatal("session still present after reset")
	}
	if bridge.SendUserMessage("chat-reset", "after reset", nil) {
		t.Fatal("send succeeded after reset")
	}
}

func TestWsBridgeReconnectsAfterUnexpectedCloseAndSendsAgain(t *testing.T) {
	var connections atomic.Int32
	firstClosed := make(chan struct{})
	secondReceived := make(chan map[string]any, 1)
	server := newBridgeTestServer(t, func(_ *http.Request, conn *websocket.Conn) {
		count := connections.Add(1)
		if count == 1 {
			_ = conn.Close(websocket.StatusGoingAway, "test reconnect")
			close(firstClosed)
			return
		}
		_, data, err := conn.Read(context.Background())
		if err != nil {
			return
		}
		var msg map[string]any
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Errorf("decode reconnected message: %v", err)
			return
		}
		secondReceived <- msg
	})
	defer server.Close()

	bridge := NewWsBridge(serverURLToWS(server.URL), "feishu", WsBridgeOptions{
		ReconnectBaseDelay: 20 * time.Millisecond,
		ReconnectMaxDelay:  20 * time.Millisecond,
	})
	defer bridge.Destroy()
	if !bridge.ConnectSession(context.Background(), "chat-reconnect", "sess-reconnect") {
		t.Fatal("initial ConnectSession returned false")
	}
	<-firstClosed
	waitUntil(t, time.Second, func() bool {
		return connections.Load() >= 2 && bridge.IsSessionReady("chat-reconnect")
	})
	if !bridge.SendUserMessage("chat-reconnect", "after reconnect", nil) {
		t.Fatal("SendUserMessage returned false after reconnect")
	}
	msg := mustReceiveBridgeMessage(t, secondReceived)
	if msg["type"] != "user_message" || msg["content"] != "after reconnect" {
		t.Fatalf("reconnected message = %+v", msg)
	}
}

func TestWsBridgeSendsHeartbeatPingAndAcceptsPong(t *testing.T) {
	pings := make(chan struct{}, 2)
	server := newBridgeTestServer(t, func(_ *http.Request, conn *websocket.Conn) {
		for {
			_, data, err := conn.Read(context.Background())
			if err != nil {
				return
			}
			var msg map[string]any
			if err := json.Unmarshal(data, &msg); err != nil {
				t.Errorf("decode heartbeat message: %v", err)
				return
			}
			if msg["type"] == "ping" {
				pings <- struct{}{}
				if err := conn.Write(context.Background(), websocket.MessageText, []byte(`{"type":"pong"}`)); err != nil {
					return
				}
			}
		}
	})
	defer server.Close()

	bridge := NewWsBridge(serverURLToWS(server.URL), "feishu", WsBridgeOptions{
		HeartbeatInterval:    20 * time.Millisecond,
		PongTimeout:          20 * time.Millisecond,
		MissedHeartbeatLimit: 3,
	})
	defer bridge.Destroy()
	if !bridge.ConnectSession(context.Background(), "chat-heartbeat", "sess-heartbeat") {
		t.Fatal("ConnectSession returned false")
	}
	if !bridge.WaitForOpen(context.Background(), "chat-heartbeat", time.Second) {
		t.Fatal("websocket did not open")
	}
	select {
	case <-pings:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for heartbeat ping")
	}
	if !bridge.IsSessionReady("chat-heartbeat") {
		t.Fatal("session should remain ready after pong")
	}
}

func newBridgeTestServer(t *testing.T, handle func(*http.Request, *websocket.Conn)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		go handle(r, conn)
	}))
}

func serverURLToWS(raw string) string {
	return "ws" + raw[len("http"):]
}

func mustReceiveBridgeMessage(t *testing.T, ch <-chan map[string]any) map[string]any {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for bridge message")
		return nil
	}
}

func waitUntil(t *testing.T, timeout time.Duration, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}
