package server

import (
	"context"
	"fmt"
	"strings"

	"synon-go/internal/sciencecapability"
	"synon-go/internal/skills"
	"synon-go/internal/toolcontract"
)

func (g serverAgentRuntimeToolGateway) invalidAskUserSelectedImplementationCorrection(
	publicName string,
	input map[string]any,
) map[string]any {
	if publicName != toolcontract.AskUser || g.taskRun == nil {
		return nil
	}
	selected := g.taskRun.selectedImplementationsSnapshot()
	implementation := strings.TrimSpace(stringValue(input["implementation"]))
	if len(selected) == 0 || implementation == "" {
		return nil
	}
	for _, current := range selected {
		if askUserImplementationIdentityMatches(current, implementation) {
			return map[string]any{
				"code":     "selected_implementation_already_resolved",
				"message":  "The exact scientific implementation is already selected; a one-option request to continue it is not a user-owned decision.",
				"recovery": "Continue the selected implementation without asking again. Inspect the latest terminal causal error and official current source, preserve completed downloads and environment generations, then run one materially different governed repair to the affected setup or execution step.",
			}
		}
	}
	return map[string]any{
		"code":     "selected_implementation_recovery_required",
		"message":  "The proposed AskUser option differs from the latest exact scientific implementation selection.",
		"recovery": "Keep the latest selected implementation and repair its ordinary setup or execution failure. Do not ask to switch implementations without a server-verified machine-capacity infeasibility for the selected implementation.",
	}
}

func (g serverAgentRuntimeToolGateway) computeQuestionImplementationPreflight(
	publicName string,
	input map[string]any,
) map[string]any {
	if publicName != askAboutComputeToolName || g.server == nil || g.server.skillCatalog == nil || g.taskRun == nil {
		return nil
	}
	selected := g.taskRun.selectedImplementationsSnapshot()
	if len(selected) == 0 {
		return nil
	}
	content := strings.Join([]string{
		stringValue(input["question"]), stringValue(input["human_description"]),
	}, "\n")
	for _, skill := range g.server.skillCatalog.Skills() {
		for _, identity := range skill.ImplementationIdentities {
			if !implementationIdentityContained(content, identity) {
				continue
			}
			matchesSelected := false
			for _, current := range selected {
				if askUserImplementationIdentityMatches(current, identity) {
					matchesSelected = true
					break
				}
			}
			if matchesSelected {
				continue
			}
			return map[string]any{
				"ok": false, "status": "selected_implementation_recovery_required", "executed": false,
				"message":  "A compute host-configuration question cannot be used to switch the selected scientific implementation.",
				"recovery": "Keep the latest selected implementation. Use compute questions only for unresolved host or provider configuration after read-only probing; repair ordinary software setup or execution failures through the governed environment path.",
			}
		}
	}
	return nil
}

func askUserImplementationSelectionContractCorrection(
	run *sessionRunnerChatRun,
	result map[string]any,
) map[string]any {
	if run == nil || (!run.implementationSelectionRequiredSnapshot() && !askUserContainsUnresolvedImplementationChoice(run, result)) {
		return nil
	}
	questions, ok := result["questions"].([]askUserQuestion)
	if !ok || len(questions) == 0 {
		return askUserImplementationSelectionCorrection([]string{"no normalized question was available"})
	}
	if correction := askUserSelectedImplementationContinuityCorrection(run, questions); correction != nil {
		return correction
	}
	issues := make([]string, 0)
	requiredPreflights := make([]any, 0)
	for questionIndex, question := range questions {
		// A pending environment decision does not turn a separate input or
		// parameter question into an implementation choice. The shared question
		// schema owns option cardinality; only typed implementation proposals
		// require execution evidence here.
		if !askUserQuestionContainsImplementationChoice(question) {
			continue
		}
		for optionIndex, option := range question.Options {
			metadata := option.Metadata
			if strings.TrimSpace(stringValue(metadata["implementation"])) == "" {
				issues = append(issues, fmt.Sprintf("question %d option %d has no exact implementation", questionIndex+1, optionIndex+1))
			}
			resources := mapValue(metadata["resources"])
			if strings.TrimSpace(stringValue(resources["cpu"])) == "" ||
				strings.TrimSpace(stringValue(resources["memory"])) == "" ||
				strings.TrimSpace(stringValue(resources["gpu"])) == "" {
				issues = append(issues, fmt.Sprintf("question %d option %d lacks the CPU, memory, and GPU/VRAM profile", questionIndex+1, optionIndex+1))
			}
			hasImplementationEvidence := false
			for _, reference := range stringArrayValue(metadata["decision_evidence"]) {
				if strings.HasPrefix(strings.TrimSpace(reference), "tool-call:") {
					hasImplementationEvidence = true
					break
				}
			}
			if !hasImplementationEvidence {
				for _, reference := range stringArrayValue(metadata["readiness_evidence"]) {
					if strings.HasPrefix(strings.TrimSpace(reference), "compute-provider:") ||
						strings.HasPrefix(strings.TrimSpace(reference), "readiness-attestation:") {
						hasImplementationEvidence = true
						break
					}
				}
			}
			if !hasImplementationEvidence {
				issues = append(issues, fmt.Sprintf("question %d option %d has no current-task implementation discovery or preflight receipt", questionIndex+1, optionIndex+1))
				if implementation := strings.TrimSpace(stringValue(metadata["implementation"])); implementation != "" {
					// Preserve the proposal's exact identity as tool arguments. These
					// are incomplete read-only requests, not selected implementations
					// or authority to install guessed packages/providers.
					requiredPreflights = append(requiredPreflights, map[string]any{
						"question_index": questionIndex + 1, "option_index": optionIndex + 1,
						"tool":      manageEnvironmentsToolName,
						"arguments": map[string]any{"mode": "preflight", "implementation": implementation},
					})
				}
			}
		}
	}
	if len(issues) == 0 {
		return nil
	}
	correction := askUserImplementationSelectionCorrection(issues)
	if len(requiredPreflights) > 0 {
		correction["required_preflights"] = requiredPreflights
	}
	if available := anySliceValue(result["available_preflights"]); len(available) > 0 {
		correction["available_preflights"] = available
	}
	return correction
}

func (s *Server) resolveUniqueRegisteredImplementationAskUser(
	ctx context.Context,
	run *sessionRunnerChatRun,
	result map[string]any,
) (map[string]any, bool) {
	if s == nil || run == nil || len(run.selectedImplementationsSnapshot()) > 0 ||
		!askUserContainsSubstantialImplementationChoice(result) {
		return nil, false
	}
	selected, found := s.uniqueRegisteredLocalImplementation(run.requiredScientificCapabilitiesSnapshot())
	if !found {
		return nil, false
	}
	run.setSelectedImplementations(selected)
	run.setRegistrySelectedImplementation(selected)
	run.setImplementationSelectionRequired(false)
	resolution := map[string]any{
		"ok": true, "executed": false, "status": "implementation_selection_resolved",
		"decision_required": false, "selected_implementations": []string{selected},
		"required_capabilities": run.requiredScientificCapabilitiesSnapshot(),
		"message":               "The active capability registry has exactly one viable local primary route; any auxiliary input-source selection remains separate.",
		"recovery":              "Continue the registered implementation through its loaded Skill, exact environment package contract, reviewed entrypoint, and controlled-input preflight. Do not ask the user to compare unavailable or duplicate environment variants.",
	}
	if available := managedExecutionEvidenceResolversForSelectedImplementations(s.skillCatalog, s.scienceCapabilities, run); len(available) > 0 {
		resolution["available_evidence_resolvers"] = available
	}
	bound, _ := s.bindRegistrySelectedImplementationReceipt(ctx, resolution).(map[string]any)
	return bound, true
}

func askUserImplementationCapabilityContractCorrection(
	catalog *skills.Catalog,
	capabilityCatalog *sciencecapability.Catalog,
	run *sessionRunnerChatRun,
	result map[string]any,
) map[string]any {
	if catalog == nil || run == nil {
		return nil
	}
	required := semanticManagedEnvironmentCapabilities(run.requiredScientificCapabilitiesSnapshot())
	if len(required) == 0 {
		return nil
	}
	questions, ok := result["questions"].([]askUserQuestion)
	if !ok || len(questions) == 0 {
		return nil
	}
	routes := registeredImplementationRoutes(catalog, capabilityCatalog)
	issues := make([]string, 0)
	for questionIndex, question := range questions {
		for optionIndex, option := range question.Options {
			implementation := strings.TrimSpace(stringValue(option.Metadata["implementation"]))
			dedicated, found := dedicatedSkillForImplementation(catalog, implementation)
			if !found {
				continue
			}
			provided := semanticManagedEnvironmentCapabilities(dedicated.RequiredCapabilities)
			if route := routes[strings.ToLower(strings.TrimSpace(dedicated.Name))]; route != nil && !route.ambiguous {
				provided = nil
				for capability := range registeredImplementationRouteCapabilities(route, routes) {
					provided = append(provided, capability)
				}
			}
			missing := missingScientificCapabilities(required, provided)
			if len(missing) > 0 {
				issues = append(issues, fmt.Sprintf(
					"question %d option %d implementation %s does not provide required capabilities: %s",
					questionIndex+1, optionIndex+1, implementation, strings.Join(missing, ", "),
				))
			}
		}
	}
	if len(issues) == 0 {
		return nil
	}
	return askUserImplementationSelectionCorrection(issues)
}

func missingScientificCapabilities(required, provided []string) []string {
	providedSet := make(map[string]struct{}, len(provided))
	for _, capability := range provided {
		providedSet[strings.ToLower(strings.TrimSpace(capability))] = struct{}{}
	}
	missing := make([]string, 0)
	for _, capability := range required {
		if _, found := providedSet[strings.ToLower(strings.TrimSpace(capability))]; !found {
			missing = append(missing, capability)
		}
	}
	return uniqueSortedFolded(missing)
}

// askUserSelectedImplementationContinuityCorrection prevents a repairable
// setup failure from being converted into a user-facing request to abandon the
// implementation they already selected. A change is allowed only when the
// latest server-observed preflight for that exact implementation proves that a
// new local setup is blocked by current machine capacity. Package-manager
// errors, source-build failures, missing checkpoints, and model prose are not
// infeasibility attestations; the agent must continue repairing the selected
// implementation through the governed environment path.
func askUserSelectedImplementationContinuityCorrection(
	run *sessionRunnerChatRun,
	questions []askUserQuestion,
) map[string]any {
	selected := run.selectedImplementationsSnapshot()
	if len(selected) == 0 {
		return nil
	}
	offersDifferentImplementation := false
	selectedCapacityBlocked := false
	for _, question := range questions {
		for _, option := range question.Options {
			implementation := strings.TrimSpace(stringValue(option.Metadata["implementation"]))
			if implementation == "" {
				continue
			}
			matchesSelected := false
			for _, current := range selected {
				if askUserImplementationIdentityMatches(current, implementation) {
					matchesSelected = true
					break
				}
			}
			if !matchesSelected {
				offersDifferentImplementation = true
				continue
			}
			selectedCapacityBlocked = selectedCapacityBlocked || askUserOptionHasCapacityInfeasibility(option)
		}
	}
	if !offersDifferentImplementation || selectedCapacityBlocked {
		return nil
	}
	return map[string]any{
		"ok": true, "executed": false, "status": "selected_implementation_recovery_required",
		"decision_required":        false,
		"selected_implementations": append([]string(nil), selected...),
		"recovery":                 "Continue the exact selected implementation. The available evidence shows an ordinary setup or execution failure, not a server-verified machine-capacity infeasibility. Inspect the terminal causal error and official current source, preserve completed downloads and environment generations, then make one materially different governed repair to only the affected step. Do not offer another implementation or a weaker fallback unless a later exact preflight returns feasible=false, setup_state=new_setup_blocked, and explicit machine-capacity blockers for the selected implementation.",
	}
}

// askUserSelectedEvidenceResolverContinuityCorrection keeps an ordinary
// setup, source, or execution failure inside the already selected auxiliary
// resolver. Offering that same resolver beside substitute implementations is
// not a new user-owned decision unless a current machine preflight proves the
// selected resolver infeasible.
func (s *Server) askUserSelectedEvidenceResolverContinuityCorrection(
	run *sessionRunnerChatRun,
	result map[string]any,
) map[string]any {
	if s == nil || run == nil {
		return nil
	}
	selected := run.selectedEvidenceResolversSnapshot()
	questions, ok := result["questions"].([]askUserQuestion)
	if len(selected) == 0 || !ok {
		return nil
	}
	offersSelected, offersAlternative, selectedCapacityBlocked := false, false, false
	for _, question := range questions {
		for _, option := range question.Options {
			implementation := strings.TrimSpace(stringValue(option.Metadata["implementation"]))
			if implementation == "" {
				continue
			}
			matched := false
			for _, resolver := range selected {
				if taskImplementationMatchesRegistered(implementation, resolver.Implementation) {
					matched = true
					break
				}
			}
			if matched {
				offersSelected = true
				selectedCapacityBlocked = selectedCapacityBlocked || askUserOptionHasCapacityInfeasibility(option)
			} else {
				offersAlternative = true
			}
		}
	}
	if !offersSelected || !offersAlternative || selectedCapacityBlocked {
		return nil
	}
	correction := map[string]any{
		"ok": true, "executed": false, "status": "selected_evidence_resolver_recovery_required",
		"decision_required":           false,
		"selected_evidence_resolvers": append([]sciencecapability.ExecutionEvidenceResolver(nil), selected...),
		"recovery":                    "Continue the exact selected evidence resolver. The current evidence shows an ordinary setup, source, or execution failure rather than a verified machine-capacity infeasibility. Preserve completed work and repair only the affected governed step; do not ask the user to reselect or abandon the resolver.",
	}
	if downloads := s.selectedEvidenceResolverDownloads(run); len(downloads) > 0 {
		correction["required_tool"] = "download_public_scientific_file"
		correction["registered_downloads"] = downloads
		correction["recovery"] = "Continue the exact selected evidence resolver. Use the registered_downloads values verbatim with download_public_scientific_file, then resume only the affected execution-pack step. The immutable catalog source is authoritative; do not search for, rewrite, or substitute its URL."
	}
	return correction
}

func askUserOptionHasCapacityInfeasibility(option askUserQuestionOption) bool {
	metadata := option.Metadata
	if boolValue(metadata["preflight_feasible"], true) ||
		!strings.EqualFold(strings.TrimSpace(stringValue(metadata["preflight_setup_state"])), "new_setup_blocked") {
		return false
	}
	allowed := map[string]struct{}{
		"insufficient_cpu": {}, "insufficient_memory": {}, "insufficient_disk": {},
		"accelerator_unavailable": {}, "insufficient_accelerator_memory": {},
	}
	for _, blocker := range stringArrayValue(metadata["preflight_blockers"]) {
		if _, ok := allowed[strings.ToLower(strings.TrimSpace(blocker))]; ok {
			return true
		}
	}
	return false
}

func askUserContainsSubstantialImplementationChoice(result map[string]any) bool {
	questions, ok := result["questions"].([]askUserQuestion)
	if !ok {
		return false
	}
	for _, question := range questions {
		if askUserQuestionContainsImplementationChoice(question) {
			return true
		}
	}
	return false
}

func askUserQuestionContainsImplementationChoice(question askUserQuestion) bool {
	for _, option := range question.Options {
		if strings.TrimSpace(stringValue(option.Metadata["implementation"])) != "" ||
			askUserResourceProfileMetadataValue(option.Metadata["resources"]) != nil {
			return true
		}
	}
	return false
}

func askUserContainsUnresolvedImplementationChoice(run *sessionRunnerChatRun, result map[string]any) bool {
	if !askUserContainsSubstantialImplementationChoice(result) || run == nil {
		return false
	}
	selected := run.selectedImplementationsSnapshot()
	if len(selected) == 0 {
		return true
	}
	questions, ok := result["questions"].([]askUserQuestion)
	if !ok {
		return false
	}
	hasImplementation, hasUnboundResources := false, false
	for _, question := range questions {
		for _, option := range question.Options {
			implementation := strings.TrimSpace(stringValue(option.Metadata["implementation"]))
			if implementation == "" {
				hasUnboundResources = hasUnboundResources || askUserResourceProfileMetadataValue(option.Metadata["resources"]) != nil
				continue
			}
			hasImplementation = true
			matched := false
			for _, current := range selected {
				if askUserImplementationIdentityMatches(current, implementation) {
					matched = true
					break
				}
			}
			if !matched {
				return true
			}
		}
	}
	return !hasImplementation && hasUnboundResources
}

func askUserImplementationSelectionCorrection(issues []string) map[string]any {
	return map[string]any{
		"ok": true, "executed": false, "status": "implementation_decision_contract_incomplete",
		"decision_required": true, "issues": append([]string(nil), issues...),
		"recovery": "Finish read-only discovery and exact preflight for the materially viable routes being compared, then ask again. Present only genuine alternatives; do not invent additional implementations to fill an option quota. The Harness automatically binds matching preflight receipts; every implementation option must name the exact implementation and include a concise CPU, memory, and GPU/VRAM profile. Use unresolved instead of unsupported numeric claims. Input and parameter decisions do not select or authorize a scientific implementation. Do not create or install an environment before its required implementation choice is resolved.",
	}
}
