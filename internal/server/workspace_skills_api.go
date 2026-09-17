package server

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"synon-go/internal/skills"
)

const (
	maxSkillArchiveFiles        = 512
	maxSkillArchiveBytes        = 10 << 20
	runtimeSkillPreferenceOwner = "_runtime"
)

type workspaceSkillCatalogEntry struct {
	Name            string            `json:"name"`
	Description     string            `json:"description"`
	DescriptionI18n map[string]string `json:"descriptionI18n,omitempty"`
	Category        string            `json:"category,omitempty"`
	Tags            []string          `json:"tags"`
	Keywords        []string          `json:"keywords"`
	Tools           []string          `json:"tools"`
	Arguments       []string          `json:"arguments"`
	References      []string          `json:"references"`
	RequiredSkills  []string          `json:"requiredSkills,omitempty"`
	BodyHash        string            `json:"bodyHash"`
	Enabled         bool              `json:"enabled"`
}

type workspaceSkillFile struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	absolute string
}

func (s *Server) handleWorkspaceSkills(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return
	}
	if s.skillCatalog == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "skill catalog is not configured"})
		return
	}
	preferences, err := store.ListSkillPreferences(runtimeSkillPreferenceOwner)
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	catalog := make([]workspaceSkillCatalogEntry, 0, len(s.skillCatalog.Skills()))
	for _, skill := range s.skillCatalog.Skills() {
		enabled, configured := preferences[skill.Name]
		if !configured {
			enabled = true
		}
		catalog = append(catalog, projectWorkspaceSkill(skill, enabled))
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"ok": true, "skills": catalog, "loadErrors": s.skillErrors,
	})
}

func (s *Server) handleWorkspaceSkill(w http.ResponseWriter, r *http.Request) {
	if s.skillCatalog == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "skill catalog is not configured"})
		return
	}
	segments := workspacePathSegments(strings.TrimPrefix(r.URL.Path, "/api/go/skills/"))
	if len(segments) == 0 || len(segments) > 2 {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "workspace endpoint not found"})
		return
	}
	name, err := url.PathUnescape(segments[0])
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid skill name"})
		return
	}
	skill, found := findCatalogSkill(s.skillCatalog, name)
	if !found {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "skill not found"})
		return
	}
	if len(segments) == 1 {
		if r.Method != http.MethodGet {
			writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{
			"ok": true,
			"skill": map[string]any{
				"name": skill.Name, "description": skill.Description, "tags": skill.Tags,
				"descriptionI18n": skill.DescriptionI18n, "category": skill.Category,
				"keywords": skill.Keywords, "tools": skill.Tools, "arguments": skill.Arguments,
				"references": skill.References, "body": skill.Body, "bodyHash": skill.BodyHash,
			},
		})
		return
	}
	switch segments[1] {
	case "enabled":
		s.handleWorkspaceSkillEnabled(w, r, skill)
	case "files":
		s.handleWorkspaceSkillFiles(w, r, skill)
	case "download":
		s.handleWorkspaceSkillDownload(w, r, skill)
	default:
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "workspace endpoint not found"})
	}
}

func (s *Server) handleWorkspaceSkillEnabled(w http.ResponseWriter, r *http.Request, skill skills.Skill) {
	if r.Method != http.MethodPut {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return
	}
	var input struct {
		UserID  string `json:"userId"`
		Enabled bool   `json:"enabled"`
	}
	if err := decodeWorkspaceJSON(r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if err := store.SetSkillEnabled(runtimeSkillPreferenceOwner, skill.Name, input.Enabled); err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "name": skill.Name, "enabled": input.Enabled})
}

func (s *Server) handleWorkspaceSkillFiles(w http.ResponseWriter, r *http.Request, skill skills.Skill) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	files, err := workspaceSkillFiles(skill)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "name": skill.Name, "files": files})
}

func (s *Server) handleWorkspaceSkillDownload(w http.ResponseWriter, r *http.Request, skill skills.Skill) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	raw, err := workspaceSkillArchive(skill)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", skill.Name+".zip"))
	w.Header().Set("Content-Length", fmt.Sprint(len(raw)))
	_, _ = w.Write(raw)
}

func workspaceSkillArchive(skill skills.Skill) ([]byte, error) {
	files, err := workspaceSkillFiles(skill)
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	archive := zip.NewWriter(&output)
	for _, file := range files {
		source, err := openGrantedRegularFile(file.absolute, []hostGrant{{
			Path: filepath.Dir(skill.Path), Mode: "read",
		}})
		if err != nil {
			_ = archive.Close()
			return nil, err
		}
		info, err := source.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() != file.Size {
			_ = source.Close()
			_ = archive.Close()
			return nil, errors.New("skill file changed while creating archive")
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			_ = source.Close()
			_ = archive.Close()
			return nil, err
		}
		header.Name = filepath.ToSlash(filepath.Join(skill.Name, file.Path))
		header.Method = zip.Deflate
		target, err := archive.CreateHeader(header)
		if err == nil {
			_, err = io.CopyN(target, source, file.Size)
		}
		_ = source.Close()
		if err != nil {
			_ = archive.Close()
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func (s *Server) runtimeSkillEnabled(name string) bool {
	if s == nil || s.workspaceStore == nil {
		return true
	}
	preferences, err := s.workspaceStore.ListSkillPreferences(runtimeSkillPreferenceOwner)
	if err != nil {
		return false
	}
	enabled, configured := preferences[name]
	return !configured || enabled
}

func (s *Server) filterRuntimeEnabledSkills(values []skills.Skill, limit int) []skills.Skill {
	preferences := map[string]bool{}
	if s != nil && s.workspaceStore != nil {
		var err error
		preferences, err = s.workspaceStore.ListSkillPreferences(runtimeSkillPreferenceOwner)
		if err != nil {
			return nil
		}
	}
	filtered := make([]skills.Skill, 0, len(values))
	for _, skill := range values {
		enabled, configured := preferences[skill.Name]
		if configured && !enabled {
			continue
		}
		filtered = append(filtered, skill)
		if limit > 0 && len(filtered) >= limit {
			break
		}
	}
	return filtered
}

func projectWorkspaceSkill(skill skills.Skill, enabled bool) workspaceSkillCatalogEntry {
	return workspaceSkillCatalogEntry{
		Name: skill.Name, Description: skill.Description, DescriptionI18n: skill.DescriptionI18n, Category: skill.Category,
		Tags: skill.Tags, Keywords: skill.Keywords,
		Tools: skill.Tools, Arguments: skill.Arguments, References: skill.References,
		RequiredSkills: skill.RequiredSkills,
		BodyHash:       skill.BodyHash, Enabled: enabled,
	}
}

func workspaceSkillFiles(skill skills.Skill) ([]workspaceSkillFile, error) {
	if strings.HasPrefix(skill.Path, "builtin:") {
		return nil, fmt.Errorf("built-in skill %q has no on-disk file bundle", skill.Name)
	}
	root := filepath.Dir(skill.Path)
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve skill root: %w", err)
	}
	files := make([]workspaceSkillFile, 0)
	var total int64
	err = filepath.WalkDir(rootAbs, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(rootAbs, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("skill file escaped its root")
		}
		total += info.Size()
		if len(files) >= maxSkillArchiveFiles || total > maxSkillArchiveBytes {
			return fmt.Errorf("skill bundle exceeds %d files or %d bytes", maxSkillArchiveFiles, maxSkillArchiveBytes)
		}
		files = append(files, workspaceSkillFile{Path: filepath.ToSlash(relative), Size: info.Size(), absolute: path})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk skill files: %w", err)
	}
	return files, nil
}
