package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestCompatibilitySessionConfigPersistsMirrorsAndSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "frame", ProjectID: "project", AgentName: "OPERON", Status: "completed", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{FileRoot: root, Workspace: store})
	app := server.Handler()
	response := compatJSONRequest(t, app, http.MethodPost, "/api/frames/frame/session-config", "local", map[string]any{
		"verifier_mode": "on", "memory_mode": "off", "auto_mode": "on",
		"reviewer_model": nil, "rc_context_ceiling": 200000,
		"python_version": "3.11", "kernel_idle_timeout": 60,
		"async_local_exec_wallclock_cap_s": 0, "goal_text": nil,
	}, http.StatusOK)
	if response["root_frame_id"] != "frame" || response["status"] != "ok" ||
		response["verifier_mode"] != "on" || response["reviewer_model"] != nil || response["rc_context_ceiling"] != float64(200000) {
		t.Fatalf("session config response = %#v", response)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata("frame")
	if err != nil || !found {
		t.Fatalf("metadata found=%t err=%v", found, err)
	}
	stored := metadata.ContextData["_original_input"].(map[string]any)
	if stored["python_version"] != "3.11" || stored["kernel_idle_timeout"] != float64(60) ||
		stored["async_local_exec_wallclock_cap_s"] != float64(0) || stored["goal_text"] != nil {
		t.Fatalf("stored session config = %#v", stored)
	}
	session, found, err := server.sessionStore.Get("frame")
	if err != nil || !found {
		t.Fatalf("runner session found=%t err=%v", found, err)
	}
	runnerConfig := session.Orchestration["sessionConfig"].(map[string]any)
	if runnerConfig["verifier_mode"] != "on" || runnerConfig["rc_context_ceiling"] != float64(200000) {
		t.Fatalf("runner session config = %#v", runnerConfig)
	}
	events, err := store.ListRealtimeEvents(workspace.RealtimeEventFilter{UserID: "local", ProjectID: "project", Type: "frame_update", Limit: 10})
	if err != nil || len(events) != 1 || events[0].Payload["action"] != "session_config_updated" {
		t.Fatalf("session config events = %#v, err=%v", events, err)
	}

	patch := compatJSONRequest(t, app, http.MethodPost, "/api/frames/frame/session-config", "local", map[string]any{
		"reviewer_model": "reviewer-model", "rc_context_ceiling": nil,
		"python_version": nil, "kernel_idle_timeout": nil,
	}, http.StatusOK)
	if patch["reviewer_model"] != "reviewer-model" || patch["rc_context_ceiling"] != nil || patch["python_version"] != nil {
		t.Fatalf("nullable session patch = %#v", patch)
	}
	compatJSONRequest(t, app, http.MethodPost, "/api/frames/frame/session-config", "local", map[string]any{"gpu_mode": "on"}, http.StatusBadRequest)
	compatJSONRequest(t, app, http.MethodPost, "/api/frames/frame/session-config", "local", map[string]any{}, http.StatusBadRequest)

	restarted := New(Options{FileRoot: root, Workspace: store}).Handler()
	configResponse := httptest.NewRecorder()
	configRequest := newLoopbackTestRequest(http.MethodGet, "/api/go/sessions/frame/config", nil)
	configRequest.Header.Set("X-Synon-User-Id", "local")
	restarted.ServeHTTP(configResponse, configRequest)
	if configResponse.Code != http.StatusOK {
		t.Fatalf("restart config status = %d: %s", configResponse.Code, configResponse.Body.String())
	}
	config := compatJSONRequest(t, restarted, http.MethodGet, "/api/go/sessions/frame/config", "local", nil, http.StatusOK)["config"].(map[string]any)
	if config["verifier_mode"] != "on" || config["reviewer_model"] != "reviewer-model" || config["rc_context_ceiling"] != nil {
		t.Fatalf("restarted runner config = %#v", config)
	}
}

func TestDecodeCompatibilitySessionConfigRejectsInvalidValues(t *testing.T) {
	tests := []map[string]any{
		{"verifier_mode": "maybe"},
		{"reviewer_model": ""},
		{"rc_context_ceiling": 199999},
		{"python_version": "2.7"},
		{"kernel_idle_timeout": 59},
		{"async_local_exec_wallclock_cap_s": -1},
		{"async_local_exec_wallclock_cap_s": 2000001},
		{"goal_text": "not-enabled"},
	}
	for _, body := range tests {
		request := compatRequest(t, http.MethodPost, "/api/frames/frame/session-config", "local", body)
		if _, err := decodeCompatibilitySessionConfig(request); err == nil {
			t.Fatalf("invalid session config accepted: %#v", body)
		}
	}
}
