package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"os"
	"strings"
)

func (s *Store) CreateProject(input CreateProjectInput) (Project, error) {
	if s == nil || s.db == nil {
		return Project{}, errors.New("workspace store is closed")
	}
	if strings.TrimSpace(input.ID) == "" {
		return Project{}, errors.New("project id is required")
	}
	if strings.TrimSpace(input.Name) == "" {
		return Project{}, errors.New("project name is required")
	}
	input.UserID = strings.TrimSpace(input.UserID)
	if input.UserID == "" {
		input.UserID = "local"
	}
	now := s.now().UTC()
	_, err := s.db.ExecContext(context.Background(), `
		INSERT INTO projects (id, user_id, name, path, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`, input.ID, input.UserID, input.Name, input.Path, now, now)
	if err != nil {
		return Project{}, fmt.Errorf("insert project: %w", err)
	}
	return Project{ID: input.ID, Name: input.Name, Path: input.Path, CreatedAt: now, UpdatedAt: now}, nil
}

func (s *Store) ListProjects(limit, offset int) ([]Project, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	if offset < 0 {
		return nil, errors.New("project offset must be non-negative")
	}
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT id, name, path, created_at, updated_at FROM projects
		ORDER BY updated_at DESC, id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()
	projects := make([]Project, 0)
	for rows.Next() {
		var project Project
		if err := rows.Scan(&project.ID, &project.Name, &project.Path, &project.CreatedAt, &project.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		projects = append(projects, project)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate projects: %w", err)
	}
	return projects, nil
}

func (s *Store) ListProjectsForUser(userID string, limit, offset int) ([]Project, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, errors.New("project user id is required")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	if offset < 0 {
		return nil, errors.New("project offset must be non-negative")
	}
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT id, name, path, created_at, updated_at FROM projects
		WHERE user_id = ? ORDER BY updated_at DESC, id DESC LIMIT ? OFFSET ?`, userID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list user projects: %w", err)
	}
	defer rows.Close()
	projects := make([]Project, 0)
	for rows.Next() {
		var project Project
		if err := rows.Scan(&project.ID, &project.Name, &project.Path, &project.CreatedAt, &project.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan user project: %w", err)
		}
		projects = append(projects, project)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate user projects: %w", err)
	}
	return projects, nil
}

func (s *Store) GetProject(id string) (Project, bool, error) {
	if s == nil || s.db == nil {
		return Project{}, false, errors.New("workspace store is closed")
	}
	if strings.TrimSpace(id) == "" {
		return Project{}, false, errors.New("project id is required")
	}
	var project Project
	err := s.db.QueryRowContext(context.Background(), `SELECT id, name, path, created_at, updated_at FROM projects WHERE id = ?`, id).Scan(&project.ID, &project.Name, &project.Path, &project.CreatedAt, &project.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, false, nil
	}
	if err != nil {
		return Project{}, false, fmt.Errorf("get project: %w", err)
	}
	return project, true, nil
}

func (s *Store) UpdateProject(id string, input UpdateProjectInput) (Project, error) {
	if s == nil || s.db == nil {
		return Project{}, errors.New("workspace store is closed")
	}
	if strings.TrimSpace(id) == "" {
		return Project{}, errors.New("project id is required")
	}
	if input.Name == nil && input.Path == nil {
		return Project{}, errors.New("at least one project field is required")
	}
	result, err := s.db.ExecContext(context.Background(), `
		UPDATE projects SET name = COALESCE(?, name), path = COALESCE(?, path), updated_at = ? WHERE id = ?`, input.Name, input.Path, s.now().UTC(), id)
	if err != nil {
		return Project{}, fmt.Errorf("update project: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return Project{}, fmt.Errorf("count updated projects: %w", err)
	}
	if changed != 1 {
		return Project{}, fmt.Errorf("project %q does not exist", id)
	}
	project, ok, err := s.GetProject(id)
	if err != nil {
		return Project{}, err
	}
	if !ok {
		return Project{}, fmt.Errorf("project %q was removed during update", id)
	}
	return project, nil
}

func (s *Store) DeleteProject(id string) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	if strings.TrimSpace(id) == "" {
		return errors.New("project id is required")
	}
	ctx := context.Background()
	rows, err := s.db.QueryContext(ctx, `
		SELECT version.storage_path
		FROM artifact_versions AS version
		JOIN artifacts AS artifact ON artifact.id = version.artifact_id
		WHERE artifact.project_id = ? AND version.storage_path <> ''`, id)
	if err != nil {
		return fmt.Errorf("list project artifact blobs before delete: %w", err)
	}
	paths := []string{}
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan project artifact blob before delete: %w", err)
		}
		paths = append(paths, path)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate project artifact blobs before delete: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close project artifact blob scan: %w", err)
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count deleted projects: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("project %q does not exist", id)
	}
	for _, path := range paths {
		absolute, err := s.blobAbsolute(path)
		if err != nil {
			return fmt.Errorf("clean deleted project artifact blob: %w", err)
		}
		if err := os.Remove(absolute); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("clean deleted project artifact blob: %w", err)
		}
	}
	return nil
}

// CreateFrame creates a root conversation or a child delegation frame. A child
// inherits the root frame and receives a monotonically increasing root sequence.
func (s *Store) CreateFrame(input CreateFrameInput) (Frame, error) {
	if s == nil || s.db == nil {
		return Frame{}, errors.New("workspace store is closed")
	}
	if strings.TrimSpace(input.ID) == "" || strings.TrimSpace(input.ProjectID) == "" {
		return Frame{}, errors.New("frame id and project id are required")
	}
	if strings.TrimSpace(input.AgentName) == "" || strings.TrimSpace(input.Status) == "" || strings.TrimSpace(input.ConversationType) == "" {
		return Frame{}, errors.New("frame agent name, status, and conversation type are required")
	}
	status, err := canonicalFrameStatus(input.Status)
	if err != nil {
		return Frame{}, err
	}
	input.Status = status
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Frame{}, fmt.Errorf("begin frame transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var projectID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM projects WHERE id = ?`, input.ProjectID).Scan(&projectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Frame{}, fmt.Errorf("project %q does not exist", input.ProjectID)
		}
		return Frame{}, fmt.Errorf("look up frame project: %w", err)
	}

	rootID := input.ID
	rootSequence := int64(0)
	if input.ParentFrameID != "" {
		var parentProjectID string
		if err := tx.QueryRowContext(ctx, `SELECT project_id, root_frame_id FROM frames WHERE id = ?`, input.ParentFrameID).Scan(&parentProjectID, &rootID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return Frame{}, fmt.Errorf("parent frame %q does not exist", input.ParentFrameID)
			}
			return Frame{}, fmt.Errorf("look up parent frame: %w", err)
		}
		if parentProjectID != input.ProjectID {
			return Frame{}, fmt.Errorf("parent frame %q belongs to project %q, not %q", input.ParentFrameID, parentProjectID, input.ProjectID)
		}
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(root_sequence), 0) + 1 FROM frames WHERE root_frame_id = ?`, rootID).Scan(&rootSequence); err != nil {
			return Frame{}, fmt.Errorf("allocate root sequence: %w", err)
		}
	}
	now := s.now().UTC()
	frame := Frame{
		ID: input.ID, IncarnationID: uuid.NewString(), ProjectID: input.ProjectID,
		ParentFrameID: input.ParentFrameID, RootFrameID: rootID,
		RootSequence: rootSequence, AgentName: input.AgentName, Status: input.Status, ConversationType: input.ConversationType,
		Name: input.Name, CreatedAt: now, UpdatedAt: now,
	}
	var incarnationColumn int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM pragma_table_info('frames') WHERE name='incarnation_id'`,
	).Scan(&incarnationColumn); err != nil {
		return Frame{}, fmt.Errorf("inspect frame incarnation schema: %w", err)
	}
	if incarnationColumn == 1 {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO frames (id, incarnation_id, project_id, parent_frame_id, root_frame_id, root_sequence, agent_name, status, conversation_type, name, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, frame.ID, frame.IncarnationID, frame.ProjectID,
			nullableString(frame.ParentFrameID), frame.RootFrameID, frame.RootSequence, frame.AgentName,
			frame.Status, frame.ConversationType, frame.Name, frame.CreatedAt, frame.UpdatedAt); err != nil {
			return Frame{}, fmt.Errorf("insert frame: %w", err)
		}
	} else {
		frame.IncarnationID = ""
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO frames (id, project_id, parent_frame_id, root_frame_id, root_sequence, agent_name, status, conversation_type, name, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, frame.ID, frame.ProjectID,
			nullableString(frame.ParentFrameID), frame.RootFrameID, frame.RootSequence, frame.AgentName,
			frame.Status, frame.ConversationType, frame.Name, frame.CreatedAt, frame.UpdatedAt); err != nil {
			return Frame{}, fmt.Errorf("insert legacy frame before incarnation migration: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Frame{}, fmt.Errorf("commit frame: %w", err)
	}
	return frame, nil
}

func (s *Store) GetFrame(id string) (Frame, bool, error) {
	db := s.readDatabase()
	if db == nil {
		return Frame{}, false, errors.New("workspace store is closed")
	}
	var frame Frame
	err := db.QueryRowContext(context.Background(), `
		SELECT frame.id, frame.project_id, COALESCE(frame.parent_frame_id, ''), frame.root_frame_id,
			frame.root_sequence, frame.agent_name, frame.status, frame.conversation_type, frame.name,
			frame.created_at, frame.updated_at,frame.incarnation_id
		FROM frames AS frame WHERE frame.id = ?`, id).Scan(
		&frame.ID, &frame.ProjectID, &frame.ParentFrameID, &frame.RootFrameID, &frame.RootSequence,
		&frame.AgentName, &frame.Status, &frame.ConversationType, &frame.Name, &frame.CreatedAt,
		&frame.UpdatedAt, &frame.IncarnationID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Frame{}, false, nil
	}
	if err != nil {
		return Frame{}, false, fmt.Errorf("get frame: %w", err)
	}
	return frame, true, nil
}

func (s *Store) GetFrameRealtimeContext(id string) (FrameRealtimeContext, bool, error) {
	return s.GetFrameRealtimeContextWithContext(context.Background(), id)
}

// GetFrameRealtimeContextWithContext is the cancellable read-side variant
// used by runner model authority resolution.
func (s *Store) GetFrameRealtimeContextWithContext(ctx context.Context, id string) (FrameRealtimeContext, bool, error) {
	db := s.readDatabase()
	if db == nil {
		return FrameRealtimeContext{}, false, errors.New("workspace store is closed")
	}
	if ctx == nil {
		return FrameRealtimeContext{}, false, errors.New("frame realtime context is required")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return FrameRealtimeContext{}, false, errors.New("frame id is required")
	}
	var result FrameRealtimeContext
	err := db.QueryRowContext(ctx, `
		SELECT p.user_id, f.id, f.project_id, COALESCE(f.parent_frame_id, ''),
			f.root_frame_id, f.root_sequence, f.agent_name, f.status,
			f.conversation_type, f.name, f.created_at, f.updated_at
		FROM frames f JOIN projects p ON p.id = f.project_id
		WHERE f.id = ?`, id).Scan(
		&result.UserID, &result.Frame.ID, &result.Frame.ProjectID,
		&result.Frame.ParentFrameID, &result.Frame.RootFrameID,
		&result.Frame.RootSequence, &result.Frame.AgentName, &result.Frame.Status,
		&result.Frame.ConversationType, &result.Frame.Name,
		&result.Frame.CreatedAt, &result.Frame.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return FrameRealtimeContext{}, false, nil
	}
	if err != nil {
		return FrameRealtimeContext{}, false, fmt.Errorf("get frame realtime context: %w", err)
	}
	return result, true, nil
}

func (s *Store) ListFrames(projectID string, limit, offset int) ([]Frame, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	if strings.TrimSpace(projectID) == "" {
		return nil, errors.New("frame project id is required")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	if offset < 0 {
		return nil, errors.New("frame offset must be non-negative")
	}
	rows, err := s.db.QueryContext(context.Background(), `SELECT id, project_id, COALESCE(parent_frame_id, ''), root_frame_id, root_sequence, agent_name, status, conversation_type, name, created_at, updated_at FROM frames WHERE project_id = ? ORDER BY created_at, id LIMIT ? OFFSET ?`, projectID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list frames: %w", err)
	}
	defer rows.Close()
	frames := make([]Frame, 0)
	for rows.Next() {
		var f Frame
		if err := rows.Scan(&f.ID, &f.ProjectID, &f.ParentFrameID, &f.RootFrameID, &f.RootSequence, &f.AgentName, &f.Status, &f.ConversationType, &f.Name, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan frame: %w", err)
		}
		frames = append(frames, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate frames: %w", err)
	}
	return frames, nil
}

func (s *Store) UpdateFrame(id string, input UpdateFrameInput) (Frame, error) {
	if s == nil || s.db == nil {
		return Frame{}, errors.New("workspace store is closed")
	}
	if id == "" || (input.Status == nil && input.Name == nil) {
		return Frame{}, errors.New("frame id and at least one update field are required")
	}
	status, err := canonicalFrameStatusPointer(input.Status)
	if err != nil {
		return Frame{}, err
	}
	input.Status = status
	result, err := s.db.ExecContext(context.Background(), `UPDATE frames SET status = COALESCE(?, status), name = COALESCE(?, name), updated_at = ? WHERE id = ?`, input.Status, input.Name, s.now().UTC(), id)
	if err != nil {
		return Frame{}, fmt.Errorf("update frame: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return Frame{}, err
	}
	if changed != 1 {
		return Frame{}, fmt.Errorf("frame %q does not exist", id)
	}
	frame, ok, err := s.GetFrame(id)
	if err != nil {
		return Frame{}, err
	}
	if !ok {
		return Frame{}, fmt.Errorf("frame %q was removed during update", id)
	}
	s.signalKernelRetentionWake()
	return frame, nil
}

func (s *Store) DeleteFrame(id string) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	result, err := s.db.ExecContext(context.Background(), `DELETE FROM frames WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete frame: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("frame %q does not exist", id)
	}
	return nil
}

// CreateAgent persists a user-scoped agent definition. The database enforces
// that names may be reused by different users but never collide for one user.
