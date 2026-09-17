package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) handleCompatibilityArtifactMetadata(w http.ResponseWriter, r *http.Request, artifactID string) {
	store, userID, ok := s.compatibilityArtifactControlContext(w, r)
	if !ok {
		return
	}
	artifact, found, err := store.GetCompatibilityArtifactMetadata(r.Context(), userID, artifactID)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	if !found {
		writeV11Detail(w, http.StatusNotFound, "Artifact "+artifactID+" not found")
		return
	}
	writeJSON(w, http.StatusOK, artifact)
}

func (s *Server) handleCompatibilityArtifactCopy(w http.ResponseWriter, r *http.Request, artifactID string) {
	body, err := decodeCompatibilityArtifactCopyBody(r)
	if err != nil {
		writeV11Detail(w, http.StatusBadRequest, err.Error())
		return
	}
	store, userID, ok := s.compatibilityArtifactControlContext(w, r)
	if !ok {
		return
	}
	result, err := store.CopyCompatibilityArtifactRealtime(
		r.Context(), userID, artifactID, body.NewFilename, body.TargetFolderID,
	)
	if err != nil {
		switch {
		case errors.Is(err, workspace.ErrCompatibilityArtifactNotFound):
			writeV11Detail(w, http.StatusNotFound, "Artifact "+artifactID+" not found")
		case errors.Is(err, workspace.ErrCompatibilityArtifactCopyFolder):
			folderID := ""
			if body.TargetFolderID != nil {
				folderID = *body.TargetFolderID
			}
			writeV11Detail(w, http.StatusNotFound, "Folder "+folderID+" not found")
		default:
			writeV11StoreError(w, err)
		}
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

type compatibilityArtifactCopyBody struct {
	NewFilename    *string
	TargetFolderID *string
}

func decodeCompatibilityArtifactCopyBody(r *http.Request) (compatibilityArtifactCopyBody, error) {
	var raw struct {
		NewFilename    json.RawMessage `json:"new_filename"`
		TargetFolderID json.RawMessage `json:"target_folder_id"`
	}
	if err := decodeAgentCompatJSON(r, &raw); err != nil {
		if errors.Is(err, io.EOF) {
			return compatibilityArtifactCopyBody{}, nil
		}
		return compatibilityArtifactCopyBody{}, fmt.Errorf("Invalid arguments: %w", err)
	}
	newFilename, err := compatibilityOptionalNullableString(raw.NewFilename, "newFilename")
	if err != nil {
		return compatibilityArtifactCopyBody{}, err
	}
	targetFolderID, err := compatibilityOptionalNullableString(raw.TargetFolderID, "targetFolderId")
	if err != nil {
		return compatibilityArtifactCopyBody{}, err
	}
	return compatibilityArtifactCopyBody{NewFilename: newFilename, TargetFolderID: targetFolderID}, nil
}

func compatibilityOptionalNullableString(raw json.RawMessage, field string) (*string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var value string
	if err := json.Unmarshal(trimmed, &value); err == nil {
		return &value, nil
	}
	received := "unknown"
	switch trimmed[0] {
	case '{':
		received = "object"
	case '[':
		received = "array"
	case 't', 'f':
		received = "boolean"
	default:
		received = "number"
	}
	return nil, fmt.Errorf("Invalid arguments: %s: Expected string, received %s", field, received)
}
