package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestTerminalFrameAuthorityCancelsLiveProviderAtLeaseHeartbeat(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-terminal-fence", "frame-terminal-fence")
	requestStarted := make(chan struct{}, 1)
	requestCancelled := make(chan struct{}, 1)
	releaseProvider := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		select {
		case requestStarted <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
			select {
			case requestCancelled <- struct{}{}:
			default:
			}
		case <-releaseProvider:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"must not commit after terminal authority"}}]}`))
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
		FrameID: "frame-terminal-fence", MessageUUID: "message-terminal-fence",
		ClientMessageID: "client-terminal-fence", Text: "keep running until durable frame authority stops this turn",
	}); err != nil {
		t.Fatal(err)
	}

	type runOutcome struct {
		result SessionRunnerCycleResult
		err    error
	}
	done := make(chan runOutcome, 1)
	runStartedAt := time.Now()
	go func() {
		result, err := server.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{
			SessionID: "frame-terminal-fence", RunnerID: "runner-terminal-fence",
			Endpoint: provider.URL + "/v1/chat/completions", APIKey: "test-key", Model: "test-model",
			LeaseTTL: 2 * time.Second, MaxAttempts: 1, DisableSkillDiscovery: true,
		})
		done <- runOutcome{result: result, err: err}
	}()
	select {
	case <-requestStarted:
	case <-time.After(10 * time.Second):
		t.Logf("provider start wait monotonic elapsed=%s", time.Since(runStartedAt))
		select {
		case outcome := <-done:
			raw, marshalErr := json.Marshal(outcome.result)
			t.Logf("runner ended before provider start: result=%s err=%v marshalErr=%v", raw, outcome.err, marshalErr)
		default:
			t.Log("runner outcome is still pending before provider start")
		}
		stream, found, streamErr := repo.GetFrameStreamBySession(context.Background(), "local", "frame-terminal-fence")
		if streamErr == nil && found {
			logTranscriptRunnerClaimState(t, repo, stream.UID, stream.OwnerID, nil)
		} else {
			t.Logf("runner stream found=%t err=%v", found, streamErr)
		}
		close(releaseProvider)
		t.Fatal("provider request did not start")
	}
	failed := workspace.FrameStatusFailed
	if _, err := store.UpdateFrame("frame-terminal-fence", workspace.UpdateFrameInput{Status: &failed}); err != nil {
		close(releaseProvider)
		t.Fatal(err)
	}
	select {
	case outcome := <-done:
		if outcome.err != nil || !outcome.result.Claimed || outcome.result.Status != "failed" ||
			outcome.result.FinishEventID <= 0 {
			close(releaseProvider)
			t.Fatalf("terminal-fenced outcome=%#v err=%v", outcome.result, outcome.err)
		}
	case <-time.After(10 * time.Second):
		close(releaseProvider)
		t.Fatal("terminal-fenced runner did not stop")
	}
	select {
	case <-requestCancelled:
	case <-time.After(10 * time.Second):
		close(releaseProvider)
		t.Fatal("terminal frame authority stopped the runner but did not close the live provider request")
	}

	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-terminal-fence")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	current, err := repo.GetStream(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	events, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		ThroughPublicationSequence: current.NextPublication - 1, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	terminalCount := 0
	for _, event := range events {
		if event.Event.Type == "assistant_message" {
			t.Fatalf("late assistant event survived terminal authority: %#v", event)
		}
		if event.Event.Type == "runner_finished" {
			terminalCount++
			var payload map[string]any
			if err := json.Unmarshal(event.ResolvedPayloadJSON, &payload); err != nil || payload["status"] != "failed" {
				t.Fatalf("terminal payload=%#v err=%v", payload, err)
			}
		}
	}
	if terminalCount != 1 {
		t.Fatalf("terminal events=%d want=1", terminalCount)
	}
	state, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID, 1)
	if err != nil || state.Status != "failed" || state.Phase != transcriptstore.RunnerPhaseTerminal || state.FinishedAt == nil {
		t.Fatalf("runner state=%#v err=%v", state, err)
	}
	if next, err := repo.ClaimNextRunner(context.Background(), transcriptstore.ClaimNextRunnerInput{
		RunnerID: "replacement-runner", TTL: time.Minute,
	}); err != nil || next.Claimed {
		t.Fatalf("terminal frame was reclaimed=%#v err=%v", next, err)
	}
}
