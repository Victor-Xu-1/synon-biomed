package server

import (
	"context"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestDetachedSettlementKeepsLiveSourceRunnerOwnership(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-detached-live", "frame-detached-live")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-detached-live", MessageUUID: "message-detached-live",
		ClientMessageID: "client-detached-live", Text: "run",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-detached-live")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "detached-live-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	wake, err := server.detachedSettlementRequiresResumeWake(context.Background(), &claimed.Claim)
	if err != nil || wake {
		t.Fatalf("live claim wake=%t err=%v", wake, err)
	}
	if _, _, _, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "finish-detached-live", Status: "failed",
		PayloadJSON: []byte(`{"status":"failed","detail":"test terminal"}`),
	}); err != nil {
		t.Fatal(err)
	}
	wake, err = server.detachedSettlementRequiresResumeWake(context.Background(), &claimed.Claim)
	if err != nil || !wake {
		t.Fatalf("stale claim wake=%t err=%v", wake, err)
	}
	wake, err = server.detachedSettlementRequiresResumeWake(context.Background(), nil)
	if err != nil || !wake {
		t.Fatalf("claimless observer wake=%t err=%v", wake, err)
	}
}
