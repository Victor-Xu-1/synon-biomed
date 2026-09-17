package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/config"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestPlanFromEnvSkipsPlatformsWithoutLiveCredentials(t *testing.T) {
	plan := planFromEnv(func(string) string { return "" })
	if plan.Message != "Synon Biomed v0.1.1 live IM smoke" {
		t.Fatalf("default message = %q", plan.Message)
	}
	if len(plan.Enabled) != 0 {
		t.Fatalf("enabled platforms = %#v", plan.Enabled)
	}
	if len(plan.Skipped) != 2 {
		t.Fatalf("skipped platforms = %#v", plan.Skipped)
	}
	for _, platform := range []string{"wechat", "feishu"} {
		if !plan.Skipped[platform] {
			t.Fatalf("platform %q should be skipped: %#v", platform, plan.Skipped)
		}
	}
}

func TestPlanFromConfigReusesAdapterCredentialsAndLiveTargets(t *testing.T) {
	cfg := config.Config{
		WeChat: config.WeChatConfig{BotToken: "wechat-config-token", BaseURL: "https://wechat.config", UserID: "wechat-adapter-user"},
		Feishu: config.FeishuConfig{AppID: "feishu-config-id", AppSecret: "feishu-config-secret", Domain: "https://feishu.config"},
		LiveIMSmoke: config.LiveIMSmokeConfig{
			Message:            "configured live smoke",
			TimeoutSeconds:     45,
			RuntimeURL:         "http://127.0.0.1:38080",
			RequireAll:         true,
			WeChatUserID:       "wechat-smoke-user",
			WeChatContextToken: "wechat-context",
			FeishuChatID:       "oc_feishu",
		},
	}
	plan := planFromConfigAndEnv(cfg, func(string) string { return "" })
	if len(plan.Enabled) != 2 || len(plan.Skipped) != 0 {
		t.Fatalf("plan readiness = enabled %#v skipped %#v", plan.Enabled, plan.Skipped)
	}
	if plan.Message != "configured live smoke" || plan.Timeout != 45*time.Second || plan.RuntimeURL != "http://127.0.0.1:38080" || !plan.RequireAll {
		t.Fatalf("plan options = %+v", plan)
	}
	if plan.WeChat.UserID != "wechat-smoke-user" || plan.WeChat.ContextToken != "wechat-context" || plan.Feishu.ChatID != "oc_feishu" {
		t.Fatalf("plan credentials/targets = %+v", plan)
	}
	overridden := planFromConfigAndEnv(cfg, func(key string) string {
		switch key {
		case "WECHAT_BOT_TOKEN":
			return "wechat-env-token"
		case "WECHAT_LIVE_SMOKE_USER_ID":
			return "wechat-env-user"
		case "SYNON_LIVE_IM_SMOKE_MESSAGE":
			return "environment live smoke"
		case "SYNON_LIVE_IM_RUNTIME_URL":
			return "http://127.0.0.1:48080"
		default:
			return ""
		}
	})
	if overridden.WeChat.Token != "wechat-env-token" || overridden.WeChat.UserID != "wechat-env-user" || overridden.Message != "environment live smoke" || overridden.RuntimeURL != "http://127.0.0.1:48080" {
		t.Fatalf("environment overrides = %+v", overridden)
	}
}

func TestPlanFromConfigAcceptsFeishuOpenIDTarget(t *testing.T) {
	cfg := config.Config{
		Feishu: config.FeishuConfig{AppID: "feishu-id", AppSecret: "feishu-secret"},
		LiveIMSmoke: config.LiveIMSmokeConfig{
			FeishuOpenID: "ou_live_user",
		},
	}
	plan := planFromConfigAndEnv(cfg, func(string) string { return "" })
	if plan.Skipped["feishu"] || plan.Feishu.OpenID != "ou_live_user" || plan.Feishu.ReceiveIDType != "open_id" || plan.Feishu.ReceiveID != "ou_live_user" {
		t.Fatalf("feishu open-id plan = %+v", plan.Feishu)
	}
	overridden := planFromConfigAndEnv(cfg, func(key string) string {
		if key == "FEISHU_LIVE_SMOKE_CHAT_ID" {
			return "oc_env_chat"
		}
		return ""
	})
	if overridden.Feishu.ReceiveIDType != "chat_id" || overridden.Feishu.ReceiveID != "oc_env_chat" {
		t.Fatalf("feishu chat-id override = %+v", overridden.Feishu)
	}
}

func TestRunFeishuSmokeSendsToOpenID(t *testing.T) {
	var receiveIDType string
	var receiveID string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"tenant-token"}`))
		case "/open-apis/im/v1/messages":
			receiveIDType = r.URL.Query().Get("receive_id_type")
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("Decode() error = %v", err)
			}
			receiveID, _ = body["receive_id"].(string)
			_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"om_live"}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer api.Close()

	err := runFeishuSmoke(context.Background(), feishuSmokeConfig{
		AppID:         "app-id",
		AppSecret:     "app-secret",
		Domain:        api.URL,
		OpenID:        "ou_live_user",
		ReceiveIDType: "open_id",
		ReceiveID:     "ou_live_user",
		Message:       "Synon live smoke",
	})
	if err != nil {
		t.Fatalf("runFeishuSmoke() error = %v", err)
	}
	if receiveIDType != "open_id" || receiveID != "ou_live_user" {
		t.Fatalf("delivery target type=%q id=%q", receiveIDType, receiveID)
	}
}

func TestLiveSmokePlanOutputReportsMissingCredentialFields(t *testing.T) {
	plan := planFromEnv(func(key string) string {
		switch key {
		case "WECHAT_BOT_TOKEN":
			return "TOKEN"
		case "SYNON_LIVE_IM_SMOKE_TIMEOUT_SECONDS":
			return "7"
		default:
			return ""
		}
	})
	result := newLiveSmokeOutput(plan)
	if result.TimeoutSeconds != 7 {
		t.Fatalf("TimeoutSeconds = %d", result.TimeoutSeconds)
	}
	for _, platform := range result.Platforms {
		if platform.Platform == "wechat" {
			if platform.Ready {
				t.Fatalf("wechat should not be ready without user id: %#v", platform)
			}
			if len(platform.MissingCredentialFields) != 1 || platform.MissingCredentialFields[0] != "WECHAT_LIVE_SMOKE_USER_ID or WECHAT_USER_ID" {
				t.Fatalf("wechat missing fields = %#v", platform.MissingCredentialFields)
			}
			return
		}
	}
	t.Fatalf("wechat platform plan missing: %#v", result.Platforms)
}

func TestRedactLiveSmokeSecretsRemovesTokensFromErrors(t *testing.T) {
	plan := liveSmokePlan{
		WeChat: wechatSmokeConfig{Token: "WECHAT-SECRET", ContextToken: "CTX-SECRET"},
		Feishu: feishuSmokeConfig{AppSecret: "FEISHU-SECRET", TenantToken: "FEISHU-TOKEN"},
	}
	redacted := redactLiveSmokeSecrets("sendMessage WECHAT-SECRET CTX-SECRET FEISHU-SECRET FEISHU-TOKEN", plan)
	for _, leaked := range []string{"WECHAT-SECRET", "CTX-SECRET", "FEISHU-SECRET", "FEISHU-TOKEN"} {
		if strings.Contains(redacted, leaked) {
			t.Fatalf("redacted error leaked %q: %s", leaked, redacted)
		}
	}
	if !strings.Contains(redacted, "<redacted>") {
		t.Fatalf("redacted error missing marker: %s", redacted)
	}
}

func TestRecordLiveSmokeRuntimeAuditWritesDeliveryEvidence(t *testing.T) {
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/auth/user" {
			_, _ = w.Write([]byte(`{"success":true,"user":{"id":"local"}}`))
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/tools/runtime_set/execute" {
			t.Errorf("runtime audit request = %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"entry":{"version":1}}}`))
	}))
	defer server.Close()

	session, err := prepareRuntimeAuditSession(context.Background(), server.Client(), server.URL, "", "")
	if err != nil {
		t.Fatalf("prepareRuntimeAuditSession() error = %v", err)
	}
	err = session.record(context.Background(), liveSmokePlatformResult{
		Platform: "wechat",
		Status:   "pass",
	})
	if err != nil {
		t.Fatalf("recordLiveSmokeRuntimeAudit() error = %v", err)
	}
	input := request["input"].(map[string]any)
	if input["namespace"] != "adapter-live-smoke" || input["key"] != "platform:wechat" {
		t.Fatalf("runtime audit target = %#v", input)
	}
	value := input["value"].(map[string]any)
	smoke := value["smoke"].(map[string]any)
	if value["platform"] != "wechat" || value["secretsRedacted"] != true || smoke["status"] != "pass" || smoke["liveChecked"] != true || smoke["deliveryChecked"] != true || smoke["method"] != "outbound_delivery" {
		t.Fatalf("runtime audit value = %#v", value)
	}
}

func TestPrepareRuntimeAuditSessionAuthenticatesAndSendsCSRF(t *testing.T) {
	const (
		username = "local"
		password = "runtime-password"
		csrf     = "csrf-token"
	)
	var auditRecorded atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/auth/user":
			if cookie, err := r.Cookie("synon_session"); err == nil && cookie.Value == "session-token" {
				_, _ = w.Write([]byte(`{"success":true,"user":{"id":"local"}}`))
				return
			}
			http.Error(w, "authentication required", http.StatusUnauthorized)
		case r.Method == http.MethodPost && r.URL.Path == "/login":
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Errorf("decode login: %v", err)
			}
			if input["username"] != username || input["password"] != password {
				t.Errorf("login input = %#v", input)
			}
			http.SetCookie(w, &http.Cookie{Name: "synon_session", Value: "session-token", Path: "/", HttpOnly: true})
			http.SetCookie(w, &http.Cookie{Name: "synon_csrf", Value: csrf, Path: "/"})
			_, _ = w.Write([]byte(`{"success":true,"user":{"id":"local"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/tools/runtime_set/execute":
			cookie, err := r.Cookie("synon_session")
			if err != nil || cookie.Value != "session-token" || r.Header.Get("X-Synon-CSRF-Token") != csrf {
				t.Errorf("authenticated audit cookie=%v err=%v csrf=%q", cookie, err, r.Header.Get("X-Synon-CSRF-Token"))
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			auditRecorded.Store(true)
			_, _ = w.Write([]byte(`{"ok":true,"result":{"entry":{"version":1}}}`))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	session, err := prepareRuntimeAuditSession(context.Background(), server.Client(), server.URL, username, password)
	if err != nil {
		t.Fatalf("prepareRuntimeAuditSession() error = %v", err)
	}
	if err := session.record(context.Background(), liveSmokePlatformResult{Platform: "feishu", Status: "pass"}); err != nil {
		t.Fatalf("record() error = %v", err)
	}
	if !auditRecorded.Load() {
		t.Fatal("authenticated runtime audit was not recorded")
	}
}

func TestPrepareRuntimeAuditSessionFailsBeforeDeliveryWithoutPassword(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "authentication required", http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := prepareRuntimeAuditSession(context.Background(), server.Client(), server.URL, "local", "")
	if err == nil || !strings.Contains(err.Error(), "password") {
		t.Fatalf("prepareRuntimeAuditSession() error = %v", err)
	}
}

func TestPrepareRuntimeAuditSessionRejectsNonLoopbackBeforeNetwork(t *testing.T) {
	called := false
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("network must not be reached")
	})}
	_, err := prepareRuntimeAuditSession(context.Background(), client, "https://example.com", "local", "secret")
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("prepareRuntimeAuditSession() error = %v", err)
	}
	if called {
		t.Fatal("non-loopback runtime audit attempted a network request")
	}
}
