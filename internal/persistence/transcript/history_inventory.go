package transcript

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

type FrameHistoryAuthorityCensus struct {
	Total         int
	NoStream      int
	Legacy        int
	PayloadActive int
	Conflict      int
}

type ReconcileNoStreamFrameHistoriesInput struct {
	Limit          int
	AfterOwnerID   string
	AfterSessionID string
}

type NoStreamFrameHistoryReconciliation struct {
	Scanned        int
	PayloadCreated int
	Blocked        int
	Deferred       int
	Truncated      bool
	NextOwnerID    string
	NextSessionID  string
	Census         FrameHistoryAuthorityCensus
}

type noStreamFrameHistoryCandidate struct {
	ownerID, sessionID, incarnationID, projectID, rootFrameID string
}

type noStreamFrameHistoryOutcome int

const (
	noStreamFrameHistorySkipped noStreamFrameHistoryOutcome = iota
	noStreamFrameHistoryPayloadCreated
	noStreamFrameHistoryBlocked
	noStreamFrameHistoryDeferred
)

const frameHistoryCensusQuery = `WITH web_frames AS (
	SELECT project.user_id AS owner_id,frame.id AS session_id,frame.incarnation_id,
		frame.project_id,frame.root_frame_id
	FROM frames frame JOIN projects project ON project.id=frame.project_id
), stream_sessions AS (
	SELECT session_id,COUNT(*) AS stream_count FROM transcript_streams
	WHERE session_id<>'' GROUP BY session_id
), facts AS (
	SELECT web.owner_id AS expected_owner,web.session_id AS expected_session,
		web.incarnation_id,web.project_id AS expected_project,web.root_frame_id AS expected_root,
		COALESCE(session_stream.stream_count,0) AS stream_count,
		authority.active_stream_uid,authority.active_epoch AS authority_epoch,
		authority.authority_generation,authority.read_authority,authority.write_authority,
		authority.activation_id,authority.genesis_id,
		active.owner_id AS active_owner,active.session_id AS active_session,active.epoch AS stream_epoch,
		active.kind AS active_kind,active.project_id AS active_project,
		active.root_frame_id AS active_root,active.frame_id AS active_frame,
		genesis.genesis_id AS valid_genesis,activation.activation_id AS valid_activation
	FROM web_frames web
	LEFT JOIN stream_sessions session_stream ON session_stream.session_id=web.session_id
	LEFT JOIN transcript_frame_authority authority
		ON authority.owner_id=web.owner_id AND authority.session_id=web.session_id
	LEFT JOIN transcript_streams active ON active.stream_uid=authority.active_stream_uid
	LEFT JOIN transcript_payload_genesis_receipts genesis
		ON genesis.genesis_id=authority.genesis_id AND genesis.stream_uid=authority.active_stream_uid
		AND genesis.owner_id=web.owner_id AND genesis.session_id=web.session_id
		AND genesis.epoch=authority.active_epoch
		AND genesis.authority_generation=authority.authority_generation AND genesis.status='active'
	LEFT JOIN transcript_history_activation_receipts activation
		ON activation.activation_id=authority.activation_id
		AND activation.target_stream_uid=authority.active_stream_uid
		AND activation.owner_id=web.owner_id AND activation.session_id=web.session_id
		AND activation.target_epoch=authority.active_epoch
		AND activation.authority_generation=authority.authority_generation AND activation.status='active'
)
SELECT COUNT(*),
	COALESCE(SUM(length(incarnation_id)>0 AND stream_count=0 AND active_stream_uid IS NULL),0),
	COALESCE(SUM(active_stream_uid IS NOT NULL AND read_authority='legacy_mixed_v1'
		AND write_authority='legacy_frame_ref_v1' AND active_owner=expected_owner
		AND active_session=expected_session AND authority_epoch=stream_epoch
		AND active_kind='frame_ref' AND active_project=expected_project
		AND active_root=expected_root AND active_frame=expected_session
		AND length(incarnation_id)>0 AND authority_generation=1
		AND activation_id IS NULL AND genesis_id IS NULL),0),
	COALESCE(SUM(active_stream_uid IS NOT NULL AND read_authority='transcript_payload_v1'
		AND write_authority='transcript_payload_v1' AND active_owner=expected_owner
		AND active_session=expected_session AND authority_epoch=stream_epoch
		AND active_kind='frame_ref' AND active_project=expected_project
		AND active_root=expected_root AND active_frame=expected_session AND length(incarnation_id)>0
		AND ((activation_id IS NULL AND genesis_id IS NOT NULL AND valid_genesis IS NOT NULL)
			OR (activation_id IS NOT NULL AND genesis_id IS NULL AND valid_activation IS NOT NULL))),0)
FROM facts`

// FrameHistoryCensus classifies every persisted agent Frame by its current
// message authority. The buckets are mutually exclusive and cover the stable
// Web-conversation denominator at one SQLite snapshot.
func (r *Repository) FrameHistoryCensus(ctx context.Context) (FrameHistoryAuthorityCensus, error) {
	var census FrameHistoryAuthorityCensus
	if r == nil || r.db == nil {
		return census, ErrSchemaUnavailable
	}
	err := r.db.QueryRowContext(ctx, frameHistoryCensusQuery).
		Scan(&census.Total, &census.NoStream, &census.Legacy, &census.PayloadActive)
	if err != nil {
		return FrameHistoryAuthorityCensus{}, schemaError(err)
	}
	census.Conflict = census.Total - census.NoStream - census.Legacy - census.PayloadActive
	if census.Total < 0 || census.NoStream < 0 || census.Legacy < 0 || census.PayloadActive < 0 || census.Conflict < 0 {
		return FrameHistoryAuthorityCensus{}, ErrEventConflict
	}
	return census, nil
}

// ReconcileNoStreamFrameHistories adopts only settled Frames whose complete
// legacy message history can be represented without guessing. Empty/plain
// histories enter payload authority directly. Rich, cursor-bound, branched,
// artifact-linked, live, or unsupported histories remain explicitly visible
// in the census and are never silently emptied or forced through an
// incompatible legacy classifier.
func (r *Repository) ReconcileNoStreamFrameHistories(
	ctx context.Context,
	input ReconcileNoStreamFrameHistoriesInput,
) (NoStreamFrameHistoryReconciliation, error) {
	var report NoStreamFrameHistoryReconciliation
	if r == nil || r.db == nil {
		return report, ErrSchemaUnavailable
	}
	input.AfterOwnerID = strings.TrimSpace(input.AfterOwnerID)
	input.AfterSessionID = strings.TrimSpace(input.AfterSessionID)
	if input.Limit <= 0 || input.Limit > 1000 ||
		((input.AfterOwnerID == "") != (input.AfterSessionID == "")) {
		return report, errors.New("bounded no-stream history reconciliation and complete cursor are required")
	}
	candidates, truncated, err := r.listNoStreamFrameHistoryCandidates(
		ctx, input.AfterOwnerID, input.AfterSessionID, input.Limit,
	)
	if err != nil {
		return report, err
	}
	report.Truncated = truncated
	if truncated && len(candidates) > 0 {
		report.NextOwnerID = candidates[len(candidates)-1].ownerID
		report.NextSessionID = candidates[len(candidates)-1].sessionID
	}
	for _, candidate := range candidates {
		if err := context.Cause(ctx); err != nil {
			return report, err
		}
		report.Scanned++
		outcome, err := r.reconcileNoStreamFrameHistory(ctx, candidate)
		if err != nil {
			return report, err
		}
		switch outcome {
		case noStreamFrameHistoryPayloadCreated:
			report.PayloadCreated++
		case noStreamFrameHistoryBlocked:
			report.Blocked++
		case noStreamFrameHistoryDeferred:
			report.Deferred++
		}
	}
	if !report.Truncated {
		report.Census, err = r.FrameHistoryCensus(ctx)
	}
	return report, err
}

func (r *Repository) listNoStreamFrameHistoryCandidates(
	ctx context.Context,
	afterOwnerID, afterSessionID string,
	limit int,
) ([]noStreamFrameHistoryCandidate, bool, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT project.user_id,frame.id,frame.incarnation_id,
			frame.project_id,frame.root_frame_id
		FROM frames frame JOIN projects project ON project.id=frame.project_id
		WHERE (project.user_id>? OR (project.user_id=? AND frame.id>?))
			AND NOT EXISTS(SELECT 1 FROM transcript_streams stream WHERE stream.session_id=frame.id)
			AND NOT EXISTS(SELECT 1 FROM transcript_frame_authority authority WHERE authority.session_id=frame.id)
		ORDER BY project.user_id,frame.id LIMIT ?`, afterOwnerID, afterOwnerID, afterSessionID, limit+1)
	if err != nil {
		return nil, false, schemaError(err)
	}
	defer rows.Close()
	result := make([]noStreamFrameHistoryCandidate, 0, limit+1)
	for rows.Next() {
		var candidate noStreamFrameHistoryCandidate
		if err := rows.Scan(&candidate.ownerID, &candidate.sessionID, &candidate.incarnationID,
			&candidate.projectID, &candidate.rootFrameID); err != nil {
			return nil, false, schemaError(err)
		}
		if strings.TrimSpace(candidate.ownerID) == "" || strings.TrimSpace(candidate.sessionID) == "" ||
			strings.TrimSpace(candidate.incarnationID) == "" || strings.TrimSpace(candidate.projectID) == "" ||
			strings.TrimSpace(candidate.rootFrameID) == "" {
			return nil, false, ErrEventConflict
		}
		result = append(result, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, false, schemaError(err)
	}
	truncated := len(result) > limit
	if truncated {
		result = result[:limit]
	}
	return result, truncated, nil
}

func (r *Repository) reconcileNoStreamFrameHistory(
	ctx context.Context,
	candidate noStreamFrameHistoryCandidate,
) (noStreamFrameHistoryOutcome, error) {
	outcome := noStreamFrameHistorySkipped
	err := r.withImmediate(ctx, func(conn *sql.Conn) error {
		var ownerID, incarnationID, projectID, rootFrameID, status string
		if err := conn.QueryRowContext(ctx, `SELECT project.user_id,frame.incarnation_id,
			frame.project_id,frame.root_frame_id,frame.status FROM frames frame
			JOIN projects project ON project.id=frame.project_id WHERE frame.id=?`, candidate.sessionID).
			Scan(&ownerID, &incarnationID, &projectID, &rootFrameID, &status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		if ownerID != candidate.ownerID || incarnationID != candidate.incarnationID ||
			projectID != candidate.projectID || rootFrameID != candidate.rootFrameID {
			return ErrEventConflict
		}
		var existing int
		if err := conn.QueryRowContext(ctx, `SELECT
			(SELECT COUNT(*) FROM transcript_streams WHERE session_id=?)+
			(SELECT COUNT(*) FROM transcript_frame_authority WHERE session_id=?)`, candidate.sessionID, candidate.sessionID).
			Scan(&existing); err != nil {
			return err
		}
		if existing != 0 {
			return nil
		}
		var liveQueue, liveClaim, liveOutbox, unsafeProjection int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM queued_user_messages
			WHERE frame_id=? AND resolved_at IS NULL`, candidate.sessionID).Scan(&liveQueue); err != nil {
			return err
		}
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM frame_execution_claims
			WHERE frame_id=? AND state IN ('claimed','parked')`, candidate.sessionID).Scan(&liveClaim); err != nil {
			return err
		}
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspace_outbox
			WHERE aggregate_type='frame' AND aggregate_id=? AND status IN ('pending','inflight')`, candidate.sessionID).
			Scan(&liveOutbox); err != nil {
			return err
		}
		if !canonicalCloneSourceSettled(status) || liveQueue != 0 || liveClaim != 0 || liveOutbox != 0 {
			outcome = noStreamFrameHistoryBlocked
			return nil
		}
		if err := conn.QueryRowContext(ctx, `SELECT
			(SELECT COUNT(*) FROM frame_branch_archives WHERE frame_id=?)+
			(SELECT COUNT(*) FROM compaction_archives WHERE frame_id=?)+
			(SELECT COUNT(*) FROM frame_read_cursors WHERE root_frame_id=?)+
			(SELECT COUNT(*) FROM artifact_runtime_metadata WHERE frame_id=? OR root_frame_id=?)+
			(SELECT COUNT(*) FROM artifact_version_provenance WHERE frame_id=?)+
			(SELECT COUNT(*) FROM workspace_outbox
				WHERE aggregate_type='frame' AND aggregate_id=? AND status='dead_letter')`,
			candidate.sessionID, candidate.sessionID, candidate.rootFrameID, candidate.sessionID, candidate.rootFrameID,
			candidate.sessionID, candidate.sessionID).Scan(&unsafeProjection); err != nil {
			return err
		}
		if unsafeProjection != 0 {
			outcome = noStreamFrameHistoryDeferred
			return nil
		}
		plain, err := inspectNoStreamFrameMessagesConn(ctx, conn, candidate.sessionID)
		if err != nil {
			return err
		}
		input := CreateStreamInput{
			UID: "frame:" + candidate.sessionID, OwnerID: candidate.ownerID,
			ExternalID: candidate.sessionID, SessionID: candidate.sessionID, Kind: StreamKindFrameRef,
			ProjectID: candidate.projectID, RootFrameID: candidate.rootFrameID, FrameID: candidate.sessionID, Epoch: 1,
		}
		if plain {
			if _, err := createStreamConn(ctx, conn, input, r.now().UTC()); err != nil {
				return err
			}
			outcome = noStreamFrameHistoryPayloadCreated
			return nil
		}
		created, err := bootstrapTypedFrameHistoryConn(ctx, conn, candidate, r.now().UTC())
		if err != nil {
			return err
		}
		if created {
			outcome = noStreamFrameHistoryPayloadCreated
			return nil
		}
		outcome = noStreamFrameHistoryDeferred
		return nil
	})
	return outcome, schemaError(err)
}

func inspectNoStreamFrameMessagesConn(
	ctx context.Context,
	conn *sql.Conn,
	frameID string,
) (plain bool, err error) {
	rows, err := conn.QueryContext(ctx, `SELECT id,event_type,payload FROM frame_events WHERE frame_id=?
		AND event_type IN ('message','user_message','assistant_message','system_message','tool_use','tool_result','ask_user_answer')
		ORDER BY sequence,id`, frameID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	plain = true
	for rows.Next() {
		var id, eventType string
		var payload []byte
		if err := rows.Scan(&id, &eventType, &payload); err != nil {
			return false, err
		}
		id = strings.TrimSpace(id)
		if id == "" || len(payload) == 0 || !json.Valid(payload) {
			return false, ErrEventConflict
		}
		if _, err := payloadGenesisFrameMessage(eventType, payload); err != nil {
			plain = false
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return plain, nil
}
