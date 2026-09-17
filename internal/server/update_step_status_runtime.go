package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"synon-go/internal/agentruntime"
)

const updateStepStatusToolName = "update_step_status"
const sessionRunnerPlanStepsIncompleteReasonCode = "plan_step_status_required"

type sessionRunnerPlanStepsIncomplete struct {
	steps []string
}

func (err sessionRunnerPlanStepsIncomplete) Error() string {
	return "approved plan has steps without terminal status: " + strings.Join(err.steps, ", ")
}

func (err sessionRunnerPlanStepsIncomplete) runnerCorrection() (string, string) {
	return sessionRunnerPlanStepsIncompleteReasonCode,
		"before completing, call update_step_status for every unreported plan step and mark each completed, blocked, or skipped; exact remaining titles: " + strings.Join(err.steps, "; ")
}

type generatedPlanStepIdentity struct {
	ID               string
	Title            string
	Description      string
	Kind             string
	OutputModule     string
	ResearchQuestion string
	ResearchDepth    string
	DiscoveryQueries []generatedPlanQuery
}

func (s *Server) executeAgentUpdateStepStatus(
	ctx context.Context,
	sessionID, toolCallID string,
	input map[string]any,
) (any, error) {
	if s == nil || s.workspaceStore == nil {
		return nil, errors.New("plan progress store is unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	toolCallID = strings.TrimSpace(toolCallID)
	if sessionID == "" || toolCallID == "" {
		return nil, errors.New("plan progress requires session and tool-call identities")
	}
	if err := s.validateRegisteredTool(updateStepStatusToolName, input); err != nil {
		return nil, err
	}
	requestedStep := strings.TrimSpace(stringValue(input["step"]))
	status := strings.TrimSpace(stringValue(input["status"]))
	notes := strings.TrimSpace(stringValue(input["notes"]))
	if len([]rune(requestedStep)) > 512 || len([]rune(notes)) > 4096 {
		return nil, errors.New("plan progress step or notes exceed the bounded contract")
	}

	s.compatRequestMu.Lock()
	defer s.compatRequestMu.Unlock()
	metadata, found, err := s.workspaceStore.GetFrameRuntimeMetadataWithContext(ctx, sessionID)
	if err != nil || !found {
		if err == nil {
			err = errors.New("plan progress metadata is unavailable")
		}
		return nil, err
	}
	contextData := copyMapAny(metadata.ContextData)
	if contextData == nil || (!boolValue(contextData["_plan_approved"], false) &&
		!boolValue(contextData["_plan_execution_authorized"], false)) {
		return nil, errors.New("plan progress requires an active executable plan")
	}
	artifactID := strings.TrimSpace(stringValue(contextData["_plan_artifact_id"]))
	versionID := strings.TrimSpace(stringValue(contextData["_plan_version_id"]))
	if artifactID == "" || versionID == "" {
		return nil, errors.New("approved plan identity is incomplete")
	}
	steps, err := generatedPlanStepIdentities(mapValue(contextData["_plan_json"]))
	if err != nil {
		return nil, err
	}
	step, err := resolveGeneratedPlanStepIdentity(steps, requestedStep)
	if err != nil {
		return nil, err
	}
	document, err := generatedPlanDocumentFromMap(mapValue(contextData["_plan_json"]))
	if err != nil {
		return nil, err
	}
	statuses := copyMapAny(mapValue(contextData["_step_statuses"]))
	if statuses == nil {
		statuses = map[string]any{}
	}
	run, _ := ctx.Value(transcriptRunnerChatRunContextKey{}).(*sessionRunnerChatRun)
	allSourceReceipts := []sessionRunnerResearchSourceReceipt{}
	allSourceAttempts := []sessionRunnerResearchSourceAttempt{}
	newSourceReceipts := []sessionRunnerResearchSourceReceipt{}
	researchMaterialsAvailable := false
	watermark := int64(numberValue(contextData[generatedPlanResearchConsumedSourceEventIDKey]))
	latest := watermark
	if run != nil && run.Transcript != nil {
		materials, materialErr := s.sessionRunnerResearchMaterials(ctx, run)
		if materialErr != nil {
			log.Printf("research_material_handoff_unavailable session=%q err_type=%T", sessionID, materialErr)
		} else {
			researchMaterialsAvailable = true
			allSourceReceipts = materials.Receipts
			allSourceAttempts = materials.Attempts
			for _, attempt := range materials.Attempts {
				if attempt.EventID > latest {
					latest = attempt.EventID
				}
			}
			for _, receipt := range materials.Receipts {
				if receipt.EventID <= watermark {
					continue
				}
				newSourceReceipts = append(newSourceReceipts, receipt)
				if receipt.EventID > latest {
					latest = receipt.EventID
				}
			}
		}
	}
	// A terminal source attempt is consumed exactly once even when the result
	// failed, was unavailable, or was non-substantive. Only usable receipts are
	// bound as evidence; the independent attempt ledger closes exact navigation
	// actions without allowing a failure to leak into the report.
	if terminal := int64(numberValue(contextData[generatedPlanResearchLatestSourceEventIDKey])); terminal > latest {
		latest = terminal
	}
	cursorAdvanced := latest > watermark
	if cursorAdvanced {
		contextData[generatedPlanResearchConsumedSourceEventIDKey] = latest
	}
	evidenceReconciled := false
	if researchMaterialsAvailable {
		evidenceReconciled = reconcileGeneratedPlanResearchEvidence(statuses, steps, allSourceReceipts)
		if evidenceReconciled {
			contextData["_step_statuses"] = statuses
		}
	}
	current := mapValue(statuses[step.ID])
	if actionabilityErr := validateGeneratedPlanStepActionability(document, contextData, step, status); actionabilityErr != nil {
		if evidenceReconciled || cursorAdvanced {
			metadata.ContextData = contextData
			if run != nil && run.Transcript != nil {
				_, err = s.workspaceStore.SetClaimedFrameRuntimeMetadata(ctx, run.Transcript.Claim, metadata)
			} else {
				_, err = s.workspaceStore.SetFrameRuntimeMetadata(sessionID, metadata)
			}
			if err != nil {
				return nil, fmt.Errorf("persist reconciled plan progress: %w", err)
			}
		}
		navigation, navigationErr := normalizeGeneratedPlanNavigationInput(map[string]any{}, current)
		if navigationErr != nil {
			return nil, navigationErr
		}
		receipt := generatedPlanStatusReceipt(document, contextData, step,
			firstNonEmpty(strings.TrimSpace(stringValue(current["status"])), "pending"),
			stringValue(current["notes"]), navigation, anySliceValue(current["source_receipts"]),
			stringValueSlice(current["query_languages"]), true,
		)
		receipt["research_transition"] = generatedPlanResearchTransition(document, contextData, step, newSourceReceipts)
		receipt["applied"] = false
		receipt["requested_status"] = status
		receipt["research_continuation"] = map[string]any{
			"reason": "plan_sequence", "detail": actionabilityErr.Error(),
		}
		return receipt, nil
	}
	currentStatus := strings.TrimSpace(stringValue(current["status"]))
	currentNotes := stringValue(current["notes"])
	navigation, err := normalizeGeneratedPlanNavigationInput(input, current)
	if err != nil {
		return nil, err
	}
	requestedSourceRefs := append([]string(nil), navigation["source_refs"]...)
	boundSourceReceipts := researchEvidenceReceiptsForStep(
		allSourceReceipts, step.ID, requestedSourceRefs,
	)
	boundSourceAttempts := researchSourceAttemptsForStep(allSourceAttempts, step.ID)
	boundSourceReceiptValues := researchSourceReceiptValues(boundSourceReceipts)
	if len(boundSourceReceiptValues) == 0 && !researchMaterialsAvailable {
		// Cached step metadata is only a continuity fallback while transcript
		// authority is unreadable. Once the immutable ledger is available, an
		// empty qualified set is authoritative and must also retire receipts that
		// were admitted by an older or weaker evidence policy.
		boundSourceReceiptValues = anySliceValue(current["source_receipts"])
	}
	queryLanguages := researchQueryLanguagesForStep(allSourceReceipts, step, requestedSourceRefs)
	if len(queryLanguages) == 0 && !researchMaterialsAvailable {
		queryLanguages = stringValueSlice(current["query_languages"])
	}
	if step.Kind == generatedPlanStepKindResearch && researchMaterialsAvailable {
		navigation["source_refs"] = researchEvidenceReceiptReferences(boundSourceReceipts)
	}
	requestedStatus := status
	var researchContinuation map[string]any
	if step.Kind == generatedPlanStepKindResearch && run != nil && run.Transcript != nil {
		var pendingSourceContinuation map[string]any
		if researchMaterialsAvailable {
			pendingSourceContinuation = researchPendingSourceAttemptContinuation(boundSourceAttempts)
		} else if existing := mapValue(current["research_continuation"]); len(anySliceValue(existing["next_actions"])) > 0 {
			// A transient transcript read failure cannot erase an already durable
			// source-owned frontier. Preserve it until authority is readable.
			pendingSourceContinuation = copyMapAny(existing)
		}
		if len(pendingSourceContinuation) > 0 {
			researchContinuation = copyMapAny(pendingSourceContinuation)
			researchContinuation["reason"] = "research_follow_up_required"
			researchContinuation["detail"] = "continue the current investigation through the source-owned follow-up action"
		}
		deepSourceReadRequired := requestedStatus == "completed" && researchMaterialsAvailable &&
			generatedPlanResearchDepthRequiresSourceRead(step.ResearchDepth) && len(boundSourceReceiptValues) == 0
		if deepSourceReadRequired {
			status = "in_progress"
			researchContinuation = generatedPlanRequiredSourceReadContinuation(
				step, allSourceReceipts, pendingSourceContinuation,
			)
		} else if requestedStatus == "completed" {
			switch {
			case len(boundSourceReceiptValues) == 0:
				researchContinuation = map[string]any{
					"reason":                "research_material_required",
					"detail":                "no qualified source receipt is currently bound; consider binding a listed receipt or acquiring a stronger source if material to the requested output",
					"available_source_refs": researchEvidenceReceiptCallIDs(allSourceReceipts),
				}
			case len(researchMissingQueryLanguages(step, queryLanguages)) > 0:
				researchContinuation = map[string]any{
					"reason":                  "research_language_coverage_required",
					"detail":                  "the planned discovery languages were not all observed; consider the remaining languages if they materially improve the requested output",
					"missing_query_languages": researchMissingQueryLanguages(step, queryLanguages),
				}
			case len(pendingSourceContinuation) > 0:
				// Keep the source-owned route visible, but the explicit completion
				// decision transfers authority back to the model.
			}
			researchContinuation = researchContinuationAsAdvisory(researchContinuation)
		}
	}
	if step.Kind == generatedPlanStepKindResearch && researchContinuation == nil && requestedStatus != "completed" &&
		!cursorAdvanced && len(newSourceReceipts) == 0 {
		// Repeating an in-progress status is administrative bookkeeping, not a
		// decision to cancel an unresolved source requirement. Preserve the
		// durable continuation until a source attempt advances its cursor or the
		// model explicitly requests a terminal step status.
		if existing := mapValue(current["research_continuation"]); researchContinuationRequiresExecution(existing) {
			researchContinuation = copyMapAny(existing)
		}
	}
	// Status and notes are independent progress data. An omitted note retains
	// prior work; an explicit empty note clears it. Neither a repeated status
	// nor a transition may silently erase newly collected material.
	if _, supplied := input["notes"]; !supplied {
		notes = currentNotes
	}
	unchanged := currentStatus == status && currentNotes == notes
	for _, field := range generatedPlanNavigationFields {
		unchanged = unchanged && equalStringValues(stringValueSlice(current[field]), navigation[field])
	}
	unchanged = unchanged && equalGeneratedPlanResearchContinuation(
		mapValue(current["research_continuation"]), researchContinuation,
	)
	unchanged = unchanged && len(newSourceReceipts) == 0 && !cursorAdvanced && !evidenceReconciled
	if unchanged {
		receipt := generatedPlanStatusReceipt(document, contextData, step, status, notes, navigation, boundSourceReceiptValues, queryLanguages, true)
		if researchContinuation != nil {
			receipt["research_continuation"] = researchContinuation
			if researchContinuationRequiresExecution(researchContinuation) {
				receipt["applied"] = false
				receipt["requested_status"] = requestedStatus
			} else {
				receipt["applied"] = true
			}
		}
		return receipt, nil
	}
	now := time.Now().UTC()
	nextState := map[string]any{
		"status": status, "title": step.Title, "description": step.Description, "notes": notes,
		"observations": navigation["observations"], "source_refs": navigation["source_refs"], "follow_ups": navigation["follow_ups"],
		"source_receipts": boundSourceReceiptValues,
		"query_languages": queryLanguages,
		"tool_call_id":    toolCallID, "updated_at": now.Format(time.RFC3339Nano),
	}
	if researchContinuation != nil {
		nextState["research_continuation"] = researchContinuation
	}
	statuses[step.ID] = nextState
	contextData["_step_statuses"] = statuses
	metadata.ContextData = contextData
	if run != nil && run.Transcript != nil {
		_, err = s.workspaceStore.SetClaimedFrameRuntimeMetadata(ctx, run.Transcript.Claim, metadata)
	} else {
		_, err = s.workspaceStore.SetFrameRuntimeMetadata(sessionID, metadata)
	}
	if err != nil {
		return nil, fmt.Errorf("persist plan progress: %w", err)
	}
	receipt := generatedPlanStatusReceipt(document, contextData, step, status, notes, navigation, boundSourceReceiptValues, queryLanguages, false)
	receipt["research_transition"] = generatedPlanResearchTransition(document, contextData, step, newSourceReceipts)
	if researchContinuation != nil {
		receipt["research_continuation"] = researchContinuation
		if researchContinuationRequiresExecution(researchContinuation) {
			receipt["applied"] = false
			receipt["requested_status"] = requestedStatus
		} else {
			receipt["applied"] = true
		}
	}
	return receipt, nil
}

func equalGeneratedPlanResearchContinuation(left, right map[string]any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func generatedPlanStatusReceipt(
	document generatedPlanDocument,
	contextData map[string]any,
	step generatedPlanStepIdentity,
	status, notes string,
	navigation map[string][]string,
	sourceReceipts []any,
	queryLanguages []string,
	idempotent bool,
) map[string]any {
	return map[string]any{
		"ok": true, "status": status, "step": step.ID, "title": step.Title,
		"notes": notes, "observations": navigation["observations"], "source_refs": navigation["source_refs"],
		"follow_ups": navigation["follow_ups"], "plan_artifact_id": contextData["_plan_artifact_id"],
		"plan_version_id":     contextData["_plan_version_id"],
		"source_receipts":     sourceReceipts,
		"query_languages":     queryLanguages,
		"research_transition": generatedPlanResearchTransition(document, contextData, step, nil), "idempotent": idempotent,
		"effect": agentruntime.ToolEffectValue(toolEffectState(idempotent), "control-state", "plan-progress"),
	}
}

func toolEffectState(idempotent bool) agentruntime.ToolEffectState {
	if idempotent {
		return agentruntime.ToolEffectUnchanged
	}
	return agentruntime.ToolEffectChanged
}

func generatedPlanStepIdentities(plan map[string]any) ([]generatedPlanStepIdentity, error) {
	steps := make([]generatedPlanStepIdentity, 0)
	for _, rawPhase := range anySliceValue(plan["phases"]) {
		phase := mapValue(rawPhase)
		for _, rawDelegation := range anySliceValue(phase["delegations"]) {
			delegation := mapValue(rawDelegation)
			for _, rawStep := range anySliceValue(delegation["steps"]) {
				step := mapValue(rawStep)
				identity := generatedPlanStepIdentity{
					ID: strings.TrimSpace(stringValue(step["id"])), Title: strings.TrimSpace(stringValue(step["title"])),
					Description: strings.TrimSpace(stringValue(step["description"])), Kind: strings.TrimSpace(stringValue(step["kind"])),
					OutputModule:     strings.TrimSpace(stringValue(step["output_module"])),
					ResearchQuestion: strings.TrimSpace(stringValue(step["research_question"])),
					ResearchDepth:    strings.TrimSpace(stringValue(step["research_depth"])),
				}
				for _, rawQuery := range anySliceValue(step["discovery_queries"]) {
					query := mapValue(rawQuery)
					identity.DiscoveryQueries = append(identity.DiscoveryQueries, generatedPlanQuery{
						Language: strings.TrimSpace(stringValue(query["language"])),
						Query:    strings.TrimSpace(stringValue(query["query"])),
					})
				}
				if identity.ID == "" || identity.Title == "" {
					return nil, errors.New("approved plan contains an invalid step identity")
				}
				steps = append(steps, identity)
			}
		}
	}
	if len(steps) == 0 {
		return nil, errors.New("approved plan contains no executable steps")
	}
	return steps, nil
}

func resolveGeneratedPlanStepIdentity(
	steps []generatedPlanStepIdentity,
	requested string,
) (generatedPlanStepIdentity, error) {
	requested = strings.TrimSpace(requested)
	candidates := []string{requested}
	for _, separator := range []string{"->", "→", "/"} {
		if index := strings.LastIndex(requested, separator); index >= 0 {
			if leaf := strings.TrimSpace(requested[index+len(separator):]); leaf != "" && leaf != requested {
				candidates = append(candidates, leaf)
			}
		}
	}
	var match generatedPlanStepIdentity
	for _, step := range steps {
		matched := false
		for _, candidate := range candidates {
			if candidate == step.ID || candidate == step.Title {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		if match.ID != "" && match.ID != step.ID {
			return generatedPlanStepIdentity{}, errors.New("plan step title is ambiguous; use the exact step id")
		}
		match = step
	}
	if match.ID == "" {
		available := make([]string, 0, len(steps))
		for _, step := range steps {
			available = append(available, step.ID+"="+step.Title)
		}
		return generatedPlanStepIdentity{}, fmt.Errorf(
			"plan step %q is not part of the approved plan; exact steps: %s",
			requested, strings.Join(available, "; "),
		)
	}
	return match, nil
}

func (s *Server) incompleteGeneratedPlanStepTitles(frameID string) ([]string, error) {
	if s == nil || s.workspaceStore == nil || strings.TrimSpace(frameID) == "" {
		return nil, nil
	}
	metadata, found, err := s.workspaceStore.GetFrameRuntimeMetadata(strings.TrimSpace(frameID))
	if err != nil || !found {
		return nil, err
	}
	contextData := mapValue(metadata.ContextData)
	if !boolValue(contextData["_plan_approved"], false) {
		return nil, nil
	}
	steps, err := generatedPlanStepIdentities(mapValue(contextData["_plan_json"]))
	if err != nil {
		return nil, err
	}
	statuses := mapValue(contextData["_step_statuses"])
	remaining := make([]string, 0)
	for _, step := range steps {
		status := strings.TrimSpace(stringValue(mapValue(statuses[step.ID])["status"]))
		if status != "completed" && status != "blocked" && status != "skipped" {
			remaining = append(remaining, step.Title)
		}
	}
	return remaining, nil
}
