package workspace

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	askUserTranscriptOriginsKey = "_ask_user_transcript_origins"
	askUserTranscriptToolIDsKey = "_ask_user_transcript_tool_ids"
	inputTranscriptStreamUIDKey = "_input_transcript_stream_uid"
)

type ParkAskUserInput struct {
	FrameID   string
	ToolID    string
	ToolName  string
	Questions []any
}

type ParkAskUserResult struct {
	Events         []FrameEvent
	AlreadyPending bool
}

func (s *Store) ParkAskUser(input ParkAskUserInput) (ParkAskUserResult, error) {
	if s == nil || s.db == nil {
		return ParkAskUserResult{}, errors.New("workspace store is closed")
	}
	var err error
	if input, err = normalizeParkAskUserInput(input); err != nil {
		return ParkAskUserResult{}, err
	}
	s.branchMu.Lock()
	defer s.branchMu.Unlock()
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ParkAskUserResult{}, fmt.Errorf("begin ask-user park: %w", err)
	}
	defer tx.Rollback()
	result, err := s.parkAskUserInTransaction(ctx, tx, input)
	if err != nil {
		return ParkAskUserResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ParkAskUserResult{}, fmt.Errorf("commit ask-user park: %w", err)
	}
	s.signalKernelRetentionWake()
	return result, nil
}

// ParkAskUserWithTranscript commits pending Frame state, immutable rich
// message facts, and their current Transcript branch memberships together.
func (s *Store) ParkAskUserWithTranscript(
	ctx context.Context,
	input ParkAskUserInput,
	pauseInput transcriptstore.AppendRunnerCheckpointInput,
) (ParkAskUserResult, transcriptstore.Event, error) {
	if s == nil || s.db == nil {
		return ParkAskUserResult{}, transcriptstore.Event{}, errors.New("workspace store is closed")
	}
	var err error
	if input, err = normalizeParkAskUserInput(input); err != nil {
		return ParkAskUserResult{}, transcriptstore.Event{}, err
	}
	if ctx == nil {
		return ParkAskUserResult{}, transcriptstore.Event{}, errors.New("ask-user context is required")
	}
	s.branchMu.Lock()
	defer s.branchMu.Unlock()
	repository := transcriptstore.NewRepository(s.db)
	var result ParkAskUserResult
	var pauseEvent transcriptstore.Event
	var projectionEvents []FrameEvent
	err = repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		ownerID, err := frameOwnerInTransaction(ctx, tx, input.FrameID)
		if err != nil {
			return err
		}
		var claimOwnerID, claimFrameID, claimKind string
		if err := tx.QueryRowContext(ctx, `
			SELECT owner_id,frame_id,kind FROM transcript_streams WHERE stream_uid=?`, pauseInput.Claim.StreamUID,
		).Scan(&claimOwnerID, &claimFrameID, &claimKind); err != nil {
			return err
		}
		if claimOwnerID != ownerID || pauseInput.Claim.OwnerID != ownerID || claimFrameID != input.FrameID || claimKind != "frame_ref" {
			return transcriptstore.ErrEventConflict
		}
		result, err = s.parkAskUserInTransaction(ctx, tx, input)
		if err != nil {
			return err
		}
		toolEventID, resultEventID, pausedEventID, err := askUserFrameReferenceIDsInTransaction(ctx, tx, input)
		if err != nil {
			return err
		}
		projectionEvents = make([]FrameEvent, 0, 3)
		for _, eventID := range []string{toolEventID, resultEventID, pausedEventID} {
			event, _, found, loadErr := frameEventByID(ctx, tx, eventID)
			if loadErr != nil {
				return loadErr
			}
			if !found || event.FrameID != input.FrameID {
				return errors.New("ask-user projection event is unavailable")
			}
			projectionEvents = append(projectionEvents, event)
		}
		_, err = tx.AppendFrameAskUserReferences(ctx, transcriptstore.AppendFrameAskUserReferencesInput{
			StreamUID: pauseInput.Claim.StreamUID, OwnerID: ownerID, FrameID: input.FrameID, ToolUseID: input.ToolID,
			ToolUseFrameEventID: toolEventID, ToolResultFrameEventID: resultEventID,
		})
		if err != nil {
			return fmt.Errorf("bind ask-user frame references: %w", err)
		}
		typedPending, err := tx.AppendFrameAskUserPending(ctx, transcriptstore.AppendFrameAskUserPendingInput{
			Claim: pauseInput.Claim, ClientMessageID: "ask-user:" + pauseInput.ClientMessageID,
			FrameID: input.FrameID, ToolUseID: input.ToolID,
			ToolUseFrameEventID: toolEventID, PendingFrameEventID: resultEventID,
		})
		if err != nil {
			return fmt.Errorf("append typed ask-user pending state: %w", err)
		}
		if err := persistAskUserTranscriptOrigin(ctx, tx, input.FrameID, input.ToolID, typedPending.Origin); err != nil {
			return fmt.Errorf("persist typed ask-user origin: %w", err)
		}
		_, pauseEvent, _, err = tx.PauseRunnerForInput(ctx, pauseInput)
		if err != nil {
			return fmt.Errorf("pause ask-user runner: %w", err)
		}
		frame, err := frameForRealtimeInTransaction(ctx, tx, input.FrameID)
		if err != nil {
			return err
		}
		for _, event := range projectionEvents {
			_, err = s.enqueueRealtimeOutboxTransaction(ctx, tx,
				FrameRealtimeEventInput("frame-event:"+event.ID, ownerID, frame, event), event.ID, "")
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return ParkAskUserResult{}, transcriptstore.Event{}, err
	}
	result.Events = projectionEvents
	s.signalKernelRetentionWake()
	return result, pauseEvent, nil
}

func persistAskUserTranscriptOrigin(
	ctx context.Context,
	tx workspaceTransaction,
	frameID, toolID string,
	origin transcriptstore.AskUserOriginV1,
) error {
	var rawContext string
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(context_data,'{}') FROM frame_runtime_metadata WHERE frame_id=?`, frameID,
	).Scan(&rawContext); err != nil {
		return err
	}
	contextData := map[string]any{}
	if json.Unmarshal([]byte(rawContext), &contextData) != nil {
		return errors.New("ask-user frame context is invalid")
	}
	var origins map[string]any
	if rawOrigins, found := contextData[askUserTranscriptOriginsKey]; found {
		var ok bool
		origins, ok = rawOrigins.(map[string]any)
		if !ok {
			return transcriptstore.ErrEventConflict
		}
	} else {
		origins = map[string]any{}
	}
	encoded, err := json.Marshal(origin)
	if err != nil {
		return err
	}
	var stored map[string]any
	if json.Unmarshal(encoded, &stored) != nil {
		return errors.New("ask-user transcript origin is invalid")
	}
	if existing, found := origins[toolID]; found && !reflect.DeepEqual(existing, stored) {
		return transcriptstore.ErrEventConflict
	}
	origins[toolID] = stored
	contextData[askUserTranscriptOriginsKey] = origins
	typedToolIDs := map[string]bool{}
	rawIDs, toolIDsFound := contextData[askUserTranscriptToolIDsKey]
	if toolIDsFound {
		values, ok := rawIDs.([]any)
		if !ok {
			return transcriptstore.ErrEventConflict
		}
		for _, value := range values {
			id := strings.TrimSpace(compatibilityStringValue(value))
			if id == "" || typedToolIDs[id] {
				return transcriptstore.ErrEventConflict
			}
			typedToolIDs[id] = true
		}
	}
	if (len(origins) > 1 || (len(origins) == 1 && origins[toolID] == nil)) && !toolIDsFound {
		return transcriptstore.ErrEventConflict
	}
	for id := range origins {
		if id == toolID {
			continue
		}
		if !typedToolIDs[id] {
			return transcriptstore.ErrEventConflict
		}
	}
	for id := range typedToolIDs {
		if id != toolID {
			if _, found := origins[id]; !found {
				return transcriptstore.ErrEventConflict
			}
		}
	}
	typedToolIDs[toolID] = true
	contextData[askUserTranscriptToolIDsKey] = compatibilitySortedKeys(typedToolIDs)
	if current := strings.TrimSpace(compatibilityStringValue(contextData[inputTranscriptStreamUIDKey])); current != "" && current != origin.StreamUID {
		return transcriptstore.ErrEventConflict
	}
	contextData[inputTranscriptStreamUIDKey] = origin.StreamUID
	updated, err := json.Marshal(contextData)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE frame_runtime_metadata SET context_data=? WHERE frame_id=?`, string(updated), frameID)
	return err
}

func frameForRealtimeInTransaction(
	ctx context.Context,
	tx workspaceTransaction,
	frameID string,
) (Frame, error) {
	var frame Frame
	err := tx.QueryRowContext(ctx, `
		SELECT id,project_id,COALESCE(parent_frame_id,''),root_frame_id,root_sequence,
			agent_name,status,conversation_type,name,created_at,updated_at
		FROM frames WHERE id=?`, frameID,
	).Scan(
		&frame.ID, &frame.ProjectID, &frame.ParentFrameID, &frame.RootFrameID, &frame.RootSequence,
		&frame.AgentName, &frame.Status, &frame.ConversationType, &frame.Name, &frame.CreatedAt, &frame.UpdatedAt,
	)
	if err != nil {
		return Frame{}, fmt.Errorf("load ask-user realtime frame: %w", err)
	}
	return frame, nil
}

func normalizeParkAskUserInput(input ParkAskUserInput) (ParkAskUserInput, error) {
	input.FrameID = strings.TrimSpace(input.FrameID)
	input.ToolID = strings.TrimSpace(input.ToolID)
	canonicalToolName, toolNameOK := transcriptstore.CanonicalAskUserToolNameV1(input.ToolName)
	if input.FrameID == "" || input.ToolID == "" || !toolNameOK || len(input.Questions) == 0 {
		return ParkAskUserInput{}, errors.New("frame id, tool id, canonical AskUser tool name, and questions are required")
	}
	input.ToolName = canonicalToolName
	rawQuestions, err := json.Marshal(input.Questions)
	if err != nil || len(rawQuestions) > 64<<10 {
		return ParkAskUserInput{}, errors.New("ask-user questions exceed the durable limit")
	}
	normalizedQuestions := []any{}
	if json.Unmarshal(rawQuestions, &normalizedQuestions) != nil || len(normalizedQuestions) == 0 {
		return ParkAskUserInput{}, errors.New("ask-user questions are invalid")
	}
	input.Questions = normalizedQuestions
	return input, nil
}

func (s *Store) parkAskUserInTransaction(
	ctx context.Context,
	tx workspaceTransaction,
	input ParkAskUserInput,
) (ParkAskUserResult, error) {

	var status, rawContext string
	err := tx.QueryRowContext(ctx, `
		SELECT frame.status, COALESCE(metadata.context_data, '{}')
		FROM frames frame LEFT JOIN frame_runtime_metadata metadata ON metadata.frame_id = frame.id
		WHERE frame.id = ?`, input.FrameID).Scan(&status, &rawContext)
	if errors.Is(err, sql.ErrNoRows) {
		return ParkAskUserResult{}, fmt.Errorf("frame %s not found", input.FrameID)
	}
	if err != nil {
		return ParkAskUserResult{}, fmt.Errorf("load ask-user frame: %w", err)
	}
	if status == "completed" || status == "failed" || status == "cancelled" {
		return ParkAskUserResult{}, fmt.Errorf("frame %s is already terminal with status %s", input.FrameID, status)
	}
	contextData := map[string]any{}
	if err := json.Unmarshal([]byte(rawContext), &contextData); err != nil {
		return ParkAskUserResult{}, fmt.Errorf("decode ask-user frame context: %w", err)
	}
	request := map[string]any{
		"tool_id": input.ToolID, "requestId": input.ToolID, "kind": "ask",
		"tool_name": input.ToolName, "questions": input.Questions,
	}
	pending := compatibilityPendingInputRequests(contextData)
	for _, item := range pending {
		if compatibilityPendingInputID(item) == input.ToolID {
			stored, storedErr := json.Marshal(item)
			expected, expectedErr := json.Marshal(request)
			if storedErr != nil || expectedErr != nil || !bytes.Equal(stored, expected) {
				return ParkAskUserResult{}, errors.New("ask-user request conflicts with durable state")
			}
			return ParkAskUserResult{AlreadyPending: true}, nil
		}
	}
	pending = append(pending, request)
	contextData["_pending_input_requests"] = compatibilityMapsToAny(pending)
	contextData["_ask_user_payload"] = request
	rawUpdated, err := json.Marshal(contextData)
	if err != nil {
		return ParkAskUserResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO frame_runtime_metadata (frame_id, context_data)
		VALUES (?, ?)
		ON CONFLICT(frame_id) DO UPDATE SET context_data = excluded.context_data`, input.FrameID, string(rawUpdated)); err != nil {
		return ParkAskUserResult{}, fmt.Errorf("persist ask-user context: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE frames SET status = 'awaiting_user_response', updated_at = ? WHERE id = ?`, s.now().UTC(), input.FrameID); err != nil {
		return ParkAskUserResult{}, fmt.Errorf("park ask-user frame: %w", err)
	}
	assistantEvent, err := appendFrameLifecycleEvent(ctx, tx, input.FrameID, "assistant_message", map[string]any{
		"role": "assistant",
		"content": []any{map[string]any{
			"type": "tool_use", "id": input.ToolID, "name": "ask_user",
			"input": map[string]any{"questions": input.Questions},
		}},
	}, s.now().UTC())
	if err != nil {
		return ParkAskUserResult{}, err
	}
	resultEvent, err := appendFrameLifecycleEvent(ctx, tx, input.FrameID, "user_message", map[string]any{
		"role": "user",
		"content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": input.ToolID,
			"content": `{"status":"awaiting_user_response"}`, "is_error": true,
		}},
	}, s.now().UTC())
	if err != nil {
		return ParkAskUserResult{}, err
	}
	pausedEvent, err := appendFrameLifecycleEvent(ctx, tx, input.FrameID, "frame_paused", map[string]any{
		"status": "awaiting_user_response", "reason": "ask_user", "tool_id": input.ToolID,
	}, s.now().UTC())
	if err != nil {
		return ParkAskUserResult{}, err
	}
	return ParkAskUserResult{Events: []FrameEvent{assistantEvent, resultEvent, pausedEvent}}, nil
}

func askUserFrameReferenceIDsInTransaction(
	ctx context.Context,
	tx workspaceTransaction,
	input ParkAskUserInput,
) (string, string, string, error) {
	expectedQuestions, err := json.Marshal(input.Questions)
	if err != nil {
		return "", "", "", err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT id,sequence,event_type,payload FROM frame_events
		WHERE frame_id=? AND event_type IN ('assistant_message','user_message','frame_paused') ORDER BY sequence`, input.FrameID)
	if err != nil {
		return "", "", "", err
	}
	defer rows.Close()
	var toolEventID, resultEventID, pausedEventID string
	var toolSequence, resultSequence, pausedSequence int64
	for rows.Next() {
		var eventID, eventType string
		var sequence int64
		var payload []byte
		if err := rows.Scan(&eventID, &sequence, &eventType, &payload); err != nil {
			return "", "", "", err
		}
		var message map[string]any
		if len(payload) == 0 || json.Unmarshal(payload, &message) != nil {
			return "", "", "", errors.New("ask-user frame message is invalid")
		}
		if eventType == "frame_paused" {
			toolID, _ := message["tool_id"].(string)
			if strings.TrimSpace(toolID) != input.ToolID {
				continue
			}
			if pausedEventID != "" || message["status"] != "awaiting_user_response" || message["reason"] != "ask_user" {
				return "", "", "", errors.New("ask-user pause event conflicts with durable state")
			}
			pausedEventID, pausedSequence = eventID, sequence
			continue
		}
		blocks, ok := message["content"].([]any)
		if !ok {
			continue
		}
		for _, rawBlock := range blocks {
			block, ok := rawBlock.(map[string]any)
			if !ok {
				return "", "", "", errors.New("ask-user frame message block is invalid")
			}
			switch block["type"] {
			case "tool_use":
				id, _ := block["id"].(string)
				if strings.TrimSpace(id) != input.ToolID {
					continue
				}
				name, _ := block["name"].(string)
				toolInput, _ := block["input"].(map[string]any)
				actualQuestions, marshalErr := json.Marshal(toolInput["questions"])
				if toolEventID != "" || eventType != "assistant_message" || strings.TrimSpace(name) != "ask_user" ||
					marshalErr != nil || !bytes.Equal(actualQuestions, expectedQuestions) {
					return "", "", "", errors.New("ask-user tool use conflicts with durable state")
				}
				toolEventID, toolSequence = eventID, sequence
			case "tool_result":
				id, _ := block["tool_use_id"].(string)
				if strings.TrimSpace(id) != input.ToolID {
					continue
				}
				content, _ := block["content"].(string)
				isError, _ := block["is_error"].(bool)
				var state map[string]any
				decodeErr := json.Unmarshal([]byte(content), &state)
				if resultEventID != "" || eventType != "user_message" || !isError || decodeErr != nil ||
					len(state) != 1 || state["status"] != "awaiting_user_response" {
					return "", "", "", errors.New("ask-user tool result conflicts with durable state")
				}
				resultEventID, resultSequence = eventID, sequence
			}
		}
	}
	if err := rows.Err(); err != nil {
		return "", "", "", err
	}
	if toolEventID == "" || resultEventID == "" || pausedEventID == "" ||
		resultSequence <= toolSequence || pausedSequence <= resultSequence {
		return "", "", "", errors.New("ask-user frame facts are incomplete")
	}
	return toolEventID, resultEventID, pausedEventID, nil
}
