package server

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"synon-go/internal/runtimecontrol"
)

var agentSavedArtifactReferencePattern = regexp.MustCompile(
	`\{\{artifact:(?:art_)?([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})\}\}`,
)

type agentSavedArtifactReferenceResolution struct {
	projectID    string
	relativePath string
}

// normalizeAgentSavedArtifactReferenceText is the one publication contract for
// references inside durable companion files. Final chat text uses immutable
// version references returned by save_artifacts; saved reports use portable
// workspace-relative links. Only exact project-scoped artifact or version IDs
// are normalized. Unknown, cross-project and self references remain unchanged
// so the existing completion gate can reject them visibly.
func normalizeAgentSavedArtifactReferenceText(
	content, currentPath, projectID string,
	resolve func(string) (agentSavedArtifactReferenceResolution, bool, error),
) (string, bool, error) {
	if resolve == nil || strings.TrimSpace(content) == "" {
		return content, false, nil
	}
	currentPath, err := normalizeAgentSavedArtifactPath(currentPath)
	if err != nil {
		return "", false, err
	}
	normalized := content
	for _, match := range agentSavedArtifactReferencePattern.FindAllStringSubmatch(content, -1) {
		if len(match) != 2 {
			continue
		}
		resolution, found, resolveErr := resolve(match[1])
		if resolveErr != nil {
			return "", false, resolveErr
		}
		if !found || strings.TrimSpace(resolution.projectID) != strings.TrimSpace(projectID) {
			continue
		}
		targetPath, pathErr := normalizeAgentSavedArtifactPath(resolution.relativePath)
		if pathErr != nil || strings.EqualFold(targetPath, currentPath) {
			continue
		}
		relativeTarget, relErr := filepath.Rel(
			filepath.Dir(filepath.FromSlash(currentPath)), filepath.FromSlash(targetPath),
		)
		if relErr != nil || relativeTarget == "." || filepath.IsAbs(relativeTarget) {
			continue
		}
		normalized = strings.ReplaceAll(normalized, match[0], filepath.ToSlash(relativeTarget))
	}
	return normalized, normalized != content, nil
}

func (s *Server) resolveAgentSavedArtifactReference(
	identifier string,
) (agentSavedArtifactReferenceResolution, bool, error) {
	if s == nil || s.workspaceStore == nil {
		return agentSavedArtifactReferenceResolution{}, false, errors.New("artifact reference authority is unavailable")
	}
	artifact, _, found, err := s.workspaceStore.GetCurrentArtifactVersionMetadata(identifier)
	if err != nil {
		return agentSavedArtifactReferenceResolution{}, false, err
	}
	if !found {
		artifact, _, found, err = s.workspaceStore.GetArtifactVersionMetadata(identifier)
		if err != nil {
			return agentSavedArtifactReferenceResolution{}, false, err
		}
	}
	if !found {
		return agentSavedArtifactReferenceResolution{}, false, nil
	}
	relativePath := strings.TrimSpace(artifact.Name)
	if s.runtimeStore != nil {
		entry, metadataFound, metadataErr := s.runtimeStore.Get(artifactRuntimeNamespace, artifact.ID)
		if metadataErr != nil {
			return agentSavedArtifactReferenceResolution{}, false, metadataErr
		}
		if metadataFound {
			if candidate := strings.TrimSpace(stringValue(mapValue(entry.Value)["relativePath"])); candidate != "" {
				relativePath = candidate
			}
		}
	}
	return agentSavedArtifactReferenceResolution{
		projectID: artifact.ProjectID, relativePath: relativePath,
	}, true, nil
}

func (s *Server) normalizeAgentSavedArtifactReferences(
	ctx context.Context,
	snapshot *os.File,
	relativePath string,
	projectID string,
) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if snapshot == nil {
		return false, errors.New("artifact snapshot is unavailable")
	}
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(relativePath))) {
	case ".md", ".txt", ".html", ".htm", ".csv", ".tsv":
	default:
		return false, nil
	}
	if _, err := snapshot.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	normalized, err := os.CreateTemp(filepath.Dir(snapshot.Name()), "synon-artifact-links-*")
	if err != nil {
		return false, err
	}
	defer func() { _ = normalized.Close(); _ = os.Remove(normalized.Name()) }()
	changed, err := streamAgentSavedArtifactReferences(ctx, snapshot,
		runtimecontrol.DiskCapacityWriter(ctx, normalized, normalized.Name()), relativePath, projectID, s.resolveAgentSavedArtifactReference)
	if err != nil {
		return false, err
	}
	if !changed {
		_, err = snapshot.Seek(0, io.SeekStart)
		return false, err
	}
	if err := snapshot.Truncate(0); err != nil {
		return false, err
	}
	if _, err := snapshot.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	if _, err := normalized.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	if _, err := io.Copy(runtimecontrol.DiskCapacityWriter(ctx, snapshot, snapshot.Name()), &contextReader{ctx: ctx, reader: normalized}); err != nil {
		return false, err
	}
	if _, err := snapshot.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	return true, nil
}

// The reference grammar is finite. Retain exactly enough overlap to avoid
// splitting any accepted token; arbitrary surrounding text streams unchanged.
func streamAgentSavedArtifactReferences(ctx context.Context, source io.Reader, destination io.Writer,
	currentPath, projectID string, resolve func(string) (agentSavedArtifactReferenceResolution, bool, error),
) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	const overlap = len("{{artifact:art_00000000-0000-0000-0000-000000000000}}")
	buffer := make([]byte, 64<<10)
	reader := &contextReader{ctx: ctx, reader: source}
	carry, changed := 0, false
	for {
		n, readErr := reader.Read(buffer[carry:])
		length := carry + n
		cut := length
		if readErr == nil {
			cut = max(0, length-overlap)
			for _, match := range agentSavedArtifactReferencePattern.FindAllIndex(buffer[:length], -1) {
				if match[0] < cut && match[1] > cut {
					cut = match[0]
					break
				}
			}
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return false, readErr
		}
		if cut > 0 {
			part, replaced, err := normalizeAgentSavedArtifactReferenceText(string(buffer[:cut]), currentPath, projectID, resolve)
			if err != nil {
				return false, err
			}
			if n, err := io.WriteString(destination, part); err != nil {
				return false, err
			} else if n != len(part) {
				return false, io.ErrShortWrite
			}
			changed = changed || replaced
		}
		if errors.Is(readErr, io.EOF) {
			return changed, nil
		}
		carry = copy(buffer, buffer[cut:length])
	}
}
