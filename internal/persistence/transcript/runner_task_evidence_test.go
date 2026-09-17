package transcript

import (
	"context"
	"fmt"
	"testing"
)

func TestRunnerTaskEvidenceSurvivesProviderReplayCheckpointWindow(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-task-evidence", "owner-a")
	source := appendRunnerReplayCheckpoint(t, repo, claim, "task-evidence-source", `{
		"status":"completed","toolPhase":"completed","toolName":"WebFetch","toolCallId":"source-call",
		"toolInput":{"url":"https://webbook.nist.gov/cgi/cbook.cgi?ID=C64175&Mask=4"},
		"toolResult":{"code":200,"url":"https://webbook.nist.gov/cgi/cbook.cgi?ID=C64175&Mask=4","result":"NIST source"}
	}`)
	for index := 0; index < 240; index++ {
		appendRunnerReplayCheckpoint(t, repo, claim, fmt.Sprintf("task-evidence-audit-%03d", index),
			fmt.Sprintf(`{"status":"running","audit":%d}`, index))
	}
	execution := appendRunnerReplayCheckpoint(t, repo, claim, "task-evidence-execution", `{
		"status":"completed","toolPhase":"completed","toolName":"software_runtime","toolCallId":"runtime-call",
		"toolResult":{"ok":true},"trustedScientificReviewSignals":["execution-tool:software_runtime"]
	}`)

	replay, err := repo.ListRunnerReplay(context.Background(), ListRunnerReplayInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, MessageLimit: 10, CheckpointLimit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if runnerReplayContainsEvent(replay, source.EventID) || !runnerReplayContainsEvent(replay, execution.EventID) {
		t.Fatalf("bounded provider replay source=%d execution=%d events=%#v", source.EventID, execution.EventID, replay)
	}

	evidence, err := repo.ListRunnerTaskEvidence(context.Background(), ListRunnerTaskEvidenceInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, ClaimedInputRevision: claim.ClaimedInputRevision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 2 || !runnerReplayContainsEvent(evidence, source.EventID) ||
		!runnerReplayContainsEvent(evidence, execution.EventID) {
		t.Fatalf("durable task evidence=%#v", evidence)
	}

	foreign := ListRunnerTaskEvidenceInput{
		StreamUID: claim.StreamUID, OwnerID: "owner-b", ClaimedInputRevision: claim.ClaimedInputRevision,
	}
	if _, err := repo.ListRunnerTaskEvidence(context.Background(), foreign); err != ErrOwnerMismatch {
		t.Fatalf("foreign task evidence error=%v", err)
	}
	otherRevision, err := repo.ListRunnerTaskEvidence(context.Background(), ListRunnerTaskEvidenceInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, ClaimedInputRevision: claim.ClaimedInputRevision + 1,
	})
	if err != nil || len(otherRevision) != 0 {
		t.Fatalf("other revision evidence=%#v err=%v", otherRevision, err)
	}
}
