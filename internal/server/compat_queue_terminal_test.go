package server

import (
	"context"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestTranscriptQueuedContinuationIsSettledAfterCompletedWithoutNewInput(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-queue-terminal", "frame-queue-terminal")
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:frame-queue-terminal", OwnerID: "local", ExternalID: "frame-queue-terminal",
		SessionID: "frame-queue-terminal", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-queue-terminal", RootFrameID: "frame-queue-terminal", FrameID: "frame-queue-terminal", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "initial", FrameEventID: "initial-event",
		MessageUUID: "initial-message", Text: "Initial task", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("input created=%t err=%v", created, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	if _, _, created, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "initial-finish", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("finish created=%t err=%v", created, err)
	}
	if _, _, _, err := store.QueueCompatibilityMessage(
		"frame-queue-terminal", "queued-continue", map[string]any{"request": "继续运行"},
		map[string]any{"text": "继续运行", "inputData": map[string]any{"request": "继续运行"}},
	); err != nil {
		t.Fatal(err)
	}
	server := New(Options{FileRoot: t.TempDir(), Workspace: store, Transcript: repo})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	advanced, err := server.advanceCompatibilityFrameAfterRunner("frame-queue-terminal", "completed")
	if err != nil || advanced != 1 {
		t.Fatalf("advanced=%d err=%v", advanced, err)
	}
	intent, found, err := store.GetCompatibilityMessageIntent("queued-continue")
	if err != nil || !found || intent.State != "drained" {
		t.Fatalf("intent=%#v found=%t err=%v", intent, found, err)
	}
	var userMessages int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='user_message'`, stream.UID).Scan(&userMessages); err != nil {
		t.Fatal(err)
	}
	if userMessages != 1 {
		t.Fatalf("stale continuation reopened transcript: user_messages=%d", userMessages)
	}
	taskIntent, found, err := repo.GetActiveFrameTaskIntent(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found || taskIntent.Revision != 1 || taskIntent.Text != "Initial task" {
		t.Fatalf("task intent=%#v found=%t err=%v", taskIntent, found, err)
	}
}
