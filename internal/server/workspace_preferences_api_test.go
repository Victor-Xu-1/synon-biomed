package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestRuntimePreferenceHTTPAPIPersistsAcrossServerRestart(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	runtimeServer := New(Options{FileRoot: root, Workspace: store})
	app := runtimeServer.Handler()

	serveWorkspaceJSON(t, app, http.MethodPut, "/api/go/settings/allowed-domains", map[string]any{
		"domains": []string{"Example.COM", "*.example.org", "example.com"},
	}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/settings/allowed-domains", map[string]any{
		"domain": "api.example.net",
	}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodDelete, "/api/go/settings/allowed-domains?domain=example.com", nil, http.StatusOK)
	realtime := runtimeCompatJSON(t, app, http.MethodGet, "/api/events?type=network_access_granted&limit=100", "local", nil, http.StatusOK)
	if events := realtime["events"].([]any); len(events) != 3 {
		t.Fatalf("allowed-domain realtime events=%#v", events)
	}
	serveWorkspaceJSON(t, app, http.MethodPut, "/api/go/settings/first-run", map[string]any{
		"completed": true,
	}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPut, "/api/go/settings/use-intent", map[string]any{
		"value": "noncommercial",
	}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPut, "/api/go/settings/ambient-backdrop", map[string]any{
		"value": true,
	}, http.StatusOK)

	restarted := New(Options{FileRoot: root, Workspace: store}).Handler()
	domains := httptest.NewRecorder()
	restarted.ServeHTTP(domains, newLoopbackTestRequest(http.MethodGet, "/api/go/settings/allowed-domains", nil))
	if domains.Code != http.StatusOK || !bytes.Contains(domains.Body.Bytes(), []byte("*.example.org")) ||
		!bytes.Contains(domains.Body.Bytes(), []byte("api.example.net")) ||
		bytes.Contains(domains.Body.Bytes(), []byte("\"example.com\"")) {
		t.Fatalf("persisted allowed domains = %d: %s", domains.Code, domains.Body.String())
	}
	for path, expected := range map[string]string{
		"/api/go/settings/first-run":        "\"completed\":true",
		"/api/go/settings/use-intent":       "\"value\":\"noncommercial\"",
		"/api/go/settings/ambient-backdrop": "\"value\":true",
	} {
		response := httptest.NewRecorder()
		restarted.ServeHTTP(response, newLoopbackTestRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(expected)) {
			t.Fatalf("persisted %s = %d: %s", path, response.Code, response.Body.String())
		}
	}
}

func TestAmbientBackdropIsBooleanUserScopedDurableAndMigratesLegacyStrings(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	runtimeServer := New(Options{FileRoot: root, Workspace: store})
	app := runtimeServer.Handler()

	if _, err := runtimeServer.settingsStore.Set(ambientBackdropSettingKey, "true"); err != nil {
		t.Fatal(err)
	}
	legacy := runtimeCompatJSON(t, app, http.MethodGet, "/api/go/settings/ambient-backdrop", "local", nil, http.StatusOK)
	if legacy["value"] != true {
		t.Fatalf("legacy ambient backdrop = %#v", legacy)
	}
	migrated, found, err := runtimeServer.settingsStore.Get(ambientBackdropSettingKey)
	if err != nil || !found || migrated.Value != true {
		t.Fatalf("migrated ambient backdrop = %#v, found=%v, err=%v", migrated.Value, found, err)
	}

	updated := runtimeCompatJSON(t, app, http.MethodPut, "/api/go/settings/ambient-backdrop", "user-a", map[string]any{
		"value": true,
	}, http.StatusOK)
	if updated["value"] != true {
		t.Fatalf("updated ambient backdrop = %#v", updated)
	}
	other := runtimeCompatJSON(t, app, http.MethodGet, "/api/go/settings/ambient-backdrop", "user-b", nil, http.StatusOK)
	if other["value"] != false {
		t.Fatalf("ambient backdrop leaked across users: %#v", other)
	}
	runtimeCompatJSON(t, app, http.MethodPut, "/api/go/settings/ambient-backdrop", "user-a", map[string]any{
		"value": "system",
	}, http.StatusBadRequest)

	restarted := New(Options{FileRoot: root, Workspace: store}).Handler()
	persisted := runtimeCompatJSON(t, restarted, http.MethodGet, "/api/go/settings/ambient-backdrop", "user-a", nil, http.StatusOK)
	if persisted["value"] != true {
		t.Fatalf("persisted ambient backdrop = %#v", persisted)
	}
}

func TestCompatibilityFirstRunAndUseIntentAreUserScopedAndDurable(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-a", UserID: "user-a", Name: "A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-b", UserID: "user-b", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "onboarding", ProjectID: "project-a", AgentName: "ONBOARDING", Status: "completed", ConversationType: "task",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "onboarding-child", ProjectID: "project-a", ParentFrameID: "onboarding",
		AgentName: "OPERON", Status: "completed", ConversationType: "task",
	}); err != nil {
		t.Fatal(err)
	}
	runtimeServer := New(Options{FileRoot: root, Workspace: store})
	app := runtimeServer.Handler()

	firstRun := runtimeCompatJSON(t, app, http.MethodGet, "/api/preferences/first-run-onboarding", "user-a", nil, http.StatusOK)
	if firstRun["complete"] != false {
		t.Fatalf("onboarding-only roots completed first run: %#v", firstRun)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "normal-root", ProjectID: "project-a", AgentName: "OPERON", Status: "running", ConversationType: "task",
	}); err != nil {
		t.Fatal(err)
	}
	firstRun = runtimeCompatJSON(t, app, http.MethodGet, "/api/preferences/first-run-onboarding", "user-a", nil, http.StatusOK)
	if firstRun["complete"] != true {
		t.Fatalf("normal root did not complete first run: %#v", firstRun)
	}
	other := runtimeCompatJSON(t, app, http.MethodGet, "/api/preferences/first-run-onboarding", "user-b", nil, http.StatusOK)
	if other["complete"] != false {
		t.Fatalf("first-run state leaked across users: %#v", other)
	}
	runtimeCompatJSON(t, app, http.MethodPost, "/api/preferences/first-run-onboarding/complete", "user-b", map[string]any{}, http.StatusOK)

	intent := runtimeCompatJSON(t, app, http.MethodGet, "/api/preferences/use-intent", "user-a", nil, http.StatusOK)
	if intent["intent"] != "commercial" || intent["declared"] != false {
		t.Fatalf("default use intent = %#v", intent)
	}
	intent = runtimeCompatJSON(t, app, http.MethodPut, "/api/preferences/use-intent", "user-a", map[string]any{
		"intent": "noncommercial",
	}, http.StatusOK)
	if intent["intent"] != "noncommercial" || intent["declared"] != true {
		t.Fatalf("declared use intent = %#v", intent)
	}
	runtimeCompatJSON(t, app, http.MethodPut, "/api/preferences/use-intent", "user-a", map[string]any{
		"intent": "research",
	}, http.StatusBadRequest)
	otherIntent := runtimeCompatJSON(t, app, http.MethodGet, "/api/preferences/use-intent", "user-b", nil, http.StatusOK)
	if otherIntent["intent"] != "commercial" || otherIntent["declared"] != false {
		t.Fatalf("use intent leaked across users: %#v", otherIntent)
	}
	if _, err := runtimeServer.settingsStore.Set(useIntentSettingKey, "drug-discovery"); err != nil {
		t.Fatal(err)
	}
	legacyIntent := runtimeCompatJSON(t, app, http.MethodGet, "/api/preferences/use-intent", "local", nil, http.StatusOK)
	if legacyIntent["intent"] != "drug-discovery" || legacyIntent["declared"] != true {
		t.Fatalf("legacy stored intent was not readable: %#v", legacyIntent)
	}

	restarted := New(Options{FileRoot: root, Workspace: store}).Handler()
	for userID, expected := range map[string]string{"user-a": "noncommercial", "user-b": "commercial"} {
		persisted := runtimeCompatJSON(t, restarted, http.MethodGet, "/api/preferences/use-intent", userID, nil, http.StatusOK)
		if persisted["intent"] != expected {
			t.Fatalf("persisted use intent for %s = %#v", userID, persisted)
		}
		completed := runtimeCompatJSON(t, restarted, http.MethodGet, "/api/preferences/first-run-onboarding", userID, nil, http.StatusOK)
		if completed["complete"] != true {
			t.Fatalf("persisted first-run state for %s = %#v", userID, completed)
		}
	}
}

func TestCompatibilityAllowedDomainsContractAndPolicy(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app := New(Options{
		FileRoot: root, Workspace: store,
		ConfigAllowedDomains: []string{"config.example.com"},
		ConfigDeniedDomains:  []string{"*.blocked.example.com"},
	}).Handler()

	get := getProjectControlJSON(t, app, "/api/preferences/allowed-domains", http.StatusOK)
	if _, found := get["ok"]; found || get["activeKernelCount"] != float64(0) {
		t.Fatalf("compatibility get response = %#v", get)
	}
	if values := get["configDomains"].([]any); len(values) != 1 || values[0] != "config.example.com" {
		t.Fatalf("config domains = %#v", values)
	}
	serveWorkspaceJSON(t, app, http.MethodPut, "/api/preferences/allowed-domains", map[string]any{
		"domains": []string{"Example.COM", "*.example.org"},
	}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/preferences/allowed-domains", map[string]any{
		"domain": "api.example.net",
	}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodDelete, "/api/preferences/allowed-domains/example.com", nil, http.StatusOK)
	final := getProjectControlJSON(t, app, "/api/preferences/allowed-domains", http.StatusOK)
	if values := final["domains"].([]any); len(values) != 2 || values[0] != "*.example.org" || values[1] != "api.example.net" {
		t.Fatalf("final user domains = %#v", values)
	}
	for name, domain := range map[string]string{
		"private": "127.0.0.1", "reserved": "service.local", "denied": "api.blocked.example.com",
	} {
		t.Run(name, func(t *testing.T) {
			serveWorkspaceJSON(t, app, http.MethodPost, "/api/preferences/allowed-domains", map[string]any{
				"domain": domain,
			}, http.StatusBadRequest)
		})
	}
}
