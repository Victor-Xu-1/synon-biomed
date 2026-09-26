package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

type compatibilityInputResponse struct {
	ToolID    string            `json:"tool_id"`
	RequestID string            `json:"requestId"`
	Action    string            `json:"action"`
	Answers   map[string]string `json:"answers"`
	Message   string            `json:"message"`
	Approved  *bool             `json:"approved"`
	Mode      string            `json:"mode"`
	Scope     string            `json:"scope"`
	Redirect  string            `json:"redirect"`
	Text      *string           `json:"text"`
}

type compatibilityResolveInputRequest struct {
	Responses    []compatibilityInputResponse `json:"responses"`
	VerifierMode *string                      `json:"verifier_mode"`
	MemoryMode   *string                      `json:"memory_mode"`
	UltraMode    *bool                        `json:"ultra_mode"`
	PlanMode     *bool                        `json:"plan_mode"`
	TargetAgent  *string                      `json:"target_agent"`
}

type compatibilityResolveInputResult struct {
	Frame        workspace.CompatibilityFrame
	Status       string
	RemainingIDs []string
}

type compatibilityApprovalResolutionAuthority struct {
	Source  string
	ActorID string
}

type compatibilityApprovalResolutionAuthorityContextKey struct{}

func withCompatibilityApprovalResolutionAuthority(
	ctx context.Context,
	authority compatibilityApprovalResolutionAuthority,
) context.Context {
	return context.WithValue(ctx, compatibilityApprovalResolutionAuthorityContextKey{}, authority)
}

func compatibilityApprovalAuthorityFromContext(
	ctx context.Context,
	defaultActorID string,
) compatibilityApprovalResolutionAuthority {
	authority, _ := ctx.Value(compatibilityApprovalResolutionAuthorityContextKey{}).(compatibilityApprovalResolutionAuthority)
	if strings.TrimSpace(authority.Source) == "" {
		authority.Source = "user"
	}
	if strings.TrimSpace(authority.ActorID) == "" {
		authority.ActorID = strings.TrimSpace(defaultActorID)
	}
	return authority
}

type compatibilityResolveInputError struct {
	Status int
	Detail string
}

func (e *compatibilityResolveInputError) Error() string { return e.Detail }

func (s *Server) handleCompatibilityResolveInput(w http.ResponseWriter, r *http.Request, frame workspace.CompatibilityFrame) {
	if r.Method != http.MethodPost {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var input compatibilityResolveInputRequest
	if err := decodeAgentCompatJSON(r, &input); err != nil {
		writeV11Detail(w, http.StatusBadRequest, "Invalid resolve-input request: "+err.Error())
		return
	}
	result, err := s.resolveCompatibilityInput(r, frame, input)
	if err != nil {
		var requestErr *compatibilityResolveInputError
		if errors.As(err, &requestErr) {
			writeV11Detail(w, requestErr.Status, requestErr.Detail)
			return
		}
		writeV11StoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"root_frame_id": result.Frame.RootFrameID, "frame_id": result.Frame.ID,
		"status": result.Status, "race_lost_refusals": []any{},
		"remaining_tool_ids": result.RemainingIDs, "remaining": len(result.RemainingIDs),
	})
}

func (s *Server) resolveCompatibilityInput(
	r *http.Request,
	frame workspace.CompatibilityFrame,
	input compatibilityResolveInputRequest,
) (compatibilityResolveInputResult, error) {
	if len(input.Responses) == 0 {
		return compatibilityResolveInputResult{}, resolveInputRequestError(http.StatusBadRequest,
			"Invalid arguments: responses: Array must contain at least 1 element(s)")
	}
	if err := validateCompatibilityPlanModeValues(input.VerifierMode, input.MemoryMode); err != nil {
		return compatibilityResolveInputResult{}, resolveInputRequestError(http.StatusBadRequest, err.Error())
	}
	found, err := s.validateCompatibilityTargetAgent(r, input.TargetAgent)
	if err != nil {
		return compatibilityResolveInputResult{}, err
	}
	if !found {
		return compatibilityResolveInputResult{}, resolveInputRequestError(http.StatusBadRequest,
			"Agent "+stringPointerValue(input.TargetAgent)+" not found in registry")
	}

	s.compatRequestMu.Lock()
	defer s.compatRequestMu.Unlock()
	current, found, err := s.workspaceStore.GetCompatibilityFrame(frame.ID)
	if err != nil {
		return compatibilityResolveInputResult{}, err
	}
	if !found {
		return compatibilityResolveInputResult{}, resolveInputRequestError(http.StatusNotFound, "Frame "+frame.ID+" not found")
	}
	metadata, _, err := s.workspaceStore.GetFrameRuntimeMetadata(current.ID)
	if err != nil {
		return compatibilityResolveInputResult{}, err
	}
	pending := compatibilityServerPendingInputs(metadata.ContextData)
	if current.Status == "awaiting_user_response" && len(pending) == 0 {
		return compatibilityResolveInputResult{}, resolveInputRequestError(http.StatusBadRequest,
			"Frame has no pending input requests to respond to.")
	}
	pendingByID := make(map[string]map[string]any, len(pending))
	for _, item := range pending {
		if id := compatibilityServerPendingInputID(item); id != "" {
			pendingByID[id] = item
		}
	}
	if result, handled, err := s.resolveKernelCapabilityInstallApprovalInputs(r.Context(), current, pendingByID, input.Responses); handled {
		return result, err
	}
	if result, handled, err := s.resolveKernelArtifactApprovalInputs(r.Context(), current, pendingByID, input.Responses); handled {
		return result, err
	}
	if result, handled, err := s.resolveKernelLocalExecApprovalInputs(r.Context(), current, pendingByID, input.Responses); handled {
		return result, err
	}
	type askUserAuthority struct {
		item   map[string]any
		origin *transcriptstore.AskUserOriginV1
	}
	typedAskUserAuthorities := make(map[string]askUserAuthority, len(input.Responses))
	var resolverScope []transcriptstore.AskUserEvidenceResolverSelection
	seen := map[string]bool{}
	for _, response := range input.Responses {
		id := strings.TrimSpace(firstNonEmpty(response.ToolID, response.RequestID))
		if id == "" {
			return compatibilityResolveInputResult{}, resolveInputRequestError(
				http.StatusBadRequest, "InputResponse requires tool_id or requestId",
			)
		}
		if seen[id] {
			return compatibilityResolveInputResult{}, resolveInputRequestError(
				http.StatusBadRequest, "Duplicate tool_id in batch: "+id+".",
			)
		}
		seen[id] = true
		item, exists := pendingByID[id]
		requireAskUserAuthority := compatibilityServerInputKind(item, exists) == "ask"
		if requireAskUserAuthority && s.transcriptStore == nil {
			return compatibilityResolveInputResult{}, resolveInputRequestError(
				http.StatusServiceUnavailable, "AskUser transcript authority is unavailable.",
			)
		}
		typedItem, typedOrigin, loadErr := s.compatibilityTypedAskUserItem(
			r.Context(), current, metadata.ContextData, id, requireAskUserAuthority,
		)
		if loadErr != nil {
			if errors.Is(loadErr, transcriptstore.ErrEventConflict) ||
				errors.Is(loadErr, transcriptstore.ErrBranchStateStale) ||
				errors.Is(loadErr, transcriptstore.ErrOwnerMismatch) {
				return compatibilityResolveInputResult{}, resolveInputRequestError(
					http.StatusConflict, "AskUser request authority conflicts with durable state.",
				)
			}
			return compatibilityResolveInputResult{}, loadErr
		}
		if typedOrigin != nil {
			typedAskUserAuthorities[id] = askUserAuthority{item: typedItem, origin: typedOrigin}
			action := strings.TrimSpace(response.Action)
			if action == "answer" || (action == "" && len(response.Answers) > 0) {
				for _, resolver := range compatibilitySelectedAskUserEvidenceResolvers(typedItem, response.Answers) {
					resolverScope = append(resolverScope, resolver)
				}
			}
		}
	}
	// Validate the complete response batch before grants or durable answers are
	// written. A conflict leaves every question available for correction.
	if err := transcriptstore.ValidateAskUserEvidenceResolverScope(resolverScope); err != nil {
		return compatibilityResolveInputResult{}, resolveInputRequestError(http.StatusBadRequest, err.Error())
	}
	resolutions := make([]workspace.CompatibilityInputResolution, 0, len(input.Responses))
	grantRollbacks := make([]func() error, 0)
	rollbackGrants := func() error {
		var rollbackErrors []error
		for index := len(grantRollbacks) - 1; index >= 0; index-- {
			if err := grantRollbacks[index](); err != nil {
				rollbackErrors = append(rollbackErrors, err)
			}
		}
		return errors.Join(rollbackErrors...)
	}
	for _, response := range input.Responses {
		id := strings.TrimSpace(firstNonEmpty(response.ToolID, response.RequestID))
		item, exists := pendingByID[id]
		authority := typedAskUserAuthorities[id]
		typedAskUserOrigin := authority.origin
		typedAskUser := typedAskUserOrigin != nil
		if typedAskUser {
			item, exists = authority.item, true
		}
		if !exists {
			if current.Status == "awaiting_user_response" {
				return compatibilityResolveInputResult{}, errors.Join(
					resolveInputRequestError(http.StatusBadRequest, "No pending request with tool_id "+id+"."), rollbackGrants())
			}
			resolutions = append(resolutions, workspace.CompatibilityInputResolution{ToolID: id})
			continue
		}
		content, modelContinuation, isError, askUserResult, rollback, err := s.resolveCompatibilityInputItem(
			r, current, item, response, typedAskUser,
		)
		if err != nil {
			return compatibilityResolveInputResult{}, errors.Join(
				resolveInputRequestError(http.StatusBadRequest, err.Error()), rollbackGrants())
		}
		if rollback != nil {
			grantRollbacks = append(grantRollbacks, rollback)
		}
		resolutions = append(resolutions, workspace.CompatibilityInputResolution{
			ToolID: id, Content: content, ModelContinuation: modelContinuation,
			IsError: isError, AskUserResult: askUserResult, AskUserOrigin: typedAskUserOrigin,
		})
	}
	var resolved workspace.CompatibilityInputResolutionResult
	runtimeConfig := compatibilityResolvedInputRuntimeConfig(current, input)
	if s.transcriptStore != nil {
		resolved, err = s.workspaceStore.ResolveCompatibilityPendingInputsWithTranscriptConfig(
			r.Context(), current.ID, resolutions, runtimeConfig,
		)
	} else {
		resolved, err = s.workspaceStore.ResolveCompatibilityPendingInputs(current.ID, resolutions)
	}
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, transcriptstore.ErrEventConflict) || errors.Is(err, transcriptstore.ErrBranchStateStale) {
			status = http.StatusConflict
		} else if strings.Contains(strings.ToLower(err.Error()), "not found") {
			status = http.StatusNotFound
		}
		return compatibilityResolveInputResult{}, errors.Join(resolveInputRequestError(status, err.Error()), rollbackGrants())
	}
	if !resolved.AlreadyResolved {
		if err := s.publishWorkspaceEvent(resolved.Event); err != nil {
			return compatibilityResolveInputResult{}, err
		}
		if err := s.prepareCompatibilityResolvedInputRuntime(resolved.Frame, resolved.Status, runtimeConfig); err != nil {
			return compatibilityResolveInputResult{}, err
		}
		if resolved.DispatchEvent != nil {
			if err := s.registerFrameResumeDispatch(resolved.DispatchEvent.ID, defaultFrameResumeReservationTTL); err != nil {
				return compatibilityResolveInputResult{}, fmt.Errorf("input resolved but dispatch registration failed: %w", err)
			}
			if err := s.publishWorkspaceEvent(*resolved.DispatchEvent); err != nil {
				return compatibilityResolveInputResult{}, fmt.Errorf("input resolved but dispatch delivery failed: %w", err)
			}
		}
	}
	resolvedIDs := make([]string, 0, len(input.Responses))
	for _, response := range input.Responses {
		if id := strings.TrimSpace(firstNonEmpty(response.ToolID, response.RequestID)); id != "" {
			resolvedIDs = append(resolvedIDs, id)
		}
	}
	if err := s.publishWebConfirmationRemovals(resolved.Frame.ID, resolvedIDs, resolved.Status); err != nil {
		return compatibilityResolveInputResult{}, fmt.Errorf("input resolved but confirmation removal delivery failed: %w", err)
	}
	return compatibilityResolveInputResult{
		Frame: resolved.Frame, Status: resolved.Status, RemainingIDs: append([]string(nil), resolved.RemainingIDs...),
	}, nil
}

func (s *Server) publishWebConfirmationRemovals(frameID string, ids []string, status string) error {
	frameContext, found, err := s.workspaceStore.GetFrameRealtimeContext(frameID)
	if err != nil || !found {
		return err
	}
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		eventID := "web-confirmation-remove:" + frameID + ":" + webEventComponent(id) + ":" + webEventComponent(status)
		if err := s.publishWebFrameEvent(frameContext, eventID, "confirmation.remove", map[string]any{
			"conversation_id": frameID, "id": id,
		}); err != nil {
			return err
		}
	}
	return nil
}

func resolveInputRequestError(status int, detail string) error {
	return &compatibilityResolveInputError{Status: status, Detail: detail}
}

func validateCompatibilityPlanModeValues(verifier, memory *string) error {
	for name, value := range map[string]*string{"verifier_mode": verifier, "memory_mode": memory} {
		if value != nil && *value != "off" && *value != "on" {
			return fmt.Errorf("%s must be off or on", name)
		}
	}
	return nil
}

func compatibilityServerPendingInputID(item map[string]any) string {
	return strings.TrimSpace(firstNonEmpty(stringValue(item["tool_id"]), stringValue(item["requestId"])))
}

func compatibilityServerPendingInputs(contextData map[string]any) []map[string]any {
	result := make([]map[string]any, 0)
	if values, ok := contextData["_pending_input_requests"].([]any); ok {
		for _, value := range values {
			if item, ok := value.(map[string]any); ok {
				result = append(result, item)
			}
		}
	}
	if len(result) == 0 {
		for _, key := range []string{"_ask_user_payload", "_pending_access_request"} {
			if item, ok := contextData[key].(map[string]any); ok {
				result = append(result, item)
			}
		}
	}
	return result
}

func (s *Server) compatibilityTypedAskUserItem(
	ctx context.Context,
	frame workspace.CompatibilityFrame,
	contextData map[string]any,
	toolID string,
	required bool,
) (map[string]any, *transcriptstore.AskUserOriginV1, error) {
	if s.transcriptStore == nil {
		return nil, nil, nil
	}
	rawOrigins, present := contextData["_ask_user_transcript_origins"]
	rawTypedIDs, typedIDsPresent := contextData["_ask_user_transcript_tool_ids"]
	if !present && !typedIDsPresent {
		if required {
			return nil, nil, transcriptstore.ErrEventConflict
		}
		return nil, nil, nil
	}
	if !present || !typedIDsPresent {
		return nil, nil, transcriptstore.ErrEventConflict
	}
	origins, ok := rawOrigins.(map[string]any)
	if !ok {
		return nil, nil, transcriptstore.ErrEventConflict
	}
	typedValues, ok := rawTypedIDs.([]any)
	if !ok {
		return nil, nil, transcriptstore.ErrEventConflict
	}
	typedIDs := map[string]bool{}
	for _, value := range typedValues {
		id := strings.TrimSpace(stringValue(value))
		if id == "" || typedIDs[id] {
			return nil, nil, transcriptstore.ErrEventConflict
		}
		typedIDs[id] = true
	}
	if len(typedIDs) != len(origins) {
		return nil, nil, transcriptstore.ErrEventConflict
	}
	for id := range origins {
		if !typedIDs[id] {
			return nil, nil, transcriptstore.ErrEventConflict
		}
	}
	rawOrigin, found := origins[toolID]
	if !found {
		if required || typedIDs[toolID] {
			return nil, nil, transcriptstore.ErrEventConflict
		}
		return nil, nil, nil
	}
	encodedOrigin, err := json.Marshal(rawOrigin)
	if err != nil {
		return nil, nil, err
	}
	origin, err := transcriptstore.DecodeAskUserOriginV1(encodedOrigin)
	if err != nil || origin.ToolUseID != toolID || origin.FrameID != frame.ID {
		return nil, nil, transcriptstore.ErrEventConflict
	}
	ownerID, found, err := s.workspaceStore.ProjectOwnerID(frame.ProjectID)
	if err != nil {
		return nil, nil, err
	}
	if !found || strings.TrimSpace(ownerID) == "" {
		return nil, nil, transcriptstore.ErrEventConflict
	}
	prompt, err := s.transcriptStore.GetFrameAskUserPrompt(ctx, transcriptstore.GetFrameAskUserPromptInput{
		OwnerID: ownerID, FrameID: frame.ID, Origin: origin,
	})
	if err != nil {
		return nil, nil, err
	}
	encodedQuestions, err := json.Marshal(prompt.Questions)
	if err != nil {
		return nil, nil, err
	}
	questions := []any{}
	if json.Unmarshal(encodedQuestions, &questions) != nil || len(questions) == 0 {
		return nil, nil, transcriptstore.ErrEventConflict
	}
	return map[string]any{
		"tool_id": toolID, "requestId": toolID, "kind": "ask",
		"tool_name": prompt.ToolName, "questions": questions,
	}, &origin, nil
}

func compatibilityServerInputKind(item map[string]any, exists bool) string {
	if !exists {
		return ""
	}
	kind := strings.TrimSpace(stringValue(item["kind"]))
	if kind == "" {
		return "ask"
	}
	return kind
}

func (s *Server) resolveCompatibilityInputItem(
	r *http.Request,
	frame workspace.CompatibilityFrame,
	item map[string]any,
	response compatibilityInputResponse,
	typedAskUser bool,
) (string, string, bool, *transcriptstore.AskUserResultV1, func() error, error) {
	kind := strings.TrimSpace(stringValue(item["kind"]))
	if kind == "" {
		kind = "ask"
	}
	if kind == "ask" {
		if !typedAskUser {
			content, err := compatibilityAskInputContent(item, response)
			return content, "", false, nil, nil, err
		}
		content, continuation, result, err := compatibilityAskInputResult(item, response)
		if err == nil && result != nil && string(result.Status) == "answered" {
			continuation, err = s.reconcileAnsweredAskUserSelection(item, result.Answers, continuation)
		}
		return content, continuation, false, result, nil, err
	}
	if kind == agentToolApprovalKind {
		content, isError, err := s.resolveCompatibilityAgentToolApproval(r.Context(), item, response)
		return content, "", isError, nil, nil, err
	}
	content, approved, scope, mode, err := compatibilityApprovalResolution(response)
	if err != nil {
		return "", "", false, nil, nil, err
	}
	var rollback func() error
	if approved && scope != "once" {
		rollback, err = s.persistCompatibilityInputGrant(r, frame, item, scope, mode)
		if err != nil {
			return "", "", false, nil, nil, err
		}
	}
	return content, "", !approved, nil, rollback, nil
}

func (s *Server) resolveCompatibilityAgentToolApproval(
	ctx context.Context,
	item map[string]any,
	response compatibilityInputResponse,
) (string, bool, error) {
	_, approved, scope, _, err := compatibilityApprovalResolution(response)
	if err != nil {
		return "", false, err
	}
	if !approved {
		// A denial grants no authority, so its scope has no persistence
		// semantics. The frontend intentionally omits scope on deny; normalize
		// the generic parser's conversation default to the one-shot boundary.
		scope = "once"
	}
	if approved && scope != "once" && scope != "always" {
		return "", false, errors.New("agent tool approval scope must be once or always")
	}
	approvalID := strings.TrimSpace(firstNonEmpty(stringValue(item["approval_id"]), compatibilityServerPendingInputID(item)))
	if approvalID == "" || s == nil || s.runtimeStore == nil {
		return "", false, errors.New("agent tool approval authority is unavailable")
	}
	entry, found, err := s.runtimeStore.Get(agentRuntimeApprovalNamespace, approvalID)
	if err != nil || !found {
		if err != nil {
			return "", false, err
		}
		return "", false, errors.New("agent tool approval request is unavailable")
	}
	value := mapValue(entry.Value)
	// Older MCP approvals omitted their session binding. Only the verified
	// pending card's exact durable call may restore that missing provenance.
	if stringValue(value["sessionId"]) == "" && stringValue(item["frame_id"]) != "" {
		value, err = s.bindAgentToolApprovalFrame(ctx, approvalID, stringValue(item["frame_id"]))
		if err != nil {
			return "", false, err
		}
	}
	status := agentRuntimeApprovalStatus(stringValue(value["status"]), value["result"])
	var resolution any
	if status == "pending" {
		message := map[string]any{
			"type": "agent_runtime_approval_response", "approvalId": approvalID, "approve": approved,
			"remember": approved && scope == "always",
			"reason":   strings.TrimSpace(response.Message),
		}
		resolution, err = s.resolveAgentRuntimeApprovalMessage(ctx, "agent-runtime", message)
		if err != nil {
			latest, latestFound, latestErr := s.runtimeStore.Get(agentRuntimeApprovalNamespace, approvalID)
			if latestErr != nil || !latestFound || strings.TrimSpace(stringValue(mapValue(latest.Value)["status"])) != "failed" {
				return "", false, err
			}
			value = mapValue(latest.Value)
			resolution = map[string]any{
				"success": false, "approvalId": approvalID, "status": "failed",
				"tool": value["tool"], "error": value["error"], "result": value["result"],
			}
		}
	} else if status == "approved" || status == "completed" || status == "denied" || status == "failed" || status == "blocked" {
		resolution = map[string]any{
			"success": status == "approved" || status == "completed", "approvalId": approvalID, "status": status,
			"tool": value["tool"], "error": value["error"], "result": value["result"],
		}
	} else {
		return "", false, errors.New("agent tool approval is still settling")
	}
	encoded, err := json.Marshal(resolution)
	if err != nil {
		return "", false, err
	}
	resolvedStatus := strings.ToLower(strings.TrimSpace(stringValue(mapValue(resolution)["status"])))
	return string(encoded), resolvedStatus != "approved" && resolvedStatus != "completed", nil
}

func compatibilityApprovalResolution(response compatibilityInputResponse) (string, bool, string, string, error) {
	action := strings.TrimSpace(response.Action)
	approved := response.Approved != nil && *response.Approved
	switch action {
	case "allow", "allow_once", "allow_always":
		approved = true
	case "deny", "cancel":
		approved = false
	case "":
		if response.Approved == nil {
			return "", false, "", "", errors.New("approval response requires action or approved")
		}
	default:
		return "", false, "", "", fmt.Errorf("unsupported approval action %q", action)
	}
	scope := strings.TrimSpace(response.Scope)
	switch {
	case action == "allow_always":
		scope = "always"
	case action == "allow_once":
		scope = "once"
	case action == "allow" && scope == "":
		scope = "once"
	case scope == "":
		scope = "conversation"
	}
	switch scope {
	case "once", "conversation", "project", "always":
	default:
		return "", false, "", "", fmt.Errorf("unsupported approval scope %q", scope)
	}
	mode := strings.TrimSpace(response.Mode)
	if mode != "" && mode != "ro" && mode != "rw" {
		return "", false, "", "", fmt.Errorf("unsupported approval mode %q", mode)
	}
	payload := map[string]any{"approved": approved, "scope": scope}
	if mode != "" {
		payload["mode"] = mode
	}
	if response.Redirect != "" {
		payload["redirect"] = response.Redirect
	}
	if response.Text != nil {
		payload["text"] = *response.Text
	}
	raw, err := json.Marshal(payload)
	return string(raw), approved, scope, mode, err
}

func compatibilityAskInputContent(item map[string]any, response compatibilityInputResponse) (string, error) {
	_, continuation, _, err := compatibilityAskInputResult(item, response)
	return continuation, err
}

func compatibilityAskInputResult(
	item map[string]any,
	response compatibilityInputResponse,
) (string, string, *transcriptstore.AskUserResultV1, error) {
	action := strings.TrimSpace(response.Action)
	if action == "" && len(response.Answers) > 0 {
		action = "answer"
	}
	var continuation string
	switch action {
	case "decide_for_me":
		delegatedImplementations, resolved := compatibilityDelegatedAskUserSelection(item)
		if !resolved {
			continuation = "User delegated this choice. Decide only within the unchanged canonical task: preserve every explicit priority and requirement, do not turn a suggested preference into a new hard constraint, and choose the option that best satisfies the canonical objective."
			break
		}
		var err error
		continuation, err = transcriptstore.EncodeDelegatedAskUserModelContinuation(delegatedImplementations)
		if err != nil {
			return "", "", nil, err
		}
	case "discuss":
		message := strings.TrimSpace(response.Message)
		if message == "" {
			return "", "", nil, errors.New("Message is required for 'discuss' action")
		}
		continuation = "The user wants to discuss these questions further. Their message: " + message + ". Respond to their input, then use ask_user again if you still need answers."
	case "cancel":
		continuation = "User cancelled the question. Continue without an answer — use your best judgment or skip this step."
	case "answer":
		questions := compatibilityPendingQuestions(item)
		for _, question := range questions {
			if _, found := response.Answers[question]; !found {
				return "", "", nil, fmt.Errorf("Missing answers for: %s", question)
			}
		}
		var err error
		continuation, err = compatibilityAnsweredAskUserContinuationWithEvidenceResolvers(
			item, response.Answers,
			compatibilitySelectedAskUserImplementations(item, response.Answers),
			compatibilitySelectedAskUserEvidenceResolvers(item, response.Answers),
		)
		if err != nil {
			return "", "", nil, err
		}
	default:
		return "", "", nil, fmt.Errorf("unsupported ask_user action %q", action)
	}
	result, err := transcriptstore.NewAskUserResultV1(
		transcriptstore.AskUserAction(action), response.Answers, response.Message,
	)
	if err != nil {
		return "", "", nil, err
	}
	encoded, err := transcriptstore.EncodeAskUserResultV1(result)
	if err != nil {
		return "", "", nil, err
	}
	return string(encoded), continuation, &result, nil
}

// compatibilityAnsweredAskUserContinuation keeps the selected visible option
// as audit evidence while granting parameter authority only to text entered
// directly by the user outside the model-authored closed options.
func compatibilityAnsweredAskUserContinuation(
	item map[string]any,
	answers map[string]string,
	implementations map[string]string,
) (string, error) {
	return compatibilityAnsweredAskUserContinuationWithEvidenceResolvers(item, answers, implementations, nil)
}

func compatibilityAnsweredAskUserContinuationWithEvidenceResolvers(
	item map[string]any,
	answers map[string]string,
	implementations map[string]string,
	evidenceResolvers map[string]transcriptstore.AskUserEvidenceResolverSelection,
) (string, error) {
	encoded, err := transcriptstore.EncodeAnsweredAskUserModelContinuationWithEvidenceResolvers(
		answers, implementations, evidenceResolvers,
	)
	if err != nil {
		return "", err
	}
	evidence := compatibilitySelectedAskUserEvidence(item, answers)
	parameterEvidence := compatibilityDirectAskUserParameterEvidence(item, answers)
	if len(evidence) == 0 && len(parameterEvidence) == 0 {
		return encoded, nil
	}
	var continuation map[string]any
	if err := json.Unmarshal([]byte(encoded), &continuation); err != nil {
		return "", err
	}
	if len(evidence) > 0 {
		continuation["evidence"] = evidence
	}
	if len(parameterEvidence) > 0 {
		continuation["parameter_evidence"] = parameterEvidence
	}
	raw, err := json.Marshal(continuation)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// compatibilityDirectAskUserParameterEvidence admits only text the user typed
// outside the model-authored closed options. Selecting an option remains a
// durable decision and implementation identity, but its label and prose cannot
// become authority for controlled scientific parameters.
func compatibilityDirectAskUserParameterEvidence(item map[string]any, answers map[string]string) map[string]string {
	questions := make([]map[string]any, 0, 1)
	if question := strings.TrimSpace(stringValue(item["question"])); question != "" {
		questions = append(questions, map[string]any{"question": question, "options": item["options"]})
	}
	for _, raw := range anySliceValue(item["questions"]) {
		if question := mapValue(raw); question != nil {
			questions = append(questions, question)
		}
	}
	evidence := map[string]string{}
	for _, question := range questions {
		questionText := strings.TrimSpace(stringValue(question["question"]))
		answer := strings.TrimSpace(answers[questionText])
		if questionText == "" || answer == "" {
			continue
		}
		closedOption := false
		for _, rawOption := range anySliceValue(question["options"]) {
			if option := mapValue(rawOption); option != nil && strings.TrimSpace(stringValue(option["label"])) == answer {
				closedOption = true
				break
			}
		}
		if !closedOption {
			evidence[questionText] = answer
		}
	}
	return evidence
}

func compatibilitySelectedAskUserEvidence(item map[string]any, answers map[string]string) map[string]string {
	questions := make([]map[string]any, 0, 1)
	if question := strings.TrimSpace(stringValue(item["question"])); question != "" {
		questions = append(questions, map[string]any{"question": question, "options": item["options"]})
	}
	for _, raw := range anySliceValue(item["questions"]) {
		if question := mapValue(raw); question != nil {
			questions = append(questions, question)
		}
	}
	evidence := map[string]string{}
	for _, question := range questions {
		questionText := strings.TrimSpace(stringValue(question["question"]))
		answer := strings.TrimSpace(answers[questionText])
		if questionText == "" || answer == "" {
			continue
		}
		selectedText := ""
		for _, rawOption := range anySliceValue(question["options"]) {
			option := mapValue(rawOption)
			if option == nil || strings.TrimSpace(stringValue(option["label"])) != answer {
				continue
			}
			parts := make([]string, 0, 4)
			for _, key := range []string{"label", "description", "pros", "cons"} {
				if value := strings.TrimSpace(stringValue(option[key])); value != "" {
					parts = append(parts, value)
				}
			}
			selectedText = strings.Join(parts, "\n")
			break
		}
		if selectedText == "" {
			selectedText = answer
		}
		evidence[questionText] = selectedText
	}
	return evidence
}

func compatibilityPendingQuestions(item map[string]any) []string {
	result := make([]string, 0)
	if question := strings.TrimSpace(stringValue(item["question"])); question != "" {
		result = append(result, question)
	}
	switch values := item["questions"].(type) {
	case []any:
		for _, value := range values {
			switch typed := value.(type) {
			case string:
				if question := strings.TrimSpace(typed); question != "" {
					result = append(result, question)
				}
			case map[string]any:
				if question := strings.TrimSpace(stringValue(typed["question"])); question != "" {
					result = append(result, question)
				}
			}
		}
	}
	sort.Strings(result)
	return result
}

// compatibilityDelegatedAskUserSelection records only the implementation
// identity chosen under delegation. Model-authored labels and descriptions are
// deliberately excluded so they cannot become resolved user-input evidence.
func compatibilityDelegatedAskUserSelection(item map[string]any) (map[string]string, bool) {
	questions := make([]map[string]any, 0, 1)
	if question := strings.TrimSpace(stringValue(item["question"])); question != "" {
		questions = append(questions, map[string]any{"question": question, "options": item["options"]})
	}
	for _, raw := range anySliceValue(item["questions"]) {
		if question := mapValue(raw); question != nil {
			questions = append(questions, question)
		}
	}
	if len(questions) == 0 {
		return nil, false
	}

	implementations := make(map[string]string, len(questions))
	for _, question := range questions {
		questionText := strings.TrimSpace(stringValue(question["question"]))
		options := anySliceValue(question["options"])
		if questionText == "" || len(options) == 0 {
			return nil, false
		}
		if _, duplicate := implementations[questionText]; duplicate {
			return nil, false
		}

		selected := map[string]any(nil)
		for _, recommendationKey := range []string{"recommended", "reported_recommended"} {
			for _, rawOption := range options {
				option := mapValue(rawOption)
				metadata := mapValue(option["metadata"])
				if option != nil && (boolValue(metadata[recommendationKey], false) || boolValue(option[recommendationKey], false)) {
					selected = option
					break
				}
			}
			if selected != nil {
				break
			}
		}
		if selected == nil {
			selected = mapValue(options[0])
		}
		implementation := strings.TrimSpace(stringValue(mapValue(selected["metadata"])["implementation"]))
		if implementation == "" {
			implementation = strings.TrimSpace(stringValue(selected["implementation"]))
		}
		if implementation == "" {
			return nil, false
		}
		implementations[questionText] = implementation
	}
	return implementations, true
}

func compatibilitySelectedAskUserImplementations(item map[string]any, answers map[string]string) map[string]string {
	selected := map[string]string{}
	questions := make([]map[string]any, 0, 1)
	if question := strings.TrimSpace(stringValue(item["question"])); question != "" {
		questions = append(questions, map[string]any{"question": question, "options": item["options"]})
	}
	for _, raw := range anySliceValue(item["questions"]) {
		if question := mapValue(raw); question != nil {
			questions = append(questions, question)
		}
	}
	for _, question := range questions {
		questionText := strings.TrimSpace(stringValue(question["question"]))
		answer := strings.TrimSpace(answers[questionText])
		if questionText == "" || answer == "" {
			continue
		}
		for _, rawOption := range anySliceValue(question["options"]) {
			option := mapValue(rawOption)
			if option == nil || strings.TrimSpace(stringValue(option["label"])) != answer {
				continue
			}
			implementation := strings.TrimSpace(stringValue(mapValue(option["metadata"])["implementation"]))
			if implementation == "" {
				implementation = strings.TrimSpace(stringValue(option["implementation"]))
			}
			if implementation != "" {
				selected[questionText] = implementation
			}
			break
		}
	}
	if len(selected) == 0 {
		return nil
	}
	return selected
}

func compatibilitySelectedAskUserEvidenceResolvers(
	item map[string]any,
	answers map[string]string,
) map[string]transcriptstore.AskUserEvidenceResolverSelection {
	selected := map[string]transcriptstore.AskUserEvidenceResolverSelection{}
	questions := make([]map[string]any, 0, 1)
	if question := strings.TrimSpace(stringValue(item["question"])); question != "" {
		questions = append(questions, map[string]any{"question": question, "options": item["options"]})
	}
	for _, raw := range anySliceValue(item["questions"]) {
		if question := mapValue(raw); question != nil {
			questions = append(questions, question)
		}
	}
	for _, question := range questions {
		questionText := strings.TrimSpace(stringValue(question["question"]))
		answer := strings.TrimSpace(answers[questionText])
		for _, rawOption := range anySliceValue(question["options"]) {
			option := mapValue(rawOption)
			if questionText == "" || answer == "" || option == nil || strings.TrimSpace(stringValue(option["label"])) != answer {
				continue
			}
			resolver := mapValue(mapValue(option["metadata"])["evidence_resolver"])
			selection := transcriptstore.AskUserEvidenceResolverSelection{
				EvidenceGroup:  strings.TrimSpace(stringValue(resolver["evidence_group"])),
				Skill:          strings.TrimSpace(stringValue(resolver["skill"])),
				Implementation: strings.TrimSpace(stringValue(resolver["implementation"])),
			}
			if selection.EvidenceGroup != "" && selection.Skill != "" && selection.Implementation != "" {
				selected[questionText] = selection
			}
			break
		}
	}
	if len(selected) == 0 {
		return nil
	}
	return selected
}

func (s *Server) persistCompatibilityInputGrant(
	r *http.Request,
	frame workspace.CompatibilityFrame,
	item map[string]any,
	scope, mode string,
) (func() error, error) {
	kind := strings.TrimSpace(stringValue(item["kind"]))
	target := strings.TrimSpace(firstNonEmpty(
		stringValue(item["target"]), stringValue(item["key"]), stringValue(item["domain"]),
		stringValue(item["path"]), stringValue(item["host"]),
	))
	userID := compatAgentUserID(r)
	if s.settingsStore == nil {
		return nil, errors.New("settings store is not configured")
	}
	grantKey := "compatibility.inputGrants"
	record := map[string]any{
		"kind": kind, "target": target, "scope": scope, "mode": mode,
		"user_id": userID, "root_frame_id": frame.RootFrameID,
		"project_id": frame.ProjectID, "granted_at": time.Now().UTC(),
	}
	previousSetting, previousFound, err := s.settingsStore.Get(grantKey)
	if err != nil {
		return nil, err
	}
	_, err = s.settingsStore.Update(grantKey, func(current any, found bool) (any, error) {
		values := make([]any, 0)
		if found {
			if existing, ok := current.([]any); ok {
				values = append(values, existing...)
			}
		}
		values = append(values, record)
		return values, nil
	})
	if err != nil {
		return nil, err
	}
	rollback := func() error {
		if previousFound {
			_, err := s.settingsStore.Set(grantKey, previousSetting.Value)
			return err
		}
		_, err := s.settingsStore.Set(grantKey, []any{})
		return err
	}
	switch kind {
	case "network":
		domain, err := normalizeAllowedDomain(target)
		if err != nil {
			return nil, errors.Join(err, rollback())
		}
		previous, _ := s.loadAllowedDomains()
		_, err = s.settingsStore.Update(allowedDomainsSettingKey, func(current any, found bool) (any, error) {
			return normalizeAllowedDomains(append(settingStringSlice(current), domain))
		})
		if err != nil {
			return nil, errors.Join(err, rollback())
		}
		priorRollback := rollback
		rollback = func() error {
			_, err := s.settingsStore.Set(allowedDomainsSettingKey, previous)
			return errors.Join(err, priorRollback())
		}
	case "host":
		hostMode := "read"
		if mode == "rw" {
			hostMode = "read_write"
		}
		_, receipt, err := s.upsertHostGrantWithReceipt(userID, target, hostMode)
		if err != nil {
			return nil, errors.Join(err, rollback())
		}
		priorRollback := rollback
		rollback = func() error {
			return errors.Join(s.rollbackHostGrantMutation(receipt), priorRollback())
		}
	case "mcp_tool":
		serverID := strings.TrimSpace(stringValue(item["server_id"]))
		toolName := strings.TrimSpace(stringValue(item["tool_name"]))
		if serverID == "" || toolName == "" {
			return nil, errors.Join(errors.New("mcp_tool approval is missing server_id or tool_name"), rollback())
		}
		grantID := serverID + ":" + userID + ":" + toolName
		previousEnabled := false
		grants, err := s.workspaceStore.ListMCPToolGrants(serverID, userID)
		if err != nil {
			return nil, errors.Join(err, rollback())
		}
		for _, grant := range grants {
			if grant.AgentName == frame.AgentName && grant.ToolName == toolName {
				previousEnabled = grant.Enabled
				grantID = grant.ID
				break
			}
		}
		_, err = s.workspaceStore.SetMCPToolGrant(workspace.MCPToolGrantInput{
			ID: grantID, MCPServerID: serverID,
			UserID: userID, AgentName: frame.AgentName, ToolName: toolName, Enabled: true,
		})
		if err != nil {
			return nil, errors.Join(err, rollback())
		}
		priorRollback := rollback
		rollback = func() error {
			_, err := s.workspaceStore.SetMCPToolGrant(workspace.MCPToolGrantInput{
				ID: grantID, MCPServerID: serverID, UserID: userID,
				AgentName: frame.AgentName, ToolName: toolName, Enabled: previousEnabled,
			})
			return errors.Join(err, priorRollback())
		}
	}
	return rollback, nil
}

func (s *Server) prepareCompatibilityResolvedInputRuntime(
	frame workspace.CompatibilityFrame,
	status string,
	config map[string]any,
) error {
	if s.transcriptStore != nil {
		return nil
	}
	var entries []eventjournal.Entry
	page, err := s.workspaceStore.CompatibilityFrameMessages(frame.ID, 0, 100000)
	if err != nil {
		return err
	}
	messages := make([]eventjournal.Message, 0, len(page.Messages))
	for _, source := range page.Messages {
		message := eventjournal.Message{}
		for key, value := range source {
			message[key] = value
		}
		messages = append(messages, message)
	}
	entries, err = s.eventJournal.ReplaceSession(frame.ID, messages)
	if err != nil {
		return err
	}
	project, found, err := s.workspaceStore.GetProject(frame.ProjectID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("project %s not found", frame.ProjectID)
	}
	session, found, err := s.sessionStore.Get(frame.ID)
	if err != nil {
		return err
	}
	if !found {
		session = sessionstore.Session{ID: frame.ID, Title: frame.Name, WorkDir: s.fileRoot, CreatedAt: frame.CreatedAt}
	}
	session = sessionWithJournalStats(session, entries)
	session.Project = &sessionstore.Project{ID: project.ID, Name: project.Name, Path: project.Path, BoundAt: time.Now().UTC()}
	orchestration := make(map[string]any, len(session.Orchestration)+2)
	for key, value := range session.Orchestration {
		orchestration[key] = value
	}
	orchestration["sessionConfig"] = config
	orchestration["inputResolutionStatus"] = status
	session.Orchestration = orchestration
	if status == "partial" {
		session.LastRole = "system"
	}
	session.Runner = nil
	if err := s.sessionStore.Save(session); err != nil {
		return err
	}
	if len(entries) > 0 {
		entry := entries[len(entries)-1]
		s.publishSessionEntry(&entry)
	}
	return nil
}

func compatibilityResolvedInputRuntimeConfig(
	frame workspace.CompatibilityFrame,
	input compatibilityResolveInputRequest,
) map[string]any {
	config := map[string]any{"agentName": frame.AgentName}
	if input.TargetAgent != nil {
		config["agentName"] = *input.TargetAgent
	}
	for key, value := range map[string]any{
		"verifier_mode": input.VerifierMode, "memory_mode": input.MemoryMode,
		"ultra_mode": input.UltraMode, "plan_mode": input.PlanMode,
	} {
		switch typed := value.(type) {
		case *string:
			if typed != nil {
				config[key] = *typed
			}
		case *bool:
			if typed != nil {
				config[key] = *typed
			}
		}
	}
	return config
}
