package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// GetFrameEventByID performs an exact durable lookup for scheduler recovery.
// Callers must still validate the frame, event type, and payload they expect.
func (s *Store) GetFrameEventByID(id string) (FrameEvent, bool, error) {
	if s == nil || s.db == nil {
		return FrameEvent{}, false, errors.New("workspace store is closed")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return FrameEvent{}, false, errors.New("frame event id is required")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FrameEvent{}, false, fmt.Errorf("begin frame event lookup: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	event, _, found, err := frameEventByID(ctx, tx, id)
	if err != nil {
		return FrameEvent{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return FrameEvent{}, false, fmt.Errorf("commit frame event lookup: %w", err)
	}
	return event, found, nil
}
