package transcript

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

var askUserHistoryShadowDimensions = []AskUserHistoryShadowDimension{
	AskUserHistoryShadowStableIDs,
	AskUserHistoryShadowBranchOrder,
	AskUserHistoryShadowStates,
	AskUserHistoryShadowTerminal,
	AskUserHistoryShadowArtifacts,
}

func finalizeAskUserHistoryAudit(audit *AskUserHistoryAudit, snapshot askUserHistorySnapshot) {
	sourceDigest := askUserHistorySnapshotDigest(snapshot)
	audit.SourceSHA256 = hex.EncodeToString(sourceDigest[:])
	audit.RunID = askUserHistoryRunID(*audit, sourceDigest)
	audit.NativeCount, audit.EligibleCount, audit.PoisonCount, audit.ConflictCount = 0, 0, 0, 0
	for index := range audit.Details {
		detail := &audit.Details[index]
		if detail.FirstOrdinal <= 0 {
			detail.FirstOrdinal = 1
		}
		evidence := askUserHistoryCandidateEvidence(snapshot, *detail)
		detail.EvidenceJSON, _ = json.Marshal(evidence)
		evidenceDigest := sha256.Sum256(detail.EvidenceJSON)
		detail.EvidenceSHA256 = hex.EncodeToString(evidenceDigest[:])
		detail.CandidateID = askUserHistoryCandidateID(audit.RunID, *detail)
		if detail.Status == AskUserHistoryQuarantined || detail.Status == AskUserHistoryConflict {
			detail.StateJSON = nil
		}
		switch detail.Status {
		case AskUserHistoryNativeV1:
			audit.NativeCount++
		case AskUserHistoryEligible:
			audit.EligibleCount++
		case AskUserHistoryQuarantined:
			audit.PoisonCount++
		case AskUserHistoryConflict:
			audit.ConflictCount++
		}
	}
	audit.CandidateCount = len(audit.Details)
	audit.Comparisons = buildAskUserHistoryShadowComparisons(snapshot, *audit)
}

func askUserHistorySnapshotDigest(snapshot askUserHistorySnapshot) [sha256.Size]byte {
	eventDigest := askUserHistorySourceDigest(snapshot.events)
	return askUserHistorySnapshotDigestFromEvents(snapshot, eventDigest)
}

func askUserHistorySnapshotDigestFromEvents(
	snapshot askUserHistorySnapshot,
	eventDigest [sha256.Size]byte,
) [sha256.Size]byte {
	digest := sha256.New()
	writeHistoryDigestField(digest, []byte("synon.transcript.history-source.v1"))
	writeHistoryDigestField(digest, []byte(snapshot.stream.UID))
	writeHistoryDigestField(digest, []byte(snapshot.stream.OwnerID))
	writeHistoryDigestField(digest, []byte(snapshot.stream.FrameID))
	writeHistoryDigestField(digest, []byte(snapshot.frameIncarnationID))
	writeHistoryDigestField(digest, []byte(snapshot.activeBranchID))
	writeHistoryDigestField(digest, []byte(snapshot.branchID))
	var number [8]byte
	binary.BigEndian.PutUint64(number[:], uint64(snapshot.stream.Epoch))
	writeHistoryDigestField(digest, number[:])
	binary.BigEndian.PutUint64(number[:], uint64(snapshot.generation))
	writeHistoryDigestField(digest, number[:])
	branchJSON, _ := json.Marshal(snapshot.branch)
	writeHistoryDigestField(digest, branchJSON)
	writeHistoryDigestField(digest, eventDigest[:])
	writeHistoryDigestField(digest, snapshot.terminalLegacySHA256[:])
	writeHistoryDigestField(digest, snapshot.terminalCandidateSHA256[:])
	writeHistoryDigestField(digest, snapshot.artifactLegacySHA256[:])
	writeHistoryDigestField(digest, snapshot.artifactCandidateSHA256[:])
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result
}

func askUserHistoryRunID(audit AskUserHistoryAudit, sourceDigest [sha256.Size]byte) string {
	digest := sha256.New()
	writeHistoryDigestField(digest, []byte("synon.transcript.history-run.v1"))
	var number [8]byte
	binary.BigEndian.PutUint64(number[:], askUserHistoryContractVersion)
	writeHistoryDigestField(digest, number[:])
	writeHistoryDigestField(digest, []byte(audit.StreamUID))
	writeHistoryDigestField(digest, []byte(audit.BranchID))
	binary.BigEndian.PutUint64(number[:], uint64(audit.BranchGeneration))
	writeHistoryDigestField(digest, number[:])
	binary.BigEndian.PutUint64(number[:], uint64(audit.ThroughOrdinal))
	writeHistoryDigestField(digest, number[:])
	binary.BigEndian.PutUint64(number[:], uint64(audit.ThroughPublicationSequence))
	writeHistoryDigestField(digest, number[:])
	writeHistoryDigestField(digest, sourceDigest[:])
	return hex.EncodeToString(digest.Sum(nil))
}

func askUserHistoryCandidateID(runID string, detail AskUserHistoryAuditDetail) string {
	digest := sha256.New()
	writeHistoryDigestField(digest, []byte("synon.transcript.history-candidate.v1"))
	writeHistoryDigestField(digest, []byte(runID))
	var number [8]byte
	binary.BigEndian.PutUint64(number[:], uint64(detail.FirstOrdinal))
	writeHistoryDigestField(digest, number[:])
	writeHistoryDigestField(digest, []byte(detail.ToolUseID))
	return hex.EncodeToString(digest.Sum(nil))
}

func askUserHistoryCandidateEvidence(snapshot askUserHistorySnapshot, detail AskUserHistoryAuditDetail) map[string]any {
	evidence := map[string]any{
		"version": 1, "stream_uid": snapshot.stream.UID, "branch_id": snapshot.branchID,
		"branch_generation": snapshot.generation, "tool_use_id": detail.ToolUseID,
		"frame_incarnation_id": snapshot.frameIncarnationID, "branch_lineage": snapshot.branch,
		"reason_code": detail.ReasonCode,
	}
	if event, found := askUserHistoryEventByID(snapshot.events, detail.ToolEventID); found {
		evidence["tool_event"] = askUserHistoryEventEvidence(event)
	}
	if event, found := askUserHistoryEventByID(snapshot.events, detail.ResultEventID); found {
		evidence["result_event"] = askUserHistoryEventEvidence(event)
	}
	return evidence
}

func askUserHistoryEventEvidence(item askUserHistoryEvent) map[string]any {
	payloadDigest := sha256.Sum256(item.payload)
	result := map[string]any{
		"event_id": item.event.EventID, "ordinal": item.ordinal,
		"publication_seq": item.event.PublicationSeq, "payload_sha256": hex.EncodeToString(payloadDigest[:]),
	}
	if item.event.FrameEventID != nil {
		result["frame_event_id"] = *item.event.FrameEventID
	}
	return result
}

func askUserHistoryEventByID(events []askUserHistoryEvent, eventID int64) (askUserHistoryEvent, bool) {
	for _, item := range events {
		if item.event.EventID == eventID {
			return item, true
		}
	}
	return askUserHistoryEvent{}, false
}

func askUserHistorySnapshotsEqual(left, right askUserHistorySnapshot) bool {
	if left.stream.UID != right.stream.UID || left.stream.OwnerID != right.stream.OwnerID ||
		left.stream.FrameID != right.stream.FrameID || left.stream.Epoch != right.stream.Epoch ||
		left.branchID != right.branchID || left.generation != right.generation ||
		askUserHistoryThroughOrdinal(left.events) != askUserHistoryThroughOrdinal(right.events) ||
		askUserHistoryThroughPublication(left.events) != askUserHistoryThroughPublication(right.events) {
		return false
	}
	return askUserHistorySnapshotDigest(left) == askUserHistorySnapshotDigest(right)
}

func askUserHistoryThroughOrdinal(events []askUserHistoryEvent) int64 {
	if len(events) == 0 {
		return 0
	}
	return events[len(events)-1].ordinal
}

func askUserHistoryThroughPublication(events []askUserHistoryEvent) int64 {
	result := int64(0)
	for _, item := range events {
		if item.event.PublicationSeq > result {
			result = item.event.PublicationSeq
		}
	}
	return result
}

func digestAskUserHistoryRows(
	ctx context.Context,
	query askUserHistoryQueryer,
	maxRows int,
	domain string,
	statement string,
	args ...any,
) ([sha256.Size]byte, error) {
	rows, err := query.QueryContext(ctx, statement, args...)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	digest := sha256.New()
	writeHistoryDigestField(digest, []byte("synon.transcript.history-rows.v1"))
	writeHistoryDigestField(digest, []byte(domain))
	var columnCount [8]byte
	binary.BigEndian.PutUint64(columnCount[:], uint64(len(columns)))
	writeHistoryDigestField(digest, columnCount[:])
	rowCount := 0
	for rows.Next() {
		if maxRows > 0 && rowCount >= maxRows {
			return [sha256.Size]byte{}, ErrHistoryAuditBudgetExceeded
		}
		rowCount++
		var rowNumber [8]byte
		binary.BigEndian.PutUint64(rowNumber[:], uint64(rowCount))
		writeHistoryDigestField(digest, rowNumber[:])
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return [sha256.Size]byte{}, err
		}
		for _, value := range values {
			encoded, err := encodeAskUserHistorySQLValue(value)
			if err != nil {
				return [sha256.Size]byte{}, err
			}
			writeHistoryDigestField(digest, encoded)
		}
	}
	if err := rows.Err(); err != nil {
		return [sha256.Size]byte{}, err
	}
	var encodedCount [8]byte
	binary.BigEndian.PutUint64(encodedCount[:], uint64(rowCount))
	writeHistoryDigestField(digest, encodedCount[:])
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result, nil
}

func encodeAskUserHistorySQLValue(value any) ([]byte, error) {
	switch typed := value.(type) {
	case nil:
		return []byte{'n'}, nil
	case int64:
		var encoded [9]byte
		encoded[0] = 'i'
		binary.BigEndian.PutUint64(encoded[1:], uint64(typed))
		return encoded[:], nil
	case float64:
		encoded, _ := json.Marshal(typed)
		return append([]byte{'f'}, encoded...), nil
	case bool:
		if typed {
			return []byte("b1"), nil
		}
		return []byte("b0"), nil
	case []byte:
		return append([]byte{'x'}, typed...), nil
	case string:
		return append([]byte{'s'}, []byte(typed)...), nil
	case time.Time:
		return append([]byte{'t'}, []byte(typed.UTC().Format(time.RFC3339Nano))...), nil
	default:
		return nil, fmt.Errorf("unsupported transcript history SQL value %T", value)
	}
}

func buildAskUserHistoryShadowComparisons(
	snapshot askUserHistorySnapshot,
	audit AskUserHistoryAudit,
) []AskUserHistoryShadowComparison {
	now := audit.ClassifiedAt
	comparable := audit.NativeCount+audit.EligibleCount > 0 && audit.PoisonCount == 0 && audit.ConflictCount == 0
	result := make([]AskUserHistoryShadowComparison, 0, len(askUserHistoryShadowDimensions))
	for _, dimension := range askUserHistoryShadowDimensions {
		legacyPayload, candidatePayload, available := askUserHistoryShadowSides(snapshot, audit, dimension, comparable)
		legacyDigest := sha256.Sum256(legacyPayload)
		comparison := AskUserHistoryShadowComparison{
			Dimension: dimension, Verdict: AskUserHistoryShadowBlocked,
			LegacySHA256: hex.EncodeToString(legacyDigest[:]), ReasonCode: "canonical_unavailable",
			ComparedAt: now,
		}
		if available {
			candidateDigest := sha256.Sum256(candidatePayload)
			comparison.CandidateSHA256 = hex.EncodeToString(candidateDigest[:])
			comparison.Verdict = AskUserHistoryShadowMatch
			comparison.ReasonCode = "none"
			if legacyDigest != candidateDigest {
				comparison.Verdict = AskUserHistoryShadowMismatch
				comparison.ReasonCode = "digest_mismatch"
			}
		} else if dimension == AskUserHistoryShadowTerminal || dimension == AskUserHistoryShadowArtifacts {
			comparison.ReasonCode = "not_compared"
		}
		comparison.EvidenceJSON, _ = json.Marshal(map[string]any{
			"version": 1, "dimension": dimension, "candidate_count": audit.CandidateCount,
		})
		result = append(result, comparison)
	}
	return result
}

func askUserHistoryShadowSides(
	snapshot askUserHistorySnapshot,
	audit AskUserHistoryAudit,
	dimension AskUserHistoryShadowDimension,
	comparable bool,
) ([]byte, []byte, bool) {
	if dimension == AskUserHistoryShadowTerminal {
		return append([]byte(nil), snapshot.terminalLegacySHA256[:]...),
			append([]byte(nil), snapshot.terminalCandidateSHA256[:]...), comparable && snapshot.branchID == snapshot.activeBranchID
	}
	if dimension == AskUserHistoryShadowArtifacts {
		return append([]byte(nil), snapshot.artifactLegacySHA256[:]...),
			append([]byte(nil), snapshot.artifactCandidateSHA256[:]...), comparable && snapshot.branchID == snapshot.activeBranchID
	}
	legacy := make([]map[string]any, 0, audit.NativeCount+audit.EligibleCount)
	candidate := make([]map[string]any, 0, audit.NativeCount+audit.EligibleCount)
	for _, detail := range audit.Details {
		if detail.Status != AskUserHistoryNativeV1 && detail.Status != AskUserHistoryEligible {
			continue
		}
		if detail.Status == AskUserHistoryEligible {
			toolFrame, toolFound := askUserHistoryEventByID(snapshot.events, detail.ToolEventID)
			resultFrame, resultFound := askUserHistoryEventByID(snapshot.events, detail.ResultEventID)
			if !toolFound || !resultFound {
				continue
			}
			switch dimension {
			case AskUserHistoryShadowStableIDs:
				legacy = append(legacy, map[string]any{
					"call_id":        legacyAskUserCallID(toolFrame.payload, "tool_use"),
					"result_call_id": legacyAskUserCallID(resultFrame.payload, "tool_result"),
				})
				candidate = append(candidate, map[string]any{"call_id": detail.ToolUseID, "result_call_id": detail.ToolUseID})
			case AskUserHistoryShadowBranchOrder:
				legacy = append(legacy, map[string]any{
					"call_id": detail.ToolUseID, "tool_before_result": toolFrame.ordinal < resultFrame.ordinal,
				})
				candidate = append(candidate, map[string]any{"call_id": detail.ToolUseID, "tool_before_result": true})
			case AskUserHistoryShadowStates:
				var result AskUserResultV1
				_ = json.Unmarshal(detail.StateJSON, &result)
				legacy = append(legacy, map[string]any{
					"call_id": detail.ToolUseID, "questions": legacyAskUserQuestions(toolFrame.payload),
					"result": legacyAskUserResultState(resultFrame.payload),
				})
				candidate = append(candidate, map[string]any{
					"call_id": detail.ToolUseID, "questions": legacyAskUserQuestions(toolFrame.payload), "result": result,
				})
			}
			continue
		}
		origin, prompt, pending, found := typedAskUserHistoryEvidence(snapshot.events, detail.ToolUseID)
		if !found {
			continue
		}
		toolFrame, toolFound := askUserHistoryFrameEventByID(snapshot.events, origin.ToolUseFrameEventID)
		resultFrame, resultFound := askUserHistoryFrameEventByID(snapshot.events, origin.PendingFrameEventID)
		switch dimension {
		case AskUserHistoryShadowStableIDs:
			legacy = append(legacy, map[string]any{
				"call_id":               legacyAskUserCallID(toolFrame.payload, "tool_use"),
				"result_call_id":        legacyAskUserCallID(resultFrame.payload, "tool_result"),
				"tool_frame_event_id":   origin.ToolUseFrameEventID,
				"result_frame_event_id": origin.PendingFrameEventID,
			})
			candidate = append(candidate, map[string]any{
				"call_id": detail.ToolUseID, "result_call_id": detail.ToolUseID,
				"tool_frame_event_id":   origin.ToolUseFrameEventID,
				"result_frame_event_id": origin.PendingFrameEventID,
			})
		case AskUserHistoryShadowBranchOrder:
			legacy = append(legacy, map[string]any{
				"call_id": detail.ToolUseID, "tool_before_result": toolFound && resultFound && toolFrame.ordinal < resultFrame.ordinal,
			})
			candidate = append(candidate, map[string]any{"call_id": detail.ToolUseID, "tool_before_result": true})
		case AskUserHistoryShadowStates:
			legacy = append(legacy, map[string]any{
				"call_id":   detail.ToolUseID,
				"questions": legacyAskUserQuestions(toolFrame.payload),
				"result":    legacyAskUserResultState(resultFrame.payload),
			})
			candidate = append(candidate, map[string]any{
				"call_id": detail.ToolUseID, "questions": prompt.Questions, "result": pending.Result,
			})
		}
	}
	legacyJSON, _ := json.Marshal(legacy)
	candidateJSON, _ := json.Marshal(candidate)
	return legacyJSON, candidateJSON, comparable && len(candidate) > 0
}

func typedAskUserHistoryEvidence(
	events []askUserHistoryEvent,
	callID string,
) (AskUserOriginV1, AskUserPromptV1, AskUserResultEventV1, bool) {
	var origin AskUserOriginV1
	var prompt AskUserPromptV1
	var result AskUserResultEventV1
	promptFound, resultFound := false, false
	for _, item := range events {
		switch item.event.Type {
		case AskUserPromptEventType:
			decoded, err := DecodeAskUserPromptV1(item.payload)
			if err == nil && decoded.ToolUseID == callID {
				prompt, origin, promptFound = decoded, decoded.Origin, true
			}
		case AskUserResultEventType:
			decoded, err := DecodeAskUserResultEventV1(item.payload)
			if err == nil && decoded.ToolUseID == callID {
				result, origin, resultFound = decoded, decoded.Origin, true
			}
		}
	}
	return origin, prompt, result, promptFound && resultFound
}

func askUserHistoryFrameEventByID(events []askUserHistoryEvent, frameEventID string) (askUserHistoryEvent, bool) {
	for _, item := range events {
		if item.event.FrameEventID != nil && *item.event.FrameEventID == frameEventID {
			return item, true
		}
	}
	return askUserHistoryEvent{}, false
}

func legacyAskUserCallID(payload []byte, blockType string) string {
	block := legacyAskUserBlock(payload, blockType)
	if blockType == "tool_use" {
		return strings.TrimSpace(webHistoryString(block["id"]))
	}
	return strings.TrimSpace(webHistoryString(block["tool_use_id"]))
}

func legacyAskUserQuestions(payload []byte) any {
	block := legacyAskUserBlock(payload, "tool_use")
	input, _ := block["input"].(map[string]any)
	questions, _ := input["questions"].([]any)
	normalized, err := normalizeAskUserQuestions(questions)
	if err != nil {
		digest := sha256.Sum256(payload)
		return map[string]any{"invalid_payload_sha256": hex.EncodeToString(digest[:])}
	}
	return normalized
}

func legacyAskUserResultState(payload []byte) any {
	block := legacyAskUserBlock(payload, "tool_result")
	content, _ := block["content"].(string)
	state := &legacyAskUserHistoryState{}
	classifyLegacyAskUserResult(content, state)
	if state.structured && !state.malformed && !state.prose {
		if decoded, err := DecodeAskUserResultV1(state.stateJSON); err == nil {
			return decoded
		}
	}
	digest := sha256.Sum256(payload)
	return map[string]any{"invalid_payload_sha256": hex.EncodeToString(digest[:])}
}

func legacyAskUserBlock(payload []byte, blockType string) map[string]any {
	var message map[string]any
	if json.Unmarshal(payload, &message) != nil {
		return nil
	}
	blocks, _ := message["content"].([]any)
	for _, raw := range blocks {
		block, _ := raw.(map[string]any)
		if strings.TrimSpace(webHistoryString(block["type"])) == blockType {
			return block
		}
	}
	return nil
}

func insertAskUserHistoryAuditConn(ctx context.Context, conn *sql.Conn, audit AskUserHistoryAudit) error {
	runID, err := hex.DecodeString(audit.RunID)
	if err != nil || len(runID) != sha256.Size {
		return ErrEventConflict
	}
	sourceDigest, err := hex.DecodeString(audit.SourceSHA256)
	if err != nil || len(sourceDigest) != sha256.Size || len(audit.Comparisons) != len(askUserHistoryShadowDimensions) {
		return ErrEventConflict
	}
	_, err = conn.ExecContext(ctx, `
		INSERT INTO transcript_history_classification_runs(
			run_id,stream_uid,branch_id,branch_generation,contract_version,through_ordinal,
			through_publication_seq,source_sha256,scan_status,scan_reason,candidate_count,
			native_count,eligible_count,poison_count,conflict_count,classified_at
		) VALUES(?,?,?,?,?,?,?,?, 'complete','none',?,?,?,?,?,?)`,
		runID, audit.StreamUID, audit.BranchID, audit.BranchGeneration, audit.ContractVersion,
		audit.ThroughOrdinal, audit.ThroughPublicationSequence, sourceDigest, audit.CandidateCount,
		audit.NativeCount, audit.EligibleCount, audit.PoisonCount, audit.ConflictCount, audit.ClassifiedAt,
	)
	if err != nil {
		return err
	}
	for _, detail := range audit.Details {
		candidateID, err := hex.DecodeString(detail.CandidateID)
		if err != nil || len(candidateID) != sha256.Size {
			return ErrEventConflict
		}
		evidenceDigest, err := hex.DecodeString(detail.EvidenceSHA256)
		if err != nil || len(evidenceDigest) != sha256.Size {
			return ErrEventConflict
		}
		var runnerAttempt any
		if detail.RunnerAttempt > 0 {
			runnerAttempt = detail.RunnerAttempt
		}
		var toolEventID any
		if detail.ToolEventID > 0 {
			toolEventID = detail.ToolEventID
		}
		var resultEventID any
		if detail.ResultEventID > 0 {
			resultEventID = detail.ResultEventID
		}
		var stateJSON any
		if len(detail.StateJSON) > 0 {
			stateJSON = detail.StateJSON
		}
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO transcript_history_classification_candidates(
				run_id,candidate_id,first_ordinal,tool_use_id,tool_event_id,result_event_id,runner_attempt,disposition,
				reason_code,state_json,evidence_json,evidence_sha256
			) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			runID, candidateID, detail.FirstOrdinal, detail.ToolUseID, toolEventID, resultEventID,
			runnerAttempt, string(detail.Status),
			string(detail.ReasonCode), stateJSON, detail.EvidenceJSON, evidenceDigest,
		); err != nil {
			return err
		}
	}
	for _, comparison := range audit.Comparisons {
		legacyDigest, err := hex.DecodeString(comparison.LegacySHA256)
		if err != nil || len(legacyDigest) != sha256.Size {
			return ErrEventConflict
		}
		var candidateDigest any
		if comparison.CandidateSHA256 != "" {
			decoded, err := hex.DecodeString(comparison.CandidateSHA256)
			if err != nil || len(decoded) != sha256.Size {
				return ErrEventConflict
			}
			candidateDigest = decoded
		}
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO transcript_history_shadow_comparisons(
				run_id,dimension,verdict,legacy_sha256,candidate_sha256,reason_code,evidence_json,compared_at
			) VALUES(?,?,?,?,?,?,?,?)`,
			runID, string(comparison.Dimension), string(comparison.Verdict), legacyDigest, candidateDigest,
			comparison.ReasonCode, comparison.EvidenceJSON, comparison.ComparedAt,
		); err != nil {
			return err
		}
	}
	return nil
}

func getAskUserHistoryAuditConn(
	ctx context.Context,
	conn *sql.Conn,
	input GetAskUserHistoryAuditInput,
) (AskUserHistoryAudit, bool, error) {
	return loadAskUserHistoryAudit(ctx, conn, input)
}

func getAskUserHistoryAuditTx(
	ctx context.Context,
	tx *sql.Tx,
	input GetAskUserHistoryAuditInput,
) (AskUserHistoryAudit, bool, error) {
	return loadAskUserHistoryAudit(ctx, tx, input)
}

func loadAskUserHistoryAudit(
	ctx context.Context,
	query askUserHistoryQueryer,
	input GetAskUserHistoryAuditInput,
) (AskUserHistoryAudit, bool, error) {
	runID, err := hex.DecodeString(input.RunID)
	if err != nil || len(runID) != sha256.Size {
		return AskUserHistoryAudit{}, false, ErrEventConflict
	}
	var audit AskUserHistoryAudit
	var sourceDigest []byte
	var scanStatus, scanReason string
	err = query.QueryRowContext(ctx, `
		SELECT run.stream_uid,stream.owner_id,run.branch_id,run.branch_generation,run.contract_version,
			run.through_ordinal,run.through_publication_seq,run.source_sha256,run.scan_status,run.scan_reason,
			run.candidate_count,run.native_count,run.eligible_count,run.poison_count,run.conflict_count,run.classified_at
		FROM transcript_history_classification_runs run
		JOIN transcript_streams stream ON stream.stream_uid=run.stream_uid
		WHERE run.run_id=? AND run.stream_uid=?`, runID, input.StreamUID,
	).Scan(
		&audit.StreamUID, &audit.OwnerID, &audit.BranchID, &audit.BranchGeneration, &audit.ContractVersion,
		&audit.ThroughOrdinal, &audit.ThroughPublicationSequence, &sourceDigest, &scanStatus, &scanReason,
		&audit.CandidateCount, &audit.NativeCount, &audit.EligibleCount, &audit.PoisonCount, &audit.ConflictCount,
		&audit.ClassifiedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return AskUserHistoryAudit{}, false, nil
	}
	if err != nil {
		return AskUserHistoryAudit{}, false, err
	}
	if audit.OwnerID != input.OwnerID || scanStatus != "complete" || scanReason != "none" || len(sourceDigest) != sha256.Size {
		if audit.OwnerID != input.OwnerID {
			return AskUserHistoryAudit{}, false, ErrOwnerMismatch
		}
		return AskUserHistoryAudit{}, false, ErrEventConflict
	}
	audit.RunID = input.RunID
	audit.SourceSHA256 = hex.EncodeToString(sourceDigest)
	rows, err := query.QueryContext(ctx, `
		SELECT candidate_id,first_ordinal,tool_use_id,tool_event_id,result_event_id,runner_attempt,disposition,reason_code,
			state_json,evidence_json,evidence_sha256
		FROM transcript_history_classification_candidates WHERE run_id=?
		ORDER BY first_ordinal,candidate_id`, runID)
	if err != nil {
		return AskUserHistoryAudit{}, false, err
	}
	for rows.Next() {
		var detail AskUserHistoryAuditDetail
		var candidateID, evidenceDigest []byte
		var toolEventID, resultEventID, runnerAttempt sql.NullInt64
		var stateJSON []byte
		var disposition, reason string
		if err := rows.Scan(
			&candidateID, &detail.FirstOrdinal, &detail.ToolUseID, &toolEventID, &resultEventID,
			&runnerAttempt, &disposition,
			&reason, &stateJSON, &detail.EvidenceJSON, &evidenceDigest,
		); err != nil {
			_ = rows.Close()
			return AskUserHistoryAudit{}, false, err
		}
		detail.CandidateID = hex.EncodeToString(candidateID)
		detail.Status = AskUserHistoryStatus(disposition)
		detail.ReasonCode = AskUserHistoryReasonCode(reason)
		detail.StateJSON = append([]byte(nil), stateJSON...)
		detail.EvidenceSHA256 = hex.EncodeToString(evidenceDigest)
		if runnerAttempt.Valid {
			detail.RunnerAttempt = runnerAttempt.Int64
		}
		if toolEventID.Valid {
			detail.ToolEventID = toolEventID.Int64
		}
		if resultEventID.Valid {
			detail.ResultEventID = resultEventID.Int64
		}
		audit.Details = append(audit.Details, detail)
	}
	if err := rows.Close(); err != nil {
		return AskUserHistoryAudit{}, false, err
	}
	comparisonRows, err := query.QueryContext(ctx, `
		SELECT dimension,verdict,legacy_sha256,candidate_sha256,reason_code,evidence_json,compared_at
		FROM transcript_history_shadow_comparisons WHERE run_id=?
		ORDER BY CASE dimension
			WHEN 'stable_ids' THEN 1 WHEN 'branch_order' THEN 2 WHEN 'ask_user_states' THEN 3
			WHEN 'terminal_facts' THEN 4 WHEN 'artifact_refs' THEN 5 ELSE 6 END`, runID)
	if err != nil {
		return AskUserHistoryAudit{}, false, err
	}
	for comparisonRows.Next() {
		var comparison AskUserHistoryShadowComparison
		var legacyDigest []byte
		var candidateDigest []byte
		var dimension, verdict string
		if err := comparisonRows.Scan(
			&dimension, &verdict, &legacyDigest, &candidateDigest, &comparison.ReasonCode,
			&comparison.EvidenceJSON, &comparison.ComparedAt,
		); err != nil {
			_ = comparisonRows.Close()
			return AskUserHistoryAudit{}, false, err
		}
		comparison.Dimension = AskUserHistoryShadowDimension(dimension)
		comparison.Verdict = AskUserHistoryShadowVerdict(verdict)
		comparison.LegacySHA256 = hex.EncodeToString(legacyDigest)
		if len(candidateDigest) > 0 {
			comparison.CandidateSHA256 = hex.EncodeToString(candidateDigest)
		}
		audit.Comparisons = append(audit.Comparisons, comparison)
	}
	if err := comparisonRows.Close(); err != nil {
		return AskUserHistoryAudit{}, false, err
	}
	deriveAskUserHistoryOutcome(&audit)
	if err := validateLoadedAskUserHistoryAudit(audit, sourceDigest); err != nil {
		return AskUserHistoryAudit{}, false, ErrEventConflict
	}
	return audit, true, nil
}

func validateLoadedAskUserHistoryAudit(audit AskUserHistoryAudit, sourceDigest []byte) error {
	if audit.CandidateCount != len(audit.Details) || len(audit.Comparisons) != len(askUserHistoryShadowDimensions) ||
		len(sourceDigest) != sha256.Size {
		return ErrEventConflict
	}
	var source [sha256.Size]byte
	copy(source[:], sourceDigest)
	if askUserHistoryRunID(audit, source) != audit.RunID {
		return ErrEventConflict
	}
	native, eligible, poison, conflict := 0, 0, 0, 0
	for _, detail := range audit.Details {
		if len(detail.CandidateID) != 64 || detail.CandidateID != askUserHistoryCandidateID(audit.RunID, detail) ||
			len(detail.EvidenceJSON) == 0 || !json.Valid(detail.EvidenceJSON) {
			return ErrEventConflict
		}
		evidenceDigest := sha256.Sum256(detail.EvidenceJSON)
		if detail.EvidenceSHA256 != hex.EncodeToString(evidenceDigest[:]) {
			return ErrEventConflict
		}
		if len(detail.StateJSON) > 0 && !json.Valid(detail.StateJSON) {
			return ErrEventConflict
		}
		switch detail.Status {
		case AskUserHistoryNativeV1:
			native++
		case AskUserHistoryEligible:
			eligible++
		case AskUserHistoryQuarantined:
			poison++
			if len(detail.StateJSON) != 0 {
				return ErrEventConflict
			}
		case AskUserHistoryConflict:
			conflict++
			if len(detail.StateJSON) != 0 {
				return ErrEventConflict
			}
		default:
			return ErrEventConflict
		}
	}
	if native != audit.NativeCount || eligible != audit.EligibleCount || poison != audit.PoisonCount ||
		conflict != audit.ConflictCount {
		return ErrEventConflict
	}
	for index, comparison := range audit.Comparisons {
		if comparison.Dimension != askUserHistoryShadowDimensions[index] || len(comparison.LegacySHA256) != 64 ||
			len(comparison.EvidenceJSON) == 0 || !json.Valid(comparison.EvidenceJSON) {
			return ErrEventConflict
		}
		switch comparison.Verdict {
		case AskUserHistoryShadowBlocked:
			if comparison.CandidateSHA256 != "" {
				return ErrEventConflict
			}
		case AskUserHistoryShadowMatch, AskUserHistoryShadowMismatch:
			if len(comparison.CandidateSHA256) != 64 {
				return ErrEventConflict
			}
		default:
			return ErrEventConflict
		}
	}
	return nil
}

func deriveAskUserHistoryOutcome(audit *AskUserHistoryAudit) {
	audit.Status, audit.ReasonCode = AskUserHistoryNotApplicable, AskUserHistoryReasonNone
	for _, detail := range audit.Details {
		audit.Status, audit.ReasonCode = mergeAskUserHistoryOutcome(
			audit.Status, audit.ReasonCode, detail.Status, detail.ReasonCode,
		)
	}
}

func askUserHistoryAuditsEqual(left, right AskUserHistoryAudit) bool {
	left.Comparisons = append([]AskUserHistoryShadowComparison(nil), left.Comparisons...)
	right.Comparisons = append([]AskUserHistoryShadowComparison(nil), right.Comparisons...)
	left.Details = append([]AskUserHistoryAuditDetail(nil), left.Details...)
	right.Details = append([]AskUserHistoryAuditDetail(nil), right.Details...)
	if len(left.Comparisons) == 0 {
		left.Comparisons = nil
	}
	if len(right.Comparisons) == 0 {
		right.Comparisons = nil
	}
	if len(left.Details) == 0 {
		left.Details = nil
	}
	if len(right.Details) == 0 {
		right.Details = nil
	}
	left.ClassifiedAt, right.ClassifiedAt = time.Time{}, time.Time{}
	for index := range left.Comparisons {
		left.Comparisons[index].ComparedAt = time.Time{}
	}
	for index := range right.Comparisons {
		right.Comparisons[index].ComparedAt = time.Time{}
	}
	return reflect.DeepEqual(left, right)
}
