package transcript

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestForkFrameUserMessageBranchFencesRunnerAndCreatesRunnableChild(t *testing.T) {
	repo, db, stream, source, claim := newFrameBranchForkFixture(t)
	base, err := repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO frame_runtime_metadata(frame_id,context_data) VALUES(?, '{"_pending_input_requests":[{"tool_id":"stale"}]}')
		ON CONFLICT(frame_id) DO UPDATE SET context_data=excluded.context_data;
		UPDATE frames SET status='awaiting_user_response' WHERE id=?`, stream.FrameID, stream.FrameID); err != nil {
		t.Fatal(err)
	}
	result, err := repo.ForkFrameUserMessageBranch(context.Background(), ForkFrameUserMessageBranchInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		SourceBranchID: base.ActiveBranchID, ExpectedActiveBranchID: base.ActiveBranchID, ExpectedGeneration: base.Generation,
		ClientMutationID: "edit-mutation-1", SourceClientMessageID: source.ClientMessageID,
		SourceMessageIndex: 0,
		ReplacementText:    "run the corrected analysis", Destinations: []string{"ws"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || result.BranchID == base.ActiveBranchID || result.ParentBranchID != base.ActiveBranchID ||
		result.Generation != 2 || result.ForkEventID != source.EventID || result.ForkPoint != 0 ||
		result.ReplacementEvent.Type != "user_message" || result.ReplacementFrameEvent.Type != "user_message" ||
		!result.RunnerCancellation.Applied || result.RunnerCancellation.CurrentStatus != "cancelled" {
		t.Fatalf("unexpected fork result %#v", result)
	}
	state, err := repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || state.ActiveBranchID != result.BranchID || state.Generation != 2 {
		t.Fatalf("state=%#v err=%v", state, err)
	}
	if _, _, _, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
		Claim: claim, ClientMessageID: "late-old-finish", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`), Destinations: []string{"ws"},
	}); !errors.Is(err, ErrClaimStale) {
		t.Fatalf("late old runner finish error=%v", err)
	}
	var baseEvents, childEvents, replacementOrdinal int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_branch_events WHERE stream_uid=? AND branch_id=?`,
		stream.UID, base.ActiveBranchID).Scan(&baseEvents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*),MIN(ordinal) FROM transcript_branch_events WHERE stream_uid=? AND branch_id=?`,
		stream.UID, result.BranchID).Scan(&childEvents, &replacementOrdinal); err != nil {
		t.Fatal(err)
	}
	if baseEvents != 3 || childEvents != 1 || replacementOrdinal != 1 {
		t.Fatalf("branch memberships base=%d child=%d ordinal=%d", baseEvents, childEvents, replacementOrdinal)
	}
	var sourceMessageID string
	if err := db.QueryRow(`SELECT source_message_id FROM transcript_branches WHERE stream_uid=? AND branch_id=?`,
		stream.UID, result.BranchID).Scan(&sourceMessageID); err != nil || sourceMessageID != "source-message" {
		t.Fatalf("source message id=%q err=%v", sourceMessageID, err)
	}
	var frameStatus string
	var inputRevision, consumedRevision int64
	if err := db.QueryRow(`SELECT status FROM frames WHERE id=?`, stream.FrameID).Scan(&frameStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT input_revision,consumed_input_revision FROM transcript_streams WHERE stream_uid=?`, stream.UID).
		Scan(&inputRevision, &consumedRevision); err != nil {
		t.Fatal(err)
	}
	if frameStatus != "processing" || inputRevision != 2 || consumedRevision != 1 {
		t.Fatalf("frame status=%q revisions=%d/%d", frameStatus, inputRevision, consumedRevision)
	}
	intent, found, err := repo.GetActiveFrameTaskIntent(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found || intent.Text != "run the corrected analysis" ||
		intent.SourceEventID != result.ReplacementEvent.ClientMessageID {
		t.Fatalf("intent=%#v found=%t err=%v", intent, found, err)
	}
	for _, event := range []Event{result.RunnerCancellation.Event, result.ReplacementEvent} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_delivery_intents WHERE stream_uid=? AND publication_seq=? AND destination='ws'`,
			stream.UID, event.PublicationSeq).Scan(&count); err != nil || count != 1 {
			t.Fatalf("event %d delivery count=%d err=%v", event.EventID, count, err)
		}
	}
}

func TestForkFrameUserMessageAtIndexResolvesCanonicalSourceAndRetriesAfterBranchSwitch(t *testing.T) {
	repo, _, stream, source, _ := newFrameBranchForkFixture(t)
	base, err := repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	input := ForkFrameUserMessageAtIndexInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		SourceBranchID: base.ActiveBranchID, ExpectedActiveBranchID: base.ActiveBranchID,
		ExpectedGeneration: base.Generation, ClientMutationID: "public-edit-retry",
		SourceMessageIndex: 0, ReplacementText: "corrected public request", Destinations: []string{"ws"},
	}
	result, err := repo.ForkFrameUserMessageAtIndex(context.Background(), input)
	if err != nil || !result.Created || result.ForkEventID != source.EventID || result.ForkPoint != 0 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	retry, err := repo.ForkFrameUserMessageAtIndex(context.Background(), input)
	if err != nil || retry.Created || retry.BranchID != result.BranchID || retry.Generation != result.Generation ||
		retry.ReplacementEvent.EventID != result.ReplacementEvent.EventID {
		t.Fatalf("retry=%#v err=%v", retry, err)
	}
	input.SourceMessageIndex = 1
	if _, err := repo.ForkFrameUserMessageAtIndex(context.Background(), input); !errors.Is(err, ErrBranchTargetNotFound) {
		t.Fatalf("wrong visible source index error=%v", err)
	}
}

func TestListBranchesAndReadInactiveBranchWithGenerationCursor(t *testing.T) {
	repo, _, stream, source, _ := newFrameBranchForkFixture(t)
	base, err := repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	forked, err := repo.ForkFrameUserMessageAtIndex(context.Background(), ForkFrameUserMessageAtIndexInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		SourceBranchID: base.ActiveBranchID, ExpectedActiveBranchID: base.ActiveBranchID,
		ExpectedGeneration: base.Generation, ClientMutationID: "read-inactive-branch",
		SourceMessageIndex: 0, ReplacementText: "replacement", Destinations: []string{"ws"},
	})
	if err != nil {
		t.Fatal(err)
	}
	state, branches, err := repo.ListBranches(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || state.ActiveBranchID != forked.BranchID || state.Generation != 2 || len(branches) != 2 {
		t.Fatalf("state=%#v branches=%#v err=%v", state, branches, err)
	}
	if branches[0].BranchID != base.ActiveBranchID || branches[0].Active || branches[0].Kind != "base" ||
		branches[1].BranchID != forked.BranchID || !branches[1].Active || branches[1].ParentBranchID != base.ActiveBranchID {
		t.Fatalf("branches=%#v", branches)
	}
	snapshot, err := repo.GetBranchProjectionSnapshot(
		context.Background(), stream.UID, stream.OwnerID, base.ActiveBranchID,
	)
	if err != nil || snapshot.BranchID != base.ActiveBranchID || snapshot.BranchGeneration != 2 ||
		snapshot.ThroughPublicationSequence < source.PublicationSeq {
		t.Fatalf("snapshot=%#v err=%v", snapshot, err)
	}
	page, err := repo.ListProjectedEvents(context.Background(), ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		BranchID: base.ActiveBranchID, BranchGeneration: snapshot.BranchGeneration,
		ThroughPublicationSequence: snapshot.ThroughPublicationSequence, Limit: 20,
	})
	if err != nil || len(page) != 3 || page[0].Event.ClientMessageID != source.ClientMessageID {
		t.Fatalf("inactive page=%#v err=%v", page, err)
	}
	_, err = repo.ListProjectedEvents(context.Background(), ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		BranchID: base.ActiveBranchID, BranchGeneration: 1,
		ThroughPublicationSequence: snapshot.ThroughPublicationSequence, Limit: 20,
	})
	if !errors.Is(err, ErrBranchStateStale) {
		t.Fatalf("stale cursor error=%v", err)
	}
}

func TestForkFrameUserMessageBranchReplacesMiddleMessageAndSharesExactPrefix(t *testing.T) {
	repo, db, stream, _, _ := newFrameBranchForkFixture(t)
	second, _, created, err := repo.AppendFrameUserEvent(context.Background(), AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "source-user-two",
		FrameEventID: "source-frame-event-two", MessageUUID: "source-message-two", Text: "second request",
		Destinations: []string{"ws"},
	})
	if err != nil || !created {
		t.Fatalf("second created=%t err=%v", created, err)
	}
	base, err := repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := repo.ForkFrameUserMessageBranch(context.Background(), ForkFrameUserMessageBranchInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		SourceBranchID: base.ActiveBranchID, ExpectedActiveBranchID: base.ActiveBranchID, ExpectedGeneration: 1,
		ClientMutationID: "middle-edit", SourceClientMessageID: second.ClientMessageID, SourceMessageIndex: 2,
		ReplacementText: "corrected second request", Destinations: []string{"ws"},
	})
	if err != nil || !result.Created || result.ForkEventID != second.EventID || result.ForkPoint != 2 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	rows, err := db.Query(`
		SELECT event.client_message_id FROM transcript_branch_events membership
		JOIN transcript_events event ON event.stream_uid=membership.stream_uid AND event.event_id=membership.event_id
		WHERE membership.stream_uid=? AND membership.branch_id=? ORDER BY membership.ordinal`, stream.UID, result.BranchID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var clientIDs []string
	for rows.Next() {
		var clientID string
		if err := rows.Scan(&clientID); err != nil {
			t.Fatal(err)
		}
		clientIDs = append(clientIDs, clientID)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"source-user", "assistant-old", "branch-edit:middle-edit"}
	if len(clientIDs) != len(want) {
		t.Fatalf("child events=%v", clientIDs)
	}
	for index := range want {
		if clientIDs[index] != want[index] {
			t.Fatalf("child events=%v", clientIDs)
		}
	}
	projected, err := repo.ListProjectedEvents(context.Background(), ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, Limit: 20,
	})
	if err != nil || len(projected) != len(want) {
		t.Fatalf("active projection=%#v err=%v", projected, err)
	}
	for index := range want {
		if projected[index].Event.ClientMessageID != want[index] {
			t.Fatalf("active projection=%#v", projected)
		}
	}
	replay, err := repo.ListRunnerReplay(context.Background(), ListRunnerReplayInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, MessageLimit: 20, CheckpointLimit: 20,
	})
	if err != nil || len(replay) != len(want) {
		t.Fatalf("active replay=%#v err=%v", replay, err)
	}
	for index := range want {
		if replay[index].Event.ClientMessageID != want[index] {
			t.Fatalf("active replay=%#v", replay)
		}
	}
}

func TestProjectionSnapshotRejectsBranchSwitchBetweenPages(t *testing.T) {
	repo, _, stream, source, _ := newFrameBranchForkFixture(t)
	base, err := repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := repo.GetProjectionSnapshot(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := repo.ListProjectedEvents(context.Background(), ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		BranchID: snapshot.BranchID, BranchGeneration: snapshot.BranchGeneration,
		ThroughPublicationSequence: snapshot.ThroughPublicationSequence, Limit: 1,
	})
	if err != nil || len(first) != 1 || first[0].Event.EventID != source.EventID {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	forked, err := repo.ForkFrameUserMessageBranch(context.Background(), ForkFrameUserMessageBranchInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		SourceBranchID: base.ActiveBranchID, ExpectedActiveBranchID: base.ActiveBranchID,
		ExpectedGeneration: base.Generation, ClientMutationID: "snapshot-switch",
		SourceClientMessageID: source.ClientMessageID, SourceMessageIndex: 0,
		ReplacementText: "replacement after snapshot", Destinations: []string{"ws"},
	})
	if err != nil || !forked.Created {
		t.Fatalf("forked=%#v err=%v", forked, err)
	}
	_, err = repo.ListProjectedEvents(context.Background(), ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		BranchID: snapshot.BranchID, BranchGeneration: snapshot.BranchGeneration,
		AfterPublicationSequence:   first[0].Event.PublicationSeq,
		ThroughPublicationSequence: snapshot.ThroughPublicationSequence, Limit: 20,
	})
	if !errors.Is(err, ErrBranchStateStale) {
		t.Fatalf("stale snapshot error=%v", err)
	}
	_, err = repo.ListProjectedEvents(context.Background(), ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		AfterPublicationSequence: first[0].Event.PublicationSeq, Limit: 20,
	})
	if !errors.Is(err, ErrBranchStateStale) {
		t.Fatalf("unscoped cursor error=%v", err)
	}
	current, err := repo.GetProjectionSnapshot(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || current.BranchID != forked.BranchID || current.BranchGeneration != snapshot.BranchGeneration+1 {
		t.Fatalf("current=%#v err=%v", current, err)
	}
	active, err := repo.ListProjectedEvents(context.Background(), ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		BranchID: current.BranchID, BranchGeneration: current.BranchGeneration,
		ThroughPublicationSequence: current.ThroughPublicationSequence, Limit: 20,
	})
	if err != nil || len(active) != 1 || active[0].Event.EventID != forked.ReplacementEvent.EventID {
		t.Fatalf("active=%#v err=%v", active, err)
	}
}

func TestForkFrameUserMessageBranchDoesNotFabricateRunnerAttempts(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES('project-no-runner','owner-no-runner')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO frames(id,project_id,root_frame_id,status) VALUES('frame-no-runner','project-no-runner','frame-no-runner','processing')`); err != nil {
		t.Fatal(err)
	}
	stream, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:frame-no-runner", OwnerID: "owner-no-runner", ExternalID: "frame-no-runner", SessionID: "frame-no-runner",
		Kind: StreamKindFrameRef, ProjectID: "project-no-runner", RootFrameID: "frame-no-runner", FrameID: "frame-no-runner", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	source, _, created, err := repo.AppendFrameUserEvent(context.Background(), AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "source-no-runner",
		FrameEventID: "source-frame-no-runner", MessageUUID: "message-no-runner", Text: "original",
		Destinations: []string{"ws"},
	})
	if err != nil || !created {
		t.Fatalf("source created=%t err=%v", created, err)
	}
	base, err := repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := repo.ForkFrameUserMessageBranch(context.Background(), ForkFrameUserMessageBranchInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		SourceBranchID: base.ActiveBranchID, ExpectedActiveBranchID: base.ActiveBranchID, ExpectedGeneration: 1,
		ClientMutationID: "no-runner-edit", SourceClientMessageID: source.ClientMessageID, SourceMessageIndex: 0,
		ReplacementText: "corrected", Destinations: []string{"ws"},
	})
	if err != nil || !result.Created || result.RunnerCancellation.Applied || result.ReplacementEvent.Type != "user_message" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	var attempts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_runner_attempts WHERE stream_uid=?`, stream.UID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	current, err := repo.GetStream(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || attempts != 0 || current.InputRevision != 2 || current.ConsumedInputRevision != 1 {
		t.Fatalf("attempts=%d stream=%#v err=%v", attempts, current, err)
	}
	intent, found, err := repo.GetActiveFrameTaskIntent(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found || intent.Text != "corrected" || intent.SourceMessageID != "branch-edit:"+result.BranchID {
		t.Fatalf("intent=%#v found=%t err=%v", intent, found, err)
	}
}

func TestForkFrameUserMessageBranchPreservesTerminalAttempt(t *testing.T) {
	repo, db, stream, source, claim := newFrameBranchForkFixture(t)
	if _, _, created, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
		Claim: claim, ClientMessageID: "finished-before-fork", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("finish created=%t err=%v", created, err)
	}
	base, err := repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := repo.ForkFrameUserMessageBranch(context.Background(), ForkFrameUserMessageBranchInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		SourceBranchID: base.ActiveBranchID, ExpectedActiveBranchID: base.ActiveBranchID, ExpectedGeneration: 1,
		ClientMutationID: "terminal-edit", SourceClientMessageID: source.ClientMessageID, SourceMessageIndex: 0,
		ReplacementText: "new work", Destinations: []string{"ws"},
	})
	if err != nil || !result.Created || result.RunnerCancellation.Applied {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	runtime, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID, claim.Attempt)
	if err != nil || runtime.Status != "completed" {
		t.Fatalf("runtime=%#v err=%v", runtime, err)
	}
	var receipts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_runner_receipts WHERE stream_uid=?`, stream.UID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if receipts != 1 {
		t.Fatalf("receipts=%d", receipts)
	}
}

func TestForkFrameUserMessageBranchIsIdempotentOwnerScopedAndCanForkInactiveSource(t *testing.T) {
	repo, db, stream, source, _ := newFrameBranchForkFixture(t)
	base, err := repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	input := ForkFrameUserMessageBranchInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		SourceBranchID: base.ActiveBranchID, ExpectedActiveBranchID: base.ActiveBranchID, ExpectedGeneration: 1,
		ClientMutationID: "edit-idempotent", SourceClientMessageID: source.ClientMessageID,
		SourceMessageIndex: 0,
		ReplacementText:    "first correction", Destinations: []string{"ws"},
	}
	first, err := repo.ForkFrameUserMessageBranch(context.Background(), input)
	if err != nil || !first.Created {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	retry, err := repo.ForkFrameUserMessageBranch(context.Background(), input)
	if err != nil || retry.Created || retry.BranchID != first.BranchID || retry.ReplacementEvent.EventID != first.ReplacementEvent.EventID {
		t.Fatalf("retry=%#v err=%v", retry, err)
	}
	conflict := input
	conflict.ReplacementText = "conflicting retry"
	if _, err := repo.ForkFrameUserMessageBranch(context.Background(), conflict); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("conflicting retry error=%v", err)
	}
	foreign := input
	foreign.ClientMutationID = "foreign-owner"
	foreign.OwnerID = "owner-foreign"
	if _, err := repo.ForkFrameUserMessageBranch(context.Background(), foreign); !errors.Is(err, ErrOwnerMismatch) {
		t.Fatalf("foreign owner error=%v", err)
	}
	stale := input
	stale.ClientMutationID = "stale-generation"
	stale.ReplacementText = "stale"
	if _, err := repo.ForkFrameUserMessageBranch(context.Background(), stale); !errors.Is(err, ErrBranchStateStale) || errors.Is(err, ErrSchemaUnavailable) {
		t.Fatalf("stale generation error=%v", err)
	}
	fromInactive := ForkFrameUserMessageBranchInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		SourceBranchID: base.ActiveBranchID, ExpectedActiveBranchID: first.BranchID, ExpectedGeneration: 2,
		ClientMutationID: "fork-inactive-base", SourceClientMessageID: source.ClientMessageID,
		SourceMessageIndex: 0,
		ReplacementText:    "alternate correction", Destinations: []string{"ws"},
	}
	alternate, err := repo.ForkFrameUserMessageBranch(context.Background(), fromInactive)
	if err != nil || !alternate.Created || alternate.ParentBranchID != base.ActiveBranchID || alternate.Generation != 3 {
		t.Fatalf("alternate=%#v err=%v", alternate, err)
	}
	var branchCount, sourceEventMemberships int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_branches WHERE stream_uid=?`, stream.UID).Scan(&branchCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_branch_events WHERE stream_uid=? AND event_id=?`, stream.UID, source.EventID).
		Scan(&sourceEventMemberships); err != nil {
		t.Fatal(err)
	}
	if branchCount != 3 || sourceEventMemberships != 1 {
		t.Fatalf("branches=%d source memberships=%d", branchCount, sourceEventMemberships)
	}
}

func TestForkFrameUserMessageBranchRollsBackEveryAuthorityOnDeliveryFailure(t *testing.T) {
	repo, db, stream, source, claim := newFrameBranchForkFixture(t)
	base, err := repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.ForkFrameUserMessageBranch(context.Background(), ForkFrameUserMessageBranchInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		SourceBranchID: base.ActiveBranchID, ExpectedActiveBranchID: base.ActiveBranchID, ExpectedGeneration: 1,
		ClientMutationID: "rollback-mutation", SourceClientMessageID: source.ClientMessageID,
		SourceMessageIndex: 0,
		ReplacementText:    "must roll back", Destinations: []string{"im:missing"},
	})
	if !errors.Is(err, ErrDeliveryRouteInactive) {
		t.Fatalf("fork error=%v", err)
	}
	state, err := repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || state != base {
		t.Fatalf("state=%#v base=%#v err=%v", state, base, err)
	}
	runtime, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID, claim.Attempt)
	if err != nil || runtime.Status != "running" {
		t.Fatalf("runner=%#v err=%v", runtime, err)
	}
	var branches, transcriptEvents, frameEvents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_branches WHERE stream_uid=?`, stream.UID).Scan(&branches); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=?`, stream.UID).Scan(&transcriptEvents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM frame_events WHERE frame_id=?`, stream.FrameID).Scan(&frameEvents); err != nil {
		t.Fatal(err)
	}
	if branches != 1 || transcriptEvents != 2 || frameEvents != 0 {
		t.Fatalf("rollback branches=%d transcript=%d frame=%d", branches, transcriptEvents, frameEvents)
	}
}

func TestForkFrameUserMessageBranchAllowsOnlyOneExpectedGenerationWinner(t *testing.T) {
	repo, db, stream, source, _ := newFrameBranchForkFixture(t)
	base, err := repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errorsByMutation := make(chan error, 2)
	var group sync.WaitGroup
	for _, mutation := range []string{"concurrent-a", "concurrent-b"} {
		mutation := mutation
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, err := repo.ForkFrameUserMessageBranch(context.Background(), ForkFrameUserMessageBranchInput{
				StreamUID: stream.UID, OwnerID: stream.OwnerID,
				SourceBranchID: base.ActiveBranchID, ExpectedActiveBranchID: base.ActiveBranchID, ExpectedGeneration: 1,
				ClientMutationID: mutation, SourceClientMessageID: source.ClientMessageID,
				SourceMessageIndex: 0,
				ReplacementText:    mutation, Destinations: []string{"ws"},
			})
			errorsByMutation <- err
		}()
	}
	close(start)
	group.Wait()
	close(errorsByMutation)
	winners, stale := 0, 0
	for err := range errorsByMutation {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrBranchStateStale):
			stale++
		default:
			t.Fatalf("unexpected concurrent error=%v", err)
		}
	}
	var branches int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_branches WHERE stream_uid=?`, stream.UID).Scan(&branches); err != nil {
		t.Fatal(err)
	}
	if winners != 1 || stale != 1 || branches != 2 {
		t.Fatalf("winners=%d stale=%d branches=%d", winners, stale, branches)
	}
}

func newFrameBranchForkFixture(t *testing.T) (*Repository, *sql.DB, Stream, Event, RunnerClaim) {
	t.Helper()
	repo, db, _ := newTranscriptRepository(t)
	if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES('project-branch','owner-branch')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO frames(id,project_id,root_frame_id,status) VALUES('frame-branch','project-branch','frame-branch','processing')`); err != nil {
		t.Fatal(err)
	}
	stream, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:frame-branch", OwnerID: "owner-branch", ExternalID: "frame-branch", SessionID: "frame-branch",
		Kind: StreamKindFrameRef, ProjectID: "project-branch", RootFrameID: "frame-branch", FrameID: "frame-branch", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	source, _, created, err := repo.AppendFrameUserEvent(context.Background(), AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "source-user",
		FrameEventID: "source-frame-event", MessageUUID: "source-message", Text: "run the original analysis",
		Destinations: []string{"ws"},
	})
	if err != nil || !created {
		t.Fatalf("source created=%t err=%v", created, err)
	}
	claim, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-old", TTL: time.Minute,
		ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	if _, created, err := repo.AppendRunnerEvent(context.Background(), AppendEventInput{
		Claim: claim.Claim, ClientMessageID: "assistant-old", Type: "assistant_message", Source: EventSourcePayload,
		PayloadJSON: []byte(`{"text":"old result"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("assistant created=%t err=%v", created, err)
	}
	return repo, db, stream, source, claim.Claim
}
