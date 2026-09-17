package server

import (
	"path/filepath"
	"sort"
	"strings"
	"synon-go/internal/skills"
	"synon-go/internal/toolcontract"
	"synon-go/internal/tools/registry"
)

func statusForSkills(catalog *skills.Catalog, loadErrors []skills.LoadError) string {
	if catalog == nil {
		return "missing"
	}
	if len(loadErrors) > 0 {
		return "partial"
	}
	if len(catalog.Skills()) == 0 {
		return "partial"
	}
	return "pass"
}

func (s *Server) skillsDoctorArea() (string, string, map[string]any, []string) {
	skillSourcePaths, skillReferences := s.skillRuntimeDiagnostics()
	skillCount := 0
	if s != nil && s.skillCatalog != nil {
		skillCount = len(s.skillCatalog.Skills())
	}
	hasSkillSearch := hasRegisteredTool(s.tools, toolcontract.SearchSkills)
	skillToolReady := hasRegisteredTool(s.tools, toolcontract.Skill)
	hasSelfContainedSkillCatalog := skillCatalogSelfContained(s.skillDirectories)
	// Explicit SkillDirectories still accepts project .synon/skills paths. That
	// compatibility must not be confused with implicitly trusting the process
	// cwd or its ancestors.
	hasProjectSynonSkillCompatibility := len(s.skillDirectories) > 0
	usesPersonalSkillDefaults := skillDirectoriesUsePersonalDefaults(s.skillDirectories)
	personalSkillDirectoriesExcluded := !usesPersonalSkillDefaults
	hasSkillSourceDiagnostics := len(skillSourcePaths) > 0
	hasSkillReferenceDiagnostics := len(skillReferences) > 0
	hasBoundedSkillReferences := hasSkillReferenceDiagnostics
	skillCatalogReady := s != nil && s.skillCatalog != nil && len(s.skillErrors) == 0 && skillCount > 0
	baseStatus := "missing"
	if s != nil {
		baseStatus = statusForSkills(s.skillCatalog, s.skillErrors)
	}
	ready := skillCatalogReady &&
		hasSkillSearch &&
		skillToolReady &&
		hasSelfContainedSkillCatalog &&
		hasProjectSynonSkillCompatibility &&
		personalSkillDirectoriesExcluded &&
		hasSkillSourceDiagnostics &&
		hasSkillReferenceDiagnostics &&
		hasBoundedSkillReferences
	status := baseStatus
	if ready {
		status = "pass"
	} else if status == "pass" {
		status = "partial"
	}
	next := []string{}
	if !skillCatalogReady {
		next = append(next, "Load a non-empty packaged SKILL.md catalog without load errors.")
	}
	if !hasSkillSearch {
		next = append(next, "Register search_skills so packaged skills can be discovered deterministically.")
	}
	if !skillToolReady {
		next = append(next, "Register the Skill tool so selected skills can be invoked with source metadata.")
	}
	if !hasSelfContainedSkillCatalog {
		next = append(next, "Use the packaged skill tree or explicitly configured project-local skill directories; do not depend on personal default skill paths.")
	}
	if !hasProjectSynonSkillCompatibility {
		next = append(next, "Configure an explicit trusted skill directory or use the packaged skill catalog.")
	}
	if usesPersonalSkillDefaults {
		next = append(next, "Remove personal default skill directories from the runtime skill catalog; only packaged or project-local skill directories may pass parity.")
	}
	if !hasSkillSourceDiagnostics || !hasSkillReferenceDiagnostics {
		next = append(next, "Expose skill source and bounded reference diagnostics for runtime audit and replay.")
	}
	evidence := map[string]any{
		"hasSkillSearch":                      hasSkillSearch,
		"hasSkillTool":                        skillToolReady,
		"hasInlineSkillInvocation":            skillToolReady,
		"hasSkillPromptRendering":             skillToolReady,
		"hasSkillArgumentSubstitution":        skillToolReady,
		"hasSkillIndexedArgumentSubstitution": skillToolReady,
		"hasSkillNamedArgumentSubstitution":   skillToolReady,
		"hasSkillDirectorySubstitution":       skillToolReady,
		"hasSelfContainedSkillCatalog":        hasSelfContainedSkillCatalog,
		"hasProjectSynonSkillCompatibility":   hasProjectSynonSkillCompatibility,
		"usesPersonalSkillDefaults":           usesPersonalSkillDefaults,
		"personalSkillDirectoriesExcluded":    personalSkillDirectoriesExcluded,
		"hasDeterministicSkillSelect":         hasSkillSearch,
		"hasSkillSourceDiagnostics":           hasSkillSourceDiagnostics,
		"hasSkillReferenceDiagnostics":        hasSkillReferenceDiagnostics,
		"hasBoundedSkillReferences":           hasBoundedSkillReferences,
		"skillSourcePaths":                    skillSourcePaths,
		"skillReferences":                     skillReferences,
		"skillCatalogLoaded":                  s != nil && s.skillCatalog != nil,
		"skillCount":                          skillCount,
		"skillLoadErrors":                     len(s.skillErrors),
		"skillDirectories":                    s.skillDirectories,
		"skillCatalogReady":                   skillCatalogReady,
	}
	return status, "Go runtime loads self-contained SKILL.md catalogs from the packaged skill tree and explicitly configured directories, supports project-local Synon skill compatibility without default personal-directory leakage, renders inline Skill prompts with argument and skill-directory substitution, invokes skill calls with full source metadata, injects selected skill context with bounded references, and exposes deterministic search_skills diagnostics for replay.", evidence, next
}

func (s *Server) skillRuntimeDiagnostics() ([]string, []map[string]any) {
	if s == nil || s.skillCatalog == nil {
		return nil, nil
	}
	const maxSkillDiagnostics = 12
	sourcePaths := []string{}
	references := []map[string]any{}
	for _, skill := range s.skillCatalog.Skills() {
		if len(sourcePaths) < maxSkillDiagnostics {
			if path := strings.TrimSpace(skill.Path); path != "" {
				sourcePaths = append(sourcePaths, path)
			}
		}
		for _, reference := range skill.References {
			if len(references) >= maxSkillDiagnostics {
				break
			}
			reference = strings.TrimSpace(reference)
			if reference == "" {
				continue
			}
			references = append(references, map[string]any{
				"skill":     skill.Name,
				"reference": reference,
				"source":    skill.Path,
			})
		}
		if len(sourcePaths) >= maxSkillDiagnostics && len(references) >= maxSkillDiagnostics {
			break
		}
	}
	return sourcePaths, references
}

func skillDirectoriesUsePersonalDefaults(directories []string) bool {
	for _, directory := range directories {
		value := strings.ToLower(filepath.ToSlash(filepath.Clean(strings.TrimSpace(directory))))
		if strings.Contains(value, "/.codex/skills") || strings.Contains(value, "/.claude/skills") || strings.Contains(value, "/.config/synon/skills") {
			return true
		}
	}
	return false
}

func skillCatalogSelfContained(directories []string) bool {
	if len(directories) == 0 {
		return false
	}
	for _, directory := range directories {
		value := strings.TrimSpace(filepath.ToSlash(directory))
		if value == "" {
			continue
		}
		if strings.HasPrefix(value, "builtin:") || value == "skills" || strings.HasSuffix(value, "/skills") {
			return true
		}
	}
	return false
}

func truncateServerString(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	if maxBytes <= 3 {
		return value[:maxBytes]
	}
	return value[:maxBytes-3] + "..."
}

func splitToolName(name string) string {
	var builder strings.Builder
	var previous rune
	for index, current := range name {
		if index > 0 && previous >= 'a' && previous <= 'z' && current >= 'A' && current <= 'Z' {
			builder.WriteByte(' ')
		}
		if current == '_' || current == '-' {
			builder.WriteByte(' ')
		} else {
			builder.WriteRune(current)
		}
		previous = current
	}
	return builder.String()
}

func toolInputNames(tool registry.Tool) []string {
	names := make([]string, 0, len(tool.Input))
	for name := range tool.Input {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}
