package server

import (
	"sort"
	"strings"

	"synon-go/internal/sciencecapability"
)

// agentRuntimeControlledEvidenceDerivationPreflight prevents an ad hoc script
// from deriving a registry-controlled execution parameter before the user has
// selected an authoritative resolver. The gate is driven only by capability
// metadata and selected resolver receipts; it contains no task, file, engine,
// or domain-specific branches.
func (g serverAgentRuntimeToolGateway) agentRuntimeControlledEvidenceDerivationPreflight(
	publicName string,
	input map[string]any,
) map[string]any {
	switch publicName {
	case "bash", "python", "r", "repl", "powershell":
	default:
		return nil
	}
	if g.server == nil || g.server.skillCatalog == nil || g.server.scienceCapabilities == nil || g.taskRun == nil {
		return nil
	}
	content := strings.Join([]string{
		stringValue(input["human_description"]), stringValue(input["command"]),
		stringValue(input["code"]), stringValue(input["script"]),
	}, "\n")
	if strings.TrimSpace(content) == "" {
		return nil
	}
	groups := managedExecutionEvidenceGroupsForSelectedImplementations(
		g.server.skillCatalog, g.server.scienceCapabilities, g.taskRun,
	)
	resolvers := managedExecutionEvidenceResolversForSelectedImplementations(
		g.server.skillCatalog, g.server.scienceCapabilities, g.taskRun,
	)
	groupNames := make([]string, 0, len(groups))
	for group := range groups {
		groupNames = append(groupNames, group)
	}
	sort.Strings(groupNames)
	for _, group := range groupNames {
		candidates := resolvers[group]
		if len(candidates) == 0 || !managedExecutionTextNamesEvidenceGroup(content, groups[group]) {
			continue
		}
		selected, selectedFound := selectedEvidenceResolverForGroup(g.taskRun, group)
		if selectedFound && g.controlledEvidenceCommandExecutesSelectedResolver(
			publicName, stringValue(input["command"]), selected,
		) {
			return nil
		}
		result := map[string]any{
			"ok": false, "executed": false, "evidence_group": group,
			"message": "An ad hoc command cannot derive a registry-controlled execution parameter.",
		}
		if selectedFound {
			result["status"] = "selected_evidence_resolver_entrypoint_required"
			result["required_skill"] = selected.Skill
			result["selected_resolver"] = selected
			result["decision_required"] = false
			result["recovery"] = "Load the selected resolver Skill and execute its exact registered entrypoint. Preserve its validated receipt for the downstream execution pack; do not calculate, copy, or estimate the controlled value through an ad hoc script."
			return result
		}
		result["status"] = "evidence_resolver_selection_required"
		result["decision_required"] = true
		result["available_resolvers"] = candidates
		result["recovery"] = "Use ask_user to offer the registry-listed evidence resolvers, an authoritative user-supplied input, and a stop-with-explanation route. Do not execute a model-authored derivation before the user chooses."
		return result
	}
	return nil
}

func managedExecutionTextNamesEvidenceGroup(
	text string,
	parameters []sciencecapability.ExecutionParameter,
) bool {
	lower := strings.ToLower(strings.Join(strings.Fields(text), " "))
	for _, term := range managedExecutionAuthorizationTerms(parameters) {
		if strings.Contains(lower, strings.ToLower(strings.Join(strings.Fields(term), " "))) {
			return true
		}
	}
	return false
}

func selectedEvidenceResolverForGroup(
	run *sessionRunnerChatRun,
	group string,
) (sciencecapability.ExecutionEvidenceResolver, bool) {
	if run == nil {
		return sciencecapability.ExecutionEvidenceResolver{}, false
	}
	return selectedEvidenceResolverFromSelection(run.selectedEvidenceResolversSnapshot(), group)
}

func selectedEvidenceResolverFromSelection(
	selected []sciencecapability.ExecutionEvidenceResolver,
	group string,
) (sciencecapability.ExecutionEvidenceResolver, bool) {
	for _, resolver := range selected {
		if strings.EqualFold(strings.TrimSpace(resolver.EvidenceGroup), strings.TrimSpace(group)) {
			return resolver, true
		}
	}
	return sciencecapability.ExecutionEvidenceResolver{}, false
}

func (g serverAgentRuntimeToolGateway) controlledEvidenceCommandExecutesSelectedResolver(
	publicName string,
	content string,
	resolver sciencecapability.ExecutionEvidenceResolver,
) bool {
	if publicName != "bash" {
		return false
	}
	for _, engine := range g.server.scienceCapabilities.LocalExecutionPacksForSkill(resolver.Skill) {
		if commandExecutesManagedExecutionPack(resolver.Skill, engine.ExecutionPack, content) {
			return true
		}
	}
	return false
}
