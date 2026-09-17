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

const traceFrameSelectColumns = `
	SELECT frame.id, frame.project_id, COALESCE(frame.parent_frame_id, ''),
		frame.root_frame_id, frame.root_sequence, frame.agent_name, frame.status,
		frame.conversation_type, frame.name, frame.created_at, frame.updated_at,
		COALESCE(metadata.delegate_name, ''), metadata.input_data, metadata.output_data,
		COALESCE(metadata.context_data, '{}'), metadata.completed_at,
		COALESCE(metadata.model, ''), COALESCE(metadata.effort, ''),
		metadata.input_tokens, metadata.output_tokens, metadata.cache_read_tokens,
		metadata.cache_write_tokens, metadata.total_cost,
		metadata.aux_input_tokens, metadata.aux_output_tokens,
		metadata.aux_cache_read_tokens, metadata.aux_cache_write_tokens, metadata.aux_cost,
		COALESCE(metadata.task_summary, ''), COALESCE(metadata.status_description, ''),
		metadata.mentioned_artifact_ids, metadata.specialists_used, COALESCE(metadata.is_hidden, 0),
		frame.incarnation_id,
		(SELECT COUNT(*) FROM frame_events event WHERE event.frame_id = frame.id AND ` + compatibilityMessagePredicate + `)
	FROM frames AS frame
	LEFT JOIN frame_runtime_metadata AS metadata ON metadata.frame_id = frame.id`

type FrameTraceSnapshot struct {
	Root       Frame   `json:"root"`
	Frames     []Frame `json:"frames"`
	AllFrames  []Frame `json:"-"`
	Cursor     int64   `json:"cursor"`
	FrameCount int     `json:"frameCount"`
}

// GetFrameTraceSnapshot returns either a complete root tree source or frames
// changed after a durable global trace revision. FrameCount always reflects
// the current complete tree so a client can detect deletion gaps and refetch.
func (s *Store) GetFrameTraceSnapshot(rootFrameID string, afterRevision *int64) (FrameTraceSnapshot, error) {
	if s == nil || s.db == nil {
		return FrameTraceSnapshot{}, errors.New("workspace store is closed")
	}
	rootFrameID = strings.TrimSpace(rootFrameID)
	if rootFrameID == "" {
		return FrameTraceSnapshot{}, errors.New("root frame id is required")
	}
	if afterRevision != nil && *afterRevision < 0 {
		return FrameTraceSnapshot{}, errors.New("trace cursor must be non-negative")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return FrameTraceSnapshot{}, fmt.Errorf("begin trace snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	root, found, err := queryTraceFrame(ctx, tx, rootFrameID)
	if err != nil {
		return FrameTraceSnapshot{}, err
	}
	if !found || root.ID != root.RootFrameID {
		return FrameTraceSnapshot{}, fmt.Errorf("root frame %q does not exist", rootFrameID)
	}
	var snapshot FrameTraceSnapshot
	snapshot.Root = root
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(trace.revision), 0)
		FROM frame_trace_revisions AS trace
		JOIN frames AS frame ON frame.id = trace.frame_id
		WHERE frame.root_frame_id = ?`, rootFrameID).Scan(&snapshot.Cursor); err != nil {
		return FrameTraceSnapshot{}, fmt.Errorf("read trace revision: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM frames WHERE root_frame_id = ?`, rootFrameID).Scan(&snapshot.FrameCount); err != nil {
		return FrameTraceSnapshot{}, fmt.Errorf("count trace frames: %w", err)
	}
	query := traceFrameSelectColumns + `
		WHERE frame.root_frame_id = ?
		ORDER BY frame.root_sequence, frame.id`
	args := []any{rootFrameID}
	if afterRevision != nil {
		query = traceFrameSelectColumns + `
			JOIN frame_trace_revisions AS trace ON trace.frame_id = frame.id
			WHERE frame.root_frame_id = ? AND trace.revision > ?
			ORDER BY frame.root_sequence, frame.id`
		args = append(args, *afterRevision)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return FrameTraceSnapshot{}, fmt.Errorf("query trace frames: %w", err)
	}
	defer rows.Close()
	snapshot.Frames = make([]Frame, 0)
	for rows.Next() {
		frame, err := scanTraceFrame(rows)
		if err != nil {
			return FrameTraceSnapshot{}, err
		}
		snapshot.Frames = append(snapshot.Frames, frame)
	}
	if err := rows.Err(); err != nil {
		return FrameTraceSnapshot{}, fmt.Errorf("iterate trace frames: %w", err)
	}
	if err := rows.Close(); err != nil {
		return FrameTraceSnapshot{}, fmt.Errorf("close trace frames: %w", err)
	}
	if afterRevision != nil {
		allRows, err := tx.QueryContext(ctx, traceFrameSelectColumns+`
			WHERE frame.root_frame_id = ?
			ORDER BY frame.root_sequence, frame.id`, rootFrameID)
		if err != nil {
			return FrameTraceSnapshot{}, fmt.Errorf("query complete trace for delta usage: %w", err)
		}
		snapshot.AllFrames = make([]Frame, 0, snapshot.FrameCount)
		for allRows.Next() {
			frame, err := scanTraceFrame(allRows)
			if err != nil {
				_ = allRows.Close()
				return FrameTraceSnapshot{}, err
			}
			snapshot.AllFrames = append(snapshot.AllFrames, frame)
		}
		if err := allRows.Err(); err != nil {
			_ = allRows.Close()
			return FrameTraceSnapshot{}, fmt.Errorf("iterate complete trace for delta usage: %w", err)
		}
		if err := allRows.Close(); err != nil {
			return FrameTraceSnapshot{}, fmt.Errorf("close complete trace for delta usage: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return FrameTraceSnapshot{}, fmt.Errorf("commit trace snapshot: %w", err)
	}
	return snapshot, nil
}

type traceFrameQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func queryTraceFrame(ctx context.Context, query traceFrameQuery, frameID string) (Frame, bool, error) {
	frame, err := scanTraceFrame(query.QueryRowContext(ctx, traceFrameSelectColumns+` WHERE frame.id = ?`, frameID))
	if errors.Is(err, sql.ErrNoRows) {
		return Frame{}, false, nil
	}
	if err != nil {
		return Frame{}, false, fmt.Errorf("get trace root frame: %w", err)
	}
	return frame, true, nil
}

type traceFrameScanner interface {
	Scan(...any) error
}

func scanTraceFrame(scanner traceFrameScanner) (Frame, error) {
	var frame Frame
	var rawInput, rawOutput, rawMentioned, rawSpecialists sql.NullString
	var rawContext string
	var completedAt sql.NullTime
	var inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens sql.NullInt64
	var auxInputTokens, auxOutputTokens, auxCacheReadTokens, auxCacheWriteTokens sql.NullInt64
	var totalCost, auxCost sql.NullFloat64
	err := scanner.Scan(
		&frame.ID, &frame.ProjectID, &frame.ParentFrameID, &frame.RootFrameID,
		&frame.RootSequence, &frame.AgentName, &frame.Status, &frame.ConversationType,
		&frame.Name, &frame.CreatedAt, &frame.UpdatedAt, &frame.DelegateName,
		&rawInput, &rawOutput, &rawContext, &completedAt, &frame.Model, &frame.Effort,
		&inputTokens, &outputTokens, &cacheReadTokens, &cacheWriteTokens, &totalCost,
		&auxInputTokens, &auxOutputTokens, &auxCacheReadTokens, &auxCacheWriteTokens, &auxCost,
		&frame.TaskSummary, &frame.StatusDescription, &rawMentioned, &rawSpecialists,
		&frame.IsHidden, &frame.IncarnationID, &frame.MessageCount,
	)
	if err != nil {
		return Frame{}, err
	}
	if err := decodeFrameTraceJSON(rawInput, &frame.InputData, "input data"); err != nil {
		return Frame{}, err
	}
	if err := decodeFrameTraceJSON(rawOutput, &frame.OutputData, "output data"); err != nil {
		return Frame{}, err
	}
	if err := decodeFrameTraceContext(rawContext, &frame); err != nil {
		return Frame{}, err
	}
	if rawMentioned.Valid {
		mentionedArtifactIDs, err := decodeMentionedArtifactIDs(rawMentioned.String)
		if err != nil {
			return Frame{}, fmt.Errorf("decode trace frame mentioned artifact ids: %w", err)
		}
		frame.MentionedArtifactIDs = mentionedArtifactIDs
	}
	if err := decodeFrameTraceJSON(rawSpecialists, &frame.SpecialistsUsed, "specialists used"); err != nil {
		return Frame{}, err
	}
	frame.CompletedAt = nullTimePointer(completedAt)
	frame.InputTokens, frame.OutputTokens = nullInt64Pointer(inputTokens), nullInt64Pointer(outputTokens)
	frame.CacheReadTokens, frame.CacheWriteTokens = nullInt64Pointer(cacheReadTokens), nullInt64Pointer(cacheWriteTokens)
	frame.AuxInputTokens, frame.AuxOutputTokens = nullInt64Pointer(auxInputTokens), nullInt64Pointer(auxOutputTokens)
	frame.AuxCacheReadTokens, frame.AuxCacheWriteTokens = nullInt64Pointer(auxCacheReadTokens), nullInt64Pointer(auxCacheWriteTokens)
	frame.TotalCost, frame.AuxCost = nullFloat64Pointer(totalCost), nullFloat64Pointer(auxCost)
	return frame, nil
}

func decodeFrameTraceJSON[T any](raw sql.NullString, target *T, label string) error {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(raw.String), target); err != nil {
		return fmt.Errorf("decode trace frame %s: %w", label, err)
	}
	return nil
}

func nullInt64Pointer(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	copy := value.Int64
	return &copy
}

func nullFloat64Pointer(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	copy := value.Float64
	return &copy
}

func nullTimePointer(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	copy := value.Time.UTC()
	return &copy
}

func decodeFrameTraceContext(raw string, frame *Frame) error {
	if err := json.Unmarshal([]byte(raw), &frame.ContextData); err != nil {
		return fmt.Errorf("decode trace frame context data: %w", err)
	}
	if frame.ContextData == nil {
		frame.ContextData = map[string]any{}
	}
	return nil
}
