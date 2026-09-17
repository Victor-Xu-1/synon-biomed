package server

import (
	"context"
	"errors"
	"strings"

	eventjournal "synon-go/internal/persistence/journal"
	workspace "synon-go/internal/persistence/workspace"
)

// prepareRunnerUserArtifactsForProvider verifies the immutable attachment
// authority before the first model request without eagerly copying attachment
// bytes into the task workspace. read_file consumes versions directly from the
// artifact store, while host.artifact_path performs the capacity-aware raw-path
// materialization lazily only when a specialist reader actually needs it.
func (s *Server) prepareRunnerUserArtifactsForProvider(
	ctx context.Context,
	sessionID string,
	entries []eventjournal.Entry,
) ([]eventjournal.Entry, error) {
	hasReferences := false
	for _, entry := range entries {
		refs, err := decodeUserArtifactReferences(entry.Message["artifactRefs"])
		if err != nil {
			return nil, err
		}
		if len(refs) > 0 {
			hasReferences = true
			break
		}
	}
	if !hasReferences {
		return entries, nil
	}
	access, _, authorized := s.resolveAgentWorkspaceAuthority(ctx, sessionID)
	if !authorized {
		return nil, errors.New("attached inputs require an authorized task workspace")
	}
	return s.validateRunnerUserArtifactsForFrame(ctx, access, entries)
}

func (s *Server) validateRunnerUserArtifactsForFrame(
	ctx context.Context,
	access workspace.KernelFrameAccess,
	entries []eventjournal.Entry,
) ([]eventjournal.Entry, error) {
	if s == nil || s.workspaceStore == nil || ctx == nil || access.UserID == "" ||
		access.Frame.ID == "" || access.Frame.ProjectID == "" {
		return nil, errors.New("attached input validation is unavailable")
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	result := append([]eventjournal.Entry(nil), entries...)
	verified := map[string]bool{}
	for index, entry := range entries {
		refs, err := decodeUserArtifactReferences(entry.Message["artifactRefs"])
		if err != nil {
			return nil, err
		}
		if len(refs) == 0 {
			continue
		}
		for _, ref := range refs {
			if verified[ref.VersionID] {
				continue
			}
			artifact, version, found, lookupErr := s.workspaceStore.GetArtifactVersionMetadata(ref.VersionID)
			if lookupErr != nil {
				return nil, lookupErr
			}
			if !found || artifact.ID != ref.ArtifactID || version.ArtifactID != ref.ArtifactID ||
				artifact.ProjectID != access.Frame.ProjectID || artifact.Name != ref.Filename ||
				version.SizeBytes != ref.SizeBytes || !strings.EqualFold(version.ContentSHA256, ref.Checksum) {
				return nil, errors.New("attached input metadata no longer matches its immutable artifact version")
			}
			verified[ref.VersionID] = true
		}
		message := make(eventjournal.Message, len(entry.Message))
		for key, value := range entry.Message {
			message[key] = value
		}
		result[index].Message = message
	}
	return result, nil
}
