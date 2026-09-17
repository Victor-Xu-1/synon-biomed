package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestCompatibilityMessageWakesExpiredTranscriptRunner(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-interrupted-message", "frame-interrupted-message")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})

	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-interrupted-message", MessageUUID: "initial-message", ClientMessageID: "initial-message",
		Text: "start the evidence review",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-interrupted-message")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: "local", RunnerID: "interrupted-message-runner", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	if _, err := repo.InterruptRunner(context.Background(), transcriptstore.InterruptRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "correction-checkpoint", ReasonCode: "artifact_reference_correction_required",
		ResumeDetail: "repair the unsupported citation before continuing",
	}); err != nil {
		t.Fatal(err)
	}

	response := compatJSONRequest(t, server.Handler(), http.MethodPost, "/api/frames/frame-interrupted-message/message", "local", map[string]any{
		"input_data": map[string]any{"request": "Continue after correcting the evidence."},
	}, http.StatusOK)
	if response["status"] != "accepted" {
		t.Fatalf("message response=%#v", response)
	}
	if inputType, err := repo.LatestRunnerInputEventType(context.Background(), stream.UID, "local"); err != nil || inputType != "user_input_response" {
		t.Fatalf("latest input type=%q err=%v", inputType, err)
	}
	dispatch, found, err := store.GetCompatibilityFrameResumeDispatchByFrame("frame-interrupted-message")
	if err != nil || !found || dispatch.Status != "registered" {
		t.Fatalf("continuation dispatch=%#v found=%t err=%v", dispatch, found, err)
	}
	if queued, err := store.CompatibilityQueuedFrameIDs(); err != nil || len(queued) != 0 {
		t.Fatalf("queued compatibility messages=%v err=%v", queued, err)
	}
}

func TestWebConversationMessageStartsFreshDispatchAfterTerminalCorrectionFailure(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-web-blocked-message", UserID: "local", Name: "Web blocked message"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-web-blocked-message", ProjectID: "project-web-blocked-message", AgentName: "OPERON",
		Status: workspace.FrameStatusProcessing, ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	server := newV11TestServer(t, Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, err := store.SetFrameRuntimeMetadata("frame-web-blocked-message", workspace.FrameRuntimeMetadata{
		FrameID: "frame-web-blocked-message",
		ContextData: map[string]any{
			"web_extra":     map[string]any{},
			"web_assistant": map[string]any{"id": "synonbiomed:operon", "locale": "zh-CN"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-web-blocked-message", MessageUUID: "initial-web-blocked", ClientMessageID: "initial-web-blocked",
		Text: "start the artifact-producing task",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-web-blocked-message")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: "local", RunnerID: "web-blocked-runner", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	if _, err := repo.InterruptRunner(context.Background(), transcriptstore.InterruptRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "web-blocked-correction", ReasonCode: "artifact_reference_correction_required",
		ResumeDetail: "missing required deliverables machine-readable validation record (.json)", AutoResume: true,
	}); err != nil {
		t.Fatal(err)
	}
	frame, found, err := store.GetFrame("frame-web-blocked-message")
	if err != nil || !found {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
	resume, err := store.CreateAutoResumeDispatch(frame.ID, frame.RootFrameID, frame.ProjectID, frame.AgentName, "artifact_reference_correction_required")
	if err != nil || resume.Event == nil {
		t.Fatalf("resume=%#v err=%v", resume, err)
	}
	dispatch, ok, err := store.ClaimNextCompatibilityFrameResumeDispatch("web-blocked-dispatcher", time.Minute)
	if err != nil || !ok {
		t.Fatalf("dispatch=%#v ok=%t err=%v", dispatch, ok, err)
	}
	if _, resumed, err := store.FailCompatibilityFrameResumeDispatch(
		dispatch.ResumeEvent.ID, dispatch.Attempt, dispatch.ClaimToken, "artifact_reference_correction_required",
	); err != nil || resumed {
		t.Fatalf("blocked dispatch resumed=%t err=%v", resumed, err)
	}

	response := p3JSONRequest(t, server, http.MethodPost, "/api/conversations/frame-web-blocked-message/messages", map[string]any{
		"content": "repair the same task with a validation JSON", "loading_id": "web-blocked-followup",
	}, "")
	if response.Code != http.StatusAccepted || webString(p3DecodeObject(t, response)["msg_id"]) == "" {
		t.Fatalf("message status=%d body=%s", response.Code, response.Body.String())
	}
	if inputType, err := repo.LatestRunnerInputEventType(context.Background(), stream.UID, "local"); err != nil || inputType != "user_input_response" {
		t.Fatalf("terminal-task continuation input type=%q err=%v", inputType, err)
	}
	oldDispatch, found, err := store.GetCompatibilityFrameResumeDispatch(resume.Event.ID)
	if err != nil || !found || oldDispatch.Status != "failed" {
		t.Fatalf("terminal old dispatch=%#v found=%t err=%v", oldDispatch, found, err)
	}
	dispatch, found, err = store.GetCompatibilityFrameResumeDispatchByFrame(frame.ID)
	if err != nil || !found || dispatch.ResumeEvent.ID == resume.Event.ID || dispatch.Status != "registered" {
		t.Fatalf("fresh follow-up dispatch=%#v found=%t err=%v", dispatch, found, err)
	}
	assertFrameResumeDispatchEvents(t, store, frame.ID, map[string]int{"frame_resume_dispatch_woken": 0})
}

func TestFollowupNeverRevivesTerminalDispatchAfterLostRealtimeWake(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-lost-message-wake", UserID: "local", Name: "Lost message wake"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-lost-message-wake", ProjectID: "project-lost-message-wake", AgentName: "OPERON",
		Status: workspace.FrameStatusProcessing, ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-lost-message-wake", MessageUUID: "lost-wake-initial", ClientMessageID: "lost-wake-initial",
		Text: "start task",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-lost-message-wake")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: "local", RunnerID: "lost-wake-runner", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	if _, err := repo.InterruptRunner(context.Background(), transcriptstore.InterruptRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "lost-wake-correction", ReasonCode: "artifact_reference_correction_required",
		ResumeDetail: "missing validation JSON", AutoResume: true,
	}); err != nil {
		t.Fatal(err)
	}
	frame, found, err := store.GetFrame("frame-lost-message-wake")
	if err != nil || !found {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
	resume, err := store.CreateAutoResumeDispatch(frame.ID, frame.RootFrameID, frame.ProjectID, frame.AgentName, "artifact_reference_correction_required")
	if err != nil || resume.Event == nil {
		t.Fatalf("resume=%#v err=%v", resume, err)
	}
	dispatch, ok, err := store.ClaimNextCompatibilityFrameResumeDispatch("lost-wake-dispatcher", time.Minute)
	if err != nil || !ok {
		t.Fatalf("dispatch=%#v ok=%t err=%v", dispatch, ok, err)
	}
	if _, resumed, err := store.FailCompatibilityFrameResumeDispatch(
		dispatch.ResumeEvent.ID, dispatch.Attempt, dispatch.ClaimToken, "artifact_reference_correction_required",
	); err != nil || resumed {
		t.Fatalf("blocked dispatch resumed=%t err=%v", resumed, err)
	}
	compatibilityFrame, found, err := store.GetCompatibilityFrame(frame.ID)
	if err != nil || !found {
		t.Fatalf("compatibility frame found=%t err=%v", found, err)
	}
	if _, err := server.submitCompatibilityFrameMessage(compatibilityFrame, compatibilityFrameMessageRequest{
		InputData: map[string]any{"request": "repair the same task"}, ClientMutationID: "lost-wake-followup",
	}); err != nil {
		t.Fatalf("submit follow-up through production path: %v", err)
	}
	oldDispatch, found, err := store.GetCompatibilityFrameResumeDispatch(resume.Event.ID)
	if err != nil || !found || oldDispatch.Status != "failed" {
		t.Fatalf("terminal old dispatch=%#v found=%t err=%v", oldDispatch, found, err)
	}
	dispatch, found, err = store.GetCompatibilityFrameResumeDispatchByFrame(frame.ID)
	if err != nil || !found || dispatch.ResumeEvent.ID == resume.Event.ID || dispatch.Status != "registered" {
		t.Fatalf("fresh follow-up dispatch=%#v found=%t err=%v", dispatch, found, err)
	}
	assertFrameResumeDispatchEvents(t, store, frame.ID, map[string]int{"frame_resume_dispatch_woken": 0})
}

func TestCompatibilityMessageQueuesExpiredWaitingApprovalRunner(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-waiting-queue", "frame-waiting-queue")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-waiting-queue", MessageUUID: "initial-waiting-queue", ClientMessageID: "initial-waiting-queue",
		Text: "start the waiting task",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-waiting-queue")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: "local", RunnerID: "waiting-queue-runner", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	if _, _, created, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claimed.Claim, ClientMessageID: "waiting-approval-checkpoint", Phase: transcriptstore.RunnerPhaseWaitingApproval,
		Resumable: true, PayloadJSON: []byte(`{"status":"awaiting_approval","operation_id":"operation-waiting-queue"}`),
	}); err != nil || !created {
		t.Fatalf("waiting checkpoint created=%t err=%v", created, err)
	}
	if _, err := db.Exec(`UPDATE transcript_runner_attempts SET expires_at=? WHERE stream_uid=? AND attempt=?`,
		time.Now().UTC().Add(-time.Minute), stream.UID, claimed.Claim.Attempt); err != nil {
		t.Fatal(err)
	}
	direct, err := server.shouldDirectlyDeliverExpiredTranscriptMessage("frame-waiting-queue")
	if err != nil || direct {
		t.Fatalf("expired waiting checkpoint direct=%t err=%v", direct, err)
	}
	response := compatJSONRequest(t, server.Handler(), http.MethodPost, "/api/frames/frame-waiting-queue/message", "local", map[string]any{
		"input_data": map[string]any{"request": "queue the next task"},
	}, http.StatusOK)
	if response["status"] != "message_queued" {
		t.Fatalf("waiting message response=%#v", response)
	}
	queued, err := store.CompatibilityQueuedFrameIDs()
	if err != nil || len(queued) != 1 || queued[0] != "frame-waiting-queue" {
		t.Fatalf("queued frame ids=%v err=%v", queued, err)
	}
}

func TestCompatibilityMessageAdmitsTerminalRootInputBeforeResumeDispatch(t *testing.T) {
	for _, terminalStatus := range []string{workspace.FrameStatusFailed, workspace.FrameStatusCancelled} {
		t.Run(terminalStatus, func(t *testing.T) {
			store, repo, _ := newTranscriptWebFixture(t)
			frameID := "frame-" + terminalStatus + "-root-message"
			seedTranscriptWebFrame(t, store, "local", "project-"+terminalStatus+"-root-message", frameID)
			server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = server.Close(ctx)
			})

			if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
				FrameID: frameID, MessageUUID: "initial-" + terminalStatus + "-root-message",
				ClientMessageID: "initial-" + terminalStatus + "-root-message", Text: "initial task",
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.UpdateFrame(frameID, workspace.UpdateFrameInput{Status: &terminalStatus}); err != nil {
				t.Fatal(err)
			}

			response := compatJSONRequest(t, server.Handler(), http.MethodPost, "/api/frames/"+frameID+"/message", "local", map[string]any{
				"input_data":         map[string]any{"request": "Continue the same logical task."},
				"client_mutation_id": "resume-" + terminalStatus + "-root-message",
			}, http.StatusOK)
			if response["status"] != "accepted" {
				t.Fatalf("message response=%#v", response)
			}
			messageID, _ := response["message_id"].(string)
			if messageID == "" {
				t.Fatalf("message response missing message_id: %#v", response)
			}
			frame, found, err := store.GetFrame(frameID)
			if err != nil || !found || frame.Status != workspace.FrameStatusProcessing {
				t.Fatalf("resumed frame=%#v found=%t err=%v", frame, found, err)
			}
			stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", frameID)
			if err != nil || !found {
				t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
			}
			if inputType, err := repo.LatestRunnerInputEventType(context.Background(), stream.UID, "local"); err != nil || inputType != "user_input_response" {
				t.Fatalf("latest input type=%q err=%v", inputType, err)
			}
			messageEvent, found, err := repo.GetEventByClientMessageID(context.Background(), stream.UID, "local", messageID)
			if err != nil || !found {
				t.Fatalf("admitted message event=%#v found=%t err=%v", messageEvent, found, err)
			}
			dispatch, found, err := store.GetCompatibilityFrameResumeDispatchByFrame(frameID)
			if err != nil || !found || dispatch.Status != "registered" {
				t.Fatalf("resume dispatch=%#v found=%t err=%v", dispatch, found, err)
			}
			if !dispatch.ResumeEvent.CreatedAt.After(messageEvent.CreatedAt) {
				t.Fatalf("resume dispatch was published before durable user input: message=%s resume=%s",
					messageEvent.CreatedAt.Format(time.RFC3339Nano), dispatch.ResumeEvent.CreatedAt.Format(time.RFC3339Nano))
			}
		})
	}
}

func TestCompatibilityMessageRetryCompletesTerminalRootResumeAfterInputAdmission(t *testing.T) {
	for _, terminalStatus := range []string{workspace.FrameStatusFailed, workspace.FrameStatusCancelled} {
		t.Run(terminalStatus, func(t *testing.T) {
			store, repo, _ := newTranscriptWebFixture(t)
			frameID := "frame-" + terminalStatus + "-root-retry"
			seedTranscriptWebFrame(t, store, "local", "project-"+terminalStatus+"-root-retry", frameID)
			server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = server.Close(ctx)
			})

			if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
				FrameID: frameID, MessageUUID: "initial-" + terminalStatus + "-root-retry",
				ClientMessageID: "initial-" + terminalStatus + "-root-retry", Text: "initial task",
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.UpdateFrame(frameID, workspace.UpdateFrameInput{Status: &terminalStatus}); err != nil {
				t.Fatal(err)
			}

			mutationID := "retry-after-durable-input"
			messageID := uuid.NewSHA1(
				uuid.NameSpaceURL,
				[]byte("synon-biomed:frame-message:"+frameID+"\x00"+mutationID),
			).String()
			request := map[string]any{"request": "Continue after the input was admitted but before dispatch."}
			frame, found, err := store.GetCompatibilityFrame(frameID)
			if err != nil || !found {
				t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
			}
			if err := server.prepareCompatibilityFrameMessageRuntime(
				frame, messageID, request, nil, nil, nil, nil, nil, "", compatibilityFrameMessageRuntimeOptions{
					DeferFrameActivation: true, MessageOrigin: "input_response",
				},
			); err != nil {
				t.Fatal(err)
			}
			frame, found, err = store.GetCompatibilityFrame(frameID)
			if err != nil || !found || frame.Status != terminalStatus {
				t.Fatalf("pre-dispatch frame=%#v found=%t err=%v", frame, found, err)
			}
			if _, found, err := store.GetCompatibilityFrameResumeDispatchByFrame(frameID); err != nil || found {
				t.Fatalf("unexpected pre-retry dispatch found=%t err=%v", found, err)
			}

			response := compatJSONRequest(t, server.Handler(), http.MethodPost, "/api/frames/"+frameID+"/message", "local", map[string]any{
				"input_data": request, "client_mutation_id": mutationID,
			}, http.StatusOK)
			if response["status"] != "accepted" || response["message_id"] != messageID {
				t.Fatalf("retry response=%#v", response)
			}
			frame, found, err = store.GetCompatibilityFrame(frameID)
			if err != nil || !found || frame.Status != workspace.FrameStatusProcessing {
				t.Fatalf("resumed frame=%#v found=%t err=%v", frame, found, err)
			}
			dispatch, found, err := store.GetCompatibilityFrameResumeDispatchByFrame(frameID)
			if err != nil || !found || dispatch.Status != "registered" {
				t.Fatalf("resume dispatch=%#v found=%t err=%v", dispatch, found, err)
			}
		})
	}
}
