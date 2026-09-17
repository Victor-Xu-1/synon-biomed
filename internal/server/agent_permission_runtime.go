package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"synon-go/internal/agentruntime"
)

const (
	listHostGrantsToolName       = "list_host_grants"
	requestHostAccessToolName    = "request_host_access"
	requestNetworkAccessToolName = "request_network_access"
	deleteHostFilesToolName      = "delete_host_files"
)

func agentPermissionToolSchemas() []agentruntime.ToolSchema {
	return []agentruntime.ToolSchema{
		{
			Name:        listHostGrantsToolName,
			Description: "List host directories the user has granted to this runtime and their current read or read-write modes.",
			Parameters:  map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{}},
		},
		{
			Name:        requestHostAccessToolName,
			Description: "Request access to one host directory. Use ~/ rather than guessing the user's home-directory name. The user remains the authority for the final mode.",
			Parameters: map[string]any{"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"host_path":         map[string]any{"type": "string", "minLength": 1, "maxLength": 4096},
					"mode":              map[string]any{"type": "string", "enum": []string{"ro", "rw"}},
					"human_description": map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
				},
				"required": []string{"host_path", "human_description"},
			},
		},
		{
			Name:        requestNetworkAccessToolName,
			Description: "Request access to one exact network hostname for a stated task purpose. Schemes, paths, ports, IP literals, and wildcards are rejected.",
			Parameters: map[string]any{"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"domain":            map[string]any{"type": "string", "minLength": 1, "maxLength": 253},
					"reason":            map[string]any{"type": "string", "minLength": 1, "maxLength": 512},
					"human_description": map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
				},
				"required": []string{"domain", "reason", "human_description"},
			},
		},
		{
			Name:        deleteHostFilesToolName,
			Description: "Move explicitly named files or directories under a granted host folder to the system Trash after approval. This never performs a permanent delete.",
			Parameters: map[string]any{"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"paths":             map[string]any{"type": "array", "minItems": 1, "maxItems": 100, "uniqueItems": true, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 4096}},
					"reason":            map[string]any{"type": "string", "minLength": 1, "maxLength": 512},
					"human_description": map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
				},
				"required": []string{"paths", "reason", "human_description"},
			},
		},
	}
}

func isAgentPermissionTool(name string) bool {
	switch strings.TrimSpace(name) {
	case listHostGrantsToolName, requestHostAccessToolName, requestNetworkAccessToolName, deleteHostFilesToolName:
		return true
	default:
		return false
	}
}

func (s *Server) executeAgentPermissionTool(ctx context.Context, identity *agentKernelContext, call agentruntime.ToolCall, name string, input map[string]any) (any, error) {
	_ = call
	if s == nil || identity == nil {
		return nil, errors.New("permission runtime is unavailable")
	}
	access, err := s.validateKernelHostIdentity(ctx, identity.access)
	if err != nil {
		return nil, err
	}
	switch name {
	case listHostGrantsToolName:
		grants, err := s.loadHostGrants(access.UserID)
		if err != nil {
			return nil, err
		}
		items := make([]any, 0, len(grants))
		for _, grant := range grants {
			mode := "ro"
			if grant.Mode == "read_write" {
				mode = "rw"
			}
			items = append(items, map[string]any{"path": grant.Path, "mode": mode})
		}
		return map[string]any{"grants": items}, nil
	case requestHostAccessToolName:
		path, err := expandAgentHostPath(stringValue(input["host_path"]))
		if err != nil {
			return nil, err
		}
		mode := strings.TrimSpace(stringValue(input["mode"]))
		internalMode := "read"
		if mode == "rw" {
			internalMode = "read_write"
		} else if mode != "" && mode != "ro" {
			return nil, errors.New("request_host_access mode must be ro or rw")
		}
		grant, err := s.upsertHostGrant(access.UserID, path, internalMode)
		if err != nil {
			return nil, err
		}
		return map[string]any{"granted": true, "path": grant.Path, "mode": mode}, nil
	case requestNetworkAccessToolName:
		domain, err := normalizeAllowedDomain(stringValue(input["domain"]))
		if err != nil {
			return nil, err
		}
		if s.settingsStore == nil {
			return nil, errors.New("network grant store is unavailable")
		}
		err = s.commitKernelConfinementMutation(access.UserID, func() (bool, error) {
			changed := false
			_, updateErr := s.settingsStore.Update(allowedDomainsSettingKey, func(current any, found bool) (any, error) {
				previous, normalizeErr := normalizeAllowedDomains(settingStringSlice(current))
				if normalizeErr != nil {
					return nil, normalizeErr
				}
				next, normalizeErr := normalizeAllowedDomains(append(previous, domain))
				if normalizeErr != nil {
					return nil, normalizeErr
				}
				changed = !slices.Equal(previous, next)
				return next, nil
			})
			return changed, updateErr
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"granted": true, "domain": domain}, nil
	case deleteHostFilesToolName:
		return s.trashGrantedHostPaths(ctx, access.UserID, stringArrayValue(input["paths"]))
	default:
		return nil, errors.New("unsupported permission tool")
	}
}

func expandAgentHostPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "~" || strings.HasPrefix(value, "~/") || strings.HasPrefix(value, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", errors.New("host home directory is unavailable")
		}
		value = filepath.Join(home, strings.TrimLeft(strings.TrimPrefix(value, "~"), `/\`))
	}
	path, err := canonicalHostGrantReference(value)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return "", errors.New("request_host_access host_path must be an existing directory")
	}
	return path, nil
}

func (s *Server) trashGrantedHostPaths(ctx context.Context, userID string, values []string) (map[string]any, error) {
	if len(values) == 0 {
		return nil, errors.New("delete_host_files paths must not be empty")
	}
	grants, err := s.loadHostGrants(userID)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		path, err := expandAgentTrashPath(value)
		if err != nil {
			return nil, err
		}
		allowed := false
		for _, grant := range grants {
			root, rootErr := canonicalHostDirectory(grant.Path)
			if rootErr == nil && hostPathWithin(root, path) && path != root {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, fmt.Errorf("delete_host_files path is outside granted folders: %s", value)
		}
		if !seen[path] {
			paths = append(paths, path)
			seen[path] = true
		}
	}
	gio, err := exec.LookPath("gio")
	if err != nil {
		return nil, errors.New("system Trash integration is unavailable; no files were removed")
	}
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	arguments := append([]string{"trash", "--"}, paths...)
	command := exec.CommandContext(runCtx, gio, arguments...)
	if output, err := command.CombinedOutput(); err != nil {
		message := strings.TrimSpace(string(output))
		if len(message) > 512 {
			message = message[:512]
		}
		return nil, fmt.Errorf("system Trash operation failed: %s", message)
	}
	return map[string]any{"trashed": paths, "recoverable": true}, nil
}

func expandAgentTrashPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "~" || strings.HasPrefix(value, "~/") || strings.HasPrefix(value, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", errors.New("host home directory is unavailable")
		}
		value = filepath.Join(home, strings.TrimLeft(strings.TrimPrefix(value, "~"), `/\`))
	}
	if !filepath.IsAbs(value) || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("delete_host_files paths must be absolute or home-relative")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(value))
	if err != nil {
		return "", errors.New("delete_host_files path does not exist")
	}
	if _, err := os.Lstat(resolved); err != nil {
		return "", errors.New("delete_host_files path could not be inspected")
	}
	return filepath.Clean(resolved), nil
}
