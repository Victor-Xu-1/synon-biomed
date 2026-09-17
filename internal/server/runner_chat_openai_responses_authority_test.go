package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	eventjournal "synon-go/internal/persistence/journal"
	secretstore "synon-go/internal/persistence/secrets"
	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
)

func TestSessionRunnerChatOnceUsesSavedOpenAIResponsesProviderAuthority(t *testing.T) {
	var requests atomic.Int32
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer responses-key" {
			t.Errorf("Responses request path=%q authorization=%q", r.URL.Path, r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var payload struct {
			Model string           `json:"model"`
			Store *bool            `json:"store"`
			Input []map[string]any `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode Responses request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if payload.Model != "gpt-responses-saved" || payload.Store == nil || *payload.Store || len(payload.Input) == 0 {
			t.Errorf("Responses payload = %#v", payload)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "resp_saved_1",
			"output": []any{map[string]any{
				"type": "message", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": "saved Responses response"}},
			}},
			"usage": map[string]any{
				"input_tokens": 11, "output_tokens": 5, "total_tokens": 16,
				"input_tokens_details": map[string]any{"cached_tokens": 4},
			},
		})
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	workspaceStore, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaceStore.Close() })
	srv := New(Options{FileRoot: root, Workspace: workspaceStore})
	if _, err := workspaceStore.CreateProject(workspace.CreateProjectInput{
		ID: "project-responses", UserID: "user-1", Name: "Responses project", Path: root,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.settingsStore.Set("model.activeProviderId", "responses-provider"); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.secretStore.Create(secretstore.Secret{
		ID: "responses-key", UserID: "user-1", Provider: "openai", Value: "responses-key",
	}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := workspaceStore.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "responses-provider", UserID: "user-1", Name: "OpenAI Responses", Type: "openai-responses",
		BaseURL: modelAPI.URL + "/v1", Model: "gpt-responses-saved",
		SecretRef: "secret://responses-key", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	session := sessionstore.Session{
		ID: "session-responses-authority", Title: "Responses authority", WorkDir: root,
		CreatedAt: now, UpdatedAt: now, LastUserMessageAt: now, MessageCount: 1, LastRole: "user",
		Project: &sessionstore.Project{ID: "project-responses", Name: "Responses project", Path: root, BoundAt: now},
	}
	if err := srv.sessionStore.Save(session); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.eventJournal.Append(session.ID, eventjournal.Message{
		"type": "message", "role": "user", "text": "use saved Responses provider",
	}, eventjournal.Metadata{ClientMessageID: "user-responses"}); err != nil {
		t.Fatal(err)
	}

	result, err := srv.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{SessionID: session.ID, RunnerID: "responses-runner",
		Endpoint: "http://127.0.0.1:1/v1/chat/completions",
		APIKey:   "wrong-static-key", Model: "wrong-static-model",
		LeaseTTL: time.Minute, ReplayLimit: 20, OutputLimitBytes: 64 * 1024,
		RequestTimeout: time.Minute, MaxAttempts: 1, MaxToolRounds: 1})
	if err != nil {
		t.Fatalf("RunSessionRunnerChatOnce() error = %v", err)
	}
	if !result.Claimed || result.Status != "completed" || result.AssistantEventID == 0 || requests.Load() != 1 {
		t.Fatalf("runner result=%+v requests=%d", result, requests.Load())
	}
	entries, err := srv.eventJournal.ReadAfter(session.ID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if !hasJournalEntry(providerAuthorityEntriesToAny(entries), "message", "", "saved Responses response") {
		t.Fatalf("journal entries = %#v", entries)
	}
	audits, err := srv.runtimeStore.List(sessionRunnerModelAuditRuntimeNamespace)
	if err != nil {
		t.Fatal(err)
	}
	audit := findSessionRunnerModelAuditValue(audits, session.ID)
	if audit == nil || audit["protocol"] != "openai-responses" ||
		audit["requestId"] != "resp_saved_1" || audit["totalTokens"] != float64(16) ||
		audit["cacheReadTokens"] != float64(4) {
		t.Fatalf("Responses audit = %#v", audit)
	}
}
