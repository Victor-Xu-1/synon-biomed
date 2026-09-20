package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"synon-go/internal/agentruntime"
	eventjournal "synon-go/internal/persistence/journal"
	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	sessionRunnerNoProgressRecoverySchema = "synon.runner_no_progress.v1"
	sessionRunnerNoProgressRecoveryField  = "runnerNoProgressRecovery"
	sessionRunnerNoProgressDetailMarker   = "\nSynon no-progress recovery state: "
	maxSessionRunnerClosedActions         = 8
)

type sessionRunnerClosedAction struct {
	Tool        string `json:"tool"`
	Fingerprint string `json:"fingerprint"`
}

type sessionRunnerNoProgressRecovery struct {
	Schema                string                      `json:"schema"`
	ObligationFingerprint string                      `json:"obligation_fingerprint"`
	Consecutive           int                         `json:"consecutive"`
	ClosedActions         []sessionRunnerClosedAction `json:"closed_actions,omitempty"`
}

type sessionRunnerRecoveryProjection struct {
	Correction           recoveredRunnerCorrection
	HasCorrection        bool
	NoProgress           sessionRunnerNoProgressRecovery
	CorrectionRepetition runnerCorrectionRepetition
}

func runnerRecoveryObligationFingerprint(
	authority *transcriptRunnerAuthority,
	correction transcriptstore.RunnerInterruptionCause,
) string {
	parts := []string{"legacy", "", "0", "0"}
	if authority != nil {
		parts = []string{
			strings.TrimSpace(authority.Stream.UID),
			strings.TrimSpace(authority.Stream.OwnerID),
			strconv.FormatInt(authority.Stream.Epoch, 10),
			strconv.FormatInt(authority.Claim.ClaimedInputRevision, 10),
		}
	}
	parts = append(parts, runnerCorrectionFingerprint(correction))
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(digest[:])
}

func newSessionRunnerNoProgressRecovery(scope string) sessionRunnerNoProgressRecovery {
	return sessionRunnerNoProgressRecovery{
		Schema: sessionRunnerNoProgressRecoverySchema, ObligationFingerprint: strings.TrimSpace(scope),
	}
}

func (state *sessionRunnerNoProgressRecovery) resetScope(scope string) {
	if state == nil {
		return
	}
	state.Schema = sessionRunnerNoProgressRecoverySchema
	state.ObligationFingerprint = strings.TrimSpace(scope)
	state.Consecutive = 0
	state.ClosedActions = nil
}

func (state *sessionRunnerNoProgressRecovery) resetMaterialProgress() {
	if state == nil {
		return
	}
	state.Consecutive = 0
	state.ClosedActions = nil
}

func (state *sessionRunnerNoProgressRecovery) addClosedAction(tool, fingerprint string) {
	if state == nil {
		return
	}
	tool = strings.TrimSpace(tool)
	fingerprint = strings.ToLower(strings.TrimSpace(fingerprint))
	if tool == "" || !validSHA256Hex(fingerprint) {
		return
	}
	for _, action := range state.ClosedActions {
		if action.Fingerprint == fingerprint {
			return
		}
	}
	state.ClosedActions = append(state.ClosedActions, sessionRunnerClosedAction{
		Tool: tool, Fingerprint: fingerprint,
	})
	if len(state.ClosedActions) > maxSessionRunnerClosedActions {
		state.ClosedActions = append(
			[]sessionRunnerClosedAction(nil),
			state.ClosedActions[len(state.ClosedActions)-maxSessionRunnerClosedActions:]...,
		)
	}
}

func (state *sessionRunnerNoProgressRecovery) mergeClosedActions(actions []sessionRunnerClosedAction) {
	for _, action := range actions {
		state.addClosedAction(action.Tool, action.Fingerprint)
	}
}

func (state *sessionRunnerNoProgressRecovery) recordNoProgress(calls []agentruntime.ToolCall) {
	if state == nil {
		return
	}
	state.Schema = sessionRunnerNoProgressRecoverySchema
	state.Consecutive++
	for _, call := range calls {
		state.addClosedAction(
			call.Name,
			agentruntime.ExecutionCallFingerprint(call.Name, call.Arguments),
		)
	}
}

func (state sessionRunnerNoProgressRecovery) containsFingerprint(fingerprint string) bool {
	for _, action := range state.ClosedActions {
		if action.Fingerprint == fingerprint {
			return true
		}
	}
	return false
}

func (state sessionRunnerNoProgressRecovery) payload() map[string]any {
	value := map[string]any{
		"schema": state.Schema, "obligation_fingerprint": state.ObligationFingerprint,
		"consecutive": state.Consecutive,
	}
	if len(state.ClosedActions) > 0 {
		actions := make([]any, 0, len(state.ClosedActions))
		for _, action := range state.ClosedActions {
			actions = append(actions, map[string]any{"tool": action.Tool, "fingerprint": action.Fingerprint})
		}
		value["closed_actions"] = actions
	}
	return value
}

func (state sessionRunnerNoProgressRecovery) resumeDetail(base string) string {
	encoded, err := json.Marshal(state.payload())
	if err != nil {
		return strings.TrimSpace(base)
	}
	return strings.TrimSpace(base) + sessionRunnerNoProgressDetailMarker + string(encoded)
}

func parseSessionRunnerNoProgressRecoveryDetail(detail string) (sessionRunnerNoProgressRecovery, bool) {
	index := strings.LastIndex(detail, sessionRunnerNoProgressDetailMarker)
	if index < 0 {
		return sessionRunnerNoProgressRecovery{}, false
	}
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(detail[index+len(sessionRunnerNoProgressDetailMarker):])))
	decoder.DisallowUnknownFields()
	var state sessionRunnerNoProgressRecovery
	if decoder.Decode(&state) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		state.Schema != sessionRunnerNoProgressRecoverySchema ||
		!validSHA256Hex(state.ObligationFingerprint) || state.Consecutive <= 0 ||
		state.Consecutive > 1_000_000 || len(state.ClosedActions) > maxSessionRunnerClosedActions {
		return sessionRunnerNoProgressRecovery{}, false
	}
	validated := newSessionRunnerNoProgressRecovery(state.ObligationFingerprint)
	validated.Consecutive = state.Consecutive
	validated.mergeClosedActions(state.ClosedActions)
	if len(validated.ClosedActions) != len(state.ClosedActions) {
		return sessionRunnerNoProgressRecovery{}, false
	}
	return validated, true
}

func validSHA256Hex(value string) bool {
	decoded, err := hex.DecodeString(strings.TrimSpace(value))
	return err == nil && len(decoded) == sha256.Size
}

func runnerNoProgressRecoveryFromEntries(
	entries []eventjournal.Entry,
	authority *transcriptRunnerAuthority,
) sessionRunnerNoProgressRecovery {
	state := newSessionRunnerNoProgressRecovery(runnerRecoveryObligationFingerprint(authority, transcriptstore.RunnerInterruptionCause{}))
	for _, entry := range entries {
		state.observeEntry(entry, authority)
	}
	return state
}

func (state *sessionRunnerNoProgressRecovery) observeEntry(
	entry eventjournal.Entry,
	authority *transcriptRunnerAuthority,
) {
	if state == nil {
		return
	}
	message := entry.Message
	logicalScope := runnerRecoveryObligationFingerprint(authority, transcriptstore.RunnerInterruptionCause{})
	if runnerEntryStartsNewLogicalTask(entry) {
		state.resetScope(logicalScope)
		return
	}
	if strings.TrimSpace(stringValue(message["type"])) != "runner_checkpoint" {
		return
	}
	if runnerCheckpointHasMaterialProgress(message) {
		state.resetMaterialProgress()
		return
	}
	cause := runnerCheckpointRecoveryCondition(entry)
	reason, detail := cause.ReasonCode, cause.Detail
	if runnerCorrectionReasonStartsNewRepairScope(reason) {
		// A protocol failure while another substantive repair is open is not a
		// new obligation. It records failure to execute the existing route.
		if reason == sessionRunnerRequiredToolChoiceUnsatisfiedReasonCode &&
			state.ObligationFingerprint != logicalScope {
			return
		}
		scope := runnerRecoveryObligationFingerprint(authority, cause)
		if scope != state.ObligationFingerprint {
			state.resetScope(scope)
		}
		return
	}
	if reason != sessionRunnerToolRoundNoProgressReasonCode &&
		reason != sessionRunnerToolRoundNoProgressExhaustedReasonCode {
		return
	}
	persisted, found := parseSessionRunnerNoProgressRecoveryDetail(detail)
	if found {
		if persisted.ObligationFingerprint != state.ObligationFingerprint {
			state.resetScope(persisted.ObligationFingerprint)
		}
		state.Consecutive = max(state.Consecutive+1, persisted.Consecutive)
		state.mergeClosedActions(persisted.ClosedActions)
		return
	}
	state.Consecutive++
}

type sessionRunnerRecoveryProjectionAccumulator struct {
	authority           *transcriptRunnerAuthority
	correction          recoveredRunnerCorrection
	hasCorrection       bool
	protocolCorrection  recoveredRunnerCorrection
	hasProtocolFallback bool
	noProgress          sessionRunnerNoProgressRecovery
	repetition          runnerCorrectionRepetition
}

func newSessionRunnerRecoveryProjectionAccumulator(
	authority *transcriptRunnerAuthority,
) sessionRunnerRecoveryProjectionAccumulator {
	return sessionRunnerRecoveryProjectionAccumulator{
		authority: authority,
		noProgress: newSessionRunnerNoProgressRecovery(
			runnerRecoveryObligationFingerprint(authority, transcriptstore.RunnerInterruptionCause{}),
		),
	}
}

func (accumulator *sessionRunnerRecoveryProjectionAccumulator) observe(entry eventjournal.Entry) {
	if accumulator == nil {
		return
	}
	if runnerEntryStartsNewLogicalTask(entry) {
		accumulator.correction = recoveredRunnerCorrection{}
		accumulator.hasCorrection = false
		accumulator.protocolCorrection = recoveredRunnerCorrection{}
		accumulator.hasProtocolFallback = false
	}
	accumulator.noProgress.observeEntry(entry, accumulator.authority)
	accumulator.repetition.observe(entry)
	correction, found := latestRunnerCorrection([]eventjournal.Entry{entry})
	if !found {
		if accumulator.hasCorrection {
			accumulator.correction.observeRead(entry)
		} else if accumulator.hasProtocolFallback {
			accumulator.protocolCorrection.observeRead(entry)
		}
		return
	}
	if correction.ReasonCode == sessionRunnerRequiredToolChoiceUnsatisfiedReasonCode {
		accumulator.protocolCorrection = correction
		accumulator.hasProtocolFallback = true
		return
	}
	if accumulator.hasCorrection && correction.Condition != nil && accumulator.correction.Condition != nil && correction.Condition.ContentID() == accumulator.correction.Condition.ContentID() {
		correction.ReadCoverage = accumulator.correction.ReadCoverage
	}
	accumulator.correction = correction
	accumulator.hasCorrection = true
}

func (accumulator sessionRunnerRecoveryProjectionAccumulator) result() sessionRunnerRecoveryProjection {
	correction, found := accumulator.correction, accumulator.hasCorrection
	if !found && accumulator.hasProtocolFallback {
		correction, found = accumulator.protocolCorrection, true
	}
	return sessionRunnerRecoveryProjection{
		Correction: correction, HasCorrection: found, NoProgress: accumulator.noProgress,
		CorrectionRepetition: accumulator.repetition,
	}
}

func (s *Server) loadSessionRunnerRecoveryProjection(
	ctx context.Context,
	authority *transcriptRunnerAuthority,
) (sessionRunnerRecoveryProjection, error) {
	if s == nil || s.transcriptStore == nil || authority == nil {
		return sessionRunnerRecoveryProjection{}, nil
	}
	accumulator := newSessionRunnerRecoveryProjectionAccumulator(authority)
	err := s.scanSessionRunnerRecoveryEntries(ctx, authority, func(entry eventjournal.Entry) error {
		accumulator.observe(entry)
		return nil
	})
	if err != nil {
		return sessionRunnerRecoveryProjection{}, err
	}
	return accumulator.result(), nil
}

// Recovery summaries and targeted admission consume the same fenced event
// projection. The visitor retains only its own bounded derived state.
func (s *Server) scanSessionRunnerRecoveryEntries(
	ctx context.Context,
	authority *transcriptRunnerAuthority,
	visit func(eventjournal.Entry) error,
) error {
	if s == nil || s.transcriptStore == nil || authority == nil {
		return fmt.Errorf("runner recovery requires transcript authority")
	}
	snapshot, err := s.transcriptStore.GetProjectionSnapshot(ctx, authority.Stream.UID, authority.Stream.OwnerID)
	if err != nil {
		return fmt.Errorf("load runner recovery projection snapshot: %w", err)
	}
	var conditions transcriptstore.RunnerCorrectionAssembler
	return s.scanTranscriptProjection(ctx, snapshot, authority.Stream.OwnerID, snapshot.ThroughPublicationSequence, func(projected transcriptstore.ProjectedEvent) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		message := eventjournal.Message{}
		var runtimeProjection any
		if len(projected.ResolvedPayloadJSON) > 0 {
			if err := json.Unmarshal(projected.ResolvedPayloadJSON, &message); err != nil {
				return fmt.Errorf("decode runner recovery event %d: %w", projected.Event.EventID, err)
			}
		}
		switch projected.Event.Type {
		case "user_message", "user_input_response", "history_user_message":
			message["type"] = "message"
			message["role"] = "user"
		case "assistant_message", "history_assistant_message":
			message["type"] = "message"
			message["role"] = "assistant"
		case "runner_checkpoint":
			if chunk, err := conditions.Observe(message); err != nil {
				return fmt.Errorf("decode runner correction evidence at event %d: %w", projected.Event.EventID, err)
			} else if chunk {
				return nil
			}
			cause, present, err := transcriptstore.ParseRunnerInterruptionCause(message)
			if err != nil {
				return fmt.Errorf("decode runner recovery cause at event %d: %w", projected.Event.EventID, err)
			}
			if present {
				cause, err = conditions.Resolve(cause)
				if err != nil {
					return fmt.Errorf("resolve runner correction evidence at event %d: %w", projected.Event.EventID, err)
				}
				runtimeProjection = cause
			}
			message["type"] = "runner_checkpoint"
		case "runner_finished":
			message["type"] = "runner_finished"
		default:
			return nil
		}
		if projected.Event.RunnerAttempt != nil {
			message["runnerAttempt"] = *projected.Event.RunnerAttempt
		}
		return visit(eventjournal.Entry{
			SessionID: authority.Stream.SessionID, EventID: projected.Event.EventID,
			Message: message, SourceEventType: projected.Event.Type,
			RuntimeProjection: runtimeProjection,
		})
	})
}

func sessionRunnerNoProgressRecoveryFromReplay(entries []eventjournal.Entry) (sessionRunnerNoProgressRecovery, bool) {
	for index := len(entries) - 1; index >= 0; index-- {
		value := mapValue(entries[index].Message[sessionRunnerNoProgressRecoveryField])
		if len(value) == 0 {
			continue
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return sessionRunnerNoProgressRecovery{}, false
		}
		state, found := parseSessionRunnerNoProgressRecoveryDetail(
			"state" + sessionRunnerNoProgressDetailMarker + string(encoded),
		)
		return state, found
	}
	return sessionRunnerNoProgressRecovery{}, false
}

func (run *sessionRunnerChatRun) restoreNoProgressRecovery(state sessionRunnerNoProgressRecovery) {
	if run == nil {
		return
	}
	expected := runnerRecoveryObligationFingerprint(run.Transcript, run.correctionCause())
	if state.Schema != sessionRunnerNoProgressRecoverySchema || state.ObligationFingerprint != expected {
		state = newSessionRunnerNoProgressRecovery(expected)
	}
	run.NoProgressRecovery = &state
}

func (run *sessionRunnerChatRun) recordNoProgress(calls []agentruntime.ToolCall) sessionRunnerNoProgressRecovery {
	if run == nil {
		return sessionRunnerNoProgressRecovery{}
	}
	expected := runnerRecoveryObligationFingerprint(run.Transcript, run.correctionCause())
	if run.NoProgressRecovery == nil || run.NoProgressRecovery.ObligationFingerprint != expected {
		state := newSessionRunnerNoProgressRecovery(expected)
		run.NoProgressRecovery = &state
	}
	run.NoProgressRecovery.recordNoProgress(calls)
	return *run.NoProgressRecovery
}

func (run *sessionRunnerChatRun) recordMaterialProgress() {
	if run == nil || run.NoProgressRecovery == nil {
		return
	}
	run.NoProgressRecovery.resetMaterialProgress()
}

func sessionRunnerNoProgressRecoveryContext(state sessionRunnerNoProgressRecovery) string {
	if state.Consecutive <= 0 || len(state.ClosedActions) == 0 {
		return ""
	}
	encoded, err := json.Marshal(state.payload())
	if err != nil {
		return ""
	}
	return "Durable no-progress recovery state (server supplied): exact execution identities listed in closed_actions cannot execute again until a material source, content, artifact, or runtime effect changes. Reuse completed receipts and choose a materially different route or finish.\n" + string(encoded)
}
