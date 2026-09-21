package server

import (
	"net/http"
	"slices"
	"strings"

	"github.com/google/uuid"

	kernelruntime "synon-go/internal/kernel"
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
			ID     string `json:"id"`
			Action string `json:"action"`
		}
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid runtime retry request"})
			return
		}
		if _, found := scientificRuntimeWarmupDefinitionByID(input.ID); !found {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"detail": "runtime is not registered"})
			return
		}
		action := strings.TrimSpace(input.Action)
		if action == "" {
			action = "retry"
		}
		selected, configured, err := s.loadScientificRuntimeWarmupSelection()
		if err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"detail": "runtime selection is unavailable"})
			return
		}
		// Uninstalling a ready environment is a deactivation operation, not a
		// preparation admission; it must remain available even after the user
		// has removed the runtime from the future-download selection.
		if action != "uninstall" && (!configured || !slices.Contains(selected, input.ID)) {
			writeWorkspaceJSON(w, http.StatusConflict, map[string]any{"detail": "select this runtime before preparing it"})
			return
		}
		switch action {
		case "pause":
			if !s.cancelScientificRuntimeWarmup(input.ID) {
				// A queued item has not acquired an operation context yet. Removing
				// it from the persisted selection is the durable cancellation fence;
				// the dequeue check will discard the stale wake-up.
			}
			selected = removeScientificRuntimeID(selected, input.ID)
			if _, err := s.storeScientificRuntimeWarmupSelection(selected); err != nil {
				writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"detail": "runtime pause could not be persisted"})
				return
			}
			s.setScientificRuntimeWarmupStatus(input.ID, scientificRuntimeWarmupStatus{
				State: "stopped", MaxAttempts: len(scientificRuntimeWarmupRetryDelays),
			})
			writeWorkspaceJSON(w, http.StatusAccepted, map[string]any{
				"id": input.ID, "action": action, "runtime": scientificRuntimeWarmupHealthValue(s.scientificRuntimeWarmupStatus(input.ID)),
			})
			return
		case "uninstall":
			status := s.scientificRuntimeWarmupStatus(input.ID)
			if activeEnvironmentWarmupState(status.State) {
				writeWorkspaceJSON(w, http.StatusConflict, map[string]any{"detail": "pause runtime preparation before uninstalling it"})
				return
			}
			if status.State == "ready" {
				if s.kernelManager == nil || strings.TrimSpace(status.Environment) == "" || strings.TrimSpace(status.Generation) == "" {
					writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "runtime deactivation is unavailable"})
					return
				}
				s.setScientificRuntimeWarmupStatus(input.ID, scientificRuntimeWarmupStatus{
					State: "uninstalling", MaxAttempts: len(scientificRuntimeWarmupRetryDelays),
					Environment: status.Environment, Generation: status.Generation,
				})
				if err := s.kernelManager.DeleteManagedEnvironment(r.Context(), kernelruntime.DeleteManagedEnvironmentInput{
					Name: status.Environment, ExpectedGeneration: status.Generation,
					OperationID: "scientific-runtime-uninstall-" + uuid.NewString(),
				}); err != nil {
					s.setScientificRuntimeWarmupStatus(input.ID, scientificRuntimeWarmupStatus{
						State: "failed", MaxAttempts: len(scientificRuntimeWarmupRetryDelays),
						Environment: status.Environment, Generation: status.Generation,
						LastErrorCode: "runtime_uninstall_failed",
					})
					writeWorkspaceJSON(w, http.StatusConflict, map[string]any{"detail": "runtime could not be deactivated"})
					return
				}
			}
			selected = removeScientificRuntimeID(selected, input.ID)
			if _, err := s.storeScientificRuntimeWarmupSelection(selected); err != nil {
				writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"detail": "runtime uninstall selection could not be persisted"})
				return
			}
			s.setScientificRuntimeWarmupStatus(input.ID, scientificRuntimeWarmupStatus{
				State: "waiting_for_selection", MaxAttempts: len(scientificRuntimeWarmupRetryDelays),
			})
			writeWorkspaceJSON(w, http.StatusAccepted, map[string]any{
				"id": input.ID, "action": action, "runtime": scientificRuntimeWarmupHealthValue(s.scientificRuntimeWarmupStatus(input.ID)),
			})
			return
		case "retry":
			// Continue through the existing bounded retry admission below.
		default:
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"detail": "unsupported runtime action"})
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

func activeEnvironmentWarmupState(state string) bool {
	switch state {
	case "scheduled", "preparing", "retrying", "uninstalling":
		return true
	default:
		return false
	}
}

func removeScientificRuntimeID(values []string, id string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != id {
			result = append(result, value)
		}
	}
	return result
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
		packages := []map[string]string{}
		if request, _, err := definition.BuildRequest(); err == nil {
			for _, item := range request.Packages {
				packages = append(packages, map[string]string{"manager": string(item.Manager), "spec": item.Spec})
			}
		}
		_, enabled := selectedSet[definition.ID]
		if enabled {
			selectedInstallBytes += definition.EstimatedInstallBytes
		}
		options = append(options, map[string]any{
			"id":                      definition.ID,
			"packages":                packages,
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
