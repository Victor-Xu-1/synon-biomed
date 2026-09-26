package transcript

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestLatestResumableCheckpointIsOwnerScoped(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-resume-latest", "owner-a")
	if _, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "checkpoint-nonresumable", Phase: RunnerPhaseExecuting,
		Resumable: false, PayloadJSON: []byte(`{"step":1}`),
	}); err != nil {
		t.Fatal(err)
	}
	expected, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "checkpoint-resumable", Phase: RunnerPhaseWaitingExternal,
		Resumable: true, PayloadJSON: []byte(`{"step":2}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
		Claim: claim, ClientMessageID: "finish-for-resume", Status: "failed",
		PayloadJSON: []byte(`{"status":"failed"}`),
	}); err != nil {
		t.Fatal(err)
	}
	checkpoint, found, err := repo.LatestResumableCheckpoint(context.Background(), claim.StreamUID, claim.OwnerID)
	if err != nil || !found || checkpoint.Sequence != expected.Sequence || checkpoint.EventID != expected.EventID ||
		checkpoint.Attempt != claim.Attempt || checkpoint.Phase != RunnerPhaseWaitingExternal {
		t.Fatalf("checkpoint=%#v found=%t err=%v", checkpoint, found, err)
	}
	if _, _, err := repo.LatestResumableCheckpoint(context.Background(), claim.StreamUID, "owner-b"); err != ErrOwnerMismatch {
		t.Fatalf("foreign error=%v", err)
	}
	if _, created, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, ClientMessageID: "user-after-checkpoint",
		PayloadJSON: []byte(`{"text":"new work"}`),
	}); err != nil || !created {
		t.Fatalf("new input created=%t err=%v", created, err)
	}
	next, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, RunnerID: "runner-without-checkpoint", TTL: time.Minute,
		ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !next.Claimed {
		t.Fatalf("next=%#v err=%v", next, err)
	}
	if _, _, _, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
		Claim: next.Claim, ClientMessageID: "finish-without-checkpoint", Status: "failed",
		PayloadJSON: []byte(`{"status":"failed"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if checkpoint, found, err := repo.LatestResumableCheckpoint(context.Background(), claim.StreamUID, claim.OwnerID); err != nil || found {
		t.Fatalf("stale checkpoint=%#v found=%t err=%v", checkpoint, found, err)
	}
}

func TestFrameTranscriptStartsNewAttemptAfterTerminalInput(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-new-turn", "owner-a")
	if _, _, _, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
		Claim: claim, ClientMessageID: "finish-first-turn", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, created, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, ClientMessageID: "user-1",
		PayloadJSON: []byte(`{"text":"create artifacts"}`), Destinations: []string{"ws"},
	}); err != nil || created {
		t.Fatalf("idempotent input created=%t err=%v", created, err)
	}
	var terminalStatus string
	if err := db.QueryRow(`SELECT status FROM frames WHERE id='frame-a'`).Scan(&terminalStatus); err != nil || terminalStatus != "completed" {
		t.Fatalf("terminal status=%q err=%v", terminalStatus, err)
	}
	if _, created, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, ClientMessageID: "user-second-turn",
		PayloadJSON: []byte(`{"text":"follow up"}`),
	}); err != nil || !created {
		t.Fatalf("append created=%t err=%v", created, err)
	}
	var frameStatus string
	if err := db.QueryRow(`SELECT status FROM frames WHERE id='frame-a'`).Scan(&frameStatus); err != nil || frameStatus != "processing" {
		t.Fatalf("frame status=%q err=%v", frameStatus, err)
	}
	next, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, RunnerID: "runner-next", TTL: time.Minute,
		ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !next.Claimed || next.Claim.Attempt != claim.Attempt+1 {
		t.Fatalf("next=%#v err=%v", next, err)
	}
	if _, _, _, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
		Claim: next.Claim, ClientMessageID: "finish-second-turn", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestListAutoResumeCandidatesReturnsResumableInterruptions(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-autoresume", "owner-a")
	if _, err := repo.InterruptRunner(context.Background(), InterruptRunnerInput{
		Claim: claim, ClientMessageID: "auto-resume-interrupt", ReasonCode: "artifact_reference_correction_required",
		ResumeDetail: "unsupported citations need repair", AutoResume: true, Destinations: []string{"ws"},
	}); err != nil {
		t.Fatal(err)
	}
	candidates, err := repo.ListAutoResumeCandidates(context.Background(), "owner-a", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates=%#v", candidates)
	}
	if candidates[0].StreamUID != claim.StreamUID || candidates[0].FrameID != "frame-a" ||
		candidates[0].ReasonCode != "artifact_reference_correction_required" {
		t.Fatalf("candidate=%#v", candidates[0])
	}
	// Another owner must never see the candidate.
	if others, err := repo.ListAutoResumeCandidates(context.Background(), "owner-b", 10); err != nil || len(others) != 0 {
		t.Fatalf("foreign candidates=%#v err=%v", others, err)
	}
}

func TestListAllExpiredRunnerCandidatesReturnsLatestResumableFrame(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-expired-runner", "owner-a")
	checkpoint, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "expired-runner-checkpoint", Phase: RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: []byte(`{"step":"after-tool"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE transcript_runner_attempts SET expires_at=? WHERE stream_uid=? AND attempt=?`,
		time.Now().UTC().Add(-time.Minute), claim.StreamUID, claim.Attempt); err != nil {
		t.Fatal(err)
	}
	candidates, err := repo.ListAllExpiredRunnerCandidates(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].StreamUID != claim.StreamUID ||
		candidates[0].FrameID != "frame-a" || candidates[0].Attempt != claim.Attempt ||
		candidates[0].CheckpointSequence != checkpoint.Sequence {
		t.Fatalf("expired candidates=%#v, want latest resumable checkpoint", candidates)
	}
}

func TestListAllExpiredRunnerCandidatesKeepsOldLeaseRecoverable(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-expired-stale", "owner-a")
	if _, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "expired-stale-checkpoint", Phase: RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: []byte(`{"step":"stale"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE transcript_runner_attempts SET expires_at=? WHERE stream_uid=? AND attempt=?`,
		time.Now().UTC().Add(-24*time.Hour), claim.StreamUID, claim.Attempt); err != nil {
		t.Fatal(err)
	}
	candidates, err := repo.ListAllExpiredRunnerCandidates(context.Background(), 10)
	if err != nil || len(candidates) != 1 || candidates[0].StreamUID != claim.StreamUID {
		t.Fatalf("old expired runner was not globally recoverable: candidates=%#v err=%v", candidates, err)
	}
}

func TestListAllExpiredRunnerCandidatesRejectsLaterNonResumableCheckpoint(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-expired-latest-nonresumable", "owner-a")
	for index, resumable := range []bool{true, false} {
		if _, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
			Claim: claim, ClientMessageID: "expired-latest-" + string(rune('1'+index)),
			Phase: RunnerPhaseExecuting, Resumable: resumable,
			PayloadJSON: []byte(`{"step":"bounded"}`),
		}); err != nil {
			t.Fatalf("append checkpoint %d: %v", index+1, err)
		}
	}
	if _, err := db.Exec(`UPDATE transcript_runner_attempts SET expires_at=? WHERE stream_uid=? AND attempt=?`,
		time.Now().UTC().Add(-time.Minute), claim.StreamUID, claim.Attempt); err != nil {
		t.Fatal(err)
	}
	candidates, err := repo.ListAllExpiredRunnerCandidates(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("expired candidates=%#v, want none after latest non-resumable checkpoint", candidates)
	}
}

func TestListAllExpiredUnrecoverableRunnerCandidatesReturnsLatestNonResumableFrame(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-expired-unrecoverable", "owner-a")
	interrupted, err := repo.InterruptRunner(context.Background(), InterruptRunnerInput{
		Claim: claim, ClientMessageID: "expired-unrecoverable-interrupt",
		ReasonCode: "artifact_reference_correction_required", ResumeDetail: "repair",
		RecoveryContractRevision: 1, AutoResume: false,
	})
	if err != nil || !interrupted.Created || interrupted.Checkpoint.Resumable {
		t.Fatalf("interruption=%#v err=%v", interrupted, err)
	}
	if _, err := db.Exec(`UPDATE transcript_runner_attempts SET expires_at=? WHERE stream_uid=? AND attempt=?`,
		time.Now().UTC().Add(-time.Minute), claim.StreamUID, claim.Attempt); err != nil {
		t.Fatal(err)
	}
	candidates, err := repo.ListAllExpiredUnrecoverableRunnerCandidates(context.Background(), 10)
	if err != nil || len(candidates) != 1 || candidates[0].StreamUID != claim.StreamUID ||
		candidates[0].CheckpointSequence != interrupted.Checkpoint.Sequence ||
		candidates[0].ReasonCode != "artifact_reference_correction_required" {
		t.Fatalf("unrecoverable candidates=%#v err=%v", candidates, err)
	}
}

func TestAutoResumeCandidatesHonorLatestNonResumableCheckpoint(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-latest-nonresumable", "owner-a")
	for index, resumable := range []bool{true, false} {
		if _, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
			Claim: claim, ClientMessageID: "latest-nonresumable-" + string(rune('1'+index)),
			Phase: RunnerPhaseExecuting, Resumable: resumable,
			PayloadJSON: []byte(`{"reason_code":"artifact_reference_correction_required","recovery_contract_revision":1}`),
		}); err != nil {
			t.Fatalf("append checkpoint %d: %v", index+1, err)
		}
	}
	if _, err := db.Exec(`UPDATE transcript_runner_attempts SET expires_at=? WHERE stream_uid=? AND attempt=?`,
		time.Now().UTC().Add(-time.Minute), claim.StreamUID, claim.Attempt); err != nil {
		t.Fatal(err)
	}
	if candidate, found, err := repo.GetAutoResumeCandidate(
		context.Background(), claim.StreamUID, claim.OwnerID,
	); err != nil || found {
		t.Fatalf("single candidate=%#v found=%t err=%v", candidate, found, err)
	}
	if candidates, err := repo.ListAutoResumeCandidates(context.Background(), claim.OwnerID, 10); err != nil || len(candidates) != 0 {
		t.Fatalf("owner candidates=%#v err=%v", candidates, err)
	}
	if candidates, err := repo.ListAllAutoResumeCandidates(context.Background(), 10); err != nil || len(candidates) != 0 {
		t.Fatalf("global candidates=%#v err=%v", candidates, err)
	}
}

func TestAutoResumeCandidatesIgnoreTerminalFrameStatus(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-terminal-frame", "owner-a")
	interrupted, err := repo.InterruptRunner(context.Background(), InterruptRunnerInput{
		Claim: claim, ClientMessageID: "terminal-frame-interruption",
		ReasonCode: "artifact_reference_correction_required", ResumeDetail: "repair",
		AutoResume: true,
	})
	if err != nil || !interrupted.Created {
		t.Fatalf("interrupted=%#v err=%v", interrupted, err)
	}
	if _, err := db.Exec(`UPDATE frames SET status='failed' WHERE id='frame-a'`); err != nil {
		t.Fatal(err)
	}
	if candidate, found, err := repo.GetAutoResumeCandidate(context.Background(), claim.StreamUID, claim.OwnerID); err != nil || found {
		t.Fatalf("terminal frame candidate=%#v found=%t err=%v", candidate, found, err)
	}
	if candidates, err := repo.ListAllAutoResumeCandidates(context.Background(), 10); err != nil || len(candidates) != 0 {
		t.Fatalf("terminal frame global candidates=%#v err=%v", candidates, err)
	}
}

func TestAutoResumeCandidatesDoNotFallBackToOlderAttempt(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-latest-attempt", "owner-a")
	interrupted, err := repo.InterruptRunner(context.Background(), InterruptRunnerInput{
		Claim: claim, ClientMessageID: "older-resumable",
		ReasonCode: "artifact_reference_correction_required", RecoveryContractRevision: 1,
		ResumeDetail: "repair", AutoResume: true,
	})
	if err != nil || !interrupted.Created {
		t.Fatalf("older interruption=%#v err=%v", interrupted, err)
	}
	checkpoint, found, err := repo.LatestResumableCheckpoint(context.Background(), claim.StreamUID, claim.OwnerID)
	if err != nil || !found {
		t.Fatalf("checkpoint=%#v found=%t err=%v", checkpoint, found, err)
	}
	latest, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, RunnerID: "latest-active",
		TTL: time.Hour, ResumeSource: ResumeSourceCheckpoint, ResumeCheckpoint: checkpoint.Sequence,
	})
	if err != nil || !latest.Claimed {
		t.Fatalf("latest claim=%#v err=%v", latest, err)
	}
	if _, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: latest.Claim, ClientMessageID: "latest-nonresumable",
		Phase: RunnerPhaseExecuting, Resumable: false,
		PayloadJSON: []byte(`{"reason_code":"artifact_reference_correction_required","recovery_contract_revision":1}`),
	}); err != nil {
		t.Fatal(err)
	}
	if candidate, found, err := repo.GetAutoResumeCandidate(
		context.Background(), claim.StreamUID, claim.OwnerID,
	); err != nil || found {
		t.Fatalf("single candidate=%#v found=%t err=%v", candidate, found, err)
	}
	if candidates, err := repo.ListAutoResumeCandidates(context.Background(), claim.OwnerID, 10); err != nil || len(candidates) != 0 {
		t.Fatalf("owner candidates=%#v err=%v", candidates, err)
	}
	if candidates, err := repo.ListAllAutoResumeCandidates(context.Background(), 10); err != nil || len(candidates) != 0 {
		t.Fatalf("global candidates=%#v err=%v", candidates, err)
	}
}

func TestListAutoResumeCandidatesKeepsSameInputRevisionBouncesEligible(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-bounce-ceiling", "owner-a")
	const bounceCount = int64(12)
	for bounce := int64(1); bounce <= bounceCount; bounce++ {
		if _, err := repo.InterruptRunner(context.Background(), InterruptRunnerInput{
			Claim: claim, ClientMessageID: "bounce-interrupt-" + string(rune('0'+bounce)),
			ReasonCode: "completion_review_correction_required", ResumeDetail: "repair",
			AutoResume: true, Destinations: []string{"ws"},
		}); err != nil {
			t.Fatalf("bounce %d interrupt: %v", bounce, err)
		}
		candidates, err := repo.ListAutoResumeCandidates(context.Background(), claim.OwnerID, 10)
		if err != nil {
			t.Fatalf("bounce %d list: %v", bounce, err)
		}
		if len(candidates) != 1 || candidates[0].InputRevisionBounces != bounce {
			t.Fatalf("bounce %d candidates=%#v, want one eligible candidate", bounce, candidates)
		}
		if bounce == bounceCount {
			allCandidates, allErr := repo.ListAllAutoResumeCandidates(context.Background(), 10)
			if allErr != nil || len(allCandidates) != 1 || allCandidates[0].StreamUID != claim.StreamUID {
				t.Fatalf("global candidates=%#v err=%v", allCandidates, allErr)
			}
			exact, found, exactErr := repo.GetAutoResumeCandidate(context.Background(), claim.StreamUID, claim.OwnerID)
			if exactErr != nil || !found || exact.StreamUID != claim.StreamUID || exact.InputRevisionBounces != bounceCount {
				t.Fatalf("exact candidate=%#v found=%t err=%v", exact, found, exactErr)
			}
			break
		}
		checkpoint, found, err := repo.LatestResumableCheckpoint(context.Background(), claim.StreamUID, claim.OwnerID)
		if err != nil || !found {
			t.Fatalf("bounce %d checkpoint=%#v found=%t err=%v", bounce, checkpoint, found, err)
		}
		next, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
			StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, RunnerID: "bounce-runner-" + string(rune('0'+bounce+1)),
			TTL: time.Minute, ResumeSource: ResumeSourceCheckpoint, ResumeCheckpoint: checkpoint.Sequence,
		})
		if err != nil || !next.Claimed || next.Claim.Attempt != claim.Attempt {
			t.Fatalf("bounce %d next=%#v err=%v", bounce, next, err)
		}
		claim = next.Claim
	}
}

func TestListAllAutoResumeCandidatePagesDoNotStarveOlderStreams(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	const total = 25
	for index := 0; index < total; index++ {
		projectID := fmt.Sprintf("project-page-%02d", index)
		frameID := fmt.Sprintf("frame-page-%02d", index)
		streamUID := "frame:" + frameID
		if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES(?,?)`, projectID, "owner-page"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO frames(id,project_id,root_frame_id) VALUES(?,?,?)`, frameID, projectID, frameID); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.CreateStream(context.Background(), CreateStreamInput{
			UID: streamUID, OwnerID: "owner-page", ExternalID: frameID, SessionID: frameID,
			Kind: StreamKindFrameRef, ProjectID: projectID, RootFrameID: frameID, FrameID: frameID, Epoch: 1,
		}); err != nil {
			t.Fatal(err)
		}
		if _, created, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
			StreamUID: streamUID, OwnerID: "owner-page", ClientMessageID: frameID + "-user",
			PayloadJSON: []byte(`{"text":"continue"}`), Destinations: []string{"ws"},
		}); err != nil || !created {
			t.Fatalf("stream %s user created=%t err=%v", streamUID, created, err)
		}
		claimed, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
			StreamUID: streamUID, OwnerID: "owner-page", RunnerID: frameID + "-runner",
			TTL: time.Minute, ResumeSource: ResumeSourceFresh,
		})
		if err != nil || !claimed.Claimed {
			t.Fatalf("stream %s claim=%#v err=%v", streamUID, claimed, err)
		}
		if interrupted, err := repo.InterruptRunner(context.Background(), InterruptRunnerInput{
			Claim: claimed.Claim, ClientMessageID: frameID + "-interrupt",
			ReasonCode: "provider_stream_no_progress", ResumeDetail: "resume", AutoResume: true,
		}); err != nil || !interrupted.Created {
			t.Fatalf("stream %s interruption=%#v err=%v", streamUID, interrupted, err)
		}
	}

	seen := make(map[string]struct{}, total)
	for offset := 0; ; {
		page, err := repo.ListAllAutoResumeCandidatesPage(context.Background(), offset, 7)
		if err != nil {
			t.Fatal(err)
		}
		for _, candidate := range page {
			if _, duplicate := seen[candidate.StreamUID]; duplicate {
				t.Fatalf("candidate %s repeated across pages", candidate.StreamUID)
			}
			seen[candidate.StreamUID] = struct{}{}
		}
		offset += len(page)
		if len(page) < 7 {
			break
		}
	}
	if len(seen) != total {
		t.Fatalf("paged candidates=%d want=%d", len(seen), total)
	}
}

func TestListAutoResumeCandidatesTracksLatestFailureReason(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-per-reason-ceiling", "owner-a")
	reasons := []string{
		"tool_call_failed",
		"provider_context_pressure",
		"completion_review_correction_required",
	}
	for index, reason := range reasons {
		interrupted, err := repo.InterruptRunner(context.Background(), InterruptRunnerInput{
			Claim: claim, ClientMessageID: "per-reason-interrupt-" + string(rune('0'+index+1)),
			ReasonCode: reason, ResumeDetail: "recover " + reason,
			AutoResume: true, Destinations: []string{"ws"},
		})
		if err != nil || !interrupted.Created {
			t.Fatalf("interrupt %q=%#v err=%v", reason, interrupted, err)
		}
		if index == len(reasons)-1 {
			break
		}
		next, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
			StreamUID: claim.StreamUID, OwnerID: claim.OwnerID,
			RunnerID: "per-reason-runner-" + string(rune('0'+index+2)), TTL: time.Minute,
			ResumeSource: ResumeSourceCheckpoint, ResumeCheckpoint: interrupted.Checkpoint.Sequence,
		})
		if err != nil || !next.Claimed {
			t.Fatalf("reclaim after %q=%#v err=%v", reason, next, err)
		}
		claim = next.Claim
	}

	candidates, err := repo.ListAutoResumeCandidates(context.Background(), claim.OwnerID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].ReasonCode != reasons[len(reasons)-1] {
		t.Fatalf("per-reason owner candidates=%#v", candidates)
	}
	allCandidates, err := repo.ListAllAutoResumeCandidates(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(allCandidates) != 1 || allCandidates[0].ReasonCode != reasons[len(reasons)-1] {
		t.Fatalf("per-reason global candidates=%#v", allCandidates)
	}
	exact, found, err := repo.GetAutoResumeCandidate(
		context.Background(), claim.StreamUID, claim.OwnerID,
	)
	if err != nil || !found || exact.StreamUID != claim.StreamUID ||
		exact.ReasonCode != reasons[len(reasons)-1] {
		t.Fatalf("per-reason exact candidate=%#v found=%t err=%v", exact, found, err)
	}
}

func TestListAllAutoResumeCandidatesKeepsOldInterruptionRecoverable(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-stale-auto-resume", "owner-a")
	interrupted, err := repo.InterruptRunner(context.Background(), InterruptRunnerInput{
		Claim: claim, ClientMessageID: "stale-auto-resume-interruption",
		ReasonCode: "artifact_reference_correction_required", ResumeDetail: "repair",
		RecoveryContractRevision: 1, AutoResume: true,
	})
	if err != nil || !interrupted.Created {
		t.Fatalf("interrupted=%#v err=%v", interrupted, err)
	}
	if _, err := db.Exec(`UPDATE transcript_events SET created_at=? WHERE stream_uid=? AND event_id=?`,
		time.Now().UTC().Add(-24*time.Hour), claim.StreamUID, interrupted.Event.EventID); err != nil {
		t.Fatal(err)
	}
	all, err := repo.ListAllAutoResumeCandidates(context.Background(), 10)
	if err != nil || len(all) != 1 || all[0].StreamUID != claim.StreamUID {
		t.Fatalf("old interruption was not globally recoverable: candidates=%#v err=%v", all, err)
	}
	if exact, found, err := repo.GetAutoResumeCandidate(context.Background(), claim.StreamUID, claim.OwnerID); err != nil || !found || exact.StreamUID != claim.StreamUID {
		t.Fatalf("explicit stream recovery was incorrectly expired: candidate=%#v found=%t err=%v", exact, found, err)
	}
}

func TestAutoResumeCandidateScopesBounceCountToRecoveryContractRevision(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-recovery-revision", "owner-a")
	for bounce := 1; bounce <= 3; bounce++ {
		interrupted, err := repo.InterruptRunner(context.Background(), InterruptRunnerInput{
			Claim: claim, ClientMessageID: "legacy-recovery-" + string(rune('0'+bounce)),
			ReasonCode: "artifact_reference_correction_required", ResumeDetail: "malformed artifact link",
			AutoResume: true, Destinations: []string{"ws"},
		})
		if err != nil || !interrupted.Created {
			t.Fatalf("legacy bounce %d=%#v err=%v", bounce, interrupted, err)
		}
		if bounce == 3 {
			break
		}
		next, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
			StreamUID: claim.StreamUID, OwnerID: claim.OwnerID,
			RunnerID: "legacy-recovery-runner-" + string(rune('0'+bounce+1)), TTL: time.Minute,
			ResumeSource: ResumeSourceCheckpoint, ResumeCheckpoint: interrupted.Checkpoint.Sequence,
		})
		if err != nil || !next.Claimed {
			t.Fatalf("legacy reclaim %d=%#v err=%v", bounce, next, err)
		}
		claim = next.Claim
	}
	if candidate, found, err := repo.GetAutoResumeCandidate(
		context.Background(), claim.StreamUID, claim.OwnerID,
	); err != nil || !found || candidate.InputRevisionBounces != 3 || candidate.RecoveryContractRevision != 0 {
		t.Fatalf("legacy candidate=%#v found=%t err=%v", candidate, found, err)
	}
	checkpoint, found, err := repo.LatestResumableCheckpoint(context.Background(), claim.StreamUID, claim.OwnerID)
	if err != nil || !found {
		t.Fatalf("legacy checkpoint=%#v found=%t err=%v", checkpoint, found, err)
	}
	next, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, RunnerID: "current-recovery-runner", TTL: time.Minute,
		ResumeSource: ResumeSourceCheckpoint, ResumeCheckpoint: checkpoint.Sequence,
	})
	if err != nil || !next.Claimed {
		t.Fatalf("current revision reclaim=%#v err=%v", next, err)
	}
	current, err := repo.InterruptRunner(context.Background(), InterruptRunnerInput{
		Claim: next.Claim, ClientMessageID: "current-recovery-1",
		ReasonCode: "artifact_reference_correction_required", ResumeDetail: "malformed artifact link",
		RecoveryContractRevision: 1, AutoResume: true, Destinations: []string{"ws"},
	})
	if err != nil || !current.Created {
		t.Fatalf("current revision interruption=%#v err=%v", current, err)
	}
	candidate, found, err := repo.GetAutoResumeCandidate(
		context.Background(), claim.StreamUID, claim.OwnerID,
	)
	if err != nil || !found || candidate.RecoveryContractRevision != 1 || candidate.InputRevisionBounces != 1 {
		t.Fatalf("current revision candidate=%#v found=%t err=%v", candidate, found, err)
	}
}

func TestLatestRunnerInterruptionProjectsProviderReasonAndCreatedAt(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-provider-paused", "owner-a")
	interrupted, err := repo.InterruptRunner(context.Background(), InterruptRunnerInput{
		Claim: claim, ClientMessageID: "provider-quota-paused",
		ReasonCode:   "model_provider_unavailable",
		ResumeDetail: "provider quota is exhausted; choose another model and continue",
		Resumable:    true, AutoResume: false, Destinations: []string{"ws"},
	})
	if err != nil || !interrupted.Created || !interrupted.Checkpoint.Resumable {
		t.Fatalf("interrupted=%#v err=%v", interrupted, err)
	}
	got, found, err := repo.LatestRunnerInterruption(context.Background(), claim.StreamUID, claim.OwnerID)
	if err != nil || !found {
		t.Fatalf("interruption=%#v found=%t err=%v", got, found, err)
	}
	if got.Attempt != claim.Attempt || got.CheckpointSequence != interrupted.Checkpoint.Sequence ||
		got.ReasonCode != "model_provider_unavailable" ||
		got.ResumeDetail != "provider quota is exhausted; choose another model and continue" ||
		got.CreatedAt.IsZero() || got.AutoResume {
		t.Fatalf("projected interruption=%#v", got)
	}
}

func TestLatestRunnerInterruptionPreservesAutomaticEligibility(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		auto, want   bool
	}{
		{"waiting", "plan_step_status_required", false, false},
		{"continuing", "plan_step_status_required", true, true},
		{"retired-policy", RetiredCorrectionBudgetReason, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, db, _ := newTranscriptRepository(t)
			claim := seedArtifactProjectionClaim(t, repo, db, "stream-wait", "owner-a")
			if _, err := repo.InterruptRunner(context.Background(), InterruptRunnerInput{
				Claim: claim, ClientMessageID: "interrupt", ReasonCode: tc.reason, Resumable: true, AutoResume: tc.auto,
			}); err != nil {
				t.Fatal(err)
			}
			got, found, err := repo.LatestRunnerInterruption(context.Background(), claim.StreamUID, claim.OwnerID)
			if err != nil || !found || got.AutoResume != tc.want {
				t.Fatalf("automatic eligibility: %#v %v", got, err)
			}
		})
	}
}
