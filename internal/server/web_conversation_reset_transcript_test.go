package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestWebConversationResetSettlesTranscriptBeforeLegacyCleanupAndSurvivesRestart(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-reset", "frame-reset")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-reset", MessageUUID: "message-reset", ClientMessageID: "client-reset",
		Text: "stop this run and clear its compatibility runtime",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-reset")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claim, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: "local", RunnerID: "reset-runner", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}

	reset := p3JSONRequest(t, server, http.MethodPost, "/api/conversations/frame-reset/reset", map[string]any{}, "local")
	if reset.Code != http.StatusNoContent {
		t.Fatalf("reset status=%d body=%s", reset.Code, reset.Body.String())
	}
	assertResetTranscriptTerminal(t, repo, stream, "cancelled", 1)
	frame, frameFound, err := store.GetFrame("frame-reset")
	if err != nil || !frameFound || frame.Status != "cancelled" {
		t.Fatalf("frame=%#v found=%t err=%v", frame, frameFound, err)
	}
	if _, found, err := server.sessionStore.Get("frame-reset"); err != nil || found {
		t.Fatalf("legacy session found=%t err=%v", found, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Close(ctx); err != nil {
		t.Fatal(err)
	}
	restarted := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
		defer closeCancel()
		_ = restarted.Close(closeCtx)
	})
	retry := p3JSONRequest(t, restarted, http.MethodPost, "/api/conversations/frame-reset/reset", map[string]any{}, "local")
	if retry.Code != http.StatusNoContent {
		t.Fatalf("retry status=%d body=%s", retry.Code, retry.Body.String())
	}
	assertResetTranscriptTerminal(t, repo, stream, "cancelled", 1)
}

func TestWebConversationResetDoesNotDeleteRuntimeBeforeTranscriptCommit(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-reset-failure", "frame-reset-failure")
	if _, err := db.Exec(`
		CREATE TRIGGER fail_conversation_reset BEFORE UPDATE OF status ON transcript_runner_attempts
		WHEN NEW.status='cancelled' BEGIN SELECT RAISE(ABORT, 'forced reset failure'); END`); err != nil {
		t.Fatal(err)
	}
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-reset-failure", MessageUUID: "message-reset-failure", ClientMessageID: "client-reset-failure",
		Text: "preserve compatibility state when reset cannot commit",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-reset-failure")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claim, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: "local", RunnerID: "reset-failure-runner", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	if err := server.sessionStore.Upsert(sessionstore.Session{
		ID: "frame-reset-failure", Title: "Compatibility reset state", WorkDir: t.TempDir(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := server.sessionStore.Get("frame-reset-failure"); err != nil || !found {
		t.Fatalf("session before reset found=%t err=%v", found, err)
	}
	reset := p3JSONRequest(t, server, http.MethodPost, "/api/conversations/frame-reset-failure/reset", map[string]any{}, "local")
	if reset.Code != http.StatusInternalServerError {
		t.Fatalf("reset status=%d body=%s", reset.Code, reset.Body.String())
	}
	if message := webString(p3DecodeObject(t, reset)["message"]); message != "unable to process conversation request" {
		t.Fatalf("reset error message=%q", message)
	}
	if _, found, err := server.sessionStore.Get("frame-reset-failure"); err != nil || !found {
		t.Fatalf("session after failed reset found=%t err=%v", found, err)
	}
	state, found, err := repo.GetLatestRunnerRuntimeState(context.Background(), stream.UID, "local")
	if err != nil || !found || state.Status != "running" {
		t.Fatalf("runtime=%#v found=%t err=%v", state, found, err)
	}
	frame, found, err := store.GetFrame("frame-reset-failure")
	if err != nil || !found || frame.Status != "processing" {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
}

func TestWebConversationResetPreservesCompletedTranscriptTerminal(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-reset-completed", "frame-reset-completed")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-reset-completed", MessageUUID: "message-reset-completed", ClientMessageID: "client-reset-completed",
		Text: "finish before reset",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-reset-completed")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claim, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: "local", RunnerID: "completed-before-reset", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	payload, err := json.Marshal(map[string]string{"status": "completed", "detail": "finished before reset"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claim.Claim, ClientMessageID: "completed-before-reset", Status: "completed", PayloadJSON: payload,
		Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("finish created=%t err=%v", created, err)
	}

	reset := p3JSONRequest(t, server, http.MethodPost, "/api/conversations/frame-reset-completed/reset", map[string]any{}, "local")
	if reset.Code != http.StatusNoContent {
		t.Fatalf("reset status=%d body=%s", reset.Code, reset.Body.String())
	}
	assertResetTranscriptTerminal(t, repo, stream, "completed", 1)
	frame, found, err := store.GetFrame("frame-reset-completed")
	if err != nil || !found || frame.Status != "completed" {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
}

func assertResetTranscriptTerminal(
	t *testing.T,
	repo *transcriptstore.Repository,
	stream transcriptstore.Stream,
	wantStatus string,
	wantTerminals int,
) {
	t.Helper()
	state, found, err := repo.GetLatestRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found || state.Status != wantStatus || state.Phase != transcriptstore.RunnerPhaseTerminal {
		t.Fatalf("runtime=%#v found=%t err=%v", state, found, err)
	}
	current, err := repo.GetStream(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	events, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ThroughPublicationSequence: current.NextPublication - 1, Limit: 100,
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
	if terminals != wantTerminals {
		t.Fatalf("terminal events=%d want=%d events=%#v", terminals, wantTerminals, events)
	}
}
