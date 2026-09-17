package server

import (
	"path/filepath"
	"strings"

	"synon-go/internal/skills"
)

// agentRuntimeCommandIsTrustedGenericMaterializedSkillExecution recognizes the
// narrow execution authority needed for an implementation-agnostic Skill to
// prepare task inputs before the user chooses a scientific implementation.
// Trust comes from current task state and declarative Skill metadata, not from
// task text, script names, engines, environments, or a path substring alone.
func (g serverAgentRuntimeToolGateway) agentRuntimeCommandIsTrustedGenericMaterializedSkillExecution(
	publicName string,
	input map[string]any,
) bool {
	if g.server == nil || g.server.skillCatalog == nil || g.taskRun == nil || g.kernel == nil {
		return false
	}
	workspaceRoot, err := g.server.canonicalAgentWorkspaceRoot(g.kernel)
	if err != nil {
		return false
	}
	tokens, scriptPath, scriptIndex, ok := agentRuntimeSingleMaterializedSkillExecution(
		publicName, stringValue(input["command"]),
	)
	if !ok || !agentRuntimeMaterializedSkillScriptIsDirectInvocation(tokens, scriptPath, scriptIndex) {
		return false
	}
	for _, skillName := range g.taskRun.executedSkillNamesSnapshot() {
		skill, found := findCatalogSkill(g.server.skillCatalog, skillName)
		if !found || len(skill.ImplementationIdentities) > 0 {
			continue
		}
		bundle, hasRuntimeResources, err := loadAgentSkillRuntimeBundle(skill)
		if err != nil || !hasRuntimeResources {
			continue
		}
		target, matched := agentRuntimeMaterializedScriptTargetForPreferredAsset(
			workspaceRoot, skill, scriptPath, bundle.digest,
		)
		if !matched {
			continue
		}
		valid, verifyErr := verifyAgentSkillRuntimeBundle(target, bundle)
		if verifyErr == nil && valid {
			return true
		}
	}
	return false
}

var agentRuntimeMaterializedSkillInterpreters = map[string]map[string]struct{}{
	".bash": {"bash": {}, "sh": {}},
	".cjs":  {"node": {}},
	".js":   {"node": {}},
	".mjs":  {"node": {}},
	".pl":   {"perl": {}},
	".ps1":  {"powershell": {}, "powershell.exe": {}, "pwsh": {}, "pwsh.exe": {}},
	".py":   {"python": {}, "python.exe": {}, "python3": {}, "python3.exe": {}},
	".r":    {"rscript": {}, "rscript.exe": {}},
	".rb":   {"ruby": {}},
	".sh":   {"bash": {}, "sh": {}},
	".zsh":  {"zsh": {}},
}

func agentRuntimeMaterializedSkillScriptIsDirectInvocation(
	tokens []string,
	scriptPath string,
	scriptIndex int,
) bool {
	if scriptIndex == 0 {
		return len(tokens) > 0 && tokens[0] == scriptPath
	}
	if scriptIndex != 1 || len(tokens) < 2 {
		return false
	}
	interpreters := agentRuntimeMaterializedSkillInterpreters[strings.ToLower(filepath.Ext(scriptPath))]
	_, allowed := interpreters[strings.ToLower(filepath.Base(tokens[0]))]
	return allowed
}

func agentRuntimeMaterializedScriptTargetForPreferredAsset(
	workspaceRoot string,
	skill skills.Skill,
	scriptPath string,
	expectedDigest string,
) (string, bool) {
	workspaceRoot = filepath.Clean(strings.TrimSpace(workspaceRoot))
	scriptPath = filepath.Clean(strings.TrimSpace(scriptPath))
	if !filepath.IsAbs(workspaceRoot) || !filepath.IsAbs(scriptPath) ||
		!agentSkillRuntimeValidSHA256(expectedDigest) {
		return "", false
	}
	target := filepath.Join(
		workspaceRoot, ".synon", "runtime", "skills", agentSkillRuntimeDirectoryName(skill.Name), expectedDigest,
	)
	if !hostPathWithin(workspaceRoot, target) {
		return "", false
	}
	for _, preferred := range skill.PreferredExecutionAssets {
		preferred = filepath.Clean(filepath.FromSlash(strings.TrimSpace(preferred)))
		if preferred == "." || filepath.IsAbs(preferred) || preferred == ".." ||
			strings.HasPrefix(preferred, ".."+string(filepath.Separator)) {
			continue
		}
		expectedScript := filepath.Join(target, preferred)
		if scriptPath == expectedScript && hostPathWithin(target, expectedScript) {
			return target, true
		}
	}
	return "", false
}
