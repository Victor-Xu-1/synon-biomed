package transcript

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"
)

func (r *Repository) AppendUserEvent(ctx context.Context, input AppendUserEventInput) (Event, bool, error) {
	input, err := normalizeAppendUserEventInput(input)
	if err != nil {
		return Event{}, false, err
	}
	var event Event
	var created bool
	err = r.withImmediate(ctx, func(conn *sql.Conn) error {
		var err error
		event, created, err = r.appendUserEventConn(ctx, conn, input, r.now().UTC())
		return err
	})
	return event, created, schemaError(err)
}

func normalizeAppendUserEventInput(input AppendUserEventInput) (AppendUserEventInput, error) {
	input.StreamUID = strings.TrimSpace(input.StreamUID)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.ClientMessageID = strings.TrimSpace(input.ClientMessageID)
	if input.StreamUID == "" || input.OwnerID == "" || input.ClientMessageID == "" {
		return AppendUserEventInput{}, errors.New("stream uid, owner, and client message id are required")
	}
	if err := validatePayload(EventSourcePayload, input.PayloadJSON, nil); err != nil {
		return AppendUserEventInput{}, err
	}
	return input, nil
}

func (r *Repository) appendUserEventConn(
	ctx context.Context,
	conn *sql.Conn,
	input AppendUserEventInput,
	now time.Time,
) (Event, bool, error) {
	stream, err := getStreamConn(ctx, conn, input.StreamUID, input.OwnerID)
	if err != nil {
		return Event{}, false, err
	}
	event, created, err := appendEventConn(ctx, conn, stream, eventRecord{
		clientMessageID: input.ClientMessageID, eventType: "user_message", source: EventSourcePayload,
		payloadJSON: input.PayloadJSON, destinations: input.Destinations, createdAt: now,
	})
	if err != nil || !created {
		return event, created, err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE transcript_streams SET input_revision=input_revision+1 WHERE stream_uid=?`, input.StreamUID); err != nil {
		return Event{}, false, err
	}
	if err := activateFrameForNewInputConn(ctx, conn, stream, now); err != nil {
		return Event{}, false, err
	}
	return event, true, nil
}

// StartInternalFrameRunner closes the admission-to-claim race for internal
// reviewer Frames. A failure rolls back the stream, input event, and claim as
// one unit, so setup cannot leave an orphan transcript behind.
func (r *Repository) StartInternalFrameRunner(
	ctx context.Context,
	input StartInternalFrameRunnerInput,
) (StartInternalFrameRunnerResult, error) {
	streamInput, err := normalizeCreateStreamInput(input.Stream)
	if err != nil {
		return StartInternalFrameRunnerResult{}, err
	}
	userInput, err := normalizeAppendUserEventInput(input.UserEvent)
	if err != nil {
		return StartInternalFrameRunnerResult{}, err
	}
	input.RunnerID = strings.TrimSpace(input.RunnerID)
	if streamInput.Kind != StreamKindFrameRef || input.RunnerID == "" || input.TTL <= 0 {
		return StartInternalFrameRunnerResult{}, errors.New("internal frame runner requires a frame stream, runner id, and positive ttl")
	}
	if userInput.StreamUID != streamInput.UID || userInput.OwnerID != streamInput.OwnerID {
		return StartInternalFrameRunnerResult{}, errors.New("internal frame runner stream and user event authority must match")
	}

	var result StartInternalFrameRunnerResult
	err = r.withImmediate(ctx, func(conn *sql.Conn) error {
		now := r.now().UTC()
		stream, err := createStreamConn(ctx, conn, streamInput, now)
		if err != nil {
			return err
		}
		event, eventCreated, err := r.appendUserEventConn(ctx, conn, userInput, now)
		if err != nil {
			return err
		}
		stream, err = getStreamConn(ctx, conn, stream.UID, stream.OwnerID)
		if err != nil {
			return err
		}
		claimed, err := r.claimRunnerConn(ctx, conn, stream, ClaimRunnerInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: input.RunnerID,
			TTL: input.TTL, ResumeSource: ResumeSourceFresh,
		}, now)
		if err != nil {
			return err
		}
		if !claimed.Claimed && claimed.OwnerRunnerID != input.RunnerID {
			return ErrEventConflict
		}
		result = StartInternalFrameRunnerResult{
			Stream: stream, Event: event, Claim: claimed.Claim,
			EventCreated: eventCreated, Claimed: claimed.Claimed,
		}
		return nil
	})
	return result, schemaError(err)
}

// StageUserEvent durably records input without making it runner-claimable.
// AdmitUserEvent performs the only input_revision transition after all
// compatibility projections required by the active runtime are ready.
func (r *Repository) StageUserEvent(ctx context.Context, input AppendUserEventInput) (StageUserEventResult, error) {
	input.StreamUID = strings.TrimSpace(input.StreamUID)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.ClientMessageID = strings.TrimSpace(input.ClientMessageID)
	if input.StreamUID == "" || input.OwnerID == "" || input.ClientMessageID == "" {
		return StageUserEventResult{}, errors.New("stream uid, owner, and client message id are required")
	}
	if err := validatePayload(EventSourcePayload, input.PayloadJSON, nil); err != nil {
		return StageUserEventResult{}, err
	}
	var result StageUserEventResult
	err := r.withImmediate(ctx, func(conn *sql.Conn) error {
		stream, err := getStreamConn(ctx, conn, input.StreamUID, input.OwnerID)
		if err != nil {
			return err
		}
		record := eventRecord{
			clientMessageID: input.ClientMessageID, eventType: "user_message_staged", source: EventSourcePayload,
			payloadJSON: input.PayloadJSON, destinations: input.Destinations, createdAt: r.now().UTC(),
		}
		if existing, found, err := findEventByClientID(ctx, conn, stream.UID, input.ClientMessageID); err != nil {
			return err
		} else if found {
			if existing.Type != "user_message_staged" && existing.Type != "user_message" {
				return ErrEventConflict
			}
			record.eventType = existing.Type
			if !eventMatches(existing, record) {
				return ErrEventConflict
			}
			destinations, err := deliveryDestinations(ctx, conn, existing.StreamUID, existing.PublicationSeq)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(destinations, normalizedDestinations(input.Destinations)) {
				return ErrEventConflict
			}
			result = StageUserEventResult{Event: existing, Admitted: existing.Type == "user_message"}
			return nil
		}
		var stagedCount int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcript_events
			WHERE stream_uid=? AND event_type='user_message_staged'`, stream.UID).Scan(&stagedCount); err != nil {
			return err
		}
		if stagedCount != 0 {
			return ErrEventConflict
		}
		event, created, err := appendEventConn(ctx, conn, stream, record)
		result = StageUserEventResult{Event: event, Created: created}
		return err
	})
	return result, schemaError(err)
}

func (r *Repository) AdmitUserEvent(ctx context.Context, input AdmitUserEventInput) (Event, bool, error) {
	input.StreamUID = strings.TrimSpace(input.StreamUID)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.ClientMessageID = strings.TrimSpace(input.ClientMessageID)
	if input.StreamUID == "" || input.OwnerID == "" || input.ClientMessageID == "" {
		return Event{}, false, errors.New("stream uid, owner, and client message id are required")
	}
	var event Event
	var admitted bool
	err := r.withImmediate(ctx, func(conn *sql.Conn) error {
		stream, err := getStreamConn(ctx, conn, input.StreamUID, input.OwnerID)
		if err != nil {
			return err
		}
		event, found, err := findEventByClientID(ctx, conn, stream.UID, input.ClientMessageID)
		if err != nil {
			return err
		}
		if !found {
			return ErrEventConflict
		}
		if event.Type == "user_message" {
			return nil
		}
		if event.Type != "user_message_staged" || event.RunnerAttempt != nil {
			return ErrEventConflict
		}
		var firstStagedEventID int64
		if err := conn.QueryRowContext(ctx, `SELECT MIN(event_id) FROM transcript_events
			WHERE stream_uid=? AND event_type='user_message_staged'`, stream.UID).Scan(&firstStagedEventID); err != nil {
			return err
		}
		if firstStagedEventID != event.EventID {
			return ErrEventConflict
		}
		updated, err := conn.ExecContext(ctx, `UPDATE transcript_events SET event_type='user_message'
			WHERE stream_uid=? AND event_id=? AND event_type='user_message_staged' AND runner_attempt IS NULL`, stream.UID, event.EventID)
		if err != nil {
			return err
		}
		if count, err := updated.RowsAffected(); err != nil || count != 1 {
			if err != nil {
				return err
			}
			return ErrEventConflict
		}
		if _, err := conn.ExecContext(ctx, `UPDATE transcript_streams
			SET input_revision=input_revision+1,updated_at=? WHERE stream_uid=?`, r.now().UTC(), stream.UID); err != nil {
			return err
		}
		event.Type = "user_message"
		admitted = true
		return activateFrameForNewInputConn(ctx, conn, stream, r.now().UTC())
	})
	return event, admitted, schemaError(err)
}

func (r *Repository) AppendFrameUserEvent(
	ctx context.Context,
	input AppendFrameUserEventInput,
) (Event, FrameReferenceEvent, bool, error) {
	input, err := normalizeAppendFrameUserEventInput(input)
	if err != nil {
		return Event{}, FrameReferenceEvent{}, false, err
	}
	var event Event
	var frameEvent FrameReferenceEvent
	var created bool
	err = r.withImmediate(ctx, func(conn *sql.Conn) error {
		var err error
		event, frameEvent, created, err = appendFrameUserEventConn(ctx, conn, input, r.now().UTC())
		return err
	})
	return event, frameEvent, created, schemaError(err)
}

func (tx *ImmediateTransaction) AppendFrameUserEvent(
	ctx context.Context,
	input AppendFrameUserEventInput,
) (Event, FrameReferenceEvent, bool, error) {
	if tx == nil || tx.repository == nil || tx.conn == nil {
		return Event{}, FrameReferenceEvent{}, false, errors.New("transcript transaction is required")
	}
	input, err := normalizeAppendFrameUserEventInput(input)
	if err != nil {
		return Event{}, FrameReferenceEvent{}, false, err
	}
	event, frameEvent, created, err := appendFrameUserEventConn(ctx, tx.conn, input, tx.repository.now().UTC())
	return event, frameEvent, created, schemaError(err)
}

func normalizeAppendFrameUserEventInput(input AppendFrameUserEventInput) (AppendFrameUserEventInput, error) {
	input.StreamUID = strings.TrimSpace(input.StreamUID)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.ClientMessageID = strings.TrimSpace(input.ClientMessageID)
	input.FrameEventID = strings.TrimSpace(input.FrameEventID)
	input.MessageUUID = strings.TrimSpace(input.MessageUUID)
	input.Text = strings.TrimSpace(input.Text)
	input.MessageOrigin = strings.TrimSpace(input.MessageOrigin)
	input.MessageContext = strings.TrimSpace(input.MessageContext)
	if input.StreamUID == "" || input.OwnerID == "" || input.ClientMessageID == "" || input.FrameEventID == "" ||
		input.MessageUUID == "" || input.Text == "" {
		return AppendFrameUserEventInput{}, errors.New("stream, owner, client message, frame event, message uuid, and text are required")
	}
	if input.MessageOrigin != "" && input.MessageOrigin != "task_intent" && input.MessageOrigin != "input_response" {
		return AppendFrameUserEventInput{}, errors.New("message origin must be task_intent or input_response")
	}
	if input.MessageContext != "" && input.MessageContext != "onboarding_first_task" && input.MessageContext != "onboarding_suggestions" {
		return AppendFrameUserEventInput{}, errors.New("message context is invalid")
	}
	refs, err := normalizeUserArtifactReferenceInputs(input.ArtifactReferences)
	if err != nil {
		return AppendFrameUserEventInput{}, err
	}
	input.ArtifactReferences = refs
	return input, nil
}

func appendFrameUserEventConn(
	ctx context.Context,
	conn *sql.Conn,
	input AppendFrameUserEventInput,
	now time.Time,
) (Event, FrameReferenceEvent, bool, error) {
	var event Event
	var frameEvent FrameReferenceEvent
	var created bool
	err := func() error {
		stream, err := getStreamConn(ctx, conn, input.StreamUID, input.OwnerID)
		if err != nil {
			return err
		}
		if stream.Kind != StreamKindFrameRef {
			return ErrEventConflict
		}
		existing, existingFound, err := findEventByClientID(ctx, conn, stream.UID, input.ClientMessageID)
		if err != nil {
			return err
		}
		eventType := "user_message"
		messageOrigin := "task_intent"
		if existingFound {
			eventType = existing.Type
			if eventType == "user_input_response" {
				messageOrigin = "input_response"
			} else if eventType != "user_message" {
				return ErrEventConflict
			}
			if input.MessageOrigin != "" && input.MessageOrigin != messageOrigin {
				return ErrEventConflict
			}
		} else if input.MessageOrigin != "" {
			messageOrigin = input.MessageOrigin
			if messageOrigin == "input_response" {
				eventType = "user_input_response"
			}
		} else {
			var frameStatus string
			if err := conn.QueryRowContext(ctx, `SELECT status FROM frames WHERE id=?`, stream.FrameID).Scan(&frameStatus); err != nil {
				return err
			}
			if frameStatus == "awaiting_user_response" || frameStatus == "awaiting_plan_approval" {
				eventType = "user_input_response"
				messageOrigin = "input_response"
			}
		}
		artifactReferences := []UserArtifactReference(nil)
		if existingFound && existing.Source == EventSourcePayload {
			var persisted struct {
				ArtifactReferences []UserArtifactReference `json:"artifactRefs"`
			}
			if err := json.Unmarshal(existing.PayloadJSON, &persisted); err != nil ||
				!userArtifactReferenceInputsMatch(input.ArtifactReferences, persisted.ArtifactReferences) {
				return ErrEventConflict
			}
			artifactReferences = persisted.ArtifactReferences
		} else if len(input.ArtifactReferences) > 0 {
			if existingFound {
				return ErrEventConflict
			}
			artifactReferences, err = validateUserArtifactReferences(ctx, conn, stream, input.ArtifactReferences)
			if err != nil {
				return err
			}
		}
		if input.MessageContext == "onboarding_first_task" &&
			(len(artifactReferences) == 0 || artifactReferences[0].Filename != "onboarding-profile.md" ||
				artifactReferences[0].ContentType != "text/markdown") {
			return ErrEventConflict
		}
		messagePayload := map[string]any{
			"messageUuid": input.MessageUUID, "clientMessageId": input.ClientMessageID,
			"text": input.Text, "role": "user", "_uuid": input.MessageUUID, "messageOrigin": messageOrigin,
			"content": []any{map[string]any{"type": "text", "text": input.Text}},
		}
		if len(input.InputData) > 0 {
			messagePayload["inputData"] = input.InputData
		}
		if len(input.RuntimeConfig) > 0 {
			messagePayload["runtimeConfig"] = input.RuntimeConfig
		}
		if len(artifactReferences) > 0 {
			messagePayload["artifactRefs"] = artifactReferences
		}
		if input.MessageContext != "" {
			messagePayload["messageContext"] = input.MessageContext
		}
		payload, err := json.Marshal(messagePayload)
		if err != nil || len(payload) > maxEventPayloadBytes {
			return errors.New("frame user event payload is invalid")
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
			record := eventRecord{
				clientMessageID: input.ClientMessageID, eventType: eventType, source: EventSourcePayload,
				payloadJSON: payload, destinations: input.Destinations, createdAt: now,
			}
			if existingFound {
				if !eventMatches(existing, record) {
					return ErrEventConflict
				}
				destinations, err := deliveryDestinations(ctx, conn, existing.StreamUID, existing.PublicationSeq)
				if err != nil || !reflect.DeepEqual(destinations, normalizedDestinations(input.Destinations)) {
					if err != nil {
						return err
					}
					return ErrEventConflict
				}
				event = existing
				frameEvent = transcriptPayloadFrameProjection(stream, existing, payload)
				return nil
			}
			var duplicateMessageUUID int
			if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcript_events
				WHERE stream_uid=? AND event_type IN ('user_message','user_input_response') AND source='payload'
					AND json_extract(payload_json,'$.messageUuid')=?`, stream.UID, input.MessageUUID).
				Scan(&duplicateMessageUUID); err != nil {
				return err
			}
			if duplicateMessageUUID != 0 {
				return ErrEventConflict
			}
			event, created, err = appendEventConn(ctx, conn, stream, record)
			if err != nil || !created {
				return err
			}
			frameEvent = transcriptPayloadFrameProjection(stream, event, payload)
			if _, err := conn.ExecContext(ctx, `UPDATE transcript_streams
				SET input_revision=input_revision+1,updated_at=? WHERE stream_uid=?`, now, stream.UID); err != nil {
				return err
			}
			if input.DeferFrameActivation {
				return nil
			}
			return activateFrameForNewInputConn(ctx, conn, stream, now)
		}
		if readAuthority != "legacy_mixed_v1" || writeAuthority != "legacy_frame_ref_v1" {
			return ErrEventConflict
		}
		record := eventRecord{
			clientMessageID: input.ClientMessageID, eventType: eventType, source: EventSourceFrameRef,
			frameEventID: &input.FrameEventID, destinations: input.Destinations, createdAt: now,
		}
		if existingFound {
			if !eventMatches(existing, record) {
				return ErrEventConflict
			}
			frameEvent, err = loadMatchingFrameReferenceEvent(ctx, conn, stream.FrameID, input.FrameEventID, eventType, payload)
			if err != nil {
				return err
			}
			if eventType == "user_message" {
				if _, err = insertOrValidateFrameTaskIntent(ctx, conn, frameEvent, input.MessageUUID, input.Text); err != nil {
					return err
				}
			}
			destinations, err := deliveryDestinations(ctx, conn, existing.StreamUID, existing.PublicationSeq)
			if err != nil || !reflect.DeepEqual(destinations, normalizedDestinations(input.Destinations)) {
				if err != nil {
					return err
				}
				return ErrEventConflict
			}
			event = existing
			return nil
		}
		frameEvent, err = insertOrValidateFrameReferenceEvent(ctx, conn, stream.FrameID, input.FrameEventID, eventType, payload, record.createdAt)
		if err != nil {
			return err
		}
		if eventType == "user_message" {
			if _, err = insertOrValidateFrameTaskIntent(ctx, conn, frameEvent, input.MessageUUID, input.Text); err != nil {
				return err
			}
		}
		event, created, err = appendEventConn(ctx, conn, stream, record)
		if err != nil || !created {
			return err
		}
		_, err = conn.ExecContext(ctx, `
			UPDATE transcript_streams SET input_revision=input_revision+1,updated_at=? WHERE stream_uid=?`, record.createdAt, stream.UID)
		if err != nil {
			return err
		}
		if input.DeferFrameActivation {
			return nil
		}
		return activateFrameForNewInputConn(ctx, conn, stream, now)
	}()
	return event, frameEvent, created, err
}
