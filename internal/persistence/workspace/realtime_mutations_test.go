package workspace

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRealtimeMutationRollsBackBusinessRowWhenEnqueueFails(t *testing.T) {
	store := openRealtimeMutationStore(t)
	err := store.WithTransaction(context.Background(), func(tx *sql.Tx) error {
		project, err := store.CreateProjectOutboxTx(context.Background(), tx, CreateProjectInput{
			ID: "project-rollback", UserID: "owner-a", Name: "must roll back",
		})
		if err != nil {
			return err
		}
		_, err = store.EnqueueRealtimeOutboxTx(context.Background(), tx, RealtimeEventInput{
			ID: "realtime-too-large", UserID: "owner-a", ProjectID: project.ID,
			Type: "frame_update", Payload: map[string]any{"project_id": project.ID, "data": strings.Repeat("x", maxRealtimeEventPayloadBytes+1)},
		}, "")
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "payload exceeds") {
		t.Fatalf("enqueue error = %v", err)
	}
	if _, found, err := store.GetProject("project-rollback"); err != nil || found {
		t.Fatalf("rolled back project found=%v err=%v", found, err)
	}
	if count, err := store.CountOutboxEvents(context.Background()); err != nil || count != 0 {
		t.Fatalf("outbox count=%d err=%v", count, err)
	}
}

func TestFrameRealtimeMutationCommitsFrameJournalAndOutboxTogether(t *testing.T) {
	store := openRealtimeMutationStore(t)
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-a", UserID: "owner-a", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrameRealtime(context.Background(), CreateFrameInput{
		ID: "frame-a", ProjectID: "project-a", AgentName: "planner", Status: "running", ConversationType: "task",
	}, "owner-a", "realtime-frame-a", "journal-frame-a")
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.ListFrameEvents(frame.ID, 0, 10)
	if err != nil || len(events) != 1 || events[0].ID != "journal-frame-a" {
		t.Fatalf("frame events=%#v err=%v", events, err)
	}
	if _, found, err := store.GetRealtimeEventByID("realtime-frame-a"); err != nil || found {
		t.Fatalf("realtime materialized before delivery found=%v err=%v", found, err)
	}
	outboxID := DeriveOutboxEventID(RealtimeOutboxTopic, "realtime-frame-a")
	outboxEvent, err := store.GetOutboxEvent(context.Background(), outboxID)
	if err != nil || outboxEvent.Status != OutboxStatusPending {
		t.Fatalf("outbox event=%#v err=%v", outboxEvent, err)
	}
}

func TestRoutineRealtimeMutationEnforcesRootFrameOwner(t *testing.T) {
	store := openRealtimeMutationStore(t)
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-a", UserID: "owner-a", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{ID: "frame-a", ProjectID: "project-a", AgentName: "planner", Status: "running", ConversationType: "task"}); err != nil {
		t.Fatal(err)
	}
	_, err := store.CreateRoutineRealtime(context.Background(), CreateRoutineInput{
		ID: "routine-cross-owner", RootFrameID: "frame-a", OwnerUserID: "owner-b",
		OnTick: "continue", EveryMinutes: 5, Enabled: true, NextDue: time.Now().UTC(),
	}, "routine-cross-owner-event")
	if err == nil || !strings.Contains(err.Error(), "does not own root frame") {
		t.Fatalf("cross-owner routine error = %v", err)
	}
	if count, err := store.CountOutboxEvents(context.Background()); err != nil || count != 0 {
		t.Fatalf("cross-owner outbox count=%d err=%v", count, err)
	}
}

func TestRoutineCommitUnknownClaimAndRepeatedCompletionAreGenerationFenced(t *testing.T) {
	store := openRealtimeMutationStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-routine", UserID: "owner-a", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{ID: "frame-routine", ProjectID: "project-routine", AgentName: "planner", Status: "running", ConversationType: "task"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRoutineRealtime(context.Background(), CreateRoutineInput{
		ID: "routine-a", RootFrameID: "frame-routine", OwnerUserID: "owner-a",
		OnTick: "continue", EveryMinutes: 1, Enabled: true, NextDue: now.Add(-time.Second),
	}, ""); err != nil {
		t.Fatal(err)
	}
	claim, claimed, err := store.ClaimNextDueRoutineRealtime(context.Background(), now, time.Minute, "owner-a", "stable-claim-token")
	if err != nil || !claimed || claim.ClaimGeneration != 1 || claim.LockedAt == nil {
		t.Fatalf("first claim=%#v claimed=%v err=%v", claim, claimed, err)
	}
	retried, retriedClaimed, err := store.ClaimNextDueRoutineRealtime(context.Background(), now.Add(time.Second), time.Minute, "owner-a", "stable-claim-token")
	if err != nil || !retriedClaimed || retried.ClaimGeneration != claim.ClaimGeneration || !retried.LockedAt.Equal(*claim.LockedAt) {
		t.Fatalf("retried claim=%#v claimed=%v err=%v", retried, retriedClaimed, err)
	}
	if _, err := store.RecordRoutineHostDone(context.Background(), claim.RootFrameID, claim.OwnerUserID, claim, false, "idle"); err != nil {
		t.Fatal(err)
	}
	completed, err := store.CompleteRoutineTickRealtime(context.Background(), claim, now.Add(2*time.Second), true, "completed")
	if err != nil || completed.TickCount != 1 || completed.IdleStreak != 1 || completed.LockedAt != nil {
		t.Fatalf("completed routine=%#v err=%v", completed, err)
	}
	repeated, err := store.CompleteRoutineTickRealtime(context.Background(), claim, now.Add(3*time.Second), true, "completed")
	if !errors.Is(err, ErrRoutineCompletionConflict) {
		t.Fatalf("changed duplicate completion error=%v", err)
	}
	repeated, err = store.CompleteRoutineTickRealtime(context.Background(), claim, now.Add(2*time.Second), true, "completed")
	if err != nil || repeated.TickCount != 1 || repeated.IdleStreak != 1 {
		t.Fatalf("repeated completion=%#v err=%v", repeated, err)
	}
	claimEventsID := DeriveOutboxEventID(RealtimeOutboxTopic, routineRealtimeEventID(claim, "claimed"))
	completionID := DeriveOutboxEventID(RealtimeOutboxTopic, routineRealtimeEventID(claim, "completed"))
	if _, err := store.GetOutboxEvent(context.Background(), claimEventsID); err != nil {
		t.Fatalf("stable claim event: %v", err)
	}
	if _, err := store.GetOutboxEvent(context.Background(), completionID); err != nil {
		t.Fatalf("stable completion event: %v", err)
	}

	secondClaim, claimed, err := store.ClaimNextDueRoutineRealtime(context.Background(), completed.NextDue.Add(time.Second), time.Minute, "owner-a", "second-claim-token")
	if err != nil || !claimed || secondClaim.ClaimGeneration != 2 {
		t.Fatalf("second claim=%#v claimed=%v err=%v", secondClaim, claimed, err)
	}
	if _, err := store.CompleteRoutineTickRealtime(context.Background(), claim, secondClaim.NextDue, true, "stale"); !errors.Is(err, ErrRoutineClaimLost) {
		t.Fatalf("stale completion error=%v", err)
	}
	current, err := store.GetRoutine("routine-a")
	if err != nil || current.ClaimGeneration != 2 || current.LockedAt == nil || current.TickCount != 1 {
		t.Fatalf("current routine after stale completion=%#v err=%v", current, err)
	}
}

func openRealtimeMutationStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(t.TempDir() + "/workspace.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
