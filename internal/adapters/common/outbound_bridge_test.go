package common

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestRegisterOutboundHandlerRoutesRealWebSocketServerMessages(t *testing.T) {
	var serverConn *websocket.Conn
	ready := make(chan struct{})
	server := newBridgeTestServer(t, func(_ *http.Request, conn *websocket.Conn) {
		serverConn = conn
		close(ready)
		<-conn.CloseRead(context.Background()).Done()
	})
	defer server.Close()

	processed := make(chan string, 1)
	observedErr := make(chan error, 1)
	bridge := NewWsBridge(serverURLToWS(server.URL), "wechat", WsBridgeOptions{})
	defer bridge.Destroy()
	RegisterOutboundHandler(bridge, "chat-1", func(ctx context.Context, message ServerMessage) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("handler context should include timeout")
		}
		text, _ := message["text"].(string)
		processed <- text
		return errors.New("send failed")
	}, OutboundBridgeOptions{
		Timeout: time.Second,
		OnError: func(err error) {
			observedErr <- err
		},
	})
	if !bridge.ConnectSession(context.Background(), "chat-1", "sess-1") {
		t.Fatal("ConnectSession returned false")
	}
	if !bridge.WaitForOpen(context.Background(), "chat-1", time.Second) {
		t.Fatal("websocket did not open")
	}
	<-ready
	if err := serverConn.Write(context.Background(), websocket.MessageText, []byte(`{"type":"content_delta","text":"hello from server"}`)); err != nil {
		t.Fatalf("server write: %v", err)
	}

	select {
	case text := <-processed:
		if text != "hello from server" {
			t.Fatalf("processed text = %q", text)
		}
	case <-time.After(time.Second):
		t.Fatal("server message was not processed")
	}
	select {
	case err := <-observedErr:
		if err == nil || err.Error() != "send failed" {
			t.Fatalf("observed error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("handler error was not reported")
	}
}

func TestRegisterOutboundHandlerRetriesAndDeduplicatesServerMessages(t *testing.T) {
	var serverConn *websocket.Conn
	ready := make(chan struct{})
	server := newBridgeTestServer(t, func(_ *http.Request, conn *websocket.Conn) {
		serverConn = conn
		close(ready)
		<-conn.CloseRead(context.Background()).Done()
	})
	defer server.Close()

	var attempts atomic.Int32
	results := make(chan OutboundDeliveryResult, 4)
	unexpectedErr := make(chan error, 1)
	bridge := NewWsBridge(serverURLToWS(server.URL), "feishu", WsBridgeOptions{})
	defer bridge.Destroy()
	RegisterOutboundHandler(bridge, "chat-retry", func(_ context.Context, message ServerMessage) error {
		attempt := attempts.Add(1)
		if attempt == 1 {
			return errors.New("transient platform send failure")
		}
		if message["eventId"] != "evt-1" {
			return errors.New("unexpected outbound message event id")
		}
		return nil
	}, OutboundBridgeOptions{
		Timeout:     time.Second,
		MaxAttempts: 2,
		Deduplicate: true,
		OnResult: func(result OutboundDeliveryResult) {
			results <- result
		},
		OnError: func(err error) {
			unexpectedErr <- err
		},
	})
	if !bridge.ConnectSession(context.Background(), "chat-retry", "sess-retry") {
		t.Fatal("ConnectSession returned false")
	}
	if !bridge.WaitForOpen(context.Background(), "chat-retry", time.Second) {
		t.Fatal("websocket did not open")
	}
	<-ready
	payload := []byte(`{"type":"content_delta","eventId":"evt-1","text":"hello once"}`)
	if err := serverConn.Write(context.Background(), websocket.MessageText, payload); err != nil {
		t.Fatalf("first server write: %v", err)
	}
	first := mustReceiveDeliveryResult(t, results)
	if !first.Delivered || first.Duplicate || first.Attempts != 2 || first.MessageKey != "eventId:evt-1" {
		t.Fatalf("first delivery = %#v", first)
	}
	if err := serverConn.Write(context.Background(), websocket.MessageText, payload); err != nil {
		t.Fatalf("second server write: %v", err)
	}
	second := mustReceiveDeliveryResult(t, results)
	if !second.Delivered || !second.Duplicate || second.Attempts != 0 || attempts.Load() != 2 {
		t.Fatalf("duplicate delivery = %#v attempts=%d", second, attempts.Load())
	}
	select {
	case err := <-unexpectedErr:
		t.Fatalf("unexpected delivery error: %v", err)
	default:
	}
}

func mustReceiveDeliveryResult(t *testing.T, ch <-chan OutboundDeliveryResult) OutboundDeliveryResult {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for delivery result")
	}
	return OutboundDeliveryResult{}
}
