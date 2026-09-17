package transcript

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

func transcriptPayloadFrameProjection(stream Stream, event Event, payload []byte) FrameReferenceEvent {
	return FrameReferenceEvent{
		ID:      "transcript:" + stream.UID + ":" + strconv.FormatInt(event.EventID, 10),
		FrameID: stream.FrameID, Sequence: event.PublicationSeq, Type: event.Type,
		PayloadJSON: append([]byte(nil), payload...), CreatedAt: event.CreatedAt,
	}
}

// AppendHistoricalFrameReferences imports immutable Frame messages as runner
// context without treating them as new input or publishing them again.
func (tx *ImmediateTransaction) AppendHistoricalFrameReferences(
	ctx context.Context,
	streamUID, ownerID string,
	frameEventIDs []string,
) ([]Event, error) {
	if tx == nil || tx.repository == nil || tx.conn == nil {
		return nil, errors.New("transcript transaction is required")
	}
	stream, err := getStreamConn(ctx, tx.conn, strings.TrimSpace(streamUID), strings.TrimSpace(ownerID))
	if err != nil {
		return nil, schemaError(err)
	}
	if stream.Kind != StreamKindFrameRef {
		return nil, ErrEventConflict
	}
	events := make([]Event, 0, len(frameEventIDs))
	seen := make(map[string]struct{}, len(frameEventIDs))
	for _, rawID := range frameEventIDs {
		frameEventID := strings.TrimSpace(rawID)
		if frameEventID == "" {
			return nil, errors.New("historical frame event id is required")
		}
		if _, duplicate := seen[frameEventID]; duplicate {
			return nil, ErrEventConflict
		}
		seen[frameEventID] = struct{}{}
		var frameID, eventType string
		var payload []byte
		var createdAt time.Time
		if err := tx.conn.QueryRowContext(ctx, `
			SELECT frame_id,event_type,payload,created_at FROM frame_events WHERE id=?`, frameEventID,
		).Scan(&frameID, &eventType, &payload, &createdAt); err != nil {
			return nil, schemaError(err)
		}
		if frameID != stream.FrameID || (eventType != "user_message" && eventType != "assistant_message" && eventType != "system_message") {
			return nil, ErrEventConflict
		}
		var message map[string]any
		if len(payload) == 0 || len(payload) > maxEventPayloadBytes || json.Unmarshal(payload, &message) != nil {
			return nil, ErrEventConflict
		}
		role, roleFound := message["role"].(string)
		if !roleFound || strings.TrimSpace(role) != strings.TrimSuffix(eventType, "_message") {
			return nil, ErrEventConflict
		}
		event, _, err := appendEventConn(ctx, tx.conn, stream, eventRecord{
			clientMessageID: "history:" + frameEventID, eventType: eventType, source: EventSourceFrameRef,
			frameEventID: &frameEventID, createdAt: createdAt,
		})
		if err != nil {
			return nil, schemaError(err)
		}
		events = append(events, event)
		stream, err = getStreamConn(ctx, tx.conn, stream.UID, stream.OwnerID)
		if err != nil {
			return nil, schemaError(err)
		}
	}
	return events, nil
}

// AppendFrameInputResponse appends one immutable user response to the active
// payload authority, or binds the legacy FrameEvent while that older authority
// is still active, inside one workspace/transcript transaction.
func (tx *ImmediateTransaction) AppendFrameInputResponse(
	ctx context.Context,
	input AppendFrameInputResponseInput,
) (Event, bool, error) {
	input.StreamUID = strings.TrimSpace(input.StreamUID)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.FrameID = strings.TrimSpace(input.FrameID)
	input.ClientMessageID = strings.TrimSpace(input.ClientMessageID)
	input.FrameEventID = strings.TrimSpace(input.FrameEventID)
	if tx == nil || tx.repository == nil || tx.conn == nil || input.OwnerID == "" || input.FrameID == "" {
		return Event{}, false, errors.New("transaction, owner, and frame are required")
	}
	streamUID := input.StreamUID
	if streamUID == "" {
		if err := tx.conn.QueryRowContext(ctx, `
			SELECT stream_uid FROM transcript_streams
			WHERE owner_id=? AND frame_id=? AND kind='frame_ref'
			ORDER BY epoch DESC LIMIT 1`, input.OwnerID, input.FrameID).Scan(&streamUID); err != nil {
			return Event{}, false, schemaError(err)
		}
	}
	stream, err := getStreamConn(ctx, tx.conn, streamUID, input.OwnerID)
	if err != nil {
		return Event{}, false, schemaError(err)
	}
	if stream.Kind != StreamKindFrameRef || stream.FrameID != input.FrameID {
		return Event{}, false, ErrEventConflict
	}
	authority, found, err := getFrameAuthorityBySessionQuery(ctx, tx.conn, stream.OwnerID, stream.SessionID)
	if err != nil {
		return Event{}, false, schemaError(err)
	}
	if !found || authority.ActiveStreamUID != stream.UID || authority.ActiveEpoch != stream.Epoch {
		return Event{}, false, fmt.Errorf("input response authority: %w", ErrEventConflict)
	}
	now := tx.repository.now().UTC()
	record := eventRecord{eventType: "user_input_response", destinations: input.Destinations, createdAt: now}
	if authority.TranscriptPayloadActive() {
		if input.FrameEventID != "" || input.ClientMessageID == "" {
			return Event{}, false, fmt.Errorf("input response payload provenance: %w", ErrEventConflict)
		}
		if err := validatePayload(EventSourcePayload, input.PayloadJSON, nil); err != nil {
			return Event{}, false, err
		}
		record.clientMessageID = input.ClientMessageID
		record.source = EventSourcePayload
		record.payloadJSON = input.PayloadJSON
	} else {
		if input.ClientMessageID == "" || input.FrameEventID == "" || len(input.PayloadJSON) != 0 {
			return Event{}, false, errors.New("client message and frame event are required for legacy input response")
		}
		frameEventID := input.FrameEventID
		if err := validateFrameEventReferenceConn(ctx, tx.conn, stream, "user_input_response", EventSourceFrameRef, &frameEventID); err != nil {
			return Event{}, false, schemaError(err)
		}
		record.clientMessageID = input.ClientMessageID
		record.source = EventSourceFrameRef
		record.frameEventID = &frameEventID
	}
	event, created, err := appendEventConn(ctx, tx.conn, stream, record)
	if err != nil || !created {
		if err != nil {
			return event, created, fmt.Errorf("append input response: %w", schemaError(err))
		}
		return event, created, nil
	}
	_, err = tx.conn.ExecContext(ctx, `
		UPDATE transcript_streams SET input_revision=input_revision+1,updated_at=? WHERE stream_uid=?`, now, stream.UID)
	return event, true, schemaError(err)
}

func (r *Repository) GetActiveFrameTaskIntent(ctx context.Context, streamUID, ownerID string) (FrameTaskIntent, bool, error) {
	stream, err := r.GetStream(ctx, streamUID, ownerID)
	if err != nil {
		return FrameTaskIntent{}, false, err
	}
	if stream.Kind != StreamKindFrameRef || strings.TrimSpace(stream.FrameID) == "" {
		return FrameTaskIntent{}, false, ErrEventConflict
	}
	authority, found, err := r.GetFrameAuthorityBySession(ctx, stream.OwnerID, stream.SessionID)
	if err != nil {
		return FrameTaskIntent{}, false, err
	}
	if found && authority.TranscriptPayloadActive() {
		if authority.ActiveStreamUID != stream.UID || authority.ActiveEpoch != stream.Epoch {
			return FrameTaskIntent{}, false, ErrEventConflict
		}
		intent, found, err := latestPayloadFrameTaskIntent(ctx, r.db, stream)
		return intent, found, schemaError(err)
	}
	var intent FrameTaskIntent
	err = r.db.QueryRowContext(ctx, `
		SELECT intent.id,intent.frame_id,intent.revision,intent.source_event_id,intent.source_message_id,
			intent.origin,intent.language,intent.text,intent.created_at
		FROM frame_active_task_intents active
		JOIN frame_task_intents intent ON intent.id=active.task_intent_id
		WHERE active.frame_id=?`, stream.FrameID,
	).Scan(&intent.ID, &intent.FrameID, &intent.Revision, &intent.SourceEventID, &intent.SourceMessageID,
		&intent.Origin, &intent.Language, &intent.Text, &intent.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return FrameTaskIntent{}, false, nil
	}
	if err != nil {
		return FrameTaskIntent{}, false, schemaError(err)
	}
	return intent, true, nil
}

// ListActiveFrameTaskIntents returns the canonical task-intent history on the
// active Transcript branch. Prompt replay is deliberately bounded and may no
// longer contain the root intent of a long continuation chain; task identity
// and evidence scoping must therefore read this durable authority directly.
func (r *Repository) ListActiveFrameTaskIntents(
	ctx context.Context,
	streamUID, ownerID string,
) ([]FrameTaskIntent, error) {
	stream, err := r.GetStream(ctx, strings.TrimSpace(streamUID), strings.TrimSpace(ownerID))
	if err != nil {
		return nil, err
	}
	if stream.Kind != StreamKindFrameRef || strings.TrimSpace(stream.FrameID) == "" {
		return nil, ErrEventConflict
	}
	authority, found, err := r.GetFrameAuthorityBySession(ctx, stream.OwnerID, stream.SessionID)
	if err != nil {
		return nil, err
	}
	if found && authority.TranscriptPayloadActive() {
		if authority.ActiveStreamUID != stream.UID || authority.ActiveEpoch != stream.Epoch {
			return nil, ErrEventConflict
		}
		intents, err := listPayloadFrameTaskIntents(ctx, r.db, stream)
		return intents, schemaError(err)
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id,frame_id,revision,source_event_id,source_message_id,origin,language,text,created_at
		FROM frame_task_intents WHERE frame_id=? ORDER BY revision ASC`, stream.FrameID)
	if err != nil {
		return nil, schemaError(err)
	}
	defer rows.Close()
	intents := make([]FrameTaskIntent, 0)
	for rows.Next() {
		var intent FrameTaskIntent
		if err := rows.Scan(
			&intent.ID, &intent.FrameID, &intent.Revision, &intent.SourceEventID,
			&intent.SourceMessageID, &intent.Origin, &intent.Language, &intent.Text, &intent.CreatedAt,
		); err != nil {
			return nil, schemaError(err)
		}
		intents = append(intents, intent)
	}
	return intents, schemaError(rows.Err())
}

func (r *Repository) EnsureActiveFrameTaskIntent(ctx context.Context, streamUID, ownerID string) (FrameTaskIntent, bool, error) {
	streamUID = strings.TrimSpace(streamUID)
	ownerID = strings.TrimSpace(ownerID)
	var intent FrameTaskIntent
	var found bool
	err := r.withImmediate(ctx, func(conn *sql.Conn) error {
		stream, err := getStreamConn(ctx, conn, streamUID, ownerID)
		if err != nil {
			return err
		}
		if stream.Kind != StreamKindFrameRef || strings.TrimSpace(stream.FrameID) == "" {
			return ErrEventConflict
		}
		var activeStreamUID, readAuthority, writeAuthority string
		var activeEpoch int64
		if err := conn.QueryRowContext(ctx, `SELECT active_stream_uid,active_epoch,read_authority,write_authority
			FROM transcript_frame_authority WHERE owner_id=? AND session_id=?`,
			stream.OwnerID, stream.SessionID).Scan(&activeStreamUID, &activeEpoch, &readAuthority, &writeAuthority); err != nil {
			return err
		}
		if activeStreamUID != stream.UID || activeEpoch != stream.Epoch {
			return ErrEventConflict
		}
		if readAuthority == "transcript_payload_v1" && writeAuthority == "transcript_payload_v1" {
			intent, found, err = latestPayloadFrameTaskIntent(ctx, conn, stream)
			return err
		}
		if readAuthority != "legacy_mixed_v1" || writeAuthority != "legacy_frame_ref_v1" {
			return ErrEventConflict
		}
		var frameEvent FrameReferenceEvent
		var payload []byte
		err = conn.QueryRowContext(ctx, `
			SELECT frame_event.id,frame_event.frame_id,frame_event.sequence,frame_event.event_type,frame_event.payload,frame_event.created_at
			FROM transcript_events event
			JOIN frame_events frame_event ON frame_event.id=event.frame_event_id
			WHERE event.stream_uid=? AND event.event_type='user_message' AND event.source='frame_ref'
				AND json_extract(frame_event.payload,'$.messageOrigin')='task_intent'
			ORDER BY event.event_id DESC LIMIT 1`, stream.UID,
		).Scan(&frameEvent.ID, &frameEvent.FrameID, &frameEvent.Sequence, &frameEvent.Type, &payload, &frameEvent.CreatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			var unclassified int
			if countErr := conn.QueryRowContext(ctx, `
				SELECT COUNT(*) FROM transcript_events
				WHERE stream_uid=? AND event_type='user_message' AND source='frame_ref'`, stream.UID,
			).Scan(&unclassified); countErr != nil {
				return countErr
			}
			if unclassified > 0 {
				return ErrEventConflict
			}
			return nil
		}
		if err != nil {
			return err
		}
		if frameEvent.FrameID != stream.FrameID || frameEvent.Type != "user_message" {
			return ErrEventConflict
		}
		var message struct {
			MessageUUID     string `json:"messageUuid"`
			ClientMessageID string `json:"clientMessageId"`
			UUID            string `json:"_uuid"`
			Role            string `json:"role"`
			Text            string `json:"text"`
			MessageOrigin   string `json:"messageOrigin"`
		}
		if err := json.Unmarshal(payload, &message); err != nil {
			return ErrEventConflict
		}
		messageID := firstNonEmptyString(message.MessageUUID, message.UUID, message.ClientMessageID)
		message.Text = strings.TrimSpace(message.Text)
		if messageID == "" || message.Text == "" || strings.TrimSpace(message.Role) != "user" ||
			strings.TrimSpace(message.MessageOrigin) != "task_intent" {
			return ErrEventConflict
		}
		intent, err = insertOrValidateFrameTaskIntent(ctx, conn, frameEvent, messageID, message.Text)
		if err != nil {
			return err
		}
		found = true
		return nil
	})
	if err != nil {
		return FrameTaskIntent{}, false, schemaError(err)
	}
	return intent, found, nil
}

type transcriptQueryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func latestPayloadFrameTaskIntent(
	ctx context.Context,
	query transcriptQueryRower,
	stream Stream,
) (FrameTaskIntent, bool, error) {
	intents, err := listPayloadFrameTaskIntents(ctx, query, stream)
	if err != nil {
		return FrameTaskIntent{}, false, err
	}
	if len(intents) == 0 {
		return FrameTaskIntent{}, false, nil
	}
	return intents[len(intents)-1], true, nil
}

func listPayloadFrameTaskIntents(
	ctx context.Context,
	query transcriptQueryRower,
	stream Stream,
) ([]FrameTaskIntent, error) {
	rows, err := queryRowsContext(query, ctx, `SELECT event.event_id,event.client_message_id,event.payload_json,event.created_at
		FROM transcript_events event
		JOIN transcript_branch_state state ON state.stream_uid=event.stream_uid
		JOIN transcript_branch_events member ON member.stream_uid=event.stream_uid
			AND member.branch_id=state.active_branch_id AND member.event_id=event.event_id
		WHERE event.stream_uid=? AND event.event_type='user_message' AND event.source='payload'
			AND json_extract(event.payload_json,'$.messageOrigin')='task_intent'
		ORDER BY member.ordinal ASC`, stream.UID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	intents := make([]FrameTaskIntent, 0)
	var revision int64
	for rows.Next() {
		var eventID int64
		var clientMessageID string
		var payload []byte
		var createdAt time.Time
		if err := rows.Scan(&eventID, &clientMessageID, &payload, &createdAt); err != nil {
			return nil, err
		}
		candidate, ok, err := payloadFrameTaskIntentCandidate(stream, eventID, clientMessageID, payload, createdAt)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		revision++
		candidate.Revision = revision
		intents = append(intents, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return intents, nil
}

type transcriptQueryRows interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func queryRowsContext(query transcriptQueryRower, ctx context.Context, statement string, args ...any) (*sql.Rows, error) {
	rowsQuery, ok := query.(transcriptQueryRows)
	if !ok {
		return nil, errors.New("transcript query does not support rows")
	}
	return rowsQuery.QueryContext(ctx, statement, args...)
}

func payloadFrameTaskIntentCandidate(
	stream Stream,
	eventID int64,
	clientMessageID string,
	payload []byte,
	createdAt time.Time,
) (FrameTaskIntent, bool, error) {
	var message struct {
		MessageUUID     string `json:"messageUuid"`
		ClientMessageID string `json:"clientMessageId"`
		UUID            string `json:"_uuid"`
		Role            string `json:"role"`
		Text            string `json:"text"`
		MessageOrigin   string `json:"messageOrigin"`
	}
	if len(payload) == 0 || len(payload) > maxEventPayloadBytes || json.Unmarshal(payload, &message) != nil {
		return FrameTaskIntent{}, false, ErrEventConflict
	}
	messageID := firstNonEmptyString(message.MessageUUID, message.UUID, message.ClientMessageID, clientMessageID)
	message.Text = strings.TrimSpace(message.Text)
	if messageID == "" || message.Text == "" || strings.TrimSpace(message.Role) != "user" ||
		strings.TrimSpace(message.MessageOrigin) != "task_intent" {
		return FrameTaskIntent{}, false, ErrEventConflict
	}
	if isExplicitTaskContinuationDirective(message.Text) {
		return FrameTaskIntent{}, false, nil
	}
	return FrameTaskIntent{
		ID:      "transcript-task:" + stream.UID + ":" + strconv.FormatInt(eventID, 10),
		FrameID: stream.FrameID, SourceEventID: clientMessageID,
		SourceMessageID: messageID, Origin: "user", Language: frameTaskIntentLanguage(message.Text),
		Text: message.Text, CreatedAt: createdAt,
	}, true, nil
}

func isExplicitTaskContinuationDirective(text string) bool {
	normalized := strings.ToLower(strings.Trim(strings.TrimSpace(text), " .!！。\t\r\n"))
	normalized = strings.Join(strings.Fields(normalized), " ")
	switch normalized {
	case "继续", "继续运行", "继续任务", "接着运行", "接续运行",
		"continue", "continue running", "resume", "resume task", "keep going":
		return true
	default:
		return false
	}
}

// IsExplicitTaskContinuationDirective exposes the canonical product command
// classifier to replay scoping. Continuation transport messages must not split
// durable evidence, source budgets or Skill state from the task they resume.
func IsExplicitTaskContinuationDirective(text string) bool {
	return isExplicitTaskContinuationDirective(text)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func insertOrValidateFrameTaskIntent(
	ctx context.Context,
	conn *sql.Conn,
	frameEvent FrameReferenceEvent,
	messageID, text string,
) (FrameTaskIntent, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT id,frame_id,revision,source_event_id,source_message_id,origin,language,text,created_at
		FROM frame_task_intents
		WHERE source_event_id=? OR (frame_id=? AND source_message_id=?)`, frameEvent.ID, frameEvent.FrameID, messageID)
	if err != nil {
		return FrameTaskIntent{}, err
	}
	defer rows.Close()
	var matches []FrameTaskIntent
	for rows.Next() {
		var candidate FrameTaskIntent
		if err := rows.Scan(&candidate.ID, &candidate.FrameID, &candidate.Revision, &candidate.SourceEventID,
			&candidate.SourceMessageID, &candidate.Origin, &candidate.Language, &candidate.Text, &candidate.CreatedAt); err != nil {
			return FrameTaskIntent{}, err
		}
		matches = append(matches, candidate)
	}
	if err := rows.Err(); err != nil {
		return FrameTaskIntent{}, err
	}
	language := frameTaskIntentLanguage(text)
	var intent FrameTaskIntent
	switch len(matches) {
	case 0:
		intent = FrameTaskIntent{
			ID: "task-intent:" + frameEvent.ID, FrameID: frameEvent.FrameID, SourceEventID: frameEvent.ID,
			SourceMessageID: messageID, Origin: "user", Language: language, Text: text, CreatedAt: frameEvent.CreatedAt,
		}
		if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision),0)+1 FROM frame_task_intents WHERE frame_id=?`, frameEvent.FrameID).Scan(&intent.Revision); err != nil {
			return FrameTaskIntent{}, err
		}
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO frame_task_intents(id,frame_id,revision,source_event_id,source_message_id,origin,language,text,created_at)
			VALUES(?,?,?,?,?,?,?,?,?)`, intent.ID, intent.FrameID, intent.Revision, intent.SourceEventID, intent.SourceMessageID,
			intent.Origin, intent.Language, intent.Text, intent.CreatedAt); err != nil {
			return FrameTaskIntent{}, err
		}
	case 1:
		intent = matches[0]
		if intent.FrameID != frameEvent.FrameID || intent.SourceEventID != frameEvent.ID || intent.SourceMessageID != messageID ||
			intent.Origin != "user" || intent.Language != language || intent.Text != text {
			return FrameTaskIntent{}, ErrEventConflict
		}
	default:
		return FrameTaskIntent{}, ErrEventConflict
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO frame_active_task_intents(frame_id,task_intent_id,updated_at) VALUES(?,?,?)
		ON CONFLICT(frame_id) DO UPDATE SET task_intent_id=excluded.task_intent_id,updated_at=excluded.updated_at
		WHERE (SELECT revision FROM frame_task_intents WHERE id=excluded.task_intent_id) >=
			(SELECT revision FROM frame_task_intents WHERE id=frame_active_task_intents.task_intent_id)`,
		intent.FrameID, intent.ID, intent.CreatedAt); err != nil {
		return FrameTaskIntent{}, err
	}
	return intent, nil
}

func frameTaskIntentLanguage(text string) string {
	latin := false
	for _, value := range text {
		if unicode.Is(unicode.Han, value) {
			return "zh"
		}
		latin = latin || unicode.Is(unicode.Latin, value)
	}
	if latin {
		return "en"
	}
	return "und"
}

func insertOrValidateFrameReferenceEvent(
	ctx context.Context,
	conn *sql.Conn,
	frameID, eventID, eventType string,
	payload []byte,
	createdAt time.Time,
) (FrameReferenceEvent, error) {
	if existing, err := loadMatchingFrameReferenceEvent(ctx, conn, frameID, eventID, eventType, payload); err == nil {
		return existing, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return FrameReferenceEvent{}, err
	}
	var sequence int64
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0)+1 FROM frame_events WHERE frame_id=?`, frameID).Scan(&sequence); err != nil {
		return FrameReferenceEvent{}, err
	}
	result := FrameReferenceEvent{
		ID: eventID, FrameID: frameID, Sequence: sequence, Type: eventType,
		PayloadJSON: append([]byte(nil), payload...), CreatedAt: createdAt,
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at)
		VALUES(?,?,?,?,?,?)`, result.ID, result.FrameID, result.Sequence, result.Type, string(payload), result.CreatedAt); err != nil {
		return FrameReferenceEvent{}, err
	}
	return result, nil
}

func loadMatchingFrameReferenceEvent(
	ctx context.Context,
	conn *sql.Conn,
	frameID, eventID, eventType string,
	payload []byte,
) (FrameReferenceEvent, error) {
	var result FrameReferenceEvent
	var storedPayload []byte
	err := conn.QueryRowContext(ctx, `
		SELECT id,frame_id,sequence,event_type,payload,created_at FROM frame_events WHERE id=?`, eventID,
	).Scan(&result.ID, &result.FrameID, &result.Sequence, &result.Type, &storedPayload, &result.CreatedAt)
	if err != nil {
		return FrameReferenceEvent{}, err
	}
	if result.FrameID != frameID || result.Type != eventType || !jsonEqual(storedPayload, payload) {
		return FrameReferenceEvent{}, ErrEventConflict
	}
	result.PayloadJSON = append([]byte(nil), storedPayload...)
	return result, nil
}

func (r *Repository) ClaimRunner(ctx context.Context, input ClaimRunnerInput) (ClaimRunnerResult, error) {
	input.StreamUID = strings.TrimSpace(input.StreamUID)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.RunnerID = strings.TrimSpace(input.RunnerID)
	if input.StreamUID == "" || input.OwnerID == "" || input.RunnerID == "" || input.TTL <= 0 {
		return ClaimRunnerResult{}, errors.New("stream uid, owner, runner id, and positive ttl are required")
	}
	if err := validateResumeRequest(input.ResumeSource, input.ResumeCheckpoint); err != nil {
		return ClaimRunnerResult{}, err
	}
	now := r.now().UTC()
	var result ClaimRunnerResult
	err := r.withImmediate(ctx, func(conn *sql.Conn) error {
		stream, err := getStreamConn(ctx, conn, input.StreamUID, input.OwnerID)
		if err != nil {
			return err
		}
		result, err = r.claimRunnerConn(ctx, conn, stream, input, now)
		return err
	})
	return result, schemaError(err)
}
