package transcript

import (
	"context"
	"testing"
)

func TestRetiredCorrectionBudgetIsEligibleWithoutRewritingHistoricalCheckpoint(t *testing.T) {
	for _, reason := range []string{"runner_correction_no_progress_exhausted", "provider_unavailable", "waiting_user_decision"} {
		t.Run(reason, func(t *testing.T) {
			repo, db, _ := newTranscriptRepository(t)
			claim := seedArtifactProjectionClaim(t, repo, db, "old-correction-budget", "owner-a")
			ctx := context.Background()
			interruption, err := repo.InterruptRunner(ctx, InterruptRunnerInput{Claim: claim, ClientMessageID: "old-boundary", ReasonCode: reason, ResumeDetail: "Historical interruption", Resumable: true, AutoResume: false})
			if err != nil {
				t.Fatal(err)
			}
			want := reason == "runner_correction_no_progress_exhausted"
			candidate, found, err := repo.GetAutoResumeCandidate(ctx, claim.StreamUID, claim.OwnerID)
			if err != nil || found != want || found && candidate.CheckpointSequence != interruption.Checkpoint.Sequence {
				t.Fatalf("automatic projection: found=%t candidate=%#v error=%v", found, candidate, err)
			}
			ownerCandidates, err := repo.ListAutoResumeCandidates(ctx, claim.OwnerID, 20)
			if err != nil || (len(ownerCandidates) == 1) != want {
				t.Fatalf("owner candidates=%#v error=%v", ownerCandidates, err)
			}
			allCandidates, err := repo.ListAllAutoResumeCandidatesPage(ctx, 0, 20)
			if err != nil || (len(allCandidates) == 1) != want {
				t.Fatalf("dispatch candidates=%#v error=%v", allCandidates, err)
			}
			var storedAutomatic bool
			if err := db.QueryRowContext(ctx, `SELECT json_extract(payload_json,'$.auto_resume') FROM transcript_events WHERE stream_uid=? AND event_id=?`, claim.StreamUID, interruption.Event.EventID).Scan(&storedAutomatic); err != nil || storedAutomatic {
				t.Fatalf("history was rewritten: automatic=%t error=%v", storedAutomatic, err)
			}
			if _, err := db.ExecContext(ctx, `UPDATE frames SET status='cancelled' WHERE id='frame-a'`); err != nil {
				t.Fatal(err)
			}
			if _, found, err := repo.GetAutoResumeCandidate(ctx, claim.StreamUID, claim.OwnerID); err != nil || found {
				t.Fatalf("cancelled task became eligible: found=%t error=%v", found, err)
			}
		})
	}
}
