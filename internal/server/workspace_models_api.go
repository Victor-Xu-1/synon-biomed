package server

import (
	"net/http"
	"net/url"
	"sort"
	"strings"

	workspace "synon-go/internal/persistence/workspace"
)

type workspaceModelProjection struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Object      string `json:"object"`
	DisplayName string `json:"display_name"`
	Description string `json:"description,omitempty"`
	Provider    string `json:"provider"`
	ProfileID   string `json:"profile_id"`
	Model       string `json:"model"`
	Active      bool   `json:"active"`
	OwnedBy     string `json:"owned_by"`
}

type compatibilityModelProjection struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Overflow bool   `json:"overflow"`
}

func (s *Server) handleWorkspaceModels(w http.ResponseWriter, r *http.Request) {
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	userID := compatAgentUserID(r)
	providers, err := store.ListModelProviders(userID)
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	activeProfileID := s.activeWorkspaceModelProfileID(providers)
	seen := make(map[string]struct{}, len(providers))
	models := make([]workspaceModelProjection, 0, len(providers))
	compatibilityModels := make([]compatibilityModelProjection, 0, len(providers))
	defaultModelID := ""
	for _, provider := range providers {
		model := strings.TrimSpace(provider.Model)
		if !provider.Enabled || model == "" {
			continue
		}
		active := provider.ID == activeProfileID
		selectorID := "profile:" + strings.ReplaceAll(url.QueryEscape(provider.ID), "+", "%20")
		if active {
			selectorID = model
		}
		if _, exists := seen[selectorID]; exists {
			continue
		}
		seen[selectorID] = struct{}{}
		description := modelProviderDescription(provider)
		projection := workspaceModelProjection{
			ID: selectorID, Type: "model", Object: "model", DisplayName: model,
			Description: description, Provider: provider.Type, ProfileID: provider.ID,
			Model: model, Active: active, OwnedBy: provider.Type,
		}
		models = append(models, projection)
		compatibilityModels = append(compatibilityModels, compatibilityModelProjection{
			ID: selectorID, Name: model, Overflow: false,
		})
		if active || defaultModelID == "" {
			defaultModelID = selectorID
		}
	}
	if r.URL.Path == "/api/models" {
		provider := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("provider")))
		if provider != "" && provider != "synon_llm" {
			writeV11Detail(w, http.StatusNotFound, "Provider '"+r.URL.Query().Get("provider")+"' not found. Available: [\"synon_llm\"]")
			return
		}
		sort.Slice(compatibilityModels, func(i, j int) bool {
			return compatibilityModels[i].ID < compatibilityModels[j].ID
		})
		key := defaultModelID
		if key == "" {
			key = "third_party_llm"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"models": map[string]any{key: compatibilityModels}, "default_model_id": defaultModelID,
		})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"object": "list", "data": models, "default_model_id": defaultModelID, "has_more": false,
	})
}

func (s *Server) activeWorkspaceModelProfileID(providers []workspace.ModelProvider) string {
	configured := ""
	if s != nil && s.settingsStore != nil {
		if setting, found, err := s.settingsStore.Get(webConversationActiveProviderSetting); err == nil && found {
			configured = strings.TrimSpace(webString(setting.Value))
		}
	}
	for _, provider := range providers {
		if provider.ID == configured && provider.Enabled && strings.TrimSpace(provider.Model) != "" {
			return provider.ID
		}
	}
	for _, provider := range providers {
		if provider.Enabled && strings.TrimSpace(provider.Model) != "" {
			return provider.ID
		}
	}
	return ""
}

func modelProviderDescription(provider workspace.ModelProvider) string {
	name := strings.TrimSpace(provider.Name)
	providerType := strings.TrimSpace(provider.Type)
	switch {
	case name == "":
		return providerType
	case providerType == "", strings.EqualFold(name, providerType):
		return name
	default:
		return name + "  -  " + providerType
	}
}
