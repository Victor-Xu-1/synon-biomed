package transcript

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestCloneFrameHistoryRetriesEmptyCanonicalSource(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES('project-clone-empty','owner-a');
		INSERT INTO frames(id,project_id,root_frame_id,status) VALUES
			('frame-clone-empty-source','project-clone-empty','frame-clone-empty-source','completed'),
			('frame-clone-empty-target','project-clone-empty','frame-clone-empty-target','completed')`); err != nil {
		t.Fatal(err)
	}
	source, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:frame-clone-empty-source", OwnerID: "owner-a", ExternalID: "frame-clone-empty-source",
		SessionID: "frame-clone-empty-source", Kind: StreamKindFrameRef, ProjectID: "project-clone-empty",
		RootFrameID: "frame-clone-empty-source", FrameID: "frame-clone-empty-source", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:frame-clone-empty-target", OwnerID: "owner-a", ExternalID: "frame-clone-empty-target",
		SessionID: "frame-clone-empty-target", Kind: StreamKindFrameRef, ProjectID: "project-clone-empty",
		RootFrameID: "frame-clone-empty-target", FrameID: "frame-clone-empty-target", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	input := CloneFrameHistoryInput{
		SourceStreamUID: source.UID, TargetStreamUID: target.UID, OwnerID: "owner-a",
	}
	first, err := repo.CloneFrameHistory(context.Background(), input)
	if err != nil || first.EventCount != 0 || first.TargetStream.UID != target.UID {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	again, err := repo.CloneFrameHistory(context.Background(), input)
	if err != nil || again != first {
		t.Fatalf("retry=%#v err=%v first=%#v", again, err, first)
	}
	start := make(chan struct{})
	results := make(chan CloneFrameHistoryResult, 32)
	errors := make(chan error, 32)
	var group sync.WaitGroup
	for range 32 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			result, err := NewRepository(db).CloneFrameHistory(context.Background(), input)
			results <- result
			errors <- err
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent retry error=%v", err)
		}
	}
	for result := range results {
		if result != first {
			t.Fatalf("concurrent retry=%#v first=%#v", result, first)
		}
	}
}

func TestCloneFrameHistoryRejectsFrameReferenceFallback(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES('project-clone-ref','owner-a');
		INSERT INTO frames(id,project_id,root_frame_id,status) VALUES
			('frame-clone-ref-source','project-clone-ref','frame-clone-ref-source','completed'),
			('frame-clone-ref-target','project-clone-ref','frame-clone-ref-target','completed');
		INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at) VALUES
			('frame-clone-ref-message','frame-clone-ref-source',1,'user_message',
			'{"role":"user","text":"must not be read as canonical payload"}',?)`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	source, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:frame-clone-ref-source", OwnerID: "owner-a", ExternalID: "frame-clone-ref-source",
		SessionID: "frame-clone-ref-source", Kind: StreamKindFrameRef, ProjectID: "project-clone-ref",
		RootFrameID: "frame-clone-ref-source", FrameID: "frame-clone-ref-source", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:frame-clone-ref-target", OwnerID: "owner-a", ExternalID: "frame-clone-ref-target",
		SessionID: "frame-clone-ref-target", Kind: StreamKindFrameRef, ProjectID: "project-clone-ref",
		RootFrameID: "frame-clone-ref-target", FrameID: "frame-clone-ref-target", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE transcript_events SET source='frame_ref',payload_json=NULL,frame_event_id=?
		WHERE stream_uid=? AND event_id=1`, "frame-clone-ref-message", source.UID); err != nil {
		t.Fatal(err)
	}
	_, err = repo.CloneFrameHistory(context.Background(), CloneFrameHistoryInput{
		SourceStreamUID: source.UID, TargetStreamUID: target.UID, OwnerID: "owner-a",
	})
	if !errors.Is(err, ErrEventConflict) {
		t.Fatalf("clone error=%v, want ErrEventConflict", err)
	}
}

func TestCloneFrameHistoryRejectsUnsettledSourceWithoutMutatingTarget(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES('project-clone-unsettled','owner-a');
		INSERT INTO frames(id,project_id,root_frame_id,status) VALUES
			('frame-clone-unsettled-source','project-clone-unsettled','frame-clone-unsettled-source','pending'),
			('frame-clone-unsettled-target','project-clone-unsettled','frame-clone-unsettled-target','completed')`); err != nil {
		t.Fatal(err)
	}
	source, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:frame-clone-unsettled-source", OwnerID: "owner-a", ExternalID: "frame-clone-unsettled-source",
		SessionID: "frame-clone-unsettled-source", Kind: StreamKindFrameRef, ProjectID: "project-clone-unsettled",
		RootFrameID: "frame-clone-unsettled-source", FrameID: "frame-clone-unsettled-source", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:frame-clone-unsettled-target", OwnerID: "owner-a", ExternalID: "frame-clone-unsettled-target",
		SessionID: "frame-clone-unsettled-target", Kind: StreamKindFrameRef, ProjectID: "project-clone-unsettled",
		RootFrameID: "frame-clone-unsettled-target", FrameID: "frame-clone-unsettled-target", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: source.UID, OwnerID: source.OwnerID, ClientMessageID: "clone-unsettled-user",
		PayloadJSON: []byte(`{"role":"user","text":"still waiting for a runner"}`),
	}); err != nil || !created {
		t.Fatalf("append created=%v err=%v", created, err)
	}
	_, err = repo.CloneFrameHistory(context.Background(), CloneFrameHistoryInput{
		SourceStreamUID: source.UID, TargetStreamUID: target.UID, OwnerID: source.OwnerID,
	})
	if !errors.Is(err, ErrEventConflict) {
		t.Fatalf("clone error=%v, want ErrEventConflict", err)
	}
	var targetEvents, targetAuthorities int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=?`, target.UID).Scan(&targetEvents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_frame_authority authority
		JOIN transcript_payload_genesis_receipts receipt ON receipt.genesis_id=authority.genesis_id
		WHERE authority.active_stream_uid=? AND receipt.source_kind='empty'`, target.UID).Scan(&targetAuthorities); err != nil {
		t.Fatal(err)
	}
	if targetEvents != 0 || targetAuthorities != 1 {
		t.Fatalf("target events=%d empty authorities=%d", targetEvents, targetAuthorities)
	}
}

func TestCloneFrameHistoryCopiesGenesisPayloadWithoutFrameFallbackAndRetries(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES('project-clone','owner-a');
		INSERT INTO frames(id,project_id,root_frame_id,status) VALUES
			('frame-clone-source','project-clone','frame-clone-source','completed'),
			('frame-clone-target','project-clone','frame-clone-target','completed');
		INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at) VALUES
			('frame-clone-user','frame-clone-source',1,'user_message',
			'{"role":"user","text":"immutable source history"}',?)`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	source, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:frame-clone-source", OwnerID: "owner-a", ExternalID: "frame-clone-source",
		SessionID: "frame-clone-source", Kind: StreamKindFrameRef, ProjectID: "project-clone",
		RootFrameID: "frame-clone-source", FrameID: "frame-clone-source", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:frame-clone-target", OwnerID: "owner-a", ExternalID: "frame-clone-target",
		SessionID: "frame-clone-target", Kind: StreamKindFrameRef, ProjectID: "project-clone",
		RootFrameID: "frame-clone-target", FrameID: "frame-clone-target", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	input := CloneFrameHistoryInput{
		SourceStreamUID: source.UID, TargetStreamUID: target.UID, OwnerID: "owner-a",
	}
	result, err := repo.CloneFrameHistory(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.TargetStream.UID != target.UID || result.EventCount != 1 || result.BranchCount != 1 ||
		result.BranchEventCount != 1 || len(result.GenesisSHA256) != sha256.Size*2 ||
		len(result.SourceSHA256) != sha256.Size*2 {
		t.Fatalf("clone result=%#v", result)
	}
	events, err := repo.ListProjectedEvents(context.Background(), ListProjectedEventsInput{
		StreamUID: target.UID, OwnerID: "owner-a", Limit: 10,
	})
	if err != nil || len(events) != 1 || events[0].Event.Source != EventSourcePayload ||
		events[0].Event.FrameEventID != nil || events[0].Event.Type != "history_user_message" ||
		!bytes.Equal(events[0].ResolvedPayloadJSON, []byte(`{"role":"user","text":"immutable source history"}`)) {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	var targetFrameMessages, targetIntents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM frame_events WHERE frame_id=? AND event_type IN
		('message','user_message','assistant_message','system_message','tool_use','tool_result','ask_user_answer')`,
		target.FrameID).Scan(&targetFrameMessages); err != nil || targetFrameMessages != 0 {
		t.Fatalf("target frame messages=%d err=%v", targetFrameMessages, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_delivery_intents WHERE stream_uid=?`, target.UID).
		Scan(&targetIntents); err != nil || targetIntents != 0 {
		t.Fatalf("target delivery intents=%d err=%v", targetIntents, err)
	}
	var sourceKind, sourceUID string
	var sourceEpoch, sourceEventCount int64
	if err := db.QueryRow(`SELECT source_kind,source_stream_uid,source_epoch,source_event_count
		FROM transcript_payload_genesis_receipts WHERE stream_uid=?`, target.UID).
		Scan(&sourceKind, &sourceUID, &sourceEpoch, &sourceEventCount); err != nil ||
		sourceKind != "canonical_clone" || sourceUID != source.UID || sourceEpoch != source.Epoch || sourceEventCount != 1 {
		t.Fatalf("source_kind=%q source_uid=%q epoch=%d count=%d err=%v",
			sourceKind, sourceUID, sourceEpoch, sourceEventCount, err)
	}
	again, err := repo.CloneFrameHistory(context.Background(), input)
	if err != nil || again.GenesisSHA256 != result.GenesisSHA256 || again.SourceSHA256 != result.SourceSHA256 ||
		again.EventCount != result.EventCount || again.TargetStream.UID != result.TargetStream.UID {
		t.Fatalf("retry=%#v err=%v first=%#v", again, err, result)
	}
	if _, err := db.Exec(`UPDATE transcript_branches SET source_message_id='tampered'
		WHERE stream_uid=? AND branch_id=?`, target.UID, result.ActiveBranchID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CloneFrameHistory(context.Background(), input); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("tampered target branch retry error=%v, want ErrEventConflict", err)
	}
}

func TestCloneFrameHistoryRebindsTypedAskUserAndDropsOnlyLegacyEvidence(t *testing.T) {
	repo, db, source, _, claim := newFrameBranchForkFixture(t)
	seedAskUserBranchFacts(t, repo, db, source, "ask_user")
	if _, err := db.Exec(`UPDATE frame_events SET payload=
		'{"role":"assistant","content":[{"type":"tool_use","id":"ask-1","name":"ask_user","input":{"questions":[
		{"question":"Which structure?","header":"Structure","options":[{"label":"5FQD","description":"Use the experimental structure."},{"label":"Predicted","description":"Use the predicted structure."}],"multiSelect":false}
		]}}]}' WHERE id='ask-tool-event'`); err != nil {
		t.Fatal(err)
	}
	var pending AppendFrameAskUserPendingResult
	if err := repo.RunImmediate(context.Background(), func(tx *ImmediateTransaction) error {
		if _, err := tx.AppendClaimedFrameAskUserReferences(context.Background(), AppendClaimedFrameAskUserReferencesInput{
			Claim: claim, FrameID: source.FrameID, ToolUseID: "ask-1",
			ToolUseFrameEventID: "ask-tool-event", ToolResultFrameEventID: "ask-result-event",
		}); err != nil {
			return err
		}
		var err error
		pending, err = tx.AppendFrameAskUserPending(context.Background(), AppendFrameAskUserPendingInput{
			Claim: claim, ClientMessageID: "ask-user:ask-1", FrameID: source.FrameID, ToolUseID: "ask-1",
			ToolUseFrameEventID: "ask-tool-event", PendingFrameEventID: "ask-result-event",
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	prompt, err := repo.GetFrameAskUserPrompt(context.Background(), GetFrameAskUserPromptInput{
		OwnerID: source.OwnerID, FrameID: source.FrameID, Origin: pending.Origin,
	})
	if err != nil {
		t.Fatal(err)
	}
	answer, err := NewAskUserResultV1(AskUserActionAnswer, map[string]string{"Which structure?": "5FQD"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.RunImmediate(context.Background(), func(tx *ImmediateTransaction) error {
		_, _, err := tx.AppendFrameAskUserResult(context.Background(), AppendFrameAskUserResultInput{
			OwnerID: source.OwnerID, FrameID: source.FrameID, Origin: prompt.Origin, Result: answer,
			ModelContinuation: `{"status":"answered","answers":{"Which structure?":"5FQD"}}`,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
		Claim: claim, ClientMessageID: "clone-ask-finish", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE frames SET status='completed' WHERE id=?;
		INSERT INTO frames(id,project_id,root_frame_id,status) VALUES
		('frame-clone-ask-target','project-branch','frame-clone-ask-target','completed')`, source.FrameID); err != nil {
		t.Fatal(err)
	}
	target, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:frame-clone-ask-target", OwnerID: source.OwnerID, ExternalID: "frame-clone-ask-target",
		SessionID: "frame-clone-ask-target", Kind: StreamKindFrameRef, ProjectID: source.ProjectID,
		RootFrameID: "frame-clone-ask-target", FrameID: "frame-clone-ask-target", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := repo.CloneFrameHistory(context.Background(), CloneFrameHistoryInput{
		SourceStreamUID: source.UID, TargetStreamUID: target.UID, OwnerID: source.OwnerID,
	})
	if err != nil {
		t.Fatal(err)
	}
	events, err := repo.ListProjectedEvents(context.Background(), ListProjectedEventsInput{
		StreamUID: target.UID, OwnerID: target.OwnerID, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	promptCount, pendingCount, answeredCount := 0, 0, 0
	for _, projected := range events {
		if projected.Event.Source != EventSourcePayload || projected.Event.FrameEventID != nil {
			t.Fatalf("clone retained non-payload event=%#v", projected.Event)
		}
		switch projected.Event.Type {
		case AskUserPromptEventType:
			promptCount++
			decoded, err := DecodeAskUserPromptV1(projected.ResolvedPayloadJSON)
			if err != nil || decoded.Origin.StreamUID != target.UID || decoded.Origin.FrameID != target.FrameID ||
				decoded.Origin.ToolUseFrameEventID != "ask-tool-event" || decoded.Origin.PendingFrameEventID != "ask-result-event" {
				t.Fatalf("prompt=%#v err=%v", decoded, err)
			}
		case AskUserResultEventType:
			decoded, err := DecodeAskUserResultEventV1(projected.ResolvedPayloadJSON)
			if err != nil || decoded.Origin.StreamUID != target.UID || decoded.Origin.FrameID != target.FrameID {
				t.Fatalf("result=%#v err=%v", decoded, err)
			}
			switch decoded.Result.Status {
			case AskUserStatusAwaitingResponse:
				pendingCount++
			case AskUserStatusAnswered:
				answeredCount++
			}
		}
	}
	if promptCount != 1 || pendingCount != 1 || answeredCount != 1 ||
		result.EventCount != len(events) || result.AttemptCount != 1 || result.BranchEventCount != len(events) {
		encoded, _ := json.Marshal(events)
		t.Fatalf("prompt=%d pending=%d answered=%d result=%#v events=%s",
			promptCount, pendingCount, answeredCount, result, encoded)
	}
	var targetFrameMessages int
	if err := db.QueryRow(`SELECT COUNT(*) FROM frame_events WHERE frame_id=?`, target.FrameID).
		Scan(&targetFrameMessages); err != nil || targetFrameMessages != 0 {
		t.Fatalf("target frame messages=%d err=%v", targetFrameMessages, err)
	}
	updated, err := db.Exec(`UPDATE transcript_events SET event_type='user_message'
		WHERE stream_uid=? AND frame_event_id='ask-tool-event'`, source.UID)
	if err != nil {
		t.Fatal(err)
	}
	if affected, err := updated.RowsAffected(); err != nil || affected != 1 {
		t.Fatalf("wrong-type fixture affected=%d err=%v", affected, err)
	}
	if _, err := db.Exec(`INSERT INTO frames(id,project_id,root_frame_id,status)
		VALUES('frame-clone-ask-invalid-target','project-branch','frame-clone-ask-invalid-target','completed')`); err != nil {
		t.Fatal(err)
	}
	invalidTarget, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:frame-clone-ask-invalid-target", OwnerID: source.OwnerID,
		ExternalID: "frame-clone-ask-invalid-target", SessionID: "frame-clone-ask-invalid-target",
		Kind: StreamKindFrameRef, ProjectID: source.ProjectID,
		RootFrameID: "frame-clone-ask-invalid-target", FrameID: "frame-clone-ask-invalid-target", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CloneFrameHistory(context.Background(), CloneFrameHistoryInput{
		SourceStreamUID: source.UID, TargetStreamUID: invalidTarget.UID, OwnerID: source.OwnerID,
	}); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("wrong-type AskUser frame reference error=%v, want ErrEventConflict", err)
	}
}

func TestCloneFrameHistoryPreservesExactArtifactVersionsAcrossRoots(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-clone-artifact-source", "owner-a")
	seedArtifactVersion(t, db, "owner-a", "project-a", "root-a", "frame-a", "artifact-clone", "version-clone")
	_, checkpointEvent, _, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "clone-artifact-tool", Phase: RunnerPhaseExecuting, Resumable: true,
		PayloadJSON: []byte(`{"tool":"artifact_register"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	commitCreatedAt := time.Now().UTC()
	if _, err := db.Exec(`INSERT INTO transcript_artifact_commits(
		stream_uid,runner_attempt,source_event_id,ordinal,artifact_id,version_id,relation,created_at)
		VALUES(?,?,?,?,?,?,?,?)`, claim.StreamUID, claim.Attempt, checkpointEvent.EventID, 0,
		"artifact-clone", "version-clone", "produced", commitCreatedAt); err != nil {
		t.Fatal(err)
	}
	assistant, refs, created, err := repo.AppendAssistantEventWithCommittedArtifacts(context.Background(), AppendEventInput{
		Claim: claim, ClientMessageID: "clone-artifact-assistant", Type: "assistant_message",
		Source: EventSourcePayload, PayloadJSON: []byte(`{"text":"artifact ready"}`),
	})
	if err != nil || !created || len(refs) != 1 || refs[0].SourceEventID != assistant.EventID {
		t.Fatalf("assistant=%#v refs=%#v created=%t err=%v", assistant, refs, created, err)
	}
	terminal, _, created, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
		Claim: claim, ClientMessageID: "clone-artifact-finish", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`),
	})
	if err != nil || !created {
		t.Fatalf("terminal=%#v created=%t err=%v", terminal, created, err)
	}
	if _, err := db.Exec(`UPDATE frames SET status='completed' WHERE id='frame-a';
		INSERT INTO frames(id,project_id,root_frame_id,status) VALUES
		('frame-clone-artifact-target','project-a','frame-clone-artifact-target','completed')`); err != nil {
		t.Fatal(err)
	}
	target, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:frame-clone-artifact-target", OwnerID: claim.OwnerID, ExternalID: "frame-clone-artifact-target",
		SessionID: "frame-clone-artifact-target", Kind: StreamKindFrameRef, ProjectID: "project-a",
		RootFrameID: "frame-clone-artifact-target", FrameID: "frame-clone-artifact-target", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	input := CloneFrameHistoryInput{
		SourceStreamUID: claim.StreamUID, TargetStreamUID: target.UID, OwnerID: claim.OwnerID,
	}
	result, err := repo.CloneFrameHistory(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.ArtifactCommitCount != 1 || result.ArtifactRefCount != 1 || result.AttemptCount != 1 ||
		result.CheckpointCount != 1 {
		t.Fatalf("result=%#v", result)
	}
	clonedRefs, err := repo.ListArtifactReferences(context.Background(), target.UID, target.OwnerID, claim.Attempt)
	if err != nil || len(clonedRefs) != 1 {
		t.Fatalf("refs=%#v err=%v", clonedRefs, err)
	}
	for _, ref := range clonedRefs {
		if ref.ArtifactID != "artifact-clone" || ref.VersionID != "version-clone" ||
			ref.Relation != ArtifactRelationProduced || ref.Availability != ArtifactAvailable {
			t.Fatalf("ref=%#v", ref)
		}
	}
	projected, err := repo.ListProjectedEvents(context.Background(), ListProjectedEventsInput{
		StreamUID: target.UID, OwnerID: target.OwnerID, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	assistantRefs, terminalRefs := 0, 0
	for _, event := range projected {
		switch event.Event.ClientMessageID {
		case "clone-artifact-assistant":
			assistantRefs = len(event.ArtifactReferences)
		case "clone-artifact-finish":
			terminalRefs = len(event.ArtifactReferences)
		}
	}
	if assistantRefs != 1 || terminalRefs != 1 {
		t.Fatalf("assistant refs=%d terminal refs=%d projected=%#v", assistantRefs, terminalRefs, projected)
	}
	var producingFrame string
	if err := db.QueryRow(`SELECT frame_id FROM artifact_version_provenance WHERE version_id='version-clone'`).
		Scan(&producingFrame); err != nil || producingFrame != "frame-a" {
		t.Fatalf("producing frame=%q err=%v", producingFrame, err)
	}
	again, err := repo.CloneFrameHistory(context.Background(), input)
	if err != nil || again.GenesisSHA256 != result.GenesisSHA256 ||
		again.ArtifactCommitCount != result.ArtifactCommitCount || again.ArtifactRefCount != result.ArtifactRefCount {
		t.Fatalf("retry=%#v err=%v first=%#v", again, err, result)
	}
	if _, err := db.Exec(`UPDATE transcript_runner_attempts SET phase_sequence=phase_sequence+1
		WHERE stream_uid=? AND attempt=?`, target.UID, claim.Attempt); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CloneFrameHistory(context.Background(), input); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("tampered target retry error=%v, want ErrEventConflict", err)
	}
}

func TestCloneFrameHistoryUsesPreparedLegacyPlanWithoutActivatingSource(t *testing.T) {
	repo, db, source, activationInput := prepareHistoryActivationTest(t, "canonical-clone-legacy")
	if _, err := db.Exec(`UPDATE frames SET status='completed' WHERE id=?`, source.FrameID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO frames(id,project_id,root_frame_id,status) VALUES
		('frame-clone-legacy-target',?,'frame-clone-legacy-target','completed')`, source.ProjectID); err != nil {
		t.Fatal(err)
	}
	target, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:frame-clone-legacy-target", OwnerID: source.OwnerID, ExternalID: "frame-clone-legacy-target",
		SessionID: "frame-clone-legacy-target", Kind: StreamKindFrameRef, ProjectID: source.ProjectID,
		RootFrameID: "frame-clone-legacy-target", FrameID: "frame-clone-legacy-target", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	input := CloneFrameHistoryInput{
		SourceStreamUID: source.UID, TargetStreamUID: target.UID, OwnerID: source.OwnerID,
		LegacyCutoverID: activationInput.CutoverID,
	}
	result, err := repo.CloneFrameHistory(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.EventCount == 0 || result.BranchCount == 0 || result.ActiveBranchID == "" {
		t.Fatalf("result=%#v", result)
	}
	sourceAuthority, found, err := repo.GetFrameAuthorityBySession(
		context.Background(), source.OwnerID, source.SessionID,
	)
	if err != nil || !found || sourceAuthority.ReadAuthority != "legacy_mixed_v1" ||
		sourceAuthority.WriteAuthority != "legacy_frame_ref_v1" ||
		sourceAuthority.ActiveStreamUID != source.UID || len(sourceAuthority.ActivationID) != 0 ||
		len(sourceAuthority.GenesisID) != 0 {
		t.Fatalf("source authority=%#v found=%t err=%v", sourceAuthority, found, err)
	}
	var activated int
	if err := db.QueryRow(`SELECT activated FROM transcript_history_cutover_runs
		WHERE lower(hex(cutover_id))=?`, activationInput.CutoverID).Scan(&activated); err != nil || activated != 0 {
		t.Fatalf("cutover activated=%d err=%v", activated, err)
	}
	events, err := repo.ListProjectedEvents(context.Background(), ListProjectedEventsInput{
		StreamUID: target.UID, OwnerID: target.OwnerID, Limit: 1000,
	})
	if err != nil || len(events) != result.EventCount {
		t.Fatalf("events=%d result=%#v err=%v", len(events), result, err)
	}
	promptCount, answeredCount := 0, 0
	for _, event := range events {
		if event.Event.Source != EventSourcePayload || event.Event.FrameEventID != nil {
			t.Fatalf("legacy clone retained frame authority event=%#v", event.Event)
		}
		switch event.Event.Type {
		case AskUserPromptEventType:
			promptCount++
			prompt, err := DecodeAskUserPromptV1(event.ResolvedPayloadJSON)
			if err != nil || prompt.Origin.StreamUID != target.UID || prompt.Origin.FrameID != target.FrameID {
				t.Fatalf("prompt=%#v err=%v", prompt, err)
			}
		case AskUserResultEventType:
			resultEvent, err := DecodeAskUserResultEventV1(event.ResolvedPayloadJSON)
			if err != nil {
				t.Fatal(err)
			}
			if resultEvent.Result.Status == AskUserStatusAnswered {
				answeredCount++
			}
		}
	}
	if promptCount != 1 || answeredCount != 1 {
		t.Fatalf("prompt=%d answered=%d events=%#v", promptCount, answeredCount, events)
	}
	again, err := repo.CloneFrameHistory(context.Background(), input)
	if err != nil || again.GenesisSHA256 != result.GenesisSHA256 || again.SourceSHA256 != result.SourceSHA256 {
		t.Fatalf("retry=%#v err=%v first=%#v", again, err, result)
	}
}

func TestCloneFrameHistoryStreamsBeyondFormerBranchInventoryCeiling(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES('project-clone-large','owner-large');
		INSERT INTO frames(id,project_id,root_frame_id,status) VALUES
			('frame-clone-large-source','project-clone-large','frame-clone-large-source','completed'),
			('frame-clone-large-target','project-clone-large','frame-clone-large-target','completed')`); err != nil {
		t.Fatal(err)
	}
	source, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:frame-clone-large-source", OwnerID: "owner-large", ExternalID: "frame-clone-large-source",
		SessionID: "frame-clone-large-source", Kind: StreamKindFrameRef, ProjectID: "project-clone-large",
		RootFrameID: "frame-clone-large-source", FrameID: "frame-clone-large-source", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:frame-clone-large-target", OwnerID: "owner-large", ExternalID: "frame-clone-large-target",
		SessionID: "frame-clone-large-target", Kind: StreamKindFrameRef, ProjectID: "project-clone-large",
		RootFrameID: "frame-clone-large-target", FrameID: "frame-clone-large-target", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: source.UID, OwnerID: source.OwnerID, ClientMessageID: "clone-large-user",
		PayloadJSON: []byte(`{"role":"user","text":"clone without an invented branch ceiling"}`),
	}); err != nil || !created {
		t.Fatalf("append created=%v err=%v", created, err)
	}
	claim, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: source.UID, OwnerID: source.OwnerID, RunnerID: "clone-large-runner",
		TTL: time.Minute, ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	if _, _, created, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
		Claim: claim.Claim, ClientMessageID: "clone-large-finished", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`),
	}); err != nil || !created {
		t.Fatalf("finish created=%v err=%v", created, err)
	}
	var baseBranch string
	if err := db.QueryRow(`SELECT active_branch_id FROM transcript_branch_state WHERE stream_uid=?`, source.UID).
		Scan(&baseBranch); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	inserted := 0
	for candidate := 1; inserted < 1024; candidate++ {
		branchID := fmt.Sprintf("br_%08x", candidate)
		if branchID == baseBranch {
			continue
		}
		requestSHA := sha256.Sum256([]byte("clone-large-branch:" + branchID))
		if _, err := tx.Exec(`INSERT INTO transcript_branches(
			stream_uid,branch_id,parent_branch_id,fork_event_id,fork_point,kind,client_mutation_id,
			request_sha256,source_message_id,created_at,updated_at) VALUES(?,?,?,?,1,'edit',?,?,?,?,?)`,
			source.UID, branchID, baseBranch, int64(1), "clone-large:"+branchID,
			requestSHA[:], "clone-large-user", now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO transcript_branch_events(stream_uid,branch_id,ordinal,event_id)
			VALUES(?,?,1,1)`, source.UID, branchID); err != nil {
			t.Fatal(err)
		}
		inserted++
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	result, err := repo.CloneFrameHistory(context.Background(), CloneFrameHistoryInput{
		SourceStreamUID: source.UID, TargetStreamUID: target.UID, OwnerID: source.OwnerID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.BranchCount != 1025 || result.BranchEventCount != 1026 || result.EventCount != 2 {
		t.Fatalf("clone counts=%#v", result)
	}
	retry, err := repo.CloneFrameHistory(context.Background(), CloneFrameHistoryInput{
		SourceStreamUID: source.UID, TargetStreamUID: target.UID, OwnerID: source.OwnerID,
	})
	if err != nil || retry != result {
		t.Fatalf("retry=%#v err=%v first=%#v", retry, err, result)
	}
}
