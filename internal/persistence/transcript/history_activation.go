package transcript

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"strings"
	"time"

	"synon-go/internal/realtime"
)

type ActivateAskUserHistoryCutoverInput struct {
	CutoverID, OwnerID                                   string
	MaxBranches, MaxEvents, MaxAttempts, MaxCheckpoints  int
	MaxBranchEvents, MaxArtifactCommits, MaxArtifactRefs int
	MaxRoutes                                            int
}

type AskUserHistoryActivation struct {
	ActivationSHA256, CutoverID, SourceStreamUID, TargetStreamUID string
	SourceEpoch, TargetEpoch, AuthorityGeneration                 int64
	ActiveBranchID                                                string
	AttemptCount, ReceiptCount, CheckpointCount, EventCount       int
	BranchCount, BranchEventCount, CursorCount                    int
	ArtifactCommitCount, ArtifactRefCount, RouteCount             int
	Active                                                        bool
	ActivatedAt                                                   time.Time
}

type storedHistoryActivationCutover struct {
	id, verification, lineage, cursor, shadow []byte
	streamUID, ownerID, sourceBranchID        string
	generation, through                       int64
	branchCount, eventCount, cursorCount      int
}

type activationEvent struct {
	key, clientMessageID, eventType string
	sourceEventID, runnerAttempt    sql.NullInt64
	publication                     int64
	payload                         []byte
	createdAt                       time.Time
}

func (r *Repository) ActivateAskUserHistoryCutover(
	ctx context.Context, input ActivateAskUserHistoryCutoverInput,
) (AskUserHistoryActivation, bool, error) {
	if r == nil || r.db == nil {
		return AskUserHistoryActivation{}, false, ErrSchemaUnavailable
	}
	input.CutoverID = strings.TrimSpace(input.CutoverID)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	if len(input.CutoverID) != sha256.Size*2 || input.OwnerID == "" ||
		input.MaxBranches <= 0 || input.MaxBranches > 1024 || input.MaxEvents <= 0 || input.MaxEvents > 100000 ||
		input.MaxAttempts <= 0 || input.MaxAttempts > 10000 || input.MaxCheckpoints <= 0 || input.MaxCheckpoints > 100000 ||
		input.MaxBranchEvents <= 0 || input.MaxBranchEvents > 100000 ||
		input.MaxArtifactCommits <= 0 || input.MaxArtifactCommits > 100000 ||
		input.MaxArtifactRefs <= 0 || input.MaxArtifactRefs > 100000 ||
		input.MaxRoutes <= 0 || input.MaxRoutes > 1024 {
		return AskUserHistoryActivation{}, false, errors.New("complete history activation authority and bounded resources are required")
	}
	cutoverID, err := hex.DecodeString(input.CutoverID)
	if err != nil || len(cutoverID) != sha256.Size {
		return AskUserHistoryActivation{}, false, ErrEventConflict
	}
	var result AskUserHistoryActivation
	created := false
	err = r.withImmediate(ctx, func(conn *sql.Conn) error {
		cutover, err := loadHistoryActivationCutoverConn(ctx, conn, cutoverID, input.OwnerID)
		if err != nil {
			return fmt.Errorf("load history activation cutover: %w", err)
		}
		if existing, found, err := loadHistoryActivationByCutoverConn(ctx, conn, cutoverID, input.OwnerID); err != nil {
			return fmt.Errorf("load existing history activation: %w", err)
		} else if found {
			// The receipt intentionally points at the superseded source epoch.
			// Reconciliation must validate that immutable source row without
			// requiring it to remain the active Frame authority.
			source, err := getRawStreamConn(ctx, conn, existing.SourceStreamUID, input.OwnerID)
			if err != nil {
				return err
			}
			if err := ensureHistoryActivationRealtimeRebaseConn(ctx, conn, existing, source, existing.ActivatedAt); err != nil {
				return fmt.Errorf("repair history activation realtime rebase: %w", err)
			}
			result = existing
			return nil
		}
		result, err = r.activateHistoryCutoverConn(ctx, conn, cutover, input)
		if err != nil {
			return err
		}
		created = true
		return nil
	})
	return result, created, schemaError(err)
}

// ReconcileActiveHistoryRealtimeRebases validates and repairs the durable
// realtime rebase event for every active Frame history authority. It is
// intentionally synchronous so callers can finish recovery before exposing
// realtime replay endpoints.
func (r *Repository) ReconcileActiveHistoryRealtimeRebases(ctx context.Context, pageSize int) (int, error) {
	if r == nil || r.db == nil {
		return 0, ErrSchemaUnavailable
	}
	if pageSize <= 0 || pageSize > 1000 {
		pageSize = 1000
	}
	type candidate struct {
		activationID []byte
		ownerID      string
	}
	after := ""
	repaired := 0
	for {
		rows, err := r.db.QueryContext(ctx, `SELECT receipt.activation_id,receipt.owner_id
			FROM transcript_history_activation_receipts receipt
			JOIN transcript_frame_authority authority ON authority.activation_id=receipt.activation_id
			WHERE receipt.status='active' AND receipt.target_stream_uid=authority.active_stream_uid
				AND receipt.target_epoch=authority.active_epoch
				AND receipt.authority_generation=authority.authority_generation
				AND authority.read_authority='transcript_payload_v1'
				AND authority.write_authority='transcript_payload_v1'
				AND lower(hex(receipt.activation_id))>?
			ORDER BY receipt.activation_id LIMIT ?`, after, pageSize)
		if err != nil {
			return repaired, schemaError(err)
		}
		page := make([]candidate, 0, pageSize)
		for rows.Next() {
			var item candidate
			if err := rows.Scan(&item.activationID, &item.ownerID); err != nil {
				_ = rows.Close()
				return repaired, schemaError(err)
			}
			if len(item.activationID) != sha256.Size || strings.TrimSpace(item.ownerID) == "" {
				_ = rows.Close()
				return repaired, ErrEventConflict
			}
			page = append(page, item)
		}
		if err := rows.Close(); err != nil {
			return repaired, schemaError(err)
		}
		if len(page) == 0 {
			return repaired, nil
		}
		for _, item := range page {
			err := r.withImmediate(ctx, func(conn *sql.Conn) error {
				activation, found, err := loadHistoryActivationByIDConn(ctx, conn, item.activationID, item.ownerID)
				if err != nil {
					return err
				}
				if !found || activation.ActivationSHA256 != hex.EncodeToString(item.activationID) {
					return ErrEventConflict
				}
				source, err := getRawStreamConn(ctx, conn, activation.SourceStreamUID, item.ownerID)
				if err != nil {
					return err
				}
				var existing int
				if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM realtime_events WHERE id=?`,
					"transcript-history-rebase:"+activation.ActivationSHA256).Scan(&existing); err != nil {
					return err
				}
				if err := ensureHistoryActivationRealtimeRebaseConn(ctx, conn, activation, source, activation.ActivatedAt); err != nil {
					return err
				}
				if existing == 0 {
					repaired++
				}
				return nil
			})
			if err != nil {
				return repaired, schemaError(err)
			}
		}
		after = hex.EncodeToString(page[len(page)-1].activationID)
		if len(page) < pageSize {
			return repaired, nil
		}
	}
}

func loadHistoryActivationCutoverConn(
	ctx context.Context, conn *sql.Conn, cutoverID []byte, ownerID string,
) (storedHistoryActivationCutover, error) {
	var result storedHistoryActivationCutover
	var status string
	var activated int
	err := conn.QueryRowContext(ctx, `SELECT cutover_id,stream_uid,owner_id,source_branch_id,source_generation,
		source_through_publication_seq,verification_sha256,lineage_sha256,cursor_sha256,shadow_sha256,
		branch_count,event_count,cursor_count,status,activated
		FROM transcript_history_cutover_runs WHERE cutover_id=?`, cutoverID).Scan(
		&result.id, &result.streamUID, &result.ownerID, &result.sourceBranchID, &result.generation, &result.through,
		&result.verification, &result.lineage, &result.cursor, &result.shadow,
		&result.branchCount, &result.eventCount, &result.cursorCount, &status, &activated,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storedHistoryActivationCutover{}, ErrEventConflict
	}
	if err != nil {
		return storedHistoryActivationCutover{}, err
	}
	if result.ownerID != ownerID {
		return storedHistoryActivationCutover{}, ErrOwnerMismatch
	}
	if status != "ready" || activated != 0 || len(result.id) != sha256.Size ||
		len(result.verification) != sha256.Size || len(result.lineage) != sha256.Size ||
		len(result.cursor) != sha256.Size || len(result.shadow) != sha256.Size ||
		result.branchCount <= 0 || result.eventCount <= 0 || result.cursorCount <= 0 {
		return storedHistoryActivationCutover{}, fmt.Errorf("history activation cutover header changed: %w", ErrEventConflict)
	}
	storedDigest, err := digestStoredHistoryCutoverRows(ctx, conn, cutoverID)
	if err != nil {
		return storedHistoryActivationCutover{}, err
	}
	if storedDigest != hex.EncodeToString(result.lineage)+hex.EncodeToString(result.cursor)+hex.EncodeToString(result.shadow) {
		return storedHistoryActivationCutover{}, fmt.Errorf("history activation cutover rows changed (stored=%.16s expected=%.16s): %w",
			storedDigest, hex.EncodeToString(result.lineage)+hex.EncodeToString(result.cursor)+hex.EncodeToString(result.shadow), ErrEventConflict)
	}
	return result, nil
}

func (r *Repository) activateHistoryCutoverConn(
	ctx context.Context, conn *sql.Conn, cutover storedHistoryActivationCutover, input ActivateAskUserHistoryCutoverInput,
) (AskUserHistoryActivation, error) {
	source, err := getStreamConn(ctx, conn, cutover.streamUID, cutover.ownerID)
	if err != nil {
		return AskUserHistoryActivation{}, err
	}
	if source.Kind != StreamKindFrameRef || source.SessionID == "" || source.Epoch <= 0 ||
		source.InputRevision != source.ConsumedInputRevision {
		return AskUserHistoryActivation{}, fmt.Errorf("history activation source is not quiescent: %w", ErrEventConflict)
	}
	var activeStreamUID, readAuthority, writeAuthority string
	var activeEpoch, authorityGeneration int64
	var currentActivation, currentGenesis []byte
	if err := conn.QueryRowContext(ctx, `SELECT active_stream_uid,active_epoch,authority_generation,
		read_authority,write_authority,activation_id,genesis_id FROM transcript_frame_authority
		WHERE owner_id=? AND session_id=?`, source.OwnerID, source.SessionID).Scan(
		&activeStreamUID, &activeEpoch, &authorityGeneration, &readAuthority, &writeAuthority,
		&currentActivation, &currentGenesis,
	); err != nil {
		return AskUserHistoryActivation{}, err
	}
	if activeStreamUID != source.UID || activeEpoch != source.Epoch || authorityGeneration <= 0 ||
		readAuthority != "legacy_mixed_v1" || writeAuthority != "legacy_frame_ref_v1" ||
		currentActivation != nil || currentGenesis != nil {
		return AskUserHistoryActivation{}, fmt.Errorf("history activation authority changed: %w", ErrEventConflict)
	}
	var branchID string
	var branchGeneration int64
	if err := conn.QueryRowContext(ctx, `SELECT active_branch_id,generation FROM transcript_branch_state WHERE stream_uid=?`,
		source.UID).Scan(&branchID, &branchGeneration); err != nil {
		return AskUserHistoryActivation{}, err
	}
	if branchID != cutover.sourceBranchID || branchGeneration != cutover.generation ||
		source.NextPublication-1 != cutover.through {
		return AskUserHistoryActivation{}, fmt.Errorf("history activation branch changed: %w", ErrBranchStateStale)
	}
	if err := blockUnsafeHistoryActivationConn(ctx, conn, source.UID); err != nil {
		return AskUserHistoryActivation{}, fmt.Errorf("validate history activation blockers: %w", err)
	}
	events, eventByKey, sourceToTarget, err := loadActivationEventsConn(ctx, conn, cutover, input.MaxEvents)
	if err != nil {
		return AskUserHistoryActivation{}, fmt.Errorf("load history activation events: %w", err)
	}
	if len(events) != cutover.eventCount {
		return AskUserHistoryActivation{}, fmt.Errorf("history activation event count changed: %w", ErrEventConflict)
	}
	activationID := historyActivationIdentity(cutover, source.Epoch+1, authorityGeneration+1)
	targetUID := "history:" + hex.EncodeToString(activationID[:16])
	now := r.now().UTC()
	if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_streams(
		stream_uid,owner_id,external_id,session_id,kind,project_id,root_frame_id,frame_id,epoch,
		input_revision,consumed_input_revision,next_event_id,next_publication_seq,next_checkpoint_sequence,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, targetUID, source.OwnerID, source.ExternalID, source.SessionID,
		string(source.Kind), source.ProjectID, source.RootFrameID, source.FrameID, source.Epoch+1,
		source.InputRevision, source.ConsumedInputRevision, int64(len(events)+1), int64(len(events)+1), 1,
		source.CreatedAt, now); err != nil {
		return AskUserHistoryActivation{}, err
	}
	for index := range events {
		rebound, reboundClientID, err := rebindHistoryActivationPayload(
			events[index].eventType, events[index].payload, targetUID, source.FrameID, source.Epoch+1,
		)
		if err != nil {
			return AskUserHistoryActivation{}, err
		}
		events[index].payload = rebound
		if reboundClientID != "" {
			events[index].clientMessageID = reboundClientID
		}
		eventByKey[events[index].key] = &events[index]
	}
	attemptCount, receiptCount, checkpointCount, err := materializeHistoryActivationEventsConn(
		ctx, conn, source, targetUID, events, sourceToTarget, input.MaxAttempts, input.MaxCheckpoints,
	)
	if err != nil {
		return AskUserHistoryActivation{}, fmt.Errorf("materialize history activation events: %w", err)
	}
	branchCount, branchEventCount, activeTargetBranch, err := materializeHistoryActivationBranchesConn(
		ctx, conn, cutover.id, targetUID, source.UID, cutover.sourceBranchID, eventByKey,
		input.MaxBranches, input.MaxBranchEvents,
	)
	if err != nil {
		return AskUserHistoryActivation{}, fmt.Errorf("materialize history activation branches: %w", err)
	}
	artifactCommitCount, artifactRefCount, err := materializeHistoryActivationArtifactsConn(
		ctx, conn, cutover.id, source.UID, targetUID, eventByKey, sourceToTarget,
		input.MaxArtifactCommits, input.MaxArtifactRefs,
	)
	if err != nil {
		return AskUserHistoryActivation{}, fmt.Errorf("materialize history activation artifacts: %w", err)
	}
	routeCount, err := materializeHistoryActivationRoutesConn(ctx, conn, source.UID, targetUID, input.MaxRoutes)
	if err != nil {
		return AskUserHistoryActivation{}, fmt.Errorf("materialize history activation routes: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `UPDATE transcript_streams SET next_checkpoint_sequence=?,updated_at=? WHERE stream_uid=?`,
		source.NextCheckpoint, now, targetUID); err != nil {
		return AskUserHistoryActivation{}, err
	}
	materialized, err := digestHistoryActivationTargetConn(ctx, conn, targetUID)
	if err != nil {
		return AskUserHistoryActivation{}, err
	}
	var realtimeHighWater int64
	var realtimeSequenceShape int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('realtime_events')
		WHERE name='sequence' AND upper(type)='INTEGER' AND pk=1`).Scan(&realtimeSequenceShape); err != nil {
		return AskUserHistoryActivation{}, err
	}
	if realtimeSequenceShape != 1 {
		return AskUserHistoryActivation{}, ErrSchemaUnavailable
	}
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM realtime_events`).Scan(&realtimeHighWater); err != nil {
		return AskUserHistoryActivation{}, err
	}
	result := AskUserHistoryActivation{
		ActivationSHA256: hex.EncodeToString(activationID[:]), CutoverID: hex.EncodeToString(cutover.id),
		SourceStreamUID: source.UID, TargetStreamUID: targetUID, SourceEpoch: source.Epoch, TargetEpoch: source.Epoch + 1,
		AuthorityGeneration: authorityGeneration + 1, ActiveBranchID: activeTargetBranch,
		AttemptCount: attemptCount, ReceiptCount: receiptCount, CheckpointCount: checkpointCount,
		EventCount: len(events), BranchCount: branchCount, BranchEventCount: branchEventCount,
		CursorCount: cutover.cursorCount, ArtifactCommitCount: artifactCommitCount,
		ArtifactRefCount: artifactRefCount, RouteCount: routeCount, Active: true, ActivatedAt: now,
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_history_activation_receipts(
		activation_id,provenance_kind,cutover_id,ordinary_cutover_id,
		source_stream_uid,target_stream_uid,owner_id,session_id,source_epoch,target_epoch,prior_activation_id,
		active_branch_id,branch_generation,source_through_publication_seq,target_through_publication_seq,
		verification_sha256,lineage_sha256,cursor_sha256,shadow_sha256,materialized_sha256,
		attempt_count,receipt_count,checkpoint_count,event_count,branch_count,branch_event_count,cursor_count,
		artifact_commit_count,artifact_ref_count,route_count,realtime_high_water,authority_generation,status,activated_at)
		VALUES(?,'ask_user_v30',?,NULL,?,?,?,?,?,?,NULL,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'active',?)`,
		activationID[:], cutover.id, source.UID, targetUID, source.OwnerID, source.SessionID, source.Epoch, source.Epoch+1,
		activeTargetBranch, branchGeneration, cutover.through, int64(len(events)),
		cutover.verification, cutover.lineage, cutover.cursor, cutover.shadow, materialized,
		attemptCount, receiptCount, checkpointCount, len(events), branchCount, branchEventCount, cutover.cursorCount,
		artifactCommitCount, artifactRefCount, routeCount, realtimeHighWater, authorityGeneration+1, now,
	); err != nil {
		return AskUserHistoryActivation{}, err
	}
	updated, err := conn.ExecContext(ctx, `UPDATE transcript_frame_authority SET
		active_stream_uid=?,active_epoch=?,authority_generation=?,read_authority='transcript_payload_v1',
		write_authority='transcript_payload_v1',activation_id=?,genesis_id=NULL,updated_at=?
		WHERE owner_id=? AND session_id=? AND active_stream_uid=? AND active_epoch=? AND authority_generation=?
			AND read_authority='legacy_mixed_v1' AND write_authority='legacy_frame_ref_v1'
			AND activation_id IS NULL AND genesis_id IS NULL`,
		targetUID, source.Epoch+1, authorityGeneration+1, activationID[:], now,
		source.OwnerID, source.SessionID, source.UID, source.Epoch, authorityGeneration)
	if err != nil {
		return AskUserHistoryActivation{}, err
	}
	if count, err := updated.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return AskUserHistoryActivation{}, err
		}
		return AskUserHistoryActivation{}, ErrEventConflict
	}
	if err := ensureHistoryActivationRealtimeRebaseConn(ctx, conn, result, source, now); err != nil {
		return AskUserHistoryActivation{}, err
	}
	return result, nil
}

func ensureHistoryActivationRealtimeRebaseConn(
	ctx context.Context, conn *sql.Conn, activation AskUserHistoryActivation,
	source Stream, now time.Time,
) error {
	const eventType = "conversation.historyRebased"
	spec, found := realtime.LookupEvent(eventType)
	if !found || spec.Kind != realtime.DeliveryInvalidate {
		return ErrSchemaUnavailable
	}
	activationID, err := hex.DecodeString(activation.ActivationSHA256)
	if err != nil || len(activationID) != sha256.Size {
		return fmt.Errorf("decode history activation rebase identity: %w", ErrEventConflict)
	}
	var branchGeneration, realtimeHighWater int64
	if err := conn.QueryRowContext(ctx, `SELECT branch_generation,realtime_high_water
		FROM transcript_history_activation_receipts WHERE activation_id=? AND status='active'`, activationID).Scan(
		&branchGeneration, &realtimeHighWater,
	); err != nil {
		return err
	}
	payload := map[string]any{
		"conversation_id": source.SessionID, "project_id": source.ProjectID,
		"root_frame_id": source.RootFrameID, "activation_id": activation.ActivationSHA256,
		"authority_generation": activation.AuthorityGeneration, "target_epoch": activation.TargetEpoch,
		"active_branch_id": activation.ActiveBranchID, "branch_generation": branchGeneration,
		"realtime_high_water": realtimeHighWater,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	invalidationsJSON, err := json.Marshal(realtime.Invalidations(eventType, payload))
	if err != nil {
		return err
	}
	result, err := conn.ExecContext(ctx, `INSERT INTO realtime_events(
		id,user_id,project_id,root_frame_id,frame_id,event_type,event_kind,payload,invalidations,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`,
		"transcript-history-rebase:"+activation.ActivationSHA256,
		source.OwnerID, source.ProjectID, source.RootFrameID, source.SessionID,
		eventType, string(spec.Kind), string(payloadJSON), string(invalidationsJSON), now)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 1 {
		return nil
	}
	var storedUserID, storedProjectID, storedRootFrameID, storedFrameID, storedType, storedKind, storedPayload, storedInvalidations string
	if err := conn.QueryRowContext(ctx, `SELECT user_id,project_id,root_frame_id,frame_id,event_type,event_kind,payload,invalidations
		FROM realtime_events WHERE id=?`, "transcript-history-rebase:"+activation.ActivationSHA256).Scan(
		&storedUserID, &storedProjectID, &storedRootFrameID, &storedFrameID,
		&storedType, &storedKind, &storedPayload, &storedInvalidations,
	); err != nil {
		return err
	}
	if storedUserID != source.OwnerID || storedProjectID != source.ProjectID || storedRootFrameID != source.RootFrameID ||
		storedFrameID != source.SessionID || storedType != eventType || storedKind != string(spec.Kind) ||
		storedPayload != string(payloadJSON) || storedInvalidations != string(invalidationsJSON) {
		return fmt.Errorf("validate existing history activation realtime rebase: %w", ErrEventConflict)
	}
	return nil
}

func blockUnsafeHistoryActivationConn(ctx context.Context, conn *sql.Conn, streamUID string) error {
	checks := []struct{ query string }{
		{`SELECT COUNT(*) FROM transcript_runner_attempts WHERE stream_uid=? AND status='running'`},
		{`SELECT COUNT(*) FROM transcript_artifact_commits WHERE stream_uid=? AND bound_event_id IS NULL`},
		{`SELECT COUNT(*) FROM transcript_delivery_intents WHERE stream_uid=? AND status IN ('pending','inflight','failed')`},
	}
	for _, check := range checks {
		var count int
		if err := conn.QueryRowContext(ctx, check.query, streamUID).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return ErrEventConflict
		}
	}
	return nil
}

func historyActivationIdentity(cutover storedHistoryActivationCutover, targetEpoch, generation int64) [sha256.Size]byte {
	h := sha256.New()
	for _, value := range []string{HistoryActivationContractID, hex.EncodeToString(cutover.id), cutover.streamUID,
		fmt.Sprintf("%d", targetEpoch), fmt.Sprintf("%d", generation)} {
		writeHistoryDigestField(h, []byte(value))
	}
	var result [sha256.Size]byte
	copy(result[:], h.Sum(nil))
	return result
}

func loadActivationEventsConn(
	ctx context.Context, conn *sql.Conn, cutover storedHistoryActivationCutover, limit int,
) ([]activationEvent, map[string]*activationEvent, map[int64]int64, error) {
	rows, err := conn.QueryContext(ctx, `SELECT target_event_key,source_event_id,target_publication_seq,
		client_message_id,event_type,runner_attempt,payload_json,created_at
		FROM transcript_history_cutover_events WHERE cutover_id=? ORDER BY target_publication_seq`, cutover.id)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	events := make([]activationEvent, 0, cutover.eventCount)
	byKey := map[string]*activationEvent{}
	sourceToTarget := map[int64]int64{}
	for rows.Next() {
		var event activationEvent
		if err := rows.Scan(&event.key, &event.sourceEventID, &event.publication, &event.clientMessageID,
			&event.eventType, &event.runnerAttempt, &event.payload, &event.createdAt); err != nil {
			return nil, nil, nil, err
		}
		if event.publication != int64(len(events)+1) || len(events) >= limit {
			return nil, nil, nil, ErrEventConflict
		}
		events = append(events, event)
		byKey[event.key] = &events[len(events)-1]
		if event.sourceEventID.Valid {
			if existing := sourceToTarget[event.sourceEventID.Int64]; event.publication > existing {
				sourceToTarget[event.sourceEventID.Int64] = event.publication
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, err
	}
	return events, byKey, sourceToTarget, nil
}

func materializeHistoryActivationEventsConn(
	ctx context.Context,
	conn *sql.Conn,
	source Stream,
	targetUID string,
	events []activationEvent,
	sourceToTarget map[int64]int64,
	maxAttempts, maxCheckpoints int,
) (int, int, int, error) {
	for _, event := range events {
		if !event.runnerAttempt.Valid {
			if err := insertHistoryActivationEventConn(ctx, conn, targetUID, event); err != nil {
				return 0, 0, 0, err
			}
		}
	}
	rows, err := conn.QueryContext(ctx, `SELECT attempt,runner_id,claim_token_sha256,claimed_input_revision,
		resume_source,resume_checkpoint_sequence,status,phase,phase_sequence,last_checkpoint_sequence,
		claimed_at,expires_at,finished_event_id,finished_at
		FROM transcript_runner_attempts WHERE stream_uid=? ORDER BY attempt`, source.UID)
	if err != nil {
		return 0, 0, 0, err
	}
	type attemptRow struct {
		attempt, claimedRevision, resumeCheckpoint, phaseSequence, lastCheckpoint int64
		runnerID, resumeSource, status, phase                                     string
		claimDigest                                                               []byte
		claimedAt, expiresAt                                                      time.Time
		finishedEvent                                                             sql.NullInt64
		finishedAt                                                                sql.NullTime
	}
	attempts := []attemptRow{}
	for rows.Next() {
		var row attemptRow
		if err := rows.Scan(&row.attempt, &row.runnerID, &row.claimDigest, &row.claimedRevision,
			&row.resumeSource, &row.resumeCheckpoint, &row.status, &row.phase, &row.phaseSequence,
			&row.lastCheckpoint, &row.claimedAt, &row.expiresAt, &row.finishedEvent, &row.finishedAt); err != nil {
			_ = rows.Close()
			return 0, 0, 0, err
		}
		if len(attempts) >= maxAttempts || row.status == "running" || !row.finishedEvent.Valid || !row.finishedAt.Valid ||
			sourceToTarget[row.finishedEvent.Int64] <= 0 {
			_ = rows.Close()
			return 0, 0, 0, fmt.Errorf("runner attempt %d is not fully materializable (status=%s finished=%t mapped=%d): %w",
				row.attempt, row.status, row.finishedEvent.Valid && row.finishedAt.Valid, sourceToTarget[row.finishedEvent.Int64], ErrEventConflict)
		}
		attempts = append(attempts, row)
	}
	if err := rows.Close(); err != nil {
		return 0, 0, 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, 0, 0, err
	}
	receiptCount := 0
	for _, attempt := range attempts {
		if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_runner_attempts(
			stream_uid,attempt,runner_id,claim_token_sha256,claimed_input_revision,resume_source,
			resume_checkpoint_sequence,status,phase,phase_sequence,last_checkpoint_sequence,
			claimed_at,expires_at,finished_event_id,finished_at)
			VALUES(?,?,?,?,?,?,?,'running','claimed',?,?,?, ?,NULL,NULL)`,
			targetUID, attempt.attempt, attempt.runnerID, attempt.claimDigest, attempt.claimedRevision,
			attempt.resumeSource, attempt.resumeCheckpoint, attempt.phaseSequence, attempt.lastCheckpoint,
			attempt.claimedAt, attempt.expiresAt); err != nil {
			return 0, 0, 0, err
		}
		foundEvent := false
		for _, event := range events {
			if event.runnerAttempt.Valid && event.runnerAttempt.Int64 == attempt.attempt {
				foundEvent = true
				if err := insertHistoryActivationEventConn(ctx, conn, targetUID, event); err != nil {
					return 0, 0, 0, err
				}
			}
		}
		if !foundEvent {
			return 0, 0, 0, fmt.Errorf("runner attempt %d has no target event: %w", attempt.attempt, ErrEventConflict)
		}
		targetFinished := sourceToTarget[attempt.finishedEvent.Int64]
		var receiptEvent int64
		var receiptStatus string
		var receiptFinished time.Time
		err := conn.QueryRowContext(ctx, `SELECT event_id,status,finished_at FROM transcript_runner_receipts
			WHERE stream_uid=? AND attempt=?`, source.UID, attempt.attempt).Scan(
			&receiptEvent, &receiptStatus, &receiptFinished)
		if err == nil {
			if sourceToTarget[receiptEvent] != targetFinished || receiptStatus != attempt.status ||
				!receiptFinished.Equal(attempt.finishedAt.Time) {
				return 0, 0, 0, fmt.Errorf("runner attempt %d receipt changed: %w", attempt.attempt, ErrEventConflict)
			}
			if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_runner_receipts(
				stream_uid,attempt,event_id,status,finished_at) VALUES(?,?,?,?,?)`,
				targetUID, attempt.attempt, targetFinished, receiptStatus, receiptFinished); err != nil {
				return 0, 0, 0, err
			}
			receiptCount++
		} else if !errors.Is(err, sql.ErrNoRows) {
			return 0, 0, 0, err
		} else if attempt.status != "reclaimed" {
			return 0, 0, 0, fmt.Errorf("runner attempt %d terminal receipt is missing: %w", attempt.attempt, ErrEventConflict)
		}
		if _, err := conn.ExecContext(ctx, `UPDATE transcript_runner_attempts SET
			status=?,phase=?,phase_sequence=?,last_checkpoint_sequence=?,finished_event_id=?,finished_at=?
			WHERE stream_uid=? AND attempt=? AND status='running'`,
			attempt.status, attempt.phase, attempt.phaseSequence, attempt.lastCheckpoint,
			targetFinished, attempt.finishedAt.Time, targetUID, attempt.attempt); err != nil {
			return 0, 0, 0, err
		}
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
		targetEvent := sourceToTarget[sourceEvent]
		if checkpointCount >= maxCheckpoints || targetEvent <= 0 {
			_ = checkpointRows.Close()
			return 0, 0, 0, fmt.Errorf("runner checkpoint %d is not materializable: %w", sequence, ErrEventConflict)
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_runner_checkpoints(
			stream_uid,checkpoint_sequence,runner_attempt,event_id,phase,resumable,created_at)
			VALUES(?,?,?,?,?,?,?)`, targetUID, sequence, attempt, targetEvent, phase, resumable, createdAt); err != nil {
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
	return len(attempts), receiptCount, checkpointCount, nil
}

func insertHistoryActivationEventConn(ctx context.Context, conn *sql.Conn, targetUID string, event activationEvent) error {
	var runner any
	if event.runnerAttempt.Valid {
		runner = event.runnerAttempt.Int64
	}
	_, err := conn.ExecContext(ctx, `INSERT INTO transcript_events(
		stream_uid,event_id,publication_seq,client_message_id,event_type,source,runner_attempt,payload_json,frame_event_id,created_at)
		VALUES(?,?,?,?,?,'payload',?,?,NULL,?)`, targetUID, event.publication, event.publication,
		event.clientMessageID, event.eventType, runner, event.payload, event.createdAt)
	return err
}

func materializeHistoryActivationBranchesConn(
	ctx context.Context,
	conn *sql.Conn,
	cutoverID []byte,
	targetUID, sourceUID, activeSourceBranch string,
	eventByKey map[string]*activationEvent,
	branchLimit, branchEventLimit int,
) (int, int, string, error) {
	rows, err := conn.QueryContext(ctx, `SELECT source_branch_id,target_branch_id,parent_target_branch_id,kind,
		target_fork_event_key,fork_point,client_mutation_id,request_sha256,source_message_id,created_at,updated_at
		FROM transcript_history_cutover_branches WHERE cutover_id=? ORDER BY source_branch_id`, cutoverID)
	if err != nil {
		return 0, 0, "", err
	}
	type branchRow struct {
		sourceID, targetID, kind, mutationID, sourceMessageID string
		parentID, forkKey                                     sql.NullString
		forkPoint                                             int64
		requestSHA                                            []byte
		createdAt, updatedAt                                  time.Time
	}
	branches := []branchRow{}
	activeTarget := ""
	for rows.Next() {
		var branch branchRow
		if err := rows.Scan(&branch.sourceID, &branch.targetID, &branch.parentID, &branch.kind,
			&branch.forkKey, &branch.forkPoint, &branch.mutationID, &branch.requestSHA,
			&branch.sourceMessageID, &branch.createdAt, &branch.updatedAt); err != nil {
			_ = rows.Close()
			return 0, 0, "", err
		}
		if len(branches) >= branchLimit {
			_ = rows.Close()
			return 0, 0, "", ErrEventConflict
		}
		if branch.sourceID == activeSourceBranch {
			activeTarget = branch.targetID
		}
		branches = append(branches, branch)
	}
	if err := rows.Close(); err != nil {
		return 0, 0, "", err
	}
	if err := rows.Err(); err != nil {
		return 0, 0, "", err
	}
	if activeTarget == "" || len(branches) == 0 {
		return 0, 0, "", ErrEventConflict
	}
	for _, branch := range branches {
		var parent, fork any
		if branch.parentID.Valid {
			parent = branch.parentID.String
		}
		if branch.forkKey.Valid {
			event := eventByKey[branch.forkKey.String]
			if event == nil {
				return 0, 0, "", ErrEventConflict
			}
			fork = event.publication
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_branches(
			stream_uid,branch_id,parent_branch_id,fork_event_id,fork_point,kind,client_mutation_id,
			request_sha256,source_message_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			targetUID, branch.targetID, parent, fork, branch.forkPoint, branch.kind, branch.mutationID,
			branch.requestSHA, branch.sourceMessageID, branch.createdAt, branch.updatedAt); err != nil {
			return 0, 0, "", err
		}
	}
	var sourceGeneration int64
	var sourceUpdatedAt time.Time
	if err := conn.QueryRowContext(ctx, `SELECT generation,updated_at FROM transcript_branch_state WHERE stream_uid=?`, sourceUID).
		Scan(&sourceGeneration, &sourceUpdatedAt); err != nil {
		return 0, 0, "", err
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_branch_state(stream_uid,active_branch_id,generation,updated_at)
		VALUES(?,?,?,?)`, targetUID, activeTarget, sourceGeneration, sourceUpdatedAt); err != nil {
		return 0, 0, "", err
	}
	membershipRows, err := conn.QueryContext(ctx, `SELECT target_branch_id,target_ordinal,target_event_key
		FROM transcript_history_cutover_branch_events WHERE cutover_id=? ORDER BY target_branch_id,target_ordinal`, cutoverID)
	if err != nil {
		return 0, 0, "", err
	}
	branchEventCount := 0
	for membershipRows.Next() {
		var branchID, eventKey string
		var ordinal int64
		if err := membershipRows.Scan(&branchID, &ordinal, &eventKey); err != nil {
			_ = membershipRows.Close()
			return 0, 0, "", err
		}
		if branchEventCount >= branchEventLimit {
			_ = membershipRows.Close()
			return 0, 0, "", ErrEventConflict
		}
		event := eventByKey[eventKey]
		if event == nil {
			_ = membershipRows.Close()
			return 0, 0, "", ErrEventConflict
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_branch_events(stream_uid,branch_id,ordinal,event_id)
			VALUES(?,?,?,?)`, targetUID, branchID, ordinal, event.publication); err != nil {
			_ = membershipRows.Close()
			return 0, 0, "", err
		}
		branchEventCount++
	}
	if err := membershipRows.Close(); err != nil {
		return 0, 0, "", err
	}
	if err := membershipRows.Err(); err != nil {
		return 0, 0, "", err
	}
	if branchEventCount == 0 {
		return 0, 0, "", ErrEventConflict
	}
	return len(branches), branchEventCount, activeTarget, nil
}

func materializeHistoryActivationArtifactsConn(
	ctx context.Context,
	conn *sql.Conn,
	cutoverID []byte,
	sourceUID, targetUID string,
	eventByKey map[string]*activationEvent,
	sourceToTarget map[int64]int64,
	commitLimit, refLimit int,
) (int, int, error) {
	commitRows, err := conn.QueryContext(ctx, `SELECT runner_attempt,source_event_id,ordinal,artifact_id,version_id,
		relation,bound_event_id,created_at FROM transcript_artifact_commits WHERE stream_uid=?
		ORDER BY runner_attempt,source_event_id,ordinal`, sourceUID)
	if err != nil {
		return 0, 0, err
	}
	commitCount := 0
	for commitRows.Next() {
		var attempt, sourceEvent, ordinal, boundEvent int64
		var artifactID, versionID, relation string
		var createdAt time.Time
		if err := commitRows.Scan(&attempt, &sourceEvent, &ordinal, &artifactID, &versionID,
			&relation, &boundEvent, &createdAt); err != nil {
			_ = commitRows.Close()
			return 0, 0, err
		}
		if commitCount >= commitLimit {
			_ = commitRows.Close()
			return 0, 0, ErrEventConflict
		}
		targetSource, targetBound := sourceToTarget[sourceEvent], sourceToTarget[boundEvent]
		if targetSource <= 0 || targetBound <= 0 {
			_ = commitRows.Close()
			return 0, 0, ErrEventConflict
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_artifact_commits(
			stream_uid,runner_attempt,source_event_id,ordinal,artifact_id,version_id,relation,bound_event_id,created_at)
			VALUES(?,?,?,?,?,?,?,?,?)`, targetUID, attempt, targetSource, ordinal, artifactID, versionID,
			relation, targetBound, createdAt); err != nil {
			_ = commitRows.Close()
			return 0, 0, err
		}
		commitCount++
	}
	if err := commitRows.Close(); err != nil {
		return 0, 0, err
	}
	if err := commitRows.Err(); err != nil {
		return 0, 0, err
	}
	refRows, err := conn.QueryContext(ctx, `SELECT target_event_key,ordinal,artifact_id,version_id,relation,availability,created_at
		FROM transcript_history_cutover_artifact_refs WHERE cutover_id=? ORDER BY target_event_key,ordinal`, cutoverID)
	if err != nil {
		return 0, 0, err
	}
	refCount := 0
	for refRows.Next() {
		var eventKey, artifactID, versionID, relation, availability string
		var ordinal int64
		var createdAt time.Time
		if err := refRows.Scan(&eventKey, &ordinal, &artifactID, &versionID, &relation, &availability, &createdAt); err != nil {
			_ = refRows.Close()
			return 0, 0, err
		}
		if refCount >= refLimit {
			_ = refRows.Close()
			return 0, 0, ErrEventConflict
		}
		event := eventByKey[eventKey]
		if event == nil || !event.runnerAttempt.Valid {
			_ = refRows.Close()
			return 0, 0, ErrEventConflict
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_artifact_refs(
			stream_uid,runner_attempt,source_event_id,ordinal,artifact_id,version_id,relation,availability,created_at)
			VALUES(?,?,?,?,?,?,?,?,?)`, targetUID, event.runnerAttempt.Int64, event.publication, ordinal,
			artifactID, versionID, relation, availability, createdAt); err != nil {
			_ = refRows.Close()
			return 0, 0, err
		}
		refCount++
	}
	if err := refRows.Close(); err != nil {
		return 0, 0, err
	}
	if err := refRows.Err(); err != nil {
		return 0, 0, err
	}
	return commitCount, refCount, nil
}

func materializeHistoryActivationRoutesConn(
	ctx context.Context, conn *sql.Conn, sourceUID, targetUID string, limit int,
) (int, error) {
	rows, err := conn.QueryContext(ctx, `SELECT destination,current_generation,status,updated_at
		FROM transcript_delivery_routes WHERE stream_uid=? ORDER BY destination`, sourceUID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var destination, status string
		var generation int64
		var updatedAt time.Time
		if err := rows.Scan(&destination, &generation, &status, &updatedAt); err != nil {
			return 0, err
		}
		if count >= limit {
			return 0, ErrEventConflict
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_delivery_routes(
			stream_uid,destination,current_generation,status,updated_at) VALUES(?,?,?,?,?)`,
			targetUID, destination, generation, status, updatedAt); err != nil {
			return 0, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return count, nil
}

func digestHistoryActivationTargetConn(ctx context.Context, conn *sql.Conn, streamUID string) ([]byte, error) {
	digest := sha256.New()
	queries := []string{
		`SELECT stream_uid,owner_id,external_id,session_id,kind,project_id,root_frame_id,frame_id,epoch,
			input_revision,consumed_input_revision,next_event_id,next_publication_seq,next_checkpoint_sequence,created_at,updated_at
			FROM transcript_streams WHERE stream_uid=?`,
		`SELECT attempt,runner_id,claim_token_sha256,claimed_input_revision,resume_source,resume_checkpoint_sequence,
			status,phase,phase_sequence,last_checkpoint_sequence,claimed_at,expires_at,finished_event_id,finished_at
			FROM transcript_runner_attempts WHERE stream_uid=? ORDER BY attempt`,
		`SELECT event_id,publication_seq,client_message_id,event_type,source,runner_attempt,payload_json,created_at
			FROM transcript_events WHERE stream_uid=? ORDER BY publication_seq`,
		`SELECT attempt,event_id,status,finished_at FROM transcript_runner_receipts WHERE stream_uid=? ORDER BY attempt`,
		`SELECT checkpoint_sequence,runner_attempt,event_id,phase,resumable,created_at
			FROM transcript_runner_checkpoints WHERE stream_uid=? ORDER BY checkpoint_sequence`,
		`SELECT branch_id,COALESCE(parent_branch_id,''),fork_event_id,fork_point,kind,client_mutation_id,
			request_sha256,source_message_id,created_at,updated_at FROM transcript_branches WHERE stream_uid=? ORDER BY branch_id`,
		`SELECT active_branch_id,generation,updated_at FROM transcript_branch_state WHERE stream_uid=?`,
		`SELECT branch_id,ordinal,event_id FROM transcript_branch_events WHERE stream_uid=? ORDER BY branch_id,ordinal`,
		`SELECT runner_attempt,source_event_id,ordinal,artifact_id,version_id,relation,bound_event_id,created_at
			FROM transcript_artifact_commits WHERE stream_uid=? ORDER BY runner_attempt,source_event_id,ordinal`,
		`SELECT runner_attempt,source_event_id,ordinal,artifact_id,version_id,relation,availability,created_at
			FROM transcript_artifact_refs WHERE stream_uid=? ORDER BY runner_attempt,source_event_id,ordinal`,
		`SELECT destination,current_generation,status,updated_at FROM transcript_delivery_routes
			WHERE stream_uid=? ORDER BY destination`,
	}
	for _, query := range queries {
		if err := digestHistoryActivationRows(ctx, conn, digest, query, streamUID); err != nil {
			return nil, err
		}
	}
	return digest.Sum(nil), nil
}

func digestHistoryActivationRows(
	ctx context.Context, conn *sql.Conn, digest hash.Hash, query string, args ...any,
) error {
	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return err
	}
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for index := range values {
			pointers[index] = &values[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			return err
		}
		for _, value := range values {
			switch typed := value.(type) {
			case nil:
				writeHistoryDigestField(digest, nil)
			case []byte:
				writeHistoryDigestField(digest, typed)
			case time.Time:
				writeHistoryDigestField(digest, []byte(typed.UTC().Format(time.RFC3339Nano)))
			default:
				writeHistoryDigestField(digest, []byte(fmt.Sprint(typed)))
			}
		}
	}
	return rows.Err()
}

func loadHistoryActivationByCutoverConn(
	ctx context.Context, conn *sql.Conn, cutoverID []byte, ownerID string,
) (AskUserHistoryActivation, bool, error) {
	var result AskUserHistoryActivation
	var activationID []byte
	var sourceOwner, status string
	err := conn.QueryRowContext(ctx, `SELECT receipt.activation_id,receipt.source_stream_uid,receipt.target_stream_uid,
		receipt.source_epoch,receipt.target_epoch,receipt.authority_generation,receipt.active_branch_id,
		receipt.attempt_count,receipt.receipt_count,receipt.checkpoint_count,receipt.event_count,
		receipt.branch_count,receipt.branch_event_count,receipt.cursor_count,receipt.artifact_commit_count,
		receipt.artifact_ref_count,receipt.route_count,receipt.status,receipt.activated_at,source.owner_id
		FROM transcript_history_activation_receipts receipt
		JOIN transcript_streams source ON source.stream_uid=receipt.source_stream_uid
		WHERE receipt.cutover_id=?`, cutoverID).Scan(
		&activationID, &result.SourceStreamUID, &result.TargetStreamUID, &result.SourceEpoch, &result.TargetEpoch,
		&result.AuthorityGeneration, &result.ActiveBranchID, &result.AttemptCount, &result.ReceiptCount,
		&result.CheckpointCount, &result.EventCount, &result.BranchCount, &result.BranchEventCount,
		&result.CursorCount, &result.ArtifactCommitCount, &result.ArtifactRefCount, &result.RouteCount,
		&status, &result.ActivatedAt, &sourceOwner,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return AskUserHistoryActivation{}, false, nil
	}
	if err != nil {
		return AskUserHistoryActivation{}, false, err
	}
	if sourceOwner != ownerID {
		return AskUserHistoryActivation{}, false, ErrOwnerMismatch
	}
	if len(activationID) != sha256.Size || status != "active" {
		return AskUserHistoryActivation{}, false, ErrEventConflict
	}
	result.ActivationSHA256 = hex.EncodeToString(activationID)
	result.CutoverID = hex.EncodeToString(cutoverID)
	result.Active = true
	return result, true, nil
}

func loadHistoryActivationByIDConn(
	ctx context.Context, conn *sql.Conn, activationID []byte, ownerID string,
) (AskUserHistoryActivation, bool, error) {
	var result AskUserHistoryActivation
	var storedActivationID, provenanceID []byte
	var provenance, sourceOwner, status string
	err := conn.QueryRowContext(ctx, `SELECT receipt.activation_id,receipt.provenance_kind,
		COALESCE(receipt.cutover_id,receipt.ordinary_cutover_id),
		receipt.source_stream_uid,receipt.target_stream_uid,receipt.source_epoch,receipt.target_epoch,
		receipt.authority_generation,receipt.active_branch_id,receipt.attempt_count,receipt.receipt_count,
		receipt.checkpoint_count,receipt.event_count,receipt.branch_count,receipt.branch_event_count,
		receipt.cursor_count,receipt.artifact_commit_count,receipt.artifact_ref_count,receipt.route_count,
		receipt.status,receipt.activated_at,source.owner_id
		FROM transcript_history_activation_receipts receipt
		JOIN transcript_streams source ON source.stream_uid=receipt.source_stream_uid
		WHERE receipt.activation_id=?`, activationID).Scan(
		&storedActivationID, &provenance, &provenanceID,
		&result.SourceStreamUID, &result.TargetStreamUID, &result.SourceEpoch, &result.TargetEpoch,
		&result.AuthorityGeneration, &result.ActiveBranchID, &result.AttemptCount, &result.ReceiptCount,
		&result.CheckpointCount, &result.EventCount, &result.BranchCount, &result.BranchEventCount,
		&result.CursorCount, &result.ArtifactCommitCount, &result.ArtifactRefCount, &result.RouteCount,
		&status, &result.ActivatedAt, &sourceOwner,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return AskUserHistoryActivation{}, false, nil
	}
	if err != nil {
		return AskUserHistoryActivation{}, false, err
	}
	if sourceOwner != ownerID {
		return AskUserHistoryActivation{}, false, ErrOwnerMismatch
	}
	if len(storedActivationID) != sha256.Size || len(provenanceID) != sha256.Size || status != "active" ||
		(provenance != "ask_user_v30" && provenance != "ordinary_v33") {
		return AskUserHistoryActivation{}, false, ErrEventConflict
	}
	result.ActivationSHA256 = hex.EncodeToString(storedActivationID)
	result.CutoverID = hex.EncodeToString(provenanceID)
	result.Active = true
	return result, true, nil
}

func rebindHistoryActivationPayload(
	eventType string,
	payload []byte,
	streamUID string,
	frameID string,
	epoch int64,
) ([]byte, string, error) {
	switch eventType {
	case AskUserPromptEventType:
		prompt, err := DecodeAskUserPromptV1(payload)
		if err != nil {
			return nil, "", ErrEventConflict
		}
		prompt.Origin.StreamUID, prompt.Origin.FrameID, prompt.Origin.Epoch = streamUID, frameID, epoch
		rebound, err := json.Marshal(prompt)
		if err != nil || strings.TrimSpace(prompt.Origin.PromptClientMessageID) == "" {
			return nil, "", ErrEventConflict
		}
		return rebound, prompt.Origin.PromptClientMessageID, nil
	case AskUserResultEventType:
		result, err := DecodeAskUserResultEventV1(payload)
		if err != nil {
			return nil, "", ErrEventConflict
		}
		result.Origin.StreamUID, result.Origin.FrameID, result.Origin.Epoch = streamUID, frameID, epoch
		clientMessageID := result.Origin.PendingClientMessageID
		if result.Result.Status != AskUserStatusAwaitingResponse {
			clientMessageID, err = AskUserResultClientMessageIDV1(result.Origin)
			if err != nil {
				return nil, "", ErrEventConflict
			}
		}
		rebound, err := json.Marshal(result)
		if err != nil || strings.TrimSpace(clientMessageID) == "" {
			return nil, "", ErrEventConflict
		}
		return rebound, clientMessageID, nil
	default:
		var value any
		if !json.Valid(payload) || json.Unmarshal(payload, &value) != nil {
			return nil, "", ErrEventConflict
		}
		return append([]byte(nil), payload...), "", nil
	}
}
