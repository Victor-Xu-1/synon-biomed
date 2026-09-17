package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type TranscriptAnnotation struct {
	ID           string
	RootFrameID  string
	MessageUUID  string
	MessageIndex int
	BlockIndex   int
	Source       string
	ToolName     string
	AnchorText   string
	StartOffset  *int
	EndOffset    *int
	Kind         string
	Origin       string
	ReadAt       *time.Time
	Note         string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type CreateTranscriptAnnotationInput struct {
	ID           string
	RootFrameID  string
	MessageUUID  string
	MessageIndex int
	BlockIndex   int
	Source       string
	ToolName     string
	AnchorText   string
	StartOffset  *int
	EndOffset    *int
	Kind         string
	Origin       string
	Note         string
}

type UpdateTranscriptAnnotationInput struct {
	Note *string
	Read *bool
}

const transcriptAnnotationColumns = `
	id, root_frame_id, message_uuid, message_index, block_index, source, tool_name,
	anchor_text, start_offset, end_offset, kind, origin, read_at, note, created_at, updated_at`

func (s *Store) ListTranscriptAnnotations(rootFrameID string) ([]TranscriptAnnotation, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	rootFrameID = strings.TrimSpace(rootFrameID)
	if rootFrameID == "" {
		return nil, errors.New("transcript annotation root frame id is required")
	}
	rows, err := s.db.QueryContext(context.Background(), `SELECT `+transcriptAnnotationColumns+`
		FROM transcript_annotations WHERE root_frame_id = ? ORDER BY created_at, id`, rootFrameID)
	if err != nil {
		return nil, fmt.Errorf("list transcript annotations: %w", err)
	}
	defer rows.Close()
	annotations := []TranscriptAnnotation{}
	for rows.Next() {
		annotation, err := scanTranscriptAnnotation(rows)
		if err != nil {
			return nil, err
		}
		annotations = append(annotations, annotation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate transcript annotations: %w", err)
	}
	return annotations, nil
}

func (s *Store) CreateTranscriptAnnotation(input CreateTranscriptAnnotationInput) (TranscriptAnnotation, error) {
	if s == nil || s.db == nil {
		return TranscriptAnnotation{}, errors.New("workspace store is closed")
	}
	input.ID = strings.TrimSpace(input.ID)
	input.RootFrameID = strings.TrimSpace(input.RootFrameID)
	input.MessageUUID = strings.TrimSpace(input.MessageUUID)
	input.Source = strings.TrimSpace(input.Source)
	input.ToolName = strings.TrimSpace(input.ToolName)
	input.Kind = strings.TrimSpace(input.Kind)
	input.Origin = strings.TrimSpace(input.Origin)
	if input.ID == "" {
		input.ID = uuid.NewString()
	}
	if input.Origin == "" {
		input.Origin = "user"
	}
	if err := validateTranscriptAnnotationInput(input); err != nil {
		return TranscriptAnnotation{}, err
	}
	now := s.now().UTC()
	annotation := TranscriptAnnotation{
		ID: input.ID, RootFrameID: input.RootFrameID, MessageUUID: input.MessageUUID,
		MessageIndex: input.MessageIndex, BlockIndex: input.BlockIndex, Source: input.Source,
		ToolName: input.ToolName, AnchorText: input.AnchorText, StartOffset: input.StartOffset,
		EndOffset: input.EndOffset, Kind: input.Kind, Origin: input.Origin, Note: input.Note,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := s.db.ExecContext(context.Background(), `
		INSERT INTO transcript_annotations (`+transcriptAnnotationColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, ?)`,
		annotation.ID, annotation.RootFrameID, nullableString(annotation.MessageUUID),
		annotation.MessageIndex, annotation.BlockIndex, annotation.Source, nullableString(annotation.ToolName),
		annotation.AnchorText, transcriptNullableInt(annotation.StartOffset), transcriptNullableInt(annotation.EndOffset),
		annotation.Kind, annotation.Origin, annotation.Note, annotation.CreatedAt, annotation.UpdatedAt,
	); err != nil {
		return TranscriptAnnotation{}, fmt.Errorf("create transcript annotation: %w", err)
	}
	return annotation, nil
}

func (s *Store) UpdateTranscriptAnnotation(rootFrameID, id string, input UpdateTranscriptAnnotationInput) (TranscriptAnnotation, bool, error) {
	if s == nil || s.db == nil {
		return TranscriptAnnotation{}, false, errors.New("workspace store is closed")
	}
	rootFrameID, id = strings.TrimSpace(rootFrameID), strings.TrimSpace(id)
	if rootFrameID == "" || id == "" {
		return TranscriptAnnotation{}, false, errors.New("transcript annotation root frame id and id are required")
	}
	now := s.now().UTC()
	assignments := []string{"updated_at = ?"}
	args := []any{now}
	if input.Note != nil {
		assignments = append(assignments, "note = ?")
		args = append(args, *input.Note)
	}
	if input.Read != nil {
		assignments = append(assignments, "read_at = ?")
		if *input.Read {
			args = append(args, now)
		} else {
			args = append(args, nil)
		}
	}
	args = append(args, id, rootFrameID)
	annotation, err := scanTranscriptAnnotation(s.db.QueryRowContext(context.Background(), `
		UPDATE transcript_annotations SET `+strings.Join(assignments, ", ")+`
		WHERE id = ? AND root_frame_id = ? RETURNING `+transcriptAnnotationColumns, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return TranscriptAnnotation{}, false, nil
	}
	if err != nil {
		return TranscriptAnnotation{}, false, fmt.Errorf("update transcript annotation: %w", err)
	}
	return annotation, true, nil
}

func (s *Store) DeleteTranscriptAnnotation(rootFrameID, id string) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("workspace store is closed")
	}
	rootFrameID, id = strings.TrimSpace(rootFrameID), strings.TrimSpace(id)
	if rootFrameID == "" || id == "" {
		return false, errors.New("transcript annotation root frame id and id are required")
	}
	result, err := s.db.ExecContext(context.Background(), `
		DELETE FROM transcript_annotations WHERE id = ? AND root_frame_id = ?`, id, rootFrameID)
	if err != nil {
		return false, fmt.Errorf("delete transcript annotation: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("count deleted transcript annotation: %w", err)
	}
	return changed > 0, nil
}

func (s *Store) DrainTranscriptAnnotations(rootFrameID string, ids []string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("workspace store is closed")
	}
	rootFrameID = strings.TrimSpace(rootFrameID)
	if rootFrameID == "" {
		return 0, errors.New("transcript annotation root frame id is required")
	}
	unique := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, found := seen[id]; found {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return 0, nil
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin transcript annotation drain: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var deleted int64
	for start := 0; start < len(unique); start += 500 {
		end := start + 500
		if end > len(unique) {
			end = len(unique)
		}
		batch := unique[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		args := make([]any, 0, len(batch)+1)
		args = append(args, rootFrameID)
		for _, id := range batch {
			args = append(args, id)
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM transcript_annotations
			WHERE root_frame_id = ? AND id IN (`+placeholders+`)`, args...)
		if err != nil {
			return 0, fmt.Errorf("drain transcript annotations: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("count drained transcript annotations: %w", err)
		}
		deleted += changed
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit transcript annotation drain: %w", err)
	}
	return deleted, nil
}

func validateTranscriptAnnotationInput(input CreateTranscriptAnnotationInput) error {
	if input.ID == "" || input.RootFrameID == "" {
		return errors.New("transcript annotation id and root frame id are required")
	}
	if input.MessageIndex < 0 || input.BlockIndex < 0 {
		return errors.New("transcript annotation message and block indexes must be non-negative")
	}
	if input.Source != "assistant" && input.Source != "tool_input" && input.Source != "tool_result" {
		return errors.New("invalid transcript annotation source")
	}
	if input.Kind != "annotation" && input.Kind != "bookmark" {
		return errors.New("invalid transcript annotation kind")
	}
	if input.Origin != "user" && input.Origin != "agent" {
		return errors.New("invalid transcript annotation origin")
	}
	if (input.StartOffset != nil && *input.StartOffset < 0) || (input.EndOffset != nil && *input.EndOffset < 0) {
		return errors.New("transcript annotation offsets must be non-negative")
	}
	if input.StartOffset != nil && input.EndOffset != nil && *input.StartOffset > *input.EndOffset {
		return errors.New("transcript annotation start offset must not exceed end offset")
	}
	return nil
}

func scanTranscriptAnnotation(scanner interface{ Scan(...any) error }) (TranscriptAnnotation, error) {
	var annotation TranscriptAnnotation
	var messageUUID, toolName sql.NullString
	var startOffset, endOffset sql.NullInt64
	var readAt sql.NullTime
	if err := scanner.Scan(
		&annotation.ID, &annotation.RootFrameID, &messageUUID, &annotation.MessageIndex,
		&annotation.BlockIndex, &annotation.Source, &toolName, &annotation.AnchorText,
		&startOffset, &endOffset, &annotation.Kind, &annotation.Origin, &readAt,
		&annotation.Note, &annotation.CreatedAt, &annotation.UpdatedAt,
	); err != nil {
		return TranscriptAnnotation{}, err
	}
	annotation.MessageUUID = messageUUID.String
	annotation.ToolName = toolName.String
	if startOffset.Valid {
		value := int(startOffset.Int64)
		annotation.StartOffset = &value
	}
	if endOffset.Valid {
		value := int(endOffset.Int64)
		annotation.EndOffset = &value
	}
	if readAt.Valid {
		value := readAt.Time.UTC()
		annotation.ReadAt = &value
	}
	annotation.CreatedAt = annotation.CreatedAt.UTC()
	annotation.UpdatedAt = annotation.UpdatedAt.UTC()
	return annotation, nil
}

func transcriptNullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}
