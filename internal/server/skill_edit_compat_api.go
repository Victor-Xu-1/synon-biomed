package server

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"synon-go/internal/skills"
)

var errCompatibilitySkillReadOnly = errors.New("skill is read-only")

func (s *Server) handleCompatibilitySkillMutation(w http.ResponseWriter, r *http.Request) {
	segments := workspacePathSegments(strings.TrimPrefix(r.URL.Path, "/api/skills/"))
	if len(segments) < 1 || len(segments) > 2 {
		writeV11Detail(w, http.StatusNotFound, "Skill endpoint not found")
		return
	}
	if s.skillCatalog == nil || strings.TrimSpace(s.fileRoot) == "" {
		writeV11Detail(w, http.StatusServiceUnavailable, "Writable skill catalog is not configured")
		return
	}
	name, err := url.PathUnescape(strings.TrimSpace(segments[0]))
	if err != nil {
		writeV11Detail(w, http.StatusBadRequest, "Invalid skill name")
		return
	}
	if !importedSkillNamePattern.MatchString(name) || name == "." || name == ".." {
		writeV11Detail(w, http.StatusBadRequest, fmt.Sprintf("invalid skill name '%s'", name))
		return
	}
	if len(segments) == 1 {
		if r.Method != http.MethodDelete {
			writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		s.handleCompatibilitySkillDelete(w, r, name, true)
		return
	}
	switch segments[1] {
	case "edit":
		if r.Method != http.MethodPost {
			writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		s.handleCompatibilitySkillEdit(w, r, name)
	case "duplicate":
		if r.Method != http.MethodPost {
			writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		s.handleCompatibilitySkillDuplicate(w, r, name)
	case "publish":
		if r.Method != http.MethodPost {
			writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		s.handleCompatibilitySkillPublish(w, r, name)
	case "full":
		if r.Method != http.MethodDelete {
			writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		s.handleCompatibilitySkillDelete(w, r, name, false)
	default:
		writeV11Detail(w, http.StatusNotFound, "Skill endpoint not found")
	}
}

func (s *Server) handleCompatibilitySkillEdit(w http.ResponseWriter, r *http.Request, name string) {
	input := map[string]any{}
	if err := decodeAgentCompatJSON(r, &input); err != nil {
		writeV11Detail(w, http.StatusBadRequest, "Invalid skill edit request: "+err.Error())
		return
	}
	relativePath := stringValue(input["path"])
	oldString := stringValue(input["old_string"])
	newString, hasNewString := input["new_string"].(string)
	if strings.TrimSpace(relativePath) == "" || !hasNewString {
		writeV11Detail(w, http.StatusBadRequest, "path and new_string are required")
		return
	}
	if !utf8.ValidString(newString) || strings.IndexByte(newString, 0) >= 0 {
		writeV11Detail(w, http.StatusBadRequest, "new_string must be valid text without NUL bytes")
		return
	}
	s.skillMutationMu.Lock()
	defer s.skillMutationMu.Unlock()
	action, absolutePath, err := s.editCompatibilitySkillFile(name, relativePath, oldString, newString)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errCompatibilitySkillReadOnly) {
			status = http.StatusForbidden
		}
		writeV11Detail(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"action": action, "absolute_path": absolutePath})
}

func (s *Server) handleCompatibilitySkillDuplicate(w http.ResponseWriter, r *http.Request, name string) {
	var input struct {
		SourceName string `json:"sourceName"`
	}
	if err := decodeAgentCompatJSON(r, &input); err != nil {
		writeV11Detail(w, http.StatusBadRequest, "Invalid skill duplicate request: "+err.Error())
		return
	}
	input.SourceName = strings.TrimSpace(input.SourceName)
	if input.SourceName == "" {
		writeV11Detail(w, http.StatusBadRequest, "sourceName is required")
		return
	}
	s.skillMutationMu.Lock()
	defer s.skillMutationMu.Unlock()
	skill, err := s.duplicateCompatibilitySkill(input.SourceName, name)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, os.ErrExist) {
			status = http.StatusConflict
		}
		writeV11Detail(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"name": skill.Name, "draft": true})
}

func (s *Server) handleCompatibilitySkillPublish(w http.ResponseWriter, r *http.Request, name string) {
	var input struct {
		Overwrite bool `json:"overwrite"`
	}
	if err := decodeAgentCompatJSON(r, &input); err != nil {
		writeV11Detail(w, http.StatusBadRequest, "Invalid skill publish request: "+err.Error())
		return
	}
	s.skillMutationMu.Lock()
	defer s.skillMutationMu.Unlock()
	skill, err := s.publishCompatibilitySkillDraft(name, input.Overwrite)
	if err != nil {
		writeV11Detail(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": skill.Name, "published": true})
}

func (s *Server) handleCompatibilitySkillDelete(w http.ResponseWriter, r *http.Request, name string, draftOnly bool) {
	s.skillMutationMu.Lock()
	defer s.skillMutationMu.Unlock()
	if err := s.deleteCompatibilitySkill(compatAgentUserID(r), name, draftOnly); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, os.ErrNotExist) {
			status = http.StatusNotFound
		}
		if errors.Is(err, errCompatibilitySkillReadOnly) {
			status = http.StatusForbidden
		}
		if errors.Is(err, errCompatibilitySkillAttached) {
			status = http.StatusConflict
		}
		writeV11Detail(w, status, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var errCompatibilitySkillAttached = errors.New("skill is attached to an agent")

func (s *Server) duplicateCompatibilitySkill(sourceName, targetName string) (skills.Skill, error) {
	if _, found := findCatalogSkill(s.skillCatalog, targetName); found {
		return skills.Skill{}, fmt.Errorf("%w: skill '%s' already exists", os.ErrExist, targetName)
	}
	source, found := findCatalogSkill(s.skillCatalog, sourceName)
	if !found {
		return skills.Skill{}, fmt.Errorf("source skill '%s' not found", sourceName)
	}
	installRoot := filepath.Join(s.fileRoot, "skills")
	if err := ensurePrivateDirectoryForImport(installRoot); err != nil {
		return skills.Skill{}, err
	}
	destination := filepath.Join(installRoot, targetName)
	if _, err := os.Lstat(destination); err == nil {
		return skills.Skill{}, fmt.Errorf("%w: skill '%s' already exists", os.ErrExist, targetName)
	} else if !errors.Is(err, os.ErrNotExist) {
		return skills.Skill{}, err
	}
	staging, err := os.MkdirTemp(installRoot, ".skill-duplicate-")
	if err != nil {
		return skills.Skill{}, err
	}
	defer os.RemoveAll(staging)
	var sourceRoot string
	if strings.HasPrefix(source.Path, "builtin:") {
		sourceRoot = filepath.Join(staging, source.Name)
		if err := os.Mkdir(sourceRoot, 0o700); err != nil {
			return skills.Skill{}, err
		}
		manifest, err := compatibilitySkillManifest(source, targetName)
		if err != nil {
			return skills.Skill{}, err
		}
		if err := os.WriteFile(filepath.Join(sourceRoot, "SKILL.md"), manifest, 0o600); err != nil {
			return skills.Skill{}, err
		}
	} else {
		raw, err := workspaceSkillArchive(source)
		if err != nil {
			return skills.Skill{}, err
		}
		if err := extractSkillArchive(staging, raw); err != nil {
			return skills.Skill{}, err
		}
		loaded := skills.Load([]string{staging})
		items := loaded.Skills()
		if failures := loaded.LoadErrors(); len(failures) > 0 || len(items) != 1 {
			return skills.Skill{}, errors.New("source skill could not be staged safely")
		}
		sourceRoot = filepath.Dir(items[0].Path)
		if err := rewriteCompatibilitySkillManifestName(filepath.Join(sourceRoot, "SKILL.md"), targetName); err != nil {
			return skills.Skill{}, err
		}
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, compatibilitySkillDraftMarker), []byte("draft\n"), 0o600); err != nil {
		return skills.Skill{}, err
	}
	if err := os.Rename(sourceRoot, destination); err != nil {
		return skills.Skill{}, err
	}
	loaded := skills.Load([]string{destination}, runtimeSkillLoadOptions())
	items := loaded.Skills()
	if failures := loaded.LoadErrors(); len(failures) > 0 || len(items) != 1 || !strings.EqualFold(items[0].Name, targetName) {
		_ = os.RemoveAll(destination)
		return skills.Skill{}, errors.New("duplicated skill failed validation")
	}
	s.skillCatalog.UpsertSkill(items[0])
	return items[0], nil
}

func (s *Server) publishCompatibilitySkillDraft(name string, overwrite bool) (skills.Skill, error) {
	root := filepath.Join(s.fileRoot, "skills", name)
	if err := verifyWritableCompatibilitySkillRoot(root); err != nil {
		return skills.Skill{}, err
	}
	marker := filepath.Join(root, compatibilitySkillDraftMarker)
	if !regularDraftFile(marker) {
		return skills.Skill{}, fmt.Errorf("skill '%s' is not a draft", name)
	}
	if !overwrite {
		if existing, found := findCatalogSkill(s.skillCatalog, name); found && !sameCleanPath(filepath.Dir(existing.Path), root) {
			return skills.Skill{}, fmt.Errorf("skill '%s' already exists", name)
		}
	}
	if err := os.Remove(marker); err != nil {
		return skills.Skill{}, err
	}
	loaded := skills.Load([]string{root}, runtimeSkillLoadOptions())
	items := loaded.Skills()
	if failures := loaded.LoadErrors(); len(failures) > 0 || len(items) != 1 || !strings.EqualFold(items[0].Name, name) {
		_ = os.WriteFile(marker, []byte("draft\n"), 0o600)
		return skills.Skill{}, errors.New("published skill failed validation")
	}
	s.skillCatalog.UpsertSkill(items[0])
	return items[0], nil
}

func (s *Server) deleteCompatibilitySkill(userID, name string, draftOnly bool) error {
	skill, found := findCatalogSkill(s.skillCatalog, name)
	if !found {
		return fmt.Errorf("%w: skill '%s' not found", os.ErrNotExist, name)
	}
	root, err := s.compatibilityEditableSkillRoot(skill)
	if err != nil {
		return err
	}
	if draftOnly && !regularDraftFile(filepath.Join(root, compatibilitySkillDraftMarker)) {
		return fmt.Errorf("skill '%s' is not a draft", name)
	}
	if s.workspaceStore != nil {
		agents, err := s.workspaceStore.ListAgents(userID)
		if err != nil {
			return err
		}
		attached := make([]string, 0)
		for _, agent := range agents {
			for _, skillName := range agent.SkillNames {
				if strings.EqualFold(skillName, name) {
					attached = append(attached, agent.Name)
					break
				}
			}
		}
		if len(attached) > 0 {
			sort.Strings(attached)
			return fmt.Errorf("%w: skill '%s' is used by agents: %s", errCompatibilitySkillAttached, name, strings.Join(attached, ", "))
		}
	}
	trash := filepath.Join(filepath.Dir(root), ".skill-delete-"+uuid.NewString())
	if err := os.Rename(root, trash); err != nil {
		return err
	}
	if err := os.RemoveAll(trash); err != nil {
		_ = os.Rename(trash, root)
		return err
	}
	s.skillCatalog.RemoveSkill(name)
	return nil
}

func verifyWritableCompatibilitySkillRoot(root string) error {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("writable skill root is not a regular directory")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil || resolved != absolute {
		return errors.New("writable skill root is not a verified regular directory")
	}
	return nil
}

func compatibilitySkillManifest(skill skills.Skill, name string) ([]byte, error) {
	metadata := map[string]any{"name": name, "description": skill.Description}
	if len(skill.Tags) > 0 {
		metadata["tags"] = skill.Tags
	}
	if len(skill.Keywords) > 0 {
		metadata["keywords"] = skill.Keywords
	}
	if len(skill.Tools) > 0 {
		metadata["tools"] = skill.Tools
	}
	if len(skill.Arguments) > 0 {
		metadata["arguments"] = skill.Arguments
	}
	if len(skill.References) > 0 {
		metadata["references"] = skill.References
	}
	frontMatter, err := yaml.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	return []byte("---\n" + strings.TrimSpace(string(frontMatter)) + "\n---\n\n" + strings.TrimSpace(skill.Body) + "\n"), nil
}

func rewriteCompatibilitySkillManifestName(path, name string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return errors.New("skill manifest is not valid UTF-8 text")
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return writeSkillFileAtomic(path, []byte("---\nname: "+name+"\n---\n\n"+text), false)
	}
	closing := strings.Index(text[4:], "\n---")
	if closing < 0 {
		return errors.New("skill manifest has unterminated frontmatter")
	}
	closing += 4
	frontMatter := text[4:closing]
	bodyStart := closing + len("\n---")
	body := strings.TrimPrefix(text[bodyStart:], "\n")
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(frontMatter), &document); err != nil {
		return err
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return errors.New("skill manifest frontmatter must be a mapping")
	}
	root := document.Content[0]
	updated := false
	for index := 0; index+1 < len(root.Content); index += 2 {
		if strings.EqualFold(strings.TrimSpace(root.Content[index].Value), "name") {
			root.Content[index+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}
			updated = true
			break
		}
	}
	if !updated {
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "name"},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name},
		)
	}
	encoded, err := yaml.Marshal(&document)
	if err != nil {
		return err
	}
	return writeSkillFileAtomic(path, []byte("---\n"+strings.TrimSpace(string(encoded))+"\n---\n"+body), false)
}

func (s *Server) editCompatibilitySkillFile(name, relativePath, oldString, newString string) (string, string, error) {
	skill, found := findCatalogSkill(s.skillCatalog, name)
	if !found {
		return "", "", fmt.Errorf("skill '%s' not found", name)
	}
	editableRoot, err := s.compatibilityEditableSkillRoot(skill)
	if err != nil {
		return "", "", err
	}
	relativePath = filepath.ToSlash(strings.TrimSpace(relativePath))
	if relativePath == "." || strings.HasPrefix(relativePath, "/") || relativePath == ".." ||
		strings.HasPrefix(relativePath, "../") || strings.Contains(relativePath, "/../") {
		return "", "", fmt.Errorf("invalid path '%s'", relativePath)
	}
	target := filepath.Join(editableRoot, filepath.FromSlash(relativePath))
	if !pathWithinRoot(target, editableRoot) {
		return "", "", fmt.Errorf("invalid path '%s'", relativePath)
	}
	if err := ensureSkillEditParent(editableRoot, filepath.Dir(target)); err != nil {
		return "", "", err
	}
	if oldString == "" {
		if _, err := os.Lstat(target); err == nil {
			return "", "", fmt.Errorf("file '%s' already exists in '%s' - read it first, then edit with a non-empty old_string", relativePath, name)
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", "", err
		}
		if len(newString) > maxSkillArchiveBytes {
			return "", "", fmt.Errorf("skill file exceeds %d bytes", maxSkillArchiveBytes)
		}
		if err := writeSkillFileAtomic(target, []byte(newString), true); err != nil {
			return "", "", err
		}
		if err := s.reloadEditedSkill(name, editableRoot); err != nil {
			_ = os.Remove(target)
			return "", "", err
		}
		return "created", target, nil
	}
	source, err := openGrantedRegularFile(target, []hostGrant{{Path: editableRoot, Mode: "read"}})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", fmt.Errorf("file '%s' does not exist in '%s' - use empty old_string to create it", relativePath, name)
		}
		return "", "", err
	}
	raw, readErr := io.ReadAll(io.LimitReader(source, maxSkillArchiveBytes+1))
	closeErr := source.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return "", "", err
	}
	if len(raw) > maxSkillArchiveBytes {
		return "", "", fmt.Errorf("skill file exceeds %d bytes", maxSkillArchiveBytes)
	}
	current := string(raw)
	matches := strings.Count(current, oldString)
	if matches == 0 {
		return "", "", errors.New("old_string not found - read the file first; the edit target may have changed")
	}
	if matches > 1 {
		return "", "", fmt.Errorf("old_string matches %d times - add more surrounding context so it matches exactly once", matches)
	}
	updated := strings.Replace(current, oldString, newString, 1)
	if len(updated) > maxSkillArchiveBytes {
		return "", "", fmt.Errorf("skill file exceeds %d bytes", maxSkillArchiveBytes)
	}
	if err := writeSkillFileAtomic(target, []byte(updated), false); err != nil {
		return "", "", err
	}
	if err := s.reloadEditedSkill(name, editableRoot); err != nil {
		rollbackErr := writeSkillFileAtomic(target, raw, false)
		return "", "", errors.Join(err, rollbackErr)
	}
	return "edited", target, nil
}

func (s *Server) compatibilityEditableSkillRoot(skill skills.Skill) (string, error) {
	if strings.HasPrefix(skill.Path, "builtin:") {
		return "", fmt.Errorf("%w: built-in skills cannot be edited", errCompatibilitySkillReadOnly)
	}
	root, err := filepath.Abs(filepath.Dir(skill.Path))
	if err != nil {
		return "", err
	}
	writableRoot, err := filepath.Abs(filepath.Join(s.fileRoot, "skills"))
	if err != nil {
		return "", err
	}
	if !pathWithinRoot(root, writableRoot) {
		return "", fmt.Errorf("%w: skill '%s' cannot be edited in place; import or duplicate it into the writable skill catalog", errCompatibilitySkillReadOnly, skill.Name)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root {
		return "", errors.New("writable skill root is not a verified regular directory")
	}
	return root, nil
}

func ensureSkillEditParent(root, parent string) error {
	if !pathWithinRoot(parent, root) {
		return errors.New("skill edit parent escaped its root")
	}
	relative, err := filepath.Rel(root, parent)
	if err != nil {
		return err
	}
	current := root
	for _, segment := range strings.Split(relative, string(filepath.Separator)) {
		if segment == "" || segment == "." {
			continue
		}
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("skill edit parent contains a non-directory or symlink")
		}
	}
	return nil
}

func writeSkillFileAtomic(target string, content []byte, exclusive bool) error {
	if exclusive {
		file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if _, err = file.Write(content); err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		return errors.Join(err, closeErr)
	}
	temporary := filepath.Join(filepath.Dir(target), "."+filepath.Base(target)+".edit-"+uuid.NewString())
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(temporary)
		}
	}()
	if _, err = file.Write(content); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return err
	}
	if err := os.Rename(temporary, target); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func (s *Server) reloadEditedSkill(name, root string) error {
	reloaded := skills.Load([]string{root})
	if loadErrors := reloaded.LoadErrors(); len(loadErrors) > 0 {
		return fmt.Errorf("edited skill is invalid: %s", loadErrors[0].Err)
	}
	for _, skill := range reloaded.Skills() {
		if strings.EqualFold(skill.Name, name) {
			s.skillCatalog.UpsertSkill(skill)
			return nil
		}
	}
	return fmt.Errorf("edited skill %q no longer has a valid SKILL.md", name)
}
