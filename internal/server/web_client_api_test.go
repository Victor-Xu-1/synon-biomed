package server

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestWebConversationRuntimeMapsTerminalFrameStatusesExplicitly(t *testing.T) {
	tests := []struct {
		frameStatus string
		wantStatus  string
	}{
		{frameStatus: "completed", wantStatus: "finished"},
		{frameStatus: "failed", wantStatus: "error"},
		{frameStatus: "cancelled", wantStatus: "cancelled"},
		{frameStatus: "canceled", wantStatus: "cancelled"},
	}
	for _, test := range tests {
		t.Run(test.frameStatus, func(t *testing.T) {
			frame := workspace.CompatibilityFrame{Frame: workspace.Frame{
				ID: "frame-terminal-runtime", ProjectID: "project-terminal-runtime",
				RootFrameID: "frame-terminal-runtime", Status: test.frameStatus,
			}}
			runtime := webConversationRuntime(frame)
			if runtime["state"] != "idle" || runtime["task_status"] != test.wantStatus ||
				runtime["can_send_message"] != true || runtime["has_task"] != false ||
				runtime["is_processing"] != false || runtime["pending_confirmations"] != 0 ||
				runtime["turn_id"] != nil {
				t.Fatalf("runtime=%#v", runtime)
			}
			conversation := webConversation(frame, "Project")
			if conversation["status"] != test.wantStatus {
				t.Fatalf("conversation=%#v", conversation)
			}
		})
	}
}

func TestWebConversationRuntimeSnapshotProjectsSettledFramesWithoutStorageReads(t *testing.T) {
	app := &Server{}
	for _, test := range []struct {
		status     string
		taskStatus string
	}{
		{status: "completed", taskStatus: "finished"},
		{status: "failed", taskStatus: "error"},
	} {
		t.Run(test.status, func(t *testing.T) {
			frame := workspace.CompatibilityFrame{Frame: workspace.Frame{
				ID: "frame-settled-" + test.status, ProjectID: "project-settled",
				RootFrameID: "frame-settled-" + test.status, Status: test.status,
			}}
			runtime, err := app.webConversationRuntimeSnapshot(frame)
			if err != nil {
				t.Fatal(err)
			}
			if runtime["state"] != "idle" || runtime["task_status"] != test.taskStatus ||
				runtime["has_task"] != false || runtime["is_processing"] != false {
				t.Fatalf("runtime=%#v", runtime)
			}
		})
	}
}

func TestWebConversationRuntimeIgnoresStalePendingInputsWhileRunnerIsActive(t *testing.T) {
	store, repository, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-active-pending", "frame-active-pending")
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:frame-active-pending", OwnerID: "local", ExternalID: "frame-active-pending",
		SessionID: "frame-active-pending", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-active-pending", RootFrameID: "frame-active-pending",
		FrameID: "frame-active-pending", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		ClientMessageID: "active-pending-user", PayloadJSON: []byte(`{"role":"user","text":"continue"}`),
	}); err != nil {
		t.Fatal(err)
	}
	claim, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		RunnerID: "active-pending-runner", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim runner=%#v err=%v", claim, err)
	}
	if _, err := store.SetFrameRuntimeMetadata("frame-active-pending", workspace.FrameRuntimeMetadata{
		FrameID: "frame-active-pending",
		ContextData: map[string]any{"_pending_input_requests": []any{
			map[string]any{"tool_id": "stale-question", "kind": "ask"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	frame, found, err := store.GetCompatibilityFrame("frame-active-pending")
	if err != nil || !found {
		t.Fatalf("load frame found=%t err=%v", found, err)
	}
	app := New(Options{Workspace: store, Transcript: repository})
	runtime, err := app.webConversationRuntimeSnapshot(frame)
	if err != nil {
		t.Fatal(err)
	}
	if runtime["state"] != "running" || runtime["task_status"] != "running" ||
		runtime["is_processing"] != true || runtime["pending_confirmations"] != 0 ||
		runtime["can_send_message"] != false {
		t.Fatalf("active runtime was downgraded by stale pending input: %#v", runtime)
	}
}

func TestWebConversationRuntimeProjectsNonResumableCancelledFrameAsPaused(t *testing.T) {
	store, repository, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-paused-conversation", "frame-paused-conversation")
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:frame-paused-conversation", OwnerID: "local", ExternalID: "frame-paused-conversation",
		SessionID: "frame-paused-conversation", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-paused-conversation", RootFrameID: "frame-paused-conversation",
		FrameID: "frame-paused-conversation", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		ClientMessageID: "paused-conversation-user", PayloadJSON: []byte(`{"role":"user","text":"pause this run"}`),
	}); err != nil {
		t.Fatal(err)
	}
	claim, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		RunnerID: "paused-conversation-runner", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim runner=%#v err=%v", claim, err)
	}
	_, _, created, err := repository.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claim.Claim, ClientMessageID: "paused-conversation-finished", Status: "cancelled",
		PayloadJSON: []byte(`{"status":"cancelled","reason_code":"user_cancelled"}`), Destinations: []string{"ws"},
	})
	if err != nil || !created {
		t.Fatalf("finish runner created=%t err=%v", created, err)
	}
	cancelled := "cancelled"
	if _, err := store.UpdateFrame("frame-paused-conversation", workspace.UpdateFrameInput{Status: &cancelled}); err != nil {
		t.Fatal(err)
	}
	frame, found, err := store.GetCompatibilityFrame("frame-paused-conversation")
	if err != nil || !found {
		t.Fatalf("load frame found=%t err=%v", found, err)
	}
	app := New(Options{Workspace: store, Transcript: repository})
	runtime, err := app.webConversationRuntimeSnapshot(frame)
	if err != nil {
		t.Fatal(err)
	}
	if runtime["state"] != "paused" || runtime["can_send_message"] != true ||
		runtime["has_task"] != true || runtime["task_status"] != "pending" ||
		runtime["is_processing"] != false || runtime["pending_confirmations"] != 0 || runtime["turn_id"] != frame.ID {
		t.Fatalf("paused runtime=%#v", runtime)
	}
	ensured := runtimeCompatJSON(
		t, app.Handler(), http.MethodPost,
		"/api/conversations/"+frame.ID+"/runtime/ensure", "local", map[string]any{}, http.StatusOK,
	)
	ensuredRuntime, _ := ensured["runtime"].(map[string]any)
	if ensuredRuntime["state"] != "paused" || ensuredRuntime["can_send_message"] != true ||
		ensuredRuntime["has_task"] != true || ensuredRuntime["task_status"] != "pending" ||
		ensuredRuntime["is_processing"] != false || ensuredRuntime["pending_confirmations"] != float64(0) ||
		ensuredRuntime["turn_id"] != frame.ID {
		t.Fatalf("paused runtime ensure=%#v", ensured)
	}
	conversation, err := app.webConversationSnapshot(frame, "Paused conversation")
	if err != nil {
		t.Fatal(err)
	}
	if conversation["status"] != "pending" {
		t.Fatalf("paused conversation=%#v", conversation)
	}
}

func TestWebConversationRuntimeProjectsMissingModelAsRecoverableInput(t *testing.T) {
	store, repository, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-model-wait", "frame-model-wait")
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:frame-model-wait", OwnerID: "local", ExternalID: "frame-model-wait",
		SessionID: "frame-model-wait", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-model-wait", RootFrameID: "frame-model-wait",
		FrameID: "frame-model-wait", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		ClientMessageID: "model-wait-user", PayloadJSON: []byte(`{"role":"user","text":"run the task"}`),
	}); err != nil {
		t.Fatal(err)
	}
	claim, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		RunnerID: "model-wait-runner", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim runner=%#v err=%v", claim, err)
	}
	interrupted, err := repository.InterruptRunner(context.Background(), transcriptstore.InterruptRunnerInput{
		Claim: claim.Claim, ClientMessageID: "model-wait-interrupted",
		ReasonCode:   sessionRunnerModelProviderUnavailableReasonCode,
		ResumeDetail: "Configure or select a model, then continue this same task.",
		Resumable:    true, AutoResume: false, Destinations: []string{"ws"},
	})
	if err != nil || !interrupted.Created {
		t.Fatalf("interrupt runner=%#v err=%v", interrupted, err)
	}
	frame, found, err := store.GetCompatibilityFrame("frame-model-wait")
	if err != nil || !found {
		t.Fatalf("load frame found=%t err=%v", found, err)
	}
	app := New(Options{Workspace: store, Transcript: repository})
	runtime, err := app.webConversationRuntimeSnapshot(frame)
	if err != nil {
		t.Fatal(err)
	}
	if runtime["state"] != "waiting_input" || runtime["can_send_message"] != true ||
		runtime["has_task"] != true || runtime["task_status"] != "pending" ||
		runtime["is_processing"] != false || runtime["pending_confirmations"] != 0 ||
		runtime["turn_id"] != frame.ID {
		t.Fatalf("model configuration wait runtime=%#v", runtime)
	}
	conversation, err := app.webConversationSnapshot(frame, "Model wait")
	if err != nil {
		t.Fatal(err)
	}
	if conversation["status"] != "pending" {
		t.Fatalf("model configuration wait conversation=%#v", conversation)
	}
}

func TestWebConversationRuntimeReportsTerminalCorrectionAsInternalFailure(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, _, err := store.CreateCompatibilityProject(workspace.CreateCompatibilityProjectInput{
		ID: "project-repair-runtime", UserID: "local", Name: "Repair Runtime",
	}); err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-repair-runtime", ProjectID: "project-repair-runtime", AgentName: "OPERON",
		Status: "processing", ConversationType: "agent", Name: "Repair checkpoint",
	})
	if err != nil {
		t.Fatal(err)
	}
	resume, err := store.CreateAutoResumeDispatch(frame.ID, frame.RootFrameID, frame.ProjectID, frame.AgentName, "artifact_reference_correction_required")
	if err != nil || resume.Event == nil {
		t.Fatalf("create resume dispatch: event=%#v err=%v", resume.Event, err)
	}
	claimed, ok, err := store.ClaimNextCompatibilityFrameResumeDispatch("repair-runtime-test", time.Second)
	if err != nil || !ok {
		t.Fatalf("claim resume dispatch: claimed=%#v ok=%v err=%v", claimed, ok, err)
	}
	if _, _, err := store.FailCompatibilityFrameResumeDispatch(
		claimed.ResumeEvent.ID, claimed.Attempt, claimed.ClaimToken, "artifact_reference_correction_required",
	); err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store})
	compatibilityFrame, found, err := store.GetCompatibilityFrame(frame.ID)
	if err != nil || !found {
		t.Fatalf("load compatibility frame: found=%v err=%v", found, err)
	}
	runtime, err := app.webConversationRuntimeSnapshot(compatibilityFrame)
	if err != nil {
		t.Fatal(err)
	}
	if runtime["state"] != "idle" || runtime["can_send_message"] != true ||
		runtime["is_processing"] != false || runtime["has_task"] != false || runtime["task_status"] != "error" {
		t.Fatalf("repair runtime=%#v", runtime)
	}
}

func TestWebConversationRuntimeStopsSpinningAfterBoundedRunnerExhaustion(t *testing.T) {
	for _, reasonCode := range []string{
		sessionRunnerToolRoundNoProgressExhaustedReasonCode,
		sessionRunnerToolRoundLimitReasonCode,
	} {
		t.Run(reasonCode, func(t *testing.T) {
			store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			if _, _, err := store.CreateCompatibilityProject(workspace.CreateCompatibilityProjectInput{
				ID: "project-bounded-runner", UserID: "local", Name: "Bounded Runner",
			}); err != nil {
				t.Fatal(err)
			}
			frame, err := store.CreateFrame(workspace.CreateFrameInput{
				ID: "frame-bounded-runner", ProjectID: "project-bounded-runner", AgentName: "OPERON",
				Status: "processing", ConversationType: "agent", Name: "Bounded runner checkpoint",
			})
			if err != nil {
				t.Fatal(err)
			}
			resume, err := store.CreateAutoResumeDispatch(frame.ID, frame.RootFrameID, frame.ProjectID, frame.AgentName, reasonCode)
			if err != nil || resume.Event == nil {
				t.Fatalf("create resume dispatch: event=%#v err=%v", resume.Event, err)
			}
			claimed, ok, err := store.ClaimNextCompatibilityFrameResumeDispatch("bounded-runner-test", time.Second)
			if err != nil || !ok {
				t.Fatalf("claim resume dispatch: claimed=%#v ok=%v err=%v", claimed, ok, err)
			}
			if _, _, err := store.FailCompatibilityFrameResumeDispatch(
				claimed.ResumeEvent.ID, claimed.Attempt, claimed.ClaimToken, reasonCode,
			); err != nil {
				t.Fatal(err)
			}
			app := New(Options{Workspace: store})
			compatibilityFrame, found, err := store.GetCompatibilityFrame(frame.ID)
			if err != nil || !found {
				t.Fatalf("load compatibility frame: found=%v err=%v", found, err)
			}
			runtime, err := app.webConversationRuntimeSnapshot(compatibilityFrame)
			if err != nil {
				t.Fatal(err)
			}
			if runtime["state"] != "idle" || runtime["can_send_message"] != true ||
				runtime["is_processing"] != false || runtime["has_task"] != false ||
				runtime["task_status"] != "error" {
				t.Fatalf("bounded runner runtime=%#v", runtime)
			}
		})
	}
}

func TestWebConversationRuntimeStopsSpinningAfterExpiredSelectedSkillInterruption(t *testing.T) {
	store, repository, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-expired-skill-selection", "frame-expired-skill-selection")
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:frame-expired-skill-selection", OwnerID: "local", ExternalID: "frame-expired-skill-selection",
		SessionID: "frame-expired-skill-selection", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-expired-skill-selection", RootFrameID: "frame-expired-skill-selection",
		FrameID: "frame-expired-skill-selection", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		ClientMessageID: "expired-skill-selection-user", PayloadJSON: []byte(`{"role":"user","text":"continue"}`),
	}); err != nil {
		t.Fatal(err)
	}
	claim, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		RunnerID: "expired-skill-selection-runner", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim runner=%#v err=%v", claim, err)
	}
	interrupted, err := repository.InterruptRunner(context.Background(), transcriptstore.InterruptRunnerInput{
		Claim: claim.Claim, ClientMessageID: "expired-skill-selection-interrupted",
		ReasonCode:   sessionRunnerSelectedSkillContractUnavailableReasonCode,
		ResumeDetail: "the explicit selection contains 67 skills; the maximum is 64",
		AutoResume:   false,
	})
	if err != nil || !interrupted.Created || interrupted.Checkpoint.Phase != transcriptstore.RunnerPhasePlanning {
		t.Fatalf("interrupt runner=%#v err=%v", interrupted, err)
	}
	if _, err := db.Exec(`UPDATE transcript_runner_attempts SET expires_at=? WHERE stream_uid=? AND attempt=?`,
		time.Now().UTC().Add(-time.Second), claim.Claim.StreamUID, claim.Claim.Attempt); err != nil {
		t.Fatal(err)
	}

	app := New(Options{Workspace: store, Transcript: repository})
	frame, found, err := store.GetCompatibilityFrame("frame-expired-skill-selection")
	if err != nil || !found {
		t.Fatalf("load compatibility frame: found=%v err=%v", found, err)
	}
	runtime, err := app.webConversationRuntimeSnapshot(frame)
	if err != nil {
		t.Fatal(err)
	}
	if runtime["state"] != "idle" || runtime["can_send_message"] != true ||
		runtime["is_processing"] != false || runtime["has_task"] != false ||
		runtime["task_status"] != "error" {
		t.Fatalf("expired selected-skill runtime=%#v", runtime)
	}
	conversation, err := app.webConversationSnapshot(frame, "Expired Skill Selection")
	if err != nil {
		t.Fatal(err)
	}
	projectedRuntime, _ := conversation["runtime"].(map[string]any)
	if conversation["status"] != "error" || projectedRuntime["is_processing"] != false {
		t.Fatalf("expired selected-skill conversation=%#v", conversation)
	}
}

func TestWebClientSettingsPersistFilterAndProtectBusinessValues(t *testing.T) {
	app := New(Options{FileRoot: t.TempDir()})
	put := httptest.NewRequest(http.MethodPut, "/api/settings/client", strings.NewReader(`{"language":"zh-CN","google.config":{"proxy":"private"}}`))
	put.RemoteAddr = "127.0.0.1:1234"
	putResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(putResponse, put)
	if putResponse.Code != http.StatusOK {
		t.Fatalf("put status=%d body=%s", putResponse.Code, putResponse.Body.String())
	}

	public := httptest.NewRequest(http.MethodGet, "/api/settings/client?scope=ui-preferences", nil)
	public.RemoteAddr = "127.0.0.1:1234"
	publicResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(publicResponse, public)
	if publicResponse.Code != http.StatusOK || !strings.Contains(publicResponse.Body.String(), `"language":"zh-CN"`) || strings.Contains(publicResponse.Body.String(), "google.config") {
		t.Fatalf("public status=%d body=%s", publicResponse.Code, publicResponse.Body.String())
	}

	filtered := httptest.NewRequest(http.MethodGet, "/api/settings/client?keys=google.config", nil)
	filtered.RemoteAddr = "127.0.0.1:1234"
	filteredResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(filteredResponse, filtered)
	if filteredResponse.Code != http.StatusOK || !strings.Contains(filteredResponse.Body.String(), "private") {
		t.Fatalf("filtered status=%d body=%s", filteredResponse.Code, filteredResponse.Body.String())
	}

	remote := httptest.NewRequest(http.MethodGet, "/api/settings/client", nil)
	remote.RemoteAddr = "203.0.113.9:1234"
	remoteResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(remoteResponse, remote)
	if remoteResponse.Code != http.StatusUnauthorized {
		t.Fatalf("remote status=%d body=%s", remoteResponse.Code, remoteResponse.Body.String())
	}
}

func TestWebConversationsMapsDurableRootFrames(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, _, err := store.CreateCompatibilityProject(workspace.CreateCompatibilityProjectInput{ID: "project-1", UserID: "local", Name: "Project One"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "frame-1", ProjectID: "project-1", AgentName: "OPERON", Status: "running", ConversationType: "task", Name: "Live task"}); err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store})
	request := httptest.NewRequest(http.MethodGet, "/api/conversations?limit=100", nil)
	request.RemoteAddr = "127.0.0.1:1234"
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Total != 1 || len(payload.Items) != 1 || payload.Items[0]["name"] != "Live task" {
		t.Fatalf("payload=%#v", payload)
	}
}

func TestWebConversationsListsVisibleRootsWithProjectNames(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, project := range []workspace.CreateCompatibilityProjectInput{
		{ID: "project-a", UserID: "local", Name: "Project A"},
		{ID: "project-b", UserID: "local", Name: "Project B"},
		{ID: "project-foreign", UserID: "other", Name: "Foreign"},
	} {
		if _, _, err := store.CreateCompatibilityProject(project); err != nil {
			t.Fatal(err)
		}
	}
	for _, frame := range []workspace.CreateFrameInput{
		{ID: "root-a", ProjectID: "project-a", AgentName: "OPERON", Status: "running", ConversationType: "task", Name: "Task A"},
		{ID: "root-b", ProjectID: "project-b", AgentName: "OPERON", Status: "completed", ConversationType: "task", Name: "Task B"},
		{ID: "child-a", ProjectID: "project-a", ParentFrameID: "root-a", AgentName: "OPERON", Status: "completed", ConversationType: "task", Name: "Child"},
		{ID: "foreign", ProjectID: "project-foreign", AgentName: "OPERON", Status: "running", ConversationType: "task", Name: "Foreign task"},
	} {
		if _, err := store.CreateFrame(frame); err != nil {
			t.Fatal(err)
		}
	}

	app := New(Options{Workspace: store})
	request := httptest.NewRequest(http.MethodGet, "/api/conversations?limit=100", nil)
	request.RemoteAddr = "127.0.0.1:1234"
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Items []struct {
			ID    string `json:"id"`
			Extra struct {
				ProjectName string `json:"project_name"`
			} `json:"extra"`
		} `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 2 {
		t.Fatalf("items=%#v", payload.Items)
	}
	projects := map[string]string{}
	for _, item := range payload.Items {
		projects[item.ID] = item.Extra.ProjectName
	}
	if projects["root-a"] != "Project A" || projects["root-b"] != "Project B" {
		t.Fatalf("projects=%#v", projects)
	}
}

func TestWebConversationsUsesStableKeysetPagination(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, _, err := store.CreateCompatibilityProject(workspace.CreateCompatibilityProjectInput{
		ID: "project", UserID: "local", Name: "Project",
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"frame-a", "frame-b", "frame-c"} {
		if _, err := store.CreateFrame(workspace.CreateFrameInput{
			ID: id, ProjectID: "project", AgentName: "OPERON", Status: "completed", ConversationType: "task", Name: id,
		}); err != nil {
			t.Fatal(err)
		}
	}
	app := New(Options{Workspace: store}).Handler()

	firstRequest := httptest.NewRequest(http.MethodGet, "/api/conversations?limit=2", nil)
	firstRequest.RemoteAddr = "127.0.0.1:1234"
	firstResponse := httptest.NewRecorder()
	app.ServeHTTP(firstResponse, firstRequest)
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", firstResponse.Code, firstResponse.Body.String())
	}
	var first struct {
		Items      []map[string]any `json:"items"`
		HasMore    bool             `json:"has_more"`
		NextCursor string           `json:"next_cursor"`
	}
	if err := json.Unmarshal(firstResponse.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || !first.HasMore || first.NextCursor == "" {
		t.Fatalf("first=%#v", first)
	}
	etag := firstResponse.Header().Get("ETag")
	if etag == "" || firstResponse.Header().Get("Cache-Control") != "private, no-cache" ||
		!strings.HasPrefix(firstResponse.Header().Get("Server-Timing"), "conversation_list;dur=") {
		t.Fatalf("etag=%q cache-control=%q timing=%q", etag,
			firstResponse.Header().Get("Cache-Control"), firstResponse.Header().Get("Server-Timing"))
	}
	cachedRequest := httptest.NewRequest(http.MethodGet, "/api/conversations?limit=2", nil)
	cachedRequest.RemoteAddr = "127.0.0.1:1234"
	cachedRequest.Header.Set("If-None-Match", etag)
	cachedResponse := httptest.NewRecorder()
	app.ServeHTTP(cachedResponse, cachedRequest)
	if cachedResponse.Code != http.StatusNotModified || cachedResponse.Body.Len() != 0 {
		t.Fatalf("cached status=%d body=%q", cachedResponse.Code, cachedResponse.Body.String())
	}

	secondRequest := httptest.NewRequest(
		http.MethodGet, "/api/conversations?limit=2&cursor="+first.NextCursor, nil,
	)
	secondRequest.RemoteAddr = "127.0.0.1:1234"
	secondResponse := httptest.NewRecorder()
	app.ServeHTTP(secondResponse, secondRequest)
	if secondResponse.Code != http.StatusOK {
		t.Fatalf("second status=%d body=%s", secondResponse.Code, secondResponse.Body.String())
	}
	var second struct {
		Items   []map[string]any `json:"items"`
		HasMore bool             `json:"has_more"`
	}
	if err := json.Unmarshal(secondResponse.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.HasMore || second.Items[0]["id"] == first.Items[0]["id"] ||
		second.Items[0]["id"] == first.Items[1]["id"] {
		t.Fatalf("first=%#v second=%#v", first.Items, second)
	}

	invalid := httptest.NewRequest(http.MethodGet, "/api/conversations?cursor=not-a-cursor", nil)
	invalid.RemoteAddr = "127.0.0.1:1234"
	invalidResponse := httptest.NewRecorder()
	app.ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid status=%d body=%s", invalidResponse.Code, invalidResponse.Body.String())
	}
}

func TestPrivateRevalidatedWorkspaceJSONScopesValidatorToOwner(t *testing.T) {
	payload := map[string]any{"items": []any{map[string]any{"id": "same-body"}}}
	firstRequest := httptest.NewRequest(http.MethodGet, "/api/conversations", nil)
	first := httptest.NewRecorder()
	writePrivateRevalidatedWorkspaceJSON(first, firstRequest, "owner-a", payload)
	if first.Code != http.StatusOK || first.Header().Get("ETag") == "" {
		t.Fatalf("first status=%d etag=%q", first.Code, first.Header().Get("ETag"))
	}

	foreignRequest := httptest.NewRequest(http.MethodGet, "/api/conversations", nil)
	foreignRequest.Header.Set("If-None-Match", first.Header().Get("ETag"))
	foreign := httptest.NewRecorder()
	writePrivateRevalidatedWorkspaceJSON(foreign, foreignRequest, "owner-b", payload)
	if foreign.Code != http.StatusOK || foreign.Header().Get("ETag") == first.Header().Get("ETag") {
		t.Fatalf("foreign status=%d etag=%q", foreign.Code, foreign.Header().Get("ETag"))
	}

	cachedRequest := httptest.NewRequest(http.MethodGet, "/api/conversations", nil)
	cachedRequest.Header.Set("If-None-Match", "W/"+first.Header().Get("ETag"))
	cached := httptest.NewRecorder()
	writePrivateRevalidatedWorkspaceJSON(cached, cachedRequest, "owner-a", payload)
	if cached.Code != http.StatusNotModified || cached.Body.Len() != 0 {
		t.Fatalf("cached status=%d body=%q", cached.Code, cached.Body.String())
	}
}

func TestPrivateRevalidatedWorkspaceJSONCompressesLargePayloadByRepresentation(t *testing.T) {
	payload := map[string]any{"items": []any{map[string]any{"content": strings.Repeat("scientific-result-", 4096)}}}
	request := httptest.NewRequest(http.MethodGet, "/api/conversations/frame/messages", nil)
	request.Header.Set("Accept-Encoding", "br, gzip")
	response := httptest.NewRecorder()
	writePrivateRevalidatedWorkspaceJSON(response, request, "owner-a", payload)
	if response.Code != http.StatusOK || response.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("status=%d encoding=%q", response.Code, response.Header().Get("Content-Encoding"))
	}
	reader, err := gzip.NewReader(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(reader)
	if closeErr := reader.Close(); err == nil {
		err = closeErr
	}
	if err != nil || !strings.Contains(string(decoded), "scientific-result-") {
		t.Fatalf("decoded=%d err=%v", len(decoded), err)
	}
	gzipETag := response.Header().Get("ETag")
	if gzipETag == "" || !strings.Contains(strings.Join(response.Header().Values("Vary"), ","), "Accept-Encoding") {
		t.Fatalf("etag=%q vary=%q", gzipETag, response.Header().Values("Vary"))
	}

	cachedRequest := httptest.NewRequest(http.MethodGet, "/api/conversations/frame/messages", nil)
	cachedRequest.Header.Set("Accept-Encoding", "gzip")
	cachedRequest.Header.Set("If-None-Match", gzipETag)
	cached := httptest.NewRecorder()
	writePrivateRevalidatedWorkspaceJSON(cached, cachedRequest, "owner-a", payload)
	if cached.Code != http.StatusNotModified || cached.Body.Len() != 0 {
		t.Fatalf("cached status=%d body=%q", cached.Code, cached.Body.String())
	}

	identityRequest := httptest.NewRequest(http.MethodGet, "/api/conversations/frame/messages", nil)
	identityRequest.Header.Set("If-None-Match", gzipETag)
	identity := httptest.NewRecorder()
	writePrivateRevalidatedWorkspaceJSON(identity, identityRequest, "owner-a", payload)
	if identity.Code != http.StatusOK || identity.Header().Get("Content-Encoding") != "" ||
		identity.Header().Get("ETag") == gzipETag {
		t.Fatalf("identity status=%d encoding=%q etag=%q", identity.Code,
			identity.Header().Get("Content-Encoding"), identity.Header().Get("ETag"))
	}
}

func TestPrivateRevalidatedWorkspaceJSONHonorsExplicitGzipDisableAfterWildcard(t *testing.T) {
	payload := map[string]any{"items": []any{map[string]any{"content": strings.Repeat("scientific-result-", 4096)}}}
	request := httptest.NewRequest(http.MethodGet, "/api/conversations/frame/messages", nil)
	request.Header.Set("Accept-Encoding", "*;q=1, gzip;q=0")
	response := httptest.NewRecorder()
	writePrivateRevalidatedWorkspaceJSON(response, request, "owner-a", payload)
	if response.Code != http.StatusOK || response.Header().Get("Content-Encoding") != "" {
		t.Fatalf("status=%d encoding=%q", response.Code, response.Header().Get("Content-Encoding"))
	}
	if !strings.Contains(response.Body.String(), "scientific-result-") {
		t.Fatalf("identity response omitted payload: %d bytes", response.Body.Len())
	}
}

func TestSynonBiomedExpertProfilesReuseAgentCompatibility(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app := newV11TestServer(t, Options{Workspace: store})
	request := httptest.NewRequest(http.MethodGet, "/api/synonbiomed/expert-profiles", nil)
	request.RemoteAddr = "127.0.0.1:1234"
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var profiles []map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &profiles); err != nil {
		t.Fatal(err)
	}
	if len(profiles) == 0 {
		t.Fatal("expected bundled expert profiles")
	}
}
