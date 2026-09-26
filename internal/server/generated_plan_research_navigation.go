package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"synon-go/internal/agentruntime"
)

func (s *Server) generatedPlanResearchHandoffContext(
	frameID string,
	call agentruntime.ToolCall,
	result any,
) string {
	state := s.generatedPlanDesiredOutputsContext(frameID)
	var navigation map[string]any
	if state != "" && json.Unmarshal([]byte(state), &navigation) != nil {
		navigation = nil
	}
	actionable := generatedPlanActionableInvestigations(navigation["current_investigations"])
	// The investigation count is model-controlled. Avoid multiplying it for a
	// capacity hint: a very large decoded list must not overflow into a negative
	// allocation before the bounded source-material path can reject it.
	focuses := make([]string, 0)
	for _, investigation := range actionable {
		for _, field := range []string{"research_question", "output_module", "description", "title"} {
			if value := strings.TrimSpace(stringValue(investigation[field])); value != "" {
				focuses = append(focuses, value)
			}
		}
	}
	material := currentResearchSourceMaterial(call, result, focuses...)
	if len(navigation) == 0 && len(material) == 0 {
		return ""
	}
	payloadValue := map[string]any{
		"schema": generatedPlanResearchHandoffSchema,
		"source_call": map[string]any{
			"id": strings.TrimSpace(call.ID), "tool": strings.TrimSpace(call.Name),
		},
		"investigations":  actionable,
		"open_follow_ups": navigation["open_follow_ups"],
	}
	if len(material) > 0 {
		payloadValue["source_material"] = material
	}
	if continuation := researchSourceContinuation(call, result); len(continuation) > 0 {
		payloadValue["source_continuation"] = continuation
	}
	payload, err := json.Marshal(payloadValue)
	if err != nil {
		return ""
	}
	return string(payload)
}

func generatedPlanResearchTransition(
	document generatedPlanDocument,
	data map[string]any,
	step generatedPlanStepIdentity,
	receipts []sessionRunnerResearchSourceReceipt,
) map[string]any {
	navigation := generatedPlanResearchNavigation(document, data)
	var updated map[string]any
	for _, raw := range anySliceValue(navigation["current_investigations"]) {
		item := mapValue(raw)
		if stringValue(item["id"]) == step.ID {
			updated = item
		}
	}
	result := map[string]any{
		"schema":                "synon.research_transition.v1",
		"navigation_state_only": true,
		"updated_investigation": updated,
		"new_source_receipts":   researchSourceReceiptValues(receipts),
		"open_follow_ups":       navigation["open_follow_ups"],
		"next_investigations":   generatedPlanActionableInvestigations(navigation["current_investigations"]),
	}
	if synthesis := generatedPlanResearchSynthesisContext(document, data); len(synthesis) > 0 {
		result["synthesis_context"] = synthesis
	}
	return result
}

func generatedPlanActionableInvestigations(value any) []map[string]any {
	items := anySliceValue(value)
	inProgress := make([]map[string]any, 0)
	for _, raw := range items {
		item := mapValue(raw)
		if strings.TrimSpace(stringValue(item["status"])) == "in_progress" {
			inProgress = append(inProgress, item)
		}
	}
	if len(inProgress) > 0 {
		return inProgress
	}
	for _, raw := range items {
		item := mapValue(raw)
		if strings.TrimSpace(stringValue(item["status"])) == "pending" {
			return []map[string]any{item}
		}
	}
	return []map[string]any{}
}

func validateGeneratedPlanStepActionability(
	document generatedPlanDocument,
	data map[string]any,
	step generatedPlanStepIdentity,
	requestedStatus string,
) error {
	if !boolValue(data["_plan_approved"], false) {
		// Autonomous plans are navigation rather than a command queue. Typed step
		// identity is still validated, but sequence cannot reject or redirect the
		// model's substantive work.
		return nil
	}
	current := mapValue(mapValue(data["_step_statuses"])[step.ID])
	currentStatus := strings.TrimSpace(stringValue(current["status"]))
	if generatedPlanTerminalStatus(currentStatus) && requestedStatus == currentStatus {
		// Replaying a terminal receipt or refining its navigation data is safe;
		// it cannot move execution past unfinished work.
		return nil
	}
	actionable := generatedPlanActionableInvestigations(
		generatedPlanResearchNavigation(document, data)["current_investigations"],
	)
	available := make([]string, 0, len(actionable))
	for _, item := range actionable {
		id := strings.TrimSpace(stringValue(item["id"]))
		if id == step.ID {
			return nil
		}
		if id != "" {
			available = append(available, id)
		}
	}
	if len(available) == 0 {
		return fmt.Errorf("plan step %q is not actionable because the plan has no open work", step.ID)
	}
	return fmt.Errorf(
		"plan step %q is not actionable yet; continue the current plan step: %s",
		step.ID, strings.Join(available, ", "),
	)
}

func generatedPlanTerminalStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "completed", "blocked", "skipped":
		return true
	default:
		return false
	}
}

func attachRuntimeResearchNavigationState(messages []chatCompletionMessage, state string) []chatCompletionMessage {
	state = strings.TrimSpace(state)
	var payload map[string]any
	if state == "" || json.Unmarshal([]byte(state), &payload) != nil ||
		stringValue(payload["schema"]) != generatedPlanResearchNavigationSchema {
		return messages
	}
	result := append([]chatCompletionMessage(nil), messages...)
	for index := range result {
		if result[index].Role != "system" ||
			!strings.Contains(result[index].Content, "<research_navigation_state>") {
			continue
		}
		result[index].Content = "[System] <research_navigation_state>" + state + "</research_navigation_state>"
		return result
	}
	for index := len(result) - 1; index >= 0; index-- {
		if result[index].Role != "tool" || strings.TrimSpace(result[index].Content) == "" {
			continue
		}
		if replaced, ok := replaceRuntimeResearchNavigationToolContext(result[index].Content, payload); ok {
			result[index].Content = replaced
			return result
		}
	}
	for index := len(result) - 1; index >= 0; index-- {
		if result[index].Role != "tool" || strings.TrimSpace(result[index].Content) == "" {
			continue
		}
		enriched, err := agentruntime.AttachToolResultModelContext(result[index].Content, payload)
		if err != nil {
			return messages
		}
		result[index].Content = enriched
		return result
	}
	// Immediately after a compact boundary there may be no surviving tool
	// message to enrich. Preserve the same state-only payload at the end of the
	// leading system block so the active investigation and source handles do not
	// disappear merely because raw history was pruned.
	return appendRuntimeTerminalPolicyContextMessage(result,
		"[System] <research_navigation_state>"+state+"</research_navigation_state>")
}

// replaceRuntimeResearchNavigationToolContext updates only the transient
// navigation fragment previously attached by this runtime. Historical tool
// bytes and unrelated model context remain untouched.
func replaceRuntimeResearchNavigationToolContext(content string, payload map[string]any) (string, bool) {
	var result map[string]any
	if json.Unmarshal([]byte(content), &result) != nil {
		return "", false
	}
	const contextKey = "_synon_model_context"
	context := mapValue(result[contextKey])
	if stringValue(context["schema"]) != generatedPlanResearchNavigationSchema {
		return "", false
	}
	result[contextKey] = payload
	encoded, err := json.Marshal(result)
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

const (
	generatedPlanResearchNavigationSchema = "synon.research_navigation.v1"
	maxGeneratedPlanNavigationItems       = 256
	maxGeneratedPlanNavigationItemRunes   = 4096
)

var generatedPlanNavigationFields = []string{"observations", "source_refs", "follow_ups"}

// Research navigation is model-recorded working state. It carries discoveries
// into later investigation and synthesis, but it is not evidence authority and
// never participates in completion admission.
func generatedPlanResearchNavigation(document generatedPlanDocument, data map[string]any) map[string]any {
	statuses := mapValue(data["_step_statuses"])
	currentIDs := make(map[string]struct{})
	current := make([]map[string]any, 0)
	openFollowUps := make([]string, 0)
	for _, phase := range document.Phases {
		for _, track := range phase.Delegations {
			for _, step := range track.Steps {
				currentIDs[step.ID] = struct{}{}
				item := generatedPlanResearchStepState(step, mapValue(statuses[step.ID]))
				item["phase"] = phase.Name
				item["phase_id"] = phase.ID
				item["track"] = track.Name
				item["track_id"] = track.ID
				current = append(current, item)
				openFollowUps = append(openFollowUps, stringValueSlice(item["follow_ups"])...)
			}
		}
	}
	retiredIDs := make([]string, 0)
	for id, value := range statuses {
		if _, present := currentIDs[id]; present || !generatedPlanResearchStateMaterial(mapValue(value)) {
			continue
		}
		retiredIDs = append(retiredIDs, id)
	}
	sort.Strings(retiredIDs)
	retired := make([]map[string]any, 0, len(retiredIDs))
	for _, id := range retiredIDs {
		state := mapValue(statuses[id])
		item := generatedPlanResearchStepState(generatedPlanStep{
			ID: id, Title: strings.TrimSpace(stringValue(state["title"])),
			Description: strings.TrimSpace(stringValue(state["description"])),
		}, state)
		retired = append(retired, item)
	}
	return map[string]any{
		"schema":                 generatedPlanResearchNavigationSchema,
		"navigation_state_only":  true,
		"plan_artifact_id":       data["_plan_artifact_id"],
		"plan_version_id":        data["_plan_version_id"],
		"control_mode":           stringValue(data["_plan_control_mode"]),
		"task_summary":           document.TaskSummary,
		"desired_outputs":        append([]string(nil), document.DesiredOutputs...),
		"current_investigations": current,
		"open_follow_ups":        uniqueStrings(openFollowUps),
		"retired_research":       retired,
	}
}

func generatedPlanResearchStepState(step generatedPlanStep, state map[string]any) map[string]any {
	item := map[string]any{
		"id": step.ID, "title": step.Title, "description": step.Description,
		"status": firstNonEmpty(strings.TrimSpace(stringValue(state["status"])), "pending"),
	}
	if kind := strings.TrimSpace(step.Kind); kind != "" && kind != generatedPlanStepKindWork {
		item["kind"] = kind
	}
	if step.Kind == generatedPlanStepKindResearch {
		item["output_module"] = step.OutputModule
		item["research_question"] = step.ResearchQuestion
		item["research_depth"] = step.ResearchDepth
		if len(step.DiscoveryQueries) > 0 {
			queries := make([]any, 0, len(step.DiscoveryQueries))
			for _, query := range step.DiscoveryQueries {
				queries = append(queries, map[string]any{"language": query.Language, "query": query.Query})
			}
			item["discovery_queries"] = queries
		}
	}
	if note := strings.TrimSpace(stringValue(state["notes"])); note != "" {
		item["notes"] = note
	}
	for _, field := range generatedPlanNavigationFields {
		if values := stringValueSlice(state[field]); len(values) > 0 {
			item[field] = values
		}
	}
	if receipts := anySliceValue(state["source_receipts"]); len(receipts) > 0 {
		item["source_receipts"] = receipts
	}
	if continuation := mapValue(state["research_continuation"]); len(continuation) > 0 {
		item["research_continuation"] = continuation
	}
	if languages := stringValueSlice(state["query_languages"]); len(languages) > 0 {
		item["query_languages"] = languages
	}
	return item
}

func generatedPlanResearchStateMaterial(state map[string]any) bool {
	if strings.TrimSpace(stringValue(state["notes"])) != "" {
		return true
	}
	for _, field := range generatedPlanNavigationFields {
		if len(stringValueSlice(state[field])) > 0 {
			return true
		}
	}
	if len(anySliceValue(state["source_receipts"])) > 0 {
		return true
	}
	if len(mapValue(state["research_continuation"])) > 0 {
		return true
	}
	return false
}

func generatedPlanDocumentFromMap(plan map[string]any) (generatedPlanDocument, error) {
	raw, err := json.Marshal(plan)
	if err != nil {
		return generatedPlanDocument{}, err
	}
	var document generatedPlanDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return generatedPlanDocument{}, err
	}
	if len(document.Phases) == 0 {
		return generatedPlanDocument{}, errors.New("active plan contains no investigations")
	}
	return document, nil
}

func normalizeGeneratedPlanNavigationInput(input map[string]any, current map[string]any) (map[string][]string, error) {
	result := make(map[string][]string, len(generatedPlanNavigationFields))
	for _, field := range generatedPlanNavigationFields {
		prior := stringValueSlice(current[field])
		raw, supplied := input[field]
		if !supplied {
			result[field] = prior
			continue
		}
		values := stringValueSlice(raw)
		if len(values) > maxGeneratedPlanNavigationItems {
			return nil, errors.New("plan research navigation exceeds its storage safety boundary")
		}
		for _, value := range values {
			if err := validateGeneratedPlanNavigationText(field, value); err != nil {
				return nil, err
			}
		}
		result[field] = uniqueStrings(values)
	}
	return result, nil
}

func validateGeneratedPlanNavigationText(field, value string) error {
	count := utf8.RuneCountInString(value)
	if count < 1 || count > maxGeneratedPlanNavigationItemRunes {
		return fmt.Errorf(
			"update_step_status.%s must contain 1-%d characters",
			field, maxGeneratedPlanNavigationItemRunes,
		)
	}
	if strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("update_step_status.%s contains an invalid null byte", field)
	}
	return nil
}

func stringValueSlice(value any) []string {
	var raw []any
	switch typed := value.(type) {
	case []string:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := strings.TrimSpace(item); text != "" {
				result = append(result, text)
			}
		}
		return result
	case []any:
		raw = typed
	default:
		return nil
	}
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		if text := strings.TrimSpace(stringValue(item)); text != "" {
			result = append(result, text)
		}
	}
	return result
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, present := seen[value]; present {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func equalStringValues(left, right []string) bool {
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
