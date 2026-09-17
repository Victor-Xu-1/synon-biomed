package transcript

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"
)

func TestLatestFrameStreamUsesAuthorityPointerInsteadOfHighestEpoch(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	active, _ := seedAskUserHistoryClassificationFrame(t, repo, db, "latest-authority", false)
	inactive, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "inactive-higher-epoch", OwnerID: active.OwnerID, ExternalID: active.ExternalID,
		SessionID: active.SessionID, Kind: StreamKindFrameRef, ProjectID: active.ProjectID,
		RootFrameID: active.RootFrameID, FrameID: active.FrameID, Epoch: active.Epoch + 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	var selected Stream
	if err := repo.RunImmediate(context.Background(), func(tx *ImmediateTransaction) error {
		var err error
		selected, err = latestFrameStreamConn(context.Background(), tx.conn, active.OwnerID, active.FrameID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if selected.UID != active.UID || selected.Epoch != active.Epoch || selected.UID == inactive.UID {
		t.Fatalf("selected=%#v active=%#v inactive=%#v", selected, active, inactive)
	}
}

func TestRepositoryRunnerRuntimeCheckpointAndResumeAuthority(t *testing.T) {
	repo, _, _ := newTranscriptRepository(t)
	now := time.Date(2026, 7, 22, 19, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return now }
	seedTranscriptInput(t, repo, "stream-runtime", "owner-a")
	if _, _, err := repo.ActivateDeliveryRoute(context.Background(), "owner-a", "stream-runtime", "im:route-a"); err != nil {
		t.Fatal(err)
	}
	first, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: "stream-runtime", OwnerID: "owner-a", RunnerID: "runner-a", TTL: time.Minute,
		ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !first.Claimed || first.Claim.ResumeSource != ResumeSourceFresh {
		t.Fatalf("first claim=%#v err=%v", first, err)
	}
	planning, _, created, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: first.Claim, ClientMessageID: "checkpoint-planning", Phase: RunnerPhasePlanning,
		PayloadJSON: []byte(`{"summary":"plan ready"}`), Destinations: []string{"ws"},
	})
	if err != nil || !created || planning.Sequence != 1 || planning.Resumable {
		t.Fatalf("planning=%#v created=%t err=%v", planning, created, err)
	}
	executingInput := AppendRunnerCheckpointInput{
		Claim: first.Claim, ClientMessageID: "checkpoint-executing", Phase: RunnerPhaseExecuting, Resumable: true,
		PayloadJSON: []byte(`{"completed_steps":4}`), Destinations: []string{"ws", "im:route-a"},
	}
	executing, executingEvent, created, err := repo.AppendRunnerCheckpoint(context.Background(), executingInput)
	if err != nil || !created || executing.Sequence != 2 || executing.EventID != executingEvent.EventID || !executing.Resumable {
		t.Fatalf("executing=%#v event=%#v created=%t err=%v", executing, executingEvent, created, err)
	}
	again, againEvent, created, err := repo.AppendRunnerCheckpoint(context.Background(), executingInput)
	if err != nil || created || again != executing || againEvent.EventID != executingEvent.EventID {
		t.Fatalf("idempotent checkpoint=%#v event=%#v created=%t err=%v", again, againEvent, created, err)
	}
	conflict := executingInput
	conflict.PayloadJSON = []byte(`{"completed_steps":5}`)
	if _, _, created, err := repo.AppendRunnerCheckpoint(context.Background(), conflict); !errors.Is(err, ErrEventConflict) || created {
		t.Fatalf("conflicting checkpoint created=%t err=%v", created, err)
	}
	tampered := executingInput
	tampered.ClientMessageID = "checkpoint-tampered"
	tampered.Claim.ResumeSource = ResumeSourceRetry
	if _, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), tampered); !errors.Is(err, ErrClaimStale) {
		t.Fatalf("tampered claim error=%v", err)
	}
	state, err := repo.GetRunnerRuntimeState(context.Background(), first.Claim.StreamUID, first.Claim.OwnerID, first.Claim.Attempt)
	if err != nil || state.Status != "running" || state.Phase != RunnerPhaseExecuting ||
		state.PhaseSequence != 3 || state.LastCheckpointSequence != 2 || state.ResumeSource != ResumeSourceFresh ||
		state.FinishedEventID != 0 {
		t.Fatalf("running state=%#v err=%v", state, err)
	}
	finished, _, created, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
		Claim: first.Claim, ClientMessageID: "finish-1", Status: "completed",
		PayloadJSON: []byte(`{"summary":"done"}`), Destinations: []string{"ws"},
	})
	if err != nil || !created {
		t.Fatalf("finish created=%t err=%v", created, err)
	}
	terminal, err := repo.GetRunnerRuntimeState(context.Background(), first.Claim.StreamUID, first.Claim.OwnerID, first.Claim.Attempt)
	if err != nil || terminal.Status != "completed" || terminal.Phase != RunnerPhaseTerminal ||
		terminal.PhaseSequence != 4 || terminal.FinishedEventID != finished.EventID || terminal.FinishedAt == nil {
		t.Fatalf("terminal state=%#v err=%v", terminal, err)
	}
	latest, found, err := repo.GetLatestRunnerRuntimeState(context.Background(), first.Claim.StreamUID, first.Claim.OwnerID)
	if err != nil || !found || latest.FinishedEventID != finished.EventID {
		t.Fatalf("latest state=%#v found=%t err=%v", latest, found, err)
	}
	if _, created, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: first.Claim.StreamUID, OwnerID: first.Claim.OwnerID, ClientMessageID: "user-2",
		PayloadJSON: []byte(`{"text":"continue"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("new input created=%t err=%v", created, err)
	}
	if _, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: first.Claim.StreamUID, OwnerID: first.Claim.OwnerID, RunnerID: "runner-b", TTL: time.Minute,
		ResumeSource: ResumeSourceCheckpoint, ResumeCheckpoint: planning.Sequence,
	}); !errors.Is(err, ErrCheckpointUnavailable) {
		t.Fatalf("non-resumable checkpoint error=%v", err)
	}
	if _, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: first.Claim.StreamUID, OwnerID: first.Claim.OwnerID, RunnerID: "runner-b", TTL: time.Minute,
		ResumeSource: ResumeSourceCheckpoint, ResumeCheckpoint: executing.Sequence,
	}); !errors.Is(err, ErrCheckpointUnavailable) {
		t.Fatalf("prior-task checkpoint error=%v", err)
	}
	second, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: first.Claim.StreamUID, OwnerID: first.Claim.OwnerID, RunnerID: "runner-b", TTL: time.Minute,
		ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !second.Claimed || second.Claim.Attempt != first.Claim.Attempt+1 ||
		second.Claim.ResumeSource != ResumeSourceFresh || second.Claim.ClaimedInputRevision <= first.Claim.ClaimedInputRevision {
		t.Fatalf("new task claim=%#v err=%v", second, err)
	}
}

func TestLatestRunnerTimingSummaryCountsActiveWorkForCurrentInputRevision(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	now := time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return now }
	seedTranscriptInput(t, repo, "stream-runtime-timing", "owner-a")

	first, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: "stream-runtime-timing", OwnerID: "owner-a", RunnerID: "runner-a", TTL: time.Hour,
		ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !first.Claimed {
		t.Fatalf("first claim=%#v err=%v", first, err)
	}
	now = now.Add(2 * time.Minute)
	if _, _, created, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
		Claim: first.Claim, ClientMessageID: "timing-failed", Status: "failed",
		PayloadJSON: []byte(`{"error":"recoverable"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("finish first created=%t err=%v", created, err)
	}

	now = time.Date(2026, 8, 10, 8, 10, 0, 0, time.UTC)
	secondAttempt := first.Claim.Attempt + 1
	secondToken := "timing-retry-token"
	secondDigest := sha256.Sum256([]byte(secondToken))
	if _, err := db.Exec(`
		INSERT INTO transcript_runner_attempts(
			stream_uid,attempt,runner_id,claim_token_sha256,claimed_input_revision,
			resume_source,resume_checkpoint_sequence,status,phase,phase_sequence,
			last_checkpoint_sequence,claimed_at,expires_at
		) VALUES(?,?,?,?,?,'retry',0,'running','claimed',1,0,?,?)`,
		first.Claim.StreamUID, secondAttempt, "runner-b", secondDigest[:], first.Claim.ClaimedInputRevision, now, now.Add(time.Hour),
	); err != nil {
		t.Fatal(err)
	}
	secondClaim := RunnerClaim{
		StreamUID: first.Claim.StreamUID, OwnerID: first.Claim.OwnerID, RunnerID: "runner-b",
		Attempt: secondAttempt, ClaimToken: secondToken, ClaimedInputRevision: first.Claim.ClaimedInputRevision,
		ResumeSource: ResumeSourceRetry, ClaimedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	now = now.Add(3 * time.Minute)
	running, found, err := repo.GetLatestRunnerTimingSummary(
		context.Background(), first.Claim.StreamUID, first.Claim.OwnerID,
	)
	if err != nil || !found || !running.Active || running.Attempt != secondAttempt ||
		running.InputRevision != first.Claim.ClaimedInputRevision || !running.StartedAt.Equal(first.Claim.ClaimedAt) ||
		running.Elapsed != 5*time.Minute {
		t.Fatalf("running timing=%#v found=%t err=%v", running, found, err)
	}

	now = now.Add(time.Minute)
	if _, _, created, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
		Claim: secondClaim, ClientMessageID: "timing-completed", Status: "completed",
		PayloadJSON: []byte(`{"summary":"done"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("finish second created=%t err=%v", created, err)
	}
	terminal, found, err := repo.GetLatestRunnerTimingSummary(
		context.Background(), first.Claim.StreamUID, first.Claim.OwnerID,
	)
	if err != nil || !found || terminal.Active || terminal.Elapsed != 6*time.Minute || terminal.FinishedAt == nil ||
		!terminal.FinishedAt.Equal(now) {
		t.Fatalf("terminal timing=%#v found=%t err=%v", terminal, found, err)
	}

	now = time.Date(2026, 8, 10, 8, 20, 0, 0, time.UTC)
	if _, created, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: first.Claim.StreamUID, OwnerID: first.Claim.OwnerID, ClientMessageID: "timing-user-2",
		PayloadJSON: []byte(`{"text":"new task"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append next input created=%t err=%v", created, err)
	}
	third, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: first.Claim.StreamUID, OwnerID: first.Claim.OwnerID, RunnerID: "runner-c", TTL: time.Hour,
		ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !third.Claimed || third.Claim.ClaimedInputRevision <= first.Claim.ClaimedInputRevision {
		t.Fatalf("third claim=%#v err=%v", third, err)
	}
	now = now.Add(time.Minute)
	reset, found, err := repo.GetLatestRunnerTimingSummary(
		context.Background(), first.Claim.StreamUID, first.Claim.OwnerID,
	)
	if err != nil || !found || !reset.Active || reset.InputRevision != third.Claim.ClaimedInputRevision ||
		!reset.StartedAt.Equal(third.Claim.ClaimedAt) || reset.Elapsed != time.Minute {
		t.Fatalf("reset timing=%#v found=%t err=%v", reset, found, err)
	}
	taskTiming, found, err := repo.GetRunnerTaskTimingSummary(
		context.Background(), first.Claim.StreamUID, first.Claim.OwnerID,
	)
	if err != nil || !found || !taskTiming.Active || taskTiming.Attempt != third.Claim.Attempt ||
		taskTiming.InputRevision != third.Claim.ClaimedInputRevision ||
		!taskTiming.StartedAt.Equal(first.Claim.ClaimedAt) || taskTiming.FinishedAt != nil ||
		taskTiming.Elapsed != 7*time.Minute {
		t.Fatalf("task timing=%#v found=%t err=%v", taskTiming, found, err)
	}
}

func TestRunnerTimingSummariesFreezeWhenLatestLeaseExpires(t *testing.T) {
	repo, _, _ := newTranscriptRepository(t)
	now := time.Date(2026, 8, 26, 1, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return now }
	seedTranscriptInput(t, repo, "stream-expired-timing", "owner-a")

	claim, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: "stream-expired-timing", OwnerID: "owner-a", RunnerID: "runner-expired",
		TTL: time.Minute, ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	now = now.Add(3 * time.Minute)

	latest, found, err := repo.GetLatestRunnerTimingSummary(
		context.Background(), claim.Claim.StreamUID, claim.Claim.OwnerID,
	)
	if err != nil || !found || latest.Active || latest.Elapsed != time.Minute || latest.FinishedAt != nil {
		t.Fatalf("expired latest timing=%#v found=%t err=%v", latest, found, err)
	}
	task, found, err := repo.GetRunnerTaskTimingSummary(
		context.Background(), claim.Claim.StreamUID, claim.Claim.OwnerID,
	)
	if err != nil || !found || task.Active || task.Elapsed != time.Minute || task.FinishedAt != nil {
		t.Fatalf("expired task timing=%#v found=%t err=%v", task, found, err)
	}
}

func TestRepositoryRunnerReviewCursorSurvivesLeaseRotationWithoutAttemptInflation(t *testing.T) {
	repo, _, _ := newTranscriptRepository(t)
	seedTranscriptInput(t, repo, "stream-review-cursor", "owner-a")
	claimed, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: "stream-review-cursor", OwnerID: "owner-a", RunnerID: "runner-a", TTL: time.Minute,
		ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	appendReview := func(clientID string, index int, status string) {
		t.Helper()
		payload, err := json.Marshal(map[string]any{
			"reviewIndex": index, "status": status, "toolPhase": "verification",
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, created, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
			Claim: claimed.Claim, ClientMessageID: clientID, Phase: RunnerPhaseExecuting,
			Resumable: true, PayloadJSON: payload,
		}); err != nil || !created {
			t.Fatalf("append %s created=%t err=%v", clientID, created, err)
		}
	}
	if next, err := repo.NextRunnerReviewIndex(context.Background(), claimed.Claim.StreamUID, claimed.Claim.OwnerID, claimed.Claim.Attempt); err != nil || next != 0 {
		t.Fatalf("empty cursor=%d err=%v", next, err)
	}
	appendReview("review-0-running", 0, "running")
	if next, err := repo.NextRunnerReviewIndex(context.Background(), claimed.Claim.StreamUID, claimed.Claim.OwnerID, claimed.Claim.Attempt); err != nil || next != 0 {
		t.Fatalf("running cursor=%d err=%v", next, err)
	}
	appendReview("review-0-completed", 0, "completed")
	if next, err := repo.NextRunnerReviewIndex(context.Background(), claimed.Claim.StreamUID, claimed.Claim.OwnerID, claimed.Claim.Attempt); err != nil || next != 1 {
		t.Fatalf("completed cursor=%d err=%v", next, err)
	}
	appendReview("review-1-running", 1, "running")
	if next, err := repo.NextRunnerReviewIndex(context.Background(), claimed.Claim.StreamUID, claimed.Claim.OwnerID, claimed.Claim.Attempt); err != nil || next != 1 {
		t.Fatalf("next running cursor=%d err=%v", next, err)
	}
}

func TestRepositoryInterruptRunnerPreservesPendingInputAndResumesWithoutTerminalReceipt(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	now := time.Date(2026, 7, 31, 4, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return now }
	seedTranscriptInput(t, repo, "stream-runtime-drain", "owner-a")
	if _, _, err := repo.ActivateDeliveryRoute(context.Background(), "owner-a", "stream-runtime-drain", "im:route-a"); err != nil {
		t.Fatal(err)
	}
	first, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: "stream-runtime-drain", OwnerID: "owner-a", RunnerID: "runner-a", TTL: time.Hour,
		ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !first.Claimed {
		t.Fatalf("claim=%#v err=%v", first, err)
	}
	input := InterruptRunnerInput{
		Claim: first.Claim, ClientMessageID: "runtime-drain-attempt-1", ReasonCode: "runtime_draining",
		ResumeDetail: "artifact graph still has a dangling claim", AutoResume: true,
		Destinations: []string{"ws"},
	}
	interrupted, err := repo.InterruptRunner(context.Background(), input)
	if err != nil || !interrupted.Created || !interrupted.Checkpoint.Resumable ||
		interrupted.Checkpoint.Phase != RunnerPhasePlanning {
		t.Fatalf("interrupted=%#v err=%v", interrupted, err)
	}
	var interruptionPayload map[string]any
	if err := json.Unmarshal(interrupted.Event.PayloadJSON, &interruptionPayload); err != nil ||
		interruptionPayload["resume_detail"] != input.ResumeDetail {
		t.Fatalf("interruption payload=%#v err=%v", interruptionPayload, err)
	}
	again, err := repo.InterruptRunner(context.Background(), input)
	if err != nil || again.Created || again.Event.EventID != interrupted.Event.EventID {
		t.Fatalf("idempotent interruption=%#v err=%v", again, err)
	}
	state, err := repo.GetRunnerRuntimeState(context.Background(), first.Claim.StreamUID, first.Claim.OwnerID, first.Claim.Attempt)
	if err != nil || state.Status != "running" || state.FinishedEventID != 0 || state.FinishedAt != nil ||
		state.LastCheckpointSequence != interrupted.Checkpoint.Sequence || state.ExpiresAt.After(now) {
		t.Fatalf("interrupted state=%#v err=%v", state, err)
	}
	var inputRevision, consumedRevision, receipts, terminals int64
	if err := db.QueryRow(`SELECT input_revision,consumed_input_revision FROM transcript_streams WHERE stream_uid=?`,
		first.Claim.StreamUID).Scan(&inputRevision, &consumedRevision); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_runner_receipts WHERE stream_uid=?`, first.Claim.StreamUID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='runner_finished'`,
		first.Claim.StreamUID).Scan(&terminals); err != nil {
		t.Fatal(err)
	}
	if inputRevision != 1 || consumedRevision != 0 || receipts != 0 || terminals != 0 {
		t.Fatalf("input=%d consumed=%d receipts=%d terminals=%d", inputRevision, consumedRevision, receipts, terminals)
	}
	resumed, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: first.Claim.StreamUID, OwnerID: first.Claim.OwnerID, RunnerID: "runner-b", TTL: time.Hour,
		ResumeSource: ResumeSourceCheckpoint, ResumeCheckpoint: interrupted.Checkpoint.Sequence,
	})
	if err != nil || !resumed.Claimed || resumed.Claim.Attempt != first.Claim.Attempt ||
		resumed.Claim.ResumeSource != ResumeSourceCheckpoint || resumed.Claim.ResumeCheckpoint != interrupted.Checkpoint.Sequence {
		t.Fatalf("resumed=%#v err=%v", resumed, err)
	}
}

func TestRepositoryControlledInterruptionDoesNotRedispatchWithoutNewAuthorization(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	seedTranscriptInput(t, repo, "stream-controlled-interruption", "owner-a")
	claimed, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: "stream-controlled-interruption", OwnerID: "owner-a", RunnerID: "runner-a",
		TTL: time.Minute, ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	interrupted, err := repo.InterruptRunner(context.Background(), InterruptRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "provider-interrupted",
		ReasonCode: "provider_stream_interrupted", ResumeDetail: "await explicit user action",
	})
	if err != nil || !interrupted.Created || interrupted.Checkpoint.Resumable {
		t.Fatalf("interrupted=%#v err=%v", interrupted, err)
	}
	next, err := repo.ClaimNextRunner(context.Background(), ClaimNextRunnerInput{
		RunnerID: "runner-background", TTL: time.Minute,
	})
	if err != nil || next.Claimed {
		t.Fatalf("automatic redispatch=%#v err=%v", next, err)
	}
	if retry, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: claimed.Claim.StreamUID, OwnerID: claimed.Claim.OwnerID, RunnerID: "runner-direct",
		TTL: time.Minute, ResumeSource: ResumeSourceCheckpoint,
		ResumeCheckpoint: interrupted.Checkpoint.Sequence,
	}); !errors.Is(err, ErrCheckpointUnavailable) || retry.Claimed {
		t.Fatalf("direct redispatch=%#v err=%v", retry, err)
	}
	var attempts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_runner_attempts WHERE stream_uid=?`, claimed.Claim.StreamUID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("attempts=%d, want one", attempts)
	}
}

func TestRepositoryRunnerHeartbeatExtendsOnlyTheLiveClaimAcrossRestart(t *testing.T) {
	repo, db, dsn := newTranscriptRepository(t)
	now := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return now }
	seedTranscriptInput(t, repo, "stream-heartbeat", "owner-a")
	claimed, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: "stream-heartbeat", OwnerID: "owner-a", RunnerID: "runner-a", TTL: time.Minute,
		ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	checkpoint, _, created, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: claimed.Claim, ClientMessageID: "heartbeat-checkpoint", Phase: RunnerPhaseExecuting, Resumable: true,
		PayloadJSON: []byte(`{"completed_steps":1}`), Destinations: []string{"ws"},
	})
	if err != nil || !created {
		t.Fatalf("checkpoint=%#v created=%t err=%v", checkpoint, created, err)
	}
	now = now.Add(30 * time.Second)
	renewed, err := repo.HeartbeatRunner(context.Background(), HeartbeatRunnerInput{Claim: claimed.Claim, TTL: 2 * time.Minute})
	if err != nil || !renewed.Renewed || !renewed.ExpiresAt.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("heartbeat=%#v err=%v", renewed, err)
	}
	var storedExpiry time.Time
	if err := db.QueryRow(`SELECT expires_at FROM transcript_runner_attempts WHERE stream_uid=? AND attempt=?`,
		claimed.Claim.StreamUID, claimed.Claim.Attempt).Scan(&storedExpiry); err != nil || !storedExpiry.Equal(renewed.ExpiresAt) {
		t.Fatalf("stored expiry=%s err=%v", storedExpiry, err)
	}

	reopenedDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedDB.Close()
	reopened := NewRepository(reopenedDB)
	reopened.now = func() time.Time { return now }
	tampered := claimed.Claim
	tampered.ClaimToken = "not-the-claim-token"
	if result, err := reopened.HeartbeatRunner(context.Background(), HeartbeatRunnerInput{Claim: tampered, TTL: time.Minute}); !errors.Is(err, ErrClaimStale) || result.Renewed {
		t.Fatalf("tampered heartbeat=%#v err=%v", result, err)
	}
	now = renewed.ExpiresAt
	if result, err := reopened.HeartbeatRunner(context.Background(), HeartbeatRunnerInput{Claim: claimed.Claim, TTL: time.Minute}); !errors.Is(err, ErrClaimStale) || result.Renewed {
		t.Fatalf("expired heartbeat=%#v err=%v", result, err)
	}
	reclaimed, err := reopened.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: claimed.Claim.StreamUID, OwnerID: claimed.Claim.OwnerID, RunnerID: "runner-b", TTL: time.Minute,
		ResumeSource: ResumeSourceCheckpoint, ResumeCheckpoint: checkpoint.Sequence,
	})
	if err != nil || !reclaimed.Claimed || reclaimed.Claim.Attempt != claimed.Claim.Attempt ||
		reclaimed.Claim.ClaimToken == claimed.Claim.ClaimToken {
		t.Fatalf("reclaim=%#v err=%v", reclaimed, err)
	}
	if result, err := reopened.HeartbeatRunner(context.Background(), HeartbeatRunnerInput{Claim: claimed.Claim, TTL: time.Minute}); !errors.Is(err, ErrClaimStale) || result.Renewed {
		t.Fatalf("old attempt heartbeat=%#v err=%v", result, err)
	}
}

func TestRepositoryRunnerHasNoTaskLifetimeCapAcrossEightDaysAndProcessRestarts(t *testing.T) {
	repo, _, dsn := newTranscriptRepository(t)
	now := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return now }
	seedTranscriptInput(t, repo, "stream-eight-day-task", "owner-a")
	claimed, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: "stream-eight-day-task", OwnerID: "owner-a", RunnerID: "runner-a", TTL: 2 * time.Hour,
		ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	checkpoint, _, created, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: claimed.Claim, ClientMessageID: "checkpoint-before-long-run", Phase: RunnerPhaseExecuting, Resumable: true,
		PayloadJSON: []byte(`{"completed_shards":["shard-0001"]}`), Destinations: []string{"ws"},
	})
	if err != nil || !created {
		t.Fatalf("checkpoint=%#v created=%t err=%v", checkpoint, created, err)
	}

	// A task has no wall-clock lifetime limit. Short leases are renewed while a
	// worker is healthy, and each simulated daily process restart reconstructs
	// authority exclusively from the durable database.
	for day := 0; day < 8; day++ {
		reopenedDB, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatal(err)
		}
		reopened := NewRepository(reopenedDB)
		reopened.now = func() time.Time { return now }
		for hour := 0; hour < 24; hour++ {
			now = now.Add(time.Hour)
			renewed, err := reopened.HeartbeatRunner(context.Background(), HeartbeatRunnerInput{
				Claim: claimed.Claim, TTL: 2 * time.Hour,
			})
			if err != nil || !renewed.Renewed || !renewed.ExpiresAt.Equal(now.Add(2*time.Hour)) {
				_ = reopenedDB.Close()
				t.Fatalf("day=%d hour=%d heartbeat=%#v err=%v", day, hour, renewed, err)
			}
		}
		if err := reopenedDB.Close(); err != nil {
			t.Fatal(err)
		}
	}

	// If the worker disappears after more than a week, the finite lease expires
	// and a new worker resumes the persisted checkpoint instead of failing the
	// task because of elapsed wall-clock time.
	now = now.Add(3 * time.Hour)
	resumed, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: claimed.Claim.StreamUID, OwnerID: claimed.Claim.OwnerID, RunnerID: "runner-b", TTL: 2 * time.Hour,
		ResumeSource: ResumeSourceCheckpoint, ResumeCheckpoint: checkpoint.Sequence,
	})
	if err != nil || !resumed.Claimed || resumed.Claim.Attempt != claimed.Claim.Attempt ||
		resumed.Claim.ResumeSource != ResumeSourceCheckpoint || resumed.Claim.ResumeCheckpoint != checkpoint.Sequence {
		t.Fatalf("resumed=%#v err=%v", resumed, err)
	}
}

func TestRepositoryRunnerCheckpointRejectsStaleAndLateAttemptsAfterRestart(t *testing.T) {
	repo, _, dsn := newTranscriptRepository(t)
	now := time.Date(2026, 7, 22, 20, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return now }
	seedTranscriptInput(t, repo, "stream-stale-runtime", "owner-a")
	first, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: "stream-stale-runtime", OwnerID: "owner-a", RunnerID: "runner-a", TTL: time.Minute,
		ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !first.Claimed {
		t.Fatalf("first claim=%#v err=%v", first, err)
	}
	checkpoint, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: first.Claim, ClientMessageID: "checkpoint-1", Phase: RunnerPhaseExecuting, Resumable: true,
		PayloadJSON: []byte(`{"completed_steps":1}`), Destinations: []string{"ws"},
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	second, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: first.Claim.StreamUID, OwnerID: first.Claim.OwnerID, RunnerID: "runner-b", TTL: time.Minute,
		ResumeSource: ResumeSourceCheckpoint, ResumeCheckpoint: checkpoint.Sequence,
	})
	if err != nil || !second.Claimed || second.Claim.Attempt != first.Claim.Attempt {
		t.Fatalf("second claim=%#v err=%v", second, err)
	}
	if _, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: first.Claim, ClientMessageID: "stale-checkpoint", Phase: RunnerPhaseExecuting,
		PayloadJSON: []byte(`{"stale":true}`),
	}); !errors.Is(err, ErrClaimStale) {
		t.Fatalf("stale checkpoint error=%v", err)
	}
	if _, _, _, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
		Claim: second.Claim, ClientMessageID: "finish-2", Status: "cancelled",
		PayloadJSON: []byte(`{"reason":"user"}`), Destinations: []string{"ws"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: second.Claim, ClientMessageID: "late-checkpoint", Phase: RunnerPhaseExecuting,
		PayloadJSON: []byte(`{"late":true}`),
	}); !errors.Is(err, ErrClaimStale) {
		t.Fatalf("late checkpoint error=%v", err)
	}

	reopenedDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedDB.Close()
	reopened := NewRepository(reopenedDB)
	state, err := reopened.GetRunnerRuntimeState(context.Background(), second.Claim.StreamUID, second.Claim.OwnerID, second.Claim.Attempt)
	if err != nil || state.Status != "cancelled" || state.Phase != RunnerPhaseTerminal ||
		state.ResumeSource != ResumeSourceCheckpoint || state.ResumeCheckpoint != checkpoint.Sequence {
		t.Fatalf("reopened state=%#v err=%v", state, err)
	}
}

func TestRepositoryRunnerLeaseReclaimHasDurableTerminalReceipt(t *testing.T) {
	repo, db, dsn := newTranscriptRepository(t)
	now := time.Date(2026, 7, 22, 20, 30, 0, 0, time.UTC)
	repo.now = func() time.Time { return now }
	first := seedArtifactProjectionClaim(t, repo, db, "stream-reclaim-receipt", "owner-a")
	seedArtifactVersion(t, db, first.OwnerID, "project-a", "root-a", "frame-a", "artifact-reclaim", "version-reclaim")
	_, checkpoint, created, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: first, ClientMessageID: "reclaim-artifact-checkpoint", Phase: RunnerPhaseExecuting, Resumable: true,
		PayloadJSON: []byte(`{"tool":"artifact_register"}`), Destinations: []string{"ws"},
	})
	if err != nil || !created {
		t.Fatalf("artifact checkpoint=%#v created=%t err=%v", checkpoint, created, err)
	}
	if _, err := db.Exec(`INSERT INTO transcript_artifact_commits(
		stream_uid,runner_attempt,source_event_id,ordinal,artifact_id,version_id,relation,created_at
	) VALUES(?,?,?,?,?,?,?,?)`, first.StreamUID, first.Attempt, checkpoint.EventID, 0,
		"artifact-reclaim", "version-reclaim", "produced", now); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if _, created, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: first.StreamUID, OwnerID: first.OwnerID, ClientMessageID: "reclaim-new-task",
		PayloadJSON: []byte(`{"text":"new task"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("new task input created=%t err=%v", created, err)
	}
	second, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: first.StreamUID, OwnerID: first.OwnerID, RunnerID: "runner-b", TTL: time.Minute,
		ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !second.Claimed || second.Claim.Attempt != 2 {
		t.Fatalf("second claim=%#v err=%v", second, err)
	}
	state, err := repo.GetRunnerRuntimeState(context.Background(), first.StreamUID, first.OwnerID, first.Attempt)
	if err != nil || state.Status != "reclaimed" || state.Phase != RunnerPhaseTerminal || state.FinishedAt == nil {
		t.Fatalf("reclaimed state=%#v err=%v", state, err)
	}
	var eventType, receiptStatus string
	var terminalEventID, publicationSequence int64
	var payload []byte
	if err := db.QueryRow(`
		SELECT event.event_id,event.event_type,event.payload_json,event.publication_seq,receipt.status
		FROM transcript_runner_receipts receipt
		JOIN transcript_events event ON event.stream_uid=receipt.stream_uid AND event.event_id=receipt.event_id
		WHERE receipt.stream_uid=? AND receipt.attempt=?`, first.StreamUID, first.Attempt,
	).Scan(&terminalEventID, &eventType, &payload, &publicationSequence, &receiptStatus); err != nil {
		t.Fatal(err)
	}
	if eventType != "runner_reclaimed" || receiptStatus != "reclaimed" ||
		string(payload) != `{"status":"reclaimed","reason_code":"lease_expired"}` {
		t.Fatalf("eventType=%q receipt=%q payload=%s", eventType, receiptStatus, payload)
	}
	projected, err := repo.ListProjectedEvents(context.Background(), ListProjectedEventsInput{
		StreamUID: first.StreamUID, OwnerID: first.OwnerID, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range projected {
		if event.Event.Type == "runner_reclaimed" {
			found = string(event.ResolvedPayloadJSON) == string(payload)
		}
	}
	if !found {
		t.Fatalf("reclaim projection=%#v", projected)
	}
	assertProjectedArtifactRefs(t, projected, terminalEventID, []string{"version-reclaim"})
	var boundEventID sql.NullInt64
	if err := db.QueryRow(`SELECT bound_event_id FROM transcript_artifact_commits
		WHERE stream_uid=? AND runner_attempt=? AND version_id=?`, first.StreamUID, first.Attempt, "version-reclaim").Scan(&boundEventID); err != nil ||
		!boundEventID.Valid || boundEventID.Int64 != terminalEventID {
		t.Fatalf("reclaim bound event=%#v want=%d err=%v", boundEventID, terminalEventID, err)
	}
	intent, err := repo.GetDeliveryIntent(context.Background(), first.OwnerID, first.StreamUID, publicationSequence, "ws", 1)
	if err != nil || intent.Status != "pending" {
		t.Fatalf("reclaim delivery=%#v err=%v", intent, err)
	}
	reopenedDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedDB.Close()
	reopened, err := NewRepository(reopenedDB).GetRunnerRuntimeState(
		context.Background(), first.StreamUID, first.OwnerID, first.Attempt,
	)
	if err != nil || reopened.Status != "reclaimed" || reopened.FinishedAt == nil {
		t.Fatalf("reopened state=%#v err=%v", reopened, err)
	}
}

func TestRepositoryRunnerCheckpointSequenceIsDenseUnderConcurrency(t *testing.T) {
	repo, _, _ := newTranscriptRepository(t)
	seedTranscriptInput(t, repo, "stream-checkpoint-race", "owner-a")
	claimed, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: "stream-checkpoint-race", OwnerID: "owner-a", RunnerID: "runner-a", TTL: time.Minute,
		ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	const workers = 16
	start := make(chan struct{})
	sequences := make(chan int64, workers)
	errs := make(chan error, workers)
	var group sync.WaitGroup
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			checkpoint, _, created, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
				Claim: claimed.Claim, ClientMessageID: fmt.Sprintf("checkpoint-%02d", index), Phase: RunnerPhaseExecuting,
				Resumable: true, PayloadJSON: []byte(fmt.Sprintf(`{"worker":%d}`, index)), Destinations: []string{"ws"},
			})
			if err != nil || !created {
				errs <- fmt.Errorf("worker %d created=%t: %w", index, created, err)
				return
			}
			sequences <- checkpoint.Sequence
		}(index)
	}
	close(start)
	group.Wait()
	close(sequences)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	values := make([]int, 0, workers)
	for sequence := range sequences {
		values = append(values, int(sequence))
	}
	sort.Ints(values)
	for index, sequence := range values {
		if sequence != index+1 {
			t.Fatalf("checkpoint sequences=%v", values)
		}
	}
	state, err := repo.GetRunnerRuntimeState(context.Background(), claimed.Claim.StreamUID, claimed.Claim.OwnerID, claimed.Claim.Attempt)
	if err != nil || state.LastCheckpointSequence != workers || state.PhaseSequence != workers+1 {
		t.Fatalf("state=%#v err=%v", state, err)
	}
}

func TestRepositoryRunnerRequiresNewUserInputAfterEveryTerminalOutcome(t *testing.T) {
	for _, test := range []struct{ status string }{
		{status: "failed"},
		{status: "cancelled"},
		{status: "completed"},
	} {
		t.Run(test.status, func(t *testing.T) {
			repo, _, _ := newTranscriptRepository(t)
			streamUID := "stream-retry-" + test.status
			seedTranscriptInput(t, repo, streamUID, "owner-a")
			first, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
				StreamUID: streamUID, OwnerID: "owner-a", RunnerID: "runner-a", TTL: time.Minute,
				ResumeSource: ResumeSourceFresh,
			})
			if err != nil || !first.Claimed {
				t.Fatalf("first claim=%#v err=%v", first, err)
			}
			if _, _, created, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
				Claim: first.Claim, ClientMessageID: "finish-1", Status: test.status,
				PayloadJSON: []byte(`{"terminal":true}`), Destinations: []string{"ws"},
			}); err != nil || !created {
				t.Fatalf("finish created=%t err=%v", created, err)
			}
			stream, err := repo.GetStream(context.Background(), streamUID, "owner-a")
			if err != nil || stream.InputRevision != 1 || stream.ConsumedInputRevision != 1 {
				t.Fatalf("stream=%#v err=%v", stream, err)
			}
			retry, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
				StreamUID: streamUID, OwnerID: "owner-a", RunnerID: "runner-b", TTL: time.Minute,
				ResumeSource: ResumeSourceRetry,
			})
			if err != nil || retry.Claimed {
				t.Fatalf("implicit retry=%#v err=%v", retry, err)
			}
			if _, created, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
				StreamUID: streamUID, OwnerID: "owner-a", ClientMessageID: "user-explicit-second",
				PayloadJSON: []byte(`{"text":"explicit second task"}`), Destinations: []string{"ws"},
			}); err != nil || !created {
				t.Fatalf("append explicit second task created=%t err=%v", created, err)
			}
			second, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
				StreamUID: streamUID, OwnerID: "owner-a", RunnerID: "runner-b", TTL: time.Minute,
				ResumeSource: ResumeSourceFresh,
			})
			if err != nil || !second.Claimed || second.Claim.Attempt != 2 || second.Claim.ClaimedInputRevision != 2 {
				t.Fatalf("explicit second task=%#v err=%v", second, err)
			}
		})
	}
}

func TestRepositoryRunnerCheckpointCannotCrossInputRevision(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	seedTranscriptInput(t, repo, "stream-checkpoint-revision", "owner-a")
	first, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: "stream-checkpoint-revision", OwnerID: "owner-a", RunnerID: "runner-a", TTL: time.Minute,
		ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !first.Claimed {
		t.Fatalf("first claim=%#v err=%v", first, err)
	}
	checkpoint, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: first.Claim, ClientMessageID: "checkpoint-1", Phase: RunnerPhaseExecuting, Resumable: true,
		PayloadJSON: []byte(`{"completed_steps":1}`), Destinations: []string{"ws"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
		Claim: first.Claim, ClientMessageID: "finish-1", Status: "failed",
		PayloadJSON: []byte(`{"status":"failed"}`), Destinations: []string{"ws"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, created, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: first.Claim.StreamUID, OwnerID: first.Claim.OwnerID, ClientMessageID: "user-2",
		PayloadJSON: []byte(`{"text":"new requirements"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("user created=%t err=%v", created, err)
	}
	second, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: first.Claim.StreamUID, OwnerID: first.Claim.OwnerID, RunnerID: "runner-b", TTL: time.Minute,
		ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !second.Claimed || second.Claim.ClaimedInputRevision != 2 {
		t.Fatalf("second claim=%#v err=%v", second, err)
	}
	if _, _, _, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
		Claim: second.Claim, ClientMessageID: "finish-2", Status: "failed",
		PayloadJSON: []byte(`{"status":"failed"}`), Destinations: []string{"ws"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: first.Claim.StreamUID, OwnerID: first.Claim.OwnerID, RunnerID: "runner-c", TTL: time.Minute,
		ResumeSource: ResumeSourceCheckpoint, ResumeCheckpoint: checkpoint.Sequence,
	}); !errors.Is(err, ErrCheckpointUnavailable) {
		t.Fatalf("cross-revision checkpoint error=%v", err)
	}
	var attempts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_runner_attempts WHERE stream_uid=?`, first.Claim.StreamUID).Scan(&attempts); err != nil || attempts != 2 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
	retry, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: first.Claim.StreamUID, OwnerID: first.Claim.OwnerID, RunnerID: "runner-c", TTL: time.Minute,
		ResumeSource: ResumeSourceRetry,
	})
	if err != nil || retry.Claimed {
		t.Fatalf("implicit retry=%#v err=%v", retry, err)
	}
}

func TestRepositoryCancelRunnerIsDurableIdempotentAndFencesLateWork(t *testing.T) {
	repo, db, dsn := newTranscriptRepository(t)
	now := time.Date(2026, 7, 22, 21, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return now }
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-cancel", "owner-a")
	if _, _, err := repo.ActivateDeliveryRoute(context.Background(), "owner-a", "stream-cancel", "im:route-a"); err != nil {
		t.Fatal(err)
	}
	seedArtifactVersion(t, db, claim.OwnerID, "project-a", "root-a", "frame-a", "artifact-cancel", "version-cancel")
	_, checkpoint, created, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "cancel-artifact-checkpoint", Phase: RunnerPhaseExecuting, Resumable: true,
		PayloadJSON: []byte(`{"tool":"artifact_register"}`), Destinations: []string{"ws"},
	})
	if err != nil || !created {
		t.Fatalf("artifact checkpoint=%#v created=%t err=%v", checkpoint, created, err)
	}
	if _, err := db.Exec(`INSERT INTO transcript_artifact_commits(
		stream_uid,runner_attempt,source_event_id,ordinal,artifact_id,version_id,relation,created_at
	) VALUES(?,?,?,?,?,?,?,?)`, claim.StreamUID, claim.Attempt, checkpoint.EventID, 0,
		"artifact-cancel", "version-cancel", "produced", now); err != nil {
		t.Fatal(err)
	}
	input := CancelRunnerInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, ExpectedAttempt: claim.Attempt,
		ClientMessageID: "cancel-1", ReasonCode: "user_cancelled", Destinations: []string{"ws", "im:route-a"},
	}
	result, err := repo.CancelRunner(context.Background(), input)
	if err != nil || !result.Applied || result.CurrentStatus != "cancelled" ||
		result.Event.Type != "runner_finished" || result.Receipt.EventID != result.Event.EventID {
		t.Fatalf("cancel=%#v err=%v", result, err)
	}
	if string(result.Event.PayloadJSON) != `{"status":"cancelled","reason_code":"user_cancelled"}` {
		t.Fatalf("payload=%s", result.Event.PayloadJSON)
	}
	refs, err := repo.ListArtifactReferences(context.Background(), claim.StreamUID, claim.OwnerID, claim.Attempt)
	if err != nil || len(refs) != 1 || refs[0].SourceEventID != result.Event.EventID ||
		refs[0].ArtifactID != "artifact-cancel" || refs[0].VersionID != "version-cancel" {
		t.Fatalf("cancel artifact refs=%#v err=%v", refs, err)
	}
	var boundEventID sql.NullInt64
	if err := db.QueryRow(`SELECT bound_event_id FROM transcript_artifact_commits
		WHERE stream_uid=? AND runner_attempt=? AND version_id=?`, claim.StreamUID, claim.Attempt, "version-cancel").Scan(&boundEventID); err != nil ||
		!boundEventID.Valid || boundEventID.Int64 != result.Event.EventID {
		t.Fatalf("cancel bound event=%#v want=%d err=%v", boundEventID, result.Event.EventID, err)
	}
	again, err := repo.CancelRunner(context.Background(), input)
	if err != nil || again.Applied || again.Event.EventID != result.Event.EventID || again.Receipt != result.Receipt {
		t.Fatalf("idempotent cancel=%#v err=%v", again, err)
	}
	refs, err = repo.ListArtifactReferences(context.Background(), claim.StreamUID, claim.OwnerID, claim.Attempt)
	if err != nil || len(refs) != 1 || refs[0].SourceEventID != result.Event.EventID {
		t.Fatalf("idempotent cancel artifact refs=%#v err=%v", refs, err)
	}
	if _, _, err := repo.AppendRunnerEvent(context.Background(), AppendEventInput{
		Claim: claim, ClientMessageID: "late-event", Type: "content_delta", Source: EventSourcePayload,
		PayloadJSON: []byte(`{"text":"late"}`),
	}); !errors.Is(err, ErrClaimStale) {
		t.Fatalf("late event error=%v", err)
	}
	var events, receipts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='runner_finished'`, input.StreamUID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_runner_receipts WHERE stream_uid=?`, input.StreamUID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if events != 1 || receipts != 1 {
		t.Fatalf("terminal events=%d receipts=%d", events, receipts)
	}
	reopenedDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedDB.Close()
	state, err := NewRepository(reopenedDB).GetRunnerRuntimeState(context.Background(), input.StreamUID, input.OwnerID, input.ExpectedAttempt)
	if err != nil || state.Status != "cancelled" || state.Phase != RunnerPhaseTerminal || state.FinishedAt == nil {
		t.Fatalf("reopened state=%#v err=%v", state, err)
	}
}

func TestRepositoryCancelRunnerPreservesAbsorbingTerminalAndOwnerAuthority(t *testing.T) {
	for _, status := range []string{"completed", "failed"} {
		t.Run(status, func(t *testing.T) {
			repo, db, _ := newTranscriptRepository(t)
			streamUID := "stream-cancel-" + status
			seedTranscriptInput(t, repo, streamUID, "owner-a")
			claimed, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
				StreamUID: streamUID, OwnerID: "owner-a", RunnerID: "runner-a", TTL: time.Minute,
				ResumeSource: ResumeSourceFresh,
			})
			if err != nil || !claimed.Claimed {
				t.Fatalf("claim=%#v err=%v", claimed, err)
			}
			if _, _, _, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
				Claim: claimed.Claim, ClientMessageID: "finish-1", Status: status,
				PayloadJSON: []byte(`{"terminal":true}`), Destinations: []string{"ws"},
			}); err != nil {
				t.Fatal(err)
			}
			cancel := CancelRunnerInput{
				StreamUID: streamUID, OwnerID: "owner-a", ExpectedAttempt: 1,
				ClientMessageID: "cancel-1", ReasonCode: "user_cancelled", Destinations: []string{"ws"},
			}
			result, err := repo.CancelRunner(context.Background(), cancel)
			if err != nil || result.Applied || result.CurrentStatus != status {
				t.Fatalf("cancel=%#v err=%v", result, err)
			}
			if status == "failed" {
				if _, created, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
					StreamUID: streamUID, OwnerID: "owner-a", ClientMessageID: "failed-new-task",
					PayloadJSON: []byte(`{"text":"try again"}`), Destinations: []string{"ws"},
				}); err != nil || !created {
					t.Fatalf("new task input created=%t err=%v", created, err)
				}
				retry, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
					StreamUID: streamUID, OwnerID: "owner-a", RunnerID: "runner-b", TTL: time.Minute,
					ResumeSource: ResumeSourceFresh,
				})
				if err != nil || !retry.Claimed || retry.Claim.Attempt != 2 {
					t.Fatalf("retry=%#v err=%v", retry, err)
				}
				if _, err := repo.CancelRunner(context.Background(), cancel); !errors.Is(err, ErrClaimStale) {
					t.Fatalf("stale cancel error=%v", err)
				}
			}
			cancel.OwnerID = "owner-b"
			if _, err := repo.CancelRunner(context.Background(), cancel); !errors.Is(err, ErrOwnerMismatch) {
				t.Fatalf("foreign cancel error=%v", err)
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='runner_finished'`, streamUID).Scan(&count); err != nil || count != 1 {
				t.Fatalf("terminal count=%d err=%v", count, err)
			}
		})
	}
}

func TestRepositoryCancelAndFinishHaveOneTerminalWinner(t *testing.T) {
	for iteration := 0; iteration < 20; iteration++ {
		repo, db, _ := newTranscriptRepository(t)
		streamUID := fmt.Sprintf("stream-cancel-race-%02d", iteration)
		seedTranscriptInput(t, repo, streamUID, "owner-a")
		claimed, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
			StreamUID: streamUID, OwnerID: "owner-a", RunnerID: "runner-a", TTL: time.Minute,
			ResumeSource: ResumeSourceFresh,
		})
		if err != nil || !claimed.Claimed {
			t.Fatalf("claim=%#v err=%v", claimed, err)
		}
		start := make(chan struct{})
		errorsOut := make(chan error, 2)
		go func() {
			<-start
			_, _, _, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
				Claim: claimed.Claim, ClientMessageID: "finish", Status: "completed",
				PayloadJSON: []byte(`{"terminal":true}`), Destinations: []string{"ws"},
			})
			errorsOut <- err
		}()
		go func() {
			<-start
			_, err := repo.CancelRunner(context.Background(), CancelRunnerInput{
				StreamUID: streamUID, OwnerID: "owner-a", ExpectedAttempt: 1,
				ClientMessageID: "cancel", ReasonCode: "user_cancelled", Destinations: []string{"ws"},
			})
			errorsOut <- err
		}()
		close(start)
		for index := 0; index < 2; index++ {
			if err := <-errorsOut; err != nil && !errors.Is(err, ErrClaimStale) {
				t.Fatalf("iteration %d error=%v", iteration, err)
			}
		}
		var eventCount, receiptCount int
		var status string
		if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='runner_finished'`, streamUID).Scan(&eventCount); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_runner_receipts WHERE stream_uid=?`, streamUID).Scan(&receiptCount); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT status FROM transcript_runner_attempts WHERE stream_uid=? AND attempt=1`, streamUID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if eventCount != 1 || receiptCount != 1 || status != "completed" && status != "cancelled" {
			t.Fatalf("iteration %d events=%d receipts=%d status=%s", iteration, eventCount, receiptCount, status)
		}
	}
}
