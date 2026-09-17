package server

import (
	"net/http"
	"net/url"
	"strings"

	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) handleWorkspaceProjectFolders(w http.ResponseWriter, r *http.Request, store *workspace.Store, projectID, userID string) {
	switch r.Method {
	case http.MethodGet:
		folders, err := store.ListArtifactFolders(projectID)
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "folders": folders})
	case http.MethodPost:
		var input struct {
			ID                   string `json:"id"`
			ParentID             string `json:"parentId"`
			Name                 string `json:"name"`
			SortOrder            int    `json:"sortOrder"`
			RootFrameID          string `json:"rootFrameId"`
			IsConversationFolder bool   `json:"isConversationFolder"`
			IsUserUploadsFolder  bool   `json:"isUserUploadsFolder"`
		}
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		folder, err := store.CreateArtifactFolderRealtime(workspaceMutationContext(r), workspace.CreateArtifactFolderInput{
			ID: input.ID, ProjectID: projectID, ParentID: input.ParentID, Name: input.Name,
			SortOrder: input.SortOrder, RootFrameID: input.RootFrameID,
			IsConversationFolder: input.IsConversationFolder,
			IsUserUploadsFolder:  input.IsUserUploadsFolder,
		}, userID)
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "folder": folder})
	default:
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
	}
}

func (s *Server) handleWorkspaceFolder(w http.ResponseWriter, r *http.Request) {
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return
	}
	folderID, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/api/go/folders/"))
	if err != nil || folderID == "" || strings.Contains(folderID, "/") {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid folder id"})
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	existing, found, err := store.GetArtifactFolder(folderID)
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if !found {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "resource not found"})
		return
	}
	if !workspaceProjectOwned(w, store, existing.ProjectID, userID) {
		return
	}
	switch r.Method {
	case http.MethodPatch:
		var input struct {
			ParentID  *string `json:"parentId"`
			Name      *string `json:"name"`
			SortOrder *int    `json:"sortOrder"`
		}
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		folder, err := store.UpdateArtifactFolderRealtime(workspaceMutationContext(r), folderID, userID, workspace.UpdateArtifactFolderInput{
			ParentID: input.ParentID, Name: input.Name, SortOrder: input.SortOrder,
		})
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "folder": folder})
	case http.MethodDelete:
		if err := store.DeleteArtifactFolderRealtime(workspaceMutationContext(r), folderID, userID); err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
	}
}

func (s *Server) handleWorkspaceArtifactFolder(w http.ResponseWriter, r *http.Request, store *workspace.Store, artifactID, userID string) {
	if r.Method != http.MethodPatch {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	var input struct {
		FolderID string `json:"folderId"`
	}
	if err := decodeWorkspaceJSON(r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	artifact, err := store.SetArtifactFolderRealtime(workspaceMutationContext(r), artifactID, input.FolderID, userID)
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "artifact": artifact})
}

func (s *Server) handleWorkspaceProjectNotes(w http.ResponseWriter, r *http.Request, store *workspace.Store, projectID, userID string) {
	switch r.Method {
	case http.MethodGet:
		notes, err := store.ListProjectNotes(projectID)
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "notes": notes})
	case http.MethodPost:
		var input struct {
			ID                 string `json:"id"`
			UserID             string `json:"userId"`
			TargetType         string `json:"targetType"`
			TargetFrameID      string `json:"targetFrameId"`
			TargetMessageIndex *int   `json:"targetMessageIndex"`
			TargetArtifactID   string `json:"targetArtifactId"`
			Content            string `json:"content"`
		}
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if strings.TrimSpace(input.UserID) == "" {
			input.UserID = userID
		}
		note, err := store.CreateProjectNoteRealtime(workspaceMutationContext(r), workspace.CreateProjectNoteInput{
			ID: input.ID, ProjectID: projectID, UserID: input.UserID,
			TargetType: input.TargetType, TargetFrameID: input.TargetFrameID,
			TargetMessageIndex: input.TargetMessageIndex, TargetArtifactID: input.TargetArtifactID,
			Content: input.Content,
		}, userID)
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "note": note})
	default:
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
	}
}

func (s *Server) handleWorkspaceNote(w http.ResponseWriter, r *http.Request) {
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return
	}
	noteID, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/api/go/notes/"))
	if err != nil || noteID == "" || strings.Contains(noteID, "/") {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid note id"})
		return
	}
	requestUserID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodPatch:
		var input struct {
			UserID  string `json:"userId"`
			Content string `json:"content"`
		}
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		existing, err := store.GetProjectNote(noteID, requestUserID)
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if !workspaceProjectOwned(w, store, existing.ProjectID, requestUserID) {
			return
		}
		note, err := store.UpdateProjectNoteRealtime(workspaceMutationContext(r), noteID, requestUserID, input.Content)
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "note": note})
	case http.MethodDelete:
		existing, err := store.GetProjectNote(noteID, requestUserID)
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if !workspaceProjectOwned(w, store, existing.ProjectID, requestUserID) {
			return
		}
		if err := store.DeleteProjectNoteRealtime(workspaceMutationContext(r), noteID, requestUserID); err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
	}
}
