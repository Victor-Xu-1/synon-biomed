package server

import (
	"net/http"

	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) handleWorkspaceFrameArtifacts(w http.ResponseWriter, r *http.Request, store *workspace.Store, rootFrameID string) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	artifacts, err := store.ListArtifactsForRoot(rootFrameID, workspaceListQuery(r, "limit", 100, 1))
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	visible := make([]workspace.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if isRunnerLargeToolResultArtifactID(artifact.ID) {
			continue
		}
		retention, intermediate, found, stateErr := store.ArtifactCurrentPresentationState(artifact.ID)
		if stateErr != nil {
			writeWorkspaceJSON(w, workspaceStatus(stateErr), map[string]any{"ok": false, "error": stateErr.Error()})
			return
		}
		if !found || retention == "working_data" || intermediate {
			continue
		}
		visible = append(visible, artifact)
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "rootFrameId": rootFrameID, "artifacts": visible})
}

func (s *Server) handleWorkspaceArtifactPriority(w http.ResponseWriter, r *http.Request, store *workspace.Store, artifactID, userID string) {
	if r.Method != http.MethodPatch {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	var input struct {
		Priority *workspace.ArtifactPriority `json:"priority"`
	}
	if err := decodeWorkspaceJSON(r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if input.Priority == nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "priority is required"})
		return
	}
	artifact, err := store.UpdateArtifactPriorityRealtime(workspaceMutationContext(r), artifactID, userID, *input.Priority)
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "artifact": artifact})
}

func (s *Server) handleWorkspaceArtifactBulkMove(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID string) {
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	var input struct {
		ArtifactIDs      []string `json:"artifactIds"`
		ArtifactIDsSnake []string `json:"artifact_ids"`
		FolderID         string   `json:"folderId"`
		FolderIDSnake    string   `json:"folder_id"`
	}
	if err := decodeWorkspaceJSON(r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	artifactIDs := input.ArtifactIDs
	if len(artifactIDs) == 0 {
		artifactIDs = input.ArtifactIDsSnake
	}
	if err := validateArtifactIDs(store, userID, artifactIDs); err != nil {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "resource not found"})
		return
	}
	artifacts, err := store.BulkMoveArtifactsRealtime(workspaceMutationContext(r), artifactIDs, firstNonEmpty(input.FolderID, input.FolderIDSnake), userID)
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "artifacts": artifacts, "moved": len(artifacts)})
}
