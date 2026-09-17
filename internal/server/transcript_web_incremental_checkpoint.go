package server

import (
	"sort"
	"strings"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func transcriptWebIncrementalAssistantStateFromReducer(
	reducer *transcriptWebCoordinateReducer,
	projectedEvents []transcriptstore.ProjectedEvent,
) (transcriptWebIncrementalAssistantCheckpoint, error) {
	if reducer == nil || reducer.assistantTimeline == nil {
		return transcriptWebIncrementalAssistantCheckpoint{}, transcriptstore.ErrEventConflict
	}
	state := transcriptWebIncrementalAssistantCheckpoint{
		Resumable: !transcriptWebHasTrailingLegacyFailureReset(projectedEvents),
	}
	for _, record := range reducer.toolHistory.byIdentity {
		if record.fact.status == "running" {
			state.Resumable = false
			break
		}
	}
	terminalAttempts := map[int64]bool{}
	openAttemptSet := map[int64]bool{}
	currentSyntheticProgress := map[int64]bool{}
	for _, projected := range projectedEvents {
		switch projected.Event.Type {
		case "history_user_message", "history_assistant_message", "history_system_message":
			state.Resumable = false
		}
		if projected.Event.RunnerAttempt == nil {
			continue
		}
		attempt := *projected.Event.RunnerAttempt
		switch projected.Event.Type {
		case "content_delta", "content_reset", "assistant_message":
			if projected.Event.Type == "content_delta" {
				payload, payloadErr := transcriptPayloadObject(projected.ResolvedPayloadJSON)
				if payloadErr != nil {
					return transcriptWebIncrementalAssistantCheckpoint{}, payloadErr
				}
				if transcriptWebRetiredPublicProgress(payload) || boolValue(payload["synthetic_progress"], false) {
					// Synthetic progress is audit-only. It must not create an open
					// assistant attempt in the resumable checkpoint; otherwise a
					// full rebuild and an incremental update expose different rows.
					continue
				}
				currentSyntheticProgress[attempt] = false
			} else {
				currentSyntheticProgress[attempt] = false
			}
			if terminalAttempts[attempt] || attempt <= state.TerminalThroughAttempt {
				state.Resumable = false
			}
			openAttemptSet[attempt] = true
		case "runner_finished":
			if terminalAttempts[attempt] || attempt <= state.TerminalThroughAttempt {
				state.Resumable = false
			}
			terminalAttempts[attempt] = true
			delete(openAttemptSet, attempt)
			if attempt > state.TerminalThroughAttempt {
				state.TerminalThroughAttempt = attempt
			}
			delete(currentSyntheticProgress, attempt)
		}
	}
	seenAttemptSet := map[int64]bool{}
	for attempt := range reducer.assistantTimeline.seen {
		if !terminalAttempts[attempt] && attempt > state.TerminalThroughAttempt {
			seenAttemptSet[attempt] = true
		}
	}
	for attempt := range reducer.assistantTimeline.current {
		if !terminalAttempts[attempt] && attempt > state.TerminalThroughAttempt {
			seenAttemptSet[attempt] = true
		}
	}
	for attempt := range reducer.assistantTimeline.segments {
		if !terminalAttempts[attempt] && attempt > state.TerminalThroughAttempt {
			seenAttemptSet[attempt] = true
		}
	}
	for attempt := range reducer.assistantTimeline.splitPending {
		if !terminalAttempts[attempt] && attempt > state.TerminalThroughAttempt {
			seenAttemptSet[attempt] = true
		}
	}
	for attempt := range openAttemptSet {
		if attempt <= state.TerminalThroughAttempt {
			state.Resumable = false
		}
		seenAttemptSet[attempt] = true
	}
	if len(seenAttemptSet) > 1 {
		state.Resumable = false
	}
	if !state.Resumable {
		return transcriptWebIncrementalAssistantCheckpoint{
			Resumable: false, TerminalThroughAttempt: state.TerminalThroughAttempt,
		}, nil
	}
	for attempt := range seenAttemptSet {
		state.SeenAttempts = append(state.SeenAttempts, attempt)
	}
	sort.Slice(state.SeenAttempts, func(i, j int) bool { return state.SeenAttempts[i] < state.SeenAttempts[j] })
	for identity := range reducer.assistantTimeline.seenIdentity {
		for attempt := range seenAttemptSet {
			if transcriptWebAssistantIdentityBelongsToAttempt(identity, reducer.sessionID, attempt) {
				state.SeenIdentities = append(state.SeenIdentities, identity)
			}
		}
	}
	sort.Strings(state.SeenIdentities)
	for _, attempt := range state.SeenAttempts {
		value := transcriptWebIncrementalAssistantAttemptCheckpoint{Attempt: attempt}
		if identity := strings.TrimSpace(reducer.assistantTimeline.currentIdentity[attempt]); identity != "" {
			value.CurrentIdentity = identity
		}
		for _, index := range reducer.assistantTimeline.segments[attempt] {
			if index < 0 || index >= len(reducer.coordinates) {
				return transcriptWebIncrementalAssistantCheckpoint{}, transcriptstore.ErrEventConflict
			}
			// Narrow correction resets deliberately retain retired segment indexes
			// in the assistant timeline so their durable identities cannot be
			// reused. They are not visible records and therefore must not be part
			// of the resumable materialized-segment checkpoint.
			if reducer.superseded[index] {
				continue
			}
			identity := strings.TrimSpace(reducer.coordinates[index].id)
			if identity == "" {
				return transcriptWebIncrementalAssistantCheckpoint{}, transcriptstore.ErrEventConflict
			}
			value.SegmentIdentities = append(value.SegmentIdentities, identity)
		}
		value.SplitPending = reducer.assistantTimeline.splitPending[attempt]
		value.CurrentIsSyntheticProgress = currentSyntheticProgress[attempt]
		state.Attempts = append(state.Attempts, value)
	}
	return state, nil
}

func transcriptWebAssistantIdentityBelongsToAttempt(identity, sessionID string, attempt int64) bool {
	base := transcriptAssistantMessageID(sessionID, attempt, 1)
	return identity == base || strings.HasPrefix(identity, base+"-")
}

func transcriptWebAttachIncrementalAssistantRecords(
	state transcriptWebIncrementalAssistantCheckpoint,
	records []transcriptstore.TranscriptWebMessageRecord,
) (transcriptWebIncrementalAssistantCheckpoint, error) {
	byIdentity := make(map[string]transcriptstore.TranscriptWebMessageRecord, len(records)*2)
	for _, record := range records {
		for _, identity := range []string{record.MessageID, record.ClientMessageID} {
			identity = strings.TrimSpace(identity)
			if identity == "" {
				continue
			}
			if prior, exists := byIdentity[identity]; exists && prior.Ordinal != record.Ordinal {
				return transcriptWebIncrementalAssistantCheckpoint{}, transcriptstore.ErrEventConflict
			}
			byIdentity[identity] = record
		}
	}
	for index := range state.Attempts {
		attempt := &state.Attempts[index]
		if attempt.CurrentIdentity == "" {
			continue
		}
		record, found := byIdentity[attempt.CurrentIdentity]
		if !found || !record.Visible || record.VisibleIndex == nil {
			return transcriptWebIncrementalAssistantCheckpoint{}, transcriptstore.ErrEventConflict
		}
		copy := record
		copy.MessageJSON = append([]byte(nil), record.MessageJSON...)
		copy.ArtifactReferences = append([]transcriptstore.TranscriptWebMessageArtifactReference(nil), record.ArtifactReferences...)
		attempt.CurrentMessage = &copy
	}
	if err := validateTranscriptWebIncrementalAssistantCheckpoint(state); err != nil {
		return transcriptWebIncrementalAssistantCheckpoint{}, err
	}
	return state, nil
}

func validateTranscriptWebIncrementalAssistantCheckpoint(state transcriptWebIncrementalAssistantCheckpoint) error {
	if state.TerminalThroughAttempt < 0 || len(state.SeenAttempts) > 1 || len(state.Attempts) > 1 {
		return transcriptstore.ErrEventConflict
	}
	seenAttempts := map[int64]bool{}
	for index, attempt := range state.SeenAttempts {
		if attempt <= state.TerminalThroughAttempt || (index > 0 && state.SeenAttempts[index-1] >= attempt) {
			return transcriptstore.ErrEventConflict
		}
		seenAttempts[attempt] = true
	}
	seenIdentities := map[string]bool{}
	for index, identity := range state.SeenIdentities {
		if strings.TrimSpace(identity) != identity || identity == "" ||
			(index > 0 && state.SeenIdentities[index-1] >= identity) {
			return transcriptstore.ErrEventConflict
		}
		seenIdentities[identity] = true
	}
	if len(state.Attempts) != len(state.SeenAttempts) {
		return transcriptstore.ErrEventConflict
	}
	if len(state.SeenAttempts) == 0 && len(state.SeenIdentities) != 0 {
		return transcriptstore.ErrEventConflict
	}
	lastAttempt := int64(0)
	for _, attempt := range state.Attempts {
		if attempt.Attempt <= lastAttempt || !seenAttempts[attempt.Attempt] {
			return transcriptstore.ErrEventConflict
		}
		lastAttempt = attempt.Attempt
		segmentSet := map[string]bool{}
		for _, identity := range attempt.SegmentIdentities {
			if strings.TrimSpace(identity) != identity || identity == "" || segmentSet[identity] || !seenIdentities[identity] {
				return transcriptstore.ErrEventConflict
			}
			segmentSet[identity] = true
		}
		if attempt.CurrentIdentity == "" {
			if attempt.CurrentMessage != nil || attempt.CurrentIsSyntheticProgress || len(attempt.SegmentIdentities) != 0 || attempt.SplitPending {
				return transcriptstore.ErrEventConflict
			}
		} else {
			if !seenIdentities[attempt.CurrentIdentity] || !segmentSet[attempt.CurrentIdentity] || attempt.CurrentMessage == nil ||
				attempt.CurrentMessage.MessageID != attempt.CurrentIdentity || !attempt.CurrentMessage.Visible ||
				attempt.CurrentMessage.VisibleIndex == nil ||
				transcriptstore.TranscriptWebSHA256(attempt.CurrentMessage.MessageJSON) != attempt.CurrentMessage.MessageSHA256 {
				return transcriptstore.ErrEventConflict
			}
		}
		if attempt.Rollback != nil {
			before := attempt.Rollback.BeforeAttempt
			rollbackIdentities := map[string]bool{}
			for index, identity := range attempt.Rollback.SeenIdentities {
				if strings.TrimSpace(identity) != identity || identity == "" ||
					(index > 0 && attempt.Rollback.SeenIdentities[index-1] >= identity) {
					return transcriptstore.ErrEventConflict
				}
				rollbackIdentities[identity] = true
			}
			if before.Attempt != attempt.Attempt || before.Rollback != nil || before.SplitPending ||
				before.CurrentIdentity == "" || len(before.SegmentIdentities) != 1 ||
				before.SegmentIdentities[0] != before.CurrentIdentity || !rollbackIdentities[before.CurrentIdentity] ||
				before.CurrentMessage == nil || before.CurrentMessage.MessageID != before.CurrentIdentity ||
				!before.CurrentMessage.Visible || before.CurrentMessage.VisibleIndex == nil ||
				transcriptstore.TranscriptWebSHA256(before.CurrentMessage.MessageJSON) != before.CurrentMessage.MessageSHA256 {
				return transcriptstore.ErrEventConflict
			}
		}
	}
	return nil
}

func transcriptWebHasTrailingLegacyFailureReset(projectedEvents []transcriptstore.ProjectedEvent) bool {
	trailing := map[int64]bool{}
	for _, projected := range projectedEvents {
		if projected.Event.RunnerAttempt == nil {
			continue
		}
		attempt := *projected.Event.RunnerAttempt
		switch projected.Event.Type {
		case "content_delta", "assistant_message", "history_assistant_message", "runner_finished":
			delete(trailing, attempt)
		case "content_reset":
			payload, err := transcriptPayloadObject(projected.ResolvedPayloadJSON)
			if err != nil || !transcriptLegacyTerminalFailureReset(payload) {
				delete(trailing, attempt)
			} else {
				trailing[attempt] = true
			}
		}
	}
	return len(trailing) > 0
}
