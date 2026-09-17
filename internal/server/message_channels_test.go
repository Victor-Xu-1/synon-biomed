package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	adapterfeishu "synon-go/internal/adapters/feishu"
)

func TestMessageChannelStatusDoesNotTreatEmptyManagersAsConfigured(t *testing.T) {
	app := New(Options{FileRoot: t.TempDir()})
	httpServer := httptest.NewServer(app.Handler())
	defer httpServer.Close()
	request, err := http.NewRequest(http.MethodGet, httpServer.URL+"/api/adapters/message-channels", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Synon-User-Id", "owner-empty")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body struct {
		Channels map[string]map[string]bool `json:"channels"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || body.Channels["feishu"]["configured"] || body.Channels["wechat"]["configured"] {
		t.Fatalf("empty configuration status = %#v status=%d", body, response.StatusCode)
	}
}

func TestMessageChannelUnpairIsScopedToCurrentOwnerAndCleansConnectionState(t *testing.T) {
	app := New(Options{FileRoot: t.TempDir()})
	if err := app.persistMessageChannelConnection("feishu", "owner-a", "ou-a", "Owner A", map[string]string{"client_id": "client-a"}); err != nil {
		t.Fatalf("persist owner-a connection: %v", err)
	}
	if err := app.persistMessageChannelConnection("feishu", "owner-b", "ou-b", "Owner B", map[string]string{"client_id": "client-b"}); err != nil {
		t.Fatalf("persist owner-b connection: %v", err)
	}

	httpServer := httptest.NewServer(app.Handler())
	defer httpServer.Close()
	unpaired := postJSONWithUserForTest(t, httpServer.URL+"/api/adapters/message-channels/unpair", `{"channel":"feishu"}`, "owner-a")
	if !unpaired.OK || unpaired.Result["unpaired"] != true || unpaired.Result["pairedUsersRevoked"] != float64(1) {
		t.Fatalf("unpair owner-a = %#v", unpaired)
	}

	if _, found, err := app.pairingStore.Get("feishu", "ou-a"); err != nil || found {
		t.Fatalf("owner-a pairing after unpair found=%t err=%v", found, err)
	}
	if _, found, err := app.pairingStore.Get("feishu", "ou-b"); err != nil || !found {
		t.Fatalf("owner-b pairing after owner-a unpair found=%t err=%v", found, err)
	}
	if _, found, err := app.secretStore.ResolveForUser(messageChannelSecretID("feishu", "owner-a"), "owner-a"); err != nil || found {
		t.Fatalf("owner-a secret after unpair found=%t err=%v", found, err)
	}
	if _, found, err := app.secretStore.ResolveForUser(messageChannelSecretID("feishu", "owner-b"), "owner-b"); err != nil || !found {
		t.Fatalf("owner-b secret after owner-a unpair found=%t err=%v", found, err)
	}
	if _, found, err := app.runtimeStore.Get(messageChannelRuntimeNamespace, messageChannelRuntimeKey("feishu", "owner-a")); err != nil || found {
		t.Fatalf("owner-a runtime after unpair found=%t err=%v", found, err)
	}
	if _, found, err := app.runtimeStore.Get(messageChannelRuntimeNamespace, messageChannelRuntimeKey("feishu", "owner-b")); err != nil || !found {
		t.Fatalf("owner-b runtime after owner-a unpair found=%t err=%v", found, err)
	}

	request, err := http.NewRequest(http.MethodGet, httpServer.URL+"/api/adapters/message-channels", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Synon-User-Id", "owner-a")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var statusBody struct {
		Channels map[string]map[string]bool `json:"channels"`
	}
	if err := json.NewDecoder(response.Body).Decode(&statusBody); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || statusBody.Channels["feishu"]["paired"] || statusBody.Channels["feishu"]["configured"] {
		t.Fatalf("owner-a status after unpair = %#v status=%d", statusBody, response.StatusCode)
	}
}

func TestFeishuDeviceQREndpointReturnsImageAndPersistsProtectedCredentials(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/v1/app/registration" || r.Method != http.MethodPost {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse device form: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Form.Get("action") {
		case "init":
			_ = json.NewEncoder(w).Encode(map[string]any{"supported_auth_methods": []string{"client_secret"}})
		case "begin":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_code":               "device-1",
				"verification_uri_complete": "https://accounts.feishu.cn/device/complete?code=device-1",
				"expires_in":                600,
				"interval":                  1,
			})
		case "poll":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"client_id":     "client-1",
				"client_secret": "secret-must-stay-encrypted",
				"user_info": map[string]any{
					"open_id":      "ou-owner",
					"display_name": "Owner",
					"tenant_brand": "tenant",
				},
			})
		default:
			http.Error(w, "unknown action", http.StatusBadRequest)
		}
	}))
	defer api.Close()

	root := t.TempDir()
	app := New(Options{
		FileRoot:       root,
		FeishuDeviceQR: adapterfeishu.NewDeviceQRLoginManager(api.URL),
		HTTPClient:     api.Client(),
	})
	httpServer := httptest.NewServer(app.Handler())
	defer httpServer.Close()

	start := postJSONWithUserForTest(t, httpServer.URL+"/api/adapters/feishu/qr/start", `{"sessionKey":"feishu-session"}`, "owner-a")
	qrCodeURL, _ := start.Result["qrCodeUrl"].(string)
	if !start.OK || !strings.HasPrefix(qrCodeURL, "data:image/png;base64,") || strings.Contains(qrCodeURL, "device-1") {
		t.Fatalf("unsafe or incomplete Feishu start response = %#v", start)
	}

	otherOwner := postJSONWithUserForTest(t, httpServer.URL+"/api/adapters/feishu/qr/poll", `{"sessionKey":"feishu-session"}`, "owner-b")
	if !otherOwner.OK || otherOwner.Result["status"] != "error" {
		t.Fatalf("cross-owner poll = %#v", otherOwner)
	}
	poll := postJSONWithUserForTest(t, httpServer.URL+"/api/adapters/feishu/qr/poll", `{"sessionKey":"feishu-session"}`, "owner-a")
	if !poll.OK || poll.Result["status"] != "confirmed" || poll.Result["connected"] != true {
		t.Fatalf("confirmed poll = %#v", poll)
	}
	for _, key := range []string{"clientId", "clientSecret", "userOpenId", "userName"} {
		if _, present := poll.Result[key]; present {
			t.Fatalf("poll leaked credential field %q: %#v", key, poll.Result)
		}
	}
	secret, found, err := app.secretStore.ResolveForUser(messageChannelSecretID("feishu", "owner-a"), "owner-a")
	if err != nil || !found || secret.Credentials["client_secret"] != "secret-must-stay-encrypted" {
		t.Fatalf("Feishu protected credentials = %#v found=%t err=%v", secret, found, err)
	}
	paired, found, err := app.pairingStore.Get("feishu", "ou-owner")
	if err != nil || !found || paired.OwnerUserID != "owner-a" {
		t.Fatalf("Feishu pairing = %#v found=%t err=%v", paired, found, err)
	}

	statusRequest, err := http.NewRequestWithContext(context.Background(), http.MethodGet, httpServer.URL+"/api/adapters/message-channels", nil)
	if err != nil {
		t.Fatal(err)
	}
	statusRequest.Header.Set("X-Synon-User-Id", "owner-a")
	statusResponse, err := http.DefaultClient.Do(statusRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer statusResponse.Body.Close()
	var statusBody struct {
		Channels map[string]map[string]bool `json:"channels"`
	}
	if err := json.NewDecoder(statusResponse.Body).Decode(&statusBody); err != nil {
		t.Fatal(err)
	}
	if statusResponse.StatusCode != http.StatusOK || !statusBody.Channels["feishu"]["configured"] || !statusBody.Channels["feishu"]["paired"] {
		t.Fatalf("channel status = %#v status=%d", statusBody, statusResponse.StatusCode)
	}
}

func postJSONWithUserForTest(t *testing.T, target, payload, userID string) responseBodyForTest {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, target, bytes.NewReader([]byte(payload)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Synon-User-Id", userID)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST %s error = %v", target, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("POST %s status = %d", target, response.StatusCode)
	}
	var decoded responseBodyForTest
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode %s response: %v", target, err)
	}
	return decoded
}
