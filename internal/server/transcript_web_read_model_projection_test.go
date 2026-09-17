package server

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
	"synon-go/internal/persistence/workspace"
)

func TestRunTranscriptWebReadModelCycleBuildsAndRefreshesDurableProjection(t *testing.T) {
	store, repository, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-projection", "project-projection", "frame-projection")
	userArtifact := seedTranscriptWebReadModelArtifact(
		t, store, db, "project-projection", "frame-projection",
		"artifact-user", "user-input.txt", []byte("attached input"), "user",
	)
	runnerArtifact := seedTranscriptWebReadModelArtifact(
		t, store, db, "project-projection", "frame-projection",
		"artifact-runner", "runner-result.txt", []byte("runner result"), "runner",
	)
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-projection", OwnerID: "owner-projection", ExternalID: "frame-projection",
		SessionID: "frame-projection", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-projection", RootFrameID: "frame-projection", FrameID: "frame-projection", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	branch, err := repository.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	readModel := transcriptstore.NewWebReadModelRepository(db, db)
	server := &Server{workspaceStore: store, transcriptStore: repository, transcriptWebReadModel: readModel}

	userEvent, _, created, err := repository.AppendFrameUserEvent(
		context.Background(), transcriptstore.AppendFrameUserEventInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "user-client-1",
			FrameEventID: "frame-event-user-1", MessageUUID: "user-message-1", Text: "analyze attached input",
			ArtifactReferences: []transcriptstore.UserArtifactReferenceInput{{
				ArtifactID: userArtifact.artifactID, VersionID: userArtifact.versionID,
			}},
			Destinations: []string{transcriptWebDestination},
		},
	)
	if err != nil || !created || userEvent.PublicationSeq != 1 {
		t.Fatalf("user event=%#v created=%t err=%v", userEvent, created, err)
	}

	fence, err := readModel.GetTranscriptWebProjectionFence(
		context.Background(), stream.OwnerID, stream.UID, branch.ActiveBranchID,
	)
	if err != nil || fence.StateFound || fence.BranchGeneration != branch.Generation ||
		fence.ThroughPublicationSequence != userEvent.PublicationSeq || fence.SourceRevision != 0 {
		t.Fatalf("missing fence=%#v err=%v", fence, err)
	}
	if foreign, err := readModel.GetTranscriptWebProjectionFence(
		context.Background(), "owner-foreign", stream.UID, branch.ActiveBranchID,
	); !errors.Is(err, transcriptstore.ErrOwnerMismatch) || !reflect.DeepEqual(foreign, transcriptstore.TranscriptWebProjectionFence{}) {
		t.Fatalf("foreign fence=%#v err=%v", foreign, err)
	}
	if inactive, err := readModel.GetTranscriptWebProjectionFence(
		context.Background(), stream.OwnerID, stream.UID, "br_deadbeef",
	); !errors.Is(err, transcriptstore.ErrBranchStateStale) || !reflect.DeepEqual(inactive, transcriptstore.TranscriptWebProjectionFence{}) {
		t.Fatalf("inactive fence=%#v err=%v", inactive, err)
	}
	work, err := readModel.ListTranscriptWebProjectionWork(context.Background(), stream.OwnerID, 10)
	if err != nil || len(work) != 1 || work[0].Status != transcriptstore.TranscriptWebProjectionWorkMissing ||
		work[0].BranchID != branch.ActiveBranchID || work[0].BranchGeneration != branch.Generation ||
		work[0].ThroughOrdinal != 1 || work[0].ThroughPublicationSequence != userEvent.PublicationSeq ||
		work[0].SourceRevision != 0 {
		t.Fatalf("missing work=%#v err=%v", work, err)
	}
	if foreignWork, err := readModel.ListTranscriptWebProjectionWork(context.Background(), "owner-foreign", 10); err != nil || len(foreignWork) != 0 {
		t.Fatalf("foreign work=%#v err=%v", foreignWork, err)
	}

	if err := server.runTranscriptWebReadModelCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	ready := requireTranscriptWebReadModelFence(t, readModel, stream, branch.ActiveBranchID)
	if ready.StateStatus != "ready" || ready.StateProjectionRevision != 1 || ready.StateMessageCount != 1 ||
		ready.VisibleMessageCount != 1 || ready.StateArtifactReferenceCount != 1 ||
		ready.StateBranchGeneration != ready.BranchGeneration ||
		ready.StateThroughPublicationSequence != ready.ThroughPublicationSequence ||
		ready.StateSourceRevision != ready.SourceRevision || ready.StateSourceChainSHA256 == "" {
		t.Fatalf("initial ready fence=%#v", ready)
	}
	page := requireTranscriptWebReadModelPage(t, readModel, stream, ready)
	if len(page.Messages) != 1 {
		t.Fatalf("initial page=%#v", page)
	}
	assertTranscriptWebReadModelMessage(t, page.Messages[0], 0, "user-message-1", "user-client-1", 1, 1)
	assertTranscriptWebReadModelUserReference(t, page.Messages[0], userEvent.EventID, userArtifact)

	stable := readTranscriptWebProjectionClock(t, db, stream.UID, branch.ActiveBranchID)
	if err := server.runTranscriptWebReadModelCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if after := readTranscriptWebProjectionClock(t, db, stream.UID, branch.ActiveBranchID); after != stable {
		t.Fatalf("ready no-op changed state before=%#v after=%#v", stable, after)
	}

	secondUser, _, created, err := repository.AppendFrameUserEvent(
		context.Background(), transcriptstore.AppendFrameUserEventInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "user-client-2",
			FrameEventID: "frame-event-user-2", MessageUUID: "user-message-2", Text: "continue",
			Destinations: []string{transcriptWebDestination},
		},
	)
	if err != nil || !created || secondUser.PublicationSeq != 2 {
		t.Fatalf("second user=%#v created=%t err=%v", secondUser, created, err)
	}
	staleFence := requireTranscriptWebReadModelFence(t, readModel, stream, branch.ActiveBranchID)
	if staleFence.ThroughPublicationSequence != 2 || staleFence.StateThroughPublicationSequence != 1 ||
		staleFence.SourceRevision != staleFence.StateSourceRevision {
		t.Fatalf("append fence=%#v", staleFence)
	}
	work, err = readModel.ListTranscriptWebProjectionWork(context.Background(), stream.OwnerID, 10)
	if err != nil || len(work) != 1 || work[0].Status != transcriptstore.TranscriptWebProjectionWorkStale {
		t.Fatalf("append work=%#v err=%v", work, err)
	}
	if err := server.runTranscriptWebReadModelCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	ready = requireTranscriptWebReadModelFence(t, readModel, stream, branch.ActiveBranchID)
	if ready.StateStatus != "ready" || ready.StateProjectionRevision != 2 ||
		ready.StateThroughPublicationSequence != 2 || ready.StateMessageCount != 2 {
		t.Fatalf("append rebuilt fence=%#v", ready)
	}
	page = requireTranscriptWebReadModelPage(t, readModel, stream, ready)
	if len(page.Messages) != 2 {
		t.Fatalf("append page=%#v", page)
	}
	assertTranscriptWebReadModelMessage(t, page.Messages[0], 0, "user-message-1", "user-client-1", 1, 1)
	assertTranscriptWebReadModelMessage(t, page.Messages[1], 1, "user-message-2", "user-client-2", 2, 2)

	claimed, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-projection",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	assistantMessageID := transcriptAssistantMessageID(stream.SessionID, claimed.Claim.Attempt, 3)
	assistantEvent, references, created, err := repository.AppendAssistantEventWithArtifacts(
		context.Background(), transcriptstore.AppendAssistantEventWithArtifactsInput{
			Claim: claimed.Claim, ClientMessageID: "assistant-client-1", Source: transcriptstore.EventSourcePayload,
			PayloadJSON:  []byte(`{"text":"runner result","assistant_segment":{"version":1,"ordinal":3}}`),
			Destinations: []string{transcriptWebDestination},
			References: []transcriptstore.ArtifactReferenceInput{{
				ArtifactID: runnerArtifact.artifactID, VersionID: runnerArtifact.versionID,
				Relation: transcriptstore.ArtifactRelationProduced,
			}},
		},
	)
	if err != nil || !created || len(references) != 1 || assistantEvent.PublicationSeq != 3 {
		t.Fatalf("assistant event=%#v refs=%#v created=%t err=%v", assistantEvent, references, created, err)
	}
	finishEvent, _, _, err := repository.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "finish-client-1", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed","assistant_segment":{"version":1,"ordinal":3}}`),
	})
	if err != nil || finishEvent.PublicationSeq != 4 {
		t.Fatalf("finish event=%#v err=%v", finishEvent, err)
	}
	work, err = readModel.ListTranscriptWebProjectionWork(context.Background(), stream.OwnerID, 10)
	if err != nil || len(work) != 1 || work[0].Status != transcriptstore.TranscriptWebProjectionWorkDirty ||
		work[0].DirtyFirstAffectedOrdinal != 3 || work[0].DirtyReasonMask&1 == 0 {
		t.Fatalf("artifact work=%#v err=%v", work, err)
	}
	if err := server.runTranscriptWebReadModelCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	ready = requireTranscriptWebReadModelFence(t, readModel, stream, branch.ActiveBranchID)
	if ready.StateStatus != "ready" || ready.StateProjectionRevision != 3 ||
		ready.StateThroughPublicationSequence != 4 || ready.StateSourceRevision != 1 ||
		ready.StateMessageCount != 3 || ready.StateArtifactReferenceCount != 2 {
		t.Fatalf("artifact rebuilt fence=%#v", ready)
	}
	page = requireTranscriptWebReadModelPage(t, readModel, stream, ready)
	if len(page.Messages) != 3 {
		t.Fatalf("artifact page=%#v", page)
	}
	assertTranscriptWebReadModelMessage(t, page.Messages[2], 2, assistantMessageID, assistantMessageID, 3, 4)
	assertTranscriptWebReadModelRunnerReference(
		t, page.Messages[2], assistantEvent.EventID, claimed.Claim.Attempt, runnerArtifact,
	)
	stable = readTranscriptWebProjectionClock(t, db, stream.UID, branch.ActiveBranchID)
	if err := server.runTranscriptWebReadModelCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if after := readTranscriptWebProjectionClock(t, db, stream.UID, branch.ActiveBranchID); after != stable {
		t.Fatalf("final ready no-op changed state before=%#v after=%#v", stable, after)
	}
}

func TestTranscriptWebDeliveryPassRebuildsAnOlderQuarantinedProjector(t *testing.T) {
	store, repository, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-projector-upgrade", "project-projector-upgrade", "frame-projector-upgrade")
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-projector-upgrade", OwnerID: "owner-projector-upgrade", ExternalID: "frame-projector-upgrade",
		SessionID: "frame-projector-upgrade", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-projector-upgrade", RootFrameID: "frame-projector-upgrade", FrameID: "frame-projector-upgrade", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	branch, err := repository.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := repository.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "projector-upgrade-user",
		PayloadJSON: []byte(`{"text":"preserve this history"}`), Destinations: []string{transcriptWebDestination},
	}); err != nil || !created {
		t.Fatalf("append user created=%t err=%v", created, err)
	}
	readModel := transcriptstore.NewWebReadModelRepository(db, db)
	server := &Server{workspaceStore: store, transcriptStore: repository, transcriptWebReadModel: readModel}
	if err := server.runTranscriptWebReadModelCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE transcript_web_projection_state
		SET projector_version=1,status='quarantined',last_error_code='projection_source_conflict'
		WHERE stream_uid=? AND branch_id=?`, stream.UID, branch.ActiveBranchID); err != nil {
		t.Fatal(err)
	}

	if _, err := server.runTranscriptWebDeliveryPass(context.Background()); err != nil {
		t.Fatalf("delivery pass rejected stale projector state: %v", err)
	}
	fence := requireTranscriptWebReadModelFence(t, readModel, stream, branch.ActiveBranchID)
	if fence.StateProjectorVersion != transcriptstore.TranscriptWebProjectorVersion ||
		fence.StateStatus != "ready" || fence.VisibleMessageCount != 1 {
		t.Fatalf("rebuilt projector fence=%#v", fence)
	}
}

func TestRunTranscriptWebReadModelCycleQuarantineIsStableAcrossTicks(t *testing.T) {
	store, repository, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-conflict", "project-conflict", "frame-conflict")
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-conflict", OwnerID: "owner-conflict", ExternalID: "frame-conflict",
		SessionID: "frame-conflict", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-conflict", RootFrameID: "frame-conflict", FrameID: "frame-conflict", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	branch, err := repository.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	event, _, created, err := repository.AppendFrameUserEvent(
		context.Background(), transcriptstore.AppendFrameUserEventInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "conflict-client",
			FrameEventID: "frame-event-conflict", MessageUUID: "conflict-message", Text: "valid before corruption",
			Destinations: []string{transcriptWebDestination},
		},
	)
	if err != nil || !created {
		t.Fatalf("event=%#v created=%t err=%v", event, created, err)
	}
	readModel := transcriptstore.NewWebReadModelRepository(db, db)
	server := &Server{workspaceStore: store, transcriptStore: repository, transcriptWebReadModel: readModel}
	if err := server.runTranscriptWebReadModelCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	ready := requireTranscriptWebReadModelFence(t, readModel, stream, branch.ActiveBranchID)
	if ready.StateStatus != "ready" || ready.StateProjectionRevision != 1 {
		t.Fatalf("ready fence=%#v", ready)
	}
	if _, err := db.Exec(`UPDATE transcript_events SET payload_json=? WHERE stream_uid=? AND event_id=?`,
		[]byte(`{"invalid"`), stream.UID, event.EventID); err != nil {
		t.Fatal(err)
	}
	if err := server.runTranscriptWebReadModelCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	quarantined := readTranscriptWebProjectionClock(t, db, stream.UID, branch.ActiveBranchID)
	if quarantined.status != "quarantined" || quarantined.lastErrorCode != "projection_source_conflict" ||
		quarantined.revision != 2 {
		t.Fatalf("quarantined state=%#v", quarantined)
	}
	fence := requireTranscriptWebReadModelFence(t, readModel, stream, branch.ActiveBranchID)
	if fence.StateStatus != "quarantined" || fence.StateSourceRevision != fence.SourceRevision ||
		fence.StateThroughPublicationSequence != fence.ThroughPublicationSequence {
		t.Fatalf("quarantined fence=%#v", fence)
	}
	work, err := readModel.ListTranscriptWebProjectionWork(context.Background(), stream.OwnerID, 10)
	if err != nil || len(work) != 1 || work[0].ProjectionStatus != "quarantined" {
		t.Fatalf("quarantined work=%#v err=%v", work, err)
	}
	for tick := 0; tick < 4; tick++ {
		if err := server.runTranscriptWebReadModelCycle(context.Background()); err != nil {
			t.Fatalf("tick %d: %v", tick, err)
		}
	}
	if after := readTranscriptWebProjectionClock(t, db, stream.UID, branch.ActiveBranchID); after != quarantined {
		t.Fatalf("quarantined projection rebuilt on later ticks before=%#v after=%#v", quarantined, after)
	}
	server.projectionRetryMu.Lock()
	server.projectionRetryAt[stream.UID] = time.Now().Add(-transcriptWebProjectionRetryInterval - time.Second)
	server.projectionRetryMu.Unlock()
	if err := server.runTranscriptWebReadModelCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if after := readTranscriptWebProjectionClock(t, db, stream.UID, branch.ActiveBranchID); after != quarantined {
		t.Fatalf("unchanged quarantined source retried after backoff before=%#v after=%#v", quarantined, after)
	}
}

func TestRunTranscriptWebReadModelCycleRecoversQuarantineAfterRetryWindow(t *testing.T) {
	store, repository, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-recover", "project-recover", "frame-recover")
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-recover", OwnerID: "owner-recover", ExternalID: "frame-recover",
		SessionID: "frame-recover", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-recover", RootFrameID: "frame-recover", FrameID: "frame-recover", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	branch, err := repository.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	event, _, created, err := repository.AppendFrameUserEvent(
		context.Background(), transcriptstore.AppendFrameUserEventInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "recover-client",
			FrameEventID: "frame-event-recover", MessageUUID: "recover-message", Text: "valid before corruption",
			Destinations: []string{transcriptWebDestination},
		},
	)
	if err != nil || !created {
		t.Fatalf("event=%#v created=%t err=%v", event, created, err)
	}
	readModel := transcriptstore.NewWebReadModelRepository(db, db)
	server := &Server{workspaceStore: store, transcriptStore: repository, transcriptWebReadModel: readModel}
	if err := server.runTranscriptWebReadModelCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Corrupt the payload to force a deterministic projection conflict.
	if _, err := db.Exec(`UPDATE transcript_events SET payload_json=? WHERE stream_uid=? AND event_id=?`,
		[]byte(`{"invalid"`), stream.UID, event.EventID); err != nil {
		t.Fatal(err)
	}
	if err := server.runTranscriptWebReadModelCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	quarantined := readTranscriptWebProjectionClock(t, db, stream.UID, branch.ActiveBranchID)
	if quarantined.status != "quarantined" {
		t.Fatalf("expected quarantine, got %#v", quarantined)
	}
	// Immediately repeated ticks must stay quarantined (bounded retry window).
	for tick := 0; tick < 2; tick++ {
		if err := server.runTranscriptWebReadModelCycle(context.Background()); err != nil {
			t.Fatalf("tick %d: %v", tick, err)
		}
	}
	if after := readTranscriptWebProjectionClock(t, db, stream.UID, branch.ActiveBranchID); after.status != "quarantined" {
		t.Fatalf("projection rebuilt before retry window: %#v", after)
	}
	// A streaming runner can continue appending source events after one bad
	// event quarantines the projection. New source coordinates must not bypass
	// the same bounded retry window and turn every token/tool update into a full
	// rebuild attempt.
	if _, err := db.Exec(`UPDATE transcript_events SET payload_json=? WHERE stream_uid=? AND event_id=?`,
		[]byte(`{"messageUuid":"recover-message","text":"temporarily valid","role":"user","messageOrigin":"task_intent"}`),
		stream.UID, event.EventID); err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repository.AppendFrameUserEvent(
		context.Background(), transcriptstore.AppendFrameUserEventInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "recover-client-continued",
			FrameEventID: "frame-event-recover-continued", MessageUUID: "recover-message-continued",
			Text: "source continued while quarantined", Destinations: []string{transcriptWebDestination},
		},
	); err != nil || !created {
		t.Fatalf("continued source created=%t err=%v", created, err)
	}
	if _, err := db.Exec(`UPDATE transcript_events SET payload_json=? WHERE stream_uid=? AND event_id=?`,
		[]byte(`{"invalid"`), stream.UID, event.EventID); err != nil {
		t.Fatal(err)
	}
	if err := server.runTranscriptWebReadModelCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if after := readTranscriptWebProjectionClock(t, db, stream.UID, branch.ActiveBranchID); after != quarantined {
		t.Fatalf("advancing source bypassed quarantine retry window before=%#v after=%#v", quarantined, after)
	}
	// Repair the source, then advance the retry clock past the window so the
	// next cycle rebuilds and recovers the projection automatically.
	if _, err := db.Exec(`UPDATE transcript_events SET payload_json=? WHERE stream_uid=? AND event_id=?`,
		[]byte(`{"messageUuid":"recover-message","text":"valid after repair","role":"user","messageOrigin":"task_intent"}`),
		stream.UID, event.EventID); err != nil {
		t.Fatal(err)
	}
	server.projectionRetryMu.Lock()
	if server.projectionRetryAt == nil {
		server.projectionRetryAt = map[string]time.Time{}
	}
	server.projectionRetryAt[stream.UID] = time.Now().Add(-transcriptWebProjectionRetryInterval - time.Second)
	server.projectionRetryMu.Unlock()
	if err := server.runTranscriptWebReadModelCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	recovered := readTranscriptWebProjectionClock(t, db, stream.UID, branch.ActiveBranchID)
	if recovered.status != "ready" {
		t.Fatalf("projection did not recover after retry window: %#v", recovered)
	}
}

type transcriptWebReadModelArtifactFixture struct {
	artifactID, versionID, filename, contentType, checksum string
	sizeBytes                                              int64
}

func seedTranscriptWebReadModelArtifact(
	t *testing.T,
	store *workspace.Store,
	db *sql.DB,
	projectID, frameID, artifactID, filename string,
	content []byte,
	createdBy string,
) transcriptWebReadModelArtifactFixture {
	t.Helper()
	artifact, version, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: artifactID, ProjectID: projectID, Name: filename, Kind: "text",
		Content: content, CreatedBy: createdBy,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO artifact_runtime_metadata(artifact_id,root_frame_id,frame_id) VALUES(?,?,?)`,
		artifact.ID, frameID, frameID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO artifact_version_provenance(version_id,frame_id,content_type) VALUES(?,?,?)`,
		version.ID, frameID, "text/plain"); err != nil {
		t.Fatal(err)
	}
	return transcriptWebReadModelArtifactFixture{
		artifactID: artifact.ID, versionID: version.ID, filename: artifact.Name, contentType: artifact.Kind,
		sizeBytes: int64(len(content)), checksum: version.ContentSHA256,
	}
}

func requireTranscriptWebReadModelFence(
	t *testing.T,
	readModel *transcriptstore.WebReadModelRepository,
	stream transcriptstore.Stream,
	branchID string,
) transcriptstore.TranscriptWebProjectionFence {
	t.Helper()
	fence, err := readModel.GetTranscriptWebProjectionFence(
		context.Background(), stream.OwnerID, stream.UID, branchID,
	)
	if err != nil {
		t.Fatal(err)
	}
	return fence
}

func requireTranscriptWebReadModelPage(
	t *testing.T,
	readModel *transcriptstore.WebReadModelRepository,
	stream transcriptstore.Stream,
	fence transcriptstore.TranscriptWebProjectionFence,
) transcriptstore.TranscriptWebMessagePage {
	t.Helper()
	from := 0
	page, found, err := readModel.GetTranscriptWebMessageRange(
		context.Background(), transcriptstore.TranscriptWebMessagePageInput{
			OwnerID: stream.OwnerID, StreamUID: stream.UID, BranchID: fence.BranchID,
			BranchGeneration:           fence.BranchGeneration,
			ThroughPublicationSequence: fence.ThroughPublicationSequence,
			SourceRevision:             fence.SourceRevision, From: &from, Limit: 100,
		},
	)
	if err != nil || !found {
		t.Fatalf("page found=%t err=%v", found, err)
	}
	return page
}

func assertTranscriptWebReadModelMessage(
	t *testing.T,
	record transcriptstore.TranscriptWebMessageRecord,
	visibleIndex int,
	messageID, clientMessageID string,
	firstPublication, lastPublication int64,
) {
	t.Helper()
	if record.Ordinal != visibleIndex+1 || !record.Visible || record.VisibleIndex == nil ||
		*record.VisibleIndex != visibleIndex || record.MessageID != messageID ||
		record.ClientMessageID != clientMessageID || record.FirstPublicationSequence != firstPublication ||
		record.LastPublicationSequence != lastPublication {
		t.Fatalf("message=%#v", record)
	}
}

func assertTranscriptWebReadModelUserReference(
	t *testing.T,
	record transcriptstore.TranscriptWebMessageRecord,
	sourceEventID int64,
	want transcriptWebReadModelArtifactFixture,
) {
	t.Helper()
	if len(record.ArtifactReferences) != 1 {
		t.Fatalf("user refs=%#v", record.ArtifactReferences)
	}
	reference := record.ArtifactReferences[0]
	if reference.MessageOrdinal != record.Ordinal || reference.Ordinal != 0 ||
		reference.SourceEventID != sourceEventID || reference.RunnerAttempt != nil ||
		reference.SourceReferenceOrdinal != 0 || reference.ArtifactID != want.artifactID ||
		reference.VersionID != want.versionID || reference.Relation != transcriptstore.ArtifactRelationAttached ||
		reference.Filename != want.filename || reference.ContentType != want.contentType ||
		reference.SizeBytes != want.sizeBytes || reference.Checksum != want.checksum ||
		reference.Availability != transcriptstore.ArtifactAvailable {
		t.Fatalf("user reference=%#v want=%#v", reference, want)
	}
}

func assertTranscriptWebReadModelRunnerReference(
	t *testing.T,
	record transcriptstore.TranscriptWebMessageRecord,
	sourceEventID, runnerAttempt int64,
	want transcriptWebReadModelArtifactFixture,
) {
	t.Helper()
	if len(record.ArtifactReferences) != 1 {
		t.Fatalf("runner refs=%#v", record.ArtifactReferences)
	}
	reference := record.ArtifactReferences[0]
	if reference.MessageOrdinal != record.Ordinal || reference.Ordinal != 0 ||
		reference.SourceEventID != sourceEventID || reference.RunnerAttempt == nil ||
		*reference.RunnerAttempt != runnerAttempt || reference.SourceReferenceOrdinal != 0 ||
		reference.ArtifactID != want.artifactID || reference.VersionID != want.versionID ||
		reference.Relation != transcriptstore.ArtifactRelationProduced || reference.Filename != want.filename ||
		reference.ContentType != want.contentType || reference.SizeBytes != want.sizeBytes ||
		reference.Checksum != want.checksum || reference.Availability != transcriptstore.ArtifactAvailable {
		t.Fatalf("runner reference=%#v want=%#v", reference, want)
	}
}

type transcriptWebProjectionClock struct {
	revision      int64
	status        string
	lastErrorCode string
	updatedAt     string
}

func readTranscriptWebProjectionClock(
	t *testing.T,
	db *sql.DB,
	streamUID, branchID string,
) transcriptWebProjectionClock {
	t.Helper()
	var clock transcriptWebProjectionClock
	if err := db.QueryRow(`SELECT projection_revision,status,last_error_code,updated_at
		FROM transcript_web_projection_state WHERE stream_uid=? AND branch_id=?`, streamUID, branchID).
		Scan(&clock.revision, &clock.status, &clock.lastErrorCode, &clock.updatedAt); err != nil {
		t.Fatal(err)
	}
	return clock
}
