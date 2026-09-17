package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) saveCompatibilityEditedPlan(frame workspace.CompatibilityFrame, contextData, editedPlan map[string]any) (string, error) {
	artifactID := stringValue(contextData["_plan_artifact_id"])
	artifacts, err := s.workspaceStore.ListArtifactsForRoot(frame.RootFrameID, 1000)
	if err != nil {
		return "", err
	}
	var plan workspace.Artifact
	for _, artifact := range artifacts {
		if artifact.ID == artifactID {
			plan = artifact
			break
		}
	}
	if plan.ID == "" {
		return "", fmt.Errorf("Plan artifact no longer exists \u2014 discard and regenerate the plan.")
	}
	name := strings.ToLower(strings.TrimSpace(plan.Name))
	if name != "plan.json" && !(strings.HasPrefix(name, "plan_") && strings.HasSuffix(name, ".json")) {
		return "", fmt.Errorf("Cannot save an edited plan to %q \u2014 expected a plan.json or plan_*.json filename. If you renamed the plan artifact, rename it back or use Approve without edits.", plan.Name)
	}
	_, history, found, err := s.workspaceStore.ListArtifactVersionHistory(plan.ID)
	if err != nil {
		return "", err
	}
	if !found || len(history) == 0 {
		return "", fmt.Errorf("Plan artifact no longer exists \u2014 discard and regenerate the plan.")
	}
	content, err := json.MarshalIndent(editedPlan, "", "  ")
	if err != nil {
		return "", err
	}
	parentID := history[len(history)-1].VersionID
	_, version, err := s.workspaceStore.WriteArtifactVersion(context.Background(), workspace.WriteArtifactVersionInput{
		ArtifactID: plan.ID, ProjectID: frame.ProjectID, Name: plan.Name,
		ContentType: "application/json", Content: bytes.NewReader(content),
		CreatedBy: "user", MaxBytes: 4 << 20, ParentVersionID: parentID,
		ProvenanceSourceID: parentID, FreshUserEditMappings: true,
		RootFrameID: frame.RootFrameID, FrameID: frame.ID,
	})
	if err != nil {
		return "", err
	}
	return version.ID, nil
}
