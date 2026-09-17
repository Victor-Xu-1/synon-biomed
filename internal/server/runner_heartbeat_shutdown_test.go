package server

import (
	"context"
	"errors"
	"testing"
	"time"

	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestRunnerHeartbeatsStopCleanlyWhileWaitingForSQLite(t *testing.T) {
	_, _, db := newTranscriptWebFixture(t)
	repo := transcriptstore.NewRepository(db)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	stream, err := repo.CreateStream(ctx, transcriptstore.CreateStreamInput{
		UID: "heartbeat-stop", OwnerID: "owner", ExternalID: "heartbeat-stop",
		SessionID: "heartbeat-stop", Kind: transcriptstore.StreamKindStandalone, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.AppendUserEvent(ctx, transcriptstore.AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "heartbeat-stop-input",
		PayloadJSON: []byte(`{"content":"stop the lease observer"}`),
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.ClaimRunner(ctx, transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "heartbeat-stop-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	server := &Server{transcriptStore: repo}
	for _, name := range []string{"runner", "reviewer"} {
		t.Run(name, func(t *testing.T) {
			// Hold the sole real writer connection until the heartbeat has joined
			// its wait queue. Stop then cancels actual database/sql acquisition.
			conn, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			beforeWaits := db.Stats().WaitCount
			var heartbeatCtx context.Context
			var stop func() error
			if name == "reviewer" {
				heartbeatCtx, stop = server.startSessionReviewerHeartbeatWithTTL(ctx, claimed.Claim, 100*time.Millisecond)
			} else {
				heartbeatCtx, stop = server.startSessionRunnerChatHeartbeat(ctx, sessionstore.RunnerMutationClaim{},
					&transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim}, SessionRunnerChatOptions{LeaseTTL: 100 * time.Millisecond})
			}
			defer stop()
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for db.Stats().WaitCount == beforeWaits {
				select {
				case <-heartbeatCtx.Done():
					t.Fatalf("heartbeat ended before waiting for SQLite: %v", context.Cause(heartbeatCtx))
				case <-ticker.C:
				}
			}
			if err := stop(); err != nil {
				t.Fatalf("normal stop reported a lease failure: %v", err)
			}
			if !errors.Is(heartbeatCtx.Err(), context.Canceled) {
				t.Fatalf("heartbeat context remained active after stop: %v", heartbeatCtx.Err())
			}
		})
	}
}
