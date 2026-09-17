package server

import (
	"errors"
	"net/http"

	runtimecontrol "synon-go/internal/runtimecontrol"
)

const (
	builtinAllowlistDisabledSettingKey = "network.builtinAllowlistDisabled"
	builtinAllowlistGroupsSettingKey   = "network.builtinAllowlistDisabledGroups"
	allowlistOnboardingSeenSettingKey  = "onboarding.allowlistSeen"
)

func (s *Server) handleBuiltinAllowlist(w http.ResponseWriter, r *http.Request) {
	if s.settingsStore == nil {
		writeError(w, http.StatusServiceUnavailable, "SETTINGS_UNAVAILABLE", "settings store is not configured")
		return
	}
	switch r.URL.Path {
	case "/api/preferences/builtin-allowlist", "/api/go/preferences/builtin-allowlist":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "builtin allowlist only supports GET")
			return
		}
		s.writeBuiltinAllowlist(w)
	case "/api/preferences/builtin-allowlist/disabled", "/api/go/preferences/builtin-allowlist/disabled":
		if r.Method != http.MethodPut {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "builtin allowlist disabled only supports PUT")
			return
		}
		s.updateBuiltinAllowlistSelection(w, r, builtinAllowlistDisabledSettingKey, "disabled")
	case "/api/preferences/builtin-allowlist/disabled-groups", "/api/go/preferences/builtin-allowlist/disabled-groups":
		if r.Method != http.MethodPut {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "builtin allowlist disabled groups only supports PUT")
			return
		}
		s.updateBuiltinAllowlistSelection(w, r, builtinAllowlistGroupsSettingKey, "disabledGroups")
	case "/api/preferences/builtin-allowlist/onboarding-seen", "/api/go/preferences/builtin-allowlist/onboarding-seen":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "builtin allowlist onboarding only supports POST")
			return
		}
		if _, err := s.settingsStore.Set(allowlistOnboardingSeenSettingKey, true); err != nil {
			writeError(w, http.StatusInternalServerError, "SETTINGS_WRITE_FAILED", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{})
	default:
		writeError(w, http.StatusNotFound, "NOT_FOUND", "builtin allowlist endpoint not found")
	}
}

func (s *Server) writeBuiltinAllowlist(w http.ResponseWriter) {
	disabled, err := s.builtinAllowlistSelection(builtinAllowlistDisabledSettingKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SETTINGS_READ_FAILED", err.Error())
		return
	}
	disabledGroups, err := s.builtinAllowlistSelection(builtinAllowlistGroupsSettingKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SETTINGS_READ_FAILED", err.Error())
		return
	}
	seen := false
	if setting, found, err := s.settingsStore.Get(allowlistOnboardingSeenSettingKey); err != nil {
		writeError(w, http.StatusInternalServerError, "SETTINGS_READ_FAILED", err.Error())
		return
	} else if found {
		seen, _ = setting.Value.(bool)
	}
	groups := runtimecontrol.BuiltinAllowlistGroups()
	groupPayload := make([]map[string]any, 0, len(groups))
	for _, group := range groups {
		groupPayload = append(groupPayload, map[string]any{
			"id": group.ID, "label": group.Label, "description": group.Description,
			"locked": group.Locked, "domains": group.Domains,
		})
	}
	activeKernels := 0
	if s.kernelManager != nil {
		activeKernels = s.kernelManager.ActiveCount()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"groups": groupPayload, "disabled": disabled, "disabledGroups": disabledGroups,
		"effectiveDomains":  runtimecontrol.EffectiveBuiltinAllowlist(disabled, disabledGroups),
		"activeKernelCount": activeKernels, "hasSeenOnboarding": seen,
	})
}

func (s *Server) updateBuiltinAllowlistSelection(w http.ResponseWriter, r *http.Request, settingKey, property string) {
	var body map[string]any
	if err := decodeBuiltinAllowlistBody(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	values, ok := stringSliceMapValue(body, property)
	if !ok {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", property+" must be string[]")
		return
	}
	values = runtimecontrol.NormalizeBuiltinAllowlistSelection(values)
	if _, err := s.settingsStore.Set(settingKey, values); err != nil {
		writeError(w, http.StatusInternalServerError, "SETTINGS_WRITE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *Server) builtinAllowlistSelection(key string) ([]string, error) {
	setting, found, err := s.settingsStore.Get(key)
	if err != nil || !found {
		return []string{}, err
	}
	return runtimecontrol.NormalizeBuiltinAllowlistSelection(settingStringSlice(setting.Value)), nil
}

func decodeBuiltinAllowlistBody(r *http.Request, target *map[string]any) error {
	if err := decodeWorkspaceJSON(r, target); err != nil {
		return errors.New("invalid JSON request")
	}
	return nil
}

func stringSliceMapValue(body map[string]any, key string) ([]string, bool) {
	raw, exists := body[key]
	if !exists {
		return nil, false
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			return nil, false
		}
		result = append(result, text)
	}
	return result, true
}
