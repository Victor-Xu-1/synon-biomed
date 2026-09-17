package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	transcriptstore "synon-go/internal/persistence/transcript"
)

// CreateProjectTx persists a project in a caller-owned transaction. Callers
// that also publish realtime state must enqueue it before committing this tx.
func (s *Store) CreateProjectOutboxTx(ctx context.Context, tx *sql.Tx, input CreateProjectInput) (Project, error) {
	if s == nil || s.db == nil {
		return Project{}, errors.New("workspace store is closed")
	}
	if tx == nil {
		return Project{}, errors.New("project transaction is required")
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
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO projects (id, user_id, name, path, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`, input.ID, input.UserID, input.Name, input.Path, now, now); err != nil {
		return Project{}, fmt.Errorf("insert project: %w", err)
	}
	return Project{ID: input.ID, Name: input.Name, Path: input.Path, CreatedAt: now, UpdatedAt: now}, nil
}

func (s *Store) UpdateProjectOutboxTx(ctx context.Context, tx *sql.Tx, id, ownerUserID string, input UpdateProjectInput) (Project, error) {
	if s == nil || s.db == nil {
		return Project{}, errors.New("workspace store is closed")
	}
	if tx == nil {
		return Project{}, errors.New("project transaction is required")
	}
	id, ownerUserID = strings.TrimSpace(id), strings.TrimSpace(ownerUserID)
	if id == "" || ownerUserID == "" {
		return Project{}, errors.New("project id and owner user id are required")
	}
	if input.Name == nil && input.Path == nil {
		return Project{}, errors.New("at least one project field is required")
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE projects SET name = COALESCE(?, name), path = COALESCE(?, path), updated_at = ?
		WHERE id = ? AND user_id = ?`, input.Name, input.Path, s.now().UTC(), id, ownerUserID)
	if err != nil {
		return Project{}, fmt.Errorf("update project: %w", err)
	}
	if err := requireOneMutationRow(result, "project", id); err != nil {
		return Project{}, err
	}
	return scanProject(tx.QueryRowContext(ctx, `
		SELECT id, name, path, created_at, updated_at FROM projects
		WHERE id = ? AND user_id = ?`, id, ownerUserID))
}

// DeleteProjectTx returns external blob paths that may be removed only after a
// successful commit. SQLite rows and the corresponding outbox event remain
// atomic even though filesystem garbage collection cannot join that tx.
func (s *Store) DeleteProjectOutboxTx(ctx context.Context, tx *sql.Tx, id, ownerUserID string) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	if tx == nil {
		return nil, errors.New("project transaction is required")
	}
	id, ownerUserID = strings.TrimSpace(id), strings.TrimSpace(ownerUserID)
	if id == "" || ownerUserID == "" {
		return nil, errors.New("project id and owner user id are required")
	}
	paths := make([]string, 0)
	deletedRoots, err := s.deleteProjectRootsTx(ctx, tx, ownerUserID, id)
	if err != nil {
		return nil, err
	}
	for _, deletedRoot := range deletedRoots {
		paths = append(paths, deletedRoot.BlobPaths...)
	}
	if _, err := transcriptstore.DeleteProjectStreamsTx(ctx, tx, ownerUserID, id); err != nil {
		return nil, fmt.Errorf("delete project transcript streams: %w", err)
	}
	artifactRows, err := tx.QueryContext(ctx, `
		SELECT version.storage_path
		FROM artifact_versions AS version
		JOIN artifacts AS artifact ON artifact.id = version.artifact_id
		JOIN projects AS project ON project.id = artifact.project_id
		WHERE project.id = ? AND project.user_id = ? AND version.storage_path <> ''`, id, ownerUserID)
	if err != nil {
		return nil, fmt.Errorf("list project artifact blobs before delete: %w", err)
	}
	for artifactRows.Next() {
		var path string
		if err := artifactRows.Scan(&path); err != nil {
			_ = artifactRows.Close()
			return nil, fmt.Errorf("scan project artifact blob before delete: %w", err)
		}
		paths = append(paths, path)
	}
	if err := artifactRows.Err(); err != nil {
		_ = artifactRows.Close()
		return nil, fmt.Errorf("iterate project artifact blobs before delete: %w", err)
	}
	if err := artifactRows.Close(); err != nil {
		return nil, fmt.Errorf("close project artifact blob scan: %w", err)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE id = ? AND user_id = ?`, id, ownerUserID)
	if err != nil {
		return nil, fmt.Errorf("delete project: %w", err)
	}
	if err := requireOneMutationRow(result, "project", id); err != nil {
		return nil, err
	}
	return paths, nil
}

func scanProject(row rowScanner) (Project, error) {
	var project Project
	if err := row.Scan(&project.ID, &project.Name, &project.Path, &project.CreatedAt, &project.UpdatedAt); err != nil {
		return Project{}, fmt.Errorf("read project mutation result: %w", err)
	}
	return project, nil
}

func (s *Store) CreateFrameOutboxTx(ctx context.Context, tx *sql.Tx, input CreateFrameInput, ownerUserID string) (Frame, error) {
	if tx == nil {
		return Frame{}, errors.New("frame transaction is required")
	}
	return s.createFrameOutboxTransaction(ctx, tx, input, ownerUserID)
}

func (s *Store) createFrameOutboxTransaction(
	ctx context.Context, tx workspaceTransaction, input CreateFrameInput, ownerUserID string,
) (Frame, error) {
	if s == nil || s.db == nil {
		return Frame{}, errors.New("workspace store is closed")
	}
	if tx == nil {
		return Frame{}, errors.New("frame transaction is required")
	}
	ownerUserID = strings.TrimSpace(ownerUserID)
	if strings.TrimSpace(input.ID) == "" || strings.TrimSpace(input.ProjectID) == "" || ownerUserID == "" {
		return Frame{}, errors.New("frame id, project id, and owner user id are required")
	}
	if strings.TrimSpace(input.AgentName) == "" || strings.TrimSpace(input.Status) == "" || strings.TrimSpace(input.ConversationType) == "" {
		return Frame{}, errors.New("frame agent name, status, and conversation type are required")
	}
	status, err := canonicalFrameStatus(input.Status)
	if err != nil {
		return Frame{}, err
	}
	input.Status = status
	var projectID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM projects WHERE id = ? AND user_id = ?`, input.ProjectID, ownerUserID).Scan(&projectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Frame{}, fmt.Errorf("project %q does not exist", input.ProjectID)
		}
		return Frame{}, fmt.Errorf("look up frame project: %w", err)
	}
	rootID, rootSequence := input.ID, int64(0)
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
	frame := Frame{ID: input.ID, IncarnationID: uuid.NewString(), ProjectID: input.ProjectID,
		ParentFrameID: input.ParentFrameID, RootFrameID: rootID,
		RootSequence: rootSequence, AgentName: input.AgentName, Status: input.Status,
		ConversationType: input.ConversationType, Name: input.Name, CreatedAt: now, UpdatedAt: now}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO frames (id, incarnation_id, project_id, parent_frame_id, root_frame_id, root_sequence, agent_name, status, conversation_type, name, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, frame.ID, frame.IncarnationID, frame.ProjectID,
		nullableString(frame.ParentFrameID), frame.RootFrameID, frame.RootSequence, frame.AgentName,
		frame.Status, frame.ConversationType, frame.Name, frame.CreatedAt, frame.UpdatedAt); err != nil {
		return Frame{}, fmt.Errorf("insert frame: %w", err)
	}
	return frame, nil
}

func (s *Store) UpdateFrameOutboxTx(ctx context.Context, tx *sql.Tx, id, ownerUserID string, input UpdateFrameInput) (Frame, error) {
	if s == nil || s.db == nil {
		return Frame{}, errors.New("workspace store is closed")
	}
	if tx == nil {
		return Frame{}, errors.New("frame transaction is required")
	}
	id, ownerUserID = strings.TrimSpace(id), strings.TrimSpace(ownerUserID)
	if id == "" || ownerUserID == "" || (input.Status == nil && input.Name == nil) {
		return Frame{}, errors.New("frame id, owner user id, and at least one update field are required")
	}
	status, err := canonicalFrameStatusPointer(input.Status)
	if err != nil {
		return Frame{}, err
	}
	input.Status = status
	result, err := tx.ExecContext(ctx, `
		UPDATE frames SET status = COALESCE(?, status), name = COALESCE(?, name), updated_at = ?
		WHERE id = ? AND project_id IN (SELECT id FROM projects WHERE user_id = ?)`, input.Status, input.Name, s.now().UTC(), id, ownerUserID)
	if err != nil {
		return Frame{}, fmt.Errorf("update frame: %w", err)
	}
	if err := requireOneMutationRow(result, "frame", id); err != nil {
		return Frame{}, err
	}
	return scanFrame(tx.QueryRowContext(ctx, `
		SELECT id, project_id, COALESCE(parent_frame_id, ''), root_frame_id, root_sequence,
			agent_name, status, conversation_type, name, created_at, updated_at
		FROM frames WHERE id = ?`, id))
}

func (s *Store) DeleteFrameOutboxTx(ctx context.Context, tx *sql.Tx, expected Frame, ownerUserID string) error {
	return s.deleteSingleFrameTx(ctx, tx, expected, ownerUserID)
}

func scanFrame(row rowScanner) (Frame, error) {
	var frame Frame
	if err := row.Scan(&frame.ID, &frame.ProjectID, &frame.ParentFrameID, &frame.RootFrameID,
		&frame.RootSequence, &frame.AgentName, &frame.Status, &frame.ConversationType,
		&frame.Name, &frame.CreatedAt, &frame.UpdatedAt); err != nil {
		return Frame{}, fmt.Errorf("read frame mutation result: %w", err)
	}
	return frame, nil
}

func (s *Store) AppendFrameOutboxEventTx(ctx context.Context, tx *sql.Tx, input FrameEventInput) (FrameEvent, error) {
	if tx == nil {
		return FrameEvent{}, errors.New("frame event transaction is required")
	}
	return s.appendFrameOutboxEventTransaction(ctx, tx, input)
}

func (s *Store) appendFrameOutboxEventTransaction(
	ctx context.Context, tx workspaceTransaction, input FrameEventInput,
) (FrameEvent, error) {
	if s == nil || s.db == nil {
		return FrameEvent{}, errors.New("workspace store is closed")
	}
	if tx == nil {
		return FrameEvent{}, errors.New("frame event transaction is required")
	}
	if strings.TrimSpace(input.FrameID) == "" || strings.TrimSpace(input.Type) == "" {
		return FrameEvent{}, errors.New("frame event frame id and type are required")
	}
	payload, rawPayload, err := normalizeOutboxFrameEventPayload(input.Payload)
	if err != nil {
		return FrameEvent{}, err
	}
	input.ID = strings.TrimSpace(input.ID)
	if input.ID != "" {
		existing, storedPayload, found, err := frameEventByID(ctx, tx, input.ID)
		if err != nil {
			return FrameEvent{}, err
		}
		if found {
			if existing.FrameID != input.FrameID || existing.Type != input.Type || storedPayload != string(rawPayload) {
				return FrameEvent{}, fmt.Errorf("frame event id %q already identifies a different frame event", input.ID)
			}
			return existing, nil
		}
	}
	var frameID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM frames WHERE id = ?`, input.FrameID).Scan(&frameID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return FrameEvent{}, fmt.Errorf("frame %q does not exist", input.FrameID)
		}
		return FrameEvent{}, fmt.Errorf("look up event frame: %w", err)
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) + 1 FROM frame_events WHERE frame_id = ?`, input.FrameID).Scan(&sequence); err != nil {
		return FrameEvent{}, fmt.Errorf("allocate frame event sequence: %w", err)
	}
	if input.ID == "" {
		input.ID = uuid.NewString()
	}
	event := FrameEvent{ID: input.ID, FrameID: input.FrameID, Sequence: sequence, Type: input.Type, Payload: payload, CreatedAt: s.now().UTC()}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO frame_events (id, frame_id, sequence, event_type, payload, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, event.ID, event.FrameID, event.Sequence, event.Type, string(rawPayload), event.CreatedAt); err != nil {
		return FrameEvent{}, fmt.Errorf("insert frame event: %w", err)
	}
	return event, nil
}

func (s *Store) CreateRoutineOutboxTx(ctx context.Context, tx *sql.Tx, input CreateRoutineInput) (Routine, error) {
	if s == nil || s.db == nil {
		return Routine{}, errors.New("workspace store is closed")
	}
	if tx == nil {
		return Routine{}, errors.New("routine transaction is required")
	}
	for field, value := range map[string]string{"routine id": input.ID, "root frame id": input.RootFrameID, "owner user id": input.OwnerUserID, "on-tick instruction": input.OnTick} {
		if strings.TrimSpace(value) == "" {
			return Routine{}, fmt.Errorf("%s is required", field)
		}
	}
	if input.EveryMinutes < 1 {
		return Routine{}, errors.New("routine interval must be at least one minute")
	}
	var rootFrameID, projectOwnerID string
	if err := tx.QueryRowContext(ctx, `
		SELECT frame.root_frame_id, project.user_id
		FROM frames AS frame JOIN projects AS project ON project.id = frame.project_id
		WHERE frame.id = ?`, input.RootFrameID).Scan(&rootFrameID, &projectOwnerID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Routine{}, fmt.Errorf("routine root frame %q does not exist", input.RootFrameID)
		}
		return Routine{}, fmt.Errorf("look up routine root frame: %w", err)
	}
	if rootFrameID != input.RootFrameID {
		return Routine{}, fmt.Errorf("routine frame %q is not a root frame", input.RootFrameID)
	}
	if projectOwnerID != input.OwnerUserID {
		return Routine{}, fmt.Errorf("routine owner %q does not own root frame %q", input.OwnerUserID, input.RootFrameID)
	}
	now := s.now().UTC()
	nextDue := input.NextDue.UTC()
	if nextDue.IsZero() {
		nextDue = now.Add(time.Duration(input.EveryMinutes) * time.Minute)
	}
	routine := Routine{ID: input.ID, RootFrameID: input.RootFrameID, OwnerUserID: input.OwnerUserID,
		Label: input.Label, OnTick: input.OnTick, EveryMinutes: input.EveryMinutes,
		Enabled: input.Enabled, NextDue: nextDue, CreatedAt: now, UpdatedAt: now}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO routine_schedules (id, root_frame_id, owner_user_id, label, on_tick, every_minutes, enabled, next_due, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, routine.ID, routine.RootFrameID, routine.OwnerUserID,
		routine.Label, routine.OnTick, routine.EveryMinutes, routine.Enabled, routine.NextDue,
		routine.CreatedAt, routine.UpdatedAt); err != nil {
		return Routine{}, fmt.Errorf("insert routine: %w", err)
	}
	return routine, nil
}

func (s *Store) GetFrameRealtimeContextOutboxTx(ctx context.Context, tx *sql.Tx, id string) (FrameRealtimeContext, bool, error) {
	if tx == nil {
		return FrameRealtimeContext{}, false, errors.New("frame transaction is required")
	}
	var result FrameRealtimeContext
	err := tx.QueryRowContext(ctx, `
		SELECT p.user_id, f.id, f.project_id, COALESCE(f.parent_frame_id, ''), f.root_frame_id,
			f.root_sequence, f.agent_name, f.status, f.conversation_type, f.name, f.created_at, f.updated_at
		FROM frames f JOIN projects p ON p.id = f.project_id WHERE f.id = ?`, strings.TrimSpace(id)).Scan(
		&result.UserID, &result.Frame.ID, &result.Frame.ProjectID, &result.Frame.ParentFrameID,
		&result.Frame.RootFrameID, &result.Frame.RootSequence, &result.Frame.AgentName,
		&result.Frame.Status, &result.Frame.ConversationType, &result.Frame.Name,
		&result.Frame.CreatedAt, &result.Frame.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return FrameRealtimeContext{}, false, nil
	}
	if err != nil {
		return FrameRealtimeContext{}, false, fmt.Errorf("get frame realtime context: %w", err)
	}
	return result, true, nil
}

func requireOneMutationRow(result sql.Result, kind, id string) error {
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count %s mutation rows: %w", kind, err)
	}
	if changed != 1 {
		return fmt.Errorf("%s %q does not exist", kind, id)
	}
	return nil
}

func normalizeOutboxFrameEventPayload(input map[string]any) (map[string]any, []byte, error) {
	payload := input
	if payload == nil {
		payload = map[string]any{}
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal frame event payload: %w", err)
	}
	return payload, rawPayload, nil
}
