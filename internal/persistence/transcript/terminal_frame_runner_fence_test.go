package transcript

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

type terminalFrameRunnerFixture struct {
	repository *Repository
	db         *sql.DB
	claim      RunnerClaim
	now        time.Time
}

func newTerminalFrameRunnerFixture(t *testing.T) terminalFrameRunnerFixture {
	t.Helper()
	repository, db, _ := newTranscriptRepository(t)
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	repository.now = func() time.Time { return now }
	if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES('project-terminal-fence','owner-a')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO frames(id,project_id,root_frame_id,status)
		VALUES('frame-terminal-fence','project-terminal-fence','frame-terminal-fence','processing')`); err != nil {
		t.Fatal(err)
	}
	stream, err := repository.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:frame-terminal-fence", OwnerID: "owner-a", ExternalID: "frame-terminal-fence",
		SessionID: "frame-terminal-fence", Kind: StreamKindFrameRef, ProjectID: "project-terminal-fence",
		RootFrameID: "frame-terminal-fence", FrameID: "frame-terminal-fence", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := repository.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "user-terminal-fence",
		PayloadJSON: []byte(`{"text":"long-running work"}`),
	}); err != nil || !created {
		t.Fatalf("append user created=%t err=%v", created, err)
	}
	claimed, err := repository.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-terminal-fence",
		TTL: time.Minute, ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	return terminalFrameRunnerFixture{repository: repository, db: db, claim: claimed.Claim, now: now}
}

func TestTerminalFrameAuthorityFencesLiveClaimAndAllReclaimSources(t *testing.T) {
	fixture := newTerminalFrameRunnerFixture(t)
	checkpoint, _, created, err := fixture.repository.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: fixture.claim, ClientMessageID: "checkpoint-before-terminal", Phase: RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: []byte(`{"completed_steps":1}`),
	})
	if err != nil || !created {
		t.Fatalf("checkpoint=%#v created=%t err=%v", checkpoint, created, err)
	}
	if _, err := fixture.db.Exec(`UPDATE frames SET status='failed' WHERE id='frame-terminal-fence'`); err != nil {
		t.Fatal(err)
	}

	heartbeat, err := fixture.repository.HeartbeatRunner(context.Background(), HeartbeatRunnerInput{
		Claim: fixture.claim, TTL: time.Minute,
	})
	if !errors.Is(err, ErrClaimStale) || heartbeat.Renewed {
		t.Fatalf("terminal heartbeat=%#v err=%v want=%v", heartbeat, err, ErrClaimStale)
	}
	if _, _, err := fixture.repository.AppendRunnerEvent(context.Background(), AppendEventInput{
		Claim: fixture.claim, ClientMessageID: "late-assistant", Type: "assistant_message",
		Source: EventSourcePayload, PayloadJSON: []byte(`{"text":"late"}`),
	}); !errors.Is(err, ErrClaimStale) {
		t.Fatalf("late append err=%v want=%v", err, ErrClaimStale)
	}
	if _, _, _, err := fixture.repository.FinishRunner(context.Background(), FinishRunnerInput{
		Claim: fixture.claim, ClientMessageID: "late-finish", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`),
	}); !errors.Is(err, ErrClaimStale) {
		t.Fatalf("late finish err=%v want=%v", err, ErrClaimStale)
	}

	fixture.repository.now = func() time.Time { return fixture.now.Add(2 * time.Minute) }
	for _, input := range []ClaimRunnerInput{
		{
			StreamUID: fixture.claim.StreamUID, OwnerID: fixture.claim.OwnerID, RunnerID: "retry-runner",
			TTL: time.Minute, ResumeSource: ResumeSourceRetry,
		},
		{
			StreamUID: fixture.claim.StreamUID, OwnerID: fixture.claim.OwnerID, RunnerID: "checkpoint-runner",
			TTL: time.Minute, ResumeSource: ResumeSourceCheckpoint, ResumeCheckpoint: checkpoint.Sequence,
		},
	} {
		claimed, err := fixture.repository.ClaimRunner(context.Background(), input)
		if err != nil || claimed.Claimed {
			t.Fatalf("terminal reclaim input=%#v result=%#v err=%v", input, claimed, err)
		}
	}
	if next, err := fixture.repository.ClaimNextRunner(context.Background(), ClaimNextRunnerInput{
		RunnerID: "next-runner", TTL: time.Minute,
	}); err != nil || next.Claimed {
		t.Fatalf("terminal next claim=%#v err=%v", next, err)
	}

	var attempts, running, reclaims, terminals, receipts, lateTerminals int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM transcript_runner_attempts WHERE stream_uid=?`, fixture.claim.StreamUID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM transcript_runner_attempts WHERE stream_uid=? AND status='running'`, fixture.claim.StreamUID).Scan(&running); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='runner_reclaimed'`, fixture.claim.StreamUID).Scan(&reclaims); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='runner_finished'`, fixture.claim.StreamUID).Scan(&terminals); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM transcript_runner_receipts WHERE stream_uid=?`, fixture.claim.StreamUID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND client_message_id='late-finish'`, fixture.claim.StreamUID).Scan(&lateTerminals); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || running != 0 || reclaims != 0 || terminals != 1 || receipts != 1 || lateTerminals != 0 {
		t.Fatalf(
			"attempts=%d running=%d reclaims=%d terminals=%d receipts=%d late_terminals=%d",
			attempts, running, reclaims, terminals, receipts, lateTerminals,
		)
	}
	var frameStatus string
	if err := fixture.db.QueryRow(`SELECT status FROM frames WHERE id='frame-terminal-fence'`).Scan(&frameStatus); err != nil || frameStatus != "failed" {
		t.Fatalf("frame status=%q err=%v", frameStatus, err)
	}
}

func TestTerminalFrameAuthorityReconcilesExpiredAttemptBeforeNextClaim(t *testing.T) {
	fixture := newTerminalFrameRunnerFixture(t)
	if _, err := fixture.db.Exec(`UPDATE frames SET status='failed' WHERE id='frame-terminal-fence'`); err != nil {
		t.Fatal(err)
	}
	fixture.repository.now = func() time.Time { return fixture.now.Add(2 * time.Minute) }
	settled, err := fixture.repository.ReconcileTerminalFrameRunnerAttempts(context.Background(), 16)
	if err != nil || settled != 1 {
		t.Fatalf("settled=%d err=%v", settled, err)
	}
	settled, err = fixture.repository.ReconcileTerminalFrameRunnerAttempts(context.Background(), 16)
	if err != nil || settled != 0 {
		t.Fatalf("idempotent settled=%d err=%v", settled, err)
	}
	var attemptStatus, receiptStatus string
	var terminals, receipts int
	if err := fixture.db.QueryRow(`
		SELECT status FROM transcript_runner_attempts WHERE stream_uid=? AND attempt=?`,
		fixture.claim.StreamUID, fixture.claim.Attempt,
	).Scan(&attemptStatus); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`
		SELECT status FROM transcript_runner_receipts WHERE stream_uid=? AND attempt=?`,
		fixture.claim.StreamUID, fixture.claim.Attempt,
	).Scan(&receiptStatus); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='runner_finished'`, fixture.claim.StreamUID).Scan(&terminals); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM transcript_runner_receipts WHERE stream_uid=?`, fixture.claim.StreamUID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if attemptStatus != "failed" || receiptStatus != "failed" || terminals != 1 || receipts != 1 {
		t.Fatalf(
			"attempt=%q receipt=%q terminals=%d receipts=%d",
			attemptStatus, receiptStatus, terminals, receipts,
		)
	}
	if next, err := fixture.repository.ClaimNextRunner(context.Background(), ClaimNextRunnerInput{
		RunnerID: "replacement-runner", TTL: time.Minute,
	}); err != nil || next.Claimed {
		t.Fatalf("terminal frame was reclaimed=%#v err=%v", next, err)
	}
}

func TestCanonicalTerminalReceiptRemainsSingleAndCannotBeRetried(t *testing.T) {
	fixture := newTerminalFrameRunnerFixture(t)
	input := CancelRunnerInput{
		StreamUID: fixture.claim.StreamUID, OwnerID: fixture.claim.OwnerID,
		ExpectedAttempt: fixture.claim.Attempt, ClientMessageID: "cancel-once", ReasonCode: "user_cancelled",
	}
	first, err := fixture.repository.CancelRunner(context.Background(), input)
	if err != nil || !first.Applied || first.CurrentStatus != "cancelled" {
		t.Fatalf("first cancellation=%#v err=%v", first, err)
	}
	again, err := fixture.repository.CancelRunner(context.Background(), input)
	if err != nil || again.Applied || again.Event.EventID != first.Event.EventID || again.Receipt != first.Receipt {
		t.Fatalf("idempotent cancellation=%#v err=%v first=%#v", again, err, first)
	}
	retry, err := fixture.repository.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: fixture.claim.StreamUID, OwnerID: fixture.claim.OwnerID, RunnerID: "retry-after-cancel",
		TTL: time.Minute, ResumeSource: ResumeSourceRetry,
	})
	if err != nil || retry.Claimed {
		t.Fatalf("retry after terminal=%#v err=%v", retry, err)
	}
	var terminals, receipts int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='runner_finished'`, fixture.claim.StreamUID).Scan(&terminals); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM transcript_runner_receipts WHERE stream_uid=?`, fixture.claim.StreamUID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if terminals != 1 || receipts != 1 {
		t.Fatalf("terminals=%d receipts=%d", terminals, receipts)
	}
}
