package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func transcriptWebStorageError(err error) error {
	if err == nil {
		return nil
	}
	return &webConversationRequestError{
		Status: http.StatusInternalServerError,
		Detail: "unable to process conversation request",
		Cause:  err,
	}
}

func transcriptWebAuthorityUnavailableError() error {
	return &webConversationRequestError{
		Status: http.StatusServiceUnavailable,
		Code:   "HISTORY_NOT_READY",
		Detail: "conversation history is still being prepared",
		Cause:  transcriptstore.ErrHistoryBackfillBlocked,
	}
}

func (s *Server) ensureTranscriptFrameStream(
	ctx context.Context,
	ownerID, frameID string,
) (transcriptstore.Stream, error) {
	if s == nil || s.transcriptStore == nil {
		return transcriptstore.Stream{}, nil
	}
	if s.transcriptContractErr != nil {
		return transcriptstore.Stream{}, transcriptWebStorageError(s.transcriptContractErr)
	}
	frameContext, found, err := s.workspaceStore.GetFrameRealtimeContext(frameID)
	if err != nil {
		return transcriptstore.Stream{}, transcriptWebStorageError(err)
	}
	if !found || frameContext.UserID != ownerID {
		return transcriptstore.Stream{}, transcriptstore.ErrOwnerMismatch
	}
	if stream, found, err := s.transcriptStore.GetFrameStreamBySession(ctx, ownerID, frameID); err != nil {
		return transcriptstore.Stream{}, transcriptWebStorageError(err)
	} else if found {
		return stream, nil
	}
	stream, err := s.transcriptStore.CreateStream(ctx, transcriptstore.CreateStreamInput{
		UID: "frame:" + frameID, OwnerID: ownerID, ExternalID: frameID, SessionID: frameID,
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: frameContext.Frame.ProjectID,
		RootFrameID: frameContext.Frame.RootFrameID, FrameID: frameContext.Frame.ID, Epoch: 1,
	})
	return stream, transcriptWebStorageError(err)
}

func (s *Server) loadTranscriptWebHistory(
	ctx context.Context,
	ownerID, sessionID string,
) ([]map[string]any, bool, error) {
	if s == nil || s.transcriptStore == nil {
		return nil, false, nil
	}
	if s.transcriptContractErr != nil {
		return nil, false, transcriptWebStorageError(s.transcriptContractErr)
	}
	stream, found, err := s.transcriptStore.GetFrameStreamBySession(ctx, ownerID, sessionID)
	if err != nil {
		return nil, false, transcriptWebStorageError(err)
	}
	if !found {
		return nil, false, nil
	}
	messages, _, err := s.projectTranscriptWebHistory(ctx, stream, ownerID, sessionID, "")
	if err != nil {
		return nil, true, transcriptWebStorageError(err)
	}
	return messages, true, nil
}

// loadTranscriptWebBranchHistory is retained for the explicit legacy
// compatibility API. Public Web history must use
// loadActivatedTranscriptWebHistory instead.
func (s *Server) loadTranscriptWebBranchHistory(
	ctx context.Context,
	ownerID, sessionID, branchID string,
) ([]map[string]any, transcriptstore.ProjectionSnapshot, bool, error) {
	if s == nil || s.transcriptStore == nil {
		return nil, transcriptstore.ProjectionSnapshot{}, false, nil
	}
	if s.transcriptContractErr != nil {
		return nil, transcriptstore.ProjectionSnapshot{}, false, transcriptstore.ErrSchemaUnavailable
	}
	stream, found, err := s.transcriptStore.GetFrameStreamBySession(ctx, ownerID, sessionID)
	if err != nil || !found {
		return nil, transcriptstore.ProjectionSnapshot{}, found, err
	}
	messages, snapshot, err := s.projectTranscriptWebHistory(ctx, stream, ownerID, sessionID, strings.TrimSpace(branchID))
	return messages, snapshot, true, err
}

func (s *Server) loadActivatedTranscriptWebHistory(
	ctx context.Context,
	ownerID, sessionID, branchID string,
) ([]map[string]any, transcriptstore.ProjectionSnapshot, bool, error) {
	view, active, err := s.loadActivatedTranscriptWebHistoryView(ctx, ownerID, sessionID, branchID)
	if err != nil || !active {
		return nil, transcriptstore.ProjectionSnapshot{}, active, err
	}
	messages := cloneTranscriptWebProjectionMessages(view.messages)
	if err := s.enrichTranscriptWebConversationMessages(ctx, view.stream.SessionID, messages); err != nil {
		return nil, transcriptstore.ProjectionSnapshot{}, true, err
	}
	if err := s.refreshCachedTranscriptWebArtifactAvailability(ctx, view.stream, ownerID, messages); err != nil {
		return nil, transcriptstore.ProjectionSnapshot{}, true, transcriptWebStorageError(err)
	}
	return messages, view.snapshot, true, nil
}

type activatedTranscriptWebHistoryView struct {
	messages []map[string]any
	snapshot transcriptstore.ProjectionSnapshot
	stream   transcriptstore.Stream
}

// loadActivatedTranscriptWebHistoryView returns an immutable in-process view.
// Callers must clone the exact page or message they expose before applying
// dynamic artifact availability or any other response-only transformation.
func (s *Server) loadActivatedTranscriptWebHistoryView(
	ctx context.Context,
	ownerID, sessionID, branchID string,
) (activatedTranscriptWebHistoryView, bool, error) {
	if s == nil || s.transcriptStore == nil {
		return activatedTranscriptWebHistoryView{}, false, nil
	}
	if s.transcriptContractErr != nil {
		return activatedTranscriptWebHistoryView{}, false, transcriptWebStorageError(s.transcriptContractErr)
	}
	authority, found, err := s.transcriptStore.GetFrameAuthorityBySession(ctx, ownerID, sessionID)
	if err != nil || !found {
		return activatedTranscriptWebHistoryView{}, false, transcriptWebStorageError(err)
	}
	if !authority.TranscriptPayloadActive() {
		return activatedTranscriptWebHistoryView{}, false, nil
	}
	stream, found, err := s.transcriptStore.GetFrameStreamBySession(ctx, ownerID, sessionID)
	if err != nil || !found {
		return activatedTranscriptWebHistoryView{}, true, transcriptWebStorageError(err)
	}
	if stream.UID != authority.ActiveStreamUID || stream.Epoch != authority.ActiveEpoch {
		return activatedTranscriptWebHistoryView{}, true, transcriptWebStorageError(transcriptstore.ErrEventConflict)
	}
	messages, snapshot, err := s.projectCachedActivatedTranscriptWebHistory(
		ctx, authority, stream, ownerID, sessionID, strings.TrimSpace(branchID),
	)
	if err != nil {
		return activatedTranscriptWebHistoryView{}, true, transcriptWebStorageError(err)
	}
	return activatedTranscriptWebHistoryView{messages: messages, snapshot: snapshot, stream: stream}, true, nil
}

func (s *Server) projectTranscriptWebHistory(
	ctx context.Context,
	stream transcriptstore.Stream,
	ownerID, sessionID, branchID string,
) ([]map[string]any, transcriptstore.ProjectionSnapshot, error) {
	typedRichHistory, err := s.transcriptStore.HasActiveTypedHistoryBootstrap(ctx, stream.UID, ownerID, stream.Epoch)
	if err != nil {
		return nil, transcriptstore.ProjectionSnapshot{}, err
	}
	const maxSnapshotAttempts = 3
	for attempt := 0; attempt < maxSnapshotAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, transcriptstore.ProjectionSnapshot{}, err
		}
		projectedEvents := make([]transcriptstore.ProjectedEvent, 0, 128)
		snapshot, err := s.visitTranscriptWebBranchHistory(ctx, stream, ownerID, branchID, func(projected transcriptstore.ProjectedEvent) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			projectedEvents = append(projectedEvents, projected)
			return nil
		})
		if errors.Is(err, transcriptstore.ErrBranchStateStale) {
			continue
		}
		if err != nil {
			return nil, transcriptstore.ProjectionSnapshot{}, fmt.Errorf("visit history: %w", err)
		}
		projectedEvents, err = normalizeTranscriptWebProjectionEvents(projectedEvents)
		if err != nil {
			return nil, transcriptstore.ProjectionSnapshot{}, fmt.Errorf("normalize history: %w", err)
		}
		projectedEvents, err = preserveTranscriptTerminalFailureCandidates(projectedEvents)
		if err != nil {
			return nil, transcriptstore.ProjectionSnapshot{}, fmt.Errorf("preserve history: %w", err)
		}
		typedFrameReferences, err := transcriptTypedAskUserFrameReferences(projectedEvents)
		if err != nil {
			return nil, transcriptstore.ProjectionSnapshot{}, fmt.Errorf("typed history: %w", err)
		}
		messages := make([]map[string]any, 0, 64)
		assistantTimeline := newTranscriptAssistantTimelineState(sessionID)
		toolHistory := newTranscriptToolHistoryState()
		importedRichHistory := newTranscriptImportedRichHistoryState(typedRichHistory)
		askUserState := newTranscriptAskUserProjectionState()
		coordinateReducer := newTranscriptWebCoordinateReducer(sessionID, typedRichHistory)
		for _, projected := range projectedEvents {
			if err := ctx.Err(); err != nil {
				return nil, transcriptstore.ProjectionSnapshot{}, err
			}
			if projected.Event.FrameEventID != nil && typedFrameReferences[*projected.Event.FrameEventID] {
				continue
			}
			if err := coordinateReducer.consume(projected); err != nil {
				return nil, transcriptstore.ProjectionSnapshot{}, fmt.Errorf("history coordinate event id=%d type=%s attempt=%v: %w", projected.Event.EventID, projected.Event.Type, projected.Event.RunnerAttempt, err)
			}
			if err := s.projectTranscriptWebProjectedEvent(
				ctx, &messages, assistantTimeline, toolHistory, importedRichHistory, askUserState,
				stream, ownerID, sessionID, projected,
			); err != nil {
				return nil, transcriptstore.ProjectionSnapshot{}, fmt.Errorf("history message event id=%d type=%s attempt=%v: %w", projected.Event.EventID, projected.Event.Type, projected.Event.RunnerAttempt, err)
			}
		}
		if err := askUserState.validate(); err != nil {
			return nil, transcriptstore.ProjectionSnapshot{}, fmt.Errorf("ask user validate: %w", err)
		}
		if err := importedRichHistory.validate(); err != nil {
			return nil, transcriptstore.ProjectionSnapshot{}, fmt.Errorf("imported history validate: %w", err)
		}
		coordinates, err := coordinateReducer.finalize()
		if err != nil {
			return nil, transcriptstore.ProjectionSnapshot{}, fmt.Errorf("history coordinate finalize: %w", err)
		}
		filteredMessages := make([]map[string]any, 0, len(messages))
		for _, message := range messages {
			if err := ctx.Err(); err != nil {
				return nil, transcriptstore.ProjectionSnapshot{}, err
			}
			if superseded, _ := message["_transcript_superseded"].(bool); superseded {
				continue
			}
			delete(message, "_transcript_superseded")
			delete(message, "_transcript_synthetic_progress")
			filteredMessages = append(filteredMessages, message)
		}
		messages = filteredMessages
		if len(coordinates) != len(messages) {
			return nil, transcriptstore.ProjectionSnapshot{}, transcriptstore.ErrEventConflict
		}
		for index, message := range messages {
			if err := ctx.Err(); err != nil {
				return nil, transcriptstore.ProjectionSnapshot{}, err
			}
			if strings.TrimSpace(webString(message["id"])) != coordinates[index].id ||
				strings.TrimSpace(webString(message["msg_id"])) != coordinates[index].messageID {
				return nil, transcriptstore.ProjectionSnapshot{}, transcriptstore.ErrEventConflict
			}
			content, ok := message["content"].(map[string]any)
			if !ok {
				return nil, transcriptstore.ProjectionSnapshot{}, transcriptstore.ErrEventConflict
			}
			content["synonBiomed"] = map[string]any{
				"messageIndex": index, "blockIndex": 0, "branchId": snapshot.BranchID,
			}
			if _, err := json.Marshal(message); err != nil {
				return nil, transcriptstore.ProjectionSnapshot{}, transcriptstore.ErrEventConflict
			}
		}
		return messages, snapshot, nil
	}
	return nil, transcriptstore.ProjectionSnapshot{}, transcriptstore.ErrBranchStateStale
}

func normalizeTranscriptWebProjectionEvents(
	projectedEvents []transcriptstore.ProjectedEvent,
) ([]transcriptstore.ProjectedEvent, error) {
	segmentNormalizer := newTranscriptWebRuntimeDrainSegmentNormalizer()
	toolRecoveryNormalizer := newTranscriptWebToolRecoveryInputNormalizer()
	syntheticProgressNormalizer := newTranscriptWebSyntheticProgressNormalizer()
	normalized := make([]transcriptstore.ProjectedEvent, 0, len(projectedEvents))
	for _, projected := range projectedEvents {
		value, err := segmentNormalizer.normalize(projected)
		if err != nil {
			return nil, err
		}
		value, err = toolRecoveryNormalizer.normalize(value)
		if err != nil {
			return nil, err
		}
		value, keep, err := syntheticProgressNormalizer.normalize(value)
		if err != nil {
			return nil, err
		}
		if keep {
			normalized = append(normalized, value)
		}
	}
	return normalized, nil
}

// transcriptWebToolRecoveryInputNormalizer repairs the narrow legacy
// projection shape written when an approved durable operation resumed with
// its admitted input instead of the immutable source tool-call arguments.
// Source events remain untouched and source-chain hashing happens before this
// copied projection input is normalized.
type transcriptWebToolRecoveryInputNormalizer struct {
	starts   map[string]map[string]any
	replayed map[string]bool
}

func newTranscriptWebToolRecoveryInputNormalizer() *transcriptWebToolRecoveryInputNormalizer {
	return &transcriptWebToolRecoveryInputNormalizer{
		starts: map[string]map[string]any{}, replayed: map[string]bool{},
	}
}

func (n *transcriptWebToolRecoveryInputNormalizer) normalize(
	projected transcriptstore.ProjectedEvent,
) (transcriptstore.ProjectedEvent, error) {
	if n == nil || projected.Event.Type != "runner_checkpoint" ||
		projected.Event.Source != transcriptstore.EventSourcePayload || projected.Event.RunnerAttempt == nil {
		return projected, nil
	}
	payload, err := transcriptPayloadObject(projected.ResolvedPayloadJSON)
	if err != nil {
		return projected, err
	}
	callID := strings.TrimSpace(webString(payload["toolCallId"]))
	name := strings.TrimSpace(webString(payload["toolName"]))
	input, inputOK := payload["toolInput"].(map[string]any)
	if callID == "" || name == "" {
		return projected, nil
	}
	identity := fmt.Sprintf("%d\x00%s\x00%s", *projected.Event.RunnerAttempt, callID, name)
	status := strings.TrimSpace(webString(payload["status"]))
	phase := strings.TrimSpace(webString(payload["toolPhase"]))
	original, exists := n.starts[identity]
	if status == "running" && phase == "start" && inputOK && !exists {
		n.starts[identity] = cloneTranscriptWebProjectionMap(input)
		return projected, nil
	}
	if !exists || !inputOK {
		return projected, nil
	}
	if status == "running" && phase == "start" {
		explicitReplay, _ := payload["recoveryReplay"].(bool)
		legacyReplay := strings.TrimSpace(webString(payload["resumeCacheKey"])) == chatToolCheckpointPhase("start", callID) &&
			webString(payload["message"]) == fmt.Sprintf("tool %s resumed", name)
		if explicitReplay || legacyReplay {
			n.replayed[identity] = true
		}
	}
	if !n.replayed[identity] || transcriptToolJSONEqual(original, input) {
		return projected, nil
	}
	payload["toolInput"] = cloneTranscriptWebProjectionMap(original)
	raw, err := json.Marshal(payload)
	if err != nil {
		return projected, transcriptstore.ErrEventConflict
	}
	projected.ResolvedPayloadJSON = raw
	return projected, nil
}

// preserveTranscriptTerminalFailureCandidates keeps the last streamed candidate
// visible when an attempt terminates unsuccessfully. Ordinary non-empty resets
// remain authoritative. Only the two exact legacy product failure markers (and
// the empty discard immediately before them) are ignored when they trail a
// failed or cancelled attempt. The durable events remain intact for audit.
func preserveTranscriptTerminalFailureCandidates(
	projectedEvents []transcriptstore.ProjectedEvent,
) ([]transcriptstore.ProjectedEvent, error) {
	trailingResets := map[int64][]int{}
	skip := map[int]bool{}
	for index, projected := range projectedEvents {
		if projected.Event.RunnerAttempt == nil {
			continue
		}
		attempt := *projected.Event.RunnerAttempt
		switch projected.Event.Type {
		case "content_delta", "assistant_message", "history_assistant_message":
			delete(trailingResets, attempt)
		case "content_reset":
			payload, err := transcriptPayloadObject(projected.ResolvedPayloadJSON)
			if err != nil {
				return nil, err
			}
			if transcriptLegacyTerminalFailureReset(payload) {
				trailingResets[attempt] = append(trailingResets[attempt], index)
			} else {
				delete(trailingResets, attempt)
			}
		case "runner_finished":
			payload, err := transcriptPayloadObject(projected.ResolvedPayloadJSON)
			if err != nil {
				return nil, err
			}
			switch strings.TrimSpace(webString(payload["status"])) {
			case "failed", "cancelled", "canceled":
				for _, resetIndex := range trailingResets[attempt] {
					skip[resetIndex] = true
				}
			}
			delete(trailingResets, attempt)
		}
	}
	if len(skip) == 0 {
		return projectedEvents, nil
	}
	filtered := make([]transcriptstore.ProjectedEvent, 0, len(projectedEvents)-len(skip))
	for index, projected := range projectedEvents {
		if !skip[index] {
			filtered = append(filtered, projected)
		}
	}
	return filtered, nil
}

func transcriptLegacyTerminalFailureReset(payload map[string]any) bool {
	text, ok := payload["text"].(string)
	if !ok {
		return false
	}
	switch text {
	case "",
		"Completion failed before a result was accepted.",
		"Completion verification failed; no result was accepted.":
		return true
	default:
		return false
	}
}

func (s *Server) projectTranscriptWebProjectedEvent(
	ctx context.Context,
	messages *[]map[string]any,
	assistantTimeline *transcriptAssistantTimelineState,
	toolHistory *transcriptToolHistoryState,
	importedRichHistory *transcriptImportedRichHistoryState,
	askUserState *transcriptAskUserProjectionState,
	stream transcriptstore.Stream,
	ownerID, sessionID string,
	projected transcriptstore.ProjectedEvent,
) error {
	payload, err := transcriptPayloadObject(projected.ResolvedPayloadJSON)
	if err != nil {
		return err
	}
	createdAt := projected.Event.CreatedAt.UnixMilli()
	importedActions, imported, err := importedRichHistory.consume(projected, payload, len(*messages))
	if err != nil {
		return err
	}
	if imported {
		for _, action := range importedActions {
			message := transcriptImportedRichMessage(action, sessionID, createdAt)
			if action.append {
				*messages = append(*messages, message)
				continue
			}
			if action.index < 0 || action.index >= len(*messages) {
				return transcriptstore.ErrEventConflict
			}
			message["created_at"] = (*messages)[action.index]["created_at"]
			(*messages)[action.index] = message
		}
		return nil
	}
	handled, err := projectTranscriptAskUserHistoryEvent(messages, askUserState, projected, payload, sessionID, createdAt)
	if err != nil {
		return err
	}
	if handled {
		// AskUser is a tool boundary even though its typed facts bypass the
		// ordinary tool-history reducer. A resumed runner can continue the same
		// attempt with a canonical assistant segment, which must not merge into
		// text emitted before the question was parked.
		if projected.Event.Type == transcriptstore.AskUserPromptEventType && projected.Event.RunnerAttempt != nil {
			// The runner cannot reach an AskUser checkpoint while an ordinary
			// foreground tool call is still executing. Close any unmatched legacy
			// start explicitly so a parked or restarted task never looks active.
			for _, action := range toolHistory.settleAttempt(*projected.Event.RunnerAttempt, "cancelled") {
				if action.index < 0 || action.index >= len(*messages) {
					return transcriptstore.ErrEventConflict
				}
				message := transcriptToolHistoryMessage(action, sessionID, createdAt)
				message["created_at"] = (*messages)[action.index]["created_at"]
				(*messages)[action.index] = message
			}
			if assistantIndex, found := assistantTimeline.toolBoundary(*projected.Event.RunnerAttempt); found {
				if assistantIndex < 0 || assistantIndex >= len(*messages) {
					return transcriptstore.ErrEventConflict
				}
				if err := transcriptWebSettleAssistantMessage((*messages)[assistantIndex], "finish"); err != nil {
					return err
				}
			}
		}
		return nil
	}
	toolAction, toolEvent, err := toolHistory.consume(projected, payload, len(*messages))
	if err != nil {
		return err
	}
	if toolEvent {
		message := transcriptToolHistoryMessage(toolAction, sessionID, createdAt)
		if toolAction.append {
			if assistantIndex, found := assistantTimeline.toolBoundary(toolAction.fact.attempt); found {
				if assistantIndex < 0 || assistantIndex >= len(*messages) {
					return transcriptstore.ErrEventConflict
				}
				if err := transcriptWebSettleAssistantMessage((*messages)[assistantIndex], "finish"); err != nil {
					return err
				}
			}
			*messages = append(*messages, message)
		} else {
			if toolAction.index < 0 || toolAction.index >= len(*messages) {
				return transcriptstore.ErrEventConflict
			}
			message["created_at"] = (*messages)[toolAction.index]["created_at"]
			(*messages)[toolAction.index] = message
		}
		return nil
	}
	if attempt, interrupted := transcriptToolHistoryInterruptedAttempt(projected, payload); interrupted {
		for _, action := range toolHistory.settleAttempt(attempt, "cancelled") {
			if action.index < 0 || action.index >= len(*messages) {
				return transcriptstore.ErrEventConflict
			}
			message := transcriptToolHistoryMessage(action, sessionID, createdAt)
			message["created_at"] = (*messages)[action.index]["created_at"]
			(*messages)[action.index] = message
		}
		// Keep the assistant reducer aligned with the coordinate reducer for
		// legacy same-attempt correction resumes. The interruption still settles
		// open tools, while its correction fence retires the empty candidate.
		if transcriptWebCorrectionResumeFence(payload) {
			if assistantIndex, found := assistantTimeline.correctionBoundary(attempt); found {
				if assistantIndex < 0 || assistantIndex >= len(*messages) {
					return transcriptstore.ErrEventConflict
				}
				if err := transcriptWebSettleAssistantMessage((*messages)[assistantIndex], "finish"); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if projected.Event.Type == "runner_checkpoint" && projected.Event.RunnerAttempt != nil &&
		transcriptWebRuntimeDrainToolBoundary(payload) {
		if assistantIndex, found := assistantTimeline.toolBoundary(*projected.Event.RunnerAttempt); found {
			if assistantIndex < 0 || assistantIndex >= len(*messages) {
				return transcriptstore.ErrEventConflict
			}
			if err := transcriptWebSettleAssistantMessage((*messages)[assistantIndex], "finish"); err != nil {
				return err
			}
		}
	}
	if projected.Event.Type == "runner_checkpoint" && projected.Event.RunnerAttempt != nil &&
		transcriptWebRuntimeDrainFence(payload) {
		attempt := *projected.Event.RunnerAttempt
		if transcriptWebCorrectionResumeFence(payload) {
			if assistantIndex, found := assistantTimeline.correctionBoundary(attempt); found {
				if assistantIndex < 0 || assistantIndex >= len(*messages) {
					return transcriptstore.ErrEventConflict
				}
				if err := transcriptWebSettleAssistantMessage((*messages)[assistantIndex], "finish"); err != nil {
					return err
				}
			}
		} else if assistantTimeline.splitPending[attempt] {
			if assistantIndex, found := assistantTimeline.toolBoundary(attempt); found {
				if assistantIndex < 0 || assistantIndex >= len(*messages) {
					return transcriptstore.ErrEventConflict
				}
				if err := transcriptWebSettleAssistantMessage((*messages)[assistantIndex], "finish"); err != nil {
					return err
				}
			}
		}
	}
	switch projected.Event.Type {
	case "user_input_response":
		// Model-only continuation. Rich tool/confirmation facts are the UI
		// authority and were handled above when this is an AskUser result.
		return nil
	case "user_message", "history_user_message":
		messageID := strings.TrimSpace(webString(payload["messageUuid"]))
		if messageID == "" {
			messageID = strings.TrimSpace(webString(payload["_uuid"]))
		}
		if messageID == "" {
			messageID = projected.Event.ClientMessageID
		}
		artifactReferences := transcriptArtifactReferences(projected.ArtifactReferences)
		if payloadReferences, err := transcriptUserArtifactReferences(payload); err != nil {
			return err
		} else if len(payloadReferences) > 0 {
			if len(artifactReferences) > 0 {
				return transcriptstore.ErrEventConflict
			}
			artifactReferences = payloadReferences
		}
		*messages = append(*messages, map[string]any{
			"id": messageID, "msg_id": projected.Event.ClientMessageID,
			"conversation_id": sessionID, "type": "text", "position": "right", "status": "finish",
			"created_at": createdAt, "content": map[string]any{"content": transcriptPayloadText(payload)},
			"artifact_refs": artifactReferences,
		})
	case "content_delta", "content_reset", "assistant_message", "history_assistant_message":
		if projected.Event.RunnerAttempt == nil {
			legacyFrameMessage := projected.Event.Type == "assistant_message" &&
				projected.Event.Source == transcriptstore.EventSourceFrameRef
			genesisPayloadMessage := projected.Event.Type == "history_assistant_message" &&
				projected.Event.Source == transcriptstore.EventSourcePayload
			if !legacyFrameMessage && !genesisPayloadMessage {
				return transcriptstore.ErrEventConflict
			}
			*messages = append(*messages, map[string]any{
				"id": projected.Event.ClientMessageID, "msg_id": projected.Event.ClientMessageID,
				"conversation_id": sessionID, "type": "text", "position": "left", "status": "finish",
				"created_at": createdAt, "content": map[string]any{"content": transcriptPayloadText(payload)},
				"artifact_refs": transcriptArtifactReferences(projected.ArtifactReferences),
			})
			return nil
		}
		attempt := *projected.Event.RunnerAttempt
		durableIdentity, err := transcriptAssistantMessageIDFromPayload(payload, sessionID, attempt)
		if err != nil {
			return err
		}
		text := transcriptPayloadText(payload)
		index := 0
		identity := ""
		created := false
		if projected.Event.Type == "content_reset" {
			targetIdentity, narrow, resetErr := transcriptAssistantSegmentResetTarget(payload, sessionID, attempt)
			if resetErr != nil {
				return resetErr
			}
			if narrow {
				if text != "" {
					return transcriptstore.ErrEventConflict
				}
				supersededIndex, retired, resetErr := assistantTimeline.resetSegment(attempt, targetIdentity, durableIdentity)
				if resetErr != nil || (retired && (supersededIndex < 0 || supersededIndex >= len(*messages))) {
					if resetErr != nil {
						return resetErr
					}
					return transcriptstore.ErrEventConflict
				}
				if !retired {
					return nil
				}
				(*messages)[supersededIndex]["_transcript_superseded"] = true
				return nil
			}
			for _, supersededIndex := range assistantTimeline.reset(attempt, durableIdentity) {
				if supersededIndex < 0 || supersededIndex >= len(*messages) {
					return transcriptstore.ErrEventConflict
				}
				(*messages)[supersededIndex]["_transcript_superseded"] = true
			}
			if text == "" {
				return nil
			}
		}
		index, identity, created, err = assistantTimeline.content(attempt, projected.Event.EventID, len(*messages), durableIdentity)
		if err != nil {
			return err
		}
		if created {
			messageType := transcriptWebAssistantMessageType(payload)
			content := transcriptWebAssistantContent("", messageType)
			if durableIdentity != "" {
				content["assistant_attempt_id"] = transcriptAssistantMessageID(sessionID, attempt, 1)
			}
			*messages = append(*messages, map[string]any{
				"id":              identity,
				"msg_id":          identity,
				"conversation_id": sessionID, "type": messageType, "position": "left", "status": "work",
				"created_at": createdAt, "content": content, "artifact_refs": []map[string]any{},
			})
		}
		if !transcriptWebAssistantMessageAcceptsPayload((*messages)[index], payload) {
			return transcriptstore.ErrEventConflict
		}
		content := (*messages)[index]["content"].(map[string]any)
		if durableIdentity != "" {
			content["assistant_attempt_id"] = transcriptAssistantMessageID(sessionID, attempt, 1)
		}
		syntheticProgress := projected.Event.Type == "content_delta" && boolValue(payload["synthetic_progress"], false)
		currentSynthetic, _ := (*messages)[index]["_transcript_synthetic_progress"].(bool)
		if projected.Event.Type == "content_reset" || projected.Event.Type == "assistant_message" ||
			projected.Event.Type == "history_assistant_message" {
			content["content"] = text
			delete((*messages)[index], "_transcript_synthetic_progress")
		} else if syntheticProgress {
			if currentSynthetic || created {
				content["content"] = text
				(*messages)[index]["_transcript_synthetic_progress"] = true
			}
		} else {
			if currentSynthetic {
				content["content"] = text
			} else {
				content["content"] = webString(content["content"]) + text
			}
			delete((*messages)[index], "_transcript_synthetic_progress")
		}
		delete((*messages)[index], "_transcript_superseded")
		if projected.Event.Type == "history_assistant_message" {
			(*messages)[index]["status"] = "finish"
		}
		if len(projected.ArtifactReferences) > 0 {
			(*messages)[index]["artifact_refs"] = transcriptArtifactReferences(projected.ArtifactReferences)
		}
	case "runner_finished":
		projection, err := s.transcriptStore.GetTerminalProjection(ctx, ownerID, stream.UID, projected.Event.EventID)
		if err != nil {
			return err
		}
		projection = s.publicTranscriptTerminalProjection(ctx, stream, projection)
		durableIdentity, err := transcriptAssistantMessageIDFromPayload(payload, sessionID, projection.Attempt)
		if err != nil {
			return err
		}
		index, identity, created, err := assistantTimeline.terminal(projection.Attempt, projected.Event.EventID, len(*messages), durableIdentity)
		if err != nil {
			return err
		}
		if created {
			content := map[string]any{"content": projection.Detail}
			if durableIdentity != "" {
				content["assistant_attempt_id"] = transcriptAssistantMessageID(sessionID, projection.Attempt, 1)
			}
			*messages = append(*messages, map[string]any{
				"id":              identity,
				"msg_id":          identity,
				"conversation_id": sessionID, "type": "text", "position": "left",
				"created_at": createdAt, "content": content,
			})
		}
		if durableIdentity != "" {
			if content, ok := (*messages)[index]["content"].(map[string]any); ok {
				content["assistant_attempt_id"] = transcriptAssistantMessageID(sessionID, projection.Attempt, 1)
			}
		}
		if superseded, _ := (*messages)[index]["_transcript_superseded"].(bool); superseded {
			delete((*messages)[index], "_transcript_superseded")
			if content, ok := (*messages)[index]["content"].(map[string]any); ok {
				content["content"] = projection.Detail
			}
		}
		if synthetic, _ := (*messages)[index]["_transcript_synthetic_progress"].(bool); synthetic {
			if content, ok := (*messages)[index]["content"].(map[string]any); ok {
				content["content"] = projection.Detail
			}
			delete((*messages)[index], "_transcript_synthetic_progress")
		}
		status := "finish"
		if projection.TerminalStatus == "failed" && !projection.Superseded {
			status = "error"
		}
		(*messages)[index]["status"] = status
		if err := transcriptWebSettleAssistantMessage((*messages)[index], status); err != nil {
			return err
		}
		(*messages)[index]["terminal_status"] = projection.TerminalStatus
		if projection.ReasonCode != "" {
			(*messages)[index]["terminal_reason_code"] = projection.ReasonCode
		}
		if projection.Superseded {
			(*messages)[index]["terminal_superseded"] = true
		}
		(*messages)[index]["artifact_refs"] = transcriptArtifactReferences(projection.ArtifactReferences)
		for _, action := range toolHistory.settleAttempt(projection.Attempt, projection.TerminalStatus) {
			if action.index < 0 || action.index >= len(*messages) {
				return transcriptstore.ErrEventConflict
			}
			message := transcriptToolHistoryMessage(action, sessionID, createdAt)
			message["created_at"] = (*messages)[action.index]["created_at"]
			(*messages)[action.index] = message
		}
	}
	return nil
}

func transcriptTypedAskUserFrameReferences(projectedEvents []transcriptstore.ProjectedEvent) (map[string]bool, error) {
	type typedFacts struct {
		origin  transcriptstore.AskUserOriginV1
		prompt  bool
		pending bool
	}
	factsByCallID := map[string]typedFacts{}
	for _, projected := range projectedEvents {
		if projected.Event.Source != transcriptstore.EventSourcePayload ||
			(projected.Event.Type != transcriptstore.AskUserPromptEventType && projected.Event.Type != transcriptstore.AskUserResultEventType) {
			continue
		}
		fact, found, err := transcriptAskUserProtocolFact(projected.Event, projected.ResolvedPayloadJSON, nil)
		if err != nil || !found || !fact.Typed {
			if err != nil {
				return nil, err
			}
			return nil, transcriptstore.ErrEventConflict
		}
		stored := factsByCallID[fact.CallID]
		if stored.origin.StreamUID != "" && stored.origin != fact.Origin {
			return nil, transcriptstore.ErrEventConflict
		}
		stored.origin = fact.Origin
		switch fact.Kind {
		case "use":
			if stored.prompt {
				return nil, transcriptstore.ErrEventConflict
			}
			stored.prompt = true
		case "result":
			if fact.Pending {
				if stored.pending {
					return nil, transcriptstore.ErrEventConflict
				}
				stored.pending = true
			}
		}
		factsByCallID[fact.CallID] = stored
	}
	frameReferences := map[string]bool{}
	for _, facts := range factsByCallID {
		if !facts.prompt || !facts.pending {
			return nil, transcriptstore.ErrEventConflict
		}
		for _, frameEventID := range []string{facts.origin.ToolUseFrameEventID, facts.origin.PendingFrameEventID} {
			if frameReferences[frameEventID] {
				return nil, transcriptstore.ErrEventConflict
			}
			frameReferences[frameEventID] = true
		}
	}
	return frameReferences, nil
}

func projectTranscriptAskUserHistoryEvent(
	messages *[]map[string]any,
	state *transcriptAskUserProjectionState,
	projected transcriptstore.ProjectedEvent,
	payload map[string]any,
	sessionID string,
	createdAt int64,
) (bool, error) {
	fact, found, err := transcriptAskUserProtocolFact(projected.Event, projected.ResolvedPayloadJSON, payload)
	if err != nil || !found {
		return false, err
	}
	switch fact.Kind {
	case "use":
		index, exists := state.byCallID[fact.CallID]
		if fact.Typed {
			if _, duplicate := state.typedOrigins[fact.CallID]; duplicate {
				return false, transcriptstore.ErrEventConflict
			}
			if !exists {
				index = len(*messages)
				state.byCallID[fact.CallID] = index
				*messages = append(*messages, newTranscriptAskUserMessage(fact, projected.Event.ClientMessageID, sessionID, createdAt))
			} else if err := resetTranscriptAskUserMessage(
				(*messages)[index], fact, projected.Event.ClientMessageID, createdAt,
			); err != nil {
				return false, err
			}
			state.typedOrigins[fact.CallID] = fact.Origin
			return true, nil
		}
		if exists {
			if _, typed := state.typedOrigins[fact.CallID]; typed {
				return true, nil
			}
			return false, transcriptstore.ErrEventConflict
		}
		state.byCallID[fact.CallID] = len(*messages)
		*messages = append(*messages, newTranscriptAskUserMessage(fact, projected.Event.ClientMessageID, sessionID, createdAt))
		return true, nil
	case "result":
		index, exists := state.byCallID[fact.CallID]
		if !exists || index < 0 || index >= len(*messages) {
			return false, transcriptstore.ErrEventConflict
		}
		if fact.Typed {
			origin, typed := state.typedOrigins[fact.CallID]
			if !typed || origin != fact.Origin {
				return false, transcriptstore.ErrEventConflict
			}
			if fact.Pending {
				if state.typedPending[fact.CallID] || state.typedTerminal[fact.CallID] {
					return false, transcriptstore.ErrEventConflict
				}
				state.typedPending[fact.CallID] = true
			} else {
				if !state.typedPending[fact.CallID] || state.typedTerminal[fact.CallID] {
					return false, transcriptstore.ErrEventConflict
				}
				state.typedTerminal[fact.CallID] = true
			}
		} else if _, typed := state.typedOrigins[fact.CallID]; typed {
			return false, transcriptstore.ErrEventConflict
		}
		if err := applyTranscriptAskUserResult((*messages)[index], fact); err != nil {
			return false, err
		}
		return true, nil
	default:
		return false, nil
	}
}

type transcriptAskUserProjectionState struct {
	byCallID      map[string]int
	typedOrigins  map[string]transcriptstore.AskUserOriginV1
	typedPending  map[string]bool
	typedTerminal map[string]bool
}

func newTranscriptAskUserProjectionState() *transcriptAskUserProjectionState {
	return &transcriptAskUserProjectionState{
		byCallID: map[string]int{}, typedOrigins: map[string]transcriptstore.AskUserOriginV1{},
		typedPending: map[string]bool{}, typedTerminal: map[string]bool{},
	}
}

func (state *transcriptAskUserProjectionState) validate() error {
	for callID := range state.typedOrigins {
		if !state.typedPending[callID] {
			return transcriptstore.ErrEventConflict
		}
	}
	return nil
}

func newTranscriptAskUserMessage(
	fact transcriptAskUserFact,
	messageID, sessionID string,
	createdAt int64,
) map[string]any {
	toolUseMessageID := messageID
	toolResultMessageID := fact.CallID + ":result"
	if fact.Typed {
		toolUseMessageID = fact.Origin.PromptClientMessageID
		toolResultMessageID = fact.Origin.PendingClientMessageID
	}
	return map[string]any{
		"id": fact.CallID, "msg_id": messageID, "conversation_id": sessionID,
		"type": "tool_call", "position": "left", "status": "work", "created_at": createdAt,
		"content": map[string]any{
			"call_id": fact.CallID, "name": "ask_user", "args": fact.Input,
			"input": fact.Input, "status": "running",
			"_frame_compat": map[string]any{
				"tool_use_message_id": toolUseMessageID, "tool_result_message_id": toolResultMessageID,
			},
		},
	}
}

func resetTranscriptAskUserMessage(
	message map[string]any,
	fact transcriptAskUserFact,
	messageID string,
	createdAt int64,
) error {
	content, ok := message["content"].(map[string]any)
	if !ok || webString(content["call_id"]) != fact.CallID || webString(content["name"]) != "ask_user" {
		return transcriptstore.ErrEventConflict
	}
	message["msg_id"] = messageID
	message["created_at"] = createdAt
	message["status"] = "work"
	content["args"] = fact.Input
	content["input"] = fact.Input
	content["status"] = "running"
	compatibility, _ := content["_frame_compat"].(map[string]any)
	if compatibility == nil {
		compatibility = map[string]any{}
		content["_frame_compat"] = compatibility
	}
	if fact.Typed {
		compatibility["tool_use_message_id"] = fact.Origin.PromptClientMessageID
		compatibility["tool_result_message_id"] = fact.Origin.PendingClientMessageID
	} else {
		compatibility["tool_use_message_id"] = messageID
	}
	delete(content, "output")
	delete(content, "error")
	return nil
}

func applyTranscriptAskUserResult(message map[string]any, fact transcriptAskUserFact) error {
	content, ok := message["content"].(map[string]any)
	if !ok || webString(content["call_id"]) != fact.CallID || webString(content["name"]) != "ask_user" {
		return transcriptstore.ErrEventConflict
	}
	content["output"] = fact.Output
	compatibility, ok := content["_frame_compat"].(map[string]any)
	if !ok {
		return transcriptstore.ErrEventConflict
	}
	if fact.Typed {
		compatibility["tool_result_message_id"] = fact.Origin.PendingClientMessageID
	}
	compatibility["result_present"] = true
	compatibility["result_output"] = fact.Output
	compatibility["result_is_error"] = fact.IsError && !fact.Pending
	delete(content, "error")
	if fact.Pending {
		content["status"] = "running"
		message["status"] = "work"
	} else if fact.IsError {
		content["status"] = "error"
		content["error"] = fact.Output
		message["status"] = "error"
	} else {
		content["status"] = "completed"
		message["status"] = "finish"
	}
	return nil
}

type transcriptAskUserFact struct {
	Kind    string
	CallID  string
	Input   map[string]any
	Output  string
	IsError bool
	Pending bool
	Typed   bool
	Origin  transcriptstore.AskUserOriginV1
}

func transcriptAskUserProtocolFact(
	event transcriptstore.Event,
	payloadJSON []byte,
	payload map[string]any,
) (transcriptAskUserFact, bool, error) {
	if event.Source == transcriptstore.EventSourcePayload {
		switch event.Type {
		case transcriptstore.AskUserPromptEventType:
			prompt, err := transcriptstore.DecodeAskUserPromptV1(payloadJSON)
			if err != nil || event.StreamUID != prompt.Origin.StreamUID || event.ClientMessageID != prompt.Origin.PromptClientMessageID ||
				event.RunnerAttempt == nil || *event.RunnerAttempt != prompt.Origin.RunnerAttempt {
				return transcriptAskUserFact{}, false, transcriptstore.ErrEventConflict
			}
			encoded, err := json.Marshal(map[string]any{"questions": prompt.Questions})
			if err != nil {
				return transcriptAskUserFact{}, false, transcriptstore.ErrEventConflict
			}
			input := map[string]any{}
			if json.Unmarshal(encoded, &input) != nil {
				return transcriptAskUserFact{}, false, transcriptstore.ErrEventConflict
			}
			return transcriptAskUserFact{
				Kind: "use", CallID: prompt.ToolUseID, Input: input, Typed: true, Origin: prompt.Origin,
			}, true, nil
		case transcriptstore.AskUserResultEventType:
			result, err := transcriptstore.DecodeAskUserResultEventV1(payloadJSON)
			if err != nil || event.StreamUID != result.Origin.StreamUID || event.RunnerAttempt == nil ||
				*event.RunnerAttempt != result.Origin.RunnerAttempt {
				return transcriptAskUserFact{}, false, transcriptstore.ErrEventConflict
			}
			pending := result.Result.Status == transcriptstore.AskUserStatusAwaitingResponse
			if pending && event.ClientMessageID != result.Origin.PendingClientMessageID {
				return transcriptAskUserFact{}, false, transcriptstore.ErrEventConflict
			}
			expectedClientMessageID, idErr := transcriptstore.AskUserResultClientMessageIDV1(result.Origin)
			if !pending && (idErr != nil || event.ClientMessageID != expectedClientMessageID) {
				return transcriptAskUserFact{}, false, transcriptstore.ErrEventConflict
			}
			output, err := transcriptstore.EncodeAskUserResultV1(result.Result)
			if err != nil {
				return transcriptAskUserFact{}, false, transcriptstore.ErrEventConflict
			}
			return transcriptAskUserFact{
				Kind: "result", CallID: result.ToolUseID, Output: string(output), Pending: pending,
				IsError: pending, Typed: true, Origin: result.Origin,
			}, true, nil
		default:
			return transcriptAskUserFact{}, false, nil
		}
	}
	if event.Source != transcriptstore.EventSourceFrameRef {
		return transcriptAskUserFact{}, false, nil
	}
	blocks := richWebRawBlocks(payload["content"])
	if len(blocks) == 0 {
		return transcriptAskUserFact{}, false, nil
	}
	toolBlocks := make([]map[string]any, 0, 1)
	for _, block := range blocks {
		switch strings.TrimSpace(webString(block["type"])) {
		case "tool_use", "tool_result":
			toolBlocks = append(toolBlocks, block)
		}
	}
	if len(toolBlocks) == 0 {
		return transcriptAskUserFact{}, false, nil
	}
	if len(blocks) != 1 || len(toolBlocks) != 1 {
		return transcriptAskUserFact{}, false, transcriptstore.ErrEventConflict
	}
	block := toolBlocks[0]
	switch strings.TrimSpace(webString(block["type"])) {
	case "tool_use":
		callID := strings.TrimSpace(webString(block["id"]))
		name := webString(block["name"])
		input, _ := block["input"].(map[string]any)
		_, nameOK := transcriptstore.CanonicalAskUserToolNameV1(name)
		if event.Type != "assistant_message" || callID == "" || !nameOK || input == nil {
			return transcriptAskUserFact{}, false, transcriptstore.ErrEventConflict
		}
		return transcriptAskUserFact{Kind: "use", CallID: callID, Input: input}, true, nil
	case "tool_result":
		callID := strings.TrimSpace(webString(block["tool_use_id"]))
		if callID == "" || (event.Type != "user_message" && event.Type != "user_input_response") {
			return transcriptAskUserFact{}, false, transcriptstore.ErrEventConflict
		}
		output := richWebFormatValue(block["content"])
		pending := false
		if output != "" {
			var state map[string]any
			if json.Unmarshal([]byte(output), &state) == nil {
				pending = strings.TrimSpace(webString(state["status"])) == "awaiting_user_response"
			}
		}
		return transcriptAskUserFact{
			Kind: "result", CallID: callID, Output: output, IsError: block["is_error"] == true, Pending: pending,
		}, true, nil
	default:
		return transcriptAskUserFact{}, false, nil
	}
}

func (s *Server) visitTranscriptWebHistory(
	ctx context.Context,
	stream transcriptstore.Stream,
	ownerID string,
	visit func(transcriptstore.ProjectedEvent) error,
) error {
	_, err := s.visitTranscriptWebBranchHistory(ctx, stream, ownerID, "", visit)
	return err
}

func (s *Server) visitTranscriptWebBranchHistory(
	ctx context.Context,
	stream transcriptstore.Stream,
	ownerID, branchID string,
	visit func(transcriptstore.ProjectedEvent) error,
) (transcriptstore.ProjectionSnapshot, error) {
	return s.visitTranscriptWebBranchHistoryMode(ctx, stream, ownerID, branchID, false, visit)
}

func (s *Server) visitTranscriptWebBranchHistoryMode(
	ctx context.Context,
	stream transcriptstore.Stream,
	ownerID, branchID string,
	coordinateOnly bool,
	visit func(transcriptstore.ProjectedEvent) error,
) (transcriptstore.ProjectionSnapshot, error) {
	normalizer := newTranscriptWebRuntimeDrainSegmentNormalizer()
	normalizedVisit := func(projected transcriptstore.ProjectedEvent) error {
		normalized, err := normalizer.normalize(projected)
		if err != nil {
			return err
		}
		return visit(normalized)
	}
	const pageSize = 1000
	var snapshot transcriptstore.ProjectionSnapshot
	var err error
	if branchID == "" {
		snapshot, err = s.transcriptStore.GetProjectionSnapshot(ctx, stream.UID, ownerID)
	} else {
		snapshot, err = s.transcriptStore.GetBranchProjectionSnapshot(ctx, stream.UID, ownerID, branchID)
	}
	if err != nil {
		return transcriptstore.ProjectionSnapshot{}, err
	}
	if snapshot.ThroughPublicationSequence <= 0 {
		return snapshot, nil
	}
	after := int64(0)
	for after < snapshot.ThroughPublicationSequence {
		if err := ctx.Err(); err != nil {
			return transcriptstore.ProjectionSnapshot{}, err
		}
		input := transcriptstore.ListProjectedEventsInput{
			StreamUID: stream.UID, OwnerID: ownerID,
			BranchID: snapshot.BranchID, BranchGeneration: snapshot.BranchGeneration,
			AfterPublicationSequence:   after,
			ThroughPublicationSequence: snapshot.ThroughPublicationSequence, Limit: pageSize,
		}
		var page []transcriptstore.ProjectedEvent
		if coordinateOnly {
			page, err = s.transcriptStore.ListProjectedCoordinateEvents(ctx, input)
		} else {
			page, err = s.transcriptStore.ListProjectedEvents(ctx, input)
		}
		if err != nil {
			return transcriptstore.ProjectionSnapshot{}, err
		}
		if len(page) == 0 {
			return transcriptstore.ProjectionSnapshot{}, transcriptstore.ErrEventConflict
		}
		for _, projected := range page {
			if err := ctx.Err(); err != nil {
				return transcriptstore.ProjectionSnapshot{}, err
			}
			if err := normalizedVisit(projected); err != nil {
				return transcriptstore.ProjectionSnapshot{}, err
			}
		}
		next := page[len(page)-1].Event.PublicationSeq
		if next <= after || next > snapshot.ThroughPublicationSequence {
			return transcriptstore.ProjectionSnapshot{}, transcriptstore.ErrEventConflict
		}
		after = next
	}
	var current transcriptstore.ProjectionSnapshot
	if branchID == "" {
		current, err = s.transcriptStore.GetProjectionSnapshot(ctx, stream.UID, ownerID)
	} else {
		current, err = s.transcriptStore.GetBranchProjectionSnapshot(ctx, stream.UID, ownerID, branchID)
	}
	if err != nil {
		return transcriptstore.ProjectionSnapshot{}, err
	}
	if current.BranchID != snapshot.BranchID || current.BranchGeneration != snapshot.BranchGeneration {
		return transcriptstore.ProjectionSnapshot{}, transcriptstore.ErrBranchStateStale
	}
	return snapshot, nil
}

func (s *Server) visitTranscriptWebProjectionSnapshot(
	ctx context.Context,
	stream transcriptstore.Stream,
	ownerID string,
	snapshot transcriptstore.ProjectionSnapshot,
	coordinateOnly bool,
	visit func(transcriptstore.ProjectedEvent) error,
) error {
	normalizer := newTranscriptWebRuntimeDrainSegmentNormalizer()
	return s.visitTranscriptWebProjectionSnapshotRaw(ctx, stream, ownerID, snapshot, coordinateOnly,
		func(projected transcriptstore.ProjectedEvent) error {
			normalized, err := normalizer.normalize(projected)
			if err != nil {
				return err
			}
			return visit(normalized)
		})
}

// visitTranscriptWebProjectionSnapshotRaw preserves the immutable source
// payloads for source-chain hashing and resume-state inspection. Projection
// consumers should use visitTranscriptWebProjectionSnapshot so the narrow v2
// legacy runtime-drain repair is applied before message identity reduction.
func (s *Server) visitTranscriptWebProjectionSnapshotRaw(
	ctx context.Context,
	stream transcriptstore.Stream,
	ownerID string,
	snapshot transcriptstore.ProjectionSnapshot,
	coordinateOnly bool,
	visit func(transcriptstore.ProjectedEvent) error,
) error {
	if snapshot.StreamUID != stream.UID || snapshot.BranchID == "" || snapshot.BranchGeneration <= 0 ||
		snapshot.ThroughPublicationSequence < 0 {
		return transcriptstore.ErrEventConflict
	}
	const pageSize = 1000
	after := int64(0)
	for after < snapshot.ThroughPublicationSequence {
		if err := ctx.Err(); err != nil {
			return err
		}
		input := transcriptstore.ListProjectedEventsInput{
			StreamUID: stream.UID, OwnerID: ownerID,
			BranchID: snapshot.BranchID, BranchGeneration: snapshot.BranchGeneration,
			AfterPublicationSequence:   after,
			ThroughPublicationSequence: snapshot.ThroughPublicationSequence, Limit: pageSize,
		}
		var page []transcriptstore.ProjectedEvent
		var err error
		if coordinateOnly {
			page, err = s.transcriptStore.ListProjectedCoordinateEvents(ctx, input)
		} else {
			page, err = s.transcriptStore.ListProjectedEvents(ctx, input)
		}
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return transcriptstore.ErrEventConflict
		}
		for _, projected := range page {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := visit(projected); err != nil {
				return err
			}
		}
		next := page[len(page)-1].Event.PublicationSeq
		if next <= after || next > snapshot.ThroughPublicationSequence {
			return transcriptstore.ErrEventConflict
		}
		after = next
	}
	current, err := s.transcriptStore.GetBranchProjectionSnapshot(ctx, stream.UID, ownerID, snapshot.BranchID)
	if err != nil {
		return err
	}
	if current.BranchID != snapshot.BranchID || current.BranchGeneration != snapshot.BranchGeneration ||
		current.ThroughPublicationSequence != snapshot.ThroughPublicationSequence {
		return transcriptstore.ErrBranchStateStale
	}
	return nil
}

type transcriptWebRuntimeDrainSegmentState struct {
	lastEffectiveOrdinal int64
	toolBoundary         bool
	armed                bool
	forceFreshSegment    bool
	rebasing             bool
	offset               int64
	lastRawOrdinal       int64
}

type transcriptWebRuntimeDrainSegmentNormalizer struct {
	attempts map[int64]*transcriptWebRuntimeDrainSegmentState
}

func newTranscriptWebRuntimeDrainSegmentNormalizer() *transcriptWebRuntimeDrainSegmentNormalizer {
	return &transcriptWebRuntimeDrainSegmentNormalizer{
		attempts: make(map[int64]*transcriptWebRuntimeDrainSegmentState),
	}
}

// normalize repairs same-attempt continuation writers that restart assistant
// ordinals after a durable tool boundary. Runtime draining and a completed
// typed AskUser answer both resume the same runner attempt. Source events are
// never changed; only copied reducer input is renumbered.
func (n *transcriptWebRuntimeDrainSegmentNormalizer) normalize(
	projected transcriptstore.ProjectedEvent,
) (transcriptstore.ProjectedEvent, error) {
	if n == nil || projected.Event.RunnerAttempt == nil {
		return projected, nil
	}
	attempt := *projected.Event.RunnerAttempt
	state := n.attempts[attempt]
	if state == nil {
		state = &transcriptWebRuntimeDrainSegmentState{}
		n.attempts[attempt] = state
	}
	payload, err := transcriptPayloadObject(projected.ResolvedPayloadJSON)
	if err != nil {
		return projected, err
	}
	if projected.Event.Type == "runner_checkpoint" {
		if transcriptWebRuntimeDrainToolBoundary(payload) && state.lastEffectiveOrdinal > 0 {
			state.toolBoundary = true
		}
		correctionFence := transcriptWebCorrectionResumeFence(payload)
		resumeFence := transcriptWebRuntimeDrainFence(payload) || transcriptWebAskUserResumeFence(payload) ||
			transcriptWebRunnerResumeStateFence(payload)
		if correctionFence {
			// Completion-review and artifact-reference corrections always start a
			// fresh candidate segment on resume; the interruption itself is the
			// boundary even when no durable tool checkpoint precedes it.
			state.forceFreshSegment = true
			state.armed = state.lastEffectiveOrdinal > 0
			state.rebasing = false
			state.offset = 0
			state.lastRawOrdinal = 0
		} else if resumeFence && !state.armed && (!state.rebasing || state.toolBoundary) {
			// A composition-root resume checkpoint can immediately follow the
			// semantic correction fence. It confirms recovery; it must not erase
			// the already armed correction boundary or an active rebasing epoch
			// before the first public delta.
			state.forceFreshSegment = false
			state.armed = state.toolBoundary && state.lastEffectiveOrdinal > 0
			state.rebasing = false
			state.offset = 0
			state.lastRawOrdinal = 0
		}
		return projected, nil
	}
	if projected.Event.Type != "content_delta" && projected.Event.Type != "content_reset" &&
		projected.Event.Type != "assistant_message" && projected.Event.Type != "runner_finished" {
		return projected, nil
	}
	segment, present, err := transcriptstore.ParseAssistantSegmentV1(payload)
	if err != nil {
		return projected, err
	}
	if !present {
		state.armed = false
		return projected, nil
	}
	if projected.Event.Type == "runner_finished" && state.rebasing && segment.ReplaceScope == "" {
		// Legacy continuations sometimes wrote the terminal receipt with the
		// provider's raw ordinal (often 1) after public deltas had already been
		// rebased. The terminal closes the current durable segment; normalize it
		// to that segment instead of treating the raw provider counter as a
		// backward jump.
		if state.lastEffectiveOrdinal <= 0 {
			return projected, transcriptstore.ErrEventConflict
		}
		payload["assistant_segment"] = transcriptstore.AssistantSegmentPayloadV1(state.lastEffectiveOrdinal, "")
		raw, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return projected, transcriptstore.ErrEventConflict
		}
		projected.ResolvedPayloadJSON = raw
		state.armed = false
		state.forceFreshSegment = false
		state.rebasing = false
		state.offset = 0
		state.lastRawOrdinal = 0
		return projected, nil
	}
	if state.armed {
		state.armed = false
		if projected.Event.Type != "runner_finished" && segment.ReplaceScope == "" &&
			(state.forceFreshSegment || segment.Ordinal == 1) {
			// A correction pass is a new candidate even when the provider starts
			// its first visible payload at raw ordinal 2 (for example after one or
			// more tool calls). Rebase that first visible ordinal to exactly the
			// next durable segment. Ordinary runtime drains retain the narrower
			// legacy ordinal-1 compatibility rule.
			state.rebasing = true
			state.offset = state.lastEffectiveOrdinal + 1 - segment.Ordinal
			state.lastRawOrdinal = 0
		}
		state.forceFreshSegment = false
	}
	if state.rebasing {
		if segment.ReplaceScope != "" {
			// A correction/clarification pass can emit provisional prose and then
			// retract it with a scoped content_reset before the next durable resume
			// fence. The reset is the legitimate end of the rebased segment, not an
			// ordinal conflict. Preserve its replacement scope while applying the
			// same offset used by the preceding deltas.
			if projected.Event.Type != "content_reset" ||
				(segment.ReplaceScope != transcriptstore.AssistantReplaceScopeAttempt &&
					segment.ReplaceScope != transcriptstore.AssistantReplaceScopeSegment) ||
				segment.Ordinal < state.lastRawOrdinal ||
				(state.lastRawOrdinal > 0 && segment.Ordinal > state.lastRawOrdinal+1) {
				return projected, transcriptstore.ErrEventConflict
			}
			effective := segment.Ordinal + state.offset
			if effective <= 0 || effective > transcriptstore.AssistantSegmentMaxOrdinal {
				return projected, transcriptstore.ErrEventConflict
			}
			payload["assistant_segment"] = transcriptstore.AssistantSegmentPayloadV1(
				effective, segment.ReplaceScope,
			)
			raw, err := json.Marshal(payload)
			if err != nil {
				return projected, transcriptstore.ErrEventConflict
			}
			projected.ResolvedPayloadJSON = raw
			state.lastEffectiveOrdinal = effective
			// A reset closes one provisional segment, not the resumed ordinal
			// epoch. Providers continue with the reset ordinal (or its successor)
			// after retracting pre-tool prose. Keep the durable offset until the
			// next resume fence or terminal event so every later segment in this
			// continuation remains monotonic across repeated runtime recovery.
			state.lastRawOrdinal = segment.Ordinal
		} else if segment.Ordinal < state.lastRawOrdinal ||
			(state.lastRawOrdinal > 0 && segment.Ordinal > state.lastRawOrdinal+1) {
			return projected, transcriptstore.ErrEventConflict
		} else {
			state.lastRawOrdinal = segment.Ordinal
			effective := segment.Ordinal + state.offset
			if effective <= 0 || effective > transcriptstore.AssistantSegmentMaxOrdinal {
				return projected, transcriptstore.ErrEventConflict
			}
			payload["assistant_segment"] = transcriptstore.AssistantSegmentPayloadV1(effective, "")
			raw, err := json.Marshal(payload)
			if err != nil {
				return projected, transcriptstore.ErrEventConflict
			}
			projected.ResolvedPayloadJSON = raw
			state.lastEffectiveOrdinal = effective
		}
	} else if segment.Ordinal > state.lastEffectiveOrdinal {
		state.lastEffectiveOrdinal = segment.Ordinal
	}
	state.toolBoundary = false
	if projected.Event.Type == "runner_finished" {
		state.armed = false
		state.forceFreshSegment = false
		state.rebasing = false
		state.offset = 0
		state.lastRawOrdinal = 0
	}
	return projected, nil
}

func transcriptWebAskUserResumeFence(payload map[string]any) bool {
	if strings.TrimSpace(webString(payload["status"])) != "completed" ||
		strings.TrimSpace(webString(payload["toolPhase"])) != "completed" {
		return false
	}
	_, askUser := transcriptstore.CanonicalAskUserToolNameV1(strings.TrimSpace(webString(payload["toolName"])))
	if !askUser {
		return false
	}
	result, ok := payload["toolResult"].(map[string]any)
	return ok && strings.TrimSpace(webString(result["status"])) != ""
}

// transcriptWebRunnerResumeStateFence identifies the canonical checkpoint
// emitted when one logical runner attempt resumes after a durable pause such
// as per-operation approval. A preceding tool checkpoint is still required by
// the segment normalizer before this fence can rebase an ordinal, so ordinary
// task startup and restart checkpoints do not manufacture message boundaries.
func transcriptWebRunnerResumeStateFence(payload map[string]any) bool {
	if !mapHasOnlyKeys(payload, "status", "stage", "detail", "lifecyclePhase") ||
		strings.TrimSpace(webString(payload["status"])) != "running" ||
		strings.TrimSpace(webString(payload["stage"])) != "resume_state" ||
		strings.TrimSpace(webString(payload["detail"])) != "restoring the durable assistant continuation state" {
		return false
	}
	if lifecycle, present := payload["lifecyclePhase"]; present && strings.TrimSpace(webString(lifecycle)) != "recovery" {
		return false
	}
	return true
}

func transcriptWebRuntimeDrainFence(payload map[string]any) bool {
	if !mapHasOnlyKeys(payload, "status", "reason_code", "resume_detail", "recovery_contract_revision", "auto_resume") ||
		strings.TrimSpace(webString(payload["status"])) != "interrupted" {
		return false
	}
	// Runtime draining, provider context pressure, explicit artifact/skill
	// corrections, AskUser resumes, and the bounded tool round stop resume the
	// same runner attempt with a fresh ordinal-1 segment.
	// Provider stream and tool-round no-progress interruptions continue the open
	// segment instead and must not be renumbered (see
	// TestRuntimeDrainSegmentNormalizerLeavesNonLegacyIdentityShapesUnchanged).
	switch strings.TrimSpace(webString(payload["reason_code"])) {
	case "runtime_draining", sessionRunnerProviderContextPressureReasonCode,
		sessionRunnerToolLifecyclePersistenceReasonCode,
		sessionRunnerCompletionReviewRecoveryReasonCode,
		sessionRunnerStoreContentionReasonCode,
		sessionRunnerModelProviderTemporaryReasonCode,
		sessionRunnerVisualMediaUnsupportedReasonCode,
		sessionRunnerResponseLanguageMismatchReasonCode,
		sessionRunnerFinalPresentationReasonCode,
		"artifact_reference_correction_required",
		sessionRunnerRequiredToolChoiceUnsatisfiedReasonCode,
		sessionRunnerVisualArtifactValidationReasonCode,
		sessionRunnerSelectedSkillContractUnavailableReasonCode, sessionRunnerToolRoundLimitReasonCode:
	default:
		return false
	}
	if detail, present := payload["resume_detail"]; present {
		value, ok := detail.(string)
		if !ok || strings.TrimSpace(value) != value {
			return false
		}
	}
	if rawRevision, present := payload["recovery_contract_revision"]; present {
		revision, ok := transcriptWebNonnegativeSafeInteger(rawRevision)
		if !ok || revision <= 0 || revision > 1_000_000 {
			return false
		}
	}
	if rawAutoResume, present := payload["auto_resume"]; present {
		if _, ok := rawAutoResume.(bool); !ok {
			return false
		}
	}
	return true
}

func transcriptWebCorrectionResumeFence(payload map[string]any) bool {
	if !transcriptWebRuntimeDrainFence(payload) {
		return false
	}
	switch strings.TrimSpace(webString(payload["reason_code"])) {
	case "artifact_reference_correction_required",
		sessionRunnerRequiredToolChoiceUnsatisfiedReasonCode,
		sessionRunnerVisualMediaUnsupportedReasonCode,
		sessionRunnerVisualArtifactValidationReasonCode,
		sessionRunnerResponseLanguageMismatchReasonCode, sessionRunnerFinalPresentationReasonCode:
		return true
	default:
		return false
	}
}

func transcriptWebRuntimeDrainToolBoundary(payload map[string]any) bool {
	// Any durable tool checkpoint (start/completed/verification/failed) proves
	// a tool intervened between assistant segments. Verification and reviewer
	// checkpoints use phase values other than "start", so requiring a start
	// phase would miss the boundary that precedes a reviewer-correction
	// resume.
	return strings.TrimSpace(webString(payload["toolCallId"])) != "" &&
		strings.TrimSpace(webString(payload["toolName"])) != ""
}
