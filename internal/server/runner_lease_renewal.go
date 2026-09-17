package server

import (
	"context"
	transcriptstore "synon-go/internal/persistence/transcript"
	"time"
)

// Renewal is retryable only inside the currently owned lease. The immutable
// claim identity is never rewritten by the heartbeat goroutine; the store is
// the authority for expiry and ownership after every attempted write.
func (s *Server) renewTranscriptRunnerLease(ctx context.Context, claim transcriptstore.RunnerClaim, ttl time.Duration, expiresAt time.Time) (transcriptstore.HeartbeatRunnerResult, error) {
	deadline := time.Now().Add(ttl / 3)
	if !expiresAt.IsZero() && expiresAt.Before(deadline) {
		deadline = expiresAt
	}
	renewalCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	var result transcriptstore.HeartbeatRunnerResult
	err := retryTransientStoreContention(renewalCtx, "runner_lease_renewal", func() error {
		var err error
		result, err = s.transcriptStore.HeartbeatRunner(renewalCtx, transcriptstore.HeartbeatRunnerInput{Claim: claim, TTL: ttl})
		return err
	})
	return result, err
}
