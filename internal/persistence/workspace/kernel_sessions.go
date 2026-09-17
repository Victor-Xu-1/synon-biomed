package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// KernelFrameAccess is the narrow ownership and visibility projection needed
// by the v1.1 session-kernel API. Keeping this query in the store prevents the
// process manager from learning database or authentication concerns.
type KernelFrameAccess struct {
	UserID                 string
	ProjectPath            string
	RootFrameIncarnationID string
	Frame                  Frame
	IsHidden               bool
	DelegateName           string
}

func (s *Store) GetKernelFrameAccess(frameID string) (KernelFrameAccess, bool, error) {
	if s == nil || s.db == nil {
		return KernelFrameAccess{}, false, errors.New("workspace store is closed")
	}
	frameID = strings.TrimSpace(frameID)
	if frameID == "" {
		return KernelFrameAccess{}, false, errors.New("kernel frame id is required")
	}
	var result KernelFrameAccess
	err := s.db.QueryRowContext(context.Background(), `
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
