package server

import (
	"context"
	"os"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

// This is an opt-in scale gate because it constructs the exact 100,000-message
// Reference server-range boundary. Ordinary focused runs keep using the
// 10,000-message regression; release/performance windows set SYNON_SCALE_TESTS.
func TestTranscriptWebHundredThousandMessageWindowAndRestart(t *testing.T) {
	if os.Getenv("SYNON_SCALE_TESTS") == "" {
		t.Skip("set SYNON_SCALE_TESTS=1 for the 100,000-message scale gate")
	}
	store, repository, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-scale", "project-scale", "frame-scale")
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-scale", OwnerID: "owner-scale", ExternalID: "frame-scale", SessionID: "frame-scale",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project-scale", RootFrameID: "frame-scale",
		FrameID: "frame-scale", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	branch, err := repository.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	seedTranscriptWebIncrementalUserEvents(t, db, stream.UID, branch.ActiveBranchID, 100_000, "scale")
	readModel := transcriptstore.NewWebReadModelRepository(db, db)
	server := &Server{workspaceStore: store, transcriptStore: repository, transcriptWebReadModel: readModel}
	if err := server.rebuildTranscriptWebReadModel(
		context.Background(), requireSingleTranscriptWebProjectionWork(t, readModel, stream.OwnerID),
	); err != nil {
		t.Fatal(err)
	}
	state := requireTranscriptWebReadModelFence(t, readModel, stream, branch.ActiveBranchID)
	if state.VisibleMessageCount != 100_000 {
		t.Fatalf("visible messages = %d, want 100000", state.VisibleMessageCount)
	}
	projectionDuration := time.Since(started)

	pageInput := transcriptstore.TranscriptWebMessagePageInput{
		OwnerID: stream.OwnerID, StreamUID: stream.UID, BranchID: branch.ActiveBranchID,
		BranchGeneration: state.BranchGeneration, ThroughPublicationSequence: state.ThroughPublicationSequence,
		SourceRevision: state.SourceRevision, Limit: 200,
	}
	pageStarted := time.Now()
	latest, found, err := readModel.GetTranscriptWebMessageRange(context.Background(), pageInput)
	latestDuration := time.Since(pageStarted)
	if err != nil || !found || latest.From != 99_800 || latest.Total != 100_000 || len(latest.Messages) != 200 {
		t.Fatalf("latest page found=%t err=%v page=%#v", found, err, latest)
	}

	from := 49_900
	pageInput.From = &from
	pageStarted = time.Now()
	middle, found, err := readModel.GetTranscriptWebMessageRange(context.Background(), pageInput)
	middleDuration := time.Since(pageStarted)
	if err != nil || !found || middle.From != from || len(middle.Messages) != 200 {
		t.Fatalf("middle page found=%t err=%v page=%#v", found, err, middle)
	}

	restarted := transcriptstore.NewWebReadModelRepository(db, db)
	pageStarted = time.Now()
	reopened, found, err := restarted.GetTranscriptWebMessageRange(context.Background(), pageInput)
	restartDuration := time.Since(pageStarted)
	if err != nil || !found || len(reopened.Messages) != 200 || reopened.Messages[0].MessageID != middle.Messages[0].MessageID {
		t.Fatalf("restart page found=%t err=%v page=%#v", found, err, reopened)
	}
	t.Logf("100k projection=%s latest200=%s middle200=%s restart200=%s", projectionDuration, latestDuration, middleDuration, restartDuration)
}
