package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
)

var compatibilityPythonVersion = regexp.MustCompile(`^3\.\d{1,2}$`)

func (s *Server) handleCompatibilitySessionConfig(w http.ResponseWriter, r *http.Request, frame workspace.CompatibilityFrame) {
	if r.Method != http.MethodPost {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	if s.sessionStore == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "Session runtime is not configured")
		return
	}
	patch, err := decodeCompatibilitySessionConfig(r)
	if err != nil {
		writeV11Detail(w, http.StatusBadRequest, err.Error())
		return
	}
	if rawTarget, ok := patch["target_agent"].(string); ok {
		target := rawTarget
		found, validationErr := s.validateCompatibilityTargetAgent(r, &target)
		if validationErr != nil {
			writeV11Detail(w, http.StatusInternalServerError, validationErr.Error())
			return
		}
		if !found {
			writeV11Detail(w, http.StatusBadRequest, "Agent "+target+" not found in registry")
			return
		}
		patch["target_agent"] = target
	}
	s.compatRequestMu.Lock()
	defer s.compatRequestMu.Unlock()
	result, err := s.workspaceStore.PatchCompatibilitySessionConfig(frame.ID, patch)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	if err := s.mergeRuntimeSessionConfig(result.RootFrameID, patch); err != nil {
		writeV11StoreError(w, fmt.Errorf("persist runner session config mirror: %w", err))
		return
	}
	if _, err := s.publishProjectEvent(frame.ProjectID, "frame_update", map[string]any{
		"action": "session_config_updated", "frame_id": result.RootFrameID, "root_frame_id": result.RootFrameID,
	}); err != nil {
		writeV11StoreError(w, fmt.Errorf("session config updated but realtime delivery failed: %w", err))
		return
	}
	response := map[string]any{"root_frame_id": result.RootFrameID, "status": "ok"}
	for key, value := range patch {
		response[key] = value
	}
	writeJSON(w, http.StatusOK, response)
}

func decodeCompatibilitySessionConfig(r *http.Request) (map[string]any, error) {
	var raw map[string]json.RawMessage
	if err := decodeAgentCompatJSON(r, &raw); err != nil {
		return nil, fmt.Errorf("Invalid session config: %w", err)
	}
	patch := map[string]any{}
	for _, field := range []string{"verifier_mode", "memory_mode", "auto_mode"} {
		if value, found := raw[field]; found {
			parsed, err := compatibilityEnum(value, field, "off", "on")
			if err != nil {
				return nil, err
			}
			patch[field] = parsed
		}
	}
	if value, found := raw["reviewer_model"]; found {
		parsed, err := compatibilityNullableString(value, "reviewer_model", 200)
		if err != nil {
			return nil, err
		}
		patch["reviewer_model"] = parsed
	}
	if value, found := raw["target_agent"]; found {
		parsed, err := compatibilityNullableString(value, "target_agent", 128)
		if err != nil || parsed == nil {
			return nil, errors.New("target_agent must be a non-empty agent name")
		}
		patch["target_agent"] = parsed
	}
	for _, field := range []struct {
		name    string
		minimum int
		maximum int
	}{
		{name: "rc_context_ceiling", minimum: 200000, maximum: 2000000},
		{name: "kernel_idle_timeout", minimum: 60, maximum: 21600},
		{name: "async_local_exec_wallclock_cap_s", minimum: 0, maximum: 2000000},
	} {
		if value, found := raw[field.name]; found {
			parsed, err := compatibilityNullableInteger(value, field.name, field.minimum, field.maximum)
			if err != nil {
				return nil, err
			}
			patch[field.name] = parsed
		}
	}
	if value, found := raw["python_version"]; found {
		parsed, err := compatibilityNullableString(value, "python_version", 4)
		if err != nil {
			return nil, err
		}
		if parsed != nil && !compatibilityPythonVersion.MatchString(parsed.(string)) {
			return nil, errors.New("python_version must match 3.x")
		}
		patch["python_version"] = parsed
	}
	if value, found := raw["goal_text"]; found {
		parsed, err := compatibilityNullableString(value, "goal_text", 4000)
		if err != nil || parsed != nil && strings.TrimSpace(parsed.(string)) == "" {
			return nil, errors.New("goal_text must be a non-empty string of at most 4,000 characters, or null to clear")
		}
		if parsed != nil {
			return nil, errors.New("goal_text is not available in this build")
		}
		patch["goal_text"] = nil
	}
	if value, found := raw["gpu_mode"]; found {
		if _, err := compatibilityEnum(value, "gpu_mode", "off", "on"); err != nil {
			return nil, err
		}
		return nil, errors.New("GPU access is set when the session starts — start a new session to change it")
	}
	if len(patch) == 0 {
		return nil, errors.New("updateSessionConfig requires at least one session knob")
	}
	return patch, nil
}

func compatibilityEnum(raw json.RawMessage, field string, allowed ...string) (string, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("%s must be one of %s", field, strings.Join(allowed, ", "))
	}
	for _, candidate := range allowed {
		if value == candidate {
			return value, nil
		}
	}
	return "", fmt.Errorf("%s must be one of %s", field, strings.Join(allowed, ", "))
}

func compatibilityNullableString(raw json.RawMessage, field string, maximum int) (any, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || value == "" || utf8.RuneCountInString(value) > maximum {
		return nil, fmt.Errorf("%s must be a non-empty string of at most %d characters, or null", field, maximum)
	}
	return value, nil
}

func compatibilityNullableInteger(raw json.RawMessage, field string, minimum, maximum int) (any, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil || math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value || value < float64(minimum) || value > float64(maximum) {
		return nil, fmt.Errorf("%s must be an integer from %d through %d, or null", field, minimum, maximum)
	}
	return int(value), nil
}

func (s *Server) mergeRuntimeSessionConfig(sessionID string, stored map[string]any) error {
	if s.sessionStore == nil {
		return errors.New("session store is not configured")
	}
	session, found, err := s.sessionStore.Get(sessionID)
	if err != nil {
		return err
	}
	if !found {
		session = sessionstore.Session{ID: sessionID, Title: sessionID, WorkDir: s.fileRoot}
	}
	orchestration := copyMapAny(session.Orchestration)
	if orchestration == nil {
		orchestration = map[string]any{}
	}
	config := map[string]any{}
	if existing, ok := orchestration["sessionConfig"].(map[string]any); ok {
		config = copyMapAny(existing)
	}
	for key, value := range stored {
		config[key] = value
	}
	orchestration["sessionConfig"] = config
	session.Orchestration = orchestration
	return s.sessionStore.Save(session)
}
