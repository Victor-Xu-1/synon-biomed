package workspace

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// NextRoutineWakeAt returns the earliest instant at which an enabled routine
// can be claimed. A locked routine becomes eligible only after its lease
// expires, so callers can sleep instead of repeatedly polling SQLite.
func (s *Store) NextRoutineWakeAt(lockTTL time.Duration) (time.Time, bool, error) {
	if s == nil || s.db == nil {
		return time.Time{}, false, errors.New("workspace store is closed")
	}
	if lockTTL <= 0 {
		return time.Time{}, false, errors.New("routine lock ttl must be positive")
	}

	rows, err := s.db.QueryContext(context.Background(), `
		SELECT next_due, locked_at
		FROM routine_schedules
		WHERE enabled = 1`)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("query routine wake times: %w", err)
	}
	defer rows.Close()

	var earliest time.Time
	for rows.Next() {
		var nextDue time.Time
		var lockedAt *time.Time
		if err := rows.Scan(&nextDue, &lockedAt); err != nil {
			return time.Time{}, false, fmt.Errorf("scan routine wake time: %w", err)
		}
		candidate := nextDue.UTC()
		if lockedAt != nil {
			leaseExpiry := lockedAt.UTC().Add(lockTTL)
			if leaseExpiry.After(candidate) {
				candidate = leaseExpiry
			}
		}
		if earliest.IsZero() || candidate.Before(earliest) {
			earliest = candidate
		}
	}
	if err := rows.Err(); err != nil {
		return time.Time{}, false, fmt.Errorf("iterate routine wake times: %w", err)
	}
	return earliest, !earliest.IsZero(), nil
}
