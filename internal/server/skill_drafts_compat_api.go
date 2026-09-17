package server

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const compatibilitySkillDraftMarker = ".synon-draft"

func (s *Server) handleCompatibilitySkillDrafts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	if strings.TrimSpace(s.fileRoot) == "" {
		writeJSON(w, http.StatusOK, map[string]any{"drafts": []string{}})
		return
	}
	root := filepath.Join(s.fileRoot, "skills")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusOK, map[string]any{"drafts": []string{}})
		return
	}
	if err != nil {
		writeV11Detail(w, http.StatusInternalServerError, "failed to list skill drafts")
		return
	}
	drafts := make([]string, 0)
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name())
		if !importedSkillNamePattern.MatchString(name) || name == "." || name == ".." {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		directory := filepath.Join(root, name)
		if !regularDraftFile(filepath.Join(directory, compatibilitySkillDraftMarker)) || !regularDraftFile(filepath.Join(directory, "SKILL.md")) {
			continue
		}
		resolved, err := filepath.EvalSymlinks(directory)
		if err != nil || resolved != directory || !pathWithinRoot(resolved, root) {
			continue
		}
		drafts = append(drafts, name)
	}
	sort.Strings(drafts)
	writeJSON(w, http.StatusOK, map[string]any{"drafts": drafts})
}

func regularDraftFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}
