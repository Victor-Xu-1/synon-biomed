package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	workspace "synon-go/internal/persistence/workspace"
)

type compatibilityArtifactFolderBody struct {
	FolderID  json.RawMessage `json:"folder_id"`
	SortOrder json.RawMessage `json:"sort_order"`
}

func (s *Server) handleArtifactCompatibility(w http.ResponseWriter, r *http.Request) {
	if s.workspaceStore == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "Workspace runtime is not configured")
		return
	}
	segments := workspacePathSegments(strings.TrimPrefix(r.URL.Path, "/api/artifacts/"))
	if len(segments) != 2 || segments[1] != "folder" {
		writeV11Detail(w, http.StatusNotFound, "Artifact endpoint not found")
		return
	}
	artifactID, err := url.PathUnescape(segments[0])
	if err != nil || strings.TrimSpace(artifactID) == "" {
		writeV11Detail(w, http.StatusBadRequest, "Invalid artifact id")
		return
	}
	if r.Method != http.MethodPatch {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	input, err := decodeCompatibilityArtifactFolderUpdate(r)
	if err != nil {
		writeV11Detail(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.workspaceStore.UpdateCompatibilityArtifactFolderRealtime(
		r.Context(), compatAgentUserID(r), artifactID, input,
	)
	if err != nil {
		switch {
		case errors.Is(err, workspace.ErrCompatibilityArtifactNotFound):
			writeV11Detail(w, http.StatusNotFound, "Artifact "+artifactID+" not found")
		case errors.Is(err, workspace.ErrCompatibilityFolderMoveTarget):
			detail := "Folder not found"
			if input.FolderID != "" {
				detail = "Folder " + input.FolderID + " not found"
			}
			writeV11Detail(w, http.StatusNotFound, detail)
		case errors.Is(err, workspace.ErrCompatibilityArtifactUploadMove):
			writeV11Detail(w, http.StatusBadRequest, "User-uploaded files can only be moved within the User Uploads folder")
		default:
			writeV11StoreError(w, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func decodeCompatibilityArtifactFolderUpdate(r *http.Request) (workspace.UpdateCompatibilityArtifactFolderInput, error) {
	var body compatibilityArtifactFolderBody
	if err := decodeAgentCompatJSON(r, &body); err != nil {
		return workspace.UpdateCompatibilityArtifactFolderInput{}, errors.New("Invalid arguments: " + err.Error())
	}
	input := workspace.UpdateCompatibilityArtifactFolderInput{}
	if len(body.FolderID) > 0 {
		input.FolderSet = true
		input.FolderNull = jsonNull(body.FolderID)
		if !input.FolderNull {
			value, err := compatibilityRequiredString(body.FolderID, "folderId")
			if err != nil {
				return input, err
			}
			input.FolderID = *value
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
