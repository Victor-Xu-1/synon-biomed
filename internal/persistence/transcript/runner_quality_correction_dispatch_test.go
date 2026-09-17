package transcript

import (
	"context"
	"testing"
	"time"
)

func TestClaimNextRunnerDefersReasonCodedInterruptionWhileResumeDispatchOwnsFrame(t *testing.T) {
	repository, database, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(
		t, repository, database, "stream-quality-correction-dispatch", "owner-a",
	)
	interrupted, err := repository.InterruptRunner(context.Background(), InterruptRunnerInput{
		Claim: claim, ClientMessageID: "quality-correction-interrupted",
		ReasonCode:               "artifact_reference_correction_required",
		ResumeDetail:             "repair one unresolved artifact reference",
		RecoveryContractRevision: 3, AutoResume: true, Destinations: []string{"ws"},
	})
	if err != nil || !interrupted.Created || !interrupted.Checkpoint.Resumable {
		t.Fatalf("interrupted=%#v err=%v", interrupted, err)
	}
	if _, err := database.Exec(`INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at)
		VALUES('quality-correction-resume','frame-a',1,'frame_resumed',
		'{"dispatch":{"status":"blocked"}}',?)`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	next, err := repository.ClaimNextRunner(context.Background(), ClaimNextRunnerInput{
		RunnerID: "ordinary-runner", TTL: time.Minute,
	})
	if err != nil || next.Claimed {
		t.Fatalf("ordinary runner bypassed quality-correction dispatch: next=%#v err=%v", next, err)
	}

	candidate, found, err := repository.GetAutoResumeCandidate(
		context.Background(), claim.StreamUID, claim.OwnerID,
	)
	if err != nil || !found || candidate.ReasonCode != "artifact_reference_correction_required" ||
		candidate.CheckpointSequence != interrupted.Checkpoint.Sequence {
		t.Fatalf("dispatch candidate=%#v found=%t err=%v", candidate, found, err)
	}
}

func TestClaimNextRunnerNewUserMessageSupersedesStaleResumeDispatch(t *testing.T) {
	repository, database, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(
		t, repository, database, "stream-stale-resume-superseded", "owner-a",
	)
	interrupted, err := repository.InterruptRunner(context.Background(), InterruptRunnerInput{
		Claim: claim, ClientMessageID: "stale-resume-interrupted",
		ReasonCode:               "artifact_reference_correction_required",
		ResumeDetail:             "repair one unresolved artifact reference",
		RecoveryContractRevision: 3, AutoResume: true, Destinations: []string{"ws"},
	})
	if err != nil || !interrupted.Created || !interrupted.Checkpoint.Resumable {
		t.Fatalf("interrupted=%#v err=%v", interrupted, err)
	}
	if _, err := database.Exec(`INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at)
		VALUES('stale-resume-event','frame-a',1,'frame_resumed',
		'{"dispatch":{"status":"registered"}}',?)`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, created, err := repository.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, ClientMessageID: "new-user-after-stale-resume",
		PayloadJSON: []byte(`{"text":"save the unchanged execution log"}`),
	}); err != nil || !created {
		t.Fatalf("new user message created=%t err=%v", created, err)
	}

	next, err := repository.ClaimNextRunner(context.Background(), ClaimNextRunnerInput{
		RunnerID: "fresh-runner-after-stale-resume", TTL: time.Minute,
	})
	if err != nil || !next.Claimed || next.Claim.ResumeSource != ResumeSourceFresh ||
		next.Claim.Attempt != claim.Attempt+1 {
		t.Fatalf("fresh claim after stale resume=%#v err=%v", next, err)
	}
}

func TestClaimNextRunnerRecoversReasonCodedInterruptionWithoutResumeDispatch(t *testing.T) {
	repository, database, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(
		t, repository, database, "stream-language-recovery", "owner-a",
	)
	interrupted, err := repository.InterruptRunner(context.Background(), InterruptRunnerInput{
		Claim: claim, ClientMessageID: "language-mismatch-interrupted",
		ReasonCode:               "response_language_mismatch",
		ResumeDetail:             "restart the response in the task language",
		RecoveryContractRevision: 3, AutoResume: true, Destinations: []string{"ws"},
	})
	if err != nil || !interrupted.Created || !interrupted.Checkpoint.Resumable {
		t.Fatalf("interrupted=%#v err=%v", interrupted, err)
	}

	next, err := repository.ClaimNextRunner(context.Background(), ClaimNextRunnerInput{
		RunnerID: "ordinary-language-recovery", TTL: time.Minute,
	})
	if err != nil || !next.Claimed || next.Claim.ResumeSource != ResumeSourceCheckpoint ||
		next.Claim.ResumeCheckpoint != interrupted.Checkpoint.Sequence {
		t.Fatalf("reason-coded ordinary recovery=%#v err=%v", next, err)
	}
}

func TestClaimNextRunnerParksExplicitResumableInterruption(t *testing.T) {
	repository, database, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(
		t, repository, database, "stream-model-selection-wait", "owner-a",
	)
	interrupted, err := repository.InterruptRunner(context.Background(), InterruptRunnerInput{
		Claim: claim, ClientMessageID: "model-selection-wait",
		ReasonCode: "model_provider_unavailable", ResumeDetail: "select a model",
		Resumable: true, AutoResume: false, Destinations: []string{"ws"},
	})
	if err != nil || !interrupted.Created || !interrupted.Checkpoint.Resumable {
		t.Fatalf("interrupted=%#v err=%v", interrupted, err)
	}

	if next, err := repository.ClaimNextRunner(context.Background(), ClaimNextRunnerInput{
		RunnerID: "ordinary-runner", TTL: time.Minute,
	}); err != nil || next.Claimed {
		t.Fatalf("ordinary runner reclaimed explicit wait: next=%#v err=%v", next, err)
	}
	if candidate, found, err := repository.GetAutoResumeCandidate(
		context.Background(), claim.StreamUID, claim.OwnerID,
	); err != nil || found {
		t.Fatalf("explicit wait became auto-resume candidate=%#v found=%t err=%v", candidate, found, err)
	}
	if candidates, err := repository.ListAllExpiredRunnerCandidates(context.Background(), 10); err != nil || len(candidates) != 0 {
		t.Fatalf("explicit wait became expired-runner recovery=%#v err=%v", candidates, err)
	}
	if latest, found, err := repository.LatestRunnerInterruption(
		context.Background(), claim.StreamUID, claim.OwnerID,
	); err != nil || !found || latest.CheckpointSequence != interrupted.Checkpoint.Sequence {
		t.Fatalf("explicit wait lost resumable interruption=%#v found=%t err=%v", latest, found, err)
	}
}

func TestClaimNextRunnerStillRecoversReasonlessCheckpoint(t *testing.T) {
	repository, database, _ := newTranscriptRepository(t)
	now := time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)
	repository.now = func() time.Time { return now }
	claim := seedArtifactProjectionClaim(
		t, repository, database, "stream-reasonless-checkpoint", "owner-a",
	)
	checkpoint, _, created, err := repository.AppendRunnerCheckpoint(
		context.Background(), AppendRunnerCheckpointInput{
			Claim: claim, ClientMessageID: "reasonless-checkpoint",
			Phase: RunnerPhaseExecuting, Resumable: true,
			PayloadJSON:  []byte(`{"status":"running","message":"durable progress"}`),
			Destinations: []string{"ws"},
		},
	)
	if err != nil || !created {
		t.Fatalf("checkpoint=%#v created=%t err=%v", checkpoint, created, err)
	}

	now = now.Add(2 * time.Minute)
	next, err := repository.ClaimNextRunner(context.Background(), ClaimNextRunnerInput{
		RunnerID: "ordinary-recovery-runner", TTL: time.Minute,
	})
	if err != nil || !next.Claimed || next.Claim.ResumeSource != ResumeSourceCheckpoint ||
		next.Claim.ResumeCheckpoint != checkpoint.Sequence {
		t.Fatalf("reasonless checkpoint recovery=%#v err=%v", next, err)
	}
}
