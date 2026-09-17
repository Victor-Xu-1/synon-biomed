package server

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"synon-go/internal/datadir"
	runtimecontrol "synon-go/internal/runtimecontrol"
)

type setDataDirectoryInput struct {
	Path            string `json:"path"`
	Migrate         bool   `json:"migrate"`
	NoRestart       bool   `json:"noRestart"`
	NoRestartLegacy bool   `json:"_noRestart"`
}

func (s *Server) handleDataDirectory(w http.ResponseWriter, r *http.Request) {
	controller, ok := s.dataDirectoryControllerForRequest(w)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.handleGetDataDirectory(w, controller)
	case http.MethodPost:
		s.handleSetDataDirectory(w, r, controller)
	default:
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
	}
}

func (s *Server) handleGetDataDirectory(w http.ResponseWriter, controller *datadir.Controller) {
	state, err := controller.Load()
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	usageBytes, freeBytes, err := runtimecontrol.PathUsage(s.fileRoot)
	var usageValue any = usageBytes
	if err != nil {
		usageValue = nil
	}
	activeFrames, err := s.activeFrameCount()
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	resolved := ""
	if value, resolveErr := filepath.EvalSymlinks(s.fileRoot); resolveErr == nil && value != s.fileRoot {
		resolved = value
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"current": s.fileRoot, "resolved": emptyStringAsNil(resolved),
		"default": s.defaultDataDirectory, "source": s.dataDirectorySource,
		"configPath": controller.Path(), "usageBytes": usageValue, "freeBytes": freeBytes,
		"activeFrames": activeFrames, "lastMove": state.LastMove, "pendingMove": state.Pending,
	})
}

func (s *Server) handleSetDataDirectory(w http.ResponseWriter, r *http.Request, controller *datadir.Controller) {
	var input setDataDirectoryInput
	if err := decodeWorkspaceJSON(r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	activeFrames, err := s.activeFrameCount()
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if activeFrames > 0 {
		writeWorkspaceJSON(w, http.StatusConflict, map[string]any{
			"ok": false, "error": "running frames must finish or stop before changing the data directory",
			"activeFrames": activeFrames,
		})
		return
	}
	condaHome := strings.TrimSpace(s.condaHome)
	if condaHome == "" {
		condaHome = runtimecontrol.CondaRoot(s.fileRoot)
	}
	condaEnvsPath := strings.TrimSpace(s.condaEnvsPath)
	if condaEnvsPath == "" {
		condaEnvsPath = filepath.Join(condaHome, "envs")
	}
	move, err := controller.StageWithPolicy(s.fileRoot, input.Path, input.Migrate, datadir.StagePolicy{
		ProtectedDirectories: []string{condaHome, condaEnvsPath},
	})
	if err != nil {
		status := http.StatusBadRequest
		var insufficient *datadir.InsufficientSpaceError
		if errors.Is(err, datadir.ErrChangePending) || errors.Is(err, datadir.ErrActivationActive) ||
			errors.Is(err, datadir.ErrTargetNotEmpty) {
			status = http.StatusConflict
		} else if errors.As(err, &insufficient) {
			status = http.StatusInsufficientStorage
		}
		writeWorkspaceJSON(w, status, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	restartRequired := true
	restarting := s.restartRuntime != nil && !input.NoRestart && !input.NoRestartLegacy
	if restarting {
		reason := "data_dir_change"
		if input.Migrate {
			reason = "data_dir_move"
		}
		if err := s.restartRuntime(reason); err != nil {
			rollbackErr := controller.CancelPending(move.ID)
			errorMessage := err.Error()
			if rollbackErr != nil {
				errorMessage = fmt.Sprintf("%s; rollback failed: %v", errorMessage, rollbackErr)
			}
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{
				"ok": false, "error": errorMessage, "restartRequired": restartRequired,
				"rolledBack": rollbackErr == nil,
			})
			return
		}
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"ok": true, "restarting": restarting, "restartRequired": restartRequired, "move": move,
	})
}

func (s *Server) handleDataDirectoryLastMove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	controller, ok := s.dataDirectoryControllerForRequest(w)
	if !ok {
		return
	}
	deleteSource := r.URL.Query().Get("deleteSource") == "1" || strings.EqualFold(r.URL.Query().Get("deleteSource"), "true")
	if err := controller.ClearLastMove(deleteSource); err != nil {
		writeWorkspaceJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) dataDirectoryControllerForRequest(w http.ResponseWriter) (*datadir.Controller, bool) {
	if s.dataDirectoryController == nil || strings.TrimSpace(s.fileRoot) == "" {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "data directory controller is not configured"})
		return nil, false
	}
	return s.dataDirectoryController, true
}

func (s *Server) activeFrameCount() (int, error) {
	if s.workspaceStore == nil {
		return 0, nil
	}
	return s.workspaceStore.CountActiveFrames()
}

func emptyStringAsNil(value string) any {
	if value == "" {
		return nil
	}
	return value
}
