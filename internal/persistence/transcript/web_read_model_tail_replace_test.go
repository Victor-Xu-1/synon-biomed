package transcript

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestApplyTranscriptWebProjectionTailReplacementIsAtomicAndRestartSafe(t *testing.T) {
	repository, db, dsn := newTranscriptRepository(t)
	fixture := seedTranscriptWebWorkFrame(t, repository, db, "owner-tail", "session-tail", "stream-tail", 1)
	appendUser := func(id string) Event {
		t.Helper()
		event, created, err := repository.AppendUserEvent(context.Background(), AppendUserEventInput{
			StreamUID: fixture.streamUID, OwnerID: fixture.ownerID, ClientMessageID: id,
			PayloadJSON: []byte(`{"text":"` + id + `"}`), Destinations: []string{"ws"},
		})
		if err != nil || !created {
			t.Fatalf("append %s created=%t err=%v", id, created, err)
		}
		return event
	}
	firstEvent := appendUser("source-1")
	secondEvent := appendUser("source-2")
	firstSnapshot, err := repository.GetProjectionSnapshot(context.Background(), fixture.streamUID, fixture.ownerID)
	if err != nil {
		t.Fatal(err)
	}
	readModel := NewWebReadModelRepository(db, db)
	firstState := transcriptWebTailTestState(fixture, firstSnapshot, 1, 2, 2, "chain-first")
	firstMessage := transcriptWebTailTestMessage(1, 0, "user-stable", firstEvent, "user")
	oldTail := transcriptWebTailTestMessage(2, 1, "assistant-old", secondEvent, "old")
	if err := readModel.ApplyTranscriptWebProjection(context.Background(), ApplyTranscriptWebProjectionInput{
		OwnerID: fixture.ownerID, State: firstState, ReplaceAll: true,
		Messages: []TranscriptWebMessageRecord{firstMessage, oldTail},
	}); err != nil {
		t.Fatal(err)
	}

	replacementEvent := appendUser("source-3")
	secondSnapshot, err := repository.GetProjectionSnapshot(context.Background(), fixture.streamUID, fixture.ownerID)
	if err != nil {
		t.Fatal(err)
	}
	replacement := transcriptWebTailTestMessage(2, 1, "assistant-new", replacementEvent, "new")
	secondState := transcriptWebTailTestState(fixture, secondSnapshot, 2, 2, 2, "chain-second")
	if err := readModel.ApplyTranscriptWebProjection(context.Background(), ApplyTranscriptWebProjectionInput{
		OwnerID: fixture.ownerID, State: secondState,
		ExpectedBranchGeneration:   firstState.BranchGeneration,
		ExpectedThroughPublication: firstState.ThroughPublicationSequence,
		ExpectedProjectionRevision: firstState.ProjectionRevision,
		ExpectedSourceRevision:     firstState.SourceRevision,
		ExpectedSourceChainSHA256:  firstState.SourceChainSHA256,
		TailReplaceFromOrdinal:     2, Messages: []TranscriptWebMessageRecord{replacement},
	}); err != nil {
		t.Fatal(err)
	}
	state, messages, found, err := readModel.GetTranscriptWebProjectionCheckpoint(
		context.Background(), fixture.ownerID, fixture.streamUID, fixture.branchID,
	)
	if err != nil || !found || state.MessageCount != 2 || len(messages) != 2 ||
		messages[0].MessageID != "user-stable" || messages[1].MessageID != "assistant-new" {
		t.Fatalf("tail state=%#v messages=%#v found=%t err=%v", state, messages, found, err)
	}

	badState := transcriptWebTailTestState(fixture, secondSnapshot, 3, 2, 2, "chain-bad")
	badReplacement := transcriptWebTailTestMessage(2, 1, "user-stable", replacementEvent, "collision")
	if err := readModel.ApplyTranscriptWebProjection(context.Background(), ApplyTranscriptWebProjectionInput{
		OwnerID: fixture.ownerID, State: badState,
		ExpectedBranchGeneration:   secondState.BranchGeneration,
		ExpectedThroughPublication: secondState.ThroughPublicationSequence,
		ExpectedProjectionRevision: secondState.ProjectionRevision,
		ExpectedSourceRevision:     secondState.SourceRevision,
		ExpectedSourceChainSHA256:  secondState.SourceChainSHA256,
		TailReplaceFromOrdinal:     2, Messages: []TranscriptWebMessageRecord{badReplacement},
	}); err == nil {
		t.Fatal("identity-colliding tail replacement was accepted")
	}
	_, messages, found, err = readModel.GetTranscriptWebProjectionCheckpoint(
		context.Background(), fixture.ownerID, fixture.streamUID, fixture.branchID,
	)
	if err != nil || !found || len(messages) != 2 || messages[1].MessageID != "assistant-new" {
		t.Fatalf("failed replacement was not atomic: messages=%#v found=%t err=%v", messages, found, err)
	}

	if err := readModel.ApplyTranscriptWebProjection(context.Background(), ApplyTranscriptWebProjectionInput{
		OwnerID: fixture.ownerID, State: badState,
		ExpectedBranchGeneration:   firstState.BranchGeneration,
		ExpectedThroughPublication: firstState.ThroughPublicationSequence,
		ExpectedProjectionRevision: firstState.ProjectionRevision,
		ExpectedSourceRevision:     firstState.SourceRevision,
		ExpectedSourceChainSHA256:  firstState.SourceChainSHA256,
		TailReplaceFromOrdinal:     2, Messages: []TranscriptWebMessageRecord{replacement},
	}); !errors.Is(err, ErrTranscriptWebProjectionStale) {
		t.Fatalf("concurrent fence error=%v", err)
	}
	if err := readModel.ApplyTranscriptWebProjection(context.Background(), ApplyTranscriptWebProjectionInput{
		OwnerID: fixture.ownerID, State: badState,
		ExpectedBranchGeneration:   secondState.BranchGeneration,
		ExpectedThroughPublication: secondState.ThroughPublicationSequence,
		ExpectedProjectionRevision: secondState.ProjectionRevision,
		ExpectedSourceRevision:     secondState.SourceRevision,
		ExpectedSourceChainSHA256:  secondState.SourceChainSHA256,
		TailReplaceFromOrdinal:     1, Messages: []TranscriptWebMessageRecord{replacement},
	}); err == nil {
		t.Fatal("non-tail replacement was accepted")
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedDB.Close()
	reopened := NewWebReadModelRepository(reopenedDB, reopenedDB)
	state, messages, found, err = reopened.GetTranscriptWebProjectionCheckpoint(
		context.Background(), fixture.ownerID, fixture.streamUID, fixture.branchID,
	)
	if err != nil || !found || state.ProjectionRevision != 2 || len(messages) != 2 ||
		messages[1].MessageID != "assistant-new" {
		t.Fatalf("reopened state=%#v messages=%#v found=%t err=%v", state, messages, found, err)
	}
}

func transcriptWebTailTestState(
	fixture transcriptWebWorkFrameFixture,
	snapshot ProjectionSnapshot,
	revision int64,
	messageCount, visibleCount int,
	chainSeed string,
) TranscriptWebProjectionState {
	checkpoint := []byte(`{"version":3}`)
	return TranscriptWebProjectionState{
		StreamUID: fixture.streamUID, BranchID: fixture.branchID,
		BranchGeneration: snapshot.BranchGeneration, ProjectorVersion: TranscriptWebProjectorVersion,
		ProjectionRevision: revision, ThroughPublicationSequence: snapshot.ThroughPublicationSequence,
		SourceRevision: 0, MessageCount: messageCount, VisibleMessageCount: visibleCount,
		ProjectorStateJSON: checkpoint, ProjectorStateSHA256: TranscriptWebSHA256(checkpoint),
		SourceChainSHA256: TranscriptWebSHA256([]byte(chainSeed)), Status: "ready", UpdatedAt: time.Now().UTC(),
	}
}

func transcriptWebTailTestMessage(
	ordinal, visibleIndex int,
	messageID string,
	event Event,
	text string,
) TranscriptWebMessageRecord {
	raw, _ := json.Marshal(map[string]any{
		"id": messageID, "msg_id": messageID, "conversation_id": "session-tail",
		"type": "text", "position": "left", "status": "work",
		"created_at": event.CreatedAt.UnixMilli(), "content": map[string]any{"content": text},
	})
	return TranscriptWebMessageRecord{
		Ordinal: ordinal, MessageID: messageID, ClientMessageID: messageID,
		Visible: true, VisibleIndex: &visibleIndex, MessageJSON: raw,
		MessageSHA256: TranscriptWebSHA256(raw), FirstPublicationSequence: event.PublicationSeq,
		LastPublicationSequence: event.PublicationSeq, UpdatedAt: event.CreatedAt.UTC(),
	}
}
