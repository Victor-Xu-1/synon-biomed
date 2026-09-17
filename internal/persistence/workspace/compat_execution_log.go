package workspace

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

const maxCompatibilityExecutionCells = 10_000
const maxCompatibilityExecutionLogPageSize = 200

type CompatibilityExecutionLogRecord struct {
	ID           string  `json:"id"`
	FrameID      string  `json:"frame_id"`
	CellIndex    int     `json:"cell_index"`
	KernelID     string  `json:"kernel_id"`
	KernelKind   *string `json:"kernel_kind"`
	CondaEnv     string  `json:"conda_env"`
	Language     string  `json:"language"`
	Source       string  `json:"source"`
	Stdout       *string `json:"stdout"`
	Stderr       *string `json:"stderr"`
	ExitStatus   string  `json:"exit_status"`
	Origin       string  `json:"origin"`
	ExecutedAt   string  `json:"executed_at"`
	FilesWritten any     `json:"files_written"`
	ErrorLine    *int    `json:"error_lineno"`
	AgentName    string  `json:"agent_name"`
	DelegateName *string `json:"delegate_name"`
}

type CompatibilityExecutionLogPage struct {
	Records    []CompatibilityExecutionLogRecord `json:"records"`
	NextBefore string                            `json:"next_before,omitempty"`
	Total      int                               `json:"total"`
}

type compatibilityExecutionLogCursor struct {
	FrameID   string `json:"f"`
	CellIndex int    `json:"c"`
	ID        string `json:"i"`
}

const compatibilityExecutionLogSelect = `
	SELECT log.id, log.frame_id, log.cell_index, log.kernel_id, log.kernel_kind,
		log.conda_env, log.language, log.source, log.stdout, log.stderr,
		log.exit_status, COALESCE(log.origin, 'agent'), log.created_at, log.files_written,
		log.error_lineno, frame.agent_name, metadata.delegate_name
	FROM execution_log log
	JOIN frames frame ON frame.id = log.frame_id
	LEFT JOIN frame_runtime_metadata metadata ON metadata.frame_id = frame.id`

// ListCompatibilityExecutionLog reproduces v1.1's public read model. A version
// narrows the query through its persisted cell_sources instead of Go's private
// execution-link table.
func (s *Store) ListCompatibilityExecutionLog(
	ctx context.Context, ownerUserID, frameID, versionID string,
) ([]CompatibilityExecutionLogRecord, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ownerUserID, frameID, versionID = strings.TrimSpace(ownerUserID), strings.TrimSpace(frameID), strings.TrimSpace(versionID)
	if ownerUserID == "" || frameID == "" {
		return nil, false, errors.New("execution log owner and frame id are required")
	}
	var ownedFrameID string
	err := s.db.QueryRowContext(ctx, `
		SELECT frame.id FROM frames frame
		JOIN projects project ON project.id = frame.project_id
		WHERE frame.id = ? AND project.user_id = ?`, frameID, ownerUserID).Scan(&ownedFrameID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("authorize compatibility execution frame: %w", err)
	}

	query := ""
	args := make([]any, 0)
	if versionID == "" {
		query = compatibilityExecutionLogSelect + `
			WHERE (frame.root_frame_id = ? OR frame.id = ?)
				AND COALESCE(metadata.is_hidden, 0) = 0
			ORDER BY log.frame_id, log.cell_index`
		args = append(args, frameID, frameID)
	} else {
		var cellSources, sourceFrame sql.NullString
		err := s.db.QueryRowContext(ctx, `
			SELECT provenance.cell_sources, provenance.frame_id
			FROM artifact_versions version
			JOIN artifacts artifact ON artifact.id = version.artifact_id
			JOIN projects project ON project.id = artifact.project_id
			LEFT JOIN artifact_version_provenance provenance ON provenance.version_id = version.id
			WHERE version.id = ? AND project.user_id = ?`, versionID, ownerUserID).Scan(&cellSources, &sourceFrame)
		if errors.Is(err, sql.ErrNoRows) {
			return []CompatibilityExecutionLogRecord{}, true, nil
		}
		if err != nil {
			return nil, false, fmt.Errorf("get compatibility execution version: %w", err)
		}
		executionFrameID := frameID
		if sourceFrame.Valid && strings.TrimSpace(sourceFrame.String) != "" {
			executionFrameID = strings.TrimSpace(sourceFrame.String)
			if executionFrameID != frameID {
				var found string
				err := s.db.QueryRowContext(ctx, `
					SELECT frame.id FROM frames frame
					JOIN projects project ON project.id = frame.project_id
					WHERE frame.id = ? AND project.user_id = ?`, executionFrameID, ownerUserID).Scan(&found)
				if errors.Is(err, sql.ErrNoRows) {
					return []CompatibilityExecutionLogRecord{}, true, nil
				}
				if err != nil {
					return nil, false, fmt.Errorf("authorize compatibility execution source frame: %w", err)
				}
			}
		}
		cellIndexes, err := compatibilityExecutionCellIndexes(cellSources)
		if err != nil {
			return nil, false, err
		}
		if len(cellIndexes) == 0 {
			return []CompatibilityExecutionLogRecord{}, true, nil
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(cellIndexes)), ",")
		query = compatibilityExecutionLogSelect + ` WHERE log.frame_id = ? AND log.cell_index IN (` + placeholders + `)
			ORDER BY log.frame_id, log.cell_index`
		args = append(args, executionFrameID)
		for _, index := range cellIndexes {
			args = append(args, index)
		}
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, fmt.Errorf("query compatibility execution log: %w", err)
	}
	defer rows.Close()
	records := make([]CompatibilityExecutionLogRecord, 0)
	for rows.Next() {
		record, err := scanCompatibilityExecutionLog(rows)
		if err != nil {
			return nil, false, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate compatibility execution log: %w", err)
	}
	return records, true, nil
}

// ListCompatibilityExecutionLogPage returns the newest execution records in a
// bounded keyset page. The durable log remains unbounded; only the active read
// window matches the reference 200-record tail/page boundary.
func (s *Store) ListCompatibilityExecutionLogPage(
	ctx context.Context, ownerUserID, frameID, before string, limit int,
) (CompatibilityExecutionLogPage, bool, error) {
	if s == nil || s.db == nil {
		return CompatibilityExecutionLogPage{}, false, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ownerUserID, frameID = strings.TrimSpace(ownerUserID), strings.TrimSpace(frameID)
	if ownerUserID == "" || frameID == "" {
		return CompatibilityExecutionLogPage{}, false, errors.New("execution log owner and frame id are required")
	}
	if limit <= 0 || limit > maxCompatibilityExecutionLogPageSize {
		return CompatibilityExecutionLogPage{}, false, errors.New("execution log page limit must be between 1 and 200")
	}
	var ownedFrameID string
	err := s.db.QueryRowContext(ctx, `
		SELECT frame.id FROM frames frame
		JOIN projects project ON project.id = frame.project_id
		WHERE frame.id = ? AND project.user_id = ?`, frameID, ownerUserID).Scan(&ownedFrameID)
	if errors.Is(err, sql.ErrNoRows) {
		return CompatibilityExecutionLogPage{}, false, nil
	}
	if err != nil {
		return CompatibilityExecutionLogPage{}, false, fmt.Errorf("authorize compatibility execution frame: %w", err)
	}

	where := ` WHERE (frame.root_frame_id = ? OR frame.id = ?)
		AND COALESCE(metadata.is_hidden, 0) = 0`
	args := []any{frameID, frameID}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM execution_log log
		JOIN frames frame ON frame.id = log.frame_id
		LEFT JOIN frame_runtime_metadata metadata ON metadata.frame_id = frame.id
		WHERE (frame.root_frame_id = ? OR frame.id = ?)
			AND COALESCE(metadata.is_hidden, 0) = 0`, frameID, frameID).Scan(&total); err != nil {
		return CompatibilityExecutionLogPage{}, false, fmt.Errorf("count compatibility execution log page: %w", err)
	}
	if strings.TrimSpace(before) != "" {
		cursor, err := decodeCompatibilityExecutionLogCursor(before)
		if err != nil {
			return CompatibilityExecutionLogPage{}, false, err
		}
		where += ` AND (log.frame_id < ? OR
			(log.frame_id = ? AND log.cell_index < ?) OR
			(log.frame_id = ? AND log.cell_index = ? AND log.id < ?))`
		args = append(args, cursor.FrameID, cursor.FrameID, cursor.CellIndex, cursor.FrameID, cursor.CellIndex, cursor.ID)
	}
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, compatibilityExecutionLogSelect+where+`
		ORDER BY log.frame_id DESC, log.cell_index DESC, log.id DESC LIMIT ?`, args...)
	if err != nil {
		return CompatibilityExecutionLogPage{}, false, fmt.Errorf("query compatibility execution log page: %w", err)
	}
	defer rows.Close()
	records := make([]CompatibilityExecutionLogRecord, 0, limit+1)
	for rows.Next() {
		record, err := scanCompatibilityExecutionLog(rows)
		if err != nil {
			return CompatibilityExecutionLogPage{}, false, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return CompatibilityExecutionLogPage{}, false, fmt.Errorf("iterate compatibility execution log page: %w", err)
	}
	hasMore := len(records) > limit
	if hasMore {
		records = records[:limit]
	}
	for left, right := 0, len(records)-1; left < right; left, right = left+1, right-1 {
		records[left], records[right] = records[right], records[left]
	}
	page := CompatibilityExecutionLogPage{Records: records, Total: total}
	if hasMore && len(records) > 0 {
		page.NextBefore, err = encodeCompatibilityExecutionLogCursor(records[0])
		if err != nil {
			return CompatibilityExecutionLogPage{}, false, err
		}
	}
	return page, true, nil
}

func encodeCompatibilityExecutionLogCursor(record CompatibilityExecutionLogRecord) (string, error) {
	raw, err := json.Marshal(compatibilityExecutionLogCursor{
		FrameID: record.FrameID, CellIndex: record.CellIndex, ID: record.ID,
	})
	if err != nil {
		return "", fmt.Errorf("encode execution log cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeCompatibilityExecutionLogCursor(value string) (compatibilityExecutionLogCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return compatibilityExecutionLogCursor{}, errors.New("execution log cursor is invalid")
	}
	var cursor compatibilityExecutionLogCursor
	if err := json.Unmarshal(raw, &cursor); err != nil || strings.TrimSpace(cursor.FrameID) == "" ||
		strings.TrimSpace(cursor.ID) == "" || cursor.CellIndex < 0 {
		return compatibilityExecutionLogCursor{}, errors.New("execution log cursor is invalid")
	}
	return cursor, nil
}

func compatibilityExecutionCellIndexes(value sql.NullString) ([]int, error) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil, nil
	}
	var sources []map[string]any
	if err := json.Unmarshal([]byte(value.String), &sources); err != nil {
		return nil, fmt.Errorf("decode compatibility execution cell sources: %w", err)
	}
	seen := make(map[int]struct{}, len(sources))
	indexes := make([]int, 0, len(sources))
	for _, source := range sources {
		number, ok := source["cell_index"].(float64)
		if !ok || number < 0 || number > math.MaxInt || math.Trunc(number) != number {
			continue
		}
		index := int(number)
		if _, exists := seen[index]; exists {
			continue
		}
		seen[index] = struct{}{}
		indexes = append(indexes, index)
		if len(indexes) > maxCompatibilityExecutionCells {
			return nil, errors.New("compatibility execution cell source limit exceeded")
		}
	}
	return indexes, nil
}

func scanCompatibilityExecutionLog(row rowScanner) (CompatibilityExecutionLogRecord, error) {
	var record CompatibilityExecutionLogRecord
	var kernelKind, stdout, stderr, filesWritten, delegateName sql.NullString
	var errorLine sql.NullInt64
	var executedAt time.Time
	if err := row.Scan(
		&record.ID, &record.FrameID, &record.CellIndex, &record.KernelID, &kernelKind,
		&record.CondaEnv, &record.Language, &record.Source, &stdout, &stderr,
		&record.ExitStatus, &record.Origin, &executedAt, &filesWritten,
		&errorLine, &record.AgentName, &delegateName,
	); err != nil {
		return CompatibilityExecutionLogRecord{}, fmt.Errorf("scan compatibility execution log: %w", err)
	}
	record.KernelKind = nullableStringPointer(kernelKind)
	record.Stdout = nullableStringPointer(stdout)
	record.Stderr = nullableStringPointer(stderr)
	record.DelegateName = nullableStringPointer(delegateName)
	record.ExecutedAt = executedAt.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
	if errorLine.Valid {
		value := int(errorLine.Int64)
		record.ErrorLine = &value
	}
	if filesWritten.Valid && strings.TrimSpace(filesWritten.String) != "" {
		if err := json.Unmarshal([]byte(filesWritten.String), &record.FilesWritten); err != nil {
			return CompatibilityExecutionLogRecord{}, fmt.Errorf("decode compatibility execution files_written: %w", err)
		}
	}
	return record, nil
}
