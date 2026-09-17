package transcript

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestRunnerTaskMemorySnapshotIsImmutablePrivateCheckpointState(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	seedTranscriptInput(t, repo, "stream-memory-snapshot", "owner-a")
	claimed, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: "stream-memory-snapshot", OwnerID: "owner-a", RunnerID: "runner-a",
		TTL: time.Minute, ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	snapshot, err := SealRunnerTaskMemorySnapshot(RunnerTaskMemorySnapshot{
		TaskIntentID: "intent-a", TaskIntentRevision: 1, InitialInputRevision: 1,
		SessionMemory: "stable session context", WorkspaceMemory: "stable user memory",
		PolicyVersion: "runner-task-memory-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, _, created, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: claimed.Claim, ClientMessageID: "task-memory-snapshot", Phase: RunnerPhasePlanning,
		Resumable: true, PayloadJSON: raw,
	})
	if err != nil || !created || checkpoint.Sequence <= 0 {
		t.Fatalf("checkpoint=%#v created=%t err=%v", checkpoint, created, err)
	}
	loaded, found, err := repo.GetRunnerTaskMemorySnapshot(
		context.Background(), claimed.Claim.StreamUID, claimed.Claim.OwnerID, claimed.Claim.Attempt,
	)
	if err != nil || !found || loaded != snapshot {
		t.Fatalf("loaded=%#v found=%t err=%v", loaded, found, err)
	}
	if _, err := db.Exec(`UPDATE transcript_events
		SET payload_json=json_set(payload_json,'$.workspace_memory','tampered')
		WHERE stream_uid=? AND event_id=?`, claimed.Claim.StreamUID, checkpoint.EventID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.GetRunnerTaskMemorySnapshot(
		context.Background(), claimed.Claim.StreamUID, claimed.Claim.OwnerID, claimed.Claim.Attempt,
	); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("tampered snapshot error=%v", err)
	}
}

func TestRunnerTaskMemorySnapshotScopesRecoveryToActiveTaskIntent(t *testing.T) {
	repo, _, _ := newTranscriptRepository(t)
	seedTranscriptInput(t, repo, "stream-memory-task-scope", "owner-a")
	claimed, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: "stream-memory-task-scope", OwnerID: "owner-a", RunnerID: "runner-a",
		TTL: time.Minute, ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	appendSnapshot := func(clientID, taskID string, revision int64) RunnerTaskMemorySnapshot {
		t.Helper()
		snapshot, sealErr := SealRunnerTaskMemorySnapshot(RunnerTaskMemorySnapshot{
			TaskIntentID: taskID, TaskIntentRevision: revision, InitialInputRevision: revision,
			SessionMemory: taskID, PolicyVersion: "runner-task-memory-v1",
		})
		if sealErr != nil {
			t.Fatal(sealErr)
		}
		raw, marshalErr := json.Marshal(snapshot)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if _, _, created, appendErr := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
			Claim: claimed.Claim, ClientMessageID: clientID, Phase: RunnerPhasePlanning,
			Resumable: true, PayloadJSON: raw,
		}); appendErr != nil || !created {
			t.Fatalf("snapshot append created=%t err=%v", created, appendErr)
		}
		return snapshot
	}
	first := appendSnapshot("task-memory-snapshot-a", "intent-a", 1)
	second := appendSnapshot("task-memory-snapshot-b", "intent-b", 2)
	if _, _, err := repo.GetRunnerTaskMemorySnapshot(
		context.Background(), claimed.Claim.StreamUID, claimed.Claim.OwnerID, claimed.Claim.Attempt,
	); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("unscoped snapshot error=%v", err)
	}
	for _, want := range []RunnerTaskMemorySnapshot{first, second} {
		got, found, err := repo.GetRunnerTaskMemorySnapshotForTask(
			context.Background(), claimed.Claim.StreamUID, claimed.Claim.OwnerID, claimed.Claim.Attempt,
			want.TaskIntentID, want.TaskIntentRevision,
		)
		if err != nil || !found || got != want {
			t.Fatalf("scoped snapshot=%#v found=%t err=%v want=%#v", got, found, err, want)
		}
		latest, latestFound, latestErr := repo.LatestRunnerTaskMemorySnapshotForTask(
			context.Background(), claimed.Claim.StreamUID, claimed.Claim.OwnerID,
			want.TaskIntentID, want.TaskIntentRevision,
		)
		if latestErr != nil || !latestFound || latest != want {
			t.Fatalf("latest scoped snapshot=%#v found=%t err=%v want=%#v", latest, latestFound, latestErr, want)
		}
	}
}
