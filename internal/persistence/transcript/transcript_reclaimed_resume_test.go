package transcript

import (
	"context"
	"testing"
	"time"
)

// TestRepositoryCheckpointResumeReusesReclaimedAttempt locks the provider
// continuation contract: a lease-expired attempt that was reconciled to
// "reclaimed" is still the same logical execution unit, so a checkpoint
// resume must revive that attempt instead of starting a new parent attempt.
func TestRepositoryCheckpointResumeReusesReclaimedAttempt(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	now := time.Date(2026, 7, 24, 1, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return now }

	if _, err := db.Exec(`
		INSERT INTO projects(id,user_id) VALUES('project-reclaimed','owner-a');
		INSERT INTO frames(id,project_id,root_frame_id,status) VALUES('frame-reclaimed','project-reclaimed','frame-reclaimed','processing');
		INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at)
		VALUES('resume-event-reclaimed','frame-reclaimed',1,'frame_resumed','{"dispatch":{"status":"claimed"}}','2026-07-24 01:00:00');`); err != nil {
		t.Fatal(err)
	}
	stream, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "stream-reclaimed", OwnerID: "owner-a", ExternalID: "frame-reclaimed",
		SessionID: "frame-reclaimed", Kind: StreamKindFrameRef, ProjectID: "project-reclaimed",
		RootFrameID: "frame-reclaimed", FrameID: "frame-reclaimed", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := repo.AppendFrameUserEvent(context.Background(), AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: "owner-a", ClientMessageID: "user-reclaimed",
		FrameEventID: "frame-event-reclaimed", MessageUUID: "message-reclaimed", Text: "resume reclaimed work",
		MessageOrigin: "task_intent", Destinations: []string{"ws"},
	}); err != nil {
		t.Fatal(err)
	}

	first, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: "owner-a", RunnerID: "runner-reclaimed",
		TTL: time.Minute, ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !first.Claimed || first.Claim.Attempt != 1 {
		t.Fatalf("first claim=%#v err=%v", first, err)
	}
	checkpoint, _, created, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: first.Claim, ClientMessageID: "checkpoint-reclaimed", Phase: RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: []byte(`{"status":"running","detail":"durable step"}`), Destinations: []string{"ws"},
	})
	if err != nil || !created {
		t.Fatalf("checkpoint created=%t err=%v", created, err)
	}

	// The worker lease expired and a reconciliation marked the attempt
	// reclaimed with a terminal receipt.
	now = now.Add(2 * time.Minute)
	if _, err := db.Exec(`
		UPDATE transcript_runner_attempts
		SET status='reclaimed',phase='terminal',phase_sequence=phase_sequence+1,
			finished_event_id=?,finished_at=?
		WHERE stream_uid=? AND attempt=1`,
		checkpoint.EventID, now, stream.UID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO transcript_runner_receipts(stream_uid,attempt,event_id,status,finished_at)
		VALUES(?,1,?,'reclaimed',?)`, stream.UID, checkpoint.EventID, now); err != nil {
		t.Fatal(err)
	}

	resumed, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: "owner-a", RunnerID: "frame-resume:resume-event-reclaimed",
		TTL: time.Minute, ResumeSource: ResumeSourceCheckpoint, ResumeCheckpoint: checkpoint.Sequence,
	})
	if err != nil || !resumed.Claimed {
		t.Fatalf("resumed claim=%#v err=%v", resumed, err)
	}
	if resumed.Claim.Attempt != 1 || resumed.Claim.ClaimToken == first.Claim.ClaimToken {
		t.Fatalf("resumed did not reuse the reclaimed attempt: %#v", resumed.Claim)
	}
	var attempts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_runner_attempts WHERE stream_uid=?`, stream.UID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("attempts=%d, want one stable parent attempt", attempts)
	}
	state, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, "owner-a", 1)
	if err != nil || state.Status != "running" || state.Phase != RunnerPhaseClaimed ||
		state.ClaimedInputRevision != 1 || state.FinishedEventID != 0 || state.FinishedAt != nil {
		t.Fatalf("revived state=%#v err=%v", state, err)
	}
	var receipts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_runner_receipts WHERE stream_uid=? AND attempt=1`, stream.UID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if receipts != 0 {
		t.Fatalf("reclaimed receipt survived revival: %d", receipts)
	}
	if _, _, _, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
		Claim: resumed.Claim, ClientMessageID: "finish-reclaimed", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`), Destinations: []string{"ws"},
	}); err != nil {
		t.Fatalf("finish revived attempt: %v", err)
	}
}

// TestRepositoryCheckpointResumeFailedAttemptStartsNewParent locks the other
// side of the contract: a real terminal failure starts a fresh authorized
// attempt rather than reviving the old one.
func TestRepositoryCheckpointResumeFailedAttemptStartsNewParent(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	now := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return now }

	if _, err := db.Exec(`
		INSERT INTO projects(id,user_id) VALUES('project-failed','owner-b');
		INSERT INTO frames(id,project_id,root_frame_id,status) VALUES('frame-failed','project-failed','frame-failed','processing');
		INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at)
		VALUES('resume-event-failed','frame-failed',1,'frame_resumed','{"dispatch":{"status":"claimed"}}','2026-07-24 02:00:00');`); err != nil {
		t.Fatal(err)
	}
	stream, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "stream-failed", OwnerID: "owner-b", ExternalID: "frame-failed",
		SessionID: "frame-failed", Kind: StreamKindFrameRef, ProjectID: "project-failed",
		RootFrameID: "frame-failed", FrameID: "frame-failed", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := repo.AppendFrameUserEvent(context.Background(), AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: "owner-b", ClientMessageID: "user-failed",
		FrameEventID: "frame-event-failed", MessageUUID: "message-failed", Text: "resume failed work",
		MessageOrigin: "task_intent", Destinations: []string{"ws"},
	}); err != nil {
		t.Fatal(err)
	}

	first, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: "owner-b", RunnerID: "runner-failed",
		TTL: time.Minute, ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !first.Claimed || first.Claim.Attempt != 1 {
		t.Fatalf("first claim=%#v err=%v", first, err)
	}
	checkpoint, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: first.Claim, ClientMessageID: "checkpoint-failed", Phase: RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: []byte(`{"status":"running"}`), Destinations: []string{"ws"},
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := db.Exec(`
		UPDATE transcript_runner_attempts
		SET status='failed',phase='terminal',phase_sequence=phase_sequence+1,
			finished_event_id=?,finished_at=?
		WHERE stream_uid=? AND attempt=1`,
		checkpoint.EventID, now, stream.UID); err != nil {
		t.Fatal(err)
	}

	resumed, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: "owner-b", RunnerID: "frame-resume:resume-event-failed",
		TTL: time.Minute, ResumeSource: ResumeSourceCheckpoint, ResumeCheckpoint: checkpoint.Sequence,
	})
	if err != nil || !resumed.Claimed {
		t.Fatalf("resumed claim=%#v err=%v", resumed, err)
	}
	if resumed.Claim.Attempt != 2 {
		t.Fatalf("failed attempt did not start a new parent attempt: %#v", resumed.Claim)
	}
	var attempts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_runner_attempts WHERE stream_uid=?`, stream.UID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("attempts=%d, want two", attempts)
	}
	if _, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, "owner-b", 1); err != nil {
		t.Fatalf("original failed attempt was mutated: %v", err)
	}
}
