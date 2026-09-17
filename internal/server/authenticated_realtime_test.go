package server

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
)

func TestP2AuthenticatedRealtimeRequiresWebIdentityAndResourceOwnership(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, project := range []workspace.CreateProjectInput{
		{ID: "owned-project", UserID: "user-1", Name: "Owned"},
		{ID: "foreign-project", UserID: "user-2", Name: "Foreign"},
	} {
		if _, err := store.CreateProject(project); err != nil {
			t.Fatal(err)
		}
	}
	for _, frame := range []workspace.CreateFrameInput{
		{ID: "owned-session", ProjectID: "owned-project", AgentName: "OPERON", Status: "running", ConversationType: "agent"},
		{ID: "foreign-session", ProjectID: "foreign-project", AgentName: "OPERON", Status: "running", ConversationType: "agent"},
	} {
		if _, err := store.CreateFrame(frame); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.AppendFrameEvent(workspace.FrameEventInput{
		FrameID: "owned-session", Type: "assistant_message", Payload: map[string]any{"text": "owner-only"},
	}); err != nil {
		t.Fatal(err)
	}
	app := New(Options{
		FileRoot:  root,
		Workspace: store,
		SynonLinkAuth: SynonLinkAuthOptions{
			Username: "operator", Password: "test-secret-password", UserID: "user-1", TTL: time.Hour,
		},
	})
	for _, sessionID := range []string{"owned-session", "foreign-session"} {
		if err := app.sessionStore.Upsert(sessionstore.Session{ID: sessionID, Title: sessionID, WorkDir: root}); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(app.Handler())
	t.Cleanup(server.Close)

	unauthenticated, err := server.Client().Get(server.URL + "/api/go/events/stream?frame_id=owned-session")
	if err != nil {
		t.Fatal(err)
	}
	unauthenticated.Body.Close()
	if unauthenticated.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated SSE status=%d", unauthenticated.StatusCode)
	}

	cookie := loginWebCookie(t, app.Handler())
	foreignRequest, err := http.NewRequest(http.MethodGet, server.URL+"/api/go/events/stream?frame_id=foreign-session", nil)
	if err != nil {
		t.Fatal(err)
	}
	foreignRequest.AddCookie(cookie)
	foreignResponse, err := server.Client().Do(foreignRequest)
	if err != nil {
		t.Fatal(err)
	}
	foreignResponse.Body.Close()
	if foreignResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign SSE status=%d", foreignResponse.StatusCode)
	}

	ownedRequest, err := http.NewRequest(http.MethodGet, server.URL+"/api/go/events/stream?frame_id=owned-session", nil)
	if err != nil {
		t.Fatal(err)
	}
	ownedRequest.AddCookie(cookie)
	ownedResponse, err := server.Client().Do(ownedRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer ownedResponse.Body.Close()
	if ownedResponse.StatusCode != http.StatusOK {
		t.Fatalf("owned SSE status=%d", ownedResponse.StatusCode)
	}
	reader := bufio.NewReader(ownedResponse.Body)
	var replay strings.Builder
	for replay.Len() < 4096 && !strings.Contains(replay.String(), "owner-only") {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read owned SSE: %v payload=%q", err, replay.String())
		}
		replay.WriteString(line)
	}
	if !strings.Contains(replay.String(), "owner-only") {
		t.Fatalf("owned SSE payload=%q", replay.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsBase := "ws" + strings.TrimPrefix(server.URL, "http")
	_, unauthenticatedWSResponse, err := websocket.Dial(ctx, wsBase+"/ws/owned-session", nil)
	if err == nil || unauthenticatedWSResponse == nil || unauthenticatedWSResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated websocket response=%#v error=%v", unauthenticatedWSResponse, err)
	}

	headers := http.Header{"Cookie": []string{cookie.String()}}
	_, foreignWSResponse, err := websocket.Dial(ctx, wsBase+"/ws/foreign-session", &websocket.DialOptions{HTTPHeader: headers})
	if err == nil || foreignWSResponse == nil || foreignWSResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign websocket response=%#v error=%v", foreignWSResponse, err)
	}

	_, ownedLegacyWSResponse, err := websocket.Dial(ctx, wsBase+"/ws/owned-session", &websocket.DialOptions{HTTPHeader: headers})
	if err == nil || ownedLegacyWSResponse == nil || ownedLegacyWSResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("owned legacy websocket response=%#v error=%v", ownedLegacyWSResponse, err)
	}

	eventConnection, eventResponse, err := websocket.Dial(ctx, wsBase+"/api/events/ws", &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatalf("event websocket response=%#v error=%v", eventResponse, err)
	}
	eventConnected := readWSMessage(t, ctx, eventConnection)
	if eventConnected["type"] != "connected" {
		t.Fatalf("event connected=%#v", eventConnected)
	}
	_ = eventConnection.Close(websocket.StatusNormalClosure, "done")

	attackerHeaders := headers.Clone()
	attackerHeaders.Set("Origin", "https://attacker.example")
	_, originResponse, err := websocket.Dial(ctx, wsBase+"/ws/owned-session", &websocket.DialOptions{
		HTTPHeader: attackerHeaders,
	})
	if err == nil || originResponse == nil || originResponse.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin websocket response=%#v error=%v", originResponse, err)
	}
}

func loginWebCookie(t *testing.T, handler http.Handler) *http.Cookie {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"operator","password":"test-secret-password"}`))
	request.RemoteAddr = "127.0.0.1:12345"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", response.Code, response.Body.String())
	}
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == webSessionCookieName {
			return cookie
		}
	}
	t.Fatal("web session cookie is missing")
	return nil
}
