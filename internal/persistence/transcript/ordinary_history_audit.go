package transcript

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"
	"strings"
	"time"
)

func (r *Repository) auditOrdinaryFrameHistory(
	ctx context.Context,
	streamUID, ownerID, branchID string,
) (AskUserHistoryAudit, bool, error) {
	var result AskUserHistoryAudit
	created := false
	err := r.withImmediate(ctx, func(conn *sql.Conn) error {
		audit, err := auditOrdinaryFrameHistoryConn(
			ctx, conn, streamUID, ownerID, branchID, r.now().UTC(),
		)
		if err != nil {
			return err
		}
		existing, found, err := getAskUserHistoryAuditConn(ctx, conn, GetAskUserHistoryAuditInput{
			RunID: audit.RunID, StreamUID: streamUID, OwnerID: ownerID,
		})
		if err != nil {
			return err
		}
		if found {
			if !askUserHistoryAuditsEqual(existing, audit) {
				return ErrEventConflict
			}
			result = existing
			return nil
		}
		if err := insertAskUserHistoryAuditConn(ctx, conn, audit); err != nil {
			return err
		}
		result = audit
		created = true
		return nil
	})
	return result, created, schemaError(err)
}

// auditOrdinaryFrameHistoryConn produces the same durable no-AskUser audit as
// AuditAskUserHistory without retaining the branch in memory or accepting a
// caller-supplied size ceiling. Rich Frame facts and typed AskUser facts remain
// outside the ordinary contract and are deferred to their typed migrations.
func auditOrdinaryFrameHistoryConn(
	ctx context.Context,
	conn *sql.Conn,
	streamUID, ownerID, requestedBranchID string,
	classifiedAt time.Time,
) (AskUserHistoryAudit, error) {
	stream, err := getRawStreamConn(ctx, conn, streamUID, ownerID)
	if err != nil {
		return AskUserHistoryAudit{}, err
	}
	if stream.Kind != StreamKindFrameRef || strings.TrimSpace(stream.FrameID) == "" {
		return AskUserHistoryAudit{}, ErrEventConflict
	}
	activeBranchID, generation, err := canonicalCloneBranchStateConn(ctx, conn, stream.UID)
	if err != nil {
		return AskUserHistoryAudit{}, err
	}
	branchID := strings.TrimSpace(requestedBranchID)
	if branchID == "" {
		branchID = activeBranchID
	}
	if !validTranscriptBranchID(branchID) {
		return AskUserHistoryAudit{}, ErrBranchTargetNotFound
	}
	var snapshot askUserHistorySnapshot
	snapshot.stream = stream
	snapshot.activeBranchID = activeBranchID
	snapshot.branchID = branchID
	snapshot.generation = generation
	if err := conn.QueryRowContext(ctx, `SELECT incarnation_id FROM frames WHERE id=?`, stream.FrameID).
		Scan(&snapshot.frameIncarnationID); err != nil {
		return AskUserHistoryAudit{}, err
	}
	snapshot.frameIncarnationID = strings.TrimSpace(snapshot.frameIncarnationID)
	if snapshot.frameIncarnationID == "" {
		return AskUserHistoryAudit{}, ErrEventConflict
	}
	var parent sql.NullString
	var forkEvent sql.NullInt64
	var requestDigest []byte
	if err := conn.QueryRowContext(ctx, `SELECT parent_branch_id,fork_event_id,fork_point,kind,
		client_mutation_id,request_sha256,source_message_id FROM transcript_branches
		WHERE stream_uid=? AND branch_id=?`, stream.UID, branchID).Scan(
		&parent, &forkEvent, &snapshot.branch.ForkPoint, &snapshot.branch.Kind,
		&snapshot.branch.ClientMutationID, &requestDigest, &snapshot.branch.SourceMessageID,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AskUserHistoryAudit{}, ErrBranchTargetNotFound
		}
		return AskUserHistoryAudit{}, err
	}
	if parent.Valid {
		snapshot.branch.ParentBranchID = parent.String
	}
	if forkEvent.Valid {
		snapshot.branch.ForkEventID = forkEvent.Int64
	}
	if len(requestDigest) != sha256.Size {
		return AskUserHistoryAudit{}, ErrEventConflict
	}
	snapshot.branch.RequestSHA256 = hex.EncodeToString(requestDigest)

	eventDigest, throughOrdinal, throughPublication, err := digestOrdinaryAuditEventsConn(
		ctx, conn, stream, branchID,
	)
	if err != nil {
		return AskUserHistoryAudit{}, err
	}
	snapshot.terminalLegacySHA256, err = digestAskUserHistoryRows(ctx, conn, 0, "terminal-facts-v1", `
		SELECT CASE status WHEN 'canceled' THEN 'cancelled' ELSE status END
		FROM frames WHERE id=? AND status IN ('completed','failed','cancelled','canceled')`, stream.FrameID)
	if err != nil {
		return AskUserHistoryAudit{}, err
	}
	snapshot.terminalCandidateSHA256, err = digestAskUserHistoryRows(ctx, conn, 0, "terminal-facts-v1", `
		SELECT CASE receipt.status WHEN 'canceled' THEN 'cancelled' ELSE receipt.status END
		FROM transcript_runner_receipts receipt
		JOIN transcript_branch_events membership
			ON membership.stream_uid=receipt.stream_uid AND membership.event_id=receipt.event_id
		WHERE receipt.stream_uid=? AND membership.branch_id=?
		ORDER BY receipt.attempt DESC LIMIT 1`, stream.UID, branchID)
	if err != nil {
		return AskUserHistoryAudit{}, err
	}
	snapshot.artifactLegacySHA256, err = digestAskUserHistoryRows(ctx, conn, 0, "artifact-refs-v1", `
		SELECT ref.runner_attempt,ref.source_event_id,ref.ordinal,ref.artifact_id,ref.version_id,ref.relation,ref.availability
		FROM transcript_artifact_refs ref
		JOIN transcript_branch_events membership
			ON membership.stream_uid=ref.stream_uid AND membership.event_id=ref.source_event_id
		WHERE ref.stream_uid=? AND membership.branch_id=?
		ORDER BY ref.runner_attempt,ref.source_event_id,ref.ordinal,ref.artifact_id,ref.version_id`, stream.UID, branchID)
	if err != nil {
		return AskUserHistoryAudit{}, err
	}
	snapshot.artifactCandidateSHA256, err = digestAskUserHistoryRows(ctx, conn, 0, "artifact-refs-v1", `
		SELECT ref.runner_attempt,ref.source_event_id,ref.ordinal,ref.artifact_id,ref.version_id,ref.relation,
			CASE WHEN ref.availability='deleted' THEN 'deleted'
				WHEN version.id IS NULL OR artifact.id IS NULL THEN 'missing' ELSE ref.availability END
		FROM transcript_artifact_refs ref
		JOIN transcript_branch_events membership
			ON membership.stream_uid=ref.stream_uid AND membership.event_id=ref.source_event_id
		LEFT JOIN artifact_versions version ON version.id=ref.version_id AND version.artifact_id=ref.artifact_id
		LEFT JOIN artifacts artifact ON artifact.id=ref.artifact_id
		WHERE ref.stream_uid=? AND membership.branch_id=?
		ORDER BY ref.runner_attempt,ref.source_event_id,ref.ordinal,ref.artifact_id,ref.version_id`, stream.UID, branchID)
	if err != nil {
		return AskUserHistoryAudit{}, err
	}

	audit := AskUserHistoryAudit{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, BranchID: branchID,
		BranchGeneration: generation, ContractVersion: askUserHistoryContractVersion,
		ThroughOrdinal: throughOrdinal, ThroughPublicationSequence: throughPublication,
		Status: AskUserHistoryNotApplicable, ReasonCode: AskUserHistoryReasonNone,
		ClassifiedAt: classifiedAt.UTC(),
	}
	sourceDigest := askUserHistorySnapshotDigestFromEvents(snapshot, eventDigest)
	audit.SourceSHA256 = hex.EncodeToString(sourceDigest[:])
	audit.RunID = askUserHistoryRunID(audit, sourceDigest)
	audit.Comparisons = buildAskUserHistoryShadowComparisons(snapshot, audit)
	return audit, nil
}

func digestOrdinaryAuditEventsConn(
	ctx context.Context,
	conn *sql.Conn,
	stream Stream,
	branchID string,
) ([sha256.Size]byte, int64, int64, error) {
	rows, err := conn.QueryContext(ctx, `SELECT membership.ordinal,event.event_id,event.publication_seq,
		event.client_message_id,event.event_type,event.source,event.runner_attempt,event.payload_json,
		event.frame_event_id,event.created_at,frame.frame_id,frame.event_type,frame.payload
		FROM transcript_branch_events membership
		JOIN transcript_events event ON event.stream_uid=membership.stream_uid AND event.event_id=membership.event_id
		LEFT JOIN frame_events frame ON event.source='frame_ref' AND frame.id=event.frame_event_id
		WHERE membership.stream_uid=? AND membership.branch_id=? ORDER BY membership.ordinal`, stream.UID, branchID)
	if err != nil {
		return [sha256.Size]byte{}, 0, 0, err
	}
	defer rows.Close()
	digest := sha256.New()
	writeHistoryDigestField(digest, []byte(HistoryClassificationContractID))
	var ordinal, throughPublication int64
	for rows.Next() {
		var item askUserHistoryEvent
		var source string
		var runnerAttempt sql.NullInt64
		var frameEventID sql.NullString
		var frameID, frameType, framePayload sql.NullString
		if err := rows.Scan(&item.ordinal, &item.event.EventID, &item.event.PublicationSeq,
			&item.event.ClientMessageID, &item.event.Type, &source, &runnerAttempt,
			&item.event.PayloadJSON, &frameEventID, &item.event.CreatedAt,
			&frameID, &frameType, &framePayload); err != nil {
			return [sha256.Size]byte{}, 0, 0, err
		}
		ordinal++
		if item.ordinal != ordinal || item.event.EventID <= 0 || item.event.PublicationSeq <= 0 {
			return [sha256.Size]byte{}, 0, 0, ErrEventConflict
		}
		item.event.StreamUID = stream.UID
		item.event.Source = EventSource(source)
		if runnerAttempt.Valid {
			attempt := runnerAttempt.Int64
			item.event.RunnerAttempt = &attempt
		}
		if frameEventID.Valid {
			id := frameEventID.String
			item.event.FrameEventID = &id
		}
		item.payload, err = materializedEventPayload(
			item.event, stream.Kind, stream.FrameID, frameID, frameType, framePayload,
		)
		if err != nil {
			return [sha256.Size]byte{}, 0, 0, err
		}
		if item.event.Type == AskUserPromptEventType || item.event.Type == AskUserResultEventType ||
			ordinaryPayloadContainsAskUserFact(item.payload) {
			return [sha256.Size]byte{}, 0, 0, errOrdinaryHistoryShapeUnsupported
		}
		if item.event.Source == EventSourceFrameRef {
			if !frameType.Valid || !framePayload.Valid {
				return [sha256.Size]byte{}, 0, 0, ErrEventConflict
			}
			if _, err := payloadGenesisFrameMessage(frameType.String, []byte(framePayload.String)); err != nil {
				return [sha256.Size]byte{}, 0, 0, errOrdinaryHistoryShapeUnsupported
			}
		}
		writeOrdinaryAuditEventDigest(digest, item)
		if item.event.PublicationSeq > throughPublication {
			throughPublication = item.event.PublicationSeq
		}
	}
	if err := rows.Err(); err != nil {
		return [sha256.Size]byte{}, 0, 0, err
	}
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result, ordinal, throughPublication, nil
}

func writeOrdinaryAuditEventDigest(digest hash.Hash, item askUserHistoryEvent) {
	var number [8]byte
	binary.BigEndian.PutUint64(number[:], uint64(item.ordinal))
	writeHistoryDigestField(digest, number[:])
	binary.BigEndian.PutUint64(number[:], uint64(item.event.EventID))
	writeHistoryDigestField(digest, number[:])
	binary.BigEndian.PutUint64(number[:], uint64(item.event.PublicationSeq))
	writeHistoryDigestField(digest, number[:])
	writeHistoryDigestField(digest, []byte(item.event.ClientMessageID))
	writeHistoryDigestField(digest, []byte(item.event.Type))
	writeHistoryDigestField(digest, []byte(item.event.Source))
	if item.event.RunnerAttempt != nil {
		binary.BigEndian.PutUint64(number[:], uint64(*item.event.RunnerAttempt))
		writeHistoryDigestField(digest, number[:])
	} else {
		writeHistoryDigestField(digest, nil)
	}
	if item.event.FrameEventID != nil {
		writeHistoryDigestField(digest, []byte(*item.event.FrameEventID))
	}
	payloadDigest := sha256.Sum256(item.payload)
	writeHistoryDigestField(digest, payloadDigest[:])
}
