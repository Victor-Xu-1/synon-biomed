package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	workspace "synon-go/internal/persistence/workspace"
)

type compatibilityFolderUpdateBody struct {
	Name      json.RawMessage `json:"name"`
	ParentID  json.RawMessage `json:"parent_id"`
	SortOrder json.RawMessage `json:"sort_order"`
}

func (s *Server) handleCompatibilityProjectFolders(w http.ResponseWriter, r *http.Request, projectID, folderID string) {
	if s.workspaceStore == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "Workspace runtime is not configured")
		return
	}
	userID := compatAgentUserID(r)
	if folderID != "" {
		if r.Method == http.MethodDelete {
			query := r.URL.Query()
			deleteArtifacts := true
			if query.Has("delete_artifacts") {
				value := query.Get("delete_artifacts")
				deleteArtifacts = value == "true" || value == "1"
			}
			result, err := s.workspaceStore.DeleteCompatibilityFolderRealtime(
				r.Context(), userID, projectID, folderID, strings.TrimSpace(query.Get("move_artifacts_to")), deleteArtifacts,
			)
			if err != nil {
				if errors.Is(err, workspace.ErrCompatibilityFolderSystem) {
					writeV11Detail(w, http.StatusForbidden, "Cannot delete system folders")
					return
				}
				writeCompatibilityFolderError(w, err, projectID, folderID, strings.TrimSpace(query.Get("move_artifacts_to")))
				return
			}
			if err := s.workspaceStore.RemoveArtifactBlobs(result.BlobPaths); err != nil {
				writeV11StoreError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, result)
			return
		}
		if r.Method != http.MethodPatch {
			writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		input, err := decodeCompatibilityFolderUpdate(r)
		if err != nil {
			writeV11Detail(w, http.StatusBadRequest, err.Error())
			return
		}
		folder, err := s.workspaceStore.UpdateCompatibilityFolderRealtime(r.Context(), userID, projectID, folderID, input)
		if err != nil {
			if errors.Is(err, workspace.ErrCompatibilityFolderProjectNotFound) {
				writeV11Detail(w, http.StatusForbidden, "Project "+projectID+" not found")
				return
			}
			writeCompatibilityFolderError(w, err, projectID, folderID, "")
			return
		}
		writeJSON(w, http.StatusOK, folder)
		return
	}
	switch r.Method {
	case http.MethodGet:
		folders, found, err := s.workspaceStore.ListCompatibilityFolders(r.Context(), userID, projectID)
		if err != nil {
			writeV11StoreError(w, err)
			return
		}
		if !found {
			writeV11Detail(w, http.StatusNotFound, "Project "+projectID+" not found")
			return
		}
		writeJSON(w, http.StatusOK, folders)
	case http.MethodPost:
		var body struct {
			Name     *string `json:"name"`
			ParentID *string `json:"parent_id"`
		}
		if err := decodeAgentCompatJSON(r, &body); err != nil {
			writeV11Detail(w, http.StatusBadRequest, "Invalid arguments: "+err.Error())
			return
		}
		name := ""
		if body.Name != nil {
			name = *body.Name
		}
		parentID := ""
		if body.ParentID != nil {
			parentID = *body.ParentID
		}
		folder, err := s.workspaceStore.CreateCompatibilityFolderRealtime(r.Context(), userID, projectID, name, parentID)
		if err != nil {
			writeCompatibilityFolderError(w, err, projectID, "", parentID)
			return
		}
		writeJSON(w, http.StatusCreated, folder)
	default:
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func decodeCompatibilityFolderUpdate(r *http.Request) (workspace.UpdateCompatibilityFolderInput, error) {
	var body compatibilityFolderUpdateBody
	if err := decodeAgentCompatJSON(r, &body); err != nil {
		return workspace.UpdateCompatibilityFolderInput{}, fmt.Errorf("Invalid arguments: %w", err)
	}
	input := workspace.UpdateCompatibilityFolderInput{}
	if len(body.Name) > 0 {
		value, err := compatibilityRequiredString(body.Name, "name")
		if err != nil {
			return input, err
		}
		input.NameSet, input.Name = true, *value
	}
	if len(body.ParentID) > 0 {
		input.ParentSet = true
		if !jsonNull(body.ParentID) {
			value, err := compatibilityRequiredString(body.ParentID, "parentId")
			if err != nil {
				return input, err
			}
			input.ParentID = *value
		}
	}
	if len(body.SortOrder) > 0 {
		if jsonNull(body.SortOrder) {
			return input, errors.New("Invalid arguments: sortOrder: Expected number, received null")
		}
		if err := json.Unmarshal(body.SortOrder, &input.SortOrder); err != nil {
			return input, errors.New("Invalid arguments: sortOrder: Expected number")
		}
		input.SortOrderSet = true
	}
	return input, nil
}

func jsonNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

func writeCompatibilityFolderError(w http.ResponseWriter, err error, projectID, folderID, parentID string) {
	switch {
	case errors.Is(err, workspace.ErrCompatibilityFolderProjectNotFound):
		writeV11Detail(w, http.StatusNotFound, "Project "+projectID+" not found")
	case errors.Is(err, workspace.ErrCompatibilityFolderNotFound):
		writeV11Detail(w, http.StatusNotFound, "Folder "+folderID+" not found")
	case errors.Is(err, workspace.ErrCompatibilityFolderParentNotFound):
		writeV11Detail(w, http.StatusNotFound, "Parent folder "+parentID+" not found")
	case errors.Is(err, workspace.ErrCompatibilityFolderSystem):
		writeV11Detail(w, http.StatusForbidden, "Cannot rename or move system folders")
	case errors.Is(err, workspace.ErrCompatibilityFolderConversationParent):
		writeV11Detail(w, http.StatusBadRequest, "Cannot nest folders under a conversation folder")
	case errors.Is(err, workspace.ErrCompatibilityFolderSelfParent):
		writeV11Detail(w, http.StatusBadRequest, "Folder cannot be its own parent")
	case errors.Is(err, workspace.ErrCompatibilityFolderCycle):
		writeV11Detail(w, http.StatusBadRequest, "Cannot move folder into itself")
	case errors.Is(err, workspace.ErrCompatibilityFolderMoveTarget):
		if parentID != "" {
			writeV11Detail(w, http.StatusNotFound, "Folder "+parentID+" not found")
			return
		}
		writeV11Detail(w, http.StatusBadRequest, "Cannot move artifacts into a folder being deleted")
	case errors.Is(err, workspace.ErrCompatibilityFolderMoveIntoDeletedTree):
		writeV11Detail(w, http.StatusBadRequest, "Cannot move artifacts into a folder being deleted")
	case errors.Is(err, workspace.ErrCompatibilityArtifactUploadMove):
		writeV11Detail(w, http.StatusBadRequest, "User-uploaded files can only be moved within the User Uploads folder")
	case err.Error() == "name is required":
		writeV11Detail(w, http.StatusBadRequest, err.Error())
	default:
		writeV11StoreError(w, err)
	}
}
