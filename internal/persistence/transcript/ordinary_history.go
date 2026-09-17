package transcript

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
)

var errOrdinaryHistoryShapeUnsupported = errors.New("ordinary transcript history shape is not yet supported")

// ActivateOrdinaryFrameHistory converts a quiescent, plain legacy Frame
// history with no AskUser facts into one immutable payload epoch. The audit,
// payload materialization, cursor map, activation receipt, authority CAS and
// realtime invalidation commit in the same BEGIN IMMEDIATE transaction.
func (r *Repository) ActivateOrdinaryFrameHistory(
	ctx context.Context,
	runID, streamUID, ownerID string,
) (AskUserHistoryActivation, bool, error) {
	if r == nil || r.db == nil {
		return AskUserHistoryActivation{}, false, ErrSchemaUnavailable
	}
	runID = strings.ToLower(strings.TrimSpace(runID))
	streamUID = strings.TrimSpace(streamUID)
	ownerID = strings.TrimSpace(ownerID)
	if len(runID) != sha256.Size*2 || streamUID == "" || ownerID == "" {
		return AskUserHistoryActivation{}, false, errors.New("complete ordinary history authority is required")
	}
	runDigest, err := hex.DecodeString(runID)
	if err != nil || len(runDigest) != sha256.Size {
		return AskUserHistoryActivation{}, false, ErrEventConflict
	}
	var result AskUserHistoryActivation
	created := false
	err = r.withImmediate(ctx, func(conn *sql.Conn) error {
		if existing, found, err := loadOrdinaryHistoryActivationByAuditConn(
			ctx, conn, runDigest, streamUID, ownerID,
		); err != nil {
			return err
		} else if found {
			if err := verifyOrdinaryHistoryActivationConn(ctx, conn, existing, ownerID); err != nil {
				return err
			}
			source, err := getRawStreamConn(ctx, conn, existing.SourceStreamUID, ownerID)
			if err != nil {
				return err
			}
			if err := ensureHistoryActivationRealtimeRebaseConn(ctx, conn, existing, source, existing.ActivatedAt); err != nil {
				return err
			}
			result = existing
			return nil
		}
		audit, found, err := getAskUserHistoryAuditConn(ctx, conn, GetAskUserHistoryAuditInput{
			RunID: runID, StreamUID: streamUID, OwnerID: ownerID,
		})
		if err != nil {
			return err
		}
		if !found || audit.Status != AskUserHistoryNotApplicable || audit.CandidateCount != 0 ||
			audit.NativeCount != 0 || audit.EligibleCount != 0 || audit.PoisonCount != 0 || audit.ConflictCount != 0 {
			return ErrHistoryBackfillBlocked
		}
		if len(audit.Comparisons) != len(askUserHistoryShadowDimensions) {
			return ErrHistoryBackfillBlocked
		}
		for index, dimension := range askUserHistoryShadowDimensions {
			comparison := audit.Comparisons[index]
			if comparison.Dimension != dimension || comparison.Verdict == AskUserHistoryShadowMismatch ||
				(comparison.Verdict == AskUserHistoryShadowBlocked &&
					comparison.ReasonCode != "canonical_unavailable" && comparison.ReasonCode != "not_compared") {
				return ErrHistoryBackfillBlocked
			}
		}
		latestRunID, err := latestAskUserHistoryRunIDConn(ctx, conn, streamUID, audit.BranchID)
		if err != nil {
			return err
		}
		if latestRunID != runID {
			return ErrBranchStateStale
		}
		current, err := auditOrdinaryFrameHistoryConn(
			ctx, conn, streamUID, ownerID, audit.BranchID, audit.ClassifiedAt,
		)
		if err != nil {
			return err
		}
		if !askUserHistoryAuditsEqual(audit, current) {
			return ErrBranchStateStale
		}
		source, err := getRawStreamConn(ctx, conn, streamUID, ownerID)
		if err != nil {
			return err
		}
		result, err = r.activateOrdinaryFrameHistoryConn(ctx, conn, audit, source, runDigest)
		if err != nil {
			return err
		}
		created = true
		return nil
	})
	return result, created, schemaError(err)
}

func (r *Repository) activateOrdinaryFrameHistoryConn(
	ctx context.Context,
	conn *sql.Conn,
	audit AskUserHistoryAudit,
	source Stream,
	runDigest []byte,
) (AskUserHistoryActivation, error) {
	authority, err := loadCloneSourceFrameAuthorityConn(ctx, conn, source)
	if err != nil {
		return AskUserHistoryActivation{}, err
	}
	if authority.ReadAuthority != "legacy_mixed_v1" || authority.WriteAuthority != "legacy_frame_ref_v1" ||
		len(authority.ActivationID) != 0 || len(authority.GenesisID) != 0 ||
		source.InputRevision != source.ConsumedInputRevision || audit.ThroughOrdinal <= 0 ||
		audit.ThroughPublicationSequence <= 0 {
		return AskUserHistoryActivation{}, ErrHistoryBackfillBlocked
	}
	if err := blockUnsafeHistoryActivationConn(ctx, conn, source.UID); err != nil {
		return AskUserHistoryActivation{}, err
	}
	var frameStatus string
	if err := conn.QueryRowContext(ctx, `SELECT status FROM frames WHERE id=?`, source.FrameID).Scan(&frameStatus); err != nil {
		return AskUserHistoryActivation{}, err
	}
	if !canonicalCloneSourceSettled(frameStatus) {
		return AskUserHistoryActivation{}, ErrHistoryBackfillBlocked
	}
	counts, err := loadCanonicalCloneSourceCountsConn(ctx, conn, source.UID)
	if err != nil {
		return AskUserHistoryActivation{}, err
	}
	if counts.events <= 0 {
		return AskUserHistoryActivation{}, errOrdinaryHistoryShapeUnsupported
	}
	var payloadEvents int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcript_events
		WHERE stream_uid=? AND source='payload'`, source.UID).Scan(&payloadEvents); err != nil {
		return AskUserHistoryActivation{}, err
	}

	targetUID := ordinaryHistoryTargetStreamUID(source)
	target, err := createStreamConn(ctx, conn, CreateStreamInput{
		UID: targetUID, OwnerID: source.OwnerID, ExternalID: source.ExternalID, SessionID: source.SessionID,
		Kind: StreamKindFrameRef, ProjectID: source.ProjectID, RootFrameID: source.RootFrameID,
		FrameID: source.FrameID, Epoch: source.Epoch + 1,
	}, r.now().UTC())
	if err != nil {
		return AskUserHistoryActivation{}, err
	}
	stage, err := stageOrdinaryHistoryEventsConn(ctx, conn, source, target, counts)
	if err != nil {
		return AskUserHistoryActivation{}, err
	}
	canonicalStageClean := false
	defer func() {
		if !canonicalStageClean {
			_ = cleanupCanonicalCloneStageConn(context.Background(), conn, target.UID)
		}
	}()
	if _, err := conn.ExecContext(ctx, `DELETE FROM transcript_branch_state WHERE stream_uid=?`, target.UID); err != nil {
		return AskUserHistoryActivation{}, err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM transcript_branches WHERE stream_uid=?`, target.UID); err != nil {
		return AskUserHistoryActivation{}, err
	}
	materializedFacts, fullMaterializedDigest, err := materializeCanonicalCloneStageConn(
		ctx, conn, source, target, stage, counts,
	)
	if err != nil {
		return AskUserHistoryActivation{}, err
	}
	now := r.now().UTC()
	if _, err := conn.ExecContext(ctx, `UPDATE transcript_streams SET input_revision=?,consumed_input_revision=?,
		next_event_id=?,next_publication_seq=?,next_checkpoint_sequence=?,updated_at=?
		WHERE stream_uid=? AND input_revision=0 AND consumed_input_revision=0`,
		source.InputRevision, source.InputRevision, int64(stage.eventCount+1), int64(stage.eventCount+1),
		source.NextCheckpoint, now, target.UID); err != nil {
		return AskUserHistoryActivation{}, err
	}
	target, err = getRawStreamConn(ctx, conn, target.UID, target.OwnerID)
	if err != nil {
		return AskUserHistoryActivation{}, err
	}
	plainShape := payloadEvents == 0 && counts.branches == 1 && counts.attempts == 0 && counts.receipts == 0 &&
		counts.checkpoints == 0 && counts.artifactCommits == 0 && counts.artifactRefs == 0
	materialized := fullMaterializedDigest
	if plainShape {
		materialized, err = digestOrdinaryHistoryEventsConn(ctx, conn, target.UID)
		if err != nil {
			return AskUserHistoryActivation{}, err
		}
	}
	sourceDigest, err := hex.DecodeString(audit.SourceSHA256)
	if err != nil || len(sourceDigest) != sha256.Size {
		return AskUserHistoryActivation{}, ErrEventConflict
	}
	cutoverDigest := sha256.New()
	for _, value := range [][]byte{
		[]byte(HistoryOrdinaryCutoverContractID), runDigest, []byte(source.UID), []byte(target.UID),
		sourceDigest, stage.eventDigest, materialized,
	} {
		writeHistoryDigestField(cutoverDigest, value)
	}
	cutoverID := cutoverDigest.Sum(nil)
	cursorPlan, err := prepareOrdinaryHistoryCursorStageConn(ctx, conn, source, target, stage)
	if err != nil {
		return AskUserHistoryActivation{}, err
	}
	cursorStageClean := false
	defer func() {
		if !cursorStageClean {
			_ = cleanupOrdinaryHistoryCursorStageConn(context.Background(), conn, target.UID)
		}
	}()
	var routeCount int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcript_delivery_routes WHERE stream_uid=?`, target.UID).
		Scan(&routeCount); err != nil {
		return AskUserHistoryActivation{}, err
	}
	if routeCount <= 0 {
		return AskUserHistoryActivation{}, ErrEventConflict
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_history_ordinary_cutover_runs(
		ordinary_cutover_id,classification_run_id,source_stream_uid,target_stream_uid,owner_id,session_id,
		source_epoch,target_epoch,source_branch_id,target_branch_id,branch_generation,
		source_through_publication_seq,target_through_publication_seq,source_sha256,materialized_sha256,
		event_count,branch_count,branch_event_count,attempt_count,receipt_count,checkpoint_count,
		artifact_commit_count,artifact_ref_count,route_count,cursor_count,history_kind,status,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		cutoverID, runDigest, source.UID, target.UID, source.OwnerID, source.SessionID,
		source.Epoch, target.Epoch, audit.BranchID, materializedFacts.activeTarget, stage.branchGeneration,
		source.NextPublication-1, target.NextPublication-1, sourceDigest, materialized,
		stage.eventCount, materializedFacts.branches, materializedFacts.branchEvents,
		materializedFacts.attempts, materializedFacts.receipts, materializedFacts.checkpoints,
		materializedFacts.artifactCommits, materializedFacts.artifactRefs, routeCount, cursorPlan.count,
		"ordinary_no_ask_user_v1", "active", now,
	); err != nil {
		return AskUserHistoryActivation{}, err
	}
	if err := insertOrdinaryHistoryCursorStageConn(
		ctx, conn, cutoverID, source, target, stage, source.NextPublication-1, now,
	); err != nil {
		return AskUserHistoryActivation{}, err
	}
	var cursorCount, realtimeHighWater int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcript_history_ordinary_cursor_map
		WHERE ordinary_cutover_id=?`, cutoverID).Scan(&cursorCount); err != nil {
		return AskUserHistoryActivation{}, err
	}
	if cursorCount != cursorPlan.count {
		return AskUserHistoryActivation{}, ErrEventConflict
	}
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM realtime_events`).Scan(&realtimeHighWater); err != nil {
		return AskUserHistoryActivation{}, err
	}
	cursorDigest := cursorPlan.digest
	if plainShape {
		cursorDigest, err = ordinaryHistoryLegacyCursorDigestConn(ctx, conn, cutoverID)
		if err != nil {
			return AskUserHistoryActivation{}, err
		}
	}
	verification := ordinaryHistoryDigest("verification", sourceDigest, materialized)
	lineage, err := digestOrdinaryHistoryLineageConn(ctx, conn, target.UID)
	if err != nil {
		return AskUserHistoryActivation{}, err
	}
	shadow := ordinaryHistoryDigest("shadow", sourceDigest, materialized, cursorDigest)
	activationID := ordinaryHistoryDigest("activation", cutoverID, verification, lineage, cursorDigest, shadow, materialized)
	if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_history_activation_receipts(
		activation_id,provenance_kind,cutover_id,ordinary_cutover_id,source_stream_uid,target_stream_uid,
		owner_id,session_id,source_epoch,target_epoch,prior_activation_id,active_branch_id,branch_generation,
		source_through_publication_seq,target_through_publication_seq,verification_sha256,lineage_sha256,
		cursor_sha256,shadow_sha256,materialized_sha256,attempt_count,receipt_count,checkpoint_count,
		event_count,branch_count,branch_event_count,cursor_count,artifact_commit_count,artifact_ref_count,
		route_count,realtime_high_water,authority_generation,status,activated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		activationID, "ordinary_v33", nil, cutoverID, source.UID, target.UID, source.OwnerID, source.SessionID,
		source.Epoch, target.Epoch, nil, materializedFacts.activeTarget, stage.branchGeneration,
		source.NextPublication-1, target.NextPublication-1, verification, lineage,
		cursorDigest, shadow, materialized, materializedFacts.attempts, materializedFacts.receipts,
		materializedFacts.checkpoints, stage.eventCount, materializedFacts.branches,
		materializedFacts.branchEvents, cursorCount, materializedFacts.artifactCommits,
		materializedFacts.artifactRefs, routeCount, realtimeHighWater,
		authority.AuthorityGeneration+1, "active", now,
	); err != nil {
		return AskUserHistoryActivation{}, err
	}
	updated, err := conn.ExecContext(ctx, `UPDATE transcript_frame_authority SET
		active_stream_uid=?,active_epoch=?,authority_generation=?,read_authority='transcript_payload_v1',
		write_authority='transcript_payload_v1',activation_id=?,genesis_id=NULL,updated_at=?
		WHERE owner_id=? AND session_id=? AND active_stream_uid=? AND active_epoch=? AND authority_generation=?
			AND read_authority='legacy_mixed_v1' AND write_authority='legacy_frame_ref_v1'
			AND activation_id IS NULL AND genesis_id IS NULL`,
		target.UID, target.Epoch, authority.AuthorityGeneration+1, activationID, now,
		source.OwnerID, source.SessionID, source.UID, source.Epoch, authority.AuthorityGeneration)
	if err != nil {
		return AskUserHistoryActivation{}, err
	}
	if changed, err := updated.RowsAffected(); err != nil || changed != 1 {
		if err != nil {
			return AskUserHistoryActivation{}, err
		}
		return AskUserHistoryActivation{}, ErrEventConflict
	}
	result := AskUserHistoryActivation{
		ActivationSHA256: hex.EncodeToString(activationID), CutoverID: hex.EncodeToString(cutoverID),
		SourceStreamUID: source.UID, TargetStreamUID: target.UID, SourceEpoch: source.Epoch, TargetEpoch: target.Epoch,
		AuthorityGeneration: authority.AuthorityGeneration + 1, ActiveBranchID: materializedFacts.activeTarget,
		AttemptCount: materializedFacts.attempts, ReceiptCount: materializedFacts.receipts,
		CheckpointCount: materializedFacts.checkpoints, EventCount: stage.eventCount,
		BranchCount: materializedFacts.branches, BranchEventCount: materializedFacts.branchEvents,
		CursorCount: cursorCount, ArtifactCommitCount: materializedFacts.artifactCommits,
		ArtifactRefCount: materializedFacts.artifactRefs, RouteCount: routeCount, Active: true, ActivatedAt: now,
	}
	if err := ensureHistoryActivationRealtimeRebaseConn(ctx, conn, result, source, now); err != nil {
		return AskUserHistoryActivation{}, err
	}
	if err := cleanupOrdinaryHistoryCursorStageConn(ctx, conn, target.UID); err != nil {
		return AskUserHistoryActivation{}, err
	}
	cursorStageClean = true
	if err := cleanupCanonicalCloneStageConn(ctx, conn, target.UID); err != nil {
		return AskUserHistoryActivation{}, err
	}
	canonicalStageClean = true
	return result, nil
}

func ordinaryHistoryTargetStreamUID(source Stream) string {
	digest := sha256.Sum256([]byte(HistoryOrdinaryCutoverContractID + "\x00" + source.OwnerID + "\x00" +
		source.SessionID + "\x00" + source.UID + "\x00" + strconv.FormatInt(source.Epoch+1, 10)))
	return "history-ordinary:" + hex.EncodeToString(digest[:])
}

func ordinaryHistoryDigest(domain string, values ...[]byte) []byte {
	digest := sha256.New()
	writeHistoryDigestField(digest, []byte(HistoryOrdinaryCutoverContractID))
	writeHistoryDigestField(digest, []byte(domain))
	for _, value := range values {
		writeHistoryDigestField(digest, value)
	}
	return digest.Sum(nil)
}

func digestOrdinaryHistoryEventsConn(ctx context.Context, conn *sql.Conn, streamUID string) ([]byte, error) {
	digest := sha256.New()
	if err := digestHistoryActivationRows(ctx, conn, digest, `SELECT event_id,publication_seq,client_message_id,
		event_type,payload_json,created_at FROM transcript_events WHERE stream_uid=? ORDER BY publication_seq,event_id`, streamUID); err != nil {
		return nil, err
	}
	return digest.Sum(nil), nil
}

func digestOrdinaryHistoryLineageConn(ctx context.Context, conn *sql.Conn, streamUID string) ([]byte, error) {
	digest := sha256.New()
	writeHistoryDigestField(digest, []byte(HistoryOrdinaryCutoverContractID))
	writeHistoryDigestField(digest, []byte("lineage"))
	if err := digestHistoryActivationRows(ctx, conn, digest, `SELECT branch_id,parent_branch_id,fork_event_id,
		fork_point,kind,client_mutation_id,request_sha256,source_message_id,created_at,updated_at
		FROM transcript_branches WHERE stream_uid=? ORDER BY branch_id`, streamUID); err != nil {
		return nil, err
	}
	if err := digestHistoryActivationRows(ctx, conn, digest, `SELECT active_branch_id,generation,updated_at
		FROM transcript_branch_state WHERE stream_uid=?`, streamUID); err != nil {
		return nil, err
	}
	if err := digestHistoryActivationRows(ctx, conn, digest, `SELECT branch_id,ordinal,event_id
		FROM transcript_branch_events WHERE stream_uid=? ORDER BY branch_id,ordinal`, streamUID); err != nil {
		return nil, err
	}
	return digest.Sum(nil), nil
}

func loadOrdinaryHistoryActivationByAuditConn(
	ctx context.Context, conn *sql.Conn, runID []byte, streamUID, ownerID string,
) (AskUserHistoryActivation, bool, error) {
	var activationID, cutoverID []byte
	var result AskUserHistoryActivation
	var status string
	err := conn.QueryRowContext(ctx, `SELECT receipt.activation_id,cutover.ordinary_cutover_id,
		receipt.source_stream_uid,receipt.target_stream_uid,receipt.source_epoch,receipt.target_epoch,
		receipt.authority_generation,receipt.active_branch_id,receipt.attempt_count,receipt.receipt_count,
		receipt.checkpoint_count,receipt.event_count,receipt.branch_count,receipt.branch_event_count,
		receipt.cursor_count,receipt.artifact_commit_count,receipt.artifact_ref_count,receipt.route_count,
		receipt.status,receipt.activated_at
		FROM transcript_history_ordinary_cutover_runs cutover
		JOIN transcript_history_activation_receipts receipt
			ON receipt.ordinary_cutover_id=cutover.ordinary_cutover_id AND receipt.provenance_kind='ordinary_v33'
		WHERE cutover.classification_run_id=? AND cutover.source_stream_uid=? AND cutover.owner_id=?`,
		runID, streamUID, ownerID).Scan(
		&activationID, &cutoverID, &result.SourceStreamUID, &result.TargetStreamUID,
		&result.SourceEpoch, &result.TargetEpoch, &result.AuthorityGeneration, &result.ActiveBranchID,
		&result.AttemptCount, &result.ReceiptCount, &result.CheckpointCount, &result.EventCount,
		&result.BranchCount, &result.BranchEventCount, &result.CursorCount,
		&result.ArtifactCommitCount, &result.ArtifactRefCount, &result.RouteCount, &status, &result.ActivatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return AskUserHistoryActivation{}, false, nil
	}
	if err != nil {
		return AskUserHistoryActivation{}, false, err
	}
	if len(activationID) != sha256.Size || len(cutoverID) != sha256.Size || status != "active" {
		return AskUserHistoryActivation{}, false, ErrEventConflict
	}
	result.ActivationSHA256 = hex.EncodeToString(activationID)
	result.CutoverID = hex.EncodeToString(cutoverID)
	result.Active = true
	return result, true, nil
}

func verifyOrdinaryHistoryActivationConn(
	ctx context.Context, conn *sql.Conn, activation AskUserHistoryActivation, ownerID string,
) error {
	activationID, err := hex.DecodeString(activation.ActivationSHA256)
	if err != nil || len(activationID) != sha256.Size {
		return ErrEventConflict
	}
	cutoverID, err := hex.DecodeString(activation.CutoverID)
	if err != nil || len(cutoverID) != sha256.Size {
		return ErrEventConflict
	}
	var receiptMaterialized, receiptCursor, receiptLineage, cutoverMaterialized []byte
	var branchGeneration, stateGeneration, sourceThrough, targetThrough, targetNextPublication int64
	var cutoverEvents, cutoverBranches, cutoverBranchEvents, cutoverCursors int
	var activeBranchID string
	err = conn.QueryRowContext(ctx, `SELECT receipt.materialized_sha256,receipt.cursor_sha256,receipt.lineage_sha256,
		receipt.branch_generation,receipt.source_through_publication_seq,
		receipt.target_through_publication_seq,cutover.materialized_sha256,
		cutover.event_count,cutover.branch_count,cutover.branch_event_count,cutover.cursor_count,
		target.next_publication_seq,state.active_branch_id,state.generation
		FROM transcript_history_activation_receipts receipt
		JOIN transcript_history_ordinary_cutover_runs cutover
			ON cutover.ordinary_cutover_id=receipt.ordinary_cutover_id
		JOIN transcript_streams target ON target.stream_uid=receipt.target_stream_uid
		JOIN transcript_branch_state state ON state.stream_uid=receipt.target_stream_uid
		JOIN transcript_frame_authority authority ON authority.owner_id=receipt.owner_id
			AND authority.session_id=receipt.session_id
		WHERE receipt.activation_id=? AND receipt.provenance_kind='ordinary_v33'
			AND receipt.ordinary_cutover_id=? AND receipt.owner_id=? AND receipt.status='active'
			AND authority.active_stream_uid=receipt.target_stream_uid
			AND authority.active_epoch=receipt.target_epoch
			AND authority.authority_generation=receipt.authority_generation
			AND authority.activation_id=receipt.activation_id
			AND authority.read_authority='transcript_payload_v1'
			AND authority.write_authority='transcript_payload_v1'`, activationID, cutoverID, ownerID).Scan(
		&receiptMaterialized, &receiptCursor, &receiptLineage, &branchGeneration, &sourceThrough, &targetThrough,
		&cutoverMaterialized, &cutoverEvents, &cutoverBranches, &cutoverBranchEvents, &cutoverCursors,
		&targetNextPublication, &activeBranchID, &stateGeneration,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrEventConflict
	}
	if err != nil {
		return err
	}
	if !bytes.Equal(receiptMaterialized, cutoverMaterialized) ||
		activeBranchID != activation.ActiveBranchID || targetNextPublication-1 != targetThrough ||
		cutoverEvents != activation.EventCount || cutoverBranches != activation.BranchCount ||
		cutoverBranchEvents != activation.BranchEventCount || cutoverCursors != activation.CursorCount ||
		sourceThrough <= 0 || branchGeneration <= 0 || stateGeneration != branchGeneration {
		return ErrEventConflict
	}
	richShape := activation.BranchCount > 1 || activation.AttemptCount > 0 || activation.ReceiptCount > 0 ||
		activation.CheckpointCount > 0 || activation.ArtifactCommitCount > 0 || activation.ArtifactRefCount > 0
	var sourcePayloadEvents int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcript_events
		WHERE stream_uid=? AND source='payload'`, activation.SourceStreamUID).Scan(&sourcePayloadEvents); err != nil {
		return err
	}
	richShape = richShape || sourcePayloadEvents > 0
	if richShape {
		source, err := getRawStreamConn(ctx, conn, activation.SourceStreamUID, ownerID)
		if err != nil {
			return err
		}
		target, err := getRawStreamConn(ctx, conn, activation.TargetStreamUID, ownerID)
		if err != nil {
			return err
		}
		counts, err := loadCanonicalCloneSourceCountsConn(ctx, conn, source.UID)
		if err != nil {
			return err
		}
		stage, err := stageOrdinaryHistoryEventsConn(ctx, conn, source, target, counts)
		if err != nil {
			return err
		}
		canonicalStageClean := false
		defer func() {
			if !canonicalStageClean {
				_ = cleanupCanonicalCloneStageConn(context.Background(), conn, target.UID)
			}
		}()
		expected, expectedDigest, err := expectedCanonicalCloneStageConn(ctx, conn, source, target, stage)
		if err != nil {
			return err
		}
		if !bytes.Equal(expectedDigest, receiptMaterialized) || expected.activeTarget != activation.ActiveBranchID ||
			expected.branches != activation.BranchCount || expected.branchEvents != activation.BranchEventCount ||
			expected.artifactCommits != activation.ArtifactCommitCount || expected.artifactRefs != activation.ArtifactRefCount {
			return ErrEventConflict
		}
		if err := verifyCanonicalCloneTargetEventsConn(ctx, conn, target.UID, stage.eventCount); err != nil {
			return err
		}
		if err := verifyCanonicalCloneTargetRunnerConn(ctx, conn, source.UID, target.UID, counts); err != nil {
			return err
		}
		branchDigest, branchCount, branchEventCount, err := digestCanonicalCloneTargetBranchesConn(
			ctx, conn, target.UID, stage.activeSource,
		)
		if err != nil || !bytes.Equal(branchDigest, expected.branchDigest) ||
			branchCount != expected.branches || branchEventCount != expected.branchEvents {
			if err != nil {
				return err
			}
			return ErrEventConflict
		}
		artifactDigest, commitCount, refCount, err := digestCanonicalCloneTargetArtifactsConn(ctx, conn, target.UID)
		if err != nil || !bytes.Equal(artifactDigest, expected.artifactDigest) ||
			commitCount != expected.artifactCommits || refCount != expected.artifactRefs {
			if err != nil {
				return err
			}
			return ErrEventConflict
		}
		if err := cleanupCanonicalCloneStageConn(ctx, conn, target.UID); err != nil {
			return err
		}
		canonicalStageClean = true
	} else {
		actualMaterialized, err := digestOrdinaryHistoryEventsConn(ctx, conn, activation.TargetStreamUID)
		if err != nil {
			return err
		}
		if !bytes.Equal(actualMaterialized, receiptMaterialized) {
			return ErrEventConflict
		}
	}
	actualLineage, err := digestOrdinaryHistoryLineageConn(ctx, conn, activation.TargetStreamUID)
	if err != nil {
		return err
	}
	if !bytes.Equal(actualLineage, receiptLineage) {
		return ErrEventConflict
	}
	var events, branches, branchEvents, cursors, attempts, receipts, checkpoints, commits, refs, routes int
	if err := conn.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM transcript_events WHERE stream_uid=?),
		(SELECT COUNT(*) FROM transcript_branches WHERE stream_uid=?),
		(SELECT COUNT(*) FROM transcript_branch_events WHERE stream_uid=?),
		(SELECT COUNT(*) FROM transcript_history_ordinary_cursor_map WHERE ordinary_cutover_id=?),
		(SELECT COUNT(*) FROM transcript_runner_attempts WHERE stream_uid=?),
		(SELECT COUNT(*) FROM transcript_runner_receipts WHERE stream_uid=?),
		(SELECT COUNT(*) FROM transcript_runner_checkpoints WHERE stream_uid=?),
		(SELECT COUNT(*) FROM transcript_artifact_commits WHERE stream_uid=?),
		(SELECT COUNT(*) FROM transcript_artifact_refs WHERE stream_uid=?),
		(SELECT COUNT(*) FROM transcript_delivery_routes WHERE stream_uid=?)`,
		activation.TargetStreamUID, activation.TargetStreamUID, activation.TargetStreamUID, cutoverID,
		activation.TargetStreamUID, activation.TargetStreamUID, activation.TargetStreamUID,
		activation.TargetStreamUID, activation.TargetStreamUID, activation.TargetStreamUID,
	).Scan(&events, &branches, &branchEvents, &cursors, &attempts, &receipts, &checkpoints, &commits, &refs, &routes); err != nil {
		return err
	}
	if events != activation.EventCount || branches != activation.BranchCount ||
		branchEvents != activation.BranchEventCount || cursors != activation.CursorCount ||
		attempts != activation.AttemptCount || receipts != activation.ReceiptCount ||
		checkpoints != activation.CheckpointCount || commits != activation.ArtifactCommitCount ||
		refs != activation.ArtifactRefCount || routes != activation.RouteCount {
		return ErrEventConflict
	}
	actualCursor, err := ordinaryHistoryCursorDigestConn(ctx, conn, cutoverID)
	if !richShape {
		actualCursor, err = ordinaryHistoryLegacyCursorDigestConn(ctx, conn, cutoverID)
	}
	if err != nil {
		return err
	}
	if !bytes.Equal(actualCursor, receiptCursor) {
		return ErrEventConflict
	}
	return nil
}
