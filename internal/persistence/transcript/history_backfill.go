package transcript

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

var ErrHistoryBackfillBlocked = errors.New("transcript history backfill is blocked")

type StageAskUserHistoryBackfillInput struct {
	RunID         string
	StreamUID     string
	OwnerID       string
	BranchID      string
	MaxEvents     int
	MaxCandidates int
	MaxShadowRows int
}

type AskUserHistoryBackfillCandidate struct {
	CandidateID             string
	CandidateOrdinal        int
	ToolUseID               string
	RunnerAttempt           int64
	ToolEventID             int64
	ResultEventID           int64
	ToolFrameEventID        string
	ResultFrameEventID      string
	ToolOrdinal             int64
	ResultOrdinal           int64
	PromptClientMessageID   string
	PendingClientMessageID  string
	TerminalClientMessageID string
	StableMessageID         string
	PromptJSON              []byte
	PendingJSON             []byte
	ResultJSON              []byte
	PayloadSHA256           string
	EvidenceSHA256          string
}

type AskUserHistoryBackfill struct {
	BackfillID          string
	ClassificationRunID string
	StreamUID           string
	OwnerID             string
	BranchID            string
	BranchGeneration    int64
	ContractVersion     int
	SourceSHA256        string
	StagingSHA256       string
	Candidates          []AskUserHistoryBackfillCandidate
	CreatedAt           time.Time
}

func (r *Repository) StageAskUserHistoryBackfill(
	ctx context.Context,
	input StageAskUserHistoryBackfillInput,
) (AskUserHistoryBackfill, bool, error) {
	if r == nil || r.db == nil {
		return AskUserHistoryBackfill{}, false, ErrSchemaUnavailable
	}
	input.RunID = strings.TrimSpace(input.RunID)
	input.StreamUID = strings.TrimSpace(input.StreamUID)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.BranchID = strings.TrimSpace(input.BranchID)
	if len(input.RunID) != sha256.Size*2 || input.StreamUID == "" || input.OwnerID == "" ||
		(input.BranchID != "" && !validTranscriptBranchID(input.BranchID)) ||
		input.MaxEvents <= 0 || input.MaxCandidates <= 0 || input.MaxShadowRows <= 0 {
		return AskUserHistoryBackfill{}, false, errors.New("complete history run authority and positive resource budgets are required")
	}
	var result AskUserHistoryBackfill
	created := false
	err := r.withImmediate(ctx, func(conn *sql.Conn) error {
		audit, found, err := getAskUserHistoryAuditConn(ctx, conn, GetAskUserHistoryAuditInput{
			RunID: input.RunID, StreamUID: input.StreamUID, OwnerID: input.OwnerID,
		})
		if err != nil {
			return err
		}
		if !found || (input.BranchID != "" && audit.BranchID != input.BranchID) {
			return ErrHistoryBackfillBlocked
		}
		latestRunID, err := latestAskUserHistoryRunIDConn(ctx, conn, audit.StreamUID, audit.BranchID)
		if err != nil {
			return err
		}
		if latestRunID != audit.RunID {
			return ErrBranchStateStale
		}
		snapshot, err := loadAskUserHistorySnapshot(ctx, conn, AuditAskUserHistoryInput{
			StreamUID: audit.StreamUID, OwnerID: audit.OwnerID, BranchID: audit.BranchID,
			MaxEvents: input.MaxEvents, MaxCandidates: input.MaxCandidates, MaxShadowRows: input.MaxShadowRows,
		})
		if err != nil {
			return err
		}
		current, err := classifyAskUserHistoryEvents(
			snapshot.stream, snapshot.branchID, snapshot.generation, snapshot.branch, snapshot.events,
			input.MaxCandidates, audit.ClassifiedAt,
		)
		if err != nil {
			return err
		}
		finalizeAskUserHistoryAudit(&current, snapshot)
		if !askUserHistoryAuditsEqual(audit, current) {
			return ErrBranchStateStale
		}
		if err := validateAskUserHistoryBackfillGate(ctx, conn, audit); err != nil {
			return err
		}
		candidates, err := buildAskUserHistoryBackfillCandidates(snapshot, audit, input.MaxCandidates)
		if err != nil {
			return err
		}
		result, err = newAskUserHistoryBackfill(audit, candidates, r.now().UTC())
		if err != nil {
			return err
		}
		existing, found, err := validateStoredAskUserHistoryBackfillConn(ctx, conn, result)
		if err != nil {
			return err
		}
		if found {
			result.CreatedAt = existing
			return nil
		}
		if err := insertAskUserHistoryBackfillConn(ctx, conn, result); err != nil {
			return err
		}
		created = true
		return nil
	})
	return result, created, schemaError(err)
}

func latestAskUserHistoryRunIDConn(ctx context.Context, conn *sql.Conn, streamUID, branchID string) (string, error) {
	var runID []byte
	if err := conn.QueryRowContext(ctx, `SELECT run_id FROM transcript_history_classification_runs
		WHERE stream_uid=? AND branch_id=? AND contract_version=1
		ORDER BY classified_at DESC,run_id DESC LIMIT 1`, streamUID, branchID).Scan(&runID); err != nil {
		return "", err
	}
	if len(runID) != sha256.Size {
		return "", ErrEventConflict
	}
	return hex.EncodeToString(runID), nil
}

func validateAskUserHistoryBackfillGate(ctx context.Context, conn *sql.Conn, audit AskUserHistoryAudit) error {
	if audit.ContractVersion != askUserHistoryContractVersion || audit.EligibleCount <= 0 ||
		audit.PoisonCount != 0 || audit.ConflictCount != 0 || audit.Status != AskUserHistoryEligible {
		return ErrHistoryBackfillBlocked
	}
	for _, detail := range audit.Details {
		if detail.Status != AskUserHistoryEligible && detail.Status != AskUserHistoryNativeV1 {
			return ErrHistoryBackfillBlocked
		}
	}
	if len(audit.Comparisons) != len(askUserHistoryShadowDimensions) {
		return ErrHistoryBackfillBlocked
	}
	for index, dimension := range askUserHistoryShadowDimensions {
		comparison := audit.Comparisons[index]
		if comparison.Dimension != dimension || comparison.Verdict != AskUserHistoryShadowMatch {
			return ErrHistoryBackfillBlocked
		}
	}
	var running int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcript_runner_attempts
		WHERE stream_uid=? AND status='running'`, audit.StreamUID).Scan(&running); err != nil {
		return err
	}
	if running != 0 {
		return ErrHistoryBackfillBlocked
	}
	var unresolvedArtifacts int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM transcript_artifact_refs ref
		JOIN transcript_streams stream ON stream.stream_uid=ref.stream_uid
		JOIN transcript_branch_events membership
			ON membership.stream_uid=ref.stream_uid AND membership.event_id=ref.source_event_id
		LEFT JOIN artifacts artifact ON artifact.id=ref.artifact_id AND artifact.project_id=stream.project_id
		LEFT JOIN artifact_versions version
			ON version.id=ref.version_id AND version.artifact_id=ref.artifact_id
		WHERE ref.stream_uid=? AND membership.branch_id=? AND (
			ref.availability='missing' OR
			(ref.availability!='deleted' AND (artifact.id IS NULL OR version.id IS NULL))
		)`, audit.StreamUID, audit.BranchID).Scan(&unresolvedArtifacts); err != nil {
		return err
	}
	if unresolvedArtifacts != 0 {
		return ErrHistoryBackfillBlocked
	}
	return nil
}

func buildAskUserHistoryBackfillCandidates(
	snapshot askUserHistorySnapshot,
	audit AskUserHistoryAudit,
	maxCandidates int,
) ([]AskUserHistoryBackfillCandidate, error) {
	budget := &askUserHistoryCandidateBudget{limit: maxCandidates}
	_, _, ignoredFrameEvents, typedReason, err := collectTypedAskUserHistory(snapshot.stream, snapshot.events, budget)
	if err != nil {
		return nil, err
	}
	if typedReason != AskUserHistoryReasonNone {
		return nil, ErrHistoryBackfillBlocked
	}
	legacy, _, err := collectLegacyAskUserHistory(snapshot.events, ignoredFrameEvents, budget)
	if err != nil {
		return nil, err
	}
	result := make([]AskUserHistoryBackfillCandidate, 0, audit.EligibleCount)
	for _, detail := range audit.Details {
		if detail.Status != AskUserHistoryEligible {
			continue
		}
		state := legacy[detail.ToolUseID]
		if state == nil || state.toolEventID != detail.ToolEventID || state.resultEventID != detail.ResultEventID ||
			state.toolAttempt != detail.RunnerAttempt || state.resultAttempt != detail.RunnerAttempt ||
			!state.questionsOK || !state.structured || state.prose || state.malformed || state.duplicate ||
			state.resultStatus != AskUserStatusAnswered {
			return nil, ErrHistoryBackfillBlocked
		}
		resultState, err := DecodeAskUserResultV1(state.stateJSON)
		if err != nil || resultState.Status != AskUserStatusAnswered {
			return nil, ErrHistoryBackfillBlocked
		}
		toolEvent, toolFound := askUserHistoryEventByID(snapshot.events, state.toolEventID)
		resultEvent, resultFound := askUserHistoryEventByID(snapshot.events, state.resultEventID)
		if !toolFound || !resultFound || toolEvent.event.FrameEventID == nil || resultEvent.event.FrameEventID == nil {
			return nil, ErrHistoryBackfillBlocked
		}
		promptClientID := "history-backfill:" + detail.CandidateID + ":prompt"
		pendingClientID := "history-backfill:" + detail.CandidateID + ":pending"
		origin := AskUserOriginV1{
			Version: AskUserPayloadVersion, StreamUID: snapshot.stream.UID, Epoch: snapshot.stream.Epoch,
			FrameID: snapshot.stream.FrameID, BranchID: snapshot.branchID, BranchGeneration: snapshot.generation,
			RunnerAttempt: detail.RunnerAttempt, ToolUseID: detail.ToolUseID,
			ToolUseFrameEventID: *toolEvent.event.FrameEventID, PendingFrameEventID: *resultEvent.event.FrameEventID,
			PromptClientMessageID: promptClientID, PendingClientMessageID: pendingClientID,
		}
		terminalClientID, err := AskUserResultClientMessageIDV1(origin)
		if err != nil {
			return nil, ErrHistoryBackfillBlocked
		}
		promptJSON, err := json.Marshal(AskUserPromptV1{
			Version: AskUserPayloadVersion, Origin: origin, ToolUseID: detail.ToolUseID,
			ToolName: "ask_user", Questions: state.questions,
		})
		if err != nil {
			return nil, ErrEventConflict
		}
		if _, err := DecodeAskUserPromptV1(promptJSON); err != nil {
			return nil, ErrHistoryBackfillBlocked
		}
		pendingJSON, err := json.Marshal(AskUserResultEventV1{
			Version: AskUserPayloadVersion, Origin: origin, ToolUseID: detail.ToolUseID,
			Result: NewAskUserPendingResultV1(),
		})
		if err != nil {
			return nil, ErrEventConflict
		}
		if pending, err := DecodeAskUserResultEventV1(pendingJSON); err != nil ||
			pending.Result.Status != AskUserStatusAwaitingResponse || pending.ModelContinuation != "" {
			return nil, ErrHistoryBackfillBlocked
		}
		modelContinuation := strings.TrimSpace(state.modelContinuation)
		if modelContinuation == "" || modelContinuation != state.modelContinuation {
			return nil, ErrHistoryBackfillBlocked
		}
		resultJSON, err := json.Marshal(AskUserResultEventV1{
			Version: AskUserPayloadVersion, Origin: origin, ToolUseID: detail.ToolUseID, Result: resultState,
			ModelContinuation: modelContinuation,
		})
		if err != nil {
			return nil, ErrEventConflict
		}
		if _, err := DecodeAskUserResultEventV1(resultJSON); err != nil {
			return nil, ErrHistoryBackfillBlocked
		}
		payloadDigest := sha256.New()
		writeHistoryDigestField(payloadDigest, []byte(promptClientID))
		writeHistoryDigestField(payloadDigest, []byte(pendingClientID))
		writeHistoryDigestField(payloadDigest, []byte(terminalClientID))
		writeHistoryDigestField(payloadDigest, []byte(*toolEvent.event.FrameEventID))
		writeHistoryDigestField(payloadDigest, []byte(*resultEvent.event.FrameEventID))
		writeHistoryDigestField(payloadDigest, promptJSON)
		writeHistoryDigestField(payloadDigest, pendingJSON)
		writeHistoryDigestField(payloadDigest, resultJSON)
		result = append(result, AskUserHistoryBackfillCandidate{
			CandidateID: detail.CandidateID, CandidateOrdinal: len(result) + 1, ToolUseID: detail.ToolUseID,
			RunnerAttempt: detail.RunnerAttempt, ToolEventID: detail.ToolEventID, ResultEventID: detail.ResultEventID,
			ToolFrameEventID: *toolEvent.event.FrameEventID, ResultFrameEventID: *resultEvent.event.FrameEventID,
			ToolOrdinal: state.toolOrdinal, ResultOrdinal: state.resultOrdinal,
			PromptClientMessageID: promptClientID, PendingClientMessageID: pendingClientID,
			TerminalClientMessageID: terminalClientID, StableMessageID: promptClientID,
			PromptJSON: promptJSON, PendingJSON: pendingJSON, ResultJSON: resultJSON,
			PayloadSHA256: hex.EncodeToString(payloadDigest.Sum(nil)), EvidenceSHA256: detail.EvidenceSHA256,
		})
	}
	if len(result) != audit.EligibleCount || len(result) == 0 {
		return nil, ErrHistoryBackfillBlocked
	}
	return result, nil
}

func newAskUserHistoryBackfill(
	audit AskUserHistoryAudit,
	candidates []AskUserHistoryBackfillCandidate,
	now time.Time,
) (AskUserHistoryBackfill, error) {
	stagingDigest := askUserHistoryBackfillDigest(audit.RunID, candidates)
	backfillDigest := sha256.New()
	writeHistoryDigestField(backfillDigest, []byte(HistoryBackfillContractID))
	writeHistoryDigestField(backfillDigest, []byte(audit.RunID))
	writeHistoryDigestField(backfillDigest, stagingDigest[:])
	return AskUserHistoryBackfill{
		BackfillID: hex.EncodeToString(backfillDigest.Sum(nil)), ClassificationRunID: audit.RunID,
		StreamUID: audit.StreamUID, OwnerID: audit.OwnerID, BranchID: audit.BranchID,
		BranchGeneration: audit.BranchGeneration, ContractVersion: askUserHistoryContractVersion,
		SourceSHA256: audit.SourceSHA256, StagingSHA256: hex.EncodeToString(stagingDigest[:]),
		Candidates: candidates, CreatedAt: now,
	}, nil
}

func askUserHistoryBackfillDigest(runID string, candidates []AskUserHistoryBackfillCandidate) [sha256.Size]byte {
	digest := sha256.New()
	writeHistoryDigestField(digest, []byte(HistoryBackfillContractID))
	writeHistoryDigestField(digest, []byte(runID))
	for _, candidate := range candidates {
		writeHistoryDigestField(digest, []byte(candidate.CandidateID))
		writeHistoryDigestField(digest, []byte(candidate.PayloadSHA256))
		writeHistoryDigestField(digest, []byte(candidate.EvidenceSHA256))
		writeHistoryDigestField(digest, []byte(candidate.StableMessageID))
	}
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result
}

func validateStoredAskUserHistoryBackfillConn(
	ctx context.Context,
	conn *sql.Conn,
	expected AskUserHistoryBackfill,
) (time.Time, bool, error) {
	runID, err := hex.DecodeString(expected.ClassificationRunID)
	if err != nil || len(runID) != sha256.Size {
		return time.Time{}, false, ErrEventConflict
	}
	var backfillID, sourceDigest, stagingDigest []byte
	var streamUID, branchID string
	var generation int64
	var contractVersion, candidateCount int
	var createdAt time.Time
	err = conn.QueryRowContext(ctx, `SELECT backfill_id,stream_uid,branch_id,branch_generation,contract_version,
		source_sha256,staging_sha256,candidate_count,created_at
		FROM transcript_history_backfill_runs WHERE classification_run_id=?`, runID).Scan(
		&backfillID, &streamUID, &branchID, &generation, &contractVersion, &sourceDigest, &stagingDigest,
		&candidateCount, &createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	if hex.EncodeToString(backfillID) != expected.BackfillID || streamUID != expected.StreamUID ||
		branchID != expected.BranchID || generation != expected.BranchGeneration ||
		contractVersion != expected.ContractVersion || hex.EncodeToString(sourceDigest) != expected.SourceSHA256 ||
		hex.EncodeToString(stagingDigest) != expected.StagingSHA256 || candidateCount != len(expected.Candidates) {
		return time.Time{}, false, ErrEventConflict
	}
	rows, err := conn.QueryContext(ctx, `SELECT candidate_id,candidate_ordinal,tool_use_id,runner_attempt,
		tool_event_id,result_event_id,tool_frame_event_id,result_frame_event_id,prompt_client_message_id,
		pending_client_message_id,terminal_client_message_id,prompt_json,pending_json,result_json,
		payload_sha256,evidence_sha256
		FROM transcript_history_backfill_candidates WHERE backfill_id=? ORDER BY candidate_ordinal`, backfillID)
	if err != nil {
		return time.Time{}, false, err
	}
	index := 0
	for rows.Next() {
		if index >= len(expected.Candidates) {
			_ = rows.Close()
			return time.Time{}, false, ErrEventConflict
		}
		want := expected.Candidates[index]
		var candidateID, promptJSON, pendingJSON, resultJSON, payloadDigest, evidenceDigest []byte
		var ordinal int
		var toolUseID, toolFrameEventID, resultFrameEventID string
		var promptClientID, pendingClientID, terminalClientID string
		var attempt, toolEventID, resultEventID int64
		if err := rows.Scan(&candidateID, &ordinal, &toolUseID, &attempt, &toolEventID, &resultEventID,
			&toolFrameEventID, &resultFrameEventID, &promptClientID, &pendingClientID, &terminalClientID,
			&promptJSON, &pendingJSON, &resultJSON, &payloadDigest, &evidenceDigest); err != nil {
			_ = rows.Close()
			return time.Time{}, false, err
		}
		if hex.EncodeToString(candidateID) != want.CandidateID || ordinal != want.CandidateOrdinal ||
			toolUseID != want.ToolUseID || attempt != want.RunnerAttempt || toolEventID != want.ToolEventID ||
			resultEventID != want.ResultEventID || toolFrameEventID != want.ToolFrameEventID ||
			resultFrameEventID != want.ResultFrameEventID || promptClientID != want.PromptClientMessageID ||
			pendingClientID != want.PendingClientMessageID || terminalClientID != want.TerminalClientMessageID ||
			!bytes.Equal(promptJSON, want.PromptJSON) || !bytes.Equal(pendingJSON, want.PendingJSON) ||
			!bytes.Equal(resultJSON, want.ResultJSON) || hex.EncodeToString(payloadDigest) != want.PayloadSHA256 ||
			hex.EncodeToString(evidenceDigest) != want.EvidenceSHA256 {
			_ = rows.Close()
			return time.Time{}, false, ErrEventConflict
		}
		index++
	}
	if err := rows.Close(); err != nil {
		return time.Time{}, false, err
	}
	if err := rows.Err(); err != nil || index != len(expected.Candidates) {
		return time.Time{}, false, ErrEventConflict
	}
	wantCursors := map[int64]string{}
	for _, candidate := range expected.Candidates {
		wantCursors[candidate.ToolOrdinal] = candidate.CandidateID + "\x00" + candidate.StableMessageID
		wantCursors[candidate.ResultOrdinal] = candidate.CandidateID + "\x00" + candidate.StableMessageID
	}
	cursorRows, err := conn.QueryContext(ctx, `SELECT candidate_id,legacy_ordinal,stable_message_id
		FROM transcript_history_backfill_cursor_map WHERE backfill_id=? ORDER BY legacy_ordinal`, backfillID)
	if err != nil {
		return time.Time{}, false, err
	}
	seenCursors := 0
	for cursorRows.Next() {
		var candidateID []byte
		var ordinal int64
		var stableMessageID string
		if err := cursorRows.Scan(&candidateID, &ordinal, &stableMessageID); err != nil {
			_ = cursorRows.Close()
			return time.Time{}, false, err
		}
		if wantCursors[ordinal] != hex.EncodeToString(candidateID)+"\x00"+stableMessageID {
			_ = cursorRows.Close()
			return time.Time{}, false, ErrEventConflict
		}
		seenCursors++
	}
	if err := cursorRows.Close(); err != nil {
		return time.Time{}, false, err
	}
	if err := cursorRows.Err(); err != nil || seenCursors != len(wantCursors) {
		return time.Time{}, false, ErrEventConflict
	}
	return createdAt, true, nil
}

func insertAskUserHistoryBackfillConn(ctx context.Context, conn *sql.Conn, backfill AskUserHistoryBackfill) error {
	backfillID, err := hex.DecodeString(backfill.BackfillID)
	if err != nil || len(backfillID) != sha256.Size {
		return ErrEventConflict
	}
	runID, err := hex.DecodeString(backfill.ClassificationRunID)
	if err != nil || len(runID) != sha256.Size {
		return ErrEventConflict
	}
	sourceDigest, err := hex.DecodeString(backfill.SourceSHA256)
	if err != nil || len(sourceDigest) != sha256.Size {
		return ErrEventConflict
	}
	stagingDigest, err := hex.DecodeString(backfill.StagingSHA256)
	if err != nil || len(stagingDigest) != sha256.Size {
		return ErrEventConflict
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_history_backfill_runs(
		backfill_id,classification_run_id,stream_uid,branch_id,branch_generation,contract_version,
		source_sha256,staging_sha256,candidate_count,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		backfillID, runID, backfill.StreamUID, backfill.BranchID, backfill.BranchGeneration,
		backfill.ContractVersion, sourceDigest, stagingDigest, len(backfill.Candidates), backfill.CreatedAt); err != nil {
		return err
	}
	for _, candidate := range backfill.Candidates {
		candidateID, err := hex.DecodeString(candidate.CandidateID)
		if err != nil || len(candidateID) != sha256.Size {
			return ErrEventConflict
		}
		payloadDigest, err := hex.DecodeString(candidate.PayloadSHA256)
		if err != nil || len(payloadDigest) != sha256.Size {
			return ErrEventConflict
		}
		evidenceDigest, err := hex.DecodeString(candidate.EvidenceSHA256)
		if err != nil || len(evidenceDigest) != sha256.Size {
			return ErrEventConflict
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_history_backfill_candidates(
			backfill_id,classification_run_id,candidate_id,candidate_ordinal,tool_use_id,runner_attempt,
			tool_event_id,result_event_id,tool_frame_event_id,result_frame_event_id,prompt_client_message_id,
			pending_client_message_id,terminal_client_message_id,prompt_json,pending_json,result_json,
			payload_sha256,evidence_sha256,created_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, backfillID, runID, candidateID, candidate.CandidateOrdinal,
			candidate.ToolUseID, candidate.RunnerAttempt, candidate.ToolEventID, candidate.ResultEventID,
			candidate.ToolFrameEventID, candidate.ResultFrameEventID, candidate.PromptClientMessageID,
			candidate.PendingClientMessageID, candidate.TerminalClientMessageID, candidate.PromptJSON,
			candidate.PendingJSON, candidate.ResultJSON, payloadDigest,
			evidenceDigest, backfill.CreatedAt); err != nil {
			return err
		}
		ordinals := []int64{candidate.ToolOrdinal}
		if candidate.ResultOrdinal != candidate.ToolOrdinal {
			ordinals = append(ordinals, candidate.ResultOrdinal)
		}
		sort.Slice(ordinals, func(left, right int) bool { return ordinals[left] < ordinals[right] })
		for _, ordinal := range ordinals {
			if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_history_backfill_cursor_map(
				backfill_id,candidate_id,legacy_ordinal,stable_message_id,created_at) VALUES(?,?,?,?,?)`,
				backfillID, candidateID, ordinal, candidate.StableMessageID, backfill.CreatedAt); err != nil {
				return err
			}
		}
	}
	return nil
}
