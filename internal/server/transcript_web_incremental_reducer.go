package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	transcriptstore "synon-go/internal/persistence/transcript"
	"time"
)

func newTranscriptWebIncrementalAssistantReducer(
	checkpoint transcriptWebIncrementalAssistantCheckpoint,
	fence transcriptstore.TranscriptWebProjectionFence,
	sessionID, branchID string,
) (*transcriptWebIncrementalAssistantReducer, error) {
	if err := validateTranscriptWebIncrementalAssistantCheckpoint(checkpoint); err != nil {
		return nil, err
	}
	reducer := &transcriptWebIncrementalAssistantReducer{
		attempts:     map[int64]*transcriptWebIncrementalAssistantAttemptCheckpoint{},
		seenAttempts: map[int64]bool{}, seenIdentities: map[string]bool{},
		changed:          map[int]transcriptstore.TranscriptWebMessageRecord{},
		baseMessageCount: fence.StateMessageCount, messageCount: fence.StateMessageCount,
		visibleCount:  fence.VisibleMessageCount,
		artifactCount: fence.StateArtifactReferenceCount, sessionID: strings.TrimSpace(sessionID),
		branchID: strings.TrimSpace(branchID), terminalThroughAttempt: checkpoint.TerminalThroughAttempt,
	}
	for _, attempt := range checkpoint.SeenAttempts {
		reducer.seenAttempts[attempt] = true
	}
	for _, identity := range checkpoint.SeenIdentities {
		reducer.seenIdentities[identity] = true
	}
	for index := range checkpoint.Attempts {
		value := checkpoint.Attempts[index]
		reducer.attempts[value.Attempt] = &value
	}
	return reducer, nil
}

func (r *transcriptWebIncrementalAssistantReducer) finish() (
	transcriptWebIncrementalAssistantCheckpoint,
	[]transcriptstore.TranscriptWebMessageRecord,
	[]transcriptstore.TranscriptWebMessageArtifactReference,
	error,
) {
	state := transcriptWebIncrementalAssistantCheckpoint{
		Resumable: true, TerminalThroughAttempt: r.terminalThroughAttempt,
	}
	for attempt := range r.seenAttempts {
		state.SeenAttempts = append(state.SeenAttempts, attempt)
	}
	sort.Slice(state.SeenAttempts, func(i, j int) bool { return state.SeenAttempts[i] < state.SeenAttempts[j] })
	for identity := range r.seenIdentities {
		state.SeenIdentities = append(state.SeenIdentities, identity)
	}
	sort.Strings(state.SeenIdentities)
	for _, attempt := range state.SeenAttempts {
		if value := r.attempts[attempt]; value != nil {
			state.Attempts = append(state.Attempts, *value)
		}
	}
	if err := validateTranscriptWebIncrementalAssistantCheckpoint(state); err != nil {
		return transcriptWebIncrementalAssistantCheckpoint{}, nil, nil, err
	}
	ordinals := make([]int, 0, len(r.changed))
	for ordinal := range r.changed {
		ordinals = append(ordinals, ordinal)
	}
	sort.Ints(ordinals)
	messages := make([]transcriptstore.TranscriptWebMessageRecord, 0, len(ordinals))
	references := make([]transcriptstore.TranscriptWebMessageArtifactReference, 0)
	for _, ordinal := range ordinals {
		record := r.changed[ordinal]
		messages = append(messages, record)
		references = append(references, record.ArtifactReferences...)
	}
	return state, messages, references, nil
}

func (r *transcriptWebIncrementalAssistantReducer) appendRecord(
	record transcriptstore.TranscriptWebMessageRecord,
) error {
	if record.Ordinal != r.messageCount+1 || !record.Visible || record.VisibleIndex == nil ||
		*record.VisibleIndex != r.visibleCount || r.changed[record.Ordinal].Ordinal != 0 {
		return transcriptstore.ErrEventConflict
	}
	r.messageCount++
	r.visibleCount++
	r.artifactCount += len(record.ArtifactReferences)
	r.changed[record.Ordinal] = record
	return nil
}

func (r *transcriptWebIncrementalAssistantReducer) updateRecord(
	prior, record transcriptstore.TranscriptWebMessageRecord,
) error {
	if prior.Ordinal <= 0 || prior.Ordinal != record.Ordinal || prior.MessageID != record.MessageID ||
		prior.ClientMessageID != record.ClientMessageID || prior.FirstPublicationSequence != record.FirstPublicationSequence ||
		prior.Visible != record.Visible || prior.VisibleIndex == nil || record.VisibleIndex == nil ||
		*prior.VisibleIndex != *record.VisibleIndex {
		return transcriptstore.ErrEventConflict
	}
	r.artifactCount += len(record.ArtifactReferences) - len(prior.ArtifactReferences)
	if r.artifactCount < 0 {
		return transcriptstore.ErrEventConflict
	}
	r.changed[record.Ordinal] = record
	return nil
}

func (r *transcriptWebIncrementalAssistantReducer) replaceTailRecord(
	prior *transcriptstore.TranscriptWebMessageRecord,
	replacement *transcriptstore.TranscriptWebMessageRecord,
) error {
	if prior == nil || prior.Ordinal != r.messageCount || !prior.Visible || prior.VisibleIndex == nil ||
		*prior.VisibleIndex != r.visibleCount-1 {
		return errTranscriptWebIncrementalAssistantFallback
	}
	newInCurrentBatch := prior.Ordinal > r.baseMessageCount
	if !newInCurrentBatch {
		if r.tailReplaceFrom != 0 {
			return errTranscriptWebIncrementalAssistantFallback
		}
		r.tailReplaceFrom = prior.Ordinal
	}
	delete(r.changed, prior.Ordinal)
	r.messageCount--
	r.visibleCount--
	r.artifactCount -= len(prior.ArtifactReferences)
	if r.messageCount < 0 || r.visibleCount < 0 || r.artifactCount < 0 {
		return transcriptstore.ErrEventConflict
	}
	if replacement != nil {
		if replacement.Ordinal != prior.Ordinal || replacement.VisibleIndex == nil ||
			*replacement.VisibleIndex != r.visibleCount {
			return transcriptstore.ErrEventConflict
		}
		if err := r.appendRecord(*replacement); err != nil {
			return err
		}
	}
	return nil
}

func (r *transcriptWebIncrementalAssistantReducer) attempt(attempt int64) *transcriptWebIncrementalAssistantAttemptCheckpoint {
	if value := r.attempts[attempt]; value != nil {
		return value
	}
	value := &transcriptWebIncrementalAssistantAttemptCheckpoint{Attempt: attempt}
	r.attempts[attempt] = value
	return value
}

func transcriptWebIncrementalBuildRecord(
	message map[string]any,
	referenceMaps []map[string]any,
	ordinal, visibleIndex int,
	firstPublication, lastPublication int64,
	lastEventID int64,
	updatedAt time.Time,
) (transcriptstore.TranscriptWebMessageRecord, error) {
	messageID := strings.TrimSpace(webString(message["id"]))
	clientMessageID := strings.TrimSpace(webString(message["msg_id"]))
	if messageID == "" || clientMessageID == "" || ordinal <= 0 || visibleIndex < 0 ||
		firstPublication <= 0 || lastPublication < firstPublication || lastEventID <= 0 || updatedAt.IsZero() {
		return transcriptstore.TranscriptWebMessageRecord{}, transcriptstore.ErrEventConflict
	}
	raw, err := json.Marshal(message)
	if err != nil || !json.Valid(raw) {
		return transcriptstore.TranscriptWebMessageRecord{}, transcriptstore.ErrEventConflict
	}
	references, err := transcriptWebMessageReferenceRecords(referenceMaps, ordinal, lastEventID)
	if err != nil {
		return transcriptstore.TranscriptWebMessageRecord{}, err
	}
	for index := range references {
		references[index].MessageOrdinal = ordinal
	}
	visible := visibleIndex
	return transcriptstore.TranscriptWebMessageRecord{
		Ordinal: ordinal, MessageID: messageID, ClientMessageID: clientMessageID,
		Visible: true, VisibleIndex: &visible, MessageJSON: raw,
		MessageSHA256:            transcriptstore.TranscriptWebSHA256(raw),
		FirstPublicationSequence: firstPublication, LastPublicationSequence: lastPublication,
		UpdatedAt: updatedAt.UTC(), ArtifactReferences: references,
	}, nil
}

func (r *transcriptWebIncrementalAssistantReducer) consumeAssistantEvent(
	ctx context.Context,
	server *Server,
	stream transcriptstore.Stream,
	projected transcriptstore.ProjectedEvent,
) error {
	if projected.Event.RunnerAttempt == nil || *projected.Event.RunnerAttempt <= 0 {
		return transcriptstore.ErrEventConflict
	}
	attemptNumber := *projected.Event.RunnerAttempt
	if attemptNumber <= r.terminalThroughAttempt {
		return transcriptstore.ErrEventConflict
	}
	if r.attempts[attemptNumber] == nil && len(r.attempts) != 0 {
		return transcriptstore.ErrEventConflict
	}
	payload, err := transcriptPayloadObject(projected.ResolvedPayloadJSON)
	if err != nil {
		return err
	}
	attempt := r.attempt(attemptNumber)
	switch projected.Event.Type {
	case "content_delta", "assistant_message":
		attempt.Rollback = nil
		return r.consumeAssistantContent(attempt, projected, payload)
	case "content_reset":
		return r.consumeAssistantReset(attempt, projected, payload)
	case "runner_finished":
		projection, err := server.transcriptStore.GetTerminalProjection(
			ctx, stream.OwnerID, stream.UID, projected.Event.EventID,
		)
		if err != nil {
			return err
		}
		if projection.Attempt != attempt.Attempt || projection.EventID != projected.Event.EventID ||
			projection.PublicationSeq != projected.Event.PublicationSeq {
			return transcriptstore.ErrEventConflict
		}
		if err := r.consumeAssistantTerminal(attempt, projected, payload, projection); err != nil {
			return err
		}
		r.compactTerminalAttempt(attemptNumber)
		return nil
	default:
		return errTranscriptWebIncrementalAssistantFallback
	}
}

// compactTerminalAttempt discards reducer state already represented by the
// immutable source chain and materialized message identity rows. Runner
// attempts are monotonic per stream, so one scalar fence rejects a later event
// that tries to reopen a completed attempt. The sole open attempt remains fully
// restartable, including reset rollback state.
func (r *transcriptWebIncrementalAssistantReducer) compactTerminalAttempt(attempt int64) {
	r.terminalThroughAttempt = attempt
	r.attempts = map[int64]*transcriptWebIncrementalAssistantAttemptCheckpoint{}
	r.seenAttempts = map[int64]bool{}
	r.seenIdentities = map[string]bool{}
}

func (r *transcriptWebIncrementalAssistantReducer) consumeAssistantContent(
	attempt *transcriptWebIncrementalAssistantAttemptCheckpoint,
	projected transcriptstore.ProjectedEvent,
	payload map[string]any,
) error {
	if transcriptWebRetiredPublicProgress(payload) {
		return nil
	}
	durableIdentity, err := transcriptAssistantMessageIDFromPayload(payload, r.sessionID, attempt.Attempt)
	if err != nil {
		return err
	}
	text := transcriptPayloadText(payload)
	syntheticProgress := projected.Event.Type == "content_delta" && boolValue(payload["synthetic_progress"], false)
	// Synthetic progress is retained in the durable audit chain, but it is not
	// a user-facing assistant message. Skip it before identity allocation so
	// full and incremental projections share the same immutable visible
	// timeline and a later real delta starts the single visible assistant row.
	if syntheticProgress {
		return nil
	}
	if transcriptWebIncrementalSyntheticProgressIsStale(
		syntheticProgress, durableIdentity, attempt.CurrentIdentity,
		attempt.CurrentIsSyntheticProgress, r.seenIdentities,
	) {
		return nil
	}
	continueCurrent := attempt.CurrentMessage != nil &&
		(!attempt.SplitPending || (durableIdentity != "" && durableIdentity == attempt.CurrentIdentity))
	if !continueCurrent {
		identity, durable, err := r.allocateAssistantIdentity(attempt.Attempt, projected.Event.EventID, durableIdentity)
		if err != nil {
			return err
		}
		messageType := transcriptWebAssistantMessageType(payload)
		content := transcriptWebAssistantContent(text, messageType)
		content["synonBiomed"] = map[string]any{"messageIndex": r.visibleCount, "blockIndex": 0, "branchId": r.branchID}
		if durable {
			content["assistant_attempt_id"] = transcriptAssistantMessageID(r.sessionID, attempt.Attempt, 1)
		}
		message := map[string]any{
			"id": identity, "msg_id": identity, "conversation_id": r.sessionID,
			"type": messageType, "position": "left", "status": "work",
			"created_at": projected.Event.CreatedAt.UnixMilli(), "content": content,
		}
		referenceMaps := []map[string]any{}
		if len(projected.ArtifactReferences) > 0 {
			referenceMaps = transcriptWebIncrementalReferenceMaps(projected.ArtifactReferences)
		}
		record, err := transcriptWebIncrementalBuildRecord(
			message, referenceMaps, r.messageCount+1, r.visibleCount,
			projected.Event.PublicationSeq, projected.Event.PublicationSeq,
			projected.Event.EventID, projected.Event.CreatedAt,
		)
		if err != nil {
			return err
		}
		if err := r.appendRecord(record); err != nil {
			return err
		}
		attempt.CurrentIdentity = identity
		attempt.SegmentIdentities = append(attempt.SegmentIdentities, identity)
		attempt.SplitPending = false
		attempt.CurrentMessage = cloneTranscriptWebIncrementalRecord(&record)
		attempt.CurrentIsSyntheticProgress = syntheticProgress
		return nil
	}
	if durableIdentity != "" && durableIdentity != attempt.CurrentIdentity {
		return transcriptstore.ErrEventConflict
	}
	prior := cloneTranscriptWebIncrementalRecord(attempt.CurrentMessage)
	message, err := transcriptWebIncrementalMessageMap(*prior)
	if err != nil {
		return err
	}
	if !transcriptWebAssistantMessageAcceptsPayload(message, payload) {
		return errTranscriptWebIncrementalAssistantFallback
	}
	content, ok := message["content"].(map[string]any)
	if !ok {
		return transcriptstore.ErrEventConflict
	}
	if durableIdentity != "" {
		content["assistant_attempt_id"] = transcriptAssistantMessageID(r.sessionID, attempt.Attempt, 1)
	}
	if projected.Event.Type == "assistant_message" {
		content["content"] = text
		attempt.CurrentIsSyntheticProgress = false
	} else if syntheticProgress {
		// Synthetic progress is transient UI state. Repeated checkpoints replace
		// the placeholder instead of becoming provider-authored transcript text;
		// once real content exists, late progress cannot overwrite it.
		if attempt.CurrentIsSyntheticProgress {
			content["content"] = text
		}
	} else {
		if attempt.CurrentIsSyntheticProgress {
			content["content"] = text
		} else {
			content["content"] = webString(content["content"]) + text
		}
		attempt.CurrentIsSyntheticProgress = false
	}
	referenceMaps := transcriptWebStoredReferenceMaps(prior.ArtifactReferences)
	if len(projected.ArtifactReferences) > 0 {
		referenceMaps = transcriptWebIncrementalReferenceMaps(projected.ArtifactReferences)
	}
	record, err := transcriptWebIncrementalBuildRecord(
		message, referenceMaps, prior.Ordinal, *prior.VisibleIndex,
		prior.FirstPublicationSequence, projected.Event.PublicationSeq,
		projected.Event.EventID, prior.UpdatedAt,
	)
	if err != nil {
		return err
	}
	if err := r.updateRecord(*prior, record); err != nil {
		return err
	}
	attempt.CurrentMessage = cloneTranscriptWebIncrementalRecord(&record)
	return nil
}

func (r *transcriptWebIncrementalAssistantReducer) allocateAssistantIdentity(
	attempt, eventID int64,
	durableIdentity string,
) (string, bool, error) {
	durableIdentity = strings.TrimSpace(durableIdentity)
	identity := durableIdentity
	wasSeen := r.seenAttempts[attempt]
	if identity == "" {
		identity = fmt.Sprintf("assistant-%s-%d", r.sessionID, attempt)
		if wasSeen {
			identity = fmt.Sprintf("assistant-%s-%d-event-%d", r.sessionID, attempt, eventID)
		}
	}
	if identity == "" || eventID <= 0 || r.seenIdentities[identity] {
		return "", false, transcriptstore.ErrEventConflict
	}
	r.seenAttempts[attempt] = true
	r.seenIdentities[identity] = true
	return identity, durableIdentity != "", nil
}

func (r *transcriptWebIncrementalAssistantReducer) consumeAssistantReset(
	attempt *transcriptWebIncrementalAssistantAttemptCheckpoint,
	projected transcriptstore.ProjectedEvent,
	payload map[string]any,
) error {
	legacyFailureReset := transcriptLegacyTerminalFailureReset(payload)
	if !legacyFailureReset {
		attempt.Rollback = nil
	}
	if attempt.CurrentMessage != nil {
		if len(attempt.SegmentIdentities) != 1 || attempt.SegmentIdentities[0] != attempt.CurrentIdentity ||
			attempt.CurrentMessage.Ordinal != r.messageCount {
			return errTranscriptWebIncrementalAssistantFallback
		}
		if legacyFailureReset && attempt.Rollback == nil {
			seen := make([]string, 0, len(r.seenIdentities))
			for identity := range r.seenIdentities {
				seen = append(seen, identity)
			}
			sort.Strings(seen)
			attempt.Rollback = &transcriptWebIncrementalAssistantRollback{
				BeforeAttempt:  cloneTranscriptWebIncrementalAttempt(*attempt),
				SeenIdentities: seen,
			}
		}
		prior := cloneTranscriptWebIncrementalRecord(attempt.CurrentMessage)
		attempt.CurrentIdentity = ""
		attempt.CurrentMessage = nil
		attempt.CurrentIsSyntheticProgress = false
		attempt.SegmentIdentities = nil
		attempt.SplitPending = false
		text := transcriptPayloadText(payload)
		if text == "" {
			return r.replaceTailRecord(prior, nil)
		}
		record, identity, err := r.newAssistantRecord(
			attempt, projected, payload, text, "work", prior.Ordinal, *prior.VisibleIndex,
		)
		if err != nil {
			return err
		}
		if err := r.replaceTailRecord(prior, &record); err != nil {
			return err
		}
		attempt.CurrentIdentity = identity
		attempt.CurrentMessage = cloneTranscriptWebIncrementalRecord(&record)
		attempt.CurrentIsSyntheticProgress = false
		attempt.SegmentIdentities = []string{identity}
		return nil
	}
	if len(attempt.SegmentIdentities) != 0 || attempt.SplitPending {
		return errTranscriptWebIncrementalAssistantFallback
	}
	text := transcriptPayloadText(payload)
	if text == "" {
		return nil
	}
	record, identity, err := r.newAssistantRecord(
		attempt, projected, payload, text, "work", r.messageCount+1, r.visibleCount,
	)
	if err != nil {
		return err
	}
	if err := r.appendRecord(record); err != nil {
		return err
	}
	attempt.CurrentIdentity = identity
	attempt.CurrentMessage = cloneTranscriptWebIncrementalRecord(&record)
	attempt.CurrentIsSyntheticProgress = false
	attempt.SegmentIdentities = []string{identity}
	return nil
}

func (r *transcriptWebIncrementalAssistantReducer) newAssistantRecord(
	attempt *transcriptWebIncrementalAssistantAttemptCheckpoint,
	projected transcriptstore.ProjectedEvent,
	payload map[string]any,
	text, status string,
	ordinal, visibleIndex int,
) (transcriptstore.TranscriptWebMessageRecord, string, error) {
	durableIdentity, err := transcriptAssistantMessageIDFromPayload(payload, r.sessionID, attempt.Attempt)
	if err != nil {
		return transcriptstore.TranscriptWebMessageRecord{}, "", err
	}
	identity, durable, err := r.allocateAssistantIdentity(attempt.Attempt, projected.Event.EventID, durableIdentity)
	if err != nil {
		return transcriptstore.TranscriptWebMessageRecord{}, "", err
	}
	messageType := transcriptWebAssistantMessageType(payload)
	content := transcriptWebAssistantContent(text, messageType)
	content["synonBiomed"] = map[string]any{"messageIndex": visibleIndex, "blockIndex": 0, "branchId": r.branchID}
	if durable {
		content["assistant_attempt_id"] = transcriptAssistantMessageID(r.sessionID, attempt.Attempt, 1)
	}
	message := map[string]any{
		"id": identity, "msg_id": identity, "conversation_id": r.sessionID,
		"type": messageType, "position": "left", "status": status,
		"created_at": projected.Event.CreatedAt.UnixMilli(), "content": content,
	}
	referenceMaps := []map[string]any{}
	if len(projected.ArtifactReferences) > 0 {
		referenceMaps = transcriptWebIncrementalReferenceMaps(projected.ArtifactReferences)
	}
	record, err := transcriptWebIncrementalBuildRecord(
		message, referenceMaps, ordinal, visibleIndex,
		projected.Event.PublicationSeq, projected.Event.PublicationSeq,
		projected.Event.EventID, projected.Event.CreatedAt,
	)
	return record, identity, err
}

func (r *transcriptWebIncrementalAssistantReducer) consumeAssistantTerminal(
	attempt *transcriptWebIncrementalAssistantAttemptCheckpoint,
	projected transcriptstore.ProjectedEvent,
	payload map[string]any,
	projection transcriptstore.TerminalProjection,
) error {
	if projection.TerminalStatus == "failed" || projection.TerminalStatus == "cancelled" ||
		projection.TerminalStatus == "canceled" {
		if attempt.Rollback != nil {
			if err := r.restoreAssistantRollback(attempt); err != nil {
				return err
			}
		}
	}
	attempt.Rollback = nil
	durableIdentity, err := transcriptAssistantMessageIDFromPayload(payload, r.sessionID, attempt.Attempt)
	if err != nil {
		return err
	}
	status := "finish"
	if projection.TerminalStatus == "failed" && !projection.Superseded {
		status = "error"
	}
	attachCurrent := attempt.CurrentMessage != nil &&
		(!attempt.SplitPending || durableIdentity == attempt.CurrentIdentity || r.seenIdentities[durableIdentity])
	if !attachCurrent {
		identity, durable, err := r.allocateAssistantIdentity(attempt.Attempt, projected.Event.EventID, durableIdentity)
		if err != nil {
			return err
		}
		content := map[string]any{
			"content":     projection.Detail,
			"synonBiomed": map[string]any{"messageIndex": r.visibleCount, "blockIndex": 0, "branchId": r.branchID},
		}
		if durable {
			content["assistant_attempt_id"] = transcriptAssistantMessageID(r.sessionID, attempt.Attempt, 1)
		}
		message := map[string]any{
			"id": identity, "msg_id": identity, "conversation_id": r.sessionID,
			"type": "text", "position": "left", "status": status,
			"terminal_status": projection.TerminalStatus,
			"created_at":      projected.Event.CreatedAt.UnixMilli(), "content": content,
		}
		if projection.Superseded {
			message["terminal_superseded"] = true
		}
		record, err := transcriptWebIncrementalBuildRecord(
			message, transcriptWebIncrementalReferenceMaps(projection.ArtifactReferences),
			r.messageCount+1, r.visibleCount,
			projected.Event.PublicationSeq, projected.Event.PublicationSeq,
			projected.Event.EventID, projected.Event.CreatedAt,
		)
		if err != nil {
			return err
		}
		if err := r.appendRecord(record); err != nil {
			return err
		}
		attempt.CurrentIdentity = identity
		attempt.SegmentIdentities = append(attempt.SegmentIdentities, identity)
		attempt.SplitPending = false
		attempt.CurrentMessage = cloneTranscriptWebIncrementalRecord(&record)
		attempt.CurrentIsSyntheticProgress = false
		return nil
	}
	if durableIdentity != "" && durableIdentity != attempt.CurrentIdentity && !r.seenIdentities[durableIdentity] {
		return transcriptstore.ErrEventConflict
	}
	prior := cloneTranscriptWebIncrementalRecord(attempt.CurrentMessage)
	message, err := transcriptWebIncrementalMessageMap(*prior)
	if err != nil {
		return err
	}
	if durableIdentity != "" {
		content, ok := message["content"].(map[string]any)
		if !ok {
			return transcriptstore.ErrEventConflict
		}
		content["assistant_attempt_id"] = transcriptAssistantMessageID(r.sessionID, attempt.Attempt, 1)
	}
	if attempt.CurrentIsSyntheticProgress {
		content, ok := message["content"].(map[string]any)
		if !ok {
			return transcriptstore.ErrEventConflict
		}
		content["content"] = projection.Detail
		attempt.CurrentIsSyntheticProgress = false
	}
	message["status"] = status
	if err := transcriptWebSettleAssistantMessage(message, status); err != nil {
		return err
	}
	message["terminal_status"] = projection.TerminalStatus
	if projection.Superseded {
		message["terminal_superseded"] = true
	}
	record, err := transcriptWebIncrementalBuildRecord(
		message, transcriptWebIncrementalReferenceMaps(projection.ArtifactReferences),
		prior.Ordinal, *prior.VisibleIndex,
		prior.FirstPublicationSequence, projected.Event.PublicationSeq,
		projected.Event.EventID, prior.UpdatedAt,
	)
	if err != nil {
		return err
	}
	if err := r.updateRecord(*prior, record); err != nil {
		return err
	}
	attempt.CurrentMessage = cloneTranscriptWebIncrementalRecord(&record)
	return nil
}
