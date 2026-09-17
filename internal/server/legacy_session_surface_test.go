package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
)

const legacySurfaceSessionID = "legacy-session"

type legacySurfaceFixture struct {
	server *Server
	token  string
}

func newLegacySurfaceFixture(t *testing.T, authenticationEnabled bool) legacySurfaceFixture {
	t.Helper()
	root := t.TempDir()
	workspaceStore, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaceStore.Close() })
	for _, project := range []workspace.CreateProjectInput{
		{ID: "owned-project", UserID: "owner-a", Name: "Owned"},
		{ID: "foreign-project", UserID: "owner-b", Name: "Foreign"},
	} {
		if _, err := workspaceStore.CreateProject(project); err != nil {
			t.Fatal(err)
		}
	}
	for _, frame := range []workspace.CreateFrameInput{
		{ID: "owned-frame", ProjectID: "owned-project", AgentName: "OPERON", Status: "queued", ConversationType: "agent"},
		{ID: "foreign-frame", ProjectID: "foreign-project", AgentName: "OPERON", Status: "queued", ConversationType: "agent"},
		{ID: "frame-id-collision", ProjectID: "owned-project", AgentName: "OPERON", Status: "queued", ConversationType: "agent"},
	} {
		if _, err := workspaceStore.CreateFrame(frame); err != nil {
			t.Fatal(err)
		}
	}

	options := Options{FileRoot: root, Workspace: workspaceStore}
	if authenticationEnabled {
		options.SynonLinkAuth = SynonLinkAuthOptions{
			Username: "operator", Password: "test-secret-password", UserID: "owner-a", TTL: time.Hour,
		}
	}
	app := New(options)
	now := time.Now().UTC()
	if err := app.sessionStore.Upsert(sessionstore.Session{
		ID: legacySurfaceSessionID, Title: "Legacy session", LastRole: "user", MessageCount: 1,
		LastUserMessageAt: now, CreatedAt: now, UpdatedAt: now,
		Runner: &sessionstore.Runner{
			RunnerID: "runner-a", Status: "running", Attempt: 3,
			ClaimedAt: now, LastHeartbeatAt: now, ExpiresAt: now.Add(time.Hour),
		},
		Orchestration: map[string]any{"sessionConfig": map[string]any{"model": "original-model"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.eventJournal.Append(legacySurfaceSessionID, map[string]any{
		"type": "user_message", "role": "user", "text": "original message",
	}, eventjournal.Metadata{ClientMessageID: "legacy-seed"}); err != nil {
		t.Fatal(err)
	}
	for _, sessionID := range []string{"owned-frame", "foreign-frame", "frame-id-collision"} {
		if err := app.sessionStore.Upsert(sessionstore.Session{ID: sessionID, Title: sessionID}); err != nil {
			t.Fatal(err)
		}
	}

	fixture := legacySurfaceFixture{server: app}
	if authenticationEnabled {
		fixture.token, _, err = app.synonLinkAuth.LoginForScope(
			"operator", "test-secret-password", synonSessionScopeWeb,
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	return fixture
}

func TestLegacySessionSurfacesHideEveryAuthenticatedIdentityShape(t *testing.T) {
	fixture := newLegacySurfaceFixture(t, true)
	server := httptest.NewServer(fixture.server.Handler())
	t.Cleanup(server.Close)
	header := http.Header{"Authorization": []string{"Bearer " + fixture.token}}

	for _, sessionID := range []string{
		legacySurfaceSessionID,
		"owned-frame",
		"foreign-frame",
		"frame-id-collision",
		"missing-session",
	} {
		t.Run(sessionID, func(t *testing.T) {
			config := legacySurfaceRequest(
				t, fixture, http.MethodGet, "/api/go/sessions/"+sessionID+"/config", "", "127.0.0.1:12345",
			)
			if config.Code != http.StatusNotFound {
				t.Fatalf("config status=%d body=%s", config.Code, config.Body.String())
			}

			conn, response, err := websocket.Dial(
				context.Background(), "ws"+strings.TrimPrefix(server.URL, "http")+"/ws/"+sessionID,
				&websocket.DialOptions{HTTPHeader: header},
			)
			if conn != nil {
				_ = conn.Close(websocket.StatusNormalClosure, "test complete")
			}
			if err == nil || response == nil || response.StatusCode != http.StatusNotFound {
				status := 0
				if response != nil {
					status = response.StatusCode
				}
				t.Fatalf("websocket err=%v status=%d", err, status)
			}
		})
	}

	catalog := legacySurfaceRequest(t, fixture, http.MethodGet, "/api/tools", "", "127.0.0.1:12345")
	if catalog.Code != http.StatusOK {
		t.Fatalf("tool catalog status=%d body=%s", catalog.Code, catalog.Body.String())
	}
	events := legacySurfaceRequest(t, fixture, http.MethodGet, "/api/events?frame_id=owned-frame&after_sequence=0", "", "127.0.0.1:12345")
	if events.Code != http.StatusOK {
		t.Fatalf("canonical events status=%d body=%s", events.Code, events.Body.String())
	}
}

func legacySurfaceRequest(t *testing.T, fixture legacySurfaceFixture, method, path, body, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.RemoteAddr = remoteAddr
	request.Host = "localhost"
	request.Header.Set("Content-Type", "application/json")
	if fixture.token != "" {
		request.Header.Set("Authorization", "Bearer "+fixture.token)
	}
	response := httptest.NewRecorder()
	fixture.server.Handler().ServeHTTP(response, request)
	return response
}

func legacySurfaceStateHash(t *testing.T, fixture legacySurfaceFixture) string {
	t.Helper()
	session, found, err := fixture.server.sessionStore.Get(legacySurfaceSessionID)
	if err != nil || !found {
		t.Fatalf("Get(session) found=%t err=%v", found, err)
	}
	entries, err := fixture.server.eventJournal.ReadAll(legacySurfaceSessionID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"session": session, "entries": entries})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func TestLegacySessionSurfacesQuarantineAuthenticatedRequestsWithoutStateChange(t *testing.T) {
	fixture := newLegacySurfaceFixture(t, true)
	before := legacySurfaceStateHash(t, fixture)

	requests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "config read", method: http.MethodGet, path: "/api/go/sessions/legacy-session/config"},
		{
			name: "config write", method: http.MethodPut, path: "/api/go/sessions/legacy-session/config",
			body: `{"config":{"model":"attacker-model"}}`,
		},
		{
			name: "direct tool execute", method: http.MethodPost, path: "/api/tools/session_append/execute",
			body: `{"input":{"sessionId":"legacy-session","role":"user","message":{"type":"user_message","text":"injected"}}}`,
		},
	}
	for _, test := range requests {
		t.Run(test.name, func(t *testing.T) {
			response := legacySurfaceRequest(t, fixture, test.method, test.path, test.body, "127.0.0.1:12345")
			if response.Code != http.StatusNotFound {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if after := legacySurfaceStateHash(t, fixture); after != before {
				t.Fatalf("legacy state changed: before=%s after=%s", before, after)
			}
		})
	}
}

func TestLegacySessionWebSocketQuarantineRejectsBeforeUpgrade(t *testing.T) {
	fixture := newLegacySurfaceFixture(t, true)
	server := httptest.NewServer(fixture.server.Handler())
	t.Cleanup(server.Close)
	before := legacySurfaceStateHash(t, fixture)

	header := http.Header{"Authorization": []string{"Bearer " + fixture.token}}
	conn, response, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http")+"/ws/"+legacySurfaceSessionID, &websocket.DialOptions{HTTPHeader: header})
	if conn != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "test complete")
	}
	if err == nil || response == nil || response.StatusCode != http.StatusNotFound {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		t.Fatalf("websocket err=%v status=%d", err, status)
	}
	if after := legacySurfaceStateHash(t, fixture); after != before {
		t.Fatalf("legacy state changed: before=%s after=%s", before, after)
	}
}

func TestLegacySessionSurfaceSecurityLayersKeepPriority(t *testing.T) {
	fixture := newLegacySurfaceFixture(t, true)

	unauthenticated := httptest.NewRequest(http.MethodGet, "/api/go/sessions/legacy-session/config", nil)
	unauthenticated.RemoteAddr = "127.0.0.1:12345"
	unauthenticatedResponse := httptest.NewRecorder()
	fixture.server.Handler().ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d body=%s", unauthenticatedResponse.Code, unauthenticatedResponse.Body.String())
	}

	crossOrigin := httptest.NewRequest(http.MethodPost, "/api/tools/session_append/execute", bytes.NewBufferString(`{"input":{}}`))
	crossOrigin.RemoteAddr = "127.0.0.1:12345"
	crossOrigin.Host = "localhost:8765"
	crossOrigin.Header.Set("Origin", "https://attacker.example")
	crossOrigin.Header.Set("Authorization", "Bearer "+fixture.token)
	crossOriginResponse := httptest.NewRecorder()
	fixture.server.Handler().ServeHTTP(crossOriginResponse, crossOrigin)
	if crossOriginResponse.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status=%d body=%s", crossOriginResponse.Code, crossOriginResponse.Body.String())
	}

	cookieMutation := httptest.NewRequest(http.MethodPut, "/api/go/sessions/legacy-session/config", strings.NewReader(`{"config":{}}`))
	cookieMutation.RemoteAddr = "127.0.0.1:12345"
	cookieMutation.AddCookie(&http.Cookie{Name: webSessionCookieName, Value: fixture.token})
	cookieMutationResponse := httptest.NewRecorder()
	fixture.server.Handler().ServeHTTP(cookieMutationResponse, cookieMutation)
	if cookieMutationResponse.Code != http.StatusForbidden {
		t.Fatalf("missing-CSRF status=%d body=%s", cookieMutationResponse.Code, cookieMutationResponse.Body.String())
	}
}

func TestLegacySessionSurfacesRejectSpoofedRemoteLocalMode(t *testing.T) {
	fixture := newLegacySurfaceFixture(t, false)
	before := legacySurfaceStateHash(t, fixture)
	for _, path := range []string{
		"/ws/legacy-session",
		"/api/go/sessions/legacy-session/config",
		"/api/tools/session_append/execute",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		if strings.Contains(path, "/execute") {
			request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"input":{}}`))
		}
		request.RemoteAddr = "198.51.100.10:12345"
		request.Host = "localhost:8765"
		request.Header.Set("X-Forwarded-For", "127.0.0.1")
		request.Header.Set("X-Real-IP", "127.0.0.1")
		request.Header.Set("X-Synon-User-Id", "local")
		response := httptest.NewRecorder()
		fixture.server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("path=%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	if after := legacySurfaceStateHash(t, fixture); after != before {
		t.Fatalf("legacy state changed: before=%s after=%s", before, after)
	}
}

func TestLegacySessionSurfaceRequiresTrustedLoopbackAuthority(t *testing.T) {
	fixture := newLegacySurfaceFixture(t, false)
	tests := []struct {
		host    string
		allowed bool
	}{
		{host: "localhost", allowed: true},
		{host: "LOCALHOST:8765", allowed: true},
		{host: "127.0.0.1", allowed: true},
		{host: "127.0.0.1:1", allowed: true},
		{host: "[::1]", allowed: true},
		{host: "[::1]:65535", allowed: true},
		{host: ""},
		{host: " localhost"},
		{host: "attacker.test"},
		{host: "localhoſt"},
		{host: "localhost."},
		{host: "localhost.attacker.test"},
		{host: "user@localhost"},
		{host: "localhost/path"},
		{host: "localhost?query"},
		{host: "localhost#fragment"},
		{host: "127.0.0.2"},
		{host: "127.0.0.1.attacker.test"},
		{host: "::1"},
		{host: "localhost:"},
		{host: "localhost:0"},
		{host: "localhost:65536"},
		{host: "localhost:not-a-port"},
	}
	for _, test := range tests {
		t.Run(test.host, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/go/sessions/legacy-session/config", nil)
			request.RemoteAddr = "127.0.0.1:12345"
			request.Host = test.host
			request.Header.Set("Forwarded", "host=localhost")
			request.Header.Set("X-Forwarded-Host", "localhost")
			if allowed := fixture.server.allowLegacySessionSurface(request); allowed != test.allowed {
				t.Fatalf("host=%q allowed=%t want=%t", test.host, allowed, test.allowed)
			}
		})
	}
}

func TestLegacySessionSurfacesRejectLoopbackRebindingAuthority(t *testing.T) {
	fixture := newLegacySurfaceFixture(t, false)
	before := legacySurfaceStateHash(t, fixture)
	for _, test := range []struct {
		method string
		path   string
		body   string
		ws     bool
	}{
		{method: http.MethodGet, path: "/api/go/sessions/legacy-session/config"},
		{method: http.MethodPost, path: "/api/tools/session_append/execute", body: `{"input":{"sessionId":"legacy-session","role":"user","message":{"type":"user_message","text":"injected"}}}`},
		{method: http.MethodGet, path: "/ws/legacy-session", ws: true},
	} {
		request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		request.RemoteAddr = "127.0.0.1:12345"
		request.Host = "attacker.test"
		request.Header.Set("Origin", "http://attacker.test")
		request.Header.Set("Forwarded", "host=localhost")
		request.Header.Set("X-Forwarded-Host", "localhost")
		if test.ws {
			request.Header.Set("Connection", "Upgrade")
			request.Header.Set("Upgrade", "websocket")
			request.Header.Set("Sec-WebSocket-Version", "13")
			request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		}
		response := httptest.NewRecorder()
		fixture.server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("path=%s status=%d body=%s", test.path, response.Code, response.Body.String())
		}
		if after := legacySurfaceStateHash(t, fixture); after != before {
			t.Fatalf("path=%s changed state: before=%s after=%s", test.path, before, after)
		}
	}
}

func TestLegacySessionSurfacesRemainAvailableForUnauthenticatedLoopback(t *testing.T) {
	fixture := newLegacySurfaceFixture(t, false)

	for _, remoteAddr := range []string{"127.23.45.67:12345", "[::1]:12345"} {
		config := legacySurfaceRequest(t, fixture, http.MethodGet, "/api/go/sessions/legacy-session/config", "", remoteAddr)
		if config.Code != http.StatusOK {
			t.Fatalf("remote=%s config status=%d body=%s", remoteAddr, config.Code, config.Body.String())
		}
	}
	configWrite := legacySurfaceRequest(
		t, fixture, http.MethodPut, "/api/go/sessions/legacy-session/config",
		`{"config":{"model":"local-model"}}`, "127.0.0.1:12345",
	)
	if configWrite.Code != http.StatusOK {
		t.Fatalf("config write status=%d body=%s", configWrite.Code, configWrite.Body.String())
	}
	toolExecute := legacySurfaceRequest(
		t, fixture, http.MethodPost, "/api/tools/session_append/execute",
		`{"input":{"sessionId":"legacy-session","role":"user","message":{"type":"user_message","text":"local input"}}}`,
		"127.0.0.1:12345",
	)
	if toolExecute.Code != http.StatusOK {
		t.Fatalf("tool execute status=%d body=%s", toolExecute.Code, toolExecute.Body.String())
	}
	catalog := legacySurfaceRequest(t, fixture, http.MethodGet, "/api/tools", "", "127.0.0.1:12345")
	if catalog.Code != http.StatusOK {
		t.Fatalf("tool catalog status=%d body=%s", catalog.Code, catalog.Body.String())
	}
	canonicalEvents := legacySurfaceRequest(t, fixture, http.MethodGet, "/api/events?after_sequence=0", "", "127.0.0.1:12345")
	if canonicalEvents.Code != http.StatusOK {
		t.Fatalf("canonical events status=%d body=%s", canonicalEvents.Code, canonicalEvents.Body.String())
	}

	server := httptest.NewServer(fixture.server.Handler())
	t.Cleanup(server.Close)
	conn, response, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http")+"/ws/"+legacySurfaceSessionID, nil)
	if err != nil {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		t.Fatalf("loopback websocket err=%v status=%d", err, status)
	}
	_ = conn.Close(websocket.StatusNormalClosure, "test complete")
}
