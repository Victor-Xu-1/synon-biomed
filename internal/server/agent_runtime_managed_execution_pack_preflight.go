package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"synon-go/internal/sciencecapability"
)

const managedExecutionOutputOwnershipMarker = ".synon-execution-pack.json"

// agentRuntimeManagedExecutionOutputMutationPreflight prevents a host file
// editor from changing an execution-pack-owned output after promotion. The
// authority is reconstructed from the exact successful execution record and
// current output digests; a workspace marker alone is never trusted.
func (g serverAgentRuntimeToolGateway) agentRuntimeManagedExecutionOutputMutationPreflight(
	ctx context.Context,
	publicName string,
	input map[string]any,
) map[string]any {
	if publicName != "edit_file" || g.server == nil || g.server.scienceCapabilities == nil ||
		g.server.workspaceStore == nil || g.kernel == nil || strings.TrimSpace(g.kernel.workspaceDir) == "" {
		return nil
	}
	root, err := filepath.Abs(g.kernel.workspaceDir)
	if err != nil {
		return nil
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil
	}
	target := strings.TrimSpace(stringValue(input["file_path"]))
	if target == "" {
		return nil
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(resolvedRoot, target)
	}
	target, err = filepath.Abs(filepath.Clean(target))
	if err != nil || !managedExecutionPathWithinRoot(resolvedRoot, target) {
		return nil
	}
	access, err := g.server.validateKernelHostIdentity(ctx, g.kernel.access)
	if err != nil {
		return managedExecutionOutputOwnershipInvalidResult(target, resolvedRoot)
	}
	records, err := g.server.workspaceStore.ListExecutionLog(access.Frame.ID, "")
	if err != nil {
		return managedExecutionOutputOwnershipInvalidResult(target, resolvedRoot)
	}
	bindings, err := g.server.workspaceStore.KernelLocalExecutionBindings(ctx, access)
	if err != nil {
		return managedExecutionOutputOwnershipInvalidResult(target, resolvedRoot)
	}
	authorities, err := g.server.managedExecutionOutputAuthorities(
		ctx, access, resolvedRoot, records, bindings,
	)
	if err != nil {
		return managedExecutionOutputOwnershipInvalidResult(target, resolvedRoot)
	}
	resolvedTarget := target
	if _, statErr := os.Lstat(target); statErr == nil {
		resolvedTarget, err = filepath.EvalSymlinks(target)
		if err != nil || !managedExecutionPathWithinRoot(resolvedRoot, resolvedTarget) {
			return managedExecutionOutputOwnershipInvalidResult(target, resolvedRoot)
		}
	} else if !os.IsNotExist(statErr) {
		return managedExecutionOutputOwnershipInvalidResult(target, resolvedRoot)
	}
	for _, authority := range authorities {
		if managedExecutionPathWithinRoot(authority.Root, resolvedTarget) {
			relative, _ := filepath.Rel(resolvedRoot, target)
			return map[string]any{
				"ok": false, "status": "managed_execution_output_immutable", "executed": false,
				"execution_pack_id": authority.PackID, "execution_id": authority.ExecutionID,
				"file_path": filepath.ToSlash(relative),
				"message":   "The target belongs to a promoted scientific execution-pack output and cannot be changed by a general file editor.",
				"recovery":  "Keep the validated quantitative output unchanged. Rerun its registered execution pack when the scientific result must change, or create a separate task-relative narrative file outside the owned output directory for translation or presentation.",
			}
		}
	}
	return nil
}

func managedExecutionPathWithinRoot(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func managedExecutionOutputOwnershipInvalidResult(target, root string) map[string]any {
	relative, _ := filepath.Rel(root, target)
	return map[string]any{
		"ok": false, "status": "managed_execution_output_ownership_invalid", "executed": false,
		"file_path": filepath.ToSlash(relative),
		"message":   "The host could not verify promoted scientific-output authority before the requested mutation.",
		"recovery":  "Do not mutate task files until the execution receipt and output digests are verified. Regenerate a stale output through its registered pack, then create presentation-only material outside the immutable output directory.",
	}
}

// agentRuntimeManagedExecutionPackPreflight enforces the canonical entrypoint
// of every registered local scientific execution pack. Its owned CLI/import
// identifiers come only from the closed capability catalog, never from Skill
// prose, task text, or implementation-specific branches in this gateway.
func (g serverAgentRuntimeToolGateway) agentRuntimeManagedExecutionPackPreflight(
	publicName string,
	input map[string]any,
) map[string]any {
	switch publicName {
	case "bash", "python", "r", "powershell":
	default:
		return nil
	}
	if g.server == nil || g.server.skillCatalog == nil || g.server.scienceCapabilities == nil || g.taskRun == nil {
		return nil
	}
	content := strings.Join([]string{
		stringValue(input["command"]), stringValue(input["code"]), stringValue(input["script"]),
	}, "\n")
	command := stringValue(input["command"])
	if strings.TrimSpace(content) == "" {
		return nil
	}
	skillNames := g.taskRun.executedSkillNamesSnapshot()
	for _, implementation := range g.taskRun.selectedImplementationsSnapshot() {
		if skill, found := dedicatedSkillForImplementation(g.server.skillCatalog, implementation); found {
			skillNames = append(skillNames, skill.Name)
		}
	}
	for _, skillName := range uniqueSortedFolded(skillNames) {
		skill, found := findCatalogSkill(g.server.skillCatalog, skillName)
		if !found {
			continue
		}
		for _, engine := range g.server.scienceCapabilities.LocalExecutionPacksForSkill(skill.Name) {
			entrypoint := engine.ExecutionPack.MaterializedSkillEntrypoint()
			if publicName == "bash" && commandExecutesManagedExecutionPack(skill.Name, engine.ExecutionPack, command) {
				if preflight := managedExecutionPackParameterEvidencePreflight(
					engine.ExecutionPack, command, g.taskRun.resolvedUserEvidenceRecordsSnapshot(),
					managedExecutionResponseLanguage(g.taskRun), g.taskRun.selectedEvidenceResolversSnapshot(),
				); preflight != nil {
					return preflight
				}
				return nil
			}
			identifiers := engine.ManagedExecutionIdentifiers()
			identifier, matched := managedExecutionIdentifier(publicName, content, identifiers)
			sourcePath := ""
			if !matched {
				identifier, sourcePath, matched = managedExecutionSourceIdentifier(content, identifiers, g.kernel)
			}
			if !matched {
				continue
			}
			result := map[string]any{
				"ok": false, "status": "skill_execution_entrypoint_required", "executed": false,
				"skill": skill.Name, "execution_pack_id": engine.ExecutionPack.ID,
				"matched_identifier": identifier, "required_entrypoint": entrypoint,
				"message":  "The scientific capability registry assigns this implementation to one reviewed execution-pack entrypoint; a competing direct command was rejected before process start.",
				"recovery": "Execute the exact materialized execution-pack entrypoint once with the task inputs and registered arguments. Do not invoke its implementation CLI, preparation utilities, imports, splitters, or report writers through a competing path.",
			}
			if sourcePath != "" {
				result["competing_source"] = sourcePath
			}
			return result
		}
	}
	return nil
}

// normalizeManagedExecutionRuntimeArguments adds server-owned execution
// parameters declared by the capability registry. The model never chooses or
// copies these values: the canonical task language is resolved once by the
// runner and becomes the same audited argument consumed by every compatible
// execution pack.
func (g serverAgentRuntimeToolGateway) normalizeManagedExecutionRuntimeArguments(
	publicName string,
	input map[string]any,
) map[string]any {
	if publicName != "bash" || g.server == nil || g.server.skillCatalog == nil ||
		g.server.scienceCapabilities == nil || g.taskRun == nil {
		return input
	}
	command := strings.TrimSpace(stringValue(input["command"]))
	if command == "" {
		return input
	}
	skillNames := g.taskRun.executedSkillNamesSnapshot()
	for _, implementation := range g.taskRun.selectedImplementationsSnapshot() {
		if skill, found := dedicatedSkillForImplementation(g.server.skillCatalog, implementation); found {
			skillNames = append(skillNames, skill.Name)
		}
	}
	for _, skillName := range uniqueSortedFolded(skillNames) {
		skill, found := findCatalogSkill(g.server.skillCatalog, skillName)
		if !found {
			continue
		}
		for _, engine := range g.server.scienceCapabilities.LocalExecutionPacksForSkill(skill.Name) {
			pack := engine.ExecutionPack
			if !commandExecutesManagedExecutionPack(skill.Name, pack, command) {
				continue
			}
			values := managedExecutionArgumentValues(command)
			normalizedCommand := command
			for _, parameter := range pack.Parameters {
				switch parameter.Evidence {
				case "runtime-response-language":
					if _, present := values[parameter.Argument]; !present {
						normalizedCommand += " " + parameter.Argument + " " + managedExecutionResponseLanguage(g.taskRun)
					}
				case "selected-evidence-resolver":
					if _, present := values[parameter.Argument]; present {
						continue
					}
					if selected, found := selectedEvidenceResolverParameterValue(
						pack, g.taskRun.selectedEvidenceResolversSnapshot(),
					); found {
						normalizedCommand += " " + parameter.Argument + " " + selected
					}
				}
			}
			if normalizedCommand == command {
				return input
			}
			normalized := copyMapAny(input)
			normalized["command"] = normalizedCommand
			return normalized
		}
	}
	return input
}

func managedExecutionResponseLanguage(run *sessionRunnerChatRun) string {
	if run != nil {
		if sessionRunnerRequiresChinese(run.ResponseLanguage) {
			return "zh"
		}
		if sessionRunnerResponseLanguage(run.TaskIntent) == "zh" {
			return "zh"
		}
	}
	return "en"
}

func managedExecutionSourceIdentifier(
	content string,
	identifiers []string,
	kernel *agentKernelContext,
) (string, string, bool) {
	if kernel == nil || strings.TrimSpace(kernel.workspaceDir) == "" {
		return "", "", false
	}
	root, err := filepath.Abs(kernel.workspaceDir)
	if err != nil {
		return "", "", false
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", false
	}
	allowedExtensions := map[string]bool{
		".py": true, ".r": true, ".sh": true, ".bash": true, ".zsh": true,
		".ps1": true, ".pl": true, ".rb": true, ".js": true, ".mjs": true, ".cjs": true,
	}
	seen := map[string]bool{}
	for _, token := range managedExecutionCommandTokens(content) {
		token = strings.TrimSpace(token)
		if !allowedExtensions[strings.ToLower(filepath.Ext(token))] {
			continue
		}
		path := token
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		path, err = filepath.Abs(filepath.Clean(path))
		if err != nil || seen[path] {
			continue
		}
		seen[path] = true
		resolvedPath, err := filepath.EvalSymlinks(path)
		if err != nil {
			continue
		}
		relative, err := filepath.Rel(resolvedRoot, resolvedPath)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		info, err := os.Stat(resolvedPath)
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 2<<20 {
			continue
		}
		raw, err := os.ReadFile(resolvedPath)
		if err != nil {
			continue
		}
		if identifier, matched := managedExecutionIdentifier(managedExecutionSourceLanguage(resolvedPath), string(raw), identifiers); matched {
			return identifier, filepath.ToSlash(relative), true
		}
	}
	return "", "", false
}

func commandExecutesManagedExecutionPack(skillName string, pack sciencecapability.ExecutionPack, content string) bool {
	tokens, ok := managedExecutionSingleShellCommandTokens(content)
	if !ok || !agentRuntimeCommandIsSingleMaterializedSkillExecution("bash", content) {
		return false
	}
	entrypoint := pack.MaterializedSkillEntrypoint()
	if entrypoint == "" {
		return false
	}
	pathIndex := -1
	for index, token := range tokens {
		path := filepath.ToSlash(filepath.Clean(token))
		if skillRuntimeCommandReferences(skillName, path) && strings.HasSuffix(path, "/"+entrypoint) {
			if pathIndex >= 0 {
				return false
			}
			pathIndex = index
		}
	}
	// The registered script must be the executable's immediate operand. Even a
	// dash-prefixed token can change interpreter startup semantics (for example,
	// attached code/module options) and must not inherit pack authority.
	if pathIndex != 1 || !strings.EqualFold(filepath.Base(tokens[0]), pack.Executable) {
		return false
	}
	return true
}
