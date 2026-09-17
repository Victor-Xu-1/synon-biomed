package server

import (
	"net/http"
	"testing"
	"time"

	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
)

func TestWebConversationModelSwitchIsScopedAndDoesNotResetRunningTask(t *testing.T) {
	app, store := newP3WebConversationServer(t)
	project := createP3Project(t, store, "conversation-model-project", "local")
	frame, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "conversation-model-frame", ProjectID: project.ID, AgentName: "GENERAL",
		Status: "processing", ConversationType: "agent", Name: "Long scientific task",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFrameRuntimeMetadata(frame.ID, workspace.FrameRuntimeMetadata{
		ContextData: map[string]any{
			"task_marker": "preserve-me",
			"web_assistant": map[string]any{
				"id":                     "synonbiomed:operon",
				"conversation_overrides": map[string]any{"model": "ark-code-latest"},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	for _, provider := range []workspace.ModelProviderInput{
		{ID: "provider-mimo", UserID: "local", Name: "Mimo", Type: "openai-compatible", BaseURL: "https://models.example.test/v1", Model: "mimo-v2.5", Enabled: &enabled},
		{ID: "provider-deepseek", UserID: "local", Name: "DeepSeek", Type: "openai-compatible", BaseURL: "https://models.example.test/v1", Model: "deepseek-v4-flash", Enabled: &enabled},
		{ID: "provider-ark", UserID: "local", Name: "ARK", Type: "openai-compatible", BaseURL: "https://models.example.test/v1", Model: "ark-code-latest", Enabled: &enabled},
	} {
		if _, err := store.RegisterModelProvider(provider); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.settingsStore.Set(webConversationActiveProviderSetting, "provider-mimo"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := app.sessionStore.Save(sessionstore.Session{
		ID: frame.ID, Title: "Long scientific task", WorkDir: t.TempDir(), CreatedAt: now, UpdatedAt: now,
		Project: &sessionstore.Project{ID: project.ID, Name: project.Name, BoundAt: now},
		Runner:  &sessionstore.Runner{RunnerID: "runner-stable", Status: "running", Attempt: 7, ClaimedAt: now, LastHeartbeatAt: now, ExpiresAt: now.Add(time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}

	ensure := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/"+frame.ID+"/runtime/ensure", map[string]any{}, "")
	if ensure.Code != http.StatusOK {
		t.Fatalf("ensure status=%d body=%s", ensure.Code, ensure.Body.String())
	}
	ensureOptions, _ := p3DecodeObject(t, ensure)["config_options"].([]any)
	initial, _ := ensureOptions[0].(map[string]any)
	if initial["current_value"] != "ark-code-latest" {
		t.Fatalf("conversation did not inherit ARK: %#v", initial)
	}

	for _, model := range []string{"deepseek-v4-flash", "mimo-v2.5", "ark-code-latest"} {
		response := p3JSONRequest(t, app, http.MethodPut, "/api/conversations/"+frame.ID+"/config-options/model", map[string]any{"value": model}, "")
		if response.Code != http.StatusOK {
			t.Fatalf("switch %s status=%d body=%s", model, response.Code, response.Body.String())
		}
		payload := p3DecodeObject(t, response)
		if payload["confirmation"] != "observed" || payload["applies_to"] != "next_model_call" {
			t.Fatalf("switch %s payload=%#v", model, payload)
		}
		storedFrame, found, err := store.GetCompatibilityFrame(frame.ID)
		if err != nil || !found || storedFrame.Status != "processing" || storedFrame.ID != frame.ID {
			t.Fatalf("switch %s changed frame: found=%v frame=%#v err=%v", model, found, storedFrame, err)
		}
		storedSession, found, err := app.sessionStore.Get(frame.ID)
		if err != nil || !found || storedSession.Runner == nil || storedSession.Runner.RunnerID != "runner-stable" || storedSession.Runner.Attempt != 7 || storedSession.Runner.Status != "running" {
			t.Fatalf("switch %s changed runner: found=%v session=%#v err=%v", model, found, storedSession, err)
		}
		setting, found, err := app.settingsStore.Get(webConversationActiveProviderSetting)
		if err != nil || !found || setting.Value != "provider-mimo" {
			t.Fatalf("switch %s changed global provider: found=%v value=%#v err=%v", model, found, setting.Value, err)
		}
	}
	metadata, found, err := store.GetFrameRuntimeMetadata(frame.ID)
	if err != nil || !found || metadata.ContextData["task_marker"] != "preserve-me" {
		t.Fatalf("task metadata changed: found=%v metadata=%#v err=%v", found, metadata, err)
	}
}
