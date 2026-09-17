package server

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"synon-go/internal/agentruntime"
)

// RecoverRequiredToolCall reconstructs deterministic plan bookkeeping and an
// exact source-owned continuation after the provider repeatedly ignores a
// named tool choice. New scientific/source selection remains model-owned; this
// path can replay only arguments already materialized in durable plan state.
func (g serverAgentRuntimeToolGateway) RecoverRequiredToolCall(
	requiredTool string,
	messages []agentruntime.Message,
	tools []agentruntime.ToolSchema,
) (agentruntime.ToolCall, bool) {
	if g.server == nil || !agentRuntimeToolSchemaNamed(tools, requiredTool) {
		return agentruntime.ToolCall{}, false
	}
	choice := mapValue(g.generatedPlanRequiredToolChoice(messages, tools))
	if normalizeAgentToolName(stringValue(choice["name"])) != normalizeAgentToolName(requiredTool) {
		return agentruntime.ToolCall{}, false
	}
	snapshot, found := g.server.generatedPlanProcessSnapshot(g.sessionID)
	if !found || len(snapshot.actionable) == 0 {
		return agentruntime.ToolCall{}, false
	}
	action := snapshot.actionable[0]
	if normalizeAgentToolName(requiredTool) != normalizeAgentToolName(updateStepStatusToolName) {
		continuation := mapValue(action["research_continuation"])
		if !researchContinuationRequiresExecution(continuation) {
			return agentruntime.ToolCall{}, false
		}
		choice := mapValue(researchContinuationRequiredToolChoice(continuation, tools))
		if normalizeAgentToolName(stringValue(choice["name"])) != normalizeAgentToolName(requiredTool) {
			return agentruntime.ToolCall{}, false
		}
		input := generatedPlanApplyResearchContinuationInput(
			requiredTool, agentRuntimeToolCapabilities(tools, requiredTool), map[string]any{}, continuation,
		)
		arguments, err := json.Marshal(input)
		if err != nil || len(input) == 0 {
			return agentruntime.ToolCall{}, false
		}
		identity := sha256.Sum256([]byte(strings.Join([]string{
			strings.TrimSpace(g.sessionID), strings.TrimSpace(stringValue(continuation["continuation_id"])),
			strings.TrimSpace(requiredTool), string(arguments),
		}, "\x00")))
		return agentruntime.ToolCall{
			ID: fmt.Sprintf("runtime-source-continuation-%x", identity[:12]), Name: requiredTool,
			Arguments: arguments, RuntimeRecovered: true,
		}, true
	}
	step, found := snapshot.typedStep(stringValue(action["id"]))
	if !found {
		return agentruntime.ToolCall{}, false
	}
	status := strings.TrimSpace(stringValue(action["status"]))
	nextStatus := ""
	switch status {
	case "pending", "":
		nextStatus = "in_progress"
	case "in_progress":
		nextStatus = "completed"
	default:
		return agentruntime.ToolCall{}, false
	}
	arguments, err := json.Marshal(map[string]any{"step": step.ID, "status": nextStatus})
	if err != nil {
		return agentruntime.ToolCall{}, false
	}
	identity := sha256.Sum256([]byte(fmt.Sprintf(
		"%s\x00%s\x00%s\x00%s\x00%d\x00%d",
		strings.TrimSpace(g.sessionID), step.ID, status, nextStatus,
		int64(numberValue(snapshot.data[generatedPlanResearchLatestSourceEventIDKey])),
		int64(numberValue(snapshot.data[generatedPlanResearchConsumedSourceEventIDKey])),
	)))
	return agentruntime.ToolCall{
		ID: fmt.Sprintf("runtime-plan-control-%x", identity[:12]), Name: updateStepStatusToolName,
		Arguments: arguments, RuntimeRecovered: true,
	}, true
}

const (
	generatedPlanResearchLatestSourceEventIDKey   = "_research_latest_source_event_id"
	generatedPlanResearchConsumedSourceEventIDKey = "_research_handoff_source_event_id"
)

func resetGeneratedPlanResearchSourceCursors(data map[string]any) {
	delete(data, generatedPlanResearchLatestSourceEventIDKey)
	delete(data, generatedPlanResearchConsumedSourceEventIDKey)
}

type generatedPlanProcessSnapshot struct {
	document   generatedPlanDocument
	data       map[string]any
	actionable []map[string]any
}

func (s *Server) generatedPlanProcessSnapshot(frameID string) (generatedPlanProcessSnapshot, bool) {
	if s == nil || s.workspaceStore == nil || strings.TrimSpace(frameID) == "" {
		return generatedPlanProcessSnapshot{}, false
	}
	metadata, found, err := s.workspaceStore.GetFrameRuntimeMetadata(strings.TrimSpace(frameID))
	if err != nil || !found {
		return generatedPlanProcessSnapshot{}, false
	}
	data := mapValue(metadata.ContextData)
	if strings.TrimSpace(stringValue(data["_plan_artifact_id"])) == "" ||
		!generatedPlanExecutionAuthorized(data) {
		return generatedPlanProcessSnapshot{}, false
	}
	document, err := generatedPlanDocumentFromMap(mapValue(data["_plan_json"]))
	if err != nil {
		return generatedPlanProcessSnapshot{}, false
	}
	navigation := generatedPlanResearchNavigation(document, data)
	return generatedPlanProcessSnapshot{
		document: document, data: data,
		actionable: generatedPlanActionableInvestigations(navigation["current_investigations"]),
	}, true
}

func generatedPlanExecutionAuthorized(data map[string]any) bool {
	return boolValue(data["_plan_execution_authorized"], false) ||
		boolValue(data["_plan_approved"], false)
}

func (snapshot generatedPlanProcessSnapshot) typedResearchStep(id string) (generatedPlanStepIdentity, bool) {
	step, found := snapshot.typedStep(id)
	if !found || step.Kind != generatedPlanStepKindResearch || step.OutputModule == "" || step.ResearchQuestion == "" {
		return generatedPlanStepIdentity{}, false
	}
	return step, true
}

func (snapshot generatedPlanProcessSnapshot) typedStep(id string) (generatedPlanStepIdentity, bool) {
	steps, err := generatedPlanStepIdentities(mapValue(snapshot.data["_plan_json"]))
	if err != nil {
		return generatedPlanStepIdentity{}, false
	}
	for _, step := range steps {
		if step.ID == strings.TrimSpace(id) {
			return step, true
		}
	}
	return generatedPlanStepIdentity{}, false
}

func (snapshot generatedPlanProcessSnapshot) resolveTypedStep(requested string) (generatedPlanStepIdentity, bool) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return generatedPlanStepIdentity{}, false
	}
	steps, err := generatedPlanStepIdentities(mapValue(snapshot.data["_plan_json"]))
	if err != nil {
		return generatedPlanStepIdentity{}, false
	}
	for _, step := range steps {
		if strings.EqualFold(step.ID, requested) || strings.EqualFold(step.Title, requested) {
			return step, true
		}
	}
	return generatedPlanStepIdentity{}, false
}

func (snapshot generatedPlanProcessSnapshot) requestedStepFollows(currentID, requested string) bool {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return false
	}
	steps, err := generatedPlanStepIdentities(mapValue(snapshot.data["_plan_json"]))
	if err != nil {
		return false
	}
	currentPosition, requestedPosition := -1, -1
	for index, step := range steps {
		if step.ID == strings.TrimSpace(currentID) {
			currentPosition = index
		}
		if strings.EqualFold(step.ID, requested) || strings.EqualFold(step.Title, requested) {
			requestedPosition = index
		}
	}
	return currentPosition >= 0 && requestedPosition > currentPosition
}

func (snapshot generatedPlanProcessSnapshot) requestedActionableStep(requested string) (map[string]any, bool) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return nil, false
	}
	for _, action := range snapshot.actionable {
		step, found := snapshot.typedStep(stringValue(action["id"]))
		if !found {
			continue
		}
		if strings.EqualFold(step.ID, requested) || strings.EqualFold(step.Title, requested) {
			return action, true
		}
	}
	return nil, false
}

func (snapshot generatedPlanProcessSnapshot) hasTypedResearchModules() bool {
	steps, err := generatedPlanStepIdentities(mapValue(snapshot.data["_plan_json"]))
	if err != nil {
		return false
	}
	for _, step := range steps {
		if step.Kind == generatedPlanStepKindResearch && step.OutputModule != "" && step.ResearchQuestion != "" {
			return true
		}
	}
	return false
}

func (s *Server) generatedPlanActiveStep(frameID string) (generatedPlanStepIdentity, map[string]any, bool) {
	snapshot, found := s.generatedPlanProcessSnapshot(frameID)
	if !found || len(snapshot.actionable) == 0 {
		return generatedPlanStepIdentity{}, nil, false
	}
	action := snapshot.actionable[0]
	step, found := snapshot.typedStep(stringValue(action["id"]))
	return step, action, found
}

func (s *Server) generatedPlanActiveResearchStep(frameID string) (generatedPlanStepIdentity, map[string]any, bool) {
	snapshot, found := s.generatedPlanProcessSnapshot(frameID)
	if !found || !snapshot.hasTypedResearchModules() || len(snapshot.actionable) == 0 {
		return generatedPlanStepIdentity{}, nil, false
	}
	action := snapshot.actionable[0]
	step, found := snapshot.typedResearchStep(stringValue(action["id"]))
	return step, action, found
}

// generatedPlanResearchReconciliationToolChoice exposes the one deterministic
// control transition that must precede every competing Skill, artifact-repair,
// and connector selector. A terminal source cursor is durable execution fact;
// until it is consumed, choosing another action can attach the evidence to the
// wrong module or leave the plan permanently behind the work already done.
func (s *Server) generatedPlanResearchReconciliationToolChoice(
	frameID string,
	tools []agentruntime.ToolSchema,
) any {
	snapshot, found := s.generatedPlanProcessSnapshot(frameID)
	if !found || len(snapshot.actionable) == 0 ||
		!boolValue(snapshot.data["_plan_approved"], false) ||
		!agentRuntimeToolSchemaNamed(tools, updateStepStatusToolName) {
		return nil
	}
	step, found := snapshot.typedResearchStep(stringValue(snapshot.actionable[0]["id"]))
	if !found || step.ID == "" {
		return nil
	}
	latestSource := int64(numberValue(snapshot.data[generatedPlanResearchLatestSourceEventIDKey]))
	consumedSource := int64(numberValue(snapshot.data[generatedPlanResearchConsumedSourceEventIDKey]))
	if latestSource <= consumedSource {
		return nil
	}
	return map[string]any{"type": "tool", "name": updateStepStatusToolName}
}

func (g serverAgentRuntimeToolGateway) generatedPlanPriorityToolChoice(
	messages []agentruntime.Message,
	tools []agentruntime.ToolSchema,
) any {
	if g.server == nil {
		return nil
	}
	if choice := g.server.generatedPlanResearchReconciliationToolChoice(g.sessionID, tools); choice != nil {
		return choice
	}
	snapshot, found := g.server.generatedPlanProcessSnapshot(g.sessionID)
	if !found || len(snapshot.actionable) == 0 {
		return nil
	}
	if continuation := mapValue(snapshot.actionable[0]["research_continuation"]); researchContinuationRequiresExecution(continuation) {
		if choice := researchContinuationRequiredToolChoice(continuation, tools); choice != nil {
			return choice
		}
	}
	if !boolValue(snapshot.data["_plan_approved"], false) {
		// Autonomous plans are durable navigation. They may expose a concrete
		// evidence obligation above, but they never force administrative status
		// transitions or rewrite an artifact-correction route.
		return nil
	}
	if strings.TrimSpace(stringValue(snapshot.actionable[0]["status"])) == "pending" &&
		len(mapValue(snapshot.data["_step_statuses"])) > 0 {
		// Once an authorized plan has durable progress, its next state transition
		// must precede artifact correction. Otherwise a pre-delivery save remains
		// working_data forever and the correction selector repeatedly repairs bytes
		// that cannot yet be published.
		return g.generatedPlanRequiredToolChoice(messages, tools)
	}
	if strings.TrimSpace(stringValue(snapshot.actionable[0]["status"])) == "in_progress" &&
		g.taskRun != nil && strings.TrimSpace(g.taskRun.CorrectionReason) == "artifact_reference_correction_required" &&
		runnerCorrectionReportsMissingDeliverable(g.taskRun.CorrectionDetail) &&
		agentRuntimeToolSchemaNamed(tools, updateStepStatusToolName) {
		// A final candidate with a missing published deliverable proves that
		// artifact authoring reached completion review. Finish the active plan
		// state before another save so pre-delivery staging can release the next
		// save as a snapshot. Other correction findings remain durable and are
		// revalidated after this control-only transition.
		return map[string]any{"type": "tool", "name": updateStepStatusToolName}
	}
	return nil
}

// generatedPlanUpdateStepStatusInput binds progress writes to the one
// currently actionable step. This is control identity, not scientific
// content: providers may choose the transition and notes, while stale titles
// or guessed IDs cannot orphan subsequent source receipts.
func (s *Server) generatedPlanUpdateStepStatusInput(frameID, toolName string, input map[string]any) map[string]any {
	if normalizeAgentToolName(toolName) != normalizeAgentToolName(updateStepStatusToolName) {
		return input
	}
	snapshot, found := s.generatedPlanProcessSnapshot(frameID)
	if !found || len(snapshot.actionable) == 0 {
		return input
	}
	if !boolValue(snapshot.data["_plan_approved"], false) {
		// Autonomous plan progress belongs to the model. Preserve the requested
		// step and its findings; update_step_status performs typed ID/title
		// resolution and records it as non-blocking navigation state.
		if step, resolved := snapshot.resolveTypedStep(stringValue(input["step"])); resolved {
			normalized := copyMapAny(input)
			normalized["step"] = step.ID
			return normalized
		}
	}
	action := snapshot.actionable[0]
	requestedStep := strings.TrimSpace(stringValue(input["step"]))
	requestedActionable := false
	if selected, matched := snapshot.requestedActionableStep(requestedStep); matched {
		action = selected
		requestedActionable = true
	}
	id := strings.TrimSpace(stringValue(action["id"]))
	if id == "" {
		return input
	}
	requestedStatus := strings.TrimSpace(stringValue(input["status"]))
	normalized := copyMapAny(input)
	normalized["step"] = id
	laterStepIntent := !requestedActionable && snapshot.requestedStepFollows(id, requestedStep)
	if laterStepIntent {
		// The payload describes the requested later step. Only its transition
		// intent can be applied to the active step; binding its notes or evidence
		// to another step corrupts the durable research history.
		delete(normalized, "notes")
		for _, field := range generatedPlanNavigationFields {
			delete(normalized, field)
		}
	}
	if strings.TrimSpace(stringValue(action["status"])) == "in_progress" && requestedStatus == "in_progress" && laterStepIntent {
		step, typed := snapshot.typedStep(id)
		if typed && (step.Kind != generatedPlanStepKindResearch ||
			len(generatedPlanQualifiedReceiptValues(action["source_receipts"])) > 0) {
			// A request to start a later plan step is a model-owned transition
			// intent. Complete only the active step here; the next round starts the
			// following step. Evidence reconciliation may update immutable receipt
			// facts, but cannot attach the later step's scientific payload here.
			normalized["status"] = "completed"
		}
	}
	currentStatus := strings.TrimSpace(stringValue(action["status"]))
	if boolValue(snapshot.data["_plan_approved"], false) &&
		(currentStatus == "pending" || currentStatus == "") && requestedStatus == "completed" {
		// A user-approved plan retains its explicit start transition. Autonomous
		// plans are navigation state and may record one model-owned completion
		// without inserting a purely administrative model round.
		normalized["status"] = "in_progress"
		delete(normalized, "notes")
		for _, field := range generatedPlanNavigationFields {
			delete(normalized, field)
		}
	}
	if strings.TrimSpace(stringValue(normalized["status"])) == "" {
		// A provider may emit an empty progress object after a long context
		// boundary. Restore only the durable current state; never infer that the
		// work completed. The valid no-op returns the active continuation so the
		// next execution unit can recover without losing evidence.
		status := strings.TrimSpace(stringValue(action["status"]))
		if status == "pending" || status == "" {
			status = "in_progress"
		}
		normalized["status"] = status
	}
	return normalized
}

// generatedPlanResearchQueryInput binds source calls to the active research
// frontier. Discovery uses the plan's bilingual queries; an already discovered
// fetch route keeps its exact URL. Once both planned lanes have run, later calls
// retain their model-authored follow-up so a finding can open a new investigation.
func (s *Server) generatedPlanResearchQueryInput(
	frameID, toolName string,
	input map[string]any,
	capabilitySets ...[]string,
) map[string]any {
	capabilities := []string(nil)
	if len(capabilitySets) > 0 {
		capabilities = capabilitySets[0]
	}
	if len(capabilities) == 0 && s != nil {
		lookupName := strings.TrimSpace(toolName)
		if canonical, err := canonicalRuntimeToolName(lookupName); err == nil {
			lookupName = canonical
		}
		if tool, found := s.registeredTool(lookupName); found {
			capabilities = tool.Capabilities
		}
	}
	researchTool := runtimeCapabilitiesContain(capabilities, "research")
	searchTool := runtimeCapabilitiesContain(capabilities, "search")
	evidenceReader := runtimeCapabilitiesContain(capabilities, "evidence-read")
	if !runtimeCapabilitiesContainSource(capabilities) {
		return input
	}
	step, action, found := s.generatedPlanActiveResearchStep(frameID)
	if !found {
		return input
	}
	researchContinuation := mapValue(action["research_continuation"])
	if !researchContinuationRequiresExecution(researchContinuation) {
		researchContinuation = nil
	}
	if researchTool {
		operation := strings.TrimSpace(stringValue(input["operation"]))
		continuationTool := researchContinuationExpectedTool(researchContinuationFirstAction(researchContinuation))
		if operation != "" && operation != "search" && operation != "search_and_fetch" && operation != "verify_fact" && operation != "research" &&
			normalizeAgentToolName(continuationTool) != normalizeAgentToolName(toolName) {
			return input
		}
	}
	normalized := copyMapAny(input)
	normalized = generatedPlanApplyResearchContinuationInput(toolName, capabilities, normalized, researchContinuation)
	if evidenceReader && !searchTool {
		return normalized
	}
	if researchTool {
		operation := strings.TrimSpace(stringValue(normalized["operation"]))
		if (operation == "" || operation == "search" || operation == "search_and_fetch" || operation == "verify_fact" || operation == "research") &&
			len(mapValue(normalized["research_session"])) == 0 {
			// A plan-owned research session makes a later execution unit continue
			// from the discovered frontier instead of replaying the same top hits.
			normalized["research_session"] = map[string]any{"mode": "start"}
		}
	}
	if researchContinuationToolCanExecute(toolName, capabilities, researchContinuation) {
		if researchTool {
			normalized["research_depth"] = step.ResearchDepth
		}
		return normalized
	}
	queries := append([]generatedPlanQuery(nil), step.DiscoveryQueries...)
	if len(queries) == 0 {
		if researchTool {
			normalized["research_depth"] = step.ResearchDepth
		}
		return normalized
	}
	if len(anySliceValue(action["source_receipts"])) > 0 {
		missing := researchMissingQueryLanguages(step, stringValueSlice(action["query_languages"]))
		if len(missing) == 0 {
			if researchTool {
				normalized["research_depth"] = step.ResearchDepth
			}
			return normalized
		}
		missingSet := make(map[string]bool, len(missing))
		for _, language := range missing {
			missingSet[language] = true
		}
		queries = queries[:0]
		for _, query := range step.DiscoveryQueries {
			if missingSet[query.Language] {
				queries = append(queries, query)
			}
		}
	}
	primary := strings.TrimSpace(queries[0].Query)
	normalized["query"] = primary
	variants := make([]string, 0, 6)
	for _, query := range queries[1:] {
		if strings.TrimSpace(query.Query) != "" && strings.TrimSpace(query.Query) != primary {
			variants = append(variants, query.Query)
		}
	}
	variants = uniqueStrings(variants)
	if len(variants) > 0 {
		normalized["query_variants"] = variants
	} else {
		delete(normalized, "query_variants")
	}
	if researchTool {
		normalized["research_depth"] = step.ResearchDepth
	}
	return normalized
}

func generatedPlanApplyResearchContinuationInput(
	toolName string,
	capabilities []string,
	input map[string]any,
	continuation map[string]any,
) map[string]any {
	action := researchContinuationFirstAction(continuation)
	if len(action) == 0 || !researchContinuationToolCanExecute(toolName, capabilities, continuation) {
		return input
	}
	normalized := copyMapAny(input)
	if query := strings.TrimSpace(stringValue(action["query"])); query != "" {
		normalized["query"] = query
		if runtimeCapabilitiesContain(capabilities, "research") {
			normalized["operation"] = "search_and_fetch"
		}
	}
	if sourceURL := strings.TrimSpace(stringValue(action["url"])); sourceURL != "" {
		normalized["url"] = sourceURL
		if runtimeCapabilitiesContain(capabilities, "research") {
			normalized["operation"] = "fetch"
		}
	}
	if runtimeCapabilitiesContain(capabilities, "research") {
		if session := mapValue(continuation["research_session"]); strings.TrimSpace(stringValue(session["id"])) != "" {
			normalized["research_session"] = map[string]any{
				"id": strings.TrimSpace(stringValue(session["id"])), "mode": "continue",
			}
		}
	}
	return normalized
}

func generatedPlanLastToolResult(messages []agentruntime.Message) (string, map[string]any) {
	call, found := generatedPlanLastSettledToolCall(messages)
	if !found {
		return "", nil
	}
	last := messages[len(messages)-1]
	var result map[string]any
	if json.Unmarshal([]byte(last.Content), &result) != nil {
		return strings.TrimSpace(call.Name), nil
	}
	if wrapped := mapValue(result["tool_result"]); len(wrapped) > 0 {
		result = wrapped
	}
	return strings.TrimSpace(call.Name), result
}

func generatedPlanLastSettledToolCall(messages []agentruntime.Message) (agentruntime.ToolCall, bool) {
	if len(messages) == 0 {
		return agentruntime.ToolCall{}, false
	}
	last := messages[len(messages)-1]
	if last.Role != "tool" || strings.TrimSpace(last.ToolCallID) == "" || strings.TrimSpace(last.Content) == "" {
		return agentruntime.ToolCall{}, false
	}
	for index := len(messages) - 2; index >= 0; index-- {
		for _, call := range messages[index].ToolCalls {
			if call.ID == last.ToolCallID {
				return call, true
			}
		}
	}
	return agentruntime.ToolCall{}, false
}

func (g serverAgentRuntimeToolGateway) generatedPlanRequiredToolChoice(
	messages []agentruntime.Message,
	tools []agentruntime.ToolSchema,
) any {
	if g.server == nil {
		return nil
	}
	snapshot, found := g.server.generatedPlanProcessSnapshot(g.sessionID)
	if !found || len(snapshot.actionable) == 0 {
		return nil
	}
	action := snapshot.actionable[0]
	step, found := snapshot.typedStep(stringValue(action["id"]))
	if !found {
		return nil
	}
	status := stringValue(action["status"])
	strictControl := boolValue(snapshot.data["_plan_approved"], false)
	if status == "pending" && strictControl &&
		len(mapValue(snapshot.data["_step_statuses"])) > 0 &&
		agentRuntimeToolSchemaNamed(tools, updateStepStatusToolName) {
		return map[string]any{"type": "tool", "name": updateStepStatusToolName}
	}
	if status != "in_progress" {
		return nil
	}
	if step.Kind == generatedPlanStepKindResearch {
		if continuation := mapValue(action["research_continuation"]); researchContinuationRequiresExecution(continuation) {
			choice := researchContinuationRequiredToolChoice(continuation, tools)
			choiceName := strings.TrimSpace(stringValue(mapValue(choice)["name"]))
			if choiceName != "" && !generatedPlanToolAttemptedAfterLatestStepUpdate(messages, choiceName) {
				return choice
			}
			if strictControl && choiceName != "" && agentRuntimeToolSchemaNamed(tools, updateStepStatusToolName) {
				return map[string]any{"type": "tool", "name": updateStepStatusToolName}
			}
			return nil
		}
	}
	if !strictControl {
		return nil
	}
	if step.Kind == generatedPlanStepKindDelivery {
		savedAfterStart := generatedPlanToolAttemptedAfterLatestStepUpdate(messages, "save_artifacts")
		preparedAfterStart := generatedPlanDeliveryPreparationAttemptedAfterLatestStepUpdate(messages)
		if preparedAfterStart && !savedAfterStart && agentRuntimeToolSchemaNamed(tools, "save_artifacts") {
			return map[string]any{"type": "tool", "name": "save_artifacts"}
		}
		if savedAfterStart && agentRuntimeToolSchemaNamed(tools, updateStepStatusToolName) {
			return map[string]any{"type": "tool", "name": updateStepStatusToolName}
		}
		return nil
	}
	if step.Kind != generatedPlanStepKindResearch {
		return nil
	}
	latestSource := int64(numberValue(snapshot.data[generatedPlanResearchLatestSourceEventIDKey]))
	consumedSource := int64(numberValue(snapshot.data[generatedPlanResearchConsumedSourceEventIDKey]))
	if latestSource > consumedSource && agentRuntimeToolSchemaNamed(tools, updateStepStatusToolName) {
		// The durable source cursor outranks replay shape and every connector
		// preference. Reconcile the exact terminal source event before selecting a
		// follow-up route, even when a later unrelated tool remains last in the
		// compact provider transcript.
		return map[string]any{"type": "tool", "name": updateStepStatusToolName}
	}
	toolName, result := generatedPlanLastToolResult(messages)
	if len(result) == 0 {
		// Provider compaction may remove the settled tool message. The durable
		// plan state remains authoritative, but a plan cannot choose a scientific
		// executor. The outer model selects among the current atomic Tool snapshot.
		return nil
	}
	if normalizeAgentToolName(toolName) != normalizeAgentToolName(updateStepStatusToolName) &&
		g.server.sessionRunnerEvidenceTool(toolName) &&
		agentRuntimeToolSchemaNamed(tools, updateStepStatusToolName) {
		// Bind every completed source result to the active investigation before
		// the provider can drift into synthesis or delivery. The update receipt
		// reconstructs the immutable source event and either closes the module or
		// records its next source-owned continuation.
		return map[string]any{"type": "tool", "name": updateStepStatusToolName}
	}
	return nil
}

func generatedPlanToolAttemptedAfterLatestStepUpdate(
	messages []agentruntime.Message,
	toolName string,
) bool {
	boundary := -1
	calls := runnerToolCallsByCallID(messages)
	for index, message := range messages {
		if message.Role != "tool" || strings.TrimSpace(message.Content) == "" {
			continue
		}
		call, found := calls[message.ToolCallID]
		if !found || normalizeAgentToolName(call.Name) != normalizeAgentToolName(updateStepStatusToolName) {
			continue
		}
		var result any
		if json.Unmarshal([]byte(message.Content), &result) == nil &&
			agentruntime.ClassifyToolResult(result) == agentruntime.ToolResultSucceeded &&
			!agentruntime.IsNonExecutingPreflight(result) {
			boundary = index
		}
	}
	for _, message := range messages[boundary+1:] {
		for _, call := range message.ToolCalls {
			if normalizeAgentToolName(call.Name) == normalizeAgentToolName(toolName) {
				return true
			}
		}
	}
	return false
}

// generatedPlanDeliveryPreparationAttemptedAfterLatestStepUpdate gives the
// delivery model one real authoring turn after the synthesis handoff before a
// snapshot save becomes mandatory. A draft saved during research is not proof
// that the final delivery consumed the completed evidence set.
func generatedPlanDeliveryPreparationAttemptedAfterLatestStepUpdate(
	messages []agentruntime.Message,
) bool {
	for _, toolName := range []string{"edit_file", "python", "r", "repl", "bash"} {
		if generatedPlanToolAttemptedAfterLatestStepUpdate(messages, toolName) {
			return true
		}
	}
	return false
}
