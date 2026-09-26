package workspace

import (
	"context"
	"errors"
	"strings"
)

// ListRecoveryConditionWaitDispatches pages the existing durable queue. Waiting
// records retain their claim generation and are never runnable by a timer.
func (s *Store) ListRecoveryConditionWaitDispatches(ctx context.Context, afterEventID string, limit int) ([]CompatibilityFrameResumeDispatch, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	if limit < 1 || limit > 500 {
		return nil, errors.New("recovery wait page size must be between 1 and 500")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.id, e.frame_id, e.sequence, e.payload, e.created_at,
			f.root_frame_id, f.project_id, f.agent_name, f.status
		FROM frame_events e JOIN frames f ON f.id=e.frame_id
		WHERE e.event_type='frame_resumed' AND e.id>?
			AND json_extract(e.payload,'$.dispatch.status')='registered'
			AND json_extract(e.payload,'$.dispatch.waitingFor')=?
			AND f.status NOT IN ('completed','failed','cancelled','canceled')
		ORDER BY e.id LIMIT ?`, strings.TrimSpace(afterEventID), CompatibilityFrameResumeDispatchWaitRecoveryCondition, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []CompatibilityFrameResumeDispatch
	for rows.Next() {
		row, found, err := scanCompatibilityFrameResumeDispatch(rows)
		if err != nil {
			return nil, err
		}
		if found {
			result = append(result, row.dispatch)
		}
	}
	return result, rows.Err()
}
