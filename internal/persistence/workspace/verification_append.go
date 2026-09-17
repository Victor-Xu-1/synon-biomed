package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// AppendVerificationCheck adds one idempotent reviewer result without replacing
// claims or checks created by other reviewers.
func (s *Store) AppendVerificationCheck(rootFrameID string, check VerificationCheck) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	rootFrameID = strings.TrimSpace(rootFrameID)
	if rootFrameID == "" {
		return errors.New("root frame id is required")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin verification append: %w", err)
	}
	defer tx.Rollback()
	var actualRoot string
	if err := tx.QueryRowContext(ctx, `SELECT root_frame_id FROM frames WHERE id = ?`, rootFrameID).Scan(&actualRoot); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("frame %s not found", rootFrameID)
		}
		return fmt.Errorf("look up verification root: %w", err)
	}
	if actualRoot != rootFrameID {
		return errors.New("verification checks must target a root frame")
	}
	if strings.TrimSpace(check.ID) != "" {
		var existingRoot string
		err := tx.QueryRowContext(ctx, `SELECT root_frame_id FROM verification_checks WHERE id = ?`, strings.TrimSpace(check.ID)).Scan(&existingRoot)
		if err == nil {
			if existingRoot != rootFrameID {
				return errors.New("verification check id belongs to a different root")
			}
			return tx.Commit()
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("look up verification check: %w", err)
		}
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM verification_checks WHERE root_frame_id = ?`, rootFrameID).Scan(&count); err != nil {
		return fmt.Errorf("count verification checks: %w", err)
	}
	if count >= maxVerificationRecords {
		return fmt.Errorf("verification checks exceed %d records", maxVerificationRecords)
	}
	if err := insertVerificationCheck(ctx, tx, rootFrameID, &check, s.now().UTC()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit verification append: %w", err)
	}
	return nil
}

func (s *Store) ResolveVerificationChecks(rootFrameID string, checkIDs []string, rebuttal string) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	rootFrameID = strings.TrimSpace(rootFrameID)
	if rootFrameID == "" {
		return errors.New("root frame id is required")
	}
	rebuttal = strings.TrimSpace(rebuttal)
	if len(rebuttal) > maxVerificationTextBytes {
		return fmt.Errorf("verification rebuttal exceeds %d bytes", maxVerificationTextBytes)
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin verification resolution: %w", err)
	}
	defer tx.Rollback()
	for _, checkID := range checkIDs {
		checkID = strings.TrimSpace(checkID)
		if checkID == "" {
			continue
		}
		result, err := tx.ExecContext(ctx, `UPDATE verification_checks SET status = 'resolved', rebuttal = ? WHERE id = ? AND root_frame_id = ?`, nullableVerificationString(rebuttal), checkID, rootFrameID)
		if err != nil {
			return fmt.Errorf("resolve verification check %s: %w", checkID, err)
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return fmt.Errorf("verification check %s not found in root %s", checkID, rootFrameID)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit verification resolution: %w", err)
	}
	return nil
}

func nullableVerificationString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.TrimSpace(value)
}
