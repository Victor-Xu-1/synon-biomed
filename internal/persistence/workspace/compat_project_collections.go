package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type CompatibilityProjectArtifact struct {
	ID                     string    `json:"id"`
	VersionID              string    `json:"version_id"`
	VersionNumber          int       `json:"version_number"`
	ProjectID              string    `json:"project_id"`
	RootFrameID            *string   `json:"root_frame_id"`
	FrameID                *string   `json:"frame_id"`
	CreatingFrameID        *string   `json:"creating_frame_id"`
	Filename               string    `json:"filename"`
	ContentType            string    `json:"content_type"`
	SizeBytes              int64     `json:"size_bytes"`
	CreatedAt              time.Time `json:"created_at"`
	Checksum               *string   `json:"checksum"`
	FilePath               string    `json:"file_path"`
	IsUserUpload           bool      `json:"is_user_upload"`
	AgentName              *string   `json:"agent_name"`
	FolderID               *string   `json:"folder_id"`
	IsIntermediate         bool      `json:"is_intermediate"`
	RefHostPath            *string   `json:"ref_host_path"`
	Priority               string    `json:"priority"`
	CreatingVersionID      *string   `json:"creating_version_id"`
	SupersededByArtifactID *string   `json:"superseded_by_artifact_id"`
	AllVersionIDs          []string  `json:"all_version_ids"`
}

type CompatibilityProjectArtifactCursor struct {
	Revision   string
	CreatedAt  time.Time
	ArtifactID string
}

type CompatibilityProjectArtifactPageInput struct {
	OwnerUserID         string
	ProjectID           string
	FrameID             string
	Search              string
	ExcludeIntermediate bool
	ExcludeInternal     bool
	Limit               int
	Cursor              *CompatibilityProjectArtifactCursor
}

type CompatibilityProjectArtifactPage struct {
	Artifacts  []CompatibilityProjectArtifact
	NextCursor *CompatibilityProjectArtifactCursor
	HasMore    bool
	Total      int
	Revision   string
}

var ErrCompatibilityProjectArtifactCursorStale = errors.New("project artifact cursor is stale")

type CompatibilityQueuedUserMessage struct {
	ID       string    `json:"id"`
	Text     string    `json:"text"`
	QueuedAt time.Time `json:"queued_at"`
	FrameID  string    `json:"frame_id"`
}

var (
	ErrCompatibilityBenchNotFound = errors.New("compatibility bench not found")
	ErrCompatibilityBenchNotRoot  = errors.New("compatibility bench is not a root frame")
)

type UpdateCompatibilityBenchInput struct {
	Name        *string
	TaskSummary *string
}

type CompatibilityBenchUpdate struct {
	ID          string
	ProjectID   string
	RootFrameID string
	Name        *string
	TaskSummary *string
	Event       RealtimeEventInput
}

func (s *Store) ListCompatibilityProjectArtifacts(
	ctx context.Context,
	ownerUserID string,
	projectID string,
	excludeIntermediate bool,
	limit int,
) ([]CompatibilityProjectArtifact, error) {
	return s.listCompatibilityProjectArtifacts(ctx, ownerUserID, projectID, excludeIntermediate, limit, true)
}

// ListCompatibilityProjectCurrentArtifacts returns only the current project
// artifact index. It deliberately skips version-lineage hydration so list
// surfaces do not pay for history that they neither return nor render.
func (s *Store) ListCompatibilityProjectCurrentArtifacts(
	ctx context.Context,
	ownerUserID string,
	projectID string,
	excludeIntermediate bool,
	limit int,
) ([]CompatibilityProjectArtifact, error) {
	page, err := s.ListCompatibilityProjectCurrentArtifactPage(ctx, CompatibilityProjectArtifactPageInput{
		OwnerUserID: ownerUserID, ProjectID: projectID, ExcludeIntermediate: excludeIntermediate, Limit: limit,
	})
	return page.Artifacts, err
}

// ListCompatibilityProjectCurrentArtifactPage reads one bounded, stable page
// from the project-current artifact index. Count/revision and rows share one
// SQLite read transaction, so a cursor is either accepted against the exact
// index revision it was issued for or fails closed without returning a mixed
// page. The page contains metadata only; artifact content remains behind the
// versioned content endpoint.
func (s *Store) ListCompatibilityProjectCurrentArtifactPage(
	ctx context.Context,
	input CompatibilityProjectArtifactPageInput,
) (CompatibilityProjectArtifactPage, error) {
	if s == nil || s.db == nil {
		return CompatibilityProjectArtifactPage{}, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	input.OwnerUserID = strings.TrimSpace(input.OwnerUserID)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.FrameID = strings.TrimSpace(input.FrameID)
	input.Search = strings.TrimSpace(input.Search)
	if input.OwnerUserID == "" || input.ProjectID == "" {
		return CompatibilityProjectArtifactPage{}, errors.New("artifact owner user id and project id are required")
	}
	if input.Limit <= 0 {
		input.Limit = 200
	}
	if input.Limit > 1000 {
		return CompatibilityProjectArtifactPage{}, errors.New("project artifact page limit exceeds 1000")
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return CompatibilityProjectArtifactPage{}, fmt.Errorf("begin project artifact page: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	where := `
		FROM artifacts a
		JOIN projects project ON project.id = a.project_id
		JOIN artifact_versions v ON v.artifact_id = a.id AND v.version_number = a.current_version_number
		LEFT JOIN artifact_runtime_metadata m ON m.artifact_id = a.id
		LEFT JOIN artifact_version_provenance p ON p.version_id = v.id
		WHERE project.user_id = ? AND a.project_id = ?`
	filterArguments := []any{input.OwnerUserID, input.ProjectID}
	if input.FrameID != "" {
		where += ` AND COALESCE(NULLIF(m.frame_id, ''), p.frame_id) = ?`
		filterArguments = append(filterArguments, input.FrameID)
	}
	if input.ExcludeIntermediate {
		where += compatibilityExcludeIntermediateArtifactWhere
	}
	if input.ExcludeInternal {
		// Runner-large-tool-result artifacts are internal evidence, not
		// user-facing generated files. They remain readable through artifact
		// content endpoints and review evidence bindings.
		where += ` AND a.id NOT LIKE 'large-tool-result-%' AND a.retention_mode <> 'working_data'`
	}
	if input.Search != "" {
		where += ` AND instr(lower(a.name), lower(?)) > 0`
		filterArguments = append(filterArguments, input.Search)
	}

	var total int
	var revisionUpdatedAt sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), MAX(a.updated_at) `+where, filterArguments...).Scan(
		&total, &revisionUpdatedAt,
	); err != nil {
		return CompatibilityProjectArtifactPage{}, fmt.Errorf("read project artifact page revision: %w", err)
	}
	revision := fmt.Sprintf("v1:%d:", total)
	if revisionUpdatedAt.Valid {
		revision += revisionUpdatedAt.String
	}
	if input.Cursor != nil {
		if strings.TrimSpace(input.Cursor.Revision) != revision || input.Cursor.CreatedAt.IsZero() || strings.TrimSpace(input.Cursor.ArtifactID) == "" {
			return CompatibilityProjectArtifactPage{}, ErrCompatibilityProjectArtifactCursorStale
		}
		where += ` AND (v.created_at < ? OR (v.created_at = ? AND a.id < ?))`
		filterArguments = append(filterArguments, input.Cursor.CreatedAt.UTC(), input.Cursor.CreatedAt.UTC(), input.Cursor.ArtifactID)
	}

	query := `SELECT a.id, v.id, v.version_number, a.project_id,
		m.root_frame_id, COALESCE(m.frame_id, p.frame_id), a.name,
		COALESCE(NULLIF(p.content_type, ''), a.kind),
		CASE WHEN COALESCE(v.storage_path, '') = '' THEN length(v.content) ELSE v.size_bytes END,
		v.created_at, NULLIF(v.content_sha256, ''), COALESCE(v.storage_path, ''),
		COALESCE(m.is_user_upload, 0), p.agent_name, a.folder_id,
		COALESCE(p.is_intermediate, 0), a.priority, m.superseded_by_artifact_id ` + where +
		` ORDER BY v.created_at DESC, a.id DESC LIMIT ?`
	arguments := append(append([]any{}, filterArguments...), input.Limit+1)
	rows, err := tx.QueryContext(ctx, query, arguments...)
	if err != nil {
		return CompatibilityProjectArtifactPage{}, fmt.Errorf("list project artifact page: %w", err)
	}
	artifacts := make([]CompatibilityProjectArtifact, 0, input.Limit+1)
	for rows.Next() {
		var artifact CompatibilityProjectArtifact
		var rootFrameID, frameID, checksum, storagePath sql.NullString
		var agentName, folderID, supersededBy sql.NullString
		if err := rows.Scan(
			&artifact.ID, &artifact.VersionID, &artifact.VersionNumber, &artifact.ProjectID,
			&rootFrameID, &frameID, &artifact.Filename, &artifact.ContentType,
			&artifact.SizeBytes, &artifact.CreatedAt, &checksum, &storagePath,
			&artifact.IsUserUpload, &agentName, &folderID, &artifact.IsIntermediate,
			&artifact.Priority, &supersededBy,
		); err != nil {
			_ = rows.Close()
			return CompatibilityProjectArtifactPage{}, fmt.Errorf("scan project artifact page: %w", err)
		}
		artifact.RootFrameID = nullableStringPointer(rootFrameID)
		artifact.FrameID = nullableStringPointer(frameID)
		artifact.Checksum = nullableStringPointer(checksum)
		artifact.AgentName = nullableStringPointer(agentName)
		artifact.FolderID = nullableStringPointer(folderID)
		artifact.SupersededByArtifactID = nullableStringPointer(supersededBy)
		artifact.AllVersionIDs = []string{}
		artifact.Priority = compatibilityArtifactPriority(artifact.Priority)
		if storagePath.Valid && strings.TrimSpace(storagePath.String) != "" {
			artifact.FilePath, err = s.blobAbsolute(storagePath.String)
			if err != nil {
				_ = rows.Close()
				return CompatibilityProjectArtifactPage{}, fmt.Errorf("resolve project artifact path: %w", err)
			}
		}
		artifacts = append(artifacts, artifact)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return CompatibilityProjectArtifactPage{}, fmt.Errorf("iterate project artifact page: %w", err)
	}
	if err := rows.Close(); err != nil {
		return CompatibilityProjectArtifactPage{}, fmt.Errorf("close project artifact page: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return CompatibilityProjectArtifactPage{}, fmt.Errorf("commit project artifact page: %w", err)
	}

	hasMore := len(artifacts) > input.Limit
	if hasMore {
		artifacts = artifacts[:input.Limit]
	}
	var nextCursor *CompatibilityProjectArtifactCursor
	if hasMore && len(artifacts) > 0 {
		last := artifacts[len(artifacts)-1]
		nextCursor = &CompatibilityProjectArtifactCursor{
			Revision: revision, CreatedAt: last.CreatedAt.UTC(), ArtifactID: last.ID,
		}
	}
	return CompatibilityProjectArtifactPage{
		Artifacts: artifacts, NextCursor: nextCursor, HasMore: hasMore, Total: total, Revision: revision,
	}, nil
}

func (s *Store) listCompatibilityProjectArtifacts(
	ctx context.Context,
	ownerUserID string,
	projectID string,
	excludeIntermediate bool,
	limit int,
	includeVersionHistory bool,
) ([]CompatibilityProjectArtifact, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ownerUserID = strings.TrimSpace(ownerUserID)
	projectID = strings.TrimSpace(projectID)
	if ownerUserID == "" || projectID == "" {
		return nil, errors.New("artifact owner user id and project id are required")
	}
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	query := `
		SELECT a.id, v.id, v.version_number, a.project_id,
			m.root_frame_id, COALESCE(m.frame_id, p.frame_id), a.name,
			COALESCE(NULLIF(p.content_type, ''), a.kind),
			CASE WHEN COALESCE(v.storage_path, '') = '' THEN length(v.content) ELSE v.size_bytes END,
			v.created_at, NULLIF(v.content_sha256, ''), COALESCE(v.storage_path, ''),
			COALESCE(m.is_user_upload, 0), p.agent_name, a.folder_id,
			COALESCE(p.is_intermediate, 0), a.priority,
			m.superseded_by_artifact_id
		FROM artifacts a
		JOIN projects project ON project.id = a.project_id
		JOIN artifact_versions v ON v.artifact_id = a.id AND v.version_number = a.current_version_number
		LEFT JOIN artifact_runtime_metadata m ON m.artifact_id = a.id
		LEFT JOIN artifact_version_provenance p ON p.version_id = v.id
		WHERE project.user_id = ? AND a.project_id = ?`
	arguments := []any{ownerUserID, projectID}
	if excludeIntermediate {
		query += compatibilityExcludeIntermediateArtifactWhere
	}
	query += ` ORDER BY v.created_at DESC, a.id DESC LIMIT ?`
	arguments = append(arguments, limit)
	rows, err := s.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("list compatibility project artifacts: %w", err)
	}
	artifacts := make([]CompatibilityProjectArtifact, 0)
	for rows.Next() {
		var artifact CompatibilityProjectArtifact
		var rootFrameID, frameID, checksum, storagePath sql.NullString
		var agentName, folderID, supersededBy sql.NullString
		if err := rows.Scan(
			&artifact.ID, &artifact.VersionID, &artifact.VersionNumber, &artifact.ProjectID,
			&rootFrameID, &frameID, &artifact.Filename, &artifact.ContentType,
			&artifact.SizeBytes, &artifact.CreatedAt, &checksum, &storagePath,
			&artifact.IsUserUpload, &agentName, &folderID, &artifact.IsIntermediate,
			&artifact.Priority, &supersededBy,
		); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan compatibility project artifact: %w", err)
		}
		artifact.RootFrameID = nullableStringPointer(rootFrameID)
		artifact.FrameID = nullableStringPointer(frameID)
		artifact.Checksum = nullableStringPointer(checksum)
		artifact.AgentName = nullableStringPointer(agentName)
		artifact.FolderID = nullableStringPointer(folderID)
		artifact.SupersededByArtifactID = nullableStringPointer(supersededBy)
		artifact.AllVersionIDs = []string{}
		artifact.Priority = compatibilityArtifactPriority(artifact.Priority)
		if storagePath.Valid && strings.TrimSpace(storagePath.String) != "" {
			artifact.FilePath, err = s.blobAbsolute(storagePath.String)
			if err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("resolve compatibility artifact path: %w", err)
			}
		}
		artifacts = append(artifacts, artifact)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate compatibility project artifacts: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close compatibility project artifacts: %w", err)
	}
	if len(artifacts) == 0 || !includeVersionHistory {
		return artifacts, nil
	}
	if err := s.attachCompatibilityArtifactVersionHistory(ctx, artifacts); err != nil {
		return nil, err
	}
	return artifacts, nil
}

func (s *Store) attachCompatibilityArtifactVersionHistory(ctx context.Context, artifacts []CompatibilityProjectArtifact) error {
	placeholders := make([]string, len(artifacts))
	arguments := make([]any, len(artifacts))
	index := make(map[string]int, len(artifacts))
	for i := range artifacts {
		placeholders[i] = "?"
		arguments[i] = artifacts[i].ID
		index[artifacts[i].ID] = i
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT v.artifact_id, v.id, p.frame_id
		FROM artifact_versions v
		LEFT JOIN artifact_version_provenance p ON p.version_id = v.id
		WHERE v.artifact_id IN (`+strings.Join(placeholders, ",")+`)
		ORDER BY v.artifact_id, v.version_number, v.id`, arguments...)
	if err != nil {
		return fmt.Errorf("list compatibility artifact version histories: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var artifactID, versionID string
		var frameID sql.NullString
		if err := rows.Scan(&artifactID, &versionID, &frameID); err != nil {
			return fmt.Errorf("scan compatibility artifact version history: %w", err)
		}
		i, found := index[artifactID]
		if !found {
			continue
		}
		artifacts[i].AllVersionIDs = append(artifacts[i].AllVersionIDs, versionID)
		if artifacts[i].CreatingVersionID == nil {
			copy := versionID
			artifacts[i].CreatingVersionID = &copy
			artifacts[i].CreatingFrameID = nullableStringPointer(frameID)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate compatibility artifact version histories: %w", err)
	}
	return nil
}

func compatibilityArtifactPriority(value string) string {
	switch strings.TrimSpace(value) {
	case "user_starred", "user_hidden", "user_no_priority":
		return strings.TrimSpace(value)
	default:
		return "unknown"
	}
}

func (s *Store) ListCompatibilityProjectBenches(
	ctx context.Context,
	ownerUserID string,
	projectID string,
	limit int,
	query string,
) ([]CompatibilityFrame, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ownerUserID = strings.TrimSpace(ownerUserID)
	projectID = strings.TrimSpace(projectID)
	if ownerUserID == "" || projectID == "" {
		return nil, false, errors.New("bench owner user id and project id are required")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, false, fmt.Errorf("begin compatibility project bench snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var foundID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM projects WHERE id = ? AND user_id = ?`, projectID, ownerUserID).Scan(&foundID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return []CompatibilityFrame{}, false, nil
		}
		return nil, false, fmt.Errorf("look up compatibility bench project: %w", err)
	}
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	needle := strings.TrimSpace(query)
	arguments := []any{ownerUserID, projectID}
	statement := `
		SELECT f.id
		FROM frames f
		JOIN projects p ON p.id = f.project_id
		LEFT JOIN frame_runtime_metadata m ON m.frame_id = f.id
		WHERE p.user_id = ? AND f.project_id = ?
			AND f.parent_frame_id IS NULL
			AND TRIM(COALESCE(f.name, '')) <> ''
			AND COALESCE(m.is_hidden, 0) = 0
			AND f.conversation_type <> 'uploads'
			AND f.agent_name NOT IN ('CONCIERGE', 'CANVAS_CONCIERGE')`
	if needle != "" {
		statement += ` AND (
			instr(lower(COALESCE(f.name, '')), lower(?)) > 0 OR
			instr(lower(COALESCE(m.task_summary, '')), lower(?)) > 0
		)`
		arguments = append(arguments, needle, needle)
	}
	statement += ` ORDER BY f.updated_at DESC, f.id DESC LIMIT ?`
	arguments = append(arguments, limit)
	rows, err := tx.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return nil, false, fmt.Errorf("list compatibility project benches: %w", err)
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, false, fmt.Errorf("scan compatibility project bench: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, false, fmt.Errorf("iterate compatibility project benches: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, false, fmt.Errorf("close compatibility project benches: %w", err)
	}
	if len(ids) == 0 {
		if err := tx.Commit(); err != nil {
			return nil, false, fmt.Errorf("commit empty compatibility project bench snapshot: %w", err)
		}
		return []CompatibilityFrame{}, true, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	frameArguments := make([]any, len(ids))
	for index, id := range ids {
		frameArguments[index] = id
	}
	frameRows, err := tx.QueryContext(ctx, traceFrameSelectColumns+`
		WHERE frame.id IN (`+placeholders+`)`, frameArguments...)
	if err != nil {
		return nil, false, fmt.Errorf("load compatibility project bench frames: %w", err)
	}
	framesByID := make(map[string]Frame, len(ids))
	for frameRows.Next() {
		frame, err := scanTraceFrame(frameRows)
		if err != nil {
			_ = frameRows.Close()
			return nil, false, fmt.Errorf("scan compatibility project bench frame: %w", err)
		}
		framesByID[frame.ID] = frame
	}
	if err := frameRows.Err(); err != nil {
		_ = frameRows.Close()
		return nil, false, fmt.Errorf("iterate compatibility project bench frames: %w", err)
	}
	if err := frameRows.Close(); err != nil {
		return nil, false, fmt.Errorf("close compatibility project bench frames: %w", err)
	}

	childRows, err := tx.QueryContext(ctx, `
		SELECT parent_frame_id, id
		FROM frames
		WHERE parent_frame_id IN (`+placeholders+`)
		ORDER BY parent_frame_id, root_sequence, id`, frameArguments...)
	if err != nil {
		return nil, false, fmt.Errorf("load compatibility project bench children: %w", err)
	}
	childrenByParent := make(map[string][]string, len(ids))
	for childRows.Next() {
		var parentID, childID string
		if err := childRows.Scan(&parentID, &childID); err != nil {
			_ = childRows.Close()
			return nil, false, fmt.Errorf("scan compatibility project bench child: %w", err)
		}
		childrenByParent[parentID] = append(childrenByParent[parentID], childID)
	}
	if err := childRows.Err(); err != nil {
		_ = childRows.Close()
		return nil, false, fmt.Errorf("iterate compatibility project bench children: %w", err)
	}
	if err := childRows.Close(); err != nil {
		return nil, false, fmt.Errorf("close compatibility project bench children: %w", err)
	}

	benches := make([]CompatibilityFrame, 0, len(ids))
	for _, id := range ids {
		frame, found := framesByID[id]
		if !found {
			return nil, false, fmt.Errorf("compatibility project bench %q disappeared during listing", id)
		}
		benches = append(benches, CompatibilityFrame{
			Frame: frame, TaskSummary: frame.TaskSummary, InputData: frame.InputData,
			IsHidden: frame.IsHidden, ChildIDs: childrenByParent[id],
		})
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("commit compatibility project bench snapshot: %w", err)
	}
	return benches, true, nil
}

func (s *Store) CompatibilityBenchActivity(ctx context.Context, rootFrameID string) (time.Time, bool, error) {
	if s == nil || s.db == nil {
		return time.Time{}, false, errors.New("workspace store is closed")
	}
	rootFrameID = strings.TrimSpace(rootFrameID)
	if rootFrameID == "" {
		return time.Time{}, false, errors.New("root frame id is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var frameActivity time.Time
	err := s.db.QueryRowContext(ctx, `
		SELECT updated_at FROM frames
		WHERE root_frame_id = ?
		ORDER BY updated_at DESC, id DESC LIMIT 1`, rootFrameID).Scan(&frameActivity)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("get compatibility bench frame activity: %w", err)
	}
	var eventActivity time.Time
	err = s.db.QueryRowContext(ctx, `
		SELECT e.created_at
		FROM frame_events e JOIN frames f ON f.id = e.frame_id
		WHERE f.root_frame_id = ?
		ORDER BY e.created_at DESC, e.id DESC LIMIT 1`, rootFrameID).Scan(&eventActivity)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, fmt.Errorf("get compatibility bench event activity: %w", err)
	}
	if err == nil && eventActivity.After(frameActivity) {
		frameActivity = eventActivity
	}
	return frameActivity.UTC(), true, nil
}

func (s *Store) CompatibilityBenchHasImageOutput(ctx context.Context, rootFrameID string) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var found int
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM artifacts a
			JOIN artifact_versions v ON v.artifact_id = a.id AND v.version_number = a.current_version_number
			LEFT JOIN artifact_runtime_metadata m ON m.artifact_id = a.id
			LEFT JOIN artifact_version_provenance p ON p.version_id = v.id
			WHERE m.root_frame_id = ?
				AND lower(COALESCE(NULLIF(p.content_type, ''), a.kind)) LIKE 'image/%'
		)`, strings.TrimSpace(rootFrameID)).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("get compatibility bench image output: %w", err)
	}
	return found != 0, nil
}

func (s *Store) CompatibilityBenchOutputData(ctx context.Context, rootFrameID string) (map[string]any, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var raw sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT m.output_data
		FROM frames f LEFT JOIN frame_runtime_metadata m ON m.frame_id = f.id
		WHERE f.id = ? AND f.root_frame_id = ?`, strings.TrimSpace(rootFrameID), strings.TrimSpace(rootFrameID),
	).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get compatibility bench output: %w", err)
	}
	if !raw.Valid {
		return nil, false, nil
	}
	output := map[string]any{}
	if err := json.Unmarshal([]byte(raw.String), &output); err != nil {
		return nil, false, fmt.Errorf("decode compatibility bench output: %w", err)
	}
	return output, true, nil
}

func (s *Store) CompatibilityBenchHasDescendantAwaitingInput(ctx context.Context, rootFrameID string) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rootFrameID = strings.TrimSpace(rootFrameID)
	rows, err := s.db.QueryContext(ctx, `
		SELECT f.status, COALESCE(m.context_data, '{}')
		FROM frames f
		LEFT JOIN frame_runtime_metadata m ON m.frame_id = f.id
		WHERE f.root_frame_id = ? AND f.id <> ?
			AND f.status IN ('awaiting_user_response', 'awaiting_plan_approval')`, rootFrameID, rootFrameID)
	if err != nil {
		return false, fmt.Errorf("get compatibility bench descendant input state: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var status, rawContext string
		if err := rows.Scan(&status, &rawContext); err != nil {
			return false, fmt.Errorf("scan compatibility bench descendant input state: %w", err)
		}
		contextData := map[string]any{}
		if err := json.Unmarshal([]byte(rawContext), &contextData); err != nil {
			return false, fmt.Errorf("decode compatibility bench descendant input state: %w", err)
		}
		switch strings.ToLower(strings.TrimSpace(status)) {
		case FrameStatusAwaitingUserResponse:
			if pending, ok := contextData["_pending_input_requests"].([]any); ok && len(pending) > 0 {
				return true, nil
			}
		case FrameStatusAwaitingPlanApproval:
			if strings.TrimSpace(compatibilityStringValue(contextData["_plan_artifact_id"])) != "" {
				return true, nil
			}
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate compatibility bench descendant input state: %w", err)
	}
	return false, nil
}

func (s *Store) ListCompatibilityBenchQueuedUserMessages(ctx context.Context, rootFrameID string) ([]CompatibilityQueuedUserMessage, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rootFrameID = strings.TrimSpace(rootFrameID)
	rows, err := s.db.QueryContext(ctx, `
		SELECT q.sequence, q.intent_id, q.frame_id, q.payload, q.state, q.created_at, q.resolved_at
		FROM queued_user_messages q
		JOIN frames f ON f.id = q.frame_id
		WHERE f.root_frame_id = ? AND q.state = 'queued'
		ORDER BY q.sequence`, rootFrameID)
	if err != nil {
		return nil, fmt.Errorf("list compatibility bench queued messages: %w", err)
	}
	defer rows.Close()
	messages := make([]CompatibilityQueuedUserMessage, 0)
	for rows.Next() {
		record, err := scanCompatibilityMessageIntent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan compatibility bench queued message: %w", err)
		}
		text, _ := record.DeliveryPayload["text"].(string)
		if text == "" {
			text, _ = record.DeliveryPayload["content"].(string)
		}
		messages = append(messages, CompatibilityQueuedUserMessage{
			ID: record.IntentID, Text: text, QueuedAt: record.CreatedAt.UTC(), FrameID: record.FrameID,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate compatibility bench queued messages: %w", err)
	}
	return messages, nil
}

func (s *Store) UpdateCompatibilityBenchRealtime(
	ctx context.Context,
	ownerUserID string,
	frameID string,
	input UpdateCompatibilityBenchInput,
	eventID string,
) (CompatibilityBenchUpdate, error) {
	if s == nil || s.db == nil {
		return CompatibilityBenchUpdate{}, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ownerUserID = strings.TrimSpace(ownerUserID)
	frameID = strings.TrimSpace(frameID)
	if ownerUserID == "" || frameID == "" {
		return CompatibilityBenchUpdate{}, errors.New("bench owner user id and frame id are required")
	}
	eventID = mutationRealtimeID(eventID)
	result := CompatibilityBenchUpdate{ID: frameID}
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var parentFrameID sql.NullString
		if err := tx.QueryRowContext(ctx, `
			SELECT f.project_id, f.root_frame_id, f.parent_frame_id
			FROM frames f JOIN projects p ON p.id = f.project_id
			WHERE f.id = ? AND p.user_id = ?`, frameID, ownerUserID,
		).Scan(&result.ProjectID, &result.RootFrameID, &parentFrameID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: %s", ErrCompatibilityBenchNotFound, frameID)
			}
			return fmt.Errorf("get compatibility bench for update: %w", err)
		}
		if parentFrameID.Valid {
			return ErrCompatibilityBenchNotRoot
		}
		if input.Name != nil || input.TaskSummary != nil {
			now := s.now().UTC()
			if input.Name != nil {
				if _, err := tx.ExecContext(ctx, `UPDATE frames SET name = ?, updated_at = ? WHERE id = ?`, *input.Name, now, frameID); err != nil {
					return fmt.Errorf("update compatibility bench name: %w", err)
				}
			} else if _, err := tx.ExecContext(ctx, `UPDATE frames SET updated_at = ? WHERE id = ?`, now, frameID); err != nil {
				return fmt.Errorf("touch compatibility bench: %w", err)
			}
			if input.TaskSummary != nil {
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO frame_runtime_metadata (frame_id, task_summary)
					VALUES (?, ?)
					ON CONFLICT(frame_id) DO UPDATE SET task_summary = excluded.task_summary`, frameID, *input.TaskSummary); err != nil {
					return fmt.Errorf("update compatibility bench task summary: %w", err)
				}
			}
		}
		var name, taskSummary sql.NullString
		if err := tx.QueryRowContext(ctx, `
			SELECT f.name, m.task_summary
			FROM frames f LEFT JOIN frame_runtime_metadata m ON m.frame_id = f.id
			WHERE f.id = ?`, frameID,
		).Scan(&name, &taskSummary); err != nil {
			return fmt.Errorf("read updated compatibility bench: %w", err)
		}
		result.Name = nullableStringPointer(name)
		result.TaskSummary = nullableStringPointer(taskSummary)
		result.Event = RealtimeEventInput{
			ID: eventID, UserID: ownerUserID, ProjectID: result.ProjectID,
			RootFrameID: result.RootFrameID, FrameID: frameID, Type: "frame_update",
			Payload: map[string]any{
				"project_id": result.ProjectID, "root_frame_id": result.RootFrameID,
				"frame_id": frameID, "action": "bench_updated",
				"name": result.Name, "task_summary": result.TaskSummary,
			},
		}
		_, err := s.EnqueueRealtimeOutboxTx(ctx, tx, result.Event, "")
		return err
	})
	return result, err
}

func (s *Store) CompatibilityProcessingCounts(ctx context.Context, ownerUserID string) (map[string]int64, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ownerUserID = strings.TrimSpace(ownerUserID)
	if ownerUserID == "" {
		return nil, errors.New("processing count owner user id is required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT f.project_id, COUNT(*)
		FROM frames f
		JOIN projects p ON p.id = f.project_id
		LEFT JOIN frame_runtime_metadata m ON m.frame_id = f.id
		WHERE p.user_id = ?
			AND f.parent_frame_id IS NULL
			AND f.status = 'processing'
			AND f.conversation_type <> 'uploads'
			AND f.agent_name NOT IN ('CONCIERGE', 'CANVAS_CONCIERGE')
			AND COALESCE(m.is_hidden, 0) = 0
		GROUP BY f.project_id
		ORDER BY f.project_id`, ownerUserID)
	if err != nil {
		return nil, fmt.Errorf("list compatibility processing counts: %w", err)
	}
	defer rows.Close()
	counts := make(map[string]int64)
	for rows.Next() {
		var projectID string
		var count int64
		if err := rows.Scan(&projectID, &count); err != nil {
			return nil, fmt.Errorf("scan compatibility processing count: %w", err)
		}
		counts[projectID] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate compatibility processing counts: %w", err)
	}
	return counts, nil
}
