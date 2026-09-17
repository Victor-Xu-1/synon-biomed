package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	transcriptstore "synon-go/internal/persistence/transcript"
)

type FrameRuntimeMetadata struct {
	FrameID      string         `json:"frameId"`
	DelegateName string         `json:"delegateName,omitempty"`
	InputData    map[string]any `json:"inputData,omitempty"`
	ContextData  map[string]any `json:"contextData"`
	TaskSummary  string         `json:"taskSummary,omitempty"`
	IsHidden     bool           `json:"isHidden"`
}

// SetClaimedFrameRuntimeMetadata validates the current runner and writes its
// frame state in one transaction. Immutable source receipts alone do not grant
// an interrupted or replaced runner permission to update live metadata.
func (s *Store) SetClaimedFrameRuntimeMetadata(ctx context.Context, claim transcriptstore.RunnerClaim, metadata FrameRuntimeMetadata) (FrameRuntimeMetadata, error) {
	if ctx == nil {
		return FrameRuntimeMetadata{}, errors.New("frame metadata context is required")
	}
	repository, err := s.TranscriptRepository(ctx)
	if err != nil {
		return FrameRuntimeMetadata{}, err
	}
	var stored FrameRuntimeMetadata
	err = repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		stream, err := tx.ValidateLiveRunnerClaim(ctx, claim)
		if err != nil {
			return err
		}
		if metadata.FrameID != "" && metadata.FrameID != stream.FrameID {
			return errors.New("frame metadata target differs from runner authority")
		}
		stored, err = s.setFrameRuntimeMetadataTransaction(ctx, tx, stream.FrameID, metadata)
		return err
	})
	return stored, err
}

// AdvanceClaimedFrameRuntimeEventCursorTx records a monotonic event cursor in
// the same transaction as its authoritative transcript checkpoint. It is used
// by runtime control planes that must distinguish newly completed work from an
// already-consumed receipt after provider compaction or process restart.
func (s *Store) AdvanceClaimedFrameRuntimeEventCursorTx(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	claim transcriptstore.RunnerClaim,
	cursor string,
	eventID int64,
) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	if ctx == nil || tx == nil {
		return errors.New("frame runtime event cursor requires a transaction context")
	}
	cursor = strings.TrimSpace(cursor)
	if cursor == "" || eventID <= 0 {
		return errors.New("frame runtime event cursor identity and event id are required")
	}
	stream, err := tx.ValidateLiveRunnerClaim(ctx, claim)
	if err != nil {
		return err
	}
	var rawContext string
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(context_data, '{}')
		FROM frame_runtime_metadata WHERE frame_id = ?`, stream.FrameID).Scan(&rawContext); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read frame runtime event cursor: %w", err)
		}
		rawContext = "{}"
	}
	contextData := map[string]any{}
	if err := json.Unmarshal([]byte(rawContext), &contextData); err != nil {
		return fmt.Errorf("decode frame runtime event cursor: %w", err)
	}
	if frameRuntimeEventCursorValue(contextData[cursor]) >= eventID {
		return nil
	}
	contextData[cursor] = eventID
	encoded, err := json.Marshal(contextData)
	if err != nil {
		return fmt.Errorf("encode frame runtime event cursor: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO frame_runtime_metadata (frame_id, context_data)
		VALUES (?, ?)
		ON CONFLICT(frame_id) DO UPDATE SET context_data = excluded.context_data`,
		stream.FrameID, string(encoded)); err != nil {
		return fmt.Errorf("advance frame runtime event cursor: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE frames SET updated_at = ? WHERE id = ?`, s.now().UTC(), stream.FrameID); err != nil {
		return fmt.Errorf("advance frame runtime event cursor trace: %w", err)
	}
	return nil
}

func frameRuntimeEventCursorValue(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case float64:
		return int64(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed
	default:
		return 0
	}
}

// SetFrameRuntimeMetadata persists runtime-owned frame fields without exposing
// a test-only HTTP mutation surface. Updating the parent frame also advances
// the durable trace cursor through the existing frames update trigger.
func (s *Store) SetFrameRuntimeMetadata(frameID string, metadata FrameRuntimeMetadata) (FrameRuntimeMetadata, error) {
	if s == nil || s.db == nil {
		return FrameRuntimeMetadata{}, errors.New("workspace store is closed")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FrameRuntimeMetadata{}, fmt.Errorf("begin frame metadata transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := s.setFrameRuntimeMetadataTransaction(ctx, tx, frameID, metadata); err != nil {
		return FrameRuntimeMetadata{}, err
	}
	if err := tx.Commit(); err != nil {
		return FrameRuntimeMetadata{}, fmt.Errorf("commit frame runtime metadata: %w", err)
	}
	stored, found, err := s.GetFrameRuntimeMetadata(frameID)
	if err != nil {
		return FrameRuntimeMetadata{}, err
	}
	if !found {
		return FrameRuntimeMetadata{}, fmt.Errorf("frame %q metadata disappeared after update", frameID)
	}
	return stored, nil
}

func (s *Store) setFrameRuntimeMetadataTransaction(
	ctx context.Context, tx workspaceTransaction, frameID string, metadata FrameRuntimeMetadata,
) (FrameRuntimeMetadata, error) {
	if s == nil || s.db == nil {
		return FrameRuntimeMetadata{}, errors.New("workspace store is closed")
	}
	if tx == nil {
		return FrameRuntimeMetadata{}, errors.New("frame metadata transaction is required")
	}
	frameID = strings.TrimSpace(frameID)
	if frameID == "" {
		return FrameRuntimeMetadata{}, errors.New("frame id is required")
	}
	contextData := metadata.ContextData
	if contextData == nil {
		contextData = map[string]any{}
	}
	rawContext, err := json.Marshal(contextData)
	if err != nil {
		return FrameRuntimeMetadata{}, fmt.Errorf("marshal frame context data: %w", err)
	}
	var foundID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM frames WHERE id = ?`, frameID).Scan(&foundID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return FrameRuntimeMetadata{}, fmt.Errorf("frame %q does not exist", frameID)
		}
		return FrameRuntimeMetadata{}, fmt.Errorf("look up frame metadata owner: %w", err)
	}
	delegateName := strings.TrimSpace(metadata.DelegateName)
	taskSummary := strings.TrimSpace(metadata.TaskSummary)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO frame_runtime_metadata (frame_id, delegate_name, context_data, task_summary)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(frame_id) DO UPDATE SET
			delegate_name = excluded.delegate_name,
			context_data = excluded.context_data,
			task_summary = excluded.task_summary`,
		frameID, nullableString(delegateName), string(rawContext), nullableString(taskSummary)); err != nil {
		return FrameRuntimeMetadata{}, fmt.Errorf("upsert frame runtime metadata: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE frames SET updated_at = ? WHERE id = ?`, s.now().UTC(), frameID); err != nil {
		return FrameRuntimeMetadata{}, fmt.Errorf("advance frame metadata trace: %w", err)
	}
	metadata.FrameID = frameID
	metadata.DelegateName = delegateName
	metadata.ContextData = contextData
	metadata.TaskSummary = taskSummary
	return metadata, nil
}

func (s *Store) GetFrameRuntimeMetadata(frameID string) (FrameRuntimeMetadata, bool, error) {
	return s.GetFrameRuntimeMetadataWithContext(context.Background(), frameID)
}

// GetFrameRuntimeMetadataWithContext is the cancellable read-side variant
// used while resolving a session's model authority.
func (s *Store) GetFrameRuntimeMetadataWithContext(ctx context.Context, frameID string) (FrameRuntimeMetadata, bool, error) {
	db := s.readDatabase()
	if db == nil {
		return FrameRuntimeMetadata{}, false, errors.New("workspace store is closed")
	}
	if ctx == nil {
		return FrameRuntimeMetadata{}, false, errors.New("frame runtime metadata context is required")
	}
	var metadata FrameRuntimeMetadata
	var rawInput sql.NullString
	var rawContext string
	err := db.QueryRowContext(ctx, `
		SELECT frame_id, COALESCE(delegate_name, ''), input_data,
			COALESCE(context_data, '{}'), COALESCE(task_summary, ''), COALESCE(is_hidden, 0)
		FROM frame_runtime_metadata WHERE frame_id = ?`, strings.TrimSpace(frameID)).Scan(
		&metadata.FrameID, &metadata.DelegateName, &rawInput, &rawContext, &metadata.TaskSummary, &metadata.IsHidden,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return FrameRuntimeMetadata{}, false, nil
	}
	if err != nil {
		return FrameRuntimeMetadata{}, false, fmt.Errorf("get frame runtime metadata: %w", err)
	}
	if err := json.Unmarshal([]byte(rawContext), &metadata.ContextData); err != nil {
		return FrameRuntimeMetadata{}, false, fmt.Errorf("decode frame context data: %w", err)
	}
	if rawInput.Valid {
		if err := json.Unmarshal([]byte(rawInput.String), &metadata.InputData); err != nil {
			return FrameRuntimeMetadata{}, false, fmt.Errorf("decode frame input data: %w", err)
		}
	}
	return metadata, true, nil
}

func (s *Store) SetFrameSubmissionMetadata(frameID string, inputData map[string]any, hidden bool) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	frameID = strings.TrimSpace(frameID)
	if frameID == "" || inputData == nil {
		return errors.New("frame id and input data are required")
	}
	rawInput, err := json.Marshal(inputData)
	if err != nil {
		return fmt.Errorf("marshal frame input data: %w", err)
	}
	result, err := s.db.ExecContext(context.Background(), `
		INSERT INTO frame_runtime_metadata (frame_id, input_data, is_hidden)
		VALUES (?, ?, ?)
		ON CONFLICT(frame_id) DO UPDATE SET
			input_data = excluded.input_data,
			is_hidden = excluded.is_hidden`, frameID, string(rawInput), hidden)
	if err != nil {
		return fmt.Errorf("set frame submission metadata: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return fmt.Errorf("inspect frame submission metadata update: %w", err)
		}
		return fmt.Errorf("frame %q does not exist", frameID)
	}
	return nil
}
