package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	workspace "synon-go/internal/persistence/workspace"
)

const (
	maxWebConversationInputFileBytes  int64 = 64 << 20
	maxWebConversationInputTotalBytes int64 = 256 << 20
)

// materializeWebConversationInputFiles makes server-selected files available
// inside an isolated Web task workspace. The browser's server-side file picker
// returns host-absolute paths, while scientific runtimes mount only the task
// workspace. Persisting the source path makes the attachment visible in the UI
// but unreadable by every runtime tool.
func (s *Server) materializeWebConversationInputFiles(
	ctx context.Context,
	frame workspace.CompatibilityFrame,
	files []string,
) ([]string, error) {
	if len(files) == 0 {
		return nil, nil
	}
	access, found, err := s.workspaceStore.GetKernelFrameAccessContext(ctx, frame.ID)
	if err != nil {
		return nil, transcriptWebStorageError(err)
	}
	if !found {
		return nil, &webConversationRequestError{Status: http.StatusNotFound, Detail: "conversation not found"}
	}
	// Repository-backed conversations retain their existing explicit-path
	// contract. Managed Web workspaces require task-local materialization.
	if strings.TrimSpace(access.ProjectPath) != "" {
		return append([]string(nil), files...), nil
	}
	taskRoot, err := s.defaultAgentKernelTaskWorkspace(
		access.Frame.ProjectID, access.Frame.RootFrameID, access.RootFrameIncarnationID,
	)
	if err != nil {
		return nil, transcriptWebStorageError(err)
	}
	taskRoot, err = canonicalOrCreateAgentWorkspaceDirectory(taskRoot, 0o700)
	if err != nil {
		return nil, transcriptWebStorageError(err)
	}
	taskDirectory, err := os.OpenRoot(taskRoot)
	if err != nil {
		return nil, transcriptWebStorageError(err)
	}
	defer taskDirectory.Close()
	if err := taskDirectory.Mkdir("inputs", 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, transcriptWebStorageError(err)
	}
	inputRoot, err := taskDirectory.OpenRoot("inputs")
	if err != nil {
		return nil, invalidWebConversationInputFile()
	}
	defer inputRoot.Close()

	materialized := make([]string, 0, len(files))
	seen := make(map[string]struct{}, len(files))
	var total int64
	for _, raw := range files {
		requested := strings.TrimSpace(raw)
		if requested == "" {
			continue
		}
		if !filepath.IsAbs(requested) {
			cleaned := filepath.Clean(requested)
			if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
				return nil, invalidWebConversationInputFile()
			}
			materialized = appendUniqueWebConversationFile(materialized, seen, filepath.ToSlash(cleaned))
			continue
		}

		allowLocalRead := (s.synonLinkAuth == nil || !s.synonLinkAuth.enabled)
		sourceAccess, err := s.resolveWebFSPath(access.UserID, requested, "", false, true, allowLocalRead)
		if err != nil {
			return nil, invalidWebConversationInputFile()
		}
		sourceFile, err := openWebFSRegularFile(sourceAccess)
		if err != nil {
			return nil, invalidWebConversationInputFile()
		}
		info, err := sourceFile.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxWebConversationInputFileBytes {
			_ = sourceFile.Close()
			return nil, invalidWebConversationInputFile()
		}
		if info.Size() > maxWebConversationInputTotalBytes-total {
			_ = sourceFile.Close()
			return nil, &webConversationRequestError{Status: http.StatusRequestEntityTooLarge, Detail: "selected files exceed the task input limit"}
		}
		if relative, inside := relativePathWithin(taskRoot, sourceAccess.Target); inside {
			if err := sourceFile.Close(); err != nil {
				return nil, invalidWebConversationInputFile()
			}
			total += info.Size()
			materialized = appendUniqueWebConversationFile(materialized, seen, relative)
			continue
		}

		limit := min(maxWebConversationInputFileBytes, maxWebConversationInputTotalBytes-total)
		relative, size, copyErr := copyWebConversationInputFile(ctx, sourceFile, filepath.Base(sourceAccess.Target), inputRoot, limit)
		err = errors.Join(copyErr, sourceFile.Close())
		if err != nil {
			return nil, invalidWebConversationInputFile()
		}
		total += size
		materialized = appendUniqueWebConversationFile(materialized, seen, relative)
	}
	return materialized, nil
}

func invalidWebConversationInputFile() error {
	return &webConversationRequestError{
		Status: http.StatusBadRequest,
		Detail: "a selected file could not be imported into the task workspace",
	}
}

func appendUniqueWebConversationFile(target []string, seen map[string]struct{}, value string) []string {
	value = filepath.ToSlash(strings.TrimSpace(value))
	if value == "" {
		return target
	}
	if _, exists := seen[value]; exists {
		return target
	}
	seen[value] = struct{}{}
	return append(target, value)
}

func rewriteWebConversationAttachedFilePaths(content string, original, materialized []string) string {
	for index := 0; index < len(original) && index < len(materialized); index++ {
		source := strings.TrimSpace(original[index])
		target := strings.TrimSpace(materialized[index])
		if source == "" || target == "" || source == target {
			continue
		}
		content = strings.ReplaceAll(content, source, target)
	}
	return content
}

func relativePathWithin(root, target string) (string, bool) {
	relative, err := filepath.Rel(root, target)
	if err != nil || filepath.IsAbs(relative) || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(relative), true
}

func copyWebConversationInputFile(ctx context.Context, source *os.File, name string, inputRoot *os.Root, maxBytes int64) (relative string, size int64, err error) {
	if name != filepath.Base(name) || name == "." || name == ".." || strings.TrimSpace(name) == "" {
		return "", 0, errors.New("invalid input filename")
	}
	before, err := source.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() > maxBytes {
		return "", 0, invalidWebConversationInputFile()
	}
	stage, err := stageWorkspaceFile(ctx, inputRoot, source, maxBytes)
	if err != nil {
		return "", 0, err
	}
	defer func() { err = errors.Join(err, stage.close()) }()
	after, err := source.Stat()
	if err != nil || before.Size() != stage.size || after.Size() != stage.size || !before.ModTime().Equal(after.ModTime()) {
		return "", 0, errWorkspaceFileConflict
	}
	err = stage.publish(ctx, name)
	if errors.Is(err, errWorkspaceFileConflict) {
		// A conflicting regular basename gets a deterministic alternative, but
		// neither that alternative nor a symlink may be replaced or followed.
		info, statErr := inputRoot.Lstat(name)
		if statErr != nil || !info.Mode().IsRegular() {
			return "", 0, err
		}
		extension := filepath.Ext(name)
		name = fmt.Sprintf("%s-%s%s", strings.TrimSuffix(name, extension), stage.digest[:12], extension)
		err = stage.publish(ctx, name)
	}
	if err != nil {
		return "", 0, err
	}
	return filepath.ToSlash(filepath.Join("inputs", name)), stage.size, nil
}
