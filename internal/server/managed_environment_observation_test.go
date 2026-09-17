package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	kernelruntime "synon-go/internal/kernel"
	transcriptstore "synon-go/internal/persistence/transcript"
	"synon-go/internal/toolprogress"
)

func TestManagedEnvironmentObservationRetainsOneRowAndFinalFacts(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "observed-env", "observed-env")
	srv := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() { _ = srv.Close(context.Background()) })
	_, _, err := srv.submitFrameMessage(store, frameMessageSubmission{FrameID: "observed-env", MessageUUID: "obs-message", ClientMessageID: "obs-message", Text: "prepare background environment"})
	if err != nil {
		t.Fatal(err)
	}
	stream, _, err := repo.GetFrameStreamBySession(context.Background(), "local", "observed-env")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "observed-env", TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh})
	if err != nil || !claimed.Claimed {
		t.Fatal(err)
	}
	run := &sessionRunnerChatRun{SessionID: stream.SessionID, Attempt: int(claimed.Claim.Attempt), ClaimToken: claimed.Claim.ClaimToken, Transcript: &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim}}
	options := SessionRunnerChatOptions{SessionID: stream.SessionID, RunnerID: claimed.Claim.RunnerID}
	call := agentruntime.ToolCall{ID: "observed-install", Name: manageEnvironmentsToolName, Arguments: json.RawMessage(`{"mode":"create","name":"observed","background":true,"human_description":"准备测试环境"}`)}
	if err := srv.checkpointChatModelToolCalls(options, run, []agentruntime.ToolCall{call}); err != nil {
		t.Fatal(err)
	}
	if err := srv.checkpointSessionRunnerToolEvent(context.Background(), options, run, agentruntime.Event{Type: agentruntime.EventToolStarted, ToolName: call.Name, ToolCallID: call.ID, Arguments: string(call.Arguments)}); err != nil {
		t.Fatal(err)
	}
	access, _, err := store.GetKernelFrameAccessContext(context.Background(), stream.FrameID)
	if err != nil {
		t.Fatal(err)
	}
	ctx := withTranscriptRunnerChatRun(context.Background(), run)
	var executions atomic.Int32
	release := make(chan struct{})
	operation := func(ctx context.Context) (kernelruntime.ManagedEnvironment, error) {
		executions.Add(1)
		completed, total := int64(1024), int64(2048)
		toolprogress.Report(ctx, toolprogress.Update{Phase: "downloading_packages", BytesCompleted: &completed, BytesTotal: &total})
		<-release
		toolprogress.Report(ctx, toolprogress.Update{Phase: "verifying_environment"})
		return kernelruntime.ManagedEnvironment{Name: "observed", Status: "ready", Language: "python"}, nil
	}
	result, err := srv.executeManagedEnvironmentOperation(ctx, access, call, call.Name, true, nil, operation)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	duplicate, err := srv.executeManagedEnvironmentOperation(ctx, access, call, call.Name, true, nil, operation)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	if mapValue(duplicate)["operation_id"] != mapValue(result)["operation_id"] {
		t.Fatal("duplicate changed identity")
	}
	waitStatus := func(want string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			messages, _, err := srv.loadTranscriptWebHistory(context.Background(), stream.OwnerID, stream.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			found := false
			for _, message := range messages {
				if message["type"] == "tool_call" {
					count++
					content := mapValue(message["content"])
					if content["status"] == want && (want != "running" || mapValue(content["progress"])["bytesCompleted"] == float64(1024)) {
						found = true
					}
				}
			}
			if count != 1 {
				t.Fatalf("duplicate/missing rows: %d %#v", count, messages)
			}
			if found {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("missing status %s: %#v", want, messages)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitStatus("running")
	close(release)
	waitStatus("completed")
	// A fast operation may finish before its protocol admission receipt. The
	// admission must not revert the public row or create a second message.
	raw, _ := json.Marshal(result)
	if err := srv.checkpointSessionRunnerToolEvent(ctx, options, run, agentruntime.Event{Type: agentruntime.EventToolCompleted, ToolName: call.Name, ToolCallID: call.ID, Arguments: string(call.Arguments), Result: string(raw)}); err != nil {
		t.Fatal(err)
	}
	waitStatus("completed")
	if executions.Load() != 1 {
		t.Fatalf("executions=%d", executions.Load())
	}
	if err := srv.drainTranscriptWebOwner(context.Background(), stream.OwnerID, true); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/events?frame_id="+stream.FrameID+"&limit=1000", nil)
	request.Header.Set("X-Synon-User-Id", stream.OwnerID)
	recorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("events API: %d %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Events []struct {
			Payload map[string]any `json:"payload"`
		} `json:"events"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	observedBytes := false
	settled := false
	for _, event := range response.Events {
		if event.Payload["type"] != "tool_call" {
			continue
		}
		data := mapValue(event.Payload["data"])
		if data["call_id"] != call.ID {
			continue
		}
		ids[stringValue(event.Payload["msg_id"])] = true
		if mapValue(data["progress"])["bytesCompleted"] == float64(1024) {
			observedBytes = true
		}
		if data["status"] == "completed" {
			settled = true
		}
	}
	if len(ids) != 1 || !observedBytes || !settled {
		t.Fatalf("realtime identity/bytes/result mismatch: ids=%v bytes=%t settled=%t body=%s", ids, observedBytes, settled, recorder.Body.String())
	}
	// Shutdown can race admission before the goroutine registers. That
	// unstarted operation must settle visibly, not leave a spinning row.
	srv.beginDetachedKernelObserverShutdown()
	call.ID = "unstarted-after-shutdown"
	if err := srv.checkpointChatModelToolCalls(options, run, []agentruntime.ToolCall{call}); err != nil {
		t.Fatal(err)
	}
	if err := srv.checkpointSessionRunnerToolEvent(ctx, options, run, agentruntime.Event{Type: agentruntime.EventToolStarted, ToolName: call.Name, ToolCallID: call.ID, Arguments: string(call.Arguments)}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.executeManagedEnvironmentOperation(ctx, access, call, call.Name, true, nil, operation); err == nil {
		t.Fatal("shutdown admitted another operation")
	}
	messages, _, err := srv.loadTranscriptWebHistory(ctx, stream.OwnerID, stream.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	failed := false
	for _, message := range messages {
		content := mapValue(message["content"])
		if message["type"] == "tool_call" && content["call_id"] == call.ID {
			failed = content["status"] == "error"
		}
	}
	if !failed || executions.Load() != 1 {
		t.Fatalf("unstarted operation did not settle: failed=%t executions=%d", failed, executions.Load())
	}
}

func TestManagedEnvironmentBackgroundObserverShutdownCancelsWork(t *testing.T) {
	srv, identity := managedEnvironmentToolFixture(t)
	started := make(chan struct{})
	_, err := srv.executeManagedEnvironmentOperation(context.Background(), identity.access, agentruntime.ToolCall{ID: "cancel-background"}, manageEnvironmentsToolName, true, nil, func(ctx context.Context) (kernelruntime.ManagedEnvironment, error) {
		close(started)
		<-ctx.Done()
		return kernelruntime.ManagedEnvironment{}, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background operation did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Close(ctx); err != nil {
		t.Fatal(err)
	}
	notifications, err := srv.workspaceStore.ConsumeUnreadNotifications(context.Background(), identity.access.Frame.ID, identity.access.Frame.RootFrameID, identity.access.UserID, 10)
	if err != nil || len(notifications) != 1 || notifications[0].Payload["status"] != "cancelled" {
		t.Fatalf("shutdown receipt=%#v err=%v", notifications, err)
	}
}
