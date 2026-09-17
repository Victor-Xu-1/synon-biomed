package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type CrossSessionArtifactRef struct {
	ArtifactID string `json:"artifact_id"`
	Filename   string `json:"filename"`
}

type MoveCompatibilityConversationResult struct {
	RootFrameID    string
	FromProjectID  string
	ToProjectID    string
	FramesMoved    int
	ArtifactsMoved int
	FoldersMoved   int
	NotesMoved     int
	Event          *FrameEvent
}

func (s *Store) SetFrameMentionedArtifactIDs(frameID string, artifactIDs []string) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	frameID = strings.TrimSpace(frameID)
	if frameID == "" {
		return errors.New("frame id is required")
	}
	artifactIDs = normalizedUniqueStrings(artifactIDs)
	raw, err := json.Marshal(artifactIDs)
	if err != nil {
		return fmt.Errorf("encode mentioned artifact ids: %w", err)
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin mentioned artifact update: %w", err)
	}
	defer tx.Rollback()
	var found string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM frames WHERE id = ?`, frameID).Scan(&found); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("frame %q does not exist", frameID)
		}
		return fmt.Errorf("find mentioned artifact frame: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO frame_runtime_metadata (frame_id, mentioned_artifact_ids)
		VALUES (?, ?)
		ON CONFLICT(frame_id) DO UPDATE SET mentioned_artifact_ids = excluded.mentioned_artifact_ids`,
		frameID, string(raw)); err != nil {
		return fmt.Errorf("store mentioned artifact ids: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE frames SET updated_at = ? WHERE id = ?`, s.now().UTC(), frameID); err != nil {
		return fmt.Errorf("advance mentioned artifact trace: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit mentioned artifact ids: %w", err)
	}
	return nil
}

func (s *Store) ListCrossSessionArtifactRefs(rootFrameID string) ([]CrossSessionArtifactRef, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	rootFrameID = strings.TrimSpace(rootFrameID)
	if rootFrameID == "" {
		return nil, errors.New("root frame id is required")
	}
	ctx := context.Background()
	var projectID string
	if err := s.db.QueryRowContext(ctx, `SELECT project_id FROM frames WHERE id = ?`, rootFrameID).Scan(&projectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("frame %q does not exist", rootFrameID)
		}
		return nil, fmt.Errorf("find cross-session reference root: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(m.mentioned_artifact_ids, '[]')
		FROM frames AS f
		LEFT JOIN frame_runtime_metadata AS m ON m.frame_id = f.id
		WHERE f.root_frame_id = ?
		ORDER BY f.root_sequence, f.id`, rootFrameID)
	if err != nil {
		return nil, fmt.Errorf("list mentioned artifact metadata: %w", err)
	}
	mentioned := make(map[string]struct{})
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan mentioned artifact metadata: %w", err)
		}
		ids, err := decodeMentionedArtifactIDs(raw)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		for _, id := range ids {
			mentioned[id] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate mentioned artifact metadata: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close mentioned artifact metadata: %w", err)
	}
	if len(mentioned) == 0 {
		return []CrossSessionArtifactRef{}, nil
	}
	ids := make([]string, 0, len(mentioned))
	for id := range mentioned {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	references := make([]CrossSessionArtifactRef, 0, len(ids))
	for start := 0; start < len(ids); start += 400 {
		end := start + 400
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		arguments := make([]any, 0, len(chunk)+2)
		arguments = append(arguments, projectID, rootFrameID)
		for _, id := range chunk {
			arguments = append(arguments, id)
		}
		query := `
			SELECT a.id, a.name
			FROM artifacts AS a
			JOIN artifact_runtime_metadata AS m ON m.artifact_id = a.id
			WHERE a.project_id = ? AND COALESCE(m.root_frame_id, '') <> ?
			  AND a.id IN (` + sqlQuestionMarks(len(chunk)) + `)
			ORDER BY a.id`
		artifactRows, err := s.db.QueryContext(ctx, query, arguments...)
		if err != nil {
			return nil, fmt.Errorf("list cross-session artifacts: %w", err)
		}
		for artifactRows.Next() {
			var reference CrossSessionArtifactRef
			if err := artifactRows.Scan(&reference.ArtifactID, &reference.Filename); err != nil {
				_ = artifactRows.Close()
				return nil, fmt.Errorf("scan cross-session artifact: %w", err)
			}
			references = append(references, reference)
		}
		if err := artifactRows.Err(); err != nil {
			_ = artifactRows.Close()
			return nil, fmt.Errorf("iterate cross-session artifacts: %w", err)
		}
		if err := artifactRows.Close(); err != nil {
			return nil, fmt.Errorf("close cross-session artifacts: %w", err)
		}
	}
	return references, nil
}

func (s *Store) MoveCompatibilityConversationToProject(rootFrameID, targetProjectID string) (MoveCompatibilityConversationResult, error) {
	if s == nil || s.db == nil {
		return MoveCompatibilityConversationResult{}, errors.New("workspace store is closed")
	}
	rootFrameID = strings.TrimSpace(rootFrameID)
	targetProjectID = strings.TrimSpace(targetProjectID)
	if rootFrameID == "" || targetProjectID == "" {
		return MoveCompatibilityConversationResult{}, errors.New("root frame id and target project id are required")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MoveCompatibilityConversationResult{}, fmt.Errorf("begin conversation move: %w", err)
	}
	defer tx.Rollback()
	var sourceProjectID, storedRootID, status, conversationType string
	if err := tx.QueryRowContext(ctx, `
		SELECT project_id, root_frame_id, status, conversation_type
		FROM frames WHERE id = ?`, rootFrameID).Scan(
		&sourceProjectID, &storedRootID, &status, &conversationType,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return MoveCompatibilityConversationResult{}, fmt.Errorf("frame %q does not exist", rootFrameID)
		}
		return MoveCompatibilityConversationResult{}, fmt.Errorf("find conversation move root: %w", err)
	}
	var targetExists string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM projects WHERE id = ?`, targetProjectID).Scan(&targetExists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return MoveCompatibilityConversationResult{}, fmt.Errorf("target project %q does not exist", targetProjectID)
		}
		return MoveCompatibilityConversationResult{}, fmt.Errorf("find conversation move target: %w", err)
	}
	result := MoveCompatibilityConversationResult{
		RootFrameID: rootFrameID, FromProjectID: sourceProjectID, ToProjectID: targetProjectID,
	}
	if sourceProjectID == targetProjectID {
		if err := tx.Commit(); err != nil {
			return MoveCompatibilityConversationResult{}, fmt.Errorf("commit idempotent conversation move: %w", err)
		}
		return result, nil
	}
	if storedRootID != rootFrameID {
		return MoveCompatibilityConversationResult{}, fmt.Errorf("frame %q is not a conversation root", rootFrameID)
	}
	if conversationType != "" && conversationType != "agent" {
		return MoveCompatibilityConversationResult{}, fmt.Errorf("frame %q is a %s conversation and cannot be moved", rootFrameID, conversationType)
	}
	switch status {
	case "processing", "awaiting_plan_approval", "awaiting_user_response", "running":
		return MoveCompatibilityConversationResult{}, fmt.Errorf("cannot move session %s while it is %s; cancel it first", rootFrameID, status)
	}
	now := s.now().UTC()
	frameIDs, err := compatibilityRootFrameIDs(ctx, tx, rootFrameID)
	if err != nil {
		return MoveCompatibilityConversationResult{}, err
	}
	folders, err := compatibilityConversationFolders(ctx, tx, rootFrameID)
	if err != nil {
		return MoveCompatibilityConversationResult{}, err
	}
	if err := moveCompatibilityFolders(ctx, tx, targetProjectID, rootFrameID, folders, now); err != nil {
		return MoveCompatibilityConversationResult{}, err
	}
	artifactIDs, err := compatibilityConversationArtifactIDs(ctx, tx, rootFrameID)
	if err != nil {
		return MoveCompatibilityConversationResult{}, err
	}
	if err := moveCompatibilityArtifacts(ctx, tx, targetProjectID, artifactIDs, folders, now); err != nil {
		return MoveCompatibilityConversationResult{}, err
	}
	if err := moveCompatibilityAnnotations(ctx, tx, targetProjectID, artifactIDs); err != nil {
		return MoveCompatibilityConversationResult{}, err
	}
	framesChanged, err := tx.ExecContext(ctx, `UPDATE frames SET project_id = ?, updated_at = ? WHERE root_frame_id = ?`, targetProjectID, now, rootFrameID)
	if err != nil {
		return MoveCompatibilityConversationResult{}, fmt.Errorf("move conversation frames: %w", err)
	}
	framesMoved, err := framesChanged.RowsAffected()
	if err != nil {
		return MoveCompatibilityConversationResult{}, fmt.Errorf("count moved conversation frames: %w", err)
	}
	notesMoved, err := updateNotesForMovedFrames(ctx, tx, targetProjectID, frameIDs, now)
	if err != nil {
		return MoveCompatibilityConversationResult{}, err
	}
	event, err := appendFrameLifecycleEvent(ctx, tx, rootFrameID, "frame_moved", map[string]any{
		"sourceProjectId": sourceProjectID, "targetProjectId": targetProjectID,
		"framesMoved": framesMoved, "artifactsMoved": len(artifactIDs),
	}, now)
	if err != nil {
		return MoveCompatibilityConversationResult{}, err
	}
	result.FramesMoved = int(framesMoved)
	result.ArtifactsMoved = len(artifactIDs)
	result.FoldersMoved = len(folders)
	result.NotesMoved = notesMoved
	result.Event = &event
	if err := tx.Commit(); err != nil {
		return MoveCompatibilityConversationResult{}, fmt.Errorf("commit conversation move: %w", err)
	}
	return result, nil
}

type compatibilityFolderMove struct {
	ID       string
	ParentID string
	Name     string
}

func compatibilityRootFrameIDs(ctx context.Context, tx *sql.Tx, rootFrameID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM frames WHERE root_frame_id = ? ORDER BY root_sequence, id`, rootFrameID)
	if err != nil {
		return nil, fmt.Errorf("list conversation frames for move: %w", err)
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan conversation frame for move: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate conversation frames for move: %w", err)
	}
	return ids, nil
}

func compatibilityConversationFolders(ctx context.Context, tx *sql.Tx, rootFrameID string) ([]compatibilityFolderMove, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, COALESCE(parent_id, ''), name
		FROM artifact_folders WHERE root_frame_id = ?
		ORDER BY created_at, id`, rootFrameID)
	if err != nil {
		return nil, fmt.Errorf("list conversation folders for move: %w", err)
	}
	defer rows.Close()
	folders := make([]compatibilityFolderMove, 0)
	for rows.Next() {
		var folder compatibilityFolderMove
		if err := rows.Scan(&folder.ID, &folder.ParentID, &folder.Name); err != nil {
			return nil, fmt.Errorf("scan conversation folder for move: %w", err)
		}
		folders = append(folders, folder)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate conversation folders for move: %w", err)
	}
	return folders, nil
}

func moveCompatibilityFolders(ctx context.Context, tx *sql.Tx, targetProjectID, rootFrameID string, folders []compatibilityFolderMove, now time.Time) error {
	movedIDs := make(map[string]struct{}, len(folders))
	for _, folder := range folders {
		movedIDs[folder.ID] = struct{}{}
	}
	rows, err := tx.QueryContext(ctx, `SELECT COALESCE(parent_id, ''), name FROM artifact_folders WHERE project_id = ?`, targetProjectID)
	if err != nil {
		return fmt.Errorf("list target folder names: %w", err)
	}
	used := make(map[string]struct{})
	for rows.Next() {
		var parentID, name string
		if err := rows.Scan(&parentID, &name); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan target folder name: %w", err)
		}
		used[parentID+"\x00"+name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate target folder names: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close target folder names: %w", err)
	}
	suffix := " (moved " + shortIdentifier(rootFrameID) + ")"
	for _, folder := range folders {
		parentID := folder.ParentID
		if _, moved := movedIDs[parentID]; !moved {
			parentID = ""
		}
		name := uniqueMovedFolderName(folder.Name, parentID, suffix, used)
		if _, err := tx.ExecContext(ctx, `
			UPDATE artifact_folders
			SET project_id = ?, parent_id = NULLIF(?, ''), name = ?, updated_at = ?
			WHERE id = ?`, targetProjectID, parentID, name, now, folder.ID); err != nil {
			return fmt.Errorf("move conversation folder %s: %w", folder.ID, err)
		}
	}
	return nil
}

func compatibilityConversationArtifactIDs(ctx context.Context, tx *sql.Tx, rootFrameID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT artifact_id FROM artifact_runtime_metadata WHERE root_frame_id = ? ORDER BY artifact_id`, rootFrameID)
	if err != nil {
		return nil, fmt.Errorf("list conversation artifacts for move: %w", err)
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan conversation artifact for move: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate conversation artifacts for move: %w", err)
	}
	return ids, nil
}

func moveCompatibilityArtifacts(ctx context.Context, tx *sql.Tx, targetProjectID string, artifactIDs []string, folders []compatibilityFolderMove, now time.Time) error {
	if len(artifactIDs) == 0 {
		return nil
	}
	folderIDs := make(map[string]struct{}, len(folders))
	for _, folder := range folders {
		folderIDs[folder.ID] = struct{}{}
	}
	for _, artifactID := range artifactIDs {
		var folderID string
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(folder_id, '') FROM artifacts WHERE id = ?`, artifactID).Scan(&folderID); err != nil {
			return fmt.Errorf("find moved artifact folder %s: %w", artifactID, err)
		}
		if _, moved := folderIDs[folderID]; !moved {
			folderID = ""
		}
		if _, err := tx.ExecContext(ctx, `UPDATE artifacts SET project_id = ?, folder_id = NULLIF(?, ''), updated_at = ? WHERE id = ?`, targetProjectID, folderID, now, artifactID); err != nil {
			return fmt.Errorf("move conversation artifact %s: %w", artifactID, err)
		}
	}
	return nil
}

func moveCompatibilityAnnotations(ctx context.Context, tx *sql.Tx, targetProjectID string, artifactIDs []string) error {
	for _, artifactID := range artifactIDs {
		if _, err := tx.ExecContext(ctx, `
			UPDATE annotations SET project_id = ?
			WHERE target_kind = 'artifact' AND (target_key = ? OR target_key IN (
				SELECT 'av:' || id FROM artifact_versions WHERE artifact_id = ?
			))`, targetProjectID, artifactID, artifactID); err != nil {
			return fmt.Errorf("move artifact annotations %s: %w", artifactID, err)
		}
	}
	return nil
}

func updateNotesForMovedFrames(ctx context.Context, tx *sql.Tx, targetProjectID string, frameIDs []string, now time.Time) (int, error) {
	if len(frameIDs) == 0 {
		return 0, nil
	}
	total := int64(0)
	for start := 0; start < len(frameIDs); start += 400 {
		end := start + 400
		if end > len(frameIDs) {
			end = len(frameIDs)
		}
		chunk := frameIDs[start:end]
		arguments := make([]any, 0, len(chunk)+2)
		arguments = append(arguments, targetProjectID, now)
		for _, id := range chunk {
			arguments = append(arguments, id)
		}
		result, err := tx.ExecContext(ctx, `UPDATE notes SET project_id = ?, updated_at = ? WHERE target_frame_id IN (`+sqlQuestionMarks(len(chunk))+`)`, arguments...)
		if err != nil {
			return 0, fmt.Errorf("move conversation notes: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("count moved conversation notes: %w", err)
		}
		total += changed
	}
	return int(total), nil
}

func decodeMentionedArtifactIDs(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return []string{}, nil
	}
	var values []any
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("decode mentioned artifact ids: %w", err)
	}
	ids := make([]string, 0, len(values))
	for _, value := range values {
		switch typed := value.(type) {
		case string:
			ids = append(ids, typed)
		case map[string]any:
			if id, ok := typed["artifact_id"].(string); ok {
				ids = append(ids, id)
			}
		}
	}
	return normalizedUniqueStrings(ids), nil
}

func normalizedUniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func sqlQuestionMarks(count int) string {
	if count <= 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func shortIdentifier(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 8 {
		return value[:8]
	}
	return value
}

func uniqueMovedFolderName(name, parentID, suffix string, used map[string]struct{}) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Moved folder"
	}
	key := parentID + "\x00" + name
	if _, exists := used[key]; !exists {
		used[key] = struct{}{}
		return name
	}
	base := truncateUTF8(name, 255-len(suffix)-4)
	candidate := truncateUTF8(base+suffix, 255)
	for counter := 2; ; counter++ {
		key = parentID + "\x00" + candidate
		if _, exists := used[key]; !exists {
			used[key] = struct{}{}
			return candidate
		}
		numberedSuffix := fmt.Sprintf("%s %d", suffix, counter)
		candidate = truncateUTF8(truncateUTF8(name, 255-len(numberedSuffix)-4)+numberedSuffix, 255)
	}
}

func truncateUTF8(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	end := limit
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end]
}
