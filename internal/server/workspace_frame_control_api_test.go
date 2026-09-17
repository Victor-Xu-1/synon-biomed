package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestWorkspaceFrameControlRemovedMutationsAreStableTombstones(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-control", "frame-control")
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:frame-control", OwnerID: "local", ExternalID: "frame-control", SessionID: "frame-control",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project-control",
		RootFrameID: "frame-control", FrameID: "frame-control", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "control-task",
		FrameEventID: "control-task-event", MessageUUID: "control-task-message", Text: "Preserve all authority state.",
	}); err != nil || !created {
		t.Fatalf("task created=%t err=%v", created, err)
	}
	server := New(Options{FileRoot: t.TempDir(), Workspace: store, Transcript: repo})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if err := server.appendClientWebSocketMessage("frame-control", "assistant", map[string]any{
		"type": "plan_proposed", "text": "Legacy state must not change",
	}); err != nil {
		t.Fatal(err)
	}
	before := branchAuthorityStateHash(t, server, store, "frame-control", "local")
	projectedBefore, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	transcriptBefore, _ := json.Marshal(projectedBefore)
	for _, test := range []struct {
		operation, code, replacement string
	}{
		{operation: "approve-plan", code: "synon.frame_control.approve_plan_removed.v1", replacement: "/api/frames/{id}/approve-plan"},
		{operation: "resolve-input", code: "synon.frame_control.resolve_input_removed.v1", replacement: "/api/frames/{id}/resolve-input"},
	} {
		for _, frameID := range []string{"frame-control", "missing-frame"} {
			response := compatJSONRequest(t, server.Handler(), http.MethodPost,
				"/api/go/frames/"+frameID+"/"+test.operation, "local", map[string]any{"ignored": true}, http.StatusGone)
			if len(response) != 4 || response["ok"] != false || response["error_code"] != test.code ||
				response["replacement"] != test.replacement || response["error"] != "Frame control endpoint has been retired" {
				t.Fatalf("%s response=%#v", test.operation, response)
			}
		}
	}
	if after := branchAuthorityStateHash(t, server, store, "frame-control", "local"); after != before {
		t.Fatalf("deprecated controls mutated workspace/session authority: before=%s after=%s", before, after)
	}
	projectedAfter, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	transcriptAfter, _ := json.Marshal(projectedAfter)
	if string(transcriptAfter) != string(transcriptBefore) {
		t.Fatalf("deprecated controls mutated transcript: before=%s after=%s", transcriptBefore, transcriptAfter)
	}
}

func TestWorkspaceFrameControlTombstonePreservesAuthAndCSRFPrecedence(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := New(Options{
		FileRoot: t.TempDir(), Workspace: store,
		SynonLinkAuth: SynonLinkAuthOptions{
			Username: "operator", Password: "test-secret-password", UserID: "user-1", TTL: time.Hour,
		},
	})
	handler := server.Handler()
	path := "/api/go/frames/never-load-this-frame/approve-plan"
	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d body=%s", unauthenticated.Code, unauthenticated.Body.String())
	}
	login := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"operator","password":"test-secret-password"}`))
	login.RemoteAddr = "127.0.0.1:12345"
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	var sessionCookie, csrfCookie *http.Cookie
	for _, cookie := range loginResponse.Result().Cookies() {
		switch cookie.Name {
		case webSessionCookieName:
			sessionCookie = cookie
		case webCSRFCookieName:
			csrfCookie = cookie
		}
	}
	if sessionCookie == nil || csrfCookie == nil {
		t.Fatalf("login cookies session=%#v csrf=%#v", sessionCookie, csrfCookie)
	}
	missingCSRFRequest := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
	missingCSRFRequest.AddCookie(sessionCookie)
	missingCSRF := httptest.NewRecorder()
	handler.ServeHTTP(missingCSRF, missingCSRFRequest)
	if missingCSRF.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status=%d body=%s", missingCSRF.Code, missingCSRF.Body.String())
	}
	authorizedRequest := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
	authorizedRequest.AddCookie(sessionCookie)
	authorizedRequest.AddCookie(csrfCookie)
	authorizedRequest.Header.Set("X-Synon-CSRF-Token", csrfCookie.Value)
	authorized := httptest.NewRecorder()
	handler.ServeHTTP(authorized, authorizedRequest)
	if authorized.Code != http.StatusGone || !strings.Contains(authorized.Body.String(), "approve_plan_removed.v1") {
		t.Fatalf("authorized status=%d body=%s", authorized.Code, authorized.Body.String())
	}
}

func TestWorkspaceDiscardPlanUsesCanonicalTranscriptAuthority(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-workspace-discard", "frame-workspace-discard")
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:frame-workspace-discard", OwnerID: "local", ExternalID: "frame-workspace-discard",
		SessionID: "frame-workspace-discard", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-workspace-discard", RootFrameID: "frame-workspace-discard", FrameID: "frame-workspace-discard", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "task", FrameEventID: "task-event",
		MessageUUID: "task-message", Text: "Prepare a controlled plan.", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("task created=%t err=%v", created, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "planning-runner", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	if _, _, created, err := repo.PauseRunnerForInput(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claimed.Claim, ClientMessageID: "plan-paused", Phase: transcriptstore.RunnerPhaseWaitingUser,
		Resumable: true, PayloadJSON: []byte(`{"status":"awaiting_plan_approval"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("pause created=%t err=%v", created, err)
	}
	if _, err := store.SetFrameRuntimeMetadata("frame-workspace-discard", workspace.FrameRuntimeMetadata{
		ContextData: map[string]any{"_plan_json": map[string]any{"title": "Plan"}},
	}); err != nil {
		t.Fatal(err)
	}
	status := workspace.FrameStatusAwaitingPlanApproval
	if _, err := store.UpdateFrame("frame-workspace-discard", workspace.UpdateFrameInput{Status: &status}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{FileRoot: t.TempDir(), Workspace: store, Transcript: repo})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	request := map[string]any{"clientMessageId": "discard-plan-control"}
	first := compatJSONRequest(t, server.Handler(), http.MethodPost,
		"/api/go/frames/frame-workspace-discard/discard-plan", "local", request, http.StatusOK)
	second := compatJSONRequest(t, server.Handler(), http.MethodPost,
		"/api/go/frames/frame-workspace-discard/discard-plan", "local", request, http.StatusOK)
	if first["idempotent"] != false || second["idempotent"] != true {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	frame, found, err := store.GetFrame("frame-workspace-discard")
	if err != nil || !found || frame.Status != workspace.FrameStatusCompleted {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
	runtime, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID, claimed.Claim.Attempt)
	if err != nil || runtime.Status != workspace.FrameStatusCompleted || runtime.Phase != transcriptstore.RunnerPhaseTerminal {
		t.Fatalf("runtime=%#v err=%v", runtime, err)
	}
	entries, err := server.eventJournal.ReadAfter("frame-workspace-discard", 0, 20)
	if err != nil || len(entries) != 0 {
		t.Fatalf("legacy journal entries=%#v err=%v", entries, err)
	}
	projected, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	terminalCount := 0
	for _, event := range projected {
		if event.Event.Type == "runner_finished" {
			terminalCount++
		}
	}
	frameEvents, err := store.ListFrameEvents(frame.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	planEventCount := 0
	for _, event := range frameEvents {
		if event.Type == "plan_discarded" {
			planEventCount++
		}
	}
	if terminalCount != 1 || planEventCount != 1 {
		t.Fatalf("terminal=%d planEvents=%d", terminalCount, planEventCount)
	}
}
