package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) publishProjectEvent(projectID, eventType string, payload map[string]any) (workspace.RealtimeEvent, error) {
	if s == nil || s.workspaceStore == nil {
		return workspace.RealtimeEvent{}, errors.New("workspace runtime is not configured")
	}
	projectID = strings.TrimSpace(projectID)
	userID, found, err := s.workspaceStore.ProjectOwnerID(projectID)
	if err != nil {
		return workspace.RealtimeEvent{}, err
	}
	if !found {
		return workspace.RealtimeEvent{}, fmt.Errorf("project %q does not exist for realtime publication", projectID)
	}
	cloned := make(map[string]any, len(payload)+1)
	for key, value := range payload {
		cloned[key] = value
	}
	cloned["project_id"] = projectID
	return s.publishCompatEvent(workspace.RealtimeEventInput{
		UserID: userID, ProjectID: projectID, Type: eventType, Payload: cloned,
	})
}

func (s *Server) publishUserEvent(userID, eventType string, payload map[string]any) (workspace.RealtimeEvent, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return workspace.RealtimeEvent{}, errors.New("realtime event user id is required")
	}
	return s.publishCompatEvent(workspace.RealtimeEventInput{UserID: userID, Type: eventType, Payload: payload})
}

func (s *Server) publishGlobalEvent(eventType string, payload map[string]any) (workspace.RealtimeEvent, error) {
	return s.publishCompatEvent(workspace.RealtimeEventInput{Type: eventType, Payload: payload})
}

func (s *Server) publishArtifactVersionEvents(artifact workspace.Artifact, version workspace.ArtifactVersion) error {
	payload := map[string]any{
		"artifact_id": artifact.ID, "version_id": version.ID,
		"version_ids": []string{version.ID}, "version_number": version.VersionNumber,
		"name": artifact.Name, "kind": artifact.Kind,
	}
	if _, err := s.publishProjectEvent(artifact.ProjectID, "artifact_created", payload); err != nil {
		return err
	}
	if _, err := s.publishProjectEvent(artifact.ProjectID, "lineage_ready", payload); err != nil {
		return err
	}
	return nil
}

func (s *Server) publishRoutineEvent(routine workspace.Routine, action string) error {
	frameContext, found, err := s.workspaceStore.GetFrameRealtimeContext(routine.RootFrameID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("routine root frame %q does not exist for realtime publication", routine.RootFrameID)
	}
	_, err = s.publishCompatEvent(workspace.RealtimeEventInput{
		UserID: frameContext.UserID, ProjectID: frameContext.Frame.ProjectID,
		RootFrameID: frameContext.Frame.RootFrameID, FrameID: frameContext.Frame.ID,
		Type: "routine_update", Payload: map[string]any{
			"project_id": frameContext.Frame.ProjectID, "root_frame_id": frameContext.Frame.RootFrameID,
			"frame_id": frameContext.Frame.ID, "routine_id": routine.ID, "action": action,
		},
	})
	return err
}

func writeDomainEventError(w http.ResponseWriter, operation string, err error) {
	writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{
		"ok": false, "error": operation + " succeeded but its realtime event could not be persisted: " + err.Error(),
	})
}
