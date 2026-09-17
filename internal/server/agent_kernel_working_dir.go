package server

import (
	"errors"
	"path/filepath"
	"strings"
)

// normalizeAgentKernelWorkingDirInput gives model-authored relative paths one
// stable meaning: they are anchored at the task workspace, never at the
// service process or a persistent kernel's previous cwd. Absolute paths retain
// the existing host-grant authorization boundary.
func (s *Server) normalizeAgentKernelWorkingDirInput(
	userID, workspaceDir string,
	input map[string]any,
) (map[string]any, string, error) {
	raw, found := input["working_dir"]
	if !found {
		return input, "", nil
	}
	requested, ok := raw.(string)
	if !ok {
		return nil, "", errors.New("working_dir must be a string path")
	}
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return input, "", nil
	}
	if !filepath.IsAbs(requested) {
		cleaned := filepath.Clean(requested)
		if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
			return nil, "", errors.New("working_dir is outside the authorized workspace")
		}
		requested = filepath.Join(workspaceDir, cleaned)
	}
	authorized, err := s.authorizeAgentKernelWorkingDir(userID, workspaceDir, requested)
	if err != nil {
		return nil, "", err
	}
	normalized := copyMapAny(input)
	normalized["working_dir"] = authorized
	return normalized, authorized, nil
}
