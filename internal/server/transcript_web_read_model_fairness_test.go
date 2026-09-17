package server

import (
	"context"
	"testing"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestTranscriptWebReadModelBatchSkipsTerminalQuarantineAndAdvancesActiveWork(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-fair", "project-a-terminal", "frame-a-terminal")
	seedTranscriptWebFrame(t, store, "owner-fair", "project-z-active", "frame-z-active")

	createStream := func(frameID, projectID string) transcriptstore.Stream {
		t.Helper()
		stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
			UID: "stream:" + frameID, OwnerID: "owner-fair", ExternalID: frameID,
			SessionID: frameID, Kind: transcriptstore.StreamKindFrameRef,
			ProjectID: projectID, RootFrameID: frameID, FrameID: frameID, Epoch: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		return stream
	}
	appendMessage := func(stream transcriptstore.Stream, suffix string) {
		t.Helper()
		if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID,
			ClientMessageID: stream.SessionID + "-client-" + suffix,
			FrameEventID:    stream.SessionID + "-event-" + suffix,
			MessageUUID:     stream.SessionID + "-message-" + suffix, Text: suffix,
		}); err != nil || !created {
			t.Fatalf("append %s created=%t err=%v", stream.SessionID, created, err)
		}
	}

	terminal := createStream("frame-a-terminal", "project-a-terminal")
	active := createStream("frame-z-active", "project-z-active")
	appendMessage(terminal, "initial")
	appendMessage(active, "initial")
	readModel := transcriptstore.NewWebReadModelRepository(db, db)
	server := &Server{workspaceStore: store, transcriptStore: repo, transcriptWebReadModel: readModel}
	if err := server.runTranscriptWebReadModelCycle(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`UPDATE transcript_web_projection_state
		SET status='quarantined',last_error_code='projection_source_conflict'
		WHERE stream_uid=?`, terminal.UID); err != nil {
		t.Fatal(err)
	}
	appendMessage(terminal, "unrepairable-tail")
	if changed, err := store.TransitionCompatibilityFrameStatus(
		terminal.FrameID, "processing", "failed",
	); err != nil || !changed {
		t.Fatalf("terminal status changed=%t err=%v", changed, err)
	}
	appendMessage(active, "new-visible-tail")
	activeTarget, err := repo.GetProjectionSnapshot(context.Background(), active.UID, active.OwnerID)
	if err != nil {
		t.Fatal(err)
	}

	first, err := readModel.ListTranscriptWebProjectionWork(context.Background(), active.OwnerID, 1)
	if err != nil || len(first) != 1 || first[0].StreamUID != terminal.UID ||
		first[0].ProjectionStatus != "quarantined" {
		t.Fatalf("first work=%#v err=%v", first, err)
	}
	if err := server.runTranscriptWebReadModelBatch(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	activeFence, err := readModel.GetTranscriptWebProjectionFence(
		context.Background(), active.OwnerID, active.UID, activeTarget.BranchID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !transcriptWebFenceReady(activeFence) ||
		activeFence.StateThroughPublicationSequence != activeTarget.ThroughPublicationSequence {
		t.Fatalf("active projection starved behind terminal quarantine: fence=%#v target=%#v",
			activeFence, activeTarget)
	}
}
