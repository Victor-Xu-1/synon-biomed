package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// UpdateCompatibilityProjectInput preserves the distinction between an
// omitted nullable field and an explicit JSON null.
type UpdateCompatibilityProjectInput struct {
	Name           *string
	DescriptionSet bool
	Description    *string
	ContextSet     bool
	ContextData    any
}

func (s *Store) UpdateCompatibilityProjectRealtime(
	ctx context.Context,
	projectID string,
	ownerUserID string,
	input UpdateCompatibilityProjectInput,
	eventID string,
) (CompatibilityProject, error) {
	if s == nil || s.db == nil {
		return CompatibilityProject{}, errors.New("workspace store is closed")
	}
	projectID = strings.TrimSpace(projectID)
	ownerUserID = strings.TrimSpace(ownerUserID)
	if projectID == "" || ownerUserID == "" {
		return CompatibilityProject{}, errors.New("project id and owner user id are required")
	}

	var project CompatibilityProject
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		current, err := scanCompatibilityProject(tx.QueryRowContext(
			ctx, compatibilityProjectSelect+" WHERE p.user_id = ? AND p.id = ?",
			ownerUserID, projectID,
		))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("project %q does not exist", projectID)
		}
		if err != nil {
			return fmt.Errorf("get compatibility project for update: %w", err)
		}
		if input.Name == nil && !input.DescriptionSet && !input.ContextSet {
			project = current
			return nil
		}

		now := s.now().UTC()
		if input.Name != nil {
			if _, err := tx.ExecContext(ctx, `
				UPDATE projects SET name = ?, updated_at = ?
				WHERE id = ? AND user_id = ?`, *input.Name, now, projectID, ownerUserID); err != nil {
				return fmt.Errorf("update compatibility project name: %w", err)
			}
		} else if _, err := tx.ExecContext(ctx, `
			UPDATE projects SET updated_at = ? WHERE id = ? AND user_id = ?`,
			now, projectID, ownerUserID); err != nil {
			return fmt.Errorf("touch compatibility project: %w", err)
		}

		if input.DescriptionSet || input.ContextSet {
			description := current.Description
			if input.DescriptionSet {
				description = ""
				if input.Description != nil {
					description = *input.Description
				}
			}
			contextData := current.ContextData
			if input.ContextSet {
				contextData = input.ContextData
			}
			rawContext, err := json.Marshal(contextData)
			if err != nil {
				return fmt.Errorf("marshal compatibility project context: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO project_runtime_metadata (project_id, description, context_data)
				VALUES (?, ?, ?)
				ON CONFLICT(project_id) DO UPDATE SET
					description = excluded.description,
					context_data = excluded.context_data`,
				projectID, description, string(rawContext)); err != nil {
				return fmt.Errorf("update compatibility project metadata: %w", err)
			}
		}

		project, err = scanCompatibilityProject(tx.QueryRowContext(
			ctx, compatibilityProjectSelect+" WHERE p.user_id = ? AND p.id = ?",
			ownerUserID, projectID,
		))
		if err != nil {
			return fmt.Errorf("read updated compatibility project: %w", err)
		}
		_, err = s.EnqueueRealtimeOutboxTx(ctx, tx, RealtimeEventInput{
			ID: mutationRealtimeID(eventID), UserID: ownerUserID, ProjectID: projectID,
			Type: "frame_update", Payload: map[string]any{
				"project_id": projectID, "action": "project_updated",
			},
		}, "")
		return err
	})
	return project, err
}

func (s *Store) ListCompatibilityProjectRootFrameIDs(ctx context.Context, ownerUserID, projectID string) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	ownerUserID = strings.TrimSpace(ownerUserID)
	projectID = strings.TrimSpace(projectID)
	if ownerUserID == "" || projectID == "" {
		return nil, errors.New("owner user id and project id are required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT f.root_frame_id
		FROM frames f JOIN projects p ON p.id = f.project_id
		WHERE p.user_id = ? AND p.id = ?
		ORDER BY f.root_frame_id`, ownerUserID, projectID)
	if err != nil {
		return nil, fmt.Errorf("list compatibility project root frames: %w", err)
	}
	defer rows.Close()
	result := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan compatibility project root frame: %w", err)
		}
		result = append(result, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate compatibility project root frames: %w", err)
	}
	return result, nil
}
