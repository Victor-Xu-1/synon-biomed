package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	eventjournal "synon-go/internal/persistence/journal"
	secretstore "synon-go/internal/persistence/secrets"
	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
)

func TestSessionRunnerChatOnceUsesSavedAnthropicProviderAuthority(t *testing.T) {
	requests := 0
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "anthropic-key" || r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("Anthropic request path=%q key=%q version=%q", r.URL.Path, r.Header.Get("x-api-key"), r.Header.Get("anthropic-version"))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode Anthropic request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if payload["model"] != "claude-saved" {
			t.Errorf("Anthropic model = %#v", payload["model"])
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_saved_1", "role": "assistant", "type": "message",
			"content": []any{map[string]any{"type": "text", "text": "saved Anthropic response"}},
			"usage":   map[string]any{"input_tokens": 9, "output_tokens": 4},
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
		ID: "project-anthropic", UserID: "user-1", Name: "Anthropic project", Path: root,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.settingsStore.Set("model.activeProviderId", "anthropic-provider"); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.secretStore.Create(secretstore.Secret{
		ID: "anthropic-key", UserID: "user-1", Provider: "anthropic", Value: "anthropic-key",
	}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := workspaceStore.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "anthropic-provider", UserID: "user-1", Name: "Anthropic", Type: "anthropic",
		BaseURL: modelAPI.URL + "/v1", Model: "claude-saved", SecretRef: "secret://anthropic-key", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	session := sessionstore.Session{
		ID: "session-anthropic-authority", Title: "Anthropic authority", WorkDir: root,
		CreatedAt: now, UpdatedAt: now, LastUserMessageAt: now, MessageCount: 1, LastRole: "user",
		Project: &sessionstore.Project{ID: "project-anthropic", Name: "Anthropic project", Path: root, BoundAt: now},
	}
	if err := srv.sessionStore.Save(session); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.eventJournal.Append(session.ID, eventjournal.Message{
		"type": "message", "role": "user", "text": "use saved Anthropic provider",
	}, eventjournal.Metadata{ClientMessageID: "user-anthropic"}); err != nil {
		t.Fatal(err)
	}

	result, err := srv.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{SessionID: session.ID, RunnerID: "anthropic-runner",
		Endpoint: "http://127.0.0.1:1/v1/chat/completions", APIKey: "wrong-static-key", Model: "wrong-static-model",
		LeaseTTL: time.Minute, ReplayLimit: 20, OutputLimitBytes: 64 * 1024,
		RequestTimeout: time.Minute, MaxAttempts: 1, MaxToolRounds: 1})
	if err != nil {
		t.Fatalf("RunSessionRunnerChatOnce() error = %v", err)
	}
	if !result.Claimed || result.Status != "completed" || result.AssistantEventID == 0 || requests != 1 {
		t.Fatalf("runner result=%+v requests=%d", result, requests)
	}
	entries, err := srv.eventJournal.ReadAfter(session.ID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if !hasJournalEntry(providerAuthorityEntriesToAny(entries), "message", "", "saved Anthropic response") {
		t.Fatalf("journal entries = %#v", entries)
	}
	audits, err := srv.runtimeStore.List(sessionRunnerModelAuditRuntimeNamespace)
	if err != nil {
		t.Fatal(err)
	}
	audit := findSessionRunnerModelAuditValue(audits, session.ID)
	if audit == nil || audit["protocol"] != "anthropic" || audit["requestId"] != "msg_saved_1" || audit["totalTokens"] != float64(13) {
		t.Fatalf("Anthropic audit = %#v", audit)
	}
}
