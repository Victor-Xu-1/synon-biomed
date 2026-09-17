package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// TransitionCompatibilityFrameStatus is a single-writer compare-and-swap for
// public compatibility operations that must not lose concurrent decisions.
func (s *Store) TransitionCompatibilityFrameStatus(frameID, fromStatus, toStatus string) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("workspace store is closed")
	}
	frameID = strings.TrimSpace(frameID)
	fromStatus = strings.TrimSpace(fromStatus)
	toStatus = strings.TrimSpace(toStatus)
	if frameID == "" || fromStatus == "" || toStatus == "" {
		return false, errors.New("frame id and transition statuses are required")
	}
	fromStatus, err := canonicalFrameStatus(fromStatus)
	if err != nil {
		return false, err
	}
	toStatus, err = canonicalFrameStatus(toStatus)
	if err != nil {
		return false, err
	}
	result, err := s.db.ExecContext(context.Background(), `
		UPDATE frames SET status = ?, updated_at = ?
		WHERE id = ? AND status = ?`, toStatus, s.now().UTC(), frameID, fromStatus)
	if err != nil {
		return false, fmt.Errorf("transition compatibility frame status: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("count compatibility frame status transition: %w", err)
	}
	return changed == 1, nil
}
