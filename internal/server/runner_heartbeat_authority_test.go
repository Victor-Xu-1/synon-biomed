package server

import (
	"context"
	"errors"
	"testing"
	"time"

	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestTranscriptRunnerHeartbeatKeepsCheckpointClaimImmutable(t *testing.T) {
	_, repo, _ := newTranscriptWebFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	stream, err := repo.CreateStream(ctx, transcriptstore.CreateStreamInput{
		UID: "heartbeat-immutable", OwnerID: "owner", ExternalID: "heartbeat-immutable",
		SessionID: "heartbeat-immutable", Kind: transcriptstore.StreamKindStandalone, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.AppendUserEvent(ctx, transcriptstore.AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "heartbeat-input",
		PayloadJSON: []byte(`{"content":"preserve the original fenced claim"}`),
	}); err != nil {
		t.Fatal(err)
	}
	// The short initial lease preserves the post-original-expiry contract. Its
	// renewal is synchronized by an explicit commit acknowledgement below, not
	// by guessing when a production ticker might have completed its transaction.
	const initialTTL = 2 * time.Second
	const renewedTTL = 30 * time.Second
	claimed, err := repo.ClaimRunner(ctx, transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "heartbeat-runner",
		TTL: initialTTL, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	authority := &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim}
	server := &Server{transcriptStore: repo}
	ticks := make(chan time.Time)
	renewed := make(chan struct{}, 1)
	heartbeatCtx, stop := server.startSessionRunnerChatHeartbeatWithTicks(
		ctx, sessionstore.RunnerMutationClaim{}, authority,
		SessionRunnerChatOptions{LeaseTTL: renewedTTL}, ticks, nil,
		func() { renewed <- struct{}{} },
	)
	defer func() { _ = stop() }()
	select {
	case ticks <- time.Now():
	case <-heartbeatCtx.Done():
		t.Fatalf("heartbeat stopped before controlled renewal: %v", context.Cause(heartbeatCtx))
	case <-time.After(5 * time.Second):
		t.Fatal("heartbeat did not accept controlled renewal")
	}
	select {
	case <-renewed:
	case <-heartbeatCtx.Done():
		t.Fatalf("heartbeat stopped before committing controlled renewal: %v", context.Cause(heartbeatCtx))
	case <-time.After(5 * time.Second):
		t.Fatal("heartbeat did not commit controlled renewal")
	}
	state, err := repo.GetRunnerRuntimeState(ctx, stream.UID, stream.OwnerID, claimed.Claim.Attempt)
	if err != nil || !state.ExpiresAt.After(claimed.Claim.ExpiresAt) || !state.ExpiresAt.After(time.Now().UTC()) {
		t.Fatalf("controlled heartbeat did not extend durable expiry: state=%#v err=%v", state, err)
	}
	timer := time.NewTimer(max(time.Until(claimed.Claim.ExpiresAt.Add(100*time.Millisecond)), 0))
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-heartbeatCtx.Done():
		t.Fatalf("heartbeat stopped before the original claim expiry boundary: %v", context.Cause(heartbeatCtx))
	case <-ctx.Done():
		t.Fatalf("original claim expiry boundary was not reached: %v", ctx.Err())
	}
	state, err = repo.GetRunnerRuntimeState(ctx, stream.UID, stream.OwnerID, claimed.Claim.Attempt)
	if err != nil || !state.ExpiresAt.After(time.Now().UTC()) {
		t.Fatalf("renewed durable lease was not live past the original expiry: state=%#v err=%v", state, err)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if authority.Claim != claimed.Claim {
		t.Fatal("heartbeat mutated the shared fencing claim")
	}
	if _, err := server.checkpointTranscriptRunnerEventWithDestinations(ctx, authority,
		transcriptstore.RunnerPhaseExecuting, "after-original-expiry", map[string]any{"completed_steps": "renewed"}, true, nil); err != nil {
		t.Fatalf("durable renewal did not preserve the original claim: %v", err)
	}
	tampered := *authority
	tampered.Claim.ClaimToken = "not-the-authoritative-token"
	if _, err := server.checkpointTranscriptRunnerEventWithDestinations(ctx, &tampered,
		transcriptstore.RunnerPhaseExecuting, "tampered-claim", map[string]any{"completed_steps": "forbidden"}, true, nil); !errors.Is(err, transcriptstore.ErrClaimStale) {
		t.Fatalf("tampered claim was not fenced: %v", err)
	}
}
