package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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
	inputRoot, err := canonicalOrCreateAgentWorkspaceDirectory(filepath.Join(taskRoot, "inputs"), 0o700)
	if err != nil {
		return nil, transcriptWebStorageError(err)
	}

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
		source := sourceAccess.Target
		info, err := os.Stat(source)
		if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxWebConversationInputFileBytes {
			return nil, invalidWebConversationInputFile()
		}
		total += info.Size()
		if total > maxWebConversationInputTotalBytes {
			return nil, &webConversationRequestError{Status: http.StatusRequestEntityTooLarge, Detail: "selected files exceed the task input limit"}
		}
		if relative, inside := relativePathWithin(taskRoot, source); inside {
			materialized = appendUniqueWebConversationFile(materialized, seen, relative)
			continue
		}

		relative, err := copyWebConversationInputFile(source, inputRoot)
		if err != nil {
			return nil, invalidWebConversationInputFile()
		}
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
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(relative), true
}

func copyWebConversationInputFile(source, inputRoot string) (string, error) {
	name := filepath.Base(source)
	if name == "." || name == string(filepath.Separator) || strings.TrimSpace(name) == "" {
		return "", errors.New("invalid input filename")
	}
	sourceFile, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer sourceFile.Close()

	temporary, err := os.CreateTemp(inputRoot, ".incoming-*")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(temporary, hash), sourceFile); err != nil {
		return "", err
	}
	if err := temporary.Sync(); err != nil {
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(temporaryPath, 0o600); err != nil {
		return "", err
	}
	sum := hex.EncodeToString(hash.Sum(nil))
	destination := filepath.Join(inputRoot, name)
	if existing, err := fileSHA256(destination); err == nil {
		if existing == sum {
			return filepath.ToSlash(filepath.Join("inputs", name)), nil
		}
		extension := filepath.Ext(name)
		stem := strings.TrimSuffix(name, extension)
		destination = filepath.Join(inputRoot, fmt.Sprintf("%s-%s%s", stem, sum[:12], extension))
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return "", err
	}
	committed = true
	return filepath.ToSlash(filepath.Join("inputs", filepath.Base(destination))), nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
