package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ListMemorySystemProfileRows returns only the profile rows the reference runtime
// places in the system prompt. Rows filed in non-auto-recalled categories stay
// available to explicit read/search tools but are not injected automatically.
func (s *Store) ListMemorySystemProfileRows(ctx context.Context, userID string) ([]Memory, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, errors.New("memory user id is required")
	}
	query := memorySelect + `
		WHERE m.user_id = ?
			AND m.superseded_by IS NULL
			AND m.subject_project_id IS NULL
			AND m.subject_artifact_id IS NULL
			AND m.subject_version_id IS NULL
			AND m.subject_frame_id IS NULL
			AND COALESCE(c.auto_recall, 1) = 1
		ORDER BY m.created_at DESC, m.id`
	rows, err := scanMemoryRows(ctx, s.db, query, userID)
	if err != nil {
		return nil, fmt.Errorf("list memory system profile rows: %w", err)
	}
	return rows, nil
}

// CountActiveMemoryEntities returns the counts used by the recall renderer's
// "showing n of total" hint. The pool includes all uncontained and contained
// rows, but only frame rows owned by the current root frame.
func (s *Store) CountActiveMemoryEntities(ctx context.Context, userID, frameID string) (map[string]int, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	userID = strings.TrimSpace(userID)
	frameID = strings.TrimSpace(frameID)
	if userID == "" {
		return nil, errors.New("memory user id is required")
	}
	query := `SELECT COALESCE(subject_frame_id, ''), COALESCE(subject_artifact_id, ''),
		COALESCE(subject_project_id, ''), COUNT(*)
		FROM memories WHERE user_id = ? AND superseded_by IS NULL`
	args := []any{userID}
	if frameID == "" {
		query += ` AND subject_frame_id IS NULL`
	} else {
		query += ` AND (subject_frame_id IS NULL OR subject_frame_id = ?)`
		args = append(args, frameID)
	}
	query += ` GROUP BY subject_frame_id, subject_artifact_id, subject_project_id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("count active memory entities: %w", err)
	}
	defer rows.Close()
	counts := make(map[string]int)
	for rows.Next() {
		var subjectFrameID, subjectArtifactID, subjectProjectID string
		var count int
		if err := rows.Scan(&subjectFrameID, &subjectArtifactID, &subjectProjectID, &count); err != nil {
			return nil, fmt.Errorf("scan active memory entity count: %w", err)
		}
		counts[memoryEntityKey(Memory{
			SubjectFrameID: subjectFrameID, SubjectArtifactID: subjectArtifactID, SubjectProjectID: subjectProjectID,
		})] += count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active memory entity counts: %w", err)
	}
	return counts, nil
}
