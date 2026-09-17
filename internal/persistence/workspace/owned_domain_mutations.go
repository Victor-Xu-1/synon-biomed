package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// CreateMemoryOwned is intentionally not enqueued: v1.1 has no Memory
// realtime event in its 47-event catalog. It still performs the owner and
// optional project-scope checks in the same transaction as the write.
func (s *Store) CreateMemoryOwned(ctx context.Context, input CreateMemoryInput, ownerUserID string) (Memory, error) {
	if err := mutationIdempotencyError(ctx); err != nil {
		return Memory{}, err
	}
	input.UserID, ownerUserID = strings.TrimSpace(input.UserID), strings.TrimSpace(ownerUserID)
	if input.UserID == "" || input.UserID != ownerUserID {
		return Memory{}, errors.New("memory user must match authenticated owner")
	}
	var memory Memory
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var err error
		memory, err = s.createMemoryTx(ctx, tx, input, ownerUserID)
		return err
	})
	return memory, err
}

func (s *Store) SupersedeMemoryOwned(ctx context.Context, memoryID, replacementID, ownerUserID string) error {
	if err := mutationIdempotencyError(ctx); err != nil {
		return err
	}
	memoryID, replacementID, ownerUserID = strings.TrimSpace(memoryID), strings.TrimSpace(replacementID), strings.TrimSpace(ownerUserID)
	if memoryID == "" || replacementID == "" || memoryID == replacementID || ownerUserID == "" {
		return errors.New("distinct memory and replacement ids plus owner are required")
	}
	return s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var oldUserID, newUserID string
		if err := tx.QueryRowContext(ctx, `SELECT user_id FROM memories WHERE id = ?`, memoryID).Scan(&oldUserID); err != nil {
			return fmt.Errorf("look up superseded memory: %w", err)
		}
		if err := tx.QueryRowContext(ctx, `SELECT user_id FROM memories WHERE id = ?`, replacementID).Scan(&newUserID); err != nil {
			return fmt.Errorf("look up replacement memory: %w", err)
		}
		if oldUserID != ownerUserID || newUserID != ownerUserID {
			return errors.New("memory replacement is unavailable to owner")
		}
		result, err := tx.ExecContext(ctx, `UPDATE memories SET superseded_by = ?, updated_at = ? WHERE id = ? AND user_id = ?`,
			replacementID, s.now().UTC(), memoryID, ownerUserID)
		if err != nil {
			return fmt.Errorf("supersede memory: %w", err)
		}
		return requireOneMutationRow(result, "memory", memoryID)
	})
}

func (s *Store) CreateAnnotationOwned(ctx context.Context, input CreateAnnotationInput, ownerUserID string) (Annotation, error) {
	if err := mutationIdempotencyError(ctx); err != nil {
		return Annotation{}, err
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		var annotation Annotation
		err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
			if err := requireProjectOwnerTx(ctx, tx, input.ProjectID, ownerUserID); err != nil {
				return err
			}
			input.ProjectID, input.TargetKind, input.TargetKey = strings.TrimSpace(input.ProjectID), strings.TrimSpace(input.TargetKind), strings.TrimSpace(input.TargetKey)
			if input.TargetKind == "" || input.TargetKey == "" || input.Body == nil {
				return errors.New("annotation target kind, target key, and body are required")
			}
			var maxIndex sql.NullInt64
			if err := tx.QueryRowContext(ctx, `SELECT MAX(label_idx) FROM annotations WHERE project_id = ? AND target_key = ?`,
				input.ProjectID, input.TargetKey).Scan(&maxIndex); err != nil {
				return fmt.Errorf("allocate annotation label: %w", err)
			}
			labelIndex := 0
			if maxIndex.Valid {
				labelIndex = int(maxIndex.Int64) + 1
			}
			now := s.now().UTC()
			annotation = Annotation{ID: uuid.NewString(), ProjectID: input.ProjectID, TargetKind: input.TargetKind,
				TargetKey: input.TargetKey, LabelIndex: labelIndex, ContentChecksum: strings.TrimSpace(input.ContentChecksum),
				Body: cloneAnnotationBody(input.Body), CreatedAt: now}
			annotation.decorateBody()
			body, err := json.Marshal(annotation.Body)
			if err != nil {
				return fmt.Errorf("marshal annotation body: %w", err)
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO annotations
				(id, project_id, target_kind, target_key, label_idx, content_checksum, body, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, annotation.ID, annotation.ProjectID, annotation.TargetKind,
				annotation.TargetKey, annotation.LabelIndex, nullableString(annotation.ContentChecksum), string(body), annotation.CreatedAt)
			return err
		})
		if err == nil {
			return annotation, nil
		}
		lastErr = err
		if !strings.Contains(err.Error(), "annotations.project_id, annotations.target_key, annotations.label_idx") {
			return Annotation{}, err
		}
	}
	return Annotation{}, fmt.Errorf("allocate annotation label after retries: %w", lastErr)
}

func (s *Store) UpdateAnnotationTextOwned(ctx context.Context, id, ownerUserID, text string) (Annotation, error) {
	if err := mutationIdempotencyError(ctx); err != nil {
		return Annotation{}, err
	}
	var annotation Annotation
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var err error
		annotation, err = scanAnnotation(tx.QueryRowContext(ctx, annotationSelect+` WHERE id = ?`, strings.TrimSpace(id)))
		if err != nil {
			return fmt.Errorf("annotation not found: %w", err)
		}
		if err := requireProjectOwnerTx(ctx, tx, annotation.ProjectID, ownerUserID); err != nil {
			return err
		}
		annotation.Body["text"] = text
		now := s.now().UTC()
		annotation.UpdatedAt = &now
		body, err := json.Marshal(annotation.Body)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE annotations SET body = ?, updated_at = ? WHERE id = ?`, string(body), now, annotation.ID)
		if err != nil {
			return fmt.Errorf("update annotation: %w", err)
		}
		return requireOneMutationRow(result, "annotation", annotation.ID)
	})
	return annotation, err
}

func (s *Store) DeleteAnnotationOwned(ctx context.Context, id, ownerUserID string) (bool, error) {
	if err := mutationIdempotencyError(ctx); err != nil {
		return false, err
	}
	deleted := false
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var projectID string
		if err := tx.QueryRowContext(ctx, `SELECT project_id FROM annotations WHERE id = ?`, strings.TrimSpace(id)).Scan(&projectID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		if err := requireProjectOwnerTx(ctx, tx, projectID, ownerUserID); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM annotations WHERE id = ?`, strings.TrimSpace(id))
		if err != nil {
			return fmt.Errorf("delete annotation: %w", err)
		}
		changed, err := result.RowsAffected()
		deleted = changed > 0
		return err
	})
	return deleted, err
}
