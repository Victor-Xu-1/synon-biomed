package server

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"synon-go/internal/memorypolicy"
	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) handleWorkspaceMemoryRecall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if projectID != "" && !workspaceProjectOwned(w, store, projectID, userID) {
		return
	}
	enabled, err := s.workspaceMemoryAccessEnabled(r.Context(), userID, projectID, "", false)
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if !enabled {
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "ranked": true, "memories": []workspace.Memory{}})
		return
	}
	limit := 8
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 32 {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "limit must be between 1 and 32"})
			return
		}
		limit = parsed
	}
	crossProject, err := strconv.ParseBool(firstNonEmpty(r.URL.Query().Get("cross_project"), "false"))
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "cross_project must be a boolean"})
		return
	}
	recordAccess, err := strconv.ParseBool(firstNonEmpty(r.URL.Query().Get("record_access"), "true"))
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "record_access must be a boolean"})
		return
	}
	memories, err := store.RecallMemories(r.Context(), workspace.MemoryRecallOptions{
		UserID: userID, ProjectID: projectID, Query: r.URL.Query().Get("q"), Limit: limit,
		CrossProject: crossProject, RecordAccess: recordAccess,
	})
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "ranked": true, "memories": memories})
}

func (s *Server) handleWorkspaceMemories(w http.ResponseWriter, r *http.Request) {
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		if requested := strings.TrimSpace(r.URL.Query().Get("user_id")); requested != "" && requested != userID {
			writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "resource not found"})
			return
		}
		projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
		if projectID != "" && !workspaceProjectOwned(w, store, projectID, userID) {
			return
		}
		memories, err := store.ListActiveMemories(userID, projectID)
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "memories": memories})
	case http.MethodPost:
		var input struct {
			ID                string `json:"id"`
			UserID            string `json:"userId"`
			Body              string `json:"body"`
			Origin            string `json:"origin"`
			Evidence          string `json:"evidence"`
			SubjectProjectID  string `json:"subjectProjectId"`
			SubjectArtifactID string `json:"subjectArtifactId"`
			SubjectVersionID  string `json:"subjectVersionId"`
			SubjectFrameID    string `json:"subjectFrameId"`
			SourceFrameID     string `json:"sourceFrameId"`
			CategoryID        string `json:"categoryId"`
		}
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if strings.TrimSpace(input.UserID) == "" {
			input.UserID = userID
		}
		if origin := strings.TrimSpace(input.Origin); origin != "" && origin != "user" {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "origin is server assigned and must be user"})
			return
		}
		bodyInput := input.Body
		if bodyInput == "" || memorypolicy.UTF16Length(bodyInput) > memorypolicy.TextMaxUTF16Units {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "memory text must contain 1 to 1000 characters"})
			return
		}
		body, err := workspace.PrepareUserMemoryBody(bodyInput)
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		evidence := strings.ToLower(strings.TrimSpace(input.Evidence))
		if evidence == "" {
			evidence = "stated"
		}
		if evidence != "stated" && evidence != "observed" && evidence != "inferred" {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "evidence must be stated, observed, or inferred"})
			return
		}
		memory, err := store.CreateMemoryOwned(workspaceMutationContext(r), workspace.CreateMemoryInput{
			ID: input.ID, UserID: input.UserID, Body: body, Origin: "user",
			Evidence: evidence, SubjectProjectID: input.SubjectProjectID,
			SubjectArtifactID: input.SubjectArtifactID, SubjectVersionID: input.SubjectVersionID,
			SubjectFrameID: input.SubjectFrameID, SourceFrameID: input.SourceFrameID,
			CategoryID: input.CategoryID,
		}, userID)
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "memory": memory})
	default:
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
	}
}

func (s *Server) handleWorkspaceMemory(w http.ResponseWriter, r *http.Request) {
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	segments := workspacePathSegments(strings.TrimPrefix(r.URL.Path, "/api/go/memories/"))
	if len(segments) != 2 || segments[1] != "supersede" || r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "workspace endpoint not found"})
		return
	}
	memoryID, err := url.PathUnescape(segments[0])
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid memory id"})
		return
	}
	var input struct {
		ReplacementID string `json:"replacementId"`
	}
	if err := decodeWorkspaceJSON(r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if err := store.SupersedeMemoryOwned(workspaceMutationContext(r), memoryID, input.ReplacementID, userID); err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true})
}
