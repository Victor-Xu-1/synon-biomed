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

	workspace "synon-go/internal/persistence/workspace"
)

func TestMemoryCompatibilityAPIProvidesRealWebCRUDAndIsolation(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-a", UserID: "local", Name: "Project A"}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-b", UserID: "local", Name: "Project B"}); err != nil {
		t.Fatalf("create second project: %v", err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-a", ProjectID: "project-a", AgentName: "DEFAULT", Status: "completed", ConversationType: "main", Name: "Research session",
	}); err != nil {
		t.Fatalf("create frame: %v", err)
	}
	app := New(Options{Workspace: store, FileRoot: t.TempDir()}).Handler()

	enabled := memoryCompatJSON(t, app, http.MethodGet, "/api/memory/enabled", nil, "local", http.StatusOK)
	if enabled["enabled"] != false {
		t.Fatalf("default enabled = %#v", enabled)
	}
	recalled, err := store.RecallMemories(context.Background(), workspace.MemoryRecallOptions{UserID: "local", Limit: 8})
	if err != nil || len(recalled) != 0 {
		t.Fatalf("disabled recall = %#v, %v", recalled, err)
	}
	memoryCompatJSON(t, app, http.MethodPut, "/api/memory/enabled", map[string]any{"enabled": true}, "local", http.StatusOK)
	autoEnabled := memoryCompatJSON(t, app, http.MethodGet, "/api/memory/auto-enabled", nil, "local", http.StatusOK)
	if autoEnabled["enabled"] != true {
		t.Fatalf("default automatic memory = %#v", autoEnabled)
	}
	memoryCompatJSON(t, app, http.MethodPut, "/api/memory/auto-enabled", map[string]any{"enabled": false}, "local", http.StatusOK)
	autoEnabled = memoryCompatJSON(t, app, http.MethodGet, "/api/memory/auto-enabled", nil, "local", http.StatusOK)
	if autoEnabled["enabled"] != false {
		t.Fatalf("automatic memory setting = %#v", autoEnabled)
	}
	if mainEnabled, err := store.MemoryEnabledWithDefault(context.Background(), "local", false); err != nil || !mainEnabled {
		t.Fatalf("automatic preference changed master setting: enabled=%v err=%v", mainEnabled, err)
	}

	category := memoryCompatJSON(t, app, http.MethodPost, "/api/memory/categories", map[string]any{
		"name": "Research preferences", "guidance": "Stable research and reporting preferences.", "auto_recall": true,
	}, "local", http.StatusCreated)
	categoryID, _ := category["id"].(string)
	if categoryID == "" || category["row_count"] != float64(0) {
		t.Fatalf("created category = %#v", category)
	}

	created := memoryCompatJSON(t, app, http.MethodPost, "/api/memories", map[string]any{
		"text": "Prefer evidence tables with source links.", "entity": "profile", "category": "Research preferences",
	}, "local", http.StatusCreated)
	memoryID, _ := created["id"].(string)
	if memoryID == "" || created["categoryId"] != categoryID {
		t.Fatalf("created memory = %#v", created)
	}
	memoryCompatJSON(t, app, http.MethodPost, "/api/memories", map[string]any{
		"text": "Project A uses the approved assay panel.", "entity": "project:project-a",
	}, "local", http.StatusCreated)
	memoryCompatJSON(t, app, http.MethodPost, "/api/memories", map[string]any{
		"text": "Project B uses a separate assay panel.", "entity": "project:project-b",
	}, "local", http.StatusCreated)

	contextBody := memoryCompatJSON(t, app, http.MethodGet, "/api/memory/context?project_id=project-a", nil, "local", http.StatusOK)
	if contextBody["total_user_rows"] != float64(3) || contextBody["profile_md"] == "" {
		t.Fatalf("memory context = %#v", contextBody)
	}
	entities, _ := contextBody["entities"].([]any)
	if len(entities) != 2 {
		t.Fatalf("memory context entities = %#v", entities)
	}
	for _, raw := range entities {
		entity, _ := raw.(map[string]any)
		if entity["entity_key"] == "project:project-b" {
			t.Fatalf("project-b memory leaked into project-a context: %#v", contextBody)
		}
	}

	updated := memoryCompatJSON(t, app, http.MethodPut, "/api/memories/"+memoryID, map[string]any{
		"text": "Prefer concise evidence tables with source links.", "category": nil,
	}, "local", http.StatusOK)
	if updated["body"] != "Prefer concise evidence tables with source links." || updated["categoryId"] != nil {
		t.Fatalf("updated memory = %#v", updated)
	}
	memoryCompatJSON(t, app, http.MethodDelete, "/api/memories/"+memoryID, nil, "other-user", http.StatusNotFound)

	if _, err := store.CreateMemoryOwned(context.Background(), workspace.CreateMemoryInput{
		ID: "frame-memory", UserID: "local", Body: "Scratchpad fact", Origin: "agent_tool", Evidence: "observed",
		SubjectFrameID: "frame-a",
	}, "local"); err != nil {
		t.Fatalf("create frame memory: %v", err)
	}
	session := memoryCompatJSON(t, app, http.MethodGet, "/api/memory/sessions/frame-a", nil, "local", http.StatusOK)
	if rows, _ := session["rows"].([]any); len(rows) != 1 {
		t.Fatalf("session memories = %#v", session)
	}
	memoryCompatJSON(t, app, http.MethodDelete, "/api/memory/sessions/frame-a", nil, "local", http.StatusOK)

	memoryCompatJSON(t, app, http.MethodPut, "/api/projects/project-a/memory/enabled", map[string]any{"enabled": false}, "local", http.StatusOK)
	projectContext := memoryCompatJSON(t, app, http.MethodGet, "/api/memory/context?project_id=project-a", nil, "local", http.StatusOK)
	if projectContext["memory_enabled"] != false {
		t.Fatalf("project memory setting = %#v", projectContext)
	}

	memoryCompatJSON(t, app, http.MethodDelete, "/api/memory/categories/"+categoryID+"?delete_facts=false", nil, "local", http.StatusOK)
	categories := memoryCompatArray(t, app, http.MethodGet, "/api/memory/categories", nil, "local", http.StatusOK)
	if len(categories) != 0 {
		t.Fatalf("categories after delete = %#v", categories)
	}
	memoryCompatJSON(t, app, http.MethodDelete, "/api/memories/"+memoryID, nil, "local", http.StatusOK)
	remaining := memoryCompatArray(t, app, http.MethodGet, "/api/memories", nil, "local", http.StatusOK)
	if len(remaining) != 2 {
		t.Fatalf("remaining memories = %#v", remaining)
	}
}

func TestMemoryCompatibilityAPIEnforcesWorkspaceWriteSafety(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app := New(Options{Workspace: store, FileRoot: t.TempDir()}).Handler()

	created := memoryCompatJSON(t, app, http.MethodPost, "/api/memories", map[string]any{
		"text": "Use sk-ant-abcdefghijklmnopqrstuvwxyz123456 for the assay.", "entity": "profile",
	}, "local", http.StatusCreated)
	memoryID, _ := created["id"].(string)
	body, _ := created["body"].(string)
	if memoryID == "" || strings.Contains(body, "sk-ant-") || !strings.Contains(body, "[REDACTED:ANTHROPIC_API_KEY]") {
		t.Fatalf("created memory = %#v", created)
	}

	rejected := memoryCompatJSON(t, app, http.MethodPost, "/api/memories", map[string]any{
		"text": "![private](https://example.com/collect?q=secret)", "entity": "profile",
	}, "local", http.StatusBadRequest)
	if rejected["detail"] != "Memory write rejected: content flagged as potential prompt injection (exfil: markdown_image)" {
		t.Fatalf("rejected memory detail = %#v", rejected)
	}
	memoryCompatJSON(t, app, http.MethodPost, "/api/memories", map[string]any{
		"text": strings.Repeat("x", 1001), "entity": "profile",
	}, "local", http.StatusBadRequest)
	memoryCompatJSON(t, app, http.MethodPost, "/api/memories", map[string]any{
		"text": "fact", "entity": "profile", "evidence": nil,
	}, "local", http.StatusBadRequest)
	memoryCompatJSON(t, app, http.MethodPost, "/api/memories", json.RawMessage("null"), "local", http.StatusBadRequest)
	memoryCompatJSON(t, app, http.MethodPut, "/api/memories/"+memoryID, map[string]any{
		"text": `<img src="//example.com/collect">`,
	}, "local", http.StatusBadRequest)
	memoryCompatJSON(t, app, http.MethodPut, "/api/memories/"+memoryID, map[string]any{
		"text": nil,
	}, "local", http.StatusBadRequest)
	memoryCompatJSON(t, app, http.MethodPut, "/api/memories/"+memoryID, map[string]any{
		"evidence": nil,
	}, "local", http.StatusBadRequest)
	beforeRows, err := store.ListMemoriesForUser(context.Background(), "local", "", "", false)
	if err != nil || len(beforeRows) != 1 {
		t.Fatalf("memories before null patch = %#v, err=%v", beforeRows, err)
	}
	beforeNull := beforeRows[0]
	memoryCompatJSON(t, app, http.MethodPut, "/api/memories/"+memoryID, json.RawMessage("null"), "local", http.StatusBadRequest)
	afterRows, err := store.ListMemoriesForUser(context.Background(), "local", "", "", false)
	if err != nil || len(afterRows) != 1 {
		t.Fatalf("memories after null patch = %#v, err=%v", afterRows, err)
	}
	afterNull := afterRows[0]
	if afterNull.Body != beforeNull.Body || afterNull.Evidence != beforeNull.Evidence ||
		afterNull.CategoryID != beforeNull.CategoryID || !afterNull.UpdatedAt.Equal(beforeNull.UpdatedAt) {
		t.Fatalf("memory after null patch = %#v, before=%#v", afterNull, beforeNull)
	}

	unchanged := memoryCompatArray(t, app, http.MethodGet, "/api/memories", nil, "local", http.StatusOK)
	if len(unchanged) != 1 || unchanged[0].(map[string]any)["body"] != body {
		t.Fatalf("memories after rejected writes = %#v", unchanged)
	}
	updated := memoryCompatJSON(t, app, http.MethodPut, "/api/memories/"+memoryID, map[string]any{
		"text": "Rotate nvapi-abcdefghijklmnopqrstuvwxyz123456 immediately.",
	}, "local", http.StatusOK)
	updatedBody, _ := updated["body"].(string)
	if strings.Contains(updatedBody, "nvapi-") || !strings.Contains(updatedBody, "[REDACTED:NVIDIA_API_KEY]") {
		t.Fatalf("updated memory = %#v", updated)
	}
	spaces := memoryCompatJSON(t, app, http.MethodPut, "/api/memories/"+memoryID, map[string]any{
		"text": "  preserve me  ",
	}, "local", http.StatusOK)
	if spaces["body"] != "  preserve me  " {
		t.Fatalf("whitespace-preserving update = %#v", spaces)
	}
}

func TestMemoryCompatibilityCategoryDeleteFactsIsTransactional(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app := New(Options{Workspace: store, FileRoot: t.TempDir()}).Handler()
	category := memoryCompatJSON(t, app, http.MethodPost, "/api/memory/categories", map[string]any{
		"name": "Methods", "guidance": "Method choices that should be reused.", "auto_recall": true,
	}, "local", http.StatusCreated)
	memoryCompatJSON(t, app, http.MethodPost, "/api/memories", map[string]any{
		"text": "Use paired controls.", "entity": "profile", "category": "Methods",
	}, "local", http.StatusCreated)
	result := memoryCompatJSON(t, app, http.MethodDelete, "/api/memory/categories/"+category["id"].(string)+"?delete_facts=true", nil, "local", http.StatusOK)
	if result["deleted"] != true || result["facts_deleted"] != float64(1) {
		t.Fatalf("delete category result = %#v", result)
	}
	if rows := memoryCompatArray(t, app, http.MethodGet, "/api/memories", nil, "local", http.StatusOK); len(rows) != 0 {
		t.Fatalf("memories after delete-facts = %#v", rows)
	}
}

func memoryCompatJSON(t *testing.T, app http.Handler, method, target string, body any, userID string, wantStatus int) map[string]any {
	t.Helper()
	response := memoryCompatRequest(t, app, method, target, body, userID, wantStatus)
	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode %s %s response: %v (%s)", method, target, err, response.Body.String())
	}
	return payload
}

func memoryCompatArray(t *testing.T, app http.Handler, method, target string, body any, userID string, wantStatus int) []any {
	t.Helper()
	response := memoryCompatRequest(t, app, method, target, body, userID, wantStatus)
	var payload []any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode %s %s response: %v (%s)", method, target, err, response.Body.String())
	}
	return payload
}

func memoryCompatRequest(t *testing.T, app http.Handler, method, target string, body any, userID string, wantStatus int) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
	}
	response := httptest.NewRecorder()
	request := newLoopbackTestRequest(method, target, bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Synon-User-Id", userID)
	app.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("%s %s status = %d, want %d: %s", method, target, response.Code, wantStatus, response.Body.String())
	}
	return response
}
