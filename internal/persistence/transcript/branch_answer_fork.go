package transcript

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type AskUserBranchResponse struct {
	Action  string
	Answers map[string]string
	Message string
}

type ForkFrameAskUserAnswerBranchInput struct {
	StreamUID              string
	OwnerID                string
	SourceBranchID         string
	ExpectedActiveBranchID string
	ExpectedGeneration     int64
	ClientMutationID       string
	ToolUseID              string
	SourceMessageIndex     int64
	Response               AskUserBranchResponse
	RuntimeConfig          map[string]any
	Destinations           []string
}

// AppendFrameAskUserReferencesInput binds the immutable Frame facts created
// when an AskUser tool pauses a frame to its current canonical branch.
type AppendFrameAskUserReferencesInput struct {
	StreamUID              string
	OwnerID                string
	FrameID                string
	ToolUseID              string
	ToolUseFrameEventID    string
	ToolResultFrameEventID string
}

// AppendClaimedFrameAskUserReferencesInput imports one legacy AskUser pair
// while preserving the exact runner attempt that produced it. This is used by
// governed migration and fixture code; new runtime writes use typed AskUser
// payload events instead.
type AppendClaimedFrameAskUserReferencesInput struct {
	Claim                  RunnerClaim
	FrameID                string
	ToolUseID              string
	ToolUseFrameEventID    string
	ToolResultFrameEventID string
}

type frameAskUserAnswerBranchDigest struct {
	Version            int                      `json:"version"`
	StreamUID          string                   `json:"stream_uid"`
	OwnerID            string                   `json:"owner_id"`
	SourceBranchID     string                   `json:"source_branch_id"`
	ClientMutationID   string                   `json:"client_mutation_id"`
	ToolUseID          string                   `json:"tool_use_id"`
	SourceMessageIndex int64                    `json:"source_message_index"`
	Response           askUserBranchResponseDTO `json:"response"`
	RuntimeConfig      map[string]any           `json:"runtime_config,omitempty"`
	Destinations       []string                 `json:"destinations"`
}

type askUserBranchResponseDTO struct {
	Action  string            `json:"action"`
	Answers map[string]string `json:"answers,omitempty"`
	Message string            `json:"message,omitempty"`
}

type askUserBranchTarget struct {
	ToolEvent            Event
	ResultEvent          Event
	Ordinal              int64
	Questions            []string
	ImplementationLabels map[string]map[string]string
}

func (r *Repository) ForkFrameAskUserAnswerBranch(
	ctx context.Context,
	input ForkFrameAskUserAnswerBranchInput,
) (ForkFrameBranchResult, error) {
	input, requestDigest, branchID, err := normalizeFrameAskUserAnswerBranchInput(input)
	if err != nil {
		return ForkFrameBranchResult{}, err
	}
	var result ForkFrameBranchResult
	err = r.withImmediate(ctx, func(conn *sql.Conn) error {
		var err error
		result, err = r.forkFrameAskUserAnswerBranchConn(ctx, conn, input, requestDigest, branchID)
		return err
	})
	return result, schemaError(err)
}

func (tx *ImmediateTransaction) ForkFrameAskUserAnswerBranch(
	ctx context.Context,
	input ForkFrameAskUserAnswerBranchInput,
) (ForkFrameBranchResult, error) {
	if tx == nil || tx.repository == nil || tx.conn == nil {
		return ForkFrameBranchResult{}, errors.New("transcript transaction is required")
	}
	input, requestDigest, branchID, err := normalizeFrameAskUserAnswerBranchInput(input)
	if err != nil {
		return ForkFrameBranchResult{}, err
	}
	result, err := tx.repository.forkFrameAskUserAnswerBranchConn(ctx, tx.conn, input, requestDigest, branchID)
	return result, schemaError(err)
}

// AppendFrameAskUserReferences records the exact tool-use/result pair on the
// active branch inside the caller's workspace/transcript transaction.
func (tx *ImmediateTransaction) AppendFrameAskUserReferences(
	ctx context.Context,
	input AppendFrameAskUserReferencesInput,
) ([]Event, error) {
	if tx == nil || tx.repository == nil || tx.conn == nil {
		return nil, errors.New("transcript transaction is required")
	}
	var err error
	input, err = normalizeAppendFrameAskUserReferencesInput(input)
	if err != nil {
		return nil, err
	}
	var storedOwnerID, storedFrameID, storedKind string
	if err := tx.conn.QueryRowContext(ctx, `
		SELECT owner_id,frame_id,kind FROM transcript_streams WHERE stream_uid=?`, input.StreamUID,
	).Scan(&storedOwnerID, &storedFrameID, &storedKind); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrEventConflict
		}
		return nil, schemaError(err)
	}
	if storedOwnerID != input.OwnerID || storedFrameID != input.FrameID || storedKind != string(StreamKindFrameRef) {
		return nil, ErrEventConflict
	}
	events, err := tx.AppendHistoricalFrameReferences(ctx, input.StreamUID, input.OwnerID, []string{
		input.ToolUseFrameEventID, input.ToolResultFrameEventID,
	})
	if err != nil {
		return nil, err
	}
	stream, err := getStreamConn(ctx, tx.conn, input.StreamUID, input.OwnerID)
	if err != nil {
		return nil, schemaError(err)
	}
	var activeBranchID string
	if err := tx.conn.QueryRowContext(ctx, `
		SELECT active_branch_id FROM transcript_branch_state WHERE stream_uid=?`, input.StreamUID,
	).Scan(&activeBranchID); err != nil {
		return nil, schemaError(err)
	}
	target, err := resolveAskUserBranchTargetConn(ctx, tx.conn, stream, activeBranchID, input.ToolUseID)
	if err != nil {
		return nil, schemaError(err)
	}
	if target.ToolEvent.FrameEventID == nil || target.ResultEvent.FrameEventID == nil ||
		*target.ToolEvent.FrameEventID != input.ToolUseFrameEventID ||
		*target.ResultEvent.FrameEventID != input.ToolResultFrameEventID {
		return nil, ErrEventConflict
	}
	return events, nil
}

// AppendClaimedFrameAskUserReferences binds an already-persisted legacy Frame
// pair to a currently owned runner attempt in the caller's transaction.
func (tx *ImmediateTransaction) AppendClaimedFrameAskUserReferences(
	ctx context.Context,
	input AppendClaimedFrameAskUserReferencesInput,
) ([]Event, error) {
	if tx == nil || tx.repository == nil || tx.conn == nil {
		return nil, errors.New("transcript transaction is required")
	}
	if err := validateClaimInput(input.Claim); err != nil {
		return nil, err
	}
	if _, err := validateClaimConn(ctx, tx.conn, input.Claim, tx.repository.now().UTC(), true); err != nil {
		return nil, schemaError(err)
	}
	referenceInput, err := normalizeAppendFrameAskUserReferencesInput(AppendFrameAskUserReferencesInput{
		StreamUID: input.Claim.StreamUID, OwnerID: input.Claim.OwnerID, FrameID: input.FrameID,
		ToolUseID: input.ToolUseID, ToolUseFrameEventID: input.ToolUseFrameEventID,
		ToolResultFrameEventID: input.ToolResultFrameEventID,
	})
	if err != nil {
		return nil, err
	}
	events, found, err := existingFrameAskUserReferencesConn(ctx, tx.conn, referenceInput)
	if err != nil {
		return nil, schemaError(err)
	}
	if !found {
		events, err = tx.AppendFrameAskUserReferences(ctx, referenceInput)
		if err != nil {
			return nil, err
		}
	}
	for index := range events {
		event := &events[index]
		if event.RunnerAttempt != nil && *event.RunnerAttempt != input.Claim.Attempt {
			return nil, ErrEventConflict
		}
		if event.RunnerAttempt == nil {
			updated, err := tx.conn.ExecContext(ctx, `UPDATE transcript_events SET runner_attempt=?
				WHERE stream_uid=? AND event_id=? AND runner_attempt IS NULL`,
				input.Claim.Attempt, event.StreamUID, event.EventID)
			if err != nil {
				return nil, schemaError(err)
			}
			if count, err := updated.RowsAffected(); err != nil || count != 1 {
				if err != nil {
					return nil, schemaError(err)
				}
				return nil, ErrEventConflict
			}
		}
		attempt := input.Claim.Attempt
		event.RunnerAttempt = &attempt
	}
	return events, nil
}

func normalizeAppendFrameAskUserReferencesInput(input AppendFrameAskUserReferencesInput) (AppendFrameAskUserReferencesInput, error) {
	input.StreamUID = strings.TrimSpace(input.StreamUID)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.FrameID = strings.TrimSpace(input.FrameID)
	input.ToolUseID = strings.TrimSpace(input.ToolUseID)
	input.ToolUseFrameEventID = strings.TrimSpace(input.ToolUseFrameEventID)
	input.ToolResultFrameEventID = strings.TrimSpace(input.ToolResultFrameEventID)
	if input.StreamUID == "" || input.OwnerID == "" || input.FrameID == "" || input.ToolUseID == "" ||
		input.ToolUseFrameEventID == "" || input.ToolResultFrameEventID == "" ||
		input.ToolUseFrameEventID == input.ToolResultFrameEventID {
		return AppendFrameAskUserReferencesInput{}, errors.New("stream, owner, frame, AskUser tool, and distinct frame events are required")
	}
	return input, nil
}

func existingFrameAskUserReferencesConn(
	ctx context.Context,
	conn *sql.Conn,
	input AppendFrameAskUserReferencesInput,
) ([]Event, bool, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT event_id,publication_seq,client_message_id,event_type,source,runner_attempt,payload_json,frame_event_id,created_at
		FROM transcript_events WHERE stream_uid=? AND frame_event_id IN (?,?) ORDER BY event_id`,
		input.StreamUID, input.ToolUseFrameEventID, input.ToolResultFrameEventID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	eventsByFrameID := make(map[string]Event, 2)
	for rows.Next() {
		var event Event
		var source string
		var runnerAttempt sql.NullInt64
		var frameEventID sql.NullString
		if err := rows.Scan(&event.EventID, &event.PublicationSeq, &event.ClientMessageID, &event.Type, &source,
			&runnerAttempt, &event.PayloadJSON, &frameEventID, &event.CreatedAt); err != nil {
			return nil, false, err
		}
		event.StreamUID = input.StreamUID
		event.Source = EventSource(source)
		if event.Source != EventSourceFrameRef || !frameEventID.Valid || frameEventID.String == "" {
			return nil, false, ErrEventConflict
		}
		if _, duplicate := eventsByFrameID[frameEventID.String]; duplicate {
			return nil, false, ErrEventConflict
		}
		if runnerAttempt.Valid {
			value := runnerAttempt.Int64
			event.RunnerAttempt = &value
		}
		value := frameEventID.String
		event.FrameEventID = &value
		eventsByFrameID[value] = event
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(eventsByFrameID) == 0 {
		return nil, false, nil
	}
	toolEvent, toolFound := eventsByFrameID[input.ToolUseFrameEventID]
	resultEvent, resultFound := eventsByFrameID[input.ToolResultFrameEventID]
	if !toolFound || !resultFound || len(eventsByFrameID) != 2 {
		return nil, false, ErrEventConflict
	}
	stream, err := getStreamConn(ctx, conn, input.StreamUID, input.OwnerID)
	if err != nil || stream.FrameID != input.FrameID || stream.Kind != StreamKindFrameRef {
		if err != nil {
			return nil, false, err
		}
		return nil, false, ErrEventConflict
	}
	var activeBranchID string
	if err := conn.QueryRowContext(ctx, `SELECT active_branch_id FROM transcript_branch_state WHERE stream_uid=?`,
		input.StreamUID).Scan(&activeBranchID); err != nil {
		return nil, false, err
	}
	target, err := resolveAskUserBranchTargetConn(ctx, conn, stream, activeBranchID, input.ToolUseID)
	if err != nil || target.ToolEvent.EventID != toolEvent.EventID || target.ResultEvent.EventID != resultEvent.EventID {
		if err != nil {
			return nil, false, err
		}
		return nil, false, ErrEventConflict
	}
	return []Event{toolEvent, resultEvent}, true, nil
}

func normalizeFrameAskUserAnswerBranchInput(
	input ForkFrameAskUserAnswerBranchInput,
) (ForkFrameAskUserAnswerBranchInput, []byte, string, error) {
	input.StreamUID = strings.TrimSpace(input.StreamUID)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.SourceBranchID = strings.TrimSpace(input.SourceBranchID)
	input.ExpectedActiveBranchID = strings.TrimSpace(input.ExpectedActiveBranchID)
	input.ClientMutationID = strings.TrimSpace(input.ClientMutationID)
	input.ToolUseID = strings.TrimSpace(input.ToolUseID)
	input.Response.Action = strings.TrimSpace(input.Response.Action)
	input.Response.Message = strings.TrimSpace(input.Response.Message)
	input.Destinations = normalizedDestinations(input.Destinations)
	normalizedAnswers, err := normalizedAskUserAnswers(input.Response.Answers)
	if err != nil {
		return ForkFrameAskUserAnswerBranchInput{}, nil, "", err
	}
	input.Response.Answers = normalizedAnswers
	if input.StreamUID == "" || input.OwnerID == "" || !validTranscriptBranchID(input.SourceBranchID) ||
		!validTranscriptBranchID(input.ExpectedActiveBranchID) || input.ExpectedGeneration <= 0 ||
		!validBranchMutationID(input.ClientMutationID) || input.ToolUseID == "" || len(input.ToolUseID) > 512 ||
		input.SourceMessageIndex < 0 || len(input.Destinations) == 0 || !validAskUserBranchAction(input.Response.Action) {
		return ForkFrameAskUserAnswerBranchInput{}, nil, "", errors.New("valid stream, owner, branches, generation, mutation, AskUser target, response, and destination are required")
	}
	if input.Response.Action == "discuss" && input.Response.Message == "" {
		return ForkFrameAskUserAnswerBranchInput{}, nil, "", errors.New("AskUser discuss response requires a message")
	}
	if len(input.Response.Message) > 64<<10 {
		return ForkFrameAskUserAnswerBranchInput{}, nil, "", errors.New("AskUser discussion message exceeds the durable limit")
	}
	if input.Response.Action == "answer" && (len(input.Response.Answers) == 0 || input.Response.Message != "") {
		return ForkFrameAskUserAnswerBranchInput{}, nil, "", errors.New("AskUser answer requires answers and no discussion message")
	}
	if input.Response.Action != "answer" && len(input.Response.Answers) != 0 {
		return ForkFrameAskUserAnswerBranchInput{}, nil, "", errors.New("AskUser non-answer response cannot contain answers")
	}
	if input.Response.Action != "discuss" && input.Response.Message != "" {
		return ForkFrameAskUserAnswerBranchInput{}, nil, "", errors.New("AskUser response contains an unexpected discussion message")
	}
	digestInput := frameAskUserAnswerBranchDigest{
		Version: 1, StreamUID: input.StreamUID, OwnerID: input.OwnerID,
		SourceBranchID: input.SourceBranchID, ClientMutationID: input.ClientMutationID,
		ToolUseID: input.ToolUseID, SourceMessageIndex: input.SourceMessageIndex,
		Response: askUserBranchResponseDTO{
			Action: input.Response.Action, Answers: input.Response.Answers, Message: input.Response.Message,
		},
		RuntimeConfig: input.RuntimeConfig,
		Destinations:  input.Destinations,
	}
	encoded, err := json.Marshal(digestInput)
	if err != nil || len(encoded) > maxEventPayloadBytes {
		return ForkFrameAskUserAnswerBranchInput{}, nil, "", errors.New("AskUser branch request is not serializable within the durable limit")
	}
	requestDigest := sha256.Sum256(encoded)
	identityDigest := sha256.Sum256(append([]byte("answer-branch\x00"+input.StreamUID+"\x00"+input.ClientMutationID+"\x00"), requestDigest[:]...))
	return input, requestDigest[:], "br_" + hex.EncodeToString(identityDigest[:4]), nil
}

func (r *Repository) forkFrameAskUserAnswerBranchConn(
	ctx context.Context,
	conn *sql.Conn,
	input ForkFrameAskUserAnswerBranchInput,
	requestDigest []byte,
	branchID string,
) (ForkFrameBranchResult, error) {
	stream, err := getStreamConn(ctx, conn, input.StreamUID, input.OwnerID)
	if err != nil {
		return ForkFrameBranchResult{}, err
	}
	if stream.Kind != StreamKindFrameRef || stream.FrameID == "" || stream.FrameID != stream.RootFrameID {
		return ForkFrameBranchResult{}, ErrEventConflict
	}
	structuredResult, modelContinuation, err := buildAskUserBranchResult(input.Response, nil, nil)
	if err != nil {
		return ForkFrameBranchResult{}, err
	}
	replacementInput, err := askUserBranchReplacementInput(stream, input, branchID, structuredResult, modelContinuation)
	if err != nil {
		return ForkFrameBranchResult{}, err
	}
	if existing, found, err := loadExistingFrameAskUserBranchConn(
		ctx, conn, stream, input, requestDigest, branchID, replacementInput,
	); err != nil {
		return ForkFrameBranchResult{}, err
	} else if found {
		return existing, nil
	}
	target, err := resolveAskUserBranchTargetConn(ctx, conn, stream, input.SourceBranchID, input.ToolUseID)
	if err != nil {
		return ForkFrameBranchResult{}, err
	}
	messageIndex, err := branchWebMessageIndexConn(ctx, conn, stream.UID, input.SourceBranchID, target.ToolEvent.EventID)
	if err != nil {
		return ForkFrameBranchResult{}, err
	}
	if messageIndex != input.SourceMessageIndex {
		return ForkFrameBranchResult{}, ErrEventConflict
	}
	structuredResult, modelContinuation, err = buildAskUserBranchResult(
		input.Response, target.Questions, target.ImplementationLabels,
	)
	if err != nil {
		return ForkFrameBranchResult{}, err
	}
	replacementInput, err = askUserBranchReplacementInput(stream, input, branchID, structuredResult, modelContinuation)
	if err != nil {
		return ForkFrameBranchResult{}, err
	}
	now := r.now().UTC()
	plan := branchMutationPlan{
		BranchID: branchID, Kind: "answer", SourceBranchID: input.SourceBranchID,
		ExpectedActiveBranchID: input.ExpectedActiveBranchID, ExpectedGeneration: input.ExpectedGeneration,
		ClientMutationID: input.ClientMutationID, RequestDigest: requestDigest,
		SourceMessageID: input.ToolUseID, SourceEventID: target.ResultEvent.EventID,
		SourceOrdinal: target.Ordinal, SourceMessageIndex: input.SourceMessageIndex,
	}
	cancellation, err := r.beginFrameBranchMutationConn(ctx, conn, stream, plan, now)
	if err != nil {
		return ForkFrameBranchResult{}, err
	}
	stream, err = getStreamConn(ctx, conn, stream.UID, stream.OwnerID)
	if err != nil {
		return ForkFrameBranchResult{}, err
	}
	replacement, frameEvent, created, err := appendFrameAskUserResponseConn(ctx, conn, stream, replacementInput, now)
	if err != nil {
		return ForkFrameBranchResult{}, err
	}
	if !created {
		return ForkFrameBranchResult{}, ErrEventConflict
	}
	if err := validateBranchReplacementMembershipConn(ctx, conn, stream.UID, branchID, replacement.EventID, target.Ordinal); err != nil {
		return ForkFrameBranchResult{}, err
	}
	return ForkFrameBranchResult{
		BranchID: branchID, ParentBranchID: input.SourceBranchID, Generation: input.ExpectedGeneration + 1,
		ForkEventID: target.ResultEvent.EventID, ForkPoint: input.SourceMessageIndex,
		ReplacementEvent: replacement, ReplacementFrameEvent: frameEvent,
		RunnerCancellation: cancellation, Created: true,
	}, nil
}

func loadExistingFrameAskUserBranchConn(
	ctx context.Context,
	conn *sql.Conn,
	stream Stream,
	input ForkFrameAskUserAnswerBranchInput,
	requestDigest []byte,
	branchID string,
	replacementInput askUserBranchReplacement,
) (ForkFrameBranchResult, bool, error) {
	var storedBranchID, parentBranchID, kind, sourceMessageID string
	var forkEventID, forkPoint int64
	var storedDigest []byte
	err := conn.QueryRowContext(ctx, `
		SELECT branch_id,parent_branch_id,fork_event_id,fork_point,kind,request_sha256,source_message_id
		FROM transcript_branches WHERE stream_uid=? AND client_mutation_id=?`, stream.UID, input.ClientMutationID,
	).Scan(&storedBranchID, &parentBranchID, &forkEventID, &forkPoint, &kind, &storedDigest, &sourceMessageID)
	if errors.Is(err, sql.ErrNoRows) {
		return ForkFrameBranchResult{}, false, nil
	}
	if err != nil {
		return ForkFrameBranchResult{}, false, err
	}
	if storedBranchID != branchID || parentBranchID != input.SourceBranchID || kind != "answer" ||
		sourceMessageID != input.ToolUseID ||
		forkPoint != input.SourceMessageIndex || !equalBytes(storedDigest, requestDigest) {
		return ForkFrameBranchResult{}, false, ErrEventConflict
	}
	replacement, frameEvent, created, err := appendFrameAskUserResponseConn(ctx, conn, stream, replacementInput, time.Time{})
	if err != nil || created {
		if err != nil {
			return ForkFrameBranchResult{}, false, err
		}
		return ForkFrameBranchResult{}, false, ErrEventConflict
	}
	var sourceOrdinal int64
	if err := conn.QueryRowContext(ctx, `
		SELECT ordinal FROM transcript_branch_events WHERE stream_uid=? AND branch_id=? AND event_id=?`,
		stream.UID, input.SourceBranchID, forkEventID,
	).Scan(&sourceOrdinal); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ForkFrameBranchResult{}, false, ErrEventConflict
		}
		return ForkFrameBranchResult{}, false, err
	}
	if err := validateBranchReplacementMembershipConn(ctx, conn, stream.UID, branchID, replacement.EventID, sourceOrdinal); err != nil {
		return ForkFrameBranchResult{}, false, err
	}
	generation, err := activeBranchGenerationConn(ctx, conn, stream.UID, branchID)
	if err != nil {
		return ForkFrameBranchResult{}, false, err
	}
	return ForkFrameBranchResult{
		BranchID: branchID, ParentBranchID: parentBranchID, Generation: generation,
		ForkEventID: forkEventID, ForkPoint: forkPoint, ReplacementEvent: replacement,
		ReplacementFrameEvent: frameEvent, Created: false,
	}, true, nil
}

func resolveAskUserBranchTargetConn(
	ctx context.Context,
	conn *sql.Conn,
	stream Stream,
	branchID, toolUseID string,
) (askUserBranchTarget, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT event.event_id,event.publication_seq,event.client_message_id,event.event_type,event.source,
			event.runner_attempt,event.payload_json,event.frame_event_id,event.created_at,membership.ordinal
		FROM transcript_branch_events membership
		JOIN transcript_events event ON event.stream_uid=membership.stream_uid AND event.event_id=membership.event_id
		WHERE membership.stream_uid=? AND membership.branch_id=? ORDER BY membership.ordinal`, stream.UID, branchID)
	if err != nil {
		return askUserBranchTarget{}, err
	}
	defer rows.Close()
	toolFound := false
	var toolOrdinal int64
	var toolEvent Event
	questions := []string{}
	implementationLabels := map[string]map[string]string{}
	var result askUserBranchTarget
	resultFound := false
	for rows.Next() {
		var event Event
		var source string
		var runnerAttempt sql.NullInt64
		var frameEventID sql.NullString
		var ordinal int64
		if err := rows.Scan(
			&event.EventID, &event.PublicationSeq, &event.ClientMessageID, &event.Type, &source,
			&runnerAttempt, &event.PayloadJSON, &frameEventID, &event.CreatedAt, &ordinal,
		); err != nil {
			return askUserBranchTarget{}, err
		}
		event.StreamUID = stream.UID
		event.Source = EventSource(source)
		if runnerAttempt.Valid {
			value := runnerAttempt.Int64
			event.RunnerAttempt = &value
		}
		if frameEventID.Valid {
			value := frameEventID.String
			event.FrameEventID = &value
		}
		payload, err := resolveEventPayloadConn(ctx, conn, event)
		if err != nil {
			return askUserBranchTarget{}, err
		}
		blocks, err := branchMessageContentBlocks(payload)
		if err != nil {
			return askUserBranchTarget{}, err
		}
		for _, block := range blocks {
			blockType, _ := block["type"].(string)
			switch blockType {
			case "tool_use":
				id, _ := block["id"].(string)
				if strings.TrimSpace(id) != toolUseID {
					continue
				}
				name, _ := block["name"].(string)
				_, askUser := CanonicalAskUserToolNameV1(name)
				if toolFound || event.Type != "assistant_message" || event.Source != EventSourceFrameRef || !askUser {
					return askUserBranchTarget{}, ErrEventConflict
				}
				input, _ := block["input"].(map[string]any)
				questions, implementationLabels, err = branchAskUserQuestions(input)
				if err != nil || len(questions) == 0 {
					return askUserBranchTarget{}, ErrEventConflict
				}
				toolFound = true
				toolOrdinal = ordinal
				toolEvent = event
			case "tool_result":
				id, _ := block["tool_use_id"].(string)
				if strings.TrimSpace(id) != toolUseID {
					continue
				}
				if !toolFound || resultFound || ordinal <= toolOrdinal || event.Source != EventSourceFrameRef ||
					(event.Type != "user_message" && event.Type != "user_input_response") || !branchAskUserResultBlock(block) {
					return askUserBranchTarget{}, ErrEventConflict
				}
				result = askUserBranchTarget{
					ToolEvent: toolEvent, ResultEvent: event, Ordinal: ordinal, Questions: questions,
					ImplementationLabels: implementationLabels,
				}
				resultFound = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		return askUserBranchTarget{}, err
	}
	if !toolFound || !resultFound || result.Ordinal <= 0 {
		return askUserBranchTarget{}, ErrEventConflict
	}
	return result, nil
}

func branchMessageContentBlocks(payload []byte) ([]map[string]any, error) {
	var message map[string]any
	if len(payload) == 0 || json.Unmarshal(payload, &message) != nil {
		return nil, ErrEventConflict
	}
	rawBlocks, ok := message["content"].([]any)
	if !ok {
		return nil, nil
	}
	blocks := make([]map[string]any, 0, len(rawBlocks))
	for _, raw := range rawBlocks {
		block, ok := raw.(map[string]any)
		if !ok {
			return nil, ErrEventConflict
		}
		blocks = append(blocks, block)
	}
	return blocks, nil
}

func branchAskUserQuestions(input map[string]any) ([]string, map[string]map[string]string, error) {
	questions := []string{}
	seen := map[string]struct{}{}
	implementationLabels := map[string]map[string]string{}
	appendQuestion := func(value string) error {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 4096 || len(questions) >= 16 {
			return ErrEventConflict
		}
		if _, found := seen[value]; found {
			return ErrEventConflict
		}
		seen[value] = struct{}{}
		questions = append(questions, value)
		return nil
	}
	if raw, found := input["question"]; found {
		value, ok := raw.(string)
		if !ok || appendQuestion(value) != nil {
			return nil, nil, ErrEventConflict
		}
		branchAskUserImplementationLabels(strings.TrimSpace(value), input["options"], implementationLabels)
	}
	if raw, found := input["questions"]; found {
		values, ok := raw.([]any)
		if !ok || len(values) == 0 {
			return nil, nil, ErrEventConflict
		}
		for _, raw := range values {
			switch value := raw.(type) {
			case string:
				if appendQuestion(value) != nil {
					return nil, nil, ErrEventConflict
				}
			case map[string]any:
				question, ok := value["question"].(string)
				if !ok || appendQuestion(question) != nil {
					return nil, nil, ErrEventConflict
				}
				branchAskUserImplementationLabels(strings.TrimSpace(question), value["options"], implementationLabels)
			default:
				return nil, nil, ErrEventConflict
			}
		}
	}
	return questions, implementationLabels, nil
}

func branchAskUserImplementationLabels(
	question string,
	rawOptions any,
	destination map[string]map[string]string,
) {
	question = strings.TrimSpace(question)
	if question == "" {
		return
	}
	options, ok := rawOptions.([]any)
	if !ok {
		return
	}
	for _, raw := range options {
		option, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		label, _ := option["label"].(string)
		label = strings.TrimSpace(label)
		implementation, _ := option["implementation"].(string)
		implementation = strings.TrimSpace(implementation)
		if implementation == "" {
			if metadata, ok := option["metadata"].(map[string]any); ok {
				implementation, _ = metadata["implementation"].(string)
				implementation = strings.TrimSpace(implementation)
			}
		}
		if label == "" || implementation == "" {
			continue
		}
		labels := destination[question]
		if labels == nil {
			labels = map[string]string{}
			destination[question] = labels
		}
		labels[label] = implementation
		publicLabel := label
		if !strings.Contains(strings.ToLower(label), strings.ToLower(implementation)) {
			publicLabel += " · " + implementation
		}
		labels[publicLabel] = implementation
	}
}

func branchAskUserResultBlock(block map[string]any) bool {
	content, _ := block["content"].(string)
	if _, err := DecodeAskUserResultV1([]byte(content)); err == nil {
		return true
	}
	// Retain the pre-v1 pending projection during migration. Terminal current
	// projections always use the strict versioned AskUserResultV1 decoder above.
	var state map[string]any
	if json.Unmarshal([]byte(content), &state) != nil || len(state) != 1 {
		return false
	}
	status, _ := state["status"].(string)
	return status == "awaiting_user_response"
}

func buildAskUserBranchResult(
	response AskUserBranchResponse,
	questions []string,
	implementationLabels map[string]map[string]string,
) ([]byte, string, error) {
	result := map[string]any{"version": 1, "action": response.Action}
	continuation := ""
	switch response.Action {
	case "answer":
		if len(questions) > 0 {
			for _, question := range questions {
				if _, found := response.Answers[question]; !found {
					return nil, "", errors.New("AskUser answer does not cover every question")
				}
			}
		}
		result["status"] = "answered"
		result["answers"] = response.Answers
		selectedImplementations := map[string]string{}
		for question, answer := range response.Answers {
			if implementation := strings.TrimSpace(implementationLabels[question][answer]); implementation != "" {
				selectedImplementations[question] = implementation
			}
		}
		if len(selectedImplementations) == 0 {
			selectedImplementations = nil
		}
		var err error
		continuation, err = EncodeAnsweredAskUserModelContinuation(response.Answers, selectedImplementations)
		if err != nil {
			return nil, "", err
		}
	case "decide_for_me":
		result["status"] = "deferred"
		continuation = "User delegated this choice. Decide only within the unchanged canonical task: preserve every explicit priority and requirement, do not turn a suggested preference into a new hard constraint, and choose the option that best satisfies the canonical objective."
	case "discuss":
		if response.Message == "" {
			return nil, "", errors.New("AskUser discuss response requires a message")
		}
		result["status"] = "discussed"
		result["message"] = response.Message
		continuation = "The user wants to discuss these questions further. Their message: " + response.Message + ". Respond to their input, then ask again if answers are still required."
	case "cancel":
		result["status"] = "cancelled"
		continuation = "User cancelled the question. Continue without an answer, using best judgment or skipping this step."
	default:
		return nil, "", errors.New("unsupported AskUser branch action")
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > maxEventPayloadBytes || len(continuation) > maxEventPayloadBytes {
		return nil, "", errors.New("AskUser branch result exceeds the durable limit")
	}
	return encoded, continuation, nil
}

type askUserBranchReplacement struct {
	ClientMessageID string
	FrameEventID    string
	PayloadJSON     []byte
	Destinations    []string
}

func askUserBranchReplacementInput(
	stream Stream,
	input ForkFrameAskUserAnswerBranchInput,
	branchID string,
	structuredResult []byte,
	modelContinuation string,
) (askUserBranchReplacement, error) {
	clientMessageID := "branch-answer:" + input.ClientMutationID
	messageUUID := "branch-answer:" + branchID
	payloadObject := map[string]any{
		"messageUuid": messageUUID, "clientMessageId": clientMessageID,
		"text": modelContinuation, "role": "user", "_uuid": messageUUID, "messageOrigin": "input_response",
		"content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": input.ToolUseID, "content": string(structuredResult),
		}},
	}
	if len(input.RuntimeConfig) > 0 {
		payloadObject["runtimeConfig"] = input.RuntimeConfig
	}
	payload, err := json.Marshal(payloadObject)
	if err != nil || len(payload) > maxEventPayloadBytes {
		return askUserBranchReplacement{}, errors.New("AskUser replacement payload is invalid")
	}
	return askUserBranchReplacement{
		ClientMessageID: clientMessageID,
		FrameEventID:    "frame-branch-answer:" + stream.FrameID + ":" + branchID,
		PayloadJSON:     payload,
		Destinations:    input.Destinations,
	}, nil
}

func appendFrameAskUserResponseConn(
	ctx context.Context,
	conn *sql.Conn,
	stream Stream,
	input askUserBranchReplacement,
	now time.Time,
) (Event, FrameReferenceEvent, bool, error) {
	record := eventRecord{
		clientMessageID: input.ClientMessageID, eventType: "user_input_response", source: EventSourceFrameRef,
		frameEventID: &input.FrameEventID, destinations: input.Destinations, createdAt: now,
	}
	if existing, found, err := findEventByClientID(ctx, conn, stream.UID, input.ClientMessageID); err != nil {
		return Event{}, FrameReferenceEvent{}, false, err
	} else if found {
		record.createdAt = existing.CreatedAt
		if !eventMatches(existing, record) {
			return Event{}, FrameReferenceEvent{}, false, ErrEventConflict
		}
		frameEvent, err := loadMatchingFrameReferenceEvent(
			ctx, conn, stream.FrameID, input.FrameEventID, "user_input_response", input.PayloadJSON,
		)
		if err != nil {
			return Event{}, FrameReferenceEvent{}, false, err
		}
		event, created, err := appendEventConn(ctx, conn, stream, record)
		if err != nil || created || event.EventID != existing.EventID {
			if err != nil {
				return Event{}, FrameReferenceEvent{}, false, err
			}
			return Event{}, FrameReferenceEvent{}, false, ErrEventConflict
		}
		return event, frameEvent, false, nil
	}
	frameEvent, err := insertOrValidateFrameReferenceEvent(
		ctx, conn, stream.FrameID, input.FrameEventID, "user_input_response", input.PayloadJSON, now,
	)
	if err != nil {
		return Event{}, FrameReferenceEvent{}, false, err
	}
	event, created, err := appendEventConn(ctx, conn, stream, record)
	if err != nil || !created {
		return Event{}, FrameReferenceEvent{}, false, err
	}
	if _, err := conn.ExecContext(ctx, `
		UPDATE transcript_streams SET input_revision=input_revision+1,updated_at=? WHERE stream_uid=?`, now, stream.UID,
	); err != nil {
		return Event{}, FrameReferenceEvent{}, false, err
	}
	if err := activateFrameForNewInputConn(ctx, conn, stream, now); err != nil {
		return Event{}, FrameReferenceEvent{}, false, err
	}
	return event, frameEvent, true, nil
}

func normalizedAskUserAnswers(values map[string]string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" || len(key) > 4096 || len(value) > 64<<10 {
			return nil, errors.New("AskUser answers contain an invalid question or value")
		}
		if _, duplicate := result[key]; duplicate {
			return nil, errors.New("AskUser answers contain duplicate normalized questions")
		}
		result[key] = value
	}
	return result, nil
}

func validAskUserBranchAction(value string) bool {
	return value == "answer" || value == "decide_for_me" || value == "discuss" || value == "cancel"
}
