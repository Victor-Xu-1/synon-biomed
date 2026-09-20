package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"unicode"

	"synon-go/internal/runtimecontrol"
)

const storageRulesSettingKey = "storage.rules"

// storageRules describes the directories below the configured data root that
// can be changed without moving the root itself. TaskRun records and the
// workspace database keep their established locations because changing either
// one requires a coordinated data migration, not a simple preference update.
type storageRules struct {
	TaskArtifacts string `json:"taskArtifacts"`
	Logs          string `json:"logs"`
	ToolResults   string `json:"toolResults"`
	Temp          string `json:"temp"`
}

var defaultStorageRules = storageRules{
	TaskArtifacts: filepath.ToSlash(filepath.Join("task_runs", "artifacts")),
	Logs:          "shell_tasks",
	ToolResults:   "tool-results",
	Temp:          "tmp",
}

type storageRulesInput struct {
	Rules storageRules `json:"rules"`
}

func (s *Server) handleStorageRules(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.settingsStore == nil || strings.TrimSpace(s.fileRoot) == "" {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok": false, "error": "storage rules are not configured",
		})
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.writeStorageRules(w)
	case http.MethodPut:
		s.handleSetStorageRules(w, r)
	default:
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
	}
}

func (s *Server) handleSetStorageRules(w http.ResponseWriter, r *http.Request) {
	activeFrames, err := s.activeFrameCount()
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if activeFrames > 0 {
		writeWorkspaceJSON(w, http.StatusConflict, map[string]any{
			"ok": false, "error": "running tasks must finish or stop before changing storage rules",
			"activeFrames": activeFrames,
		})
		return
	}
	var input storageRulesInput
	if err := decodeWorkspaceJSON(r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	rules, err := normalizeStorageRules(input.Rules)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if _, err := s.settingsStore.Set(storageRulesSettingKey, rules); err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.configureStorageScanner(rules)
	s.writeStorageRulesWithValue(w, rules)
}

func (s *Server) writeStorageRules(w http.ResponseWriter) {
	rules, err := s.loadStorageRules()
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.writeStorageRulesWithValue(w, rules)
}

func (s *Server) writeStorageRulesWithValue(w http.ResponseWriter, rules storageRules) {
	paths := map[string]string{}
	for key, relative := range map[string]string{
		"taskArtifacts": rules.TaskArtifacts,
		"logs":          rules.Logs,
		"toolResults":   rules.ToolResults,
		"temp":          rules.Temp,
	} {
		paths[key] = filepath.Join(s.fileRoot, filepath.FromSlash(relative))
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"root":  s.fileRoot,
		"rules": rules,
		"paths": paths,
		"systemPaths": map[string]string{
			"taskRuns":  filepath.Join(s.fileRoot, "task_runs"),
			"workspace": filepath.Join(s.fileRoot, "workspace"),
		},
	})
}

func (s *Server) configureStorageScanner(rules storageRules) {
	if s == nil || s.usageScanner == nil || strings.TrimSpace(s.fileRoot) == "" {
		return
	}
	s.usageScanner.SetStorageRoots(runtimecontrol.StorageRoots{
		Artifacts:   filepath.Join(s.fileRoot, filepath.FromSlash(rules.TaskArtifacts)),
		ToolResults: filepath.Join(s.fileRoot, filepath.FromSlash(rules.ToolResults)),
		Logs:        filepath.Join(s.fileRoot, filepath.FromSlash(rules.Logs)),
		Temp:        filepath.Join(s.fileRoot, filepath.FromSlash(rules.Temp)),
	})
}

func (s *Server) loadStorageRules() (storageRules, error) {
	if s == nil || s.settingsStore == nil {
		return storageRules{}, errors.New("storage rules store is not configured")
	}
	setting, found, err := s.settingsStore.Get(storageRulesSettingKey)
	if err != nil {
		return storageRules{}, err
	}
	if !found {
		return defaultStorageRules, nil
	}
	encoded, err := json.Marshal(setting.Value)
	if err != nil {
		return storageRules{}, fmt.Errorf("encode storage rules: %w", err)
	}
	var rules storageRules
	if err := json.Unmarshal(encoded, &rules); err != nil {
		return storageRules{}, fmt.Errorf("decode storage rules: %w", err)
	}
	return normalizeStorageRules(rules)
}

func normalizeStorageRules(rules storageRules) (storageRules, error) {
	if rules.TaskArtifacts == "" {
		rules.TaskArtifacts = defaultStorageRules.TaskArtifacts
	}
	if rules.Logs == "" {
		rules.Logs = defaultStorageRules.Logs
	}
	if rules.ToolResults == "" {
		rules.ToolResults = defaultStorageRules.ToolResults
	}
	if rules.Temp == "" {
		rules.Temp = defaultStorageRules.Temp
	}
	var err error
	if rules.TaskArtifacts, err = normalizeStorageRulePath("taskArtifacts", rules.TaskArtifacts); err != nil {
		return storageRules{}, err
	}
	if rules.Logs, err = normalizeStorageRulePath("logs", rules.Logs); err != nil {
		return storageRules{}, err
	}
	if rules.ToolResults, err = normalizeStorageRulePath("toolResults", rules.ToolResults); err != nil {
		return storageRules{}, err
	}
	if rules.Temp, err = normalizeStorageRulePath("temp", rules.Temp); err != nil {
		return storageRules{}, err
	}
	return rules, nil
}

func normalizeStorageRulePath(name string, value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" || value == "." || strings.HasPrefix(value, "/") || filepath.VolumeName(value) != "" || hasWindowsVolumePrefix(value) {
		return "", fmt.Errorf("storage rule %s must be a non-empty relative path", name)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("storage rule %s contains a control character", name)
		}
	}
	parts := strings.Split(value, "/")
	for _, part := range parts {
		if part == ".." {
			return "", fmt.Errorf("storage rule %s may not leave the data root", name)
		}
	}
	normalized := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	if normalized == "." || strings.HasPrefix(normalized, "../") || normalized == ".." {
		return "", fmt.Errorf("storage rule %s may not leave the data root", name)
	}
	return normalized, nil
}

func hasWindowsVolumePrefix(value string) bool {
	return len(value) >= 2 && value[1] == ':'
}

func (s *Server) storageDirectory(rule string, children ...string) (string, error) {
	rules, err := s.loadStorageRules()
	if err != nil {
		return "", err
	}
	relative := map[string]string{
		"taskArtifacts": rules.TaskArtifacts,
		"logs":          rules.Logs,
		"toolResults":   rules.ToolResults,
		"temp":          rules.Temp,
	}[rule]
	if relative == "" {
		return "", fmt.Errorf("unknown storage rule %q", rule)
	}
	parts := append([]string{s.fileRoot, filepath.FromSlash(relative)}, children...)
	path := filepath.Join(parts...)
	if err := ensurePathWithinRoot(s.fileRoot, path, "storage rule output"); err != nil {
		return "", err
	}
	return path, nil
}
