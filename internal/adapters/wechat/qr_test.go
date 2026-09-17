package wechat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestQRLoginManagerStartsAndReusesFreshQRCode(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ilink/bot/get_bot_qrcode" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("bot_type") != "3" {
			t.Fatalf("bot_type = %q", r.URL.Query().Get("bot_type"))
		}
		requests++
		_, _ = w.Write([]byte(`{"qrcode":"qr-ticket","qrcode_img_content":"https://example.test/qr.png"}`))
	}))
	defer server.Close()

	manager := NewQRLoginManager(server.URL)
	manager.TTL = time.Minute
	first, err := manager.Start(context.Background(), http.DefaultClient, QRStartOptions{SessionKey: "sess-1"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	second, err := manager.Start(context.Background(), http.DefaultClient, QRStartOptions{SessionKey: "sess-1"})
	if err != nil {
		t.Fatalf("second Start() error = %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d", requests)
	}
	if first.SessionKey != "sess-1" || first.QRCodeURL != "https://example.test/qr.png" {
		t.Fatalf("first start = %+v", first)
	}
	if second.QRCodeURL != first.QRCodeURL {
		t.Fatalf("second start = %+v, first = %+v", second, first)
	}
}

func TestQRLoginManagerPollsConfirmedLogin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/get_bot_qrcode":
			_, _ = w.Write([]byte(`{"qrcode":"qr-ticket","qrcode_img_content":"https://example.test/qr.png"}`))
		case "/ilink/bot/get_qrcode_status":
			if r.URL.Query().Get("qrcode") != "qr-ticket" {
				t.Fatalf("qrcode = %q", r.URL.Query().Get("qrcode"))
			}
			_, _ = w.Write([]byte(`{
				"status":"confirmed",
				"bot_token":"wechat-token",
				"ilink_bot_id":"wechat-account",
				"baseurl":"https://wechat-gateway.example",
				"ilink_user_id":"wechat-user"
			}`))
		default:
			t.Fatalf("path = %q", r.URL.Path)
		}
	}))
	defer server.Close()

	manager := NewQRLoginManager(server.URL)
	started, err := manager.Start(context.Background(), http.DefaultClient, QRStartOptions{SessionKey: "sess-2"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if started.SessionKey != "sess-2" {
		t.Fatalf("started = %+v", started)
	}
	result, err := manager.Poll(context.Background(), http.DefaultClient, QRPollOptions{SessionKey: "sess-2"})
	if err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	if !result.Connected || result.Status != QRStatusConfirmed {
		t.Fatalf("poll result = %+v", result)
	}
	if result.BotToken != "wechat-token" || result.AccountID != "wechat-account" || result.BaseURL != "https://wechat-gateway.example" || result.UserID != "wechat-user" {
		t.Fatalf("credentials = %+v", result)
	}
	manager.Forget("sess-2")
	missing, err := manager.Poll(context.Background(), http.DefaultClient, QRPollOptions{SessionKey: "sess-2"})
	if err != nil {
		t.Fatalf("Poll missing error = %v", err)
	}
	if missing.Status != QRStatusNotStarted || missing.Connected {
		t.Fatalf("missing result = %+v", missing)
	}
}
