package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

const referenceContactEmailNoticeVersion = "99f2e8cddaf43594d50084dc3f990846f39c0e314d766e474e72df4d7e7fd89a"

func TestContactEmailCompatibilityAPIIsUserScopedDurableAndRevocable(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app := New(Options{FileRoot: root, Workspace: store}).Handler()

	initial := runtimeCompatJSON(t, app, http.MethodGet, "/api/contact-email", "user-a", nil, http.StatusOK)
	if initial["decision"] != nil || initial["email"] != nil || initial["notice_stale"] != false ||
		initial["notice_version"] != referenceContactEmailNoticeVersion {
		t.Fatalf("initial contact email = %#v", initial)
	}
	if text, _ := initial["notice_text"].(string); !strings.Contains(text, "saved locally") || !strings.Contains(text, "LLM session's context") {
		t.Fatalf("contact email notice = %q", text)
	}

	stale := runtimeCompatJSON(t, app, http.MethodPut, "/api/contact-email", "user-a", map[string]any{
		"email": "private@example.test", "notice_version": "old-notice",
	}, http.StatusConflict)
	if stale["notice_version"] != referenceContactEmailNoticeVersion || strings.Contains(asJSONString(stale), "private@example.test") {
		t.Fatalf("stale response = %#v", stale)
	}
	for _, email := range []string{"", "missing-at.example.test", "bad\n@example.test", strings.Repeat("a", 310) + "@example.test"} {
		response := runtimeCompatJSON(t, app, http.MethodPut, "/api/contact-email", "user-a", map[string]any{
			"email": email, "notice_version": referenceContactEmailNoticeVersion,
		}, http.StatusBadRequest)
		if strings.Contains(asJSONString(response), email) && email != "" {
			t.Fatalf("invalid email leaked in response: %#v", response)
		}
	}

	allowed := runtimeCompatJSON(t, app, http.MethodPut, "/api/contact-email", "user-a", map[string]any{
		"email": " private@example.test ", "notice_version": referenceContactEmailNoticeVersion,
	}, http.StatusOK)
	if allowed["decision"] != "allowed" || allowed["email"] != "private@example.test" || allowed["notice_stale"] != false {
		t.Fatalf("allowed contact email = %#v", allowed)
	}
	other := runtimeCompatJSON(t, app, http.MethodGet, "/api/contact-email", "user-b", nil, http.StatusOK)
	if other["decision"] != nil || other["email"] != nil {
		t.Fatalf("contact email leaked across users: %#v", other)
	}

	restarted := New(Options{FileRoot: root, Workspace: store}).Handler()
	persisted := runtimeCompatJSON(t, restarted, http.MethodGet, "/api/contact-email", "user-a", nil, http.StatusOK)
	if persisted["decision"] != "allowed" || persisted["email"] != "private@example.test" {
		t.Fatalf("persisted contact email = %#v", persisted)
	}
	revoked := runtimeCompatJSON(t, restarted, http.MethodDelete, "/api/contact-email", "user-a", nil, http.StatusOK)
	if revoked["decision"] != "revoked" || revoked["email"] != nil {
		t.Fatalf("revoked contact email = %#v", revoked)
	}
	revokedAgain := runtimeCompatJSON(t, restarted, http.MethodDelete, "/api/contact-email", "user-a", nil, http.StatusOK)
	if revokedAgain["decision"] != "revoked" || revokedAgain["email"] != nil {
		t.Fatalf("idempotent revocation = %#v", revokedAgain)
	}
}

func TestContactEmailCompatibilityAPIFailsClosed(t *testing.T) {
	app := New(Options{}).Handler()
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		response := httptest.NewRecorder()
		request := newLoopbackTestRequest(method, "/api/contact-email", bytes.NewReader([]byte(`{"email":"x@example.test"}`)))
		app.ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable || bytes.Contains(response.Body.Bytes(), []byte("x@example.test")) {
			t.Fatalf("unavailable %s = %d: %s", method, response.Code, response.Body.String())
		}
	}

	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	configured := New(Options{FileRoot: root, Workspace: store}).Handler()
	response := httptest.NewRecorder()
	configured.ServeHTTP(response, newLoopbackTestRequest(http.MethodPost, "/api/contact-email", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("unsupported method = %d: %s", response.Code, response.Body.String())
	}
	get := httptest.NewRecorder()
	configured.ServeHTTP(get, newLoopbackTestRequest(http.MethodGet, "/api/contact-email", nil))
	if get.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cache control = %q", get.Header().Get("Cache-Control"))
	}
}

func asJSONString(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
