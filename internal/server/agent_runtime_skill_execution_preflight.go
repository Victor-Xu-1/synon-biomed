package server

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"synon-go/internal/executionprep"
	kernelruntime "synon-go/internal/kernel"
	"synon-go/internal/skills"
)

// agentRuntimeSkillExecutionContractPreflight composes the machine-readable
// Skill, environment, and scientific execution-pack boundaries for a task.
func (g serverAgentRuntimeToolGateway) agentRuntimeSkillExecutionContractPreflight(
	publicName string,
	input map[string]any,
	parents ...context.Context,
) map[string]any {
	if g.server == nil || g.server.skillCatalog == nil || g.taskRun == nil {
		return nil
	}
	name := strings.ToLower(strings.TrimSpace(publicName))
	if preflight := g.agentRuntimeBuiltinAttachmentReaderPreflight(name, input); preflight != nil {
		return preflight
	}
	if preflight := g.agentRuntimeSelectedImplementationSkillPreflight(name, input); preflight != nil {
		return preflight
	}
	if preflight := g.agentRuntimeImplementationProvisioningSkillPreflight(name, input); preflight != nil {
		return preflight
	}
	if preflight := g.agentRuntimeImplementationExecutionChoicePreflight(name, input, parents...); preflight != nil {
		return preflight
	}
	if preflight := g.agentRuntimeSelectedImplementationEnvironmentPreflight(name, input); preflight != nil {
		return preflight
	}
	if preflight := g.agentRuntimeSelectedImplementationMaterializedSkillPreflight(name, input); preflight != nil {
		return preflight
	}
	if preflight := g.agentRuntimeCanonicalPackEnvironmentPreflight(name, input, parents...); preflight != nil {
		return preflight
	}
	if preflight := g.agentRuntimeControlledEvidenceDerivationPreflight(name, input); preflight != nil {
		return preflight
	}
	if preflight := g.agentRuntimeManagedExecutionPackPreflight(name, input); preflight != nil {
		return preflight
	}
	if preflight := g.agentRuntimeSkillOwnedPackageMutationPreflight(name, input); preflight != nil {
		return preflight
	}
	switch name {
	case "bash", "python", "r", "repl", "powershell":
	default:
		return nil
	}
	content := strings.ToLower(strings.Join([]string{
		stringValue(input["command"]), stringValue(input["code"]), stringValue(input["script"]),
	}, "\n"))
	if strings.TrimSpace(content) == "" {
		return nil
	}
	if preflight := agentRuntimeMaterializedSkillScriptPreflight(name, input, g.kernel); preflight != nil {
		return preflight
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
		// REPL is the stdlib-only control plane and never executes the Skill's
		// scientific runtime. It may inspect the materialized bundle or invoke
		// governed host methods without inheriting the Skill package contract.
		// The contract belongs only to the managed execution environments.
		if name != "repl" && len(skill.RequiredEnvironmentPackages) > 0 &&
			skillRuntimeCommandReferences(skill.Name, content) {
			if preflight := g.agentRuntimeSkillEnvironmentPreflight(skill, input); preflight != nil {
				return preflight
			}
		}
	}
	return nil
}

func (g serverAgentRuntimeToolGateway) agentRuntimeCanonicalPackEnvironmentPreflight(
	publicName string,
	input map[string]any,
	parents ...context.Context,
) map[string]any {
	if g.server == nil || g.server.kernelManager == nil {
		return nil
	}
	engine, found := g.server.canonicalManagedExecutionPack(publicName, input)
	if !found {
		return nil
	}
	pack := engine.ExecutionPack
	condaPackages, pipPackages, err := registeredExecutionPackPackageSpecs(pack)
	if err != nil {
		return map[string]any{
			"ok": false, "status": "execution_pack_environment_contract_invalid", "executed": false,
			"execution_pack_id": pack.ID,
			"message":           "The registered execution pack has an invalid environment contract.",
			"recovery":          "Repair the registered execution-pack environment contract before retrying the same entrypoint.",
		}
	}
	packageSpecs := append([]string(nil), condaPackages...)
	for _, spec := range pipPackages {
		packageSpecs = append(packageSpecs, "pip::"+spec)
	}
	dependencies, _ := managedEnvironmentPreflightDependencyNames(packageSpecs)
	requestedEnvironment := strings.TrimSpace(stringValue(input["environment"]))
	parent := context.Background()
	if len(parents) > 0 && parents[0] != nil {
		parent = parents[0]
	}
	// Inventory can contain hundreds of immutable generations. A full strict
	// health scan is intentionally not part of this admission gate: it makes a
	// ready, explicitly requested environment lose a short deadline while
	// unrelated environments are inspected. The execution boundary performs
	// the final generation-health check immediately before process start.
	const discoveryTimeout = 30 * time.Second
	ctx, cancel := context.WithTimeout(parent, discoveryTimeout)
	defer cancel()
	candidates, listErr := g.server.kernelManager.ListManagedEnvironments(ctx, kernelruntime.ManagedEnvironmentQuery{
		Name:            requestedEnvironment,
		Language:        strings.TrimSpace(pack.Language),
		Dependencies:    dependencies,
		IncludePackages: true,
		SkipHealth:      true,
	})
	if listErr == nil {
		rankManagedEnvironmentPreflightCandidates(candidates, requestedEnvironment)
		for _, candidate := range candidates[:min(len(candidates), maxManagedEnvironmentPreflightAlternatives)] {
			if err := g.server.kernelManager.VerifyManagedEnvironmentExecutable(candidate.Name, pack.Executable); err != nil {
				continue
			}
			witnessesValid := true
			for _, witness := range pack.CLIWitnesses {
				if err := g.server.kernelManager.VerifyManagedEnvironmentExecutable(candidate.Name, witness.Executable); err != nil {
					witnessesValid = false
					break
				}
			}
			if !witnessesValid {
				continue
			}
			if err := g.server.kernelManager.VerifyManagedEnvironmentImports(ctx, candidate.Name, pack.Imports); err != nil {
				continue
			}
			input["environment"] = candidate.Name
			return nil
		}
	}
	return map[string]any{
		"ok": false, "status": "execution_pack_environment_preflight_required", "executed": false,
		"execution_pack_id": pack.ID, "requested_environment": requestedEnvironment,
		"required_packages": packageSpecs, "required_imports": append([]string(nil), pack.Imports...),
		"message":  "No ready managed environment satisfies the registered execution pack's complete package and import contract.",
		"recovery": "Use manage_environments preflight with the same implementation. Reuse the returned compatible environment, or create one immutable environment from the registered package contract before retrying this exact entrypoint.",
	}
}

// agentRuntimeImplementationExecutionChoicePreflight prevents a historical
// managed environment from silently choosing a substantial scientific engine
// for a new task. The gate is capability- and metadata-driven: generic Skill
// assets may prepare inputs before the choice, while arbitrary engine code must
// follow the current task's exact AskUser selection and dedicated Skill.
func (g serverAgentRuntimeToolGateway) agentRuntimeImplementationExecutionChoicePreflight(
	publicName string,
	input map[string]any,
	parents ...context.Context,
) map[string]any {
	switch publicName {
	case "bash", "python", "r", "powershell":
	default:
		return nil
	}
	if g.taskRun == nil {
		return nil
	}
	if g.server != nil {
		if _, found := g.server.canonicalManagedExecutionPack(publicName, input); found {
			return nil
		}
	}
	capabilities := semanticManagedEnvironmentCapabilities(g.taskRun.requiredScientificCapabilitiesSnapshot())
	if len(capabilities) == 0 {
		return nil
	}
	parent := context.Background()
	if len(parents) > 0 && parents[0] != nil {
		parent = parents[0]
	}
	if g.agentRuntimeDiagnosticObservation(parent, publicName, input) != nil {
		// This only prepares a conditional exemption. The host must carry its
		// binding obligations to the real executor; durable choice is unchanged.
		return nil
	}
	selected := g.taskRun.selectedImplementationsSnapshot()
	if len(selected) == 0 {
		if g.agentRuntimeCommandIsTrustedGenericMaterializedSkillExecution(publicName, input) {
			// Input preparation does not answer the still-pending implementation
			// choice. Retain that durable state so a later engine call remains
			// gated even though this exact generic Skill entrypoint may run.
			g.taskRun.setImplementationSelectionRequired(true)
			return nil
		}
		if dedicated, found := g.taskExplicitDedicatedImplementationSkill(); found {
			if g.taskRunHasExecutedSkill(dedicated.Name) {
				return nil
			}
			return implementationExecutionSkillRequiredResult(dedicated, nil)
		}
		g.taskRun.setImplementationSelectionRequired(true)
		return map[string]any{
			"ok": true, "status": "implementation_selection_required", "executed": false,
			"decision_required": true, "required_capabilities": append([]string(nil), capabilities...),
			"recovery": "Use the loaded capability-routing Skill and current machine/environment inventory to preflight viable concrete implementations, then ask the user to select one. Reuse is allowed only after this task owns that exact selection; an old environment label is not implementation authority. Do not infer or execute an engine from a module name, environment name, repository name, target, or prior task.",
		}
	}
	if g.server == nil || g.server.skillCatalog == nil {
		return nil
	}
	for _, implementation := range selected {
		dedicated, found := dedicatedSkillForImplementation(g.server.skillCatalog, implementation)
		if !found || g.taskRunHasExecutedSkill(dedicated.Name) {
			continue
		}
		return implementationExecutionSkillRequiredResult(dedicated, selected)
	}
	return nil
}

// Only a positive, full-source effect contract can prepare this exemption. The
// execution gateway obtains it again from the final arguments after hooks and
// approval; no model field or previously prepared source can supply it.
func (g serverAgentRuntimeToolGateway) agentRuntimeDiagnosticObservation(parent context.Context, language string, input map[string]any) *executionprep.Observation {
	if g.server == nil || g.server.kernelManager == nil || g.taskRun == nil ||
		len(semanticManagedEnvironmentCapabilities(g.taskRun.requiredScientificCapabilitiesSnapshot())) == 0 {
		return nil
	}
	// Do not impose a diagnostic obligation on ordinarily authorized code after
	// the selection and dedicated Skill contracts have already been satisfied.
	selected := g.taskRun.selectedImplementationsSnapshot()
	pending := len(selected) == 0
	if !pending && g.server.skillCatalog != nil {
		for _, implementation := range selected {
			if skill, found := dedicatedSkillForImplementation(g.server.skillCatalog, implementation); found && !g.taskRunHasExecutedSkill(skill.Name) {
				pending = true
				break
			}
		}
	}
	if !pending {
		return nil
	}
	switch language {
	case "python", "r", "bash", "powershell":
	default:
		return nil
	}
	if _, found := g.server.canonicalManagedExecutionPack(language, input); found {
		return nil
	}
	if len(selected) == 0 {
		if dedicated, found := g.taskExplicitDedicatedImplementationSkill(); found && g.taskRunHasExecutedSkill(dedicated.Name) {
			return nil
		}
	}
	source := stringValue(input["code"])
	if language == "bash" || language == "powershell" {
		source = stringValue(input["command"])
	}
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	result, err := g.server.kernelManager.PrepareExecutionSource(ctx, executionprep.Request{
		Language: language, Source: source, Environment: stringValue(input["environment"]),
	})
	if err != nil || !result.Observation.Matches(language, source) {
		return nil
	}
	return result.Observation
}

func (g serverAgentRuntimeToolGateway) taskExplicitDedicatedImplementationSkill() (skills.Skill, bool) {
	if g.server == nil || g.server.skillCatalog == nil || g.taskRun == nil {
		return skills.Skill{}, false
	}
	for _, skill := range g.server.skillCatalog.Skills() {
		for _, identity := range skill.ImplementationIdentities {
			if taskExplicitlyNamesImplementation(g.taskRun.TaskIntent, identity) {
				return skill, true
			}
		}
	}
	return skills.Skill{}, false
}

func (g serverAgentRuntimeToolGateway) taskRunHasExecutedSkill(name string) bool {
	for _, loaded := range g.taskRun.executedSkillNamesSnapshot() {
		if strings.EqualFold(strings.TrimSpace(loaded), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

func (g serverAgentRuntimeToolGateway) pendingSelectedEvidenceResolverSkillNames() []string {
	if g.taskRun == nil {
		return nil
	}
	pending := make([]string, 0)
	for _, resolver := range g.taskRun.selectedEvidenceResolversSnapshot() {
		if resolver.Skill != "" && !g.taskRunHasExecutedSkill(resolver.Skill) {
			pending = append(pending, resolver.Skill)
		}
	}
	return uniqueSortedFolded(pending)
}

func (run *sessionRunnerChatRun) evidenceResolverAuthorizesSkill(skill skills.Skill) bool {
	if run == nil {
		return false
	}
	for _, resolver := range run.selectedEvidenceResolversSnapshot() {
		if !strings.EqualFold(strings.TrimSpace(resolver.Skill), strings.TrimSpace(skill.Name)) {
			continue
		}
		for _, identity := range skill.ImplementationIdentities {
			if askUserImplementationIdentityMatches(identity, resolver.Implementation) {
				return true
			}
		}
	}
	return false
}

func (run *sessionRunnerChatRun) evidenceResolverAuthorizesImplementation(implementation string) bool {
	if run == nil || strings.TrimSpace(implementation) == "" {
		return false
	}
	for _, resolver := range run.selectedEvidenceResolversSnapshot() {
		if taskImplementationMatchesRegistered(implementation, resolver.Implementation) {
			return true
		}
	}
	return false
}

func implementationExecutionSkillRequiredResult(skill skills.Skill, selected []string) map[string]any {
	result := map[string]any{
		"ok": true, "status": "implementation_skill_required", "executed": false,
		"decision_required":         false,
		"required_skill":            skill.Name,
		"implementation_identities": append([]string(nil), skill.ImplementationIdentities...),
		"recovery":                  "Load the exact dedicated Skill, then execute the selected implementation through its reviewed entrypoint, source, checkpoint, environment, recovery, and representative-run contract. Do not guess a module, executable, or command from the implementation or environment name.",
	}
	if len(selected) > 0 {
		result["selected_implementations"] = append([]string(nil), selected...)
	}
	return result
}

func agentRuntimeCommandIsSingleMaterializedSkillExecution(publicName, content string) bool {
	_, _, _, ok := agentRuntimeSingleMaterializedSkillExecution(publicName, content)
	return ok
}

func agentRuntimeSingleMaterializedSkillExecution(
	publicName string,
	content string,
) ([]string, string, int, bool) {
	if publicName != "bash" {
		return nil, "", -1, false
	}
	tokens, ok := managedExecutionSingleShellCommandTokens(content)
	if !ok {
		return nil, "", -1, false
	}
	scriptPath := ""
	scriptIndex := -1
	for index, token := range tokens {
		match := materializedSkillScriptPattern.FindStringSubmatch(token)
		if len(match) == 2 && match[1] == token {
			if scriptIndex >= 0 {
				return nil, "", -1, false
			}
			scriptPath = match[1]
			scriptIndex = index
		}
	}
	return tokens, scriptPath, scriptIndex, scriptIndex >= 0
}

// Admission and execution identity share the grammar-based preparation parser.
func managedExecutionSingleShellCommandTokens(content string) ([]string, bool) {
	return executionprep.SingleShellCommand(content)
}

// agentRuntimeImplementationProvisioningSkillPreflight prevents a model from
// guessing an environment matrix from an implementation display name after a
// user has made a material choice. Read-only discovery and resource preflight
// remain available before the choice; mutation must first load the reviewed
// dedicated Skill when one exists. Implementations without a dedicated Skill
// continue through the generic capability-acquisition path.
func (g serverAgentRuntimeToolGateway) agentRuntimeImplementationProvisioningSkillPreflight(
	publicName string,
	input map[string]any,
) map[string]any {
	if g.server == nil || g.server.skillCatalog == nil || g.taskRun == nil {
		return nil
	}
	mode := strings.ToLower(strings.TrimSpace(stringValue(input["mode"])))
	switch publicName {
	case "manage_environments":
		if mode != "create" {
			return nil
		}
	case "manage_packages":
		if mode != "install" {
			return nil
		}
	default:
		return nil
	}
	implementation := strings.TrimSpace(stringValue(input["implementation"]))
	if implementation == "" || len(g.taskRun.selectedImplementationsSnapshot()) == 0 {
		return nil
	}
	dedicated, found := dedicatedSkillForImplementation(g.server.skillCatalog, implementation)
	if !found {
		return nil
	}
	for _, loaded := range g.taskRun.executedSkillNamesSnapshot() {
		if strings.EqualFold(strings.TrimSpace(loaded), dedicated.Name) {
			return nil
		}
	}
	return map[string]any{
		"ok": true, "status": "implementation_skill_required", "executed": false,
		"decision_required":         false,
		"implementation":            implementation,
		"required_skill":            dedicated.Name,
		"implementation_identities": append([]string(nil), dedicated.ImplementationIdentities...),
		"recovery":                  "Load the exact dedicated Skill with the skill tool, then retry the same selected implementation using its reviewed source, dependency, execution, and validation contract. Do not guess a package matrix from the implementation name and do not change implementations.",
	}
}

func dedicatedSkillForImplementation(catalog *skills.Catalog, implementation string) (skills.Skill, bool) {
	if catalog == nil {
		return skills.Skill{}, false
	}
	implementation = strings.TrimSpace(implementation)
	if implementation == "" {
		return skills.Skill{}, false
	}
	candidates := make([]skills.Skill, 0, 1)
	for _, skill := range catalog.Skills() {
		matched := strings.EqualFold(strings.TrimSpace(skill.Name), implementation)
		for _, identity := range skill.ImplementationIdentities {
			matched = matched || askUserImplementationIdentityMatches(identity, implementation)
		}
		if matched && len(skill.ImplementationIdentities) > 0 {
			candidates = append(candidates, skill)
		}
	}
	if len(candidates) == 0 {
		return skills.Skill{}, false
	}
	sort.Slice(candidates, func(left, right int) bool {
		leftExact := strings.EqualFold(candidates[left].Name, implementation)
		rightExact := strings.EqualFold(candidates[right].Name, implementation)
		if leftExact != rightExact {
			return leftExact
		}
		return candidates[left].Name < candidates[right].Name
	})
	return candidates[0], true
}

func skillSupportsSelectedImplementation(skill skills.Skill, selected []string) bool {
	if len(skill.ImplementationIdentities) == 0 || len(selected) == 0 {
		return true
	}
	for _, identity := range skill.ImplementationIdentities {
		for _, current := range selected {
			if askUserImplementationIdentityMatches(current, identity) {
				return true
			}
		}
	}
	return false
}

func skillAuthorizedByImplementationState(skill skills.Skill, selected []string, taskIntent string) bool {
	if len(skill.ImplementationIdentities) == 0 {
		return true
	}
	if len(selected) > 0 {
		return skillSupportsSelectedImplementation(skill, selected)
	}
	for _, identity := range skill.ImplementationIdentities {
		if taskExplicitlyNamesImplementation(taskIntent, identity) {
			return true
		}
	}
	return false
}

func runtimeSkillsForSelectedImplementation(candidates []skills.Skill, selected []string, taskIntent string) []skills.Skill {
	if len(candidates) == 0 {
		return candidates
	}
	filtered := make([]skills.Skill, 0, len(candidates))
	for _, skill := range candidates {
		if skillAuthorizedByImplementationState(skill, selected, taskIntent) {
			filtered = append(filtered, skill)
		}
	}
	return filtered
}

// agentRuntimeSelectedImplementationEnvironmentPreflight prevents an older
// implementation-specific environment from becoming a silent fallback after
// AskUser selected a different engine. Associations are learned from durable
// successful manage_environments/manage_packages calls rather than inferred
// from environment names, package names, targets, or task wording.
func (g serverAgentRuntimeToolGateway) agentRuntimeSelectedImplementationEnvironmentPreflight(
	publicName string,
	input map[string]any,
) map[string]any {
	switch publicName {
	case "bash", "python", "r":
	default:
		return nil
	}
	if g.taskRun == nil {
		return nil
	}
	selected := g.taskRun.selectedImplementationsSnapshot()
	if len(selected) == 0 {
		return nil
	}
	environment := strings.TrimSpace(stringValue(input["environment"]))
	bound := g.taskRun.managedEnvironmentImplementationsSnapshot(environment)
	if len(bound) == 0 {
		return nil
	}
	for _, identity := range bound {
		for _, current := range selected {
			if askUserImplementationIdentityMatches(current, identity) {
				return nil
			}
		}
		if g.taskRun.evidenceResolverAuthorizesImplementation(identity) {
			return nil
		}
	}
	return map[string]any{
		"ok": false, "status": "selected_implementation_environment_mismatch", "executed": false,
		"message":                     "The requested managed environment belongs to a different scientific implementation than the user's latest answered choice.",
		"environment":                 environment,
		"environment_implementations": append([]string(nil), bound...),
		"selected_implementations":    append([]string(nil), selected...),
		"recovery":                    "Keep the latest selected implementation. Reuse or repair a managed environment that was provisioned for that implementation, preserving completed source and downloads. Do not execute through an older implementation-specific environment unless a later server-admitted user decision changes the implementation.",
	}
}

// agentRuntimeSelectedImplementationMaterializedSkillPreflight closes the
// execution-time half of Skill authority. A stale provider replay may still
// contain an old immutable script path, so checking only new Skill tool calls
// is insufficient. The materialized directory identity is generated by the
// Harness itself and is matched back to declarative Skill metadata.
func (g serverAgentRuntimeToolGateway) agentRuntimeSelectedImplementationMaterializedSkillPreflight(
	publicName string,
	input map[string]any,
) map[string]any {
	if publicName != "bash" || g.server == nil || g.server.skillCatalog == nil || g.taskRun == nil {
		return nil
	}
	selected := g.taskRun.selectedImplementationsSnapshot()
	if len(selected) == 0 {
		return nil
	}
	command := strings.ReplaceAll(stringValue(input["command"]), "\\\n", " ")
	matches := materializedSkillScriptPattern.FindAllStringSubmatch(command, -1)
	if len(matches) == 0 {
		return nil
	}
	for _, match := range matches {
		if len(match) != 2 {
			continue
		}
		parts := strings.Split(filepath.ToSlash(match[1]), "/")
		for index := 0; index+1 < len(parts); index++ {
			if parts[index] != "skills" {
				continue
			}
			directory := parts[index+1]
			for _, skill := range g.server.skillCatalog.Skills() {
				if directory != agentSkillRuntimeDirectoryName(skill.Name) ||
					skillSupportsSelectedImplementation(skill, selected) ||
					g.taskRun.evidenceResolverAuthorizesSkill(skill) {
					continue
				}
				return map[string]any{
					"ok": false, "status": "selected_implementation_skill_mismatch", "executed": false,
					"message":                  "The requested materialized Skill asset belongs to a different scientific implementation than the user's latest answered choice.",
					"skill":                    skill.Name,
					"skill_implementations":    append([]string(nil), skill.ImplementationIdentities...),
					"selected_implementations": append([]string(nil), selected...),
					"recovery":                 "Keep the latest selected implementation. Use its current matching Skill asset when available, or continue through an implementation-agnostic recovery workflow. Do not execute a stale asset from another engine.",
				}
			}
		}
	}
	return nil
}

// agentRuntimeSelectedImplementationSkillPreflight makes exact implementation
// ownership declarative. A dedicated Skill lists its public implementation
// identities in frontmatter; the runtime compares those values with the latest
// durable AskUser answer without containing engine-specific routing code.
// Workflow, evidence, and recovery Skills omit the field and remain usable.
func (g serverAgentRuntimeToolGateway) agentRuntimeSelectedImplementationSkillPreflight(
	publicName string,
	input map[string]any,
) map[string]any {
	if publicName != "skill" || g.server == nil || g.server.skillCatalog == nil || g.taskRun == nil {
		return nil
	}
	requested := strings.TrimSpace(stringValue(input["skill"]))
	pending := g.taskRun.pendingRequiredSkillNamesSnapshot()
	if len(pending) > 0 && !pendingSkillNamesContain(pending, requested) {
		return map[string]any{
			"ok": false, "status": "required_skill_load_pending", "executed": false,
			"message":         "This Skill does not satisfy the exact pending Skill requirements from the latest implementation-choice validation.",
			"required_skills": pending,
			"recovery":        "Load one of the exact required_skills now. Do not substitute an already-loaded workflow or a different implementation Skill.",
		}
	}
	skill, found := findCatalogSkill(g.server.skillCatalog, requested)
	if !found || len(skill.ImplementationIdentities) == 0 {
		return nil
	}
	if g.taskRun.evidenceResolverAuthorizesSkill(skill) {
		return nil
	}
	// The latest proposal can require inspecting an alternative's contract.
	// This permits only the exact pending read; it does not change selection
	// or authorize that alternative's provisioning, scripts, or execution.
	if pendingSkillNamesContain(pending, requested) {
		return nil
	}
	selected := g.taskRun.selectedImplementationsSnapshot()
	if len(selected) == 0 {
		for _, identity := range skill.ImplementationIdentities {
			if taskExplicitlyNamesImplementation(g.taskRun.TaskIntent, identity) {
				return nil
			}
		}
		capabilities := semanticManagedEnvironmentCapabilities(append(
			g.taskRun.requiredScientificCapabilitiesSnapshot(), skill.RequiredCapabilities...,
		))
		if len(capabilities) == 0 {
			return nil
		}
		// Loading a Skill is a read-only planning action, not implementation
		// authority. Allow the model to inspect dedicated contracts while it
		// prepares a truthful AskUser choice; execution and provisioning remain
		// blocked until an exact user selection is durably recorded.
		g.taskRun.setImplementationSelectionRequired(true)
		return nil
	}
	if skillSupportsSelectedImplementation(skill, selected) {
		return nil
	}
	return map[string]any{
		"ok": false, "status": "selected_implementation_skill_mismatch", "executed": false,
		"message":                  "The requested dedicated Skill belongs to a different scientific implementation than the user's latest answered choice.",
		"selected_implementations": append([]string(nil), selected...),
		"skill_implementations":    append([]string(nil), skill.ImplementationIdentities...),
		"recovery":                 "Keep the latest selected implementation. Load its matching dedicated Skill when available, or use an implementation-agnostic workflow/recovery Skill to repair the same software. Do not load or execute a dedicated Skill for another engine until a server-admitted user decision changes the implementation.",
	}
}

func pendingSkillNamesContain(names []string, requested string) bool {
	for _, name := range names {
		if strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(requested)) {
			return true
		}
	}
	return false
}

var (
	materializedSkillScriptPattern = regexp.MustCompile(`(?i)(/[^\s"';&|]*/\.synon/runtime/skills/[^\s"';&|]+/scripts/[^\s"';&|]+)`)
	pythonArgumentPattern          = regexp.MustCompile(`(?m)add_argument\s*\(\s*(?:f)?["'](--[a-zA-Z0-9][a-zA-Z0-9_-]*(?:\{[a-zA-Z_][a-zA-Z0-9_]*\})?)["']`)
	pythonArgumentValuePattern     = regexp.MustCompile(`--[a-zA-Z][a-zA-Z0-9-]*`)
	pythonArgumentLoopPattern      = regexp.MustCompile(`(?m)for\s+([a-zA-Z_][a-zA-Z0-9_]*)\s+in\s+\(([^)]*)\)`)
	pythonLoopValuePattern         = regexp.MustCompile(`["']([a-zA-Z0-9_-]+)["']`)
	pythonPathArgumentPattern      = regexp.MustCompile(`(?m)add_argument\s*\(\s*["'](--[a-zA-Z0-9][a-zA-Z0-9_-]*)["'][^\n]*type\s*=\s*Path[^\n]*required\s*=\s*True`)
	pythonDirectInputPattern       = regexp.MustCompile(`(?m)(?:read_[a-zA-Z0-9_]+|load_[a-zA-Z0-9_]+|sha256_file)\s*\(\s*args\.([a-zA-Z_][a-zA-Z0-9_]*)`)
	pythonResolvedPathPattern      = regexp.MustCompile(`(?m)^\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*=\s*\([^\n]*args\.([a-zA-Z_][a-zA-Z0-9_]*)[^\n]*\)\.resolve\(\)`)
)

const maxMaterializedSkillScriptBytes = 1 << 20

// agentRuntimeMaterializedSkillScriptPreflight validates the executable
// contract already materialized by Skill. Claude-style runtimes correct an
// invalid tool call before publishing it; they do not start a process merely
// to discover that the entrypoint, flag, or declared input never existed.
// The checks are derived from the selected script itself rather than from task
// keywords, so they protect every domain without creating a competing route.
func agentRuntimeMaterializedSkillScriptPreflight(
	publicName string,
	input map[string]any,
	identity *agentKernelContext,
) map[string]any {
	if publicName != "bash" || identity == nil || strings.TrimSpace(identity.workspaceDir) == "" {
		return nil
	}
	command := strings.ReplaceAll(stringValue(input["command"]), "\\\n", " ")
	matches := materializedSkillScriptPattern.FindAllStringSubmatch(command, -1)
	if len(matches) == 0 {
		return nil
	}
	workspace := filepath.Clean(identity.workspaceDir)
	workingDirectory := workspace
	if requested := strings.TrimSpace(stringValue(input["working_dir"])); requested != "" {
		if filepath.IsAbs(requested) {
			workingDirectory = filepath.Clean(requested)
		} else {
			workingDirectory = filepath.Clean(filepath.Join(workspace, filepath.FromSlash(requested)))
		}
	}
	if !hostPathWithin(workspace, workingDirectory) {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		if len(match) != 2 {
			continue
		}
		scriptPath := filepath.Clean(match[1])
		if _, duplicate := seen[scriptPath]; duplicate {
			continue
		}
		seen[scriptPath] = struct{}{}
		if !hostPathWithin(workspace, scriptPath) || !strings.Contains(filepath.ToSlash(scriptPath), "/.synon/runtime/skills/") {
			return materializedSkillEntrypointDiagnostic(scriptPath)
		}
		info, err := os.Lstat(scriptPath)
		if err != nil || info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return materializedSkillEntrypointDiagnostic(scriptPath)
		}
		if info.Size() <= 0 || info.Size() > maxMaterializedSkillScriptBytes || !strings.EqualFold(filepath.Ext(scriptPath), ".py") {
			continue
		}
		raw, err := os.ReadFile(scriptPath)
		if err != nil {
			continue
		}
		source := string(raw)
		allowedFlags := pythonScriptArgumentFlags(source)
		if len(allowedFlags) > 0 {
			unknown := unknownPythonScriptFlags(command, scriptPath, allowedFlags)
			if len(unknown) > 0 {
				available := sortedStringSet(allowedFlags)
				return map[string]any{
					"ok": false, "status": "skill_arguments_preflight_required", "executed": false,
					"message":  "The selected Skill entrypoint does not advertise one or more requested arguments: " + strings.Join(unknown, ", ") + ".",
					"recovery": "Use the exact entrypoint once with --help, then issue one corrected call using only its advertised arguments. Available arguments: " + strings.Join(available, ", ") + ". Do not write a wrapper or split a supported multi-record input into an ad-hoc loop.",
				}
			}
		}
		missing := missingPythonScriptInputs(source, command, scriptPath, workingDirectory, workspace)
		if len(missing) > 0 {
			return map[string]any{
				"ok": false, "status": "skill_input_preflight_required", "executed": false,
				"message":  "The selected Skill entrypoint requires task inputs that do not yet exist: " + strings.Join(missing, ", ") + ".",
				"recovery": "Complete or restore the upstream step that produces these exact inputs, then invoke the same documented Skill entrypoint once. Do not run a downstream assembler early, invent placeholder inputs, or guess a second script path.",
			}
		}
	}
	return nil
}

func materializedSkillEntrypointDiagnostic(scriptPath string) map[string]any {
	scriptsDirectory := filepath.Dir(scriptPath)
	available := make([]string, 0, 8)
	if entries, err := os.ReadDir(scriptsDirectory); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			available = append(available, entry.Name())
			if len(available) >= 16 {
				break
			}
		}
	}
	sort.Strings(available)
	recovery := "Load the exact delegated Skill, use its materialized preferred execution asset, inspect that asset with --help, and then issue one corrected call. Do not guess a script name inside the current Skill."
	if len(available) > 0 {
		recovery += " Scripts present in the selected Skill: " + strings.Join(available, ", ") + "."
	}
	return map[string]any{
		"ok": false, "status": "skill_entrypoint_preflight_required", "executed": false,
		"message":  "The requested task-scoped Skill entrypoint does not exist and was rejected before process start.",
		"recovery": recovery,
	}
}

func pythonScriptArgumentFlags(source string) map[string]struct{} {
	flags := map[string]struct{}{"--help": {}}
	loops := make(map[string][]string)
	for _, match := range pythonArgumentLoopPattern.FindAllStringSubmatch(source, -1) {
		if len(match) != 3 {
			continue
		}
		for _, value := range pythonLoopValuePattern.FindAllStringSubmatch(match[2], -1) {
			if len(value) == 2 {
				loops[match[1]] = append(loops[match[1]], value[1])
			}
		}
	}
	for _, match := range pythonArgumentPattern.FindAllStringSubmatch(source, -1) {
		if len(match) != 2 {
			continue
		}
		flag := match[1]
		open, close := strings.Index(flag, "{"), strings.Index(flag, "}")
		if open < 0 || close <= open {
			flags[flag] = struct{}{}
			continue
		}
		for _, value := range loops[flag[open+1:close]] {
			flags[flag[:open]+value+flag[close+1:]] = struct{}{}
		}
	}
	return flags
}

func unknownPythonScriptFlags(command, scriptPath string, allowed map[string]struct{}) []string {
	index := strings.Index(command, scriptPath)
	if index < 0 {
		return nil
	}
	segment := command[index+len(scriptPath):]
	if boundary := strings.IndexAny(segment, ";|&"); boundary >= 0 {
		segment = segment[:boundary]
	}
	unknownSet := make(map[string]struct{})
	for _, flag := range pythonArgumentValuePattern.FindAllString(segment, -1) {
		if _, ok := allowed[flag]; !ok {
			unknownSet[flag] = struct{}{}
		}
	}
	return sortedStringSet(unknownSet)
}

func missingPythonScriptInputs(source, command, scriptPath, workingDirectory, workspace string) []string {
	inputDestinations := make(map[string]struct{})
	for _, match := range pythonDirectInputPattern.FindAllStringSubmatch(source, -1) {
		if len(match) == 2 {
			inputDestinations[match[1]] = struct{}{}
		}
	}
	resolvedDestinations := make(map[string]string)
	for _, match := range pythonResolvedPathPattern.FindAllStringSubmatch(source, -1) {
		if len(match) != 3 {
			continue
		}
		resolvedDestinations[match[1]] = match[2]
		if strings.Contains(source, match[1]+".is_file()") {
			inputDestinations[match[2]] = struct{}{}
		}
	}
	for _, match := range pythonArgumentLoopPattern.FindAllStringSubmatch(source, -1) {
		if len(match) != 3 || !strings.Contains(source, match[1]+".is_file()") {
			continue
		}
		for _, variable := range strings.Split(match[2], ",") {
			if destination := resolvedDestinations[strings.TrimSpace(variable)]; destination != "" {
				inputDestinations[destination] = struct{}{}
			}
		}
	}
	pathFlags := make(map[string]string)
	for _, match := range pythonPathArgumentPattern.FindAllStringSubmatch(source, -1) {
		if len(match) != 2 {
			continue
		}
		flag := match[1]
		pathFlags[strings.ReplaceAll(strings.TrimPrefix(flag, "--"), "-", "_")] = flag
	}
	index := strings.Index(command, scriptPath)
	if index < 0 {
		return nil
	}
	segment := command[index+len(scriptPath):]
	values := shellLongOptionValues(segment)
	missing := make(map[string]struct{})
	for destination := range inputDestinations {
		flag := "--" + strings.ReplaceAll(destination, "_", "-")
		if declared, ok := pathFlags[destination]; ok {
			flag = declared
		}
		value := strings.TrimSpace(values[flag])
		if value == "" || strings.HasPrefix(value, "-") || strings.ContainsAny(value, "$*?[]{}") {
			continue
		}
		target := filepath.Clean(value)
		if !filepath.IsAbs(target) {
			target = filepath.Clean(filepath.Join(workingDirectory, filepath.FromSlash(value)))
		}
		if !hostPathWithin(workspace, target) {
			continue
		}
		if info, err := os.Stat(target); err != nil || info.IsDir() || info.Size() == 0 {
			missing[flag+"="+filepath.ToSlash(value)] = struct{}{}
		}
	}
	return sortedStringSet(missing)
}

func shellLongOptionValues(segment string) map[string]string {
	values := make(map[string]string)
	fields := strings.Fields(strings.NewReplacer("\\\n", " ", "\\\r\n", " ").Replace(segment))
	for index := 0; index < len(fields); index++ {
		field := strings.Trim(fields[index], `"'`)
		if !strings.HasPrefix(field, "--") {
			continue
		}
		if parts := strings.SplitN(field, "=", 2); len(parts) == 2 {
			values[parts[0]] = strings.Trim(parts[1], `"'`)
			continue
		}
		if index+1 >= len(fields) {
			continue
		}
		next := strings.Trim(fields[index+1], `"'`)
		if next != "\\" && !strings.HasPrefix(next, "--") {
			values[field] = next
			index++
		}
	}
	return values
}

func sortedStringSet(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func (g serverAgentRuntimeToolGateway) agentRuntimeSkillEnvironmentPreflight(
	skill skills.Skill,
	input map[string]any,
) map[string]any {
	environment := strings.TrimSpace(stringValue(input["environment"]))
	if g.server != nil && g.server.kernelManager != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		environments, err := g.server.kernelManager.ListManagedEnvironments(ctx, kernelruntime.ManagedEnvironmentQuery{
			Dependencies: append([]string(nil), skill.RequiredEnvironmentPackages...), SkipHealth: true,
		})
		if err == nil {
			if selected := selectManagedSkillEnvironment(environments, environment); selected != "" {
				input["environment"] = selected
				return nil
			}
		}
	}
	return map[string]any{
		"ok": false, "status": "skill_environment_preflight_required", "executed": false,
		"message":  "The selected environment does not satisfy the loaded Skill's declared package contract for " + skill.Name + ".",
		"recovery": "Call manage_environments in list mode with the loaded Skill's complete required-environment-packages set. Reuse one returned ready environment, or create one complete immutable environment and wait for its notification before invoking the Skill script. Do not probe the script first and do not incrementally install the Skill-owned runtime packages.",
	}
}

func (g serverAgentRuntimeToolGateway) agentRuntimeSkillOwnedPackageMutationPreflight(
	publicName string,
	input map[string]any,
) map[string]any {
	if publicName != "manage_packages" || g.server == nil || g.server.skillCatalog == nil || g.taskRun == nil ||
		strings.EqualFold(strings.TrimSpace(stringValue(input["mode"])), "list") {
		return nil
	}
	requested := uniqueSortedFolded(skillStringValues(input["packages"]))
	if len(requested) == 0 {
		return nil
	}
	for _, skillName := range g.taskRun.executedSkillNamesSnapshot() {
		skill, found := findCatalogSkill(g.server.skillCatalog, skillName)
		if !found || !packageSpecsOverlap(requested, skill.RequiredEnvironmentPackages) {
			continue
		}
		return map[string]any{
			"ok": false, "status": "skill_environment_immutable_contract_required", "executed": false,
			"message":  "The package mutation targets runtime packages owned by the loaded Skill contract for " + skill.Name + ".",
			"recovery": "Use manage_environments with the Skill's complete required-environment-packages set. Reuse a compatible immutable environment or create a complete new environment; do not repair a partial environment through manage_packages.",
		}
	}
	return nil
}

func skillRuntimeCommandReferences(skillName, lowerContent string) bool {
	skillName = strings.ToLower(strings.TrimSpace(skillName))
	lowerContent = strings.ReplaceAll(strings.ToLower(lowerContent), `\`, "/")
	return skillName != "" && strings.Contains(lowerContent, "/.synon/runtime/skills/"+skillName+"-")
}

func selectManagedSkillEnvironment(environments []kernelruntime.ManagedEnvironment, preferred string) string {
	preferred = strings.TrimSpace(preferred)
	ready := make([]string, 0, len(environments))
	for _, environment := range environments {
		name := strings.TrimSpace(environment.Name)
		if name == "" || environment.Status != "ready" {
			continue
		}
		if name == preferred {
			return name
		}
		ready = append(ready, name)
	}
	sort.Strings(ready)
	if len(ready) == 0 {
		return ""
	}
	return ready[0]
}

func packageSpecsOverlap(requested, owned []string) bool {
	for _, request := range requested {
		requestName := packageSpecName(request)
		for _, dependency := range owned {
			if requestName != "" && requestName == packageSpecName(dependency) {
				return true
			}
		}
	}
	return false
}

func packageSpecName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	for index, char := range value {
		if strings.ContainsRune("[<>=!~ ", char) {
			return strings.TrimSpace(value[:index])
		}
	}
	return value
}

func skillStringValues(value any) []string {
	items := anySliceValue(value)
	values := make([]string, 0, len(items))
	for _, item := range items {
		if value := strings.TrimSpace(stringValue(item)); value != "" {
			values = append(values, value)
		}
	}
	return values
}
