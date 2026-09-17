package transcript

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	canonicalCloneEventTable   = "temp.synon_clone_event_map"
	canonicalCloneSupportTable = "temp.synon_clone_frame_support"
	canonicalCloneBranchTable  = "temp.synon_clone_branch_map"
)

type canonicalCloneStage struct {
	eventDigest      []byte
	eventCount       int
	activeSource     string
	activeTarget     string
	branchGeneration int64
	legacyCutover    *storedHistoryActivationCutover
}

type canonicalCloneMaterialized struct {
	attempts, receipts, checkpoints int
	branches, branchEvents          int
	artifactCommits, artifactRefs   int
	activeTarget                    string
	branchDigest, artifactDigest    []byte
}

func prepareCanonicalCloneStageConn(
	ctx context.Context, conn *sql.Conn, source, target Stream, input CloneFrameHistoryInput,
) (canonicalCloneStage, error) {
	if err := resetCanonicalCloneStageConn(ctx, conn, target.UID); err != nil {
		return canonicalCloneStage{}, err
	}
	authority, err := loadCloneSourceFrameAuthorityConn(ctx, conn, source)
	if err != nil {
		return canonicalCloneStage{}, err
	}
	switch {
	case authority.TranscriptPayloadActive():
		if input.LegacyCutoverID != "" {
			return canonicalCloneStage{}, ErrEventConflict
		}
		return stageDirectCanonicalCloneEventsConn(ctx, conn, source, target, input.sourceCounts)
	case authority.ReadAuthority == "legacy_mixed_v1" && authority.WriteAuthority == "legacy_frame_ref_v1" &&
		len(authority.ActivationID) == 0 && len(authority.GenesisID) == 0:
		if input.LegacyCutoverID == "" {
			return canonicalCloneStage{}, ErrEventConflict
		}
		return stageLegacyCanonicalCloneEventsConn(ctx, conn, source, target, input.LegacyCutoverID)
	default:
		return canonicalCloneStage{}, ErrEventConflict
	}
}

func resetCanonicalCloneStageConn(ctx context.Context, conn *sql.Conn, targetUID string) error {
	// Clone staging is a transport for one transaction, not a second durable
	// authority. Keep arbitrary history volume off the Go heap and require a
	// SQLite build whose default TEMP b-tree can spill to a managed file.
	var tempStore int
	if err := conn.QueryRowContext(ctx, `PRAGMA temp_store`).Scan(&tempStore); err != nil {
		return err
	}
	if tempStore == 0 {
		var fileDefault int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_compile_options
			WHERE compile_options='TEMP_STORE=1'`).Scan(&fileDefault); err != nil {
			return err
		}
		if fileDefault != 1 {
			return errors.New("canonical clone requires file-backed SQLite temporary storage")
		}
	} else if tempStore != 1 {
		return errors.New("canonical clone requires file-backed SQLite temporary storage")
	}
	statements := []string{
		`CREATE TEMP TABLE IF NOT EXISTS synon_clone_event_map(
			target_uid TEXT NOT NULL,
			source_key TEXT NOT NULL,
			source_event_id INTEGER,
			target_event_id INTEGER,
			client_message_id TEXT NOT NULL,
			event_type TEXT NOT NULL,
			runner_attempt INTEGER,
			payload_json BLOB,
			created_at TIMESTAMP NOT NULL,
			keep INTEGER NOT NULL CHECK(keep IN (0,1)),
			PRIMARY KEY(target_uid,source_key),
			UNIQUE(target_uid,target_event_id))`,
		`CREATE INDEX IF NOT EXISTS synon_clone_event_source_idx
			ON synon_clone_event_map(target_uid,source_event_id)`,
		`CREATE TEMP TABLE IF NOT EXISTS synon_clone_frame_support(
			target_uid TEXT NOT NULL,
			frame_event_id TEXT NOT NULL,
			event_type TEXT NOT NULL,
			matched INTEGER NOT NULL DEFAULT 0 CHECK(matched IN (0,1)),
			PRIMARY KEY(target_uid,frame_event_id))`,
		`CREATE TEMP TABLE IF NOT EXISTS synon_clone_branch_map(
			target_uid TEXT NOT NULL,
			source_branch_id TEXT NOT NULL,
			target_branch_id TEXT NOT NULL,
			PRIMARY KEY(target_uid,source_branch_id),
			UNIQUE(target_uid,target_branch_id))`,
	}
	for _, statement := range statements {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM `+canonicalCloneEventTable+` WHERE target_uid=?`, targetUID); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM `+canonicalCloneSupportTable+` WHERE target_uid=?`, targetUID); err != nil {
		return err
	}
	_, err := conn.ExecContext(ctx, `DELETE FROM `+canonicalCloneBranchTable+` WHERE target_uid=?`, targetUID)
	return err
}

func stageDirectCanonicalCloneEventsConn(
	ctx context.Context, conn *sql.Conn, source, target Stream, counts canonicalCloneSourceCounts,
) (canonicalCloneStage, error) {
	if err := stageDirectCanonicalCloneSupportConn(ctx, conn, source, target.UID); err != nil {
		return canonicalCloneStage{}, err
	}
	rows, err := conn.QueryContext(ctx, `SELECT DISTINCT event.event_id,event.publication_seq,event.client_message_id,
		event.event_type,event.runner_attempt,event.frame_event_id,event.payload_json,event.created_at,event.source
		FROM transcript_branch_events member
		JOIN transcript_events event ON event.stream_uid=member.stream_uid AND event.event_id=member.event_id
		WHERE member.stream_uid=? ORDER BY event.publication_seq,event.event_id`, source.UID)
	if err != nil {
		return canonicalCloneStage{}, err
	}
	digest := sha256.New()
	processed, kept := 0, 0
	for rows.Next() {
		var sourceEventID, publication int64
		var clientMessageID, eventType, sourceKind string
		var runnerAttempt sql.NullInt64
		var frameEventID sql.NullString
		var payload []byte
		var createdAt any
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
		sourceKey := "event:" + strconv.FormatInt(sourceEventID, 10)
		keep := 1
		if EventSource(sourceKind) == EventSourcePayload {
			if frameEventID.Valid || len(payload) == 0 || len(payload) > maxEventPayloadBytes || !json.Valid(payload) {
				_ = rows.Close()
				return canonicalCloneStage{}, ErrEventConflict
			}
			if err := validateCanonicalCloneTypedPayload(eventType, payload, source); err != nil {
				_ = rows.Close()
				return canonicalCloneStage{}, err
			}
			rebound, reboundClientID, err := rebindHistoryActivationPayload(
				eventType, payload, target.UID, target.FrameID, target.Epoch,
			)
			if err != nil {
				_ = rows.Close()
				return canonicalCloneStage{}, err
			}
			payload = rebound
			if reboundClientID != "" {
				clientMessageID = reboundClientID
			}
		} else {
			if EventSource(sourceKind) != EventSourceFrameRef || !frameEventID.Valid || len(payload) != 0 {
				_ = rows.Close()
				return canonicalCloneStage{}, ErrEventConflict
			}
			var expectedType string
			var matched int
			if err := conn.QueryRowContext(ctx, `SELECT event_type,matched FROM `+canonicalCloneSupportTable+
				` WHERE target_uid=? AND frame_event_id=?`, target.UID, frameEventID.String).Scan(&expectedType, &matched); err != nil ||
				expectedType != eventType || matched != 0 {
				_ = rows.Close()
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return canonicalCloneStage{}, err
				}
				return canonicalCloneStage{}, ErrEventConflict
			}
			if _, err := conn.ExecContext(ctx, `UPDATE `+canonicalCloneSupportTable+
				` SET matched=1 WHERE target_uid=? AND frame_event_id=? AND matched=0`, target.UID, frameEventID.String); err != nil {
				_ = rows.Close()
				return canonicalCloneStage{}, err
			}
			keep = 0
		}
		var targetEvent any
		if keep == 1 {
			kept++
			targetEvent = kept
			writeCanonicalCloneEventDigest(digest, sourceKey, clientMessageID, eventType, payload, int64(kept))
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO `+canonicalCloneEventTable+`(
			target_uid,source_key,source_event_id,target_event_id,client_message_id,event_type,
			runner_attempt,payload_json,created_at,keep) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			target.UID, sourceKey, sourceEventID, targetEvent, clientMessageID, eventType,
			nullableInt64Value(runnerAttempt), payload, createdAt, keep); err != nil {
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
	var supportCount, matchedCount int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(matched),0) FROM `+canonicalCloneSupportTable+
		` WHERE target_uid=?`, target.UID).Scan(&supportCount, &matchedCount); err != nil {
		return canonicalCloneStage{}, err
	}
	if processed != counts.events || matchedCount != supportCount || supportCount%2 != 0 {
		return canonicalCloneStage{}, ErrEventConflict
	}
	active, generation, err := canonicalCloneBranchStateConn(ctx, conn, source.UID)
	if err != nil {
		return canonicalCloneStage{}, err
	}
	return canonicalCloneStage{
		eventDigest: digest.Sum(nil), eventCount: kept,
		activeSource: active, activeTarget: active, branchGeneration: generation,
	}, nil
}

func stageDirectCanonicalCloneSupportConn(
	ctx context.Context, conn *sql.Conn, source Stream, targetUID string,
) error {
	rows, err := conn.QueryContext(ctx, `SELECT payload_json FROM transcript_events
		WHERE stream_uid=? AND event_type=? AND source='payload' ORDER BY publication_seq,event_id`,
		source.UID, AskUserPromptEventType)
	if err != nil {
		return err
	}
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			_ = rows.Close()
			return err
		}
		prompt, err := DecodeAskUserPromptV1(payload)
		if err != nil || !canonicalCloneAskUserOriginMatchesSource(prompt.Origin, source) ||
			prompt.Origin.ToolUseFrameEventID == prompt.Origin.PendingFrameEventID {
			_ = rows.Close()
			return ErrEventConflict
		}
		for frameEventID, eventType := range map[string]string{
			prompt.Origin.ToolUseFrameEventID: "assistant_message",
			prompt.Origin.PendingFrameEventID: "user_message",
		} {
			if strings.TrimSpace(frameEventID) == "" {
				_ = rows.Close()
				return ErrEventConflict
			}
			if _, err := conn.ExecContext(ctx, `INSERT INTO `+canonicalCloneSupportTable+
				`(target_uid,frame_event_id,event_type) VALUES(?,?,?)`, targetUID, frameEventID, eventType); err != nil {
				_ = rows.Close()
				return ErrEventConflict
			}
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	return rows.Err()
}

func stageLegacyCanonicalCloneEventsConn(
	ctx context.Context, conn *sql.Conn, source, target Stream, cutoverHex string,
) (canonicalCloneStage, error) {
	cutoverID, err := hex.DecodeString(cutoverHex)
	if err != nil || len(cutoverID) != sha256.Size {
		return canonicalCloneStage{}, ErrEventConflict
	}
	cutover, err := loadHistoryActivationCutoverConn(ctx, conn, cutoverID, source.OwnerID)
	if err != nil {
		return canonicalCloneStage{}, err
	}
	if cutover.streamUID != source.UID || cutover.ownerID != source.OwnerID ||
		cutover.through != source.NextPublication-1 || cutover.eventCount < 0 {
		return canonicalCloneStage{}, ErrEventConflict
	}
	active, generation, err := canonicalCloneBranchStateConn(ctx, conn, source.UID)
	if err != nil || active != cutover.sourceBranchID || generation != cutover.generation {
		return canonicalCloneStage{}, ErrEventConflict
	}
	if err := blockUnsafeHistoryActivationConn(ctx, conn, source.UID); err != nil {
		return canonicalCloneStage{}, err
	}
	rows, err := conn.QueryContext(ctx, `SELECT target_event_key,source_event_id,target_publication_seq,
		client_message_id,event_type,runner_attempt,payload_json,created_at
		FROM transcript_history_cutover_events WHERE cutover_id=? ORDER BY target_publication_seq`, cutover.id)
	if err != nil {
		return canonicalCloneStage{}, err
	}
	digest := sha256.New()
	count := 0
	for rows.Next() {
		var sourceKey, clientMessageID, eventType string
		var sourceEventID, runnerAttempt sql.NullInt64
		var publication int64
		var payload []byte
		var createdAt any
		if err := rows.Scan(&sourceKey, &sourceEventID, &publication, &clientMessageID, &eventType,
			&runnerAttempt, &payload, &createdAt); err != nil {
			_ = rows.Close()
			return canonicalCloneStage{}, err
		}
		count++
		if publication != int64(count) || strings.TrimSpace(sourceKey) == "" || strings.TrimSpace(clientMessageID) == "" {
			_ = rows.Close()
			return canonicalCloneStage{}, ErrEventConflict
		}
		rebound, reboundClientID, err := rebindHistoryActivationPayload(
			eventType, payload, target.UID, target.FrameID, target.Epoch,
		)
		if err != nil {
			_ = rows.Close()
			return canonicalCloneStage{}, err
		}
		if reboundClientID != "" {
			clientMessageID = reboundClientID
		}
		writeCanonicalCloneEventDigest(digest, sourceKey, clientMessageID, eventType, rebound, publication)
		if _, err := conn.ExecContext(ctx, `INSERT INTO `+canonicalCloneEventTable+`(
			target_uid,source_key,source_event_id,target_event_id,client_message_id,event_type,
			runner_attempt,payload_json,created_at,keep) VALUES(?,?,?,?,?,?,?,?,?,1)`,
			target.UID, sourceKey, nullableInt64Value(sourceEventID), publication, clientMessageID,
			eventType, nullableInt64Value(runnerAttempt), rebound, createdAt); err != nil {
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
	if count != cutover.eventCount {
		return canonicalCloneStage{}, ErrEventConflict
	}
	copyCutover := cutover
	return canonicalCloneStage{
		eventDigest: digest.Sum(nil), eventCount: count,
		activeSource: active, branchGeneration: generation, legacyCutover: &copyCutover,
	}, nil
}

func validateCanonicalCloneTypedPayload(eventType string, payload []byte, source Stream) error {
	switch eventType {
	case AskUserPromptEventType:
		prompt, err := DecodeAskUserPromptV1(payload)
		if err != nil || !canonicalCloneAskUserOriginMatchesSource(prompt.Origin, source) ||
			prompt.Origin.ToolUseFrameEventID == prompt.Origin.PendingFrameEventID {
			return ErrEventConflict
		}
	case AskUserResultEventType:
		result, err := DecodeAskUserResultEventV1(payload)
		if err != nil || !canonicalCloneAskUserOriginMatchesSource(result.Origin, source) {
			return ErrEventConflict
		}
	}
	return nil
}

func canonicalCloneBranchStateConn(ctx context.Context, conn *sql.Conn, streamUID string) (string, int64, error) {
	var active string
	var generation int64
	if err := conn.QueryRowContext(ctx, `SELECT active_branch_id,generation FROM transcript_branch_state
		WHERE stream_uid=?`, streamUID).Scan(&active, &generation); err != nil {
		return "", 0, err
	}
	if strings.TrimSpace(active) == "" || generation <= 0 {
		return "", 0, ErrEventConflict
	}
	return active, generation, nil
}

func nullableInt64Value(value sql.NullInt64) any {
	if value.Valid {
		return value.Int64
	}
	return nil
}

func canonicalCloneMappedSourceEventConn(
	ctx context.Context, conn *sql.Conn, targetUID string, sourceEventID int64,
) (sql.NullInt64, error) {
	var targetEvent sql.NullInt64
	err := conn.QueryRowContext(ctx, `SELECT MAX(target_event_id) FROM `+canonicalCloneEventTable+
		` WHERE target_uid=? AND source_event_id=? AND keep=1`, targetUID, sourceEventID).Scan(&targetEvent)
	return targetEvent, err
}

func writeCanonicalCloneEventDigest(
	digest interface{ Write([]byte) (int, error) },
	sourceKey, clientMessageID, eventType string,
	payload []byte,
	publication int64,
) {
	writePayloadGenesisDigestField(digest, []byte(sourceKey))
	writePayloadGenesisDigestField(digest, []byte(clientMessageID))
	writePayloadGenesisDigestField(digest, []byte(eventType))
	writePayloadGenesisDigestField(digest, payload)
	var number [8]byte
	binary.BigEndian.PutUint64(number[:], uint64(publication))
	_, _ = digest.Write(number[:])
}

func cleanupCanonicalCloneStageConn(ctx context.Context, conn *sql.Conn, targetUID string) error {
	if _, err := conn.ExecContext(ctx, `DELETE FROM `+canonicalCloneEventTable+` WHERE target_uid=?`, targetUID); err != nil {
		return fmt.Errorf("clear clone event stage: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM `+canonicalCloneSupportTable+` WHERE target_uid=?`, targetUID); err != nil {
		return fmt.Errorf("clear clone support stage: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM `+canonicalCloneBranchTable+` WHERE target_uid=?`, targetUID); err != nil {
		return fmt.Errorf("clear clone branch stage: %w", err)
	}
	return nil
}

func materializeCanonicalCloneRunnerConn(
	ctx context.Context,
	conn *sql.Conn,
	source, target Stream,
	stage canonicalCloneStage,
	counts canonicalCloneSourceCounts,
) (int, int, int, error) {
	var invalid int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM transcript_runner_attempts attempt
		LEFT JOIN `+canonicalCloneEventTable+` event
			ON event.target_uid=? AND event.source_event_id=attempt.finished_event_id AND event.keep=1
		LEFT JOIN transcript_runner_receipts receipt
			ON receipt.stream_uid=attempt.stream_uid AND receipt.attempt=attempt.attempt
		WHERE attempt.stream_uid=? AND (
			attempt.status='running' OR attempt.finished_event_id IS NULL OR attempt.finished_at IS NULL OR
			event.target_event_id IS NULL OR
			(attempt.status!='reclaimed' AND receipt.attempt IS NULL) OR
			(receipt.attempt IS NOT NULL AND (receipt.event_id!=attempt.finished_event_id OR
				receipt.status!=attempt.status OR receipt.finished_at!=attempt.finished_at)))`,
		target.UID, source.UID).Scan(&invalid); err != nil {
		return 0, 0, 0, err
	}
	if invalid != 0 {
		return 0, 0, 0, ErrEventConflict
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_events(
		stream_uid,event_id,publication_seq,client_message_id,event_type,source,
		runner_attempt,payload_json,frame_event_id,created_at)
		SELECT target_uid,target_event_id,target_event_id,client_message_id,event_type,'payload',
		NULL,payload_json,NULL,created_at FROM `+canonicalCloneEventTable+
		` WHERE target_uid=? AND keep=1 AND runner_attempt IS NULL ORDER BY target_event_id`, target.UID); err != nil {
		return 0, 0, 0, err
	}
	attemptRows, err := conn.QueryContext(ctx, `SELECT attempt,runner_id,claim_token_sha256,claimed_input_revision,
		resume_source,resume_checkpoint_sequence,status,phase,phase_sequence,last_checkpoint_sequence,
		claimed_at,expires_at,finished_event_id,finished_at
		FROM transcript_runner_attempts WHERE stream_uid=? ORDER BY attempt`, source.UID)
	if err != nil {
		return 0, 0, 0, err
	}
	attemptCount, receiptCount := 0, 0
	for attemptRows.Next() {
		fact, err := scanCanonicalCloneAttempt(attemptRows)
		if err != nil {
			_ = attemptRows.Close()
			return 0, 0, 0, err
		}
		attemptCount++
		targetFinished, err := canonicalCloneMappedSourceEventConn(ctx, conn, target.UID, fact.finishedEvent.Int64)
		if err != nil || !targetFinished.Valid || targetFinished.Int64 <= 0 {
			_ = attemptRows.Close()
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return 0, 0, 0, err
			}
			return 0, 0, 0, ErrEventConflict
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_runner_attempts(
			stream_uid,attempt,runner_id,claim_token_sha256,claimed_input_revision,resume_source,
			resume_checkpoint_sequence,status,phase,phase_sequence,last_checkpoint_sequence,
			claimed_at,expires_at,finished_event_id,finished_at)
			VALUES(?,?,?,?,?,?,?,'running','claimed',?,?,?, ?,NULL,NULL)`,
			target.UID, fact.attempt, fact.runnerID, fact.claimDigest, fact.claimedRevision,
			fact.resumeSource, fact.resumeCheckpoint, fact.phaseSequence, fact.lastCheckpoint,
			fact.claimedAt, fact.expiresAt); err != nil {
			_ = attemptRows.Close()
			return 0, 0, 0, err
		}
		inserted, err := conn.ExecContext(ctx, `INSERT INTO transcript_events(
			stream_uid,event_id,publication_seq,client_message_id,event_type,source,
			runner_attempt,payload_json,frame_event_id,created_at)
			SELECT target_uid,target_event_id,target_event_id,client_message_id,event_type,'payload',
			runner_attempt,payload_json,NULL,created_at FROM `+canonicalCloneEventTable+
			` WHERE target_uid=? AND keep=1 AND runner_attempt=? ORDER BY target_event_id`, target.UID, fact.attempt)
		if err != nil {
			_ = attemptRows.Close()
			return 0, 0, 0, err
		}
		if changed, err := inserted.RowsAffected(); err != nil || changed <= 0 {
			_ = attemptRows.Close()
			if err != nil {
				return 0, 0, 0, err
			}
			return 0, 0, 0, ErrEventConflict
		}
		var receiptEvent int64
		var receiptStatus string
		var receiptFinished time.Time
		err = conn.QueryRowContext(ctx, `SELECT event_id,status,finished_at FROM transcript_runner_receipts
			WHERE stream_uid=? AND attempt=?`, source.UID, fact.attempt).
			Scan(&receiptEvent, &receiptStatus, &receiptFinished)
		if err == nil {
			if receiptEvent != fact.finishedEvent.Int64 || receiptStatus != fact.status ||
				!receiptFinished.Equal(fact.finishedAt.Time) {
				_ = attemptRows.Close()
				return 0, 0, 0, ErrEventConflict
			}
			if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_runner_receipts(
				stream_uid,attempt,event_id,status,finished_at) VALUES(?,?,?,?,?)`,
				target.UID, fact.attempt, targetFinished.Int64, receiptStatus, receiptFinished); err != nil {
				_ = attemptRows.Close()
				return 0, 0, 0, err
			}
			receiptCount++
		} else if !errors.Is(err, sql.ErrNoRows) {
			_ = attemptRows.Close()
			return 0, 0, 0, err
		} else if fact.status != "reclaimed" {
			_ = attemptRows.Close()
			return 0, 0, 0, ErrEventConflict
		}
		if _, err := conn.ExecContext(ctx, `UPDATE transcript_runner_attempts SET
			status=?,phase=?,phase_sequence=?,last_checkpoint_sequence=?,finished_event_id=?,finished_at=?
			WHERE stream_uid=? AND attempt=? AND status='running'`,
			fact.status, fact.phase, fact.phaseSequence, fact.lastCheckpoint, targetFinished.Int64,
			fact.finishedAt.Time, target.UID, fact.attempt); err != nil {
			_ = attemptRows.Close()
			return 0, 0, 0, err
		}
	}
	if err := attemptRows.Close(); err != nil {
		return 0, 0, 0, err
	}
	if err := attemptRows.Err(); err != nil {
		return 0, 0, 0, err
	}
	checkpointRows, err := conn.QueryContext(ctx, `SELECT checkpoint_sequence,runner_attempt,event_id,phase,resumable,created_at
		FROM transcript_runner_checkpoints WHERE stream_uid=? ORDER BY checkpoint_sequence`, source.UID)
	if err != nil {
		return 0, 0, 0, err
	}
	checkpointCount := 0
	for checkpointRows.Next() {
		var sequence, attempt, sourceEvent int64
		var phase string
		var resumable int
		var createdAt time.Time
		if err := checkpointRows.Scan(&sequence, &attempt, &sourceEvent, &phase, &resumable, &createdAt); err != nil {
			_ = checkpointRows.Close()
			return 0, 0, 0, err
		}
		targetEvent, err := canonicalCloneMappedSourceEventConn(ctx, conn, target.UID, sourceEvent)
		if err != nil || !targetEvent.Valid || targetEvent.Int64 <= 0 {
			_ = checkpointRows.Close()
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return 0, 0, 0, err
			}
			return 0, 0, 0, ErrEventConflict
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_runner_checkpoints(
			stream_uid,checkpoint_sequence,runner_attempt,event_id,phase,resumable,created_at)
			VALUES(?,?,?,?,?,?,?)`, target.UID, sequence, attempt, targetEvent.Int64, phase, resumable, createdAt); err != nil {
			_ = checkpointRows.Close()
			return 0, 0, 0, err
		}
		checkpointCount++
	}
	if err := checkpointRows.Close(); err != nil {
		return 0, 0, 0, err
	}
	if err := checkpointRows.Err(); err != nil {
		return 0, 0, 0, err
	}
	if attemptCount != counts.attempts || receiptCount != counts.receipts || checkpointCount != counts.checkpoints ||
		stage.eventCount < 0 {
		return 0, 0, 0, ErrEventConflict
	}
	return attemptCount, receiptCount, checkpointCount, nil
}

func planCanonicalCloneBranchesConn(
	ctx context.Context,
	conn *sql.Conn,
	source, target Stream,
	stage *canonicalCloneStage,
	writeTarget bool,
) (int, int, []byte, error) {
	digest := sha256.New()
	writePayloadGenesisDigestField(digest, []byte(stage.activeSource))
	writePayloadGenesisDigestField(digest, []byte(strconv.FormatInt(stage.branchGeneration, 10)))
	var branchRows *sql.Rows
	var err error
	if stage.legacyCutover == nil {
		branchRows, err = conn.QueryContext(ctx, `SELECT branch_id,branch_id,parent_branch_id,fork_event_id,
			fork_point,kind,client_mutation_id,request_sha256,source_message_id,created_at,updated_at
			FROM transcript_branches WHERE stream_uid=? ORDER BY branch_id`, source.UID)
	} else {
		branchRows, err = conn.QueryContext(ctx, `SELECT source_branch_id,target_branch_id,parent_target_branch_id,
			target_fork_event_key,fork_point,kind,client_mutation_id,request_sha256,source_message_id,created_at,updated_at
			FROM transcript_history_cutover_branches WHERE cutover_id=? ORDER BY source_branch_id`, stage.legacyCutover.id)
	}
	if err != nil {
		return 0, 0, nil, err
	}
	branchCount := 0
	for branchRows.Next() {
		var sourceID, targetID, kind, mutationID, sourceMessageID string
		var parent sql.NullString
		var forkSource any
		var forkPoint int64
		var requestSHA []byte
		var createdAt, updatedAt time.Time
		if err := branchRows.Scan(&sourceID, &targetID, &parent, &forkSource, &forkPoint, &kind,
			&mutationID, &requestSHA, &sourceMessageID, &createdAt, &updatedAt); err != nil {
			_ = branchRows.Close()
			return 0, 0, nil, err
		}
		branchCount++
		if strings.TrimSpace(sourceID) == "" || !validTranscriptBranchID(targetID) {
			_ = branchRows.Close()
			return 0, 0, nil, ErrEventConflict
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO `+canonicalCloneBranchTable+
			`(target_uid,source_branch_id,target_branch_id) VALUES(?,?,?)`, target.UID, sourceID, targetID); err != nil {
			_ = branchRows.Close()
			return 0, 0, nil, ErrEventConflict
		}
		var forkEvent any
		var forkKey sql.NullString
		if stage.legacyCutover == nil {
			if sourceFork, ok := forkSource.(int64); ok {
				forkKey = sql.NullString{String: "event:" + strconv.FormatInt(sourceFork, 10), Valid: true}
				targetFork, err := canonicalCloneMappedSourceEventConn(ctx, conn, target.UID, sourceFork)
				if err != nil || !targetFork.Valid {
					_ = branchRows.Close()
					if err != nil && !errors.Is(err, sql.ErrNoRows) {
						return 0, 0, nil, err
					}
					return 0, 0, nil, ErrEventConflict
				}
				forkEvent = targetFork.Int64
			}
		} else if sourceForkKey, ok := forkSource.(string); ok {
			forkKey = sql.NullString{String: sourceForkKey, Valid: true}
			var targetFork sql.NullInt64
			if err := conn.QueryRowContext(ctx, `SELECT target_event_id FROM `+canonicalCloneEventTable+
				` WHERE target_uid=? AND source_key=? AND keep=1`, target.UID, sourceForkKey).Scan(&targetFork); err != nil || !targetFork.Valid {
				_ = branchRows.Close()
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return 0, 0, nil, err
				}
				return 0, 0, nil, ErrEventConflict
			}
			forkEvent = targetFork.Int64
		}
		for _, value := range []string{
			sourceID, targetID, parent.String, strconv.FormatBool(parent.Valid), forkKey.String,
			strconv.FormatBool(forkKey.Valid), strconv.FormatInt(forkPoint, 10), kind, mutationID,
			sourceMessageID, createdAt.UTC().Format(time.RFC3339Nano), updatedAt.UTC().Format(time.RFC3339Nano),
		} {
			writePayloadGenesisDigestField(digest, []byte(value))
		}
		writePayloadGenesisDigestField(digest, requestSHA)
		if writeTarget {
			if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_branches(
				stream_uid,branch_id,parent_branch_id,fork_event_id,fork_point,kind,client_mutation_id,
				request_sha256,source_message_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
				target.UID, targetID, nullableStringValue(parent), forkEvent, forkPoint, kind,
				mutationID, requestSHA, sourceMessageID, createdAt, updatedAt); err != nil {
				_ = branchRows.Close()
				return 0, 0, nil, err
			}
		}
	}
	if err := branchRows.Close(); err != nil {
		return 0, 0, nil, err
	}
	if err := branchRows.Err(); err != nil {
		return 0, 0, nil, err
	}
	if branchCount == 0 {
		return 0, 0, nil, ErrEventConflict
	}
	var membershipRows *sql.Rows
	if stage.legacyCutover == nil {
		membershipRows, err = conn.QueryContext(ctx, `SELECT branch_id,ordinal,event_id FROM transcript_branch_events
			WHERE stream_uid=? ORDER BY branch_id,ordinal`, source.UID)
	} else {
		membershipRows, err = conn.QueryContext(ctx, `SELECT target_branch_id,target_ordinal,target_event_key
			FROM transcript_history_cutover_branch_events WHERE cutover_id=? ORDER BY target_branch_id,target_ordinal`,
			stage.legacyCutover.id)
	}
	if err != nil {
		return 0, 0, nil, err
	}
	membershipCount := 0
	lastBranch := ""
	var sourceOrdinal, targetOrdinal int64
	for membershipRows.Next() {
		var branchID string
		var ordinal int64
		var sourceEvent any
		if err := membershipRows.Scan(&branchID, &ordinal, &sourceEvent); err != nil {
			_ = membershipRows.Close()
			return 0, 0, nil, err
		}
		if branchID != lastBranch {
			lastBranch, sourceOrdinal, targetOrdinal = branchID, 0, 0
		}
		sourceOrdinal++
		if ordinal != sourceOrdinal {
			_ = membershipRows.Close()
			return 0, 0, nil, ErrEventConflict
		}
		var eventKey string
		var targetEvent sql.NullInt64
		if stage.legacyCutover == nil {
			sourceID, ok := sourceEvent.(int64)
			if !ok {
				_ = membershipRows.Close()
				return 0, 0, nil, ErrEventConflict
			}
			eventKey = "event:" + strconv.FormatInt(sourceID, 10)
			targetEvent, err = canonicalCloneMappedSourceEventConn(ctx, conn, target.UID, sourceID)
		} else {
			var ok bool
			eventKey, ok = sourceEvent.(string)
			if !ok {
				_ = membershipRows.Close()
				return 0, 0, nil, ErrEventConflict
			}
			err = conn.QueryRowContext(ctx, `SELECT target_event_id FROM `+canonicalCloneEventTable+
				` WHERE target_uid=? AND source_key=?`, target.UID, eventKey).Scan(&targetEvent)
		}
		if err != nil {
			_ = membershipRows.Close()
			if !errors.Is(err, sql.ErrNoRows) {
				return 0, 0, nil, err
			}
			return 0, 0, nil, ErrEventConflict
		}
		if !targetEvent.Valid {
			continue
		}
		targetOrdinal++
		membershipCount++
		writePayloadGenesisDigestField(digest, []byte(branchID))
		writePayloadGenesisDigestField(digest, []byte(strconv.FormatInt(targetOrdinal, 10)))
		writePayloadGenesisDigestField(digest, []byte(eventKey))
		if writeTarget {
			if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_branch_events(stream_uid,branch_id,ordinal,event_id)
				VALUES(?,?,?,?)`, target.UID, branchID, targetOrdinal, targetEvent.Int64); err != nil {
				_ = membershipRows.Close()
				return 0, 0, nil, err
			}
		}
	}
	if err := membershipRows.Close(); err != nil {
		return 0, 0, nil, err
	}
	if err := membershipRows.Err(); err != nil {
		return 0, 0, nil, err
	}
	var activeTarget string
	if err := conn.QueryRowContext(ctx, `SELECT target_branch_id FROM `+canonicalCloneBranchTable+
		` WHERE target_uid=? AND source_branch_id=?`, target.UID, stage.activeSource).Scan(&activeTarget); err != nil {
		return 0, 0, nil, err
	}
	stage.activeTarget = activeTarget
	if writeTarget {
		var updatedAt time.Time
		if err := conn.QueryRowContext(ctx, `SELECT updated_at FROM transcript_branch_state WHERE stream_uid=?`, source.UID).
			Scan(&updatedAt); err != nil {
			return 0, 0, nil, err
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_branch_state(stream_uid,active_branch_id,generation,updated_at)
			VALUES(?,?,?,?)`, target.UID, activeTarget, stage.branchGeneration, updatedAt); err != nil {
			return 0, 0, nil, err
		}
		var emptyBranches int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcript_branches branch
			WHERE branch.stream_uid=? AND NOT EXISTS(SELECT 1 FROM transcript_branch_events member
				WHERE member.stream_uid=branch.stream_uid AND member.branch_id=branch.branch_id)`, target.UID).
			Scan(&emptyBranches); err != nil {
			return 0, 0, nil, err
		}
		if emptyBranches != 0 {
			var validEmpty int
			if stage.eventCount == 0 && branchCount == 1 && membershipCount == 0 {
				if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcript_branches WHERE stream_uid=?
					AND branch_id=? AND parent_branch_id IS NULL AND fork_event_id IS NULL AND fork_point=0 AND kind='base'`,
					target.UID, activeTarget).Scan(&validEmpty); err != nil {
					return 0, 0, nil, err
				}
			}
			if validEmpty != 1 {
				return 0, 0, nil, ErrEventConflict
			}
		}
	}
	return branchCount, membershipCount, digest.Sum(nil), nil
}

func nullableStringValue(value sql.NullString) any {
	if value.Valid {
		return value.String
	}
	return nil
}

func planCanonicalCloneArtifactsConn(
	ctx context.Context,
	conn *sql.Conn,
	source, target Stream,
	stage canonicalCloneStage,
	writeTarget bool,
) (int, int, []byte, error) {
	digest := sha256.New()
	commitRows, err := conn.QueryContext(ctx, `SELECT runner_attempt,source_event_id,ordinal,artifact_id,version_id,
		relation,bound_event_id,created_at FROM transcript_artifact_commits WHERE stream_uid=?
		ORDER BY runner_attempt,source_event_id,ordinal`, source.UID)
	if err != nil {
		return 0, 0, nil, err
	}
	commitCount := 0
	for commitRows.Next() {
		var attempt, sourceEvent, ordinal, boundEvent int64
		var artifactID, versionID string
		var relation ArtifactRelation
		var createdAt time.Time
		if err := commitRows.Scan(&attempt, &sourceEvent, &ordinal, &artifactID, &versionID,
			&relation, &boundEvent, &createdAt); err != nil {
			_ = commitRows.Close()
			return 0, 0, nil, err
		}
		commit := payloadHistoryArtifactCommitPlan{
			runnerAttempt: attempt, sourceEventID: sourceEvent, ordinal: ordinal,
			artifactID: artifactID, versionID: versionID, relation: relation, createdAt: createdAt,
		}
		if !validPayloadHistoryArtifactCommit(commit) {
			_ = commitRows.Close()
			return 0, 0, nil, ErrEventConflict
		}
		if err := validatePayloadHistoryArtifactSourceConn(
			ctx, conn, source, artifactID, versionID, relation, ArtifactAvailable,
		); err != nil {
			_ = commitRows.Close()
			return 0, 0, nil, err
		}
		targetSourceValue, err := canonicalCloneMappedSourceEventConn(ctx, conn, target.UID, sourceEvent)
		if err != nil || !targetSourceValue.Valid {
			_ = commitRows.Close()
			return 0, 0, nil, ErrEventConflict
		}
		targetBoundValue, err := canonicalCloneMappedSourceEventConn(ctx, conn, target.UID, boundEvent)
		if err != nil || !targetBoundValue.Valid {
			_ = commitRows.Close()
			return 0, 0, nil, ErrEventConflict
		}
		targetSource, targetBound := targetSourceValue.Int64, targetBoundValue.Int64
		writePayloadHistoryArtifactDigest(digest, attempt, targetSource, ordinal, artifactID, versionID,
			string(relation), targetBound, createdAt.UTC().Format(time.RFC3339Nano))
		if writeTarget {
			if err := validatePayloadHistoryArtifactTargetEventConn(ctx, conn, target.UID, attempt, targetSource); err != nil {
				_ = commitRows.Close()
				return 0, 0, nil, err
			}
			if err := validatePayloadHistoryArtifactTargetEventConn(ctx, conn, target.UID, attempt, targetBound); err != nil {
				_ = commitRows.Close()
				return 0, 0, nil, err
			}
			if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_artifact_commits(
				stream_uid,runner_attempt,source_event_id,ordinal,artifact_id,version_id,relation,bound_event_id,created_at)
				VALUES(?,?,?,?,?,?,?,?,?)`, target.UID, attempt, targetSource, ordinal, artifactID, versionID,
				relation, targetBound, createdAt); err != nil {
				_ = commitRows.Close()
				return 0, 0, nil, err
			}
		}
		commitCount++
	}
	if err := commitRows.Close(); err != nil {
		return 0, 0, nil, err
	}
	if err := commitRows.Err(); err != nil {
		return 0, 0, nil, err
	}
	var refRows *sql.Rows
	if stage.legacyCutover == nil {
		refRows, err = conn.QueryContext(ctx, `SELECT runner_attempt,source_event_id,ordinal,artifact_id,version_id,
			relation,availability,created_at FROM transcript_artifact_refs WHERE stream_uid=?
			ORDER BY runner_attempt,source_event_id,ordinal`, source.UID)
	} else {
		refRows, err = conn.QueryContext(ctx, `SELECT event.runner_attempt,ref.target_event_key,ref.ordinal,
			ref.artifact_id,ref.version_id,ref.relation,ref.availability,ref.created_at
			FROM transcript_history_cutover_artifact_refs ref
			JOIN `+canonicalCloneEventTable+` event ON event.target_uid=? AND event.source_key=ref.target_event_key
			WHERE ref.cutover_id=? ORDER BY ref.target_event_key,ref.ordinal`, target.UID, stage.legacyCutover.id)
	}
	if err != nil {
		return 0, 0, nil, err
	}
	refCount := 0
	for refRows.Next() {
		var attempt, ordinal int64
		var sourceEvent any
		var artifactID, versionID string
		var relation ArtifactRelation
		var availability ArtifactAvailability
		var createdAt time.Time
		if err := refRows.Scan(&attempt, &sourceEvent, &ordinal, &artifactID, &versionID,
			&relation, &availability, &createdAt); err != nil {
			_ = refRows.Close()
			return 0, 0, nil, err
		}
		var targetEvent int64
		if stage.legacyCutover == nil {
			sourceID, ok := sourceEvent.(int64)
			if !ok {
				_ = refRows.Close()
				return 0, 0, nil, ErrEventConflict
			}
			targetValue, err := canonicalCloneMappedSourceEventConn(ctx, conn, target.UID, sourceID)
			if err != nil || !targetValue.Valid {
				_ = refRows.Close()
				return 0, 0, nil, ErrEventConflict
			}
			targetEvent = targetValue.Int64
		} else {
			eventKey, ok := sourceEvent.(string)
			if !ok {
				_ = refRows.Close()
				return 0, 0, nil, ErrEventConflict
			}
			if err := conn.QueryRowContext(ctx, `SELECT target_event_id FROM `+canonicalCloneEventTable+
				` WHERE target_uid=? AND source_key=? AND keep=1`, target.UID, eventKey).Scan(&targetEvent); err != nil {
				_ = refRows.Close()
				return 0, 0, nil, ErrEventConflict
			}
		}
		ref := payloadHistoryArtifactRefPlan{
			runnerAttempt: attempt, sourceEventID: targetEvent, ordinal: ordinal,
			artifactID: artifactID, versionID: versionID, relation: relation,
			availability: availability, createdAt: createdAt,
		}
		if !validPayloadHistoryArtifactRef(ref) {
			_ = refRows.Close()
			return 0, 0, nil, ErrEventConflict
		}
		if err := validatePayloadHistoryArtifactSourceConn(
			ctx, conn, source, artifactID, versionID, relation, availability,
		); err != nil {
			_ = refRows.Close()
			return 0, 0, nil, err
		}
		writePayloadHistoryArtifactDigest(digest, attempt, targetEvent, ordinal, artifactID, versionID,
			string(relation), string(availability), createdAt.UTC().Format(time.RFC3339Nano))
		if writeTarget {
			if err := validatePayloadHistoryArtifactTargetEventConn(ctx, conn, target.UID, attempt, targetEvent); err != nil {
				_ = refRows.Close()
				return 0, 0, nil, err
			}
			if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_artifact_refs(
				stream_uid,runner_attempt,source_event_id,ordinal,artifact_id,version_id,relation,availability,created_at)
				VALUES(?,?,?,?,?,?,?,?,?)`, target.UID, attempt, targetEvent, ordinal, artifactID, versionID,
				relation, availability, createdAt); err != nil {
				_ = refRows.Close()
				return 0, 0, nil, err
			}
		}
		refCount++
	}
	if err := refRows.Close(); err != nil {
		return 0, 0, nil, err
	}
	if err := refRows.Err(); err != nil {
		return 0, 0, nil, err
	}
	return commitCount, refCount, digest.Sum(nil), nil
}

func materializeCanonicalCloneStageConn(
	ctx context.Context,
	conn *sql.Conn,
	source, target Stream,
	stage canonicalCloneStage,
	counts canonicalCloneSourceCounts,
) (canonicalCloneMaterialized, []byte, error) {
	attempts, receipts, checkpoints, err := materializeCanonicalCloneRunnerConn(
		ctx, conn, source, target, stage, counts,
	)
	if err != nil {
		return canonicalCloneMaterialized{}, nil, err
	}
	branches, branchEvents, branchDigest, err := planCanonicalCloneBranchesConn(
		ctx, conn, source, target, &stage, true,
	)
	if err != nil {
		return canonicalCloneMaterialized{}, nil, err
	}
	artifactCommits, artifactRefs, artifactDigest, err := planCanonicalCloneArtifactsConn(
		ctx, conn, source, target, stage, true,
	)
	if err != nil {
		return canonicalCloneMaterialized{}, nil, err
	}
	runnerDigest, err := digestCanonicalCloneRunnerFactsConn(ctx, conn, source.UID)
	if err != nil {
		return canonicalCloneMaterialized{}, nil, err
	}
	digestParts := make([][]byte, 0, 9)
	if stage.legacyCutover != nil {
		digestParts = append(digestParts,
			stage.legacyCutover.id,
			stage.legacyCutover.verification,
			stage.legacyCutover.lineage,
			stage.legacyCutover.cursor,
			stage.legacyCutover.shadow,
		)
	}
	digestParts = append(digestParts, stage.eventDigest, branchDigest, artifactDigest, runnerDigest)
	return canonicalCloneMaterialized{
		attempts: attempts, receipts: receipts, checkpoints: checkpoints,
		branches: branches, branchEvents: branchEvents,
		artifactCommits: artifactCommits, artifactRefs: artifactRefs,
		activeTarget: stage.activeTarget, branchDigest: branchDigest, artifactDigest: artifactDigest,
	}, combinePayloadHistoryCloneDigests(digestParts...), nil
}

func expectedCanonicalCloneStageConn(
	ctx context.Context,
	conn *sql.Conn,
	source, target Stream,
	stage canonicalCloneStage,
) (canonicalCloneMaterialized, []byte, error) {
	branches, branchEvents, branchDigest, err := planCanonicalCloneBranchesConn(
		ctx, conn, source, target, &stage, false,
	)
	if err != nil {
		return canonicalCloneMaterialized{}, nil, err
	}
	artifactCommits, artifactRefs, artifactDigest, err := planCanonicalCloneArtifactsConn(
		ctx, conn, source, target, stage, false,
	)
	if err != nil {
		return canonicalCloneMaterialized{}, nil, err
	}
	runnerDigest, err := digestCanonicalCloneRunnerFactsConn(ctx, conn, source.UID)
	if err != nil {
		return canonicalCloneMaterialized{}, nil, err
	}
	digestParts := make([][]byte, 0, 9)
	if stage.legacyCutover != nil {
		digestParts = append(digestParts,
			stage.legacyCutover.id,
			stage.legacyCutover.verification,
			stage.legacyCutover.lineage,
			stage.legacyCutover.cursor,
			stage.legacyCutover.shadow,
		)
	}
	digestParts = append(digestParts, stage.eventDigest, branchDigest, artifactDigest, runnerDigest)
	return canonicalCloneMaterialized{
		branches: branches, branchEvents: branchEvents,
		artifactCommits: artifactCommits, artifactRefs: artifactRefs,
		activeTarget: stage.activeTarget, branchDigest: branchDigest, artifactDigest: artifactDigest,
	}, combinePayloadHistoryCloneDigests(digestParts...), nil
}

func loadExistingCanonicalCloneStreamingConn(
	ctx context.Context,
	conn *sql.Conn,
	source, target Stream,
	input CloneFrameHistoryInput,
	stage canonicalCloneStage,
) (CloneFrameHistoryResult, bool, error) {
	var genesisID, sourceSHA []byte
	var sourceUID, activeBranch string
	var sourceEpoch, sourceEventCount, branchGeneration int64
	err := conn.QueryRowContext(ctx, `SELECT receipt.genesis_id,receipt.source_stream_uid,receipt.source_epoch,
		receipt.source_event_count,receipt.source_sha256,receipt.active_branch_id,receipt.branch_generation
		FROM transcript_frame_authority authority
		JOIN transcript_payload_genesis_receipts receipt ON receipt.genesis_id=authority.genesis_id
		WHERE authority.owner_id=? AND authority.session_id=? AND authority.active_stream_uid=?
			AND authority.active_epoch=1 AND authority.authority_generation=1
			AND authority.read_authority='transcript_payload_v1'
			AND authority.write_authority='transcript_payload_v1'
			AND authority.activation_id IS NULL AND receipt.source_kind='canonical_clone'
			AND receipt.stream_uid=authority.active_stream_uid AND receipt.status='active'`,
		target.OwnerID, target.SessionID, target.UID).Scan(
		&genesisID, &sourceUID, &sourceEpoch, &sourceEventCount, &sourceSHA, &activeBranch, &branchGeneration,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return CloneFrameHistoryResult{}, false, nil
	}
	if err != nil {
		return CloneFrameHistoryResult{}, false, err
	}
	if len(genesisID) != sha256.Size || len(sourceSHA) != sha256.Size || sourceUID != source.UID ||
		sourceEpoch != source.Epoch || sourceEventCount != int64(stage.eventCount) ||
		activeBranch == "" || branchGeneration != stage.branchGeneration {
		return CloneFrameHistoryResult{}, false, ErrEventConflict
	}
	expected, expectedDigest, err := expectedCanonicalCloneStageConn(ctx, conn, source, target, stage)
	if err != nil {
		return CloneFrameHistoryResult{}, false, fmt.Errorf("rebuild canonical clone expectation: %w", err)
	}
	if !bytes.Equal(expectedDigest, sourceSHA) || expected.activeTarget != activeBranch ||
		target.InputRevision != source.InputRevision || target.ConsumedInputRevision != source.InputRevision ||
		target.NextEventID != sourceEventCount+1 || target.NextPublication != sourceEventCount+1 ||
		target.NextCheckpoint != source.NextCheckpoint {
		return CloneFrameHistoryResult{}, false, fmt.Errorf("canonical clone receipt or stream changed: %w", ErrEventConflict)
	}
	if err := verifyCanonicalCloneTargetEventsConn(ctx, conn, target.UID, stage.eventCount); err != nil {
		return CloneFrameHistoryResult{}, false, fmt.Errorf("verify canonical clone events: %w", err)
	}
	if err := verifyCanonicalCloneTargetRunnerConn(ctx, conn, source.UID, target.UID, input.sourceCounts); err != nil {
		return CloneFrameHistoryResult{}, false, fmt.Errorf("verify canonical clone runner facts: %w", err)
	}
	branchDigest, branchCount, branchEventCount, err := digestCanonicalCloneTargetBranchesConn(
		ctx, conn, target.UID, stage.activeSource,
	)
	if err != nil || !bytes.Equal(branchDigest, expected.branchDigest) ||
		branchCount != expected.branches || branchEventCount != expected.branchEvents {
		if err != nil {
			return CloneFrameHistoryResult{}, false, err
		}
		return CloneFrameHistoryResult{}, false, fmt.Errorf("verify canonical clone branches: %w", ErrEventConflict)
	}
	artifactDigest, artifactCommitCount, artifactRefCount, err := digestCanonicalCloneTargetArtifactsConn(ctx, conn, target.UID)
	if err != nil || !bytes.Equal(artifactDigest, expected.artifactDigest) ||
		artifactCommitCount != expected.artifactCommits || artifactRefCount != expected.artifactRefs {
		if err != nil {
			return CloneFrameHistoryResult{}, false, err
		}
		return CloneFrameHistoryResult{}, false, fmt.Errorf("verify canonical clone artifacts: %w", ErrEventConflict)
	}
	return CloneFrameHistoryResult{
		TargetStream: target, GenesisSHA256: hexDigest(genesisID), SourceSHA256: hexDigest(sourceSHA),
		EventCount: stage.eventCount, AttemptCount: input.sourceCounts.attempts,
		CheckpointCount: input.sourceCounts.checkpoints, BranchCount: branchCount,
		BranchEventCount: branchEventCount, ArtifactCommitCount: artifactCommitCount,
		ArtifactRefCount: artifactRefCount, ActiveBranchID: activeBranch,
	}, true, nil
}

func verifyCanonicalCloneTargetEventsConn(
	ctx context.Context, conn *sql.Conn, targetUID string, expectedCount int,
) error {
	var targetCount, mismatched int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcript_events WHERE stream_uid=?`, targetUID).
		Scan(&targetCount); err != nil {
		return err
	}
	if targetCount != expectedCount {
		return ErrEventConflict
	}
	err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+canonicalCloneEventTable+` expected
		LEFT JOIN transcript_events actual ON actual.stream_uid=expected.target_uid
			AND actual.event_id=expected.target_event_id
		WHERE expected.target_uid=? AND expected.keep=1 AND (
			actual.event_id IS NULL OR actual.publication_seq!=expected.target_event_id OR
			actual.client_message_id!=expected.client_message_id OR actual.event_type!=expected.event_type OR
			actual.source!='payload' OR actual.runner_attempt IS NOT expected.runner_attempt OR
			actual.payload_json IS NOT expected.payload_json OR actual.frame_event_id IS NOT NULL OR
			actual.created_at IS NOT expected.created_at)`, targetUID).Scan(&mismatched)
	if err != nil {
		return err
	}
	if mismatched != 0 {
		return ErrEventConflict
	}
	return nil
}

func verifyCanonicalCloneTargetRunnerConn(
	ctx context.Context,
	conn *sql.Conn,
	sourceUID, targetUID string,
	counts canonicalCloneSourceCounts,
) error {
	attemptRows, err := conn.QueryContext(ctx, `SELECT attempt,runner_id,claim_token_sha256,claimed_input_revision,
		resume_source,resume_checkpoint_sequence,status,phase,phase_sequence,last_checkpoint_sequence,
		claimed_at,expires_at,finished_event_id,finished_at
		FROM transcript_runner_attempts WHERE stream_uid=? ORDER BY attempt`, sourceUID)
	if err != nil {
		return err
	}
	attemptCount := 0
	for attemptRows.Next() {
		source, err := scanCanonicalCloneAttempt(attemptRows)
		if err != nil {
			_ = attemptRows.Close()
			return err
		}
		mapped, err := canonicalCloneMappedSourceEventConn(ctx, conn, targetUID, source.finishedEvent.Int64)
		if err != nil || !mapped.Valid {
			_ = attemptRows.Close()
			return ErrEventConflict
		}
		target, err := scanCanonicalCloneAttempt(conn.QueryRowContext(ctx, `SELECT attempt,runner_id,
			claim_token_sha256,claimed_input_revision,resume_source,resume_checkpoint_sequence,status,phase,
			phase_sequence,last_checkpoint_sequence,claimed_at,expires_at,finished_event_id,finished_at
			FROM transcript_runner_attempts WHERE stream_uid=? AND attempt=?`, targetUID, source.attempt))
		if err != nil || !canonicalCloneAttemptEqual(source, target, mapped.Int64) {
			_ = attemptRows.Close()
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			return ErrEventConflict
		}
		attemptCount++
	}
	if err := attemptRows.Close(); err != nil {
		return err
	}
	if err := attemptRows.Err(); err != nil {
		return err
	}
	receiptRows, err := conn.QueryContext(ctx, `SELECT attempt,event_id,status,finished_at
		FROM transcript_runner_receipts WHERE stream_uid=? ORDER BY attempt`, sourceUID)
	if err != nil {
		return err
	}
	receiptCount := 0
	for receiptRows.Next() {
		var attempt, sourceEvent int64
		var status string
		var finishedAt time.Time
		if err := receiptRows.Scan(&attempt, &sourceEvent, &status, &finishedAt); err != nil {
			_ = receiptRows.Close()
			return err
		}
		mapped, err := canonicalCloneMappedSourceEventConn(ctx, conn, targetUID, sourceEvent)
		if err != nil || !mapped.Valid {
			_ = receiptRows.Close()
			return ErrEventConflict
		}
		var targetEvent int64
		var targetStatus string
		var targetFinished time.Time
		err = conn.QueryRowContext(ctx, `SELECT event_id,status,finished_at FROM transcript_runner_receipts
			WHERE stream_uid=? AND attempt=?`, targetUID, attempt).Scan(&targetEvent, &targetStatus, &targetFinished)
		if err != nil || targetEvent != mapped.Int64 || targetStatus != status || !targetFinished.Equal(finishedAt) {
			_ = receiptRows.Close()
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			return ErrEventConflict
		}
		receiptCount++
	}
	if err := receiptRows.Close(); err != nil {
		return err
	}
	if err := receiptRows.Err(); err != nil {
		return err
	}
	checkpointRows, err := conn.QueryContext(ctx, `SELECT checkpoint_sequence,runner_attempt,event_id,phase,resumable,created_at
		FROM transcript_runner_checkpoints WHERE stream_uid=? ORDER BY checkpoint_sequence`, sourceUID)
	if err != nil {
		return err
	}
	checkpointCount := 0
	for checkpointRows.Next() {
		var sequence, attempt, sourceEvent int64
		var phase string
		var resumable int
		var createdAt time.Time
		if err := checkpointRows.Scan(&sequence, &attempt, &sourceEvent, &phase, &resumable, &createdAt); err != nil {
			_ = checkpointRows.Close()
			return err
		}
		mapped, err := canonicalCloneMappedSourceEventConn(ctx, conn, targetUID, sourceEvent)
		if err != nil || !mapped.Valid {
			_ = checkpointRows.Close()
			return ErrEventConflict
		}
		var targetAttempt, targetEvent int64
		var targetPhase string
		var targetResumable int
		var targetCreated time.Time
		err = conn.QueryRowContext(ctx, `SELECT runner_attempt,event_id,phase,resumable,created_at
			FROM transcript_runner_checkpoints WHERE stream_uid=? AND checkpoint_sequence=?`, targetUID, sequence).
			Scan(&targetAttempt, &targetEvent, &targetPhase, &targetResumable, &targetCreated)
		if err != nil || targetAttempt != attempt || targetEvent != mapped.Int64 || targetPhase != phase ||
			targetResumable != resumable || !targetCreated.Equal(createdAt) {
			_ = checkpointRows.Close()
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			return ErrEventConflict
		}
		checkpointCount++
	}
	if err := checkpointRows.Close(); err != nil {
		return err
	}
	if err := checkpointRows.Err(); err != nil {
		return err
	}
	var targetAttempts, targetReceipts, targetCheckpoints int
	if err := conn.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM transcript_runner_attempts WHERE stream_uid=?),
		(SELECT COUNT(*) FROM transcript_runner_receipts WHERE stream_uid=?),
		(SELECT COUNT(*) FROM transcript_runner_checkpoints WHERE stream_uid=?)`,
		targetUID, targetUID, targetUID).Scan(&targetAttempts, &targetReceipts, &targetCheckpoints); err != nil {
		return err
	}
	if attemptCount != counts.attempts || receiptCount != counts.receipts || checkpointCount != counts.checkpoints ||
		targetAttempts != counts.attempts || targetReceipts != counts.receipts || targetCheckpoints != counts.checkpoints {
		return ErrEventConflict
	}
	return nil
}

func digestCanonicalCloneTargetBranchesConn(
	ctx context.Context, conn *sql.Conn, targetUID, activeSource string,
) ([]byte, int, int, error) {
	var activeTarget string
	var generation int64
	if err := conn.QueryRowContext(ctx, `SELECT active_branch_id,generation FROM transcript_branch_state
		WHERE stream_uid=?`, targetUID).Scan(&activeTarget, &generation); err != nil {
		return nil, 0, 0, err
	}
	var mappedActive string
	if err := conn.QueryRowContext(ctx, `SELECT target_branch_id FROM `+canonicalCloneBranchTable+
		` WHERE target_uid=? AND source_branch_id=?`, targetUID, activeSource).Scan(&mappedActive); err != nil {
		return nil, 0, 0, err
	}
	if mappedActive != activeTarget || generation <= 0 {
		return nil, 0, 0, ErrEventConflict
	}
	digest := sha256.New()
	writePayloadGenesisDigestField(digest, []byte(activeSource))
	writePayloadGenesisDigestField(digest, []byte(strconv.FormatInt(generation, 10)))
	rows, err := conn.QueryContext(ctx, `SELECT mapping.source_branch_id,branch.branch_id,
		branch.parent_branch_id,branch.fork_event_id,event.source_key,branch.fork_point,branch.kind,
		branch.client_mutation_id,branch.request_sha256,branch.source_message_id,branch.created_at,branch.updated_at
		FROM transcript_branches branch
		JOIN `+canonicalCloneBranchTable+` mapping ON mapping.target_uid=branch.stream_uid
			AND mapping.target_branch_id=branch.branch_id
		LEFT JOIN `+canonicalCloneEventTable+` event ON event.target_uid=branch.stream_uid
			AND event.target_event_id=branch.fork_event_id
		WHERE branch.stream_uid=? ORDER BY mapping.source_branch_id`, targetUID)
	if err != nil {
		return nil, 0, 0, err
	}
	branchCount := 0
	for rows.Next() {
		var sourceID, targetID, kind, mutationID, sourceMessageID string
		var parent, forkKey sql.NullString
		var forkEvent sql.NullInt64
		var forkPoint int64
		var requestSHA []byte
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&sourceID, &targetID, &parent, &forkEvent, &forkKey, &forkPoint,
			&kind, &mutationID, &requestSHA, &sourceMessageID, &createdAt, &updatedAt); err != nil {
			_ = rows.Close()
			return nil, 0, 0, err
		}
		if forkEvent.Valid != forkKey.Valid {
			_ = rows.Close()
			return nil, 0, 0, ErrEventConflict
		}
		for _, value := range []string{
			sourceID, targetID, parent.String, strconv.FormatBool(parent.Valid), forkKey.String,
			strconv.FormatBool(forkKey.Valid), strconv.FormatInt(forkPoint, 10), kind, mutationID,
			sourceMessageID, createdAt.UTC().Format(time.RFC3339Nano), updatedAt.UTC().Format(time.RFC3339Nano),
		} {
			writePayloadGenesisDigestField(digest, []byte(value))
		}
		writePayloadGenesisDigestField(digest, requestSHA)
		branchCount++
	}
	if err := rows.Close(); err != nil {
		return nil, 0, 0, err
	}
	if err := rows.Err(); err != nil {
		return nil, 0, 0, err
	}
	membershipRows, err := conn.QueryContext(ctx, `SELECT member.branch_id,member.ordinal,event.source_key
		FROM transcript_branch_events member
		LEFT JOIN `+canonicalCloneEventTable+` event ON event.target_uid=member.stream_uid
			AND event.target_event_id=member.event_id
		WHERE member.stream_uid=? ORDER BY member.branch_id,member.ordinal`, targetUID)
	if err != nil {
		return nil, 0, 0, err
	}
	membershipCount := 0
	for membershipRows.Next() {
		var branchID string
		var ordinal int64
		var sourceKey sql.NullString
		if err := membershipRows.Scan(&branchID, &ordinal, &sourceKey); err != nil {
			_ = membershipRows.Close()
			return nil, 0, 0, err
		}
		if !sourceKey.Valid {
			_ = membershipRows.Close()
			return nil, 0, 0, ErrEventConflict
		}
		writePayloadGenesisDigestField(digest, []byte(branchID))
		writePayloadGenesisDigestField(digest, []byte(strconv.FormatInt(ordinal, 10)))
		writePayloadGenesisDigestField(digest, []byte(sourceKey.String))
		membershipCount++
	}
	if err := membershipRows.Close(); err != nil {
		return nil, 0, 0, err
	}
	if err := membershipRows.Err(); err != nil {
		return nil, 0, 0, err
	}
	var totalBranches, totalMemberships int
	if err := conn.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM transcript_branches WHERE stream_uid=?),
		(SELECT COUNT(*) FROM transcript_branch_events WHERE stream_uid=?)`, targetUID, targetUID).
		Scan(&totalBranches, &totalMemberships); err != nil {
		return nil, 0, 0, err
	}
	if totalBranches != branchCount || totalMemberships != membershipCount {
		return nil, 0, 0, ErrEventConflict
	}
	return digest.Sum(nil), branchCount, membershipCount, nil
}

func digestCanonicalCloneTargetArtifactsConn(
	ctx context.Context, conn *sql.Conn, targetUID string,
) ([]byte, int, int, error) {
	digest := sha256.New()
	commitRows, err := conn.QueryContext(ctx, `SELECT runner_attempt,source_event_id,ordinal,artifact_id,version_id,
		relation,bound_event_id,created_at FROM transcript_artifact_commits WHERE stream_uid=?
		ORDER BY runner_attempt,source_event_id,ordinal`, targetUID)
	if err != nil {
		return nil, 0, 0, err
	}
	commitCount := 0
	for commitRows.Next() {
		var attempt, sourceEvent, ordinal, boundEvent int64
		var artifactID, versionID, relation string
		var createdAt time.Time
		if err := commitRows.Scan(&attempt, &sourceEvent, &ordinal, &artifactID, &versionID,
			&relation, &boundEvent, &createdAt); err != nil {
			_ = commitRows.Close()
			return nil, 0, 0, err
		}
		writePayloadHistoryArtifactDigest(digest, attempt, sourceEvent, ordinal, artifactID, versionID,
			relation, boundEvent, createdAt.UTC().Format(time.RFC3339Nano))
		commitCount++
	}
	if err := commitRows.Close(); err != nil {
		return nil, 0, 0, err
	}
	if err := commitRows.Err(); err != nil {
		return nil, 0, 0, err
	}
	refRows, err := conn.QueryContext(ctx, `SELECT runner_attempt,source_event_id,ordinal,artifact_id,version_id,
		relation,availability,created_at FROM transcript_artifact_refs WHERE stream_uid=?
		ORDER BY runner_attempt,source_event_id,ordinal`, targetUID)
	if err != nil {
		return nil, 0, 0, err
	}
	refCount := 0
	for refRows.Next() {
		var attempt, sourceEvent, ordinal int64
		var artifactID, versionID, relation, availability string
		var createdAt time.Time
		if err := refRows.Scan(&attempt, &sourceEvent, &ordinal, &artifactID, &versionID,
			&relation, &availability, &createdAt); err != nil {
			_ = refRows.Close()
			return nil, 0, 0, err
		}
		writePayloadHistoryArtifactDigest(digest, attempt, sourceEvent, ordinal, artifactID, versionID,
			relation, availability, createdAt.UTC().Format(time.RFC3339Nano))
		refCount++
	}
	if err := refRows.Close(); err != nil {
		return nil, 0, 0, err
	}
	if err := refRows.Err(); err != nil {
		return nil, 0, 0, err
	}
	return digest.Sum(nil), commitCount, refCount, nil
}
