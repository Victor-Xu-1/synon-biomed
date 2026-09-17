package transcript

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const (
	DefaultRunnerTaskEvidenceEvents = 10000
	DefaultRunnerTaskEvidenceBytes  = int64(128 << 20)
	MaxRunnerTaskEvidenceEvents     = 100000
	MaxRunnerTaskEvidenceBytes      = int64(256 << 20)
)

var ErrRunnerTaskEvidenceWindowTooLarge = errors.New("runner task evidence window is too large")

// ListRunnerTaskEvidence returns completed tool checkpoints for one exact
// admitted input revision on the active branch. It is intentionally separate
// from provider replay limits: these immutable server receipts feed
// deterministic completion gates and are never projected into model context.
type ListRunnerTaskEvidenceInput struct {
	StreamUID            string
	OwnerID              string
	ClaimedInputRevision int64
	MaxEvents            int
	MaxBytes             int64
}

func (r *Repository) ListRunnerTaskEvidence(
	ctx context.Context,
	input ListRunnerTaskEvidenceInput,
) ([]RunnerReplayEvent, error) {
	if r == nil || r.db == nil {
		return nil, ErrSchemaUnavailable
	}
	input.StreamUID = strings.TrimSpace(input.StreamUID)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	if input.MaxEvents == 0 {
		input.MaxEvents = DefaultRunnerTaskEvidenceEvents
	}
	if input.MaxBytes == 0 {
		input.MaxBytes = DefaultRunnerTaskEvidenceBytes
	}
	if input.StreamUID == "" || input.OwnerID == "" || input.ClaimedInputRevision <= 0 ||
		input.MaxEvents <= 0 || input.MaxEvents > MaxRunnerTaskEvidenceEvents ||
		input.MaxBytes <= 0 || input.MaxBytes > MaxRunnerTaskEvidenceBytes {
		return nil, errors.New("stream, owner, input revision, and bounded task evidence limits are required")
	}

	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, schemaError(err)
	}
	defer func() { _ = tx.Rollback() }()

	var ownerID, kind, frameID string
	if err := tx.QueryRowContext(ctx,
		`SELECT owner_id,kind,frame_id FROM transcript_streams WHERE stream_uid=?`, input.StreamUID,
	).Scan(&ownerID, &kind, &frameID); err != nil {
		return nil, schemaError(err)
	}
	if ownerID != input.OwnerID {
		return nil, ErrOwnerMismatch
	}

	const from = `
		FROM transcript_branch_state state
		JOIN transcript_branch_events membership
			ON membership.stream_uid=state.stream_uid AND membership.branch_id=state.active_branch_id
		JOIN transcript_events event
			ON event.stream_uid=membership.stream_uid AND event.event_id=membership.event_id
		JOIN transcript_runner_attempts attempt
			ON attempt.stream_uid=event.stream_uid AND attempt.attempt=event.runner_attempt
		WHERE state.stream_uid=? AND attempt.claimed_input_revision=?
			AND event.event_type='runner_checkpoint' AND event.source='payload'
			AND json_extract(event.payload_json,'$.status')='completed'
			AND json_extract(event.payload_json,'$.toolPhase')='completed'`

	var eventCount int
	var payloadBytes int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*),COALESCE(SUM(length(CAST(event.payload_json AS BLOB))),0)`+from,
		input.StreamUID, input.ClaimedInputRevision,
	).Scan(&eventCount, &payloadBytes); err != nil {
		return nil, schemaError(err)
	}
	if eventCount > input.MaxEvents || payloadBytes > input.MaxBytes {
		return nil, fmt.Errorf("%w: events=%d/%d bytes=%d/%d",
			ErrRunnerTaskEvidenceWindowTooLarge, eventCount, input.MaxEvents, payloadBytes, input.MaxBytes)
	}

	rows, err := tx.QueryContext(ctx, `SELECT
			event.stream_uid,event.event_id,event.publication_seq,event.client_message_id,event.event_type,event.source,
			event.runner_attempt,event.payload_json,event.frame_event_id,event.created_at,
			NULL,NULL,NULL `+from+` ORDER BY event.publication_seq`, input.StreamUID, input.ClaimedInputRevision)
	if err != nil {
		return nil, schemaError(err)
	}
	result := make([]RunnerReplayEvent, 0, eventCount)
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
