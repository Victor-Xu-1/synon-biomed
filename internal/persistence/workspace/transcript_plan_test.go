package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestCompatibilityPlanApprovalIdentityIsCanonicalAndBaseVersionFenced(t *testing.T) {
	base := CompatibilityPlanApprovalIdentityInput{
		FrameID: "frame", ArtifactID: "artifact", BaseVersionID: "version-1",
		Text: "approve", AgentName: "agent",
		RuntimeConfig: map[string]any{"ultra_mode": true, "agentName": "agent"},
		EditedPlan:    map[string]any{"steps": []any{"a", "b"}, "title": "Plan"},
	}
	id, fingerprint, err := BuildCompatibilityPlanApprovalIdentity(base)
	if err != nil {
		t.Fatal(err)
	}
	reordered := base
	reordered.RuntimeConfig = map[string]any{"agentName": "agent", "ultra_mode": true}
	reordered.EditedPlan = map[string]any{"title": "Plan", "steps": []any{"a", "b"}}
	reorderedID, reorderedFingerprint, err := BuildCompatibilityPlanApprovalIdentity(reordered)
	if err != nil || reorderedID != id || reorderedFingerprint != fingerprint {
		t.Fatalf("canonical identity=%q/%q reordered=%q/%q err=%v", id, fingerprint, reorderedID, reorderedFingerprint, err)
	}
	differentBase := base
	differentBase.BaseVersionID = "version-2"
	differentID, differentFingerprint, err := BuildCompatibilityPlanApprovalIdentity(differentBase)
	if err != nil || differentID == id || differentFingerprint == fingerprint {
		t.Fatalf("base version was not fenced: id=%q fingerprint=%q err=%v", differentID, differentFingerprint, err)
	}
}

func TestDiscardCompatibilityPlanWithTranscriptSettlesOneAuthority(t *testing.T) {
	store, repo, stream, claim := newTranscriptPlanFixture(t)
	frame, event, idempotent, err := store.DiscardCompatibilityPlanWithTranscript(context.Background(), "frame-plan", "discard-plan")
	if err != nil || frame.Status != FrameStatusCompleted || event.Type != "plan_discarded" {
		t.Fatalf("frame=%#v event=%#v err=%v", frame, event, err)
	}
	if idempotent {
		t.Fatal("first discard was idempotent")
	}
	metadata, found, err := store.GetFrameRuntimeMetadata("frame-plan")
	if err != nil || !found || metadata.ContextData["preserved"] != "yes" || metadata.ContextData["_plan_json"] != nil {
		t.Fatalf("metadata=%#v found=%t err=%v", metadata, found, err)
	}
	state, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID, claim.Attempt)
	if err != nil || state.Status != FrameStatusCompleted || state.Phase != transcriptstore.RunnerPhaseTerminal {
		t.Fatalf("state=%#v err=%v", state, err)
	}
	againFrame, againEvent, idempotent, err := store.DiscardCompatibilityPlanWithTranscript(
		context.Background(), "frame-plan", "discard-plan",
	)
	if err != nil || !idempotent || againFrame.Status != FrameStatusCompleted || againEvent.ID != event.ID {
		t.Fatalf("againFrame=%#v againEvent=%#v idempotent=%t err=%v", againFrame, againEvent, idempotent, err)
	}
	projected, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, Limit: 100,
	})
	if err != nil || len(projected) == 0 || projected[len(projected)-1].Event.Type != "runner_finished" {
		t.Fatalf("projected=%#v err=%v", projected, err)
	}
	projection, err := repo.GetTerminalProjection(
		context.Background(), stream.OwnerID, stream.UID, projected[len(projected)-1].Event.EventID,
	)
	if err != nil || projection.StreamType != "finish" || projection.TerminalStatus != FrameStatusCompleted {
		t.Fatalf("projection=%#v err=%v", projection, err)
	}
	var terminals, receipts, planEvents int
	for query, target := range map[string]*int{
		`SELECT COUNT(*) FROM transcript_events WHERE stream_uid='frame:frame-plan' AND event_type='runner_finished'`: &terminals,
		`SELECT COUNT(*) FROM transcript_runner_receipts WHERE stream_uid='frame:frame-plan'`:                         &receipts,
		`SELECT COUNT(*) FROM frame_events WHERE frame_id='frame-plan' AND event_type='plan_discarded'`:               &planEvents,
	} {
		if err := store.db.QueryRow(query).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if terminals != 1 || receipts != 1 || planEvents != 1 {
		t.Fatalf("terminals=%d receipts=%d planEvents=%d", terminals, receipts, planEvents)
	}
}

func TestApproveCompatibilityPlanWithTranscriptCommitsOneResumeAuthority(t *testing.T) {
	store, repo, stream, originalClaim := newTranscriptPlanFixture(t)
	input := bindTranscriptPlanApprovalIdentity(t, ApproveCompatibilityPlanWithTranscriptInput{
		FrameID:              "frame-plan",
		Text:                 "[System] The user approved the proposed plan. Continue execution.",
		ExpectedPlanArtifact: "artifact-plan", AgentName: "agent",
		ContextData: map[string]any{
			"_plan_artifact_id": "artifact-plan", "_plan_json": map[string]any{"title": "Plan"}, "preserved": "yes",
		},
		RuntimeConfig: map[string]any{"agentName": "agent", "ultra_mode": true},
	})
	metadataBefore, found, err := store.GetFrameRuntimeMetadata("frame-plan")
	if err != nil || !found {
		t.Fatalf("metadata before=%#v found=%t err=%v", metadataBefore, found, err)
	}
	metadataBefore.ContextData["concurrent_metadata"] = "preserved"
	if _, err := store.SetFrameRuntimeMetadata("frame-plan", metadataBefore); err != nil {
		t.Fatal(err)
	}
	result, err := store.ApproveCompatibilityPlanWithTranscript(context.Background(), input)
	if err != nil || result.Frame.Status != FrameStatusProcessing || result.InputEvent.Type != "user_input_response" ||
		result.ResumeEvent.Type != "frame_resumed" || result.Checkpoint.Attempt != originalClaim.Attempt {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if result.ResumeEvent.Payload["approvalId"] != input.ApprovalID ||
		result.ResumeEvent.Payload["clientMessageId"] != input.ClientMessageID ||
		result.ResumeEvent.Payload["requestHash"] != input.ApprovalFingerprint {
		t.Fatalf("resume authority=%#v", result.ResumeEvent.Payload)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata("frame-plan")
	if err != nil || !found || metadata.ContextData["_plan_approved"] != true ||
		metadata.ContextData["_plan_approval_id"] != input.ApprovalID ||
		metadata.ContextData["_plan_approval_fingerprint"] != input.ApprovalFingerprint || metadata.ContextData["preserved"] != "yes" ||
		metadata.ContextData["concurrent_metadata"] != "preserved" {
		t.Fatalf("metadata=%#v found=%t err=%v", metadata, found, err)
	}
	current, err := repo.GetStream(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || current.InputRevision != 2 || current.ConsumedInputRevision != 1 {
		t.Fatalf("stream=%#v err=%v", current, err)
	}
	again, err := store.ApproveCompatibilityPlanWithTranscript(context.Background(), input)
	if err != nil || !again.Idempotent || again.Frame.Status != FrameStatusProcessing {
		t.Fatalf("idempotent result=%#v err=%v", again, err)
	}
	if again.InputEvent.EventID != result.InputEvent.EventID || again.ResumeEvent.ID != result.ResumeEvent.ID ||
		again.Checkpoint.Sequence != result.Checkpoint.Sequence {
		t.Fatalf("replayed receipt=%#v original=%#v", again, result)
	}
	tamperedRuntime := input
	tamperedRuntime.RuntimeConfig = map[string]any{"agentName": "agent", "ultra_mode": false}
	if _, err := store.ApproveCompatibilityPlanWithTranscript(context.Background(), tamperedRuntime); !errors.Is(err, transcriptstore.ErrEventConflict) {
		t.Fatalf("same fingerprint with changed runtime err=%v", err)
	}
	conflict := bindTranscriptPlanApprovalIdentity(t, ApproveCompatibilityPlanWithTranscriptInput{
		FrameID:              "frame-plan",
		Text:                 "[System] The user approved the proposed plan. Continue execution.",
		ExpectedPlanArtifact: "artifact-plan", AgentName: "agent",
		ContextData:   map[string]any{"_plan_artifact_id": "artifact-plan"},
		RuntimeConfig: map[string]any{"agentName": "agent"},
	})
	if _, err := store.ApproveCompatibilityPlanWithTranscript(context.Background(), conflict); !errors.Is(err, transcriptstore.ErrEventConflict) {
		t.Fatalf("conflicting replay err=%v", err)
	}
	var outboxRows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM workspace_outbox
		WHERE topic=? AND json_extract(payload_json,'$.frameEventId')=?`, RealtimeOutboxTopic, result.ResumeEvent.ID).Scan(&outboxRows); err != nil || outboxRows != 1 {
		t.Fatalf("resume outbox rows=%d err=%v", outboxRows, err)
	}
	runtimeState, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID, originalClaim.Attempt)
	if err != nil || runtimeState.Status != "running" || runtimeState.Phase != transcriptstore.RunnerPhaseWaitingApproval ||
		runtimeState.ExpiresAt.After(time.Now().UTC()) {
		t.Fatalf("runtime=%#v err=%v", runtimeState, err)
	}
	if generic, err := repo.ClaimNextRunner(context.Background(), transcriptstore.ClaimNextRunnerInput{
		RunnerID: "generic-runner", TTL: time.Minute,
	}); err != nil || generic.Claimed {
		t.Fatalf("generic claim=%#v err=%v", generic, err)
	}
	dispatch, claimed, err := store.ClaimNextCompatibilityFrameResumeDispatch("plan-dispatch", time.Minute)
	if err != nil || !claimed || dispatch.ResumeEvent.ID != result.ResumeEvent.ID {
		t.Fatalf("dispatch=%#v claimed=%t err=%v", dispatch, claimed, err)
	}
	resumed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "frame-resume:" + dispatch.ResumeEvent.ID,
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceCheckpoint,
		ResumeCheckpoint: result.Checkpoint.Sequence,
	})
	if err != nil || !resumed.Claimed || resumed.Claim.Attempt != originalClaim.Attempt {
		t.Fatalf("resumed=%#v err=%v", resumed, err)
	}
}

func TestApproveCompatibilityPlanWithTranscriptRollsBackEveryAuthorityOnDispatchFailure(t *testing.T) {
	store, repo, stream, originalClaim := newTranscriptPlanFixture(t)
	beforeState, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID, originalClaim.Attempt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER reject_plan_resume BEFORE INSERT ON frame_events
		WHEN NEW.event_type='frame_resumed' BEGIN SELECT RAISE(ABORT,'forced plan resume failure'); END`); err != nil {
		t.Fatal(err)
	}
	rollbackInput := bindTranscriptPlanApprovalIdentity(t, ApproveCompatibilityPlanWithTranscriptInput{
		FrameID:              "frame-plan",
		Text:                 "[System] The user approved the proposed plan. Continue execution.",
		ExpectedPlanArtifact: "artifact-plan", AgentName: "agent",
		ContextData:   map[string]any{"_plan_artifact_id": "artifact-plan", "_plan_json": map[string]any{"title": "Plan"}},
		RuntimeConfig: map[string]any{"agentName": "agent"},
	})
	_, err = store.ApproveCompatibilityPlanWithTranscript(context.Background(), rollbackInput)
	if err == nil {
		t.Fatal("approval succeeded without durable resume dispatch")
	}
	frame, found, getErr := store.GetFrame("frame-plan")
	if getErr != nil || !found || frame.Status != FrameStatusAwaitingPlanApproval {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, getErr)
	}
	metadata, found, getErr := store.GetFrameRuntimeMetadata("frame-plan")
	if getErr != nil || !found || metadata.ContextData["_plan_approved"] != nil || metadata.ContextData["_plan_json"] == nil {
		t.Fatalf("metadata=%#v found=%t err=%v", metadata, found, getErr)
	}
	current, getErr := repo.GetStream(context.Background(), stream.UID, stream.OwnerID)
	if getErr != nil || current.InputRevision != 1 || current.ConsumedInputRevision != 1 {
		t.Fatalf("stream=%#v err=%v", current, getErr)
	}
	afterState, getErr := repo.GetRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID, originalClaim.Attempt)
	if getErr != nil || afterState.Status != beforeState.Status || afterState.Phase != beforeState.Phase ||
		!afterState.ExpiresAt.Equal(beforeState.ExpiresAt) {
		t.Fatalf("before=%#v after=%#v err=%v", beforeState, afterState, getErr)
	}
	var inputs, resumes int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='user_input_response'`, stream.UID).Scan(&inputs); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM frame_events WHERE frame_id=? AND event_type='frame_resumed'`, stream.FrameID).Scan(&resumes); err != nil {
		t.Fatal(err)
	}
	if inputs != 0 || resumes != 0 {
		t.Fatalf("inputs=%d resumes=%d", inputs, resumes)
	}
}

func TestApproveCompatibilityPlanWithTranscriptCommitsEditedArtifactAtomicallyAndIdempotently(t *testing.T) {
	store, _, _, _ := newTranscriptPlanFixture(t)
	original := seedTranscriptPlanArtifactVersion(t, store)
	input := bindTranscriptPlanApprovalIdentity(t, ApproveCompatibilityPlanWithTranscriptInput{
		FrameID:              "frame-plan",
		Text:                 "[System] The user approved the proposed plan. Continue execution.",
		ExpectedPlanArtifact: "artifact-plan", ExpectedPlanVersion: original.ID, AgentName: "agent",
		ContextData: map[string]any{
			"_plan_artifact_id": "artifact-plan", "_plan_version_id": original.ID,
			"_plan_json": map[string]any{"title": "Edited Plan"}, "preserved": "yes",
		},
		RuntimeConfig:  map[string]any{"agentName": "agent", "ultra_mode": true},
		EditedPlanJSON: []byte(`{"title":"Edited Plan"}`),
	})
	result, err := store.ApproveCompatibilityPlanWithTranscript(context.Background(), input)
	if err != nil || result.Idempotent || result.EditedPlanVersionID == "" || result.BlobFinalizeDeferred {
		t.Fatalf("edited approval=%#v err=%v", result, err)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata("frame-plan")
	if err != nil || !found || metadata.ContextData["_plan_version_id"] != result.EditedPlanVersionID {
		t.Fatalf("metadata=%#v found=%t err=%v", metadata, found, err)
	}
	_, history, found, err := store.ListArtifactVersionHistory("artifact-plan")
	if err != nil || !found || len(history) != 2 || history[0].VersionID != original.ID || history[1].VersionID != result.EditedPlanVersionID {
		t.Fatalf("history=%#v found=%t err=%v", history, found, err)
	}
	replayed, err := store.ApproveCompatibilityPlanWithTranscript(context.Background(), input)
	if err != nil || !replayed.Idempotent || replayed.Frame.Status != FrameStatusProcessing {
		t.Fatalf("edited replay=%#v err=%v", replayed, err)
	}
	tampered := input
	tampered.EditedPlanJSON = []byte(`{"title":"Different Plan"}`)
	if _, err := store.ApproveCompatibilityPlanWithTranscript(context.Background(), tampered); !errors.Is(err, transcriptstore.ErrEventConflict) {
		t.Fatalf("tampered edited replay err=%v", err)
	}
	_, history, _, err = store.ListArtifactVersionHistory("artifact-plan")
	if err != nil || len(history) != 2 {
		t.Fatalf("replay created artifact versions: history=%#v err=%v", history, err)
	}
}

func TestApproveCompatibilityPlanWithTranscriptRollsBackEditedArtifactWithDispatch(t *testing.T) {
	store, _, _, _ := newTranscriptPlanFixture(t)
	original := seedTranscriptPlanArtifactVersion(t, store)
	if _, err := store.db.Exec(`CREATE TRIGGER reject_edited_plan_resume BEFORE INSERT ON frame_events
		WHEN NEW.event_type='frame_resumed' BEGIN SELECT RAISE(ABORT,'forced edited plan resume failure'); END`); err != nil {
		t.Fatal(err)
	}
	rollbackInput := bindTranscriptPlanApprovalIdentity(t, ApproveCompatibilityPlanWithTranscriptInput{
		FrameID:              "frame-plan",
		Text:                 "[System] The user approved the proposed plan. Continue execution.",
		ExpectedPlanArtifact: "artifact-plan", ExpectedPlanVersion: original.ID, AgentName: "agent",
		ContextData: map[string]any{
			"_plan_artifact_id": "artifact-plan", "_plan_version_id": original.ID,
			"_plan_json": map[string]any{"title": "Edited Plan"},
		},
		RuntimeConfig:  map[string]any{"agentName": "agent"},
		EditedPlanJSON: []byte(`{"title":"Edited Plan"}`),
	})
	_, err := store.ApproveCompatibilityPlanWithTranscript(context.Background(), rollbackInput)
	if err == nil {
		t.Fatal("edited approval succeeded without durable resume")
	}
	_, history, found, historyErr := store.ListArtifactVersionHistory("artifact-plan")
	if historyErr != nil || !found || len(history) != 1 || history[0].VersionID != original.ID {
		t.Fatalf("rolled back history=%#v found=%t err=%v", history, found, historyErr)
	}
	metadata, found, metadataErr := store.GetFrameRuntimeMetadata("frame-plan")
	if metadataErr != nil || !found || metadata.ContextData["_plan_version_id"] != original.ID || metadata.ContextData["_plan_approved"] != nil {
		t.Fatalf("rolled back metadata=%#v found=%t err=%v", metadata, found, metadataErr)
	}
	var markers int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM blob_commit_markers`).Scan(&markers); err != nil || markers != 0 {
		t.Fatalf("blob markers=%d err=%v", markers, err)
	}
}

func TestApproveCompatibilityPlanWithTranscriptPreservesBlobSafetyWhenCommitIsRejected(t *testing.T) {
	store, _, _, _ := newTranscriptPlanFixture(t)
	original := seedTranscriptPlanArtifactVersion(t, store)
	for _, statement := range []string{
		`CREATE TABLE plan_commit_parent(id INTEGER PRIMARY KEY)`,
		`CREATE TABLE plan_commit_child(parent_id INTEGER REFERENCES plan_commit_parent(id) DEFERRABLE INITIALLY DEFERRED)`,
		`CREATE TRIGGER reject_plan_commit AFTER INSERT ON frame_events
			WHEN NEW.event_type='frame_resumed' BEGIN INSERT INTO plan_commit_child(parent_id) VALUES(999); END`,
	} {
		if _, err := store.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	input := bindTranscriptPlanApprovalIdentity(t, ApproveCompatibilityPlanWithTranscriptInput{
		FrameID: "frame-plan", Text: "[System] The user approved the proposed plan. Continue execution.",
		ExpectedPlanArtifact: "artifact-plan", ExpectedPlanVersion: original.ID, AgentName: "agent",
		RuntimeConfig: map[string]any{"agentName": "agent"}, EditedPlanJSON: []byte(`{"title":"Edited Plan"}`),
	})
	if _, err := store.ApproveCompatibilityPlanWithTranscript(context.Background(), input); err == nil {
		t.Fatal("approval succeeded after deferred commit constraint rejected COMMIT")
	}
	frame, found, err := store.GetFrame("frame-plan")
	if err != nil || !found || frame.Status != FrameStatusAwaitingPlanApproval {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
	_, history, found, err := store.ListArtifactVersionHistory("artifact-plan")
	if err != nil || !found || len(history) != 1 || history[0].VersionID != original.ID {
		t.Fatalf("history=%#v found=%t err=%v", history, found, err)
	}
	staging, err := filepath.Glob(filepath.Join(store.blobRoot, "staging", ".artifact-write-*"))
	if err != nil || len(staging) != 0 {
		t.Fatalf("staging=%#v err=%v", staging, err)
	}
}

func TestDiscardCompatibilityPlanWithTranscriptRollsBackOnReceiptFailure(t *testing.T) {
	store, repo, stream, claim := newTranscriptPlanFixture(t)
	if _, err := store.db.Exec(`DROP TABLE transcript_runner_receipts`); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.DiscardCompatibilityPlanWithTranscript(context.Background(), "frame-plan", "discard-plan"); err == nil {
		t.Fatal("discard succeeded without terminal receipt storage")
	}
	frame, found, err := store.GetFrame("frame-plan")
	if err != nil || !found || frame.Status != FrameStatusAwaitingPlanApproval {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata("frame-plan")
	if err != nil || !found || metadata.ContextData["_plan_json"] == nil {
		t.Fatalf("metadata=%#v found=%t err=%v", metadata, found, err)
	}
	state, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID, claim.Attempt)
	if err != nil || state.Status != "running" || state.Phase != transcriptstore.RunnerPhaseWaitingApproval {
		t.Fatalf("state=%#v err=%v", state, err)
	}
	var terminals, planEvents int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='runner_finished'`, stream.UID).Scan(&terminals); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM frame_events WHERE frame_id='frame-plan' AND event_type='plan_discarded'`).Scan(&planEvents); err != nil {
		t.Fatal(err)
	}
	if terminals != 0 || planEvents != 0 {
		t.Fatalf("terminals=%d planEvents=%d", terminals, planEvents)
	}
}

func newTranscriptPlanFixture(t *testing.T) (*Store, *transcriptstore.Repository, transcriptstore.Stream, transcriptstore.RunnerClaim) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-plan", UserID: "owner-plan", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: "frame-plan", ProjectID: "project-plan", AgentName: "agent",
		Status: FrameStatusProcessing, ConversationType: "task",
	}); err != nil {
		t.Fatal(err)
	}
	repo, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:frame-plan", OwnerID: "owner-plan", ExternalID: "frame-plan", SessionID: "frame-plan",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project-plan", RootFrameID: "frame-plan", FrameID: "frame-plan", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "task", FrameEventID: "task-event",
		MessageUUID: "task-message", Text: "Prepare a plan.", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append created=%t err=%v", created, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "planning-runner", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	if _, _, created, err := repo.PauseRunnerForApproval(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claimed.Claim, ClientMessageID: "plan-paused", Phase: transcriptstore.RunnerPhaseWaitingApproval,
		Resumable: true, PayloadJSON: []byte(`{"status":"awaiting_plan_approval"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("pause created=%t err=%v", created, err)
	}
	if _, err := store.SetFrameRuntimeMetadata("frame-plan", FrameRuntimeMetadata{ContextData: map[string]any{
		"_plan_artifact_id": "artifact-plan", "_plan_json": map[string]any{"title": "Plan"}, "preserved": "yes",
	}}); err != nil {
		t.Fatal(err)
	}
	status := FrameStatusAwaitingPlanApproval
	if _, err := store.UpdateFrame("frame-plan", UpdateFrameInput{Status: &status}); err != nil {
		t.Fatal(err)
	}
	return store, repo, stream, claimed.Claim
}

func seedTranscriptPlanArtifactVersion(t *testing.T, store *Store) ArtifactVersion {
	t.Helper()
	_, version, err := store.WriteArtifactVersion(context.Background(), WriteArtifactVersionInput{
		ArtifactID: "artifact-plan", ProjectID: "project-plan", Name: "plan.json", ContentType: "application/json",
		Content: strings.NewReader(`{"title":"Plan"}`), CreatedBy: "agent", MaxBytes: 1 << 20,
		RootFrameID: "frame-plan", FrameID: "frame-plan",
	})
	if err != nil {
		t.Fatal(err)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata("frame-plan")
	if err != nil || !found {
		t.Fatalf("metadata=%#v found=%t err=%v", metadata, found, err)
	}
	metadata.ContextData["_plan_version_id"] = version.ID
	if _, err := store.SetFrameRuntimeMetadata("frame-plan", metadata); err != nil {
		t.Fatal(err)
	}
	return version
}

func bindTranscriptPlanApprovalIdentity(
	t *testing.T,
	input ApproveCompatibilityPlanWithTranscriptInput,
) ApproveCompatibilityPlanWithTranscriptInput {
	t.Helper()
	var editedPlan map[string]any
	if len(input.EditedPlanJSON) > 0 {
		if err := json.Unmarshal(input.EditedPlanJSON, &editedPlan); err != nil {
			t.Fatal(err)
		}
	}
	approvalID, fingerprint, err := BuildCompatibilityPlanApprovalIdentity(CompatibilityPlanApprovalIdentityInput{
		FrameID: input.FrameID, ArtifactID: input.ExpectedPlanArtifact, BaseVersionID: input.ExpectedPlanVersion,
		Text: input.Text, AgentName: input.AgentName, RuntimeConfig: input.RuntimeConfig, EditedPlan: editedPlan,
	})
	if err != nil {
		t.Fatal(err)
	}
	input.ApprovalID = approvalID
	input.ApprovalFingerprint = fingerprint
	input.ClientMessageID = approvalID
	return input
}
