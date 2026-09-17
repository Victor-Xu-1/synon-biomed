package server

import (
	"path/filepath"
	"strings"
)

// normalizeKernelMCPWorkspaceOutputPaths anchors connector-requested local
// outputs to the current task workspace. External MCP helpers may execute with
// their package directory as cwd; allowing a relative *_output_path to pass
// through would mutate the installed capability and leave the kernel unable to
// read the file it requested. Unknown fields and non-output paths are unchanged.
func normalizeKernelMCPWorkspaceOutputPaths(input map[string]any, workspaceDir string) map[string]any {
	workspaceDir = strings.TrimSpace(workspaceDir)
	if len(input) == 0 || workspaceDir == "" {
		return input
	}
	workspace, err := filepath.Abs(filepath.Clean(workspaceDir))
	if err != nil {
		return input
	}
	return normalizeKernelMCPWorkspaceOutputValue(input, workspace).(map[string]any)
}

func normalizeKernelMCPWorkspaceOutputValue(value any, workspace string) any {
	switch typed := value.(type) {
	case map[string]any:
		normalized := make(map[string]any, len(typed))
		for key, item := range typed {
			if requested, ok := item.(string); ok && kernelMCPOutputPathField(key) {
				normalized[key] = kernelMCPWorkspaceOutputPath(workspace, requested)
				continue
			}
			normalized[key] = normalizeKernelMCPWorkspaceOutputValue(item, workspace)
		}
		return normalized
	case []any:
		normalized := make([]any, len(typed))
		for index, item := range typed {
			normalized[index] = normalizeKernelMCPWorkspaceOutputValue(item, workspace)
		}
		return normalized
	default:
		return value
	}
}

func kernelMCPOutputPathField(name string) bool {
	name = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(name), "-", "_"))
	return name == "output_path" || strings.HasSuffix(name, "_output_path")
}

func kernelMCPWorkspaceOutputPath(workspace, requested string) string {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return requested
	}
	candidate := filepath.Clean(filepath.FromSlash(requested))
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(workspace, candidate)
	}
	if hostPathWithin(workspace, candidate) && filepath.Clean(candidate) != filepath.Clean(workspace) {
		return candidate
	}
	name := filepath.Base(candidate)
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = "mcp-output"
	}
	return filepath.Join(workspace, name)
}
