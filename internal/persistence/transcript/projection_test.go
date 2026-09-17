package transcript

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestRepositoryProjectedEventsUseFixedThroughWatermarkAcrossPages(t *testing.T) {
	repo, _, _ := newTranscriptRepository(t)
	seedTranscriptInput(t, repo, "stream-watermark", "owner-a")
	claimed, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: "stream-watermark", OwnerID: "owner-a", RunnerID: "runner-a", TTL: time.Minute,
		ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	if _, created, err := repo.AppendRunnerEvent(context.Background(), AppendEventInput{
		Claim: claimed.Claim, ClientMessageID: "delta-before", Type: "content_delta",
		Source: EventSourcePayload, PayloadJSON: []byte(`{"text":"before"}`),
	}); err != nil || !created {
		t.Fatalf("before created=%t err=%v", created, err)
	}
	snapshot, err := repo.GetProjectionSnapshot(context.Background(), "stream-watermark", "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	through := snapshot.ThroughPublicationSequence
	if _, created, err := repo.AppendRunnerEvent(context.Background(), AppendEventInput{
		Claim: claimed.Claim, ClientMessageID: "delta-after", Type: "content_delta",
		Source: EventSourcePayload, PayloadJSON: []byte(`{"text":"after"}`),
	}); err != nil || !created {
		t.Fatalf("after created=%t err=%v", created, err)
	}
	page, err := repo.ListProjectedEvents(context.Background(), ListProjectedEventsInput{
		StreamUID: "stream-watermark", OwnerID: "owner-a", BranchID: snapshot.BranchID,
		BranchGeneration: snapshot.BranchGeneration, ThroughPublicationSequence: through, Limit: 1,
	})
	if err != nil || len(page) != 1 {
		t.Fatalf("first page=%#v err=%v", page, err)
	}
	second, err := repo.ListProjectedEvents(context.Background(), ListProjectedEventsInput{
		StreamUID: "stream-watermark", OwnerID: "owner-a", BranchID: snapshot.BranchID,
		BranchGeneration: snapshot.BranchGeneration, AfterPublicationSequence: page[0].Event.PublicationSeq,
		ThroughPublicationSequence: through, Limit: 10,
	})
	if err != nil || len(second) != 1 || second[0].Event.ClientMessageID != "delta-before" {
		t.Fatalf("second page=%#v err=%v", second, err)
	}
	if second[0].Event.PublicationSeq != through {
		t.Fatalf("second sequence=%d through=%d", second[0].Event.PublicationSeq, through)
	}
}

func TestRepositoryArtifactProjectionMatchesHistoryDeliveryAndTerminalAfterRestart(t *testing.T) {
	repo, db, dsn := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-projection", "owner-a")
	seedArtifactVersion(t, db, "owner-a", "project-a", "root-a", "frame-a", "artifact-a", "version-a1")
	seedArtifactVersion(t, db, "owner-a", "project-a", "root-a", "frame-a", "artifact-b", "version-b1")
	input := AppendAssistantEventWithArtifactsInput{
		Claim: claim, ClientMessageID: "assistant-1", Source: EventSourcePayload,
		PayloadJSON: []byte(`{"text":"artifacts ready"}`), Destinations: []string{"ws"},
		References: []ArtifactReferenceInput{
			{ArtifactID: "artifact-a", VersionID: "version-a1", Relation: ArtifactRelationProduced},
			{ArtifactID: "artifact-b", VersionID: "version-b1", Relation: ArtifactRelationCited},
		},
	}
	assistant, refs, created, err := repo.AppendAssistantEventWithArtifacts(context.Background(), input)
	if err != nil || !created || len(refs) != 2 {
		t.Fatalf("assistant=%#v refs=%#v created=%t err=%v", assistant, refs, created, err)
	}
	again, againRefs, created, err := repo.AppendAssistantEventWithArtifacts(context.Background(), input)
	if err != nil || created || again.EventID != assistant.EventID || len(againRefs) != 2 {
		t.Fatalf("idempotent assistant=%#v refs=%#v created=%t err=%v", again, againRefs, created, err)
	}
	terminal, _, created, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
		Claim: claim, ClientMessageID: "finish-1", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`), Destinations: []string{"ws"},
	})
	if err != nil || !created {
		t.Fatalf("terminal=%#v created=%t err=%v", terminal, created, err)
	}
	afterTerminal, afterTerminalRefs, created, err := repo.AppendAssistantEventWithArtifacts(context.Background(), input)
	if err != nil || created || afterTerminal.EventID != assistant.EventID || len(afterTerminalRefs) != 2 {
		t.Fatalf("post-terminal idempotency event=%#v refs=%#v created=%t err=%v", afterTerminal, afterTerminalRefs, created, err)
	}
	projected, err := repo.ListProjectedEvents(context.Background(), ListProjectedEventsInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, Limit: 100,
	})
	if err != nil || len(projected) != 3 {
		t.Fatalf("projected=%#v err=%v", projected, err)
	}
	assertProjectedArtifactRefs(t, projected, assistant.EventID, []string{"version-a1", "version-b1"})
	assertProjectedArtifactRefs(t, projected, terminal.EventID, []string{"version-a1", "version-b1"})
	snapshot, err := repo.GetProjectionSnapshot(context.Background(), claim.StreamUID, claim.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	page, err := repo.ListProjectedEvents(context.Background(), ListProjectedEventsInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, BranchID: snapshot.BranchID,
		BranchGeneration: snapshot.BranchGeneration, AfterPublicationSequence: assistant.PublicationSeq,
		ThroughPublicationSequence: snapshot.ThroughPublicationSequence, Limit: 1,
	})
	if err != nil || len(page) != 1 || page[0].Event.EventID != terminal.EventID || len(page[0].ArtifactReferences) != 2 {
		t.Fatalf("terminal page=%#v err=%v", page, err)
	}
	for {
		delivery, err := repo.ClaimNextDelivery(context.Background(), ClaimDeliveryInput{
			OwnerID: claim.OwnerID, Destination: "ws", WorkerID: "worker-a", TTL: time.Minute,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !delivery.Claimed {
			break
		}
		if delivery.Claim.Event.EventID == assistant.EventID || delivery.Claim.Event.EventID == terminal.EventID {
			if string(delivery.Claim.ResolvedPayloadJSON) != string(delivery.Claim.Event.PayloadJSON) {
				t.Fatalf("resolved payload=%s event payload=%s", delivery.Claim.ResolvedPayloadJSON, delivery.Claim.Event.PayloadJSON)
			}
			want := projectedRefsForEvent(t, projected, delivery.Claim.Event.EventID)
			gotJSON, _ := json.Marshal(delivery.Claim.ArtifactReferences)
			wantJSON, _ := json.Marshal(want)
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("delivery refs=%s want=%s", gotJSON, wantJSON)
			}
		} else if len(delivery.Claim.ArtifactReferences) != 0 {
			t.Fatalf("user delivery refs=%#v", delivery.Claim.ArtifactReferences)
		}
		if _, err := repo.AcknowledgeDelivery(context.Background(), AcknowledgeDeliveryInput{Claim: delivery.Claim}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`DELETE FROM artifact_versions WHERE id='version-a1'`); err != nil {
		t.Fatal(err)
	}
	if affected, err := repo.MarkArtifactVersionUnavailable(context.Background(), "owner-a", "artifact-b", "version-b1", ArtifactDeleted); err != nil || affected != 1 {
		t.Fatalf("mark deleted affected=%d err=%v", affected, err)
	}
	projected, err = repo.ListProjectedEvents(context.Background(), ListProjectedEventsInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, eventID := range []int64{assistant.EventID, terminal.EventID} {
		values := projectedRefsForEvent(t, projected, eventID)
		if values[0].Availability != ArtifactMissing || values[1].Availability != ArtifactDeleted {
			t.Fatalf("event %d refs=%#v", eventID, values)
		}
	}
	reopenedDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedDB.Close()
	reopened, err := NewRepository(reopenedDB).ListProjectedEvents(context.Background(), ListProjectedEventsInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, _ := json.Marshal(reopened)
	wantJSON, _ := json.Marshal(projected)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("restart projection=%s want=%s", gotJSON, wantJSON)
	}
	if _, err := repo.ListProjectedEvents(context.Background(), ListProjectedEventsInput{
		StreamUID: claim.StreamUID, OwnerID: "owner-b", Limit: 100,
	}); !errors.Is(err, ErrOwnerMismatch) {
		t.Fatalf("foreign projection error=%v", err)
	}
}

func TestRepositoryFailedAndCancelledTerminalProjectionsRetainCommittedArtifacts(t *testing.T) {
	for _, status := range []string{"failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			repo, db, _ := newTranscriptRepository(t)
			claim := seedArtifactProjectionClaim(t, repo, db, "stream-projection-"+status, "owner-a")
			seedArtifactVersion(t, db, "owner-a", "project-a", "root-a", "frame-a", "artifact-a", "version-a1")
			assistant, _, _, err := repo.AppendAssistantEventWithArtifacts(context.Background(), AppendAssistantEventWithArtifactsInput{
				Claim: claim, ClientMessageID: "assistant-1", Source: EventSourcePayload,
				PayloadJSON: []byte(`{"text":"partial result"}`), Destinations: []string{"ws"},
				References: []ArtifactReferenceInput{{ArtifactID: "artifact-a", VersionID: "version-a1", Relation: ArtifactRelationProduced}},
			})
			if err != nil {
				t.Fatal(err)
			}
			var terminal Event
			if status == "cancelled" {
				result, err := repo.CancelRunner(context.Background(), CancelRunnerInput{
					StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, ExpectedAttempt: claim.Attempt,
					ClientMessageID: "cancel-1", ReasonCode: "user_cancelled", Destinations: []string{"ws"},
				})
				if err != nil || !result.Applied {
					t.Fatalf("cancel=%#v err=%v", result, err)
				}
				terminal = result.Event
			} else {
				terminal, _, _, err = repo.FinishRunner(context.Background(), FinishRunnerInput{
					Claim: claim, ClientMessageID: "finish-1", Status: status,
					PayloadJSON: []byte(`{"status":"failed"}`), Destinations: []string{"ws"},
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			projected, err := repo.ListProjectedEvents(context.Background(), ListProjectedEventsInput{
				StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, Limit: 100,
			})
			if err != nil {
				t.Fatal(err)
			}
			assertProjectedArtifactRefs(t, projected, assistant.EventID, []string{"version-a1"})
			assertProjectedArtifactRefs(t, projected, terminal.EventID, []string{"version-a1"})
		})
	}
}

func TestRepositoryAssistantArtifactAssociationRollsBackAndRejectsTerminalAttempt(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-projection-rollback", "owner-a")
	input := AppendAssistantEventWithArtifactsInput{
		Claim: claim, ClientMessageID: "assistant-invalid", Source: EventSourcePayload,
		PayloadJSON: []byte(`{"text":"invalid"}`), Destinations: []string{"ws"},
		References: []ArtifactReferenceInput{{ArtifactID: "missing", VersionID: "missing-v1", Relation: ArtifactRelationProduced}},
	}
	if _, _, created, err := repo.AppendAssistantEventWithArtifacts(context.Background(), input); !errors.Is(err, ErrArtifactMissing) || created {
		t.Fatalf("invalid association created=%t err=%v", created, err)
	}
	var eventCount, deliveryCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=?`, claim.StreamUID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_delivery_intents WHERE stream_uid=?`, claim.StreamUID).Scan(&deliveryCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 || deliveryCount != 1 {
		t.Fatalf("rolled back events=%d deliveries=%d", eventCount, deliveryCount)
	}
	if _, _, _, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
		Claim: claim, ClientMessageID: "finish", Status: "failed",
		PayloadJSON: []byte(`{"status":"failed"}`), Destinations: []string{"ws"},
	}); err != nil {
		t.Fatal(err)
	}
	seedArtifactVersion(t, db, "owner-a", "project-a", "root-a", "frame-a", "artifact-a", "version-a1")
	input.References = []ArtifactReferenceInput{{ArtifactID: "artifact-a", VersionID: "version-a1", Relation: ArtifactRelationProduced}}
	if _, _, _, err := repo.AppendAssistantEventWithArtifacts(context.Background(), input); !errors.Is(err, ErrClaimStale) {
		t.Fatalf("late association error=%v", err)
	}
}

func TestRepositoryFrameBackedArtifactProjectionReferencesCanonicalUUIDWithoutPayloadCopy(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-frame-projection", "owner-a")
	seedArtifactVersion(t, db, "owner-a", "project-a", "root-a", "frame-a", "artifact-a", "version-a1")
	frameEventID := "9d6316c2-0c1d-47bd-8b22-ddef63247817"
	if _, err := db.Exec(`
		INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at)
		VALUES(?, 'frame-a', 2, 'assistant_message', '{"text":"canonical"}', ?)`, frameEventID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	assistant, refs, created, err := repo.AppendAssistantEventWithArtifacts(context.Background(), AppendAssistantEventWithArtifactsInput{
		Claim: claim, ClientMessageID: "assistant-frame-ref", Source: EventSourceFrameRef, FrameEventID: &frameEventID,
		Destinations: []string{"ws"},
		References:   []ArtifactReferenceInput{{ArtifactID: "artifact-a", VersionID: "version-a1", Relation: ArtifactRelationProduced}},
	})
	if err != nil || !created || assistant.FrameEventID == nil || *assistant.FrameEventID != frameEventID ||
		len(assistant.PayloadJSON) != 0 || len(refs) != 1 {
		t.Fatalf("assistant=%#v refs=%#v created=%t err=%v", assistant, refs, created, err)
	}
	projected, err := repo.ListProjectedEvents(context.Background(), ListProjectedEventsInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertProjectedArtifactRefs(t, projected, assistant.EventID, []string{"version-a1"})
	for _, value := range projected {
		if value.Event.EventID == assistant.EventID {
			if len(value.Event.PayloadJSON) != 0 || string(value.ResolvedPayloadJSON) != `{"text":"canonical"}` {
				t.Fatalf("projected frame event=%#v", value)
			}
		}
	}
	for {
		delivery, err := repo.ClaimNextDelivery(context.Background(), ClaimDeliveryInput{
			OwnerID: claim.OwnerID, Destination: "ws", WorkerID: "worker-a", TTL: time.Minute,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !delivery.Claimed {
			break
		}
		if delivery.Claim.Event.EventID == assistant.EventID && string(delivery.Claim.ResolvedPayloadJSON) != `{"text":"canonical"}` {
			t.Fatalf("delivery frame payload=%s", delivery.Claim.ResolvedPayloadJSON)
		}
		if _, err := repo.AcknowledgeDelivery(context.Background(), AcknowledgeDeliveryInput{Claim: delivery.Claim}); err != nil {
			t.Fatal(err)
		}
	}
	var payload []byte
	var storedFrameEventID string
	if err := db.QueryRow(`SELECT payload_json,frame_event_id FROM transcript_events WHERE stream_uid=? AND event_id=?`,
		claim.StreamUID, assistant.EventID).Scan(&payload, &storedFrameEventID); err != nil {
		t.Fatal(err)
	}
	if payload != nil || storedFrameEventID != frameEventID {
		t.Fatalf("payload=%q frameEventID=%q", payload, storedFrameEventID)
	}
}

func seedArtifactProjectionClaim(t *testing.T, repo *Repository, db *sql.DB, streamUID, ownerID string) RunnerClaim {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES('project-a',?)`, ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO frames(id,project_id,root_frame_id) VALUES('frame-a','project-a','root-a')`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: streamUID, OwnerID: ownerID, ExternalID: "frame-a", SessionID: "session-a", Kind: StreamKindFrameRef,
		ProjectID: "project-a", RootFrameID: "root-a", FrameID: "frame-a", Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, created, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: streamUID, OwnerID: ownerID, ClientMessageID: "user-1",
		PayloadJSON: []byte(`{"text":"create artifacts"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("user event created=%t err=%v", created, err)
	}
	result, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: streamUID, OwnerID: ownerID, RunnerID: "runner-a", TTL: time.Minute,
		ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !result.Claimed {
		t.Fatalf("claim=%#v err=%v", result, err)
	}
	return result.Claim
}

func assertProjectedArtifactRefs(t *testing.T, events []ProjectedEvent, eventID int64, versions []string) {
	t.Helper()
	refs := projectedRefsForEvent(t, events, eventID)
	if len(refs) != len(versions) {
		t.Fatalf("event %d refs=%#v", eventID, refs)
	}
	for index, version := range versions {
		if refs[index].Ordinal != index || refs[index].VersionID != version {
			t.Fatalf("event %d ref[%d]=%#v", eventID, index, refs[index])
		}
	}
}

func projectedRefsForEvent(t *testing.T, events []ProjectedEvent, eventID int64) []ArtifactReference {
	t.Helper()
	for _, event := range events {
		if event.Event.EventID == eventID {
			return event.ArtifactReferences
		}
	}
	t.Fatalf("event %d not projected", eventID)
	return nil
}
