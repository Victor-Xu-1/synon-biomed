package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
	"synon-go/internal/toolcontract"
)

type sessionRunnerResearchCheckpoint struct {
	EventID          int64
	Checkpoint       sessionRunnerDurableToolCheckpoint
	ResultReference  map[string]any
	ResultUnreadable bool
	// The immutable checkpoint is the attestation authority. Decoding a JSON
	// string or restoring an external result creates only this in-memory view.
	verifiedResult json.RawMessage
}

// sessionRunnerResearchSourceAttempt is a host-observed terminal invocation.
// It is process authority only: a failed, unavailable, or non-substantive
// result may close one exact navigation action but can never become evidence.
type sessionRunnerResearchSourceAttempt struct {
	EventID              int64
	ToolCallID           string
	ToolName             string
	ToolCapabilities     []string
	InvestigationIDs     []string
	Request              map[string]any
	ResearchContinuation map[string]any
	Outcome              agentruntime.ToolResultOutcome
	MaterialUsable       bool
	ResultSHA256         string
}

type sessionRunnerResearchMaterialSet struct {
	Receipts []sessionRunnerResearchSourceReceipt
	Attempts []sessionRunnerResearchSourceAttempt
}

type sessionRunnerResearchSourceReceipt struct {
	EventID              int64            `json:"event_id"`
	ToolCallID           string           `json:"tool_call_id"`
	ToolName             string           `json:"tool_name"`
	ToolCapabilities     []string         `json:"tool_capabilities,omitempty"`
	MaterialRole         string           `json:"material_role"`
	InvestigationIDs     []string         `json:"investigation_ids"`
	Request              map[string]any   `json:"request,omitempty"`
	ResultReference      map[string]any   `json:"result_reference,omitempty"`
	EvidenceCards        []map[string]any `json:"evidence_cards,omitempty"`
	ResearchContinuation map[string]any   `json:"research_continuation,omitempty"`
	MaterialState        string           `json:"material_state"`
	ResultSHA256         string           `json:"result_sha256"`
}

// researchQualifiedEvidenceMessages removes research-source call/result pairs
// that never advanced from an execution attempt or discovery receipt into
// qualified evidence. Non-research tool evidence is preserved unchanged.
func researchQualifiedEvidenceMessages(
	messages []agentruntime.Message,
	materials sessionRunnerResearchMaterialSet,
) []agentruntime.Message {
	attempted := make(map[string]struct{}, len(materials.Attempts))
	qualified := make(map[string]sessionRunnerResearchSourceReceipt, len(materials.Receipts))
	for _, attempt := range materials.Attempts {
		if id := strings.TrimSpace(attempt.ToolCallID); id != "" {
			attempted[id] = struct{}{}
		}
	}
	for _, receipt := range materials.Receipts {
		if receipt.MaterialRole != "evidence" {
			continue
		}
		if id := strings.TrimSpace(receipt.ToolCallID); id != "" {
			qualified[id] = receipt
		}
	}
	result := make([]agentruntime.Message, 0, len(messages))
	for _, message := range messages {
		if strings.EqualFold(strings.TrimSpace(message.Role), "tool") {
			id := strings.TrimSpace(message.ToolCallID)
			if _, isResearchAttempt := attempted[id]; isResearchAttempt {
				receipt, isQualified := qualified[id]
				if !isQualified {
					continue
				}
				if runtimeCapabilitiesContain(receipt.ToolCapabilities, "research") ||
					len(receipt.ToolCapabilities) == 0 && normalizeAgentToolName(receipt.ToolName) == "webresearch" {
					message.Content = researchQualifiedWebResearchMessageContent(message.Content)
					if message.Content == "" {
						continue
					}
				}
			}
			result = append(result, message)
			continue
		}
		if len(message.ToolCalls) == 0 {
			result = append(result, message)
			continue
		}
		filtered := make([]agentruntime.ToolCall, 0, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			id := strings.TrimSpace(call.ID)
			if _, isResearchAttempt := attempted[id]; isResearchAttempt {
				if _, isQualified := qualified[id]; !isQualified {
					continue
				}
			}
			filtered = append(filtered, call)
		}
		if len(filtered) == 0 && strings.TrimSpace(message.Content) == "" && len(message.Parts) == 0 {
			continue
		}
		message.ToolCalls = filtered
		result = append(result, message)
	}
	return result
}

func researchQualifiedWebResearchMessageContent(content string) string {
	object := runnerEvidenceDepthResultObject(content)
	if object == nil {
		return ""
	}
	documents := make([]any, 0)
	for _, raw := range anySliceValue(object["documents"]) {
		document := mapValue(raw)
		if len(document) == 0 {
			continue
		}
		readReceipt := mapValue(document["readReceipt"])
		if relevant, recorded := readReceipt["queryRelevant"]; recorded && !boolValue(relevant, false) {
			continue
		}
		documents = append(documents, document)
	}
	if len(documents) == 0 {
		return ""
	}
	raw, err := json.Marshal(map[string]any{
		"ok": true,
		"result": map[string]any{
			"documents": documents,
		},
	})
	if err != nil {
		return ""
	}
	return string(raw)
}

// sessionRunnerResearchMaterials reconstructs the separate execution-attempt
// and qualified-evidence ledgers from the immutable logical-task transcript.
// No model-authored status label is treated as either authority.
func (s *Server) sessionRunnerResearchMaterials(
	ctx context.Context,
	run *sessionRunnerChatRun,
) (sessionRunnerResearchMaterialSet, error) {
	if run == nil || run.Transcript == nil {
		return sessionRunnerResearchMaterialSet{}, nil
	}
	if s == nil || s.transcriptStore == nil {
		return sessionRunnerResearchMaterialSet{}, errors.New("runner transcript research-material authority is unavailable")
	}
	authority := run.Transcript
	boundary, boundaryRequired, err := s.sessionRunnerDurableTaskBoundary(ctx, run)
	if err != nil {
		return sessionRunnerResearchMaterialSet{}, err
	}
	snapshot, err := s.transcriptStore.GetProjectionSnapshot(ctx, authority.Stream.UID, authority.Stream.OwnerID)
	if err != nil {
		return sessionRunnerResearchMaterialSet{}, err
	}
	after := int64(0)
	boundaryReached := !boundaryRequired
	events := make([]sessionRunnerResearchCheckpoint, 0)
	for {
		page, err := s.transcriptStore.ListProjectedCoordinateEvents(ctx, transcriptstore.ListProjectedEventsInput{
			StreamUID: authority.Stream.UID, OwnerID: authority.Stream.OwnerID,
			BranchID: snapshot.BranchID, BranchGeneration: snapshot.BranchGeneration,
			AfterPublicationSequence: after, ThroughPublicationSequence: snapshot.ThroughPublicationSequence,
			Limit: sessionRunnerDurableEvidencePageSize,
		})
		if err != nil {
			return sessionRunnerResearchMaterialSet{}, err
		}
		for _, projected := range page {
			after = projected.Event.PublicationSeq
			if !boundaryReached && sessionRunnerProjectedEventMatchesTaskBoundary(projected.Event, boundary) {
				boundaryReached = true
			}
			if !boundaryReached || projected.Event.Type != "runner_checkpoint" {
				continue
			}
			payload := projected.ResolvedPayloadJSON
			if len(payload) == 0 {
				payload = projected.Event.PayloadJSON
			}
			var checkpoint sessionRunnerDurableToolCheckpoint
			if json.Unmarshal(payload, &checkpoint) != nil || !s.retainResearchMaterialCheckpoint(checkpoint) {
				continue
			}
			resultReference := researchMaterialResultReference(decodeResearchCheckpointObject(checkpoint.ToolResult))
			resultUnreadable := false
			var verifiedResult json.RawMessage
			if s.sessionRunnerResearchAttemptTool(checkpoint) ||
				normalizeAgentToolName(checkpoint.ToolName) == normalizeAgentToolName(updateStepStatusToolName) {
				// Progress receipts are part of source routing authority: the
				// applied status can differ from the model's requested status.
				// Restore an externalized result before rebuilding the active
				// investigation set, just as we restore externalized evidence.
				result, valid := sessionRunnerDurableToolResult(checkpoint.ToolResult)
				if !valid {
					if normalizeAgentToolName(checkpoint.ToolName) == normalizeAgentToolName(updateStepStatusToolName) {
						continue
					}
					resultUnreadable = true
				} else if restored, restoreErr := s.restoreDurableEvidencePayload(
					ctx, authority.Stream, checkpoint, projected.Event.EventID, result,
				); restoreErr != nil {
					if normalizeAgentToolName(checkpoint.ToolName) == normalizeAgentToolName(updateStepStatusToolName) ||
						(!errors.Is(restoreErr, errRunnerLargeToolResultAuthority) &&
							!errors.Is(restoreErr, errRunnerLargeToolResultConflict)) {
						return sessionRunnerResearchMaterialSet{}, restoreErr
					}
					// The terminal call remains an immutable process fact even if
					// its externalized evidence payload cannot be reconstructed.
					// Keep the attempt and require a different usable source.
					resultUnreadable = true
				} else {
					verifiedResult = json.RawMessage(restored)
				}
			}
			events = append(events, sessionRunnerResearchCheckpoint{
				EventID: projected.Event.EventID, Checkpoint: checkpoint,
				ResultReference: resultReference, ResultUnreadable: resultUnreadable,
				verifiedResult: verifiedResult,
			})
		}
		if len(page) < sessionRunnerDurableEvidencePageSize {
			break
		}
	}
	if !boundaryReached {
		return sessionRunnerResearchMaterialSet{}, errors.New("runner transcript research-material task boundary is unavailable")
	}
	return researchMaterialsFromCheckpoints(s, events, s.generatedPlanResearchFocuses(authority.Stream.FrameID)), nil
}

func (s *Server) generatedPlanResearchFocuses(frameID string) map[string][]string {
	snapshot, found := s.generatedPlanProcessSnapshot(frameID)
	if !found {
		return nil
	}
	plan := mapValue(snapshot.data["_plan_json"])
	steps, err := generatedPlanStepIdentities(plan)
	if err != nil {
		return nil
	}
	focuses := make(map[string][]string)
	for _, step := range steps {
		if step.Kind != generatedPlanStepKindResearch {
			continue
		}
		// Only structured per-module research fields define evidence scope.
		// Display titles and the whole-task summary are navigation prose: using
		// them as lexical authority creates false negatives for valid subtopics.
		focuses[step.ID] = uniqueStrings([]string{
			strings.TrimSpace(step.ResearchQuestion), strings.TrimSpace(step.OutputModule),
		})
	}
	return focuses
}

func (s *Server) retainResearchMaterialCheckpoint(checkpoint sessionRunnerDurableToolCheckpoint) bool {
	phase := strings.ToLower(strings.TrimSpace(checkpoint.ToolPhase))
	switch normalizeAgentToolName(checkpoint.ToolName) {
	case normalizeAgentToolName(updateStepStatusToolName), "readfile":
		return phase == "completed"
	default:
		return researchCheckpointExecutedTerminal(checkpoint) && s.sessionRunnerResearchAttemptTool(checkpoint)
	}
}

func researchCheckpointExecutedTerminal(checkpoint sessionRunnerDurableToolCheckpoint) bool {
	phase := strings.ToLower(strings.TrimSpace(checkpoint.ToolPhase))
	if (phase != "completed" && phase != "failed") || strings.TrimSpace(checkpoint.ToolCallID) == "" ||
		checkpoint.RejectedBeforeExecution {
		return false
	}
	result, valid := sessionRunnerDurableToolResult(checkpoint.ToolResult)
	if !valid {
		return true
	}
	var value any
	return json.Unmarshal([]byte(result), &value) != nil || !agentruntime.ToolResultDidNotExecute(value)
}

func (s *Server) sessionRunnerResearchAttemptTool(checkpoint sessionRunnerDurableToolCheckpoint) bool {
	name := strings.TrimSpace(checkpoint.ToolName)
	if strings.HasPrefix(strings.ToLower(name), "mcp__") {
		// MCP names are caller-controlled. The immutable host attestation,
		// including request/result digests, is required even for a failed
		// attempt; evidence qualification remains a separate later decision.
		return validSessionRunnerMCPDurableCheckpoint(checkpoint)
	}
	if runtimeCapabilitiesContainSource(checkpoint.ToolCapabilities) {
		return true
	}
	// Older checkpoints did not persist Tool capabilities. Resolve their
	// first-party contract through the registry compatibility boundary.
	return s != nil && s.sessionRunnerEvidenceTool(name)
}

func researchMaterialsFromCheckpoints(
	s *Server,
	events []sessionRunnerResearchCheckpoint,
	optionalFocuses ...map[string][]string,
) sessionRunnerResearchMaterialSet {
	var focusByInvestigation map[string][]string
	if len(optionalFocuses) > 0 {
		focusByInvestigation = optionalFocuses[0]
	}
	readVersions := make(map[string]struct{})
	for _, event := range events {
		checkpoint := event.Checkpoint
		if normalizeAgentToolName(checkpoint.ToolName) != "readfile" {
			continue
		}
		if input := decodeResearchCheckpointObject(sessionRunnerDurableExecutedToolInput(checkpoint)); input != nil {
			if version := strings.TrimSpace(stringValue(input["version_id"])); version != "" {
				readVersions[version] = struct{}{}
			}
		}
	}
	active := make(map[string]struct{})
	materials := sessionRunnerResearchMaterialSet{
		Receipts: make([]sessionRunnerResearchSourceReceipt, 0),
		Attempts: make([]sessionRunnerResearchSourceAttempt, 0),
	}
	for _, event := range events {
		checkpoint := event.Checkpoint
		executedInput := sessionRunnerDurableExecutedToolInput(checkpoint)
		projected := checkpoint
		if len(event.verifiedResult) > 0 {
			projected.ToolResult = event.verifiedResult
		}
		if strings.EqualFold(strings.TrimSpace(checkpoint.ToolName), updateStepStatusToolName) {
			if !strings.EqualFold(strings.TrimSpace(checkpoint.ToolPhase), "completed") {
				continue
			}
			input := decodeResearchCheckpointObject(executedInput)
			result := decodeResearchCheckpointObject(projected.ToolResult)
			step := firstNonEmpty(strings.TrimSpace(stringValue(result["step"])), strings.TrimSpace(stringValue(input["step"])))
			status := firstNonEmpty(strings.TrimSpace(stringValue(result["status"])), strings.TrimSpace(stringValue(input["status"])))
			// A completion request may be redirected back to in_progress. Route
			// subsequent sources by the applied receipt, never by the requested
			// transition, otherwise recovery evidence becomes orphaned.
			switch status {
			case "in_progress":
				if step != "" {
					active[step] = struct{}{}
				}
			case "completed", "blocked", "skipped":
				delete(active, step)
			}
			continue
		}
		if !researchCheckpointExecutedTerminal(projected) || s == nil || !s.sessionRunnerResearchAttemptTool(checkpoint) {
			continue
		}
		investigations := make([]string, 0, len(active))
		for step := range active {
			investigations = append(investigations, step)
		}
		sort.Strings(investigations)
		call := agentruntime.ToolCall{
			ID: checkpoint.ToolCallID, Name: checkpoint.ToolName, Arguments: executedInput,
		}
		digest := sha256.Sum256(checkpoint.ToolResult)
		resultSHA256 := hex.EncodeToString(digest[:])
		if descriptor, _, externalized, err := toolcontract.DecodeExternalizedResult(checkpoint.ToolResult); externalized && err == nil {
			resultSHA256 = descriptor.SHA256
		}
		attempt := sessionRunnerResearchSourceAttempt{
			EventID: event.EventID, ToolCallID: strings.TrimSpace(checkpoint.ToolCallID),
			ToolName:         strings.TrimSpace(checkpoint.ToolName),
			ToolCapabilities: append([]string(nil), checkpoint.ToolCapabilities...),
			InvestigationIDs: investigations,
			Request:          decodeResearchCheckpointObject(executedInput), Outcome: agentruntime.ToolResultUnavailable,
			ResultSHA256: resultSHA256,
		}
		if event.ResultUnreadable {
			materials.Attempts = append(materials.Attempts, attempt)
			continue
		}
		resultText, valid := sessionRunnerDurableToolResult(projected.ToolResult)
		if !valid {
			materials.Attempts = append(materials.Attempts, attempt)
			continue
		}
		var resultValue any
		if json.Unmarshal([]byte(resultText), &resultValue) != nil {
			materials.Attempts = append(materials.Attempts, attempt)
			continue
		}
		outcome := agentruntime.ClassifyToolResult(resultValue)
		attempt.Outcome = outcome
		attempt.ResearchContinuation = researchSourceContinuation(call, resultValue)
		investigationFocuses := researchFocusesForInvestigations(investigations, focusByInvestigation)
		normalizedTool := normalizeAgentToolName(checkpoint.ToolName)
		discoveryOnly := runtimeCapabilitiesContain(checkpoint.ToolCapabilities, "search") &&
			!runtimeCapabilitiesContain(checkpoint.ToolCapabilities, "evidence-read") ||
			len(checkpoint.ToolCapabilities) == 0 && normalizedTool == "websearch" ||
			(strings.HasPrefix(strings.ToLower(strings.TrimSpace(checkpoint.ToolName)), "mcp__") &&
				sessionRunnerMCPDiscoveryTool(checkpoint.ToolName))
		materialRole := ""
		switch {
		case discoveryOnly && !outcome.HardFailed() && researchDiscoveryCallUsable(call, resultValue):
			materialRole = "discovery"
			attempt.MaterialUsable = outcome != agentruntime.ToolResultUnavailable
		case !discoveryOnly && !outcome.HardFailed() && outcome != agentruntime.ToolResultUnavailable &&
			researchSourceCallUsable(call, resultValue, investigationFocuses):
			materialRole = "evidence"
			attempt.MaterialUsable = true
		case (runtimeCapabilitiesContain(checkpoint.ToolCapabilities, "research") ||
			len(checkpoint.ToolCapabilities) == 0 && normalizedTool == "webresearch") &&
			!outcome.HardFailed() &&
			researchWebResearchDiscoveryUsable(resultValue):
			// Candidate identities and source-owned routes are useful process
			// state even when no fetched page qualified as evidence. They retain
			// discovery coverage but never satisfy module evidence binding.
			materialRole = "discovery"
		}
		materials.Attempts = append(materials.Attempts, attempt)
		if materialRole == "" {
			continue
		}
		result := decodeResearchCheckpointObject(projected.ToolResult)
		resultReference := event.ResultReference
		if len(resultReference) == 0 {
			resultReference = researchMaterialResultReference(result)
		}
		materialState := "inline_result"
		if version := strings.TrimSpace(stringValue(resultReference["version_id"])); version != "" {
			if _, read := readVersions[version]; read {
				materialState = "read_requested"
			} else if resultReference["read_with"] != nil || boolValue(resultReference["truncated"], false) {
				materialState = "preview_with_read_handle"
			}
		}
		materials.Receipts = append(materials.Receipts, sessionRunnerResearchSourceReceipt{
			EventID: event.EventID, ToolCallID: strings.TrimSpace(checkpoint.ToolCallID),
			ToolName:             strings.TrimSpace(checkpoint.ToolName),
			ToolCapabilities:     append([]string(nil), checkpoint.ToolCapabilities...),
			MaterialRole:         materialRole,
			InvestigationIDs:     investigations,
			Request:              decodeResearchCheckpointObject(executedInput),
			ResultReference:      resultReference,
			EvidenceCards:        researchMaterialEvidenceCards(call, resultValue, investigationFocuses...),
			ResearchContinuation: attempt.ResearchContinuation,
			MaterialState:        materialState,
			ResultSHA256:         attempt.ResultSHA256,
		})
	}
	return materials
}

func researchWebResearchDiscoveryUsable(result any) bool {
	object := runnerEvidenceDepthResultObject(mustMarshalRunnerCorrectionResult(result))
	if object == nil {
		return false
	}
	for _, field := range []string{"candidateSources", "sources", "results", "records"} {
		if len(anySliceValue(object[field])) > 0 {
			return true
		}
	}
	return len(anySliceValue(mapValue(object["sourceFrontier"])["next_routes"])) > 0
}

func researchDiscoveryCallUsable(call agentruntime.ToolCall, result any) bool {
	object := runnerEvidenceDepthResultObject(mustMarshalRunnerCorrectionResult(result))
	if object == nil {
		return false
	}
	if normalizeAgentToolName(call.Name) != "websearch" {
		// MCP discovery is navigation rather than claim evidence. A non-empty,
		// non-failed structured result is sufficient to preserve the attempted
		// route and its language; module completion still requires a separate
		// substantive evidence receipt.
		return len(object) > 0
	}
	for _, field := range []string{"sources", "results", "records", "candidateSources"} {
		if len(anySliceValue(object[field])) > 0 {
			return true
		}
	}
	return false
}

func researchFocusesForInvestigations(investigations []string, focusByInvestigation map[string][]string) []string {
	focuses := make([]string, 0)
	for _, investigation := range investigations {
		focuses = append(focuses, focusByInvestigation[investigation]...)
	}
	return uniqueStrings(focuses)
}

func researchSourceCallUsable(call agentruntime.ToolCall, result any, focuses []string) bool {
	if normalizeAgentToolName(call.Name) != "webresearch" {
		if !runnerCorrectionSourceCallUsable(call, result) {
			return false
		}
		switch normalizeAgentToolName(call.Name) {
		case "webfetch":
			return researchWebFetchRelevantToInvestigation(call, result, focuses)
		case "fetcharticlefulltext":
			return researchArticleRecordRelevantToInvestigation(call, result, focuses)
		}
		return true
	}
	object := runnerEvidenceDepthResultObject(mustMarshalRunnerCorrectionResult(result))
	if object == nil || len(anySliceValue(object["documents"])) == 0 {
		return false
	}
	// A substantive read is source material even when an adaptive research
	// sweep reports that it can keep broadening. Counts and stop reasons are
	// navigation signals, never module-success authority.
	return int(numberValue(mapValue(object["quality"])["deepReadSources"])) > 0
}

func researchWebFetchRelevantToInvestigation(call agentruntime.ToolCall, result any, focuses []string) bool {
	object := runnerCorrectionResultObject(result)
	if object == nil {
		return false
	}
	readable := webResearchReadableDocument(firstNonEmpty(
		stringValue(object["body"]), stringValue(object["content"]), stringValue(object["text"]),
	), firstNonEmpty(stringValue(object["contentType"]), stringValue(object["content_type"])))
	if readable == "" {
		return false
	}
	var input map[string]any
	_ = json.Unmarshal(call.Arguments, &input)
	focusCompared, focusRelevant := researchDocumentMatchesAnyFocus(focuses, readable)
	if focusCompared {
		// The structured investigation scope is authoritative. Navigation prose
		// can share generic words with an unrelated page, but it cannot override
		// a module-level entity or topic mismatch.
		return focusRelevant
	}
	requestCompared, requestRelevant := researchDocumentMatchesAnyFocus([]string{
		strings.TrimSpace(stringValue(input["prompt"])),
		strings.TrimSpace(stringValue(input["query"])),
	}, readable)
	if requestCompared {
		return requestRelevant
	}
	// Preserve generic fetches that carry no comparable research focus.
	return true
}

func researchDocumentMatchesAnyFocus(focuses []string, readable string) (bool, bool) {
	compared := false
	for _, query := range uniqueStrings(focuses) {
		relevant, _, comparableTerms := webResearchDocumentQueryRelevance(query, readable)
		if comparableTerms == 0 {
			continue
		}
		compared = true
		if relevant {
			return true, true
		}
	}
	return compared, false
}

func researchSourceAttemptsForStep(
	attempts []sessionRunnerResearchSourceAttempt,
	stepID string,
) []sessionRunnerResearchSourceAttempt {
	result := make([]sessionRunnerResearchSourceAttempt, 0)
	seen := make(map[string]struct{})
	for _, attempt := range attempts {
		matched := false
		for _, investigationID := range attempt.InvestigationIDs {
			if strings.TrimSpace(investigationID) == strings.TrimSpace(stepID) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		identity := fmt.Sprintf("%d:%s", attempt.EventID, attempt.ToolCallID)
		if _, exists := seen[identity]; exists {
			continue
		}
		seen[identity] = struct{}{}
		result = append(result, attempt)
	}
	return result
}

func researchContinuationFirstAction(continuation map[string]any) map[string]any {
	actions := anySliceValue(continuation["next_actions"])
	if len(actions) > 0 {
		return mapValue(actions[0])
	}
	// A deep-read requirement is created from durable discovery receipts rather
	// than from a source tool's nextActions response. Project the first bounded
	// locator into the same action contract so every continuation consumer sees
	// one authoritative route instead of requiring a parallel special case.
	if !boolValue(continuation["required"], false) ||
		strings.TrimSpace(stringValue(continuation["required_capability"])) == "" {
		return nil
	}
	for _, rawTarget := range anySliceValue(continuation["available_read_targets"]) {
		target := copyMapAny(mapValue(rawTarget))
		if len(target) == 0 {
			continue
		}
		if strings.TrimSpace(stringValue(target["url"])) != "" {
			target["action"] = "read_source"
			return target
		}
	}
	return nil
}

func decodeResearchCheckpointObject(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value map[string]any
	if decoder.Decode(&value) != nil {
		return nil
	}
	return value
}

func researchMaterialResultReference(result map[string]any) map[string]any {
	if len(result) == 0 {
		return nil
	}
	reference := make(map[string]any)
	for _, key := range []string{"artifact_id", "version_id", "content_url", "read_with", "truncated", "size_bytes", "sha256"} {
		if value, present := result[key]; present && value != nil {
			reference[key] = value
		}
	}
	if len(reference) == 0 {
		return nil
	}
	return reference
}

func researchSourceReceiptValues(receipts []sessionRunnerResearchSourceReceipt) []any {
	result := make([]any, 0, len(receipts))
	for _, receipt := range receipts {
		result = append(result, researchSourceReceiptValue(receipt))
	}
	return result
}

// reconcileGeneratedPlanResearchEvidence projects the immutable, currently
// qualified material ledger back into every persisted research module. This is
// deliberately broader than the step being updated: a stronger evidence policy
// must be able to retire a receipt admitted by an older runner before later
// modules or final synthesis can replay it.
func reconcileGeneratedPlanResearchEvidence(
	statuses map[string]any,
	steps []generatedPlanStepIdentity,
	receipts []sessionRunnerResearchSourceReceipt,
) bool {
	changed := false
	for _, step := range steps {
		if step.Kind != generatedPlanStepKindResearch {
			continue
		}
		current := mapValue(statuses[step.ID])
		if len(current) == 0 {
			continue
		}
		next := copyMapAny(current)
		bound := researchEvidenceReceiptsForStep(receipts, step.ID, stringValueSlice(current["source_refs"]))
		next["source_receipts"] = researchSourceReceiptValues(bound)
		next["source_refs"] = researchEvidenceReceiptReferences(bound)
		languages := researchQueryLanguagesForStep(receipts, step, stringValueSlice(current["source_refs"]))
		next["query_languages"] = languages
		if strings.TrimSpace(stringValue(current["status"])) == "completed" {
			var continuation map[string]any
			switch {
			case len(bound) == 0:
				continuation = map[string]any{
					"reason":                "research_material_required",
					"detail":                "no qualified source receipt is currently bound; consider binding a listed receipt or acquiring a stronger source if material to the requested output",
					"available_source_refs": researchEvidenceReceiptCallIDs(receipts),
				}
			case len(researchMissingQueryLanguages(step, languages)) > 0:
				continuation = map[string]any{
					"reason":                  "research_language_coverage_required",
					"detail":                  "the planned discovery languages were not all observed; consider the remaining languages if they materially improve the requested output",
					"missing_query_languages": researchMissingQueryLanguages(step, languages),
				}
			}
			if continuation != nil {
				next["research_continuation"] = researchContinuationAsAdvisory(continuation)
			} else if existing := mapValue(current["research_continuation"]); len(existing) > 0 {
				next["research_continuation"] = researchContinuationAsAdvisory(existing)
			} else {
				delete(next, "research_continuation")
			}
		}
		before, beforeErr := json.Marshal(current)
		after, afterErr := json.Marshal(next)
		if beforeErr == nil && afterErr == nil && string(before) == string(after) {
			continue
		}
		statuses[step.ID] = next
		changed = true
	}
	return changed
}

func researchSourceReceiptValue(receipt sessionRunnerResearchSourceReceipt) map[string]any {
	value := map[string]any{
		"event_id": receipt.EventID, "tool_call_id": receipt.ToolCallID, "tool_name": receipt.ToolName,
		"material_role":     receipt.MaterialRole,
		"investigation_ids": append([]string(nil), receipt.InvestigationIDs...),
		"request":           receipt.Request, "result_reference": receipt.ResultReference,
		"material_state": receipt.MaterialState, "result_sha256": receipt.ResultSHA256,
	}
	if len(receipt.EvidenceCards) > 0 {
		cards := make([]any, 0, len(receipt.EvidenceCards))
		for _, card := range receipt.EvidenceCards {
			cards = append(cards, copyMapAny(card))
		}
		value["evidence_cards"] = cards
	}
	if len(receipt.ResearchContinuation) > 0 {
		value["research_continuation"] = receipt.ResearchContinuation
	}
	return value
}

func researchEvidenceReceiptsForStep(
	receipts []sessionRunnerResearchSourceReceipt,
	stepID string,
	references []string,
) []sessionRunnerResearchSourceReceipt {
	return researchReceiptsForStep(receipts, stepID, references, false)
}

func researchReceiptsForStep(
	receipts []sessionRunnerResearchSourceReceipt,
	stepID string,
	references []string,
	includeDiscovery bool,
) []sessionRunnerResearchSourceReceipt {
	result := make([]sessionRunnerResearchSourceReceipt, 0)
	seen := make(map[string]struct{})
	for _, receipt := range receipts {
		if receipt.MaterialRole != "evidence" && !includeDiscovery {
			continue
		}
		matched := false
		for _, investigationID := range receipt.InvestigationIDs {
			if strings.TrimSpace(investigationID) == strings.TrimSpace(stepID) {
				matched = true
				break
			}
		}
		if !matched {
			for _, reference := range references {
				if researchSourceReceiptMatchesReference(receipt, reference) {
					matched = true
					break
				}
			}
		}
		if !matched {
			continue
		}
		identity := fmt.Sprintf("%d:%s", receipt.EventID, receipt.ToolCallID)
		if _, exists := seen[identity]; exists {
			continue
		}
		seen[identity] = struct{}{}
		result = append(result, receipt)
	}
	return result
}

func researchQueryLanguagesForStep(
	receipts []sessionRunnerResearchSourceReceipt,
	step generatedPlanStepIdentity,
	references []string,
) []string {
	bound := researchReceiptsForStep(receipts, step.ID, references, true)
	covered := map[string]bool{}
	for _, receipt := range bound {
		if researchReceiptRanNativeModuleDiscovery(receipt, step.ID) {
			for _, required := range step.DiscoveryQueries {
				covered[required.Language] = true
			}
			continue
		}
		for _, required := range step.DiscoveryQueries {
			if researchValueContainsExactText(receipt.Request, required.Query) {
				covered[required.Language] = true
			}
		}
	}
	result := make([]string, 0, len(covered))
	for _, language := range []string{"zh", "en"} {
		if covered[language] {
			result = append(result, language)
		}
	}
	return result
}

func researchReceiptRanNativeModuleDiscovery(receipt sessionRunnerResearchSourceReceipt, stepID string) bool {
	active := false
	for _, investigationID := range receipt.InvestigationIDs {
		if strings.TrimSpace(investigationID) == strings.TrimSpace(stepID) {
			active = true
			break
		}
	}
	if !active {
		return false
	}
	switch normalizeAgentToolName(receipt.ToolName) {
	case "websearch":
		return true
	case "webresearch":
		switch strings.ToLower(strings.TrimSpace(stringValue(receipt.Request["operation"]))) {
		case "search", "search_and_fetch", "verify_fact", "research":
			return true
		}
	}
	return false
}

func researchMissingQueryLanguages(step generatedPlanStepIdentity, covered []string) []string {
	coveredSet := map[string]bool{}
	for _, language := range covered {
		coveredSet[strings.ToLower(strings.TrimSpace(language))] = true
	}
	required := map[string]bool{}
	for _, query := range step.DiscoveryQueries {
		if language := strings.ToLower(strings.TrimSpace(query.Language)); language != "" {
			required[language] = true
		}
	}
	missing := []string{}
	for _, language := range []string{"zh", "en"} {
		if required[language] && !coveredSet[language] {
			missing = append(missing, language)
		}
	}
	return missing
}

func researchValueContainsExactText(value any, target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) == target
	case map[string]any:
		for _, nested := range typed {
			if researchValueContainsExactText(nested, target) {
				return true
			}
		}
	case []any:
		for _, nested := range typed {
			if researchValueContainsExactText(nested, target) {
				return true
			}
		}
	case []string:
		for _, nested := range typed {
			if strings.TrimSpace(nested) == target {
				return true
			}
		}
	}
	return false
}

func researchSourceReceiptMatchesReference(receipt sessionRunnerResearchSourceReceipt, reference string) bool {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return false
	}
	artifactReference := strings.TrimSpace(strings.TrimPrefix(reference, "artifact:"))
	callID := strings.TrimSpace(receipt.ToolCallID)
	if reference == callID || strings.HasPrefix(reference, callID+":") || strings.HasPrefix(reference, callID+"#") {
		return true
	}
	for _, key := range []string{"artifact_id", "version_id"} {
		if identity := strings.TrimSpace(stringValue(receipt.ResultReference[key])); identity != "" && artifactReference == identity {
			return true
		}
	}
	for _, key := range []string{"url", "doi", "pmid", "accession", "id"} {
		if locator := strings.TrimSpace(stringValue(receipt.Request[key])); locator != "" && reference == locator {
			return true
		}
	}
	referenceIdentifiers := make(map[string]struct{})
	addSessionRunnerReferences(referenceIdentifiers, reference)
	for _, card := range receipt.EvidenceCards {
		cardText := strings.TrimSpace(strings.Join([]string{
			stringValue(card["title"]), stringValue(card["url"]),
		}, " "))
		if cardText == "" {
			continue
		}
		cardIdentifiers := make(map[string]struct{})
		addSessionRunnerReferences(cardIdentifiers, cardText)
		for identifier := range referenceIdentifiers {
			if _, matched := cardIdentifiers[identifier]; matched {
				return true
			}
		}
	}
	return false
}

// researchEvidenceReceiptReferences projects only source identities that were
// admitted as qualified evidence. Model-authored navigation may nominate a
// source to bind an earlier receipt, but unmatched discovery URLs never become
// durable writing context merely because the model repeated them.
func researchEvidenceReceiptReferences(receipts []sessionRunnerResearchSourceReceipt) []string {
	result := make([]string, 0)
	seen := make(map[string]struct{})
	appendReference := func(reference string) {
		reference = strings.TrimSpace(reference)
		if reference == "" || len(result) >= maxGeneratedPlanNavigationItems {
			return
		}
		key := strings.ToLower(reference)
		if canonical := canonicalWebResearchURL(reference); canonical != "" {
			key = canonical
		}
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		result = append(result, reference)
	}
	for _, receipt := range receipts {
		before := len(result)
		hasCardReference := false
		for _, card := range receipt.EvidenceCards {
			if reference := strings.TrimSpace(stringValue(card["url"])); reference != "" {
				hasCardReference = true
				appendReference(reference)
			}
		}
		if hasCardReference {
			continue
		}
		for _, key := range []string{"url", "doi", "pmid", "accession", "id"} {
			appendReference(stringValue(receipt.Request[key]))
		}
		if len(result) == before {
			appendReference(receipt.ToolCallID)
		}
	}
	return result
}

func researchEvidenceReceiptCallIDs(receipts []sessionRunnerResearchSourceReceipt) []string {
	result := make([]string, 0, len(receipts))
	for _, receipt := range receipts {
		if receipt.MaterialRole == "evidence" && strings.TrimSpace(receipt.ToolCallID) != "" {
			result = append(result, strings.TrimSpace(receipt.ToolCallID))
		}
	}
	return uniqueStrings(result)
}
