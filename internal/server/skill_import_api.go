package server

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"synon-go/internal/skills"
)

const maxSkillImportRequestBytes = maxSkillArchiveBytes + (1 << 20)

var importedSkillNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type skillImportResult struct {
	Name          string
	InstalledPath string
	Files         []string
	Description   *string
}

func (s *Server) handleSkillBundleImport(w http.ResponseWriter, r *http.Request) {
	compatibility := r.URL.Path == "/api/skills/import"
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	if s == nil || strings.TrimSpace(s.fileRoot) == "" || s.skillCatalog == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "writable skill catalog is not configured"})
		return
	}
	contentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	if compatibility && strings.HasPrefix(contentType, "application/json") {
		s.handleWebSkillPathImport(w, r)
		return
	}
	if compatibility && !strings.HasPrefix(contentType, "multipart/form-data") {
		writeWorkspaceJSON(w, http.StatusNotAcceptable, map[string]any{
			"detail": "the request is not multipart", "error": "multipart/form-data required",
		})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSkillImportRequestBytes)
	if err := r.ParseMultipartForm(maxSkillImportRequestBytes); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid skill bundle upload: " + err.Error()})
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		if compatibility {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"error": "missing file"})
		} else {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "skill bundle file is required"})
		}
		return
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxSkillArchiveBytes+1))
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "read skill bundle: " + err.Error()})
		return
	}
	if len(raw) == 0 || len(raw) > maxSkillArchiveBytes {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": fmt.Sprintf("skill bundle must be between 1 and %d bytes", maxSkillArchiveBytes)})
		return
	}
	result, err := s.importSkillBundle(header.Filename, strings.TrimSpace(r.FormValue("name")), parseImportReplace(r.FormValue("replace")), raw)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, os.ErrExist) {
			status = http.StatusConflict
		}
		writeWorkspaceJSON(w, status, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if compatibility {
		operationID := uuid.NewString()
		entry := newWebSkillImportHistory(operationID, "File upload", "", header.Filename, result.Name, "imported", "")
		entry.ActualBytes = int64(len(raw))
		if err := s.appendWebSkillImportHistory(compatAgentUserID(r), entry); err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{
				"ok": false, "error": "skill imported but import history could not be persisted",
			})
			return
		}
	}
	status := http.StatusOK
	if compatibility {
		status = http.StatusCreated
	}
	writeWorkspaceJSON(w, status, map[string]any{
		"ok": true, "imported": true, "name": result.Name,
		"skill_name": result.Name, "skill_names": []string{result.Name},
		"installed_to": filepath.ToSlash(result.InstalledPath), "files": result.Files, "description": result.Description,
	})
}

func (s *Server) handleWebSkillPathImport(w http.ResponseWriter, r *http.Request) {
	var input struct {
		SkillPath  string `json:"skill_path"`
		LegacyPath string `json:"skillPath"`
		Replace    bool   `json:"replace"`
	}
	if err := decodeAgentCompatJSON(r, &input); err != nil {
		writeV11Detail(w, http.StatusBadRequest, "Invalid skill import request: "+err.Error())
		return
	}
	requested := strings.TrimSpace(firstNonEmpty(input.SkillPath, input.LegacyPath))
	items, canonical, err := s.loadGrantedWebSkills(compatAgentUserID(r), requested)
	if err != nil {
		writeV11Detail(w, webSkillPathStatus(err), err.Error())
		return
	}
	s.skillMutationMu.Lock()
	defer s.skillMutationMu.Unlock()
	operationID := uuid.NewString()
	imported := make([]string, 0, len(items))
	failed := make([]map[string]any, 0)
	history := make([]webSkillImportHistoryEntry, 0, len(items))
	for _, skill := range items {
		raw, archiveErr := workspaceSkillArchive(skill)
		if archiveErr == nil {
			_, archiveErr = s.importSkillBundle(skill.Name+".zip", "", input.Replace, raw)
		}
		if archiveErr != nil {
			code := "IMPORT_FAILED"
			if errors.Is(archiveErr, os.ErrExist) {
				code = "ALREADY_EXISTS"
			}
			failed = append(failed, map[string]any{
				"source_name": skill.Name, "code": code, "error_path": skill.Path,
			})
			history = append(history, newWebSkillImportHistory(
				operationID, filepath.Base(canonical), canonical, skill.Name, skill.Name, "failed", code,
			))
			continue
		}
		imported = append(imported, skill.Name)
		history = append(history, newWebSkillImportHistory(
			operationID, filepath.Base(canonical), canonical, skill.Name, skill.Name, "imported", "",
		))
	}
	if err := s.appendWebSkillImportHistory(compatAgentUserID(r), history...); err != nil {
		writeV11Detail(w, http.StatusInternalServerError, "skill import history could not be persisted")
		return
	}
	if len(imported) == 0 {
		writeWorkspaceJSON(w, http.StatusConflict, map[string]any{
			"skill_name": "", "skill_names": []string{}, "failed": failed,
		})
		return
	}
	sort.Strings(imported)
	writeWorkspaceJSON(w, http.StatusCreated, map[string]any{
		"skill_name": imported[0], "skill_names": imported, "failed": failed,
	})
}

func listImportedSkillFiles(root string) ([]string, error) {
	files := make([]string, 0, 8)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list imported skill files: %w", err)
	}
	sort.Strings(files)
	return files, nil
}

func (s *Server) importSkillBundle(filename, requestedName string, replace bool, raw []byte) (skillImportResult, error) {
	extension := strings.ToLower(filepath.Ext(strings.TrimSpace(filename)))
	if extension != ".zip" && extension != ".skill" && extension != ".md" && extension != ".markdown" {
		return skillImportResult{}, errors.New("skill bundle must use .zip, .skill, .md, or .markdown")
	}
	installRoot := filepath.Join(s.fileRoot, "skills")
	if err := ensurePrivateDirectoryForImport(installRoot); err != nil {
		return skillImportResult{}, fmt.Errorf("prepare skill directory: %w", err)
	}
	staging, err := os.MkdirTemp(installRoot, ".skill-import-")
	if err != nil {
		return skillImportResult{}, fmt.Errorf("create skill staging directory: %w", err)
	}
	cleanupStaging := true
	defer func() {
		if cleanupStaging {
			_ = os.RemoveAll(staging)
		}
	}()

	isArchive := extension == ".zip" || (extension == ".skill" && len(raw) >= 4 && bytes.Equal(raw[:2], []byte("PK")))
	if isArchive {
		if err := extractSkillArchive(staging, raw); err != nil {
			return skillImportResult{}, err
		}
	} else {
		if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
			return skillImportResult{}, errors.New("markdown skill is not valid UTF-8 text")
		}
		if err := os.WriteFile(filepath.Join(staging, "SKILL.md"), raw, 0o600); err != nil {
			return skillImportResult{}, fmt.Errorf("stage markdown skill: %w", err)
		}
	}

	loaded := skills.Load([]string{staging}, runtimeSkillLoadOptions())
	if loadErrors := loaded.LoadErrors(); len(loadErrors) > 0 {
		return skillImportResult{}, fmt.Errorf("invalid skill bundle: %s", loadErrors[0].Err)
	}
	items := loaded.Skills()
	if len(items) != 1 {
		return skillImportResult{}, fmt.Errorf("skill bundle must contain exactly one SKILL.md, found %d", len(items))
	}
	skill := items[0]
	manifest, err := os.ReadFile(skill.Path)
	if err != nil {
		return skillImportResult{}, fmt.Errorf("read staged SKILL.md: %w", err)
	}
	fallbackName := strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	if requestedName != "" {
		fallbackName = requestedName
	}
	if !skillManifestHasName(manifest) {
		skill.Name = fallbackName
	}
	skill.Name = strings.TrimSpace(skill.Name)
	if !importedSkillNamePattern.MatchString(skill.Name) || skill.Name == "." || skill.Name == ".." {
		return skillImportResult{}, fmt.Errorf("invalid imported skill name %q", skill.Name)
	}
	if existingSkill, found := findCatalogSkill(s.skillCatalog, skill.Name); found {
		existingRoot := filepath.Clean(filepath.Dir(existingSkill.Path))
		writableRoot := filepath.Clean(filepath.Join(installRoot, skill.Name))
		if strings.HasPrefix(existingSkill.Path, "builtin:") || compatibilitySkillSource(existingSkill) == "synon_llm" {
			return skillImportResult{}, fmt.Errorf("%w: skill %q conflicts with a bundled read-only catalog entry", os.ErrExist, skill.Name)
		}
		if sameCleanPath(existingRoot, writableRoot) && !replace {
			return skillImportResult{}, fmt.Errorf("%w: skill %q is already installed", os.ErrExist, skill.Name)
		}
	}

	sourceRoot := filepath.Dir(skill.Path)
	files, err := listImportedSkillFiles(sourceRoot)
	if err != nil {
		return skillImportResult{}, err
	}
	var description *string
	if skillManifestHasDescription(manifest) {
		description = &skill.Description
	}
	destinationName, existing, err := findCaseInsensitiveSkillDestination(installRoot, skill.Name)
	if err != nil {
		return skillImportResult{}, err
	}
	if destinationName == "" {
		destinationName = skill.Name
	}
	destination := filepath.Join(installRoot, destinationName)
	if existing && !replace {
		return skillImportResult{}, fmt.Errorf("%w: skill %q is already installed", os.ErrExist, destinationName)
	}
	if err := installSkillDirectoryAtomic(sourceRoot, destination, existing); err != nil {
		return skillImportResult{}, err
	}
	if sourceRoot == staging {
		cleanupStaging = false
	}
	relativeManifest, err := filepath.Rel(sourceRoot, skill.Path)
	if err != nil || relativeManifest == "." || strings.HasPrefix(relativeManifest, ".."+string(filepath.Separator)) {
		return skillImportResult{}, errors.New("staged skill manifest escaped its source directory")
	}
	skill.Name = destinationName
	skill.Path = filepath.Join(destination, relativeManifest)
	s.skillCatalog.UpsertSkill(skill)
	return skillImportResult{
		Name: skill.Name, InstalledPath: filepath.Join("skills", skill.Name), Files: files, Description: description,
	}, nil
}

func extractSkillArchive(staging string, raw []byte) error {
	archive, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return fmt.Errorf("open skill archive: %w", err)
	}
	if len(archive.File) > maxSkillArchiveFiles {
		return fmt.Errorf("skill bundle exceeds %d archive entries", maxSkillArchiveFiles)
	}
	seen := map[string]bool{}
	var extractedBytes int64
	for _, entry := range archive.File {
		name := strings.TrimSpace(entry.Name)
		if name == "" || strings.Contains(name, "\\") {
			return errors.New("skill archive contains an invalid path")
		}
		cleaned := pathpkg.Clean(name)
		if cleaned == "." || pathpkg.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
			return fmt.Errorf("skill archive path %q escapes the bundle", name)
		}
		key := strings.ToLower(cleaned)
		if seen[key] {
			return fmt.Errorf("skill archive contains duplicate path %q", cleaned)
		}
		seen[key] = true
		mode := entry.Mode()
		if mode&os.ModeSymlink != 0 || (!entry.FileInfo().IsDir() && !mode.IsRegular()) {
			return fmt.Errorf("skill archive path %q is not a regular file or directory", cleaned)
		}
		target := filepath.Join(staging, filepath.FromSlash(cleaned))
		if !pathWithinRoot(target, staging) {
			return fmt.Errorf("skill archive path %q escapes the bundle", name)
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return fmt.Errorf("create skill archive directory: %w", err)
			}
			continue
		}
		if entry.UncompressedSize64 > uint64(maxSkillArchiveBytes) || extractedBytes+int64(entry.UncompressedSize64) > maxSkillArchiveBytes {
			return fmt.Errorf("skill bundle exceeds %d extracted bytes", maxSkillArchiveBytes)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return fmt.Errorf("create skill archive parent: %w", err)
		}
		source, err := entry.Open()
		if err != nil {
			return fmt.Errorf("open skill archive entry: %w", err)
		}
		destination, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			_ = source.Close()
			return fmt.Errorf("create skill archive entry: %w", err)
		}
		written, copyErr := io.Copy(destination, io.LimitReader(source, maxSkillArchiveBytes-extractedBytes+1))
		closeDestinationErr := destination.Close()
		closeSourceErr := source.Close()
		if copyErr != nil {
			return fmt.Errorf("extract skill archive entry: %w", copyErr)
		}
		if closeDestinationErr != nil || closeSourceErr != nil {
			return errors.New("close extracted skill archive entry")
		}
		extractedBytes += written
		if extractedBytes > maxSkillArchiveBytes || written != int64(entry.UncompressedSize64) {
			return errors.New("skill archive entry size did not match its authenticated bounds")
		}
	}
	return nil
}

func installSkillDirectoryAtomic(source, destination string, replace bool) error {
	if !replace {
		if err := os.Rename(source, destination); err != nil {
			return fmt.Errorf("install skill atomically: %w", err)
		}
		return nil
	}
	info, err := os.Lstat(destination)
	if err != nil {
		return fmt.Errorf("inspect existing skill: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("existing skill destination is not a regular directory")
	}
	backup := destination + ".backup-" + uuid.NewString()
	if err := os.Rename(destination, backup); err != nil {
		return fmt.Errorf("stage existing skill replacement: %w", err)
	}
	if err := os.Rename(source, destination); err != nil {
		_ = os.Rename(backup, destination)
		return fmt.Errorf("install replacement skill atomically: %w", err)
	}
	if err := os.RemoveAll(backup); err != nil {
		removeNewErr := os.RemoveAll(destination)
		restoreErr := os.Rename(backup, destination)
		return fmt.Errorf("remove replaced skill backup: %w", errors.Join(err, removeNewErr, restoreErr))
	}
	return nil
}

func findCaseInsensitiveSkillDestination(root, name string) (string, bool, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", false, fmt.Errorf("list installed skills: %w", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".skill-import-") || strings.Contains(entry.Name(), ".backup-") {
			continue
		}
		if strings.EqualFold(entry.Name(), name) {
			return entry.Name(), true, nil
		}
	}
	return "", false, nil
}

func skillManifestHasName(raw []byte) bool {
	return skillManifestHasStringField(raw, "name")
}

func skillManifestHasDescription(raw []byte) bool {
	return skillManifestHasStringField(raw, "description")
}

func skillManifestHasStringField(raw []byte, field string) bool {
	lines := strings.Split(string(raw), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return false
	}
	for index, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			var metadata map[string]any
			if err := yaml.Unmarshal([]byte(strings.Join(lines[1:index+1], "\n")), &metadata); err != nil {
				return false
			}
			for key, value := range metadata {
				if strings.EqualFold(strings.TrimSpace(key), field) {
					text, ok := value.(string)
					return ok && strings.TrimSpace(text) != ""
				}
			}
			return false
		}
	}
	return false
}

func ensurePrivateDirectoryForImport(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return os.MkdirAll(path, 0o700)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("skill root must be a regular directory")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return os.Chmod(path, 0o700)
	}
	return nil
}

func pathWithinRoot(candidate, root string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func parseImportReplace(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "1" || value == "true" || value == "yes"
}
