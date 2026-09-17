package transcript

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"synon-go/internal/sqliteutil"
	"testing"
	"time"
)

func TestClaimContentionDoesNotMasqueradeAsSchemaLoss(t *testing.T) {
	repo, db, path := newTranscriptRepository(t)
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA busy_timeout=1"); err != nil {
		t.Fatal(err)
	}
	blocker, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	conn, err := blocker.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	_, err = repo.ClaimNextRunner(context.Background(), ClaimNextRunnerInput{RunnerID: "contended", TTL: time.Minute})
	if !sqliteutil.IsTransientContention(err) || errors.Is(err, ErrSchemaUnavailable) {
		t.Fatalf("lock classified as schema failure: %v", err)
	}
	if _, err := conn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ClaimNextRunner(context.Background(), ClaimNextRunnerInput{RunnerID: "contended", TTL: time.Minute}); err != nil {
		t.Fatalf("claim did not recover: %v", err)
	}
}

func TestRunnerLeaseChecksClockAfterWaitingForConnection(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	seedTranscriptInput(t, repo, "clock-test", "owner")
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	repo.now = func() time.Time { return time.Unix(0, clock.Load()) }
	claimed, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{StreamUID: "clock-test", OwnerID: "owner", RunnerID: "worker", TTL: time.Minute, ResumeSource: ResumeSourceFresh})
	if err != nil || !claimed.Claimed {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	held, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	waitBefore := db.Stats().WaitCount
	done := make(chan error, 1)
	go func() {
		_, err := repo.HeartbeatRunner(context.Background(), HeartbeatRunnerInput{Claim: claimed.Claim, TTL: time.Minute})
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for db.Stats().WaitCount == waitBefore {
		if time.Now().After(deadline) {
			t.Fatal("renewal did not wait")
		}
		time.Sleep(time.Millisecond)
	}
	clock.Add(int64(2 * time.Minute))
	held.Close()
	if err := <-done; !errors.Is(err, ErrClaimStale) {
		t.Fatalf("expired lease renewed using pre-wait clock: %v", err)
	}
}

func TestRunnerAuthorityPreservesDatabaseAndContextErrors(t *testing.T) {
	_, db, _ := newTranscriptRepository(t)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = validateRunnerAttemptAuthorityConn(ctx, conn, runnerAttemptAuthority{StreamUID: "absent", Attempt: 1}, time.Now(), true)
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrClaimStale) {
		t.Fatalf("context loss presented as ownership loss: %v", err)
	}
	if err := validateRunnerAttemptAuthorityConn(context.Background(), conn, runnerAttemptAuthority{StreamUID: "absent", Attempt: 1}, time.Now(), true); !errors.Is(err, ErrClaimStale) {
		t.Fatalf("missing authority accepted: %v", err)
	}
}
