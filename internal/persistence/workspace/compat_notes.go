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

var (
	ErrCompatibilityNoteProjectNotFound = errors.New("compatibility note project not found")
	ErrCompatibilityNoteFrameNotFound   = errors.New("compatibility note frame not found")
	ErrCompatibilityNoteNotFound        = errors.New("compatibility note not found")
	ErrCompatibilityNoteContent         = errors.New("content is required")
	ErrCompatibilityNoteMessageIndex    = errors.New("target_message_index is required for message notes")
	ErrCompatibilityNoteArtifact        = errors.New("target_artifact_id is required for artifact notes")
)

type CompatibilityNoteFrameNotFoundError struct {
	FrameID string
}

func (err CompatibilityNoteFrameNotFoundError) Error() string {
	return "Frame " + err.FrameID + " not found"
}

func (CompatibilityNoteFrameNotFoundError) Unwrap() error {
	return ErrCompatibilityNoteFrameNotFound
}

type CompatibilityNoteArtifactNotFoundError struct{}

func (CompatibilityNoteArtifactNotFoundError) Error() string {
	return "target artifact not found"
}

func (CompatibilityNoteArtifactNotFoundError) Unwrap() error {
	return ErrCompatibilityNoteArtifact
}

type CompatibilityNote struct {
	ID                 string  `json:"id"`
	ProjectID          string  `json:"project_id"`
	UserID             string  `json:"user_id"`
	TargetType         string  `json:"target_type"`
	TargetFrameID      string  `json:"target_frame_id"`
	TargetMessageIndex *int    `json:"target_message_index"`
	TargetArtifactID   *string `json:"target_artifact_id"`
	Content            string  `json:"content"`
	CreatedAt          string  `json:"created_at"`
	UpdatedAt          string  `json:"updated_at"`
	TargetName         *string `json:"target_name"`
	MessagePreview     *string `json:"message_preview"`
}

type CompatibilityNoteListOptions struct {
	Query         string
	TargetType    string
	TargetFrameID string
}

func ValidCompatibilityNoteTargetType(value string) bool {
	switch value {
	case "message", "bench", "artifact":
		return true
	default:
		return false
	}
}

func (s *Store) ListCompatibilityNotes(
	ctx context.Context, ownerUserID, projectID string, options CompatibilityNoteListOptions,
) ([]CompatibilityNote, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ownerUserID, projectID = strings.TrimSpace(ownerUserID), strings.TrimSpace(projectID)
	var found string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM projects WHERE id = ? AND user_id = ?`, projectID, ownerUserID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("authorize compatibility notes project: %w", err)
	}
	query := compatibilityNoteSelect + ` WHERE note.project_id = ?`
	args := []any{projectID}
	if targetType := strings.TrimSpace(options.TargetType); targetType != "" {
		query += ` AND note.target_type = ?`
		args = append(args, targetType)
	}
	if targetFrameID := strings.TrimSpace(options.TargetFrameID); targetFrameID != "" {
		query += ` AND note.target_frame_id = ?`
		args = append(args, targetFrameID)
	}
	if text := strings.TrimSpace(options.Query); text != "" {
		query += ` AND lower(note.content) LIKE lower(?)`
		args = append(args, "%"+text+"%")
	}
	query += ` ORDER BY note.created_at DESC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, fmt.Errorf("list compatibility notes: %w", err)
	}
	defer rows.Close()
	notes := make([]CompatibilityNote, 0)
	for rows.Next() {
		note, err := scanCompatibilityNote(rows)
		if err != nil {
			return nil, false, err
		}
		notes = append(notes, note)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate compatibility notes: %w", err)
	}
	return notes, true, nil
}

func (s *Store) CreateCompatibilityNoteRealtime(
	ctx context.Context, ownerUserID string, input CreateProjectNoteInput,
) (CompatibilityNote, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ownerUserID = strings.TrimSpace(ownerUserID)
	input.ID = strings.TrimSpace(input.ID)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.TargetType = strings.TrimSpace(input.TargetType)
	input.TargetFrameID = strings.TrimSpace(input.TargetFrameID)
	input.TargetArtifactID = strings.TrimSpace(input.TargetArtifactID)
	if ownerUserID == "" || input.ID == "" || input.ProjectID == "" || input.TargetFrameID == "" {
		return CompatibilityNote{}, errors.New("note owner, id, project id, and target frame id are required")
	}
	if !ValidCompatibilityNoteTargetType(input.TargetType) {
		return CompatibilityNote{}, errors.New("invalid note target type")
	}
	if strings.TrimSpace(input.Content) == "" {
		return CompatibilityNote{}, ErrCompatibilityNoteContent
	}
	if input.TargetType == "message" && input.TargetMessageIndex == nil {
		return CompatibilityNote{}, ErrCompatibilityNoteMessageIndex
	}
	if input.TargetType == "artifact" && strings.TrimSpace(input.TargetArtifactID) == "" {
		return CompatibilityNote{}, ErrCompatibilityNoteArtifact
	}
	var note CompatibilityNote
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		if err := requireCompatibilityNoteProject(ctx, tx, ownerUserID, input.ProjectID); err != nil {
			return err
		}
		var frameProjectID string
		if err := tx.QueryRowContext(ctx, `SELECT project_id FROM frames WHERE id = ?`, input.TargetFrameID).Scan(&frameProjectID); err != nil || frameProjectID != input.ProjectID {
			return CompatibilityNoteFrameNotFoundError{FrameID: input.TargetFrameID}
		}
		if input.TargetType == "artifact" {
			var artifactProjectID string
			if err := tx.QueryRowContext(ctx, `SELECT project_id FROM artifacts WHERE id = ?`, input.TargetArtifactID).Scan(&artifactProjectID); err != nil || artifactProjectID != input.ProjectID {
				return CompatibilityNoteArtifactNotFoundError{}
			}
		}
		now := s.now().UTC()
		if _, err := tx.ExecContext(ctx, `INSERT INTO notes
			(id, project_id, user_id, target_type, target_frame_id, target_message_index, target_artifact_id, content, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, input.ID, input.ProjectID, ownerUserID, input.TargetType,
			input.TargetFrameID, input.TargetMessageIndex, nullableString(input.TargetArtifactID), input.Content, now, now); err != nil {
			return fmt.Errorf("insert compatibility note: %w", err)
		}
		var err error
		note, err = getCompatibilityNoteTx(ctx, tx, input.ID, ownerUserID)
		if err != nil {
			return err
		}
		return s.enqueueProjectDomainEventTx(ctx, tx, ownerUserID, input.ProjectID, "note_update", input.ID,
			map[string]any{"action": "created", "note": note})
	})
	return note, err
}

func (s *Store) UpdateCompatibilityNoteRealtime(
	ctx context.Context, ownerUserID, noteID, content string,
) (CompatibilityNote, error) {
	if strings.TrimSpace(content) == "" {
		return CompatibilityNote{}, ErrCompatibilityNoteContent
	}
	var note CompatibilityNote
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var projectID string
		if err := tx.QueryRowContext(ctx, `SELECT project_id FROM notes WHERE id = ? AND user_id = ?`, noteID, ownerUserID).Scan(&projectID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrCompatibilityNoteNotFound
			}
			return fmt.Errorf("get compatibility note owner: %w", err)
		}
		if err := requireCompatibilityNoteProject(ctx, tx, ownerUserID, projectID); err != nil {
			return ErrCompatibilityNoteNotFound
		}
		if _, err := tx.ExecContext(ctx, `UPDATE notes SET content = ?, updated_at = ? WHERE id = ? AND user_id = ?`,
			content, s.now().UTC(), noteID, ownerUserID); err != nil {
			return fmt.Errorf("update compatibility note: %w", err)
		}
		var err error
		note, err = getCompatibilityNoteTx(ctx, tx, noteID, ownerUserID)
		if err != nil {
			return err
		}
		return s.enqueueProjectDomainEventTx(ctx, tx, ownerUserID, projectID, "note_update", noteID,
			map[string]any{"action": "updated", "note": note})
	})
	return note, err
}

func (s *Store) DeleteCompatibilityNoteRealtime(ctx context.Context, ownerUserID, noteID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ownerUserID = strings.TrimSpace(ownerUserID)
	noteID = strings.TrimSpace(noteID)
	if ownerUserID == "" || noteID == "" {
		return ErrCompatibilityNoteNotFound
	}
	return s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var projectID string
		if err := tx.QueryRowContext(ctx,
			`SELECT project_id FROM notes WHERE id = ? AND user_id = ?`, noteID, ownerUserID,
		).Scan(&projectID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrCompatibilityNoteNotFound
			}
			return fmt.Errorf("get compatibility note owner: %w", err)
		}
		if err := requireCompatibilityNoteProject(ctx, tx, ownerUserID, projectID); err != nil {
			return ErrCompatibilityNoteNotFound
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM notes WHERE id = ? AND user_id = ?`, noteID, ownerUserID)
		if err != nil {
			return fmt.Errorf("delete compatibility note: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("count deleted compatibility note: %w", err)
		}
		if affected != 1 {
			return ErrCompatibilityNoteNotFound
		}
		return s.enqueueProjectDomainEventTx(ctx, tx, ownerUserID, projectID, "note_update", noteID,
			map[string]any{"action": "deleted", "note_id": noteID})
	})
}

const compatibilityNoteSelect = `
	SELECT note.id, note.project_id, note.user_id, note.target_type, note.target_frame_id,
		note.target_message_index, note.target_artifact_id, note.content, note.created_at, note.updated_at,
		frame.name, COALESCE(metadata.context_data, '{}')
	FROM notes note
	LEFT JOIN frames frame ON frame.id = note.target_frame_id
	LEFT JOIN frame_runtime_metadata metadata ON metadata.frame_id = note.target_frame_id`

func getCompatibilityNoteTx(ctx context.Context, tx *sql.Tx, noteID, ownerUserID string) (CompatibilityNote, error) {
	note, err := scanCompatibilityNote(tx.QueryRowContext(ctx, compatibilityNoteSelect+`
		WHERE note.id = ? AND note.user_id = ?`, noteID, ownerUserID))
	if errors.Is(err, sql.ErrNoRows) {
		return CompatibilityNote{}, ErrCompatibilityNoteNotFound
	}
	return note, err
}

func scanCompatibilityNote(row rowScanner) (CompatibilityNote, error) {
	var note CompatibilityNote
	var messageIndex sql.NullInt64
	var artifactID, targetName sql.NullString
	var createdAt, updatedAt time.Time
	var contextData string
	if err := row.Scan(&note.ID, &note.ProjectID, &note.UserID, &note.TargetType, &note.TargetFrameID,
		&messageIndex, &artifactID, &note.Content, &createdAt, &updatedAt, &targetName, &contextData); err != nil {
		return CompatibilityNote{}, err
	}
	if messageIndex.Valid {
		value := int(messageIndex.Int64)
		note.TargetMessageIndex = &value
	}
	note.TargetArtifactID = nullableStringPointer(artifactID)
	if targetName.Valid && strings.TrimSpace(targetName.String) != "" {
		name := targetName.String
		note.TargetName = &name
	}
	note.CreatedAt = compatibilityNoteTimestamp(createdAt)
	note.UpdatedAt = compatibilityNoteTimestamp(updatedAt)
	preview, err := compatibilityNoteMessagePreview(contextData, note.TargetMessageIndex)
	if err != nil {
		return CompatibilityNote{}, err
	}
	note.MessagePreview = preview
	return note, nil
}

func compatibilityNoteTimestamp(value time.Time) string {
	return value.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
}

func compatibilityNoteMessagePreview(raw string, messageIndex *int) (*string, error) {
	if messageIndex == nil || *messageIndex < 0 {
		return nil, nil
	}
	var contextData map[string]any
	if err := json.Unmarshal([]byte(raw), &contextData); err != nil {
		return nil, fmt.Errorf("decode compatibility note frame context: %w", err)
	}
	messages, ok := contextData["_messages"].([]any)
	if !ok || *messageIndex >= len(messages) {
		return nil, nil
	}
	message, ok := messages[*messageIndex].(map[string]any)
	if !ok {
		return nil, nil
	}
	text := ""
	switch content := message["content"].(type) {
	case string:
		text = content
	case []any:
		for _, block := range content {
			item, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if value, ok := item["text"]; ok {
				text = fmt.Sprint(value)
				break
			}
		}
	}
	text = compatibilityUTF16Prefix(text, 100)
	if text == "" {
		return nil, nil
	}
	return &text, nil
}

func compatibilityUTF16Prefix(value string, units int) string {
	var output strings.Builder
	used := 0
	for _, char := range value {
		width := 1
		if char > 0xffff {
			width = 2
		}
		if used+width > units {
			break
		}
		output.WriteRune(char)
		used += width
	}
	return output.String()
}

func requireCompatibilityNoteProject(ctx context.Context, tx *sql.Tx, ownerUserID, projectID string) error {
	var found string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM projects WHERE id = ? AND user_id = ?`, projectID, ownerUserID).Scan(&found); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrCompatibilityNoteProjectNotFound
		}
		return fmt.Errorf("authorize compatibility note project: %w", err)
	}
	return nil
}
