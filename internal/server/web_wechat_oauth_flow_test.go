package server

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type testWeChatOAuthProvider struct {
	server        *httptest.Server
	appID         string
	appSecret     string
	mu            sync.Mutex
	redirectURL   string
	authorizeSeen bool
	tokenSeen     bool
	userInfoSeen  bool
}

func newTestWeChatOAuthProvider(t *testing.T) *testWeChatOAuthProvider {
	t.Helper()
	fixture := &testWeChatOAuthProvider{appID: "wx-synon-test", appSecret: "wechat-test-secret"}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (p *testWeChatOAuthProvider) serveHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/authorize":
		p.handleAuthorize(w, r)
	case "/token":
		p.handleToken(w, r)
	case "/userinfo":
		p.handleUserInfo(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (p *testWeChatOAuthProvider) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if query.Get("appid") != p.appID || query.Get("response_type") != "code" ||
		query.Get("scope") != webWeChatOAuthScope || query.Get("state") == "" ||
		query.Get("redirect_uri") == "" {
		http.Error(w, "invalid authorization request", http.StatusBadRequest)
		return
	}
	callback, err := url.Parse(query.Get("redirect_uri"))
	if err != nil {
		http.Error(w, "invalid redirect", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	p.redirectURL = callback.String()
	p.authorizeSeen = true
	p.mu.Unlock()
	values := callback.Query()
	values.Set("code", "wechat-fixture-code")
	values.Set("state", query.Get("state"))
	callback.RawQuery = values.Encode()
	http.Redirect(w, r, callback.String(), http.StatusSeeOther)
}

func (p *testWeChatOAuthProvider) handleToken(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if r.Method != http.MethodGet || query.Get("appid") != p.appID || query.Get("secret") != p.appSecret ||
		query.Get("code") != "wechat-fixture-code" || query.Get("grant_type") != "authorization_code" {
		http.Error(w, "invalid token exchange", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	p.tokenSeen = true
	p.mu.Unlock()
	writeTestWeChatJSON(w, http.StatusOK, map[string]any{
		"access_token":  "wechat-access-token-must-not-persist",
		"expires_in":    7200,
		"refresh_token": "wechat-refresh-token-must-not-persist",
		"openid":        "wechat-openid-must-not-persist",
		"scope":         webWeChatOAuthScope,
		"unionid":       "wechat-stable-unionid",
	})
}

func (p *testWeChatOAuthProvider) handleUserInfo(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if r.Method != http.MethodGet || query.Get("access_token") != "wechat-access-token-must-not-persist" ||
		query.Get("openid") != "wechat-openid-must-not-persist" || query.Get("lang") != "zh_CN" {
		http.Error(w, "invalid user info request", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	p.userInfoSeen = true
	p.mu.Unlock()
	writeTestWeChatJSON(w, http.StatusOK, map[string]any{
		"openid":   "wechat-openid-must-not-persist",
		"nickname": "微信研究员",
		"unionid":  "wechat-stable-unionid",
	})
}

func writeTestWeChatJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func TestWebWeChatOAuthProtocolFlowUsesUnionIDAndOpaqueSession(t *testing.T) {
	provider := newTestWeChatOAuthProvider(t)
	root := t.TempDir()
	app := New(Options{
		FileRoot: root,
		WebAuth: WebAuthOptions{WeChat: WebWeChatOAuthOptions{
			AppID: provider.appID, AppSecret: provider.appSecret,
			AuthorizeURL:   provider.server.URL + "/authorize",
			AccessTokenURL: provider.server.URL + "/token",
			UserInfoURL:    provider.server.URL + "/userinfo",
		}},
		HTTPClient: provider.server.Client(),
	})
	server := httptest.NewServer(app.Handler())
	t.Cleanup(server.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	client.Jar = jar
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

	providers, err := client.Get(server.URL + "/api/auth/providers")
	if err != nil {
		t.Fatal(err)
	}
	var capabilities struct {
		Providers []webExternalAuthProviderStatus `json:"providers"`
	}
	if err := json.NewDecoder(providers.Body).Decode(&capabilities); err != nil {
		providers.Body.Close()
		t.Fatal(err)
	}
	providers.Body.Close()
	if providers.StatusCode != http.StatusOK || len(capabilities.Providers) != 1 ||
		capabilities.Providers[0].ID != webWeChatProviderID ||
		capabilities.Providers[0].DisplayName != webWeChatDisplayName ||
		capabilities.Providers[0].StartPath != webWeChatOAuthRoutePrefix+"start" {
		t.Fatalf("provider status=%d capabilities=%#v", providers.StatusCode, capabilities)
	}

	start, err := client.Get(server.URL + webWeChatOAuthRoutePrefix + "start?remember=1&return_to=%2F%23%2Fsettings%2Faccount")
	if err != nil {
		t.Fatal(err)
	}
	start.Body.Close()
	if start.StatusCode != http.StatusSeeOther || !strings.HasPrefix(start.Header.Get("Location"), provider.server.URL+"/authorize") ||
		!strings.HasSuffix(start.Header.Get("Location"), "#wechat_redirect") {
		t.Fatalf("start status=%d location=%q", start.StatusCode, start.Header.Get("Location"))
	}
	if start.Header.Get("Cache-Control") != "no-store" || start.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("start security headers cache=%q referrer=%q", start.Header.Get("Cache-Control"), start.Header.Get("Referrer-Policy"))
	}
	authorize, err := client.Get(start.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	authorize.Body.Close()
	if authorize.StatusCode != http.StatusSeeOther ||
		!strings.HasPrefix(authorize.Header.Get("Location"), server.URL+webWeChatOAuthRoutePrefix+"callback") {
		t.Fatalf("authorize status=%d location=%q", authorize.StatusCode, authorize.Header.Get("Location"))
	}
	callbackURL := authorize.Header.Get("Location")
	callback, err := client.Get(callbackURL)
	if err != nil {
		t.Fatal(err)
	}
	callback.Body.Close()
	if callback.StatusCode != http.StatusSeeOther || callback.Header.Get("Location") != "/#/settings/account" {
		t.Fatalf("callback status=%d location=%q", callback.StatusCode, callback.Header.Get("Location"))
	}

	provider.mu.Lock()
	authorizeSeen, tokenSeen, userInfoSeen := provider.authorizeSeen, provider.tokenSeen, provider.userInfoSeen
	provider.mu.Unlock()
	if !authorizeSeen || !tokenSeen || !userInfoSeen {
		t.Fatalf("provider calls authorize=%v token=%v userInfo=%v", authorizeSeen, tokenSeen, userInfoSeen)
	}

	current, err := client.Get(server.URL + "/api/auth/user")
	if err != nil {
		t.Fatal(err)
	}
	var currentBody struct {
		Success bool `json:"success"`
		User    struct {
			ID       string `json:"id"`
			Username string `json:"username"`
			Email    string `json:"email"`
			Provider string `json:"provider"`
		} `json:"user"`
	}
	if err := json.NewDecoder(current.Body).Decode(&currentBody); err != nil {
		current.Body.Close()
		t.Fatal(err)
	}
	current.Body.Close()
	if current.StatusCode != http.StatusOK || !currentBody.Success || currentBody.User.ID == "" ||
		currentBody.User.Username != "微信研究员" || currentBody.User.Email != "" ||
		currentBody.User.Provider != webWeChatProviderID {
		t.Fatalf("current status=%d body=%#v", current.StatusCode, currentBody)
	}

	security, err := client.Get(server.URL + "/api/account/security")
	if err != nil {
		t.Fatal(err)
	}
	var securityBody struct {
		LoginMethods []string `json:"loginMethods"`
	}
	if err := json.NewDecoder(security.Body).Decode(&securityBody); err != nil {
		security.Body.Close()
		t.Fatal(err)
	}
	security.Body.Close()
	if security.StatusCode != http.StatusOK || len(securityBody.LoginMethods) != 1 ||
		securityBody.LoginMethods[0] != webWeChatProviderID {
		t.Fatalf("security status=%d body=%#v", security.StatusCode, securityBody)
	}

	for _, name := range []string{"webui-accounts.json", "webui-sessions.json"} {
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{
			provider.appSecret,
			"wechat-access-token-must-not-persist",
			"wechat-refresh-token-must-not-persist",
			"wechat-openid-must-not-persist",
		} {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("provider secret material persisted in %s", name)
			}
		}
	}

	replay, err := client.Get(callbackURL)
	if err != nil {
		t.Fatal(err)
	}
	replay.Body.Close()
	if replay.StatusCode != http.StatusSeeOther ||
		!strings.Contains(replay.Header.Get("Location"), "auth_error=transaction_invalid") {
		t.Fatalf("replay status=%d location=%q", replay.StatusCode, replay.Header.Get("Location"))
	}
}

func TestWebWeChatOAuthConfigurationRejectsPartialCredentialsAndInsecureEndpoints(t *testing.T) {
	if _, err := newWebExternalAuthRuntime(WebAuthOptions{
		WeChat: WebWeChatOAuthOptions{AppID: "wx-only"},
	}, nil); err == nil || !strings.Contains(err.Error(), "both AppID and AppSecret") {
		t.Fatalf("partial credentials error = %v", err)
	}
	if _, err := newWebExternalAuthRuntime(WebAuthOptions{
		WeChat: WebWeChatOAuthOptions{
			AppID: "wx-test", AppSecret: "secret",
			AuthorizeURL: "http://auth.example/authorize",
		},
	}, nil); err == nil || !strings.Contains(err.Error(), "must use HTTPS") {
		t.Fatalf("insecure endpoint error = %v", err)
	}
}
