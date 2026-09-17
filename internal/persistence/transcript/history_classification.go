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
	"hash"
	"sort"
	"strings"
	"time"
)

const askUserHistoryContractVersion = 1

var ErrHistoryAuditBudgetExceeded = errors.New("transcript history audit resource budget exceeded")

type AskUserHistoryStatus string

const (
	AskUserHistoryNotApplicable AskUserHistoryStatus = "not_applicable"
	AskUserHistoryNativeV1      AskUserHistoryStatus = "native_v1"
	AskUserHistoryEligible      AskUserHistoryStatus = "eligible"
	AskUserHistoryQuarantined   AskUserHistoryStatus = "poison"
	AskUserHistoryConflict      AskUserHistoryStatus = "conflict"
)

type AskUserHistoryReasonCode string

const (
	AskUserHistoryReasonNone                 AskUserHistoryReasonCode = "none"
	AskUserHistoryReasonNativeV1             AskUserHistoryReasonCode = "native_v1"
	AskUserHistoryReasonStructuredLegacy     AskUserHistoryReasonCode = "structured_legacy"
	AskUserHistoryReasonMissingRunnerAttempt AskUserHistoryReasonCode = "missing_runner_attempt"
	AskUserHistoryReasonAmbiguousProse       AskUserHistoryReasonCode = "ambiguous_prose"
	AskUserHistoryReasonInvalidQuestions     AskUserHistoryReasonCode = "invalid_questions"
	AskUserHistoryReasonAnswerKeyMismatch    AskUserHistoryReasonCode = "answer_key_mismatch"
	AskUserHistoryReasonMalformedFact        AskUserHistoryReasonCode = "malformed_fact"
	AskUserHistoryReasonIncompletePair       AskUserHistoryReasonCode = "incomplete_pair"
	AskUserHistoryReasonIncompleteLineage    AskUserHistoryReasonCode = "incomplete_lineage"
	AskUserHistoryReasonDuplicateFact        AskUserHistoryReasonCode = "duplicate_fact"
	AskUserHistoryReasonOriginConflict       AskUserHistoryReasonCode = "origin_conflict"
)

type AskUserHistoryAuditDetail struct {
	CandidateID    string                   `json:"candidate_id"`
	FirstOrdinal   int64                    `json:"first_ordinal"`
	ToolUseID      string                   `json:"tool_use_id"`
	ToolEventID    int64                    `json:"tool_event_id,omitempty"`
	ResultEventID  int64                    `json:"result_event_id,omitempty"`
	RunnerAttempt  int64                    `json:"runner_attempt,omitempty"`
	Status         AskUserHistoryStatus     `json:"status"`
	ReasonCode     AskUserHistoryReasonCode `json:"reason_code"`
	StateJSON      []byte                   `json:"state_json,omitempty"`
	EvidenceJSON   []byte                   `json:"evidence_json"`
	EvidenceSHA256 string                   `json:"evidence_sha256"`
}

type AskUserHistoryShadowDimension string

const (
	AskUserHistoryShadowStableIDs   AskUserHistoryShadowDimension = "stable_ids"
	AskUserHistoryShadowBranchOrder AskUserHistoryShadowDimension = "branch_order"
	AskUserHistoryShadowStates      AskUserHistoryShadowDimension = "ask_user_states"
	AskUserHistoryShadowTerminal    AskUserHistoryShadowDimension = "terminal_facts"
	AskUserHistoryShadowArtifacts   AskUserHistoryShadowDimension = "artifact_refs"
)

type AskUserHistoryShadowVerdict string

const (
	AskUserHistoryShadowMatch    AskUserHistoryShadowVerdict = "match"
	AskUserHistoryShadowMismatch AskUserHistoryShadowVerdict = "mismatch"
	AskUserHistoryShadowBlocked  AskUserHistoryShadowVerdict = "blocked"
)

type AskUserHistoryShadowComparison struct {
	Dimension       AskUserHistoryShadowDimension
	Verdict         AskUserHistoryShadowVerdict
	LegacySHA256    string
	CandidateSHA256 string
	ReasonCode      string
	EvidenceJSON    []byte
	ComparedAt      time.Time
}

type AskUserHistoryAudit struct {
	RunID                      string
	StreamUID                  string
	OwnerID                    string
	BranchID                   string
	BranchGeneration           int64
	ContractVersion            int
	ThroughOrdinal             int64
	ThroughPublicationSequence int64
	SourceSHA256               string
	Status                     AskUserHistoryStatus
	ReasonCode                 AskUserHistoryReasonCode
	CandidateCount             int
	NativeCount                int
	EligibleCount              int
	PoisonCount                int
	ConflictCount              int
	Details                    []AskUserHistoryAuditDetail
	Comparisons                []AskUserHistoryShadowComparison
	ClassifiedAt               time.Time
}

type AuditAskUserHistoryInput struct {
	StreamUID     string
	OwnerID       string
	BranchID      string
	MaxEvents     int
	MaxCandidates int
	MaxShadowRows int
}

type GetAskUserHistoryAuditInput struct {
	RunID     string
	StreamUID string
	OwnerID   string
}

type askUserHistoryEvent struct {
	ordinal int64
	event   Event
	payload []byte
}

type typedAskUserHistoryState struct {
	origin          AskUserOriginV1
	promptEventID   int64
	promptOrdinal   int64
	pendingEventID  int64
	pendingOrdinal  int64
	terminalOrdinal int64
	runnerAttempt   int64
	questions       []AskUserQuestionV1
	result          AskUserResultV1
	stateJSON       []byte
}

type legacyAskUserHistoryState struct {
	toolEventID       int64
	toolOrdinal       int64
	toolAttempt       int64
	resultEventID     int64
	resultOrdinal     int64
	resultAttempt     int64
	questionsOK       bool
	questions         []AskUserQuestionV1
	questionKeys      []string
	answerKeys        []string
	resultStatus      AskUserStatus
	structured        bool
	prose             bool
	malformed         bool
	duplicate         bool
	stateJSON         []byte
	modelContinuation string
}

type askUserHistoryCandidateBudget struct {
	limit int
	used  int
}

func (budget *askUserHistoryCandidateBudget) reserve() error {
	if budget == nil || budget.limit <= 0 || budget.used >= budget.limit {
		return ErrHistoryAuditBudgetExceeded
	}
	budget.used++
	return nil
}

func (r *Repository) AuditAskUserHistory(
	ctx context.Context,
	input AuditAskUserHistoryInput,
) (AskUserHistoryAudit, bool, error) {
	if r == nil || r.db == nil {
		return AskUserHistoryAudit{}, false, ErrSchemaUnavailable
	}
	input.StreamUID = strings.TrimSpace(input.StreamUID)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.BranchID = strings.TrimSpace(input.BranchID)
	if input.StreamUID == "" || input.OwnerID == "" || (input.BranchID != "" && !validTranscriptBranchID(input.BranchID)) ||
		input.MaxEvents <= 0 || input.MaxCandidates <= 0 || input.MaxShadowRows <= 0 {
		return AskUserHistoryAudit{}, false, errors.New("stream, owner, valid branch, and positive resource budgets are required")
	}
	readTx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return AskUserHistoryAudit{}, false, schemaError(err)
	}
	snapshot, err := loadAskUserHistorySnapshot(ctx, readTx, input)
	if err != nil {
		_ = readTx.Rollback()
		return AskUserHistoryAudit{}, false, schemaError(err)
	}
	if err := readTx.Commit(); err != nil {
		return AskUserHistoryAudit{}, false, schemaError(err)
	}
	audit, err := classifyAskUserHistoryEvents(
		snapshot.stream, snapshot.branchID, snapshot.generation, snapshot.branch, snapshot.events,
		input.MaxCandidates, r.now().UTC(),
	)
	if err != nil {
		return AskUserHistoryAudit{}, false, err
	}
	finalizeAskUserHistoryAudit(&audit, snapshot)
	created := false
	err = r.withImmediate(ctx, func(conn *sql.Conn) error {
		current, err := loadAskUserHistorySnapshot(ctx, conn, input)
		if err != nil {
			return err
		}
		if !askUserHistorySnapshotsEqual(snapshot, current) {
			return ErrBranchStateStale
		}
		existing, found, err := getAskUserHistoryAuditConn(ctx, conn, GetAskUserHistoryAuditInput{
			RunID: audit.RunID, StreamUID: audit.StreamUID, OwnerID: audit.OwnerID,
		})
		if err != nil {
			return err
		}
		if found {
			if !askUserHistoryAuditsEqual(existing, audit) {
				return ErrEventConflict
			}
			audit = existing
			return nil
		}
		if err := insertAskUserHistoryAuditConn(ctx, conn, audit); err != nil {
			return err
		}
		created = true
		return nil
	})
	return audit, created, schemaError(err)
}

func (r *Repository) GetAskUserHistoryAudit(
	ctx context.Context,
	input GetAskUserHistoryAuditInput,
) (AskUserHistoryAudit, bool, error) {
	if r == nil || r.db == nil {
		return AskUserHistoryAudit{}, false, ErrSchemaUnavailable
	}
	input.RunID = strings.TrimSpace(input.RunID)
	input.StreamUID = strings.TrimSpace(input.StreamUID)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	if len(input.RunID) != 64 || input.StreamUID == "" || input.OwnerID == "" {
		return AskUserHistoryAudit{}, false, errors.New("complete AskUser history audit identity is required")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return AskUserHistoryAudit{}, false, schemaError(err)
	}
	defer func() { _ = tx.Rollback() }()
	var storedOwnerID string
	if err := tx.QueryRowContext(ctx, `SELECT owner_id FROM transcript_streams WHERE stream_uid=?`, input.StreamUID).Scan(&storedOwnerID); err != nil {
		return AskUserHistoryAudit{}, false, schemaError(err)
	}
	if storedOwnerID != input.OwnerID {
		return AskUserHistoryAudit{}, false, ErrOwnerMismatch
	}
	audit, found, err := getAskUserHistoryAuditTx(ctx, tx, input)
	if err != nil {
		return AskUserHistoryAudit{}, false, schemaError(err)
	}
	if err := tx.Commit(); err != nil {
		return AskUserHistoryAudit{}, false, schemaError(err)
	}
	return audit, found, nil
}

type askUserHistoryQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type askUserHistorySnapshot struct {
	stream                  Stream
	frameIncarnationID      string
	activeBranchID          string
	branchID                string
	generation              int64
	branch                  askUserHistoryBranchSnapshot
	events                  []askUserHistoryEvent
	terminalLegacySHA256    [sha256.Size]byte
	terminalCandidateSHA256 [sha256.Size]byte
	artifactLegacySHA256    [sha256.Size]byte
	artifactCandidateSHA256 [sha256.Size]byte
}

type askUserHistoryBranchSnapshot struct {
	ParentBranchID   string `json:"parent_branch_id,omitempty"`
	ForkEventID      int64  `json:"fork_event_id,omitempty"`
	ForkPoint        int64  `json:"fork_point"`
	Kind             string `json:"kind"`
	ClientMutationID string `json:"client_mutation_id"`
	RequestSHA256    string `json:"request_sha256"`
	SourceMessageID  string `json:"source_message_id,omitempty"`
}

func loadAskUserHistorySnapshot(
	ctx context.Context,
	query askUserHistoryQueryer,
	input AuditAskUserHistoryInput,
) (askUserHistorySnapshot, error) {
	var stream Stream
	var kind string
	var activeBranchID string
	var generation int64
	err := query.QueryRowContext(ctx, `
		SELECT stream.stream_uid,stream.owner_id,stream.external_id,stream.session_id,stream.kind,
			stream.project_id,stream.root_frame_id,stream.frame_id,stream.epoch,
			stream.input_revision,stream.consumed_input_revision,stream.next_event_id,
			stream.next_publication_seq,stream.next_checkpoint_sequence,stream.created_at,stream.updated_at,
			state.active_branch_id,state.generation
		FROM transcript_streams stream JOIN transcript_branch_state state ON state.stream_uid=stream.stream_uid
		WHERE stream.stream_uid=?`, input.StreamUID,
	).Scan(
		&stream.UID, &stream.OwnerID, &stream.ExternalID, &stream.SessionID, &kind,
		&stream.ProjectID, &stream.RootFrameID, &stream.FrameID, &stream.Epoch,
		&stream.InputRevision, &stream.ConsumedInputRevision, &stream.NextEventID,
		&stream.NextPublication, &stream.NextCheckpoint, &stream.CreatedAt, &stream.UpdatedAt,
		&activeBranchID, &generation,
	)
	if err != nil {
		return askUserHistorySnapshot{}, err
	}
	stream.Kind = StreamKind(kind)
	if stream.OwnerID != input.OwnerID {
		return askUserHistorySnapshot{}, ErrOwnerMismatch
	}
	if stream.Kind != StreamKindFrameRef || stream.FrameID == "" {
		return askUserHistorySnapshot{}, ErrEventConflict
	}
	var frameIncarnationID string
	if err := query.QueryRowContext(ctx, `SELECT incarnation_id FROM frames WHERE id=?`, stream.FrameID).Scan(&frameIncarnationID); err != nil {
		return askUserHistorySnapshot{}, err
	}
	frameIncarnationID = strings.TrimSpace(frameIncarnationID)
	if frameIncarnationID == "" {
		return askUserHistorySnapshot{}, ErrEventConflict
	}
	branchID := input.BranchID
	if branchID == "" {
		branchID = activeBranchID
	}
	var branch askUserHistoryBranchSnapshot
	var parentBranchID sql.NullString
	var forkEventID sql.NullInt64
	var requestDigest []byte
	if err := query.QueryRowContext(ctx, `
		SELECT parent_branch_id,fork_event_id,fork_point,kind,client_mutation_id,request_sha256,source_message_id
		FROM transcript_branches WHERE stream_uid=? AND branch_id=?`, stream.UID, branchID,
	).Scan(&parentBranchID, &forkEventID, &branch.ForkPoint, &branch.Kind, &branch.ClientMutationID,
		&requestDigest, &branch.SourceMessageID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return askUserHistorySnapshot{}, ErrBranchTargetNotFound
		}
		return askUserHistorySnapshot{}, err
	}
	if parentBranchID.Valid {
		branch.ParentBranchID = parentBranchID.String
	}
	if forkEventID.Valid {
		branch.ForkEventID = forkEventID.Int64
	}
	if len(requestDigest) != sha256.Size {
		return askUserHistorySnapshot{}, ErrEventConflict
	}
	branch.RequestSHA256 = hex.EncodeToString(requestDigest)
	events, err := loadAskUserHistoryEvents(ctx, query, stream, branchID, input.MaxEvents)
	if err != nil {
		return askUserHistorySnapshot{}, err
	}
	terminalLegacyDigest, err := digestAskUserHistoryRows(ctx, query, input.MaxShadowRows, "terminal-facts-v1", `
		SELECT CASE status WHEN 'canceled' THEN 'cancelled' ELSE status END
		FROM frames WHERE id=? AND status IN ('completed','failed','cancelled','canceled')`, stream.FrameID)
	if err != nil {
		return askUserHistorySnapshot{}, err
	}
	terminalCandidateDigest, err := digestAskUserHistoryRows(ctx, query, input.MaxShadowRows, "terminal-facts-v1", `
		SELECT CASE receipt.status WHEN 'canceled' THEN 'cancelled' ELSE receipt.status END
		FROM transcript_runner_receipts receipt
		JOIN transcript_branch_events membership
			ON membership.stream_uid=receipt.stream_uid AND membership.event_id=receipt.event_id
		WHERE receipt.stream_uid=? AND membership.branch_id=?
		ORDER BY receipt.attempt DESC LIMIT 1`, stream.UID, branchID)
	if err != nil {
		return askUserHistorySnapshot{}, err
	}
	artifactLegacyDigest, err := digestAskUserHistoryRows(ctx, query, input.MaxShadowRows, "artifact-refs-v1", `
		SELECT ref.runner_attempt,ref.source_event_id,ref.ordinal,ref.artifact_id,ref.version_id,ref.relation,ref.availability
		FROM transcript_artifact_refs ref
		JOIN transcript_branch_events membership
			ON membership.stream_uid=ref.stream_uid AND membership.event_id=ref.source_event_id
		WHERE ref.stream_uid=? AND membership.branch_id=?
		ORDER BY ref.runner_attempt,ref.source_event_id,ref.ordinal,ref.artifact_id,ref.version_id`, stream.UID, branchID)
	if err != nil {
		return askUserHistorySnapshot{}, err
	}
	artifactCandidateDigest, err := digestAskUserHistoryRows(ctx, query, input.MaxShadowRows, "artifact-refs-v1", `
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
		return askUserHistorySnapshot{}, err
	}
	return askUserHistorySnapshot{
		stream: stream, frameIncarnationID: frameIncarnationID, activeBranchID: activeBranchID, branchID: branchID,
		generation: generation, branch: branch, events: events,
		terminalLegacySHA256: terminalLegacyDigest, terminalCandidateSHA256: terminalCandidateDigest,
		artifactLegacySHA256: artifactLegacyDigest, artifactCandidateSHA256: artifactCandidateDigest,
	}, nil
}

func loadAskUserHistoryEvents(
	ctx context.Context,
	query askUserHistoryQueryer,
	stream Stream,
	branchID string,
	maxEvents int,
) ([]askUserHistoryEvent, error) {
	rows, err := query.QueryContext(ctx, `
		SELECT membership.ordinal,event.event_id,event.publication_seq,event.client_message_id,
			event.event_type,event.source,event.runner_attempt,event.payload_json,event.frame_event_id,event.created_at,
			frame.frame_id,frame.event_type,frame.payload
		FROM transcript_branch_events membership
		JOIN transcript_events event ON event.stream_uid=membership.stream_uid AND event.event_id=membership.event_id
		LEFT JOIN frame_events frame ON event.source='frame_ref' AND frame.id=event.frame_event_id
		WHERE membership.stream_uid=? AND membership.branch_id=? ORDER BY membership.ordinal`, stream.UID, branchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]askUserHistoryEvent, 0, 128)
	seenEventIDs := map[int64]bool{}
	seenPublications := map[int64]bool{}
	for rows.Next() {
		if len(result) >= maxEvents {
			return nil, ErrHistoryAuditBudgetExceeded
		}
		var item askUserHistoryEvent
		var source string
		var runnerAttempt sql.NullInt64
		var frameEventID sql.NullString
		var canonicalFrameID, canonicalType, canonicalPayload sql.NullString
		if err := rows.Scan(
			&item.ordinal, &item.event.EventID, &item.event.PublicationSeq, &item.event.ClientMessageID,
			&item.event.Type, &source, &runnerAttempt, &item.event.PayloadJSON, &frameEventID, &item.event.CreatedAt,
			&canonicalFrameID, &canonicalType, &canonicalPayload,
		); err != nil {
			return nil, err
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
			item.event, stream.Kind, stream.FrameID, canonicalFrameID, canonicalType, canonicalPayload,
		)
		if err != nil {
			return nil, err
		}
		if item.ordinal != int64(len(result)+1) || item.event.EventID <= 0 || item.event.PublicationSeq <= 0 ||
			seenEventIDs[item.event.EventID] || seenPublications[item.event.PublicationSeq] {
			return nil, ErrEventConflict
		}
		seenEventIDs[item.event.EventID] = true
		seenPublications[item.event.PublicationSeq] = true
		result = append(result, item)
	}
	return result, rows.Err()
}

func classifyAskUserHistoryEvents(
	stream Stream,
	branchID string,
	generation int64,
	branch askUserHistoryBranchSnapshot,
	events []askUserHistoryEvent,
	maxCandidates int,
	now time.Time,
) (AskUserHistoryAudit, error) {
	budget := &askUserHistoryCandidateBudget{limit: maxCandidates}
	typed, typedOrder, ignoredFrameEvents, typedErr, err := collectTypedAskUserHistory(stream, events, budget)
	if err != nil {
		return AskUserHistoryAudit{}, err
	}
	if typedErr != AskUserHistoryReasonNone {
		if err := budget.reserve(); err != nil {
			return AskUserHistoryAudit{}, err
		}
	}
	legacy, legacyOrder, err := collectLegacyAskUserHistory(events, ignoredFrameEvents, budget)
	if err != nil {
		return AskUserHistoryAudit{}, err
	}
	details := make([]AskUserHistoryAuditDetail, 0, len(typedOrder)+len(legacyOrder))
	status := AskUserHistoryNotApplicable
	reason := AskUserHistoryReasonNone
	if typedErr != AskUserHistoryReasonNone {
		status, reason = AskUserHistoryConflict, typedErr
		if typedErr == AskUserHistoryReasonMalformedFact || typedErr == AskUserHistoryReasonIncompletePair {
			status = AskUserHistoryQuarantined
		}
	}
	for _, callID := range typedOrder {
		state := typed[callID]
		detailStatus, detailReason := classifyTypedAskUserHistoryState(stream, branchID, generation, state, events)
		details = append(details, AskUserHistoryAuditDetail{
			FirstOrdinal: state.promptOrdinal, ToolUseID: callID,
			ToolEventID: state.promptEventID, ResultEventID: state.pendingEventID,
			RunnerAttempt: state.runnerAttempt, Status: detailStatus, ReasonCode: detailReason,
			StateJSON: append([]byte(nil), state.stateJSON...),
		})
		status, reason = mergeAskUserHistoryOutcome(status, reason, detailStatus, detailReason)
	}
	if typedErr != AskUserHistoryReasonNone {
		anomalyStatus := AskUserHistoryConflict
		if typedErr == AskUserHistoryReasonMalformedFact || typedErr == AskUserHistoryReasonIncompletePair {
			anomalyStatus = AskUserHistoryQuarantined
		}
		details = append(details, AskUserHistoryAuditDetail{
			FirstOrdinal: 1, ToolUseID: "typed-anomaly", Status: anomalyStatus, ReasonCode: typedErr,
		})
		status, reason = mergeAskUserHistoryOutcome(status, reason, anomalyStatus, typedErr)
	}
	for _, callID := range legacyOrder {
		state := legacy[callID]
		detailStatus, detailReason, attempt := classifyLegacyAskUserHistoryState(state, branch)
		details = append(details, AskUserHistoryAuditDetail{
			FirstOrdinal: firstAskUserHistoryOrdinal(state.toolOrdinal, state.resultOrdinal), ToolUseID: callID,
			ToolEventID: state.toolEventID, ResultEventID: state.resultEventID,
			RunnerAttempt: attempt, Status: detailStatus, ReasonCode: detailReason,
			StateJSON: append([]byte(nil), state.stateJSON...),
		})
		status, reason = mergeAskUserHistoryOutcome(status, reason, detailStatus, detailReason)
	}
	if len(details) == 0 && status == AskUserHistoryConflict {
		if err := budget.reserve(); err != nil {
			return AskUserHistoryAudit{}, err
		}
		firstOrdinal := int64(1)
		if len(events) > 0 {
			firstOrdinal = events[0].ordinal
		}
		details = append(details, AskUserHistoryAuditDetail{
			FirstOrdinal: firstOrdinal, Status: AskUserHistoryConflict, ReasonCode: reason,
		})
	}
	if len(details) > maxCandidates || len(details) != budget.used {
		return AskUserHistoryAudit{}, ErrHistoryAuditBudgetExceeded
	}
	throughOrdinal := int64(0)
	throughPublication := int64(0)
	if len(events) > 0 {
		throughOrdinal = events[len(events)-1].ordinal
		for _, item := range events {
			if item.event.PublicationSeq > throughPublication {
				throughPublication = item.event.PublicationSeq
			}
		}
	}
	digest := askUserHistorySourceDigest(events)
	return AskUserHistoryAudit{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, BranchID: branchID, BranchGeneration: generation,
		ContractVersion: askUserHistoryContractVersion, ThroughOrdinal: throughOrdinal,
		ThroughPublicationSequence: throughPublication, SourceSHA256: hex.EncodeToString(digest[:]),
		Status: status, ReasonCode: reason, CandidateCount: len(details), Details: details, ClassifiedAt: now,
	}, nil
}

func collectTypedAskUserHistory(
	stream Stream,
	events []askUserHistoryEvent,
	budget *askUserHistoryCandidateBudget,
) (map[string]*typedAskUserHistoryState, []string, map[string]bool, AskUserHistoryReasonCode, error) {
	states := map[string]*typedAskUserHistoryState{}
	order := []string{}
	ignoredFrameEvents := map[string]bool{}
	reason := AskUserHistoryReasonNone
	for _, item := range events {
		if item.event.Source != EventSourcePayload ||
			(item.event.Type != AskUserPromptEventType && item.event.Type != AskUserResultEventType) {
			continue
		}
		switch item.event.Type {
		case AskUserPromptEventType:
			prompt, err := DecodeAskUserPromptV1(item.payload)
			if err != nil {
				reason = AskUserHistoryReasonMalformedFact
				continue
			}
			if item.event.StreamUID != stream.UID || item.event.ClientMessageID != prompt.Origin.PromptClientMessageID ||
				item.event.RunnerAttempt == nil || *item.event.RunnerAttempt != prompt.Origin.RunnerAttempt {
				reason = AskUserHistoryReasonOriginConflict
				continue
			}
			state := states[prompt.ToolUseID]
			if state == nil {
				if err := budget.reserve(); err != nil {
					return nil, nil, nil, AskUserHistoryReasonNone, err
				}
				state = &typedAskUserHistoryState{}
				states[prompt.ToolUseID] = state
				order = append(order, prompt.ToolUseID)
			}
			if state.promptEventID != 0 || (state.origin.StreamUID != "" && state.origin != prompt.Origin) {
				reason = AskUserHistoryReasonDuplicateFact
				continue
			}
			state.origin = prompt.Origin
			state.promptEventID = item.event.EventID
			state.promptOrdinal = item.ordinal
			state.runnerAttempt = prompt.Origin.RunnerAttempt
			state.questions = append([]AskUserQuestionV1(nil), prompt.Questions...)
			ignoredFrameEvents[prompt.Origin.ToolUseFrameEventID] = true
			ignoredFrameEvents[prompt.Origin.PendingFrameEventID] = true
		case AskUserResultEventType:
			result, err := DecodeAskUserResultEventV1(item.payload)
			if err != nil {
				reason = AskUserHistoryReasonMalformedFact
				continue
			}
			if item.event.StreamUID != stream.UID || item.event.RunnerAttempt == nil ||
				*item.event.RunnerAttempt != result.Origin.RunnerAttempt {
				reason = AskUserHistoryReasonOriginConflict
				continue
			}
			pending := result.Result.Status == AskUserStatusAwaitingResponse
			expectedID := result.Origin.PendingClientMessageID
			if !pending {
				expectedID, err = AskUserResultClientMessageIDV1(result.Origin)
			}
			if err != nil || item.event.ClientMessageID != expectedID {
				reason = AskUserHistoryReasonOriginConflict
				continue
			}
			state := states[result.ToolUseID]
			if state == nil {
				if err := budget.reserve(); err != nil {
					return nil, nil, nil, AskUserHistoryReasonNone, err
				}
				state = &typedAskUserHistoryState{}
				states[result.ToolUseID] = state
				order = append(order, result.ToolUseID)
			}
			if state.origin.StreamUID != "" && state.origin != result.Origin {
				reason = AskUserHistoryReasonOriginConflict
				continue
			}
			state.origin = result.Origin
			state.runnerAttempt = result.Origin.RunnerAttempt
			state.result = result.Result
			state.stateJSON, _ = EncodeAskUserResultV1(result.Result)
			ignoredFrameEvents[result.Origin.ToolUseFrameEventID] = true
			ignoredFrameEvents[result.Origin.PendingFrameEventID] = true
			if pending {
				if state.pendingEventID != 0 {
					reason = AskUserHistoryReasonDuplicateFact
					continue
				}
				state.pendingEventID = item.event.EventID
				state.pendingOrdinal = item.ordinal
			} else {
				if state.terminalOrdinal != 0 {
					reason = AskUserHistoryReasonDuplicateFact
					continue
				}
				state.terminalOrdinal = item.ordinal
			}
		}
	}
	return states, order, ignoredFrameEvents, reason, nil
}

func collectLegacyAskUserHistory(
	events []askUserHistoryEvent,
	ignoredFrameEvents map[string]bool,
	budget *askUserHistoryCandidateBudget,
) (map[string]*legacyAskUserHistoryState, []string, error) {
	states := map[string]*legacyAskUserHistoryState{}
	order := []string{}
	askUserCallIDs := map[string]bool{}
	for _, item := range events {
		if item.event.Source != EventSourceFrameRef || item.event.FrameEventID == nil || ignoredFrameEvents[*item.event.FrameEventID] {
			continue
		}
		var message map[string]any
		if jsonHasDuplicateObjectKeys(item.payload) || json.Unmarshal(item.payload, &message) != nil {
			continue
		}
		blocks, _ := message["content"].([]any)
		for _, raw := range blocks {
			block, ok := raw.(map[string]any)
			_, askUser := CanonicalAskUserToolNameV1(webHistoryString(block["name"]))
			if !ok || strings.TrimSpace(webHistoryString(block["type"])) != "tool_use" || !askUser {
				continue
			}
			if callID := strings.TrimSpace(webHistoryString(block["id"])); callID != "" && !askUserCallIDs[callID] {
				if err := budget.reserve(); err != nil {
					return nil, nil, err
				}
				askUserCallIDs[callID] = true
			}
		}
	}
	for _, item := range events {
		if item.event.Source != EventSourceFrameRef || item.event.FrameEventID == nil || ignoredFrameEvents[*item.event.FrameEventID] {
			continue
		}
		var message map[string]any
		if jsonHasDuplicateObjectKeys(item.payload) || json.Unmarshal(item.payload, &message) != nil {
			continue
		}
		blocks, _ := message["content"].([]any)
		for _, raw := range blocks {
			block, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			switch strings.TrimSpace(webHistoryString(block["type"])) {
			case "tool_use":
				if _, ok := CanonicalAskUserToolNameV1(webHistoryString(block["name"])); !ok {
					continue
				}
				callID := strings.TrimSpace(webHistoryString(block["id"]))
				if callID == "" {
					if err := addMalformedLegacyAskUserState(states, &order, item, budget); err != nil {
						return nil, nil, err
					}
					continue
				}
				state := states[callID]
				if state == nil {
					state = &legacyAskUserHistoryState{}
					states[callID] = state
					order = append(order, callID)
				}
				if state.toolEventID != 0 {
					state.duplicate = true
					continue
				}
				state.toolEventID = item.event.EventID
				state.toolOrdinal = item.ordinal
				if len(blocks) != 1 || item.event.Type != "assistant_message" || message["role"] != "assistant" ||
					!hasOnlyAskUserKeys(message, "role", "content") ||
					!hasOnlyAskUserKeys(block, "type", "id", "name", "input") {
					state.malformed = true
				}
				if item.event.RunnerAttempt != nil {
					state.toolAttempt = *item.event.RunnerAttempt
				}
				input, inputOK := block["input"].(map[string]any)
				if !inputOK || !hasOnlyAskUserKeys(input, "questions") {
					state.malformed = true
				}
				questions, _ := input["questions"].([]any)
				normalizedQuestions, err := normalizeAskUserQuestions(questions)
				state.questionsOK = err == nil
				if err == nil {
					state.questions = append([]AskUserQuestionV1(nil), normalizedQuestions...)
					state.questionKeys = askUserQuestionKeys(normalizedQuestions)
				}
			case "tool_result":
				callID := strings.TrimSpace(webHistoryString(block["tool_use_id"]))
				if !askUserCallIDs[callID] && !looksLikeStructuredAskUserResult(block["content"]) {
					continue
				}
				if callID == "" {
					if err := addMalformedLegacyAskUserState(states, &order, item, budget); err != nil {
						return nil, nil, err
					}
					continue
				}
				state := states[callID]
				if state == nil {
					if !askUserCallIDs[callID] {
						if err := budget.reserve(); err != nil {
							return nil, nil, err
						}
					}
					state = &legacyAskUserHistoryState{}
					states[callID] = state
					order = append(order, callID)
				}
				if state.resultEventID != 0 {
					state.duplicate = true
					continue
				}
				state.resultEventID = item.event.EventID
				state.resultOrdinal = item.ordinal
				if len(blocks) != 1 || (item.event.Type != "user_message" && item.event.Type != "user_input_response") ||
					message["role"] != "user" || !hasOnlyAskUserKeys(message, "role", "content") ||
					!hasOnlyAskUserKeys(block, "type", "tool_use_id", "content", "is_error") || block["is_error"] != true {
					state.malformed = true
				}
				if item.event.RunnerAttempt != nil {
					state.resultAttempt = *item.event.RunnerAttempt
				}
				content, ok := block["content"].(string)
				if !ok {
					state.malformed = true
					continue
				}
				classifyLegacyAskUserResult(content, state)
			}
		}
	}
	return states, order, nil
}

func looksLikeStructuredAskUserResult(raw any) bool {
	content, ok := raw.(string)
	if !ok {
		return false
	}
	content = strings.TrimSpace(content)
	if content == "" || !json.Valid([]byte(content)) || jsonHasDuplicateObjectKeys([]byte(content)) {
		return false
	}
	state := &legacyAskUserHistoryState{}
	classifyLegacyAskUserResult(content, state)
	return state.structured && !state.malformed && !state.prose
}

func classifyLegacyAskUserResult(content string, state *legacyAskUserHistoryState) {
	content = strings.TrimSpace(content)
	if content == "" {
		state.malformed = true
		return
	}
	if !json.Valid([]byte(content)) {
		state.prose = true
		return
	}
	if jsonHasDuplicateObjectKeys([]byte(content)) {
		state.malformed = true
		return
	}
	if result, err := DecodeAskUserResultV1([]byte(content)); err == nil {
		state.structured = true
		state.resultStatus = result.Status
		state.answerKeys = askUserAnswerKeys(result.Answers)
		state.stateJSON, _ = EncodeAskUserResultV1(result)
		state.modelContinuation = content
		return
	}
	var legacy map[string]any
	_ = json.Unmarshal([]byte(content), &legacy)
	if len(legacy) == 1 && strings.TrimSpace(webHistoryString(legacy["status"])) == string(AskUserStatusAwaitingResponse) {
		state.structured = true
		state.resultStatus = AskUserStatusAwaitingResponse
		state.stateJSON, _ = EncodeAskUserResultV1(NewAskUserPendingResultV1())
		state.modelContinuation = content
		return
	}
	if strings.TrimSpace(webHistoryString(legacy["status"])) == string(AskUserStatusAnswered) {
		rawAnswers, ok := legacy["answers"].(map[string]any)
		if !ok || len(rawAnswers) == 0 || len(legacy) != 2 {
			state.malformed = true
			return
		}
		answers := make(map[string]string, len(rawAnswers))
		for question, raw := range rawAnswers {
			answer, ok := raw.(string)
			if !ok {
				state.malformed = true
				return
			}
			answers[question] = answer
		}
		result, err := NewAskUserResultV1(AskUserActionAnswer, answers, "")
		if err != nil {
			state.malformed = true
			return
		}
		state.structured = true
		state.resultStatus = AskUserStatusAnswered
		state.answerKeys = askUserAnswerKeys(answers)
		state.stateJSON, _ = EncodeAskUserResultV1(result)
		state.modelContinuation = content
		return
	}
	state.malformed = true
}

func classifyTypedAskUserHistoryState(
	stream Stream,
	branchID string,
	generation int64,
	state *typedAskUserHistoryState,
	events []askUserHistoryEvent,
) (AskUserHistoryStatus, AskUserHistoryReasonCode) {
	if state == nil || state.origin.StreamUID == "" {
		return AskUserHistoryQuarantined, AskUserHistoryReasonMalformedFact
	}
	if state.promptEventID == 0 || state.pendingEventID == 0 {
		return AskUserHistoryQuarantined, AskUserHistoryReasonIncompletePair
	}
	if state.origin.StreamUID != stream.UID || state.origin.Epoch != stream.Epoch ||
		state.origin.FrameID != stream.FrameID || state.origin.BranchID != branchID ||
		state.origin.BranchGeneration != generation {
		return AskUserHistoryConflict, AskUserHistoryReasonOriginConflict
	}
	toolOrdinal, toolFound := historyFrameReferenceOrdinal(events, state.origin.ToolUseFrameEventID)
	resultOrdinal, resultFound := historyFrameReferenceOrdinal(events, state.origin.PendingFrameEventID)
	if state.promptOrdinal >= state.pendingOrdinal ||
		(state.terminalOrdinal > 0 && state.terminalOrdinal <= state.pendingOrdinal) ||
		!toolFound || !resultFound || toolOrdinal >= resultOrdinal || resultOrdinal >= state.promptOrdinal {
		return AskUserHistoryConflict, AskUserHistoryReasonOriginConflict
	}
	if state.result.Status == AskUserStatusAnswered && !askUserStringSetsEqual(
		askUserQuestionKeys(state.questions), askUserAnswerKeys(state.result.Answers),
	) {
		return AskUserHistoryConflict, AskUserHistoryReasonAnswerKeyMismatch
	}
	return AskUserHistoryNativeV1, AskUserHistoryReasonNativeV1
}

func classifyLegacyAskUserHistoryState(
	state *legacyAskUserHistoryState,
	branch askUserHistoryBranchSnapshot,
) (AskUserHistoryStatus, AskUserHistoryReasonCode, int64) {
	if state == nil || state.toolEventID == 0 || state.resultEventID == 0 || state.toolOrdinal >= state.resultOrdinal {
		return AskUserHistoryConflict, AskUserHistoryReasonIncompletePair, 0
	}
	if state.duplicate {
		return AskUserHistoryConflict, AskUserHistoryReasonDuplicateFact, 0
	}
	if state.malformed || !state.structured {
		if state.prose && !state.malformed {
			return AskUserHistoryQuarantined, AskUserHistoryReasonAmbiguousProse, 0
		}
		return AskUserHistoryQuarantined, AskUserHistoryReasonMalformedFact, 0
	}
	if !state.questionsOK {
		return AskUserHistoryQuarantined, AskUserHistoryReasonInvalidQuestions, 0
	}
	if state.resultStatus == AskUserStatusAnswered && !askUserStringSetsEqual(state.questionKeys, state.answerKeys) {
		return AskUserHistoryQuarantined, AskUserHistoryReasonAnswerKeyMismatch, 0
	}
	if state.toolAttempt <= 0 || state.resultAttempt <= 0 || state.toolAttempt != state.resultAttempt {
		return AskUserHistoryQuarantined, AskUserHistoryReasonMissingRunnerAttempt, 0
	}
	if branch.Kind != "base" {
		return AskUserHistoryQuarantined, AskUserHistoryReasonIncompleteLineage, 0
	}
	return AskUserHistoryEligible, AskUserHistoryReasonStructuredLegacy, state.toolAttempt
}

func addMalformedLegacyAskUserState(
	states map[string]*legacyAskUserHistoryState,
	order *[]string,
	item askUserHistoryEvent,
	budget *askUserHistoryCandidateBudget,
) error {
	key := "malformed:"
	if item.event.FrameEventID != nil {
		digest := sha256.Sum256([]byte(*item.event.FrameEventID))
		key += hex.EncodeToString(digest[:])
	} else {
		key += fmt.Sprintf("%d", item.event.EventID)
	}
	if _, found := states[key]; found {
		return nil
	}
	if err := budget.reserve(); err != nil {
		return err
	}
	states[key] = &legacyAskUserHistoryState{
		toolEventID: item.event.EventID, toolOrdinal: item.ordinal,
		resultEventID: item.event.EventID, resultOrdinal: item.ordinal + 1,
		malformed: true,
	}
	*order = append(*order, key)
	return nil
}

func askUserQuestionKeys(questions []AskUserQuestionV1) []string {
	keys := make([]string, 0, len(questions))
	for _, question := range questions {
		keys = append(keys, question.Question)
	}
	sort.Strings(keys)
	return keys
}

func askUserAnswerKeys(answers map[string]string) []string {
	keys := make([]string, 0, len(answers))
	for question := range answers {
		keys = append(keys, question)
	}
	sort.Strings(keys)
	return keys
}

func askUserStringSetsEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func jsonHasDuplicateObjectKeys(payload []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var walkValue func() bool
	walkValue = func() bool {
		token, err := decoder.Token()
		if err != nil {
			return true
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return false
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				key, ok := keyToken.(string)
				if err != nil || !ok || seen[key] {
					return true
				}
				seen[key] = true
				if walkValue() {
					return true
				}
			}
			end, err := decoder.Token()
			return err != nil || end != json.Delim('}')
		case '[':
			for decoder.More() {
				if walkValue() {
					return true
				}
			}
			end, err := decoder.Token()
			return err != nil || end != json.Delim(']')
		default:
			return true
		}
	}
	if walkValue() {
		return true
	}
	return decoder.More()
}

func historyFrameReferenceOrdinal(events []askUserHistoryEvent, frameEventID string) (int64, bool) {
	for _, item := range events {
		if item.event.Source == EventSourceFrameRef && item.event.FrameEventID != nil && *item.event.FrameEventID == frameEventID {
			return item.ordinal, true
		}
	}
	return 0, false
}

func mergeAskUserHistoryOutcome(
	currentStatus AskUserHistoryStatus,
	currentReason AskUserHistoryReasonCode,
	nextStatus AskUserHistoryStatus,
	nextReason AskUserHistoryReasonCode,
) (AskUserHistoryStatus, AskUserHistoryReasonCode) {
	priority := map[AskUserHistoryStatus]int{
		AskUserHistoryNotApplicable: 0, AskUserHistoryNativeV1: 1, AskUserHistoryEligible: 2,
		AskUserHistoryQuarantined: 3, AskUserHistoryConflict: 4,
	}
	if priority[nextStatus] > priority[currentStatus] {
		return nextStatus, nextReason
	}
	return currentStatus, currentReason
}

func askUserHistorySourceDigest(events []askUserHistoryEvent) [sha256.Size]byte {
	digest := sha256.New()
	writeHistoryDigestField(digest, []byte(HistoryClassificationContractID))
	for _, item := range events {
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
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result
}

func writeHistoryDigestField(target hash.Hash, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = target.Write(length[:])
	_, _ = target.Write(value)
}

func webHistoryString(value any) string {
	text, _ := value.(string)
	return text
}

func firstAskUserHistoryOrdinal(left, right int64) int64 {
	if left <= 0 {
		return right
	}
	if right <= 0 || left < right {
		return left
	}
	return right
}
