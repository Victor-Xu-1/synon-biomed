package workspace

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
)

func TestToolOperationObservationAdmissionFencesAndRecovery(t *testing.T) {
	store, repo, claim := newToolCallBatchFixture(t)
	batch, items := createToolCallBatchForTest(t, store, repo, claim, "observed-batch", toolCallBatchTestCall{id: "bg-env", name: "manage_environments", arguments: map[string]any{"background": true, "mode": "create", "name": "test"}})
	batch, err := store.ClaimToolCallBatch(context.Background(), ClaimToolCallBatchInput{Claim: claim, BatchID: batch.BatchID, ExpectedStateVersion: batch.StateVersion})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = appendToolCallBatchStart(t, store, repo, claim, batch, items[0], "observed-start")
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, created, err := repo.BeginToolOperationObservation(context.Background(), claim, "bg-env", "manage_environments", "boot-one")
			if err != nil {
				t.Error(err)
			}
			if created {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("admission winners=%d", winners.Load())
	}
	observer, created, err := repo.BeginToolOperationObservation(context.Background(), claim, "bg-env", "manage_environments", "boot-one")
	if err != nil || created {
		t.Fatalf("replay created=%t err=%v", created, err)
	}
	wrong := observer
	wrong.CallID = "other-call"
	if _, err := repo.AppendToolOperationObservation(context.Background(), wrong, "running", 1, nil); err == nil {
		t.Fatal("foreign call accepted")
	}
	wrong = observer
	wrong.BootID = "boot-two"
	if _, err := repo.AppendToolOperationObservation(context.Background(), wrong, "running", 1, nil); err == nil {
		t.Fatal("foreign observer accepted")
	}
	details := map[string]any{"progress": map[string]any{"phase": "downloading_packages", "bytesCompleted": 1024, "bytesTotal": 2048, "elapsedMs": 10}}
	event, err := repo.AppendToolOperationObservation(context.Background(), observer, "running", 1, details)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := repo.AppendToolOperationObservation(context.Background(), observer, "running", 1, details)
	if err != nil || replay.EventID != event.EventID {
		t.Fatalf("observation retry: %v", err)
	}
	if _, err := repo.AppendToolOperationObservation(context.Background(), observer, "running", 3, details); err == nil {
		t.Fatal("skipped ordinal accepted")
	}
	recovered, err := repo.RecoverInterruptedToolObservations(context.Background(), "boot-one")
	if err != nil || len(recovered) != 0 {
		t.Fatalf("live operation recovered: %v %v", recovered, err)
	}
	recovered, err = repo.RecoverInterruptedToolObservations(context.Background(), "boot-two")
	if err != nil || len(recovered) != 1 {
		t.Fatalf("recovery: %v %v", recovered, err)
	}
	var payload map[string]any
	_ = json.Unmarshal(recovered[0].PayloadJSON, &payload)
	result, _ := payload["toolResult"].(map[string]any)
	if result["status"] != "outcome_unknown" {
		t.Fatalf("invented outcome: %#v", payload)
	}
	recovered, err = repo.RecoverInterruptedToolObservations(context.Background(), "boot-two")
	if err != nil || len(recovered) != 0 {
		t.Fatal("recovery not idempotent", err)
	}
	if _, err := repo.AppendToolOperationObservation(context.Background(), observer, "running", 3, details); err == nil {
		t.Fatal("late progress reopened terminal operation")
	}
}
