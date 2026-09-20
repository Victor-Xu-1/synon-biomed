package transcript

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const (
	// MaxRunnerReplayProjection is the largest message or checkpoint seed
	// accepted by ListRunnerReplay and by runner configuration validation.
	MaxRunnerReplayProjection = 1000

	DefaultRunnerReplayExpandedEvents = 4096
	DefaultRunnerReplayExpandedBytes  = 32 << 20
	MaxRunnerReplayExpandedEvents     = 100000
	MaxRunnerReplayExpandedBytes      = 256 << 20
)

const runnerReplaySeedCTE = `compact_boundary(event_id) AS (
	SELECT COALESCE(MAX(event.event_id),0)
	FROM transcript_branch_state state
	JOIN transcript_branch_events membership
		ON membership.stream_uid=state.stream_uid AND membership.branch_id=state.active_branch_id
	JOIN transcript_events event
		ON event.stream_uid=membership.stream_uid AND event.event_id=membership.event_id
	WHERE state.stream_uid=? AND event.event_type='runner_checkpoint'
		AND json_extract(event.payload_json,'$.toolPhase')='auto_compact'
),
compact_input(event_id) AS (
	SELECT event.event_id FROM transcript_branch_state state
	JOIN transcript_branch_events membership
		ON membership.stream_uid=state.stream_uid AND membership.branch_id=state.active_branch_id
	JOIN transcript_events event
		ON event.stream_uid=membership.stream_uid AND event.event_id=membership.event_id
	WHERE state.stream_uid=? AND event.event_type IN (
		'user_message','history_user_message'
	) AND event.event_id<=(SELECT event_id FROM compact_boundary)
	ORDER BY event.publication_seq DESC LIMIT 1
),
current_task_input(event_id) AS (
	SELECT event.event_id FROM transcript_branch_state state
	JOIN transcript_branch_events membership
		ON membership.stream_uid=state.stream_uid AND membership.branch_id=state.active_branch_id
	JOIN transcript_events event
		ON event.stream_uid=membership.stream_uid AND event.event_id=membership.event_id
	WHERE state.stream_uid=? AND event.event_type IN ('user_message','history_user_message')
		AND COALESCE(json_extract(event.payload_json,'$.messageOrigin'),'task_intent')!='input_response'
	ORDER BY event.publication_seq DESC LIMIT 1
),
seed(event_id) AS (
	SELECT event_id FROM compact_input
	UNION
	SELECT event_id FROM (
		SELECT event.event_id FROM transcript_branch_state state
		JOIN transcript_branch_events membership
			ON membership.stream_uid=state.stream_uid AND membership.branch_id=state.active_branch_id
		JOIN transcript_events event
			ON event.stream_uid=membership.stream_uid AND event.event_id=membership.event_id
		WHERE state.stream_uid=? AND event.event_type IN (
			'user_message','user_input_response','assistant_message',
			'history_user_message','history_assistant_message','history_system_message'
		) AND event.event_id>(SELECT event_id FROM compact_boundary)
		ORDER BY event.publication_seq DESC LIMIT ?
	)
	UNION
	SELECT event_id FROM (
		SELECT event.event_id FROM transcript_branch_state state
		JOIN transcript_branch_events membership
			ON membership.stream_uid=state.stream_uid AND membership.branch_id=state.active_branch_id
		JOIN transcript_events event
			ON event.stream_uid=membership.stream_uid AND event.event_id=membership.event_id
		WHERE state.stream_uid=? AND event.event_type='runner_checkpoint'
			AND event.event_id>=(SELECT event_id FROM current_task_input)
			AND json_extract(event.payload_json,'$.status')='completed'
			AND json_extract(event.payload_json,'$.toolPhase')='completed'
			AND lower(json_extract(event.payload_json,'$.toolName'))='skill'
		ORDER BY event.publication_seq DESC LIMIT 32
	)
	UNION
	SELECT event_id FROM (
		SELECT event.event_id FROM transcript_branch_state state
		JOIN transcript_branch_events membership
			ON membership.stream_uid=state.stream_uid AND membership.branch_id=state.active_branch_id
		JOIN transcript_events event
			ON event.stream_uid=membership.stream_uid AND event.event_id=membership.event_id
		WHERE state.stream_uid=? AND event.event_type='runner_checkpoint'
			AND event.event_id>(SELECT event_id FROM compact_boundary)
			AND json_type(event.payload_json,'$.correction_condition_chunk') IS NULL
		ORDER BY event.publication_seq DESC LIMIT ?
	)
	UNION
	SELECT event_id FROM (
		SELECT event.event_id FROM transcript_branch_state state
		JOIN transcript_branch_events membership
			ON membership.stream_uid=state.stream_uid AND membership.branch_id=state.active_branch_id
		JOIN transcript_events event
			ON event.stream_uid=membership.stream_uid AND event.event_id=membership.event_id
		WHERE state.stream_uid=? AND event.event_type='runner_checkpoint'
			AND json_extract(event.payload_json,'$.toolPhase')='auto_compact'
		ORDER BY event.publication_seq DESC LIMIT 1
	)
	UNION
	SELECT event.event_id FROM transcript_branch_state state
	JOIN transcript_branch_events membership
		ON membership.stream_uid=state.stream_uid AND membership.branch_id=state.active_branch_id
	JOIN transcript_events event
		ON event.stream_uid=membership.stream_uid AND event.event_id=membership.event_id
	WHERE state.stream_uid=? AND event.event_id=? AND event.event_type='runner_checkpoint'
)`

const runnerReplayLegacySelectionCTE = `WITH ` + runnerReplaySeedCTE + `,
selected(event_id) AS (SELECT event_id FROM seed)`

const runnerReplayBatchSelectionCTE = `WITH ` + runnerReplaySeedCTE + `,
active_batches(batch_id,source_event_id,call_count,state,branch_id) AS (
	SELECT batch.batch_id,batch.source_event_id,batch.call_count,batch.state,batch.branch_id
	FROM transcript_tool_call_batches batch
	JOIN transcript_branch_state branch ON branch.stream_uid=batch.stream_uid
		AND branch.active_branch_id=batch.branch_id AND branch.generation=batch.branch_generation
	JOIN transcript_branch_events source ON source.stream_uid=batch.stream_uid
		AND source.branch_id=batch.branch_id AND source.event_id=batch.source_event_id
	WHERE batch.stream_uid=?
),
hit_batches(batch_id,source_event_id,call_count,state,branch_id) AS (
	SELECT batch.batch_id,batch.source_event_id,batch.call_count,batch.state,batch.branch_id
	FROM active_batches batch
	WHERE batch.source_event_id IN (SELECT event_id FROM seed)
		OR EXISTS (
			SELECT 1 FROM transcript_tool_call_items item
			JOIN seed ON seed.event_id IN (item.started_event_id,item.waiting_event_id,item.terminal_event_id)
			WHERE item.batch_id=batch.batch_id
		)
),
selected(event_id) AS (
	SELECT event_id FROM seed
	UNION
	SELECT source_event_id FROM hit_batches
	UNION
	SELECT item.terminal_event_id
	FROM hit_batches batch
	JOIN transcript_tool_call_items item ON item.batch_id=batch.batch_id
	JOIN transcript_branch_events membership ON membership.stream_uid=item.stream_uid
		AND membership.branch_id=batch.branch_id AND membership.event_id=item.terminal_event_id
	WHERE item.terminal_event_id IS NOT NULL
	UNION
	SELECT item.waiting_event_id
	FROM hit_batches batch
	JOIN transcript_tool_call_items item ON item.batch_id=batch.batch_id
	JOIN transcript_branch_events membership ON membership.stream_uid=item.stream_uid
		AND membership.branch_id=batch.branch_id AND membership.event_id=item.waiting_event_id
	WHERE item.waiting_event_id IS NOT NULL
)`

// ListRunnerReplay projects messages and checkpoints with independent seed
// limits. If the v40 tool-batch authority is present, any batch touched by the
// seed expands atomically to its assistant root and every terminal tool result.
// An open batch or an expanded window beyond either hard budget fails closed;
// no partial provider transaction is returned.
func (r *Repository) ListRunnerReplay(ctx context.Context, input ListRunnerReplayInput) ([]RunnerReplayEvent, error) {
	if r == nil || r.db == nil {
		return nil, ErrSchemaUnavailable
	}
	input.StreamUID = strings.TrimSpace(input.StreamUID)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	if err := normalizeRunnerReplayInput(&input); err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, schemaError(err)
	}
	defer func() { _ = tx.Rollback() }()

	var ownerID, kind, frameID string
	if err := tx.QueryRowContext(ctx, `SELECT owner_id,kind,frame_id FROM transcript_streams WHERE stream_uid=?`, input.StreamUID).
		Scan(&ownerID, &kind, &frameID); err != nil {
		return nil, schemaError(err)
	}
	if ownerID != input.OwnerID {
		return nil, ErrOwnerMismatch
	}
	if input.RequiredCheckpointEventID > 0 {
		var count int
		err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcript_branch_state state
			JOIN transcript_branch_events membership ON membership.stream_uid=state.stream_uid AND membership.branch_id=state.active_branch_id
			JOIN transcript_events event ON event.stream_uid=membership.stream_uid AND event.event_id=membership.event_id
			WHERE state.stream_uid=? AND event.event_id=? AND event.event_type='runner_checkpoint'`, input.StreamUID, input.RequiredCheckpointEventID).Scan(&count)
		if err != nil {
			return nil, schemaError(err)
		}
		if count != 1 {
			return nil, ErrCheckpointUnavailable
		}
	}

	hasBatchAuthority, err := runnerReplayHasToolBatchAuthority(ctx, tx)
	if err != nil {
		return nil, schemaError(err)
	}
	selectionCTE := runnerReplayLegacySelectionCTE
	selectionArgs := runnerReplaySeedArgs(input)
	if hasBatchAuthority {
		parkedBatchID, err := allowRunnerReplayParkedToolBatch(ctx, tx, input.StreamUID)
		if err != nil {
			return nil, err
		}
		selectionCTE = runnerReplayBatchSelectionCTE
		selectionArgs = append(selectionArgs, input.StreamUID)
		if err := validateClosedRunnerReplayToolBatches(ctx, tx, selectionCTE, selectionArgs, parkedBatchID); err != nil {
			return nil, err
		}
	}

	var expandedEvents int
	var expandedBytes int64
	budgetArgs := append(append([]any(nil), selectionArgs...), input.StreamUID)
	err = tx.QueryRowContext(ctx, selectionCTE+`
		SELECT COUNT(*),COALESCE(SUM(
			CASE WHEN event.source='frame_ref'
				THEN length(CAST(COALESCE(frame.payload,'') AS BLOB))
				ELSE length(CAST(event.payload_json AS BLOB)) END
		),0)
		FROM transcript_events event
		JOIN selected ON selected.event_id=event.event_id
		LEFT JOIN frame_events frame ON event.source='frame_ref' AND frame.id=event.frame_event_id
		WHERE event.stream_uid=?`, budgetArgs...).Scan(&expandedEvents, &expandedBytes)
	if err != nil {
		return nil, schemaError(err)
	}
	if expandedEvents > input.MaxExpandedEvents || expandedBytes > input.MaxExpandedBytes {
		return nil, fmt.Errorf("%w: events=%d/%d bytes=%d/%d", ErrProviderReplayWindowTooLarge,
			expandedEvents, input.MaxExpandedEvents, expandedBytes, input.MaxExpandedBytes)
	}

	queryArgs := append(append([]any(nil), selectionArgs...), input.StreamUID)
	rows, err := tx.QueryContext(ctx, selectionCTE+`
		SELECT event.stream_uid,event.event_id,event.publication_seq,event.client_message_id,event.event_type,event.source,
			event.runner_attempt,event.payload_json,event.frame_event_id,event.created_at,
			frame.frame_id,frame.event_type,frame.payload
		FROM transcript_events event
		JOIN selected ON selected.event_id=event.event_id
		LEFT JOIN frame_events frame ON event.source='frame_ref' AND frame.id=event.frame_event_id
		WHERE event.stream_uid=? ORDER BY event.publication_seq`, queryArgs...)
	if err != nil {
		return nil, schemaError(err)
	}
	result := []RunnerReplayEvent{}
	for rows.Next() {
		event, payload, err := scanProjectedEvent(rows, StreamKind(kind), frameID)
		if err != nil {
			_ = rows.Close()
			return nil, schemaError(err)
		}
		result = append(result, RunnerReplayEvent{Event: event, ResolvedPayloadJSON: payload})
	}
	if err := rows.Close(); err != nil {
		return nil, schemaError(err)
	}
	if err := rows.Err(); err != nil {
		return nil, schemaError(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, schemaError(err)
	}
	return result, nil
}

func normalizeRunnerReplayInput(input *ListRunnerReplayInput) error {
	if input == nil || input.StreamUID == "" || input.OwnerID == "" || input.MessageLimit <= 0 ||
		input.MessageLimit > MaxRunnerReplayProjection || input.CheckpointLimit < 0 ||
		input.CheckpointLimit > MaxRunnerReplayProjection || input.RequiredCheckpointEventID < 0 || input.MaxExpandedEvents < 0 || input.MaxExpandedBytes < 0 ||
		input.MaxExpandedEvents > MaxRunnerReplayExpandedEvents || input.MaxExpandedBytes > MaxRunnerReplayExpandedBytes {
		return errors.New("stream, owner, bounded replay seeds, and bounded closure budgets are required")
	}
	if input.MaxExpandedEvents == 0 {
		input.MaxExpandedEvents = DefaultRunnerReplayExpandedEvents
	}
	if input.MaxExpandedBytes == 0 {
		input.MaxExpandedBytes = DefaultRunnerReplayExpandedBytes
	}
	return nil
}

func runnerReplaySeedArgs(input ListRunnerReplayInput) []any {
	return []any{
		input.StreamUID,
		input.StreamUID,
		input.StreamUID,
		input.StreamUID, input.MessageLimit,
		input.StreamUID,
		input.StreamUID, input.CheckpointLimit,
		input.StreamUID,
		input.StreamUID, input.RequiredCheckpointEventID,
	}
}

func runnerReplayHasToolBatchAuthority(ctx context.Context, tx *sql.Tx) (bool, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
		WHERE type='table' AND name IN ('transcript_tool_call_batches','transcript_tool_call_items')`).Scan(&count); err != nil {
		return false, err
	}
	if count == 0 {
		return false, nil
	}
	if count != 2 {
		return false, errors.New("transcript tool call batch authority is incomplete")
	}
	return true, nil
}

type runnerReplayOpenToolBatch struct {
	batchID          string
	state            string
	nextOrdinal      int64
	callCount        int64
	waitingOrdinal   sql.NullInt64
	admittedRevision int64
	sourceAttempt    int64
	sourceEventID    int64
	runnerID         string
	runnerAttempt    sql.NullInt64
	reasonCode       string
}

func (b runnerReplayOpenToolBatch) Error() error {
	return fmt.Errorf(
		"%w: batch=%s state=%s next_ordinal=%d/%d waiting_ordinal=%v admitted_input_revision=%d source_runner_attempt=%d source_event_id=%d runner_id=%q runner_attempt=%v reason_code=%q",
		ErrOpenToolBatch, b.batchID, b.state, b.nextOrdinal, b.callCount, b.waitingOrdinal,
		b.admittedRevision, b.sourceAttempt, b.sourceEventID, b.runnerID, b.runnerAttempt, b.reasonCode,
	)
}

func findOpenRunnerReplayToolBatch(
	ctx context.Context,
	tx *sql.Tx,
	streamUID string,
) (runnerReplayOpenToolBatch, bool, error) {
	var batch runnerReplayOpenToolBatch
	err := tx.QueryRowContext(ctx, `SELECT batch.batch_id,batch.state,batch.next_ordinal,batch.call_count,
			batch.waiting_ordinal,batch.admitted_input_revision,batch.source_runner_attempt,batch.source_event_id,
			COALESCE(batch.runner_id,''),batch.runner_attempt,COALESCE(batch.reason_code,'')
		FROM transcript_tool_call_batches batch
		JOIN transcript_branch_state branch ON branch.stream_uid=batch.stream_uid
			AND branch.active_branch_id=batch.branch_id AND branch.generation=batch.branch_generation
		JOIN transcript_branch_events source ON source.stream_uid=batch.stream_uid
			AND source.branch_id=batch.branch_id AND source.event_id=batch.source_event_id
		WHERE batch.stream_uid=? AND batch.state IN ('ready','running','waiting')
		ORDER BY batch.source_event_id LIMIT 1`, streamUID).Scan(
		&batch.batchID, &batch.state, &batch.nextOrdinal, &batch.callCount, &batch.waitingOrdinal,
		&batch.admittedRevision, &batch.sourceAttempt, &batch.sourceEventID,
		&batch.runnerID, &batch.runnerAttempt, &batch.reasonCode,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return runnerReplayOpenToolBatch{}, false, nil
	}
	if err != nil {
		return runnerReplayOpenToolBatch{}, false, schemaError(err)
	}
	return batch, true, nil
}

// allowRunnerReplayParkedToolBatch permits exactly one open batch when it is a
// durable pause: the batch is waiting, only its last item is waiting, that item
// carries a committed start and waiting event, and every earlier item is
// terminal. This is the legitimate awaiting_user_response / approval-parked
// state; anything else stays fail-closed so a torn or crashed tool batch is
// never projected as if it were complete.
func allowRunnerReplayParkedToolBatch(
	ctx context.Context,
	tx *sql.Tx,
	streamUID string,
) (string, error) {
	open, found, err := findOpenRunnerReplayToolBatch(ctx, tx, streamUID)
	if err != nil || !found {
		return "", err
	}
	if open.state != "waiting" || !open.waitingOrdinal.Valid ||
		open.waitingOrdinal.Int64 != open.nextOrdinal || open.callCount != open.nextOrdinal+1 {
		return "", open.Error()
	}
	var parked int
	var unsettled int
	err = tx.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM transcript_tool_call_items park
			WHERE park.batch_id=? AND park.ordinal=? AND park.state='waiting'
				AND park.started_event_id IS NOT NULL AND park.waiting_event_id IS NOT NULL
				AND park.terminal_event_id IS NULL),
		(SELECT COUNT(*) FROM transcript_tool_call_items earlier
			WHERE earlier.batch_id=? AND earlier.ordinal<?
				AND (earlier.terminal_event_id IS NULL OR earlier.state NOT IN
					('completed','failed','blocked','cancelled','outcome_unknown')))`,
		open.batchID, open.nextOrdinal, open.batchID, open.nextOrdinal,
	).Scan(&parked, &unsettled)
	if err != nil {
		return "", schemaError(err)
	}
	if parked != 1 || unsettled != 0 {
		return "", open.Error()
	}
	return open.batchID, nil
}

func validateClosedRunnerReplayToolBatches(
	ctx context.Context,
	tx *sql.Tx,
	selectionCTE string,
	selectionArgs []any,
	excludeParkedBatchID string,
) error {
	whereClause := ""
	if excludeParkedBatchID != "" {
		whereClause = "WHERE batch.batch_id<>?"
		selectionArgs = append(append([]any(nil), selectionArgs...), excludeParkedBatchID)
	}
	var batchID string
	err := tx.QueryRowContext(ctx, selectionCTE+`
		SELECT batch.batch_id
		FROM hit_batches batch
		LEFT JOIN transcript_tool_call_items item ON item.batch_id=batch.batch_id
		LEFT JOIN transcript_branch_events terminal ON terminal.stream_uid=item.stream_uid
			AND terminal.branch_id=batch.branch_id AND terminal.event_id=item.terminal_event_id
		`+whereClause+`
		GROUP BY batch.batch_id,batch.call_count,batch.state
		HAVING batch.state NOT IN ('settled','cancelled','outcome_unknown') OR COUNT(item.ordinal)!=batch.call_count OR
			COUNT(item.terminal_event_id)!=batch.call_count OR COUNT(terminal.event_id)!=batch.call_count OR
			MIN(item.ordinal)!=0 OR MAX(item.ordinal)!=batch.call_count-1
		LIMIT 1`, selectionArgs...).Scan(&batchID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return schemaError(err)
	}
	return fmt.Errorf("%w: batch=%s lacks a complete terminal closure", ErrOpenToolBatch, batchID)
}
