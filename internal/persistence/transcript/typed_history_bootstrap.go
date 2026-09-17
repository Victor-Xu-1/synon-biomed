package transcript

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

var errTypedHistoryBootstrapUnsupported = errors.New("typed history bootstrap shape is unsupported")

type typedHistoryBootstrapEvent struct {
	id, rawType, eventType string
	sequence               int64
	payload                []byte
	createdAt              time.Time
}

type typedHistoryBootstrapPlan struct {
	events                                          []typedHistoryBootstrapEvent
	sourceThroughSequence                           int64
	textBlocks, toolUses, toolResults               int
	sourceDigest, historyDigest, materializedDigest []byte
}

func planTypedHistoryBootstrapConn(
	ctx context.Context,
	conn *sql.Conn,
	frameID string,
) (typedHistoryBootstrapPlan, error) {
	rows, err := conn.QueryContext(ctx, `SELECT id,sequence,event_type,payload,created_at
		FROM frame_events WHERE frame_id=? AND event_type IN (
			'message','user_message','assistant_message','system_message','tool_use','tool_result','ask_user_answer'
		) ORDER BY sequence,id`, frameID)
	if err != nil {
		return typedHistoryBootstrapPlan{}, err
	}
	defer rows.Close()
	plan := typedHistoryBootstrapPlan{}
	openTools := map[string]bool{}
	resolvedTools := map[string]bool{}
	sourceHash := sha256.New()
	historyHash := sha256.New()
	materializedHash := sha256.New()
	for rows.Next() {
		var event typedHistoryBootstrapEvent
		if err := rows.Scan(&event.id, &event.sequence, &event.rawType, &event.payload, &event.createdAt); err != nil {
			return typedHistoryBootstrapPlan{}, err
		}
		event.id = strings.TrimSpace(event.id)
		if event.id == "" || len(event.id) > 512 || event.sequence <= plan.sourceThroughSequence ||
			len(event.payload) == 0 || len(event.payload) > maxEventPayloadBytes ||
			jsonHasDuplicateObjectKeys(event.payload) {
			return typedHistoryBootstrapPlan{}, errTypedHistoryBootstrapUnsupported
		}
		if event.rawType == "tool_use" || event.rawType == "tool_result" || event.rawType == "ask_user_answer" {
			return typedHistoryBootstrapPlan{}, errTypedHistoryBootstrapUnsupported
		}
		var message map[string]any
		if json.Unmarshal(event.payload, &message) != nil || message == nil || ordinaryPayloadContainsAskUserFact(event.payload) {
			return typedHistoryBootstrapPlan{}, errTypedHistoryBootstrapUnsupported
		}
		role, ok := exactTypedHistoryProtocolString(message["role"])
		if !ok || (role != "user" && role != "assistant" && role != "system") ||
			(event.rawType != "message" && event.rawType != role+"_message") {
			return typedHistoryBootstrapPlan{}, errTypedHistoryBootstrapUnsupported
		}
		blocks, ok := message["content"].([]any)
		if !ok || len(blocks) == 0 {
			return typedHistoryBootstrapPlan{}, errTypedHistoryBootstrapUnsupported
		}
		for _, rawBlock := range blocks {
			block, ok := rawBlock.(map[string]any)
			if !ok {
				return typedHistoryBootstrapPlan{}, errTypedHistoryBootstrapUnsupported
			}
			blockType, typeOK := exactTypedHistoryProtocolString(block["type"])
			if !typeOK {
				return typedHistoryBootstrapPlan{}, errTypedHistoryBootstrapUnsupported
			}
			switch blockType {
			case "text":
				if !hasOnlyAskUserKeys(block, "type", "text") {
					return typedHistoryBootstrapPlan{}, errTypedHistoryBootstrapUnsupported
				}
				if _, ok := block["text"].(string); !ok {
					return typedHistoryBootstrapPlan{}, errTypedHistoryBootstrapUnsupported
				}
				plan.textBlocks++
			case "tool_use":
				if role != "assistant" || !hasOnlyAskUserKeys(block, "type", "id", "name", "input") {
					return typedHistoryBootstrapPlan{}, errTypedHistoryBootstrapUnsupported
				}
				toolID, idOK := exactTypedHistoryProtocolString(block["id"])
				toolName, nameOK := exactTypedHistoryProtocolString(block["name"])
				_, inputOK := block["input"].(map[string]any)
				_, askUser := CanonicalAskUserToolNameV1(toolName)
				if !idOK || len(toolID) > 512 || !nameOK || askUser || !inputOK || openTools[toolID] {
					return typedHistoryBootstrapPlan{}, errTypedHistoryBootstrapUnsupported
				}
				openTools[toolID] = true
				plan.toolUses++
			case "tool_result":
				if role != "user" || !hasOnlyAskUserKeys(block, "type", "tool_use_id", "content", "is_error") {
					return typedHistoryBootstrapPlan{}, errTypedHistoryBootstrapUnsupported
				}
				toolID, idOK := exactTypedHistoryProtocolString(block["tool_use_id"])
				if !idOK || !openTools[toolID] || resolvedTools[toolID] {
					return typedHistoryBootstrapPlan{}, errTypedHistoryBootstrapUnsupported
				}
				if _, ok := block["content"].(string); !ok {
					return typedHistoryBootstrapPlan{}, errTypedHistoryBootstrapUnsupported
				}
				if value, found := block["is_error"]; found {
					if _, ok := value.(bool); !ok {
						return typedHistoryBootstrapPlan{}, errTypedHistoryBootstrapUnsupported
					}
				}
				resolvedTools[toolID] = true
				plan.toolResults++
			default:
				return typedHistoryBootstrapPlan{}, errTypedHistoryBootstrapUnsupported
			}
		}
		event.eventType = "history_" + role + "_message"
		writeTypedHistorySourceDigest(sourceHash, event)
		writePayloadGenesisDigestField(historyHash, []byte(event.id))
		writePayloadGenesisDigestField(historyHash, []byte(event.eventType))
		writePayloadGenesisDigestField(historyHash, event.payload)
		eventIndex := int64(len(plan.events) + 1)
		writeTypedHistoryMaterializedDigest(materializedHash, eventIndex, event)
		plan.events = append(plan.events, event)
		plan.sourceThroughSequence = event.sequence
	}
	if err := rows.Err(); err != nil {
		return typedHistoryBootstrapPlan{}, err
	}
	if len(plan.events) == 0 || plan.toolUses == 0 || plan.toolUses != plan.toolResults || len(openTools) != len(resolvedTools) {
		return typedHistoryBootstrapPlan{}, errTypedHistoryBootstrapUnsupported
	}
	for toolID := range openTools {
		if !resolvedTools[toolID] {
			return typedHistoryBootstrapPlan{}, errTypedHistoryBootstrapUnsupported
		}
	}
	plan.sourceDigest = sourceHash.Sum(nil)
	plan.historyDigest = historyHash.Sum(nil)
	plan.materializedDigest = materializedHash.Sum(nil)
	return plan, nil
}

func exactTypedHistoryProtocolString(value any) (string, bool) {
	text, ok := value.(string)
	if !ok || text == "" || strings.TrimSpace(text) != text {
		return "", false
	}
	for _, character := range text {
		if character <= 0x20 || character == 0x7f {
			return "", false
		}
	}
	return text, true
}

func (r *Repository) HasActiveTypedHistoryBootstrap(
	ctx context.Context,
	streamUID, ownerID string,
	epoch int64,
) (bool, error) {
	if r == nil || r.db == nil {
		return false, ErrSchemaUnavailable
	}
	streamUID, ownerID = strings.TrimSpace(streamUID), strings.TrimSpace(ownerID)
	if streamUID == "" || ownerID == "" || epoch <= 0 {
		return false, errors.New("typed history bootstrap identity is required")
	}
	var marker int
	err := r.db.QueryRowContext(ctx, `SELECT 1
		FROM transcript_typed_history_bootstrap_receipts receipt
		JOIN transcript_frame_authority authority
			ON authority.active_stream_uid=receipt.stream_uid AND authority.active_epoch=receipt.epoch
			AND authority.genesis_id=receipt.genesis_id
		WHERE receipt.stream_uid=? AND receipt.owner_id=? AND receipt.epoch=? AND receipt.status='active'
			AND authority.read_authority='transcript_payload_v1'
			AND authority.write_authority='transcript_payload_v1'`, streamUID, ownerID, epoch).Scan(&marker)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, schemaError(err)
	}
	return marker == 1, nil
}

func bootstrapTypedFrameHistoryConn(
	ctx context.Context,
	conn *sql.Conn,
	candidate noStreamFrameHistoryCandidate,
	now time.Time,
) (bool, error) {
	plan, err := planTypedHistoryBootstrapConn(ctx, conn, candidate.sessionID)
	if errors.Is(err, errTypedHistoryBootstrapUnsupported) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	input, err := normalizeCreateStreamInput(CreateStreamInput{
		UID: "frame:" + candidate.sessionID, OwnerID: candidate.ownerID,
		ExternalID: candidate.sessionID, SessionID: candidate.sessionID, Kind: StreamKindFrameRef,
		ProjectID: candidate.projectID, RootFrameID: candidate.rootFrameID,
		FrameID: candidate.sessionID, Epoch: 1,
	})
	if err != nil {
		return false, err
	}
	stream, err := createStreamRowsConn(ctx, conn, input, now)
	if err != nil {
		return false, err
	}
	for _, source := range plan.events {
		frameEventID := source.id
		if err := validateFrameEventReferenceConn(ctx, conn, stream, source.rawType, EventSourceFrameRef, &frameEventID); err != nil {
			return false, err
		}
		event, created, err := appendEventConn(ctx, conn, stream, eventRecord{
			clientMessageID: "payload-genesis:" + source.id,
			eventType:       source.eventType, source: EventSourcePayload,
			payloadJSON: source.payload, createdAt: source.createdAt,
		})
		if err != nil || !created || event.RunnerAttempt != nil || event.FrameEventID != nil {
			if err != nil {
				return false, err
			}
			return false, ErrEventConflict
		}
		stream, err = getRawStreamConn(ctx, conn, stream.UID, stream.OwnerID)
		if err != nil {
			return false, err
		}
	}
	if err := verifyTypedHistoryBootstrapMaterializedConn(ctx, conn, stream, plan); err != nil {
		return false, err
	}
	var branchID string
	var generation int64
	if err := conn.QueryRowContext(ctx, `SELECT active_branch_id,generation
		FROM transcript_branch_state WHERE stream_uid=?`, stream.UID).Scan(&branchID, &generation); err != nil {
		return false, err
	}
	genesisID := payloadGenesisID(stream, "frame_import", int64(len(plan.events)), plan.sourceDigest)
	if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_payload_genesis_receipts(
		genesis_id,stream_uid,owner_id,session_id,epoch,active_branch_id,branch_generation,
		authority_generation,source_kind,source_stream_uid,source_epoch,
		source_event_count,source_sha256,status,created_at)
		VALUES(?,?,?,?,?,?,1,1,'frame_import',NULL,NULL,?,?,'active',?)`,
		genesisID[:], stream.UID, stream.OwnerID, stream.SessionID, stream.Epoch, branchID,
		len(plan.events), plan.sourceDigest, now); err != nil {
		return false, err
	}
	bootstrapID := typedHistoryBootstrapID(candidate, stream, plan)
	if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_typed_history_bootstrap_receipts(
		bootstrap_id,genesis_id,stream_uid,owner_id,session_id,frame_incarnation_id,epoch,
		active_branch_id,branch_generation,authority_generation,history_kind,source_through_sequence,
		source_row_count,history_event_count,branch_event_count,text_block_count,tool_use_count,
		tool_result_count,ask_user_prompt_count,ask_user_result_count,attempt_count,runner_receipt_count,
		checkpoint_count,artifact_commit_count,artifact_ref_count,source_snapshot_sha256,history_sha256,
		materialized_sha256,status,created_at)
		VALUES(?,?,?,?,?,?,1,?,1,1,'ordinary_rich_v1',?,?,?,?,?,?,?,0,0,0,0,0,0,0,?,?,?,'active',?)`,
		bootstrapID[:], genesisID[:], stream.UID, stream.OwnerID, stream.SessionID, candidate.incarnationID,
		branchID, plan.sourceThroughSequence, len(plan.events), len(plan.events), len(plan.events),
		plan.textBlocks, plan.toolUses, plan.toolResults, plan.sourceDigest, plan.historyDigest,
		plan.materializedDigest, now); err != nil {
		return false, err
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_frame_authority(
		owner_id,session_id,active_stream_uid,active_epoch,authority_generation,
		read_authority,write_authority,activation_id,genesis_id,updated_at)
		VALUES(?,?,?,?,1,'transcript_payload_v1','transcript_payload_v1',NULL,?,?)`,
		stream.OwnerID, stream.SessionID, stream.UID, stream.Epoch, genesisID[:], now); err != nil {
		return false, err
	}
	return true, nil
}

func verifyTypedHistoryBootstrapMaterializedConn(
	ctx context.Context,
	conn *sql.Conn,
	stream Stream,
	plan typedHistoryBootstrapPlan,
) error {
	rows, err := conn.QueryContext(ctx, `SELECT event_id,publication_seq,client_message_id,event_type,
		source,runner_attempt,payload_json,frame_event_id,created_at FROM transcript_events
		WHERE stream_uid=? ORDER BY event_id`, stream.UID)
	if err != nil {
		return err
	}
	defer rows.Close()
	digest := sha256.New()
	count := 0
	for rows.Next() {
		if count >= len(plan.events) {
			return ErrEventConflict
		}
		var eventID, publication int64
		var clientID, eventType, source string
		var attempt sql.NullInt64
		var payload []byte
		var frameEvent sql.NullString
		var createdAt time.Time
		if err := rows.Scan(&eventID, &publication, &clientID, &eventType, &source, &attempt,
			&payload, &frameEvent, &createdAt); err != nil {
			return err
		}
		expected := plan.events[count]
		if eventID != int64(count+1) || publication != eventID ||
			clientID != "payload-genesis:"+expected.id || eventType != expected.eventType ||
			source != string(EventSourcePayload) || attempt.Valid || frameEvent.Valid ||
			!bytes.Equal(payload, expected.payload) || !createdAt.Equal(expected.createdAt) {
			return ErrEventConflict
		}
		writeTypedHistoryMaterializedDigest(digest, eventID, expected)
		count++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != len(plan.events) || !bytes.Equal(digest.Sum(nil), plan.materializedDigest) {
		return ErrEventConflict
	}
	var branchEvents int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcript_branch_events
		WHERE stream_uid=?`, stream.UID).Scan(&branchEvents); err != nil {
		return err
	}
	if branchEvents != count {
		return ErrEventConflict
	}
	return nil
}

func typedHistoryBootstrapID(
	candidate noStreamFrameHistoryCandidate,
	stream Stream,
	plan typedHistoryBootstrapPlan,
) [sha256.Size]byte {
	digest := sha256.New()
	for _, value := range [][]byte{
		[]byte(TypedHistoryBootstrapContractID), []byte(candidate.ownerID), []byte(candidate.sessionID),
		[]byte(candidate.incarnationID), []byte(stream.UID), []byte(strconv.FormatInt(stream.Epoch, 10)),
		plan.sourceDigest, plan.historyDigest, plan.materializedDigest,
	} {
		writePayloadGenesisDigestField(digest, value)
	}
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result
}

func writeTypedHistorySourceDigest(target interface{ Write([]byte) (int, error) }, event typedHistoryBootstrapEvent) {
	writePayloadGenesisDigestField(target, []byte(event.id))
	var number [8]byte
	binary.BigEndian.PutUint64(number[:], uint64(event.sequence))
	_, _ = target.Write(number[:])
	writePayloadGenesisDigestField(target, []byte(event.rawType))
	writePayloadGenesisDigestField(target, event.payload)
	writePayloadGenesisDigestField(target, []byte(event.createdAt.UTC().Format(time.RFC3339Nano)))
}

func writeTypedHistoryMaterializedDigest(
	target interface{ Write([]byte) (int, error) },
	eventID int64,
	event typedHistoryBootstrapEvent,
) {
	var number [8]byte
	binary.BigEndian.PutUint64(number[:], uint64(eventID))
	_, _ = target.Write(number[:])
	writePayloadGenesisDigestField(target, []byte("payload-genesis:"+event.id))
	writePayloadGenesisDigestField(target, []byte(event.eventType))
	writePayloadGenesisDigestField(target, event.payload)
	writePayloadGenesisDigestField(target, []byte(event.createdAt.UTC().Format(time.RFC3339Nano)))
}
