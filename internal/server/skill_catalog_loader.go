package server

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"synon-go/internal/skills"
)

func loadSkillCatalog(provided *skills.Catalog, configured []string, fileRoot string) (*skills.Catalog, []string, []skills.LoadError) {
	if provided != nil {
		provided = withBuiltinRuntimeSkill(provided)
		return provided, configured, provided.LoadErrors()
	}
	if len(configured) > 0 {
		catalog := skills.NewCatalog()
		absolute := make([]string, 0, len(configured))
		for _, directory := range configured {
			directory = strings.TrimSpace(directory)
			if directory == "" {
				continue
			}
			if !filepath.IsAbs(directory) {
				catalog.AddLoadError(directory, errors.New("configured skill directories must be absolute trusted paths"))
				continue
			}
			absolute = append(absolute, directory)
		}
		// Explicit skill directories identify the verified bundled/external
		// sources, but they must not disable the user's persistent Skill
		// library. Personal Skills are stored under FileRoot/skills and must be
		// loaded on every restart so the settings catalog can classify them as
		// personal rather than silently dropping them.
		absolute = append(absolute, existingSkillDirectories([]string{personalSkillDirectory(fileRoot)})...)
		absolute = uniqueSkillDirectories(absolute)
		loaded := skills.Load(absolute, runtimeSkillLoadOptions())
		for _, skill := range loaded.Skills() {
			catalog.AddSkill(skill)
		}
		for _, loadErr := range loaded.LoadErrors() {
			catalog.AddLoadError(loadErr.Path, errors.New(loadErr.Err))
		}
		catalog = withBuiltinRuntimeSkill(catalog)
		return catalog, absolute, catalog.LoadErrors()
	}

	directories := existingSkillDirectories(defaultSkillDirectoryCandidates(fileRoot))
	if len(directories) == 0 {
		catalog := skills.BuiltinRuntimeCatalog()
		return catalog, []string{"builtin:synon-runtime"}, catalog.LoadErrors()
	}
	catalog := skills.Load(directories, runtimeSkillLoadOptions())
	catalog = withBuiltinRuntimeSkill(catalog)
	return catalog, directories, catalog.LoadErrors()
}

func runtimeSkillLoadOptions() skills.LoadOptions {
	return skills.LoadOptions{
		MaxBodyBytes:       maxRuntimeSkillContractBytes,
		RejectOversizeBody: true,
	}
}

func withBuiltinRuntimeSkill(catalog *skills.Catalog) *skills.Catalog {
	if catalog == nil {
		catalog = skills.NewCatalog()
	}
	for _, skill := range skills.BuiltinRuntimeCatalog().Skills() {
		catalog.UpsertSkill(skill)
	}
	return catalog
}

func personalSkillDirectory(fileRoot string) string {
	if strings.TrimSpace(fileRoot) == "" || !filepath.IsAbs(fileRoot) {
		return ""
	}
	return filepath.Join(fileRoot, "skills")
}

func defaultSkillDirectoryCandidates(fileRoot string) []string {
	directories := []string{}
	if strings.TrimSpace(fileRoot) != "" && filepath.IsAbs(fileRoot) {
		// FileRoot/skills is populated only through the product's explicit import
		// flow. Do not trust cwd, repository ancestors, learned output, or other
		// ambient filesystem locations as executable model instructions.
		directories = append(directories, filepath.Join(fileRoot, "skills"))
	}
	if executable, err := os.Executable(); err == nil && strings.TrimSpace(executable) != "" {
		executableDir := filepath.Dir(executable)
		// Packaged skills live beside the verified product executable. Source
		// development declares its skill root explicitly through SYNON_SKILL_DIRS.
		directories = append(directories, filepath.Join(executableDir, "skills"))
	}
	return uniqueSkillDirectories(directories)
}

func existingSkillDirectories(candidates []string) []string {
	directories := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && info.IsDir() {
			directories = append(directories, candidate)
		}
	}
	return directories
}

func uniqueSkillDirectories(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		cleaned := filepath.Clean(value)
		key := strings.ToLower(filepath.ToSlash(cleaned))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, cleaned)
	}
	return out
}
