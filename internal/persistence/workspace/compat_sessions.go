package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

type CompatibilityProject struct {
	Project
	Description          string
	ContextData          any
	ArtifactCount        int
	ConversationCount    int
	LatestConversationID string
}

type CreateCompatibilityProjectInput struct {
	ID           string
	UserID       string
	Name         string
	Description  string
	ContextData  any
	FindOrCreate bool
}

type CompatibilityFrame struct {
	Frame
	TaskSummary string
	InputData   map[string]any
	IsHidden    bool
	ChildIDs    []string
}

// CompatibilityFrameSummary is the root-frame projection needed by the Web
// conversation sidebar. The page is read as one consistent bounded snapshot so
// listing conversations does not perform one frame lookup (and one child
// lookup) per row.
type CompatibilityFrameSummary struct {
	Frame       CompatibilityFrame
	ProjectName string
}

type CompatibilityFrameSummaryCursor struct {
	UpdatedAt time.Time
	FrameID   string
}

type UpdateCompatibilityFrameInput struct {
	Name        *string
	TaskSummary *string
}

type DeleteCompatibilityFrameResult struct {
	FrameID                 string
	RootFrameID             string
	FramesDeleted           int64
	ArtifactsDeleted        int64
	ArtifactCleanupFailures int
}

type CompatibilityMessagePage struct {
	Messages []map[string]any
	From     int
	Total    int
}

type CancelCompatibilityFrameResult struct {
	RootFrameID             string
	CancelledFrameIDs       []string
	CancelledResumeEventIDs []string
	Events                  []FrameEvent
}

type ResumeCompatibilityFrameInput struct {
	VerifierMode *string
	MemoryMode   *string
	PlanMode     *bool
	UltraMode    *bool
	TargetAgent  *string
	Model        *string
	// AllowProcessingInterrupted permits an explicit continue of a nonterminal
	// frame whose transcript holds a durable runner interruption (provider
	// interruption, correction-required, or drain). The caller must verify the
	// interruption exists; the frame remains processing and is not marked
	// failed/cancelled.
	AllowProcessingInterrupted bool
}

type ResumeCompatibilityFrameResult struct {
	RootFrameID     string
	ResumedFrameID  string
	AgentName       string
	LeafFramesReady int
	Event           *FrameEvent
}

const compatibilityMessagePredicate = `event_type IN (
	'message', 'user_message', 'assistant_message', 'system_message', 'tool_use', 'tool_result', 'ask_user_answer'
)`

func (s *Store) CreateCompatibilityProject(input CreateCompatibilityProjectInput) (CompatibilityProject, bool, error) {
	if s == nil || s.db == nil {
		return CompatibilityProject{}, false, errors.New("workspace store is closed")
	}
	input.ID = strings.TrimSpace(input.ID)
	input.UserID = strings.TrimSpace(input.UserID)
	input.Name = strings.TrimSpace(input.Name)
	if input.ID == "" || input.UserID == "" || input.Name == "" {
		return CompatibilityProject{}, false, errors.New("project id, user id, and name are required")
	}
	rawContext, err := json.Marshal(input.ContextData)
	if err != nil {
		return CompatibilityProject{}, false, fmt.Errorf("marshal project context: %w", err)
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CompatibilityProject{}, false, fmt.Errorf("begin compatibility project transaction: %w", err)
	}
	defer tx.Rollback()

	if input.FindOrCreate {
		project, found, err := compatibilityProjectByName(ctx, tx, input.UserID, input.Name)
		if err != nil {
			return CompatibilityProject{}, false, err
		}
		if found {
			if err := tx.Commit(); err != nil {
				return CompatibilityProject{}, false, fmt.Errorf("commit compatibility project lookup: %w", err)
			}
			return project, false, nil
		}
	}

	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO projects (id, user_id, name, path, created_at, updated_at)
		VALUES (?, ?, ?, '', ?, ?)`, input.ID, input.UserID, input.Name, now, now); err != nil {
		return CompatibilityProject{}, false, fmt.Errorf("insert compatibility project: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO project_runtime_metadata (project_id, description, context_data)
		VALUES (?, ?, ?)`, input.ID, input.Description, string(rawContext)); err != nil {
		return CompatibilityProject{}, false, fmt.Errorf("insert compatibility project metadata: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO artifact_folders (
			id, project_id, parent_id, name, sort_order, root_frame_id,
			is_conversation_folder, is_user_uploads_folder, created_at, updated_at
		) VALUES (?, ?, NULL, 'User Uploads', -1000, NULL, 0, 1, ?, ?)`,
		uuid.NewString(), input.ID, now, now); err != nil {
		return CompatibilityProject{}, false, fmt.Errorf("insert compatibility user uploads folder: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return CompatibilityProject{}, false, fmt.Errorf("commit compatibility project: %w", err)
	}
	return CompatibilityProject{
		Project:     Project{ID: input.ID, Name: input.Name, CreatedAt: now, UpdatedAt: now},
		Description: input.Description, ContextData: input.ContextData,
	}, true, nil
}

func (s *Store) ListCompatibilityProjects(userID string, limit, offset int) ([]CompatibilityProject, error) {
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
	// The Web conversation API requests one look-ahead row to determine
	// has_more while preserving its public 1000-item maximum.
	if limit > 1001 {
		limit = 1001
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(context.Background(), compatibilityProjectSelect+`
		WHERE p.user_id = ? ORDER BY p.updated_at DESC, p.id DESC LIMIT ? OFFSET ?`, userID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list compatibility projects: %w", err)
	}
	defer rows.Close()
	projects := make([]CompatibilityProject, 0)
	for rows.Next() {
		project, err := scanCompatibilityProject(rows)
		if err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate compatibility projects: %w", err)
	}
	return projects, nil
}

const compatibilityProjectSelect = `
	SELECT p.id, p.name, p.path, p.created_at, p.updated_at,
		COALESCE(m.description, ''), COALESCE(m.context_data, 'null'),
		(SELECT COUNT(*) FROM artifacts a WHERE a.project_id = p.id AND a.id NOT LIKE 'large-tool-result-%'),
		(SELECT COUNT(*) FROM frames f
			LEFT JOIN frame_runtime_metadata fm ON fm.frame_id = f.id
			WHERE f.project_id = p.id AND f.parent_frame_id IS NULL
				AND TRIM(COALESCE(f.name, '')) <> '' AND COALESCE(fm.is_hidden, 0) = 0),
		COALESCE((
			SELECT latest.id
			FROM frames latest
			LEFT JOIN frame_runtime_metadata latest_meta ON latest_meta.frame_id = latest.id
			WHERE latest.project_id = p.id
				AND latest.parent_frame_id IS NULL
				AND TRIM(COALESCE(latest.name, '')) <> ''
				AND COALESCE(latest_meta.is_hidden, 0) = 0
				AND latest.conversation_type <> 'uploads'
				AND latest.agent_name NOT IN ('CONCIERGE', 'CANVAS_CONCIERGE')
			ORDER BY MAX(
				julianday(latest.updated_at),
				COALESCE((
					SELECT MAX(julianday(descendant.updated_at))
					FROM frames descendant
					WHERE descendant.root_frame_id = latest.id
				), 0),
				COALESCE((
					SELECT MAX(julianday(event.created_at))
					FROM frame_events event
					JOIN frames event_frame ON event_frame.id = event.frame_id
					WHERE event_frame.root_frame_id = latest.id
				), 0)
			) DESC, latest.id DESC
			LIMIT 1
		), '')
	FROM projects p LEFT JOIN project_runtime_metadata m ON m.project_id = p.id `

func compatibilityProjectByName(ctx context.Context, tx *sql.Tx, userID, name string) (CompatibilityProject, bool, error) {
	project, err := scanCompatibilityProject(tx.QueryRowContext(ctx, compatibilityProjectSelect+`
		WHERE p.user_id = ? AND p.name = ? ORDER BY p.created_at, p.id LIMIT 1`, userID, name))
	if errors.Is(err, sql.ErrNoRows) {
		return CompatibilityProject{}, false, nil
	}
	if err != nil {
		return CompatibilityProject{}, false, err
	}
	return project, true, nil
}

type compatibilityProjectScanner interface {
	Scan(...any) error
}

func scanCompatibilityProject(scanner compatibilityProjectScanner) (CompatibilityProject, error) {
	var project CompatibilityProject
	var rawContext string
	if err := scanner.Scan(
		&project.ID, &project.Name, &project.Path, &project.CreatedAt, &project.UpdatedAt,
		&project.Description, &rawContext, &project.ArtifactCount, &project.ConversationCount,
		&project.LatestConversationID,
	); err != nil {
		return CompatibilityProject{}, err
	}
	if err := json.Unmarshal([]byte(rawContext), &project.ContextData); err != nil {
		return CompatibilityProject{}, fmt.Errorf("decode compatibility project context: %w", err)
	}
	return project, nil
}

func (s *Store) ListCompatibilityFrames(userID, projectID string, rootOnly bool, limit int) ([]CompatibilityFrame, error) {
	return s.listCompatibilityFrames(userID, projectID, rootOnly, limit, true)
}

func (s *Store) ListVisibleCompatibilityFrames(userID, projectID string, rootOnly bool, limit int) ([]CompatibilityFrame, error) {
	return s.listCompatibilityFrames(userID, projectID, rootOnly, limit, false)
}

// ListVisibleRootCompatibilityFrameSummaries returns the newest visible root
// frames and their project names in two bounded queries. It is intentionally
// separate from ListVisibleCompatibilityFrames: callers that need ChildIDs
// retain the complete compatibility-frame contract, while the sidebar avoids
// the former 2N+2 query pattern.
func (s *Store) ListVisibleRootCompatibilityFrameSummaries(
	ctx context.Context,
	userID string,
	limit int,
	before *CompatibilityFrameSummaryCursor,
) ([]CompatibilityFrameSummary, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, errors.New("frame user id is required")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	if before != nil && (before.UpdatedAt.IsZero() || strings.TrimSpace(before.FrameID) == "") {
		return nil, errors.New("valid visible root compatibility frame summary cursor is required")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin visible root compatibility frame summary snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	query := `
		SELECT frame.id, project.name
		FROM frames AS frame
		JOIN projects AS project ON project.id = frame.project_id
		LEFT JOIN frame_runtime_metadata AS metadata ON metadata.frame_id = frame.id
		WHERE project.user_id = ? AND frame.parent_frame_id IS NULL
			AND COALESCE(metadata.is_hidden, 0) = 0`
	args := []any{userID}
	if before != nil {
		query += ` AND (frame.updated_at < ? OR (frame.updated_at = ? AND frame.id < ?))`
		args = append(args, before.UpdatedAt, before.UpdatedAt, strings.TrimSpace(before.FrameID))
	}
	query += ` ORDER BY frame.updated_at DESC, frame.id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list visible root compatibility frame summaries: %w", err)
	}
	ids := make([]string, 0, limit)
	projectNames := make(map[string]string, limit)
	for rows.Next() {
		var id string
		var projectName string
		if err := rows.Scan(&id, &projectName); err != nil {
			return nil, fmt.Errorf("scan visible root compatibility frame summary: %w", err)
		}
		ids = append(ids, id)
		projectNames[id] = projectName
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate visible root compatibility frame summaries: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close visible root compatibility frame summaries: %w", err)
	}
	if len(ids) == 0 {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit empty visible root compatibility frame summary snapshot: %w", err)
		}
		return []CompatibilityFrameSummary{}, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	frameArgs := make([]any, len(ids))
	for index, id := range ids {
		frameArgs[index] = id
	}
	frameRows, err := tx.QueryContext(ctx, traceFrameSelectColumns+`
		WHERE frame.id IN (`+placeholders+`)`, frameArgs...)
	if err != nil {
		return nil, fmt.Errorf("load visible root compatibility frame summaries: %w", err)
	}
	defer frameRows.Close()
	framesByID := make(map[string]Frame, len(ids))
	for frameRows.Next() {
		frame, err := scanTraceFrame(frameRows)
		if err != nil {
			return nil, fmt.Errorf("scan visible root compatibility frame: %w", err)
		}
		framesByID[frame.ID] = frame
	}
	if err := frameRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate visible root compatibility frames: %w", err)
	}
	if err := frameRows.Close(); err != nil {
		return nil, fmt.Errorf("close visible root compatibility frames: %w", err)
	}
	summaries := make([]CompatibilityFrameSummary, 0, len(ids))
	for _, id := range ids {
		frame, found := framesByID[id]
		if !found {
			return nil, fmt.Errorf("visible root compatibility frame %q disappeared during listing", id)
		}
		summaries = append(summaries, CompatibilityFrameSummary{
			Frame: CompatibilityFrame{
				Frame: frame, TaskSummary: frame.TaskSummary, InputData: frame.InputData, IsHidden: frame.IsHidden,
			},
			ProjectName: projectNames[id],
		})
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit visible root compatibility frame summary snapshot: %w", err)
	}
	return summaries, nil
}

func (s *Store) listCompatibilityFrames(userID, projectID string, rootOnly bool, limit int, includeHidden bool) ([]CompatibilityFrame, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	userID, projectID = strings.TrimSpace(userID), strings.TrimSpace(projectID)
	if userID == "" {
		return nil, errors.New("frame user id is required")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	query := `SELECT f.id FROM frames f JOIN projects p ON p.id = f.project_id`
	if !includeHidden {
		query += ` LEFT JOIN frame_runtime_metadata m ON m.frame_id = f.id`
	}
	query += ` WHERE p.user_id = ?`
	args := []any{userID}
	if projectID != "" {
		query += ` AND f.project_id = ?`
		args = append(args, projectID)
	}
	if !includeHidden {
		query += ` AND COALESCE(m.is_hidden, 0) = 0`
	}
	if rootOnly {
		query += ` AND f.parent_frame_id IS NULL`
	}
	query += ` ORDER BY f.updated_at DESC, f.id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(context.Background(), query, args...)
	if err != nil {
		return nil, fmt.Errorf("list compatibility frames: %w", err)
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan compatibility frame id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate compatibility frame ids: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close compatibility frame ids: %w", err)
	}
	frames := make([]CompatibilityFrame, 0, len(ids))
	for _, id := range ids {
		frame, found, err := s.GetCompatibilityFrame(id)
		if err != nil {
			return nil, err
		}
		if found {
			frames = append(frames, frame)
		}
	}
	return frames, nil
}

func (s *Store) GetCompatibilityFrame(id string) (CompatibilityFrame, bool, error) {
	db := s.readDatabase()
	if db == nil {
		return CompatibilityFrame{}, false, errors.New("workspace store is closed")
	}
	frame, found, err := queryTraceFrame(context.Background(), db, strings.TrimSpace(id))
	if err != nil || !found {
		return CompatibilityFrame{}, found, err
	}
	rows, err := db.QueryContext(context.Background(), `SELECT id FROM frames WHERE parent_frame_id = ? ORDER BY root_sequence, id`, frame.ID)
	if err != nil {
		return CompatibilityFrame{}, false, fmt.Errorf("list compatibility frame children: %w", err)
	}
	childIDs := make([]string, 0)
	for rows.Next() {
		var childID string
		if err := rows.Scan(&childID); err != nil {
			_ = rows.Close()
			return CompatibilityFrame{}, false, fmt.Errorf("scan compatibility frame child: %w", err)
		}
		childIDs = append(childIDs, childID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return CompatibilityFrame{}, false, fmt.Errorf("iterate compatibility frame children: %w", err)
	}
	if err := rows.Close(); err != nil {
		return CompatibilityFrame{}, false, fmt.Errorf("close compatibility frame children: %w", err)
	}
	return CompatibilityFrame{
		Frame: frame, TaskSummary: frame.TaskSummary, InputData: frame.InputData,
		IsHidden: frame.IsHidden, ChildIDs: childIDs,
	}, true, nil
}

func (s *Store) UpdateCompatibilityFrame(id string, input UpdateCompatibilityFrameInput) (CompatibilityFrame, error) {
	if s == nil || s.db == nil {
		return CompatibilityFrame{}, errors.New("workspace store is closed")
	}
	id = strings.TrimSpace(id)
	if id == "" || input.Name == nil && input.TaskSummary == nil {
		return CompatibilityFrame{}, errors.New("frame id and at least one metadata field are required")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CompatibilityFrame{}, fmt.Errorf("begin compatibility frame update: %w", err)
	}
	defer tx.Rollback()
	var foundID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM frames WHERE id = ?`, id).Scan(&foundID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CompatibilityFrame{}, fmt.Errorf("frame %q does not exist", id)
		}
		return CompatibilityFrame{}, fmt.Errorf("load compatibility frame: %w", err)
	}
	if input.Name != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE frames SET name = ?, updated_at = ? WHERE id = ?`, strings.TrimSpace(*input.Name), s.now().UTC(), id); err != nil {
			return CompatibilityFrame{}, fmt.Errorf("update compatibility frame name: %w", err)
		}
	}
	if input.TaskSummary != nil {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO frame_runtime_metadata (frame_id, task_summary) VALUES (?, ?)
			ON CONFLICT(frame_id) DO UPDATE SET task_summary = excluded.task_summary`, id, strings.TrimSpace(*input.TaskSummary)); err != nil {
			return CompatibilityFrame{}, fmt.Errorf("update compatibility frame task summary: %w", err)
		}
		if input.Name == nil {
			if _, err := tx.ExecContext(ctx, `UPDATE frames SET updated_at = ? WHERE id = ?`, s.now().UTC(), id); err != nil {
				return CompatibilityFrame{}, fmt.Errorf("advance compatibility frame revision: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return CompatibilityFrame{}, fmt.Errorf("commit compatibility frame update: %w", err)
	}
	updated, found, err := s.GetCompatibilityFrame(id)
	if err != nil {
		return CompatibilityFrame{}, err
	}
	if !found {
		return CompatibilityFrame{}, fmt.Errorf("frame %q disappeared after update", id)
	}
	return updated, nil
}

func (s *Store) CancelCompatibilityFrameTree(id, reason string) (CancelCompatibilityFrameResult, error) {
	if s == nil || s.db == nil {
		return CancelCompatibilityFrameResult{}, errors.New("workspace store is closed")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return CancelCompatibilityFrameResult{}, errors.New("frame id is required")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CancelCompatibilityFrameResult{}, fmt.Errorf("begin compatibility frame cancellation: %w", err)
	}
	defer tx.Rollback()
	result, err := cancelCompatibilityFrameTreeInTransaction(ctx, tx, id, reason, s.now().UTC())
	if err != nil {
		return CancelCompatibilityFrameResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CancelCompatibilityFrameResult{}, fmt.Errorf("commit compatibility frame cancellation: %w", err)
	}
	return result, nil
}

func cancelCompatibilityFrameTreeInTransaction(
	ctx context.Context,
	tx workspaceTransaction,
	id, reason string,
	now time.Time,
) (CancelCompatibilityFrameResult, error) {
	var rootID, projectID, ownerUserID string
	if err := tx.QueryRowContext(ctx, `
		SELECT frame.root_frame_id, frame.project_id, project.user_id
		FROM frames AS frame JOIN projects AS project ON project.id = frame.project_id
		WHERE frame.id = ?`, id).Scan(&rootID, &projectID, &ownerUserID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CancelCompatibilityFrameResult{}, fmt.Errorf("frame %q does not exist", id)
		}
		return CancelCompatibilityFrameResult{}, fmt.Errorf("load compatibility frame cancellation root: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT id, status FROM frames WHERE root_frame_id = ? ORDER BY root_sequence, id`, rootID)
	if err != nil {
		return CancelCompatibilityFrameResult{}, fmt.Errorf("list compatibility frame cancellation tree: %w", err)
	}
	type frameStatus struct {
		ID     string
		Status string
	}
	frames := make([]frameStatus, 0)
	for rows.Next() {
		var frame frameStatus
		if err := rows.Scan(&frame.ID, &frame.Status); err != nil {
			_ = rows.Close()
			return CancelCompatibilityFrameResult{}, fmt.Errorf("scan compatibility frame cancellation tree: %w", err)
		}
		frames = append(frames, frame)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return CancelCompatibilityFrameResult{}, fmt.Errorf("iterate compatibility frame cancellation tree: %w", err)
	}
	if err := rows.Close(); err != nil {
		return CancelCompatibilityFrameResult{}, fmt.Errorf("close compatibility frame cancellation tree: %w", err)
	}
	result := CancelCompatibilityFrameResult{
		RootFrameID: rootID, CancelledFrameIDs: make([]string, 0, len(frames)), Events: make([]FrameEvent, 0, len(frames)),
	}
	for _, frame := range frames {
		switch frame.Status {
		case "cancelled":
			result.CancelledFrameIDs = append(result.CancelledFrameIDs, frame.ID)
			continue
		case "completed", "failed":
			continue
		}
		update, err := tx.ExecContext(ctx, `UPDATE frames SET status = 'cancelled', updated_at = ? WHERE id = ? AND status = ?`, now, frame.ID, frame.Status)
		if err != nil {
			return CancelCompatibilityFrameResult{}, fmt.Errorf("cancel compatibility frame %q: %w", frame.ID, err)
		}
		updated, err := update.RowsAffected()
		if err != nil {
			return CancelCompatibilityFrameResult{}, fmt.Errorf("read compatibility frame %q cancellation result: %w", frame.ID, err)
		}
		if updated != 1 {
			return CancelCompatibilityFrameResult{}, fmt.Errorf("compatibility frame %q changed during cancellation", frame.ID)
		}
		if err := clearFramePendingInputsForCancellation(ctx, tx, frame.ID); err != nil {
			return CancelCompatibilityFrameResult{}, err
		}
		result.CancelledFrameIDs = append(result.CancelledFrameIDs, frame.ID)
		event, err := appendFrameLifecycleEvent(ctx, tx, frame.ID, "frame_cancelled", map[string]any{
			"previousStatus": frame.Status, "reason": strings.TrimSpace(reason), "rootFrameId": rootID,
		}, now)
		if err != nil {
			return CancelCompatibilityFrameResult{}, err
		}
		result.Events = append(result.Events, event)
	}
	dispatchEvents, dispatchEventIDs, err := cancelCompatibilityFrameResumeDispatches(ctx, tx, rootID, reason, now)
	if err != nil {
		return CancelCompatibilityFrameResult{}, err
	}
	result.Events = append(result.Events, dispatchEvents...)
	result.CancelledResumeEventIDs = append(result.CancelledResumeEventIDs, dispatchEventIDs...)
	return result, nil
}

type compatibilityResumeFrameState struct {
	ID            string
	ParentFrameID string
	AgentName     string
	Status        string
	RootSequence  int64
}

func (s *Store) ResumeCompatibilityFrameConversation(rootID string, input ResumeCompatibilityFrameInput) (ResumeCompatibilityFrameResult, error) {
	if s == nil || s.db == nil {
		return ResumeCompatibilityFrameResult{}, errors.New("workspace store is closed")
	}
	rootID = strings.TrimSpace(rootID)
	if rootID == "" {
		return ResumeCompatibilityFrameResult{}, errors.New("root frame id is required")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ResumeCompatibilityFrameResult{}, fmt.Errorf("begin compatibility frame resume: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
		SELECT id, parent_frame_id, agent_name, status, root_sequence
		FROM frames WHERE root_frame_id = ? ORDER BY root_sequence, id`, rootID)
	if err != nil {
		return ResumeCompatibilityFrameResult{}, fmt.Errorf("list compatibility frame resume tree: %w", err)
	}
	frames := make([]compatibilityResumeFrameState, 0)
	for rows.Next() {
		var frame compatibilityResumeFrameState
		var parentID sql.NullString
		if err := rows.Scan(&frame.ID, &parentID, &frame.AgentName, &frame.Status, &frame.RootSequence); err != nil {
			_ = rows.Close()
			return ResumeCompatibilityFrameResult{}, fmt.Errorf("scan compatibility frame resume tree: %w", err)
		}
		if parentID.Valid {
			frame.ParentFrameID = parentID.String
		}
		frames = append(frames, frame)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return ResumeCompatibilityFrameResult{}, fmt.Errorf("iterate compatibility frame resume tree: %w", err)
	}
	if err := rows.Close(); err != nil {
		return ResumeCompatibilityFrameResult{}, fmt.Errorf("close compatibility frame resume tree: %w", err)
	}
	if len(frames) == 0 {
		return ResumeCompatibilityFrameResult{}, fmt.Errorf("conversation %q does not exist", rootID)
	}

	rootIndex := -1
	for index := range frames {
		if frames[index].ID == rootID && frames[index].ParentFrameID == "" {
			rootIndex = index
			break
		}
	}
	if rootIndex < 0 {
		return ResumeCompatibilityFrameResult{}, fmt.Errorf("conversation %q does not exist", rootID)
	}
	if existing, found, err := activeCompatibilityResume(ctx, tx, rootID); err != nil {
		return ResumeCompatibilityFrameResult{}, err
	} else if found {
		if err := tx.Commit(); err != nil {
			return ResumeCompatibilityFrameResult{}, fmt.Errorf("commit compatibility frame resume lookup: %w", err)
		}
		return existing, nil
	}

	resumable := make([]compatibilityResumeFrameState, 0)
	resumableParents := make(map[string]bool)
	for _, frame := range frames {
		if frame.Status != "cancelled" && frame.Status != "failed" &&
			!(input.AllowProcessingInterrupted && frame.Status == FrameStatusProcessing) {
			continue
		}
		resumable = append(resumable, frame)
		if frame.ParentFrameID != "" {
			resumableParents[frame.ParentFrameID] = true
		}
	}
	if len(resumable) == 0 {
		return ResumeCompatibilityFrameResult{}, fmt.Errorf(
			"conversation is not in a resumable state: %s", frames[rootIndex].Status,
		)
	}
	leaves := make([]compatibilityResumeFrameState, 0, len(resumable))
	for _, frame := range resumable {
		if !resumableParents[frame.ID] {
			leaves = append(leaves, frame)
		}
	}

	selected := compatibilityResumeFrameState{}
	if frames[rootIndex].Status == "cancelled" || frames[rootIndex].Status == "failed" {
		selected = frames[rootIndex]
	} else if len(leaves) > 0 {
		selected = leaves[0]
	} else {
		return ResumeCompatibilityFrameResult{}, errors.New("no resumable leaf frames found")
	}

	now := s.now().UTC()
	resumedAgentName := selected.AgentName
	if selected.ID == rootID && input.TargetAgent != nil && strings.TrimSpace(*input.TargetAgent) != "" {
		resumedAgentName = strings.TrimSpace(*input.TargetAgent)
	}
	update, err := tx.ExecContext(ctx, `
		UPDATE frames SET status = 'processing', agent_name = ?, updated_at = ?
		WHERE id = ? AND status = ?`, resumedAgentName, now, selected.ID, selected.Status)
	if err != nil {
		return ResumeCompatibilityFrameResult{}, fmt.Errorf("resume compatibility frame %q: %w", selected.ID, err)
	}
	if changed, err := update.RowsAffected(); err != nil {
		return ResumeCompatibilityFrameResult{}, fmt.Errorf("count compatibility frame resume: %w", err)
	} else if changed != 1 {
		return ResumeCompatibilityFrameResult{}, errors.New("compatibility frame resume changed concurrently")
	}
	if err := resetCompatibilityFrameTerminalPresentation(ctx, tx, selected.ID); err != nil {
		return ResumeCompatibilityFrameResult{}, err
	}

	payload := map[string]any{
		"previousStatus":  selected.Status,
		"rootFrameId":     rootID,
		"leafFramesReady": len(leaves),
		"agentName":       resumedAgentName,
		"dispatch": map[string]any{
			"status":       frameResumeDispatchRegistered,
			"attempt":      0,
			"registeredAt": now.Format(time.RFC3339Nano),
		},
	}
	if controls := compatibilityResumeControlPayload(input); len(controls) > 0 {
		payload["controls"] = controls
	}
	event, err := appendFrameLifecycleEvent(ctx, tx, selected.ID, "frame_resumed", payload, now)
	if err != nil {
		return ResumeCompatibilityFrameResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ResumeCompatibilityFrameResult{}, fmt.Errorf("commit compatibility frame resume: %w", err)
	}
	return ResumeCompatibilityFrameResult{
		RootFrameID: rootID, ResumedFrameID: selected.ID, AgentName: resumedAgentName,
		LeafFramesReady: len(leaves), Event: &event,
	}, nil
}

func activeCompatibilityResume(ctx context.Context, tx *sql.Tx, rootID string) (ResumeCompatibilityFrameResult, bool, error) {
	var event FrameEvent
	var agentName, rawPayload string
	err := tx.QueryRowContext(ctx, `
		SELECT e.id, e.frame_id, e.sequence, f.agent_name, e.payload, e.created_at
		FROM frame_events e JOIN frames f ON f.id = e.frame_id
		WHERE f.root_frame_id = ?
			AND f.status IN ('processing', 'running')
			AND e.event_type = 'frame_resumed'
			AND json_extract(e.payload, '$.rootFrameId') = ?
			AND json_extract(e.payload, '$.dispatch.status') IN ('registered', 'claimed', 'blocked')
		ORDER BY e.created_at DESC, e.id DESC LIMIT 1`, rootID, rootID,
	).Scan(&event.ID, &event.FrameID, &event.Sequence, &agentName, &rawPayload, &event.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ResumeCompatibilityFrameResult{}, false, nil
	}
	if err != nil {
		return ResumeCompatibilityFrameResult{}, false, fmt.Errorf("load active compatibility frame resume: %w", err)
	}
	event.Type = "frame_resumed"
	if err := json.Unmarshal([]byte(rawPayload), &event.Payload); err != nil {
		return ResumeCompatibilityFrameResult{}, false, fmt.Errorf("decode active compatibility frame resume: %w", err)
	}
	return ResumeCompatibilityFrameResult{
		RootFrameID: rootID, ResumedFrameID: event.FrameID, AgentName: agentName,
		LeafFramesReady: resumeDispatchInt(event.Payload["leafFramesReady"]), Event: &event,
	}, true, nil
}

func compatibilityResumeControlPayload(input ResumeCompatibilityFrameInput) map[string]any {
	controls := make(map[string]any)
	if input.VerifierMode != nil {
		controls["verifier_mode"] = *input.VerifierMode
	}
	if input.MemoryMode != nil {
		controls["memory_mode"] = *input.MemoryMode
	}
	if input.PlanMode != nil {
		controls["plan_mode"] = *input.PlanMode
	}
	if input.UltraMode != nil {
		controls["ultra_mode"] = *input.UltraMode
	}
	if input.TargetAgent != nil {
		controls["target_agent"] = *input.TargetAgent
	}
	if input.Model != nil {
		controls["model"] = *input.Model
	}
	return controls
}

// ActivateCompatibilityFrameRequest atomically reserves an idle frame for a
// request. A false result means another request is active and the new message
// must remain retractable in the frame queue.
func (s *Store) ActivateCompatibilityFrameRequest(frameID string) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("workspace store is closed")
	}
	frameID = strings.TrimSpace(frameID)
	if frameID == "" {
		return false, errors.New("frame id is required")
	}
	activated, err := s.activateCompatibilityFrameRequest(
		context.Background(), frameID, compatibilityFrameActivationIdle,
	)
	if err != nil {
		return false, fmt.Errorf("activate compatibility frame request: %w", err)
	}
	if activated {
		return true, nil
	}
	var exists int
	if err := s.db.QueryRowContext(context.Background(), `SELECT 1 FROM frames WHERE id = ?`, frameID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, fmt.Errorf("frame %q does not exist", frameID)
		}
		return false, fmt.Errorf("check compatibility frame activation: %w", err)
	}
	return false, nil
}

func (s *Store) RetractCompatibilityQueuedMessage(frameID, messageUUID string) (FrameEvent, error) {
	if s == nil || s.db == nil {
		return FrameEvent{}, errors.New("workspace store is closed")
	}
	frameID = strings.TrimSpace(frameID)
	messageUUID = strings.TrimSpace(messageUUID)
	if frameID == "" || messageUUID == "" {
		return FrameEvent{}, errors.New("frame id and message uuid are required")
	}
	return s.retractCompatibilityQueuedMessage(frameID, messageUUID)
}

func (s *Store) DeleteCompatibilityFrameTree(
	id, ownerUserID, expectedIncarnationID string,
) (DeleteCompatibilityFrameResult, error) {
	if s == nil || s.db == nil {
		return DeleteCompatibilityFrameResult{}, errors.New("workspace store is closed")
	}
	id, ownerUserID = strings.TrimSpace(id), strings.TrimSpace(ownerUserID)
	expectedIncarnationID = strings.TrimSpace(expectedIncarnationID)
	if id == "" || ownerUserID == "" {
		return DeleteCompatibilityFrameResult{}, errors.New("frame id and owner user id are required")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DeleteCompatibilityFrameResult{}, fmt.Errorf("begin compatibility frame delete: %w", err)
	}
	defer tx.Rollback()
	var rootID, projectID, incarnationID string
	if err := tx.QueryRowContext(ctx, `
		SELECT frame.root_frame_id,frame.project_id,frame.incarnation_id
		FROM frames AS frame JOIN projects AS project ON project.id = frame.project_id
		WHERE frame.id = ? AND project.user_id = ?`, id, ownerUserID,
	).Scan(&rootID, &projectID, &incarnationID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DeleteCompatibilityFrameResult{}, fmt.Errorf("frame %q does not exist", id)
		}
		return DeleteCompatibilityFrameResult{}, fmt.Errorf("load compatibility frame tree root: %w", err)
	}
	if incarnationID != expectedIncarnationID {
		return DeleteCompatibilityFrameResult{}, fmt.Errorf("frame %q changed before delete", id)
	}
	isWebConversation := false
	var rawContext sql.NullString
	if err := tx.QueryRowContext(ctx, `
		SELECT metadata.context_data FROM frame_runtime_metadata AS metadata
		JOIN frames AS frame ON frame.id=metadata.frame_id
		JOIN projects AS project ON project.id=frame.project_id
		WHERE metadata.frame_id=? AND frame.project_id=? AND project.user_id=?`,
		rootID, projectID, ownerUserID,
	).Scan(&rawContext); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return DeleteCompatibilityFrameResult{}, fmt.Errorf("load compatibility frame delete metadata: %w", err)
	}
	if rawContext.Valid && strings.TrimSpace(rawContext.String) != "" {
		contextData := map[string]any{}
		if err := json.Unmarshal([]byte(rawContext.String), &contextData); err != nil {
			return DeleteCompatibilityFrameResult{}, fmt.Errorf("decode compatibility frame delete metadata: %w", err)
		}
		_, hasExtra := contextData["web_extra"]
		_, hasAssistant := contextData["web_assistant"]
		isWebConversation = hasExtra || hasAssistant
	}
	deletedRoot, err := s.deleteRootAggregateTx(ctx, tx, ownerUserID, projectID, rootID)
	if err != nil {
		return DeleteCompatibilityFrameResult{}, err
	}
	for _, frame := range deletedRoot.Frames {
		input := RealtimeEventInput{
			ID: uuid.NewString(), UserID: ownerUserID, ProjectID: projectID,
			RootFrameID: rootID, FrameID: frame.ID, Type: "frame_update",
			Payload: map[string]any{
				"project_id": projectID, "root_frame_id": rootID, "frame_id": frame.ID, "action": "deleted",
			},
		}
		var err error
		if frame.IncarnationID != "" {
			_, err = s.enqueueRealtimeFrameRetirementTx(ctx, tx, input, frame.IncarnationID)
		} else {
			_, err = s.EnqueueRealtimeOutboxTx(ctx, tx, input, "")
		}
		if err != nil {
			return DeleteCompatibilityFrameResult{}, fmt.Errorf("enqueue compatibility frame delete: %w", err)
		}
	}
	if isWebConversation {
		if _, err := s.EnqueueRealtimeOutboxTx(ctx, tx, RealtimeEventInput{
			ID: uuid.NewString(), UserID: ownerUserID, ProjectID: projectID,
			RootFrameID: rootID, FrameID: rootID, Type: "conversation.listChanged",
			Payload: map[string]any{"conversation_id": rootID, "action": "deleted", "source": "synonbiomed"},
		}, ""); err != nil {
			return DeleteCompatibilityFrameResult{}, fmt.Errorf("enqueue compatibility conversation delete: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return DeleteCompatibilityFrameResult{}, fmt.Errorf("commit compatibility frame delete: %w", err)
	}
	artifactCleanupFailures := 0
	for _, path := range deletedRoot.BlobPaths {
		absolute, err := s.blobAbsolute(path)
		if err != nil {
			artifactCleanupFailures++
			continue
		}
		if err := os.Remove(absolute); err != nil && !errors.Is(err, os.ErrNotExist) {
			artifactCleanupFailures++
		}
	}
	return DeleteCompatibilityFrameResult{
		FrameID: id, RootFrameID: rootID, FramesDeleted: deletedRoot.FramesDeleted, ArtifactsDeleted: deletedRoot.ArtifactsDeleted,
		ArtifactCleanupFailures: artifactCleanupFailures,
	}, nil
}

func (s *Store) CompatibilityFrameMessages(frameID string, from, limit int) (CompatibilityMessagePage, error) {
	if s == nil || s.db == nil {
		return CompatibilityMessagePage{}, errors.New("workspace store is closed")
	}
	if from < 0 {
		return CompatibilityMessagePage{}, errors.New("message offset must be non-negative")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	frameID = strings.TrimSpace(frameID)
	var total int
	if err := s.db.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM frame_events WHERE frame_id = ? AND `+compatibilityMessagePredicate, frameID).Scan(&total); err != nil {
		return CompatibilityMessagePage{}, fmt.Errorf("count compatibility frame messages: %w", err)
	}
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT id, payload FROM frame_events WHERE frame_id = ? AND `+compatibilityMessagePredicate+`
		ORDER BY sequence LIMIT ? OFFSET ?`, frameID, limit, from)
	if err != nil {
		return CompatibilityMessagePage{}, fmt.Errorf("list compatibility frame messages: %w", err)
	}
	defer rows.Close()
	messages := make([]map[string]any, 0, limit)
	for rows.Next() {
		var eventID string
		var raw string
		if err := rows.Scan(&eventID, &raw); err != nil {
			return CompatibilityMessagePage{}, fmt.Errorf("scan compatibility frame message: %w", err)
		}
		var message map[string]any
		if err := json.Unmarshal([]byte(raw), &message); err != nil {
			return CompatibilityMessagePage{}, fmt.Errorf("decode compatibility frame message: %w", err)
		}
		if value, _ := message["_uuid"].(string); strings.TrimSpace(value) == "" {
			message["_uuid"] = eventID
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return CompatibilityMessagePage{}, fmt.Errorf("iterate compatibility frame messages: %w", err)
	}
	return CompatibilityMessagePage{Messages: messages, From: from, Total: total}, nil
}

func (s *Store) LocateCompatibilityFrameMessage(frameID, uuid string) (*int, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT id, payload FROM frame_events WHERE frame_id = ? AND `+compatibilityMessagePredicate+` ORDER BY sequence`, strings.TrimSpace(frameID))
	if err != nil {
		return nil, fmt.Errorf("locate compatibility frame message: %w", err)
	}
	defer rows.Close()
	uuid = strings.TrimSpace(uuid)
	index := 0
	for rows.Next() {
		var eventID string
		var raw string
		if err := rows.Scan(&eventID, &raw); err != nil {
			return nil, fmt.Errorf("scan compatibility frame message for locate: %w", err)
		}
		var message map[string]any
		if err := json.Unmarshal([]byte(raw), &message); err != nil {
			return nil, fmt.Errorf("decode compatibility frame message for locate: %w", err)
		}
		if eventID == uuid {
			position := index
			return &position, nil
		}
		for _, key := range []string{"_uuid", "uuid", "messageUuid", "message_uuid"} {
			if value, _ := message[key].(string); value == uuid {
				position := index
				return &position, nil
			}
		}
		index++
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate compatibility frame messages for locate: %w", err)
	}
	return nil, nil
}

// CreateAutoResumeDispatch creates a frame_resumed event with a // registered dispatch payload for an auto-resumable // interrupted frame. The frame remains in its current // nonterminal state (processing/running).
func (s *Store) CreateAutoResumeDispatch(
	frameID, rootFrameID, projectID, agentName, reasonCode string,
	coordinates ...AutoResumeDispatchTranscriptCoordinate,
) (ResumeCompatibilityFrameResult, error) {
	if s == nil || s.db == nil {
		return ResumeCompatibilityFrameResult{}, errors.New("workspace store is closed")
	}
	frameID = strings.TrimSpace(frameID)
	if frameID == "" {
		return ResumeCompatibilityFrameResult{}, errors.New("frame id is required")
	}
	rootFrameID = strings.TrimSpace(rootFrameID)
	if rootFrameID == "" {
		rootFrameID = frameID
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ResumeCompatibilityFrameResult{}, fmt.Errorf("begin auto resume dispatch: %w", err)
	}
	defer tx.Rollback()
	if existing, found, err := activeCompatibilityResume(ctx, tx, rootFrameID); err != nil {
		return ResumeCompatibilityFrameResult{}, err
	} else if found {
		if err := tx.Commit(); err != nil {
			return ResumeCompatibilityFrameResult{}, fmt.Errorf("commit existing auto resume dispatch: %w", err)
		}
		return existing, nil
	}
	var frameStatus string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM frames WHERE id = ?`, frameID).Scan(&frameStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ResumeCompatibilityFrameResult{}, fmt.Errorf("auto resume frame %q does not exist", frameID)
		}
		return ResumeCompatibilityFrameResult{}, fmt.Errorf("load auto resume frame status: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(frameStatus)) {
	case FrameStatusProcessing, "running":
	case "awaiting_plan_approval":
		if reasonCode != "plan_text_decision" {
			return ResumeCompatibilityFrameResult{}, errors.New("plan approval requires an explicit user decision")
		}
		// Persist activation with its claimable dispatch. A saved text decision
		// must not remain parked or become processing without runnable work.
		if _, err := tx.ExecContext(ctx, `UPDATE frames SET status = ?, updated_at = ? WHERE id = ? AND status = ?`,
			FrameStatusProcessing, s.now().UTC(), frameID, frameStatus); err != nil {
			return ResumeCompatibilityFrameResult{}, fmt.Errorf("activate plan text decision: %w", err)
		}
	case FrameStatusFailed:
		// A caller that has observed a concrete recovery signal (for example a
		// provider switch) may start a fresh execution unit for the same logical
		// task. Reactivation and dispatch creation share this transaction so the
		// UI can never observe processing without claimable work.
		if _, err := tx.ExecContext(ctx, `UPDATE frames SET status = ?, updated_at = ? WHERE id = ? AND status = ?`,
			FrameStatusProcessing, s.now().UTC(), frameID, FrameStatusFailed); err != nil {
			return ResumeCompatibilityFrameResult{}, fmt.Errorf("reactivate failed auto resume frame: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE frame_runtime_metadata SET status_description = '' WHERE frame_id = ?`, frameID); err != nil {
			return ResumeCompatibilityFrameResult{}, fmt.Errorf("clear recovered frame failure description: %w", err)
		}
	default:
		return ResumeCompatibilityFrameResult{}, fmt.Errorf("auto resume frame %q is terminal with status %q", frameID, frameStatus)
	}

	now := s.now().UTC()
	if len(coordinates) > 1 {
		return ResumeCompatibilityFrameResult{}, errors.New("auto resume dispatch accepts one Transcript coordinate")
	}
	dispatchPayload := map[string]any{
		"status":       frameResumeDispatchRegistered,
		"attempt":      0,
		"registeredAt": now.Format(time.RFC3339Nano),
		"autoResume":   true,
		"reasonCode":   reasonCode,
	}
	if len(coordinates) == 1 {
		coordinate := coordinates[0]
		if coordinate.RunnerAttempt <= 0 || coordinate.CheckpointEventID <= 0 {
			return ResumeCompatibilityFrameResult{}, errors.New("auto resume dispatch Transcript coordinate is invalid")
		}
		dispatchPayload["runnerAttempt"] = coordinate.RunnerAttempt
		dispatchPayload["checkpointEventId"] = coordinate.CheckpointEventID
	}
	payload := map[string]any{
		"previousStatus":  "interrupted",
		"rootFrameId":     rootFrameID,
		"leafFramesReady": 1,
		"agentName":       agentName,
		"dispatch":        dispatchPayload,
	}
	event, err := appendFrameLifecycleEvent(ctx, tx, frameID, "frame_resumed", payload, now)
	if err != nil {
		return ResumeCompatibilityFrameResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ResumeCompatibilityFrameResult{}, fmt.Errorf("commit auto resume dispatch: %w", err)
	}
	return ResumeCompatibilityFrameResult{
		RootFrameID:     rootFrameID,
		ResumedFrameID:  frameID,
		AgentName:       agentName,
		LeafFramesReady: 1,
		Event:           &event,
	}, nil
}
