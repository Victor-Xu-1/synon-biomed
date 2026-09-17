package server

import (
	"fmt"
	"strings"

	"synon-go/internal/skills"
)

const sessionRunnerSkillDependencyClosureLimit = 32
const maxRuntimeSkillContractBytes = 256 << 10

type runtimeSkillPolicyAuthority struct {
	AllowedNames  []string
	ExcludedNames []string
	Restrict      bool
}

func runtimeSkillPolicyAuthorityFromOptions(options SessionRunnerChatOptions) runtimeSkillPolicyAuthority {
	return runtimeSkillPolicyAuthority{
		AllowedNames:  append([]string(nil), options.AllowedSkillNames...),
		ExcludedNames: append([]string(nil), options.ExcludedSkillNames...),
		Restrict:      options.RestrictSkillDiscovery,
	}
}

// expandRuntimeSkillDependencies resolves declarative Skill composition before
// any body reaches the model. Dependencies are ordered before dependants,
// deduplicated, checked against the same enabled/allowed/excluded policy and
// live tool authority as their parent, and bounded to prevent cycles or an
// accidental context explosion. This keeps composition in one loader path
// instead of relying on a weaker model to interpret prose such as "load X
// first" or duplicating X across several higher-level Skills.
func (s *Server) expandRuntimeSkillDependencies(
	roots []skills.Skill,
	excluded, allowed []string,
	restrict bool,
	toolAuthority map[string]struct{},
	sessionSkills ...[]skills.Skill,
) ([]skills.Skill, error) {
	if len(roots) == 0 {
		return nil, nil
	}
	if s == nil || s.skillCatalog == nil {
		return nil, selectedSkillContractUnavailable{detail: "the skill dependency catalog is unavailable"}
	}

	available := make(map[string]skills.Skill, len(s.skillCatalog.Skills())+len(roots))
	for _, skill := range s.skillCatalog.Skills() {
		if name := normalizeRuntimeSkillDependencyName(skill.Name); name != "" {
			available[name] = skill
		}
	}
	// Connector contracts belong to the immutable session snapshot, never the
	// global catalog. Include them for dependency resolution as well as roots.
	for _, snapshot := range sessionSkills {
		for _, skill := range snapshot {
			if name := normalizeRuntimeSkillDependencyName(skill.Name); name != "" {
				available[name] = skill
			}
		}
	}
	// Session-scoped connector Skills are not stored in the global catalog.
	// Preserve them as roots and as potential dependencies if a future runtime
	// composition declares one explicitly.
	for _, skill := range roots {
		if name := normalizeRuntimeSkillDependencyName(skill.Name); name != "" {
			available[name] = skill
		}
	}

	excludedSet := normalizedSkillNameSet(excluded)
	allowedSet := normalizedSkillNameSet(allowed)
	state := make(map[string]uint8, len(available))
	ordered := make([]skills.Skill, 0, len(roots))
	stack := make([]string, 0, 4)

	var visit func(skills.Skill, string) error
	visit = func(skill skills.Skill, requiredBy string) error {
		name := normalizeRuntimeSkillDependencyName(skill.Name)
		if name == "" {
			return selectedSkillContractUnavailable{detail: "a selected skill has an empty dependency identity"}
		}
		if !s.runtimeSkillEnabled(skill.Name) {
			detail := fmt.Sprintf("skill %q is disabled", skill.Name)
			if requiredBy != "" {
				detail = fmt.Sprintf("skill %q requires disabled skill %q", requiredBy, skill.Name)
			}
			return selectedSkillContractUnavailable{detail: detail}
		}
		if _, blocked := excludedSet[name]; blocked {
			detail := fmt.Sprintf("skill %q is excluded by the current policy", skill.Name)
			if requiredBy != "" {
				detail = fmt.Sprintf("skill %q requires excluded skill %q", requiredBy, skill.Name)
			}
			return selectedSkillContractUnavailable{detail: detail}
		}
		if restrict {
			if _, permitted := allowedSet[name]; !permitted {
				detail := fmt.Sprintf("skill %q is outside the current allow-list", skill.Name)
				if requiredBy != "" {
					detail = fmt.Sprintf("skill %q requires skill %q outside the current allow-list", requiredBy, skill.Name)
				}
				return selectedSkillContractUnavailable{detail: detail}
			}
		}
		if len(toolAuthority) > 0 {
			if _, referenceable := s.agentRuntimeReferenceableSkillSet(
				[]skills.Skill{skill}, toolAuthority,
			)[name]; !referenceable {
				detail := fmt.Sprintf("skill %q has no live tool contract", skill.Name)
				if requiredBy != "" {
					detail = fmt.Sprintf("skill %q requires skill %q whose live tool contract is unavailable", requiredBy, skill.Name)
				}
				return selectedSkillContractUnavailable{detail: detail}
			}
		}
		switch state[name] {
		case 2:
			return nil
		case 1:
			cycle := append(append([]string(nil), stack...), name)
			return selectedSkillContractUnavailable{detail: "skill dependency cycle: " + strings.Join(cycle, " -> ")}
		}
		if len(ordered)+len(stack) >= sessionRunnerSkillDependencyClosureLimit {
			return selectedSkillContractUnavailable{detail: fmt.Sprintf(
				"skill dependency closure exceeds %d entries", sessionRunnerSkillDependencyClosureLimit,
			)}
		}
		state[name] = 1
		stack = append(stack, name)
		for _, rawDependency := range skill.RequiredSkills {
			dependencyName := normalizeRuntimeSkillDependencyName(rawDependency)
			dependency, found := available[dependencyName]
			if !found {
				return selectedSkillContractUnavailable{detail: fmt.Sprintf(
					"skill %q requires unavailable skill %q", skill.Name, rawDependency,
				)}
			}
			if err := visit(dependency, skill.Name); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		state[name] = 2
		ordered = append(ordered, skill)
		return nil
	}

	for _, root := range roots {
		if err := visit(root, ""); err != nil {
			return nil, err
		}
	}
	return ordered, nil
}

func normalizeRuntimeSkillDependencyName(value string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(value), "/"))
}
