package server

import (
	"path/filepath"
	"strings"
)

// A repeated Skill load is not new execution progress. Its recovery receipt
// must nevertheless retain the current, verified callable asset rather than
// force the model to reconstruct a path from the Skill's display name.
func (g serverAgentRuntimeToolGateway) retainedSkillExecutionDiagnostic(name string) map[string]any {
	name = strings.TrimPrefix(strings.TrimSpace(name), "/")
	if g.server == nil || g.server.skillCatalog == nil || g.server.scienceCapabilities == nil ||
		g.taskRun == nil || g.kernel == nil || g.kernel.workspaceDir == "" || !g.taskRunHasExecutedSkill(name) {
		return nil
	}
	skill, found := findCatalogSkill(g.server.skillCatalog, name)
	if !found || !skillAuthorizedByImplementationState(skill, g.taskRun.selectedImplementationsSnapshot(), g.taskRun.TaskIntent) &&
		!g.taskRun.evidenceResolverAuthorizesSkill(skill) {
		return nil
	}
	engines := g.server.scienceCapabilities.LocalExecutionPacksForSkill(skill.Name)
	if len(engines) != 1 {
		return nil // Multiple entrypoints require their complete existing Skill contract.
	}
	bundle, resources, err := loadAgentSkillRuntimeBundle(skill)
	if err != nil || !resources {
		return nil
	}
	directory := filepath.Join(g.kernel.workspaceDir, ".synon", "runtime", "skills", agentSkillRuntimeDirectoryName(skill.Name), bundle.digest)
	if valid, err := verifyAgentSkillRuntimeBundle(directory, bundle); err != nil || !valid {
		return nil
	}
	entrypoint := engines[0].ExecutionPack.MaterializedSkillEntrypoint()
	if entrypoint == "" {
		return nil
	}
	entrypointPresent := false
	for _, file := range bundle.files {
		entrypointPresent = entrypointPresent || file.relativePath == entrypoint
	}
	if !entrypointPresent {
		return nil
	}
	return map[string]any{
		"ok": false, "executed": false, "status": "skill_contract_already_available",
		"skill": skill.Name, "required_entrypoint": filepath.ToSlash(filepath.Join(directory, entrypoint)),
		"execution_pack_id": engines[0].ExecutionPack.ID,
		"message":           "The selected Skill is already loaded and its current task-scoped runtime bundle is verified.",
		"recovery":          "Reuse required_entrypoint with its documented arguments or --help through the existing bash tool and selected managed environment. Loading the Skill does not execute the computation; do not guess another script, binary, package or download.",
	}
}
