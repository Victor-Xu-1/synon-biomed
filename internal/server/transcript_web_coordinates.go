package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

type transcriptTypedAskUserReferenceCollector struct {
	facts map[string]struct {
		origin  transcriptstore.AskUserOriginV1
		prompt  bool
		pending bool
	}
}

func newTranscriptTypedAskUserReferenceCollector() *transcriptTypedAskUserReferenceCollector {
	return &transcriptTypedAskUserReferenceCollector{facts: map[string]struct {
		origin  transcriptstore.AskUserOriginV1
		prompt  bool
		pending bool
	}{}}
}

func (c *transcriptTypedAskUserReferenceCollector) consume(projected transcriptstore.ProjectedEvent) error {
	if projected.Event.Source != transcriptstore.EventSourcePayload ||
		(projected.Event.Type != transcriptstore.AskUserPromptEventType && projected.Event.Type != transcriptstore.AskUserResultEventType) {
		return nil
	}
	payload, err := transcriptPayloadObject(projected.ResolvedPayloadJSON)
	if err != nil {
		return err
	}
	fact, found, err := transcriptAskUserProtocolFact(projected.Event, projected.ResolvedPayloadJSON, payload)
	if err != nil || !found || !fact.Typed {
		if err == nil {
			err = transcriptstore.ErrEventConflict
		}
		return err
	}
	stored := c.facts[fact.CallID]
	if stored.origin != (transcriptstore.AskUserOriginV1{}) && stored.origin != fact.Origin {
		return transcriptstore.ErrEventConflict
	}
	stored.origin = fact.Origin
	switch fact.Kind {
	case "use":
		if stored.prompt {
			return transcriptstore.ErrEventConflict
		}
		stored.prompt = true
	case "result":
		if fact.Pending && stored.pending {
			return transcriptstore.ErrEventConflict
		}
		if !fact.Pending {
			return nil
		}
		stored.pending = true
	}
	c.facts[fact.CallID] = stored
	return nil
}

func (c *transcriptTypedAskUserReferenceCollector) finish() (map[string]bool, error) {
	references := map[string]bool{}
	for _, facts := range c.facts {
		if !facts.prompt || !facts.pending || facts.origin == (transcriptstore.AskUserOriginV1{}) {
			return nil, transcriptstore.ErrEventConflict
		}
		for _, frameEventID := range []string{facts.origin.ToolUseFrameEventID, facts.origin.PendingFrameEventID} {
			if frameEventID == "" || references[frameEventID] {
				return nil, transcriptstore.ErrEventConflict
			}
			references[frameEventID] = true
		}
	}
	return references, nil
}

// transcriptWebCoordinateReducer owns canonical message identity and index
// allocation. The full Web renderer and cursor-only projection both consume
// this reducer, so cursor persistence cannot drift from visible history.
type transcriptWebCoordinateReducer struct {
	coordinates       []canonicalReadCursorCoordinate
	publicationRanges []transcriptWebPublicationRange
	assistantTimeline *transcriptAssistantTimelineState
	toolHistory       *transcriptToolHistoryState
	importedRich      *transcriptImportedRichHistoryState
	askUser           *transcriptAskUserProjectionState
	identityIndex     map[string]int
	superseded        map[int]bool
	sessionID         string
}

type transcriptWebPublicationRange struct {
	First        int64
	Last         int64
	FirstEventID int64
	LastEventID  int64
}

func newTranscriptWebCoordinateReducer(sessionID string, typedRichHistory bool) *transcriptWebCoordinateReducer {
	return &transcriptWebCoordinateReducer{
		assistantTimeline: newTranscriptAssistantTimelineState(sessionID), toolHistory: newTranscriptToolHistoryState(),
		importedRich:  newTranscriptImportedRichHistoryState(typedRichHistory),
		askUser:       newTranscriptAskUserProjectionState(),
		identityIndex: map[string]int{}, superseded: map[int]bool{}, sessionID: strings.TrimSpace(sessionID),
	}
}

func (r *transcriptWebCoordinateReducer) append(
	id, messageID string, publicationSequence, eventID int64,
) error {
	id = strings.TrimSpace(id)
	messageID = strings.TrimSpace(messageID)
	if (id == "" && messageID == "") || publicationSequence <= 0 || eventID <= 0 {
		return transcriptstore.ErrEventConflict
	}
	index := len(r.coordinates)
	for _, identity := range []string{id, messageID} {
		if identity == "" {
			continue
		}
		if previous, found := r.identityIndex[identity]; found && previous != index {
			return transcriptstore.ErrEventConflict
		}
		r.identityIndex[identity] = index
	}
	r.coordinates = append(r.coordinates, canonicalReadCursorCoordinate{id: id, messageID: messageID})
	r.publicationRanges = append(r.publicationRanges, transcriptWebPublicationRange{
		First: publicationSequence, Last: publicationSequence,
		FirstEventID: eventID, LastEventID: eventID,
	})
	return nil
}

func (r *transcriptWebCoordinateReducer) touch(index int, publicationSequence, eventID int64) error {
	if index < 0 || index >= len(r.publicationRanges) || publicationSequence <= 0 || eventID <= 0 {
		return transcriptstore.ErrEventConflict
	}
	rangeValue := &r.publicationRanges[index]
	if publicationSequence < rangeValue.First || publicationSequence < rangeValue.Last {
		return transcriptstore.ErrEventConflict
	}
	rangeValue.Last = publicationSequence
	rangeValue.LastEventID = eventID
	return nil
}

func (r *transcriptWebCoordinateReducer) replaceMessageID(index int, messageID string) error {
	messageID = strings.TrimSpace(messageID)
	if index < 0 || index >= len(r.coordinates) || messageID == "" {
		return transcriptstore.ErrEventConflict
	}
	old := r.coordinates[index].messageID
	if old != "" && old != r.coordinates[index].id {
		delete(r.identityIndex, old)
	}
	if previous, found := r.identityIndex[messageID]; found && previous != index {
		return transcriptstore.ErrEventConflict
	}
	r.coordinates[index].messageID = messageID
	r.identityIndex[messageID] = index
	return nil
}

func (r *transcriptWebCoordinateReducer) consume(projected transcriptstore.ProjectedEvent) error {
	payload, err := transcriptPayloadObject(projected.ResolvedPayloadJSON)
	if err != nil {
		return err
	}
	importedActions, imported, err := r.importedRich.consume(projected, payload, len(r.coordinates))
	if err != nil {
		return err
	}
	if imported {
		for _, action := range importedActions {
			if action.append {
				if err := r.append(action.id, action.messageID, projected.Event.PublicationSeq, projected.Event.EventID); err != nil {
					return err
				}
			} else if err := r.touch(action.index, projected.Event.PublicationSeq, projected.Event.EventID); err != nil {
				return err
			}
		}
		return nil
	}
	fact, askUser, err := transcriptAskUserProtocolFact(projected.Event, projected.ResolvedPayloadJSON, payload)
	if err != nil {
		return err
	}
	if askUser {
		switch fact.Kind {
		case "use":
			index, exists := r.askUser.byCallID[fact.CallID]
			if fact.Typed {
				if _, duplicate := r.askUser.typedOrigins[fact.CallID]; duplicate {
					return transcriptstore.ErrEventConflict
				}
				if !exists {
					index = len(r.coordinates)
					r.askUser.byCallID[fact.CallID] = index
					if err := r.append(fact.CallID, projected.Event.ClientMessageID, projected.Event.PublicationSeq, projected.Event.EventID); err != nil {
						return err
					}
				} else if err := r.replaceMessageID(index, projected.Event.ClientMessageID); err != nil {
					return err
				} else if err := r.touch(index, projected.Event.PublicationSeq, projected.Event.EventID); err != nil {
					return err
				}
				r.askUser.typedOrigins[fact.CallID] = fact.Origin
				if projected.Event.RunnerAttempt != nil {
					for _, action := range r.toolHistory.settleAttempt(*projected.Event.RunnerAttempt, "cancelled") {
						if err := r.touch(action.index, projected.Event.PublicationSeq, projected.Event.EventID); err != nil {
							return err
						}
					}
					r.assistantTimeline.toolBoundary(*projected.Event.RunnerAttempt)
				}
				return nil
			}
			if exists {
				if _, typed := r.askUser.typedOrigins[fact.CallID]; typed {
					return nil
				}
				return transcriptstore.ErrEventConflict
			}
			r.askUser.byCallID[fact.CallID] = len(r.coordinates)
			if err := r.append(fact.CallID, projected.Event.ClientMessageID, projected.Event.PublicationSeq, projected.Event.EventID); err != nil {
				return err
			}
			if projected.Event.RunnerAttempt != nil {
				for _, action := range r.toolHistory.settleAttempt(*projected.Event.RunnerAttempt, "cancelled") {
					if err := r.touch(action.index, projected.Event.PublicationSeq, projected.Event.EventID); err != nil {
						return err
					}
				}
				r.assistantTimeline.toolBoundary(*projected.Event.RunnerAttempt)
			}
			return nil
		case "result":
			index, exists := r.askUser.byCallID[fact.CallID]
			if !exists || index < 0 || index >= len(r.coordinates) {
				return transcriptstore.ErrEventConflict
			}
			if fact.Typed {
				origin, typed := r.askUser.typedOrigins[fact.CallID]
				if !typed || origin != fact.Origin {
					return transcriptstore.ErrEventConflict
				}
				if fact.Pending {
					if r.askUser.typedPending[fact.CallID] || r.askUser.typedTerminal[fact.CallID] {
						return transcriptstore.ErrEventConflict
					}
					r.askUser.typedPending[fact.CallID] = true
				} else {
					if !r.askUser.typedPending[fact.CallID] || r.askUser.typedTerminal[fact.CallID] {
						return transcriptstore.ErrEventConflict
					}
					r.askUser.typedTerminal[fact.CallID] = true
				}
			} else if _, typed := r.askUser.typedOrigins[fact.CallID]; typed {
				return transcriptstore.ErrEventConflict
			}
			return r.touch(index, projected.Event.PublicationSeq, projected.Event.EventID)
		}
	}
	toolAction, toolEvent, err := r.toolHistory.consume(projected, payload, len(r.coordinates))
	if err != nil {
		return fmt.Errorf("tool history: %w", err)
	}
	if toolEvent {
		if toolAction.append {
			if err := r.append(toolAction.identity, toolAction.msgID, projected.Event.PublicationSeq, projected.Event.EventID); err != nil {
				return err
			}
			r.assistantTimeline.toolBoundary(toolAction.fact.attempt)
		} else if err := r.touch(toolAction.index, projected.Event.PublicationSeq, projected.Event.EventID); err != nil {
			return fmt.Errorf("tool history coordinate: %w", err)
		}
		return nil
	}
	if attempt, interrupted := transcriptToolHistoryInterruptedAttempt(projected, payload); interrupted {
		for _, action := range r.toolHistory.settleAttempt(attempt, "cancelled") {
			if err := r.touch(action.index, projected.Event.PublicationSeq, projected.Event.EventID); err != nil {
				return err
			}
		}
		// Older writers could resume a bounded correction on the same runner
		// attempt even when today's interruption policy would settle that reason.
		// Preserve that immutable correction boundary so the next durable
		// assistant segment does not collide with the retracted candidate.
		if transcriptWebCorrectionResumeFence(payload) {
			r.assistantTimeline.correctionBoundary(attempt)
		}
		return nil
	}

	if projected.Event.Type == "runner_checkpoint" && projected.Event.RunnerAttempt != nil &&
		transcriptWebRuntimeDrainToolBoundary(payload) {
		r.assistantTimeline.toolBoundary(*projected.Event.RunnerAttempt)
	}
	if projected.Event.Type == "runner_checkpoint" && projected.Event.RunnerAttempt != nil &&
		transcriptWebRuntimeDrainFence(payload) {
		attempt := *projected.Event.RunnerAttempt
		// Corrections always start a fresh candidate segment; runtime drains
		// split only when a durable tool boundary already closed the segment.
		// This mirrors the drain-segment normalizer so identities never collide.
		if transcriptWebCorrectionResumeFence(payload) {
			r.assistantTimeline.correctionBoundary(attempt)
		} else if r.assistantTimeline.splitPending[attempt] {
			r.assistantTimeline.toolBoundary(attempt)
		}
	}

	switch projected.Event.Type {
	case "user_input_response":
		return nil
	case "user_message", "history_user_message":
		messageID := strings.TrimSpace(webString(payload["messageUuid"]))
		if messageID == "" {
			messageID = strings.TrimSpace(webString(payload["_uuid"]))
		}
		if messageID == "" {
			messageID = projected.Event.ClientMessageID
		}
		return r.append(messageID, projected.Event.ClientMessageID, projected.Event.PublicationSeq, projected.Event.EventID)
	case "content_delta", "content_reset", "assistant_message", "history_assistant_message":
		if projected.Event.RunnerAttempt == nil {
			legacyFrameMessage := projected.Event.Type == "assistant_message" &&
				projected.Event.Source == transcriptstore.EventSourceFrameRef
			genesisPayloadMessage := projected.Event.Type == "history_assistant_message" &&
				projected.Event.Source == transcriptstore.EventSourcePayload
			if !legacyFrameMessage && !genesisPayloadMessage {
				return transcriptstore.ErrEventConflict
			}
			return r.append(projected.Event.ClientMessageID, projected.Event.ClientMessageID, projected.Event.PublicationSeq, projected.Event.EventID)
		}
		attempt := *projected.Event.RunnerAttempt
		durableIdentity, err := transcriptAssistantMessageIDFromPayload(payload, r.sessionID, attempt)
		if err != nil {
			return err
		}
		if projected.Event.Type == "content_reset" {
			targetIdentity, narrow, resetErr := transcriptAssistantSegmentResetTarget(payload, r.sessionID, attempt)
			if resetErr != nil {
				return resetErr
			}
			if narrow {
				if transcriptPayloadText(payload) != "" {
					return transcriptstore.ErrEventConflict
				}
				index, retired, resetErr := r.assistantTimeline.resetSegment(attempt, targetIdentity, durableIdentity)
				if resetErr != nil || (retired && (index < 0 || index >= len(r.coordinates))) {
					if resetErr != nil {
						return resetErr
					}
					return transcriptstore.ErrEventConflict
				}
				if !retired {
					return nil
				}
				r.superseded[index] = true
				return r.touch(index, projected.Event.PublicationSeq, projected.Event.EventID)
			}
			for _, index := range r.assistantTimeline.reset(attempt, durableIdentity) {
				if index < 0 || index >= len(r.coordinates) {
					return transcriptstore.ErrEventConflict
				}
				r.superseded[index] = true
				if err := r.touch(index, projected.Event.PublicationSeq, projected.Event.EventID); err != nil {
					return err
				}
			}
			if transcriptPayloadText(payload) == "" {
				return nil
			}
		}
		index, identity, created, err := r.assistantTimeline.content(attempt, projected.Event.EventID, len(r.coordinates), durableIdentity)
		if err != nil {
			return err
		}
		if !created {
			delete(r.superseded, index)
			return r.touch(index, projected.Event.PublicationSeq, projected.Event.EventID)
		}
		return r.append(identity, identity, projected.Event.PublicationSeq, projected.Event.EventID)
	case "runner_finished":
		if projected.Event.RunnerAttempt == nil {
			return transcriptstore.ErrEventConflict
		}
		attempt := *projected.Event.RunnerAttempt
		for _, action := range r.toolHistory.settleAttempt(attempt, strings.TrimSpace(webString(payload["status"]))) {
			if err := r.touch(action.index, projected.Event.PublicationSeq, projected.Event.EventID); err != nil {
				return err
			}
		}
		durableIdentity, err := transcriptAssistantMessageIDFromPayload(payload, r.sessionID, attempt)
		if err != nil {
			return err
		}
		index, identity, created, err := r.assistantTimeline.terminal(attempt, projected.Event.EventID, len(r.coordinates), durableIdentity)
		if err != nil {
			return err
		}
		if !created {
			delete(r.superseded, index)
			return r.touch(index, projected.Event.PublicationSeq, projected.Event.EventID)
		}
		return r.append(identity, identity, projected.Event.PublicationSeq, projected.Event.EventID)
	}
	return nil
}

func (r *transcriptWebCoordinateReducer) finalize() ([]canonicalReadCursorCoordinate, error) {
	coordinates, _, err := r.finalizeWithPublicationRanges()
	return coordinates, err
}

func (r *transcriptWebCoordinateReducer) finalizeWithPublicationRanges() (
	[]canonicalReadCursorCoordinate,
	[]transcriptWebPublicationRange,
	error,
) {
	if err := r.askUser.validate(); err != nil {
		return nil, nil, err
	}
	if err := r.importedRich.validate(); err != nil {
		return nil, nil, err
	}
	if len(r.coordinates) != len(r.publicationRanges) {
		return nil, nil, transcriptstore.ErrEventConflict
	}
	if len(r.superseded) == 0 {
		return r.coordinates, r.publicationRanges, nil
	}
	coordinates := make([]canonicalReadCursorCoordinate, 0, len(r.coordinates)-len(r.superseded))
	ranges := make([]transcriptWebPublicationRange, 0, len(r.publicationRanges)-len(r.superseded))
	for index, coordinate := range r.coordinates {
		if !r.superseded[index] {
			coordinates = append(coordinates, coordinate)
			ranges = append(ranges, r.publicationRanges[index])
		}
	}
	return coordinates, ranges, nil
}

func (s *Server) projectTranscriptWebCoordinates(
	ctx context.Context, stream transcriptstore.Stream, ownerID, sessionID, branchID string,
) ([]canonicalReadCursorCoordinate, transcriptstore.ProjectionSnapshot, error) {
	typedRichHistory, err := s.transcriptStore.HasActiveTypedHistoryBootstrap(ctx, stream.UID, ownerID, stream.Epoch)
	if err != nil {
		return nil, transcriptstore.ProjectionSnapshot{}, err
	}
	const maxSnapshotAttempts = 3
	for attempt := 0; attempt < maxSnapshotAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, transcriptstore.ProjectionSnapshot{}, err
		}
		var snapshot transcriptstore.ProjectionSnapshot
		var err error
		if branchID == "" {
			snapshot, err = s.transcriptStore.GetProjectionSnapshot(ctx, stream.UID, ownerID)
		} else {
			snapshot, err = s.transcriptStore.GetBranchProjectionSnapshot(ctx, stream.UID, ownerID, branchID)
		}
		if err == transcriptstore.ErrBranchStateStale {
			continue
		}
		if err != nil {
			return nil, transcriptstore.ProjectionSnapshot{}, err
		}
		projectedEvents := make([]transcriptstore.ProjectedEvent, 0, 128)
		err = s.visitTranscriptWebProjectionSnapshot(ctx, stream, ownerID, snapshot, true, func(projected transcriptstore.ProjectedEvent) error {
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
			return nil, transcriptstore.ProjectionSnapshot{}, err
		}
		projectedEvents, err = normalizeTranscriptWebProjectionEvents(projectedEvents)
		if err != nil {
			return nil, transcriptstore.ProjectionSnapshot{}, err
		}
		projectedEvents, err = preserveTranscriptTerminalFailureCandidates(projectedEvents)
		if err != nil {
			return nil, transcriptstore.ProjectionSnapshot{}, err
		}
		typedFrameReferences, err := transcriptTypedAskUserFrameReferences(projectedEvents)
		if err != nil {
			return nil, transcriptstore.ProjectionSnapshot{}, err
		}
		reducer := newTranscriptWebCoordinateReducer(sessionID, typedRichHistory)
		for _, projected := range projectedEvents {
			if err := ctx.Err(); err != nil {
				return nil, transcriptstore.ProjectionSnapshot{}, err
			}
			if projected.Event.FrameEventID != nil && typedFrameReferences[*projected.Event.FrameEventID] {
				continue
			}
			if err := reducer.consume(projected); err != nil {
				return nil, transcriptstore.ProjectionSnapshot{}, err
			}
		}
		coordinates, err := reducer.finalize()
		return coordinates, snapshot, err
	}
	return nil, transcriptstore.ProjectionSnapshot{}, transcriptstore.ErrBranchStateStale
}
