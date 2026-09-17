package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

func (s *Store) ListFramesForRoot(rootFrameID string) ([]Frame, error) {
	db := s.readDatabase()
	if db == nil {
		return nil, errors.New("workspace store is closed")
	}
	rootFrameID = strings.TrimSpace(rootFrameID)
	if rootFrameID == "" {
		return nil, errors.New("root frame id is required")
	}
	rows, err := db.QueryContext(context.Background(), `
		SELECT id, project_id, COALESCE(parent_frame_id, ''), root_frame_id,
		       root_sequence, agent_name, status, conversation_type, name,
		       created_at, updated_at
		FROM frames
		WHERE root_frame_id = ?
		ORDER BY root_sequence, created_at, id`, rootFrameID)
	if err != nil {
		return nil, fmt.Errorf("list frames for root: %w", err)
	}
	defer rows.Close()
	frames := make([]Frame, 0)
	for rows.Next() {
		var frame Frame
		if err := rows.Scan(
			&frame.ID, &frame.ProjectID, &frame.ParentFrameID, &frame.RootFrameID,
			&frame.RootSequence, &frame.AgentName, &frame.Status,
			&frame.ConversationType, &frame.Name, &frame.CreatedAt, &frame.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan root frame: %w", err)
		}
		frames = append(frames, frame)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate root frames: %w", err)
	}
	return frames, nil
}

// ListActiveFramesForRoot is the indexed live-projection read. Historical
// terminal frames remain available through the durable transcript and are not
// re-read on every realtime wake.
func (s *Store) ListActiveFramesForRoot(projectID, rootFrameID string) ([]Frame, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	projectID = strings.TrimSpace(projectID)
	rootFrameID = strings.TrimSpace(rootFrameID)
	if projectID == "" || rootFrameID == "" {
		return nil, errors.New("project id and root frame id are required")
	}
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT id, project_id, COALESCE(parent_frame_id, ''), root_frame_id,
		       root_sequence, agent_name, status, conversation_type, name,
		       created_at, updated_at
		FROM frames
		WHERE project_id = ? AND root_frame_id = ?
		  AND status NOT IN ('completed','failed','cancelled','canceled','stopped')
		ORDER BY root_sequence, created_at, id`, projectID, rootFrameID)
	if err != nil {
		return nil, fmt.Errorf("list active frames for root: %w", err)
	}
	defer rows.Close()
	frames := make([]Frame, 0)
	for rows.Next() {
		var frame Frame
		if err := rows.Scan(
			&frame.ID, &frame.ProjectID, &frame.ParentFrameID, &frame.RootFrameID,
			&frame.RootSequence, &frame.AgentName, &frame.Status,
			&frame.ConversationType, &frame.Name, &frame.CreatedAt, &frame.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan active root frame: %w", err)
		}
		frames = append(frames, frame)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active root frames: %w", err)
	}
	return frames, nil
}
