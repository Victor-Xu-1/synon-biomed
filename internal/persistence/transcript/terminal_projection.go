package transcript

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

func (r *Repository) GetTerminalProjection(
	ctx context.Context,
	ownerID, streamUID string,
	eventID int64,
) (TerminalProjection, error) {
	db := r.readDatabase()
	if db == nil {
		return TerminalProjection{}, ErrSchemaUnavailable
	}
	ownerID = strings.TrimSpace(ownerID)
	streamUID = strings.TrimSpace(streamUID)
	if ownerID == "" || streamUID == "" || eventID <= 0 {
		return TerminalProjection{}, errors.New("owner, stream, and positive terminal event id are required")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return TerminalProjection{}, schemaError(err)
	}
	defer func() { _ = tx.Rollback() }()
	projection, err := terminalProjection(ctx, tx, ownerID, streamUID, eventID)
	if err != nil {
		return TerminalProjection{}, schemaError(err)
	}
	if err := tx.Commit(); err != nil {
		return TerminalProjection{}, schemaError(err)
	}
	return projection, nil
}

func (r *Repository) RecoverTerminalDeliveryIntents(ctx context.Context, ownerID string) (int64, error) {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return 0, errors.New("owner is required")
	}
	var recovered int64
	err := r.withImmediate(ctx, func(conn *sql.Conn) error {
		rows, err := conn.QueryContext(ctx, `
			SELECT event.stream_uid,event.event_id
			FROM transcript_runner_receipts receipt
			JOIN transcript_events event ON event.stream_uid=receipt.stream_uid AND event.event_id=receipt.event_id
			JOIN transcript_streams stream ON stream.stream_uid=event.stream_uid
			JOIN transcript_frame_authority authority ON authority.active_stream_uid=stream.stream_uid
				AND authority.owner_id=stream.owner_id AND authority.session_id=stream.session_id
				AND authority.active_epoch=stream.epoch
			LEFT JOIN transcript_history_activation_receipts activation
				ON activation.target_stream_uid=stream.stream_uid
			LEFT JOIN transcript_delivery_intents intent
				ON intent.stream_uid=event.stream_uid AND intent.publication_seq=event.publication_seq AND intent.destination='ws'
			WHERE stream.owner_id=? AND stream.kind='frame_ref' AND stream.session_id!=''
				AND event.event_type='runner_finished' AND receipt.status IN ('completed','failed','cancelled')
				AND (activation.activation_id IS NULL OR event.publication_seq>activation.target_through_publication_seq)
				AND intent.stream_uid IS NULL
			ORDER BY event.stream_uid,event.event_id`, ownerID)
		if err != nil {
			return err
		}
		type missingTerminal struct {
			streamUID string
			eventID   int64
		}
		missing := []missingTerminal{}
		for rows.Next() {
			var item missingTerminal
			if err := rows.Scan(&item.streamUID, &item.eventID); err != nil {
				_ = rows.Close()
				return err
			}
			missing = append(missing, item)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, item := range missing {
			projection, err := terminalProjection(ctx, conn, ownerID, item.streamUID, item.eventID)
			if err != nil {
				return err
			}
			generation, err := activeDeliveryGenerationConn(ctx, conn, item.streamUID, "ws")
			if err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, `
				INSERT INTO transcript_delivery_intents(
					stream_uid,publication_seq,destination,route_generation,status,updated_at
				) VALUES(?,?, 'ws',?,'pending',?)`,
				projection.StreamUID, projection.PublicationSeq, generation, projection.CreatedAt); err != nil {
				return err
			}
			recovered++
		}
		return nil
	})
	return recovered, schemaError(err)
}

func (r *Repository) ListTerminalProjectionOwners(ctx context.Context, limit int) ([]string, error) {
	if r == nil || r.db == nil {
		return nil, ErrSchemaUnavailable
	}
	if limit <= 0 || limit > 1000 {
		return nil, errors.New("bounded terminal owner limit is required")
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT DISTINCT stream.owner_id
		FROM transcript_runner_receipts receipt
		JOIN transcript_events event
			ON event.stream_uid=receipt.stream_uid AND event.event_id=receipt.event_id
		JOIN transcript_streams stream ON stream.stream_uid=receipt.stream_uid
		JOIN transcript_frame_authority authority ON authority.active_stream_uid=stream.stream_uid
			AND authority.owner_id=stream.owner_id AND authority.session_id=stream.session_id
			AND authority.active_epoch=stream.epoch
		LEFT JOIN transcript_history_activation_receipts activation
			ON activation.target_stream_uid=stream.stream_uid
		LEFT JOIN transcript_delivery_intents intent
			ON intent.stream_uid=event.stream_uid AND intent.publication_seq=event.publication_seq AND intent.destination='ws'
		WHERE stream.kind='frame_ref' AND stream.session_id!=''
			AND event.event_type='runner_finished' AND receipt.status IN ('completed','failed','cancelled')
			AND (activation.activation_id IS NULL OR event.publication_seq>activation.target_through_publication_seq)
			AND intent.stream_uid IS NULL
		ORDER BY stream.owner_id LIMIT ?`, limit)
	if err != nil {
		return nil, schemaError(err)
	}
	defer rows.Close()
	owners := make([]string, 0, limit)
	for rows.Next() {
		var ownerID string
		if err := rows.Scan(&ownerID); err != nil {
			return nil, schemaError(err)
		}
		if ownerID = strings.TrimSpace(ownerID); ownerID != "" {
			owners = append(owners, ownerID)
		}
	}
	return owners, schemaError(rows.Err())
}

func terminalProjection(
	ctx context.Context,
	queryer transcriptQueryer,
	ownerID, streamUID string,
	eventID int64,
) (TerminalProjection, error) {
	var projection TerminalProjection
	var kind, eventType, receiptStatus string
	var payload []byte
	err := queryer.QueryRowContext(ctx, `
		SELECT stream.stream_uid,stream.owner_id,stream.kind,stream.session_id,stream.project_id,stream.root_frame_id,stream.frame_id,
			event.runner_attempt,event.event_id,event.publication_seq,event.event_type,event.payload_json,event.created_at,receipt.status,
			EXISTS(
				SELECT 1 FROM transcript_runner_attempts later
				WHERE later.stream_uid=event.stream_uid
					AND later.attempt>event.runner_attempt
					AND later.claimed_input_revision=terminal_attempt.claimed_input_revision
			)
		FROM transcript_streams stream
		JOIN transcript_events event ON event.stream_uid=stream.stream_uid
		JOIN transcript_runner_receipts receipt
			ON receipt.stream_uid=event.stream_uid AND receipt.event_id=event.event_id AND receipt.attempt=event.runner_attempt
		JOIN transcript_runner_attempts terminal_attempt
			ON terminal_attempt.stream_uid=event.stream_uid AND terminal_attempt.attempt=event.runner_attempt
		WHERE stream.stream_uid=? AND event.event_id=?`, streamUID, eventID).Scan(
		&projection.StreamUID, &projection.OwnerID, &kind, &projection.SessionID, &projection.ProjectID,
		&projection.RootFrameID, &projection.FrameID, &projection.Attempt, &projection.EventID,
		&projection.PublicationSeq, &eventType, &payload, &projection.CreatedAt, &receiptStatus, &projection.Superseded,
	)
	if err != nil {
		return TerminalProjection{}, err
	}
	if projection.OwnerID != ownerID {
		return TerminalProjection{}, ErrOwnerMismatch
	}
	if kind != string(StreamKindFrameRef) || eventType != "runner_finished" || projection.SessionID == "" || projection.ProjectID == "" ||
		projection.RootFrameID == "" || projection.FrameID == "" {
		return TerminalProjection{}, ErrTerminalProjectionUnavailable
	}
	projection.TerminalStatus, projection.StreamType, err = MapTerminalStatus(receiptStatus)
	if err != nil {
		return TerminalProjection{}, err
	}
	projection.Detail, projection.ReasonCode, err = terminalPayloadDetail(payload, projection.TerminalStatus)
	if err != nil {
		return TerminalProjection{}, err
	}
	projection.ArtifactReferences, err = listArtifactReferencesConn(ctx, queryer, streamUID, projection.Attempt)
	return projection, err
}

// MapTerminalStatus is the single server-side mapping from durable runner
// terminal state to the public message stream contract.
func MapTerminalStatus(status string) (string, string, error) {
	status = normalizeTerminalStatus(status)
	switch status {
	case "completed", "cancelled":
		return status, "finish", nil
	case "failed":
		return status, "error", nil
	default:
		return "", "", ErrTerminalProjectionUnavailable
	}
}

func normalizeTerminalStatus(status string) string {
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "canceled" {
		return "cancelled"
	}
	return status
}

func terminalPayloadDetail(payload []byte, terminalStatus string) (string, string, error) {
	var value map[string]any
	if len(payload) == 0 || !json.Valid(payload) || json.Unmarshal(payload, &value) != nil {
		return "", "", ErrEventConflict
	}
	if rawStatus, ok := value["status"]; ok {
		status, ok := rawStatus.(string)
		if !ok || normalizeTerminalStatus(status) != terminalStatus {
			return "", "", ErrEventConflict
		}
	}
	reasonCode := ""
	for _, key := range []string{"reason_code", "reasonCode"} {
		if value, ok := value[key].(string); ok {
			reasonCode = strings.TrimSpace(value)
			if validReasonCode(reasonCode) {
				break
			}
			reasonCode = ""
		}
	}
	for _, key := range []string{"detail", "text"} {
		if detail, ok := value[key].(string); ok {
			return strings.TrimSpace(detail), reasonCode, nil
		}
	}
	// Cancellation reason codes are durable machine state for diagnostics and
	// resume semantics, not assistant-authored transcript content.
	if terminalStatus == "cancelled" {
		return "", reasonCode, nil
	}
	return reasonCode, reasonCode, nil
}

var _ transcriptQueryer = (*sql.Tx)(nil)
var _ transcriptQueryer = (*sql.Conn)(nil)
