package transcript

import (
	"context"
	"testing"
	"time"
)

func TestExpireRunnerAttemptsClaimedBeforeFencesOnlyPreviousRuntime(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	now := time.Date(2026, 8, 27, 3, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return now }
	if _, err := db.Exec(`
		INSERT INTO projects(id,user_id) VALUES('project-restart','owner');
		INSERT INTO frames(id,project_id,root_frame_id,status)
		VALUES('frame-restart','project-restart','frame-restart','processing');`); err != nil {
		t.Fatal(err)
	}
	stream, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:frame-restart", OwnerID: "owner", ExternalID: "frame-restart",
		SessionID: "frame-restart", Kind: StreamKindFrameRef, ProjectID: "project-restart",
		RootFrameID: "frame-restart", FrameID: "frame-restart", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := repo.AppendFrameUserEvent(context.Background(), AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "restart-input",
		FrameEventID: "restart-frame-event", MessageUUID: "restart-message", Text: "run a long task",
		Destinations: []string{"ws"},
	}); err != nil {
		t.Fatal(err)
	}
	claim, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "old-runtime/chat-1",
		TTL: 5 * time.Minute, ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	startup := now.Add(time.Second)
	now = startup.Add(time.Second)
	fenced, err := repo.ExpireRunnerAttemptsClaimedBefore(context.Background(), startup)
	if err != nil || fenced != 1 {
		t.Fatalf("fenced=%d err=%v", fenced, err)
	}
	state, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID, claim.Claim.Attempt)
	if err != nil || state.ExpiresAt.After(now) {
		t.Fatalf("fenced state=%#v err=%v", state, err)
	}
	if fenced, err := repo.ExpireRunnerAttemptsClaimedBefore(context.Background(), startup); err != nil || fenced != 0 {
		t.Fatalf("idempotent fenced=%d err=%v", fenced, err)
	}
}

func TestCountActiveRunnerAttemptsBridgesLeaseHandoffWithoutKeepingOldOrphansBusy(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	now := time.Date(2026, 8, 29, 16, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return now }
	if _, err := db.Exec(`
		INSERT INTO projects(id,user_id) VALUES('project-handoff','owner');
		INSERT INTO frames(id,project_id,root_frame_id,status)
		VALUES('lease-handoff','project-handoff','lease-handoff','processing');`); err != nil {
		t.Fatal(err)
	}
	stream, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "frame:lease-handoff", OwnerID: "owner", ExternalID: "lease-handoff",
		SessionID: "lease-handoff", Kind: StreamKindFrameRef, ProjectID: "project-handoff",
		RootFrameID: "lease-handoff", FrameID: "lease-handoff", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := repo.AppendFrameUserEvent(context.Background(), AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "handoff-input",
		FrameEventID: "handoff-frame-event", MessageUUID: "handoff-message", Text: "continue",
		Destinations: []string{"ws"},
	}); err != nil {
		t.Fatal(err)
	}
	claim, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner",
		TTL: 5 * time.Minute, ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	if _, err := db.Exec(`UPDATE transcript_runner_attempts SET expires_at=? WHERE stream_uid=?`,
		now.Add(-time.Second), stream.UID); err != nil {
		t.Fatal(err)
	}
	if count, err := repo.CountActiveRunnerAttempts(context.Background()); err != nil || count != 1 {
		t.Fatalf("handoff count=%d err=%v", count, err)
	}
	if _, err := db.Exec(`UPDATE transcript_runner_attempts SET expires_at=? WHERE stream_uid=?`,
		now.Add(-runnerAttemptLeaseHandoffGrace-time.Second), stream.UID); err != nil {
		t.Fatal(err)
	}
	if count, err := repo.CountActiveRunnerAttempts(context.Background()); err != nil || count != 0 {
		t.Fatalf("orphan count=%d err=%v", count, err)
	}
}
