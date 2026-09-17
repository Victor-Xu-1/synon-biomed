package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

func TestObservabilityEndpointsRequireAuthProjectDeadLettersAndEnforceCSRF(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	event, err := store.EnqueueOutbox(context.Background(), workspace.EnqueueOutboxInput{
		ID: "dead-1", IdempotencyKey: "dead-1", Topic: "runtime.events", PartitionKey: "session-1",
		Type: "runtime.failed", Payload: json.RawMessage(`{"secret":"payload-secret"}`),
		Headers: map[string]string{"Authorization": "Bearer header-secret"}, MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimOutbox(context.Background(), workspace.ClaimOutboxInput{WorkerID: "worker-1", Limit: 1, Lease: time.Second})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	if dead, err := store.RetryOutbox(context.Background(), event.ID, claimed[0].ClaimToken, "delivery failed: Authorization=Bearer last-error-secret password=pass-secret user@example.com", 0); err != nil || !dead {
		t.Fatalf("dead=%v err=%v", dead, err)
	}

	app := New(Options{Workspace: store, SynonLinkAuth: SynonLinkAuthOptions{
		Username: "operator", Password: "test-secret-password", UserID: "user-1",
	}})
	handler := app.Handler()
	unauthenticated := serveObservabilityRequest(handler, http.MethodGet, "/metrics", nil, nil, "")
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated metrics status=%d body=%s", unauthenticated.Code, unauthenticated.Body.String())
	}
	sessionCookie, csrfCookie := loginObservabilityCookies(t, handler)

	metrics := serveObservabilityRequest(handler, http.MethodGet, "/metrics", sessionCookie, nil, "")
	if metrics.Code != http.StatusOK || !strings.Contains(metrics.Header().Get("Content-Type"), "text/plain") ||
		!strings.Contains(metrics.Body.String(), "synon_http_requests_total") {
		t.Fatalf("metrics status=%d headers=%v body=%s", metrics.Code, metrics.Header(), metrics.Body.String())
	}
	diagnostics := serveObservabilityRequest(handler, http.MethodGet, "/api/go/diagnostics/runtime", sessionCookie, nil, "")
	if diagnostics.Code != http.StatusOK {
		t.Fatalf("diagnostics status=%d body=%s", diagnostics.Code, diagnostics.Body.String())
	}
	var diagnosticPayload map[string]any
	if err := json.Unmarshal(diagnostics.Body.Bytes(), &diagnosticPayload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"release", "http", "shellSandbox", "environments", "schema", "outbox", "recovery", "go", "errors"} {
		if _, ok := diagnosticPayload[key]; !ok {
			t.Fatalf("diagnostics missing %q: %#v", key, diagnosticPayload)
		}
	}

	deadLetters := serveObservabilityRequest(handler, http.MethodGet, "/api/go/diagnostics/outbox/dead-letters?limit=1", sessionCookie, nil, "")
	if deadLetters.Code != http.StatusOK || !strings.Contains(deadLetters.Body.String(), `"id":"dead-1"`) {
		t.Fatalf("dead letters status=%d body=%s", deadLetters.Code, deadLetters.Body.String())
	}
	for _, secret := range []string{"payload-secret", "header-secret", "last-error-secret", "pass-secret", "user@example.com", `"payload"`, `"headers"`, `"claimToken"`} {
		if strings.Contains(deadLetters.Body.String(), secret) {
			t.Fatalf("dead-letter projection leaked %q: %s", secret, deadLetters.Body.String())
		}
	}

	requeuePath := "/api/go/diagnostics/outbox/dead-letters/dead-1/requeue"
	withoutCSRF := serveObservabilityRequest(handler, http.MethodPost, requeuePath, sessionCookie, nil, "")
	if withoutCSRF.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status=%d body=%s", withoutCSRF.Code, withoutCSRF.Body.String())
	}
	withCSRF := serveObservabilityRequest(handler, http.MethodPost, requeuePath, sessionCookie, csrfCookie, csrfCookie.Value)
	if withCSRF.Code != http.StatusOK || !strings.Contains(withCSRF.Body.String(), `"status":"pending"`) {
		t.Fatalf("requeue status=%d body=%s", withCSRF.Code, withCSRF.Body.String())
	}
}

func loginObservabilityCookies(t *testing.T, handler http.Handler) (*http.Cookie, *http.Cookie) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"operator","password":"test-secret-password"}`))
	request.RemoteAddr = "127.0.0.1:12345"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", response.Code, response.Body.String())
	}
	var sessionCookie, csrfCookie *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		switch cookie.Name {
		case webSessionCookieName:
			sessionCookie = cookie
		case webCSRFCookieName:
			csrfCookie = cookie
		}
	}
	if sessionCookie == nil || csrfCookie == nil {
		t.Fatalf("login cookies=%#v", response.Result().Cookies())
	}
	return sessionCookie, csrfCookie
}

func serveObservabilityRequest(handler http.Handler, method, target string, sessionCookie, csrfCookie *http.Cookie, csrfToken string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, nil)
	request.RemoteAddr = "127.0.0.1:12345"
	if sessionCookie != nil {
		request.AddCookie(sessionCookie)
	}
	if csrfCookie != nil {
		request.AddCookie(csrfCookie)
	}
	if csrfToken != "" {
		request.Header.Set(webCSRFHeaderName, csrfToken)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
