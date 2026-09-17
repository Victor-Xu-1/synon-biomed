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

type BranchState struct {
	StreamUID      string
	ActiveBranchID string
	Generation     int64
	UpdatedAt      time.Time
}

type ForkFrameUserMessageBranchInput struct {
	StreamUID              string
	OwnerID                string
	SourceBranchID         string
	ExpectedActiveBranchID string
	ExpectedGeneration     int64
	ClientMutationID       string
	SourceClientMessageID  string
	SourceMessageIndex     int64
	ReplacementText        string
	RuntimeConfig          map[string]any
	Destinations           []string
}

type ForkFrameBranchResult struct {
	BranchID              string
	ParentBranchID        string
	Generation            int64
	ForkEventID           int64
	ForkPoint             int64
	ReplacementEvent      Event
	ReplacementFrameEvent FrameReferenceEvent
	RunnerCancellation    CancelRunnerResult
	Created               bool
}

type frameUserMessageBranchDigest struct {
	Version               int            `json:"version"`
	StreamUID             string         `json:"stream_uid"`
	OwnerID               string         `json:"owner_id"`
	SourceBranchID        string         `json:"source_branch_id"`
	ClientMutationID      string         `json:"client_mutation_id"`
	SourceClientMessageID string         `json:"source_client_message_id"`
	SourceMessageIndex    int64          `json:"source_message_index"`
	ReplacementText       string         `json:"replacement_text"`
	RuntimeConfig         map[string]any `json:"runtime_config,omitempty"`
	Destinations          []string       `json:"destinations"`
}

type branchSourceTarget struct {
	Event           Event
	Ordinal         int64
	MessageIdentity string
}

type branchMutationPlan struct {
	BranchID               string
	Kind                   string
	SourceBranchID         string
	ExpectedActiveBranchID string
	ExpectedGeneration     int64
	ClientMutationID       string
	RequestDigest          []byte
	SourceMessageID        string
	SourceEventID          int64
	SourceOrdinal          int64
	SourceMessageIndex     int64
}

func BaseBranchIdentity(streamUID string) (string, string, []byte) {
	branchDigest := sha256.Sum256([]byte("base-branch\x00" + streamUID))
	requestDigest := sha256.Sum256([]byte("base-mutation\x00" + streamUID))
	return "br_" + hex.EncodeToString(branchDigest[:4]), "base:" + streamUID, requestDigest[:]
}

func (r *Repository) ForkFrameUserMessageBranch(
	ctx context.Context,
	input ForkFrameUserMessageBranchInput,
) (ForkFrameBranchResult, error) {
	input, requestDigest, branchID, err := normalizeFrameUserMessageBranchInput(input)
	if err != nil {
		return ForkFrameBranchResult{}, err
	}
	var result ForkFrameBranchResult
	err = r.withImmediate(ctx, func(conn *sql.Conn) error {
		var err error
		result, err = r.forkFrameUserMessageBranchConn(ctx, conn, input, requestDigest, branchID)
		return err
	})
	return result, schemaError(err)
}

func (tx *ImmediateTransaction) ForkFrameUserMessageBranch(
	ctx context.Context,
	input ForkFrameUserMessageBranchInput,
) (ForkFrameBranchResult, error) {
	if tx == nil || tx.repository == nil || tx.conn == nil {
		return ForkFrameBranchResult{}, errors.New("transcript transaction is required")
	}
	input, requestDigest, branchID, err := normalizeFrameUserMessageBranchInput(input)
	if err != nil {
		return ForkFrameBranchResult{}, err
	}
	result, err := tx.repository.forkFrameUserMessageBranchConn(ctx, tx.conn, input, requestDigest, branchID)
	return result, schemaError(err)
}

func normalizeFrameUserMessageBranchInput(
	input ForkFrameUserMessageBranchInput,
) (ForkFrameUserMessageBranchInput, []byte, string, error) {
	input.StreamUID = strings.TrimSpace(input.StreamUID)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.SourceBranchID = strings.TrimSpace(input.SourceBranchID)
	input.ExpectedActiveBranchID = strings.TrimSpace(input.ExpectedActiveBranchID)
	input.ClientMutationID = strings.TrimSpace(input.ClientMutationID)
	input.SourceClientMessageID = strings.TrimSpace(input.SourceClientMessageID)
	input.ReplacementText = strings.TrimSpace(input.ReplacementText)
	input.Destinations = normalizedDestinations(input.Destinations)
	if input.StreamUID == "" || input.OwnerID == "" || !validTranscriptBranchID(input.SourceBranchID) ||
		!validTranscriptBranchID(input.ExpectedActiveBranchID) || input.ExpectedGeneration <= 0 ||
		!validBranchMutationID(input.ClientMutationID) || input.SourceClientMessageID == "" ||
		len(input.SourceClientMessageID) > 512 || input.SourceMessageIndex < 0 ||
		input.ReplacementText == "" || len(input.Destinations) == 0 {
		return ForkFrameUserMessageBranchInput{}, nil, "", errors.New("valid stream, owner, branches, generation, mutation, source message, replacement, and destination are required")
	}
	digestInput := frameUserMessageBranchDigest{
		Version: 1, StreamUID: input.StreamUID, OwnerID: input.OwnerID,
		SourceBranchID: input.SourceBranchID, ClientMutationID: input.ClientMutationID,
		SourceClientMessageID: input.SourceClientMessageID, SourceMessageIndex: input.SourceMessageIndex,
		ReplacementText: input.ReplacementText,
		RuntimeConfig:   input.RuntimeConfig, Destinations: input.Destinations,
	}
	encoded, err := json.Marshal(digestInput)
	if err != nil || len(encoded) > maxEventPayloadBytes {
		return ForkFrameUserMessageBranchInput{}, nil, "", errors.New("branch request is not serializable within the durable limit")
	}
	requestDigest := sha256.Sum256(encoded)
	identityDigest := sha256.Sum256(append([]byte("edit-branch\x00"+input.StreamUID+"\x00"+input.ClientMutationID+"\x00"), requestDigest[:]...))
	return input, requestDigest[:], "br_" + hex.EncodeToString(identityDigest[:4]), nil
}

func (r *Repository) forkFrameUserMessageBranchConn(
	ctx context.Context,
	conn *sql.Conn,
	input ForkFrameUserMessageBranchInput,
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
	replacementInput := branchReplacementFrameInput(stream, input, branchID)
	if existing, found, err := loadExistingFrameUserMessageBranchConn(
		ctx, conn, stream, input, requestDigest, branchID, replacementInput,
	); err != nil {
		return ForkFrameBranchResult{}, err
	} else if found {
		return existing, nil
	}
	source, err := resolveFrameUserMessageBranchTargetConn(ctx, conn, stream, input.SourceBranchID, input.SourceClientMessageID)
	if err != nil {
		return ForkFrameBranchResult{}, err
	}
	messageIndex, err := branchWebMessageIndexConn(ctx, conn, stream.UID, input.SourceBranchID, source.Event.EventID)
	if err != nil {
		return ForkFrameBranchResult{}, err
	}
	if messageIndex != input.SourceMessageIndex {
		return ForkFrameBranchResult{}, ErrEventConflict
	}
	now := r.now().UTC()
	plan := branchMutationPlan{
		BranchID: branchID, Kind: "edit", SourceBranchID: input.SourceBranchID,
		ExpectedActiveBranchID: input.ExpectedActiveBranchID, ExpectedGeneration: input.ExpectedGeneration,
		ClientMutationID: input.ClientMutationID, RequestDigest: requestDigest,
		SourceMessageID: source.MessageIdentity, SourceEventID: source.Event.EventID,
		SourceOrdinal: source.Ordinal, SourceMessageIndex: input.SourceMessageIndex,
	}
	cancellation, err := r.beginFrameBranchMutationConn(ctx, conn, stream, plan, now)
	if err != nil {
		return ForkFrameBranchResult{}, err
	}
	replacement, frameEvent, created, err := appendFrameUserEventConn(ctx, conn, replacementInput, now)
	if err != nil {
		return ForkFrameBranchResult{}, err
	}
	if !created {
		return ForkFrameBranchResult{}, ErrEventConflict
	}
	if err := validateBranchReplacementMembershipConn(ctx, conn, stream.UID, branchID, replacement.EventID, source.Ordinal); err != nil {
		return ForkFrameBranchResult{}, err
	}
	return ForkFrameBranchResult{
		BranchID: branchID, ParentBranchID: input.SourceBranchID, Generation: input.ExpectedGeneration + 1,
		ForkEventID: source.Event.EventID, ForkPoint: input.SourceMessageIndex, ReplacementEvent: replacement,
		ReplacementFrameEvent: frameEvent, RunnerCancellation: cancellation, Created: true,
	}, nil
}

func (r *Repository) beginFrameBranchMutationConn(
	ctx context.Context,
	conn *sql.Conn,
	stream Stream,
	plan branchMutationPlan,
	now time.Time,
) (CancelRunnerResult, error) {
	var activeBranchID string
	var generation int64
	if err := conn.QueryRowContext(ctx, `
		SELECT active_branch_id,generation FROM transcript_branch_state WHERE stream_uid=?`, stream.UID,
	).Scan(&activeBranchID, &generation); err != nil {
		return CancelRunnerResult{}, err
	}
	if activeBranchID != plan.ExpectedActiveBranchID || generation != plan.ExpectedGeneration {
		return CancelRunnerResult{}, ErrBranchStateStale
	}
	cancellation, err := cancelRunningRunnerForBranchConn(ctx, conn, stream, plan.ClientMutationID, now)
	if err != nil {
		return CancelRunnerResult{}, err
	}
	if _, err := conn.ExecContext(ctx, `
		UPDATE transcript_streams SET consumed_input_revision=MAX(consumed_input_revision,input_revision),updated_at=?
		WHERE stream_uid=?`, now, stream.UID); err != nil {
		return CancelRunnerResult{}, err
	}
	if err := clearSupersededFrameInputStateConn(ctx, conn, stream); err != nil {
		return CancelRunnerResult{}, err
	}
	var collidingMutation string
	err = conn.QueryRowContext(ctx, `
		SELECT client_mutation_id FROM transcript_branches WHERE stream_uid=? AND branch_id=?`, stream.UID, plan.BranchID,
	).Scan(&collidingMutation)
	if err == nil {
		return CancelRunnerResult{}, ErrEventConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return CancelRunnerResult{}, err
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO transcript_branches(
			stream_uid,branch_id,parent_branch_id,fork_event_id,fork_point,kind,
			client_mutation_id,request_sha256,source_message_id,created_at,updated_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		stream.UID, plan.BranchID, plan.SourceBranchID, plan.SourceEventID, plan.SourceMessageIndex, plan.Kind,
		plan.ClientMutationID, plan.RequestDigest, plan.SourceMessageID, now, now,
	); err != nil {
		return CancelRunnerResult{}, err
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO transcript_branch_events(stream_uid,branch_id,ordinal,event_id)
		SELECT stream_uid,?,ordinal,event_id FROM transcript_branch_events
		WHERE stream_uid=? AND branch_id=? AND ordinal<? ORDER BY ordinal`,
		plan.BranchID, stream.UID, plan.SourceBranchID, plan.SourceOrdinal,
	); err != nil {
		return CancelRunnerResult{}, err
	}
	updated, err := conn.ExecContext(ctx, `
		UPDATE transcript_branch_state SET active_branch_id=?,generation=generation+1,updated_at=?
		WHERE stream_uid=? AND active_branch_id=? AND generation=?`,
		plan.BranchID, now, stream.UID, plan.ExpectedActiveBranchID, plan.ExpectedGeneration,
	)
	if err != nil {
		return CancelRunnerResult{}, err
	}
	if count, err := updated.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return CancelRunnerResult{}, err
		}
		return CancelRunnerResult{}, ErrBranchStateStale
	}
	return cancellation, nil
}

func clearSupersededFrameInputStateConn(ctx context.Context, conn *sql.Conn, stream Stream) error {
	if stream.Kind != StreamKindFrameRef || stream.FrameID == "" {
		return nil
	}
	var rawContext string
	err := conn.QueryRowContext(ctx, `SELECT context_data FROM frame_runtime_metadata WHERE frame_id=?`, stream.FrameID).
		Scan(&rawContext)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	contextData := map[string]any{}
	if json.Unmarshal([]byte(rawContext), &contextData) != nil {
		return ErrEventConflict
	}
	changed := false
	for _, key := range []string{
		"_pending_input_requests", "_ask_user_payload", "_pending_access_request", "_resolved_input_tool_ids",
	} {
		if _, found := contextData[key]; found {
			delete(contextData, key)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	updated, err := json.Marshal(contextData)
	if err != nil || len(updated) > maxEventPayloadBytes {
		return ErrEventConflict
	}
	_, err = conn.ExecContext(ctx, `UPDATE frame_runtime_metadata SET context_data=? WHERE frame_id=?`, string(updated), stream.FrameID)
	return err
}

func loadExistingFrameUserMessageBranchConn(
	ctx context.Context,
	conn *sql.Conn,
	stream Stream,
	input ForkFrameUserMessageBranchInput,
	requestDigest []byte,
	branchID string,
	replacementInput AppendFrameUserEventInput,
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
	if storedBranchID != branchID || parentBranchID != input.SourceBranchID || kind != "edit" || sourceMessageID == "" ||
		forkPoint != input.SourceMessageIndex || !equalBytes(storedDigest, requestDigest) {
		return ForkFrameBranchResult{}, false, ErrEventConflict
	}
	replacement, found, err := findEventByClientID(ctx, conn, stream.UID, replacementInput.ClientMessageID)
	if err != nil {
		return ForkFrameBranchResult{}, false, err
	}
	if !found {
		return ForkFrameBranchResult{}, false, ErrEventConflict
	}
	validated, frameEvent, created, err := appendFrameUserEventConn(ctx, conn, replacementInput, replacement.CreatedAt)
	if err != nil || created || validated.EventID != replacement.EventID {
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

func activeBranchGenerationConn(ctx context.Context, conn *sql.Conn, streamUID, branchID string) (int64, error) {
	var activeBranchID string
	var generation int64
	if err := conn.QueryRowContext(ctx, `
		SELECT active_branch_id,generation FROM transcript_branch_state WHERE stream_uid=?`, streamUID,
	).Scan(&activeBranchID, &generation); err != nil {
		return 0, err
	}
	if activeBranchID != branchID || generation <= 0 {
		return 0, ErrBranchStateStale
	}
	return generation, nil
}

func resolveFrameUserMessageBranchTargetConn(
	ctx context.Context,
	conn *sql.Conn,
	stream Stream,
	branchID, clientMessageID string,
) (branchSourceTarget, error) {
	event, found, err := findEventByClientID(ctx, conn, stream.UID, clientMessageID)
	if err != nil {
		return branchSourceTarget{}, err
	}
	if !found || event.Type != "user_message" ||
		(event.Source != EventSourceFrameRef && event.Source != EventSourcePayload) ||
		(event.Source == EventSourceFrameRef && event.FrameEventID == nil) ||
		(event.Source == EventSourcePayload && event.FrameEventID != nil) {
		return branchSourceTarget{}, ErrEventConflict
	}
	var ordinal int64
	if err := conn.QueryRowContext(ctx, `
		SELECT ordinal FROM transcript_branch_events WHERE stream_uid=? AND branch_id=? AND event_id=?`,
		stream.UID, branchID, event.EventID,
	).Scan(&ordinal); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return branchSourceTarget{}, ErrEventConflict
		}
		return branchSourceTarget{}, err
	}
	payload, err := resolveEventPayloadConn(ctx, conn, event)
	if err != nil {
		return branchSourceTarget{}, err
	}
	var message map[string]any
	if len(payload) == 0 || json.Unmarshal(payload, &message) != nil {
		return branchSourceTarget{}, ErrEventConflict
	}
	messageUUID, _ := message["messageUuid"].(string)
	legacyUUID, _ := message["_uuid"].(string)
	messageUUID, legacyUUID = strings.TrimSpace(messageUUID), strings.TrimSpace(legacyUUID)
	if ordinal <= 0 || messageUUID == "" || messageUUID != legacyUUID || len(messageUUID) > 512 {
		return branchSourceTarget{}, ErrEventConflict
	}
	return branchSourceTarget{Event: event, Ordinal: ordinal, MessageIdentity: messageUUID}, nil
}

func branchReplacementFrameInput(stream Stream, input ForkFrameUserMessageBranchInput, branchID string) AppendFrameUserEventInput {
	return AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		ClientMessageID: "branch-edit:" + input.ClientMutationID,
		FrameEventID:    "frame-branch-edit:" + stream.FrameID + ":" + branchID,
		MessageUUID:     "branch-edit:" + branchID, Text: input.ReplacementText, MessageOrigin: "task_intent",
		RuntimeConfig: input.RuntimeConfig, Destinations: input.Destinations,
	}
}

func branchWebMessageIndexConn(
	ctx context.Context,
	conn *sql.Conn,
	streamUID, branchID string,
	targetEventID int64,
) (int64, error) {
	result := int64(-1)
	err := visitBranchWebMessagePositionsConn(ctx, conn, streamUID, branchID, func(event Event, index int64) (bool, error) {
		if event.EventID != targetEventID {
			return false, nil
		}
		result = index
		return true, nil
	})
	if err != nil {
		return 0, err
	}
	if result < 0 {
		return 0, ErrEventConflict
	}
	return result, nil
}

func visitBranchWebMessagePositionsConn(
	ctx context.Context,
	conn *sql.Conn,
	streamUID, branchID string,
	visit func(Event, int64) (bool, error),
) error {
	rows, err := conn.QueryContext(ctx, `
		SELECT event.event_id,event.client_message_id,event.event_type,event.source,event.runner_attempt,
			CASE WHEN event.source='frame_ref' THEN frame.payload ELSE event.payload_json END
		FROM transcript_branch_events membership
		JOIN transcript_events event ON event.stream_uid=membership.stream_uid AND event.event_id=membership.event_id
		LEFT JOIN frame_events frame ON event.source='frame_ref' AND frame.id=event.frame_event_id
		WHERE membership.stream_uid=? AND membership.branch_id=? ORDER BY membership.ordinal`, streamUID, branchID)
	if err != nil {
		return err
	}
	defer rows.Close()
	messageIndex := int64(-1)
	assistantAttempts := map[int64]struct{}{}
	askUserIndices := map[string]int64{}
	for rows.Next() {
		var event Event
		var source string
		var runnerAttempt sql.NullInt64
		var payload []byte
		if err := rows.Scan(
			&event.EventID, &event.ClientMessageID, &event.Type, &source, &runnerAttempt, &payload,
		); err != nil {
			return err
		}
		event.StreamUID = streamUID
		event.Source = EventSource(source)
		if runnerAttempt.Valid {
			value := runnerAttempt.Int64
			event.RunnerAttempt = &value
		}
		switch event.Type {
		case "user_message", "user_input_response", "history_user_message":
			if EventSource(source) == EventSourceFrameRef {
				fact, callID, found, err := branchAskUserProtocolFact(payload)
				if err != nil {
					return err
				}
				if found {
					if fact != "result" {
						return ErrEventConflict
					}
					index, exists := askUserIndices[callID]
					if !exists || index != messageIndex {
						return ErrEventConflict
					}
					break
				}
			}
			messageIndex++
		case "content_delta", "content_reset", "assistant_message", "history_assistant_message":
			if runnerAttempt.Valid {
				if _, found := assistantAttempts[runnerAttempt.Int64]; !found {
					assistantAttempts[runnerAttempt.Int64] = struct{}{}
					messageIndex++
				}
			} else if event.Type == "assistant_message" && EventSource(source) == EventSourceFrameRef {
				fact, callID, found, err := branchAskUserProtocolFact(payload)
				if err != nil {
					return err
				}
				messageIndex++
				if found {
					if fact != "use" {
						return ErrEventConflict
					}
					if _, duplicate := askUserIndices[callID]; duplicate {
						return ErrEventConflict
					}
					askUserIndices[callID] = messageIndex
				}
			} else if (event.Type != "assistant_message" && event.Type != "history_assistant_message") ||
				EventSource(source) != EventSourcePayload {
				return ErrEventConflict
			}
		case "history_system_message":
			if EventSource(source) != EventSourcePayload {
				return ErrEventConflict
			}
		case "runner_finished":
			if !runnerAttempt.Valid {
				return ErrEventConflict
			}
			if _, found := assistantAttempts[runnerAttempt.Int64]; !found {
				assistantAttempts[runnerAttempt.Int64] = struct{}{}
				messageIndex++
			}
		}
		if messageIndex < 0 {
			continue
		}
		done, err := visit(event, messageIndex)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}

func branchAskUserProtocolFact(payload []byte) (string, string, bool, error) {
	var message map[string]any
	if len(payload) == 0 || len(payload) > maxEventPayloadBytes || json.Unmarshal(payload, &message) != nil {
		return "", "", false, ErrEventConflict
	}
	blocks, ok := message["content"].([]any)
	if !ok {
		return "", "", false, nil
	}
	var fact, callID string
	for _, value := range blocks {
		block, ok := value.(map[string]any)
		if !ok {
			return "", "", false, ErrEventConflict
		}
		blockType, _ := block["type"].(string)
		switch strings.TrimSpace(blockType) {
		case "tool_use":
			name, _ := block["name"].(string)
			id, _ := block["id"].(string)
			if _, ok := CanonicalAskUserToolNameV1(name); !ok || strings.TrimSpace(id) == "" || fact != "" {
				return "", "", false, ErrEventConflict
			}
			fact, callID = "use", strings.TrimSpace(id)
		case "tool_result":
			id, _ := block["tool_use_id"].(string)
			if strings.TrimSpace(id) == "" || fact != "" {
				return "", "", false, ErrEventConflict
			}
			fact, callID = "result", strings.TrimSpace(id)
		}
	}
	return fact, callID, fact != "", nil
}

func cancelRunningRunnerForBranchConn(
	ctx context.Context,
	conn *sql.Conn,
	stream Stream,
	mutationID string,
	now time.Time,
) (CancelRunnerResult, error) {
	var attempt int64
	var status string
	err := conn.QueryRowContext(ctx, `
		SELECT attempt,status FROM transcript_runner_attempts WHERE stream_uid=? ORDER BY attempt DESC LIMIT 1`, stream.UID,
	).Scan(&attempt, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return CancelRunnerResult{}, nil
	}
	if err != nil {
		return CancelRunnerResult{}, err
	}
	if status != "running" {
		return CancelRunnerResult{}, nil
	}
	return cancelRunnerConn(ctx, conn, now, CancelRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ExpectedAttempt: attempt,
		ClientMessageID: "branch-cancel:" + mutationID, ReasonCode: "branch_superseded", Destinations: []string{"ws"},
	})
}

func validateBranchReplacementMembershipConn(
	ctx context.Context,
	conn *sql.Conn,
	streamUID, branchID string,
	eventID, expectedOrdinal int64,
) error {
	var ordinal int64
	if err := conn.QueryRowContext(ctx, `
		SELECT ordinal FROM transcript_branch_events WHERE stream_uid=? AND branch_id=? AND event_id=?`,
		streamUID, branchID, eventID,
	).Scan(&ordinal); err != nil {
		return err
	}
	if ordinal != expectedOrdinal {
		return ErrEventConflict
	}
	return nil
}

func validTranscriptBranchID(value string) bool {
	if len(value) != 11 || !strings.HasPrefix(value, "br_") {
		return false
	}
	for _, character := range value[3:] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validBranchMutationID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character != '-' && character != '_' && character != '.' &&
			(character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func ensureBaseBranchConn(ctx context.Context, conn *sql.Conn, stream Stream, now time.Time) error {
	branchID, mutationID, requestDigest := BaseBranchIdentity(stream.UID)
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO transcript_branches(
			stream_uid,branch_id,parent_branch_id,fork_event_id,fork_point,kind,
			client_mutation_id,request_sha256,source_message_id,created_at,updated_at
		) VALUES(?,?,NULL,NULL,0,'base',?,?, '',?,?)
		ON CONFLICT(stream_uid,branch_id) DO NOTHING`,
		stream.UID, branchID, mutationID, requestDigest, now, now,
	); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO transcript_branch_state(stream_uid,active_branch_id,generation,updated_at)
		VALUES(?,?,1,?) ON CONFLICT(stream_uid) DO NOTHING`, stream.UID, branchID, now); err != nil {
		return err
	}
	var storedBranchID, storedMutationID string
	var storedDigest []byte
	if err := conn.QueryRowContext(ctx, `
		SELECT branch_id,client_mutation_id,request_sha256 FROM transcript_branches
		WHERE stream_uid=? AND kind='base'`, stream.UID,
	).Scan(&storedBranchID, &storedMutationID, &storedDigest); err != nil {
		return err
	}
	if storedBranchID != branchID || storedMutationID != mutationID || !equalBytes(storedDigest, requestDigest) {
		return ErrEventConflict
	}
	return nil
}

func appendActiveBranchEventConn(ctx context.Context, conn *sql.Conn, streamUID string, eventID int64) error {
	var branchID string
	if err := conn.QueryRowContext(ctx, `
		SELECT active_branch_id FROM transcript_branch_state WHERE stream_uid=?`, streamUID,
	).Scan(&branchID); err != nil {
		return err
	}
	var ordinal int64
	if err := conn.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(ordinal),0)+1 FROM transcript_branch_events
		WHERE stream_uid=? AND branch_id=?`, streamUID, branchID,
	).Scan(&ordinal); err != nil {
		return err
	}
	_, err := conn.ExecContext(ctx, `
		INSERT INTO transcript_branch_events(stream_uid,branch_id,ordinal,event_id)
		VALUES(?,?,?,?)`, streamUID, branchID, ordinal, eventID)
	return err
}

func validateExistingEventBranchMembership(ctx context.Context, conn *sql.Conn, streamUID string, eventID int64) error {
	var count int
	if err := conn.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM transcript_branch_events WHERE stream_uid=? AND event_id=?`, streamUID, eventID,
	).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return ErrEventConflict
	}
	return nil
}

func (r *Repository) GetBranchState(ctx context.Context, streamUID, ownerID string) (BranchState, error) {
	if r == nil || r.db == nil {
		return BranchState{}, ErrSchemaUnavailable
	}
	streamUID, ownerID = strings.TrimSpace(streamUID), strings.TrimSpace(ownerID)
	if streamUID == "" || ownerID == "" {
		return BranchState{}, errors.New("stream and owner are required")
	}
	var state BranchState
	var storedOwner string
	err := r.db.QueryRowContext(ctx, `
		SELECT state.stream_uid,stream.owner_id,state.active_branch_id,state.generation,state.updated_at
		FROM transcript_branch_state state
		JOIN transcript_streams stream ON stream.stream_uid=state.stream_uid
		WHERE state.stream_uid=?`, streamUID,
	).Scan(&state.StreamUID, &storedOwner, &state.ActiveBranchID, &state.Generation, &state.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return BranchState{}, ErrEventConflict
	}
	if err != nil {
		return BranchState{}, schemaError(err)
	}
	if storedOwner != ownerID {
		return BranchState{}, ErrOwnerMismatch
	}
	return state, nil
}

func equalBytes(left, right []byte) bool {
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
