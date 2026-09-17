package server

import (
	"context"
	"errors"
)

// Repair a container/version mix-up only when the immutable choice is unique.
// Multiple versions produce a pinned, executable recovery handle, never a
// silently moving "latest" page. Check project ownership before returning IDs.
func (s *Server) resolveAgentWorkspaceArtifactHandle(ctx context.Context, userID, projectID, handle string) (agentWorkspaceReadableVersion, map[string]any, error) {
	artifact, version, found, err := s.workspaceStore.GetCurrentArtifactVersionMetadata(handle)
	if err != nil || !found || artifact.ProjectID != projectID {
		return agentWorkspaceReadableVersion{}, nil, errors.New("read_file artifact version is unavailable")
	}
	if err := context.Cause(ctx); err != nil {
		return agentWorkspaceReadableVersion{}, nil, err
	}
	if artifact.CurrentVersionNumber != 1 {
		return agentWorkspaceReadableVersion{}, map[string]any{"ok": false, "code": "artifact_version_required", "error": "This is an artifact identity with multiple versions. Select an immutable version; the current version is available through read_with.", "read_with": map[string]any{"version_id": version.ID, "human_description": "Reading an immutable artifact version"}}, nil
	}
	readable, found, err := s.openAgentWorkspaceReadableVersion(ctx, userID, projectID, version.ID)
	if err != nil || !found {
		return agentWorkspaceReadableVersion{}, nil, errors.New("read_file artifact version is unavailable")
	}
	return readable, nil, nil
}
