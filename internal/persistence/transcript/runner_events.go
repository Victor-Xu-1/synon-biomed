package transcript

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"
)

func (r *Repository) AppendRunnerEvent(ctx context.Context, input AppendEventInput) (Event, bool, error) {
	if err := validateRunnerEventInput(input); err != nil {
		return Event{}, false, err
	}
	var event Event
	var created bool
	err := r.withImmediate(ctx, func(conn *sql.Conn) error {
		stream, err := validateClaimConn(ctx, conn, input.Claim, r.now().UTC(), true)
		if err != nil {
			return err
		}
		if err := validateFrameEventReferenceConn(ctx, conn, stream, input.Type, input.Source, input.FrameEventID); err != nil {
			return err
		}
		event, created, err = appendEventConn(ctx, conn, stream, eventRecord{
			clientMessageID: input.ClientMessageID, eventType: input.Type, source: input.Source,
			runnerAttempt: &input.Claim.Attempt,
			payloadJSON:   input.PayloadJSON, frameEventID: input.FrameEventID,
			destinations: input.Destinations, createdAt: r.now().UTC(),
		})
		return err
	})
	return event, created, schemaError(err)
}

func (r *Repository) FinishRunner(ctx context.Context, input FinishRunnerInput) (Event, FinishReceipt, bool, error) {
	input.ClientMessageID = strings.TrimSpace(input.ClientMessageID)
	input.Status = strings.TrimSpace(input.Status)
	if input.ClientMessageID == "" || !terminalStatus(input.Status) {
		return Event{}, FinishReceipt{}, false, errors.New("client message id and terminal status are required")
	}
	if err := validateClaimInput(input.Claim); err != nil {
		return Event{}, FinishReceipt{}, false, err
	}
	if err := validatePayload(EventSourcePayload, input.PayloadJSON, nil); err != nil {
		return Event{}, FinishReceipt{}, false, err
	}
	statusDescription := frameTerminalStatusDescription(input.Status, input.PayloadJSON)
	var event Event
	var receipt FinishReceipt
	var created bool
	err := r.withImmediate(ctx, func(conn *sql.Conn) error {
		now := r.now().UTC()
		stream, err := getStreamConn(ctx, conn, input.Claim.StreamUID, input.Claim.OwnerID)
		if err != nil {
			return err
		}
		input.Destinations = terminalDeliveryDestinations(stream, input.Destinations)
		if existing, found, err := findEventByClientID(ctx, conn, input.Claim.StreamUID, input.ClientMessageID); err != nil {
			return err
		} else if found {
			if _, err := validateClaimConn(ctx, conn, input.Claim, now, false); err != nil {
				return err
			}
			event = existing
			if err := loadMatchingReceipt(ctx, conn, input, event, &receipt); err != nil {
				return err
			}
			if _, err := bindCommittedArtifactReferencesConn(ctx, conn, stream, input.Claim.Attempt, event.EventID, receipt.FinishedAt); err != nil {
				return err
			}
			return applyFrameTerminalStatusConn(ctx, conn, stream, input.Status, statusDescription, receipt.FinishedAt)
		}
		stream, err = validateClaimConn(ctx, conn, input.Claim, now, true)
		if err != nil {
			return err
		}
		event, created, err = appendEventConn(ctx, conn, stream, eventRecord{
			clientMessageID: input.ClientMessageID, eventType: "runner_finished", source: EventSourcePayload,
			runnerAttempt: &input.Claim.Attempt,
			payloadJSON:   input.PayloadJSON, destinations: input.Destinations, createdAt: now,
		})
		if err != nil {
			return err
		}
		if _, err := bindCommittedArtifactReferencesConn(ctx, conn, stream, input.Claim.Attempt, event.EventID, now); err != nil {
			return err
		}
		result, err := conn.ExecContext(ctx, `
			UPDATE transcript_runner_attempts SET status=?,phase='terminal',phase_sequence=phase_sequence+1,
				finished_event_id=?,finished_at=?
			WHERE stream_uid=? AND attempt=? AND runner_id=? AND status='running'`,
			input.Status, event.EventID, now, input.Claim.StreamUID, input.Claim.Attempt, input.Claim.RunnerID,
		)
		if err != nil {
			return err
		}
		if rows, err := result.RowsAffected(); err != nil || rows != 1 {
			return ErrClaimStale
		}
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO transcript_runner_receipts(stream_uid,attempt,event_id,status,finished_at)
			VALUES(?,?,?,?,?)`, input.Claim.StreamUID, input.Claim.Attempt, event.EventID, input.Status, now); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `
			UPDATE transcript_streams SET consumed_input_revision=MAX(consumed_input_revision,?) WHERE stream_uid=?`,
			input.Claim.ClaimedInputRevision, input.Claim.StreamUID); err != nil {
			return err
		}
		if err := applyFrameTerminalStatusConn(ctx, conn, stream, input.Status, statusDescription, now); err != nil {
			return err
		}
		receipt = FinishReceipt{StreamUID: input.Claim.StreamUID, Attempt: input.Claim.Attempt, EventID: event.EventID, Status: input.Status, FinishedAt: now}
		return nil
	})
	return event, receipt, created, schemaError(err)
}

func applyFrameTerminalStatusConn(
	ctx context.Context,
	conn *sql.Conn,
	stream Stream,
	status string,
	statusDescription string,
	finishedAt time.Time,
) error {
	if stream.Kind != StreamKindFrameRef {
		return nil
	}
	var current string
	if err := conn.QueryRowContext(ctx, `SELECT status FROM frames WHERE id=?`, stream.FrameID).Scan(&current); err != nil {
		return err
	}
	current = normalizeTerminalStatus(current)
	if terminalStatus(current) {
		if current != status {
			return ErrEventConflict
		}
	} else {
		updated, err := conn.ExecContext(ctx, `
			UPDATE frames SET status=?,updated_at=?
			WHERE id=? AND status NOT IN ('completed','failed','cancelled','canceled')`,
			status, finishedAt, stream.FrameID)
		if err != nil {
			return err
		}
		if rows, err := updated.RowsAffected(); err != nil || rows != 1 {
			if err != nil {
				return err
			}
			return ErrEventConflict
		}
	}
	description := ""
	if normalizeTerminalStatus(status) == "failed" {
		description = strings.TrimSpace(statusDescription)
	}
	_, err := conn.ExecContext(ctx, `
		INSERT INTO frame_runtime_metadata (frame_id,status_description,completed_at,context_data)
		VALUES(?,?,?,'{}')
		ON CONFLICT(frame_id) DO UPDATE SET
			status_description=excluded.status_description,completed_at=excluded.completed_at`,
		stream.FrameID, description, finishedAt)
	return err
}

func frameTerminalStatusDescription(status string, payload []byte) string {
	if normalizeTerminalStatus(status) != "failed" || len(payload) == 0 {
		return ""
	}
	var terminal struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(payload, &terminal); err != nil {
		return ""
	}
	return strings.TrimSpace(terminal.Detail)
}

type eventRecord struct {
	clientMessageID string
	eventType       string
	source          EventSource
	runnerAttempt   *int64
	payloadJSON     []byte
	frameEventID    *string
	destinations    []string
	createdAt       time.Time
}

func appendEventConn(ctx context.Context, conn *sql.Conn, stream Stream, record eventRecord) (Event, bool, error) {
	if existing, found, err := findEventByClientID(ctx, conn, stream.UID, record.clientMessageID); err != nil {
		return Event{}, false, err
	} else if found {
		if !eventMatches(existing, record) {
			return Event{}, false, ErrEventConflict
		}
		destinations, err := deliveryDestinations(ctx, conn, existing.StreamUID, existing.PublicationSeq)
		if err != nil {
			return Event{}, false, err
		}
		if !reflect.DeepEqual(destinations, normalizedDestinations(record.destinations)) {
			return Event{}, false, ErrEventConflict
		}
		if err := validateExistingEventBranchMembership(ctx, conn, existing.StreamUID, existing.EventID); err != nil {
			return Event{}, false, err
		}
		return existing, false, nil
	}
	event := Event{
		StreamUID: stream.UID, EventID: stream.NextEventID, PublicationSeq: stream.NextPublication,
		ClientMessageID: record.clientMessageID, Type: record.eventType, Source: record.source,
		RunnerAttempt: record.runnerAttempt,
		PayloadJSON:   append([]byte(nil), record.payloadJSON...), FrameEventID: record.frameEventID, CreatedAt: record.createdAt,
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO transcript_events(
			stream_uid,event_id,publication_seq,client_message_id,event_type,source,runner_attempt,payload_json,frame_event_id,created_at
		) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		event.StreamUID, event.EventID, event.PublicationSeq, event.ClientMessageID, event.Type,
		string(event.Source), event.RunnerAttempt, nullablePayload(event.PayloadJSON), event.FrameEventID, event.CreatedAt,
	); err != nil {
		return Event{}, false, err
	}
	if err := appendActiveBranchEventConn(ctx, conn, stream.UID, event.EventID); err != nil {
		return Event{}, false, err
	}
	for _, destination := range normalizedDestinations(record.destinations) {
		generation, err := activeDeliveryGenerationConn(ctx, conn, stream.UID, destination)
		if err != nil {
			return Event{}, false, err
		}
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO transcript_delivery_intents(
				stream_uid,publication_seq,destination,route_generation,status,updated_at
			) VALUES(?,?,?,?,'pending',?)`, stream.UID, event.PublicationSeq, destination, generation, event.CreatedAt); err != nil {
			return Event{}, false, err
		}
	}
	if _, err := conn.ExecContext(ctx, `
		UPDATE transcript_streams SET next_event_id=?,next_publication_seq=?,updated_at=? WHERE stream_uid=?`,
		event.EventID+1, event.PublicationSeq+1, event.CreatedAt, stream.UID); err != nil {
		return Event{}, false, err
	}
	return event, true, nil
}

func findEventByClientID(ctx context.Context, conn *sql.Conn, streamUID, clientID string) (Event, bool, error) {
	var event Event
	var source string
	var payload []byte
	var frameEventID sql.NullString
	var runnerAttempt sql.NullInt64
	err := conn.QueryRowContext(ctx, `
		SELECT stream_uid,event_id,publication_seq,client_message_id,event_type,source,runner_attempt,payload_json,frame_event_id,created_at
		FROM transcript_events WHERE stream_uid=? AND client_message_id=?`, streamUID, clientID,
	).Scan(&event.StreamUID, &event.EventID, &event.PublicationSeq, &event.ClientMessageID, &event.Type,
		&source, &runnerAttempt, &payload, &frameEventID, &event.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Event{}, false, nil
	}
	if err != nil {
		return Event{}, false, err
	}
	event.Source = EventSource(source)
	event.PayloadJSON = append([]byte(nil), payload...)
	if runnerAttempt.Valid {
		value := runnerAttempt.Int64
		event.RunnerAttempt = &value
	}
	if frameEventID.Valid {
		value := frameEventID.String
		event.FrameEventID = &value
	}
	return event, true, nil
}

func loadMatchingReceipt(ctx context.Context, conn *sql.Conn, input FinishRunnerInput, event Event, receipt *FinishReceipt) error {
	if event.Type != "runner_finished" || !jsonEqual(event.PayloadJSON, input.PayloadJSON) {
		return ErrEventConflict
	}
	destinations, err := deliveryDestinations(ctx, conn, event.StreamUID, event.PublicationSeq)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(destinations, normalizedDestinations(input.Destinations)) {
		return ErrEventConflict
	}
	err = conn.QueryRowContext(ctx, `
		SELECT stream_uid,attempt,event_id,status,finished_at FROM transcript_runner_receipts
		WHERE stream_uid=? AND attempt=?`, input.Claim.StreamUID, input.Claim.Attempt,
	).Scan(&receipt.StreamUID, &receipt.Attempt, &receipt.EventID, &receipt.Status, &receipt.FinishedAt)
	if err != nil || receipt.EventID != event.EventID || receipt.Status != input.Status {
		return ErrEventConflict
	}
	return nil
}

func deliveryDestinations(ctx context.Context, conn *sql.Conn, streamUID string, publicationSeq int64) ([]string, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT destination FROM transcript_delivery_intents
		WHERE stream_uid=? AND publication_seq=? ORDER BY destination`, streamUID, publicationSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func validateClaimConn(ctx context.Context, conn *sql.Conn, claim RunnerClaim, now time.Time, requireLive bool) (Stream, error) {
	if err := validateClaimInput(claim); err != nil {
		return Stream{}, err
	}
	stream, err := getStreamConn(ctx, conn, claim.StreamUID, claim.OwnerID)
	if err != nil {
		return Stream{}, err
	}
	claimDigest := sha256.Sum256([]byte(strings.TrimSpace(claim.ClaimToken)))
	err = validateRunnerAttemptAuthorityConn(ctx, conn, runnerAttemptAuthority{
		StreamUID: claim.StreamUID, RunnerID: claim.RunnerID, Attempt: claim.Attempt,
		ClaimDigest: claimDigest[:], ClaimedInputRevision: claim.ClaimedInputRevision,
		ResumeSource: claim.ResumeSource, ResumeCheckpoint: claim.ResumeCheckpoint,
	}, now, requireLive)
	if err != nil {
		return Stream{}, err
	}
	if requireLive {
		blocked, err := frameTerminalBlocksRunnerClaimConn(ctx, conn, stream)
		if err != nil {
			return Stream{}, err
		}
		if blocked {
			return Stream{}, ErrClaimStale
		}
	}
	return stream, nil
}

func getStreamConn(ctx context.Context, conn *sql.Conn, streamUID, ownerID string) (Stream, error) {
	stream, err := getRawStreamConn(ctx, conn, streamUID, ownerID)
	if err != nil {
		return Stream{}, err
	}
	if stream.Kind != StreamKindFrameRef {
		return stream, nil
	}
	var activeStreamUID string
	var activeEpoch int64
	err = conn.QueryRowContext(ctx, `SELECT active_stream_uid,active_epoch FROM transcript_frame_authority
		WHERE owner_id=? AND session_id=?`, stream.OwnerID, stream.SessionID).Scan(&activeStreamUID, &activeEpoch)
	if err != nil {
		return Stream{}, err
	}
	if activeStreamUID != stream.UID || activeEpoch != stream.Epoch {
		return Stream{}, ErrEventConflict
	}
	return stream, nil
}

func getRawStreamConn(ctx context.Context, conn *sql.Conn, streamUID, ownerID string) (Stream, error) {
	var stream Stream
	var kind string
	err := conn.QueryRowContext(ctx, `
		SELECT stream_uid,owner_id,external_id,session_id,kind,project_id,root_frame_id,frame_id,epoch,input_revision,consumed_input_revision,
			next_event_id,next_publication_seq,next_checkpoint_sequence,created_at,updated_at
		FROM transcript_streams WHERE stream_uid=?`, streamUID,
	).Scan(&stream.UID, &stream.OwnerID, &stream.ExternalID, &stream.SessionID, &kind, &stream.ProjectID, &stream.RootFrameID, &stream.FrameID, &stream.Epoch,
		&stream.InputRevision, &stream.ConsumedInputRevision, &stream.NextEventID, &stream.NextPublication, &stream.NextCheckpoint,
		&stream.CreatedAt, &stream.UpdatedAt)
	if err != nil {
		return Stream{}, err
	}
	if stream.OwnerID != ownerID {
		return Stream{}, ErrOwnerMismatch
	}
	stream.Kind = StreamKind(kind)
	return stream, nil
}

type ImmediateTransactionOutcome struct {
	CommitAttempted bool
	Committed       bool
}
