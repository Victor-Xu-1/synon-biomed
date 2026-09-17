package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestWebAccountStorePersistsScryptAccountsAndAuthenticatesAfterReopen(t *testing.T) {
	root := t.TempDir()
	store, err := openWebAccountStore(root, "operator")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.Register("Researcher", "USER@example.org", "strong-pass-1")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "webui-accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "strong-pass-1") || !strings.Contains(string(raw), "passwordHash") {
		t.Fatalf("account file leaks password or lacks hash: %s", raw)
	}
	info, err := os.Stat(filepath.Join(root, "webui-accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("account mode = %o", info.Mode().Perm())
	}
	reopened, err := openWebAccountStore(root, "operator")
	if err != nil {
		t.Fatal(err)
	}
	authenticated, found, err := reopened.Authenticate("researcher", "strong-pass-1")
	if err != nil || !found || authenticated.ID != user.ID || authenticated.Email != "USER@example.org" {
		t.Fatalf("authenticated=%#v found=%t err=%v", authenticated, found, err)
	}
	if _, found, err := reopened.Authenticate("researcher", "wrong-pass"); err != nil || found {
		t.Fatalf("wrong password found=%t err=%v", found, err)
	}
	if _, err := reopened.Register("RESEARCHER", "", "another-pass"); registrationErrorCode(err) != "USERNAME_EXISTS" {
		t.Fatalf("duplicate error = %v", err)
	}
	if _, err := reopened.Register("Another", "user@EXAMPLE.org", "another-pass"); registrationErrorCode(err) != "EMAIL_EXISTS" {
		t.Fatalf("email duplicate error = %v", err)
	}
}

func TestWebAccountStoreSerializesConcurrentRegistration(t *testing.T) {
	root := t.TempDir()
	store, err := openWebAccountStore(root, "operator")
	if err != nil {
		t.Fatal(err)
	}
	const accountCount = 6
	var wg sync.WaitGroup
	errs := make(chan error, accountCount)
	for index := 0; index < accountCount; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, err := store.Register(
				"user-"+string(rune('a'+index)),
				"",
				"password-"+string(rune('a'+index)),
			)
			errs <- err
		}(index)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := openWebAccountStore(root, "operator")
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.accounts) != accountCount {
		t.Fatalf("accounts = %d", len(reopened.accounts))
	}
}

func TestWebRegistrationAPIIsDurableAndDoesNotExpandSynonLinkLogin(t *testing.T) {
	root := t.TempDir()
	options := Options{
		FileRoot: root,
		SynonLinkAuth: SynonLinkAuthOptions{
			Username: "operator", Password: "test-secret-password", UserID: "bootstrap-user",
		},
	}
	app := New(options)
	register := httptest.NewRequest(
		http.MethodPost,
		"/api/auth/register",
		strings.NewReader(`{"name":"Scientist","email":"scientist@example.org","password":"password-123","remember":true}`),
	)
	register.RemoteAddr = "127.0.0.1:12345"
	register.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, register)
	if response.Code != http.StatusOK {
		t.Fatalf("register status=%d body=%s", response.Code, response.Body.String())
	}
	var userBody struct {
		Success bool `json:"success"`
		User    struct {
			ID          string `json:"id"`
			Username    string `json:"username"`
			DisplayName string `json:"displayName"`
			Email       string `json:"email"`
		} `json:"user"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &userBody); err != nil {
		t.Fatal(err)
	}
	if !userBody.Success || userBody.User.ID == "" || userBody.User.Username != "Scientist" ||
		userBody.User.DisplayName != "Scientist" || userBody.User.Email != "scientist@example.org" {
		t.Fatalf("register body = %#v", userBody)
	}
	var sessionCookie *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == webSessionCookieName {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil || !sessionCookie.HttpOnly || sessionCookie.MaxAge <= 0 {
		t.Fatalf("session cookie = %#v", sessionCookie)
	}

	current := httptest.NewRequest(http.MethodGet, "/api/auth/user", nil)
	current.RemoteAddr = "127.0.0.1:12345"
	current.AddCookie(sessionCookie)
	currentResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(currentResponse, current)
	if currentResponse.Code != http.StatusOK || !strings.Contains(currentResponse.Body.String(), "scientist@example.org") {
		t.Fatalf("current user status=%d body=%s", currentResponse.Code, currentResponse.Body.String())
	}

	restarted := New(options)
	login := httptest.NewRequest(
		http.MethodPost,
		"/login",
		strings.NewReader(`{"username":"scientist","password":"password-123"}`),
	)
	login.RemoteAddr = "127.0.0.1:12345"
	loginResponse := httptest.NewRecorder()
	restarted.Handler().ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("restarted login status=%d body=%s", loginResponse.Code, loginResponse.Body.String())
	}

	linkLogin := httptest.NewRequest(
		http.MethodPost,
		"/api/auth/login",
		strings.NewReader(`{"username":"scientist","password":"password-123"}`),
	)
	linkLogin.RemoteAddr = "127.0.0.1:12345"
	linkResponse := httptest.NewRecorder()
	restarted.Handler().ServeHTTP(linkResponse, linkLogin)
	if linkResponse.Code != http.StatusUnauthorized {
		t.Fatalf("registered account expanded Synon Link login: status=%d body=%s", linkResponse.Code, linkResponse.Body.String())
	}
}

func TestWebRegistrationRejectsDuplicateInvalidCrossOriginAndCorruptStore(t *testing.T) {
	root := t.TempDir()
	options := Options{
		FileRoot:      root,
		SynonLinkAuth: SynonLinkAuthOptions{Username: "operator", Password: "test-secret-password"},
	}
	app := New(options)
	request := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/register", strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:12345"
		res := httptest.NewRecorder()
		app.Handler().ServeHTTP(res, req)
		return res
	}
	if response := request(`{"name":"operator","password":"password-123"}`); response.Code != http.StatusConflict {
		t.Fatalf("bootstrap duplicate status=%d body=%s", response.Code, response.Body.String())
	}
	if response := request(`{"name":"Another","email":"not-an-email","password":"password-123"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid email status=%d body=%s", response.Code, response.Body.String())
	}
	crossOrigin := httptest.NewRequest(http.MethodPost, "/api/auth/register", strings.NewReader(`{"name":"Another","password":"password-123"}`))
	crossOrigin.RemoteAddr = "127.0.0.1:12345"
	crossOrigin.Host = "localhost:8765"
	crossOrigin.Header.Set("Origin", "https://attacker.example")
	crossResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(crossResponse, crossOrigin)
	if crossResponse.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status=%d body=%s", crossResponse.Code, crossResponse.Body.String())
	}
	remoteApp := New(Options{
		FileRoot:      t.TempDir(),
		SynonLinkAuth: options.SynonLinkAuth,
		WebAuth:       WebAuthOptions{PublicBaseURL: "https://biomed.example"},
	})
	remote := httptest.NewRequest(http.MethodPost, "https://biomed.example/api/auth/register", strings.NewReader(`{"name":"Remote","password":"password-123"}`))
	remote.RemoteAddr = "203.0.113.8:12345"
	remote.Header.Set("Origin", "https://biomed.example")
	remoteResponse := httptest.NewRecorder()
	remoteApp.Handler().ServeHTTP(remoteResponse, remote)
	if remoteResponse.Code != http.StatusForbidden {
		t.Fatalf("remote registration status=%d body=%s", remoteResponse.Code, remoteResponse.Body.String())
	}

	if err := os.WriteFile(filepath.Join(root, "webui-accounts.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	corrupt := New(options)
	if response := postWebLoginForRegistrationTest(corrupt, "operator", "test-secret-password"); response.Code != http.StatusOK {
		t.Fatalf("bootstrap login with corrupt account store status=%d body=%s", response.Code, response.Body.String())
	}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/register", strings.NewReader(`{"name":"New User","password":"password-123"}`))
	req.RemoteAddr = "127.0.0.1:12345"
	res := httptest.NewRecorder()
	corrupt.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("corrupt store registration status=%d body=%s", res.Code, res.Body.String())
	}
}

func postWebLoginForRegistrationTest(app *Server, username, password string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(
		http.MethodPost,
		"/login",
		strings.NewReader(`{"username":"`+username+`","password":"`+password+`"}`),
	)
	request.RemoteAddr = "127.0.0.1:12345"
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	return response
}

func registrationErrorCode(err error) string {
	var validation *webAccountValidationError
	if errors.As(err, &validation) {
		return validation.code
	}
	return ""
}
