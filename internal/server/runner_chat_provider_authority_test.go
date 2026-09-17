package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	eventjournal "synon-go/internal/persistence/journal"
	runtimekv "synon-go/internal/persistence/runtimekv"
	secretstore "synon-go/internal/persistence/secrets"
	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/providers"
)

func TestSessionRunnerChatOnceUsesSavedProviderProfileAuthorityOverStaticOptions(t *testing.T) {
	longResponse := "saved profile response\n" + strings.Repeat("x", 60*1024)
	staticRequests := atomic.Int64{}
	staticAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		staticRequests.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	defer staticAPI.Close()

	savedRequests := atomic.Int64{}
	savedAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		savedRequests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("saved provider request = %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Header.Get("Authorization") != "Bearer saved-key" {
			t.Errorf("saved provider Authorization = %q", r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var request struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode saved provider request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if request.Model != "saved-model" {
			t.Errorf("saved provider model = %q", request.Model)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		last := request.Messages[len(request.Messages)-1]
		if last.Role != "user" || !strings.Contains(last.Content, "use saved authority") {
			t.Errorf("saved provider last message = %#v", last)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-request-id", "saved-req-1")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "saved-req-1",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": longResponse},
			}},
			"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5},
		})
	}))
	defer savedAPI.Close()

	root := t.TempDir()
	workspaceStore, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaceStore.Close() })
	srv := New(Options{FileRoot: root, Workspace: workspaceStore})
	if _, err := srv.workspaceStore.CreateProject(workspace.CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Saved provider project", Path: root}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.settingsStore.Set("model.activeProviderId", "saved-provider"); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.secretStore.Create(secretstore.Secret{ID: "saved-provider-key", UserID: "user-1", Provider: "openai", Value: "saved-key"}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := srv.workspaceStore.RegisterModelProvider(workspace.ModelProviderInput{
		ID:        "saved-provider",
		UserID:    "user-1",
		Name:      "Saved provider",
		Type:      "openai",
		BaseURL:   savedAPI.URL + "/v1",
		Model:     "saved-model",
		SecretRef: "secret://saved-provider-key",
		Enabled:   &enabled,
	}); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	session := sessionstore.Session{
		ID:                "session-provider-authority",
		Title:             "Provider authority",
		WorkDir:           root,
		CreatedAt:         now,
		UpdatedAt:         now,
		LastUserMessageAt: now,
		MessageCount:      1,
		LastRole:          "user",
		Project:           &sessionstore.Project{ID: "project-1", Name: "Saved provider project", Path: root, BoundAt: now},
	}
	if err := srv.sessionStore.Save(session); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.eventJournal.Append(session.ID, eventjournal.Message{"type": "message", "role": "user", "text": "use saved authority"}, eventjournal.Metadata{ClientMessageID: "user-1"}); err != nil {
		t.Fatal(err)
	}

	result, err := srv.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{SessionID: session.ID,
		RunnerID:    "saved-provider-runner",
		Endpoint:    staticAPI.URL + "/v1/chat/completions",
		APIKey:      "static-key",
		Model:       "static-model",
		LeaseTTL:    time.Minute,
		ReplayLimit: 20,
		// This is deliberately lower than the saved model response. It is the
		// tool-result budget and must not truncate model transport.
		OutputLimitBytes: 50 * 1024,
		RequestTimeout:   time.Minute,
		MaxAttempts:      2,
		MaxToolRounds:    1,
	})
	if err != nil {
		t.Fatalf("RunSessionRunnerChatOnce() error = %v", err)
	}
	if !result.Claimed || result.Status != "completed" || result.AssistantEventID == 0 || result.FinishEventID == 0 {
		t.Fatalf("runner result = %+v", result)
	}
	if staticRequests.Load() != 0 {
		t.Fatalf("static endpoint requests = %d", staticRequests.Load())
	}
	if savedRequests.Load() != 1 {
		t.Fatalf("saved endpoint requests = %d", savedRequests.Load())
	}
	entries, err := srv.eventJournal.ReadAfter(session.ID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if !hasJournalEntry(providerAuthorityEntriesToAny(entries), "message", "", longResponse) || !hasJournalEntry(providerAuthorityEntriesToAny(entries), "runner_finished", "completed", "") {
		t.Fatalf("journal entries = %#v", entries)
	}
	audits, err := srv.runtimeStore.List(sessionRunnerModelAuditRuntimeNamespace)
	if err != nil {
		t.Fatalf("runtimeStore.List(%s) error = %v", sessionRunnerModelAuditRuntimeNamespace, err)
	}
	audit := findSessionRunnerModelAuditValue(audits, session.ID)
	if audit == nil {
		t.Fatalf("missing provider authority audit: %#v", audits)
	}
	if audit["providerId"] != "saved-provider" || audit["requestId"] != "saved-req-1" || audit["totalTokens"] != float64(5) || audit["providerAuthority"] != true {
		t.Fatalf("provider authority audit = %#v", audit)
	}
}

func TestSessionRunnerChatOnceAuditsStaticProviderAuthorityForTaskMetrics(t *testing.T) {
	staticRequests := atomic.Int64{}
	staticAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		staticRequests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("static provider request = %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		if r.Header.Get("Authorization") != "Bearer static-key" {
			t.Errorf("static provider Authorization = %q", r.Header.Get("Authorization"))
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var request struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode static provider request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if request.Model != "static-model" || !request.Stream {
			t.Errorf("static provider request = %#v", request)
			http.Error(w, "static model request contract mismatch", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-request-id", "static-req-1")
		_, _ = w.Write([]byte("data: {\"id\":\"static-req-1\",\"model\":\"static-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"static authority \"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"static-req-1\",\"model\":\"static-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"audited\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":3,\"total_tokens\":14}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer staticAPI.Close()

	root := t.TempDir()
	workspaceStore, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaceStore.Close() })
	srv := New(Options{FileRoot: root, Workspace: workspaceStore})
	if _, err := workspaceStore.CreateProject(workspace.CreateProjectInput{
		ID: "project-static-authority", UserID: "user-static", Name: "Static authority", Path: root,
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	session := sessionstore.Session{
		ID: "session-static-authority", Title: "Static authority", WorkDir: root,
		CreatedAt: now, UpdatedAt: now, LastUserMessageAt: now, MessageCount: 1, LastRole: "user",
		Project: &sessionstore.Project{ID: "project-static-authority", Name: "Static authority", Path: root, BoundAt: now},
	}
	if err := srv.sessionStore.Save(session); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.eventJournal.Append(session.ID, eventjournal.Message{
		"type": "message", "role": "user", "text": "use the deployment-configured model",
	}, eventjournal.Metadata{ClientMessageID: "static-user-1"}); err != nil {
		t.Fatal(err)
	}

	result, err := srv.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{
		SessionID:               session.ID,
		RunnerID:                "static-authority-runner",
		Endpoint:                staticAPI.URL + "/v1/chat/completions",
		APIKey:                  "static-key",
		Model:                   "static-model",
		LeaseTTL:                time.Minute,
		ReplayLimit:             20,
		OutputLimitBytes:        64 * 1024,
		ModelResponseLimitBytes: 64 * 1024,
		RequestTimeout:          time.Minute,
		MaxAttempts:             1,
		MaxToolRounds:           1,
		DisableSkillDiscovery:   true,
	})
	if err != nil {
		t.Fatalf("RunSessionRunnerChatOnce() error = %v", err)
	}
	if !result.Claimed || result.Status != "completed" || staticRequests.Load() != 1 {
		t.Fatalf("runner result=%+v static requests=%d", result, staticRequests.Load())
	}
	audits, err := srv.runtimeStore.List(sessionRunnerModelAuditRuntimeNamespace)
	if err != nil {
		t.Fatal(err)
	}
	audit := findSessionRunnerModelAuditValue(audits, session.ID)
	if audit == nil || audit["providerId"] != "static-runner-config" ||
		audit["protocol"] != providers.ProtocolOpenAICompatible || audit["requestId"] != "static-req-1" ||
		audit["totalTokens"] != float64(14) || audit["providerAuthority"] != true {
		t.Fatalf("static provider authority audit = %#v", audit)
	}
	usage, err := aggregateFrameTaskModelUsage(audits, session.ID, []int64{int64(result.Attempt)})
	if err != nil {
		t.Fatal(err)
	}
	if usage.ModelCallCount != 1 || !usage.UsageAvailable || usage.InputTokens != 11 ||
		usage.OutputTokens != 3 || usage.TotalTokens != 14 {
		t.Fatalf("task-center usage = %#v", usage)
	}
}

func TestCompactModelSummaryUsesActiveWorkspaceProviderOverStaticConfig(t *testing.T) {
	staticRequests := atomic.Int64{}
	staticAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		staticRequests.Add(1)
		http.Error(w, "inactive compact provider must not be called", http.StatusTeapot)
	}))
	defer staticAPI.Close()

	arkRequests := atomic.Int64{}
	arkAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arkRequests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("ARK compact request = %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		if r.Header.Get("Authorization") != "Bearer ark-key" {
			t.Errorf("ARK compact Authorization = %q", r.Header.Get("Authorization"))
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var request struct {
			Model string `json:"model"`
			Tools []any  `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode ARK compact request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if request.Model != "ark-code-latest" || len(request.Tools) != 0 {
			t.Errorf("ARK compact request = %#v", request)
			http.Error(w, "unexpected request body", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"<summary>ARK compact summary.</summary>"}}]}`))
	}))
	defer arkAPI.Close()

	root := t.TempDir()
	workspaceStore, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaceStore.Close() })
	srv := New(Options{
		FileRoot:  root,
		Workspace: workspaceStore,
		CompactSummarizer: SessionRunnerChatOptions{Endpoint: staticAPI.URL + "/v1/chat/completions",
			APIKey:         "test-secret-inactive-deepseek-key",
			Model:          "deepseek-v4-flash",
			RequestTimeout: time.Second,
			MaxAttempts:    1,
		},
	})
	if _, err := workspaceStore.CreateProject(workspace.CreateProjectInput{
		ID: "project-ark-compact", UserID: "user-ark", Name: "ARK compact project", Path: root,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.settingsStore.Set("model.activeProviderId", "ark-provider"); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.secretStore.Create(secretstore.Secret{
		ID: "ark-key", UserID: "user-ark", Provider: "openai-compatible", Value: "ark-key",
	}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := workspaceStore.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "ark-provider", UserID: "user-ark", Name: "ARK", Type: "openai-compatible",
		BaseURL: arkAPI.URL + "/v1", Model: "ark-code-latest", SecretRef: "secret://ark-key", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	session := sessionstore.Session{
		ID: "session-ark-compact", Title: "ARK compact", WorkDir: root, CreatedAt: now, UpdatedAt: now,
		Project: &sessionstore.Project{ID: "project-ark-compact", Name: "ARK compact project", Path: root, BoundAt: now},
	}
	summary, metadata, err := srv.generateCompactModelSummary(context.Background(), session, []eventjournal.Entry{{
		SessionID: session.ID, EventID: 1, Message: eventjournal.Message{"type": "message", "role": "user", "text": "preserve this task"},
	}}, "continue without changing the task", "auto")
	if err != nil {
		t.Fatal(err)
	}
	if summary != "ARK compact summary." {
		t.Fatalf("summary = %q", summary)
	}
	if staticRequests.Load() != 0 || arkRequests.Load() != 1 {
		t.Fatalf("compact requests static=%d ARK=%d", staticRequests.Load(), arkRequests.Load())
	}
	if metadata["source"] != "model" || metadata["authority"] != "workspace-active-provider" ||
		metadata["providerId"] != "ark-provider" || metadata["model"] != "ark-code-latest" {
		t.Fatalf("compact metadata = %#v", metadata)
	}
}

func TestCompactModelSummaryDoesNotFallBackToStaticProviderWhenActiveAuthorityIsInvalid(t *testing.T) {
	staticRequests := atomic.Int64{}
	staticAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		staticRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"<summary>wrong provider</summary>"}}]}`))
	}))
	defer staticAPI.Close()

	root := t.TempDir()
	workspaceStore, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaceStore.Close() })
	srv := New(Options{
		FileRoot:          root,
		Workspace:         workspaceStore,
		CompactSummarizer: SessionRunnerChatOptions{Endpoint: staticAPI.URL + "/v1/chat/completions", Model: "deepseek-v4-flash", MaxAttempts: 1},
	})
	if _, err := workspaceStore.CreateProject(workspace.CreateProjectInput{
		ID: "project-invalid-active", UserID: "user-ark", Name: "Invalid active provider", Path: root,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.settingsStore.Set("model.activeProviderId", "missing-ark-provider"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	session := sessionstore.Session{
		ID: "session-invalid-active", WorkDir: root, CreatedAt: now, UpdatedAt: now,
		Project: &sessionstore.Project{ID: "project-invalid-active", Name: "Invalid active provider", Path: root, BoundAt: now},
	}
	_, _, err = srv.generateCompactModelSummary(context.Background(), session, nil, "preserve active authority", "auto")
	if err == nil || !strings.Contains(err.Error(), "missing-ark-provider") {
		t.Fatalf("error = %v, want active provider resolution failure", err)
	}
	if staticRequests.Load() != 0 {
		t.Fatalf("static compact provider requests = %d, want 0", staticRequests.Load())
	}
}

func TestSessionRunnerChatOnceInterruptsClaimWhenActiveProviderIsUnavailable(t *testing.T) {
	staticRequests := atomic.Int64{}
	staticAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		staticRequests.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	defer staticAPI.Close()

	root := t.TempDir()
	workspaceStore, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaceStore.Close() })
	srv := New(Options{FileRoot: root, Workspace: workspaceStore})
	if _, err := workspaceStore.CreateProject(workspace.CreateProjectInput{
		ID: "project-1", UserID: "user-1", Name: "Provider failure project", Path: root,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.settingsStore.Set("model.activeProviderId", "missing-provider"); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	session := sessionstore.Session{
		ID:                "session-provider-resolution-failure",
		Title:             "Provider resolution failure",
		WorkDir:           root,
		CreatedAt:         now,
		UpdatedAt:         now,
		LastUserMessageAt: now,
		MessageCount:      1,
		LastRole:          "user",
		Project:           &sessionstore.Project{ID: "project-1", Name: "Provider failure project", Path: root, BoundAt: now},
	}
	if err := srv.sessionStore.Save(session); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.eventJournal.Append(session.ID, eventjournal.Message{
		"type": "message", "role": "user", "text": "do not leave this claim running",
	}, eventjournal.Metadata{ClientMessageID: "user-failure"}); err != nil {
		t.Fatal(err)
	}

	result, err := srv.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{SessionID: session.ID, RunnerID: "provider-failure-runner",
		Endpoint: staticAPI.URL + "/v1/chat/completions", APIKey: "static-key", Model: "static-model",
		LeaseTTL: time.Minute, ReplayLimit: 20, OutputLimitBytes: 64 * 1024,
		RequestTimeout: time.Minute, MaxAttempts: 1, MaxToolRounds: 1,
	})
	if err != nil {
		t.Fatalf("RunSessionRunnerChatOnce() error = %v", err)
	}
	if !result.Claimed || result.Status != "interrupted" ||
		result.InterruptionReasonCode != sessionRunnerModelProviderUnavailableReasonCode ||
		result.FinishEventID != 0 || result.AssistantEventID != 0 {
		t.Fatalf("runner result = %+v", result)
	}
	if staticRequests.Load() != 0 {
		t.Fatalf("static endpoint requests = %d, want 0", staticRequests.Load())
	}
	persisted, found, err := srv.sessionStore.Get(session.ID)
	if err != nil || !found {
		t.Fatalf("persisted session found=%v err=%v", found, err)
	}
	if persisted.Runner != nil {
		t.Fatalf("persisted runner = %#v", persisted.Runner)
	}
	entries, err := srv.eventJournal.ReadAfter(session.ID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if !hasJournalEntry(providerAuthorityEntriesToAny(entries), "runner_checkpoint", "running", "runner interrupted; resumable lease released") {
		t.Fatalf("journal entries = %#v", entries)
	}
}

func providerAuthorityEntriesToAny(entries []eventjournal.Entry) []any {
	out := make([]any, 0, len(entries))
	for _, entry := range entries {
		out = append(out, map[string]any{"message": map[string]any(entry.Message)})
	}
	return out
}

func findSessionRunnerModelAuditValue(entries []runtimekv.Entry, sessionID string) map[string]any {
	for _, entry := range entries {
		value, ok := entry.Value.(map[string]any)
		if ok && value["sessionId"] == sessionID {
			return value
		}
	}
	return nil
}
