package workspace

import (
	"context"
	"database/sql"
	"fmt"
)

// memorySelect reads the canonical direct category reference introduced by
// workspace schema v7. The owner predicate prevents imported or corrupt rows
// from exposing another user's category metadata.
const memorySelect = `SELECT m.id, m.user_id, m.body, COALESCE(m.subject_project_id, ''),
	COALESCE(m.subject_artifact_id, ''), COALESCE(m.subject_version_id, ''),
	COALESCE(m.subject_frame_id, ''), COALESCE(m.source_frame_id, ''),
	m.origin, m.evidence, COALESCE(m.superseded_by, ''),
	COALESCE(c.id, ''), COALESCE(c.name, ''), COALESCE(c.guidance, ''),
	m.last_surfaced_at, m.created_at, m.updated_at
	FROM memories AS m
	LEFT JOIN memory_categories AS c ON c.id = m.category_id AND c.user_id = m.user_id`

func assignMemoryCategoryTx(ctx context.Context, tx *sql.Tx, memoryID, ownerUserID, categoryID string) (name, guidance string, err error) {
	result, err := tx.ExecContext(ctx, `UPDATE memories SET category_id = ?
		WHERE id = ? AND user_id = ? AND EXISTS (
			SELECT 1 FROM memory_categories WHERE id = ? AND user_id = ?
		)`, categoryID, memoryID, ownerUserID, categoryID, ownerUserID)
	if err != nil {
		return "", "", fmt.Errorf("assign memory category: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return "", "", fmt.Errorf("count memory category assignment: %w", err)
	}
	if changed != 1 {
		return "", "", sql.ErrNoRows
	}
	if err := tx.QueryRowContext(ctx, `SELECT name, guidance FROM memory_categories
		WHERE id = ? AND user_id = ?`, categoryID, ownerUserID).Scan(&name, &guidance); err != nil {
		return "", "", fmt.Errorf("read assigned memory category: %w", err)
	}
	return name, guidance, nil
}

func replaceMemoryCategoryTx(ctx context.Context, tx *sql.Tx, memoryID, ownerUserID, categoryID string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE memories SET category_id = NULL WHERE id = ? AND user_id = ?`, memoryID, ownerUserID); err != nil {
		return fmt.Errorf("clear memory category: %w", err)
	}
	if categoryID == "" {
		return nil
	}
	_, _, err := assignMemoryCategoryTx(ctx, tx, memoryID, ownerUserID, categoryID)
	return err
}

func countMemoryCategoryRowsTx(ctx context.Context, tx *sql.Tx, categoryID, ownerUserID string) (int, error) {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM memories AS m
		JOIN memory_categories AS c ON c.id = m.category_id AND c.user_id = m.user_id
		WHERE c.id = ? AND c.user_id = ? AND m.superseded_by IS NULL`, categoryID, ownerUserID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count category rows: %w", err)
	}
	return count, nil
}

func (s *Store) listMemoryCategories(ctx context.Context, userID string) ([]MemoryCategory, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.id, c.user_id, c.name, c.guidance, c.auto_recall,
		COUNT(m.id), c.created_at, c.updated_at
		FROM memory_categories AS c
		LEFT JOIN memories AS m ON m.category_id = c.id AND m.user_id = c.user_id AND m.superseded_by IS NULL
		WHERE c.user_id = ?
		GROUP BY c.id ORDER BY c.created_at, c.id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list memory categories: %w", err)
	}
	defer rows.Close()
	categories := make([]MemoryCategory, 0)
	for rows.Next() {
		var category MemoryCategory
		if err := rows.Scan(&category.ID, &category.UserID, &category.Name, &category.Guidance, &category.AutoRecall, &category.RowCount, &category.CreatedAt, &category.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan memory category: %w", err)
		}
		categories = append(categories, category)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate memory categories: %w", err)
	}
	return categories, nil
}

func deleteMemoryCategoryFactsTx(ctx context.Context, tx *sql.Tx, categoryID, ownerUserID string) (int, error) {
	result, err := tx.ExecContext(ctx, `DELETE FROM memories WHERE user_id = ? AND category_id = ?
		AND EXISTS (SELECT 1 FROM memory_categories WHERE id = ? AND user_id = ?)`,
		ownerUserID, categoryID, categoryID, ownerUserID)
	if err != nil {
		return 0, fmt.Errorf("delete category memories: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count category memory deletion: %w", err)
	}
	return int(count), nil
}

func clearMemoryCategoryAssignmentsTx(ctx context.Context, tx *sql.Tx, categoryID, ownerUserID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE memories SET category_id = NULL WHERE user_id = ? AND category_id = ?
		AND EXISTS (SELECT 1 FROM memory_categories WHERE id = ? AND user_id = ?)`,
		ownerUserID, categoryID, categoryID, ownerUserID)
	if err != nil {
		return fmt.Errorf("unassign memory category: %w", err)
	}
	return nil
}
