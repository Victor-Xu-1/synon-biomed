package server

import (
	"net/http"
	"slices"
)

func (s *Server) handleScientificRuntimeWarmups(w http.ResponseWriter, r *http.Request) {
	if s.settingsStore == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{
			"detail": "settings store is not configured",
		})
		return
	}

	switch r.Method {
	case http.MethodGet:
		selected, configured, err := s.loadScientificRuntimeWarmupSelection()
		if err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, s.scientificRuntimeWarmupSelectionPayload(selected, configured))
	case http.MethodPut:
		var input struct {
			EnabledIDs []string `json:"enabled_ids"`
		}
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
		if input.EnabledIDs == nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"detail": "enabled_ids: string[] required"})
			return
		}
		normalized, err := normalizeScientificRuntimeWarmupIDs(input.EnabledIDs)
		if err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
		selected, err := s.storeScientificRuntimeWarmupSelection(normalized)
		if err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, s.scientificRuntimeWarmupSelectionPayload(selected, true))
	case http.MethodPost:
		var input struct {
			ID string `json:"id"`
		}
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid runtime retry request"})
			return
		}
		if _, found := scientificRuntimeWarmupDefinitionByID(input.ID); !found {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"detail": "runtime is not registered"})
			return
		}
		selected, configured, err := s.loadScientificRuntimeWarmupSelection()
		if err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"detail": "runtime selection is unavailable"})
			return
		}
		if !configured || !slices.Contains(selected, input.ID) {
			writeWorkspaceJSON(w, http.StatusConflict, map[string]any{"detail": "select this runtime before preparing it"})
			return
		}
		if !s.ManagedEnvironmentSupervisorEnabled() || !s.queueScientificRuntimeWarmup(input.ID) {
			writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "runtime preparation is unavailable"})
			return
		}
		writeWorkspaceJSON(w, http.StatusAccepted, map[string]any{"id": input.ID, "runtime": scientificRuntimeWarmupHealthValue(s.scientificRuntimeWarmupStatus(input.ID))})
	default:
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
	}
}

func (s *Server) scientificRuntimeWarmupSelectionPayload(selected []string, configured bool) map[string]any {
	selectedSet := make(map[string]struct{}, len(selected))
	for _, id := range selected {
		selectedSet[id] = struct{}{}
	}
	definitions := scientificRuntimeWarmupDefinitions()
	options := make([]map[string]any, 0, len(definitions))
	selectedInstallBytes := int64(0)
	for _, definition := range definitions {
		_, enabled := selectedSet[definition.ID]
		if enabled {
			selectedInstallBytes += definition.EstimatedInstallBytes
		}
		options = append(options, map[string]any{
			"id":                      definition.ID,
			"estimated_install_bytes": definition.EstimatedInstallBytes,
			"estimated_install_mb":    definition.EstimatedInstallBytes / (1024 * 1024),
			"default_enabled":         definition.DefaultEnabled,
			"selected":                enabled,
			"available":               s.ManagedEnvironmentSupervisorEnabled(),
			"runtime":                 scientificRuntimeWarmupHealthValue(s.scientificRuntimeWarmupStatus(definition.ID)),
		})
	}
	return map[string]any{
		"configured":             configured,
		"selected_install_bytes": selectedInstallBytes,
		"selected_install_mb":    selectedInstallBytes / (1024 * 1024),
		"options":                options,
	}
}
