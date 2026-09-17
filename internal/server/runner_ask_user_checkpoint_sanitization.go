package server

import (
	"encoding/json"
	"sort"
	"strings"
	"unicode"

	"synon-go/internal/agentruntime"
	"synon-go/internal/sciencecapability"
	"synon-go/internal/toolcontract"
)

// sanitizeManagedAskUserModelTurnForCheckpoint keeps the model narration and
// the durable AskUser payload on one evidence boundary. Streaming text remains
// private until EventModelResponse reveals the tool calls, so an unsupported
// model-derived value can be replaced before either surface is published.
func (s *Server) sanitizeManagedAskUserModelTurnForCheckpoint(
	run *sessionRunnerChatRun,
	message string,
	calls []agentruntime.ToolCall,
) (string, []agentruntime.ToolCall) {
	if len(calls) == 0 {
		return message, calls
	}
	sanitizedCalls := make([]agentruntime.ToolCall, len(calls))
	hasAskUser := false
	payloadChanged := false
	for index, original := range calls {
		canonical, err := canonicalRuntimeToolName(original.Name)
		if err == nil && canonical == toolcontract.AskUser && !original.RejectedBeforeExecution {
			hasAskUser = true
		}
		sanitized := s.sanitizeManagedAskUserToolCallForCheckpoint(run, original)
		sanitizedCalls[index] = sanitized
		if sanitized.Name != original.Name || string(sanitized.Arguments) != string(original.Arguments) {
			payloadChanged = true
		}
	}
	if !hasAskUser || run == nil || s == nil || s.skillCatalog == nil || s.scienceCapabilities == nil {
		return message, sanitizedCalls
	}
	groups := managedExecutionEvidenceGroupsForSelectedImplementations(s.skillCatalog, s.scienceCapabilities, run)
	userEvidence := run.resolvedUserEvidenceRecordsSnapshot()
	groupNames := make([]string, 0, len(groups))
	for group := range groups {
		groupNames = append(groupNames, group)
	}
	sort.Strings(groupNames)
	narrationUnsafe := false
	for _, group := range groupNames {
		if managedExecutionRouteContainsUnauthorizedEvidenceValues(message, group, groups[group], userEvidence) {
			narrationUnsafe = true
			break
		}
	}
	if (!payloadChanged && !narrationUnsafe) || strings.TrimSpace(message) == "" {
		return message, sanitizedCalls
	}
	if strings.IndexFunc(message, func(char rune) bool { return unicode.Is(unicode.Han, char) }) >= 0 {
		return "继续执行前需要补充可验证的输入，请在下方选择提供方式。", sanitizedCalls
	}
	return "Verified input is required before execution can continue; choose how to provide it below.", sanitizedCalls
}

// sanitizeManagedAskUserToolCallForCheckpoint removes model-authored values
// for registry-controlled execution parameters before the tool call becomes an
// immutable transcript batch. The persisted call, pending card, and recovery
// path therefore share one canonical question instead of reconciling two
// different payloads after the fact.
func (s *Server) sanitizeManagedAskUserToolCallForCheckpoint(
	run *sessionRunnerChatRun,
	call agentruntime.ToolCall,
) agentruntime.ToolCall {
	canonical, err := canonicalRuntimeToolName(call.Name)
	if err != nil || canonical != toolcontract.AskUser || call.RejectedBeforeExecution || run == nil ||
		s == nil || s.skillCatalog == nil || s.scienceCapabilities == nil {
		return call
	}
	input := map[string]any{}
	decoder := json.NewDecoder(strings.NewReader(string(call.Arguments)))
	decoder.UseNumber()
	if err := decoder.Decode(&input); err != nil || input == nil || decoder.Decode(&struct{}{}) == nil {
		return call
	}
	normalizedInput, err := normalizeAskUserToolInput(input)
	if err != nil {
		return call
	}
	questions, err := askUserQuestionValue(normalizedInput)
	rawOptions, rawOK := normalizedInput["options"].([]any)
	if err != nil || len(questions) != 1 || !rawOK || len(rawOptions) != len(questions[0].Options) {
		return call
	}
	groups := managedExecutionEvidenceGroupsForSelectedImplementations(s.skillCatalog, s.scienceCapabilities, run)
	if len(groups) == 0 {
		return call
	}
	resolvers := managedExecutionEvidenceResolversForSelectedImplementations(s.skillCatalog, s.scienceCapabilities, run)
	userEvidence := run.resolvedUserEvidenceRecordsSnapshot()
	kept := make([]any, 0, len(rawOptions))
	var firstCorrection map[string]any
	for index, option := range questions[0].Options {
		correction := askUserManagedExecutionOptionEvidenceCorrection(groups, userEvidence, option)
		if correction == nil {
			if raw, valid := rawOptions[index].(map[string]any); valid {
				kept = append(kept, copyMapAny(raw))
			}
			continue
		}
		if firstCorrection == nil {
			firstCorrection = correction
		}
	}
	group := ""
	if firstCorrection != nil {
		group = strings.TrimSpace(stringValue(firstCorrection["evidence_group"]))
	} else {
		group = managedExecutionAskUserResolverGroup(questions[0], groups, resolvers)
	}
	parameters := groups[group]
	if group == "" || len(parameters) == 0 {
		return call
	}
	if firstCorrection != nil {
		if len(kept) == 0 {
			kept = append(kept, managedExecutionSafeAskUserOption(group, parameters, questions[0].Question, true))
		}
		if len(kept) < 2 {
			kept = append(kept, managedExecutionSafeAskUserOption(group, parameters, questions[0].Question, false))
		}
	}
	resolverAdded := false
	if candidates := resolvers[group]; len(candidates) > 0 {
		kept, resolverAdded = managedExecutionPrioritizeResolverOption(
			kept, candidates[0], group, parameters, questions[0].Question,
		)
	}
	if resolverAdded {
		kept = managedExecutionEnsureStopOption(
			kept, group, parameters, questions[0].Question,
		)
	}
	if firstCorrection == nil && !resolverAdded {
		return call
	}
	if len(kept) < 2 {
		kept = append(kept, managedExecutionSafeAskUserOption(group, parameters, questions[0].Question, false))
	}
	for index, raw := range kept {
		option, valid := raw.(map[string]any)
		if !valid {
			return call
		}
		option["recommended"] = index == 0
	}
	if firstCorrection != nil {
		normalizedInput["question"] = askUserManagedExecutionSafeQuestion(group, parameters, questions[0].Question)
	}
	normalizedInput["options"] = kept
	normalizedInput["multi_select"] = false
	encoded, err := json.Marshal(normalizedInput)
	if err != nil {
		return call
	}
	call.Arguments = encoded
	call.Name = canonical
	return call
}

func managedExecutionAskUserResolverGroup(
	question askUserQuestion,
	groups map[string][]sciencecapability.ExecutionParameter,
	resolvers map[string][]sciencecapability.ExecutionEvidenceResolver,
) string {
	parts := []string{question.Question, question.Header}
	for _, option := range question.Options {
		parts = append(parts, askUserManagedExecutionOptionText(option))
	}
	text := strings.ToLower(strings.Join(parts, " "))
	groupNames := make([]string, 0, len(resolvers))
	for group := range resolvers {
		groupNames = append(groupNames, group)
	}
	sort.Strings(groupNames)
	for _, group := range groupNames {
		for _, term := range managedExecutionAuthorizationTerms(groups[group]) {
			if strings.Contains(text, strings.ToLower(term)) {
				return group
			}
		}
	}
	return ""
}

func managedExecutionPrioritizeResolverOption(
	options []any,
	resolver sciencecapability.ExecutionEvidenceResolver,
	group string,
	parameters []sciencecapability.ExecutionParameter,
	original string,
) ([]any, bool) {
	resolverTerms := []string{
		strings.ToLower(strings.TrimSpace(resolver.Implementation)),
		strings.ToLower(strings.TrimSpace(resolver.Skill)),
	}
	for index, raw := range options {
		option, valid := raw.(map[string]any)
		if !valid {
			continue
		}
		metadata := mapValue(option["metadata"])
		declaredImplementation := strings.TrimSpace(stringValue(metadata["implementation"]))
		if declaredImplementation != "" &&
			!taskImplementationMatchesRegistered(declaredImplementation, resolver.Implementation) {
			continue
		}
		if boolValue(metadata["terminal_decision"], false) {
			continue
		}
		text := strings.ToLower(managedExecutionRawAskUserOptionText(option))
		matches := false
		for _, term := range resolverTerms {
			if term != "" && strings.Contains(text, term) {
				matches = true
				break
			}
		}
		if !matches {
			continue
		}
		bound := copyMapAny(option)
		metadata = copyMapAny(mapValue(bound["metadata"]))
		if metadata == nil {
			metadata = map[string]any{}
		}
		metadata["evidence_resolver"] = map[string]any{
			"evidence_group": group,
			"skill":          resolver.Skill,
			"implementation": resolver.Implementation,
		}
		delete(metadata, "implementation")
		delete(metadata, "resources")
		bound["metadata"] = metadata
		if index == 0 {
			updated := append([]any(nil), options...)
			updated[0] = bound
			return updated, true
		}
		prioritized := make([]any, 0, len(options))
		prioritized = append(prioritized, bound)
		prioritized = append(prioritized, options[:index]...)
		prioritized = append(prioritized, options[index+1:]...)
		return prioritized, true
	}
	if len(options) >= 4 {
		options = options[:3]
	}
	prioritized := make([]any, 0, len(options)+1)
	prioritized = append(prioritized, managedExecutionResolverAskUserOption(resolver, group, parameters, original))
	prioritized = append(prioritized, options...)
	return prioritized, true
}

func managedExecutionEnsureStopOption(
	options []any,
	group string,
	parameters []sciencecapability.ExecutionParameter,
	original string,
) []any {
	for _, raw := range options {
		if boolValue(mapValue(mapValue(raw)["metadata"])["terminal_decision"], false) {
			return options
		}
	}
	zh := strings.IndexFunc(original, func(char rune) bool { return unicode.Is(unicode.Han, char) }) >= 0
	label := managedExecutionEvidenceDisplayTerm(group, parameters, zh)
	var stop map[string]any
	if zh {
		stop = managedExecutionSafeAskUserOptionFields(
			"结束并说明原因", "不生成或猜测"+label+"，说明缺少可验证输入并结束当前任务。",
			"避免使用未经验证的受控参数产生不可靠结果。", "不会执行依赖该输入的后续步骤。",
		)
	} else {
		stop = managedExecutionSafeAskUserOptionFields(
			"Stop with explanation", "Do not generate or guess "+label+"; explain the missing verified input and end the current task.",
			"Avoids unreliable output based on unverified controlled parameters.", "Downstream steps that require this input will not run.",
		)
	}
	stop["metadata"] = map[string]any{"terminal_decision": true}
	if len(options) >= 4 {
		updated := append([]any(nil), options...)
		updated[len(updated)-1] = stop
		return updated
	}
	return append(options, stop)
}

func managedExecutionRawAskUserOptionText(option map[string]any) string {
	parts := []string{
		stringValue(option["label"]), stringValue(option["description"]),
		stringValue(option["pros"]), stringValue(option["cons"]), stringValue(option["preview"]),
	}
	if metadata, valid := option["metadata"].(map[string]any); valid {
		parts = append(parts, stringValue(metadata["route_description"]))
	}
	return strings.Join(parts, " ")
}

func managedExecutionResolverAskUserOption(
	resolver sciencecapability.ExecutionEvidenceResolver,
	group string,
	parameters []sciencecapability.ExecutionParameter,
	original string,
) map[string]any {
	zh := strings.IndexFunc(original, func(char rune) bool { return unicode.Is(unicode.Han, char) }) >= 0
	label := managedExecutionEvidenceDisplayTerm(group, parameters, zh)
	implementation := strings.TrimSpace(resolver.Implementation)
	var option map[string]any
	if zh {
		option = managedExecutionSafeAskUserOptionFields(
			"使用"+implementation+"自动识别", "运行已注册的"+implementation+"解析器，生成可验证的"+label+"与执行回执。",
			"无需猜测"+label+"，并保留可审计的解析结果。", "自动识别结果仍需结合任务背景审阅。",
		)
	} else {
		option = managedExecutionSafeAskUserOptionFields(
			"Use "+implementation+" detection", "Run the registered "+implementation+" resolver to produce verifiable "+label+" evidence and an execution receipt.",
			"Avoids guessed "+label+" and preserves an auditable resolver result.", "The automatic result still requires task-context review.",
		)
	}
	option["metadata"] = map[string]any{
		"evidence_resolver": map[string]any{
			"evidence_group": group, "skill": resolver.Skill, "implementation": implementation,
		},
	}
	return option
}

func managedExecutionSafeAskUserOption(
	group string,
	parameters []sciencecapability.ExecutionParameter,
	original string,
	values bool,
) map[string]any {
	zh := strings.IndexFunc(original, func(char rune) bool { return unicode.Is(unicode.Han, char) }) >= 0
	label := managedExecutionEvidenceDisplayTerm(group, parameters, zh)
	if zh {
		if values {
			return managedExecutionSafeAskUserOptionFields(
				"提供所需参数", "在自定义回答中提供可验证的"+label+"参数。",
				"任务可以使用用户确认的数值继续。", "需要提供参数后才能运行。",
			)
		}
		return managedExecutionSafeAskUserOptionFields(
			"补充权威输入", "上传或引用能够确定"+label+"的权威输入。",
			"系统可以从可验证来源解析所需参数。", "需要先补充输入。",
		)
	}
	if values {
		return managedExecutionSafeAskUserOptionFields(
			"Provide required values", "Provide authoritative "+label+" values in the custom answer.",
			"The task can continue with user-confirmed values.", "Execution waits for the values.",
		)
	}
	return managedExecutionSafeAskUserOptionFields(
		"Provide authoritative input", "Upload or reference an authoritative input that resolves "+label+".",
		"The system can derive the parameters from a verifiable source.", "An additional input is required.",
	)
}

func managedExecutionSafeAskUserOptionFields(label, description, pros, cons string) map[string]any {
	return map[string]any{
		"label": label, "description": description, "pros": pros, "cons": cons,
		"readiness": "Waiting for authoritative input.", "readiness_status": "not_applicable",
		"decision_evidence": []any{"user-input:current-task"}, "readiness_evidence": []any{},
		"selection_basis": "user_objective", "expected_outcome": "Continue with verified execution parameters.",
		"selection_rationale": "Choose this route to provide the missing authoritative input.",
	}
}

func managedExecutionEvidenceDisplayTerm(
	group string,
	parameters []sciencecapability.ExecutionParameter,
	zh bool,
) string {
	for _, parameter := range parameters {
		for _, term := range parameter.EvidenceTerms {
			hasHan := strings.IndexFunc(term, func(char rune) bool { return unicode.Is(unicode.Han, char) }) >= 0
			if hasHan == zh {
				return term
			}
		}
	}
	return group
}
