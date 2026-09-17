package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// AccountOverviewTask is the minimal, owner-scoped task projection required
// by the personal account dashboard. Keeping this read model free of message
// content prevents the overview endpoint from loading or exposing transcripts.
type AccountOverviewTask struct {
	ID        string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// AccountOverviewSnapshot is read in one SQLite snapshot so task, project,
// and artifact totals cannot describe different points in time.
type AccountOverviewSnapshot struct {
	ProjectCount  int
	ArtifactCount int
	Tasks         []AccountOverviewTask
	UpdatedAt     time.Time
}

// ReadAccountOverview returns the complete visible root-task activity for one
// owner together with project and user-facing artifact totals. Large tool
// result backing artifacts are intentionally excluded, matching the existing
// project compatibility projection.
func (s *Store) ReadAccountOverview(ctx context.Context, userID string) (AccountOverviewSnapshot, error) {
	db := s.readDatabase()
	if db == nil {
		return AccountOverviewSnapshot{}, errors.New("workspace store is closed")
	}
	if ctx == nil {
		return AccountOverviewSnapshot{}, errors.New("account overview context is required")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return AccountOverviewSnapshot{}, errors.New("account overview user id is required")
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return AccountOverviewSnapshot{}, fmt.Errorf("begin account overview snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var snapshot AccountOverviewSnapshot
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT project.id), COUNT(artifact.id)
		FROM projects AS project
		LEFT JOIN artifacts AS artifact
			ON artifact.project_id = project.id
			AND artifact.id NOT LIKE 'large-tool-result-%'
		WHERE project.user_id = ?`, userID,
	).Scan(&snapshot.ProjectCount, &snapshot.ArtifactCount); err != nil {
		return AccountOverviewSnapshot{}, fmt.Errorf("read account project totals: %w", err)
	}
	for _, query := range []string{
		`SELECT project.updated_at FROM projects AS project
			WHERE project.user_id = ? ORDER BY project.updated_at DESC LIMIT 1`,
		`SELECT artifact.updated_at FROM artifacts AS artifact
			JOIN projects AS project ON project.id = artifact.project_id
			WHERE project.user_id = ? AND artifact.id NOT LIKE 'large-tool-result-%'
			ORDER BY artifact.updated_at DESC LIMIT 1`,
	} {
		var observed time.Time
		if err := tx.QueryRowContext(ctx, query, userID).Scan(&observed); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return AccountOverviewSnapshot{}, fmt.Errorf("read account data timestamp: %w", err)
		}
		if observed.After(snapshot.UpdatedAt) {
			snapshot.UpdatedAt = observed
		}
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT frame.id, frame.status, frame.created_at, frame.updated_at
		FROM frames AS frame
		JOIN projects AS project ON project.id = frame.project_id
		LEFT JOIN frame_runtime_metadata AS metadata ON metadata.frame_id = frame.id
		WHERE project.user_id = ?
			AND frame.parent_frame_id IS NULL
			AND COALESCE(metadata.is_hidden, 0) = 0
		ORDER BY frame.updated_at, frame.id`, userID)
	if err != nil {
		return AccountOverviewSnapshot{}, fmt.Errorf("read account task activity: %w", err)
	}
	for rows.Next() {
		var task AccountOverviewTask
		if err := rows.Scan(&task.ID, &task.Status, &task.CreatedAt, &task.UpdatedAt); err != nil {
			_ = rows.Close()
			return AccountOverviewSnapshot{}, fmt.Errorf("scan account task activity: %w", err)
		}
		snapshot.Tasks = append(snapshot.Tasks, task)
		if task.UpdatedAt.After(snapshot.UpdatedAt) {
			snapshot.UpdatedAt = task.UpdatedAt
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return AccountOverviewSnapshot{}, fmt.Errorf("iterate account task activity: %w", err)
	}
	if err := rows.Close(); err != nil {
		return AccountOverviewSnapshot{}, fmt.Errorf("close account task activity: %w", err)
	}
	if snapshot.Tasks == nil {
		snapshot.Tasks = []AccountOverviewTask{}
	}
	if err := tx.Commit(); err != nil {
		return AccountOverviewSnapshot{}, fmt.Errorf("commit account overview snapshot: %w", err)
	}
	return snapshot, nil
}
