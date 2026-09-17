package transcript

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCancelRunningRunnerIsAtomicAndInvalidatesClaim(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-cancel", "owner-a")
	const callers = 16
	var cancelled atomic.Int64
	var wait sync.WaitGroup
	errorsCh := make(chan error, callers)
	for index := 0; index < callers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := repo.CancelRunner(context.Background(), CancelRunnerInput{
				StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, ExpectedAttempt: claim.Attempt,
				ClientMessageID: "cancel-attempt", ReasonCode: "user_stopped", Destinations: []string{"ws"},
			})
			if err != nil {
				errorsCh <- err
				return
			}
			if result.Applied {
				cancelled.Add(1)
			}
		}()
	}
	wait.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
	if cancelled.Load() != 1 {
		t.Fatalf("cancel winners=%d", cancelled.Load())
	}
	state, err := repo.GetRunnerRuntimeState(context.Background(), claim.StreamUID, claim.OwnerID, claim.Attempt)
	if err != nil || state.Status != "cancelled" || state.Phase != RunnerPhaseTerminal {
		t.Fatalf("state=%#v err=%v", state, err)
	}
	latest, found, err := repo.GetLatestRunnerRuntimeState(context.Background(), claim.StreamUID, claim.OwnerID)
	if err != nil || !found || latest.Attempt != claim.Attempt || latest.Status != "cancelled" {
		t.Fatalf("latest=%#v found=%t err=%v", latest, found, err)
	}
	if _, _, err := repo.GetLatestRunnerRuntimeState(context.Background(), claim.StreamUID, "owner-b"); !errors.Is(err, ErrOwnerMismatch) {
		t.Fatalf("foreign latest error=%v", err)
	}
	if _, _, err := repo.AppendRunnerEvent(context.Background(), AppendEventInput{
		Claim: claim, ClientMessageID: "late-assistant", Type: "assistant_message",
		Source: EventSourcePayload, PayloadJSON: []byte(`{"text":"late"}`),
	}); !errors.Is(err, ErrClaimStale) {
		t.Fatalf("late assistant error=%v", err)
	}
	if _, _, _, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
		Claim: claim, ClientMessageID: "late-finish", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`),
	}); !errors.Is(err, ErrClaimStale) {
		t.Fatalf("late finish error=%v", err)
	}
	var frameStatus string
	if err := db.QueryRow(`SELECT status FROM frames WHERE id='frame-a'`).Scan(&frameStatus); err != nil || frameStatus != "cancelled" {
		t.Fatalf("frame status=%q err=%v", frameStatus, err)
	}
	if _, err := repo.CancelRunner(context.Background(), CancelRunnerInput{
		StreamUID: claim.StreamUID, OwnerID: "owner-b", ExpectedAttempt: claim.Attempt,
		ClientMessageID: "cancel-foreign", ReasonCode: "user_stopped",
	}); !errors.Is(err, ErrOwnerMismatch) {
		t.Fatalf("foreign error=%v", err)
	}
}

func TestCancelledFrameBlocksFreshClaimUntilNewInput(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES('project-cancel-admission','owner-a')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO frames(id,project_id,root_frame_id,status) VALUES('frame-cancel-admission','project-cancel-admission','frame-cancel-admission','processing')`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "stream-cancel-admission", OwnerID: "owner-a", ExternalID: "frame-cancel-admission",
		SessionID: "frame-cancel-admission", Kind: StreamKindFrameRef, ProjectID: "project-cancel-admission",
		RootFrameID: "frame-cancel-admission", FrameID: "frame-cancel-admission", Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, created, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: "stream-cancel-admission", OwnerID: "owner-a", ClientMessageID: "user-before-cancel",
		PayloadJSON: []byte(`{"text":"work"}`),
	}); err != nil || !created {
		t.Fatalf("input created=%t err=%v", created, err)
	}
	if _, err := db.Exec(`UPDATE frames SET status='cancelled' WHERE id='frame-cancel-admission'`); err != nil {
		t.Fatal(err)
	}
	blocked, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: "stream-cancel-admission", OwnerID: "owner-a", RunnerID: "runner-blocked",
		TTL: time.Minute, ResumeSource: ResumeSourceFresh,
	})
	if err != nil || blocked.Claimed {
		t.Fatalf("blocked claim=%#v err=%v", blocked, err)
	}
	if _, created, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: "stream-cancel-admission", OwnerID: "owner-a", ClientMessageID: "user-after-cancel",
		PayloadJSON: []byte(`{"text":"new work"}`),
	}); err != nil || !created {
		t.Fatalf("new input created=%t err=%v", created, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: "stream-cancel-admission", OwnerID: "owner-a", RunnerID: "runner-new-input",
		TTL: time.Minute, ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("new input claim=%#v err=%v", claimed, err)
	}
}
