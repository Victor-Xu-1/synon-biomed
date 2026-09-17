package server

import (
	"context"
	"database/sql"
	"errors"
	transcriptstore "synon-go/internal/persistence/transcript"
	"testing"
	"time"
)

func TestRunnerLeaseRenewalRecoversRealLockWithoutMutatingClaim(t *testing.T) {
	_, repo, db := newTranscriptWebFixture(t)
	ctx := context.Background()
	if _, err := repo.CreateStream(ctx, transcriptstore.CreateStreamInput{UID: "lease-test", OwnerID: "owner", ExternalID: "lease-test", Kind: transcriptstore.StreamKindStandalone, Epoch: 1}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.AppendUserEvent(ctx, transcriptstore.AppendUserEventInput{StreamUID: "lease-test", OwnerID: "owner", ClientMessageID: "input", PayloadJSON: []byte(`{"text":"work"}`)}); err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.ClaimRunner(ctx, transcriptstore.ClaimRunnerInput{StreamUID: "lease-test", OwnerID: "owner", RunnerID: "worker", TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim: %v", err)
	}
	// Discover the fixture's real main database, not a mocked persistence error.
	var seq int
	var name, path string
	if err := db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	blocker, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	conn, err := blocker.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, "ROLLBACK")
	done := make(chan error, 1)
	app := &Server{transcriptStore: repo}
	go func() {
		result, err := app.renewTranscriptRunnerLease(ctx, claimed.Claim, time.Minute, claimed.Claim.ExpiresAt)
		if err == nil && !result.Renewed {
			err = errors.New("not renewed")
		}
		done <- err
	}()
	time.Sleep(75 * time.Millisecond)
	if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("renewal did not recover")
	}
	expired := time.Now().Add(-time.Second)
	if _, err := app.renewTranscriptRunnerLease(ctx, claimed.Claim, time.Minute, expired); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired local lease reused: %v", err)
	}
}
