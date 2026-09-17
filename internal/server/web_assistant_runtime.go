package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) validateWebConversationSendInput(
	userID string,
	frame workspace.CompatibilityFrame,
	input *webConversationSendInput,
) error {
	if input == nil {
		return &webConversationRequestError{Status: http.StatusBadRequest, Detail: "message input is required"}
	}
	record, err := s.webConversationAssistantRecord(userID, frame.ID, frame.AgentName)
	if err != nil {
		return err
	}
	if !record.Agent.Enabled {
		return &webConversationRequestError{Status: http.StatusConflict, Detail: "selected assistant is unavailable or disabled"}
	}
	if len(input.Files) > 50 {
		return &webConversationRequestError{Status: http.StatusBadRequest, Detail: "files contains more than 50 entries"}
	}
	for _, selectedFile := range input.Files {
		if value := strings.TrimSpace(selectedFile); value == "" || len(value) > 8192 {
			return &webConversationRequestError{Status: http.StatusBadRequest, Detail: "files entries must be non-empty strings of at most 8192 bytes"}
		}
	}
	skillNames, err := webAssistantStringList(input.InjectSkills, 64, 256)
	if err != nil {
		return &webConversationRequestError{Status: http.StatusBadRequest, Detail: "Skill selection: " + err.Error()}
	}
	input.InjectSkills = skillNames
	allowed := s.webAssistantAllowedSkillNames(record)
	excluded := appendUniqueFolded(record.Agent.SkillTombstones, record.Config.DisabledBuiltinSkills...)
	if err := validateWebAssistantSkillSelection(input.InjectSkills, allowed, excluded); err != nil {
		return &webConversationRequestError{Status: http.StatusForbidden, Detail: err.Error()}
	}
	mcpIDs, err := webAssistantStringList(input.InjectMCPServerIDs, 64, 256)
	if err != nil {
		return &webConversationRequestError{Status: http.StatusBadRequest, Detail: "MCP selection: " + err.Error()}
	}
	allowedMCPIDs, err := s.webAssistantAllowedMCPIDs(userID, record)
	if err != nil {
		return err
	}
	allowedMCPSet := normalizedSkillNameSet(allowedMCPIDs)
	configuredMCPIDs, mcpConstrained, err := s.webAssistantConfiguredMCPSelection(userID, frame.ID)
	if err != nil {
		return err
	}
	configuredMCPSet := normalizedSkillNameSet(configuredMCPIDs)
	for _, id := range mcpIDs {
		if _, found := allowedMCPSet[strings.ToLower(id)]; !found {
			return &webConversationRequestError{Status: http.StatusForbidden, Detail: fmt.Sprintf("MCP server %q is outside the selected assistant authority", id)}
		}
		if mcpConstrained {
			if _, found := configuredMCPSet[strings.ToLower(id)]; !found {
				return &webConversationRequestError{Status: http.StatusForbidden, Detail: fmt.Sprintf("MCP server %q is not loaded for this conversation", id)}
			}
		}
	}
	input.InjectMCPServerIDs = mcpIDs
	if len(input.ArtifactRefs)+len(input.InjectSkills)+len(input.InjectMCPServerIDs) > 64 {
		return &webConversationRequestError{Status: http.StatusBadRequest, Detail: "combined artifact, Skill, and MCP context contains more than 64 entries"}
	}
	for key, value := range input.SessionOptions {
		switch key {
		case "ultra_mode", "plan_mode":
			if _, ok := value.(bool); !ok {
				return &webConversationRequestError{Status: http.StatusBadRequest, Detail: key + " must be boolean"}
			}
		case "verifier_mode", "memory_mode":
			mode, ok := value.(string)
			mode = strings.ToLower(strings.TrimSpace(mode))
			if !ok || (mode != "on" && mode != "off") {
				return &webConversationRequestError{Status: http.StatusBadRequest, Detail: key + " must be on or off"}
			}
		case "target_agent":
			target, ok := value.(string)
			if !ok || normalizeBundledAgentName(target) != normalizeBundledAgentName(frame.AgentName) {
				return &webConversationRequestError{Status: http.StatusForbidden, Detail: "target_agent cannot exceed the conversation assistant authority"}
			}
		case "target_branch_id":
			branchID, ok := value.(string)
			branchID = strings.TrimSpace(branchID)
			if !ok || branchID == "" || len(branchID) > 256 {
				return &webConversationRequestError{Status: http.StatusBadRequest, Detail: "target_branch_id must be a non-empty string"}
			}
			if frame.ParentFrameID != "" {
				return &webConversationRequestError{Status: http.StatusBadRequest, Detail: "target_branch_id is only valid for root conversations"}
			}
		case "expected_branch_id":
			branchID, ok := value.(string)
			if !ok || strings.TrimSpace(branchID) == "" || len(strings.TrimSpace(branchID)) > 256 {
				return &webConversationRequestError{Status: http.StatusBadRequest, Detail: "expected_branch_id must be a non-empty string"}
			}
		case "expected_generation":
			if _, ok := webPositiveSafeInteger(value); !ok {
				return &webConversationRequestError{Status: http.StatusBadRequest, Detail: "expected_generation must be a positive safe integer"}
			}
		case "effort":
			effort, ok := value.(string)
			effort = strings.ToLower(strings.TrimSpace(effort))
			if !ok || (effort != "low" && effort != "medium" && effort != "high") {
				return &webConversationRequestError{Status: http.StatusBadRequest, Detail: "effort must be low, medium, or high"}
			}
		case "model", "subagent_model":
			model, ok := value.(string)
			model = strings.TrimSpace(model)
			if !ok || model == "" {
				return &webConversationRequestError{Status: http.StatusBadRequest, Detail: key + " must be a non-empty string"}
			}
			allowedModels, err := s.webAssistantAllowedModels(userID, record)
			if err != nil {
				return err
			}
			if len(allowedModels) > 0 {
				if _, allowed := normalizedSkillNameSet(allowedModels)[strings.ToLower(model)]; !allowed {
					return &webConversationRequestError{Status: http.StatusForbidden, Detail: key + " is unavailable for the selected assistant"}
				}
			}
		default:
			return &webConversationRequestError{Status: http.StatusBadRequest, Detail: "session_options contains unknown field " + key}
		}
	}
	targetBranchID := strings.TrimSpace(webString(input.SessionOptions["target_branch_id"]))
	expectedBranchID := strings.TrimSpace(webString(input.SessionOptions["expected_branch_id"]))
	_, expectedGenerationOK := webPositiveSafeInteger(input.SessionOptions["expected_generation"])
	input.LoadingID = strings.TrimSpace(input.LoadingID)
	input.MessageContext = strings.TrimSpace(input.MessageContext)
	if input.MessageContext != "" && input.MessageContext != "onboarding_first_task" {
		return &webConversationRequestError{Status: http.StatusBadRequest, Detail: "message_context is invalid"}
	}
	if targetBranchID == "" && (expectedBranchID != "" || expectedGenerationOK) {
		return &webConversationRequestError{
			Status: http.StatusBadRequest, Detail: "expected branch state requires target_branch_id",
		}
	}
	return nil
}

func webPositiveSafeInteger(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		if typed < 1 || typed > 1<<53-1 || float64(int64(typed)) != typed {
			return 0, false
		}
		return int64(typed), true
	case int:
		return int64(typed), typed > 0
	case int64:
		return typed, typed > 0
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil && parsed > 0 && parsed <= 1<<53-1
	default:
		return 0, false
	}
}

func webConversationRunnerSessionConfig(options map[string]any) map[string]any {
	keys := map[string]string{
		"subagent_model": "subagentModel",
		"target_agent":   "targetAgent",
		"plan_mode":      "planMode",
		"ultra_mode":     "ultraMode",
		"verifier_mode":  "verifierMode",
		"memory_mode":    "memoryMode",
	}
	config := make(map[string]any, len(keys))
	for source, target := range keys {
		if value, found := options[source]; found {
			config[target] = value
		}
	}
	if len(config) == 0 {
		return nil
	}
	return config
}

func (s *Server) webConversationAssistantRecord(userID, frameID, frameAgentName string) (webAssistantRecord, error) {
	assistantID := ""
	metadata, found, err := s.workspaceStore.GetFrameRuntimeMetadata(frameID)
	if err != nil {
		return webAssistantRecord{}, err
	}
	if found {
		if assistant, ok := metadata.ContextData["web_assistant"].(map[string]any); ok {
			assistantID = strings.TrimSpace(webString(assistant["id"]))
		}
	}
	if assistantID == "" {
		assistantID = webAssistantID(frameAgentName)
	}
	record, recordFound, err := s.webAssistantRecord(userID, assistantID)
	if err != nil {
		return webAssistantRecord{}, err
	}
	if !recordFound {
		return webAssistantRecord{}, &webConversationRequestError{Status: http.StatusConflict, Detail: "conversation assistant no longer exists"}
	}
	runtimeAgent, err := s.resolveWebAssistantRuntimeAgent(userID, record.RuntimeID)
	if err != nil {
		var requestErr *agentProfileRequestError
		if errors.As(err, &requestErr) {
			return webAssistantRecord{}, &webConversationRequestError{Status: http.StatusConflict, Detail: requestErr.Detail}
		}
		return webAssistantRecord{}, err
	}
	if normalizeBundledAgentName(runtimeAgent) != normalizeBundledAgentName(frameAgentName) {
		return webAssistantRecord{}, &webConversationRequestError{Status: http.StatusConflict, Detail: "conversation assistant authority changed; create a new conversation"}
	}
	return record, nil
}

func (s *Server) normalizeWebAssistantConversationOverrides(
	userID string,
	record webAssistantRecord,
	raw map[string]any,
) (map[string]any, error) {
	if raw == nil {
		return map[string]any{}, nil
	}
	for key := range raw {
		switch key {
		case "model", "permission", "thought_level", "skill_ids", "disabled_builtin_skill_ids", "mcp_ids":
		default:
			return nil, &webConversationRequestError{Status: http.StatusBadRequest, Detail: "assistant override contains unknown field " + key}
		}
	}
	result := map[string]any{}
	if value, provided := raw["model"]; provided {
		model, ok := value.(string)
		model = strings.TrimSpace(model)
		if !ok || model == "" || len(model) > 256 {
			return nil, &webConversationRequestError{Status: http.StatusBadRequest, Detail: "assistant model override is invalid"}
		}
		allowedModels, err := s.webAssistantAllowedModels(userID, record)
		if err != nil {
			return nil, err
		}
		if len(allowedModels) > 0 {
			if _, allowed := normalizedSkillNameSet(allowedModels)[strings.ToLower(model)]; !allowed {
				return nil, &webConversationRequestError{Status: http.StatusBadRequest, Detail: "assistant model override is unavailable"}
			}
		}
		result["model"] = model
	}
	if value, provided := raw["permission"]; provided {
		permissionRaw, ok := value.(string)
		permission, valid := normalizeWebAssistantPermissionMode(permissionRaw)
		if !ok || !valid {
			return nil, &webConversationRequestError{Status: http.StatusBadRequest, Detail: "assistant permission override is invalid"}
		}
		result["permission"] = permission
	}
	if value, provided := raw["thought_level"]; provided {
		thought, ok := value.(string)
		thought = strings.ToLower(strings.TrimSpace(thought))
		if !ok || !webAssistantThoughtLevel(thought) {
			return nil, &webConversationRequestError{Status: http.StatusBadRequest, Detail: "assistant thought level override is invalid"}
		}
		result["thought_level"] = thought
	}
	allowedSkills := s.webAssistantAllowedSkillNames(record)
	excludedSkills := appendUniqueFolded(record.Agent.SkillTombstones, record.Config.DisabledBuiltinSkills...)
	if value, provided := raw["disabled_builtin_skill_ids"]; provided {
		disabled, err := webAssistantStringList(value, 256, 256)
		if err != nil {
			return nil, &webConversationRequestError{Status: http.StatusBadRequest, Detail: "disabled skill override: " + err.Error()}
		}
		for _, name := range disabled {
			if _, found := findCatalogSkill(s.skillCatalog, name); !found {
				return nil, &webConversationRequestError{Status: http.StatusBadRequest, Detail: fmt.Sprintf("disabled skill %q does not exist", name)}
			}
		}
		excludedSkills = appendUniqueFolded(excludedSkills, disabled...)
		result["disabled_builtin_skill_ids"] = disabled
	}
	if value, provided := raw["skill_ids"]; provided {
		selected, err := webAssistantStringList(value, 256, 256)
		if err != nil {
			return nil, &webConversationRequestError{Status: http.StatusBadRequest, Detail: "skill override: " + err.Error()}
		}
		if err := validateWebAssistantSkillSelection(selected, allowedSkills, excludedSkills); err != nil {
			return nil, &webConversationRequestError{Status: http.StatusForbidden, Detail: err.Error()}
		}
		result["skill_ids"] = selected
	}
	if value, provided := raw["mcp_ids"]; provided {
		selected, err := webAssistantStringList(value, 256, 256)
		if err != nil {
			return nil, &webConversationRequestError{Status: http.StatusBadRequest, Detail: "MCP override: " + err.Error()}
		}
		allowed, err := s.webAssistantAllowedMCPIDs(userID, record)
		if err != nil {
			return nil, err
		}
		allowedSet := normalizedSkillNameSet(allowed)
		for _, id := range selected {
			if _, found := allowedSet[strings.ToLower(id)]; !found {
				return nil, &webConversationRequestError{Status: http.StatusForbidden, Detail: fmt.Sprintf("MCP server %q is outside the selected assistant authority", id)}
			}
		}
		result["mcp_ids"] = selected
	}
	return result, nil
}

func (s *Server) webAssistantAllowedModels(userID string, record webAssistantRecord) ([]string, error) {
	models := appendUniqueFolded(nil, record.Config.Models...)
	option, found, err := s.webConversationModelOption(userID)
	if err != nil {
		return nil, err
	}
	if found {
		if current := strings.TrimSpace(webString(option["current_value"])); current != "" {
			models = appendUniqueFolded(models, current)
		}
		switch values := option["options"].(type) {
		case []map[string]any:
			for _, value := range values {
				models = appendUniqueFolded(models, webString(value["value"]))
			}
		case []any:
			for _, value := range values {
				if item, ok := value.(map[string]any); ok {
					models = appendUniqueFolded(models, webString(item["value"]))
				}
			}
		}
	}
	return models, nil
}

func (s *Server) webAssistantAllowedMCPIDs(userID string, record webAssistantRecord) ([]string, error) {
	runtimeAgent, err := s.webAssistantRuntimeAgentForRecord(userID, record)
	if err != nil {
		return nil, err
	}
	return s.webAssistantAllowedMCPIDsForRuntimeAgent(userID, runtimeAgent)
}

func (s *Server) webAssistantAllowedMCPIDsForRuntimeAgent(userID, runtimeAgent string) ([]string, error) {
	profile, found, err := s.workspaceStore.GetAgent(userID, runtimeAgent)
	if err != nil {
		return nil, err
	}
	var ids []string
	if strings.EqualFold(runtimeAgent, "OPERON") || (found && profile.Unrestricted) {
		connectors, _, err := s.workspaceMCPRuntimeConnectors(userID, runtimeAgent, profile, found)
		if err != nil {
			return nil, err
		}
		blocked := normalizedSkillNameSet(profile.ConnectorTombstones)
		for _, connector := range connectors {
			if connector.Enabled {
				if _, excluded := blocked[strings.ToLower(connector.ID)]; !excluded {
					ids = append(ids, connector.ID)
				}
			}
		}
		return appendUniqueFolded(nil, ids...), nil
	}
	if !found {
		return []string{}, nil
	}
	attachments, err := s.workspaceStore.ListAgentConnectorAttachments(userID, runtimeAgent)
	if err != nil {
		return nil, err
	}
	for _, attachment := range attachments {
		connector, resolved, err := s.workspaceMCPRuntimeConnectorFromAttachment(userID, attachment)
		if err != nil {
			return nil, err
		}
		if resolved && connector.Enabled {
			ids = append(ids, connector.ID)
		}
	}
	return appendUniqueFolded(nil, ids...), nil
}

func webAssistantPermissionMode(value string) bool {
	_, ok := normalizeWebAssistantPermissionMode(value)
	return ok
}

// normalizeWebAssistantPermissionMode is the one wire-to-runtime mapping for
// the compact permission selector. The UI names are intentionally stable
// across new and existing conversations; the runtime consumes the canonical
// approval decisions below.
func normalizeWebAssistantPermissionMode(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "default", "ask":
		return "default", true
	case "smart", "confirm", "acceptedits", "auto", "dontask", "autoedit":
		return "smart", true
	case "bypasspermissions", "bypass-permissions", "bypass", "yolo", "yolonosandbox", "full-access", "full_access", "allow":
		return "allow", true
	case "deny":
		return "deny", true
	default:
		return "", false
	}
}

func webAssistantThoughtLevel(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "default", "auto", "off", "disabled", "none", "on", "enabled", "low", "medium", "high", "max":
		return true
	default:
		return false
	}
}

func (s *Server) applyWebAssistantRuntimeOptions(
	session sessionstore.Session,
	userID string,
	frameAgentName string,
	options SessionRunnerChatOptions,
) (SessionRunnerChatOptions, string, error) {
	metadata, found, err := s.workspaceStore.GetFrameRuntimeMetadata(session.ID)
	if err != nil || !found {
		return options, "", err
	}
	assistant, ok := metadata.ContextData["web_assistant"].(map[string]any)
	if !ok {
		return options, "", nil
	}
	assistantID := strings.TrimSpace(webString(assistant["id"]))
	if assistantID == "" {
		// A conversation-scoped model override is stored in the same metadata
		// namespace even when no assistant was explicitly selected. Resolve that
		// compatibility shape through the frame's existing agent authority instead
		// of treating the model-only patch as a corrupt assistant selection.
		assistantID = webAssistantID(frameAgentName)
	}
	record, found, err := s.webAssistantRecord(userID, assistantID)
	if err != nil {
		return options, "", err
	}
	if !found || !record.Agent.Enabled {
		return options, "", fmt.Errorf("selected assistant %q is unavailable or disabled", assistantID)
	}
	runtimeAgent, err := s.resolveWebAssistantRuntimeAgent(userID, assistantID)
	if err != nil {
		return options, "", err
	}
	if normalizeBundledAgentName(runtimeAgent) != normalizeBundledAgentName(frameAgentName) {
		return options, "", fmt.Errorf("assistant runtime agent does not match the conversation authority")
	}

	if prompt, promptFound, err := s.workspaceStore.GetCustomAgentPrompt(userID, record.Agent.Name); err != nil {
		return options, "", err
	} else if promptFound {
		if record.Bundled && strings.TrimSpace(options.SystemPrompt) != "" {
			options.SystemPrompt = strings.TrimSpace(options.SystemPrompt) +
				"\n\nAssistant-specific rules:\n" + strings.TrimSpace(prompt.PromptText)
		} else {
			options.SystemPrompt = strings.TrimSpace(prompt.PromptText)
		}
	}

	overrides, _ := assistant["conversation_overrides"].(map[string]any)
	allowed := s.webAssistantAllowedSkillNames(record)
	excluded := appendUniqueFolded(record.Agent.SkillTombstones, record.Config.DisabledBuiltinSkills...)
	excluded = appendUniqueFolded(excluded, webAssistantStringValues(overrides["disabled_builtin_skill_ids"])...)
	allowed = subtractFoldedStrings(allowed, excluded)
	selected, fixed := webAssistantDefaultSkillSelection(record)
	if _, provided := overrides["skill_ids"]; provided {
		selected = webAssistantStringValues(overrides["skill_ids"])
		fixed = true
	}
	if inputData, ok := session.Orchestration["inputData"].(map[string]any); ok {
		selected = appendUniqueFolded(selected, webAssistantStringValues(inputData["inject_skills"])...)
		options.SystemPrompt = appendWebComposerRuntimeContext(options.SystemPrompt, inputData)
	}
	if err := validateWebAssistantSkillSelection(selected, allowed, excluded); err != nil {
		return options, "", err
	}
	options.SelectedSkillNames = appendUniqueFolded(nil, selected...)
	options.ExcludedSkillNames = appendUniqueFolded(options.ExcludedSkillNames, excluded...)
	options.AllowedSkillNames = appendUniqueFolded(nil, allowed...)
	options.RestrictSkillDiscovery = fixed || !record.Agent.Unrestricted

	if model := webAssistantRuntimeScalar(record, overrides, "model"); model != "" && model != "default" {
		options.Model = model
	}
	switch strings.ToLower(webAssistantRuntimeScalar(record, overrides, "thought_level")) {
	case "off", "disabled", "none":
		options.DisableThinking = true
	case "on", "enabled", "low", "medium", "high", "max":
		options.DisableThinking = false
	}
	return options, record.RuntimeID, nil
}

func (s *Server) webAssistantAllowedSkillNames(record webAssistantRecord) []string {
	values := appendUniqueFolded(record.Agent.SkillNames, record.Config.CustomSkillNames...)
	if record.Agent.Unrestricted && s.skillCatalog != nil {
		values = values[:0]
		for _, skill := range s.skillCatalog.Skills() {
			values = append(values, skill.Name)
		}
	}
	return subtractFoldedStrings(values, appendUniqueFolded(record.Agent.SkillTombstones, record.Config.DisabledBuiltinSkills...))
}

func webAssistantDefaultSkillSelection(record webAssistantRecord) ([]string, bool) {
	// OPERON's skill list is a durable discovery allowlist, not a frozen
	// per-turn selection. Older persisted assistant settings may still contain
	// a fixed list from before the catalog changed; replaying that list makes a
	// valid task fail closed before the model can discover the current skills.
	// Explicit conversation overrides are applied by applyWebAssistantRuntimeOptions
	// after this default is resolved and therefore remain authoritative.
	if webAssistantUsesAutomaticSkillDefaults(record) {
		return nil, false
	}
	defaults := normalizeStoredWebAssistantDefaults(record.Config.Defaults, record.Agent.SkillNames, webAssistantUsesAutomaticSkillDefaults(record))
	option, _ := defaults["skills"].(map[string]any)
	mode := strings.ToLower(strings.TrimSpace(webString(option["mode"])))
	if mode == "auto" {
		return nil, false
	}
	return webAssistantStringValues(option["value"]), true
}

func webAssistantRuntimeScalar(record webAssistantRecord, overrides map[string]any, key string) string {
	if value := strings.TrimSpace(webString(overrides[key])); value != "" {
		return value
	}
	defaults := normalizeStoredWebAssistantDefaults(record.Config.Defaults, record.Agent.SkillNames, webAssistantUsesAutomaticSkillDefaults(record))
	option, _ := defaults[key].(map[string]any)
	if strings.EqualFold(strings.TrimSpace(webString(option["mode"])), "fixed") {
		return strings.TrimSpace(webString(option["value"]))
	}
	return ""
}

func (s *Server) webAssistantConfiguredMCPSelection(
	userID string,
	frameID string,
) ([]string, bool, error) {
	metadata, found, err := s.workspaceStore.GetFrameRuntimeMetadata(frameID)
	if err != nil || !found {
		return nil, false, err
	}
	assistant, ok := metadata.ContextData["web_assistant"].(map[string]any)
	if !ok {
		return nil, false, nil
	}
	record, found, err := s.webAssistantRecord(userID, webString(assistant["id"]))
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, fmt.Errorf("conversation assistant no longer exists")
	}
	overrides, _ := assistant["conversation_overrides"].(map[string]any)
	selected := []string(nil)
	constrained := false
	if value, provided := overrides["mcp_ids"]; provided {
		selected = webAssistantStringValues(value)
		constrained = true
	} else {
		defaults := normalizeStoredWebAssistantDefaults(record.Config.Defaults, record.Agent.SkillNames, webAssistantUsesAutomaticSkillDefaults(record))
		option, _ := defaults["mcps"].(map[string]any)
		if strings.EqualFold(webString(option["mode"]), "fixed") {
			selected = webAssistantStringValues(option["value"])
			constrained = true
		}
	}
	if !constrained {
		return nil, false, nil
	}
	allowed, err := s.webAssistantAllowedMCPIDs(userID, record)
	if err != nil {
		return nil, false, err
	}
	allowedSet := normalizedSkillNameSet(allowed)
	for _, id := range selected {
		if _, ok := allowedSet[strings.ToLower(strings.TrimSpace(id))]; !ok {
			return nil, false, fmt.Errorf("MCP server %q is outside the selected assistant authority", id)
		}
	}
	return appendUniqueFolded(nil, selected...), true, nil
}

func (s *Server) webAssistantRuntimeMCPSelection(
	userID string,
	frameID string,
) ([]string, bool, error) {
	configured, constrained, err := s.webAssistantConfiguredMCPSelection(userID, frameID)
	if err != nil || s.sessionStore == nil {
		return configured, constrained, err
	}
	session, found, err := s.sessionStore.Get(strings.TrimSpace(frameID))
	if err != nil || !found {
		return configured, constrained, err
	}
	inputData, _ := session.Orchestration["inputData"].(map[string]any)
	injected := appendUniqueFolded(nil, webAssistantStringValues(inputData["inject_mcp_server_ids"])...)
	if len(injected) == 0 {
		return configured, constrained, nil
	}
	if constrained {
		configuredSet := normalizedSkillNameSet(configured)
		for _, id := range injected {
			if _, ok := configuredSet[strings.ToLower(strings.TrimSpace(id))]; !ok {
				return nil, false, fmt.Errorf("MCP server %q is not loaded for this conversation", id)
			}
		}
	}
	// A turn-scoped MCP selection is an exact capability fence. It narrows the
	// connector snapshot for this run without mutating the conversation's durable
	// default, so queued and retried turns keep their own explicit authority.
	return injected, true, nil
}

func (s *Server) webSessionApprovalMode(sessionID string) (string, error) {
	selection, found, err := s.webSessionPermissionSelection(sessionID)
	if err != nil || !found {
		return "", err
	}
	return webConversationPermissionRuntimeMode(selection), nil
}

func webConversationPermissionRuntimeMode(selection string) string {
	// The composer permission selector is the sole authority for an active
	// conversation. "default" is deliberately an explicit safe choice rather
	// than a request to fall back to the assistant's stored defaults.
	switch strings.ToLower(strings.TrimSpace(selection)) {
	case "default", "ask", "":
		return "ask"
	case "smart", "confirm":
		return "smart"
	case "allow":
		return "allow"
	case "deny":
		return "deny"
	default:
		return strings.ToLower(strings.TrimSpace(selection))
	}
}

// webPermissionDecision is the single three-tier runtime decision contract.
// Request approval asks for every governed operation. Smart mode auto-approves
// routine sandboxed work and asks only for operations explicitly classified as
// risky. Full access never creates an approval request; hard policy denials and
// non-permission validation gates remain authoritative failures.
func webPermissionDecision(mode string, risky bool) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "allow":
		return "allow", nil
	case "deny":
		return "deny", nil
	case "smart":
		if risky {
			return "ask", nil
		}
		return "allow", nil
	case "", "default", "ask", "confirm":
		return "ask", nil
	default:
		return "", fmt.Errorf("conversation permission mode is invalid")
	}
}

// webSessionPermissionSelection returns the canonical value selected by the
// conversation composer. It intentionally does not consult assistant defaults:
// those settings may seed a new conversation in the client, but must not
// override the active conversation's bottom-bar permission control.
func (s *Server) webSessionPermissionSelection(sessionID string) (string, bool, error) {
	frameContext, found, err := s.workspaceStore.GetFrameRealtimeContext(sessionID)
	if err != nil || !found {
		return "", false, err
	}
	rootFrameID := strings.TrimSpace(frameContext.Frame.RootFrameID)
	if rootFrameID != "" && rootFrameID != strings.TrimSpace(sessionID) {
		rootContext, rootFound, err := s.workspaceStore.GetFrameRealtimeContext(rootFrameID)
		if err != nil || !rootFound {
			return "", false, err
		}
		if rootContext.UserID != frameContext.UserID || rootContext.Frame.ProjectID != frameContext.Frame.ProjectID ||
			rootContext.Frame.RootFrameID != rootFrameID {
			return "", false, fmt.Errorf("conversation permission root authority is invalid")
		}
		selection, rootHasSelection, err := s.webFramePermissionSelection(rootContext.UserID, rootFrameID)
		if err != nil || rootHasSelection {
			return selection, rootHasSelection, err
		}
	}
	return s.webFramePermissionSelection(frameContext.UserID, sessionID)
}

func (s *Server) webFramePermissionSelection(userID, frameID string) (string, bool, error) {
	metadata, found, err := s.workspaceStore.GetFrameRuntimeMetadata(frameID)
	if err != nil || !found {
		return "", false, err
	}
	assistant, ok := metadata.ContextData["web_assistant"].(map[string]any)
	if !ok {
		return "", false, nil
	}
	_, found, err = s.webAssistantRecord(userID, webString(assistant["id"]))
	if err != nil {
		return "", false, err
	}
	if !found {
		return "", false, fmt.Errorf("conversation assistant no longer exists")
	}
	overrides, _ := assistant["conversation_overrides"].(map[string]any)
	rawMode := strings.TrimSpace(webString(overrides["permission"]))
	if rawMode == "" {
		return "default", true, nil
	}
	mode, valid := normalizeWebAssistantPermissionMode(rawMode)
	if !valid {
		return "", false, fmt.Errorf("stored conversation permission mode is invalid")
	}
	return mode, true, nil
}

func webAssistantStringValues(value any) []string {
	switch typed := value.(type) {
	case []string:
		return appendUniqueFolded(nil, typed...)
	case []any:
		values := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				values = append(values, text)
			}
		}
		return appendUniqueFolded(nil, values...)
	default:
		return nil
	}
}

func validateWebAssistantSkillSelection(selected, allowed, excluded []string) error {
	allowedSet := normalizedSkillNameSet(allowed)
	excludedSet := normalizedSkillNameSet(excluded)
	for _, skillName := range selected {
		key := strings.ToLower(strings.TrimSpace(skillName))
		if _, blocked := excludedSet[key]; blocked {
			return fmt.Errorf("skill %q is disabled for the selected assistant", skillName)
		}
		if _, permitted := allowedSet[key]; !permitted {
			return fmt.Errorf("skill %q is outside the selected assistant authority", skillName)
		}
	}
	return nil
}

func subtractFoldedStrings(values, excluded []string) []string {
	blocked := normalizedSkillNameSet(excluded)
	result := make([]string, 0, len(values))
	for _, value := range appendUniqueFolded(nil, values...) {
		if _, found := blocked[strings.ToLower(strings.TrimSpace(value))]; !found {
			result = append(result, value)
		}
	}
	return result
}
