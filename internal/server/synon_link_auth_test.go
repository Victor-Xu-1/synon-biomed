package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	workspace "synon-go/internal/persistence/workspace"
)

func TestSynonLinkExtensionLoginAndCompatibilityWebSocket(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	authOptions := SynonLinkAuthOptions{
		Username: "local-user",
		Password: "correct horse battery staple",
		UserID:   "local",
		TTL:      time.Hour,
	}
	app := New(Options{
		FileRoot: root, Workspace: store, SynonLinkAuth: authOptions,
	})
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	wrong := postSynonLinkLogin(t, app.Handler(), server.URL, "local-user", "wrong-password")
	if wrong.Code != http.StatusUnauthorized {
		t.Fatalf("wrong login status = %d body=%s", wrong.Code, wrong.Body.String())
	}
	remoteBody := bytes.NewBufferString(`{"username":"local-user","password":"correct horse battery staple"}`)
	remoteRequest := httptest.NewRequest(http.MethodPost, server.URL+"/api/auth/login", remoteBody)
	remoteRequest.RemoteAddr = "198.51.100.10:12345"
	remoteRequest.Header.Set("Content-Type", "application/json")
	remoteResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(remoteResponse, remoteRequest)
	if remoteResponse.Code != http.StatusForbidden {
		t.Fatalf("remote login status = %d body=%s", remoteResponse.Code, remoteResponse.Body.String())
	}
	login := postSynonLinkLogin(t, app.Handler(), server.URL, "local-user", "correct horse battery staple")
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d body=%s", login.Code, login.Body.String())
	}
	var loginBody struct {
		Token string `json:"token"`
		User  struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"user"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &loginBody); err != nil {
		t.Fatal(err)
	}
	if len(loginBody.Token) < 64 || loginBody.User.ID != "local" || loginBody.User.Username != "local-user" {
		t.Fatalf("login body = %#v", loginBody)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/synon-link/ws?appSession=" + loginBody.Token + "&clientId=chrome-real"
	conn, response, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("compat WebSocket response=%#v error=%v", response, err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")
	connected := readWSMessage(t, ctx, conn)
	if connected["type"] != "connected" || connected["userId"] != "local" {
		t.Fatalf("connected = %#v", connected)
	}
	writeWSMessage(t, ctx, conn, map[string]any{
		"type": "hello", "clientId": "chrome-real", "name": "Synon Link",
		"browser": "Chrome", "extensionVersion": "0.6.10",
		"capabilities":     []string{"tabs", "humanPageRead"},
		"supportedActions": []string{"get_active_tab", "read_page"},
	})
	ack := readWSMessage(t, ctx, conn)
	if ack["type"] != "hello_ack" {
		t.Fatalf("hello ack = %#v", ack)
	}
	clients := app.synonLink.ListClients("local")
	if len(clients) != 1 || clients[0].ID != "chrome-real" || clients[0].Version != "0.6.10" {
		t.Fatalf("registered clients = %#v", clients)
	}
	logoutRequest := httptest.NewRequest(http.MethodPost, server.URL+"/api/auth/logout", bytes.NewReader([]byte(`{}`)))
	logoutRequest.Header.Set("Authorization", "Bearer "+loginBody.Token)
	logoutResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(logoutResponse, logoutRequest)
	if logoutResponse.Code != http.StatusOK || !strings.Contains(logoutResponse.Body.String(), `"tokens_deleted":true`) {
		t.Fatalf("Synon Link logout status=%d body=%s", logoutResponse.Code, logoutResponse.Body.String())
	}
	revokedURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/synon-link/ws?appSession=" + loginBody.Token + "&clientId=revoked"
	revokedConn, revokedResponse, err := websocket.Dial(ctx, revokedURL, nil)
	if revokedConn != nil {
		_ = revokedConn.CloseNow()
	}
	if err == nil || revokedResponse == nil || revokedResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked token response=%#v error=%v", revokedResponse, err)
	}
	restarted := New(Options{FileRoot: root, Workspace: store, SynonLinkAuth: authOptions})
	restartedServer := httptest.NewServer(restarted.Handler())
	defer restartedServer.Close()
	restartedURL := "ws" + strings.TrimPrefix(restartedServer.URL, "http") + "/synon-link/ws?appSession=" + loginBody.Token + "&clientId=restarted"
	restartedConn, restartedResponse, err := websocket.Dial(ctx, restartedURL, nil)
	if restartedConn != nil {
		_ = restartedConn.CloseNow()
	}
	if err == nil || restartedResponse == nil || restartedResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("restarted revoked token response=%#v error=%v", restartedResponse, err)
	}

	tamperedURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/synon-link/ws?appSession=" + loginBody.Token + "x&clientId=bad"
	badConn, badResponse, err := websocket.Dial(ctx, tamperedURL, nil)
	if badConn != nil {
		_ = badConn.CloseNow()
	}
	if err == nil || badResponse == nil || badResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("tampered token response=%#v error=%v", badResponse, err)
	}
}

func TestSynonLinkSessionTokenExpires(t *testing.T) {
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	auth := newSynonLinkAuthenticator(SynonLinkAuthOptions{
		Username: "local", Password: "secret", UserID: "local", TTL: time.Minute,
	})
	auth.now = func() time.Time { return now }
	token, user, err := auth.Login("local", "secret")
	if err != nil || user.ID != "local" {
		t.Fatalf("Login() token=%q user=%#v err=%v", token, user, err)
	}
	if got, ok := auth.Authenticate(token); !ok || got.ID != "local" {
		t.Fatalf("Authenticate() = %#v, %v", got, ok)
	}
	restarted := newSynonLinkAuthenticator(SynonLinkAuthOptions{
		Username: "local", Password: "secret", UserID: "local", TTL: time.Minute,
	})
	restarted.now = func() time.Time { return now }
	if got, ok := restarted.Authenticate(token); !ok || got.ID != "local" {
		t.Fatalf("restarted Authenticate() = %#v, %v", got, ok)
	}
	now = now.Add(time.Minute + time.Nanosecond)
	if _, ok := auth.Authenticate(token); ok {
		t.Fatal("expired Synon Link session token was accepted")
	}
}

func TestSynonLinkDefaultSessionOutlivesTwentyFourHourRun(t *testing.T) {
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	auth := newSynonLinkAuthenticator(SynonLinkAuthOptions{
		Username: "local", Password: "secret", UserID: "local",
	})
	auth.now = func() time.Time { return now }
	token, _, err := auth.LoginForScope("local", "secret", synonSessionScopeWeb)
	if err != nil {
		t.Fatal(err)
	}

	now = now.Add(24*time.Hour + time.Minute)
	if _, ok := auth.AuthenticateScope(token, synonSessionScopeWeb); !ok {
		t.Fatal("default web session expired during a 24-hour run")
	}
	now = now.Add(24 * time.Hour)
	if _, ok := auth.AuthenticateScope(token, synonSessionScopeWeb); ok {
		t.Fatal("default web session remained valid beyond its 48-hour bound")
	}
}

func postSynonLinkLogin(t *testing.T, handler http.Handler, baseURL, username, password string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{"username": username, "password": password})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, baseURL+"/api/auth/login", bytes.NewReader(body))
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	return recorder
}
