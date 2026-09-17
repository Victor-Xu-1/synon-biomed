package feishu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestDeviceQRLoginManagerUsesOfficialDeviceFlowAndKeepsCredentialsServerSide(t *testing.T) {
	var pollCount int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != deviceRegistrationPath {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
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
				"interval":                  5,
			})
		case "poll":
			pollCount++
			if pollCount == 1 {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "authorization_pending"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"client_id":     "cli-1",
				"client_secret": "secret-must-not-leave-server",
				"user_info": map[string]any{
					"open_id":      "ou-owner",
					"display_name": "Owner",
					"tenant_brand": "feishu",
				},
			})
		default:
			http.Error(w, "unknown action", http.StatusBadRequest)
		}
	}))
	defer api.Close()

	manager := NewDeviceQRLoginManager(api.URL)
	now := time.Now().UTC()
	manager.now = func() time.Time { return now }
	start, err := manager.Start(context.Background(), api.Client(), DeviceQRStartOptions{SessionKey: "session-1"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if start.SessionKey != "session-1" || start.VerificationURL == "" || start.ExpiresAt.Before(now.Add(9*time.Minute)) {
		t.Fatalf("unexpected start result: %#v", start)
	}
	if pollCount != 0 {
		t.Fatalf("poll started during QR creation")
	}

	pending, err := manager.Poll(context.Background(), api.Client(), DeviceQRPollOptions{SessionKey: start.SessionKey})
	if err != nil || pending.Status != DeviceQRStatusWait || pollCount != 1 {
		t.Fatalf("pending poll = %#v, err=%v, calls=%d", pending, err, pollCount)
	}
	now = now.Add(5 * time.Second)
	confirmed, err := manager.Poll(context.Background(), api.Client(), DeviceQRPollOptions{SessionKey: start.SessionKey})
	if err != nil {
		t.Fatalf("confirmed poll: %v", err)
	}
	if confirmed.Status != DeviceQRStatusConfirmed || !confirmed.Connected || confirmed.UserOpenID != "ou-owner" {
		t.Fatalf("unexpected confirmed result: %#v", confirmed)
	}
	if confirmed.ClientSecret != "secret-must-not-leave-server" {
		t.Fatalf("manager did not retain provider credential for the persistence boundary")
	}
	raw, _ := json.Marshal(confirmed)
	if strings.Contains(string(raw), "secret-must-not-leave-server") {
		t.Fatalf("provider secret escaped the JSON response: %s", raw)
	}
	if pollCount != 2 {
		t.Fatalf("poll calls = %d, want 2", pollCount)
	}

	manager.Forget(start.SessionKey)
	expired, err := manager.Poll(context.Background(), api.Client(), DeviceQRPollOptions{SessionKey: start.SessionKey})
	if err != nil || expired.Status != DeviceQRStatusExpired {
		t.Fatalf("expired session = %#v, err=%v", expired, err)
	}
}

func TestDeviceQRLoginManagerRejectsIncompleteVerificationPayload(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Form.Get("action") == "init" {
			_ = json.NewEncoder(w).Encode(map[string]any{"supported_auth_methods": []string{"client_secret"}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "device-1", "verification_uri": "https://accounts.feishu.cn"})
	}))
	defer api.Close()

	_, err := NewDeviceQRLoginManager(api.URL).Start(context.Background(), api.Client(), DeviceQRStartOptions{})
	if err == nil || !strings.Contains(err.Error(), "incomplete QR payload") {
		t.Fatalf("start error = %v", err)
	}
}

func TestDeviceQRLoginManagerReturnsExistingSessionWithoutForce(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Form.Get("action") {
		case "init":
			_ = json.NewEncoder(w).Encode(map[string]any{"supported_auth_methods": []string{"client_secret"}})
		case "begin":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_code":               "device-1",
				"verification_uri_complete": "https://accounts.feishu.cn/device/complete?code=device-1",
			})
		}
	}))
	defer api.Close()

	manager := NewDeviceQRLoginManager(api.URL)
	first, err := manager.Start(context.Background(), api.Client(), DeviceQRStartOptions{SessionKey: "session-1"})
	if err != nil {
		t.Fatalf("first start: %v", err)
	}
	second, err := manager.Start(context.Background(), api.Client(), DeviceQRStartOptions{SessionKey: "session-1"})
	if err != nil {
		t.Fatalf("second start: %v", err)
	}
	if first.VerificationURL != second.VerificationURL || first.SessionKey != second.SessionKey {
		t.Fatalf("existing session was not reused: first=%#v second=%#v", first, second)
	}
}

func TestDeviceQRLoginManagerStartFormUsesExpectedOAuthValues(t *testing.T) {
	var forms []url.Values
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		forms = append(forms, r.Form)
		w.Header().Set("Content-Type", "application/json")
		if r.Form.Get("action") == "init" {
			_ = json.NewEncoder(w).Encode(map[string]any{"supported_auth_methods": []string{"client_secret"}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":               "device-1",
			"verification_uri_complete": "https://accounts.feishu.cn/device/complete?code=device-1",
		})
	}))
	defer api.Close()

	if _, err := NewDeviceQRLoginManager(api.URL).Start(context.Background(), api.Client(), DeviceQRStartOptions{}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if len(forms) != 2 || forms[0].Get("action") != "init" || forms[1].Get("action") != "begin" ||
		forms[1].Get("archetype") != "PersonalAgent" || forms[1].Get("auth_method") != "client_secret" {
		t.Fatalf("unexpected device flow forms: %#v", forms)
	}
}
