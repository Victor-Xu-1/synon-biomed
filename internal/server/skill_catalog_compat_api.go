package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"synon-go/internal/skills"
)

type compatibilitySkillCatalogEntry struct {
	Name            string            `json:"name"`
	DisplayName     string            `json:"displayName"`
	Description     string            `json:"description"`
	DescriptionI18n map[string]string `json:"description_i18n,omitempty"`
	Category        string            `json:"category,omitempty"`
	Source          string            `json:"source"`
	SkillID         string            `json:"skillId,omitempty"`
	Enabled         *bool             `json:"enabled,omitempty"`
	UserHidden      bool              `json:"userHidden,omitempty"`
	AttachedAgents  []string          `json:"attachedAgents"`
}

func (s *Server) handleCompatibilitySkillCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	if s.skillCatalog == nil || s.workspaceStore == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "Skill catalog is not configured")
		return
	}
	userID := compatAgentUserID(r)
	preferences, err := s.workspaceStore.ListSkillPreferences(userID)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	agents, err := s.workspaceStore.ListAgents(userID)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	attached := make(map[string][]string)
	for _, agent := range agents {
		for _, name := range agent.SkillNames {
			attached[name] = append(attached[name], agent.Name)
		}
	}
	entries := make([]compatibilitySkillCatalogEntry, 0, len(s.skillCatalog.Skills()))
	sourceCounts := map[string]int{}
	packagedDirectorySkills := 0
	builtinPlatformSkills := 0
	for _, skill := range s.skillCatalog.Skills() {
		source := compatibilitySkillSource(skill)
		sourceCounts[source]++
		if compatibilityRequiredPlatformSkill(skill) {
			builtinPlatformSkills++
		} else if source == "synon_llm" {
			packagedDirectorySkills++
		}
		entry := compatibilitySkillCatalogEntry{
			Name: skill.Name, DisplayName: skill.Name, Description: skill.Description,
			DescriptionI18n: skill.DescriptionI18n, Category: skill.Category,
			Source: source, AttachedAgents: uniqueSortedStrings(attached[skill.Name]),
		}
		if source == "synon_llm" {
			entry.SkillID = "bundled:" + skill.Name
		} else {
			entry.SkillID = "local:" + skill.Name
		}
		if enabled, configured := preferences[skill.Name]; configured && !enabled {
			entry.Enabled = boolPointer(false)
		}
		if compatibilityRequiredPlatformSkill(skill) {
			entry.UserHidden = true
		}
		entries = append(entries, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"skills": entries, "degraded": len(s.skillErrors) > 0,
		"catalog_counts": map[string]any{
			"total": len(entries), "by_source": sourceCounts,
			"packaged_directory_skills": packagedDirectorySkills,
			"builtin_platform_skills":   builtinPlatformSkills,
		},
		"identity_contract": "directory name, manifest name and SKILL.md frontmatter name must match exactly",
	})
}

func (s *Server) handleCompatibilityCatalogSkill(w http.ResponseWriter, r *http.Request) {
	if s.skillCatalog == nil || s.workspaceStore == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "Skill catalog is not configured")
		return
	}
	segments := workspacePathSegments(strings.TrimPrefix(r.URL.Path, "/api/skills/catalog/"))
	if len(segments) != 2 {
		writeV11Detail(w, http.StatusNotFound, "Skill endpoint not found")
		return
	}
	name, err := url.PathUnescape(segments[0])
	if err != nil {
		writeV11Detail(w, http.StatusBadRequest, "Invalid skill name")
		return
	}
	skill, found := findCatalogSkill(s.skillCatalog, name)
	if !found {
		writeV11Detail(w, http.StatusNotFound, fmt.Sprintf("skill '%s'", name))
		return
	}
	switch segments[1] {
	case "enabled":
		s.handleCompatibilityCatalogSkillEnabled(w, r, skill)
	case "content":
		s.handleCompatibilityCatalogSkillContent(w, r, skill)
	case "files":
		s.handleCompatibilityCatalogSkillFiles(w, r, skill)
	case "download":
		s.handleWorkspaceSkillDownload(w, r, skill)
	default:
		writeV11Detail(w, http.StatusNotFound, "Skill endpoint not found")
	}
}

func (s *Server) handleCompatibilityCatalogSkillFiles(w http.ResponseWriter, r *http.Request, skill skills.Skill) {
	if r.Method != http.MethodGet {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	files, err := workspaceSkillFiles(skill)
	if err != nil {
		writeV11Detail(w, http.StatusBadRequest, err.Error())
		return
	}
	paths := make([]string, len(files))
	for index := range files {
		paths[index] = files[index].Path
	}
	sort.SliceStable(paths, func(i, j int) bool {
		if paths[i] == "SKILL.md" {
			return true
		}
		if paths[j] == "SKILL.md" {
			return false
		}
		return paths[i] < paths[j]
	})
	writeJSON(w, http.StatusOK, map[string]any{"files": paths})
}

func (s *Server) handleCompatibilityCatalogSkillEnabled(w http.ResponseWriter, r *http.Request, skill skills.Skill) {
	if r.Method != http.MethodPut {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeAgentCompatJSON(r, &input); err != nil || input.Enabled == nil {
		writeV11Detail(w, http.StatusBadRequest, "enabled: boolean required")
		return
	}
	if !*input.Enabled && compatibilityRequiredPlatformSkill(skill) {
		writeV11Detail(w, http.StatusForbidden, fmt.Sprintf("'%s' is a required platform skill and cannot be disabled", skill.Name))
		return
	}
	if err := s.workspaceStore.SetSkillEnabled(compatAgentUserID(r), skill.Name, *input.Enabled); err != nil {
		writeV11StoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": skill.Name, "enabled": *input.Enabled})
}

func (s *Server) handleCompatibilityCatalogSkillContent(w http.ResponseWriter, r *http.Request, skill skills.Skill) {
	if r.Method != http.MethodGet {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	content, err := compatibilityCatalogSkillContent(skill, r.URL.Query().Get("path"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeV11Detail(w, http.StatusNotFound, err.Error())
		} else {
			writeV11Detail(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"content": content})
}

func compatibilityCatalogSkillContent(skill skills.Skill, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		requested = "SKILL.md"
	}
	requested = filepath.ToSlash(requested)
	if requested == "." || strings.HasPrefix(requested, "/") ||
		requested == ".." || strings.HasPrefix(requested, "../") || strings.Contains(requested, "/../") {
		return "", errors.New("invalid skill content path")
	}
	if strings.HasPrefix(skill.Path, "builtin:") {
		if requested != "SKILL.md" {
			return "", fmt.Errorf("%w: skill file %q", os.ErrNotExist, requested)
		}
		return skill.Body, nil
	}
	files, err := workspaceSkillFiles(skill)
	if err != nil {
		return "", err
	}
	for _, file := range files {
		if file.Path != requested {
			continue
		}
		source, err := openGrantedRegularFile(file.absolute, []hostGrant{{
			Path: filepath.Dir(skill.Path), Mode: "read",
		}})
		if err != nil {
			return "", err
		}
		defer source.Close()
		info, err := source.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() != file.Size {
			return "", errors.New("skill file changed while being read")
		}
		raw, err := io.ReadAll(io.LimitReader(source, file.Size+1))
		if err != nil {
			return "", err
		}
		if int64(len(raw)) != file.Size {
			return "", errors.New("skill file changed while being read")
		}
		return string(raw), nil
	}
	return "", fmt.Errorf("%w: skill file %q", os.ErrNotExist, requested)
}

func compatibilitySkillSource(skill skills.Skill) string {
	normalizedPath := filepath.ToSlash(skill.Path)
	if strings.HasPrefix(skill.Path, "builtin:") ||
		strings.Contains(normalizedPath, "/runtime/assets/skills/") ||
		strings.Contains(normalizedPath, "/skills/synonbiomed/") {
		return "synon_llm"
	}
	return "personal"
}

func compatibilityRequiredPlatformSkill(skill skills.Skill) bool {
	return strings.HasPrefix(skill.Path, "builtin:")
}

func uniqueSortedStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
