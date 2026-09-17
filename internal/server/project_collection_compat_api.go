package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) handleCompatibilityProjectCollection(w http.ResponseWriter, r *http.Request, projectID, resource string) {
	if s.workspaceStore == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "Workspace runtime is not configured")
		return
	}
	if r.Method != http.MethodGet {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	limit, err := compatibilityOptionalPositiveQuery(r, "limit", 200, 1000)
	if err != nil {
		writeV11Detail(w, http.StatusBadRequest, err.Error())
		return
	}
	userID := compatAgentUserID(r)
	switch resource {
	case "artifacts":
		excludeIntermediate := r.URL.Query().Get("exclude_intermediate") != "false"
		artifacts, err := s.workspaceStore.ListCompatibilityProjectArtifacts(
			r.Context(), userID, projectID, excludeIntermediate, limit,
		)
		if err != nil {
			writeV11StoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, artifacts)
	case "benches":
		query, err := compatibilityBenchQuery(r)
		if err != nil {
			writeV11Detail(w, http.StatusBadRequest, err.Error())
			return
		}
		benches, found, err := s.workspaceStore.ListCompatibilityProjectBenches(
			r.Context(), userID, projectID, compatibilityBenchLimit(limit, query), query,
		)
		if err != nil {
			writeV11StoreError(w, err)
			return
		}
		if !found {
			writeV11Detail(w, http.StatusNotFound, "Project "+projectID+" not found")
			return
		}
		response, err := s.compatibilityBenchResponses(r.Context(), benches)
		if err != nil {
			writeV11StoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, response)
	default:
		writeV11Detail(w, http.StatusNotFound, "Project collection not found")
	}
}

func (s *Server) handleCompatibilityProjectBatchCollection(w http.ResponseWriter, r *http.Request, resource string) {
	if s.workspaceStore == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "Workspace runtime is not configured")
		return
	}
	if r.Method != http.MethodGet {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	if resource != "benches" && resource != "artifacts" {
		writeV11Detail(w, http.StatusNotFound, "Project batch collection not found")
		return
	}
	limit, err := compatibilityOptionalPositiveQuery(r, "limit", 200, 1000)
	if err != nil {
		writeV11Detail(w, http.StatusBadRequest, err.Error())
		return
	}
	projectIDs, err := compatibilityBatchProjectIDs(r.URL.Query().Get("pids"), 1000)
	if err != nil {
		writeV11Detail(w, http.StatusBadRequest, err.Error())
		return
	}
	response := make(map[string]any, len(projectIDs))
	userID := compatAgentUserID(r)
	if resource == "artifacts" {
		for _, projectID := range projectIDs {
			artifacts, err := s.workspaceStore.ListCompatibilityProjectArtifacts(
				r.Context(), userID, projectID, true, limit,
			)
			if err != nil {
				continue
			}
			response[projectID] = artifacts
		}
		writeJSON(w, http.StatusOK, response)
		return
	}
	query, err := compatibilityBenchQuery(r)
	if err != nil {
		writeV11Detail(w, http.StatusBadRequest, err.Error())
		return
	}
	for _, projectID := range projectIDs {
		benches, found, err := s.workspaceStore.ListCompatibilityProjectBenches(
			r.Context(), userID, projectID, compatibilityBenchLimit(limit, query), query,
		)
		if err != nil || !found {
			continue
		}
		projection, err := s.compatibilityBenchResponses(r.Context(), benches)
		if err != nil {
			writeV11StoreError(w, err)
			return
		}
		response[projectID] = projection
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) compatibilityBenchResponses(ctx context.Context, frames []workspace.CompatibilityFrame) ([]map[string]any, error) {
	response := make([]map[string]any, 0, len(frames))
	for _, frame := range frames {
		projection, err := s.compatibilityFrameResponse(frame, false, map[string]bool{})
		if err != nil {
			return nil, err
		}
		if err := s.decorateCompatibilityBenchProjection(ctx, projection, frame.RootFrameID); err != nil {
			return nil, err
		}
		response = append(response, compatibilityBenchProjection(projection))
	}
	return response, nil
}

func (s *Server) decorateCompatibilityBenchProjection(ctx context.Context, projection map[string]any, rootFrameID string) error {
	projection["input_data"] = nil
	projection["message_count"] = nil
	lastActivity, found, err := s.workspaceStore.CompatibilityBenchActivity(ctx, rootFrameID)
	if err != nil {
		return err
	}
	if found {
		projection["last_activity_at"] = lastActivity
	} else {
		projection["last_activity_at"] = projection["updated_at"]
	}
	hasImage, err := s.workspaceStore.CompatibilityBenchHasImageOutput(ctx, rootFrameID)
	if err != nil {
		return err
	}
	projection["has_image_output"] = hasImage
	output, outputFound, err := s.workspaceStore.CompatibilityBenchOutputData(ctx, rootFrameID)
	if err != nil {
		return err
	}
	descendantAwaiting, err := s.workspaceStore.CompatibilityBenchHasDescendantAwaitingInput(ctx, rootFrameID)
	if err != nil {
		return err
	}
	if descendantAwaiting {
		if !outputFound {
			output = map[string]any{}
		}
		output["descendant_awaiting_input"] = true
		outputFound = true
	}
	if outputFound {
		projection["output_data"] = output
	}
	queued, err := s.workspaceStore.ListCompatibilityBenchQueuedUserMessages(ctx, rootFrameID)
	if err != nil {
		return err
	}
	if len(queued) > 0 {
		projection["queued_user_messages"] = queued
	}
	return nil
}

func compatibilityBenchProjection(frame map[string]any) map[string]any {
	keys := [...]string{
		"id", "root_frame_id", "parent_frame_id", "agent_name", "delegate_name", "status",
		"input_data", "output_data", "created_at", "completed_at", "updated_at", "last_activity_at",
		"model", "effort", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens",
		"total_cost", "children", "project_id", "name", "message_count", "task_summary",
		"status_description", "mentioned_files", "specialists_used", "is_hidden", "has_image_output",
		"activity_counts",
	}
	projection := make(map[string]any, len(keys)+1)
	for _, key := range keys {
		projection[key] = frame[key]
	}
	if queued, found := frame["queued_user_messages"]; found {
		projection["queued_user_messages"] = queued
	}
	return projection
}

func compatibilityOptionalPositiveQuery(r *http.Request, key string, fallback, maximum int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, errors.New(key + " must be a positive integer")
	}
	if value > maximum {
		value = maximum
	}
	return value, nil
}

func compatibilityBenchQuery(r *http.Request) (string, error) {
	raw, present := r.URL.Query()["q"]
	if !present || len(raw) == 0 || raw[0] == "" {
		return "", nil
	}
	query := strings.TrimSpace(raw[0])
	if len([]rune(query)) < 2 {
		return "", errors.New("q must be at least 2 characters")
	}
	return query, nil
}

func compatibilityBenchLimit(limit int, query string) int {
	if query != "" && limit > 64 {
		return 64
	}
	return limit
}

func compatibilityBatchProjectIDs(raw string, maximum int) ([]string, error) {
	seen := map[string]bool{}
	result := make([]string, 0)
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
		if len(result) > maximum {
			return nil, fmt.Errorf("pids is limited to %d project ids", maximum)
		}
	}
	return result, nil
}
