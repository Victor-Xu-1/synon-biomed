package workspace

import (
	"context"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestRunnerTaskMetricsAuthoritySeparatesTaskTotalsFromLatestInputRevision(t *testing.T) {
	store, repo, claim := newToolCallBatchFixture(t)

	metrics, found, err := repo.GetRunnerTaskMetricsAuthority(
		context.Background(), claim.StreamUID, claim.OwnerID, claim.ClaimedInputRevision,
	)
	if err != nil || !found || len(metrics.TaskAttempts) != 1 || len(metrics.LatestAttempts) != 1 ||
		metrics.TaskAttempts[0] != claim.Attempt || metrics.LatestAttempts[0] != claim.Attempt ||
		metrics.TaskToolCallCount != 0 || metrics.LatestToolCallCount != 0 {
		t.Fatalf("metrics=%#v found=%t err=%v", metrics, found, err)
	}

	createToolCallBatchForTest(
		t, store, repo, claim, "task-metrics-batch",
		toolCallBatchTestCall{id: "task-metrics-read", name: "read_file", arguments: map[string]any{"path": "notes.txt"}},
		toolCallBatchTestCall{id: "task-metrics-python", name: "python", arguments: map[string]any{"code": "print(1)"}},
		toolCallBatchTestCall{id: "task-metrics-save", name: "save_artifact", arguments: map[string]any{"name": "result.txt"}},
	)

	if _, _, created, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claim, ClientMessageID: "task-metrics-first-finished", Status: "completed",
		PayloadJSON: []byte(`{"summary":"first round complete"}`),
	}); err != nil || !created {
		t.Fatalf("finish first created=%t err=%v", created, err)
	}
	if _, created, err := repo.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, ClientMessageID: "task-metrics-second-input",
		PayloadJSON: []byte(`{"text":"continue"}`),
	}); err != nil || !created {
		t.Fatalf("append second input created=%t err=%v", created, err)
	}
	second, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, RunnerID: "task-metrics-runner-2", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !second.Claimed || second.Claim.ClaimedInputRevision <= claim.ClaimedInputRevision {
		t.Fatalf("second claim=%#v err=%v", second, err)
	}
	createToolCallBatchForTest(
		t, store, repo, second.Claim, "task-metrics-second-batch",
		toolCallBatchTestCall{id: "task-metrics-second-read", name: "read_file", arguments: map[string]any{"path": "result.txt"}},
		toolCallBatchTestCall{id: "task-metrics-second-save", name: "save_artifact", arguments: map[string]any{"name": "final.txt"}},
	)

	metrics, found, err = repo.GetRunnerTaskMetricsAuthority(
		context.Background(), claim.StreamUID, claim.OwnerID, second.Claim.ClaimedInputRevision,
	)
	if err != nil || !found || len(metrics.TaskAttempts) != 2 || len(metrics.LatestAttempts) != 1 ||
		metrics.TaskAttempts[0] != claim.Attempt || metrics.TaskAttempts[1] != second.Claim.Attempt ||
		metrics.LatestAttempts[0] != second.Claim.Attempt || metrics.TaskToolCallCount != 5 ||
		metrics.LatestToolCallCount != 2 {
		t.Fatalf("metrics=%#v found=%t err=%v", metrics, found, err)
	}
}
