package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) handleCompatibilityProjectRead(w http.ResponseWriter, r *http.Request, projectID string) {
	if s.workspaceStore == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "Workspace runtime is not configured")
		return
	}
	if projectID == "dashboard" {
		if r.Method != http.MethodGet {
			writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		s.handleCompatibilityProjectDashboard(w, compatAgentUserID(r))
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.handleCompatibilityProjectGet(w, r, projectID)
	case http.MethodPatch:
		s.handleCompatibilityProjectUpdate(w, r, projectID)
	case http.MethodDelete:
		s.handleCompatibilityProjectDelete(w, r, projectID)
	default:
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func (s *Server) handleCompatibilityProjectGet(w http.ResponseWriter, r *http.Request, projectID string) {
	userID := compatAgentUserID(r)
	project, found, err := s.workspaceStore.GetCompatibilityProject(userID, projectID)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	if !found {
		writeV11Detail(w, http.StatusNotFound, "Project "+projectID+" not found")
		return
	}
	writeJSON(w, http.StatusOK, compatibilityProjectResponse(project))
}

func (s *Server) handleCompatibilityProjectUpdate(w http.ResponseWriter, r *http.Request, projectID string) {
	var body map[string]json.RawMessage
	decoder := json.NewDecoder(io.LimitReader(r.Body, workspaceRequestLimit))
	if err := decoder.Decode(&body); err != nil {
		writeV11Detail(w, http.StatusBadRequest, err.Error())
		return
	}
	if body == nil {
		writeV11Detail(w, http.StatusBadRequest, "request body must be an object")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			err = errors.New("request body must contain one JSON value")
		}
		writeV11Detail(w, http.StatusBadRequest, err.Error())
		return
	}
	input := workspace.UpdateCompatibilityProjectInput{}
	if raw, found := body["name"]; found {
		var value string
		if compatibilityJSONNull(raw) || json.Unmarshal(raw, &value) != nil {
			writeV11Detail(w, http.StatusBadRequest, "name must be a string")
			return
		}
		value = strings.TrimSpace(value)
		if value == "" || utf8.RuneCountInString(value) > 255 {
			writeV11Detail(w, http.StatusBadRequest, "name must contain between 1 and 255 characters")
			return
		}
		input.Name = &value
	}
	if raw, found := body["description"]; found {
		input.DescriptionSet = true
		if !compatibilityJSONNull(raw) {
			var value string
			if err := json.Unmarshal(raw, &value); err != nil {
				writeV11Detail(w, http.StatusBadRequest, "description must be a string or null")
				return
			}
			input.Description = &value
		}
	}
	if raw, found := body["context"]; found {
		input.ContextSet = true
		if !compatibilityJSONNull(raw) {
			var value string
			if err := json.Unmarshal(raw, &value); err != nil {
				writeV11Detail(w, http.StatusBadRequest, "context must be a string or null")
				return
			}
			input.ContextData = value
		}
	}
	project, err := s.workspaceStore.UpdateCompatibilityProjectRealtime(
		r.Context(), projectID, compatAgentUserID(r), input, "",
	)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			writeV11Detail(w, http.StatusNotFound, "Project "+projectID+" not found")
			return
		}
		writeV11StoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, compatibilityProjectResponse(project))
}

func compatibilityJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func (s *Server) handleCompatibilityProjectDelete(w http.ResponseWriter, r *http.Request, projectID string) {
	userID := compatAgentUserID(r)
	if _, found, err := s.workspaceStore.GetCompatibilityProject(userID, projectID); err != nil {
		writeV11StoreError(w, err)
		return
	} else if !found {
		writeV11Detail(w, http.StatusNotFound, "Project "+projectID+" not found")
		return
	}
	rootFrameIDs, err := s.workspaceStore.ListCompatibilityProjectRootFrameIDs(r.Context(), userID, projectID)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	cleanupErrors := make([]error, 0)
	runtimeCloseCtx, cancelRuntimeClose := compatibilityRuntimeCleanupContext(r.Context())
	defer cancelRuntimeClose()
	for _, rootFrameID := range rootFrameIDs {
		cleanupErrors = append(cleanupErrors, s.stopCompatibilityFrameRuntime(runtimeCloseCtx, rootFrameID)...)
	}
	blobPaths, err := s.workspaceStore.DeleteProjectRealtime(r.Context(), projectID, userID, "")
	if err != nil {
		log.Printf("compatibility project delete transaction failed: project_id=%s error=%v", projectID, err)
		writeV11StoreError(w, err)
		return
	}
	for _, rootFrameID := range rootFrameIDs {
		cleanupErrors = append(cleanupErrors, s.stopCompatibilityFrameRuntime(runtimeCloseCtx, rootFrameID)...)
		cleanupErrors = append(cleanupErrors, s.removeCompatibilityFrameRuntime(rootFrameID)...)
	}
	if err := s.workspaceStore.RemoveProjectArtifactBlobs(blobPaths); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	if err := s.removeCompatibilityProjectRuntime(projectID); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	if len(cleanupErrors) > 0 {
		log.Printf("compatibility project delete committed with cleanup warnings: project_id=%s failures=%d errors=%v", projectID, len(cleanupErrors), errors.Join(cleanupErrors...))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "deleted", "project_id": projectID, "cleanup_warnings": len(cleanupErrors),
	})
}

func (s *Server) handleCompatibilityProjectDashboard(w http.ResponseWriter, userID string) {
	projects, err := s.workspaceStore.ListCompatibilityProjects(userID, 1000, 0)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	response := make([]map[string]any, 0, len(projects))
	totalProcessing := 0
	totalNeedsInput := 0
	totalRecentlyCompleted := 0
	for _, project := range projects {
		entry, err := s.compatibilityProjectDashboardEntry(userID, project)
		if err != nil {
			writeV11StoreError(w, err)
			return
		}
		totalProcessing += entry["processing_count"].(int)
		totalNeedsInput += entry["needs_input_count"].(int)
		totalRecentlyCompleted += entry["completed_count"].(int)
		response = append(response, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"projects": response, "total_projects": len(response),
		"total_processing": totalProcessing, "total_needs_input": totalNeedsInput,
		"total_recently_completed": totalRecentlyCompleted,
	})
}

func (s *Server) compatibilityProjectDashboardEntry(userID string, project workspace.CompatibilityProject) (map[string]any, error) {
	entry := compatibilityProjectResponse(project)
	lastActive, err := s.workspaceStore.CompatibilityProjectLastActiveAt(userID, project.ID)
	if err != nil {
		return nil, err
	}
	entry["last_active_at"] = lastActive
	frames, err := s.workspaceStore.ListVisibleCompatibilityFrames(userID, project.ID, true, 1000)
	if err != nil {
		return nil, err
	}
	named := make([]workspace.CompatibilityFrame, 0, len(frames))
	for _, frame := range frames {
		if strings.TrimSpace(frame.Name) != "" {
			named = append(named, frame)
		}
	}
	sort.Slice(named, func(i, j int) bool {
		if named[i].UpdatedAt.Equal(named[j].UpdatedAt) {
			return named[i].ID > named[j].ID
		}
		return named[i].UpdatedAt.After(named[j].UpdatedAt)
	})
	processing := make([]map[string]any, 0)
	needsInput := make([]map[string]any, 0)
	completed := make([]map[string]any, 0)
	updated := make([]map[string]any, 0)
	for _, frame := range named {
		item := compatibilityDashboardFrame(frame, true)
		updated = append(updated, item)
		switch frame.Status {
		case "processing", "running":
			processing = append(processing, item)
		case "awaiting_user_response", "awaiting_plan_approval":
			needsInput = append(needsInput, item)
		case "completed":
			completed = append(completed, compatibilityDashboardFrame(frame, false))
		}
	}
	processing = compatibilityLimitMaps(processing, 5)
	processingCount := len(processing)
	needsInputCount := len(needsInput)
	completedCount := len(completed)
	needsInput = compatibilityLimitMaps(needsInput, 5)
	completed = compatibilityLimitMaps(completed, 5)
	updated = compatibilityLimitMaps(updated, 5)
	artifacts, err := s.workspaceStore.ListArtifacts(project.ID, 5, 0)
	if err != nil {
		return nil, err
	}
	recentArtifacts := make([]map[string]any, 0, len(artifacts))
	imageCount := 0
	allArtifacts, err := s.workspaceStore.ListArtifacts(project.ID, 1000, 0)
	if err != nil {
		return nil, err
	}
	for _, artifact := range allArtifacts {
		if strings.HasPrefix(strings.ToLower(artifact.Kind), "image/") {
			imageCount++
		}
	}
	for _, artifact := range artifacts {
		recentArtifacts = append(recentArtifacts, map[string]any{
			"id": artifact.ID, "name": artifact.Name,
			"artifact_type": nil, "created_at": artifact.CreatedAt.UTC(),
		})
	}
	entry["processing_benches"] = processing
	entry["processing_count"] = processingCount
	entry["needs_input_benches"] = needsInput
	entry["needs_input_count"] = needsInputCount
	entry["recently_completed"] = completed
	entry["completed_count"] = completedCount
	entry["recently_updated"] = updated
	entry["recent_artifacts"] = recentArtifacts
	entry["image_artifact_count"] = imageCount
	entry["total_session_count"] = len(named)
	return entry, nil
}

func compatibilityDashboardFrame(frame workspace.CompatibilityFrame, includeActivity bool) map[string]any {
	result := map[string]any{
		"id": frame.ID, "name": frame.Name, "status": frame.Status,
		"task_summary": nullableCompatibilityString(frame.TaskSummary),
		"updated_at":   frame.UpdatedAt.UTC(),
	}
	if includeActivity {
		result["last_activity_at"] = frame.UpdatedAt.UTC()
	} else {
		result["last_activity_at"] = nil
		result["completed_at"] = frame.UpdatedAt.UTC()
	}
	return result
}

func compatibilityLimitMaps(values []map[string]any, limit int) []map[string]any {
	if len(values) <= limit {
		return values
	}
	return values[:limit]
}
