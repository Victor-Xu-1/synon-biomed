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
		if !isRequiredScientificRuntimeID(input.ID) {
			if _, found := scientificRuntimeWarmupDefinitionByID(input.ID); !found {
				writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"detail": "runtime is not registered"})
				return
			}
		}
		action := strings.TrimSpace(input.Action)
		if action == "" {
			action = "retry"
		}
		if isRequiredScientificRuntimeID(input.ID) {
			// Required runtimes are owned by the service supervisor. They cannot
			// be paused or uninstalled, but an explicit retry remains useful for
			// recovery after a failed startup attempt.
			if action != "retry" {
				writeWorkspaceJSON(w, http.StatusConflict, map[string]any{"detail": "required scientific runtimes cannot be paused or uninstalled"})
				return
			}
			if s.kernelManager == nil {
				writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "required runtime provisioning is unavailable"})
				return
			}
			var err error
			if input.ID == managedPythonScientificRuntimeID {
				err = s.kernelManager.RetryManagedPythonEnvironment(r.Context())
			} else {
				err = s.kernelManager.RetryManagedREnvironment(r.Context())
			}
			if err != nil {
				writeWorkspaceJSON(w, http.StatusConflict, map[string]any{"detail": "required runtime preparation failed"})
				return
			}
			writeWorkspaceJSON(w, http.StatusAccepted, map[string]any{
				"id": input.ID, "action": action, "runtime": s.managedScientificRuntimeHealth(input.ID),
			})
			return
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
			if !s.pauseScientificRuntimeWarmup(input.ID) {
				writeWorkspaceJSON(w, http.StatusConflict, map[string]any{"detail": "runtime is not being prepared"})
				return
			}
			selected = removeScientificRuntimeID(selected, input.ID)
			if _, err := s.storeScientificRuntimeWarmupSelection(selected); err != nil {
				writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"detail": "runtime pause could not be persisted"})
				return
			}
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
	options := make([]map[string]any, 0, len(definitions)+2)
	selectedInstallBytes := int64(0)
	for _, id := range []string{managedPythonScientificRuntimeID, managedRScientificRuntimeID} {
		options = append(options, s.managedScientificRuntimeOption(id))
	}
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
			"kind":                    "optional",
			"required":                false,
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

// managedScientificRuntimeOption exposes the two service-owned runtimes using
// the same bounded catalog shape as optional warmups. The option deliberately
// contains no filesystem path; the kernel resolves the user's managed root at
// runtime and only returns a stable environment identity.
func (s *Server) managedScientificRuntimeOption(id string) map[string]any {
	name := id
	language := "python"
	available := false
	packages := []map[string]string{}
	if s != nil && s.kernelManager != nil {
		if id == managedPythonScientificRuntimeID {
			name = s.kernelManager.ManagedPythonEnvironmentName()
			available = s.kernelManager.ManagedPythonProvisioningEnabled()
			if values, err := s.kernelManager.ManagedPythonPackages(); err == nil {
				packages = managedScientificRuntimePackageOptions(values)
			}
		} else {
			name = s.kernelManager.ManagedREnvironmentName()
			language = "r"
			available = s.kernelManager.ManagedRProvisioningEnabled()
			if values, err := s.kernelManager.ManagedRPackages(); err == nil {
				packages = managedScientificRuntimePackageOptions(values)
			}
		}
	}
	status := s.managedScientificRuntimeHealth(id)
	if statusValue, ok := status["status"].(string); ok && statusValue == "ready" &&
		(id != managedRScientificRuntimeID || (s != nil && s.kernelManager != nil && s.kernelManager.ManagedRProvisioningEnabled())) {
		available = true
	}
	return map[string]any{
		"id":                      id,
		"kind":                    "core",
		"required":                true,
		"language":                language,
		"packages":                packages,
		"estimated_install_bytes": int64(0),
		"estimated_install_mb":    int64(0),
		"default_enabled":         true,
		"selected":                true,
		"available":               available,
		"runtime":                 status,
		"environment":             name,
	}
}

func managedScientificRuntimePackageOptions(values []string) []map[string]string {
	result := make([]map[string]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		result = append(result, map[string]string{"manager": "conda", "spec": value})
	}
	return result
}

func (s *Server) managedScientificRuntimeHealth(id string) map[string]any {
	state := scientificRuntimeWarmupStatus{
		State:       "disabled",
		MaxAttempts: 1,
	}
	var environmentName string
	if s != nil && s.kernelManager != nil {
		pythonUnavailable := id == managedPythonScientificRuntimeID && !s.kernelManager.ManagedPythonProvisioningEnabled()
		rUnavailable := id == managedRScientificRuntimeID && !s.kernelManager.ManagedRProvisioningEnabled()
		if pythonUnavailable || rUnavailable {
			state.State = "failed"
			state.LastErrorCode = managedScientificRuntimeUnavailableCode()
			return scientificRuntimeWarmupHealthValue(state)
		}
		for _, runtime := range s.kernelManager.RuntimeEnvironmentStatuses(false) {
			if id == managedPythonScientificRuntimeID && (runtime.Language != "python" || runtime.EnvironmentName == "python-kernel-sidecar") {
				continue
			}
			if id == managedRScientificRuntimeID && runtime.Language != "r" {
				continue
			}
			environmentName = runtime.EnvironmentName
			switch runtime.Status {
			case "ready":
				state.State = "ready"
			case "installing":
				state.State = "preparing"
			case "pending":
				state.State = "scheduled"
			case "failed", "unavailable":
				state.State = "failed"
				state.LastErrorCode = managedScientificRuntimeUnavailableCode()
			default:
				state.State = "disabled"
			}
			state.Environment = runtime.EnvironmentName
			if strings.TrimSpace(runtime.Phase) != "" {
				state.Progress = &scientificRuntimeProgress{Phase: runtime.Phase}
			}
			break
		}
	}
	result := scientificRuntimeWarmupHealthValue(state)
	if environmentName != "" {
		result["environment"] = environmentName
	}
	return result
}

func managedScientificRuntimeUnavailableCode() string {
	if kernelruntime.ManagedScientificRuntimePlatform() != "linux-x86_64" {
		return "bundled_runtime_platform_unsupported"
	}
	return "managed_runtime_unavailable"
}
