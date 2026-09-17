package transcript

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestAppendFrameUserEventToBranchSwitchesAtomicallyAndRetriesExactlyOnce(t *testing.T) {
	repo, db, stream, baseBranchID, activeBranchID, activeClaim := newBranchContinuationFixture(t)
	beforeInput, beforeConsumed := branchContinuationRevisions(t, db, stream.UID)
	input := AppendFrameUserEventToBranchInput{
		AppendFrameUserEventInput: AppendFrameUserEventInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID,
			ClientMessageID: "branch-continue:continue-base-1",
			FrameEventID:    "frame-branch-continue:" + stream.FrameID + ":" + baseBranchID + ":continue-base-1",
			MessageUUID:     "branch-continue:continue-base-1", Text: "continue the original branch",
			Destinations: []string{"ws"},
		},
		TargetBranchID: baseBranchID, ExpectedActiveBranchID: activeBranchID,
		ExpectedGeneration: 2, ClientMutationID: "continue-base-1",
	}
	result, err := repo.AppendFrameUserEventToBranch(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || !result.Switched || result.BranchGeneration != 3 ||
		!result.RunnerCancellation.Applied || result.Event.Type != "user_message" {
		t.Fatalf("continuation result=%#v", result)
	}
	state, err := repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || state.ActiveBranchID != baseBranchID || state.Generation != 3 {
		t.Fatalf("branch state=%#v err=%v", state, err)
	}
	if _, _, _, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
		Claim: activeClaim, ClientMessageID: "late-branch-finish", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`), Destinations: []string{"ws"},
	}); !errors.Is(err, ErrClaimStale) {
		t.Fatalf("late runner finish error=%v", err)
	}
	var targetMembership, oldMembership int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_branch_events WHERE stream_uid=? AND branch_id=? AND event_id=?`,
		stream.UID, baseBranchID, result.Event.EventID).Scan(&targetMembership); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_branch_events WHERE stream_uid=? AND branch_id=? AND event_id=?`,
		stream.UID, activeBranchID, result.Event.EventID).Scan(&oldMembership); err != nil {
		t.Fatal(err)
	}
	if targetMembership != 1 || oldMembership != 0 {
		t.Fatalf("memberships target=%d old=%d", targetMembership, oldMembership)
	}
	var frameStatus string
	if err := db.QueryRow(`SELECT status FROM frames WHERE id=?`, stream.FrameID).Scan(&frameStatus); err != nil {
		t.Fatal(err)
	}
	afterInput, afterConsumed := branchContinuationRevisions(t, db, stream.UID)
	if frameStatus != "processing" || afterInput != beforeInput+1 || afterConsumed != beforeInput || beforeConsumed > beforeInput {
		t.Fatalf("frame=%q revisions before=%d/%d after=%d/%d", frameStatus, beforeInput, beforeConsumed, afterInput, afterConsumed)
	}
	intent, found, err := repo.GetActiveFrameTaskIntent(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found || intent.SourceEventID != result.Event.ClientMessageID || intent.Text != input.Text {
		t.Fatalf("intent=%#v found=%t err=%v", intent, found, err)
	}
	var deliveryCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_delivery_intents WHERE stream_uid=? AND publication_seq=? AND destination='ws'`,
		stream.UID, result.Event.PublicationSeq).Scan(&deliveryCount); err != nil || deliveryCount != 1 {
		t.Fatalf("delivery count=%d err=%v", deliveryCount, err)
	}

	retry, err := repo.AppendFrameUserEventToBranch(context.Background(), input)
	if err != nil || retry.Created || retry.Switched || retry.Event.EventID != result.Event.EventID || retry.BranchGeneration != 3 {
		t.Fatalf("retry=%#v err=%v", retry, err)
	}
	retryInput, retryConsumed := branchContinuationRevisions(t, db, stream.UID)
	if retryInput != afterInput || retryConsumed != afterConsumed {
		t.Fatalf("retry revisions=%d/%d want=%d/%d", retryInput, retryConsumed, afterInput, afterConsumed)
	}

	conflict := input
	conflict.Text = "different retry payload"
	if _, err := repo.AppendFrameUserEventToBranch(context.Background(), conflict); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("payload conflict error=%v", err)
	}
	conflict = input
	conflict.TargetBranchID = activeBranchID
	if _, err := repo.AppendFrameUserEventToBranch(context.Background(), conflict); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("target conflict error=%v", err)
	}
}

func TestAppendFrameUserEventToBranchRejectsStaleAndRollsBackDeliveryFailure(t *testing.T) {
	repo, db, stream, baseBranchID, activeBranchID, activeClaim := newBranchContinuationFixture(t)
	stateBefore, err := repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	input := AppendFrameUserEventToBranchInput{
		AppendFrameUserEventInput: AppendFrameUserEventInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID,
			ClientMessageID: "branch-continue:rollback-1",
			FrameEventID:    "frame-branch-continue:" + stream.FrameID + ":" + baseBranchID + ":rollback-1",
			MessageUUID:     "branch-continue:rollback-1", Text: "must roll back",
			Destinations: []string{"ws"},
		},
		TargetBranchID: baseBranchID, ExpectedActiveBranchID: baseBranchID,
		ExpectedGeneration: 2, ClientMutationID: "rollback-1",
	}
	if _, err := repo.AppendFrameUserEventToBranch(context.Background(), input); !errors.Is(err, ErrBranchStateStale) {
		t.Fatalf("stale state error=%v", err)
	}
	assertBranchContinuationState(t, repo, db, stream, stateBefore, activeClaim, "processing")

	input.ExpectedActiveBranchID = activeBranchID
	input.Destinations = []string{"im:missing"}
	if _, err := repo.AppendFrameUserEventToBranch(context.Background(), input); !errors.Is(err, ErrDeliveryRouteInactive) {
		t.Fatalf("delivery rollback error=%v", err)
	}
	assertBranchContinuationState(t, repo, db, stream, stateBefore, activeClaim, "processing")
}

func TestAppendFrameUserEventToActiveBranchCancelsPriorRunnerAndRejectsPendingInput(t *testing.T) {
	repo, db, stream, _, activeBranchID, activeClaim := newBranchContinuationFixture(t)
	input := AppendFrameUserEventToBranchInput{
		AppendFrameUserEventInput: AppendFrameUserEventInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID,
			ClientMessageID: "branch-continue:continue-active-1",
			FrameEventID:    "frame-branch-continue:" + stream.FrameID + ":" + activeBranchID + ":continue-active-1",
			MessageUUID:     "branch-continue:continue-active-1", Text: "continue the current branch",
			Destinations: []string{"ws"},
		},
		TargetBranchID: activeBranchID, ExpectedActiveBranchID: activeBranchID,
		ExpectedGeneration: 2, ClientMutationID: "continue-active-1",
	}
	result, err := repo.AppendFrameUserEventToBranch(context.Background(), input)
	if err != nil || !result.Created || result.Switched || result.BranchGeneration != 3 ||
		!result.RunnerCancellation.Applied {
		t.Fatalf("active continuation=%#v err=%v", result, err)
	}
	if _, _, _, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
		Claim: activeClaim, ClientMessageID: "late-active-finish", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`), Destinations: []string{"ws"},
	}); !errors.Is(err, ErrClaimStale) {
		t.Fatalf("late active runner finish error=%v", err)
	}
	var origin string
	if err := db.QueryRow(`SELECT json_extract(payload_json,'$.messageOrigin') FROM transcript_events
		WHERE stream_uid=? AND event_id=?`, stream.UID, result.Event.EventID).
		Scan(&origin); err != nil || origin != "task_intent" {
		t.Fatalf("message origin=%q err=%v", origin, err)
	}

	repo, db, stream, _, activeBranchID, activeClaim = newBranchContinuationFixture(t)
	if _, err := db.Exec(`UPDATE frames SET status='awaiting_user_response' WHERE id=?`, stream.FrameID); err != nil {
		t.Fatal(err)
	}
	input = AppendFrameUserEventToBranchInput{
		AppendFrameUserEventInput: AppendFrameUserEventInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID,
			ClientMessageID: "branch-continue:pending-active-1",
			FrameEventID:    "frame-branch-continue:" + stream.FrameID + ":" + activeBranchID + ":pending-active-1",
			MessageUUID:     "branch-continue:pending-active-1", Text: "must use resolve input",
			Destinations: []string{"ws"},
		},
		TargetBranchID: activeBranchID, ExpectedActiveBranchID: activeBranchID,
		ExpectedGeneration: 2, ClientMutationID: "pending-active-1",
	}
	if _, err := repo.AppendFrameUserEventToBranch(context.Background(), input); !errors.Is(err, ErrBranchStateStale) {
		t.Fatalf("pending active continuation error=%v", err)
	}
	runtime, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID, activeClaim.Attempt)
	if err != nil || runtime.Status != "running" {
		t.Fatalf("pending runner=%#v err=%v", runtime, err)
	}
}

func TestAppendFrameUserEventToActiveBranchAllowsOnlyOneExpectedGenerationWinner(t *testing.T) {
	repo, db, stream, _, activeBranchID, _ := newBranchContinuationFixture(t)
	start := make(chan struct{})
	errorsByMutation := make(chan error, 2)
	var group sync.WaitGroup
	for _, mutation := range []string{"active-concurrent-a", "active-concurrent-b"} {
		mutation := mutation
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			messageID := "branch-continue:" + mutation
			_, err := repo.AppendFrameUserEventToBranch(context.Background(), AppendFrameUserEventToBranchInput{
				AppendFrameUserEventInput: AppendFrameUserEventInput{
					StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: messageID,
					FrameEventID: "frame-branch-continue:" + stream.FrameID + ":" + activeBranchID + ":" + mutation,
					MessageUUID:  messageID, Text: mutation, Destinations: []string{"ws"},
				},
				TargetBranchID: activeBranchID, ExpectedActiveBranchID: activeBranchID,
				ExpectedGeneration: 2, ClientMutationID: mutation,
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
			t.Fatalf("concurrent active continuation error=%v", err)
		}
	}
	state, err := repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || state.ActiveBranchID != activeBranchID || state.Generation != 3 {
		t.Fatalf("state=%#v err=%v", state, err)
	}
	var continuationEvents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND client_message_id LIKE 'branch-continue:active-concurrent-%'`,
		stream.UID).Scan(&continuationEvents); err != nil {
		t.Fatal(err)
	}
	if winners != 1 || stale != 1 || continuationEvents != 1 {
		t.Fatalf("winners=%d stale=%d continuation_events=%d", winners, stale, continuationEvents)
	}
}

func TestAppendFrameUserEventToBranchRejectsNonRootFrameStreamWithoutMutation(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	if _, err := db.Exec(`
		INSERT INTO projects(id,user_id) VALUES('project-nonroot','owner-nonroot');
		INSERT INTO frames(id,project_id,root_frame_id,status) VALUES
			('frame-root','project-nonroot','frame-root','completed'),
			('frame-child','project-nonroot','frame-root','completed')`); err != nil {
		t.Fatal(err)
	}
	stream, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:frame-child", OwnerID: "owner-nonroot", ExternalID: "frame-child", SessionID: "frame-child",
		Kind: StreamKindFrameRef, ProjectID: "project-nonroot", RootFrameID: "frame-root", FrameID: "frame-child", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	baseBranchID, _, _ := BaseBranchIdentity(stream.UID)
	_, err = repo.AppendFrameUserEventToBranch(context.Background(), AppendFrameUserEventToBranchInput{
		AppendFrameUserEventInput: AppendFrameUserEventInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID,
			ClientMessageID: "branch-continue:nonroot-1",
			FrameEventID:    "frame-branch-continue:" + stream.FrameID + ":" + baseBranchID + ":nonroot-1",
			MessageUUID:     "branch-continue:nonroot-1", Text: "must not mutate child stream", Destinations: []string{"ws"},
		},
		TargetBranchID: baseBranchID, ExpectedActiveBranchID: baseBranchID,
		ExpectedGeneration: 1, ClientMutationID: "nonroot-1",
	})
	if !errors.Is(err, ErrEventConflict) {
		t.Fatalf("nonroot continuation error=%v", err)
	}
	var events, frameEvents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=?`, stream.UID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM frame_events WHERE frame_id=?`, stream.FrameID).Scan(&frameEvents); err != nil {
		t.Fatal(err)
	}
	if events != 0 || frameEvents != 0 {
		t.Fatalf("nonroot mutation events=%d frame_events=%d", events, frameEvents)
	}
}

func newBranchContinuationFixture(t *testing.T) (*Repository, *sql.DB, Stream, string, string, RunnerClaim) {
	t.Helper()
	repo, db, stream, source, _ := newFrameBranchForkFixture(t)
	base, err := repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	forked, err := repo.ForkFrameUserMessageBranch(context.Background(), ForkFrameUserMessageBranchInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		SourceBranchID: base.ActiveBranchID, ExpectedActiveBranchID: base.ActiveBranchID,
		ExpectedGeneration: base.Generation, ClientMutationID: "continuation-fixture-fork",
		SourceClientMessageID: source.ClientMessageID, SourceMessageIndex: 0,
		ReplacementText: "alternate branch", Destinations: []string{"ws"},
	})
	if err != nil || !forked.Created || forked.Generation != 2 {
		t.Fatalf("fork=%#v err=%v", forked, err)
	}
	claim, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-active-branch",
		TTL: time.Minute, ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	return repo, db, stream, base.ActiveBranchID, forked.BranchID, claim.Claim
}

func branchContinuationRevisions(t *testing.T, db *sql.DB, streamUID string) (int64, int64) {
	t.Helper()
	var input, consumed int64
	if err := db.QueryRow(`SELECT input_revision,consumed_input_revision FROM transcript_streams WHERE stream_uid=?`, streamUID).
		Scan(&input, &consumed); err != nil {
		t.Fatal(err)
	}
	return input, consumed
}

func assertBranchContinuationState(
	t *testing.T,
	repo *Repository,
	db *sql.DB,
	stream Stream,
	want BranchState,
	claim RunnerClaim,
	wantFrameStatus string,
) {
	t.Helper()
	got, err := repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || got != want {
		t.Fatalf("branch state=%#v want=%#v err=%v", got, want, err)
	}
	runtime, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID, claim.Attempt)
	if err != nil || runtime.Status != "running" {
		t.Fatalf("runner=%#v err=%v", runtime, err)
	}
	var frameStatus string
	var clientEvents int
	if err := db.QueryRow(`SELECT status FROM frames WHERE id=?`, stream.FrameID).Scan(&frameStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND client_message_id='branch-continue:rollback-1'`,
		stream.UID).Scan(&clientEvents); err != nil {
		t.Fatal(err)
	}
	if frameStatus != wantFrameStatus || clientEvents != 0 {
		t.Fatalf("frame=%q events=%d", frameStatus, clientEvents)
	}
}
