package server

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var standaloneScriptCommandPattern = regexp.MustCompile(`(?:^|&&|[;\n])\s*(?:python(?:3(?:\.\d+)?)?|Rscript)\s+([^\s;&|]+)`)

func agentRuntimeWorkspaceExistencePreflight(
	publicName string,
	input map[string]any,
	identity *agentKernelContext,
) map[string]any {
	if identity == nil || strings.TrimSpace(identity.workspaceDir) == "" {
		return nil
	}
	name := strings.ToLower(strings.TrimSpace(publicName))
	var requested string
	switch name {
	case "read_file":
		if strings.TrimSpace(stringValue(input["version_id"])) != "" {
			return nil
		}
		requested = strings.TrimSpace(stringValue(input["file_path"]))
	case "bash":
		match := standaloneScriptCommandPattern.FindStringSubmatch(stringValue(input["command"]))
		if len(match) != 2 {
			return nil
		}
		requested = strings.Trim(strings.TrimSpace(match[1]), `"'`)
		if strings.HasPrefix(requested, "-") {
			return nil
		}
	default:
		return nil
	}
	if requested == "" || filepath.IsAbs(requested) || strings.Contains(requested, "{{artifact:") ||
		strings.HasPrefix(requested, "ltr-") {
		return nil
	}
	base := filepath.Clean(identity.workspaceDir)
	if workingDir := strings.TrimSpace(stringValue(input["working_dir"])); workingDir != "" && !filepath.IsAbs(workingDir) {
		base = filepath.Join(base, workingDir)
	}
	target := filepath.Clean(filepath.Join(base, filepath.FromSlash(requested)))
	if !hostPathWithin(filepath.Clean(identity.workspaceDir), target) {
		return nil
	}
	if info, err := os.Stat(target); err == nil && !info.IsDir() {
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return nil
	}
	result := map[string]any{
		"ok": false, "status": "workspace_file_preflight_required", "executed": false,
		"message": "The referenced task-workspace file does not exist, so the call was rejected before execution.",
	}
	if name == "read_file" {
		result["requested_file"] = filepath.ToSlash(requested)
		result["available_files"] = agentRuntimeWorkspaceFileInventory(identity.workspaceDir, 64)
		result["recovery"] = "Select the exact task-relative path from available_files, or use an existing successful artifact version_id. The absence is already confirmed: do not guess another filename or call read_file for the same missing path. Create a new file only when the task actually requires a new output."
		return result
	}
	result["recovery"] = "Create the missing script with edit_file using empty old_string, or execute the exact existing task-relative script path shown by the workspace receipts. Do not retry the execution until a successful write receipt exists."
	return result
}

func agentRuntimeWorkspaceFileInventory(root string, limit int) []string {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "." || root == "" || limit <= 0 {
		return nil
	}
	files := make([]string, 0, min(limit, 32))
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry == nil {
			return nil
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
			return nil
		}
		if entry.IsDir() {
			if len(strings.Split(filepath.ToSlash(relative), "/")) > 5 {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return nil
		}
		files = append(files, filepath.ToSlash(relative))
		if len(files) >= limit {
			return filepath.SkipAll
		}
		return nil
	})
	sort.Strings(files)
	return files
}
