package transcript

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

const ordinaryHistoryCursorStageTable = "temp.synon_ordinary_cursor_stage"

type ordinaryHistoryCursorStage struct {
	count  int
	digest []byte
}

// stageOrdinaryHistoryEventsConn builds the same file-backed mapping consumed
// by the canonical clone materializer, but it never treats Frame references as
// disposable support rows. Plain Frame messages become immutable payload
// events; already-canonical non-AskUser payload facts retain their runner and
// event identity. Rich Frame facts without a typed Transcript contract remain
// unsupported instead of being guessed from presentation JSON.
func stageOrdinaryHistoryEventsConn(
	ctx context.Context,
	conn *sql.Conn,
	source, target Stream,
	counts canonicalCloneSourceCounts,
) (canonicalCloneStage, error) {
	if err := resetCanonicalCloneStageConn(ctx, conn, target.UID); err != nil {
		return canonicalCloneStage{}, err
	}
	rows, err := conn.QueryContext(ctx, `SELECT DISTINCT event.event_id,event.publication_seq,
		event.client_message_id,event.event_type,event.runner_attempt,event.frame_event_id,
		event.payload_json,event.created_at,event.source
		FROM transcript_branch_events member
		JOIN transcript_events event ON event.stream_uid=member.stream_uid AND event.event_id=member.event_id
		WHERE member.stream_uid=? ORDER BY event.publication_seq,event.event_id`, source.UID)
	if err != nil {
		return canonicalCloneStage{}, err
	}
	digest := sha256.New()
	processed := 0
	for rows.Next() {
		var sourceEventID, publication int64
		var clientMessageID, eventType, sourceKind string
		var runnerAttempt sql.NullInt64
		var frameEventID sql.NullString
		var payload []byte
		var createdAt time.Time
		if err := rows.Scan(&sourceEventID, &publication, &clientMessageID, &eventType, &runnerAttempt,
			&frameEventID, &payload, &createdAt, &sourceKind); err != nil {
			_ = rows.Close()
			return canonicalCloneStage{}, err
		}
		processed++
		if sourceEventID <= 0 || publication <= 0 || strings.TrimSpace(clientMessageID) == "" {
			_ = rows.Close()
			return canonicalCloneStage{}, ErrEventConflict
		}
		switch EventSource(sourceKind) {
		case EventSourcePayload:
			if frameEventID.Valid || len(payload) == 0 || len(payload) > maxEventPayloadBytes || !json.Valid(payload) ||
				eventType == AskUserPromptEventType || eventType == AskUserResultEventType ||
				ordinaryPayloadContainsAskUserFact(payload) {
				_ = rows.Close()
				return canonicalCloneStage{}, errOrdinaryHistoryShapeUnsupported
			}
			if err := validateCanonicalCloneTypedPayload(eventType, payload, source); err != nil {
				_ = rows.Close()
				return canonicalCloneStage{}, err
			}
		case EventSourceFrameRef:
			if !frameEventID.Valid || len(payload) != 0 || runnerAttempt.Valid {
				_ = rows.Close()
				return canonicalCloneStage{}, ErrEventConflict
			}
			var frameID, rawEventType string
			var framePayload []byte
			var frameCreatedAt time.Time
			err := conn.QueryRowContext(ctx, `SELECT frame_id,event_type,payload,created_at
				FROM frame_events WHERE id=?`, frameEventID.String).
				Scan(&frameID, &rawEventType, &framePayload, &frameCreatedAt)
			if errors.Is(err, sql.ErrNoRows) {
				_ = rows.Close()
				return canonicalCloneStage{}, ErrEventConflict
			}
			if err != nil {
				_ = rows.Close()
				return canonicalCloneStage{}, err
			}
			if frameID != source.FrameID || !frameCreatedAt.Equal(createdAt) {
				_ = rows.Close()
				return canonicalCloneStage{}, ErrEventConflict
			}
			eventType, err = payloadGenesisFrameMessage(rawEventType, framePayload)
			if err != nil {
				_ = rows.Close()
				return canonicalCloneStage{}, errOrdinaryHistoryShapeUnsupported
			}
			payload = framePayload
			clientMessageID = "payload-genesis:" + frameEventID.String
		default:
			_ = rows.Close()
			return canonicalCloneStage{}, ErrEventConflict
		}
		targetEventID := int64(processed)
		sourceKey := "event:" + strconv.FormatInt(sourceEventID, 10)
		writeCanonicalCloneEventDigest(digest, sourceKey, clientMessageID, eventType, payload, targetEventID)
		if _, err := conn.ExecContext(ctx, `INSERT INTO `+canonicalCloneEventTable+`(
			target_uid,source_key,source_event_id,target_event_id,client_message_id,event_type,
			runner_attempt,payload_json,created_at,keep) VALUES(?,?,?,?,?,?,?,?,?,1)`,
			target.UID, sourceKey, sourceEventID, targetEventID, clientMessageID, eventType,
			nullableInt64Value(runnerAttempt), payload, createdAt); err != nil {
			_ = rows.Close()
			return canonicalCloneStage{}, err
		}
	}
	if err := rows.Close(); err != nil {
		return canonicalCloneStage{}, err
	}
	if err := rows.Err(); err != nil {
		return canonicalCloneStage{}, err
	}
	if processed != counts.events {
		return canonicalCloneStage{}, ErrEventConflict
	}
	active, generation, err := canonicalCloneBranchStateConn(ctx, conn, source.UID)
	if err != nil {
		return canonicalCloneStage{}, err
	}
	return canonicalCloneStage{
		eventDigest: digest.Sum(nil), eventCount: processed,
		activeSource: active, activeTarget: active, branchGeneration: generation,
	}, nil
}

func ordinaryPayloadContainsAskUserFact(payload []byte) bool {
	var object map[string]any
	if json.Unmarshal(payload, &object) != nil {
		return false
	}
	if _, ok := CanonicalAskUserToolNameV1(webHistoryString(object["toolName"])); ok {
		return true
	}
	for _, field := range []string{"content", "modelToolCalls"} {
		items, _ := object[field].([]any)
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			_, byName := CanonicalAskUserToolNameV1(webHistoryString(item["name"]))
			_, byToolName := CanonicalAskUserToolNameV1(webHistoryString(item["toolName"]))
			if byName || byToolName {
				return true
			}
		}
	}
	return false
}

func prepareOrdinaryHistoryCursorStageConn(
	ctx context.Context, conn *sql.Conn, source, target Stream, stage canonicalCloneStage,
) (ordinaryHistoryCursorStage, error) {
	if _, err := conn.ExecContext(ctx, `CREATE TEMP TABLE IF NOT EXISTS synon_ordinary_cursor_stage(
		target_uid TEXT NOT NULL,
		source_branch_id TEXT NOT NULL,
		target_branch_id TEXT NOT NULL,
		source_message_index INTEGER NOT NULL,
		source_ordinal INTEGER NOT NULL,
		source_event_id INTEGER NOT NULL,
		target_event_id INTEGER NOT NULL,
		target_publication_seq INTEGER NOT NULL,
		target_message_index INTEGER,
		semantic_key TEXT NOT NULL,
		stable_message_id TEXT NOT NULL,
		PRIMARY KEY(target_uid,source_branch_id,source_message_index),
		UNIQUE(target_uid,source_branch_id,semantic_key))`); err != nil {
		return ordinaryHistoryCursorStage{}, err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM `+ordinaryHistoryCursorStageTable+` WHERE target_uid=?`, target.UID); err != nil {
		return ordinaryHistoryCursorStage{}, err
	}
	afterBranch := ""
	for {
		var sourceBranch, targetBranch string
		err := conn.QueryRowContext(ctx, `SELECT source_branch_id,target_branch_id FROM `+canonicalCloneBranchTable+`
			WHERE target_uid=? AND source_branch_id>? ORDER BY source_branch_id LIMIT 1`, target.UID, afterBranch).
			Scan(&sourceBranch, &targetBranch)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return ordinaryHistoryCursorStage{}, err
		}
		if err := projectOrdinarySourceBranchCursorConn(ctx, conn, source, target, sourceBranch, targetBranch); err != nil {
			return ordinaryHistoryCursorStage{}, err
		}
		if err := projectOrdinaryTargetBranchCursorConn(ctx, conn, target, sourceBranch, targetBranch); err != nil {
			return ordinaryHistoryCursorStage{}, err
		}
		afterBranch = sourceBranch
	}
	var count, incomplete int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(target_message_index IS NULL),0)
		FROM `+ordinaryHistoryCursorStageTable+` WHERE target_uid=?`, target.UID).Scan(&count, &incomplete); err != nil {
		return ordinaryHistoryCursorStage{}, err
	}
	if count <= 0 || incomplete != 0 {
		return ordinaryHistoryCursorStage{}, errOrdinaryHistoryShapeUnsupported
	}
	digest := sha256.New()
	if err := digestHistoryActivationRows(ctx, conn, digest, `SELECT source_branch_id,target_branch_id,
		source_message_index,source_ordinal,source_event_id,target_event_id,target_publication_seq,
		target_message_index,stable_message_id FROM `+ordinaryHistoryCursorStageTable+`
		WHERE target_uid=? ORDER BY source_branch_id,source_message_index`, target.UID); err != nil {
		return ordinaryHistoryCursorStage{}, err
	}
	return ordinaryHistoryCursorStage{count: count, digest: digest.Sum(nil)}, nil
}

func cleanupOrdinaryHistoryCursorStageConn(ctx context.Context, conn *sql.Conn, targetUID string) error {
	_, err := conn.ExecContext(ctx, `DELETE FROM `+ordinaryHistoryCursorStageTable+` WHERE target_uid=?`, targetUID)
	return err
}

func projectOrdinarySourceBranchCursorConn(
	ctx context.Context, conn *sql.Conn, source, target Stream, sourceBranch, targetBranch string,
) error {
	var afterOrdinal, messageIndex int64
	messageIndex = -1
	for {
		var ordinal, sourceEventID, targetEventID, targetPublication int64
		var eventType, mappedClientID string
		var runnerAttempt sql.NullInt64
		err := conn.QueryRowContext(ctx, `SELECT member.ordinal,event.event_id,event.event_type,event.runner_attempt,
			target_event.event_id,target_event.publication_seq,mapping.client_message_id
			FROM transcript_branch_events member
			JOIN transcript_events event ON event.stream_uid=member.stream_uid AND event.event_id=member.event_id
			JOIN `+canonicalCloneEventTable+` mapping ON mapping.target_uid=?
				AND mapping.source_event_id=event.event_id AND mapping.keep=1
			JOIN transcript_events target_event ON target_event.stream_uid=mapping.target_uid
				AND target_event.event_id=mapping.target_event_id
			WHERE member.stream_uid=? AND member.branch_id=? AND member.ordinal>?
			ORDER BY member.ordinal LIMIT 1`, target.UID, source.UID, sourceBranch, afterOrdinal).
			Scan(&ordinal, &sourceEventID, &eventType, &runnerAttempt,
				&targetEventID, &targetPublication, &mappedClientID)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return err
		}
		semantic, stable, visible, err := ordinaryHistoryMessageCoordinate(
			target.SessionID, eventType, runnerAttempt, mappedClientID,
		)
		if err != nil {
			return err
		}
		if visible {
			var existing int64
			err := conn.QueryRowContext(ctx, `SELECT source_message_index FROM `+ordinaryHistoryCursorStageTable+`
				WHERE target_uid=? AND source_branch_id=? AND semantic_key=?`, target.UID, sourceBranch, semantic).
				Scan(&existing)
			if errors.Is(err, sql.ErrNoRows) {
				messageIndex++
				if _, err := conn.ExecContext(ctx, `INSERT INTO `+ordinaryHistoryCursorStageTable+`(
					target_uid,source_branch_id,target_branch_id,source_message_index,source_ordinal,
					source_event_id,target_event_id,target_publication_seq,target_message_index,
					semantic_key,stable_message_id) VALUES(?,?,?,?,?,?,?,?,NULL,?,?)`,
					target.UID, sourceBranch, targetBranch, messageIndex, ordinal, sourceEventID,
					targetEventID, targetPublication, semantic, stable); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
		}
		afterOrdinal = ordinal
	}
	return nil
}

func projectOrdinaryTargetBranchCursorConn(
	ctx context.Context, conn *sql.Conn, target Stream, sourceBranch, targetBranch string,
) error {
	var afterOrdinal, messageIndex int64
	messageIndex = -1
	for {
		var ordinal int64
		var eventType, clientMessageID string
		var runnerAttempt sql.NullInt64
		err := conn.QueryRowContext(ctx, `SELECT member.ordinal,event.event_type,event.runner_attempt,event.client_message_id
			FROM transcript_branch_events member
			JOIN transcript_events event ON event.stream_uid=member.stream_uid AND event.event_id=member.event_id
			WHERE member.stream_uid=? AND member.branch_id=? AND member.ordinal>?
			ORDER BY member.ordinal LIMIT 1`, target.UID, targetBranch, afterOrdinal).
			Scan(&ordinal, &eventType, &runnerAttempt, &clientMessageID)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return err
		}
		semantic, stable, visible, err := ordinaryHistoryMessageCoordinate(
			target.SessionID, eventType, runnerAttempt, clientMessageID,
		)
		if err != nil {
			return err
		}
		if visible {
			var storedStable string
			var targetIndex sql.NullInt64
			err := conn.QueryRowContext(ctx, `SELECT stable_message_id,target_message_index
				FROM `+ordinaryHistoryCursorStageTable+`
				WHERE target_uid=? AND source_branch_id=? AND semantic_key=?`, target.UID, sourceBranch, semantic).
				Scan(&storedStable, &targetIndex)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrEventConflict
			}
			if err != nil {
				return err
			}
			if !targetIndex.Valid {
				messageIndex++
				if storedStable != stable {
					return ErrEventConflict
				}
				updated, err := conn.ExecContext(ctx, `UPDATE `+ordinaryHistoryCursorStageTable+`
					SET target_message_index=? WHERE target_uid=? AND source_branch_id=?
						AND semantic_key=? AND target_message_index IS NULL`,
					messageIndex, target.UID, sourceBranch, semantic)
				if err != nil {
					return err
				}
				if changed, err := updated.RowsAffected(); err != nil || changed != 1 {
					if err != nil {
						return err
					}
					return ErrEventConflict
				}
			}
		}
		afterOrdinal = ordinal
	}
	var sourceMessages, targetMessages int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(target_message_index)+1,0)
		FROM `+ordinaryHistoryCursorStageTable+` WHERE target_uid=? AND source_branch_id=?`,
		target.UID, sourceBranch).Scan(&sourceMessages, &targetMessages); err != nil {
		return err
	}
	if sourceMessages != targetMessages {
		return ErrEventConflict
	}
	return nil
}

func ordinaryHistoryMessageCoordinate(
	sessionID, eventType string, runnerAttempt sql.NullInt64, clientMessageID string,
) (string, string, bool, error) {
	clientMessageID = strings.TrimSpace(clientMessageID)
	switch eventType {
	case "user_message", "history_user_message":
		if clientMessageID == "" {
			return "", "", false, ErrEventConflict
		}
		return "user:" + clientMessageID, clientMessageID, true, nil
	case "user_input_response":
		// Transcript Web history treats non-AskUser input responses as
		// model-only continuation, so they do not advance the public cursor.
		return "", "", false, nil
	case "content_delta", "content_reset", "assistant_message", "history_assistant_message", "runner_finished":
		if runnerAttempt.Valid {
			if runnerAttempt.Int64 <= 0 || strings.TrimSpace(sessionID) == "" {
				return "", "", false, ErrEventConflict
			}
			value := strconv.FormatInt(runnerAttempt.Int64, 10)
			return "assistant:" + value, "assistant-" + sessionID + "-" + value, true, nil
		}
		if eventType == "assistant_message" || eventType == "history_assistant_message" {
			if clientMessageID == "" {
				return "", "", false, ErrEventConflict
			}
			return "assistant-frame:" + clientMessageID, clientMessageID, true, nil
		}
		if eventType == "runner_finished" {
			return "", "", false, ErrEventConflict
		}
	}
	return "", "", false, nil
}

func insertOrdinaryHistoryCursorStageConn(
	ctx context.Context,
	conn *sql.Conn,
	cutoverID []byte,
	source, target Stream,
	stage canonicalCloneStage,
	sourceThrough int64,
	now time.Time,
) error {
	inserted, err := conn.ExecContext(ctx, `INSERT INTO transcript_history_ordinary_cursor_map(
		ordinary_cutover_id,source_stream_uid,target_stream_uid,source_branch_id,target_branch_id,
		source_generation,source_through_publication_seq,source_message_index,source_ordinal,source_event_id,
		target_event_id,target_publication_seq,target_message_index,stable_message_id,created_at)
	SELECT ?,?,?,source_branch_id,target_branch_id,?,?,source_message_index,source_ordinal,source_event_id,
		target_event_id,target_publication_seq,target_message_index,stable_message_id,?
	FROM `+ordinaryHistoryCursorStageTable+` WHERE target_uid=? ORDER BY source_branch_id,source_message_index`,
		cutoverID, source.UID, target.UID, stage.branchGeneration, sourceThrough, now, target.UID)
	if err != nil {
		return err
	}
	changed, err := inserted.RowsAffected()
	if err != nil {
		return err
	}
	var expected int64
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+ordinaryHistoryCursorStageTable+`
		WHERE target_uid=?`, target.UID).Scan(&expected); err != nil {
		return err
	}
	if changed != expected {
		return ErrEventConflict
	}
	return nil
}

func ordinaryHistoryCursorDigestConn(
	ctx context.Context, conn *sql.Conn, cutoverID []byte,
) ([]byte, error) {
	digest := sha256.New()
	if err := digestHistoryActivationRows(ctx, conn, digest, `SELECT source_branch_id,target_branch_id,
		source_message_index,source_ordinal,source_event_id,target_event_id,target_publication_seq,
		target_message_index,stable_message_id FROM transcript_history_ordinary_cursor_map
		WHERE ordinary_cutover_id=? ORDER BY source_branch_id,source_message_index`, cutoverID); err != nil {
		return nil, err
	}
	return digest.Sum(nil), nil
}

func ordinaryHistoryLegacyCursorDigestConn(
	ctx context.Context, conn *sql.Conn, cutoverID []byte,
) ([]byte, error) {
	digest := sha256.New()
	if err := digestHistoryActivationRows(ctx, conn, digest, `SELECT source_stream_uid,target_stream_uid,
		source_branch_id,target_branch_id,source_ordinal,source_event_id,target_event_id,
		target_publication_seq,target_message_index,stable_message_id
		FROM transcript_history_ordinary_cursor_map WHERE ordinary_cutover_id=?
		ORDER BY source_branch_id,source_ordinal`, cutoverID); err != nil {
		return nil, err
	}
	return digest.Sum(nil), nil
}
