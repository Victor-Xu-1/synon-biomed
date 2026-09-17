package server

import "synon-go/internal/skills"

// runtimeSkillClosure resolves one selected Skill and its dependencies against
// the immutable turn authority. Selection itself remains model- or user-owned;
// this loader never chooses a Skill or a subsequent executable Tool.
func (g serverAgentRuntimeToolGateway) runtimeSkillClosure(name string) ([]skills.Skill, error) {
	selected, err := g.server.runtimeSkillsByNameWithConnectorSchemas(
		[]string{name}, g.skillPolicy.ExcludedNames, g.toolSchemas,
	)
	if err != nil {
		return nil, err
	}
	allowed := runtimeSkillAllowedNamesWithConnectorDependencies(g.skillPolicy.AllowedNames, selected)
	return g.server.expandRuntimeSkillDependencies(
		selected,
		g.skillPolicy.ExcludedNames,
		allowed,
		g.skillPolicy.Restrict,
		agentRuntimeToolSchemaNameSet(g.toolSchemas),
		selected,
	)
}
