package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestWebConversationCompletedTranscriptRunnerAcceptsReviewRepair(t *testing.T) {
	app, store := newP3WebConversationServer(t)
	project := createP3Project(t, store, "project-completed-review-repair", "local")
	created := p3JSONRequest(t, app, http.MethodPost, "/api/conversations", map[string]any{
		"name":      "Completed review repair",
		"assistant": map[string]any{"id": "synonbiomed:OPERON"},
		"extra":     map[string]any{"project_id": project.ID, "project_name": project.Name},
	}, "")
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	conversationID := webString(p3DecodeObject(t, created)["id"])
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:" + conversationID, OwnerID: "local", ExternalID: conversationID, SessionID: conversationID,
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: project.ID,
		RootFrameID: conversationID, FrameID: conversationID, Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	app.transcriptStore = repository

	first := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/"+conversationID+"/messages", map[string]any{
		"content": "Run the original task.",
	}, "")
	if first.Code != http.StatusAccepted {
		t.Fatalf("first send status=%d body=%s", first.Code, first.Body.String())
	}
	stream, found, err := app.transcriptStore.GetFrameStreamBySession(
		context.Background(), "local", conversationID,
	)
	if err != nil || !found {
		t.Fatalf("stream found=%t err=%v", found, err)
	}
	claim, err := app.transcriptStore.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "completed-review-first-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("first claim=%#v err=%v", claim, err)
	}
	if _, _, created, err := app.transcriptStore.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claim.Claim, ClientMessageID: "completed-review-first-finish", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`),
	}); err != nil || !created {
		t.Fatalf("first finish created=%t err=%v", created, err)
	}
	frame, found, err := store.GetCompatibilityFrame(conversationID)
	if err != nil || !found || frame.Status != "completed" {
		t.Fatalf("terminal frame=%#v found=%t err=%v", frame, found, err)
	}

	repair := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/"+conversationID+"/messages", map[string]any{
		"content": "Repair the review findings and regenerate the result.",
	}, "")
	if repair.Code != http.StatusAccepted {
		t.Fatalf("repair send status=%d body=%s", repair.Code, repair.Body.String())
	}
	stream, found, err = app.transcriptStore.GetFrameStreamBySession(
		context.Background(), "local", conversationID,
	)
	if err != nil || !found || stream.InputRevision != 2 || stream.ConsumedInputRevision != 1 {
		t.Fatalf("continued stream=%#v found=%t err=%v", stream, found, err)
	}
	secondClaim, err := app.transcriptStore.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "completed-review-second-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !secondClaim.Claimed || secondClaim.Claim.Attempt != 2 || secondClaim.Claim.ClaimedInputRevision != 2 {
		t.Fatalf("second claim=%#v err=%v", secondClaim, err)
	}
}
