package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"synon-go/internal/synonlink"

	workspace "synon-go/internal/persistence/workspace"
)

const webApprovalRememberedClient = "synon-web"

func (s *Server) handleWebConversationSlashCommands(w http.ResponseWriter, r *http.Request, frame workspace.CompatibilityFrame) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	commands, err := s.webAgentSlashCommands(strings.TrimSpace(r.Header.Get("X-Synon-User-Id")), frame.AgentName)
	if err != nil {
		writeWebConversationError(w, err)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, commands)
}

func (s *Server) handleWebConversationConfirmations(w http.ResponseWriter, r *http.Request, frame workspace.CompatibilityFrame) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	mode, err := s.webSessionApprovalMode(frame.ID)
	if err != nil {
		writeWebConversationError(w, err)
		return
	}
	if mode == "allow" || mode == "smart" || mode == "deny" {
		userID := strings.TrimSpace(r.Header.Get("X-Synon-User-Id"))
		if userID == "" {
			if frameContext, found, contextErr := s.workspaceStore.GetFrameRealtimeContext(frame.ID); contextErr != nil {
				writeWebConversationError(w, contextErr)
				return
			} else if found {
				userID = strings.TrimSpace(frameContext.UserID)
			}
		}
		if err := s.reconcileWebConversationPendingTree(r.Context(), userID, frame, mode); err != nil {
			writeWebConversationError(w, err)
			return
		}
	}
	confirmations, err := s.webConversationPendingConfirmationsContext(r.Context(), frame.ID)
	if err != nil {
		writeWebConversationError(w, err)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, confirmations)
}

func (s *Server) webConversationPendingConfirmations(frameID string) ([]map[string]any, error) {
	return s.webConversationPendingConfirmationsContext(context.Background(), frameID)
}

func (s *Server) webConversationPendingConfirmationsContext(
	ctx context.Context,
	frameID string,
) ([]map[string]any, error) {
	metadata, found, err := s.workspaceStore.GetFrameRuntimeMetadata(frameID)
	if err != nil {
		return nil, err
	}
	if !found {
		return []map[string]any{}, nil
	}
	pending := compatibilityServerPendingInputs(metadata.ContextData)
	confirmations := make([]map[string]any, 0, len(pending))
	for _, item := range pending {
		if confirmation, ok := webPendingConfirmation(item); ok {
			confirmations = append(confirmations, confirmation)
		}
	}
	sort.Slice(confirmations, func(i, j int) bool {
		return webString(confirmations[i]["id"]) < webString(confirmations[j]["id"])
	})
	return confirmations, nil
}

func webPendingConfirmation(item map[string]any) (map[string]any, bool) {
	id := compatibilityServerPendingInputID(item)
	if id == "" {
		return nil, false
	}
	kind := strings.ToLower(strings.TrimSpace(webString(item["kind"])))
	if kind == "" {
		kind = "ask"
	}
	action := webConfirmationAction(item)
	description := webConfirmationDescription(item, kind)
	title := strings.TrimSpace(firstNonEmpty(webString(item["title"]), webString(item["name"])))
	if title == "" {
		switch kind {
		case "ask":
			title = "Agent question"
		case "network":
			title = "Network access request"
		case "host":
			title = "Host access request"
		case "mcp_tool":
			title = "MCP tool approval"
		case "local_exec":
			title = "Local execution approval"
		default:
			title = "Permission request"
		}
	}
	options := []map[string]any{}
	if kind == "ask" {
		options = append(options,
			map[string]any{"label": "Decide for me", "value": "decide_for_me"},
			map[string]any{"label": "Cancel question", "value": "cancel"},
		)
		if choices := webPendingChoiceOptions(item); len(choices) > 0 {
			options = choices
		}
	} else if kind == "artifact_delete" || kind == "capability_install" || kind == agentToolApprovalKind {
		options = append(options,
			map[string]any{"label": "Allow once", "value": "proceed_once"},
			map[string]any{"label": "Deny", "value": "deny"},
		)
	} else {
		options = append(options,
			map[string]any{"label": "Allow once", "value": "proceed_once"},
			map[string]any{"label": "Always allow", "value": "proceed_always"},
			map[string]any{"label": "Deny", "value": "deny"},
		)
	}
	confirmation := map[string]any{
		"id": id, "call_id": id, "title": title, "description": description,
		"kind": kind, "action": action, "options": options,
	}
	for _, key := range []string{
		"tool", "tool_name", "environment", "code", "working_dir", "background", "fresh",
		"items", "total_bytes", "reason", "target", "rememberable",
	} {
		if value, found := item[key]; found {
			confirmation[key] = value
		}
	}
	if commandType := webConfirmationCommandType(item); commandType != "" {
		confirmation["command_type"] = commandType
	}
	return confirmation, true
}

func webConfirmationDescription(item map[string]any, kind string) string {
	if description := strings.TrimSpace(firstNonEmpty(webString(item["description"]), webString(item["message"]))); description != "" {
		return description
	}
	if kind == "ask" {
		questions := compatibilityPendingQuestions(item)
		if len(questions) > 0 {
			return strings.Join(questions, "\n")
		}
	}
	target := strings.TrimSpace(firstNonEmpty(
		webString(item["target"]), webString(item["domain"]), webString(item["path"]),
		webString(item["host"]), webString(item["tool_name"]),
	))
	if target != "" {
		return "Allow " + kind + " access to " + target + "?"
	}
	return "The agent requires permission to continue."
}

func webConfirmationAction(item map[string]any) string {
	if action := strings.TrimSpace(webString(item["action"])); action != "" {
		return action
	}
	kind := strings.ToLower(strings.TrimSpace(webString(item["kind"])))
	switch kind {
	case "network":
		return "network_access"
	case "host":
		return "host_access"
	case "mcp_tool":
		return "mcp"
	case "ask", "":
		return "info"
	default:
		return kind
	}
}

func webConfirmationCommandType(item map[string]any) string {
	return strings.TrimSpace(firstNonEmpty(
		webString(item["command_type"]), webString(item["tool_name"]), webString(item["kind"]),
	))
}

func webPendingChoiceOptions(item map[string]any) []map[string]any {
	questions, _ := item["questions"].([]any)
	if len(questions) != 1 {
		return nil
	}
	question, _ := questions[0].(map[string]any)
	choices, _ := question["options"].([]any)
	if len(choices) == 0 || len(choices) > 20 {
		return nil
	}
	result := make([]map[string]any, 0, len(choices))
	for _, raw := range choices {
		switch choice := raw.(type) {
		case string:
			value := strings.TrimSpace(choice)
			if value != "" {
				result = append(result, map[string]any{"label": value, "value": value})
			}
		case map[string]any:
			value := strings.TrimSpace(firstNonEmpty(webString(choice["value"]), webString(choice["label"])))
			label := strings.TrimSpace(firstNonEmpty(webString(choice["label"]), value))
			if value != "" {
				result = append(result, map[string]any{"label": label, "value": value})
			}
		}
	}
	return result
}

func (s *Server) handleWebConversationConfirmation(
	w http.ResponseWriter,
	r *http.Request,
	frame workspace.CompatibilityFrame,
	rawCallID string,
) {
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	callID, err := url.PathUnescape(rawCallID)
	if err != nil || strings.TrimSpace(callID) == "" {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "invalid confirmation id"})
		return
	}
	var input struct {
		MsgID       string `json:"msg_id"`
		Data        any    `json:"data"`
		AlwaysAllow bool   `json:"always_allow"`
	}
	if err := decodeWebConversationJSON(w, r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "invalid confirmation response: " + err.Error()})
		return
	}
	metadata, found, err := s.workspaceStore.GetFrameRuntimeMetadata(frame.ID)
	if err != nil {
		writeWebConversationError(w, err)
		return
	}
	if !found {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "confirmation not found"})
		return
	}
	var pending map[string]any
	for _, item := range compatibilityServerPendingInputs(metadata.ContextData) {
		if compatibilityServerPendingInputID(item) == callID {
			pending = item
			break
		}
	}
	if pending == nil {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "confirmation not found"})
		return
	}
	response, alwaysAllow, err := webConfirmationInputResponse(callID, pending, input.Data, input.AlwaysAllow)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	result, err := s.resolveCompatibilityInput(r, frame, compatibilityResolveInputRequest{Responses: []compatibilityInputResponse{response}})
	if err != nil {
		var requestErr *compatibilityResolveInputError
		if errors.As(err, &requestErr) {
			writeWorkspaceJSON(w, requestErr.Status, map[string]any{"message": requestErr.Detail})
			return
		}
		writeWebConversationError(w, err)
		return
	}
	warning := ""
	remembered := false
	if alwaysAllow {
		userID := strings.TrimSpace(r.Header.Get("X-Synon-User-Id"))
		if err := s.rememberWebApproval(userID, webConfirmationAction(pending), webConfirmationCommandType(pending)); err != nil {
			if warning != "" {
				warning += "; approval preference was not saved"
			} else {
				warning = "confirmation resolved for this request, but the approval preference was not saved"
			}
		} else {
			remembered = true
		}
	}
	responseBody := map[string]any{
		"success": true, "status": result.Status, "remaining": len(result.RemainingIDs),
		"remembered": remembered,
	}
	if warning != "" {
		responseBody["warning"] = warning
	}
	writeWorkspaceJSON(w, http.StatusOK, responseBody)
}

func webConfirmationInputResponse(
	callID string,
	item map[string]any,
	data any,
	alwaysAllow bool,
) (compatibilityInputResponse, bool, error) {
	selected := ""
	answers := map[string]string{}
	switch value := data.(type) {
	case string:
		selected = strings.TrimSpace(value)
	case map[string]any:
		selected = strings.TrimSpace(firstNonEmpty(webString(value["value"]), webString(value["action"])))
		if rawAnswers, ok := value["answers"].(map[string]any); ok {
			for question, raw := range rawAnswers {
				if answer, ok := raw.(string); ok && strings.TrimSpace(question) != "" {
					answers[question] = answer
				}
			}
		}
	default:
		return compatibilityInputResponse{}, false, errors.New("confirmation data must be a string or object")
	}
	if alwaysAllow {
		selected = "proceed_always"
	}
	kind := strings.ToLower(strings.TrimSpace(webString(item["kind"])))
	if kind == "" {
		kind = "ask"
	}
	response := compatibilityInputResponse{ToolID: callID}
	if (kind == "artifact_delete" || kind == "capability_install") &&
		(alwaysAllow || selected == "proceed_always" || selected == "allow_always") {
		return compatibilityInputResponse{}, false, errors.New("this approval cannot be remembered")
	}
	if kind == "ask" {
		if len(answers) > 0 {
			response.Action, response.Answers = "answer", answers
			return response, false, nil
		}
		switch selected {
		case "decide_for_me", "cancel":
			response.Action = selected
			return response, false, nil
		case "":
			return compatibilityInputResponse{}, false, errors.New("a confirmation option is required")
		default:
			questions := compatibilityPendingQuestions(item)
			if len(questions) != 1 {
				return compatibilityInputResponse{}, false, errors.New("this question requires structured answers")
			}
			response.Action = "answer"
			response.Answers = map[string]string{questions[0]: selected}
			return response, false, nil
		}
	}
	approved := true
	switch selected {
	case "proceed_always", "allow_always":
		response.Action, response.Scope = "allow_always", "always"
		alwaysAllow = true
	case "proceed_once", "proceed", "allow", "allow_once", "approve":
		response.Action, response.Scope = "allow_once", "once"
	case "deny", "reject", "cancel":
		approved = false
		response.Action = "deny"
	case "":
		return compatibilityInputResponse{}, false, errors.New("a confirmation option is required")
	default:
		return compatibilityInputResponse{}, false, fmt.Errorf("unsupported confirmation option %q", selected)
	}
	response.Approved = &approved
	return response, alwaysAllow, nil
}

func (s *Server) handleWebConversationApprovalCheck(
	w http.ResponseWriter,
	r *http.Request,
	_ workspace.CompatibilityFrame,
) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	action := strings.TrimSpace(r.URL.Query().Get("action"))
	if action == "" {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "action is required"})
		return
	}
	approved, err := s.webConversationApproval(
		strings.TrimSpace(r.Header.Get("X-Synon-User-Id")),
		action,
		strings.TrimSpace(r.URL.Query().Get("command_type")),
	)
	if err != nil {
		writeWebConversationError(w, err)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"approved": approved})
}

func (s *Server) webConversationApproval(userID string, action string, commandType string) (bool, error) {
	userID = strings.TrimSpace(userID)
	action = strings.ToLower(strings.TrimSpace(action))
	commandType = strings.ToLower(strings.TrimSpace(commandType))
	if userID == "" {
		return false, &webConversationRequestError{Status: http.StatusUnauthorized, Detail: "authentication required"}
	}
	if action == "" {
		return false, &webConversationRequestError{Status: http.StatusBadRequest, Detail: "action is required"}
	}
	defaults := s.agentRuntimeApprovalDefaults()
	mode := strings.ToLower(strings.TrimSpace(defaults.Mode))
	profile, knownAction := synonlink.PolicyMatrixWithDefaults(defaults)[action]
	if mode == "deny" || (knownAction && profile.EffectiveApprovalPolicy == "deny") {
		return false, nil
	}
	if mode == "allow" || (knownAction && profile.EffectiveApprovalPolicy == "none") {
		return true, nil
	}
	if s.settingsStore == nil {
		return false, nil
	}
	setting, found, err := s.settingsStore.Get(approvalRememberedSettingKey)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	decisions := rememberedApprovalDecisionsFromSetting(setting.Value)
	key := synonlink.RememberedApprovalKey(
		userID,
		webApprovalRememberedClient,
		webApprovalDecisionAction(action, commandType),
	)
	_, approved := decisions[key]
	return approved, nil
}

func (s *Server) rememberWebApproval(userID string, action string, commandType string) error {
	if s == nil || s.settingsStore == nil {
		return errors.New("approval remembered decision store is not configured")
	}
	userID = strings.TrimSpace(userID)
	action = strings.ToLower(strings.TrimSpace(action))
	commandType = strings.ToLower(strings.TrimSpace(commandType))
	if userID == "" {
		return errors.New("authenticated user is required")
	}
	if action == "" || action == "info" {
		return errors.New("permission action is required")
	}
	if strings.EqualFold(strings.TrimSpace(s.agentRuntimeApprovalDefaults().Mode), "deny") {
		return errors.New("approval defaults deny remembered decisions")
	}
	decision := synonlink.RememberedApprovalDecision{
		UserID:    userID,
		ClientID:  webApprovalRememberedClient,
		Action:    webApprovalDecisionAction(action, commandType),
		Reason:    "approved from Synon Web confirmation",
		CreatedAt: time.Now().UTC(),
	}
	if s.approvalDecisionMu != nil {
		s.approvalDecisionMu.Lock()
		defer s.approvalDecisionMu.Unlock()
	}
	setting, found, err := s.settingsStore.Get(approvalRememberedSettingKey)
	if err != nil {
		return err
	}
	decisions := map[string]synonlink.RememberedApprovalDecision{}
	if found {
		decisions = rememberedApprovalDecisionsFromSetting(setting.Value)
	}
	key := synonlink.RememberedApprovalKey(decision.UserID, decision.ClientID, decision.Action)
	decisions[key] = decision
	_, err = s.settingsStore.Set(approvalRememberedSettingKey, rememberedApprovalDecisionsToSetting(decisions))
	return err
}

func webApprovalDecisionAction(action string, commandType string) string {
	action = strings.ToLower(strings.TrimSpace(action))
	commandType = strings.ToLower(strings.TrimSpace(commandType))
	sum := sha256.Sum256([]byte(action + "\x00" + commandType))
	return "synon-web:" + action + ":" + hex.EncodeToString(sum[:])[:24]
}

func (s *Server) handleWebConversationSideQuestion(
	w http.ResponseWriter,
	r *http.Request,
	frame workspace.CompatibilityFrame,
) {
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	var input struct {
		Question string `json:"question"`
	}
	if err := decodeWebConversationJSON(w, r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "invalid side question: " + err.Error()})
		return
	}
	input.Question = strings.TrimSpace(input.Question)
	if input.Question == "" {
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"status": "invalid", "reason": "emptyQuestion"})
		return
	}
	intentID := uuid.NewString()
	created, err := s.createCompatibilityAside(r, frame, compatibilityAsideRequest{
		Request: input.Question, IntentID: &intentID, AsSession: false,
	})
	if err != nil {
		var requestErr *compatibilityAsideError
		if errors.As(err, &requestErr) {
			writeWorkspaceJSON(w, requestErr.Status, map[string]any{"message": requestErr.Detail})
			return
		}
		writeWebConversationError(w, err)
		return
	}
	answer, status, err := s.waitForWebSideQuestion(r.Context(), created.Frame.ID, 120*time.Second)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		if errors.Is(err, context.DeadlineExceeded) {
			writeWorkspaceJSON(w, http.StatusGatewayTimeout, map[string]any{"message": "side question timed out"})
			return
		}
		writeWebConversationError(w, err)
		return
	}
	response := map[string]any{"status": status}
	if status == "ok" {
		response["answer"] = answer
	}
	writeWorkspaceJSON(w, http.StatusOK, response)
}

func (s *Server) waitForWebSideQuestion(
	ctx context.Context,
	frameID string,
	timeout time.Duration,
) (string, string, error) {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	waitContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		frame, found, err := s.workspaceStore.GetCompatibilityFrame(frameID)
		if err != nil {
			return "", "", err
		}
		if !found {
			return "", "", &webConversationRequestError{Status: http.StatusNotFound, Detail: "side question frame not found"}
		}
		output, hasOutput, err := s.workspaceStore.GetFrameOutputData(frameID)
		if err != nil {
			return "", "", err
		}
		if hasOutput {
			if answer := strings.TrimSpace(webString(output["response"])); answer != "" {
				return answer, "ok", nil
			}
		}
		switch strings.ToLower(strings.TrimSpace(frame.Status)) {
		case "completed", "cancelled", "canceled", "stopped":
			return "", "noAnswer", nil
		case "failed":
			detail := "side question failed"
			if hasOutput {
				detail = firstNonEmpty(webString(output["error"]), detail)
			}
			return "", "", errors.New(detail)
		}
		select {
		case <-waitContext.Done():
			return "", "", waitContext.Err()
		case <-ticker.C:
		}
	}
}
