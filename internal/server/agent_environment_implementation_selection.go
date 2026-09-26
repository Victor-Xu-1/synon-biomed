package server

import (
	"context"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"

	"synon-go/internal/sciencecapability"
)

func selectedImplementationAuthorityContext(run *sessionRunnerChatRun) string {
	if run == nil {
		return ""
	}
	selected := run.selectedImplementationsSnapshot()
	if len(selected) == 0 {
		return ""
	}
	raw, err := json.Marshal(selected)
	if err != nil {
		return ""
	}
	contextValue := "Authoritative selected implementation (JSON): " + string(raw) +
		". Tool admission enforces it until a later answered AskUser changes it."
	if resolvers := run.selectedEvidenceResolversSnapshot(); len(resolvers) > 0 {
		if encoded, encodeErr := json.Marshal(resolvers); encodeErr == nil {
			contextValue += " Authorized auxiliary evidence resolvers (JSON): " + string(encoded) +
				". Load and execute each resolver through its registered Skill and execution pack without replacing the primary implementation."
		}
	}
	return contextValue
}

func canonicalManagedEnvironmentSelectedImplementation(ctx context.Context, requested string) string {
	requested = strings.TrimSpace(requested)
	run, _ := transcriptRunnerChatRunFromContext(ctx)
	if run == nil || requested == "" {
		return requested
	}
	matches := make([]string, 0, 1)
	for _, selected := range run.selectedImplementationsSnapshot() {
		if taskImplementationMatchesRegistered(requested, selected) {
			matches = append(matches, selected)
		}
	}
	for _, resolver := range run.selectedEvidenceResolversSnapshot() {
		if taskImplementationMatchesRegistered(requested, resolver.Implementation) {
			matches = append(matches, resolver.Implementation)
		}
	}
	matches = uniqueSortedFolded(matches)
	if len(matches) == 1 {
		return strings.TrimSpace(matches[0])
	}
	return requested
}

// canonicalManagedEnvironmentImplementation resolves a registry singleton at
// the first environment preflight, including when the model omitted the public
// implementation field. The selection and its provenance live in the task
// runtime; package names and environment labels never select an engine.
func (s *Server) canonicalManagedEnvironmentImplementation(ctx context.Context, requested string) string {
	requested = canonicalManagedEnvironmentSelectedImplementation(ctx, requested)
	run, _ := transcriptRunnerChatRunFromContext(ctx)
	if run == nil || len(run.selectedImplementationsSnapshot()) > 0 {
		return requested
	}
	selected, found := s.uniqueRegisteredLocalImplementation(run.requiredScientificCapabilitiesSnapshot())
	if !found || (requested != "" && !taskImplementationMatchesRegistered(requested, selected)) {
		return requested
	}
	if requested == "" {
		// A unique primary route can include separate auxiliary environments.
		// An unnamed provisioning request does not identify which stage it is
		// for; retain the existing exact-identity correction in that case.
		skill, _ := dedicatedSkillForImplementation(s.skillCatalog, selected)
		route := registeredImplementationRoutes(s.skillCatalog, s.scienceCapabilities)[strings.ToLower(strings.TrimSpace(skill.Name))]
		if !route.directlyProvides(semanticManagedEnvironmentCapabilities(run.requiredScientificCapabilitiesSnapshot())) {
			return requested
		}
	}
	run.setSelectedImplementations(selected)
	run.setRegistrySelectedImplementation(selected)
	run.setImplementationSelectionRequired(false)
	return selected
}

// registeredManagedEnvironmentExecutionPack returns the single reviewed pack
// that implements the task's selected capability. The capability registry,
// rather than the Skill's broad discovery package list or model-authored
// aliases, is the environment materialization authority.
func (s *Server) registeredManagedEnvironmentExecutionPack(
	ctx context.Context,
	implementation string,
) (sciencecapability.ExecutionPack, bool) {
	run, _ := transcriptRunnerChatRunFromContext(ctx)
	if s == nil || s.skillCatalog == nil || s.scienceCapabilities == nil || run == nil {
		return sciencecapability.ExecutionPack{}, false
	}
	if run.evidenceResolverAuthorizesImplementation(implementation) {
		var selectedPack sciencecapability.ExecutionPack
		for _, resolver := range run.selectedEvidenceResolversSnapshot() {
			if !taskImplementationMatchesRegistered(implementation, resolver.Implementation) {
				continue
			}
			skill, found := findCatalogSkill(s.skillCatalog, resolver.Skill)
			if !found || !run.evidenceResolverAuthorizesSkill(skill) {
				return sciencecapability.ExecutionPack{}, false
			}
			for _, engine := range s.scienceCapabilities.LocalExecutionPacksForSkill(skill.Name) {
				pack := engine.ExecutionPack
				if selectedPack.ID != "" && !strings.EqualFold(selectedPack.ID, pack.ID) {
					return sciencecapability.ExecutionPack{}, false
				}
				selectedPack = pack
			}
		}
		return selectedPack, selectedPack.ID != "" && len(selectedPack.Packages) > 0
	}
	selected := run.selectedImplementationsSnapshot()
	if len(selected) == 0 && taskExplicitlyNamesImplementation(run.TaskIntent, implementation) {
		skill, found := dedicatedSkillForImplementation(s.skillCatalog, implementation)
		if !found {
			return sciencecapability.ExecutionPack{}, false
		}
		engines := s.scienceCapabilities.LocalExecutionPacksForSkill(skill.Name)
		if len(engines) != 1 || len(engines[0].ExecutionPack.Packages) == 0 {
			return sciencecapability.ExecutionPack{}, false
		}
		return engines[0].ExecutionPack, true
	}
	if len(selected) != 1 || !askUserImplementationIdentityMatches(selected[0], implementation) {
		return sciencecapability.ExecutionPack{}, false
	}
	skill, found := dedicatedSkillForImplementation(s.skillCatalog, selected[0])
	if !found {
		return sciencecapability.ExecutionPack{}, false
	}
	required := map[string]bool{}
	for _, capability := range semanticManagedEnvironmentCapabilities(run.requiredScientificCapabilitiesSnapshot()) {
		required[strings.ToLower(strings.TrimSpace(capability))] = true
	}
	var selectedPack sciencecapability.ExecutionPack
	for _, capability := range s.scienceCapabilities.Capabilities {
		if !required[strings.ToLower(strings.TrimSpace(capability.ID))] {
			continue
		}
		for _, engine := range capability.AcceptedEngines {
			pack := engine.ExecutionPack
			if pack.Mode != "local" || !strings.EqualFold(strings.TrimSpace(pack.Skill), strings.TrimSpace(skill.Name)) {
				continue
			}
			if selectedPack.ID != "" && !strings.EqualFold(selectedPack.ID, pack.ID) {
				return sciencecapability.ExecutionPack{}, false
			}
			selectedPack = pack
		}
	}
	return selectedPack, selectedPack.ID != "" && len(selectedPack.Packages) > 0
}

func implementationIdentityContained(requested, selected string) bool {
	requested = strings.ToLower(strings.TrimSpace(requested))
	selected = strings.ToLower(strings.TrimSpace(selected))
	if requested == selected {
		return true
	}
	if requested == "" || selected == "" {
		return false
	}
	for offset := 0; offset < len(requested); {
		index := strings.Index(requested[offset:], selected)
		if index < 0 {
			return false
		}
		start := offset + index
		end := start + len(selected)
		leftBoundary := start == 0
		if !leftBoundary {
			r, _ := utf8.DecodeLastRuneInString(requested[:start])
			leftBoundary = !unicode.IsLetter(r) && !unicode.IsDigit(r)
		}
		rightBoundary := end == len(requested)
		if !rightBoundary {
			r, _ := utf8.DecodeRuneInString(requested[end:])
			rightBoundary = !unicode.IsLetter(r) && !unicode.IsDigit(r)
		}
		if leftBoundary && rightBoundary {
			return true
		}
		offset = start + 1
	}
	return false
}

var managedEnvironmentResourceCapabilities = map[string]struct{}{
	"cpu": {}, "gpu": {}, "memory": {}, "accelerator": {},
}

// managedEnvironmentImplementationDecision makes scientific environment
// provisioning follow typed task capability and exact user-choice state. It
// deliberately contains no target, package, engine, or domain-name routing.
func managedEnvironmentImplementationDecision(
	ctx context.Context,
	implementation string,
	requireSelection bool,
) (map[string]any, bool) {
	run, _ := transcriptRunnerChatRunFromContext(ctx)
	if run != nil && run.evidenceResolverAuthorizesImplementation(implementation) {
		return nil, false
	}
	if run == nil {
		return nil, false
	}
	capabilities := semanticManagedEnvironmentCapabilities(run.requiredScientificCapabilitiesSnapshot())
	selected := run.selectedImplementationsSnapshot()
	// An answered choice remains authoritative even if capability discovery
	// has not yet been rehydrated in this execution unit.
	if len(capabilities) == 0 && len(selected) == 0 {
		return nil, false
	}
	implementation = strings.TrimSpace(implementation)
	if implementation == "" {
		run.setImplementationSelectionRequired(true)
		return managedEnvironmentImplementationDecisionResult(
			"implementation_identity_required",
			"Name the exact scientific engine, service, or executable before preflighting or provisioning its environment. A runtime, framework, utility library, or generic dependency bundle is not a scientific implementation.",
			capabilities, nil,
		), true
	}
	if !requireSelection {
		return nil, false
	}
	if len(selected) > 0 {
		for _, allowed := range selected {
			if strings.EqualFold(strings.TrimSpace(allowed), implementation) {
				run.setImplementationSelectionRequired(false)
				return nil, false
			}
		}
		run.setImplementationSelectionRequired(true)
		return managedEnvironmentImplementationDecisionResult(
			"selected_implementation_mismatch",
			"The requested implementation differs from the user's answered choice. Copy one selected_implementations value verbatim into implementation and keep explanatory text only in human_description. Do not provision through a shell or another tool; if the scientific implementation itself must change, explain the new evidence and ask the user again.",
			capabilities, selected,
		), true
	}
	if taskExplicitlyNamesImplementation(run.TaskIntent, implementation) {
		run.setImplementationSelectionRequired(false)
		return nil, false
	}
	run.setImplementationSelectionRequired(true)
	return managedEnvironmentImplementationDecisionResult(
		"implementation_selection_required",
		"This is the first setup of a substantial scientific implementation. Preflight the viable concrete implementations, present their material trade-offs with ask_user, and provision only the exact implementation the user selects.",
		capabilities, nil,
	), true
}

// managedEnvironmentImplementationDecision lets a registry-defined singleton
// proceed without manufacturing a user choice. Unavailable packs are excluded;
// two or more viable local implementations still require an explicit decision.
func (s *Server) managedEnvironmentImplementationDecision(
	ctx context.Context,
	implementation string,
	requireSelection bool,
) (map[string]any, bool) {
	run, _ := transcriptRunnerChatRunFromContext(ctx)
	if requireSelection && run != nil && len(run.selectedImplementationsSnapshot()) == 0 {
		if selected, found := s.uniqueRegisteredLocalImplementation(run.requiredScientificCapabilitiesSnapshot()); found &&
			taskImplementationMatchesRegistered(implementation, selected) {
			run.setSelectedImplementations(selected)
			run.setRegistrySelectedImplementation(selected)
			run.setImplementationSelectionRequired(false)
			return nil, false
		}
	}
	return managedEnvironmentImplementationDecision(ctx, implementation, requireSelection)
}

func (s *Server) uniqueRegisteredLocalImplementation(requiredCapabilities []string) (string, bool) {
	if s == nil || s.scienceCapabilities == nil || s.skillCatalog == nil {
		return "", false
	}
	required := semanticManagedEnvironmentCapabilities(requiredCapabilities)
	if len(required) == 0 {
		return "", false
	}
	candidates := registeredImplementationRouteCandidates(s.skillCatalog, s.scienceCapabilities, required)
	if len(candidates) != 1 {
		return "", false
	}
	return candidates[0], true
}

func taskImplementationMatchesRegistered(requested, registered string) bool {
	requested = strings.TrimSpace(requested)
	return askUserImplementationIdentityMatches(requested, registered) || implementationIdentityContained(requested, registered)
}

func (s *Server) bindRegistrySelectedImplementationReceipt(ctx context.Context, result any) any {
	run, _ := transcriptRunnerChatRunFromContext(ctx)
	resultMap, ok := result.(map[string]any)
	if run == nil || !ok || resultMap == nil {
		return result
	}
	selected := run.consumeRegistrySelectedImplementations()
	if len(selected) != 1 {
		return result
	}
	resultMap["implementation_selection"] = map[string]any{
		"provenance":      "registry-unique-local-pack",
		"implementations": append([]string(nil), selected...),
	}
	return resultMap
}

func semanticManagedEnvironmentCapabilities(capabilities []string) []string {
	semantic := make([]string, 0, len(capabilities))
	for _, capability := range uniqueSortedScientificCapabilities(capabilities) {
		if _, resourceOnly := managedEnvironmentResourceCapabilities[strings.ToLower(capability)]; !resourceOnly {
			semantic = append(semantic, capability)
		}
	}
	return semantic
}

func taskExplicitlyNamesImplementation(taskIntent, implementation string) bool {
	taskIntent = strings.ToLower(strings.TrimSpace(taskIntent))
	implementation = strings.ToLower(strings.TrimSpace(implementation))
	return len([]rune(implementation)) >= 3 && strings.Contains(taskIntent, implementation)
}

func managedEnvironmentImplementationDecisionResult(
	status, recovery string,
	capabilities, selected []string,
) map[string]any {
	result := map[string]any{
		"ok": true, "executed": false, "feasible": false, "decision_required": true,
		"status": status, "required_capabilities": append([]string(nil), capabilities...),
		"recovery": recovery,
	}
	if len(selected) > 0 {
		result["selected_implementations"] = append([]string(nil), selected...)
	}
	return result
}
