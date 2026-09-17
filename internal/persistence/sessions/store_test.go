package sessions

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStorePersistsAndReloadsSessions(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)

	createdAt := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(2 * time.Minute)
	if err := store.Upsert(Session{
		ID:        "session-1",
		Title:     "Go rewrite",
		WorkDir:   "/home/victor_1/.synon-go-v4.0.2",
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if err := store.AppendMessage("session-1", "user"); err != nil {
		t.Fatalf("AppendMessage(user) error = %v", err)
	}
	if err := store.AppendMessage("session-1", "assistant"); err != nil {
		t.Fatalf("AppendMessage(assistant) error = %v", err)
	}

	fresh := NewStore(root)
	session, ok, err := fresh.Get("session-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !ok {
		t.Fatal("session-1 should exist")
	}
	if session.Title != "Go rewrite" || session.MessageCount != 2 || session.WorkDir == "" {
		t.Fatalf("session = %#v", session)
	}
	if session.CreatedAt.IsZero() || session.UpdatedAt.Before(updatedAt) {
		t.Fatalf("session timestamps = %#v", session)
	}
}

func TestStoreReconcileMessageStatsIsMonotonicAndRepairsMissingProjection(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Upsert(Session{ID: "projection", Title: "Projection"}); err != nil {
		t.Fatal(err)
	}
	firstUserAt := time.Date(2026, 7, 23, 1, 0, 0, 0, time.UTC)
	if err := store.ReconcileMessageStats("projection", 2, "assistant", firstUserAt); err != nil {
		t.Fatal(err)
	}
	if err := store.ReconcileMessageStats("projection", 1, "user", firstUserAt.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	session, found, err := store.Get("projection")
	if err != nil || !found || session.MessageCount != 2 || session.LastRole != "assistant" || !session.LastUserMessageAt.Equal(firstUserAt) {
		t.Fatalf("session=%#v found=%t err=%v", session, found, err)
	}
	if err := store.ReconcileMessageStats("projection", 3, "user", firstUserAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	session, _, err = store.Get("projection")
	if err != nil || session.MessageCount != 3 || session.LastRole != "user" || !session.LastUserMessageAt.Equal(firstUserAt.Add(time.Minute)) {
		t.Fatalf("repaired session=%#v err=%v", session, err)
	}
}

func TestStoreDeletesSessionDurablyAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	if err := store.Save(Session{ID: "delete-me", Title: "Temporary"}); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.Delete("delete-me")
	if err != nil || !deleted {
		t.Fatalf("Delete() deleted=%t err=%v", deleted, err)
	}
	if _, found, err := NewStore(root).Get("delete-me"); err != nil || found {
		t.Fatalf("deleted session reloaded: found=%t err=%v", found, err)
	}
	deleted, err = store.Delete("delete-me")
	if err != nil || deleted {
		t.Fatalf("idempotent Delete() deleted=%t err=%v", deleted, err)
	}
	if _, err := store.Delete(""); err == nil {
		t.Fatal("Delete(empty) should fail")
	}
}

func TestStoreListsSessionsByUpdatedAtDescending(t *testing.T) {
	store := NewStore(t.TempDir())
	base := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)

	for _, session := range []Session{
		{ID: "old", Title: "Old", CreatedAt: base, UpdatedAt: base.Add(time.Minute)},
		{ID: "new", Title: "New", CreatedAt: base, UpdatedAt: base.Add(3 * time.Minute)},
		{ID: "middle", Title: "Middle", CreatedAt: base, UpdatedAt: base.Add(2 * time.Minute)},
	} {
		if err := store.Upsert(session); err != nil {
			t.Fatalf("Upsert(%s) error = %v", session.ID, err)
		}
	}

	list, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	got := []string{list[0].ID, list[1].ID, list[2].ID}
	want := []string{"new", "middle", "old"}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("List order = %#v, want %#v", got, want)
		}
	}
}

func TestStoreRejectsInvalidSessionAndMissingAppendTarget(t *testing.T) {
	store := NewStore(t.TempDir())

	if err := store.Upsert(Session{}); err == nil {
		t.Fatal("Upsert(empty) should fail")
	}
	if err := store.AppendMessage("missing", "user"); err == nil {
		t.Fatal("AppendMessage(missing) should fail")
	}
}

func TestStoreClaimsHeartbeatsReleasesAndExpiresRunnerLease(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }

	if err := store.Upsert(Session{ID: "session-runner", Title: "Runner"}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	session, claimed, err := store.ClaimRunner("session-runner", "runner-a", time.Minute)
	if err != nil {
		t.Fatalf("ClaimRunner(runner-a) error = %v", err)
	}
	if !claimed || session.Runner == nil || session.Runner.RunnerID != "runner-a" || session.Runner.Status != "running" || session.Runner.Attempt != 1 {
		t.Fatalf("first claim = session=%#v claimed=%v", session, claimed)
	}
	if session.Runner.ExpiresAt != now.Add(time.Minute) || session.Runner.LastHeartbeatAt != now {
		t.Fatalf("first runner timestamps = %#v", session.Runner)
	}

	conflict, claimed, err := store.ClaimRunner("session-runner", "runner-b", time.Minute)
	if err != nil {
		t.Fatalf("ClaimRunner(runner-b conflict) error = %v", err)
	}
	if claimed || conflict.Runner == nil || conflict.Runner.RunnerID != "runner-a" {
		t.Fatalf("conflicting claim = session=%#v claimed=%v", conflict, claimed)
	}

	now = now.Add(30 * time.Second)
	claim := RunnerClaimFromSession(session)
	renewed, ok, err := store.HeartbeatRunner(claim, 2*time.Minute)
	if err != nil {
		t.Fatalf("HeartbeatRunner(runner-a) error = %v", err)
	}
	if !ok || renewed.Runner == nil || renewed.Runner.ExpiresAt != now.Add(2*time.Minute) || renewed.Runner.LastHeartbeatAt != now || renewed.Runner.Attempt != 1 {
		t.Fatalf("heartbeat = session=%#v ok=%v", renewed, ok)
	}

	wrongClaim := claim
	wrongClaim.RunnerID = "runner-b"
	wrongHeartbeat, ok, err := store.HeartbeatRunner(wrongClaim, time.Minute)
	if !errors.Is(err, ErrRunnerClaimStale) || ok || wrongHeartbeat.Runner == nil || wrongHeartbeat.Runner.RunnerID != "runner-a" {
		t.Fatalf("wrong heartbeat = session=%#v ok=%v", wrongHeartbeat, ok)
	}

	wrongRelease, released, err := store.ReleaseRunner(wrongClaim)
	if !errors.Is(err, ErrRunnerClaimStale) || released || wrongRelease.Runner == nil || wrongRelease.Runner.RunnerID != "runner-a" {
		t.Fatalf("wrong release = session=%#v released=%v", wrongRelease, released)
	}

	now = now.Add(3 * time.Minute)
	expiredClaim, claimed, err := store.ClaimRunner("session-runner", "runner-b", time.Minute)
	if err != nil {
		t.Fatalf("ClaimRunner(runner-b expired) error = %v", err)
	}
	if !claimed || expiredClaim.Runner == nil || expiredClaim.Runner.RunnerID != "runner-b" || expiredClaim.Runner.Attempt != 2 {
		t.Fatalf("expired reclaim = session=%#v claimed=%v", expiredClaim, claimed)
	}

	releasedSession, released, err := store.ReleaseRunner(RunnerClaimFromSession(expiredClaim))
	if err != nil {
		t.Fatalf("ReleaseRunner(runner-b owner) error = %v", err)
	}
	if !released || releasedSession.Runner != nil {
		t.Fatalf("owner release = session=%#v released=%v", releasedSession, released)
	}
}

func TestStoreRunnerMutationClaimSurvivesRestartAndFencesReclaim(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 7, 22, 11, 0, 0, 0, time.UTC)
	store := NewStore(root)
	store.now = func() time.Time { return now }
	if err := store.Upsert(Session{ID: "durable-claim", LastRole: "user"}); err != nil {
		t.Fatal(err)
	}
	first, claimed, err := store.ClaimRunner("durable-claim", "runner-a", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("first claim=%#v claimed=%t err=%v", first, claimed, err)
	}
	firstClaim := RunnerClaimFromSession(first)
	if firstClaim.Attempt != 1 || firstClaim.ClaimToken == "" {
		t.Fatalf("first credential=%#v", firstClaim)
	}

	reopened := NewStore(root)
	reopened.now = func() time.Time { return now }
	if _, err := reopened.ValidateRunnerClaim(firstClaim, true); err != nil {
		t.Fatalf("reopened ValidateRunnerClaim() error=%v", err)
	}
	tampered := firstClaim
	tampered.ClaimToken = "not-the-durable-token"
	if _, err := reopened.ValidateRunnerClaim(tampered, true); !errors.Is(err, ErrRunnerClaimStale) || strings.Contains(err.Error(), firstClaim.ClaimToken) {
		t.Fatalf("tampered claim error=%v", err)
	}

	now = now.Add(2 * time.Minute)
	second, claimed, err := reopened.ClaimRunner("durable-claim", "runner-a", time.Minute)
	if err != nil || !claimed || second.Runner == nil || second.Runner.Attempt != 2 {
		t.Fatalf("reclaim=%#v claimed=%t err=%v", second, claimed, err)
	}
	secondClaim := RunnerClaimFromSession(second)
	if secondClaim.ClaimToken == "" || secondClaim.ClaimToken == firstClaim.ClaimToken {
		t.Fatalf("reclaim credential first=%#v second=%#v", firstClaim, secondClaim)
	}
	if _, renewed, err := reopened.HeartbeatRunner(firstClaim, time.Minute); !errors.Is(err, ErrRunnerClaimStale) || renewed {
		t.Fatalf("stale heartbeat renewed=%t err=%v", renewed, err)
	}
	if _, released, err := reopened.ReleaseRunner(firstClaim); !errors.Is(err, ErrRunnerClaimStale) || released {
		t.Fatalf("stale release released=%t err=%v", released, err)
	}
	if _, err := reopened.CheckpointRunner(CheckpointRunnerInput{Claim: firstClaim, Status: "running", EventID: 7, RecordedAt: now}); !errors.Is(err, ErrRunnerClaimStale) {
		t.Fatalf("stale checkpoint error=%v", err)
	}
	current, found, err := reopened.Get("durable-claim")
	if err != nil || !found || current.Runner == nil || current.Runner.Attempt != secondClaim.Attempt || RunnerClaimToken(current.ID, current.Runner) != secondClaim.ClaimToken {
		t.Fatalf("current claim=%#v found=%t err=%v", current, found, err)
	}
}

func TestStoreExpiredRunnerMayHeartbeatUntilAnotherClaimIsFenced(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	store := NewStore(root)
	store.now = func() time.Time { return now }
	if err := store.Upsert(Session{ID: "delayed-heartbeat", LastRole: "user"}); err != nil {
		t.Fatal(err)
	}
	first, claimed, err := store.ClaimRunner("delayed-heartbeat", "runner-a", time.Second)
	if err != nil || !claimed {
		t.Fatalf("first claim=%#v claimed=%t err=%v", first, claimed, err)
	}
	firstClaim := RunnerClaimFromSession(first)

	now = now.Add(2 * time.Second)
	renewed, ok, err := store.HeartbeatRunner(firstClaim, time.Minute)
	if err != nil || !ok || renewed.Runner == nil || renewed.Runner.Attempt != firstClaim.Attempt || renewed.Runner.ExpiresAt != now.Add(time.Minute) {
		t.Fatalf("delayed heartbeat=%#v renewed=%t err=%v", renewed, ok, err)
	}

	now = now.Add(2 * time.Minute)
	second, claimed, err := store.ClaimRunner("delayed-heartbeat", "runner-b", time.Minute)
	if err != nil || !claimed || second.Runner == nil || second.Runner.Attempt != firstClaim.Attempt+1 {
		t.Fatalf("reclaimed session=%#v claimed=%t err=%v", second, claimed, err)
	}
	if _, ok, err := store.HeartbeatRunner(firstClaim, time.Minute); !errors.Is(err, ErrRunnerClaimStale) || ok {
		t.Fatalf("fenced heartbeat renewed=%t err=%v", ok, err)
	}
}

func TestStoreSaveProjectionPreservingRunnerCannotRollBackHeartbeat(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 7, 30, 1, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	if err := store.Upsert(Session{ID: "projection-runner", Title: "before", LastRole: "user"}); err != nil {
		t.Fatal(err)
	}
	claimedSession, claimed, err := store.ClaimRunner("projection-runner", "runner-a", time.Minute)
	if err != nil || !claimed || claimedSession.Runner == nil {
		t.Fatalf("claim=%#v claimed=%t err=%v", claimedSession, claimed, err)
	}
	staleProjection := claimedSession
	now = now.Add(20 * time.Second)
	renewed, ok, err := store.HeartbeatRunner(RunnerClaimFromSession(claimedSession), 3*time.Minute)
	if err != nil || !ok || renewed.Runner == nil {
		t.Fatalf("heartbeat=%#v renewed=%t err=%v", renewed, ok, err)
	}
	staleProjection.Title = "after"
	staleProjection.Runner.ExpiresAt = claimedSession.Runner.ExpiresAt
	saved, err := store.SaveProjectionPreservingRunner(staleProjection)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Title != "after" || saved.Runner == nil || !saved.Runner.ExpiresAt.Equal(renewed.Runner.ExpiresAt) ||
		!saved.Runner.LastHeartbeatAt.Equal(renewed.Runner.LastHeartbeatAt) {
		t.Fatalf("projection save rolled back runner: saved=%#v renewed=%#v", saved, renewed)
	}
}

func TestStoreExpiredSameRunnerIDStartsNewAttempt(t *testing.T) {
	for _, claim := range []struct {
		name string
		run  func(*Store) (Session, bool, error)
	}{
		{name: "direct", run: func(store *Store) (Session, bool, error) {
			return store.ClaimRunner("same-runner", "runner-a", time.Minute)
		}},
		{name: "next", run: func(store *Store) (Session, bool, error) {
			return store.ClaimNextRunner("runner-a", time.Minute)
		}},
	} {
		t.Run(claim.name, func(t *testing.T) {
			store := NewStore(t.TempDir())
			now := time.Date(2026, 7, 22, 9, 45, 0, 0, time.UTC)
			store.now = func() time.Time { return now }
			if err := store.Upsert(Session{ID: "same-runner", LastRole: "user"}); err != nil {
				t.Fatal(err)
			}
			first, claimed, err := claim.run(store)
			if err != nil || !claimed || first.Runner == nil || first.Runner.Attempt != 1 {
				t.Fatalf("first claim=%#v claimed=%t err=%v", first, claimed, err)
			}
			now = now.Add(30 * time.Second)
			live, claimed, err := claim.run(store)
			if err != nil || claimed || live.Runner == nil || live.Runner.Attempt != 1 ||
				!live.Runner.ClaimedAt.Equal(first.Runner.ClaimedAt) || !live.Runner.ExpiresAt.Equal(first.Runner.ExpiresAt) ||
				RunnerClaimToken(live.ID, live.Runner) != RunnerClaimToken(first.ID, first.Runner) {
				t.Fatalf("live repeat=%#v claimed=%t err=%v", live, claimed, err)
			}
			now = now.Add(2 * time.Minute)
			reclaimed, claimed, err := claim.run(store)
			if err != nil || !claimed || reclaimed.Runner == nil || reclaimed.Runner.Attempt != 2 ||
				!reclaimed.Runner.ReclaimedExpiredLease || reclaimed.Runner.PreviousRunnerID != "runner-a" {
				t.Fatalf("expired same-id reclaim=%#v claimed=%t err=%v", reclaimed, claimed, err)
			}
		})
	}
}

func TestStoreConcurrentSameRunnerClaimHasOneDurableWinner(t *testing.T) {
	for _, claimNext := range []bool{false, true} {
		name := "direct"
		if claimNext {
			name = "next"
		}
		t.Run(name, func(t *testing.T) {
			store := NewStore(t.TempDir())
			now := time.Date(2026, 7, 29, 16, 0, 0, 0, time.UTC)
			store.now = func() time.Time { return now }
			if err := store.Upsert(Session{ID: "claim-a", LastRole: "user", UpdatedAt: now.Add(-time.Minute)}); err != nil {
				t.Fatal(err)
			}
			if claimNext {
				if err := store.Upsert(Session{ID: "claim-b", LastRole: "user", UpdatedAt: now}); err != nil {
					t.Fatal(err)
				}
			}
			start := make(chan struct{})
			type outcome struct {
				session Session
				claimed bool
				err     error
			}
			outcomes := make(chan outcome, 2)
			var wait sync.WaitGroup
			for index := 0; index < 2; index++ {
				wait.Add(1)
				go func() {
					defer wait.Done()
					<-start
					var session Session
					var claimed bool
					var err error
					if claimNext {
						session, claimed, err = store.ClaimNextRunner("runner-a", time.Minute)
					} else {
						session, claimed, err = store.ClaimRunner("claim-a", "runner-a", time.Minute)
					}
					outcomes <- outcome{session: session, claimed: claimed, err: err}
				}()
			}
			close(start)
			wait.Wait()
			close(outcomes)
			winners := 0
			var token string
			for result := range outcomes {
				if result.err != nil || result.session.ID != "claim-a" || result.session.Runner == nil {
					t.Fatalf("outcome=%#v", result)
				}
				if result.claimed {
					winners++
				}
				currentToken := RunnerClaimToken(result.session.ID, result.session.Runner)
				if token == "" {
					token = currentToken
				} else if currentToken != token {
					t.Fatalf("claim tokens diverged: %q != %q", currentToken, token)
				}
			}
			if winners != 1 {
				t.Fatalf("winners=%d, want 1", winners)
			}
			if claimNext {
				queued, found, err := store.Get("claim-b")
				if err != nil || !found || queued.Runner != nil {
					t.Fatalf("second pending session was claimed: %#v found=%t err=%v", queued, found, err)
				}
			}
		})
	}
}

func TestStoreFinalizeRunnerUsesAttemptEventAndChronologyCAS(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	now := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	claimedAt := now.Add(-2 * time.Minute)
	heartbeatAt := now.Add(-time.Minute)
	if err := store.Save(Session{ID: "finish", LastRole: "user", Runner: &Runner{
		RunnerID: "runner-a", Status: "waiting", Attempt: 2, ClaimedAt: claimedAt,
		LastHeartbeatAt: heartbeatAt, ExpiresAt: now.Add(time.Minute),
	}}); err != nil {
		t.Fatal(err)
	}
	input := FinalizeRunnerInput{
		SessionID: "finish", RunnerID: "runner-a", Attempt: 2, Status: "completed",
		Checkpoint: "done", CheckpointAt: now, EventID: 7, FinishedAt: now,
	}
	finished, applied, err := store.FinalizeRunner(input)
	if err != nil || !applied || finished.Runner == nil || finished.Runner.Status != "completed" ||
		finished.Runner.LastCheckpointEventID != 7 || finished.Runner.LastCheckpoint != "done" ||
		!finished.Runner.ExpiresAt.Equal(now) {
		t.Fatalf("FinalizeRunner() session=%#v applied=%t err=%v", finished, applied, err)
	}
	reopened := NewStore(root)
	reopened.now = func() time.Time { return now }
	again, applied, err := reopened.FinalizeRunner(input)
	if err != nil || applied || again.Runner == nil || again.Runner.LastCheckpointEventID != 7 {
		t.Fatalf("idempotent finalize session=%#v applied=%t err=%v", again, applied, err)
	}
	for name, mutate := range map[string]func(*FinalizeRunnerInput){
		"event":      func(input *FinalizeRunnerInput) { input.EventID++ },
		"status":     func(input *FinalizeRunnerInput) { input.Status = "failed" },
		"checkpoint": func(input *FinalizeRunnerInput) { input.Checkpoint = "different" },
	} {
		t.Run(name, func(t *testing.T) {
			conflict := input
			mutate(&conflict)
			if _, _, err := reopened.FinalizeRunner(conflict); !errors.Is(err, ErrRunnerFinalizeConflict) {
				t.Fatalf("conflict error = %v", err)
			}
		})
	}
	if err := reopened.Save(Session{ID: "finish", LastRole: "user", Runner: &Runner{
		RunnerID: "runner-a", Status: "running", Attempt: 3, ClaimedAt: now,
		ReclaimedExpiredLease: true, PreviousRunnerID: "runner-a", ExpiresAt: now.Add(time.Minute),
	}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reopened.FinalizeRunner(input); !errors.Is(err, ErrRunnerClaimStale) {
		t.Fatalf("stale attempt error = %v", err)
	}

	if err := reopened.Save(Session{ID: "finish-time", Runner: &Runner{
		RunnerID: "runner-time", Status: "running", Attempt: 1, ClaimedAt: claimedAt,
		LastHeartbeatAt: heartbeatAt, ExpiresAt: now.Add(time.Minute),
	}}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(reopened.path)
	if err != nil {
		t.Fatal(err)
	}
	invalidTimes := []FinalizeRunnerInput{
		{SessionID: "finish-time", RunnerID: "runner-time", Attempt: 1, Status: "completed", EventID: 10, FinishedAt: now.Add(time.Second)},
		{SessionID: "finish-time", RunnerID: "runner-time", Attempt: 1, Status: "completed", EventID: 10, FinishedAt: now, CheckpointAt: now.Add(time.Second)},
		{SessionID: "finish-time", RunnerID: "runner-time", Attempt: 1, Status: "completed", EventID: 10, FinishedAt: claimedAt.Add(-time.Second)},
		{SessionID: "finish-time", RunnerID: "runner-time", Attempt: 1, Status: "completed", EventID: 10, FinishedAt: now, CheckpointAt: heartbeatAt.Add(-time.Second)},
	}
	for _, invalid := range invalidTimes {
		if _, applied, err := reopened.FinalizeRunner(invalid); err == nil || applied {
			t.Fatalf("invalid finish time applied=%t err=%v input=%#v", applied, err, invalid)
		}
		after, err := os.ReadFile(reopened.path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(after, before) {
			t.Fatal("invalid finish time changed the durable session file")
		}
	}
}

func TestStoreFinalizeRunnerSupersedesOnlyProvenTerminalCheckpoint(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 7, 22, 10, 15, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	seed := func(id string) {
		t.Helper()
		if err := store.Save(Session{ID: id, LastRole: "user", Runner: &Runner{
			RunnerID: "runner-a", Status: "failed", Attempt: 2,
			ClaimedAt: now.Add(-time.Minute), LastHeartbeatAt: now.Add(-30 * time.Second),
			ExpiresAt: now.Add(time.Minute), LastCheckpoint: "tool failed", LastCheckpointAt: now.Add(-time.Second), LastCheckpointEventID: 6,
		}}); err != nil {
			t.Fatal(err)
		}
	}
	seed("checkpoint-conflict")
	input := FinalizeRunnerInput{
		SessionID: "checkpoint-conflict", RunnerID: "runner-a", Attempt: 2, Status: "completed",
		Checkpoint: "recovered", CheckpointAt: now, EventID: 7, FinishedAt: now,
	}
	if _, applied, err := store.FinalizeRunner(input); !errors.Is(err, ErrRunnerFinalizeConflict) || applied {
		t.Fatalf("unproven checkpoint supersession applied=%t err=%v", applied, err)
	}
	input.SupersedesCheckpointEventID = 5
	if _, applied, err := store.FinalizeRunner(input); !errors.Is(err, ErrRunnerFinalizeConflict) || applied {
		t.Fatalf("wrong checkpoint supersession applied=%t err=%v", applied, err)
	}
	input.SupersedesCheckpointEventID = 6
	finished, applied, err := store.FinalizeRunner(input)
	if err != nil || !applied || finished.Runner == nil || finished.Runner.Status != "completed" || finished.Runner.LastCheckpointEventID != 7 {
		t.Fatalf("proven checkpoint supersession session=%#v applied=%t err=%v", finished, applied, err)
	}
}

func TestStoreClaimsNextPendingRunnerSession(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 7, 4, 13, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }

	for _, session := range []Session{
		{ID: "assistant-done", Title: "Done", LastRole: "assistant", CreatedAt: now.Add(-20 * time.Minute), UpdatedAt: now.Add(-10 * time.Minute)},
		{ID: "oldest-user", Title: "Oldest", LastRole: "user", CreatedAt: now.Add(-15 * time.Minute), UpdatedAt: now.Add(-5 * time.Minute)},
		{ID: "newest-user", Title: "Newest", LastRole: "user", CreatedAt: now.Add(-10 * time.Minute), UpdatedAt: now.Add(-1 * time.Minute)},
	} {
		if err := store.Upsert(session); err != nil {
			t.Fatalf("Upsert(%s) error = %v", session.ID, err)
		}
	}

	first, claimed, err := store.ClaimNextRunner("runner-a", time.Minute)
	if err != nil {
		t.Fatalf("ClaimNextRunner(runner-a) error = %v", err)
	}
	if !claimed || first.ID != "oldest-user" || first.Runner == nil || first.Runner.RunnerID != "runner-a" || first.Runner.Attempt != 1 {
		t.Fatalf("first pick = session=%#v claimed=%v", first, claimed)
	}

	now = now.Add(10 * time.Second)
	renewed, claimed, err := store.ClaimNextRunner("runner-a", 2*time.Minute)
	if err != nil {
		t.Fatalf("ClaimNextRunner(runner-a renew) error = %v", err)
	}
	if claimed || renewed.ID != "oldest-user" || renewed.Runner.ClaimedAt != first.Runner.ClaimedAt ||
		renewed.Runner.ExpiresAt != first.Runner.ExpiresAt || renewed.Runner.Attempt != 1 {
		t.Fatalf("renewed pick = session=%#v claimed=%v", renewed, claimed)
	}

	second, claimed, err := store.ClaimNextRunner("runner-b", time.Minute)
	if err != nil {
		t.Fatalf("ClaimNextRunner(runner-b) error = %v", err)
	}
	if !claimed || second.ID != "newest-user" || second.Runner == nil || second.Runner.RunnerID != "runner-b" || second.Runner.Attempt != 1 {
		t.Fatalf("second pick = session=%#v claimed=%v", second, claimed)
	}

	none, claimed, err := store.ClaimNextRunner("runner-c", time.Minute)
	if err != nil {
		t.Fatalf("ClaimNextRunner(runner-c none) error = %v", err)
	}
	if claimed || none.ID != "" {
		t.Fatalf("no pending pick = session=%#v claimed=%v", none, claimed)
	}

	now = now.Add(3 * time.Minute)
	expired, claimed, err := store.ClaimNextRunner("runner-c", time.Minute)
	if err != nil {
		t.Fatalf("ClaimNextRunner(runner-c expired) error = %v", err)
	}
	if !claimed || expired.ID != "oldest-user" || expired.Runner == nil || expired.Runner.RunnerID != "runner-c" || expired.Runner.Attempt != 2 {
		t.Fatalf("expired pick = session=%#v claimed=%v", expired, claimed)
	}
}

func TestStoreClearsTerminalRunnerWhenNewUserMessageArrives(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 7, 4, 14, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }

	if err := store.Upsert(Session{
		ID:           "terminal",
		Title:        "Terminal",
		LastRole:     "user",
		MessageCount: 2,
		Runner: &Runner{
			RunnerID:              "runner-a",
			Status:                "completed",
			ClaimedAt:             now.Add(-2 * time.Minute),
			LastHeartbeatAt:       now.Add(-time.Minute),
			ExpiresAt:             now,
			LastCheckpoint:        "done",
			LastCheckpointAt:      now,
			LastCheckpointEventID: 2,
		},
	}); err != nil {
		t.Fatalf("Upsert(terminal) error = %v", err)
	}

	now = now.Add(time.Second)
	if err := store.AppendMessage("terminal", "user"); err != nil {
		t.Fatalf("AppendMessage(user) error = %v", err)
	}
	reopened, ok, err := store.Get("terminal")
	if err != nil {
		t.Fatalf("Get(terminal) error = %v", err)
	}
	if !ok {
		t.Fatal("terminal session should exist")
	}
	if reopened.Runner != nil || reopened.MessageCount != 3 || reopened.LastRole != "user" {
		t.Fatalf("new user message should reopen terminal runner state: %#v", reopened)
	}

	picked, claimed, err := store.ClaimNextRunner("runner-b", time.Minute)
	if err != nil {
		t.Fatalf("ClaimNextRunner(reopened) error = %v", err)
	}
	if !claimed || picked.ID != "terminal" || picked.Runner == nil || picked.Runner.RunnerID != "runner-b" || picked.Runner.Attempt != 1 {
		t.Fatalf("reopened pick = session=%#v claimed=%v", picked, claimed)
	}
}

func TestStoreTerminalFutureLeaseIsNeverLive(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 7, 22, 10, 30, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	terminal := func(id string) Session {
		return Session{ID: id, LastRole: "assistant", UpdatedAt: now, Runner: &Runner{
			RunnerID: "runner-a", Status: "completed", Attempt: 1,
			ClaimedAt: now.Add(-2 * time.Minute), LastHeartbeatAt: now.Add(-time.Minute),
			ExpiresAt: now.Add(time.Hour), LastCheckpointAt: now, LastCheckpointEventID: 7,
		}}
	}
	for _, id := range []string{"terminal-same", "terminal-other", "terminal-next"} {
		if err := store.Save(terminal(id)); err != nil {
			t.Fatal(err)
		}
	}
	heartbeat, renewed, err := store.HeartbeatRunner(RunnerClaimFromSession(terminal("terminal-same")), time.Minute)
	if !errors.Is(err, ErrRunnerClaimStale) || renewed || heartbeat.Runner == nil || heartbeat.Runner.Status != "completed" || heartbeat.Runner.Attempt != 1 {
		t.Fatalf("terminal heartbeat session=%#v renewed=%t err=%v", heartbeat, renewed, err)
	}
	snapshot, err := store.RunnerQueueSnapshot()
	if err != nil || snapshot.Running != 0 || snapshot.Terminal != 3 || snapshot.Pending != 0 {
		t.Fatalf("terminal future lease snapshot=%#v err=%v", snapshot, err)
	}
	if next, claimed, err := store.ClaimNextRunner("runner-a", time.Minute); err != nil || claimed || next.ID != "" {
		t.Fatalf("terminal ClaimNextRunner session=%#v claimed=%t err=%v", next, claimed, err)
	}
	same, claimed, err := store.ClaimRunner("terminal-same", "runner-a", time.Minute)
	if err != nil || !claimed || same.Runner == nil || same.Runner.Attempt != 2 || same.Runner.RunnerID != "runner-a" {
		t.Fatalf("same runner terminal claim=%#v claimed=%t err=%v", same, claimed, err)
	}
	other, claimed, err := store.ClaimRunner("terminal-other", "runner-b", time.Minute)
	if err != nil || !claimed || other.Runner == nil || other.Runner.Attempt != 2 || other.Runner.RunnerID != "runner-b" {
		t.Fatalf("different runner terminal claim=%#v claimed=%t err=%v", other, claimed, err)
	}
}

func TestStoreSummarizesRunnerQueue(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 7, 4, 15, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }

	sessions := []Session{
		{ID: "assistant-done", LastRole: "assistant", UpdatedAt: now.Add(-30 * time.Minute)},
		{ID: "pending-old", LastRole: "user", UpdatedAt: now.Add(-20 * time.Minute)},
		{ID: "pending-new", LastRole: "user", UpdatedAt: now.Add(-10 * time.Minute)},
		{
			ID:        "running",
			LastRole:  "user",
			UpdatedAt: now.Add(-9 * time.Minute),
			Runner: &Runner{
				RunnerID:  "runner-a",
				Status:    "running",
				Attempt:   1,
				ExpiresAt: now.Add(time.Minute),
			},
		},
		{
			ID:        "expired",
			LastRole:  "user",
			UpdatedAt: now.Add(-8 * time.Minute),
			Runner: &Runner{
				RunnerID:  "runner-b",
				Status:    "running",
				Attempt:   2,
				ExpiresAt: now.Add(-time.Minute),
			},
		},
		{
			ID:        "terminal",
			LastRole:  "user",
			UpdatedAt: now.Add(-7 * time.Minute),
			Runner: &Runner{
				RunnerID:  "runner-c",
				Status:    "completed",
				Attempt:   1,
				ExpiresAt: now.Add(-time.Minute),
			},
		},
	}
	for _, session := range sessions {
		if err := store.Upsert(session); err != nil {
			t.Fatalf("Upsert(%s) error = %v", session.ID, err)
		}
	}

	snapshot, err := store.RunnerQueueSnapshot()
	if err != nil {
		t.Fatalf("RunnerQueueSnapshot() error = %v", err)
	}
	if snapshot.TotalSessions != 6 || snapshot.Pending != 3 || snapshot.Running != 1 || snapshot.Expired != 1 || snapshot.Terminal != 1 {
		t.Fatalf("snapshot counts = %#v", snapshot)
	}
	if snapshot.OldestPending == nil || snapshot.OldestPending.ID != "pending-old" {
		t.Fatalf("oldest pending = %#v", snapshot.OldestPending)
	}
	if snapshot.OldestPendingAgeSeconds != int64((20 * time.Minute).Seconds()) {
		t.Fatalf("oldest pending age = %d", snapshot.OldestPendingAgeSeconds)
	}
}

func TestStoreBuildsRunnerBacklogPlan(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 7, 5, 9, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }

	sessions := []Session{
		{
			ID:                "pending-old",
			Title:             "Old project work",
			WorkDir:           "/workspace/alpha",
			LastRole:          "user",
			LastUserMessageAt: now.Add(-40 * time.Minute),
			UpdatedAt:         now.Add(-39 * time.Minute),
			MessageCount:      3,
			Project:           &Project{ID: "alpha", Name: "Alpha", Path: "projects/alpha", BoundAt: now.Add(-time.Hour)},
		},
		{
			ID:                "pending-new",
			Title:             "New project work",
			LastRole:          "user",
			LastUserMessageAt: now.Add(-10 * time.Minute),
			UpdatedAt:         now.Add(-9 * time.Minute),
			MessageCount:      1,
		},
		{
			ID:                "running",
			Title:             "Running project work",
			LastRole:          "user",
			LastUserMessageAt: now.Add(-20 * time.Minute),
			UpdatedAt:         now.Add(-2 * time.Minute),
			Runner: &Runner{
				RunnerID:         "runner-a",
				Status:           "running",
				Attempt:          2,
				ClaimedAt:        now.Add(-5 * time.Minute),
				LastHeartbeatAt:  now.Add(-30 * time.Second),
				ExpiresAt:        now.Add(90 * time.Second),
				LastCheckpoint:   "processing",
				LastCheckpointAt: now.Add(-time.Minute),
			},
		},
		{
			ID:                "expired",
			Title:             "Expired project work",
			LastRole:          "user",
			LastUserMessageAt: now.Add(-30 * time.Minute),
			UpdatedAt:         now.Add(-25 * time.Minute),
			Runner: &Runner{
				RunnerID:        "runner-b",
				Status:          "running",
				Attempt:         3,
				ClaimedAt:       now.Add(-35 * time.Minute),
				LastHeartbeatAt: now.Add(-20 * time.Minute),
				ExpiresAt:       now.Add(-5 * time.Minute),
			},
		},
		{
			ID:        "assistant-done",
			Title:     "No work",
			LastRole:  "assistant",
			UpdatedAt: now.Add(-15 * time.Minute),
		},
	}
	for _, session := range sessions {
		if err := store.Upsert(session); err != nil {
			t.Fatalf("Upsert(%s) error = %v", session.ID, err)
		}
	}

	backlog, err := store.RunnerBacklog(RunnerBacklogOptions{Limit: 3, IncludeRunning: true})
	if err != nil {
		t.Fatalf("RunnerBacklog() error = %v", err)
	}
	if backlog.TotalSessions != 5 || backlog.Returned != 3 || backlog.Pending != 3 || backlog.Running != 1 || backlog.Expired != 1 || backlog.Truncated != true {
		t.Fatalf("backlog counts = %#v", backlog)
	}
	got := []string{backlog.Items[0].SessionID, backlog.Items[1].SessionID, backlog.Items[2].SessionID}
	want := []string{"pending-old", "expired", "pending-new"}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("backlog order = %#v, want %#v", got, want)
		}
	}
	first := backlog.Items[0]
	if first.State != "pending" || first.Priority != 1 || first.AgeSeconds != int64((40*time.Minute).Seconds()) || first.NextAction != "claim" {
		t.Fatalf("first backlog item = %#v", first)
	}
	if first.Project == nil || first.Project.ID != "alpha" || first.WorkDir != "/workspace/alpha" {
		t.Fatalf("first project/workdir = %#v", first)
	}
	expired := backlog.Items[1]
	if expired.State != "expired" || expired.Attempt != 3 || expired.RunnerID != "runner-b" || expired.NextAction != "reclaim" {
		t.Fatalf("expired backlog item = %#v", expired)
	}
}

func TestStoreFiltersRunnerBacklogByProjectAndState(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }

	for _, session := range []Session{
		{
			ID:                "alpha-pending",
			Title:             "Alpha pending",
			LastRole:          "user",
			LastUserMessageAt: now.Add(-20 * time.Minute),
			UpdatedAt:         now.Add(-19 * time.Minute),
			Project:           &Project{ID: "alpha", Path: "projects/alpha", BoundAt: now.Add(-time.Hour)},
		},
		{
			ID:                "alpha-running",
			Title:             "Alpha running",
			LastRole:          "user",
			LastUserMessageAt: now.Add(-18 * time.Minute),
			UpdatedAt:         now.Add(-17 * time.Minute),
			Project:           &Project{ID: "alpha", Path: "projects/alpha", BoundAt: now.Add(-time.Hour)},
			Runner:            &Runner{RunnerID: "runner-a", Status: "running", Attempt: 1, ExpiresAt: now.Add(time.Minute)},
		},
		{
			ID:                "beta-pending",
			Title:             "Beta pending",
			LastRole:          "user",
			LastUserMessageAt: now.Add(-15 * time.Minute),
			UpdatedAt:         now.Add(-14 * time.Minute),
			Project:           &Project{ID: "beta", Path: "projects/beta", BoundAt: now.Add(-time.Hour)},
		},
	} {
		if err := store.Upsert(session); err != nil {
			t.Fatalf("Upsert(%s) error = %v", session.ID, err)
		}
	}

	backlog, err := store.RunnerBacklog(RunnerBacklogOptions{ProjectID: "alpha", State: "pending", IncludeRunning: true})
	if err != nil {
		t.Fatalf("RunnerBacklog(filtered) error = %v", err)
	}
	if backlog.Returned != 1 || len(backlog.Items) != 1 || backlog.Items[0].SessionID != "alpha-pending" {
		t.Fatalf("filtered backlog = %#v", backlog)
	}
	if backlog.Pending != 2 || backlog.Running != 1 {
		t.Fatalf("filter should not change global counts: %#v", backlog)
	}
}
