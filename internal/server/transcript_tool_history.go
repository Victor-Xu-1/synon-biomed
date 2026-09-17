package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

type transcriptAssistantTimelineState struct {
	current         map[int64]int
	currentIdentity map[int64]string
	pendingIdentity map[int64]string
	segments        map[int64][]int
	seen            map[int64]bool
	seenIdentity    map[string]bool
	splitPending    map[int64]bool
	pendingMaySkip  map[int64]bool
	sessionID       string
}

func newTranscriptAssistantTimelineState(sessionID string) *transcriptAssistantTimelineState {
	return &transcriptAssistantTimelineState{
		current: map[int64]int{}, segments: map[int64][]int{}, seen: map[int64]bool{},
		currentIdentity: map[int64]string{}, pendingIdentity: map[int64]string{}, seenIdentity: map[string]bool{},
		splitPending: map[int64]bool{}, pendingMaySkip: map[int64]bool{}, sessionID: strings.TrimSpace(sessionID),
	}
}

func (s *transcriptAssistantTimelineState) content(attempt, eventID int64, nextIndex int, durableIdentity string) (int, string, bool, error) {
	if index, found := s.current[attempt]; found && (!s.splitPending[attempt] || durableIdentity == s.currentIdentity[attempt]) {
		if durableIdentity != "" && durableIdentity != s.currentIdentity[attempt] {
			return 0, "", false, transcriptstore.ErrEventConflict
		}
		return index, "", false, nil
	}
	if err := s.consumePendingIdentity(attempt, durableIdentity); err != nil {
		return 0, "", false, err
	}
	identity := durableIdentity
	if identity == "" {
		identity = fmt.Sprintf("assistant-%s-%d", s.sessionID, attempt)
		if s.seen[attempt] {
			identity = fmt.Sprintf("assistant-%s-%d-event-%d", s.sessionID, attempt, eventID)
		}
	}
	if s.seenIdentity[identity] {
		return 0, "", false, transcriptstore.ErrEventConflict
	}
	s.current[attempt] = nextIndex
	s.currentIdentity[attempt] = identity
	s.segments[attempt] = append(s.segments[attempt], nextIndex)
	s.seen[attempt] = true
	s.seenIdentity[identity] = true
	s.splitPending[attempt] = false
	return nextIndex, identity, true, nil
}

func (s *transcriptAssistantTimelineState) reset(attempt int64, nextIdentity string) []int {
	segments := append([]int(nil), s.segments[attempt]...)
	delete(s.current, attempt)
	delete(s.currentIdentity, attempt)
	delete(s.pendingIdentity, attempt)
	delete(s.segments, attempt)
	delete(s.splitPending, attempt)
	delete(s.pendingMaySkip, attempt)
	if nextIdentity = strings.TrimSpace(nextIdentity); nextIdentity != "" {
		s.pendingIdentity[attempt] = nextIdentity
	}
	return segments
}

// resetSegment retires only the currently open candidate. Earlier progress
// segments in the same attempt remain canonical, including those already
// closed by tool boundaries. A reset also declares the fresh segment named by
// its ordinal. A later empty reset may advance that still-unmaterialized
// segment, which is how consecutive automatic correction gates are represented
// without inventing an empty public message. Any skipped or stale identity is
// still a hard conflict.
func (s *transcriptAssistantTimelineState) resetSegment(
	attempt int64,
	targetIdentity, nextIdentity string,
) (int, bool, error) {
	targetIdentity = strings.TrimSpace(targetIdentity)
	nextIdentity = strings.TrimSpace(nextIdentity)
	if targetIdentity == "" || nextIdentity == "" || targetIdentity == nextIdentity {
		return 0, false, transcriptstore.ErrEventConflict
	}
	index, found := s.current[attempt]
	if found {
		if s.currentIdentity[attempt] != targetIdentity {
			return 0, false, transcriptstore.ErrEventConflict
		}
		delete(s.current, attempt)
		delete(s.currentIdentity, attempt)
		delete(s.splitPending, attempt)
		delete(s.pendingMaySkip, attempt)
		s.pendingIdentity[attempt] = nextIdentity
		return index, true, nil
	}
	if pending, exists := s.pendingIdentity[attempt]; exists {
		if pending != targetIdentity {
			return 0, false, transcriptstore.ErrEventConflict
		}
		s.pendingIdentity[attempt] = nextIdentity
		delete(s.pendingMaySkip, attempt)
		return 0, false, nil
	}
	if !s.seen[attempt] && targetIdentity == transcriptAssistantMessageID(s.sessionID, attempt, 1) {
		s.pendingIdentity[attempt] = nextIdentity
		delete(s.pendingMaySkip, attempt)
		return 0, false, nil
	}
	return 0, false, transcriptstore.ErrEventConflict
}

func (s *transcriptAssistantTimelineState) toolBoundary(attempt int64) (int, bool) {
	// A narrow reset can retire answer-like prose immediately before the
	// provider announces a tool call. The reset reserves the next identity,
	// while the durable tool boundary advances the resumed runner to the one
	// after it. Mirror that recovery rule here so the read model accepts only
	// the exact next ordinal instead of quarantining a valid resumed stream.
	if _, pending := s.pendingIdentity[attempt]; pending {
		s.pendingMaySkip[attempt] = true
	}
	if index, found := s.current[attempt]; found {
		s.splitPending[attempt] = true
		return index, true
	}
	return 0, false
}

// correctionBoundary closes a materialized candidate or abandons an empty
// candidate reserved by a preceding reset. A bounded correction may reject a
// provider response before its first public delta, so requiring that empty
// identity to materialize would make the next legitimate segment conflict
// with durable history.
func (s *transcriptAssistantTimelineState) correctionBoundary(attempt int64) (int, bool) {
	if _, pending := s.pendingIdentity[attempt]; pending {
		s.pendingMaySkip[attempt] = true
	}
	if index, found := s.current[attempt]; found {
		s.splitPending[attempt] = true
		return index, true
	}
	delete(s.splitPending, attempt)
	return 0, false
}

func (s *transcriptAssistantTimelineState) consumePendingIdentity(attempt int64, durableIdentity string) error {
	pending, found := s.pendingIdentity[attempt]
	if !found {
		return nil
	}
	if durableIdentity == "" {
		return transcriptstore.ErrEventConflict
	}
	if durableIdentity != pending {
		ordinal, ok := transcriptAssistantMessageOrdinal(s.sessionID, attempt, pending)
		if !ok || !s.pendingMaySkip[attempt] ||
			durableIdentity != transcriptAssistantMessageID(s.sessionID, attempt, ordinal+1) {
			return transcriptstore.ErrEventConflict
		}
	}
	delete(s.pendingIdentity, attempt)
	delete(s.pendingMaySkip, attempt)
	return nil
}

func (s *transcriptAssistantTimelineState) terminal(attempt, eventID int64, nextIndex int, durableIdentity string) (int, string, bool, error) {
	// The terminal event closes the attempt, so it attaches to the last open
	// segment even when its raw ordinal refers to an earlier (already seen)
	// segment: renumbered correction passes make the raw terminal ordinal
	// unreliable while the durable identity still belongs to this attempt.
	if index, found := s.current[attempt]; found && (!s.splitPending[attempt] || durableIdentity == s.currentIdentity[attempt] || s.seenIdentity[durableIdentity]) {
		if durableIdentity != "" && durableIdentity != s.currentIdentity[attempt] && !s.seenIdentity[durableIdentity] {
			return 0, "", false, transcriptstore.ErrEventConflict
		}
		return index, "", false, nil
	}
	if durableIdentity == "" {
		// A terminal failure may arrive after a scoped reset without publishing
		// another assistant delta. The reset's reserved identity is the canonical
		// location for that terminal detail; dropping it would quarantine an
		// otherwise valid completed lifecycle and blank the conversation.
		durableIdentity = s.pendingIdentity[attempt]
	}
	if err := s.consumePendingIdentity(attempt, durableIdentity); err != nil {
		return 0, "", false, err
	}
	identity := durableIdentity
	if identity == "" {
		identity = fmt.Sprintf("assistant-%s-%d", s.sessionID, attempt)
		if s.seen[attempt] {
			identity = fmt.Sprintf("assistant-%s-%d-event-%d", s.sessionID, attempt, eventID)
		}
	}
	if s.seenIdentity[identity] {
		return 0, "", false, transcriptstore.ErrEventConflict
	}
	s.current[attempt] = nextIndex
	s.currentIdentity[attempt] = identity
	s.segments[attempt] = append(s.segments[attempt], nextIndex)
	s.seen[attempt] = true
	s.seenIdentity[identity] = true
	s.splitPending[attempt] = false
	return nextIndex, identity, true, nil
}

func transcriptAssistantMessageID(sessionID string, attempt, ordinal int64) string {
	base := fmt.Sprintf("assistant-%s-%d", strings.TrimSpace(sessionID), attempt)
	if ordinal <= 1 {
		return base
	}
	return base + "-segment-" + strconv.FormatInt(ordinal, 10)
}

func transcriptAssistantMessageOrdinal(sessionID string, attempt int64, identity string) (int64, bool) {
	base := transcriptAssistantMessageID(sessionID, attempt, 1)
	identity = strings.TrimSpace(identity)
	if identity == base {
		return 1, true
	}
	prefix := base + "-segment-"
	if !strings.HasPrefix(identity, prefix) {
		return 0, false
	}
	ordinal, err := strconv.ParseInt(strings.TrimPrefix(identity, prefix), 10, 64)
	return ordinal, err == nil && ordinal > 1 && ordinal < transcriptstore.AssistantSegmentMaxOrdinal
}

func transcriptAssistantMessageIDFromPayload(payload map[string]any, sessionID string, attempt int64) (string, error) {
	segment, present, err := transcriptstore.ParseAssistantSegmentV1(payload)
	if err != nil {
		return "", err
	}
	if !present {
		return "", nil
	}
	return transcriptAssistantMessageID(sessionID, attempt, segment.Ordinal), nil
}

// transcriptAssistantSegmentResetTarget resolves the candidate retired by a
// narrow content_reset. Only the explicit segment scope opts into this
// behavior. The reset event's ordinal names the fresh segment that follows, so
// the exact target is the immediately preceding ordinal. Legacy resets without
// assistant_segment, empty-scope resets, and attempt-scoped resets retain their
// original whole-attempt semantics.
func transcriptAssistantSegmentResetTarget(
	payload map[string]any,
	sessionID string,
	attempt int64,
) (string, bool, error) {
	segment, present, err := transcriptstore.ParseAssistantSegmentV1(payload)
	if err != nil || !present || segment.ReplaceScope != transcriptstore.AssistantReplaceScopeSegment {
		return "", false, err
	}
	if segment.Ordinal <= 1 {
		return "", true, transcriptstore.ErrEventConflict
	}
	return transcriptAssistantMessageID(sessionID, attempt, segment.Ordinal-1), true, nil
}

type transcriptToolHistoryFact struct {
	attempt               int64
	callID                string
	name                  string
	status                string
	phase                 string
	revision              int64
	parentID              string
	settlement            string
	provisional           bool
	description           string
	input                 map[string]any
	inputSet              bool
	result                any
	resultSet             bool
	progress              map[string]any
	progressSet           bool
	background            bool
	backgroundAdmission   bool
	backgroundObservation bool
}

type transcriptToolHistoryRecord struct {
	fact     transcriptToolHistoryFact
	index    int
	msgID    string
	identity string
}

type transcriptToolHistoryAction struct {
	fact     transcriptToolHistoryFact
	index    int
	identity string
	msgID    string
	append   bool
}

type transcriptToolHistoryState struct {
	byIdentity map[string]transcriptToolHistoryRecord
}

func newTranscriptToolHistoryState() *transcriptToolHistoryState {
	return &transcriptToolHistoryState{byIdentity: map[string]transcriptToolHistoryRecord{}}
}

func (s *transcriptToolHistoryState) consume(
	projected transcriptstore.ProjectedEvent, payload map[string]any, nextIndex int,
) (transcriptToolHistoryAction, bool, error) {
	fact, found, err := transcriptToolHistoryFactFromEvent(projected, payload)
	if err != nil || !found {
		return transcriptToolHistoryAction{}, found, err
	}
	identity := transcriptToolHistoryIdentity(projected.Event.StreamUID, fact.attempt, fact.callID)
	record, exists := s.byIdentity[identity]
	if fact.provisional && fact.status == "running" {
		// Provisional tool starts/progress are private. They must not create a
		// public history row, but a provisional terminal checkpoint may still
		// close an already-visible durable start below.
		return transcriptToolHistoryAction{}, false, nil
	}
	if !exists && (fact.status != "running" || fact.phase == "approval_resumed" || strings.HasPrefix(fact.phase, "progress-")) {
		// A resumed runner can commit the terminal checkpoint under a new
		// attempt number while the durable tool batch keeps the original call
		// identity. Reconcile that exact visible running call instead of
		// creating a second coordinate.
		originalIdentity := identity
		candidateIdentity, candidateRecord, candidateExists, err := s.findCompatibleRunningTool(fact)
		if err != nil {
			return transcriptToolHistoryAction{}, true, err
		}
		if candidateExists {
			identity, record, exists = candidateIdentity, candidateRecord, true
		} else {
			identity = originalIdentity
		}
		if fact.provisional && !exists {
			// Private terminal output without a public start is still private.
			return transcriptToolHistoryAction{}, false, nil
		}
	}
	if !exists {
		record = transcriptToolHistoryRecord{
			fact: fact, index: nextIndex, msgID: projected.Event.ClientMessageID, identity: identity,
		}
		s.byIdentity[identity] = record
		return transcriptToolHistoryAction{fact: fact, index: nextIndex, identity: identity, msgID: record.msgID, append: true}, true, nil
	}
	if fact.provisional {
		// Use provisional output only as a lifecycle settlement. Do not copy
		// private tool input/result/description into the public history.
		fact.input = nil
		fact.inputSet = false
		fact.result = nil
		fact.resultSet = false
		fact.description = ""
	}
	fact.attempt = record.fact.attempt
	merged, err := mergeTranscriptToolHistoryFact(record.fact, fact)
	if err != nil {
		return transcriptToolHistoryAction{}, true, err
	}
	record.fact = merged
	s.byIdentity[identity] = record
	return transcriptToolHistoryAction{fact: merged, index: record.index, identity: identity, msgID: record.msgID}, true, nil
}

func transcriptToolHistoryIdentity(streamUID string, attempt int64, callID string) string {
	identity := fmt.Sprintf("transcript-tool:%s:%d:%s", streamUID, attempt, callID)
	if len(identity) <= 256 {
		return identity
	}
	digest := sha256.Sum256([]byte(identity))
	return fmt.Sprintf("transcript-tool:%x", digest)
}

func (s *transcriptToolHistoryState) settleAttempt(attempt int64, terminal string) []transcriptToolHistoryAction {
	status := ""
	settlement := ""
	switch terminal {
	case "completed":
		status = "interrupted"
		settlement = "task_completed_without_tool_receipt"
	case "failed":
		status = "interrupted"
		settlement = "task_failed_without_tool_receipt"
	case "cancelled", "canceled":
		status = "canceled"
		settlement = "task_cancelled"
	case "interrupted":
		status = "interrupted"
		settlement = "execution_interrupted"
	default:
		return nil
	}
	actions := make([]transcriptToolHistoryAction, 0)
	for identity, record := range s.byIdentity {
		if record.fact.attempt != attempt || record.fact.background || !transcriptToolStatusActive(record.fact.status) {
			continue
		}
		record.fact.status = status
		record.fact.settlement = settlement
		record.fact.result = nil
		record.fact.resultSet = false
		s.byIdentity[identity] = record
		actions = append(actions, transcriptToolHistoryAction{
			fact: record.fact, index: record.index, identity: identity, msgID: record.msgID,
		})
	}
	return actions
}

func transcriptToolHistoryInterruptedAttempt(
	projected transcriptstore.ProjectedEvent,
	payload map[string]any,
) (int64, bool) {
	if projected.Event.Type != "runner_checkpoint" || projected.Event.RunnerAttempt == nil {
		return 0, false
	}
	if strings.EqualFold(strings.TrimSpace(webString(payload["status"])), "interrupted") {
		if runnerInterruptionMayContinueSameTask(strings.TrimSpace(webString(payload["reason_code"]))) {
			// A recoverable interruption keeps ownership of the same durable
			// tool lifecycle. Its later terminal checkpoint is authoritative;
			// projecting a synthetic cancellation here would make that valid
			// completion look like a terminal-state regression.
			return 0, false
		}
		return *projected.Event.RunnerAttempt, true
	}
	return 0, false
}

func transcriptToolHistoryFactFromEvent(
	projected transcriptstore.ProjectedEvent, payload map[string]any,
) (transcriptToolHistoryFact, bool, error) {
	if (projected.Event.Type != "runner_checkpoint" && projected.Event.Type != transcriptstore.ToolOperationObservationEventType) || projected.Event.Source != transcriptstore.EventSourcePayload {
		return transcriptToolHistoryFact{}, false, nil
	}
	visibility, _ := payload["visibility"].(string)
	phaseValue, phasePresent := payload["toolPhase"]
	phase, phaseValid := transcriptToolExactString(phaseValue)
	if !phasePresent {
		phase = ""
		phaseValid = true
	}
	if phase == "verification" || phase == "verification_tool" || phase == "review_policy" {
		return transcriptToolHistoryFact{}, false, nil
	}
	if phase == "auto_compact" {
		return transcriptToolHistoryFact{}, false, nil
	}
	if phase == planModeDenialToolPhase {
		return transcriptToolHistoryFact{}, false, nil
	}
	progressPhase := strings.HasPrefix(phase, "progress-")
	if progressPhase && !validTranscriptToolProgressPhase(phase) {
		return transcriptToolHistoryFact{}, true, transcriptstore.ErrEventConflict
	}
	progress, progressSet, progressErr := transcriptToolHistoryProgress(payload["progress"])
	if progressErr != nil {
		return transcriptToolHistoryFact{}, true, progressErr
	}
	if progressPhase && !progressSet {
		// Generic elapsed-time heartbeats stay in the durable audit log but do
		// not rewrite the public tool row. Only observed structured progress is
		// projected into the live timeline.
		return transcriptToolHistoryFact{}, false, nil
	}
	if !phaseValid {
		return transcriptToolHistoryFact{}, true, transcriptstore.ErrEventConflict
	}
	callID, callIDValid := transcriptToolExactString(payload["toolCallId"])
	name, nameValid := transcriptToolExactString(payload["toolName"])
	if callID == "" && name == "" && phase == "" && payload["toolInput"] == nil && payload["toolResult"] == nil {
		return transcriptToolHistoryFact{}, false, nil
	}
	if projected.Event.RunnerAttempt == nil || !callIDValid || !nameValid {
		return transcriptToolHistoryFact{}, true, transcriptstore.ErrEventConflict
	}
	originAttempt := *projected.Event.RunnerAttempt
	if value, present := payload["toolOriginAttempt"]; present {
		origin, ok := transcriptWebNonnegativeSafeInteger(value)
		if !ok || origin <= 0 || origin > originAttempt {
			return transcriptToolHistoryFact{}, true, transcriptstore.ErrEventConflict
		}
		originAttempt = origin
	}
	if _, ok := transcriptstore.CanonicalAskUserToolNameV1(name); ok {
		return transcriptToolHistoryFact{}, false, nil
	}
	status := ""
	statusValue, statusValid := transcriptToolExactString(payload["status"])
	if !statusValid {
		return transcriptToolHistoryFact{}, true, transcriptstore.ErrEventConflict
	}
	if statusValue == "completed" && phase == prestartToolFailurePhase &&
		boolValue(payload["rejectedBeforeExecution"], false) {
		result, _ := payload["toolResult"].(map[string]any)
		if result["executed"] == false {
			// Older writers described a pre-execution rejection as both
			// `completed` and `prestart_failed`. Its immutable executed=false
			// receipt makes the intended error settlement unambiguous. Normalize
			// that historical contradiction at the read boundary; new writers
			// persist `failed` directly.
			statusValue = "failed"
		}
	}
	switch statusValue {
	case "running":
		status = "running"
	case "waiting":
		status = "waiting"
	case "completed":
		status = "completed"
	case "cancelled", "canceled":
		status = "canceled"
	case "failed":
		status = "error"
	case "blocked":
		status = "blocked"
	default:
		return transcriptToolHistoryFact{}, true, transcriptstore.ErrEventConflict
	}
	validPhase := (phase == "start" && status == "running") ||
		(phase == "approval_resumed" && status == "running") ||
		(phase == "waiting" && status == "waiting") ||
		(progressPhase && status == "running") ||
		(phase == "completed" && status == "completed") ||
		((phase == "cancelled" || phase == "canceled") && status == "canceled") ||
		((phase == "failed" || phase == prestartToolFailurePhase) && status == "error") ||
		(phase == "blocked" && status == "blocked") ||
		(phase == "outcome_unknown" && status == "error")
	if !validPhase {
		return transcriptToolHistoryFact{}, true, transcriptstore.ErrEventConflict
	}
	input := map[string]any{}
	inputSet := false
	if value, present := payload["toolInput"]; present {
		var ok bool
		input, ok = value.(map[string]any)
		if !ok {
			return transcriptToolHistoryFact{}, true, transcriptstore.ErrEventConflict
		}
		inputSet = true
	}
	result, resultSet := payload["toolResult"]
	background := projected.Event.Type == transcriptstore.ToolOperationObservationEventType
	backgroundAdmission := false
	if receipt, ok := result.(map[string]any); ok && status == "completed" && receipt["status"] == "running" && receipt["stream_progress"] == true && webString(receipt["operation_id"]) != "" {
		background, backgroundAdmission = true, true
		status = "running"
		result, resultSet = nil, false
	}
	description := strings.TrimSpace(webString(payload["message"]))
	parentID, parentValid := transcriptToolOptionalExactString(payload["outerToolCallId"])
	if !parentValid || parentID == callID {
		return transcriptToolHistoryFact{}, true, transcriptstore.ErrEventConflict
	}
	if progressPhase || phase == "approval_resumed" {
		// Keep the stable human description from the start checkpoint. The
		// structured progress fields own the changing phase presentation.
		description = ""
	}
	return transcriptToolHistoryFact{
		attempt: originAttempt, callID: callID, name: name, status: status,
		phase: phase, revision: projected.Event.PublicationSeq, parentID: parentID,
		provisional: visibility == "provisional",
		description: description, input: input, inputSet: inputSet,
		result: result, resultSet: resultSet, progress: progress, progressSet: progressSet,
		background: background, backgroundAdmission: backgroundAdmission, backgroundObservation: projected.Event.Type == transcriptstore.ToolOperationObservationEventType,
	}, true, nil
}

func transcriptToolHistoryProgress(value any) (map[string]any, bool, error) {
	if value == nil {
		return nil, false, nil
	}
	progress, ok := value.(map[string]any)
	if !ok || len(progress) == 0 {
		return nil, false, transcriptstore.ErrEventConflict
	}
	allowed := map[string]bool{
		"phase": true, "message": true, "phasePercent": true, "bytesPerSecond": true,
		"bytesCompleted": true, "bytesTotal": true,
		"completedItems": true, "totalItems": true, "elapsedMs": true, "indeterminate": true,
	}
	for key := range progress {
		if !allowed[key] {
			return nil, false, transcriptstore.ErrEventConflict
		}
	}
	phase, phaseOK := progress["phase"].(string)
	if !phaseOK || !validTranscriptToolProgressPhase(phase) {
		return nil, false, transcriptstore.ErrEventConflict
	}
	if message, present := progress["message"]; present {
		text, ok := message.(string)
		if !ok || text == "" || strings.TrimSpace(text) != text || len(text) > 240 {
			return nil, false, transcriptstore.ErrEventConflict
		}
	}
	if percent, present := progress["phasePercent"]; present {
		number, ok := percent.(float64)
		if !ok || number < 0 || number > 100 {
			return nil, false, transcriptstore.ErrEventConflict
		}
	}
	if rate, present := progress["bytesPerSecond"]; present {
		number, ok := rate.(float64)
		if !ok || number < 0 || number > 1e15 {
			return nil, false, transcriptstore.ErrEventConflict
		}
	}
	bytesCompleted, bytesCompletedPresent := transcriptWebNonnegativeSafeInteger(progress["bytesCompleted"])
	bytesTotal, bytesTotalPresent := transcriptWebNonnegativeSafeInteger(progress["bytesTotal"])
	if (progress["bytesCompleted"] != nil && !bytesCompletedPresent) ||
		(progress["bytesTotal"] != nil && (!bytesTotalPresent || bytesTotal <= 0)) ||
		(bytesCompletedPresent && bytesTotalPresent && bytesCompleted > bytesTotal) {
		return nil, false, transcriptstore.ErrEventConflict
	}
	completed, completedPresent := transcriptWebNonnegativeSafeInteger(progress["completedItems"])
	total, totalPresent := transcriptWebNonnegativeSafeInteger(progress["totalItems"])
	if (progress["completedItems"] != nil && !completedPresent) ||
		(progress["totalItems"] != nil && (!totalPresent || total <= 0)) ||
		(completedPresent && totalPresent && completed > total) {
		return nil, false, transcriptstore.ErrEventConflict
	}
	if indeterminate, present := progress["indeterminate"]; present {
		if _, ok := indeterminate.(bool); !ok {
			return nil, false, transcriptstore.ErrEventConflict
		}
	}
	if elapsed, present := progress["elapsedMs"]; present {
		if value, ok := transcriptWebNonnegativeSafeInteger(elapsed); !ok || value > 365*24*60*60*1000 {
			return nil, false, transcriptstore.ErrEventConflict
		}
	}
	result := make(map[string]any, len(progress))
	for key, candidate := range progress {
		result[key] = candidate
	}
	return result, true, nil
}

func validTranscriptToolProgressPhase(value string) bool {
	if value == "" || len(value) > 80 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') ||
			character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func mergeTranscriptToolHistoryFact(current, update transcriptToolHistoryFact) (transcriptToolHistoryFact, error) {
	// The protocol may acknowledge background admission after a very short
	// operation has already settled. Admission must never overwrite its facts.
	if current.background && !update.backgroundObservation {
		return current, nil
	}
	update.background = update.background || current.background
	if current.attempt != update.attempt || current.callID != update.callID || current.name != update.name {
		return transcriptToolHistoryFact{}, fmt.Errorf("tool identity changed: %w", transcriptstore.ErrEventConflict)
	}
	if current.inputSet && update.inputSet && !transcriptToolJSONEqual(current.input, update.input) {
		return transcriptToolHistoryFact{}, fmt.Errorf("tool input changed: %w", transcriptstore.ErrEventConflict)
	}
	if !current.inputSet && update.inputSet {
		current.input = update.input
		current.inputSet = true
	}
	if !transcriptToolStatusTransitionAllowed(current.status, update.status) {
		return transcriptToolHistoryFact{}, fmt.Errorf("tool status regressed: %w", transcriptstore.ErrEventConflict)
	}
	settling := transcriptToolStatusActive(current.status) && !transcriptToolStatusActive(update.status)
	if settling {
		// A waiting checkpoint may carry an approval/pending result. The
		// terminal execution result is the authoritative settlement of that same
		// lifecycle and must replace, rather than conflict with, the pending value.
		current.result = update.result
		current.resultSet = update.resultSet
	} else {
		if current.resultSet && update.resultSet && !transcriptToolJSONEqual(current.result, update.result) {
			return transcriptToolHistoryFact{}, fmt.Errorf("tool result changed: %w", transcriptstore.ErrEventConflict)
		}
		if !current.resultSet && update.resultSet {
			current.result = update.result
			current.resultSet = true
		}
	}
	if update.description != "" {
		current.description = update.description
	}
	if update.progressSet {
		current.progress = update.progress
		current.progressSet = true
	}
	if update.phase != "" {
		current.phase = update.phase
	}
	if update.revision > current.revision {
		current.revision = update.revision
	}
	if current.parentID != "" && update.parentID != "" && current.parentID != update.parentID {
		return transcriptToolHistoryFact{}, fmt.Errorf("tool parent changed: %w", transcriptstore.ErrEventConflict)
	}
	if current.parentID == "" {
		current.parentID = update.parentID
	}
	if update.settlement != "" {
		current.settlement = update.settlement
	}
	current.status = update.status
	current.background = update.background
	return current, nil
}

func transcriptToolStatusActive(status string) bool {
	return status == "running" || status == "waiting" || status == "blocked"
}

func transcriptToolStatusTransitionAllowed(current, update string) bool {
	if current == update {
		return true
	}
	if !transcriptToolStatusActive(current) {
		return false
	}
	if current == "running" {
		return true
	}
	return update == "running" || update == "waiting" || update == "blocked" ||
		update == "completed" || update == "error" || update == "canceled" ||
		update == "interrupted"
}

func transcriptToolExactString(value any) (string, bool) {
	text, ok := value.(string)
	if !ok || text == "" || strings.TrimSpace(text) != text {
		return "", false
	}
	for _, character := range text {
		if character <= 0x20 || character == 0x7f {
			return "", false
		}
	}
	return text, true
}

func transcriptToolOptionalExactString(value any) (string, bool) {
	if value == nil {
		return "", true
	}
	return transcriptToolExactString(value)
}

func transcriptToolJSONEqual(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func transcriptToolHistoryMessage(action transcriptToolHistoryAction, sessionID string, createdAt int64) map[string]any {
	content := map[string]any{
		"call_id": action.fact.callID, "name": action.fact.name,
		"status": action.fact.status, "attempt": action.fact.attempt,
		"operation_id": action.fact.callID, "revision": action.fact.revision,
	}
	if action.fact.inputSet {
		content["args"] = action.fact.input
		content["input"] = action.fact.input
	}
	if action.fact.parentID != "" {
		content["parent_operation_id"] = action.fact.parentID
	}
	if action.fact.phase != "" {
		content["phase"] = action.fact.phase
	}
	if action.fact.settlement != "" {
		content["settlement_reason"] = action.fact.settlement
	}
	if action.fact.description != "" {
		content["description"] = action.fact.description
	}
	if action.fact.resultSet {
		output := transcriptToolResultText(action.fact.result)
		content["output"] = output
		if action.fact.status == "error" {
			content["error"] = output
		}
	}
	if action.fact.progressSet {
		content["progress"] = action.fact.progress
	}
	status := "finish"
	if transcriptToolStatusActive(action.fact.status) {
		status = "work"
	} else if action.fact.status == "error" {
		status = "error"
	}
	return map[string]any{
		"id": action.identity, "msg_id": action.msgID, "conversation_id": sessionID,
		"type": "tool_call", "position": "left", "status": status, "created_at": createdAt, "content": content,
	}
}

func transcriptToolResultText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(raw)
}

func (s *transcriptToolHistoryState) findCompatibleRunningTool(
	fact transcriptToolHistoryFact,
) (string, transcriptToolHistoryRecord, bool, error) {
	var identity string
	var record transcriptToolHistoryRecord
	found := false
	for candidateIdentity, candidate := range s.byIdentity {
		if !transcriptToolStatusActive(candidate.fact.status) || candidate.fact.callID != fact.callID ||
			candidate.fact.name != fact.name {
			continue
		}
		if candidate.fact.inputSet && fact.inputSet &&
			!transcriptToolJSONEqual(candidate.fact.input, fact.input) {
			continue
		}
		if found {
			return "", transcriptToolHistoryRecord{}, false, transcriptstore.ErrEventConflict
		}
		identity, record, found = candidateIdentity, candidate, true
	}
	return identity, record, found, nil
}
