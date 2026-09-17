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

type ExecutionLogRecord struct {
	ID           string    `json:"id"`
	FrameID      string    `json:"frame_id"`
	CellIndex    int       `json:"cell_index"`
	KernelID     string    `json:"kernel_id"`
	KernelKind   string    `json:"kernel_kind,omitempty"`
	CondaEnv     string    `json:"conda_env"`
	Language     string    `json:"language"`
	Source       string    `json:"source"`
	Stdout       string    `json:"stdout,omitempty"`
	Stderr       string    `json:"stderr,omitempty"`
	ExitStatus   string    `json:"exit_status"`
	Origin       string    `json:"origin"`
	ExecutedAt   time.Time `json:"executed_at"`
	FilesWritten any       `json:"files_written,omitempty"`
	FilesRead    any       `json:"files_read,omitempty"`
	ErrorLine    *int      `json:"error_lineno,omitempty"`
	Detection    any       `json:"detection,omitempty"`
	AgentName    string    `json:"agent_name"`
	DelegateName string    `json:"delegate_name,omitempty"`
}

type SaveExecutionLogInput struct {
	Record                         ExecutionLogRecord
	VersionIDs                     []string
	ExpectedOwnerID                string
	ExpectedProjectID              string
	ExpectedFrameIncarnationID     string
	ExpectedRootFrameIncarnationID string
}

const executionLogSelect = `
	SELECT log.id, log.frame_id, log.cell_index, log.kernel_id, COALESCE(log.kernel_kind, ''),
		log.conda_env, log.language, log.source, COALESCE(log.stdout, ''), COALESCE(log.stderr, ''),
		log.exit_status, log.origin, log.created_at, log.files_written, log.files_read,
		log.error_lineno, log.detection, frame.agent_name, COALESCE(metadata.delegate_name, '')
	FROM execution_log log
	JOIN frames frame ON frame.id = log.frame_id
	LEFT JOIN frame_runtime_metadata metadata ON metadata.frame_id = frame.id`

// ListExecutionLog returns the original v1.1 execution record shape. When a
// version is supplied, its persisted cell-source links determine the result.
func (s *Store) ListExecutionLog(frameID, versionID string) ([]ExecutionLogRecord, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	frameID, versionID = strings.TrimSpace(frameID), strings.TrimSpace(versionID)
	if frameID == "" {
		return nil, errors.New("execution log frame id is required")
	}
	ctx := context.Background()
	var rows *sql.Rows
	var err error
	if versionID != "" {
		rows, err = s.db.QueryContext(ctx, executionLogSelect+`
			JOIN artifact_version_execution_links link ON link.execution_log_id = log.id
			WHERE link.version_id = ? ORDER BY log.frame_id, log.cell_index, log.id`, versionID)
	} else {
		rootFrameID, resolveErr := s.resolveRootFrameID(frameID)
		if resolveErr != nil {
			return nil, resolveErr
		}
		rows, err = s.db.QueryContext(ctx, executionLogSelect+`
			WHERE (frame.root_frame_id = ? OR frame.id = ?)
				AND COALESCE(metadata.is_hidden, 0) = 0
			ORDER BY log.frame_id, log.cell_index, log.id`, rootFrameID, rootFrameID)
	}
	if err != nil {
		return nil, fmt.Errorf("query execution log: %w", err)
	}
	defer rows.Close()
	records := []ExecutionLogRecord{}
	for rows.Next() {
		record, err := scanExecutionLog(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate execution log: %w", err)
	}
	return records, nil
}

// GetExecutionLog returns one exact frame-owned execution record. It is used
// by idempotent host tools to distinguish a replay from a new mutation without
// scanning the root Frame history.
func (s *Store) GetExecutionLog(frameID, executionID string) (ExecutionLogRecord, bool, error) {
	if s == nil || s.db == nil {
		return ExecutionLogRecord{}, false, errors.New("workspace store is closed")
	}
	frameID, executionID = strings.TrimSpace(frameID), strings.TrimSpace(executionID)
	if frameID == "" || executionID == "" {
		return ExecutionLogRecord{}, false, errors.New("execution log frame and identity are required")
	}
	row := s.db.QueryRowContext(context.Background(), executionLogSelect+`
		WHERE log.frame_id = ? AND log.id = ?`, frameID, executionID)
	record, err := scanExecutionLog(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ExecutionLogRecord{}, false, nil
	}
	if err != nil {
		return ExecutionLogRecord{}, false, err
	}
	return record, true, nil
}

// SaveExecutionLog persists a real kernel execution and optionally links it to
// artifact versions whose provenance references the executed cell.
func (s *Store) SaveExecutionLog(input SaveExecutionLogInput) (ExecutionLogRecord, error) {
	if s == nil || s.db == nil {
		return ExecutionLogRecord{}, errors.New("workspace store is closed")
	}
	prepared, err := s.prepareExecutionLog(input)
	if err != nil {
		return ExecutionLogRecord{}, err
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ExecutionLogRecord{}, err
	}
	defer tx.Rollback()
	record, err := saveExecutionLogTx(ctx, tx, prepared)
	if err != nil {
		return ExecutionLogRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return ExecutionLogRecord{}, err
	}
	return record, nil
}

type preparedExecutionLog struct {
	record                         ExecutionLogRecord
	versionIDs                     []string
	expectedOwnerID                string
	expectedProjectID              string
	expectedFrameIncarnationID     string
	expectedRootFrameIncarnationID string
	expectedCount                  int
	filesWritten                   any
	filesRead                      any
	detection                      any
}

func (s *Store) prepareExecutionLog(input SaveExecutionLogInput) (preparedExecutionLog, error) {
	record := input.Record
	record.ID, record.FrameID = strings.TrimSpace(record.ID), strings.TrimSpace(record.FrameID)
	record.KernelID, record.CondaEnv = strings.TrimSpace(record.KernelID), strings.TrimSpace(record.CondaEnv)
	record.Language, record.ExitStatus = strings.TrimSpace(record.Language), strings.TrimSpace(record.ExitStatus)
	input.ExpectedOwnerID = strings.TrimSpace(input.ExpectedOwnerID)
	input.ExpectedProjectID = strings.TrimSpace(input.ExpectedProjectID)
	input.ExpectedFrameIncarnationID = strings.TrimSpace(input.ExpectedFrameIncarnationID)
	input.ExpectedRootFrameIncarnationID = strings.TrimSpace(input.ExpectedRootFrameIncarnationID)
	if record.ID == "" || record.FrameID == "" || record.KernelID == "" || record.CondaEnv == "" || record.Language == "" || record.ExitStatus == "" {
		return preparedExecutionLog{}, errors.New("execution log identity, frame, kernel, environment, language, and exit status are required")
	}
	if record.CellIndex < 0 {
		return preparedExecutionLog{}, errors.New("execution log cell index must be non-negative")
	}
	expectedAuthority := []string{
		input.ExpectedOwnerID, input.ExpectedProjectID,
		input.ExpectedFrameIncarnationID, input.ExpectedRootFrameIncarnationID,
	}
	expectedCount := 0
	for _, value := range expectedAuthority {
		if value != "" {
			expectedCount++
		}
	}
	if expectedCount != 0 && expectedCount != len(expectedAuthority) {
		return preparedExecutionLog{}, errors.New("execution log owner, project, frame incarnation, and root incarnation must be supplied together")
	}
	if record.Origin == "" {
		record.Origin = "agent"
	}
	if record.ExecutedAt.IsZero() {
		record.ExecutedAt = s.now().UTC()
	} else {
		record.ExecutedAt = record.ExecutedAt.UTC()
	}
	filesWritten, err := encodeExecutionJSON(record.FilesWritten)
	if err != nil {
		return preparedExecutionLog{}, fmt.Errorf("encode execution files_written: %w", err)
	}
	filesRead, err := encodeExecutionJSON(record.FilesRead)
	if err != nil {
		return preparedExecutionLog{}, fmt.Errorf("encode execution files_read: %w", err)
	}
	detection, err := encodeExecutionJSON(record.Detection)
	if err != nil {
		return preparedExecutionLog{}, fmt.Errorf("encode execution detection: %w", err)
	}
	return preparedExecutionLog{
		record: record, versionIDs: append([]string(nil), input.VersionIDs...),
		expectedOwnerID: input.ExpectedOwnerID, expectedProjectID: input.ExpectedProjectID,
		expectedFrameIncarnationID:     input.ExpectedFrameIncarnationID,
		expectedRootFrameIncarnationID: input.ExpectedRootFrameIncarnationID,
		expectedCount:                  expectedCount, filesWritten: filesWritten, filesRead: filesRead, detection: detection,
	}, nil
}

func saveExecutionLogTx(ctx context.Context, tx workspaceTransaction, prepared preparedExecutionLog) (ExecutionLogRecord, error) {
	record := prepared.record
	var agentName, frameProjectID, frameIncarnationID, rootFrameIncarnationID, frameOwnerID string
	if err := tx.QueryRowContext(ctx, `
		SELECT frame.agent_name,frame.project_id,frame.incarnation_id,root.incarnation_id,project.user_id
		FROM frames frame JOIN projects project ON project.id=frame.project_id
		JOIN frames root ON root.id=frame.root_frame_id AND root.project_id=frame.project_id
		WHERE frame.id=?`, record.FrameID).Scan(
		&agentName, &frameProjectID, &frameIncarnationID, &rootFrameIncarnationID, &frameOwnerID,
	); err != nil {
		return ExecutionLogRecord{}, fmt.Errorf("look up execution frame: %w", err)
	}
	if prepared.expectedCount > 0 && (frameOwnerID != prepared.expectedOwnerID || frameProjectID != prepared.expectedProjectID ||
		frameIncarnationID != prepared.expectedFrameIncarnationID ||
		rootFrameIncarnationID != prepared.expectedRootFrameIncarnationID) {
		return ExecutionLogRecord{}, errors.New("execution frame authority changed before the result was persisted")
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO execution_log (
			id, frame_id, cell_index, kernel_id, kernel_kind, conda_env, language, source,
			stdout, stderr, exit_status, origin, created_at, files_written, files_read, error_lineno, detection
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID, record.FrameID, record.CellIndex, record.KernelID, nullableString(record.KernelKind),
		record.CondaEnv, record.Language, record.Source, nullableString(record.Stdout), nullableString(record.Stderr),
		record.ExitStatus, record.Origin, record.ExecutedAt, prepared.filesWritten, prepared.filesRead, record.ErrorLine, prepared.detection,
	); err != nil {
		return ExecutionLogRecord{}, fmt.Errorf("insert execution log: %w", err)
	}
	for _, versionID := range prepared.versionIDs {
		versionID = strings.TrimSpace(versionID)
		if versionID == "" {
			continue
		}
		var found, versionProjectID, contentType string
		if err := tx.QueryRowContext(ctx, `
			SELECT version.id, artifact.project_id, artifact.kind
			FROM artifact_versions version
			JOIN artifacts artifact ON artifact.id = version.artifact_id
			WHERE version.id = ?`, versionID).Scan(&found, &versionProjectID, &contentType); err != nil {
			return ExecutionLogRecord{}, fmt.Errorf("look up execution artifact version %s: %w", versionID, err)
		}
		if versionProjectID != frameProjectID {
			return ExecutionLogRecord{}, fmt.Errorf("execution artifact version %s belongs to another project", versionID)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO artifact_version_execution_links (version_id, execution_log_id, cell_index)
			VALUES (?, ?, ?)`, versionID, record.ID, record.CellIndex); err != nil {
			return ExecutionLogRecord{}, fmt.Errorf("link execution artifact version: %w", err)
		}
		cellSources, err := mergeExecutionLogCellSource(ctx, tx, versionID, record.CellIndex)
		if err != nil {
			return ExecutionLogRecord{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO artifact_version_provenance (version_id, frame_id, content_type, cell_sources)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(version_id) DO UPDATE SET
				frame_id = COALESCE(artifact_version_provenance.frame_id, excluded.frame_id),
				cell_sources = excluded.cell_sources`,
			versionID, record.FrameID, contentType, cellSources); err != nil {
			return ExecutionLogRecord{}, fmt.Errorf("update execution artifact cell sources: %w", err)
		}
	}
	record.AgentName = agentName
	return record, nil
}

func mergeExecutionLogCellSource(ctx context.Context, tx workspaceTransaction, versionID string, cellIndex int) (string, error) {
	var encoded sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT cell_sources FROM artifact_version_provenance WHERE version_id = ?`, versionID).Scan(&encoded)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("read execution artifact cell sources: %w", err)
	}
	sources := make([]any, 0)
	if encoded.Valid && strings.TrimSpace(encoded.String) != "" {
		if err := json.Unmarshal([]byte(encoded.String), &sources); err != nil {
			return "", fmt.Errorf("decode execution artifact cell sources: %w", err)
		}
	}
	for _, source := range sources {
		mapping, ok := source.(map[string]any)
		if !ok {
			continue
		}
		value, ok := mapping["cell_index"].(float64)
		if ok && value == float64(cellIndex) {
			return encoded.String, nil
		}
	}
	sources = append(sources, map[string]any{"kind": "cell", "cell_index": cellIndex})
	result, err := json.Marshal(sources)
	if err != nil {
		return "", fmt.Errorf("encode execution artifact cell sources: %w", err)
	}
	return string(result), nil
}

func scanExecutionLog(row rowScanner) (ExecutionLogRecord, error) {
	var record ExecutionLogRecord
	var filesWritten, filesRead, detection sql.NullString
	var errorLine sql.NullInt64
	if err := row.Scan(
		&record.ID, &record.FrameID, &record.CellIndex, &record.KernelID, &record.KernelKind,
		&record.CondaEnv, &record.Language, &record.Source, &record.Stdout, &record.Stderr,
		&record.ExitStatus, &record.Origin, &record.ExecutedAt, &filesWritten, &filesRead,
		&errorLine, &detection, &record.AgentName, &record.DelegateName,
	); err != nil {
		return ExecutionLogRecord{}, fmt.Errorf("scan execution log: %w", err)
	}
	var err error
	if record.FilesWritten, err = decodeExecutionJSON(filesWritten); err != nil {
		return ExecutionLogRecord{}, fmt.Errorf("decode execution files_written: %w", err)
	}
	if record.FilesRead, err = decodeExecutionJSON(filesRead); err != nil {
		return ExecutionLogRecord{}, fmt.Errorf("decode execution files_read: %w", err)
	}
	if record.Detection, err = decodeExecutionJSON(detection); err != nil {
		return ExecutionLogRecord{}, fmt.Errorf("decode execution detection: %w", err)
	}
	if errorLine.Valid {
		value := int(errorLine.Int64)
		record.ErrorLine = &value
	}
	return record, nil
}

func decodeExecutionJSON(value sql.NullString) (any, error) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil, nil
	}
	var decoded any
	if err := json.Unmarshal([]byte(value.String), &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func encodeExecutionJSON(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return string(raw), nil
}
