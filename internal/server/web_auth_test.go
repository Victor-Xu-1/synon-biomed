package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"synon-go/internal/persistence/workspace"
)

func TestWebAuthProvidesLoopbackLocalModeAndRejectsRemoteBootstrap(t *testing.T) {
	app := New(Options{})
	request := httptest.NewRequest(http.MethodGet, "/api/auth/user", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"username":"local"`) {
		t.Fatalf("loopback status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/auth/user", nil)
	request.RemoteAddr = "203.0.113.8:12345"
	response = httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("remote status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestWebAuthLoopbackLocalModeIgnoresStaleSessionCookie(t *testing.T) {
	app := New(Options{})
	for _, testCase := range []struct {
		path   string
		marker string
	}{
		{path: "/api/auth/user", marker: `"username":"local"`},
		{path: "/api/me", marker: `"user_id":"local"`},
	} {
		request := httptest.NewRequest(http.MethodGet, testCase.path, nil)
		request.RemoteAddr = "127.0.0.1:12345"
		request.AddCookie(&http.Cookie{Name: webSessionCookieName, Value: "stale-session-token"})
		response := httptest.NewRecorder()

		app.Handler().ServeHTTP(response, request)

		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), testCase.marker) {
			t.Fatalf("path=%s stale-cookie status=%d body=%s", testCase.path, response.Code, response.Body.String())
		}
	}
}

func TestWebAuthPasswordSessionInjectsIdentity(t *testing.T) {
	app := New(Options{FileRoot: t.TempDir(), SynonLinkAuth: SynonLinkAuthOptions{
		Username: "operator", Password: "test-secret-password", UserID: "user-1", TTL: time.Hour,
	}})
	login := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"operator","password":"test-secret-password","remember":true}`))
	login.RemoteAddr = "127.0.0.1:12345"
	login.Header.Set("Content-Type", "application/json")
	login.Header.Set("Origin", "http://localhost:8765")
	login.Host = "localhost:8765"
	loginResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	var sessionCookie *http.Cookie
	for _, cookie := range loginResponse.Result().Cookies() {
		if cookie.Name == webSessionCookieName {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil || !sessionCookie.HttpOnly || sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie = %#v", sessionCookie)
	}

	me := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	me.RemoteAddr = "127.0.0.1:12345"
	me.AddCookie(sessionCookie)
	meResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(meResponse, me)
	if meResponse.Code != http.StatusOK {
		t.Fatalf("me status=%d body=%s", meResponse.Code, meResponse.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(meResponse.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["user_id"] != "user-1" {
		t.Fatalf("me payload = %#v", payload)
	}
}

func TestWebAuthMigratesLegacySignedBrowserSessionToOpaqueStore(t *testing.T) {
	app := New(Options{
		FileRoot: t.TempDir(),
		SynonLinkAuth: SynonLinkAuthOptions{
			Username: "operator", Password: "test-secret-password", UserID: "user-1", TTL: time.Hour,
		},
	})
	legacyToken, _, err := app.synonLinkAuth.LoginForScope(
		"operator", "test-secret-password", synonSessionScopeWeb,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/auth/user", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	request.AddCookie(&http.Cookie{Name: webSessionCookieName, Value: legacyToken})
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("migration status=%d body=%s", response.Code, response.Body.String())
	}
	var migrated *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == webSessionCookieName {
			migrated = cookie
		}
	}
	if migrated == nil || migrated.Value == legacyToken {
		t.Fatalf("migrated cookie = %#v", migrated)
	}
	if _, valid, err := app.webSessions.Authenticate(migrated.Value, false); err != nil || !valid {
		t.Fatalf("opaque migration valid=%v err=%v", valid, err)
	}
	replay := httptest.NewRequest(http.MethodGet, "/api/auth/user", nil)
	replay.RemoteAddr = "127.0.0.1:12345"
	replay.AddCookie(&http.Cookie{Name: webSessionCookieName, Value: legacyToken})
	replayResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(replayResponse, replay)
	if replayResponse.Code != http.StatusUnauthorized {
		t.Fatalf("legacy replay status=%d body=%s", replayResponse.Code, replayResponse.Body.String())
	}
}

func TestWebLogoutKeepsLegacyCompatibilityCookieRevocation(t *testing.T) {
	root := t.TempDir()
	workspaceStore, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaceStore.Close() })
	app := New(Options{FileRoot: root, Workspace: workspaceStore, SynonLinkAuth: SynonLinkAuthOptions{
		Username: "operator", Password: "test-secret-password", UserID: "user-1",
	}})
	legacyToken, _, err := app.synonLinkAuth.LoginForScope(
		"operator", "test-secret-password", synonSessionScopeWeb,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/logout", strings.NewReader(`{}`))
	request.RemoteAddr = "127.0.0.1:12345"
	request.AddCookie(&http.Cookie{Name: "session", Value: legacyToken})
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("legacy logout status=%d body=%s", response.Code, response.Body.String())
	}
	if revoked, err := app.synonLinkSessionRevoked(legacyToken); err != nil || !revoked {
		t.Fatalf("legacy session revoked=%v err=%v", revoked, err)
	}
}

func TestP2PasswordModeDefaultsToDenyAndRejectsSpoofedIdentity(t *testing.T) {
	app := New(Options{SynonLinkAuth: SynonLinkAuthOptions{
		Username: "operator", Password: "test-secret-password", UserID: "user-1",
	}})
	for _, path := range []string{"/api/me", "/api/tools", "/api/events/stream", "/ws/session-1"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.RemoteAddr = "127.0.0.1:12345"
		request.Header.Set("X-Synon-User-Id", "attacker")
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("path=%s status=%d body=%s", path, response.Code, response.Body.String())
		}
		if response.Header().Get("X-Synon-Auth-Required") != "session" {
			t.Fatalf("path=%s auth-required=%q", path, response.Header().Get("X-Synon-Auth-Required"))
		}
		if response.Header().Get(webErrorCodeHeader) != webAuthRequiredCode ||
			!strings.Contains(response.Body.String(), `"code":"AUTH_SESSION_REQUIRED"`) {
			t.Fatalf("path=%s error-code=%q body=%s", path, response.Header().Get(webErrorCodeHeader), response.Body.String())
		}
	}
	querySpoof := httptest.NewRequest(http.MethodGet, "/api/me?userId=attacker", nil)
	querySpoof.RemoteAddr = "127.0.0.1:12345"
	querySpoofResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(querySpoofResponse, querySpoof)
	if querySpoofResponse.Code != http.StatusUnauthorized {
		t.Fatalf("query spoof status=%d body=%s", querySpoofResponse.Code, querySpoofResponse.Body.String())
	}

	health := httptest.NewRequest(http.MethodGet, "/health", nil)
	health.RemoteAddr = "203.0.113.8:12345"
	healthResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(healthResponse, health)
	if healthResponse.Code == http.StatusUnauthorized {
		t.Fatalf("health endpoint unexpectedly requires authentication: %s", healthResponse.Body.String())
	}

	currentUser := httptest.NewRequest(http.MethodGet, "/api/auth/user", nil)
	currentUser.RemoteAddr = "127.0.0.1:12345"
	currentUserResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(currentUserResponse, currentUser)
	if currentUserResponse.Code != http.StatusUnauthorized {
		t.Fatalf("auth status=%d body=%s", currentUserResponse.Code, currentUserResponse.Body.String())
	}
}

func TestP2WebAndSynonLinkSessionsHaveSeparateScopes(t *testing.T) {
	app := New(Options{SynonLinkAuth: SynonLinkAuthOptions{
		Username: "operator", Password: "test-secret-password", UserID: "user-1",
	}})
	webLogin := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"operator","password":"test-secret-password"}`))
	webLogin.RemoteAddr = "127.0.0.1:12345"
	webLoginResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(webLoginResponse, webLogin)
	if webLoginResponse.Code != http.StatusOK {
		t.Fatalf("web login status=%d body=%s", webLoginResponse.Code, webLoginResponse.Body.String())
	}
	webCookie := webLoginResponse.Result().Cookies()[0]
	linkWS := httptest.NewRequest(http.MethodGet, "/synon-link/ws?appSession="+webCookie.Value, nil)
	linkWS.RemoteAddr = "127.0.0.1:12345"
	linkWSResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(linkWSResponse, linkWS)
	if linkWSResponse.Code != http.StatusUnauthorized {
		t.Fatalf("web token reached Link websocket: status=%d body=%s", linkWSResponse.Code, linkWSResponse.Body.String())
	}

	linkLogin := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"operator","password":"test-secret-password"}`))
	linkLogin.RemoteAddr = "127.0.0.1:12345"
	linkLoginResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(linkLoginResponse, linkLogin)
	if linkLoginResponse.Code != http.StatusOK {
		t.Fatalf("Link login status=%d body=%s", linkLoginResponse.Code, linkLoginResponse.Body.String())
	}
	var linkPayload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(linkLoginResponse.Body.Bytes(), &linkPayload); err != nil {
		t.Fatal(err)
	}
	apiRequest := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	apiRequest.RemoteAddr = "127.0.0.1:12345"
	apiRequest.Header.Set("Authorization", "Bearer "+linkPayload.Token)
	apiResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(apiResponse, apiRequest)
	if apiResponse.Code != http.StatusUnauthorized {
		t.Fatalf("Link token reached Web API: status=%d body=%s", apiResponse.Code, apiResponse.Body.String())
	}
}

func TestP2LoginRateLimitIsSharedAndBoundedAcrossLoginEndpoints(t *testing.T) {
	app := New(Options{SynonLinkAuth: SynonLinkAuthOptions{
		Username: "operator", Password: "test-secret-password", UserID: "user-1",
	}})
	now := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	app.loginLimiter.now = func() time.Time { return now }
	paths := []string{"/login", "/api/auth/login", "/login", "/api/auth/login", "/login"}
	for index, path := range paths {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"username":"operator","password":"wrong"}`))
		request.RemoteAddr = "127.0.0.1:12345"
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("attempt=%d path=%s status=%d body=%s", index+1, path, response.Code, response.Body.String())
		}
	}
	blocked := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"operator","password":"test-secret-password"}`))
	blocked.RemoteAddr = "127.0.0.1:12345"
	blockedResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(blockedResponse, blocked)
	if blockedResponse.Code != http.StatusTooManyRequests || blockedResponse.Header().Get("Retry-After") == "" {
		t.Fatalf("blocked status=%d retry=%q body=%s", blockedResponse.Code, blockedResponse.Header().Get("Retry-After"), blockedResponse.Body.String())
	}

	now = now.Add(loginLockoutDuration + time.Second)
	recovered := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"operator","password":"test-secret-password"}`))
	recovered.RemoteAddr = "127.0.0.1:12345"
	recoveredResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(recoveredResponse, recovered)
	if recoveredResponse.Code != http.StatusOK {
		t.Fatalf("recovered status=%d body=%s", recoveredResponse.Code, recoveredResponse.Body.String())
	}
}

func TestP2CookieAuthenticatedMutationsRequireCSRF(t *testing.T) {
	app := New(Options{
		FileRoot: t.TempDir(),
		SynonLinkAuth: SynonLinkAuthOptions{
			Username: "operator", Password: "test-secret-password", UserID: "user-1",
		},
	})
	login := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"operator","password":"test-secret-password"}`))
	login.RemoteAddr = "127.0.0.1:12345"
	loginResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	var sessionCookie, csrfCookie *http.Cookie
	for _, cookie := range loginResponse.Result().Cookies() {
		switch cookie.Name {
		case webSessionCookieName:
			sessionCookie = cookie
		case webCSRFCookieName:
			csrfCookie = cookie
		}
	}
	if sessionCookie == nil || csrfCookie == nil || csrfCookie.HttpOnly || csrfCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("session=%#v csrf=%#v", sessionCookie, csrfCookie)
	}

	mutation := func(csrfHeader string, includeCSRFCookie bool) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPut, "/api/settings/client", strings.NewReader(`{"language":"zh-CN"}`))
		request.RemoteAddr = "127.0.0.1:12345"
		request.AddCookie(sessionCookie)
		if includeCSRFCookie {
			request.AddCookie(csrfCookie)
		}
		if csrfHeader != "" {
			request.Header.Set(webCSRFHeaderName, csrfHeader)
		}
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, request)
		return response
	}
	if response := mutation("", false); response.Code != http.StatusForbidden ||
		response.Header().Get(webErrorCodeHeader) != webCSRFInvalidCode ||
		!strings.Contains(response.Body.String(), `"code":"CSRF_INVALID"`) {
		t.Fatalf("missing CSRF status=%d error-code=%q body=%s", response.Code, response.Header().Get(webErrorCodeHeader), response.Body.String())
	}
	if response := mutation("wrong", true); response.Code != http.StatusForbidden ||
		response.Header().Get(webErrorCodeHeader) != webCSRFInvalidCode ||
		!strings.Contains(response.Body.String(), `"code":"CSRF_INVALID"`) {
		t.Fatalf("wrong CSRF status=%d error-code=%q body=%s", response.Code, response.Header().Get(webErrorCodeHeader), response.Body.String())
	}
	if response := mutation(csrfCookie.Value, true); response.Code != http.StatusOK {
		t.Fatalf("valid CSRF status=%d body=%s", response.Code, response.Body.String())
	}

	csrfRefresh := httptest.NewRequest(http.MethodGet, "/api/csrf", nil)
	csrfRefresh.RemoteAddr = "127.0.0.1:12345"
	csrfRefresh.AddCookie(sessionCookie)
	csrfRefreshResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(csrfRefreshResponse, csrfRefresh)
	if csrfRefreshResponse.Code != http.StatusNoContent || len(csrfRefreshResponse.Result().Cookies()) != 1 {
		t.Fatalf("CSRF refresh status=%d cookies=%#v", csrfRefreshResponse.Code, csrfRefreshResponse.Result().Cookies())
	}

	bearer, _, err := app.synonLinkAuth.LoginForScope("operator", "test-secret-password", synonSessionScopeWeb)
	if err != nil {
		t.Fatal(err)
	}
	bearerMutation := httptest.NewRequest(http.MethodPut, "/api/settings/client", strings.NewReader(`{"language":"en-US"}`))
	bearerMutation.RemoteAddr = "127.0.0.1:12345"
	bearerMutation.Header.Set("Authorization", "Bearer "+bearer)
	bearerResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(bearerResponse, bearerMutation)
	if bearerResponse.Code != http.StatusOK {
		t.Fatalf("bearer mutation status=%d body=%s", bearerResponse.Code, bearerResponse.Body.String())
	}
}

func TestBrowserOriginPolicyRejectsCrossOriginMutation(t *testing.T) {
	app := New(Options{})
	request := httptest.NewRequest(http.MethodPost, "/logout", strings.NewReader(`{}`))
	request.RemoteAddr = "127.0.0.1:12345"
	request.Host = "localhost:8765"
	request.Header.Set("Origin", "https://attacker.example")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestBrowserOriginPolicyKeepsSynonLinkExtensionLoginCompatible(t *testing.T) {
	app := New(Options{})
	request := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"operator","password":"secret"}`))
	request.RemoteAddr = "127.0.0.1:12345"
	request.Host = "localhost:8765"
	request.Header.Set("Origin", "chrome-extension://abcdefghijklmnop")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code == http.StatusForbidden {
		t.Fatalf("Synon Link login was rejected by Web origin policy: body=%s", response.Body.String())
	}
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("login status=%d body=%s", response.Code, response.Body.String())
	}
}
