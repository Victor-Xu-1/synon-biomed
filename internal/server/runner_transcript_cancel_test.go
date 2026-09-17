package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestTranscriptFrameCancelBeforeRunnerAdmissionNeverCallsProvider(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-cancel-before", "frame-cancel-before")
	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"must not run"}}]}`))
	}))
	defer provider.Close()
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-cancel-before", MessageUUID: "message-cancel-before", ClientMessageID: "client-cancel-before",
		Text: "do not invoke the provider after cancellation",
	}); err != nil {
		t.Fatal(err)
	}
	compatJSONRequest(t, server.Handler(), http.MethodPost, "/api/frames/frame-cancel-before/cancel", "local", nil, http.StatusOK)
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-cancel-before")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	state, found, err := repo.GetLatestRunnerRuntimeState(context.Background(), stream.UID, "local")
	if err != nil || !found || state.Status != "cancelled" || state.Phase != transcriptstore.RunnerPhaseTerminal {
		t.Fatalf("state=%#v found=%t err=%v", state, found, err)
	}
	if err := server.drainTranscriptWebDeliveries(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := server.drainTranscriptWebDeliveries(context.Background()); err != nil {
		t.Fatal(err)
	}
	streamTerminals, runtimeTerminals, completedTurns := 0, 0, 0
	for _, event := range transcriptWebEvents(t, store, "local") {
		switch event.Type {
		case "message.stream":
			if event.Payload["terminal_status"] == "cancelled" && event.Payload["stream_type"] == "finish" {
				streamTerminals++
			}
		case "runtime.statusChanged":
			if event.Payload["terminal_status"] == "cancelled" {
				runtimeTerminals++
			}
		case "turn.completed":
			if event.Payload["terminal_status"] == "cancelled" && event.Payload["status"] == "cancelled" {
				completedTurns++
			}
		}
	}
	if streamTerminals != 1 || runtimeTerminals != 1 || completedTurns != 1 {
		t.Fatalf("terminal projections stream=%d runtime=%d turn=%d", streamTerminals, runtimeTerminals, completedTurns)
	}
	result, err := server.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{
		SessionID: "frame-cancel-before", RunnerID: "runner-cancel-before", Endpoint: provider.URL + "/v1/chat/completions",
		APIKey: "test-key", Model: "test-model", LeaseTTL: time.Minute, DisableSkillDiscovery: true,
	})
	if err != nil || result.Claimed || requests.Load() != 0 {
		t.Fatalf("result=%#v requests=%d err=%v", result, requests.Load(), err)
	}
}

func TestWorkspaceHTTPFrameCancelUsesTranscriptTerminalAuthority(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-workspace-cancel", "frame-workspace-cancel")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-workspace-cancel", MessageUUID: "message-workspace-cancel",
		ClientMessageID: "client-workspace-cancel", Text: "cancel through the workspace HTTP projection",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-workspace-cancel")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claim, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: "local", RunnerID: "workspace-http-runner", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	serveWorkspaceJSON(
		t, server.Handler(), http.MethodPost, "/api/go/frames/frame-workspace-cancel/cancel", map[string]any{}, http.StatusOK,
	)
	state, found, err := repo.GetLatestRunnerRuntimeState(context.Background(), stream.UID, "local")
	if err != nil || !found || state.Status != "cancelled" || state.Phase != transcriptstore.RunnerPhaseTerminal {
		t.Fatalf("state=%#v found=%t err=%v", state, found, err)
	}
	compatJSONRequest(
		t, server.Handler(), http.MethodPost, "/api/frames/frame-workspace-cancel/cancel", "local", nil, http.StatusOK,
	)
	current, err := repo.GetStream(context.Background(), stream.UID, "local")
	if err != nil {
		t.Fatal(err)
	}
	events, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: "local", ThroughPublicationSequence: current.NextPublication - 1, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	terminals := 0
	for _, event := range events {
		if event.Event.Type == "runner_finished" {
			terminals++
		}
	}
	if terminals != 1 {
		t.Fatalf("terminal events=%d events=%#v", terminals, events)
	}
}

func TestTranscriptFrameCancelRecoversDetachedClaimWithoutCheckpoint(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-cancel-detached", "frame-cancel-detached")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-cancel-detached", MessageUUID: "message-cancel-detached", ClientMessageID: "client-cancel-detached",
		Text: "cancel the detached claim",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-cancel-detached")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claim, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: "local", RunnerID: "detached-runner", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	compatJSONRequest(t, server.Handler(), http.MethodPost, "/api/frames/frame-cancel-detached/cancel", "local", nil, http.StatusOK)
	state, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, "local", claim.Claim.Attempt)
	if err != nil || state.Status != "cancelled" {
		t.Fatalf("state=%#v err=%v", state, err)
	}
}

func TestTranscriptFrameCancelWinsBeforeLateProviderSettlement(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-cancel-live", "frame-cancel-live")
	entered := make(chan struct{})
	releaseProvider := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
		case <-releaseProvider:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"late response"}}]}`))
		}
	}))
	defer provider.Close()
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-cancel-live", MessageUUID: "message-cancel-live", ClientMessageID: "client-cancel-live",
		Text: "cancel this active provider call",
	}); err != nil {
		t.Fatal(err)
	}
	type runOutcome struct {
		result SessionRunnerCycleResult
		err    error
	}
	done := make(chan runOutcome, 1)
	go func() {
		result, err := server.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{
			SessionID: "frame-cancel-live", RunnerID: "runner-cancel-live", Endpoint: provider.URL + "/v1/chat/completions",
			APIKey: "test-key", Model: "test-model", LeaseTTL: time.Minute, MaxAttempts: 1,
			DisableSkillDiscovery: true,
		})
		done <- runOutcome{result: result, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider was not called")
	}
	compatJSONRequest(t, server.Handler(), http.MethodPost, "/api/frames/frame-cancel-live/cancel", "local", nil, http.StatusOK)
	close(releaseProvider)
	select {
	case outcome := <-done:
		if outcome.err != nil || outcome.result.Status != "cancelled" {
			t.Fatalf("outcome=%#v", outcome)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled runner did not settle")
	}
	compatJSONRequest(t, server.Handler(), http.MethodPost, "/api/frames/frame-cancel-live/cancel", "local", nil, http.StatusOK)
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-cancel-live")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	stream, err = repo.GetStream(context.Background(), stream.UID, "local")
	if err != nil {
		t.Fatal(err)
	}
	events, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: "local", ThroughPublicationSequence: stream.NextPublication - 1, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	terminals, assistants := 0, 0
	for _, event := range events {
		switch event.Event.Type {
		case "runner_finished":
			terminals++
			var payload map[string]any
			if err := json.Unmarshal(event.ResolvedPayloadJSON, &payload); err != nil || webString(payload["status"]) != "cancelled" {
				t.Fatalf("terminal payload=%s err=%v", event.ResolvedPayloadJSON, err)
			}
		case "assistant_message":
			assistants++
		}
	}
	if terminals != 1 || assistants != 0 {
		t.Fatalf("terminals=%d assistants=%d events=%#v", terminals, assistants, events)
	}
}

func TestTranscriptFrameCompletedRunnerWinsLaterCancel(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-complete-first", "frame-complete-first")
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"completed first"}}]}`))
	}))
	defer provider.Close()
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-complete-first", MessageUUID: "message-complete-first", ClientMessageID: "client-complete-first",
		Text: "finish before cancellation",
	}); err != nil {
		t.Fatal(err)
	}
	result, err := server.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{
		SessionID: "frame-complete-first", RunnerID: "runner-complete-first", Endpoint: provider.URL + "/v1/chat/completions",
		APIKey: "test-key", Model: "test-model", LeaseTTL: time.Minute,
		DisableSkillDiscovery: true,
	})
	if err != nil || result.Status != "completed" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	cancelled := compatJSONRequest(t, server.Handler(), http.MethodPost, "/api/frames/frame-complete-first/cancel", "local", nil, http.StatusOK)
	if frames, ok := cancelled["cancelled_frames"].([]any); !ok || len(frames) != 0 {
		t.Fatalf("cancel response=%#v", cancelled)
	}
	frame, found, err := store.GetFrame("frame-complete-first")
	if err != nil || !found || frame.Status != "completed" {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
}
