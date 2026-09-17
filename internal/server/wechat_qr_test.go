package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	adapterwechat "synon-go/internal/adapters/wechat"
)

func TestWeChatQRLoginEndpointsStartAndPoll(t *testing.T) {
	const providerQRURL = "https://liteapp.weixin.qq.com/q/provider-ticket"
	wechatAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/get_bot_qrcode":
			if r.URL.Query().Get("bot_type") != "3" {
				t.Fatalf("bot_type = %q", r.URL.Query().Get("bot_type"))
			}
			_, _ = w.Write([]byte(`{"qrcode":"qr-ticket","qrcode_img_content":"` + providerQRURL + `"}`))
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
			t.Fatalf("unexpected WeChat API path %q", r.URL.Path)
		}
	}))
	defer wechatAPI.Close()

	root := t.TempDir()
	app := New(Options{
		FileRoot:   root,
		WeChatQR:   adapterwechat.NewQRLoginManager(wechatAPI.URL),
		HTTPClient: wechatAPI.Client(),
	})
	httpServer := httptest.NewServer(app.Handler())
	defer httpServer.Close()

	start, startRaw := postJSONForTest(t, httpServer.URL+"/api/adapters/wechat/qr/start", `{"sessionKey":"session-1"}`)
	qrCodeURL, _ := start.Result["qrCodeUrl"].(string)
	if start.OK != true || start.Result["sessionKey"] != "session-1" || !strings.HasPrefix(qrCodeURL, "data:image/png;base64,") {
		t.Fatalf("start response = %#v", start)
	}
	png, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(qrCodeURL, "data:image/png;base64,"))
	if err != nil || len(png) < 8 || string(png[:8]) != "\x89PNG\r\n\x1a\n" {
		t.Fatalf("start QR is not a PNG: bytes=%x err=%v", png[:min(len(png), 8)], err)
	}
	for _, sensitive := range []string{providerQRURL, "qr-ticket", "wechat-token", "wechat-account", "wechat-user"} {
		if bytes.Contains(startRaw, []byte(sensitive)) {
			t.Fatalf("start response leaked %q: %s", sensitive, startRaw)
		}
	}

	poll, pollRaw := postJSONForTest(t, httpServer.URL+"/api/adapters/wechat/qr/poll", `{"sessionKey":"session-1"}`)
	if poll.OK != true || poll.Result["connected"] != true || poll.Result["status"] != string(adapterwechat.QRStatusConfirmed) {
		t.Fatalf("poll response = %#v", poll)
	}
	for _, sensitive := range []string{"qr-ticket", providerQRURL, "wechat-token", "wechat-account", "https://wechat-gateway.example", "wechat-user"} {
		if bytes.Contains(pollRaw, []byte(sensitive)) {
			t.Fatalf("poll response leaked %q: %s", sensitive, pollRaw)
		}
	}
	for _, key := range []string{"botToken", "accountId", "baseUrl", "userId"} {
		if _, present := poll.Result[key]; present {
			t.Fatalf("poll leaked credential field %q: %#v", key, poll.Result)
		}
	}
	secret, found, err := app.secretStore.ResolveForUser(messageChannelSecretID("wechat", "local"), "local")
	if err != nil || !found || secret.Credentials["bot_token"] != "wechat-token" || secret.Credentials["account_id"] != "wechat-account" {
		t.Fatalf("persisted WeChat credentials = %#v found=%t err=%v", secret, found, err)
	}
	paired, found, err := app.pairingStore.Get("wechat", "wechat-user")
	if err != nil || !found || paired.OwnerUserID != "local" {
		t.Fatalf("pairing = %#v found=%t err=%v", paired, found, err)
	}

	missing, _ := postJSONForTest(t, httpServer.URL+"/api/adapters/wechat/qr/poll", `{"sessionKey":"session-1"}`)
	if missing.OK != true || missing.Result["connected"] != false || missing.Result["status"] != "error" {
		t.Fatalf("missing response = %#v", missing)
	}
}

type responseBodyForTest struct {
	OK     bool           `json:"ok"`
	Result map[string]any `json:"result"`
}

func postJSONForTest(t *testing.T, target string, payload string) (responseBodyForTest, []byte) {
	t.Helper()
	resp, err := http.Post(target, "application/json", bytes.NewReader([]byte(payload)))
	if err != nil {
		t.Fatalf("POST %s error = %v", target, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s status = %d", target, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		t.Fatalf("read %s response: %v", target, err)
	}
	var decoded responseBodyForTest
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode %s response: %v", target, err)
	}
	return decoded, raw
}
