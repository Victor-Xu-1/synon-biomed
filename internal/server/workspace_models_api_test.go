package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestWorkspaceModelsAPIListsEnabledProviderModels(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "provider-1", UserID: "user-1", Name: "primary", Type: "openai-compatible",
		BaseURL: "https://models.example.test/v1", Model: "model-a", SecretRef: "secret://primary",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "provider-2", UserID: "user-2", Name: "foreign", Type: "openai-compatible",
		BaseURL: "https://foreign.example.test/v1", Model: "foreign-model", SecretRef: "secret://foreign",
	}); err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store}).Handler()
	compatibility := httptest.NewRecorder()
	compatibilityRequest := httptest.NewRequest(http.MethodGet, "/api/models", nil)
	compatibilityRequest.Header.Set("X-Synon-User-Id", "user-1")
	app.ServeHTTP(compatibility, compatibilityRequest)
	if compatibility.Code != http.StatusOK || bytes.Contains(compatibility.Body.Bytes(), []byte("secret://")) ||
		bytes.Contains(compatibility.Body.Bytes(), []byte("foreign-model")) {
		t.Fatalf("/api/models = %d: %s", compatibility.Code, compatibility.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(compatibility.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["default_model_id"] != "model-a" {
		t.Fatalf("compatibility models = %#v", payload)
	}
	groups := payload["models"].(map[string]any)
	items := groups["model-a"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != "model-a" ||
		items[0].(map[string]any)["overflow"] != false {
		t.Fatalf("compatibility model items = %#v", items)
	}

	openAI := httptest.NewRecorder()
	openAIRequest := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	openAIRequest.Header.Set("X-Synon-User-Id", "user-1")
	app.ServeHTTP(openAI, openAIRequest)
	if openAI.Code != http.StatusOK || !bytes.Contains(openAI.Body.Bytes(), []byte("\"id\":\"model-a\"")) ||
		bytes.Contains(openAI.Body.Bytes(), []byte("secret://")) || bytes.Contains(openAI.Body.Bytes(), []byte("foreign-model")) {
		t.Fatalf("/v1/models = %d: %s", openAI.Code, openAI.Body.String())
	}

	unknown := httptest.NewRecorder()
	unknownRequest := httptest.NewRequest(http.MethodGet, "/api/models?provider=other", nil)
	unknownRequest.Header.Set("X-Synon-User-Id", "user-1")
	app.ServeHTTP(unknown, unknownRequest)
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown provider = %d: %s", unknown.Code, unknown.Body.String())
	}
}

func TestWorkspaceModelsAPIProjectsActiveProfileAndStableSelectors(t *testing.T) {
	type modelRow struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
		Provider    string `json:"provider"`
		ProfileID   string `json:"profile_id"`
		Model       string `json:"model"`
		Active      bool   `json:"active"`
	}
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, provider := range []workspace.ModelProviderInput{
		{
			ID: "profile-primary", UserID: "local", Name: "Primary",
			Type: "custom-openai", BaseURL: "https://primary.example.test/v1",
			Model: "shared-model", SecretRef: "secret://primary",
		},
		{
			ID: "profile-active", UserID: "local", Name: "Active",
			Type: "deepseek", BaseURL: "https://active.example.test/v1",
			Model: "active-model", SecretRef: "secret://active",
		},
	} {
		if _, err := store.RegisterModelProvider(provider); err != nil {
			t.Fatal(err)
		}
	}
	server := New(Options{Workspace: store, FileRoot: root})
	if _, err := server.settingsStore.Set(webConversationActiveProviderSetting, "profile-active"); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("X-Synon-User-Id", "local")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("/v1/models = %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		DefaultModelID string     `json:"default_model_id"`
		HasMore        bool       `json:"has_more"`
		Data           []modelRow `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.DefaultModelID != "active-model" || payload.HasMore || len(payload.Data) != 2 {
		t.Fatalf("models = %#v", payload)
	}
	byProfile := make(map[string]modelRow, len(payload.Data))
	for _, model := range payload.Data {
		byProfile[model.ProfileID] = model
	}
	if active := byProfile["profile-active"]; active.ID != "active-model" || !active.Active ||
		active.Model != "active-model" || active.Provider != "deepseek" {
		t.Fatalf("active model = %#v", active)
	}
	if inactive := byProfile["profile-primary"]; inactive.ID != "profile:profile-primary" ||
		inactive.Active || inactive.DisplayName != "shared-model" {
		t.Fatalf("inactive model = %#v", inactive)
	}
}
