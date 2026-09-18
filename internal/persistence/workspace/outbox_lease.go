package workspace

import (
	"context"
	"errors"
	"strings"
	"time"
)

// RenewOutboxClaim never revives an expired claim, even if no successor has
// claimed it yet. Database time is the shared lease clock across processes.
func (s *Store) RenewOutboxClaim(ctx context.Context, eventID, token string, lease time.Duration) error {
	if s == nil || s.db == nil || lease < time.Millisecond || lease > outboxMaxLease || strings.TrimSpace(token) == "" {
		return errors.New("valid outbox claim and lease are required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE workspace_outbox SET lease_expires_at_ms=`+sqliteNowMillis+`+?
		WHERE event_id=? AND status='inflight' AND claim_token=? AND lease_expires_at_ms>`+sqliteNowMillis,
		lease.Milliseconds(), eventID, token)
	if err != nil {
		return err
	}
	return requireOutboxClaim(result)
}
