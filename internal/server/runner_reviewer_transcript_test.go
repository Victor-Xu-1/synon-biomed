package server

import (
	"context"
	"errors"
	"testing"
	"time"

	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestSessionReviewerFrameTerminalUsesTranscriptAndRecoversProjection(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner", "review-project", "review-root")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	closeServer := func(server *Server) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}
	frame, err := server.beginSessionReviewerFrame(context.Background(), sessionstore.Session{ID: "review-root"}, "review-model", 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "owner", frame.ID)
	if err != nil || !found {
		t.Fatalf("reviewer stream=%#v found=%t err=%v", stream, found, err)
	}
	state, found, err := repo.GetLatestRunnerRuntimeState(context.Background(), stream.UID, "owner")
	if err != nil || !found || state.Status != "running" || state.Phase != transcriptstore.RunnerPhaseClaimed {
		t.Fatalf("reviewer runtime=%#v found=%t err=%v", state, found, err)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata(frame.ID)
	if err != nil || !found || metadata.InputData["review_trigger"] != "auto" {
		t.Fatalf("reviewer metadata=%#v found=%t err=%v", metadata, found, err)
	}
	if _, err := db.Exec(`
		CREATE TRIGGER fail_reviewer_terminal_projection BEFORE INSERT ON frame_events
		WHEN NEW.event_type='verification_completed' BEGIN SELECT RAISE(ABORT, 'forced reviewer projection failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := server.finishSessionReviewerFrame(frame.ID, "completed", "review passed", 0); err == nil {
		t.Fatal("reviewer finish unexpectedly ignored the projection failure")
	}
	assertReviewerTranscriptTerminal(t, repo, stream, "completed", 1)
	stored, found, err := store.GetFrame(frame.ID)
	if err != nil || !found || stored.Status != "completed" {
		t.Fatalf("reviewer frame=%#v found=%t err=%v", stored, found, err)
	}
	if _, err := db.Exec(`DROP TRIGGER fail_reviewer_terminal_projection`); err != nil {
		t.Fatal(err)
	}
	closeServer(server)

	restarted := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() { closeServer(restarted) })
	if err := restarted.finishSessionReviewerFrame(frame.ID, "completed", "review passed", 0); err != nil {
		t.Fatal(err)
	}
	if err := restarted.finishSessionReviewerFrame(frame.ID, "completed", "different terminal detail", 0); !errors.Is(err, transcriptstore.ErrEventConflict) {
		t.Fatalf("conflicting reviewer terminal error=%v", err)
	}
	if err := restarted.finishSessionReviewerFrame(frame.ID, "completed", "review passed", 0); err != nil {
		t.Fatal(err)
	}
	assertReviewerTranscriptTerminal(t, repo, stream, "completed", 1)
	events, err := store.ListFrameEvents(frame.ID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	started, completed := 0, 0
	for _, event := range events {
		switch event.Type {
		case "verification_started":
			started++
		case "verification_completed":
			completed++
		}
	}
	if started != 1 || completed != 1 {
		t.Fatalf("reviewer events started=%d completed=%d events=%#v", started, completed, events)
	}
}

func TestCompatibilityReviewProjectionRequiresLiveReviewerRunnerAuthority(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner", "review-projection-project", "review-projection-root")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	root, found, err := store.GetFrame("review-projection-root")
	if err != nil || !found {
		t.Fatalf("root=%#v found=%t err=%v", root, found, err)
	}
	frame, err := server.beginSessionReviewerFrame(
		context.Background(), sessionstore.Session{ID: root.ID}, "review-model", 3, 0,
	)
	if err != nil {
		t.Fatal(err)
	}
	summary := transcriptstore.RunnerTimingSummary{Attempt: 3, StartedAt: root.CreatedAt}
	projection, err := server.compatibilityFrameReviewProjection(context.Background(), root, summary)
	if err != nil || projection["runtime_stage"] != "reviewing" ||
		projection["runtime_review_status"] != "processing" || projection["runtime_review_trigger"] != "auto" {
		t.Fatalf("live projection=%#v err=%v", projection, err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "owner", frame.ID)
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	if _, err := db.Exec(`UPDATE transcript_runner_attempts SET expires_at=? WHERE stream_uid=?`,
		time.Now().UTC().Add(-time.Minute), stream.UID); err != nil {
		t.Fatal(err)
	}
	projection, err = server.compatibilityFrameReviewProjection(context.Background(), root, summary)
	if err != nil || projection["runtime_stage"] != "review_failed" ||
		projection["runtime_review_status"] != "failed" || projection["runtime_review_trigger"] != "auto" {
		t.Fatalf("stale projection=%#v err=%v", projection, err)
	}
}

func TestSessionReviewerHeartbeatRenewsFencedClaim(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner", "review-heartbeat-project", "review-heartbeat-root")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	_, claim, err := server.beginSessionReviewerFrameWithClaim(
		context.Background(), sessionstore.Session{ID: "review-heartbeat-root"}, "review-model", 4, 0,
	)
	if err != nil {
		t.Fatal(err)
	}
	// The lease must accommodate real SQLite work under race instrumentation;
	// observe persisted renewal instead of assuming a tick within a short sleep.
	const ttl = 2 * time.Second
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	initialExpiry := time.Now().UTC().Add(ttl)
	if _, err := db.Exec(`UPDATE transcript_runner_attempts SET expires_at=? WHERE stream_uid=? AND attempt=?`,
		initialExpiry, claim.StreamUID, claim.Attempt); err != nil {
		t.Fatal(err)
	}
	claim.ExpiresAt = initialExpiry
	heartbeatCtx, stop := server.startSessionReviewerHeartbeatWithTTL(ctx, claim, ttl)
	defer stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, found, err := repo.GetLatestRunnerRuntimeState(heartbeatCtx, claim.StreamUID, claim.OwnerID)
		if err != nil || !found || state.Status != "running" {
			t.Fatalf("heartbeat state=%#v found=%t err=%v", state, found, err)
		}
		if state.ExpiresAt.After(initialExpiry) {
			break
		}
		select {
		case <-heartbeatCtx.Done():
			t.Fatalf("heartbeat stopped before durable renewal: %v", context.Cause(heartbeatCtx))
		case <-ticker.C:
		}
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	staleClaim := claim
	staleClaim.ClaimToken = "not-the-authoritative-reviewer-token"
	staleCtx, stopStale := server.startSessionReviewerHeartbeatWithTTL(ctx, staleClaim, ttl)
	defer stopStale()
	<-staleCtx.Done()
	if !errors.Is(context.Cause(staleCtx), transcriptstore.ErrClaimStale) {
		t.Fatalf("stale reviewer claim was not fenced: %v", context.Cause(staleCtx))
	}
	if err := stopStale(); !errors.Is(err, transcriptstore.ErrClaimStale) {
		t.Fatalf("stale reviewer heartbeat error=%v", err)
	}
}

func TestBindSessionReviewerRunUsesReviewerTranscriptClaim(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner", "review-bind-project", "review-bind-root")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	frame, claim, err := server.beginSessionReviewerFrameWithClaim(
		context.Background(), sessionstore.Session{ID: "review-bind-root"}, "review-model", 2, 0,
	)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := server.bindSessionReviewerRun(context.Background(), frame, claim)
	if err != nil {
		t.Fatal(err)
	}
	run, _ := bound.Value(transcriptRunnerChatRunContextKey{}).(*sessionRunnerChatRun)
	if run == nil || run.SessionID != frame.ID || run.Attempt != int(claim.Attempt) ||
		run.ClaimToken != claim.ClaimToken || run.Transcript == nil ||
		run.Transcript.Stream.FrameID != frame.ID || run.Transcript.Claim.StreamUID != claim.StreamUID {
		t.Fatalf("reviewer run=%#v frame=%#v claim=%#v", run, frame, claim)
	}
}

func TestSessionReviewerFrameFailureTerminalsUseTranscriptAuthority(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner", "review-terminal-project", "review-terminal-root")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	for index, status := range []string{"failed", "cancelled"} {
		frame, err := server.beginSessionReviewerFrame(
			context.Background(), sessionstore.Session{ID: "review-terminal-root"}, "review-model", 9, index,
		)
		if err != nil {
			t.Fatal(err)
		}
		stream, found, err := repo.GetFrameStreamBySession(context.Background(), "owner", frame.ID)
		if err != nil || !found {
			t.Fatalf("status=%s stream=%#v found=%t err=%v", status, stream, found, err)
		}
		if err := server.finishSessionReviewerFrame(frame.ID, status, "review did not complete", index); err != nil {
			t.Fatal(err)
		}
		assertReviewerTranscriptTerminal(t, repo, stream, status, 1)
		stored, found, err := store.GetFrame(frame.ID)
		if err != nil || !found || stored.Status != status {
			t.Fatalf("status=%s frame=%#v found=%t err=%v", status, stored, found, err)
		}
	}
}

func TestSessionReviewerFrameSetupRollsBackTranscriptWhenClaimFails(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner", "review-rollback-project", "review-rollback-root")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, err := db.Exec(`
		CREATE TRIGGER fail_internal_reviewer_claim BEFORE INSERT ON transcript_runner_attempts
		WHEN NEW.runner_id LIKE 'reviewer:%'
		BEGIN SELECT RAISE(ABORT, 'forced reviewer claim failure'); END`); err != nil {
		t.Fatal(err)
	}

	const attempt = 7
	const reviewIndex = 2
	frameID := runnerReviewerFrameID("review-rollback-root", attempt, reviewIndex)
	if _, err := server.beginSessionReviewerFrame(
		context.Background(), sessionstore.Session{ID: "review-rollback-root"}, "review-model", attempt, reviewIndex,
	); err == nil {
		t.Fatal("reviewer setup unexpectedly ignored the forced claim failure")
	}
	if frame, found, err := store.GetFrame(frameID); err != nil || found {
		t.Fatalf("partial reviewer frame=%#v found=%t err=%v", frame, found, err)
	}
	if stream, found, err := repo.GetFrameStreamBySession(context.Background(), "owner", frameID); err != nil || found {
		t.Fatalf("orphan reviewer stream=%#v found=%t err=%v", stream, found, err)
	}
}

func TestSessionReviewerFrameRetriesTransientDatabaseContention(t *testing.T) {
	store, repo, lockDB := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner", "review-busy-project", "review-busy-root")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := repo.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		_, err := tx.ExecContext(ctx, `PRAGMA busy_timeout=1`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	lock, err := lockDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := lock.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	released := make(chan error, 1)
	go func() {
		timer := time.NewTimer(90 * time.Millisecond)
		defer timer.Stop()
		<-timer.C
		_, releaseErr := lock.ExecContext(context.Background(), `COMMIT`)
		released <- releaseErr
	}()

	started := time.Now()
	frame, err := server.beginSessionReviewerFrame(
		ctx, sessionstore.Session{ID: "review-busy-root"}, "review-model", 11, 0,
	)
	if err != nil {
		t.Fatalf("reviewer frame did not recover from transient contention: %v", err)
	}
	if releaseErr := <-released; releaseErr != nil {
		t.Fatal(releaseErr)
	}
	if elapsed := time.Since(started); elapsed < 80*time.Millisecond {
		t.Fatalf("reviewer frame completed in %s without observing the injected lock", elapsed)
	}
	if frame.ID != runnerReviewerFrameID("review-busy-root", 11, 0) {
		t.Fatalf("reviewer frame id=%q", frame.ID)
	}
	stream, found, err := repo.GetFrameStreamBySession(ctx, "owner", frame.ID)
	if err != nil || !found {
		t.Fatalf("reviewer stream=%#v found=%t err=%v", stream, found, err)
	}
}

func assertReviewerTranscriptTerminal(
	t *testing.T,
	repo *transcriptstore.Repository,
	stream transcriptstore.Stream,
	wantStatus string,
	wantTerminals int,
) {
	t.Helper()
	state, found, err := repo.GetLatestRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found || state.Status != wantStatus || state.Phase != transcriptstore.RunnerPhaseTerminal {
		t.Fatalf("runtime=%#v found=%t err=%v", state, found, err)
	}
	current, err := repo.GetStream(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	events, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ThroughPublicationSequence: current.NextPublication - 1, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	terminals := 0
	for _, event := range events {
		if event.Event.Type == "runner_finished" {
			terminals++
		}
	}
	if terminals != wantTerminals {
		t.Fatalf("terminal events=%d want=%d events=%#v", terminals, wantTerminals, events)
	}
}
