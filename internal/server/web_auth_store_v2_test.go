package server

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWebAccountSecurityRevokesOtherSessionsDurablyWithPerSessionCSRF(t *testing.T) {
	root := t.TempDir()
	options := Options{
		FileRoot: root,
		SynonLinkAuth: SynonLinkAuthOptions{
			Username: "operator", Password: "test-secret-password", UserID: "account-1",
		},
	}
	app := New(options)
	firstSession, firstCSRF := loginWebSessionCookies(t, app)
	secondSession, secondCSRF := loginWebSessionCookies(t, app)

	wrongCSRF := httptest.NewRequest(http.MethodPost, "/api/account/security/sessions/revoke-others", strings.NewReader("{}"))
	wrongCSRF.RemoteAddr = "127.0.0.1:12345"
	wrongCSRF.AddCookie(secondSession)
	wrongCSRF.AddCookie(firstCSRF)
	wrongCSRF.Header.Set(webCSRFHeaderName, firstCSRF.Value)
	wrongResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(wrongResponse, wrongCSRF)
	if wrongResponse.Code != http.StatusForbidden {
		t.Fatalf("cross-session CSRF status=%d body=%s", wrongResponse.Code, wrongResponse.Body.String())
	}

	revoke := httptest.NewRequest(http.MethodPost, "/api/account/security/sessions/revoke-others", strings.NewReader("{}"))
	revoke.RemoteAddr = "127.0.0.1:12345"
	revoke.AddCookie(secondSession)
	revoke.AddCookie(secondCSRF)
	revoke.Header.Set(webCSRFHeaderName, secondCSRF.Value)
	revokeResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(revokeResponse, revoke)
	if revokeResponse.Code != http.StatusOK || !strings.Contains(revokeResponse.Body.String(), `"revoked":1`) {
		t.Fatalf("revoke status=%d body=%s", revokeResponse.Code, revokeResponse.Body.String())
	}

	restarted := New(options)
	assertWebSessionStatus(t, restarted, firstSession, http.StatusUnauthorized)
	assertWebSessionStatus(t, restarted, secondSession, http.StatusOK)
}

func TestWebAccountSecurityDescribesTrustedLoopbackModeWithoutManagedSessions(t *testing.T) {
	app := New(Options{FileRoot: t.TempDir()})
	request := httptest.NewRequest(http.MethodGet, "/api/account/security", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("security status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Success  bool             `json:"success"`
		Managed  bool             `json:"managed"`
		Sessions []webSessionView `json:"sessions"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Success || payload.Managed || len(payload.Sessions) != 0 {
		t.Fatalf("security payload=%#v", payload)
	}
}

func loginWebSessionCookies(t *testing.T, app *Server) (*http.Cookie, *http.Cookie) {
	t.Helper()
	request := httptest.NewRequest(
		http.MethodPost,
		"/login",
		strings.NewReader(`{"username":"operator","password":"test-secret-password","remember":true}`),
	)
	request.RemoteAddr = "127.0.0.1:12345"
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", response.Code, response.Body.String())
	}
	var session, csrf *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		switch cookie.Name {
		case webSessionCookieName:
			session = cookie
		case webCSRFCookieName:
			csrf = cookie
		}
	}
	if session == nil || csrf == nil {
		t.Fatalf("login cookies = %#v", response.Result().Cookies())
	}
	return session, csrf
}

func assertWebSessionStatus(t *testing.T, app *Server, session *http.Cookie, expected int) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/auth/user", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	request.AddCookie(session)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != expected {
		t.Fatalf("session status=%d want=%d body=%s", response.Code, expected, response.Body.String())
	}
}

func TestWebAccountStoreMigratesV1WithRecoverableBackup(t *testing.T) {
	root := t.TempDir()
	salt := []byte("0123456789abcdef")
	hash, err := deriveWebAccountPassword("password-123", salt)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := "2026-08-30T08:00:00Z"
	v1 := map[string]any{
		"version": 1,
		"accounts": []map[string]any{{
			"id": "account-1", "username": "Scientist", "displayName": "Scientist",
			"email": "scientist@example.org", "normalizedUsername": "scientist",
			"normalizedEmail": "scientist@example.org",
			"passwordSalt":    base64.RawURLEncoding.EncodeToString(salt),
			"passwordHash":    base64.RawURLEncoding.EncodeToString(hash),
			"createdAt":       createdAt,
		}},
	}
	raw, err := json.Marshal(v1)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "webui-accounts.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := openWebAccountStore(root, "operator")
	if err != nil {
		t.Fatal(err)
	}
	if len(store.accounts) != 1 || store.accounts[0].Status != "active" ||
		store.accounts[0].UpdatedAt != createdAt {
		t.Fatalf("migrated accounts = %#v", store.accounts)
	}
	migratedRaw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var migrated webAccountsDocument
	if err := json.Unmarshal(migratedRaw, &migrated); err != nil {
		t.Fatal(err)
	}
	if migrated.Version != webAccountsVersion || migrated.Identities == nil {
		t.Fatalf("migrated document = %#v", migrated)
	}
	backupPath := filepath.Join(root, "webui-accounts.v1.bak")
	backupRaw, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(backupRaw) != string(raw) {
		t.Fatal("v1 backup did not preserve the original bytes")
	}
	info, err := os.Stat(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %v", info.Mode().Perm())
	}
}

func TestWebExternalIdentityDoesNotSilentlyLinkByEmail(t *testing.T) {
	store, err := openWebAccountStore(t.TempDir(), "operator")
	if err != nil {
		t.Fatal(err)
	}
	local, err := store.Register("Scientist", "scientist@example.org", "password-123")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.ResolveExternalIdentity(webExternalIdentityClaims{
		Provider: "google", Issuer: webGoogleIssuerURL, Subject: "google-subject-1",
		Email: "scientist@example.org", EmailVerified: true, DisplayName: "Scientist", RequireVerifiedEmail: true,
	})
	var validation *webAccountValidationError
	if !errors.As(err, &validation) || validation.code != "EXTERNAL_EMAIL_LINK_REQUIRED" {
		t.Fatalf("email collision error = %v", err)
	}

	google, created, err := store.ResolveExternalIdentity(webExternalIdentityClaims{
		Provider: "google", Issuer: webGoogleIssuerURL, Subject: "google-subject-2",
		Email: "google@example.org", EmailVerified: true, DisplayName: "Researcher", RequireVerifiedEmail: true,
	})
	if err != nil || !created || google.ID == "" || google.Provider != "google" {
		t.Fatalf("created=%v user=%#v err=%v", created, google, err)
	}
	again, created, err := store.ResolveExternalIdentity(webExternalIdentityClaims{
		Provider: "google", Issuer: webGoogleIssuerURL, Subject: "google-subject-2",
		Email: "google@example.org", EmailVerified: true, DisplayName: "Researcher", RequireVerifiedEmail: true,
	})
	if err != nil || created || again.ID != google.ID {
		t.Fatalf("repeat created=%v user=%#v err=%v", created, again, err)
	}
	if methods := store.LoginMethods(local.ID); len(methods) != 1 || methods[0] != "local" {
		t.Fatalf("local methods = %#v", methods)
	}
	if methods := store.LoginMethods(google.ID); len(methods) != 1 || methods[0] != "google" {
		t.Fatalf("google methods = %#v", methods)
	}
}

func TestWebWeChatIdentityUsesUnionIDWithoutInventingAnEmail(t *testing.T) {
	store, err := openWebAccountStore(t.TempDir(), "operator")
	if err != nil {
		t.Fatal(err)
	}
	claims := webExternalIdentityClaims{
		Provider:    webWeChatProviderID,
		Issuer:      "https://open.weixin.qq.com/app/wx-test",
		Subject:     "stable-union-id",
		DisplayName: "微信研究员",
	}
	createdUser, created, err := store.ResolveExternalIdentity(claims)
	if err != nil || !created || createdUser.Provider != webWeChatProviderID || createdUser.Email != "" {
		t.Fatalf("created=%v user=%#v err=%v", created, createdUser, err)
	}
	again, created, err := store.ResolveExternalIdentity(claims)
	if err != nil || created || again.ID != createdUser.ID || again.Email != "" {
		t.Fatalf("repeat created=%v user=%#v err=%v", created, again, err)
	}
	if methods := store.LoginMethods(createdUser.ID); len(methods) != 1 || methods[0] != webWeChatProviderID {
		t.Fatalf("WeChat methods = %#v", methods)
	}
}

func TestWebSessionStoreUsesIndependentHashedCSRFAndDurableRevocation(t *testing.T) {
	root := t.TempDir()
	store, err := openWebSessionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	store.now = func() time.Time { return now }
	firstToken, firstCSRF, first, err := store.Create(
		"account-1", "local", true,
		webSessionMetadata{UserAgent: "Chrome · Windows", UserAgentHash: webSecretHash("ua-1"), NetworkClass: "loopback"},
	)
	if err != nil {
		t.Fatal(err)
	}
	secondToken, secondCSRF, second, err := store.Create(
		"account-1", "google", false,
		webSessionMetadata{UserAgent: "Firefox · Linux", UserAgentHash: webSecretHash("ua-2"), NetworkClass: "private"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if firstToken == secondToken || firstCSRF == secondCSRF {
		t.Fatal("session or CSRF secrets were reused")
	}
	if valid, err := store.ValidateCSRF(firstToken, firstCSRF); err != nil || !valid {
		t.Fatalf("first CSRF valid=%v err=%v", valid, err)
	}
	if valid, err := store.ValidateCSRF(firstToken, secondCSRF); err != nil || valid {
		t.Fatalf("cross-session CSRF valid=%v err=%v", valid, err)
	}
	persisted, err := os.ReadFile(filepath.Join(root, "webui-sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), firstToken) || strings.Contains(string(persisted), firstCSRF) {
		t.Fatal("raw Web session or CSRF secret was persisted")
	}
	views, err := store.List("account-1", secondToken)
	if err != nil || len(views) != 2 || !views[0].Current || views[0].ID != webSessionDeviceID(second) {
		t.Fatalf("session views = %#v err=%v", views, err)
	}
	revoked, err := store.RevokeOthers("account-1", secondToken)
	if err != nil || revoked != 1 {
		t.Fatalf("revoke others = %d err=%v", revoked, err)
	}
	if _, ok, err := store.Authenticate(firstToken, false); err != nil || ok {
		t.Fatalf("revoked first session remained valid: ok=%v err=%v", ok, err)
	}
	if _, ok, err := store.Authenticate(secondToken, false); err != nil || !ok {
		t.Fatalf("current session missing: ok=%v err=%v", ok, err)
	}

	reopened, err := openWebSessionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	reopened.now = func() time.Time { return now }
	if _, ok, err := reopened.Authenticate(secondToken, false); err != nil || !ok {
		t.Fatalf("durable session missing: ok=%v err=%v", ok, err)
	}
	now = now.Add(webSessionIdleTTL + time.Minute)
	if _, ok, err := reopened.Authenticate(secondToken, false); err != nil || ok {
		t.Fatalf("idle-expired session remained valid: ok=%v err=%v", ok, err)
	}
	if first.ID == "" || second.ID == "" {
		t.Fatal("session IDs were not generated")
	}
}

func TestWebSessionAuthenticateDoesNotReallocateUnchangedSessionState(t *testing.T) {
	store, err := openWebSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	store.now = func() time.Time { return now }
	token, _, _, err := store.Create(
		"account-stable", "local", false,
		webSessionMetadata{UserAgent: "Chrome · Windows", UserAgentHash: webSecretHash("stable-ua")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(store.sessions))
	}
	initialSession := &store.sessions[0]
	initialLastSeen := store.sessions[0].LastSeenAt

	for _, touch := range []bool{false, true} {
		if _, ok, authErr := store.Authenticate(token, touch); authErr != nil || !ok {
			t.Fatalf("Authenticate(touch=%v) ok=%v err=%v", touch, ok, authErr)
		}
		if &store.sessions[0] != initialSession {
			t.Fatalf("Authenticate(touch=%v) reallocated unchanged session storage", touch)
		}
		if store.sessions[0].LastSeenAt != initialLastSeen {
			t.Fatalf("Authenticate(touch=%v) changed LastSeenAt before touch interval", touch)
		}
	}
}

func TestWebSessionTokenIndexTracksExpiryAndRevocation(t *testing.T) {
	store, err := openWebSessionStore("")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	store.now = func() time.Time { return now }
	firstToken, _, first, err := store.Create("account-index", "local", false, webSessionMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	secondToken, _, _, err := store.Create("account-index", "local", false, webSessionMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.tokenIndex) != 2 {
		t.Fatalf("token index entries=%d want=2", len(store.tokenIndex))
	}
	if revoked, _, revokeErr := store.Revoke("account-index", first.ID, secondToken); revokeErr != nil || !revoked {
		t.Fatalf("revoked=%t err=%v", revoked, revokeErr)
	}
	if _, found := store.tokenIndex[webSecretHash(firstToken)]; found {
		t.Fatal("revoked token remained indexed")
	}
	if _, ok, authErr := store.Authenticate(secondToken, false); authErr != nil || !ok {
		t.Fatalf("second token ok=%t err=%v", ok, authErr)
	}
	now = now.Add(webSessionIdleTTL + time.Minute)
	if _, ok, authErr := store.Authenticate(secondToken, false); authErr != nil || ok {
		t.Fatalf("expired token ok=%t err=%v", ok, authErr)
	}
	if len(store.tokenIndex) != 0 {
		t.Fatalf("expired token index entries=%d want=0", len(store.tokenIndex))
	}
}

func BenchmarkWebSessionAuthenticateIndexed(b *testing.B) {
	now := time.Now().UTC()
	timestamps := storedWebSession{
		CreatedAt:     now.Format(time.RFC3339Nano),
		LastSeenAt:    now.Format(time.RFC3339Nano),
		IdleExpiresAt: now.Add(time.Hour).Format(time.RFC3339Nano),
		ExpiresAt:     now.Add(2 * time.Hour).Format(time.RFC3339Nano),
	}
	store := &webSessionStore{
		now:        func() time.Time { return now },
		random:     rand.Reader,
		tokenIndex: map[string]int{},
	}
	token := "benchmark-session-token"
	for index := 0; index < 16_384; index++ {
		candidate := fmt.Sprintf("benchmark-session-%d", index)
		session := timestamps
		session.TokenHash = webSecretHash(candidate)
		store.sessions = append(store.sessions, session)
	}
	target := timestamps
	target.TokenHash = webSecretHash(token)
	store.sessions = append(store.sessions, target)
	store.rebuildTokenIndexLocked()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, ok, err := store.Authenticate(token, false); err != nil || !ok {
			b.Fatalf("ok=%t err=%v", ok, err)
		}
	}
}
