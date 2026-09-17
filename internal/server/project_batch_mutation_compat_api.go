package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	workspace "synon-go/internal/persistence/workspace"
)

type compatibilityBenchPatchBody struct {
	Name        json.RawMessage `json:"name"`
	TaskSummary json.RawMessage `json:"task_summary"`
}

func (s *Server) handleCompatibilityBenchUpdate(w http.ResponseWriter, r *http.Request) {
	if s.workspaceStore == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "Workspace runtime is not configured")
		return
	}
	if r.Method != http.MethodPatch {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	rawFrameID := strings.TrimPrefix(r.URL.Path, "/api/benches/")
	if rawFrameID == "" || strings.Contains(rawFrameID, "/") {
		writeV11Detail(w, http.StatusNotFound, "Bench endpoint not found")
		return
	}
	frameID, err := url.PathUnescape(rawFrameID)
	if err != nil || strings.TrimSpace(frameID) == "" {
		writeV11Detail(w, http.StatusBadRequest, "Invalid bench id")
		return
	}
	input, err := decodeCompatibilityBenchPatch(r)
	if err != nil {
		writeV11Detail(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.workspaceStore.UpdateCompatibilityBenchRealtime(
		r.Context(), compatAgentUserID(r), frameID, input, "",
	)
	if err != nil {
		switch {
		case errors.Is(err, workspace.ErrCompatibilityBenchNotFound):
			writeV11Detail(w, http.StatusNotFound, "Bench "+frameID+" not found")
		case errors.Is(err, workspace.ErrCompatibilityBenchNotRoot):
			writeV11Detail(w, http.StatusBadRequest, "Can only update root frames")
		default:
			writeV11StoreError(w, err)
		}
		return
	}
	if _, err := s.publishCompatEvent(result.Event); err != nil {
		writeV11StoreError(w, fmt.Errorf("bench updated but realtime delivery failed: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": result.ID, "name": result.Name, "task_summary": result.TaskSummary,
	})
}

func decodeCompatibilityBenchPatch(r *http.Request) (workspace.UpdateCompatibilityBenchInput, error) {
	var body compatibilityBenchPatchBody
	if err := decodeAgentCompatJSON(r, &body); err != nil {
		return workspace.UpdateCompatibilityBenchInput{}, fmt.Errorf("Invalid arguments: %w", err)
	}
	name, err := compatibilityRequiredString(body.Name, "name")
	if err != nil {
		return workspace.UpdateCompatibilityBenchInput{}, err
	}
	taskSummary, err := compatibilityRequiredString(body.TaskSummary, "taskSummary")
	if err != nil {
		return workspace.UpdateCompatibilityBenchInput{}, err
	}
	return workspace.UpdateCompatibilityBenchInput{Name: name, TaskSummary: taskSummary}, nil
}

func compatibilityRequiredString(raw json.RawMessage, field string) (*string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, fmt.Errorf("Invalid arguments: %s: Expected string, received null", field)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("Invalid arguments: %s: Expected string", field)
	}
	return &value, nil
}

func (s *Server) handleCompatibilityProcessingCounts(w http.ResponseWriter, r *http.Request) {
	if s.workspaceStore == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "Workspace runtime is not configured")
		return
	}
	if r.Method != http.MethodGet {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	counts, err := s.workspaceStore.CompatibilityProcessingCounts(r.Context(), compatAgentUserID(r))
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, counts)
}
