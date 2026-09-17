package transcript

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
)

func (r *Repository) CreateStream(ctx context.Context, input CreateStreamInput) (Stream, error) {
	input, err := normalizeCreateStreamInput(input)
	if err != nil {
		return Stream{}, err
	}
	var stream Stream
	err = r.withImmediate(ctx, func(conn *sql.Conn) error {
		var err error
		stream, err = createStreamConn(ctx, conn, input, r.now().UTC())
		return err
	})
	return stream, schemaError(err)
}

func (tx *ImmediateTransaction) CreateStream(ctx context.Context, input CreateStreamInput) (Stream, error) {
	if tx == nil || tx.repository == nil || tx.conn == nil {
		return Stream{}, errors.New("transcript transaction is required")
	}
	input, err := normalizeCreateStreamInput(input)
	if err != nil {
		return Stream{}, err
	}
	stream, err := createStreamConn(ctx, tx.conn, input, tx.repository.now().UTC())
	return stream, schemaError(err)
}

func normalizeCreateStreamInput(input CreateStreamInput) (CreateStreamInput, error) {
	input.UID = strings.TrimSpace(input.UID)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.ExternalID = strings.TrimSpace(input.ExternalID)
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.RootFrameID = strings.TrimSpace(input.RootFrameID)
	input.FrameID = strings.TrimSpace(input.FrameID)
	if input.UID == "" || input.OwnerID == "" || input.ExternalID == "" || !validStreamKind(input.Kind) || input.Epoch <= 0 {
		return CreateStreamInput{}, errors.New("valid stream uid, owner, external id, kind, and epoch are required")
	}
	if input.Kind == StreamKindFrameRef && (input.SessionID == "" || input.ProjectID == "" || input.RootFrameID == "" || input.FrameID == "") {
		return CreateStreamInput{}, errors.New("frame reference streams require session, project, root frame, and frame ids")
	}
	return input, nil
}

func createStreamConn(ctx context.Context, conn *sql.Conn, input CreateStreamInput, now time.Time) (Stream, error) {
	stream, err := createStreamRowsConn(ctx, conn, input, now)
	if err != nil {
		return Stream{}, err
	}
	if stream.Kind == StreamKindFrameRef {
		if err := ensureInitialFrameAuthorityConn(ctx, conn, stream, now); err != nil {
			return Stream{}, err
		}
	}
	return stream, nil
}

// createStreamRowsConn creates only the stream, route, and base branch rows.
// Callers must publish the matching Frame authority in the same transaction
// before returning. Keeping this primitive unexported prevents a partially
// initialized Frame stream from escaping the repository boundary.
func createStreamRowsConn(ctx context.Context, conn *sql.Conn, input CreateStreamInput, now time.Time) (Stream, error) {
	var stream Stream
	if input.SessionID != "" {
		rows, err := conn.QueryContext(ctx, `SELECT stream_uid,owner_id,kind FROM transcript_streams WHERE session_id=?`, input.SessionID)
		if err != nil {
			return Stream{}, err
		}
		type sessionOwner struct{ streamUID, ownerID, kind string }
		existing := make([]sessionOwner, 0, 2)
		for rows.Next() {
			var item sessionOwner
			if err := rows.Scan(&item.streamUID, &item.ownerID, &item.kind); err != nil {
				_ = rows.Close()
				return Stream{}, err
			}
			existing = append(existing, item)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return Stream{}, err
		}
		if err := rows.Close(); err != nil {
			return Stream{}, err
		}
		for _, item := range existing {
			if item.ownerID != input.OwnerID {
				return Stream{}, ErrOwnerMismatch
			}
			if item.streamUID != input.UID &&
				(input.Kind == StreamKindStandalone || input.Kind == StreamKindTaskRun ||
					StreamKind(item.kind) == StreamKindStandalone || StreamKind(item.kind) == StreamKindTaskRun) {
				return Stream{}, ErrEventConflict
			}
		}
	}
	if input.Kind == StreamKindFrameRef {
		var ownerID, projectID, rootFrameID string
		err := conn.QueryRowContext(ctx, `
				SELECT project.user_id,frame.project_id,frame.root_frame_id
				FROM projects project JOIN frames frame ON frame.project_id=project.id
				WHERE project.id=? AND frame.id=?`, input.ProjectID, input.FrameID,
		).Scan(&ownerID, &projectID, &rootFrameID)
		if errors.Is(err, sql.ErrNoRows) {
			return Stream{}, ErrEventConflict
		}
		if err != nil {
			return Stream{}, err
		}
		if ownerID != input.OwnerID {
			return Stream{}, ErrOwnerMismatch
		}
		if projectID != input.ProjectID || rootFrameID != input.RootFrameID {
			return Stream{}, ErrEventConflict
		}
	}
	_, err := conn.ExecContext(ctx, `
			INSERT INTO transcript_streams(
				stream_uid, owner_id, external_id, session_id, kind, project_id, root_frame_id, frame_id, epoch,
				input_revision, consumed_input_revision, next_event_id, next_publication_seq, next_checkpoint_sequence,
				created_at, updated_at
			) VALUES(?,?,?,?,?,?,?,?,?,0,0,1,1,1,?,?)
			ON CONFLICT(stream_uid) DO NOTHING`,
		input.UID, input.OwnerID, input.ExternalID, input.SessionID, string(input.Kind), input.ProjectID, input.RootFrameID, input.FrameID, input.Epoch, now, now,
	)
	if err != nil {
		return Stream{}, err
	}
	stream, err = getRawStreamConn(ctx, conn, input.UID, input.OwnerID)
	if err != nil {
		return Stream{}, err
	}
	if stream.ExternalID != input.ExternalID || stream.SessionID != input.SessionID || stream.Kind != input.Kind || stream.ProjectID != input.ProjectID ||
		stream.RootFrameID != input.RootFrameID || stream.FrameID != input.FrameID || stream.Epoch != input.Epoch {
		return Stream{}, ErrEventConflict
	}
	_, err = conn.ExecContext(ctx, `
			INSERT INTO transcript_delivery_routes(stream_uid,destination,current_generation,status,updated_at)
			VALUES(?, 'ws', 1, 'active', ?) ON CONFLICT(stream_uid,destination) DO NOTHING`, input.UID, now)
	if err != nil {
		return Stream{}, err
	}
	if err := ensureBaseBranchConn(ctx, conn, stream, now); err != nil {
		return Stream{}, err
	}
	return stream, nil
}

func ensureInitialFrameAuthorityConn(ctx context.Context, conn *sql.Conn, stream Stream, now time.Time) error {
	var activeStreamUID string
	var activeEpoch int64
	err := conn.QueryRowContext(ctx, `SELECT active_stream_uid,active_epoch
		FROM transcript_frame_authority WHERE owner_id=? AND session_id=?`,
		stream.OwnerID, stream.SessionID).Scan(&activeStreamUID, &activeEpoch)
	if err == nil {
		if activeStreamUID == stream.UID && activeEpoch != stream.Epoch {
			return ErrEventConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if stream.Epoch != 1 {
		return ErrEventConflict
	}
	var branchID string
	var branchGeneration int64
	if err := conn.QueryRowContext(ctx, `SELECT active_branch_id,generation
		FROM transcript_branch_state WHERE stream_uid=?`, stream.UID).Scan(&branchID, &branchGeneration); err != nil {
		return err
	}
	if branchGeneration != 1 || branchID == "" {
		return ErrEventConflict
	}
	importedCount, sourceSHA, err := importPayloadGenesisHistoryConn(ctx, conn, stream)
	if err != nil {
		return err
	}
	sourceKind := "empty"
	if importedCount > 0 {
		sourceKind = "frame_import"
	}
	genesisID := payloadGenesisID(stream, sourceKind, importedCount, sourceSHA)
	if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_payload_genesis_receipts(
		genesis_id,stream_uid,owner_id,session_id,epoch,active_branch_id,branch_generation,
		authority_generation,source_kind,source_stream_uid,source_epoch,
		source_event_count,source_sha256,status,created_at)
		VALUES(?,?,?,?,?,?,1,1,?,NULL,NULL,?,?,'active',?)`,
		genesisID[:], stream.UID, stream.OwnerID, stream.SessionID, stream.Epoch, branchID,
		sourceKind, importedCount, sourceSHA, now); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO transcript_frame_authority(
		owner_id,session_id,active_stream_uid,active_epoch,authority_generation,
		read_authority,write_authority,activation_id,genesis_id,updated_at)
		VALUES(?,?,?,?,1,'transcript_payload_v1','transcript_payload_v1',NULL,?,?)`,
		stream.OwnerID, stream.SessionID, stream.UID, stream.Epoch, genesisID[:], now)
	return err
}

func payloadGenesisID(stream Stream, sourceKind string, importedCount int64, sourceSHA []byte) [sha256.Size]byte {
	return sha256.Sum256([]byte(HistoryPayloadGenesisContractID + "\x00" + sourceKind + "\x00" + stream.OwnerID + "\x00" +
		stream.SessionID + "\x00" + stream.UID + "\x00" + strconv.FormatInt(stream.Epoch, 10) + "\x001\x00" +
		strconv.FormatInt(importedCount, 10) + "\x00" + string(sourceSHA)))
}

// importPayloadGenesisHistoryConn converts pre-existing, unambiguous Frame
// text messages into immutable payload events before payload authority is
// published. Rich or tool-shaped legacy facts require the governed history
// cutover classifier and therefore fail this creation transaction closed.
func importPayloadGenesisHistoryConn(
	ctx context.Context, conn *sql.Conn, stream Stream,
) (int64, []byte, error) {
	type sourceEvent struct {
		id, eventType string
		sequence      int64
		payload       []byte
		createdAt     time.Time
	}
	digest := sha256.New()
	var importedCount, afterSequence int64
	for {
		var source sourceEvent
		err := conn.QueryRowContext(ctx, `SELECT id,sequence,event_type,payload,created_at
			FROM frame_events WHERE frame_id=? AND (?=0 OR sequence>?) AND event_type IN (
				'message','user_message','assistant_message','system_message','tool_use','tool_result','ask_user_answer'
			) ORDER BY sequence,id LIMIT 1`, stream.FrameID, importedCount, afterSequence).Scan(
			&source.id, &source.sequence, &source.eventType, &source.payload, &source.createdAt,
		)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return 0, nil, err
		}
		if source.sequence <= afterSequence || strings.TrimSpace(source.id) == "" || len(source.id) > 512 ||
			len(source.payload) == 0 || len(source.payload) > maxEventPayloadBytes {
			return 0, nil, ErrEventConflict
		}
		rawEventType := source.eventType
		source.eventType, err = payloadGenesisFrameMessage(rawEventType, source.payload)
		if err != nil {
			return 0, nil, ErrEventConflict
		}
		writePayloadGenesisDigestField(digest, []byte(source.id))
		var number [8]byte
		binary.BigEndian.PutUint64(number[:], uint64(source.sequence))
		_, _ = digest.Write(number[:])
		writePayloadGenesisDigestField(digest, []byte(rawEventType))
		writePayloadGenesisDigestField(digest, source.payload)
		writePayloadGenesisDigestField(digest, []byte(source.createdAt.UTC().Format(time.RFC3339Nano)))
		event, created, err := appendEventConn(ctx, conn, stream, eventRecord{
			clientMessageID: "payload-genesis:" + source.id,
			eventType:       source.eventType,
			source:          EventSourcePayload,
			payloadJSON:     source.payload,
			createdAt:       source.createdAt,
		})
		if err != nil || !created || event.RunnerAttempt != nil || event.FrameEventID != nil {
			if err != nil {
				return 0, nil, err
			}
			return 0, nil, ErrEventConflict
		}
		stream, err = getRawStreamConn(ctx, conn, stream.UID, stream.OwnerID)
		if err != nil {
			return 0, nil, err
		}
		afterSequence = source.sequence
		importedCount++
	}
	return importedCount, digest.Sum(nil), nil
}

func payloadGenesisFrameMessage(rawEventType string, payload []byte) (string, error) {
	if len(payload) == 0 || len(payload) > maxEventPayloadBytes {
		return "", ErrEventConflict
	}
	var message map[string]any
	if err := json.Unmarshal(payload, &message); err != nil || message == nil {
		return "", ErrEventConflict
	}
	role, ok := message["role"].(string)
	role = strings.ToLower(strings.TrimSpace(role))
	if !ok || (role != "user" && role != "assistant" && role != "system") ||
		!payloadGenesisPlainTextMessage(message) {
		return "", ErrEventConflict
	}
	expectedType := role + "_message"
	if rawEventType != "message" && rawEventType != expectedType {
		return "", ErrEventConflict
	}
	return "history_" + expectedType, nil
}

func payloadGenesisPlainTextMessage(message map[string]any) bool {
	if content, exists := message["content"]; exists {
		switch typed := content.(type) {
		case string:
		case []any:
			if len(typed) == 0 {
				return false
			}
			for _, raw := range typed {
				block, ok := raw.(map[string]any)
				if !ok || len(block) != 2 || block["type"] != "text" {
					return false
				}
				if _, ok := block["text"].(string); !ok {
					return false
				}
			}
		default:
			return false
		}
	}
	if text, exists := message["text"]; exists {
		if _, ok := text.(string); !ok {
			return false
		}
	}
	_, hasContent := message["content"]
	_, hasText := message["text"]
	return hasContent || hasText
}

func writePayloadGenesisDigestField(digest io.Writer, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write(value)
}
