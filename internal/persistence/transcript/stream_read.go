package transcript

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
)

func (r *Repository) GetStream(ctx context.Context, streamUID, ownerID string) (Stream, error) {
	db := r.readDatabase()
	if db == nil {
		return Stream{}, ErrSchemaUnavailable
	}
	streamUID = strings.TrimSpace(streamUID)
	ownerID = strings.TrimSpace(ownerID)
	var stream Stream
	var kind string
	err := db.QueryRowContext(ctx, `
		SELECT stream_uid, owner_id, external_id, session_id, kind, project_id, root_frame_id, frame_id, epoch,
			input_revision, consumed_input_revision, next_event_id, next_publication_seq, next_checkpoint_sequence,
			created_at, updated_at
		FROM transcript_streams WHERE stream_uid=?`, streamUID,
	).Scan(&stream.UID, &stream.OwnerID, &stream.ExternalID, &stream.SessionID, &kind, &stream.ProjectID, &stream.RootFrameID, &stream.FrameID, &stream.Epoch,
		&stream.InputRevision, &stream.ConsumedInputRevision, &stream.NextEventID, &stream.NextPublication, &stream.NextCheckpoint,
		&stream.CreatedAt, &stream.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Stream{}, sql.ErrNoRows
	}
	if err != nil {
		return Stream{}, schemaError(err)
	}
	if ownerID == "" || stream.OwnerID != ownerID {
		return Stream{}, ErrOwnerMismatch
	}
	stream.Kind = StreamKind(kind)
	return stream, nil
}

func (r *Repository) GetFrameStreamBySession(ctx context.Context, ownerID, sessionID string) (Stream, bool, error) {
	db := r.readDatabase()
	if db == nil {
		return Stream{}, false, ErrSchemaUnavailable
	}
	ownerID = strings.TrimSpace(ownerID)
	sessionID = strings.TrimSpace(sessionID)
	if ownerID == "" || sessionID == "" {
		return Stream{}, false, errors.New("owner and session are required")
	}
	var streamUID string
	var activeEpoch int64
	err := db.QueryRowContext(ctx, `
		SELECT authority.active_stream_uid,authority.active_epoch
		FROM transcript_frame_authority authority
		JOIN transcript_streams stream ON stream.stream_uid=authority.active_stream_uid
		WHERE authority.owner_id=? AND authority.session_id=? AND stream.kind='frame_ref'
			AND stream.owner_id=authority.owner_id AND stream.session_id=authority.session_id
			AND stream.epoch=authority.active_epoch`, ownerID, sessionID).Scan(&streamUID, &activeEpoch)
	if errors.Is(err, sql.ErrNoRows) {
		return Stream{}, false, nil
	}
	if err != nil {
		return Stream{}, false, schemaError(err)
	}
	stream, err := r.GetStream(ctx, streamUID, ownerID)
	if err == nil && stream.Epoch != activeEpoch {
		return Stream{}, false, ErrEventConflict
	}
	return stream, err == nil, err
}

// GetEventByClientMessageID returns the canonical event for a caller-owned
// stream without exposing whether another owner's stream or event exists.
func (r *Repository) GetEventByClientMessageID(
	ctx context.Context,
	streamUID, ownerID, clientMessageID string,
) (Event, bool, error) {
	if r == nil || r.db == nil {
		return Event{}, false, ErrSchemaUnavailable
	}
	streamUID = strings.TrimSpace(streamUID)
	ownerID = strings.TrimSpace(ownerID)
	clientMessageID = strings.TrimSpace(clientMessageID)
	if streamUID == "" || ownerID == "" || clientMessageID == "" {
		return Event{}, false, errors.New("stream, owner, and client message id are required")
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return Event{}, false, schemaError(err)
	}
	defer conn.Close()
	if _, err := getStreamConn(ctx, conn, streamUID, ownerID); err != nil {
		return Event{}, false, schemaError(err)
	}
	event, found, err := findEventByClientID(ctx, conn, streamUID, clientMessageID)
	return event, found, schemaError(err)
}

func (r *Repository) GetFrameAuthorityBySession(ctx context.Context, ownerID, sessionID string) (FrameAuthority, bool, error) {
	db := r.readDatabase()
	if db == nil {
		return FrameAuthority{}, false, ErrSchemaUnavailable
	}
	ownerID = strings.TrimSpace(ownerID)
	sessionID = strings.TrimSpace(sessionID)
	if ownerID == "" || sessionID == "" {
		return FrameAuthority{}, false, errors.New("owner and session are required")
	}
	authority, found, err := getFrameAuthorityBySessionQuery(ctx, db, ownerID, sessionID)
	return authority, found, schemaError(err)
}

// GetFrameAuthorityBySession reads the current authority through the same
// BEGIN IMMEDIATE connection used by a workspace/transcript mutation. Callers
// can therefore select payload versus legacy Frame projection without a
// time-of-check/time-of-use window.
func (tx *ImmediateTransaction) GetFrameAuthorityBySession(
	ctx context.Context, ownerID, sessionID string,
) (FrameAuthority, bool, error) {
	if tx == nil || tx.repository == nil || tx.conn == nil {
		return FrameAuthority{}, false, ErrSchemaUnavailable
	}
	ownerID = strings.TrimSpace(ownerID)
	sessionID = strings.TrimSpace(sessionID)
	if ownerID == "" || sessionID == "" {
		return FrameAuthority{}, false, errors.New("owner and session are required")
	}
	authority, found, err := getFrameAuthorityBySessionQuery(ctx, tx.conn, ownerID, sessionID)
	return authority, found, schemaError(err)
}

func getFrameAuthorityBySessionQuery(
	ctx context.Context, query transcriptQueryRower, ownerID, sessionID string,
) (FrameAuthority, bool, error) {
	var authority FrameAuthority
	err := query.QueryRowContext(ctx, `SELECT owner_id,session_id,active_stream_uid,active_epoch,
		authority_generation,read_authority,write_authority,activation_id,genesis_id,updated_at
		FROM transcript_frame_authority WHERE owner_id=? AND session_id=?`, ownerID, sessionID).Scan(
		&authority.OwnerID, &authority.SessionID, &authority.ActiveStreamUID, &authority.ActiveEpoch,
		&authority.AuthorityGeneration, &authority.ReadAuthority, &authority.WriteAuthority,
		&authority.ActivationID, &authority.GenesisID, &authority.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return FrameAuthority{}, false, nil
	}
	if err != nil {
		return FrameAuthority{}, false, err
	}
	if authority.ActiveStreamUID == "" || authority.ActiveEpoch <= 0 || authority.AuthorityGeneration <= 0 ||
		!authority.CanonicalProjectionReadable() {
		return FrameAuthority{}, false, ErrEventConflict
	}
	return authority, true, nil
}

func (r *Repository) ListActiveFrameRealtimeRebases(
	ctx context.Context, ownerID, sessionID string, afterSequence int64, limit int,
) ([]FrameRealtimeRebase, error) {
	if r == nil || r.db == nil {
		return nil, ErrSchemaUnavailable
	}
	ownerID = strings.TrimSpace(ownerID)
	sessionID = strings.TrimSpace(sessionID)
	if ownerID == "" || afterSequence < 0 {
		return nil, errors.New("owner is required")
	}
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := r.db.QueryContext(ctx, `SELECT authority.owner_id,authority.session_id,
		target.project_id,target.root_frame_id,authority.active_stream_uid,authority.active_epoch,
		authority.authority_generation,receipt.activation_id,receipt.active_branch_id,
		receipt.branch_generation,receipt.realtime_high_water,realtime.sequence,receipt.activated_at
		FROM transcript_frame_authority authority
		JOIN transcript_history_activation_receipts receipt ON receipt.activation_id=authority.activation_id
		JOIN transcript_streams target ON target.stream_uid=authority.active_stream_uid
		JOIN realtime_events realtime ON realtime.id='transcript-history-rebase:' || lower(hex(receipt.activation_id))
		WHERE authority.owner_id=? AND (?='' OR authority.session_id=?)
			AND realtime.user_id=authority.owner_id AND realtime.frame_id=authority.session_id
			AND realtime.root_frame_id=target.root_frame_id AND realtime.event_type='conversation.historyRebased'
			AND realtime.sequence>?
			AND authority.read_authority='transcript_payload_v1'
			AND authority.write_authority='transcript_payload_v1'
			AND receipt.status='active'
			AND receipt.target_stream_uid=authority.active_stream_uid
			AND receipt.target_epoch=authority.active_epoch
			AND receipt.authority_generation=authority.authority_generation
			AND target.owner_id=authority.owner_id AND target.session_id=authority.session_id
			AND target.epoch=authority.active_epoch AND target.kind='frame_ref'
		ORDER BY realtime.sequence LIMIT ?`, ownerID, sessionID, sessionID, afterSequence, limit)
	if err != nil {
		return nil, schemaError(err)
	}
	defer rows.Close()
	rebases := make([]FrameRealtimeRebase, 0)
	for rows.Next() {
		var rebase FrameRealtimeRebase
		var activationID []byte
		if err := rows.Scan(
			&rebase.OwnerID, &rebase.SessionID, &rebase.ProjectID, &rebase.RootFrameID,
			&rebase.ActiveStreamUID, &rebase.ActiveEpoch, &rebase.AuthorityGeneration,
			&activationID, &rebase.ActiveBranchID, &rebase.BranchGeneration,
			&rebase.RealtimeHighWater, &rebase.RebaseSequence, &rebase.ActivatedAt,
		); err != nil {
			return nil, schemaError(err)
		}
		if len(activationID) != sha256.Size || rebase.SessionID == "" || rebase.ProjectID == "" ||
			rebase.RootFrameID == "" || rebase.ActiveStreamUID == "" || rebase.ActiveEpoch <= 0 ||
			rebase.AuthorityGeneration <= 1 || rebase.ActiveBranchID == "" ||
			rebase.BranchGeneration <= 0 || rebase.RealtimeHighWater < 0 ||
			rebase.RebaseSequence <= rebase.RealtimeHighWater {
			return nil, ErrEventConflict
		}
		rebase.ActivationSHA256 = hex.EncodeToString(activationID)
		rebases = append(rebases, rebase)
	}
	if err := rows.Err(); err != nil {
		return nil, schemaError(err)
	}
	return rebases, nil
}

func (r *Repository) ResolveActivatedLegacyCursor(
	ctx context.Context,
	ownerID, sessionID, sourceBranchID string,
	sourceGeneration, sourceThroughPublication int64,
	sourceMessageIndex int,
) (ActivatedLegacyCursor, bool, error) {
	if r == nil || r.db == nil {
		return ActivatedLegacyCursor{}, false, ErrSchemaUnavailable
	}
	ownerID = strings.TrimSpace(ownerID)
	sessionID = strings.TrimSpace(sessionID)
	sourceBranchID = strings.TrimSpace(sourceBranchID)
	if ownerID == "" || sessionID == "" || sourceBranchID == "" ||
		sourceGeneration <= 0 || sourceThroughPublication < 0 || sourceMessageIndex < 0 {
		return ActivatedLegacyCursor{}, false, errors.New("valid legacy cursor authority is required")
	}
	rows, err := r.db.QueryContext(ctx, `SELECT target_branch_id,target_message_index,stable_message_id FROM (
		SELECT cursor.target_branch_id,cursor.target_message_index,cursor.stable_message_id
		FROM transcript_frame_authority authority
		JOIN transcript_history_activation_receipts receipt ON receipt.activation_id=authority.activation_id
		JOIN transcript_history_cutover_cursor_map cursor ON cursor.cutover_id=receipt.cutover_id
		WHERE authority.owner_id=? AND authority.session_id=?
			AND receipt.provenance_kind='ask_user_v30'
			AND authority.read_authority='transcript_payload_v1' AND authority.write_authority='transcript_payload_v1'
			AND cursor.source_branch_id=? AND cursor.source_generation=?
			AND cursor.source_through_publication_seq=? AND cursor.source_message_index=?
		UNION ALL
		SELECT cursor.target_branch_id,cursor.target_message_index,cursor.stable_message_id
		FROM transcript_frame_authority authority
		JOIN transcript_history_activation_receipts receipt ON receipt.activation_id=authority.activation_id
		JOIN transcript_history_ordinary_cutover_runs cutover
			ON cutover.ordinary_cutover_id=receipt.ordinary_cutover_id
		JOIN transcript_history_ordinary_cursor_map cursor
			ON cursor.ordinary_cutover_id=cutover.ordinary_cutover_id
		WHERE authority.owner_id=? AND authority.session_id=?
			AND receipt.provenance_kind='ordinary_v33'
			AND authority.read_authority='transcript_payload_v1' AND authority.write_authority='transcript_payload_v1'
			AND cursor.source_branch_id=? AND cursor.source_generation=?
			AND cursor.source_through_publication_seq=? AND cursor.source_message_index=?
	) ORDER BY target_message_index LIMIT 2`,
		ownerID, sessionID, sourceBranchID, sourceGeneration, sourceThroughPublication, sourceMessageIndex,
		ownerID, sessionID, sourceBranchID, sourceGeneration, sourceThroughPublication, sourceMessageIndex)
	if err != nil {
		return ActivatedLegacyCursor{}, false, schemaError(err)
	}
	defer rows.Close()
	results := make([]ActivatedLegacyCursor, 0, 2)
	for rows.Next() {
		var translated ActivatedLegacyCursor
		if err := rows.Scan(&translated.TargetBranchID, &translated.TargetMessageIndex, &translated.StableMessageID); err != nil {
			return ActivatedLegacyCursor{}, false, schemaError(err)
		}
		results = append(results, translated)
	}
	if err := rows.Err(); err != nil {
		return ActivatedLegacyCursor{}, false, schemaError(err)
	}
	if len(results) == 0 {
		return ActivatedLegacyCursor{}, false, nil
	}
	if len(results) > 1 && results[0] != results[1] {
		return ActivatedLegacyCursor{}, false, ErrEventConflict
	}
	translated := results[0]
	if translated.TargetBranchID == "" || translated.TargetMessageIndex < 0 || translated.StableMessageID == "" {
		return ActivatedLegacyCursor{}, false, ErrEventConflict
	}
	return translated, true, nil
}

func (r *Repository) ResolveActivatedStoredReadCursor(
	ctx context.Context, ownerID, sessionID, storedMessageID string, storedMessageIndex int,
) (ActivatedLegacyCursor, bool, error) {
	if r == nil || r.db == nil {
		return ActivatedLegacyCursor{}, false, ErrSchemaUnavailable
	}
	ownerID = strings.TrimSpace(ownerID)
	sessionID = strings.TrimSpace(sessionID)
	storedMessageID = strings.TrimSpace(storedMessageID)
	if ownerID == "" || sessionID == "" || storedMessageIndex < 0 {
		return ActivatedLegacyCursor{}, false, errors.New("valid stored read cursor authority is required")
	}
	rows, err := r.db.QueryContext(ctx, `SELECT target_branch_id,target_message_index,stable_message_id FROM (
		SELECT cursor.target_branch_id,cursor.target_message_index,cursor.stable_message_id
		FROM transcript_frame_authority authority
		JOIN transcript_history_activation_receipts receipt ON receipt.activation_id=authority.activation_id
		JOIN transcript_history_cutover_runs cutover ON cutover.cutover_id=receipt.cutover_id
		JOIN transcript_branch_state state ON state.stream_uid=authority.active_stream_uid
		JOIN transcript_history_cutover_cursor_map cursor ON cursor.cutover_id=receipt.cutover_id
			AND cursor.target_branch_id=state.active_branch_id
		WHERE authority.owner_id=? AND authority.session_id=?
			AND receipt.provenance_kind='ask_user_v30'
			AND authority.read_authority='transcript_payload_v1' AND authority.write_authority='transcript_payload_v1'
			AND state.active_branch_id=receipt.active_branch_id
			AND cursor.source_branch_id=cutover.source_branch_id
			AND cursor.source_generation=cutover.source_generation
			AND cursor.source_through_publication_seq=cutover.source_through_publication_seq
			AND ?<>'' AND cursor.stable_message_id=?
			AND (cursor.target_message_index=? OR cursor.source_message_index=?)
		UNION ALL
		SELECT cursor.target_branch_id,cursor.target_message_index,cursor.stable_message_id
		FROM transcript_frame_authority authority
		JOIN transcript_history_activation_receipts receipt ON receipt.activation_id=authority.activation_id
		JOIN transcript_history_ordinary_cutover_runs cutover
			ON cutover.ordinary_cutover_id=receipt.ordinary_cutover_id
		JOIN transcript_branch_state state ON state.stream_uid=authority.active_stream_uid
		JOIN transcript_history_ordinary_cursor_map cursor
			ON cursor.ordinary_cutover_id=cutover.ordinary_cutover_id
			AND cursor.target_branch_id=state.active_branch_id
		WHERE authority.owner_id=? AND authority.session_id=?
			AND receipt.provenance_kind='ordinary_v33'
			AND authority.read_authority='transcript_payload_v1' AND authority.write_authority='transcript_payload_v1'
			AND state.active_branch_id=receipt.active_branch_id
			AND cursor.source_branch_id=cutover.source_branch_id
			AND ?<>'' AND cursor.stable_message_id=?
			AND (cursor.target_message_index=? OR cursor.source_message_index=?)
	) ORDER BY target_message_index LIMIT 2`,
		ownerID, sessionID, storedMessageID, storedMessageID, storedMessageIndex, storedMessageIndex,
		ownerID, sessionID, storedMessageID, storedMessageID, storedMessageIndex, storedMessageIndex)
	if err != nil {
		return ActivatedLegacyCursor{}, false, schemaError(err)
	}
	defer rows.Close()
	results := make([]ActivatedLegacyCursor, 0, 2)
	for rows.Next() {
		var result ActivatedLegacyCursor
		if err := rows.Scan(&result.TargetBranchID, &result.TargetMessageIndex, &result.StableMessageID); err != nil {
			return ActivatedLegacyCursor{}, false, schemaError(err)
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return ActivatedLegacyCursor{}, false, schemaError(err)
	}
	if len(results) == 0 {
		return ActivatedLegacyCursor{}, false, nil
	}
	if len(results) > 1 && (results[0] != results[1]) {
		return ActivatedLegacyCursor{}, false, ErrEventConflict
	}
	return results[0], true, nil
}

func (r *Repository) GetStreamBySession(ctx context.Context, ownerID, sessionID string) (Stream, bool, error) {
	db := r.readDatabase()
	if db == nil {
		return Stream{}, false, ErrSchemaUnavailable
	}
	ownerID = strings.TrimSpace(ownerID)
	sessionID = strings.TrimSpace(sessionID)
	if ownerID == "" || sessionID == "" {
		return Stream{}, false, errors.New("owner and session are required")
	}
	rows, err := db.QueryContext(ctx, `
		SELECT stream_uid FROM transcript_streams
		WHERE owner_id=? AND session_id=? ORDER BY epoch DESC,created_at DESC LIMIT 2`, ownerID, sessionID)
	if err != nil {
		return Stream{}, false, schemaError(err)
	}
	defer rows.Close()
	streamUIDs := make([]string, 0, 2)
	for rows.Next() {
		var streamUID string
		if err := rows.Scan(&streamUID); err != nil {
			return Stream{}, false, schemaError(err)
		}
		streamUIDs = append(streamUIDs, streamUID)
	}
	if err := rows.Err(); err != nil {
		return Stream{}, false, schemaError(err)
	}
	if len(streamUIDs) == 0 {
		return Stream{}, false, nil
	}
	if len(streamUIDs) != 1 {
		return Stream{}, false, ErrEventConflict
	}
	stream, err := r.GetStream(ctx, streamUIDs[0], ownerID)
	return stream, err == nil, err
}

// ListStagedUserEventsBySession returns a bounded repair set for the existing
// recovery loop. A saturated set fails closed instead of silently stranding
// input beyond the scan boundary.
func (r *Repository) ListStagedUserEventsBySession(ctx context.Context, ownerID, sessionID string, limit int) (Stream, []Event, error) {
	if limit <= 0 || limit > 1000 {
		return Stream{}, nil, errors.New("staged event recovery requires a bounded limit")
	}
	stream, found, err := r.GetStreamBySession(ctx, ownerID, sessionID)
	if err != nil || !found {
		if err == nil {
			err = ErrEventConflict
		}
		return Stream{}, nil, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT stream_uid,event_id,publication_seq,client_message_id,event_type,source,runner_attempt,payload_json,frame_event_id,created_at
		FROM transcript_events WHERE stream_uid=? AND event_type='user_message_staged'
		ORDER BY event_id LIMIT ?`, stream.UID, limit+1)
	if err != nil {
		return Stream{}, nil, schemaError(err)
	}
	defer rows.Close()
	events := make([]Event, 0, limit)
	for rows.Next() {
		var event Event
		var source string
		var runnerAttempt sql.NullInt64
		var payload []byte
		var frameEventID sql.NullString
		if err := rows.Scan(
			&event.StreamUID, &event.EventID, &event.PublicationSeq, &event.ClientMessageID, &event.Type,
			&source, &runnerAttempt, &payload, &frameEventID, &event.CreatedAt,
		); err != nil {
			return Stream{}, nil, schemaError(err)
		}
		event.Source = EventSource(source)
		event.PayloadJSON = append([]byte(nil), payload...)
		if runnerAttempt.Valid {
			value := runnerAttempt.Int64
			event.RunnerAttempt = &value
			return Stream{}, nil, ErrEventConflict
		}
		if frameEventID.Valid {
			value := frameEventID.String
			event.FrameEventID = &value
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return Stream{}, nil, schemaError(err)
	}
	if len(events) > limit {
		return Stream{}, nil, errors.New("staged event recovery exceeds limit")
	}
	return stream, events, nil
}
