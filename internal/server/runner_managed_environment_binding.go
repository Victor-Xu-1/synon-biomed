package server

import (
	"context"
	"encoding/json"
	"strings"

	"synon-go/internal/agentruntime"
	kernelruntime "synon-go/internal/kernel"
)

type managedEnvironmentInvalidation struct {
	Generation string
	Code       string
}

func (run *sessionRunnerChatRun) invalidateManagedEnvironment(environment, generation, code string) {
	if run == nil {
		return
	}
	environment = strings.TrimSpace(environment)
	code = strings.TrimSpace(code)
	if environment == "" || code == "" {
		return
	}
	run.managedEnvironmentMu.Lock()
	defer run.managedEnvironmentMu.Unlock()
	if run.managedEnvironmentInvalidations == nil {
		run.managedEnvironmentInvalidations = make(map[string]managedEnvironmentInvalidation)
	}
	run.managedEnvironmentInvalidations[strings.ToLower(environment)] = managedEnvironmentInvalidation{
		Generation: strings.TrimSpace(generation), Code: code,
	}
}

func (run *sessionRunnerChatRun) managedEnvironmentInvalidationSnapshot(environment string) (managedEnvironmentInvalidation, bool) {
	if run == nil {
		return managedEnvironmentInvalidation{}, false
	}
	environment = strings.ToLower(strings.TrimSpace(environment))
	if environment == "" {
		return managedEnvironmentInvalidation{}, false
	}
	run.managedEnvironmentMu.Lock()
	defer run.managedEnvironmentMu.Unlock()
	value, found := run.managedEnvironmentInvalidations[environment]
	return value, found
}

func (run *sessionRunnerChatRun) clearManagedEnvironmentInvalidation(environment, generation string) {
	if run == nil {
		return
	}
	environment = strings.ToLower(strings.TrimSpace(environment))
	generation = strings.TrimSpace(generation)
	if environment == "" || generation == "" {
		return
	}
	run.managedEnvironmentMu.Lock()
	defer run.managedEnvironmentMu.Unlock()
	invalid, found := run.managedEnvironmentInvalidations[environment]
	if !found || (invalid.Generation != "" && strings.EqualFold(invalid.Generation, generation)) {
		return
	}
	delete(run.managedEnvironmentInvalidations, environment)
}

func (run *sessionRunnerChatRun) managedEnvironmentCandidateReusable(environment kernelruntime.ManagedEnvironment) bool {
	invalid, found := run.managedEnvironmentInvalidationSnapshot(environment.Name)
	if !found {
		return true
	}
	generation := strings.TrimSpace(environment.Generation)
	return invalid.Generation != "" && generation != "" && !strings.EqualFold(invalid.Generation, generation)
}

func (run *sessionRunnerChatRun) filterReusableManagedEnvironmentCandidates(
	candidates []kernelruntime.ManagedEnvironment,
) []kernelruntime.ManagedEnvironment {
	if run == nil || len(candidates) == 0 {
		return candidates
	}
	filtered := make([]kernelruntime.ManagedEnvironment, 0, len(candidates))
	for _, candidate := range candidates {
		if run.managedEnvironmentCandidateReusable(candidate) {
			filtered = append(filtered, candidate)
		}
	}
	return filtered
}

// bindManagedEnvironment records the canonical ready environment selected by
// environment management. A create request may intentionally reuse an existing
// immutable environment, so the model's requested label is not execution
// authority; the returned environment.name is.
func (run *sessionRunnerChatRun) bindManagedEnvironment(requested, selected string) {
	if run == nil {
		return
	}
	requested = strings.TrimSpace(requested)
	selected = strings.TrimSpace(selected)
	if requested == "" || selected == "" {
		return
	}
	run.managedEnvironmentMu.Lock()
	defer run.managedEnvironmentMu.Unlock()
	if run.managedEnvironmentBindings == nil {
		run.managedEnvironmentBindings = make(map[string]string)
	}
	key := strings.ToLower(requested)
	if strings.EqualFold(requested, selected) {
		delete(run.managedEnvironmentBindings, key)
		return
	}
	run.managedEnvironmentBindings[key] = selected
}

func (run *sessionRunnerChatRun) selectedManagedEnvironment(requested string) string {
	requested = strings.TrimSpace(requested)
	if run == nil || requested == "" {
		return requested
	}
	run.managedEnvironmentMu.Lock()
	defer run.managedEnvironmentMu.Unlock()
	selected := requested
	seen := make(map[string]struct{}, 4)
	for range 8 {
		key := strings.ToLower(strings.TrimSpace(selected))
		if _, duplicate := seen[key]; duplicate {
			return requested
		}
		seen[key] = struct{}{}
		next := strings.TrimSpace(run.managedEnvironmentBindings[key])
		if next == "" || strings.EqualFold(next, selected) {
			return selected
		}
		selected = next
	}
	return selected
}

// bindManagedEnvironmentImplementation records which scientific
// implementation a ready managed environment was provisioned for. The
// association is task-scoped and reconstructed from durable tool messages;
// environment names remain labels rather than implementation authority.
// Multiple identities are retained because one immutable utility environment
// may legitimately be shared by more than one user-selected implementation.
func (run *sessionRunnerChatRun) bindManagedEnvironmentImplementation(environment, implementation string) {
	if run == nil {
		return
	}
	environment = strings.TrimSpace(environment)
	implementation = strings.TrimSpace(implementation)
	if environment == "" || implementation == "" {
		return
	}
	run.managedEnvironmentMu.Lock()
	defer run.managedEnvironmentMu.Unlock()
	if run.managedEnvironmentImplementations == nil {
		run.managedEnvironmentImplementations = make(map[string][]string)
	}
	key := strings.ToLower(environment)
	run.managedEnvironmentImplementations[key] = uniqueSortedFolded(append(
		append([]string(nil), run.managedEnvironmentImplementations[key]...), implementation,
	))
}

func (run *sessionRunnerChatRun) managedEnvironmentImplementationsSnapshot(environment string) []string {
	if run == nil {
		return nil
	}
	environment = strings.ToLower(strings.TrimSpace(environment))
	if environment == "" {
		return nil
	}
	run.managedEnvironmentMu.Lock()
	defer run.managedEnvironmentMu.Unlock()
	return append([]string(nil), run.managedEnvironmentImplementations[environment]...)
}

func (run *sessionRunnerChatRun) bindManagedEnvironmentToolResult(
	toolName string,
	input map[string]any,
	response any,
) {
	result := mapValue(response)
	environment := mapValue(result["environment"])
	if !strings.EqualFold(strings.TrimSpace(stringValue(environment["status"])), "ready") {
		// A preflight recommendation is already a verified immutable ready
		// environment. Bind the requested label at this boundary too, so a weak
		// model cannot immediately fail by reusing its proposed label instead of
		// the exact environment selected by the preflight authority.
		environment = mapValue(result["recommended_environment"])
	}
	if !strings.EqualFold(strings.TrimSpace(stringValue(environment["status"])), "ready") {
		return
	}
	selected := strings.TrimSpace(stringValue(environment["name"]))
	run.clearManagedEnvironmentInvalidation(selected, stringValue(environment["generation"]))
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case manageEnvironmentsToolName:
		run.bindManagedEnvironmentImplementation(selected, stringValue(input["implementation"]))
		requested := strings.TrimSpace(stringValue(result["requested_name"]))
		if requested == "" {
			requested = strings.TrimSpace(stringValue(input["name"]))
		}
		run.bindManagedEnvironment(requested, selected)
	case managePackagesToolName:
		requested := strings.TrimSpace(stringValue(input["environment"]))
		forkTo := strings.TrimSpace(stringValue(input["fork_to"]))
		if !strings.EqualFold(selected, requested) &&
			(forkTo == "" || !strings.EqualFold(selected, forkTo)) {
			// Older runtimes could redirect a one-package delta to any environment
			// that already contained that package. Ignore those durable results so
			// hydration cannot recreate the unsafe cross-implementation alias.
			return
		}
		run.bindManagedEnvironmentImplementation(selected, stringValue(input["implementation"]))
		run.bindManagedEnvironment(requested, selected)
		if forkTo != "" {
			run.bindManagedEnvironment(forkTo, selected)
		}
	}
}

func (run *sessionRunnerChatRun) bindManagedEnvironmentFailureResult(input map[string]any, response any) {
	result := mapValue(response)
	code := strings.TrimSpace(stringValue(result["code"]))
	if !compatibilityPlanBool(result["environment_incompatible"]) {
		code = agentKernelEnvironmentFailureCode(stringValue(result["exit_status"]), stringValue(result["stderr"]))
		if code == "" {
			return
		}
	}
	environment := strings.TrimSpace(stringValue(result["environment"]))
	if environment == "" {
		environment = strings.TrimSpace(stringValue(input["environment"]))
	}
	run.invalidateManagedEnvironment(environment, stringValue(result["environment_generation"]), code)
}

func (run *sessionRunnerChatRun) bindManagedEnvironmentNotificationResponse(response any) {
	result := mapValue(response)
	for _, raw := range anySliceValue(result["notifications"]) {
		notification := mapValue(raw)
		payload := mapValue(notification["payload"])
		if !strings.EqualFold(strings.TrimSpace(stringValue(payload["tool"])), manageEnvironmentsToolName) &&
			!strings.EqualFold(strings.TrimSpace(stringValue(payload["tool"])), managePackagesToolName) {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(stringValue(payload["status"])), "completed") {
			continue
		}
		environment := mapValue(payload["environment"])
		if !strings.EqualFold(strings.TrimSpace(stringValue(environment["status"])), "ready") {
			continue
		}
		run.bindManagedEnvironmentImplementation(
			stringValue(environment["name"]), stringValue(payload["implementation"]),
		)
		run.clearManagedEnvironmentInvalidation(
			stringValue(environment["name"]), stringValue(environment["generation"]),
		)
	}
}

func (run *sessionRunnerChatRun) bindManagedEnvironmentMessages(messages []agentruntime.Message) {
	if run == nil {
		return
	}
	type callRecord struct {
		name  string
		input map[string]any
	}
	calls := make(map[string]callRecord)
	for _, message := range messages {
		if message.Role == "assistant" {
			for _, call := range message.ToolCalls {
				name := strings.ToLower(strings.TrimSpace(call.Name))
				if name != manageEnvironmentsToolName && name != managePackagesToolName &&
					name != "python" && name != "bash" && name != "r" {
					continue
				}
				input := map[string]any{}
				if json.Unmarshal(call.Arguments, &input) == nil {
					calls[strings.TrimSpace(call.ID)] = callRecord{name: name, input: input}
				}
			}
			continue
		}
		if message.Role != "tool" {
			continue
		}
		var response any
		if json.Unmarshal([]byte(message.Content), &response) == nil {
			run.bindManagedEnvironmentNotificationResponse(response)
		}
		call, found := calls[strings.TrimSpace(message.ToolCallID)]
		if !found {
			continue
		}
		if response != nil {
			if call.name == manageEnvironmentsToolName || call.name == managePackagesToolName {
				run.bindManagedEnvironmentToolResult(call.name, call.input, response)
			} else {
				run.bindManagedEnvironmentFailureResult(call.input, response)
			}
		}
	}
}

func (s *Server) hydrateSessionRunnerManagedEnvironmentBindings(
	ctx context.Context,
	run *sessionRunnerChatRun,
) {
	if s == nil || run == nil {
		return
	}
	run.managedEnvironmentMu.Lock()
	if run.managedEnvironmentBindingsHydrated {
		run.managedEnvironmentMu.Unlock()
		return
	}
	run.managedEnvironmentBindingsHydrated = true
	run.managedEnvironmentMu.Unlock()
	messages, err := s.sessionRunnerDurableExplicitToolContractMessages(ctx, run)
	if err == nil {
		run.bindManagedEnvironmentMessages(messages)
	}
}

func normalizeSelectedManagedEnvironment(
	run *sessionRunnerChatRun,
	toolName string,
	input map[string]any,
) map[string]any {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "python", "r", "bash", managePackagesToolName:
	default:
		return input
	}
	requested := strings.TrimSpace(stringValue(input["environment"]))
	selected := run.selectedManagedEnvironment(requested)
	if selected == "" || selected == requested {
		return input
	}
	normalized := copyMapAny(input)
	normalized["environment"] = selected
	return normalized
}

// bindTaskRunContext keeps every gateway path on the same task-scoped runtime
// state. Some recovery and exact-execution paths supply the run through the
// invocation context rather than the gateway constructor. Environment aliases
// and other task-scoped state must remain available on those paths too.
func (g serverAgentRuntimeToolGateway) bindTaskRunContext(ctx context.Context) (serverAgentRuntimeToolGateway, context.Context) {
	run, _ := ctx.Value(transcriptRunnerChatRunContextKey{}).(*sessionRunnerChatRun)
	if run == nil && g.taskRun != nil {
		return g, withTranscriptRunnerChatRun(ctx, g.taskRun)
	}
	if run != nil && g.taskRun == nil {
		g.taskRun = run
		if g.server != nil {
			g.server.hydrateSessionRunnerManagedEnvironmentBindings(ctx, run)
		}
	}
	return g, ctx
}
