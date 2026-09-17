package transcript

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// FramePendingInputState is a primary-database control-plane snapshot used to
// decide whether a blocked frame dispatch has newer user input to consume.
type FramePendingInputState struct {
	InputRevision              int64
	ConsumedInputRevision      int64
	LatestClaimedInputRevision int64
	HasRunnerAttempt           bool
}

func (r *Repository) GetFramePendingInputState(
	ctx context.Context,
	ownerID, sessionID string,
) (FramePendingInputState, bool, error) {
	if r == nil || r.db == nil {
		return FramePendingInputState{}, false, ErrSchemaUnavailable
	}
	ownerID = strings.TrimSpace(ownerID)
	sessionID = strings.TrimSpace(sessionID)
	if ownerID == "" || sessionID == "" {
		return FramePendingInputState{}, false, errors.New("owner and session are required")
	}
	var state FramePendingInputState
	var latestClaimed sql.NullInt64
	err := r.db.QueryRowContext(ctx, `
		SELECT stream.input_revision,stream.consumed_input_revision,
			(SELECT attempt.claimed_input_revision
			 FROM transcript_runner_attempts attempt
			 WHERE attempt.stream_uid=stream.stream_uid
			 ORDER BY attempt.attempt DESC LIMIT 1)
		FROM transcript_frame_authority authority
		JOIN transcript_streams stream ON stream.stream_uid=authority.active_stream_uid
		WHERE authority.owner_id=? AND authority.session_id=? AND stream.kind='frame_ref'
			AND stream.owner_id=authority.owner_id AND stream.session_id=authority.session_id
			AND stream.epoch=authority.active_epoch`, ownerID, sessionID,
	).Scan(&state.InputRevision, &state.ConsumedInputRevision, &latestClaimed)
	if errors.Is(err, sql.ErrNoRows) {
		return FramePendingInputState{}, false, nil
	}
	if err != nil {
		return FramePendingInputState{}, false, schemaError(err)
	}
	if latestClaimed.Valid {
		state.HasRunnerAttempt = true
		state.LatestClaimedInputRevision = latestClaimed.Int64
	}
	return state, true, nil
}
