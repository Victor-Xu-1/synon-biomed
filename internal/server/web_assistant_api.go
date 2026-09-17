package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"synon-go/internal/buildinfo"
	workspace "synon-go/internal/persistence/workspace"
)

const webAssistantSettingNamespace = "assistant."

type webAssistantMutationInput struct {
	ID                    string               `json:"id"`
	Name                  *string              `json:"name"`
	Description           *string              `json:"description"`
	Avatar                *string              `json:"avatar"`
	AgentID               *string              `json:"agent_id"`
	EnabledSkills         *[]string            `json:"enabled_skills"`
	CustomSkillNames      *[]string            `json:"custom_skill_names"`
	DisabledBuiltinSkills *[]string            `json:"disabled_builtin_skills"`
	Prompts               *[]string            `json:"prompts"`
	Models                *[]string            `json:"models"`
	NameI18n              *map[string]string   `json:"name_i18n"`
	DescriptionI18n       *map[string]string   `json:"description_i18n"`
	PromptsI18n           *map[string][]string `json:"prompts_i18n"`
	RecommendedPrompts    *[]string            `json:"recommended_prompts"`
	RecommendedI18n       *map[string][]string `json:"recommended_prompts_i18n"`
	Defaults              map[string]any       `json:"defaults"`
}

type webAssistantStoredConfig struct {
	Avatar                string              `json:"avatar,omitempty"`
	EngineAgentName       string              `json:"engine_agent_name,omitempty"`
	NameI18n              map[string]string   `json:"name_i18n,omitempty"`
	DescriptionI18n       map[string]string   `json:"description_i18n,omitempty"`
	Prompts               []string            `json:"prompts,omitempty"`
	PromptsI18n           map[string][]string `json:"prompts_i18n,omitempty"`
	Models                []string            `json:"models,omitempty"`
	CustomSkillNames      []string            `json:"custom_skill_names,omitempty"`
	DisabledBuiltinSkills []string            `json:"disabled_builtin_skills,omitempty"`
	Defaults              map[string]any      `json:"defaults,omitempty"`
	SortOrder             int                 `json:"sort_order,omitempty"`
	LastUsedAt            *int64              `json:"last_used_at,omitempty"`
}

type webAssistantRecord struct {
	Agent     workspace.Agent
	Bundled   bool
	Source    string
	Healthy   bool
	Config    webAssistantStoredConfig
	RuntimeID string
	Deletable bool
}

func (s *Server) handleWebAssistants(w http.ResponseWriter, r *http.Request) {
	if s.workspaceStore == nil || s.agentCatalog == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "assistant catalog is not configured"})
		return
	}
	userID := strings.TrimSpace(r.Header.Get("X-Synon-User-Id"))
	if userID == "" {
		writeWorkspaceJSON(w, http.StatusUnauthorized, map[string]any{"message": "authentication required"})
		return
	}
	if r.URL.Path == "/api/assistants" {
		switch r.Method {
		case http.MethodGet:
			records, err := s.webAssistantRecords(userID)
			if err != nil {
				writeWebAssistantError(w, err)
				return
			}
			items := make([]map[string]any, 0, len(records))
			for _, record := range records {
				items = append(items, webAssistantListProjection(record))
			}
			writePrivateRevalidatedWorkspaceJSON(w, r, userID, map[string]any{"success": true, "data": items})
		case http.MethodPost:
			var input webAssistantMutationInput
			if err := decodeWebConversationJSON(w, r, &input); err != nil {
				writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "invalid assistant request: " + err.Error()})
				return
			}
			record, err := s.createWebAssistant(userID, input)
			if err != nil {
				writeWebAssistantError(w, err)
				return
			}
			writeWorkspaceJSON(w, http.StatusCreated, map[string]any{"success": true, "data": webAssistantProjection(record)})
		default:
			w.Header().Set("Allow", "GET, POST")
			writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		}
		return
	}

	segments := workspacePathSegments(strings.TrimPrefix(r.URL.Path, "/api/assistants/"))
	if len(segments) == 1 && segments[0] == "import" {
		s.handleWebAssistantImport(w, r, userID)
		return
	}
	if len(segments) == 0 {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "assistant endpoint not found"})
		return
	}
	assistantID, err := url.PathUnescape(segments[0])
	if err != nil || strings.TrimSpace(assistantID) == "" {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "invalid assistant id"})
		return
	}
	record, found, err := s.webAssistantRecord(userID, assistantID)
	if err != nil {
		writeWebAssistantError(w, err)
		return
	}
	if !found {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "assistant not found"})
		return
	}
	if len(segments) == 2 && segments[1] == "state" {
		s.handleWebAssistantState(w, r, userID, record)
		return
	}
	if len(segments) == 2 && segments[1] == "composer-capabilities" {
		s.handleWebAssistantComposerCapabilities(w, r, userID, record)
		return
	}
	if len(segments) != 1 {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "assistant endpoint not found"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		detail, err := s.webAssistantDetail(userID, record)
		if err != nil {
			writeWebAssistantError(w, err)
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"success": true, "data": detail})
	case http.MethodPut:
		var input webAssistantMutationInput
		if err := decodeWebConversationJSON(w, r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "invalid assistant request: " + err.Error()})
			return
		}
		updated, err := s.updateWebAssistant(userID, record, input)
		if err != nil {
			writeWebAssistantError(w, err)
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"success": true, "data": webAssistantProjection(updated)})
	case http.MethodDelete:
		if !record.Deletable {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "bundled assistants cannot be deleted"})
			return
		}
		if err := deleteAgentProfile(s.workspaceStore, userID, record.Agent); err != nil {
			writeWebAssistantError(w, err)
			return
		}
		if err := s.deleteWebAssistantConfig(userID, record.Agent.Name); err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"message": "assistant deleted but metadata cleanup failed"})
			return
		}
		_ = s.workspaceStore.DeleteCustomAgentPrompt(userID, record.Agent.Name)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
	}
}

func (s *Server) createWebAssistant(userID string, input webAssistantMutationInput) (webAssistantRecord, error) {
	name := ""
	if input.Name != nil {
		name = strings.TrimSpace(*input.Name)
	}
	if name == "" {
		return webAssistantRecord{}, &agentProfileRequestError{Status: http.StatusBadRequest, Detail: "assistant name is required"}
	}
	description := "Custom " + buildinfo.Release().Name + " assistant " + name + "."
	if input.Description != nil && strings.TrimSpace(*input.Description) != "" {
		description = strings.TrimSpace(*input.Description)
	}
	skills := []string{}
	if input.EnabledSkills != nil {
		skills = uniqueSortedStrings(*input.EnabledSkills)
	}
	if input.CustomSkillNames != nil {
		skills = uniqueSortedStrings(append(skills, (*input.CustomSkillNames)...))
	}
	if err := s.validateAgentSkillNames(skills); err != nil {
		return webAssistantRecord{}, &agentProfileRequestError{Status: http.StatusBadRequest, Detail: err.Error()}
	}
	enabled := true
	create := agentProfileCreateRequest{
		Name: name, DisplayName: name, Description: description, SystemPrompt: "",
		SkillNames: &skills, Enabled: &enabled,
	}
	if input.Avatar != nil {
		create.IconKey = strings.TrimSpace(*input.Avatar)
	}
	agent, _, err := s.createAgentProfile(s.workspaceStore, userID, create)
	if err != nil {
		return webAssistantRecord{}, err
	}
	record := webAssistantRecord{
		Agent: agent, Source: "user", Healthy: true,
		RuntimeID: webAssistantID(agent.Name), Deletable: true,
	}
	config, err := s.mergeWebAssistantConfig(userID, record, input, skills)
	if err != nil {
		_ = s.workspaceStore.DeleteAgent(userID, agent.Name)
		return webAssistantRecord{}, err
	}
	record.Config = config
	return record, nil
}

func (s *Server) updateWebAssistant(
	userID string,
	record webAssistantRecord,
	input webAssistantMutationInput,
) (webAssistantRecord, error) {
	if input.AgentID != nil {
		engineName, err := s.resolveWebAssistantEngineAgent(userID, *input.AgentID)
		if err != nil {
			return webAssistantRecord{}, err
		}
		record.Config.EngineAgentName = engineName
	}
	if !record.Bundled {
		patch := agentProfilePatchRequest{}
		if input.Name != nil {
			value := strings.TrimSpace(*input.Name)
			if value == "" {
				return webAssistantRecord{}, &agentProfileRequestError{Status: http.StatusBadRequest, Detail: "assistant name is required"}
			}
			patch.DisplayName = &value
		}
		if input.Description != nil {
			value := strings.TrimSpace(*input.Description)
			if value == "" {
				return webAssistantRecord{}, &agentProfileRequestError{Status: http.StatusBadRequest, Detail: "assistant description is required"}
			}
			patch.Description = &value
		}
		if input.Avatar != nil {
			patch.IconKey = input.Avatar
		}
		if input.EnabledSkills != nil || input.CustomSkillNames != nil {
			skills := append([]string(nil), record.Agent.SkillNames...)
			if input.EnabledSkills != nil {
				skills = append([]string(nil), (*input.EnabledSkills)...)
			}
			if input.CustomSkillNames != nil {
				skills = append(skills, (*input.CustomSkillNames)...)
			}
			skills = uniqueSortedStrings(skills)
			if err := s.validateAgentSkillNames(skills); err != nil {
				return webAssistantRecord{}, &agentProfileRequestError{Status: http.StatusBadRequest, Detail: err.Error()}
			}
			patch.SkillNames = &skills
		}
		if input.DisabledBuiltinSkills != nil {
			values := uniqueSortedStrings(*input.DisabledBuiltinSkills)
			if err := s.validateAgentSkillNames(values); err != nil {
				return webAssistantRecord{}, &agentProfileRequestError{Status: http.StatusBadRequest, Detail: err.Error()}
			}
			patch.SkillTombstones = &values
		}
		updated, _, err := s.updateAgentProfile(s.workspaceStore, userID, record.Agent, patch)
		if err != nil {
			return webAssistantRecord{}, err
		}
		record.Agent = updated
	}
	config, err := s.mergeWebAssistantConfig(userID, record, input, record.Agent.SkillNames)
	if err != nil {
		return webAssistantRecord{}, err
	}
	record.Config = config
	return record, nil
}

func (s *Server) handleWebAssistantState(w http.ResponseWriter, r *http.Request, userID string, record webAssistantRecord) {
	if r.Method != http.MethodPatch {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	var input struct {
		Enabled    *bool  `json:"enabled"`
		SortOrder  *int   `json:"sort_order"`
		LastUsedAt *int64 `json:"last_used_at"`
	}
	if err := decodeWebConversationJSON(w, r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "invalid assistant state: " + err.Error()})
		return
	}
	if input.Enabled != nil {
		profile, err := s.ensureAgentProfile(s.workspaceStore, userID, record.Agent.Name)
		if err != nil {
			writeWebAssistantError(w, err)
			return
		}
		profile, err = setAgentProfileEnabled(s.workspaceStore, userID, profile, *input.Enabled)
		if err != nil {
			writeWebAssistantError(w, err)
			return
		}
		record.Agent = profile
	}
	if input.SortOrder != nil {
		if *input.SortOrder < 0 || *input.SortOrder > 10_000_000 {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "sort_order is out of range"})
			return
		}
		record.Config.SortOrder = *input.SortOrder
	}
	if input.LastUsedAt != nil {
		if *input.LastUsedAt < 0 || *input.LastUsedAt > time.Now().Add(24*time.Hour).UnixMilli() {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "last_used_at is out of range"})
			return
		}
		record.Config.LastUsedAt = input.LastUsedAt
	}
	if err := s.setWebAssistantConfig(userID, record.Agent.Name, record.Config); err != nil {
		writeWebAssistantError(w, err)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"success": true, "data": webAssistantProjection(record)})
}

func (s *Server) handleWebAssistantImport(w http.ResponseWriter, r *http.Request, userID string) {
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	var input struct {
		Assistants []webAssistantMutationInput `json:"assistants"`
	}
	if err := decodeWebConversationJSON(w, r, &input); err != nil || len(input.Assistants) == 0 || len(input.Assistants) > 100 {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "assistants must contain 1-100 records"})
		return
	}
	imported, skipped := 0, 0
	errorsOut := make([]map[string]any, 0)
	for index, item := range input.Assistants {
		record, err := s.createWebAssistant(userID, item)
		if err == nil {
			_ = record
			imported++
			continue
		}
		var requestErr *agentProfileRequestError
		if errors.As(err, &requestErr) && requestErr.Status == http.StatusConflict {
			skipped++
			continue
		}
		id := fmt.Sprintf("record-%d", index+1)
		if item.ID != "" {
			id = item.ID
		} else if item.Name != nil && strings.TrimSpace(*item.Name) != "" {
			id = strings.TrimSpace(*item.Name)
		}
		errorsOut = append(errorsOut, map[string]any{"id": id, "error": err.Error()})
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"success": true, "data": map[string]any{
		"imported": imported, "skipped": skipped, "failed": len(errorsOut), "errors": errorsOut,
	}})
}

func (s *Server) webAssistantRecords(userID string) ([]webAssistantRecord, error) {
	profiles, err := s.workspaceStore.ListAgents(userID)
	if err != nil {
		return nil, err
	}
	profilesByName := make(map[string]workspace.Agent, len(profiles))
	for _, profile := range profiles {
		profilesByName[normalizeBundledAgentName(profile.Name)] = profile
	}
	records := make([]webAssistantRecord, 0, len(profiles)+len(s.agentCatalog.Agents()))
	bundledNames := map[string]struct{}{}
	for _, bundled := range s.agentCatalog.Agents() {
		if bundled.UserHidden {
			continue
		}
		name := normalizeBundledAgentName(bundled.Name)
		bundledNames[name] = struct{}{}
		profile, customized := profilesByName[name]
		if !customized {
			profile = workspace.Agent{
				ID: webAssistantID(name), UserID: userID, Name: name,
				DisplayName: bundled.DisplayName, Description: bundled.Description,
				SystemPrompt: bundled.EffectiveSystemPrompt(), SkillNames: s.effectiveBundledAgentSkillNames(bundled.Name, bundled.SkillNames, nil),
				Enabled: bundled.Enabled,
			}
		}
		if strings.EqualFold(strings.TrimSpace(bundled.Name), "OPERON") {
			// OPERON's bundled catalog is the skill authority. The owner-scoped
			// row stores customization and tombstones; it must not freeze a copy
			// of its signed discovery allowlist or load every skill each turn.
			profile.SkillNames = append([]string(nil), bundled.SkillNames...)
		} else {
			profile.SkillNames = s.effectiveBundledAgentSkillNames(bundled.Name, profile.SkillNames, profile.SkillTombstones)
		}
		config, err := s.getWebAssistantConfig(userID, name)
		if err != nil {
			return nil, err
		}
		records = append(records, webAssistantRecord{
			Agent: profile, Bundled: true, Source: "builtin", Healthy: bundled.Healthy,
			Config: config, RuntimeID: webAssistantID(name), Deletable: false,
		})
	}
	for _, profile := range profiles {
		name := normalizeBundledAgentName(profile.Name)
		if _, bundled := bundledNames[name]; bundled {
			continue
		}
		config, err := s.getWebAssistantConfig(userID, name)
		if err != nil {
			return nil, err
		}
		records = append(records, webAssistantRecord{
			Agent: profile, Source: "user", Healthy: true, Config: config,
			RuntimeID: webAssistantID(name), Deletable: true,
		})
	}
	sort.Slice(records, func(i, j int) bool {
		leftOrder := webAssistantSortOrder(records[i])
		rightOrder := webAssistantSortOrder(records[j])
		if leftOrder != rightOrder {
			return leftOrder < rightOrder
		}
		return records[i].RuntimeID < records[j].RuntimeID
	})
	return records, nil
}

func (s *Server) webAssistantRecord(userID, assistantID string) (webAssistantRecord, bool, error) {
	records, err := s.webAssistantRecords(userID)
	if err != nil {
		return webAssistantRecord{}, false, err
	}
	for _, record := range records {
		if strings.EqualFold(record.RuntimeID, strings.TrimSpace(assistantID)) ||
			strings.EqualFold(record.Agent.ID, strings.TrimSpace(assistantID)) ||
			strings.EqualFold(record.Agent.Name, strings.TrimSpace(assistantID)) {
			return record, true, nil
		}
	}
	return webAssistantRecord{}, false, nil
}

func webAssistantProjection(record webAssistantRecord) map[string]any {
	name := strings.TrimSpace(record.Agent.DisplayName)
	if name == "" {
		name = record.Agent.Name
	}
	description := strings.TrimSpace(record.Agent.Description)
	nameI18n := cloneStringMap(record.Config.NameI18n)
	if len(nameI18n) == 0 {
		nameI18n = map[string]string{"en-US": name, "zh-CN": name}
	}
	descriptionI18n := cloneStringMap(record.Config.DescriptionI18n)
	if len(descriptionI18n) == 0 {
		descriptionI18n = map[string]string{"en-US": description, "zh-CN": description}
	}
	agentID := record.Agent.Name
	if strings.TrimSpace(record.Config.EngineAgentName) != "" && record.Bundled {
		agentID = record.Config.EngineAgentName
	}
	status := "online"
	if !record.Agent.Enabled || !record.Healthy {
		status = "offline"
	}
	projection := map[string]any{
		"id": record.RuntimeID, "source": record.Source, "name": name, "name_i18n": nameI18n,
		"description": description, "description_i18n": descriptionI18n,
		"avatar":  firstNonEmpty(record.Config.Avatar, record.Agent.IconKey, "SB"),
		"enabled": record.Agent.Enabled, "sort_order": webAssistantSortOrder(record), "agent_id": agentID,
		"agent":                   map[string]any{"type": "synonbiomed", "source": webAssistantAgentSource(record), "acp_backend": "synonbiomed"},
		"enabled_skills":          append([]string(nil), record.Agent.SkillNames...),
		"custom_skill_names":      append([]string(nil), record.Config.CustomSkillNames...),
		"disabled_builtin_skills": append([]string(nil), record.Config.DisabledBuiltinSkills...),
		"context":                 record.Agent.SystemPrompt, "context_i18n": map[string]string{},
		"prompts": append([]string(nil), record.Config.Prompts...), "prompts_i18n": cloneStringSliceMap(record.Config.PromptsI18n),
		"models": append([]string(nil), record.Config.Models...), "agent_status": status,
		"deletable": record.Deletable,
	}
	if status != "online" {
		projection["agent_status_message"] = buildinfo.Release().Name + " agent is disabled or unhealthy."
	}
	if record.Config.LastUsedAt != nil {
		projection["last_used_at"] = *record.Config.LastUsedAt
	}
	return projection
}

// The list is navigation/catalog data. The full system prompt is private
// detail state loaded only when an editor opens an assistant. Keeping it out
// of every guide/settings bootstrap removes the dominant assistant payload
// without weakening assistant execution or editing authority.
func webAssistantListProjection(record webAssistantRecord) map[string]any {
	projection := webAssistantProjection(record)
	delete(projection, "context")
	return projection
}

func (s *Server) webAssistantDetail(userID string, record webAssistantRecord) (map[string]any, error) {
	item := webAssistantProjection(record)
	defaults := normalizeStoredWebAssistantDefaults(
		record.Config.Defaults,
		record.Agent.SkillNames,
		webAssistantUsesAutomaticSkillDefaults(record),
	)
	allowedMCPIDs, err := s.webAssistantAllowedMCPIDs(userID, record)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"id": item["id"], "source": item["source"], "agent_status": item["agent_status"],
		"agent_status_message": item["agent_status_message"], "deletable": item["deletable"],
		"profile": map[string]any{
			"name": item["name"], "name_i18n": item["name_i18n"], "description": item["description"],
			"description_i18n": item["description_i18n"], "avatar": item["avatar"],
		},
		"state": map[string]any{
			"enabled": item["enabled"], "sort_order": item["sort_order"], "last_used_at": item["last_used_at"],
		},
		"engine":   map[string]any{"agent_id": item["agent_id"], "agent": item["agent"]},
		"rules":    map[string]any{"content": item["context"], "storage_mode": "synonbiomed_runtime"},
		"prompts":  map[string]any{"recommended": item["prompts"], "recommended_i18n": item["prompts_i18n"]},
		"defaults": defaults,
		"capabilities": map[string]any{
			"default_skill_ids": item["enabled_skills"], "custom_skill_names": item["custom_skill_names"],
			"default_disabled_builtin_skill_ids": item["disabled_builtin_skills"],
			"allowed_skill_ids":                  s.webAssistantAllowedSkillNames(record),
			"allowed_mcp_ids":                    allowedMCPIDs,
		},
		"preferences": map[string]any{
			"last_skill_ids": []string{}, "last_disabled_builtin_skill_ids": []string{}, "last_mcp_ids": []string{},
		},
	}, nil
}

func webAssistantSortOrder(record webAssistantRecord) int {
	if record.Config.SortOrder > 0 {
		return record.Config.SortOrder
	}
	if record.Bundled {
		return 900_000
	}
	return 800_000
}

func webAssistantAgentSource(record webAssistantRecord) string {
	if record.Bundled {
		return "builtin"
	}
	return "custom"
}

func webAssistantID(agentName string) string {
	slug := strings.ToLower(strings.ReplaceAll(normalizeBundledAgentName(agentName), "_", "-"))
	return "synonbiomed:" + slug
}

func (s *Server) resolveWebAssistantEngineAgent(userID, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", &agentProfileRequestError{Status: http.StatusBadRequest, Detail: "agent_id is required"}
	}
	if record, found, err := s.webAssistantRecord(userID, value); err != nil {
		return "", err
	} else if found && record.Agent.Enabled {
		return record.Agent.Name, nil
	}
	name := normalizeBundledAgentName(value)
	if bundled, found := s.agentCatalog.Agent(name); found && bundled.Enabled {
		return bundled.Name, nil
	}
	if profile, found, err := s.workspaceStore.GetAgent(userID, name); err != nil {
		return "", err
	} else if found && profile.Enabled {
		return profile.Name, nil
	}
	return "", &agentProfileRequestError{Status: http.StatusBadRequest, Detail: "selected agent is unavailable or disabled"}
}

func (s *Server) resolveWebAssistantRuntimeAgent(userID, assistantID string) (string, error) {
	record, found, err := s.webAssistantRecord(userID, assistantID)
	if err != nil {
		return "", err
	}
	if !found {
		return "", &agentProfileRequestError{Status: http.StatusBadRequest, Detail: "selected assistant is unavailable or disabled"}
	}
	return s.webAssistantRuntimeAgentForRecord(userID, record)
}

func (s *Server) webAssistantRuntimeAgentForRecord(userID string, record webAssistantRecord) (string, error) {
	if !record.Agent.Enabled {
		return "", &agentProfileRequestError{Status: http.StatusBadRequest, Detail: "selected assistant is unavailable or disabled"}
	}
	if record.Bundled && strings.TrimSpace(record.Config.EngineAgentName) != "" {
		return s.resolveWebAssistantEngineAgent(userID, record.Config.EngineAgentName)
	}
	return record.Agent.Name, nil
}

func (s *Server) mergeWebAssistantConfig(
	userID string,
	record webAssistantRecord,
	input webAssistantMutationInput,
	skills []string,
) (webAssistantStoredConfig, error) {
	config := record.Config
	if input.Avatar != nil {
		config.Avatar = strings.TrimSpace(*input.Avatar)
	}
	if input.NameI18n != nil {
		config.NameI18n = cloneStringMap(*input.NameI18n)
	}
	if input.DescriptionI18n != nil {
		config.DescriptionI18n = cloneStringMap(*input.DescriptionI18n)
	}
	if input.Prompts != nil {
		config.Prompts = boundedNonEmptyStrings(*input.Prompts, 64, 1000)
	}
	if input.RecommendedPrompts != nil {
		config.Prompts = boundedNonEmptyStrings(*input.RecommendedPrompts, 64, 1000)
	}
	if input.PromptsI18n != nil {
		config.PromptsI18n = cloneStringSliceMap(*input.PromptsI18n)
	}
	if input.RecommendedI18n != nil {
		config.PromptsI18n = cloneStringSliceMap(*input.RecommendedI18n)
	}
	if input.Models != nil {
		config.Models = boundedNonEmptyStrings(*input.Models, 128, 256)
	}
	if input.CustomSkillNames != nil {
		config.CustomSkillNames = uniqueSortedStrings(*input.CustomSkillNames)
	}
	if input.DisabledBuiltinSkills != nil {
		config.DisabledBuiltinSkills = uniqueSortedStrings(*input.DisabledBuiltinSkills)
	}
	if input.AgentID != nil {
		engineName, err := s.resolveWebAssistantEngineAgent(userID, *input.AgentID)
		if err != nil {
			return webAssistantStoredConfig{}, err
		}
		config.EngineAgentName = engineName
	}
	record.Config = config
	if input.Defaults != nil {
		allowedMCPIDs, err := s.webAssistantAllowedMCPIDs(userID, record)
		if err != nil {
			return webAssistantStoredConfig{}, err
		}
		defaults, err := s.validateWebAssistantDefaults(
			userID, input.Defaults, skills, config.DisabledBuiltinSkills, config.Models, allowedMCPIDs,
		)
		if err != nil {
			return webAssistantStoredConfig{}, &agentProfileRequestError{Status: http.StatusBadRequest, Detail: err.Error()}
		}
		config.Defaults = defaults
	}
	if err := s.setWebAssistantConfig(userID, record.Agent.Name, config); err != nil {
		return webAssistantStoredConfig{}, err
	}
	return config, nil
}

func (s *Server) validateWebAssistantDefaults(
	userID string,
	raw map[string]any,
	skills []string,
	disabledSkills []string,
	configuredModels []string,
	allowedMCPIDs []string,
) (map[string]any, error) {
	result := normalizeStoredWebAssistantDefaults(nil, skills, false)
	for key, value := range raw {
		switch key {
		case "model", "permission", "thought_level":
			option, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("defaults.%s must be an object", key)
			}
			for optionKey := range option {
				if optionKey != "mode" && optionKey != "value" {
					return nil, fmt.Errorf("defaults.%s contains unknown field %s", key, optionKey)
				}
			}
			mode := strings.ToLower(strings.TrimSpace(webString(option["mode"])))
			if mode != "auto" && mode != "fixed" {
				return nil, fmt.Errorf("defaults.%s.mode must be auto or fixed", key)
			}
			next := map[string]any{"mode": mode}
			if mode == "fixed" {
				selected := strings.TrimSpace(webString(option["value"]))
				if selected == "" || len(selected) > 256 {
					return nil, fmt.Errorf("defaults.%s.value is required for fixed mode", key)
				}
				switch key {
				case "permission":
					if !webAssistantPermissionMode(strings.ToLower(selected)) {
						return nil, errors.New("defaults.permission.value is not a supported permission mode")
					}
				case "thought_level":
					if !webAssistantThoughtLevel(strings.ToLower(selected)) {
						return nil, errors.New("defaults.thought_level.value is not a supported thought level")
					}
				case "model":
					allowed, err := s.webAssistantDefaultModelNames(userID, configuredModels)
					if err != nil {
						return nil, err
					}
					if len(allowed) > 0 {
						if _, found := normalizedSkillNameSet(allowed)[strings.ToLower(selected)]; !found {
							return nil, fmt.Errorf("defaults.model.value %q is unavailable", selected)
						}
					}
				}
				next["value"] = selected
			}
			result[key] = next
		case "skills", "mcps":
			option, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("defaults.%s must be an object", key)
			}
			for optionKey := range option {
				if optionKey != "mode" && optionKey != "value" {
					return nil, fmt.Errorf("defaults.%s contains unknown field %s", key, optionKey)
				}
			}
			mode := strings.ToLower(strings.TrimSpace(webString(option["mode"])))
			if mode != "auto" && mode != "fixed" {
				return nil, fmt.Errorf("defaults.%s.mode must be auto or fixed", key)
			}
			values, err := webAssistantStringList(option["value"], 256, 256)
			if err != nil {
				return nil, fmt.Errorf("defaults.%s.value: %w", key, err)
			}
			if key == "skills" {
				if err := validateWebAssistantSkillSelection(values, skills, disabledSkills); err != nil {
					return nil, fmt.Errorf("defaults.skills.value: %w", err)
				}
			} else if len(values) > 0 {
				allowed := normalizedSkillNameSet(allowedMCPIDs)
				for _, selected := range values {
					if _, found := allowed[strings.ToLower(selected)]; !found {
						return nil, fmt.Errorf("defaults.mcps.value contains unavailable MCP %q", selected)
					}
				}
			}
			result[key] = map[string]any{"mode": mode, "value": values}
		default:
			return nil, fmt.Errorf("defaults contains unknown field %s", key)
		}
	}
	return result, nil
}

func (s *Server) webAssistantDefaultModelNames(userID string, configured []string) ([]string, error) {
	models := appendUniqueFolded(nil, configured...)
	choices, err := s.webConversationModelChoices(userID)
	if err != nil {
		return nil, err
	}
	for _, choice := range choices {
		models = appendUniqueFolded(models, choice.Value)
	}
	return models, nil
}

func normalizeStoredWebAssistantDefaults(raw map[string]any, skills []string, automaticSkills bool) map[string]any {
	skillDefaults := map[string]any{"mode": "fixed", "value": append([]string(nil), skills...)}
	if automaticSkills {
		skillDefaults = map[string]any{"mode": "auto"}
	}
	result := map[string]any{
		"model": map[string]any{"mode": "auto"}, "permission": map[string]any{"mode": "auto"},
		"thought_level": map[string]any{"mode": "auto"},
		"skills":        skillDefaults,
		// An absent MCP default means that the runtime agent's current,
		// owner-scoped connector authority applies. Treating absence as an
		// explicit empty fixed selection silently removed every connected
		// connector from built-in OPERON conversations.
		"mcps": map[string]any{"mode": "auto"},
	}
	for _, key := range []string{"model", "permission", "thought_level", "skills", "mcps"} {
		if value, found := raw[key]; found {
			result[key] = value
		}
	}
	return result
}

func webAssistantUsesAutomaticSkillDefaults(record webAssistantRecord) bool {
	return record.Bundled && strings.EqualFold(strings.TrimSpace(record.Agent.Name), "OPERON")
}

func webAssistantStringList(value any, maxItems, maxLength int) ([]string, error) {
	if value == nil {
		return []string{}, nil
	}
	raw, ok := value.([]any)
	if !ok {
		if stringsValue, ok := value.([]string); ok {
			raw = make([]any, len(stringsValue))
			for index := range stringsValue {
				raw[index] = stringsValue[index]
			}
		} else {
			return nil, errors.New("must be an array of strings")
		}
	}
	if len(raw) > maxItems {
		return nil, fmt.Errorf("contains more than %d entries", maxItems)
	}
	values := make([]string, 0, len(raw))
	for _, item := range raw {
		text, ok := item.(string)
		text = strings.TrimSpace(text)
		if !ok || text == "" || len(text) > maxLength {
			return nil, fmt.Errorf("entries must be non-empty strings of at most %d bytes", maxLength)
		}
		values = append(values, text)
	}
	return uniqueSortedStrings(values), nil
}

func (s *Server) getWebAssistantConfig(userID, agentName string) (webAssistantStoredConfig, error) {
	if s.settingsStore == nil {
		return webAssistantStoredConfig{}, errors.New("assistant settings storage is not configured")
	}
	setting, found, err := s.settingsStore.Get(webAssistantConfigKey(userID, agentName))
	if err != nil || !found {
		return webAssistantStoredConfig{}, err
	}
	raw, err := json.Marshal(setting.Value)
	if err != nil {
		return webAssistantStoredConfig{}, err
	}
	var config webAssistantStoredConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return webAssistantStoredConfig{}, fmt.Errorf("decode assistant settings: %w", err)
	}
	return config, nil
}

func (s *Server) setWebAssistantConfig(userID, agentName string, config webAssistantStoredConfig) error {
	if s.settingsStore == nil {
		return errors.New("assistant settings storage is not configured")
	}
	_, err := s.settingsStore.Set(webAssistantConfigKey(userID, agentName), config)
	return err
}

func (s *Server) deleteWebAssistantConfig(userID, agentName string) error {
	if s.settingsStore == nil {
		return errors.New("assistant settings storage is not configured")
	}
	_, err := s.settingsStore.Delete(webAssistantConfigKey(userID, agentName))
	return err
}

func webAssistantConfigKey(userID, agentName string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(userID)))
	return "web." + webAssistantSettingNamespace + hex.EncodeToString(digest[:16]) + "." + strings.ToLower(normalizeBundledAgentName(agentName))
}

func boundedNonEmptyStrings(values []string, maxItems, maxLength int) []string {
	if len(values) > maxItems {
		values = values[:maxItems]
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if len(value) > maxLength {
			value = value[:maxLength]
		}
		result = append(result, value)
	}
	return result
}

func cloneStringMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		if key = strings.TrimSpace(key); key != "" && len(key) <= 32 {
			result[key] = value
		}
	}
	return result
}

func cloneStringSliceMap(source map[string][]string) map[string][]string {
	result := make(map[string][]string, len(source))
	for key, value := range source {
		if key = strings.TrimSpace(key); key != "" && len(key) <= 32 {
			result[key] = boundedNonEmptyStrings(value, 64, 1000)
		}
	}
	return result
}

func writeWebAssistantError(w http.ResponseWriter, err error) {
	var requestErr *agentProfileRequestError
	if errors.As(err, &requestErr) {
		writeWorkspaceJSON(w, requestErr.Status, map[string]any{"message": requestErr.Detail})
		return
	}
	writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"message": "unable to process assistant request"})
}

func (s *Server) handleWebAssistantRules(w http.ResponseWriter, r *http.Request) {
	if s.workspaceStore == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "assistant storage is not configured"})
		return
	}
	userID := strings.TrimSpace(r.Header.Get("X-Synon-User-Id"))
	if userID == "" {
		writeWorkspaceJSON(w, http.StatusUnauthorized, map[string]any{"message": "authentication required"})
		return
	}
	switch r.URL.Path {
	case "/api/skills/assistant-rule/read":
		if r.Method != http.MethodPost {
			writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
			return
		}
		var input struct {
			AssistantID string `json:"assistant_id"`
			Locale      string `json:"locale"`
		}
		if err := decodeWebConversationJSON(w, r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "invalid assistant rule request"})
			return
		}
		record, found, err := s.webAssistantRecord(userID, input.AssistantID)
		if err != nil || !found {
			writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "assistant not found"})
			return
		}
		content := ""
		if prompt, found, err := s.workspaceStore.GetCustomAgentPrompt(userID, record.Agent.Name); err != nil {
			writeWebAssistantError(w, err)
			return
		} else if found {
			content = prompt.PromptText
		} else if !record.Bundled {
			content = record.Agent.SystemPrompt
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"success": true, "data": content})
	case "/api/skills/assistant-rule/write":
		if r.Method != http.MethodPost {
			writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
			return
		}
		var input struct {
			AssistantID string `json:"assistant_id"`
			Content     string `json:"content"`
			Locale      string `json:"locale"`
		}
		if err := decodeWebConversationJSON(w, r, &input); err != nil || strings.TrimSpace(input.Content) == "" || len(input.Content) > 64*1024 {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "assistant rule content must be 1-65536 bytes"})
			return
		}
		record, found, err := s.webAssistantRecord(userID, input.AssistantID)
		if err != nil || !found {
			writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "assistant not found"})
			return
		}
		if _, err := s.workspaceStore.UpsertCustomAgentPrompt(userID, record.Agent.Name, input.Content); err != nil {
			writeWebAssistantError(w, err)
			return
		}
		if !record.Bundled {
			prompt := input.Content
			if _, _, err := s.updateAgentProfile(s.workspaceStore, userID, record.Agent, agentProfilePatchRequest{SystemPrompt: &prompt}); err != nil {
				_ = s.workspaceStore.DeleteCustomAgentPrompt(userID, record.Agent.Name)
				writeWebAssistantError(w, err)
				return
			}
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"success": true, "data": true})
	default:
		if !strings.HasPrefix(r.URL.Path, "/api/skills/assistant-rule/") || r.Method != http.MethodDelete {
			writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "assistant rule endpoint not found"})
			return
		}
		assistantID, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/api/skills/assistant-rule/"))
		if err != nil || strings.TrimSpace(assistantID) == "" {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "invalid assistant id"})
			return
		}
		record, found, err := s.webAssistantRecord(userID, assistantID)
		if err != nil || !found {
			writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "assistant not found"})
			return
		}
		if err := s.workspaceStore.DeleteCustomAgentPrompt(userID, record.Agent.Name); err != nil && !strings.Contains(strings.ToLower(err.Error()), "not found") {
			writeWebAssistantError(w, err)
			return
		}
		if !record.Bundled && record.Agent.SystemPrompt != "" {
			empty := ""
			if _, _, err := s.updateAgentProfile(s.workspaceStore, userID, record.Agent, agentProfilePatchRequest{SystemPrompt: &empty}); err != nil {
				writeWebAssistantError(w, err)
				return
			}
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"success": true, "data": true})
	}
}

func (s *Server) handleWebManagedExperts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	userID := strings.TrimSpace(r.Header.Get("X-Synon-User-Id"))
	if userID == "" {
		writeWorkspaceJSON(w, http.StatusUnauthorized, map[string]any{"message": "authentication required"})
		return
	}
	records, err := s.webAssistantRecords(userID)
	if err != nil {
		writeWebAssistantError(w, err)
		return
	}
	modelSnapshot, err := s.webManagedModelSnapshot(userID)
	if err != nil {
		writeWebAssistantError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(records))
	for _, record := range records {
		commands, err := s.webAgentSlashCommands(userID, record.Agent.Name)
		if err != nil {
			writeWebAssistantError(w, err)
			return
		}
		availableCommands := make([]map[string]any, 0, len(commands))
		for _, command := range commands {
			availableCommands = append(availableCommands, map[string]any{
				"name": command["command"], "description": command["description"],
			})
		}
		status := "online"
		if !record.Agent.Enabled || !record.Healthy {
			status = "offline"
		}
		configOptions := map[string]any{"config_options": []any{}}
		if modelSnapshot.Option != nil {
			configOptions["config_options"] = []any{modelSnapshot.Option}
		}
		availableCommandsPayload := map[string]any{"available_commands": availableCommands}
		item := map[string]any{
			"id": record.Agent.Name, "icon": firstNonEmpty(record.Agent.IconKey, "activity"),
			"avatar": firstNonEmpty(record.Config.Avatar, record.Agent.IconKey, "SB"),
			"name":   record.Agent.DisplayName, "name_i18n": map[string]string{"en-US": record.Agent.DisplayName, "zh-CN": record.Agent.DisplayName},
			"description":      record.Agent.Description,
			"description_i18n": map[string]string{"en-US": record.Agent.Description, "zh-CN": record.Agent.Description},
			"backend":          "synonbiomed", "agent_type": "synonbiomed", "agent_source": webAssistantAgentSource(record),
			"agent_source_info": map[string]any{"version": buildinfo.Release().Version},
			"enabled":           record.Agent.Enabled, "installed": true, "status": status,
			"behavior_policy": map[string]any{"supports_side_question": true},
			"config_options":  configOptions, "available_modes": map[string]any{
				"current_mode_id": "default", "available_modes": []map[string]any{{
					"id": "default", "name": "Default", "description": buildinfo.Release().Name + " biomedical agent routing",
				}},
			},
			"available_models": modelSnapshot.Available, "available_commands": availableCommandsPayload,
			"handshake": map[string]any{
				"config_options": configOptions, "available_modes": map[string]any{
					"current_mode_id": "default", "available_modes": []map[string]any{{"id": "default", "name": "Default"}},
				},
				"available_models": modelSnapshot.Available, "available_commands": availableCommandsPayload,
				"agent_capabilities": map[string]any{"load_session": true, "mcp_capabilities": map[string]bool{"stdio": true, "http": true, "sse": true}},
			},
			"command": "synon-go", "args": []string{"serve"},
		}
		items = append(items, item)
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"success": true, "data": items})
}

type webManagedModelSnapshot struct {
	Option    map[string]any
	Available map[string]any
}

func (s *Server) webManagedModelSnapshot(userID string) (webManagedModelSnapshot, error) {
	option, found, err := s.webConversationModelOption(userID)
	if err != nil {
		return webManagedModelSnapshot{}, err
	}
	available := map[string]any{"current_model_id": nil, "current_model_label": nil, "available_models": []any{}}
	if !found {
		return webManagedModelSnapshot{Available: available}, nil
	}
	current := strings.TrimSpace(webString(option["current_value"]))
	models := make([]map[string]any, 0)
	if rawOptions, ok := option["options"].([]map[string]any); ok {
		for _, raw := range rawOptions {
			model := map[string]any{
				"id": webString(raw["value"]), "label": webString(raw["name"]), "description": webString(raw["description"]),
			}
			models = append(models, model)
			if model["id"] == current {
				available["current_model_label"] = model["label"]
			}
		}
	}
	if current != "" {
		available["current_model_id"] = current
	}
	available["available_models"] = models
	return webManagedModelSnapshot{Option: option, Available: available}, nil
}

func (s *Server) webAgentSlashCommands(userID, agentName string) ([]map[string]any, error) {
	if s.skillCatalog == nil {
		return []map[string]any{}, nil
	}
	selected := map[string]struct{}{}
	unrestricted := false
	if profile, found, err := s.workspaceStore.GetAgent(userID, normalizeBundledAgentName(agentName)); err != nil {
		return nil, err
	} else if found {
		unrestricted = profile.Unrestricted
		for _, name := range s.effectiveBundledAgentSkillNames(profile.Name, profile.SkillNames, profile.SkillTombstones) {
			selected[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
		}
		for _, name := range profile.SkillTombstones {
			delete(selected, strings.ToLower(strings.TrimSpace(name)))
		}
	} else if bundled, found := s.agentCatalog.Agent(agentName); found {
		for _, name := range s.effectiveBundledAgentSkillNames(bundled.Name, bundled.SkillNames, nil) {
			selected[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
		}
	}
	preferences, err := s.workspaceStore.ListSkillPreferences(userID)
	if err != nil {
		return nil, err
	}
	commands := make([]map[string]any, 0)
	for _, skill := range s.skillCatalog.Skills() {
		if compatibilityRequiredPlatformSkill(skill) {
			continue
		}
		if enabled, configured := preferences[skill.Name]; configured && !enabled {
			continue
		}
		if !unrestricted {
			if _, enabled := selected[strings.ToLower(strings.TrimSpace(skill.Name))]; !enabled {
				continue
			}
		}
		commands = append(commands, map[string]any{"command": skill.Name, "description": strings.TrimSpace(skill.Description)})
	}
	sort.Slice(commands, func(i, j int) bool {
		return strings.ToLower(webString(commands[i]["command"])) < strings.ToLower(webString(commands[j]["command"]))
	})
	return commands, nil
}
