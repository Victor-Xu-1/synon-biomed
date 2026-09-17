package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"synon-go/internal/memorypolicy"

	"github.com/google/uuid"
)

// ListMemoryExtractionRows returns the workspace existing-facts manifest
// pool: active non-frame rows relevant to the current project, excluding
// categories explicitly configured not to auto-recall.
func (s *Store) ListMemoryExtractionRows(ctx context.Context, ownerUserID, projectID string) ([]Memory, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	ownerUserID = strings.TrimSpace(ownerUserID)
	projectID = strings.TrimSpace(projectID)
	if ownerUserID == "" {
		return nil, errors.New("memory owner is required")
	}
	query := memorySelect + ` WHERE m.user_id = ?
		AND m.superseded_by IS NULL
		AND m.subject_frame_id IS NULL
		AND COALESCE(c.auto_recall, 1) = 1`
	args := []any{ownerUserID}
	if projectID == "" {
		query += ` AND m.subject_project_id IS NULL AND m.subject_artifact_id IS NULL AND m.subject_version_id IS NULL`
	} else {
		query += ` AND (
			m.subject_project_id = ?
			OR m.subject_artifact_id IN (SELECT id FROM artifacts WHERE project_id = ?)
			OR m.subject_version_id IN (
				SELECT version.id FROM artifact_versions AS version
				JOIN artifacts AS artifact ON artifact.id = version.artifact_id
				WHERE artifact.project_id = ?
			)
			OR (m.subject_project_id IS NULL AND m.subject_artifact_id IS NULL AND m.subject_version_id IS NULL)
		)`
		args = append(args, projectID, projectID, projectID)
	}
	query += ` ORDER BY m.created_at DESC, m.id`
	rows, err := scanMemoryRows(ctx, s.db, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list memory extraction rows: %w", err)
	}
	return rows, nil
}

// SanitizeMemoryMultilineText is the workspace sanitizeMemoryBody helper.
// Unlike display sanitization it preserves meaningful line breaks.
func SanitizeMemoryMultilineText(value string) string {
	if value == "" {
		return value
	}
	value = memoryTagPattern.ReplaceAllString(value, "")
	value = memoryRolePrefixPattern.ReplaceAllString(value, "")
	value = memoryHeadingPattern.ReplaceAllString(value, "")
	return memoryBlankLinesPattern.ReplaceAllString(value, "\n\n")
}

// LastExtractMessageIndex reads the workspace durable per-root-frame
// extraction cursor. A missing runtime metadata row is the same as cursor 0.
func (s *Store) LastExtractMessageIndex(ctx context.Context, frameID string) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("workspace store is closed")
	}
	frameID = strings.TrimSpace(frameID)
	if frameID == "" {
		return 0, errors.New("frame id is required")
	}
	var index int
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(metadata.last_extract_msg_idx, 0)
		FROM frames AS frame
		LEFT JOIN frame_runtime_metadata AS metadata ON metadata.frame_id = frame.id
		WHERE frame.id = ?`, frameID).Scan(&index)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("frame %q does not exist", frameID)
	}
	if err != nil {
		return 0, fmt.Errorf("read memory extraction cursor: %w", err)
	}
	return index, nil
}

// SetLastExtractMessageIndex mirrors the workspace cursor fencing:
// ordinary advances are monotonic, while an overshoot recovery may move the
// cursor backwards only when the stored value still equals expectedOvershoot.
func (s *Store) SetLastExtractMessageIndex(ctx context.Context, frameID string, next int, expectedOvershoot *int) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("workspace store is closed")
	}
	frameID = strings.TrimSpace(frameID)
	if frameID == "" {
		return false, errors.New("frame id is required")
	}
	if next < 0 || expectedOvershoot != nil && *expectedOvershoot < 0 {
		return false, errors.New("memory extraction cursor must be non-negative")
	}

	var (
		result sql.Result
		err    error
	)
	if expectedOvershoot == nil {
		result, err = s.db.ExecContext(ctx, `INSERT INTO frame_runtime_metadata (frame_id, last_extract_msg_idx)
			SELECT id, ? FROM frames WHERE id = ?
			ON CONFLICT(frame_id) DO UPDATE SET last_extract_msg_idx = excluded.last_extract_msg_idx
			WHERE COALESCE(frame_runtime_metadata.last_extract_msg_idx, 0) < excluded.last_extract_msg_idx`, next, frameID)
	} else {
		result, err = s.db.ExecContext(ctx, `INSERT INTO frame_runtime_metadata (frame_id, last_extract_msg_idx)
			SELECT frame.id, ? FROM frames AS frame
			WHERE frame.id = ? AND (
				? = 0 OR EXISTS (
					SELECT 1 FROM frame_runtime_metadata AS metadata
					WHERE metadata.frame_id = frame.id
					AND COALESCE(metadata.last_extract_msg_idx, 0) = ?
				)
			)
			ON CONFLICT(frame_id) DO UPDATE SET last_extract_msg_idx = excluded.last_extract_msg_idx
			WHERE COALESCE(frame_runtime_metadata.last_extract_msg_idx, 0) = ?`, next, frameID, *expectedOvershoot, *expectedOvershoot, *expectedOvershoot)
	}
	if err != nil {
		return false, fmt.Errorf("set memory extraction cursor: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("count memory extraction cursor update: %w", err)
	}
	if rows == 0 {
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM frames WHERE id = ?`, frameID).Scan(&exists); err != nil {
			return false, fmt.Errorf("verify memory extraction frame: %w", err)
		}
		if exists == 0 {
			return false, fmt.Errorf("frame %q does not exist", frameID)
		}
	}
	return rows == 1, nil
}

// CreateAndSupersedeExtractorMemory creates the successor row and links its
// predecessor in one transaction. It is intentionally separate from the
// write_memory tool, whose workspace contract updates an active row in
// place. The expected snapshot excludes timestamps and last_surfaced_at, just
// like the workspace createAndSupersede expectation object.
func (s *Store) CreateAndSupersedeExtractorMemory(
	ctx context.Context,
	expected Memory,
	ownerUserID string,
	currentProjectID string,
	sourceFrameID string,
	newID string,
	body string,
	evidence string,
) (Memory, bool, error) {
	if s == nil || s.db == nil {
		return Memory{}, false, errors.New("workspace store is closed")
	}
	ownerUserID = strings.TrimSpace(ownerUserID)
	currentProjectID = strings.TrimSpace(currentProjectID)
	sourceFrameID = strings.TrimSpace(sourceFrameID)
	newID = strings.TrimSpace(newID)
	if newID == "" {
		newID = "mem_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:memorypolicy.GeneratedIDHexLength]
	}
	if ownerUserID == "" || expected.ID == "" || expected.UserID != ownerUserID {
		return Memory{}, false, errors.New("extractor replacement owner and predecessor are required")
	}
	if expected.Origin == "user" || expected.SubjectFrameID != "" {
		return Memory{}, false, ErrAgentMemoryMutationForbidden
	}
	if evidence = strings.TrimSpace(evidence); evidence == "" {
		evidence = expected.Evidence
	}

	var successor Memory
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		current, err := scanMemory(tx.QueryRowContext(ctx, memorySelect+` WHERE m.id = ? AND m.user_id = ?`, expected.ID, ownerUserID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrAgentMemoryChanged
		}
		if err != nil {
			return fmt.Errorf("read extractor predecessor: %w", err)
		}
		if !sameExtractorMemorySnapshot(current, expected) {
			return ErrAgentMemoryChanged
		}
		if err := requireAgentMemoryScopeTx(ctx, tx, current, ownerUserID, currentProjectID, ""); err != nil {
			return err
		}

		successor, err = s.createMemoryTx(ctx, tx, CreateMemoryInput{
			ID: newID, UserID: ownerUserID, Body: body, Origin: current.Origin, Evidence: evidence,
			SubjectProjectID: current.SubjectProjectID, SubjectArtifactID: current.SubjectArtifactID,
			SubjectVersionID: current.SubjectVersionID, SubjectFrameID: current.SubjectFrameID,
			SourceFrameID: sourceFrameID, CategoryID: current.CategoryID,
		}, ownerUserID)
		if err != nil {
			return err
		}
		updated, err := tx.ExecContext(ctx, `UPDATE memories SET superseded_by = ?, updated_at = ?
			WHERE id = ? AND user_id = ? AND superseded_by IS NULL
			AND body = ? AND evidence = ? AND origin = ?
			AND COALESCE(subject_project_id, '') = ?
			AND COALESCE(subject_artifact_id, '') = ?
			AND COALESCE(subject_version_id, '') = ?
			AND COALESCE(subject_frame_id, '') = ?
			AND COALESCE(category_id, '') = ?`,
			successor.ID, s.now().UTC(), current.ID, ownerUserID,
			expected.Body, expected.Evidence, expected.Origin,
			expected.SubjectProjectID, expected.SubjectArtifactID, expected.SubjectVersionID,
			expected.SubjectFrameID, expected.CategoryID)
		if err != nil {
			return fmt.Errorf("supersede extractor memory: %w", err)
		}
		if err := requireOneMutationRow(updated, "memory", current.ID); err != nil {
			return ErrAgentMemoryChanged
		}
		return nil
	})
	if errors.Is(err, ErrAgentMemoryChanged) {
		return Memory{}, false, nil
	}
	if err != nil {
		return Memory{}, false, err
	}
	return successor, true, nil
}

func sameExtractorMemorySnapshot(left, right Memory) bool {
	return left.ID == right.ID && left.UserID == right.UserID && left.Body == right.Body &&
		left.Evidence == right.Evidence && left.Origin == right.Origin && left.SupersededBy == right.SupersededBy &&
		left.SubjectProjectID == right.SubjectProjectID && left.SubjectArtifactID == right.SubjectArtifactID &&
		left.SubjectVersionID == right.SubjectVersionID && left.SubjectFrameID == right.SubjectFrameID &&
		left.CategoryID == right.CategoryID
}
