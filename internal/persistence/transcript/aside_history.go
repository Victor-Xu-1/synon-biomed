package transcript

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

const asideForkNotice = "[System] This session was forked from another session at this point. Background kernels, in-memory variables, and running jobs from the original session are not available here."

type CloneAsideHistoryInput struct {
	SourceStreamUID   string
	TargetStreamUID   string
	OwnerID           string
	IncludeForkNotice bool
	StripThinking     bool
}

type asideHistorySeed struct {
	eventID     int64
	eventType   string
	payloadJSON []byte
	createdAt   time.Time
}

func (tx *ImmediateTransaction) CountAsideHistory(
	ctx context.Context,
	streamUID, ownerID string,
	stripThinking bool,
) (int, error) {
	if tx == nil || tx.repository == nil || tx.conn == nil {
		return 0, errors.New("transcript transaction is required")
	}
	streamUID, ownerID = strings.TrimSpace(streamUID), strings.TrimSpace(ownerID)
	if streamUID == "" || ownerID == "" {
		return 0, errors.New("stream and owner are required")
	}
	stream, err := getStreamConn(ctx, tx.conn, streamUID, ownerID)
	if err != nil {
		return 0, schemaError(err)
	}
	if err := validateAsideHistorySourceConn(ctx, tx, stream); err != nil {
		return 0, schemaError(err)
	}
	seeds, err := loadAsideHistorySeedsConn(ctx, tx, stream)
	if err != nil {
		return 0, schemaError(err)
	}
	count := 0
	for _, seed := range seeds {
		if stripThinking {
			seed.payloadJSON, err = stripAsideHistoryThinking(seed.payloadJSON)
			if err != nil {
				return 0, schemaError(err)
			}
			if len(seed.payloadJSON) == 0 {
				continue
			}
		}
		count++
	}
	return count, nil
}

func (tx *ImmediateTransaction) CloneAsideHistory(
	ctx context.Context,
	input CloneAsideHistoryInput,
) ([]Event, error) {
	if tx == nil || tx.repository == nil || tx.conn == nil {
		return nil, errors.New("transcript transaction is required")
	}
	input.SourceStreamUID = strings.TrimSpace(input.SourceStreamUID)
	input.TargetStreamUID = strings.TrimSpace(input.TargetStreamUID)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	if input.SourceStreamUID == "" || input.TargetStreamUID == "" || input.OwnerID == "" ||
		input.SourceStreamUID == input.TargetStreamUID {
		return nil, errors.New("complete aside history identity is required")
	}
	source, err := getStreamConn(ctx, tx.conn, input.SourceStreamUID, input.OwnerID)
	if err != nil {
		return nil, schemaError(err)
	}
	target, err := getStreamConn(ctx, tx.conn, input.TargetStreamUID, input.OwnerID)
	if err != nil {
		return nil, schemaError(err)
	}
	if source.Kind != StreamKindFrameRef || target.Kind != StreamKindFrameRef ||
		source.ProjectID == "" || source.ProjectID != target.ProjectID || source.RootFrameID == target.RootFrameID {
		return nil, ErrEventConflict
	}
	if err := validateAsideHistorySourceConn(ctx, tx, source); err != nil {
		return nil, schemaError(err)
	}
	targetAuthority, err := loadCloneSourceFrameAuthorityConn(ctx, tx.conn, target)
	if err != nil || !targetAuthority.TranscriptPayloadActive() {
		if err != nil {
			return nil, schemaError(err)
		}
		return nil, ErrEventConflict
	}
	if err := validateEmptyCloneTargetConn(ctx, tx.conn, target); err != nil {
		return nil, schemaError(err)
	}
	seeds, err := loadAsideHistorySeedsConn(ctx, tx, source)
	if err != nil {
		return nil, schemaError(err)
	}
	events := make([]Event, 0, len(seeds)+1)
	for _, seed := range seeds {
		if input.StripThinking {
			seed.payloadJSON, err = stripAsideHistoryThinking(seed.payloadJSON)
			if err != nil {
				return nil, err
			}
			if len(seed.payloadJSON) == 0 {
				continue
			}
		}
		eventType := seed.eventType
		switch eventType {
		case "user_message":
			eventType = "history_user_message"
		case "assistant_message":
			eventType = "history_assistant_message"
		}
		digest := sha256.Sum256([]byte("synon.transcript.aside-history.v1\x00" + source.UID + "\x00" +
			strconv.FormatInt(seed.eventID, 10) + "\x00" + target.UID))
		event, created, err := appendEventConn(ctx, tx.conn, target, eventRecord{
			clientMessageID: "aside-history:" + hex.EncodeToString(digest[:]),
			eventType:       eventType, source: EventSourcePayload,
			payloadJSON: seed.payloadJSON, createdAt: seed.createdAt,
		})
		if err != nil || !created {
			if err != nil {
				return nil, schemaError(err)
			}
			return nil, ErrEventConflict
		}
		events = append(events, event)
		target, err = getStreamConn(ctx, tx.conn, target.UID, target.OwnerID)
		if err != nil {
			return nil, schemaError(err)
		}
	}
	if input.IncludeForkNotice {
		payload, err := json.Marshal(map[string]any{
			"role": "user", "content": asideForkNotice, "text": asideForkNotice, "_harness_notice": true,
		})
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256([]byte("synon.transcript.aside-history.v1\x00notice\x00" + source.UID + "\x00" + target.UID))
		event, created, err := appendEventConn(ctx, tx.conn, target, eventRecord{
			clientMessageID: "aside-history:" + hex.EncodeToString(digest[:]),
			eventType:       "history_user_message", source: EventSourcePayload,
			payloadJSON: payload, createdAt: tx.repository.now().UTC(),
		})
		if err != nil || !created {
			if err != nil {
				return nil, schemaError(err)
			}
			return nil, ErrEventConflict
		}
		events = append(events, event)
	}
	return events, nil
}

func validateAsideHistorySourceConn(ctx context.Context, tx *ImmediateTransaction, stream Stream) error {
	authority, err := loadCloneSourceFrameAuthorityConn(ctx, tx.conn, stream)
	if err != nil {
		return err
	}
	if !authority.TranscriptPayloadActive() {
		return ErrEventConflict
	}
	return nil
}

func loadAsideHistorySeedsConn(
	ctx context.Context,
	tx *ImmediateTransaction,
	stream Stream,
) ([]asideHistorySeed, error) {
	rows, err := tx.conn.QueryContext(ctx, `
		SELECT event.event_id,event.event_type,event.source,event.payload_json,event.created_at
		FROM transcript_branch_state state
		JOIN transcript_branch_events membership
			ON membership.stream_uid=state.stream_uid AND membership.branch_id=state.active_branch_id
		JOIN transcript_events event
			ON event.stream_uid=membership.stream_uid AND event.event_id=membership.event_id
		WHERE state.stream_uid=? AND event.event_type IN (
			'user_message','user_input_response','assistant_message',
			'history_user_message','history_assistant_message','history_system_message'
		)
		ORDER BY membership.ordinal`, stream.UID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seeds := make([]asideHistorySeed, 0)
	for rows.Next() {
		var seed asideHistorySeed
		var source string
		if err := rows.Scan(&seed.eventID, &seed.eventType, &source, &seed.payloadJSON, &seed.createdAt); err != nil {
			return nil, err
		}
		if seed.eventID <= 0 || source != string(EventSourcePayload) ||
			validatePayload(EventSourcePayload, seed.payloadJSON, nil) != nil {
			return nil, ErrEventConflict
		}
		seeds = append(seeds, seed)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return seeds, nil
}

func stripAsideHistoryThinking(payload []byte) ([]byte, error) {
	var message map[string]any
	if json.Unmarshal(payload, &message) != nil || message == nil {
		return nil, ErrEventConflict
	}
	if refused, _ := message["_refusal"].(bool); refused {
		return append([]byte(nil), payload...), nil
	}
	blocks, ok := message["content"].([]any)
	if !ok {
		return append([]byte(nil), payload...), nil
	}
	filtered := make([]any, 0, len(blocks))
	for _, raw := range blocks {
		block, _ := raw.(map[string]any)
		if block["type"] == "thinking" || block["type"] == "redacted_thinking" {
			continue
		}
		filtered = append(filtered, raw)
	}
	if len(filtered) == 0 {
		return nil, nil
	}
	message["content"] = filtered
	result, err := json.Marshal(message)
	if err != nil {
		return nil, err
	}
	return result, nil
}
