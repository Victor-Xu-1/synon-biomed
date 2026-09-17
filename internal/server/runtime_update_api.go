package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"

	"synon-go/internal/buildinfo"
)

type RuntimeUpdateOptions struct {
	Channel    string
	Current    string
	AutoUpdate bool
	Check      func(context.Context, string, string) (string, error)
	Stage      func(context.Context, string, string) (string, error)
}

type runtimeUpdateStatus struct {
	Channel    string  `json:"channel"`
	Current    string  `json:"current"`
	Latest     *string `json:"latest"`
	CheckedAt  *string `json:"checkedAt"`
	Error      *string `json:"error"`
	AutoUpdate bool    `json:"autoUpdate"`
	Required   *string `json:"required"`
}

type runtimeUpdateController struct {
	mu       sync.Mutex
	status   runtimeUpdateStatus
	check    func(context.Context, string, string) (string, error)
	stage    func(context.Context, string, string) (string, error)
	checking bool
	applying bool
}

func newRuntimeUpdateController(options RuntimeUpdateOptions) *runtimeUpdateController {
	channel := strings.TrimSpace(options.Channel)
	if channel == "" {
		channel = "local"
	}
	current := strings.TrimSpace(options.Current)
	if current == "" {
		current = buildinfo.Release().Version
	}
	return &runtimeUpdateController{
		status: runtimeUpdateStatus{
			Channel: channel, Current: current, AutoUpdate: options.AutoUpdate,
		},
		check: options.Check, stage: options.Stage,
	}
}

func (u *runtimeUpdateController) snapshot() runtimeUpdateStatus {
	if u == nil {
		return runtimeUpdateStatus{}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return cloneRuntimeUpdateStatus(u.status)
}

func cloneRuntimeUpdateStatus(status runtimeUpdateStatus) runtimeUpdateStatus {
	if status.Latest != nil {
		value := *status.Latest
		status.Latest = &value
	}
	if status.CheckedAt != nil {
		value := *status.CheckedAt
		status.CheckedAt = &value
	}
	if status.Error != nil {
		value := *status.Error
		status.Error = &value
	}
	if status.Required != nil {
		value := *status.Required
		status.Required = &value
	}
	return status
}

func (s *Server) handleRuntimeUpdate(w http.ResponseWriter, r *http.Request) {
	if s.runtimeUpdate == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "runtime update controller is unavailable")
		return
	}
	suffix := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/status/update"), "/")
	if suffix == "" {
		if r.Method != http.MethodGet {
			writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		status := s.runtimeUpdate.snapshot()
		writeJSON(w, http.StatusOK, map[string]any{"update_available": status.Latest != nil})
		return
	}
	if r.Method != http.MethodPost {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	switch suffix {
	case "check":
		s.handleRuntimeUpdateCheck(w, r)
	case "required":
		s.handleRuntimeUpdateRequired(w, r)
	case "apply":
		s.handleRuntimeUpdateApply(w, r)
	default:
		writeV11Detail(w, http.StatusNotFound, "Update endpoint not found")
	}
}

func (s *Server) handleRuntimeUpdateCheck(w http.ResponseWriter, r *http.Request) {
	controller := s.runtimeUpdate
	controller.mu.Lock()
	if controller.checking {
		controller.mu.Unlock()
		writeV11Detail(w, http.StatusConflict, "update check already in progress")
		return
	}
	check := controller.check
	channel, current := controller.status.Channel, controller.status.Current
	previous := pointerValue(controller.status.Latest)
	controller.checking = true
	controller.mu.Unlock()
	defer func() {
		controller.mu.Lock()
		controller.checking = false
		controller.mu.Unlock()
	}()
	if check == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "release update source is not configured")
		return
	}
	latest, err := check(r.Context(), channel, current)
	checkedAt := time.Now().UTC().Format(time.RFC3339Nano)
	latest = strings.TrimSpace(latest)
	if err == nil && latest != "" {
		if !validRuntimeVersion(latest) {
			err = errors.New("update source returned an invalid semantic version")
		} else if compareRuntimeVersions(latest, current) <= 0 {
			latest = ""
		}
	}

	if err != nil {
		controller.mu.Lock()
		controller.status.CheckedAt = &checkedAt
		message := err.Error()
		controller.status.Error = &message
		controller.mu.Unlock()
		writeV11Detail(w, http.StatusBadGateway, err.Error())
		return
	}
	var publishErr error
	switch {
	case latest != "" && latest != previous:
		_, publishErr = s.publishGlobalEvent("update_available", map[string]any{
			"type": "update_available", "channel": channel, "current": current, "latest": latest,
		})
	case latest == "" && previous != "":
		_, publishErr = s.publishGlobalEvent("update_retracted", map[string]any{
			"type": "update_retracted", "channel": channel,
		})
	}
	if publishErr != nil {
		controller.mu.Lock()
		controller.status.CheckedAt = &checkedAt
		message := "update event publication failed"
		controller.status.Error = &message
		controller.mu.Unlock()
		writeV11Detail(w, http.StatusInternalServerError, message)
		return
	}
	controller.mu.Lock()
	controller.status.CheckedAt = &checkedAt
	controller.status.Error = nil
	if latest == "" {
		controller.status.Latest = nil
	} else {
		controller.status.Latest = &latest
	}
	status := cloneRuntimeUpdateStatus(controller.status)
	controller.mu.Unlock()
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleRuntimeUpdateRequired(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Required *string `json:"required"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeV11Detail(w, http.StatusBadRequest, "Invalid update requirement")
		return
	}
	required := ""
	if body.Required != nil {
		required = strings.TrimSpace(*body.Required)
		if required != "" && !validRuntimeVersion(required) {
			writeV11Detail(w, http.StatusBadRequest, "required must be a semantic version or null")
			return
		}
	}
	controller := s.runtimeUpdate
	controller.mu.Lock()
	current := controller.status.Current
	if required != "" && compareRuntimeVersions(current, required) >= 0 {
		required = ""
	}
	previous := pointerValue(controller.status.Required)
	if required == previous {
		status := cloneRuntimeUpdateStatus(controller.status)
		controller.mu.Unlock()
		writeJSON(w, http.StatusOK, status)
		return
	}
	var requiredValue any
	if required != "" {
		requiredValue = required
	}
	if _, err := s.publishGlobalEvent("update_required", map[string]any{
		"type": "update_required", "current": current, "required": requiredValue,
	}); err != nil {
		controller.mu.Unlock()
		writeV11Detail(w, http.StatusInternalServerError, "update requirement publication failed")
		return
	}
	if required == "" {
		controller.status.Required = nil
	} else {
		controller.status.Required = &required
	}
	status := cloneRuntimeUpdateStatus(controller.status)
	controller.mu.Unlock()
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleRuntimeUpdateApply(w http.ResponseWriter, r *http.Request) {
	controller := s.runtimeUpdate
	controller.mu.Lock()
	if controller.applying {
		controller.mu.Unlock()
		writeV11Detail(w, http.StatusConflict, "update already in progress")
		return
	}
	latest := pointerValue(controller.status.Latest)
	channel, stage := controller.status.Channel, controller.stage
	if latest == "" {
		controller.mu.Unlock()
		writeV11Detail(w, http.StatusConflict, "no newer update is available")
		return
	}
	if stage == nil || s.restartRuntime == nil {
		controller.mu.Unlock()
		writeV11Detail(w, http.StatusServiceUnavailable, "verified update staging and restart are not configured")
		return
	}
	controller.applying = true
	controller.mu.Unlock()
	defer func() {
		controller.mu.Lock()
		controller.applying = false
		controller.mu.Unlock()
	}()
	if s.workspaceStore != nil {
		active, err := s.workspaceStore.CountActiveFrames()
		if err != nil {
			writeV11Detail(w, http.StatusInternalServerError, "failed to count active sessions")
			return
		}
		if active > 0 {
			writeV11Detail(
				w, http.StatusConflict,
				fmt.Sprintf("%d session(s) still running - finish or cancel them first", active),
			)
			return
		}
	}

	prepared, err := stage(r.Context(), channel, latest)
	if err != nil {
		writeV11Detail(w, http.StatusBadGateway, err.Error())
		return
	}
	if err := validatePreparedRuntimeUpdate(prepared); err != nil {
		writeV11Detail(w, http.StatusBadGateway, err.Error())
		return
	}
	if _, err := s.publishGlobalEvent("daemon_restarting", map[string]any{
		"type": "daemon_restarting", "channel": channel, "latest": latest,
	}); err != nil {
		writeV11Detail(w, http.StatusInternalServerError, "update was staged but restart publication failed")
		return
	}
	if err := s.restartRuntime("self-update -> " + latest); err != nil {
		abortKind := classifyRuntimeRestartAbort(err)
		_, _ = s.publishGlobalEvent("daemon_restart_aborted", map[string]any{
			"type": "daemon_restart_aborted", "error": "restart aborted (" + abortKind + ")",
		})
		writeV11Detail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": latest})
}

func validRuntimeVersion(value string) bool {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "v") {
		value = "v" + value
	}
	return semver.IsValid(value)
}

func compareRuntimeVersions(left, right string) int {
	if !strings.HasPrefix(left, "v") {
		left = "v" + left
	}
	if !strings.HasPrefix(right, "v") {
		right = "v" + right
	}
	return semver.Compare(left, right)
}

func validatePreparedRuntimeUpdate(path string) error {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return errors.New("update stage did not return an absolute executable path")
	}
	info, err := os.Stat(path)
	if err != nil {
		return errors.New("staged update executable is unavailable")
	}
	if !info.Mode().IsRegular() {
		return errors.New("staged update path is not a regular file")
	}
	if info.Mode().Perm()&0o111 == 0 {
		return errors.New("staged update file is not executable")
	}
	return nil
}

func classifyRuntimeRestartAbort(err error) string {
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "spawn"):
		return "spawn_error"
	case strings.Contains(message, "ready"):
		return "ready_timeout"
	case strings.Contains(message, "exited"):
		return "exited_early"
	default:
		return "other"
	}
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
