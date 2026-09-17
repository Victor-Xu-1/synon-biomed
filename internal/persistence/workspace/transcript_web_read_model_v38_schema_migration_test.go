package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestTranscriptWebReadModelV38MigratesReopensAndPagesWithoutFullHistoryRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	status, err := store.SchemaStatus(context.Background())
	if err != nil || status.CurrentVersion != workspaceSchemaVersion || status.TargetVersion != workspaceSchemaVersion {
		t.Fatalf("schema status=%#v err=%v", status, err)
	}
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-web-v38", OwnerID: "owner-a", ExternalID: "external-web-v38",
		SessionID: "session-web-v38", Kind: transcriptstore.StreamKindStandalone, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	appendUser := func(id, text string) {
		t.Helper()
		if _, created, err := repository.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: id,
			PayloadJSON: []byte(`{"text":` + quoteJSONForTest(text) + `}`), Destinations: []string{"ws"},
		}); err != nil || !created {
			t.Fatalf("append %s created=%t err=%v", id, created, err)
		}
	}
	appendUser("user-1", "first")
	snapshot1, err := repository.GetProjectionSnapshot(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	readModel, err := store.TranscriptWebReadModel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	message1 := transcriptWebTestMessageWithIdentities(
		1, 0, "stable-user-1", "user-1", "first", snapshot1.ThroughPublicationSequence, now,
	)
	state1 := transcriptWebTestState(stream.UID, snapshot1, 1, 1, 1, "ready", now)
	if err := readModel.ApplyTranscriptWebProjection(context.Background(), transcriptstore.ApplyTranscriptWebProjectionInput{
		OwnerID: stream.OwnerID, State: state1, ReplaceAll: true,
		Messages: []transcriptstore.TranscriptWebMessageRecord{message1},
	}); err != nil {
		t.Fatal(err)
	}
	page, found, err := readModel.GetTranscriptWebMessageRange(context.Background(), transcriptstore.TranscriptWebMessagePageInput{
		OwnerID:   stream.OwnerID,
		StreamUID: stream.UID, BranchID: snapshot1.BranchID, BranchGeneration: snapshot1.BranchGeneration,
		ThroughPublicationSequence: snapshot1.ThroughPublicationSequence,
		Limit:                      50,
	})
	if err != nil || !found || len(page.Messages) != 1 || page.Messages[0].MessageID != "stable-user-1" ||
		page.From != 0 || page.Total != 1 || page.State.ProjectionRevision != 1 {
		t.Fatalf("initial page=%#v found=%t err=%v", page, found, err)
	}

	appendUser("user-2", "second")
	snapshot2, err := repository.GetProjectionSnapshot(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := readModel.GetTranscriptWebMessageRange(context.Background(), transcriptstore.TranscriptWebMessagePageInput{
		OwnerID:   stream.OwnerID,
		StreamUID: stream.UID, BranchID: snapshot1.BranchID, BranchGeneration: snapshot1.BranchGeneration,
		ThroughPublicationSequence: snapshot1.ThroughPublicationSequence,
		Limit:                      50,
	}); !errors.Is(err, transcriptstore.ErrTranscriptWebProjectionStale) {
		t.Fatalf("stale ready page error=%v", err)
	}
	building := transcriptWebTestState(stream.UID, snapshot1, 1, 1, 2, "building", now.Add(time.Second))
	if err := readModel.ApplyTranscriptWebProjection(context.Background(), transcriptstore.ApplyTranscriptWebProjectionInput{
		OwnerID: stream.OwnerID, State: building, ExpectedBranchGeneration: snapshot1.BranchGeneration,
		ExpectedThroughPublication: snapshot1.ThroughPublicationSequence, ExpectedProjectionRevision: 1,
		ExpectedSourceChainSHA256: state1.SourceChainSHA256,
	}); err != nil {
		t.Fatalf("building checkpoint: %v", err)
	}
	message2 := transcriptWebTestMessage(2, 1, "user-2", "second", snapshot2.ThroughPublicationSequence, now.Add(2*time.Second))
	ready2 := transcriptWebTestState(stream.UID, snapshot2, 2, 2, 3, "ready", now.Add(2*time.Second))
	if err := readModel.ApplyTranscriptWebProjection(context.Background(), transcriptstore.ApplyTranscriptWebProjectionInput{
		OwnerID: stream.OwnerID, State: ready2, ExpectedBranchGeneration: snapshot1.BranchGeneration,
		ExpectedThroughPublication: snapshot1.ThroughPublicationSequence, ExpectedProjectionRevision: 2,
		ExpectedSourceChainSHA256: building.SourceChainSHA256,
		Messages:                  []transcriptstore.TranscriptWebMessageRecord{message2},
	}); err != nil {
		t.Fatalf("incremental ready projection: %v", err)
	}
	fromZero := 0
	first, found, err := readModel.GetTranscriptWebMessageRange(context.Background(), transcriptstore.TranscriptWebMessagePageInput{
		OwnerID:   stream.OwnerID,
		StreamUID: stream.UID, BranchID: snapshot2.BranchID, BranchGeneration: snapshot2.BranchGeneration,
		ThroughPublicationSequence: snapshot2.ThroughPublicationSequence,
		From:                       &fromZero, Limit: 1,
	})
	if err != nil || !found || len(first.Messages) != 1 || first.Messages[0].MessageID != "stable-user-1" || first.From != 0 || first.Total != 2 {
		t.Fatalf("first range=%#v found=%t err=%v", first, found, err)
	}
	fromOne := 1
	second, found, err := readModel.GetTranscriptWebMessageRange(context.Background(), transcriptstore.TranscriptWebMessagePageInput{
		OwnerID:   stream.OwnerID,
		StreamUID: stream.UID, BranchID: snapshot2.BranchID, BranchGeneration: snapshot2.BranchGeneration,
		ThroughPublicationSequence: snapshot2.ThroughPublicationSequence,
		From:                       &fromOne, Limit: 1,
	})
	if err != nil || !found || len(second.Messages) != 1 || second.Messages[0].MessageID != "user-2" || second.From != 1 || second.Total != 2 {
		t.Fatalf("second range=%#v found=%t err=%v", second, found, err)
	}
	identity := transcriptstore.TranscriptWebMessageIdentityInput{
		OwnerID: stream.OwnerID, StreamUID: stream.UID, BranchID: snapshot2.BranchID,
		BranchGeneration: snapshot2.BranchGeneration, ThroughPublicationSequence: snapshot2.ThroughPublicationSequence,
		Identity: "user-2",
	}
	located, found, err := readModel.LocateTranscriptWebMessage(context.Background(), identity)
	if err != nil || !found || located != 1 {
		t.Fatalf("locate index=%d found=%t err=%v", located, found, err)
	}
	exact, found, err := readModel.GetTranscriptWebMessageByID(context.Background(), identity)
	if err != nil || !found || exact.MessageID != "user-2" || exact.VisibleIndex == nil || *exact.VisibleIndex != 1 {
		t.Fatalf("exact=%#v found=%t err=%v", exact, found, err)
	}
	identity.Identity = "missing-message"
	if _, found, err := readModel.GetTranscriptWebMessageByID(context.Background(), identity); err != nil || found {
		t.Fatalf("missing found=%t err=%v", found, err)
	}
	if err := readModel.ApplyTranscriptWebProjection(context.Background(), transcriptstore.ApplyTranscriptWebProjectionInput{
		OwnerID: stream.OwnerID, State: ready2, ExpectedBranchGeneration: snapshot1.BranchGeneration,
		ExpectedThroughPublication: snapshot1.ThroughPublicationSequence, ExpectedProjectionRevision: 1,
		ExpectedSourceChainSHA256: state1.SourceChainSHA256,
	}); !errors.Is(err, transcriptstore.ErrTranscriptWebProjectionStale) {
		t.Fatalf("stale writer error=%v", err)
	}
	if _, _, err := readModel.GetTranscriptWebMessageRange(context.Background(), transcriptstore.TranscriptWebMessagePageInput{
		OwnerID: "owner-b", StreamUID: stream.UID, BranchID: snapshot2.BranchID,
		BranchGeneration: snapshot2.BranchGeneration, ThroughPublicationSequence: snapshot2.ThroughPublicationSequence,
		Limit: 50,
	}); !errors.Is(err, transcriptstore.ErrOwnerMismatch) {
		t.Fatalf("foreign owner page error=%v", err)
	}
	if _, _, err := readModel.GetTranscriptWebMessageRange(context.Background(), transcriptstore.TranscriptWebMessagePageInput{
		OwnerID: "owner-b", StreamUID: stream.UID, BranchID: snapshot2.BranchID,
		BranchGeneration:           snapshot2.BranchGeneration + 10,
		ThroughPublicationSequence: snapshot2.ThroughPublicationSequence + 10,
		SourceRevision:             10, Limit: 50,
	}); !errors.Is(err, transcriptstore.ErrOwnerMismatch) {
		t.Fatalf("foreign owner stale-coordinate page error=%v", err)
	}
	foreignIdentity := transcriptstore.TranscriptWebMessageIdentityInput{
		OwnerID: "owner-b", StreamUID: stream.UID, BranchID: snapshot2.BranchID,
		BranchGeneration:           snapshot2.BranchGeneration + 10,
		ThroughPublicationSequence: snapshot2.ThroughPublicationSequence + 10,
		SourceRevision:             10, Identity: "user-2",
	}
	if _, _, err := readModel.LocateTranscriptWebMessage(context.Background(), foreignIdentity); !errors.Is(err, transcriptstore.ErrOwnerMismatch) {
		t.Fatalf("foreign owner stale-coordinate locate error=%v", err)
	}
	if _, _, err := readModel.GetTranscriptWebMessageByID(context.Background(), foreignIdentity); !errors.Is(err, transcriptstore.ErrOwnerMismatch) {
		t.Fatalf("foreign owner stale-coordinate exact error=%v", err)
	}
	identityDriftState := transcriptWebTestState(stream.UID, snapshot2, 2, 2, 4, "ready", now.Add(3*time.Second))
	identityDriftMessage := transcriptWebTestMessage(1, 0, "rewritten-user-1", "rewritten", 1, now.Add(3*time.Second))
	if err := readModel.ApplyTranscriptWebProjection(context.Background(), transcriptstore.ApplyTranscriptWebProjectionInput{
		OwnerID: stream.OwnerID, State: identityDriftState,
		ExpectedBranchGeneration: snapshot2.BranchGeneration, ExpectedThroughPublication: snapshot2.ThroughPublicationSequence,
		ExpectedProjectionRevision: 3, ExpectedSourceRevision: 0,
		ExpectedSourceChainSHA256: ready2.SourceChainSHA256,
		Messages:                  []transcriptstore.TranscriptWebMessageRecord{identityDriftMessage},
	}); !errors.Is(err, transcriptstore.ErrTranscriptWebProjectionStale) {
		t.Fatalf("stable message identity rewrite error=%v", err)
	}
	first, found, err = readModel.GetTranscriptWebMessageRange(context.Background(), transcriptstore.TranscriptWebMessagePageInput{
		OwnerID: stream.OwnerID, StreamUID: stream.UID, BranchID: snapshot2.BranchID,
		BranchGeneration: snapshot2.BranchGeneration, ThroughPublicationSequence: snapshot2.ThroughPublicationSequence,
		From: &fromZero, Limit: 1,
	})
	if err != nil || !found || len(first.Messages) != 1 || first.Messages[0].MessageID != "stable-user-1" {
		t.Fatalf("stable identity rollback page=%#v found=%t err=%v", first, found, err)
	}
	crossIdentityState := transcriptWebTestState(stream.UID, snapshot2, 3, 3, 4, "ready", now.Add(4*time.Second))
	crossIdentityMessage := transcriptWebTestMessageWithIdentities(
		3, 2, "user-1", "client-user-3", "cross-column collision",
		snapshot2.ThroughPublicationSequence, now.Add(4*time.Second),
	)
	if err := readModel.ApplyTranscriptWebProjection(context.Background(), transcriptstore.ApplyTranscriptWebProjectionInput{
		OwnerID: stream.OwnerID, State: crossIdentityState,
		ExpectedBranchGeneration: snapshot2.BranchGeneration, ExpectedThroughPublication: snapshot2.ThroughPublicationSequence,
		ExpectedProjectionRevision: 3, ExpectedSourceRevision: 0,
		ExpectedSourceChainSHA256: ready2.SourceChainSHA256,
		Messages:                  []transcriptstore.TranscriptWebMessageRecord{crossIdentityMessage},
	}); !errors.Is(err, transcriptstore.ErrTranscriptWebProjectionStale) {
		t.Fatalf("cross-column identity collision error=%v", err)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	readModel, err = store.TranscriptWebReadModel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reopened, messages, found, err := readModel.GetTranscriptWebProjectionCheckpoint(context.Background(), stream.OwnerID, stream.UID, snapshot2.BranchID)
	if err != nil || !found || reopened.ThroughPublicationSequence != snapshot2.ThroughPublicationSequence ||
		reopened.ProjectionRevision != 3 || len(messages) != 2 {
		t.Fatalf("reopened state=%#v messages=%d found=%t err=%v", reopened, len(messages), found, err)
	}
}

func TestTranscriptWebReadModelV38InvalidatesReadyPageWhenPublishedEventGainsArtifactReference(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-web-v38-artifact", OwnerID: "owner-a", ExternalID: "external-web-v38-artifact",
		SessionID: "session-web-v38-artifact", Kind: transcriptstore.StreamKindStandalone, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := repository.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "artifact-user",
		PayloadJSON: []byte(`{"text":"create artifact"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append user created=%t err=%v", created, err)
	}
	claim, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-a", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	assistant, created, err := repository.AppendRunnerEvent(context.Background(), transcriptstore.AppendEventInput{
		Claim: claim.Claim, ClientMessageID: "artifact-assistant", Type: "assistant_message",
		Source: transcriptstore.EventSourcePayload, PayloadJSON: []byte(`{"text":"artifact ready"}`),
		Destinations: []string{"ws"},
	})
	if err != nil || !created {
		t.Fatalf("append assistant created=%t err=%v", created, err)
	}
	snapshot, err := repository.GetProjectionSnapshot(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	state := transcriptWebTestState(stream.UID, snapshot, 2, 2, 1, "ready", now)
	messages := []transcriptstore.TranscriptWebMessageRecord{
		transcriptWebTestMessage(1, 0, "artifact-user", "create artifact", 1, now),
		transcriptWebTestMessage(2, 1, "artifact-assistant", "artifact ready", assistant.PublicationSeq, now),
	}
	readModel, err := store.TranscriptWebReadModel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := readModel.ApplyTranscriptWebProjection(context.Background(), transcriptstore.ApplyTranscriptWebProjectionInput{
		OwnerID: stream.OwnerID, State: state, ReplaceAll: true, Messages: messages,
	}); err != nil {
		t.Fatal(err)
	}
	pageInput := transcriptstore.TranscriptWebMessagePageInput{
		OwnerID: stream.OwnerID, StreamUID: stream.UID, BranchID: snapshot.BranchID,
		BranchGeneration: snapshot.BranchGeneration, ThroughPublicationSequence: snapshot.ThroughPublicationSequence,
		SourceRevision: 0, Limit: 50,
	}
	if _, found, err := readModel.GetTranscriptWebMessageRange(context.Background(), pageInput); err != nil || !found {
		t.Fatalf("initial ready page found=%t err=%v", found, err)
	}
	const siblingBranchID = "br_1234abcd"
	var userEventID int64
	if err := store.db.QueryRow(`SELECT event_id FROM transcript_events
		WHERE stream_uid=? AND client_message_id='artifact-user'`, stream.UID).Scan(&userEventID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO transcript_branches(
		stream_uid,branch_id,parent_branch_id,fork_event_id,fork_point,kind,client_mutation_id,
		request_sha256,source_message_id,created_at,updated_at
	) VALUES(?,?,?,?,?,'edit','artifact-sibling',?,'artifact-user',?,?)`, stream.UID, siblingBranchID,
		snapshot.BranchID, userEventID, 1, make([]byte, 32), now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO transcript_branch_events(stream_uid,branch_id,ordinal,event_id)
		VALUES(?,?,1,?)`, stream.UID, siblingBranchID, userEventID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO transcript_artifact_refs(
		stream_uid,runner_attempt,source_event_id,ordinal,artifact_id,version_id,relation,availability,created_at
	) VALUES(?,?,?,?,?,?,?,?,?)`, stream.UID, claim.Claim.Attempt, assistant.EventID, 0,
		"artifact-a", "version-a1", "cited", "available", now); err != nil {
		t.Fatal(err)
	}
	var through, referenceCount, artifactRevision, sourceRevision int64
	if err := store.db.QueryRow(`SELECT through_publication_seq FROM transcript_branch_heads
		WHERE stream_uid=? AND branch_id=?`, stream.UID, snapshot.BranchID).Scan(&through); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transcript_artifact_refs WHERE stream_uid=?`, stream.UID).
		Scan(&referenceCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT revision FROM transcript_artifact_reference_heads
		WHERE stream_uid=?`, stream.UID).Scan(&artifactRevision); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT source_revision FROM transcript_branch_heads
		WHERE stream_uid=? AND branch_id=?`, stream.UID, snapshot.BranchID).Scan(&sourceRevision); err != nil {
		t.Fatal(err)
	}
	if through != snapshot.ThroughPublicationSequence || referenceCount != 1 || artifactRevision != 1 || sourceRevision != 1 {
		t.Fatalf("through=%d refs=%d artifact-revision=%d source-revision=%d",
			through, referenceCount, artifactRevision, sourceRevision)
	}
	var siblingSourceRevision, siblingDirty int64
	if err := store.db.QueryRow(`SELECT source_revision FROM transcript_branch_heads
		WHERE stream_uid=? AND branch_id=?`, stream.UID, siblingBranchID).Scan(&siblingSourceRevision); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transcript_web_projection_dirty
		WHERE stream_uid=? AND branch_id=?`, stream.UID, siblingBranchID).Scan(&siblingDirty); err != nil {
		t.Fatal(err)
	}
	if siblingSourceRevision != 0 || siblingDirty != 0 {
		t.Fatalf("unrelated sibling source-revision=%d dirty=%d", siblingSourceRevision, siblingDirty)
	}
	if _, _, err := readModel.GetTranscriptWebMessageRange(context.Background(), pageInput); !errors.Is(err, transcriptstore.ErrTranscriptWebProjectionStale) {
		t.Fatalf("artifact-stale ready page error=%v", err)
	}
	projected := transcriptWebTestState(stream.UID, snapshot, 2, 2, 2, "ready", now.Add(time.Second))
	projected.SourceRevision = 1
	projected.MessageArtifactReferenceCount = 1
	projected.SourceChainSHA256 = transcriptstore.TranscriptWebSHA256([]byte("artifact-source-revision-1"))
	if err := readModel.ApplyTranscriptWebProjection(context.Background(), transcriptstore.ApplyTranscriptWebProjectionInput{
		OwnerID: stream.OwnerID, State: projected,
		ExpectedBranchGeneration: snapshot.BranchGeneration, ExpectedThroughPublication: snapshot.ThroughPublicationSequence,
		ExpectedProjectionRevision: 1, ExpectedSourceRevision: 0,
		ExpectedSourceChainSHA256: state.SourceChainSHA256,
		Messages:                  []transcriptstore.TranscriptWebMessageRecord{messages[1]},
		ArtifactReferences: []transcriptstore.TranscriptWebMessageArtifactReference{{
			MessageOrdinal: 2, Ordinal: 0, SourceEventID: assistant.EventID,
			RunnerAttempt: &claim.Claim.Attempt, SourceReferenceOrdinal: 0,
			ArtifactID: "artifact-a", VersionID: "version-a1", Relation: transcriptstore.ArtifactRelationCited,
		}},
	}); err != nil {
		t.Fatalf("project normalized artifact reference: %v", err)
	}
	pageInput.SourceRevision = 1
	page, found, err := readModel.GetTranscriptWebMessageRange(context.Background(), pageInput)
	if err != nil || !found || len(page.Messages) != 2 || len(page.Messages[1].ArtifactReferences) != 1 ||
		page.Messages[1].ArtifactReferences[0].ArtifactID != "artifact-a" ||
		page.Messages[1].ArtifactReferences[0].Availability != transcriptstore.ArtifactMissing {
		t.Fatalf("normalized artifact page=%#v found=%t err=%v", page, found, err)
	}
	if _, err := store.db.Exec(`UPDATE transcript_artifact_refs SET availability='deleted'
		WHERE stream_uid=? AND source_event_id=?`, stream.UID, assistant.EventID); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transcript_artifact_refs WHERE stream_uid=?`, stream.UID).
		Scan(&referenceCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT revision FROM transcript_artifact_reference_heads
		WHERE stream_uid=?`, stream.UID).Scan(&artifactRevision); err != nil {
		t.Fatal(err)
	}
	if referenceCount != 1 || artifactRevision != 1 {
		t.Fatalf("availability changed structural ref-head=(%d,%d)", referenceCount, artifactRevision)
	}
	if _, err := store.db.Exec(`UPDATE transcript_artifact_reference_heads SET revision=revision
		WHERE stream_uid=?`, stream.UID); err == nil {
		t.Fatal("direct transcript artifact reference head update was accepted")
	}
	page, found, err = readModel.GetTranscriptWebMessageRange(context.Background(), pageInput)
	if err != nil || !found || page.Messages[1].ArtifactReferences[0].Availability != transcriptstore.ArtifactDeleted {
		t.Fatalf("dynamic deleted artifact page=%#v found=%t err=%v", page, found, err)
	}
	if _, err := store.db.Exec(`DELETE FROM transcript_artifact_refs
		WHERE stream_uid=? AND source_event_id=?`, stream.UID, assistant.EventID); err != nil {
		t.Fatalf("retire transcript artifact reference: %v", err)
	}
	if err := store.db.QueryRow(`SELECT revision FROM transcript_artifact_reference_heads
		WHERE stream_uid=?`, stream.UID).Scan(&artifactRevision); err != nil || artifactRevision != 2 {
		t.Fatalf("retired artifact revision=%d err=%v", artifactRevision, err)
	}
	if err := store.db.QueryRow(`SELECT source_revision FROM transcript_branch_heads
		WHERE stream_uid=? AND branch_id=?`, stream.UID, snapshot.BranchID).Scan(&sourceRevision); err != nil || sourceRevision != 2 {
		t.Fatalf("retired artifact source revision=%d err=%v", sourceRevision, err)
	}
	if _, _, err := readModel.GetTranscriptWebMessageRange(context.Background(), pageInput); !errors.Is(err, transcriptstore.ErrTranscriptWebProjectionStale) {
		t.Fatalf("retired artifact stale page error=%v", err)
	}
	if _, err := store.db.Exec(`DELETE FROM transcript_streams WHERE stream_uid=?`, stream.UID); err != nil {
		t.Fatalf("stream cascade with artifact reference: %v", err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transcript_artifact_reference_heads WHERE stream_uid=?`, stream.UID).
		Scan(&referenceCount); err != nil || referenceCount != 0 {
		t.Fatalf("stream cascade artifact heads=%d err=%v", referenceCount, err)
	}
}

func TestTranscriptWebReadModelV38ContractRejectsDriftAndOlderTarget(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := validateVersionedSchemaJournal(context.Background(), store.db, 37); err == nil {
		t.Fatal("v37 target accepted a v38 migration journal")
	}
	if _, err := store.db.Exec(`DROP INDEX transcript_web_messages_page`); err != nil {
		t.Fatal(err)
	}
	if err := transcriptstore.NewRepository(store.db).ValidateCurrentContract(context.Background()); !errors.Is(err, transcriptstore.ErrSchemaUnavailable) {
		t.Fatalf("drifted read model contract error=%v", err)
	}
}

func TestTranscriptWebReadModelV38ArtifactVersionTombstonesPreserveExactDeletedIdentity(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	if _, err := store.db.Exec(`INSERT INTO projects(id,user_id,name,path,created_at,updated_at)
		VALUES('project-tombstone','owner-a','Project','',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO artifacts(
		id,project_id,name,kind,current_version_number,created_at,updated_at
	) VALUES('artifact-tombstone','project-tombstone','result.csv','text/csv',2,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	for number, versionID := range []string{"version-tombstone-1", "version-tombstone-2"} {
		if _, err := store.db.Exec(`INSERT INTO artifact_versions(
			id,artifact_id,version_number,content,content_sha256,created_at
		) VALUES(?,?,?,?,?,?)`, versionID, "artifact-tombstone", number+1, []byte(versionID), "sha-"+versionID, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec(`DELETE FROM artifact_versions WHERE id='version-tombstone-1'`); err != nil {
		t.Fatal(err)
	}
	assertArtifactVersionTombstone(t, store.db, "artifact-tombstone", "version-tombstone-1", "project-tombstone", "owner-a")
	if _, err := store.db.Exec(`INSERT INTO artifact_versions(
		id,artifact_id,version_number,content,content_sha256,created_at
	) VALUES('version-tombstone-1','artifact-tombstone',3,'reused','sha-reused',?)`, now); err == nil {
		t.Fatal("retired artifact version identity was reused")
	}
	if _, err := store.db.Exec(`DELETE FROM artifacts WHERE id='artifact-tombstone'`); err != nil {
		t.Fatal(err)
	}
	assertArtifactVersionTombstone(t, store.db, "artifact-tombstone", "version-tombstone-2", "project-tombstone", "owner-a")
	if _, err := store.db.Exec(`DELETE FROM artifact_version_tombstones
		WHERE artifact_id='artifact-tombstone' AND version_id='version-tombstone-1'`); err == nil {
		t.Fatal("live-project artifact tombstone deletion was accepted")
	}
	if _, err := store.db.Exec(`DELETE FROM projects WHERE id='project-tombstone'`); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM artifact_version_tombstones
		WHERE project_id='project-tombstone'`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("project tombstones remaining=%d err=%v", remaining, err)
	}
}

func TestTranscriptWebReadModelV38BackfillsAndMaintainsCanonicalBranchHeads(t *testing.T) {
	db, _ := openTargetTwentyTwoProviderDB(t)
	defer db.Close()
	for version := 23; version <= 37; version++ {
		applyTranscriptMigration(t, db, version)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	repository := transcriptstore.NewRepository(db)
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-v37-head", OwnerID: "owner-a", ExternalID: "external-v37-head",
		SessionID: "session-v37-head", Kind: transcriptstore.StreamKindStandalone, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"head-user-1", "head-user-2"} {
		if _, created, err := repository.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: id,
			PayloadJSON: []byte(`{"text":"head"}`), Destinations: []string{"ws"},
		}); err != nil || !created {
			t.Fatalf("append %s created=%t err=%v", id, created, err)
		}
	}
	var branchID string
	if err := db.QueryRow(`SELECT active_branch_id FROM transcript_branch_state WHERE stream_uid=?`, stream.UID).Scan(&branchID); err != nil {
		t.Fatal(err)
	}
	applyTranscriptMigration(t, db, 38)
	assertTranscriptBranchHead(t, db, stream.UID, branchID, 2, 2, 2)
	if _, created, err := repository.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "head-user-3",
		PayloadJSON: []byte(`{"text":"head-3"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append post-v38 created=%t err=%v", created, err)
	}
	assertTranscriptBranchHead(t, db, stream.UID, branchID, 3, 3, 3)
	for name, statement := range map[string]string{
		"membership update": `UPDATE transcript_branch_events SET ordinal=ordinal WHERE stream_uid='stream-v37-head'`,
		"membership delete": `DELETE FROM transcript_branch_events WHERE stream_uid='stream-v37-head' AND ordinal=1`,
		"head update":       `UPDATE transcript_branch_heads SET through_ordinal=through_ordinal+1 WHERE stream_uid='stream-v37-head'`,
		"head delete":       `DELETE FROM transcript_branch_heads WHERE stream_uid='stream-v37-head'`,
	} {
		if _, err := db.Exec(statement); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	if _, err := db.Exec(`DELETE FROM transcript_streams WHERE stream_uid=?`, stream.UID); err != nil {
		t.Fatalf("parent stream cascade: %v", err)
	}
	var heads int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_branch_heads WHERE stream_uid=?`, stream.UID).Scan(&heads); err != nil || heads != 0 {
		t.Fatalf("cascade heads=%d err=%v", heads, err)
	}
}

func TestTranscriptWebReadModelV38RejectsCorruptV37BranchMembership(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		corruptSQL string
		want       string
	}{
		{name: "ordinal gap", corruptSQL: `UPDATE transcript_branch_events SET ordinal=3 WHERE stream_uid='stream-v37-corrupt' AND ordinal=2`, want: "ordinal continuity"},
		{name: "orphan event", corruptSQL: `UPDATE transcript_branch_events SET event_id=999 WHERE stream_uid='stream-v37-corrupt' AND ordinal=1`, want: "event integrity"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			db, _ := openTargetTwentyTwoProviderDB(t)
			defer db.Close()
			for version := 23; version <= 37; version++ {
				applyTranscriptMigration(t, db, version)
			}
			repository := transcriptstore.NewRepository(db)
			stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
				UID: "stream-v37-corrupt", OwnerID: "owner-a", ExternalID: "external-v37-corrupt",
				SessionID: "session-v37-corrupt", Kind: transcriptstore.StreamKindStandalone, Epoch: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"corrupt-user-1", "corrupt-user-2"} {
				if _, created, err := repository.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
					StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: id,
					PayloadJSON: []byte(`{"text":"corrupt"}`), Destinations: []string{"ws"},
				}); err != nil || !created {
					t.Fatalf("append %s created=%t err=%v", id, created, err)
				}
			}
			if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(scenario.corruptSQL); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
				t.Fatal(err)
			}
			err = applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 38)
			if err == nil || !strings.Contains(err.Error(), scenario.want) {
				t.Fatalf("migration error=%v", err)
			}
			assertSchemaJournalVersion(t, db, 37)
			var objects int
			if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name IN (` +
				`'transcript_branch_heads','transcript_artifact_reference_heads',` +
				`'transcript_web_projection_dirty','transcript_web_projection_state',` +
				`'transcript_web_messages','transcript_web_message_artifact_refs','artifact_version_tombstones')`).
				Scan(&objects); err != nil || objects != 0 {
				t.Fatalf("v38 objects=%d err=%v", objects, err)
			}
		})
	}
}

func assertTranscriptBranchHead(
	t *testing.T, db *sql.DB, streamUID, branchID string, ordinal, eventID, publication int64,
) {
	t.Helper()
	var gotOrdinal, gotEventID, gotPublication int64
	if err := db.QueryRow(`SELECT through_ordinal,tail_event_id,through_publication_seq
		FROM transcript_branch_heads WHERE stream_uid=? AND branch_id=?`, streamUID, branchID).
		Scan(&gotOrdinal, &gotEventID, &gotPublication); err != nil {
		t.Fatal(err)
	}
	if gotOrdinal != ordinal || gotEventID != eventID || gotPublication != publication {
		t.Fatalf("head=(%d,%d,%d) want=(%d,%d,%d)", gotOrdinal, gotEventID, gotPublication, ordinal, eventID, publication)
	}
}

func assertArtifactVersionTombstone(
	t *testing.T, db *sql.DB, artifactID, versionID, projectID, ownerID string,
) {
	t.Helper()
	var gotProject, gotOwner, deletedAt string
	if err := db.QueryRow(`SELECT project_id,owner_id,deleted_at FROM artifact_version_tombstones
		WHERE artifact_id=? AND version_id=?`, artifactID, versionID).
		Scan(&gotProject, &gotOwner, &deletedAt); err != nil {
		t.Fatal(err)
	}
	if gotProject != projectID || gotOwner != ownerID || strings.TrimSpace(deletedAt) == "" {
		t.Fatalf("tombstone=(%q,%q,%q)", gotProject, gotOwner, deletedAt)
	}
}

func transcriptWebTestState(
	streamUID string, snapshot transcriptstore.ProjectionSnapshot, messageCount, visibleCount int, revision int64,
	status string, now time.Time,
) transcriptstore.TranscriptWebProjectionState {
	stateJSON := []byte(`{"version":1}`)
	return transcriptstore.TranscriptWebProjectionState{
		StreamUID: streamUID, BranchID: snapshot.BranchID, BranchGeneration: snapshot.BranchGeneration,
		ProjectorVersion: transcriptstore.TranscriptWebProjectorVersion, ProjectionRevision: revision,
		ThroughPublicationSequence: snapshot.ThroughPublicationSequence,
		MessageCount:               messageCount, VisibleMessageCount: visibleCount,
		ProjectorStateJSON: stateJSON, ProjectorStateSHA256: transcriptstore.TranscriptWebSHA256(stateJSON),
		SourceChainSHA256: transcriptstore.TranscriptWebSHA256([]byte(fmt.Sprintf("fixture-source:%d", snapshot.ThroughPublicationSequence))),
		Status:            status, UpdatedAt: now,
	}
}

func transcriptWebTestMessage(
	ordinal, visibleIndex int, id, text string, publication int64, now time.Time,
) transcriptstore.TranscriptWebMessageRecord {
	return transcriptWebTestMessageWithIdentities(ordinal, visibleIndex, id, id, text, publication, now)
}

func transcriptWebTestMessageWithIdentities(
	ordinal, visibleIndex int, messageID, clientMessageID, text string, publication int64, now time.Time,
) transcriptstore.TranscriptWebMessageRecord {
	messageJSON := []byte(`{"id":` + quoteJSONForTest(messageID) + `,"msg_id":` + quoteJSONForTest(clientMessageID) +
		`,"conversation_id":"session-web-v38","type":"text","position":"right","status":"finish","created_at":1,"content":{"content":` +
		quoteJSONForTest(text) + `}}`)
	return transcriptstore.TranscriptWebMessageRecord{
		Ordinal: ordinal, MessageID: messageID, ClientMessageID: clientMessageID, Visible: true, VisibleIndex: &visibleIndex,
		MessageJSON: messageJSON, MessageSHA256: transcriptstore.TranscriptWebSHA256(messageJSON),
		FirstPublicationSequence: publication, LastPublicationSequence: publication, UpdatedAt: now,
	}
}

func quoteJSONForTest(value string) string {
	quoted, _ := json.Marshal(value)
	return string(quoted)
}
