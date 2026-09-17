package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

func (s *Store) GetKernelFrameAccessContext(ctx context.Context, frameID string) (KernelFrameAccess, bool, error) {
	if s == nil || s.db == nil {
		return KernelFrameAccess{}, false, errors.New("workspace store is closed")
	}
	if ctx == nil {
		return KernelFrameAccess{}, false, errors.New("kernel frame context is required")
	}
	frameID = strings.TrimSpace(frameID)
	if frameID == "" {
		return KernelFrameAccess{}, false, errors.New("kernel frame id is required")
	}
	var result KernelFrameAccess
	err := s.db.QueryRowContext(ctx, `
		SELECT p.user_id, p.path, f.id, f.incarnation_id, f.project_id, COALESCE(f.parent_frame_id, ''),
			f.root_frame_id, root.incarnation_id, f.root_sequence, f.agent_name, f.status,
			f.conversation_type, f.name, f.created_at, f.updated_at,
			COALESCE(metadata.is_hidden, 0), COALESCE(metadata.delegate_name, '')
		FROM frames f
		JOIN projects p ON p.id = f.project_id
		JOIN frames root ON root.id = f.root_frame_id AND root.project_id = f.project_id
		LEFT JOIN frame_runtime_metadata metadata ON metadata.frame_id = f.id
		WHERE f.id = ?`, frameID).Scan(
		&result.UserID, &result.ProjectPath, &result.Frame.ID, &result.Frame.IncarnationID, &result.Frame.ProjectID,
		&result.Frame.ParentFrameID, &result.Frame.RootFrameID,
		&result.RootFrameIncarnationID,
		&result.Frame.RootSequence, &result.Frame.AgentName, &result.Frame.Status,
		&result.Frame.ConversationType, &result.Frame.Name,
		&result.Frame.CreatedAt, &result.Frame.UpdatedAt,
		&result.IsHidden, &result.DelegateName,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return KernelFrameAccess{}, false, nil
	}
	if err != nil {
		return KernelFrameAccess{}, false, fmt.Errorf("get kernel frame access: %w", err)
	}
	return result, true, nil
}

func (s *Store) ProjectOwnerIDContext(ctx context.Context, projectID string) (string, bool, error) {
	if s == nil || s.db == nil {
		return "", false, errors.New("workspace store is closed")
	}
	if ctx == nil {
		return "", false, errors.New("project owner context is required")
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return "", false, errors.New("project id is required")
	}
	var userID string
	err := s.readDatabase().QueryRowContext(ctx, `SELECT user_id FROM projects WHERE id = ?`, projectID).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get project owner: %w", err)
	}
	return userID, true, nil
}
