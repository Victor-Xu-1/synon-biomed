package server

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"synon-go/internal/sciencecapability"
	"synon-go/internal/skills"
)

// askUserManagedExecutionParameterEvidenceCorrection prevents model-authored
// values from being laundered into explicit user evidence through an option.
// The controlled groups come only from the capability registry; the gateway
// contains no engine, task, domain, coordinate, or filename allowlist.
func askUserManagedExecutionParameterEvidenceCorrection(
	skillCatalog *skills.Catalog,
	capabilityCatalog *sciencecapability.Catalog,
	run *sessionRunnerChatRun,
	result map[string]any,
) map[string]any {
	if skillCatalog == nil || capabilityCatalog == nil || run == nil {
		return nil
	}
	groups := managedExecutionEvidenceGroupsForSelectedImplementations(skillCatalog, capabilityCatalog, run)
	if len(groups) == 0 {
		return nil
	}
	questions, ok := result["questions"].([]askUserQuestion)
	if !ok {
		return nil
	}
	userEvidence := run.resolvedUserEvidenceRecordsSnapshot()
	for _, question := range questions {
		for _, option := range question.Options {
			if correction := askUserManagedExecutionOptionEvidenceCorrection(groups, userEvidence, option); correction != nil {
				return correction
			}
		}
	}
	return nil
}

func askUserManagedExecutionOptionEvidenceCorrection(
	groups map[string][]sciencecapability.ExecutionParameter,
	userEvidence []managedExecutionUserEvidence,
	option askUserQuestionOption,
) map[string]any {
	routeDescription := askUserManagedExecutionOptionText(option)
	declaredValues, valid := askUserExecutionParameterValuesMetadata(option.Metadata["execution_parameter_values"])
	if !valid {
		return askUserExecutionParameterContractCorrection("", nil,
			"The option contains an invalid controlled execution-parameter declaration.")
	}
	declaredGroups := make(map[string]bool, len(declaredValues))
	for _, declaration := range declaredValues {
		declaredGroups[declaration.EvidenceGroup] = true
	}
	groupNames := make([]string, 0, len(groups))
	for group := range groups {
		groupNames = append(groupNames, group)
	}
	sort.Strings(groupNames)
	for _, group := range groupNames {
		parameters := groups[group]
		if declaredGroups[group] || !askUserRouteContainsUndeclaredEvidenceValues(routeDescription, group, parameters) {
			continue
		}
		return askUserExecutionParameterContractCorrection(group, parameters,
			"The option appears to propose controlled execution values but omits their typed registry evidence-group declaration.")
	}
	for _, declaration := range declaredValues {
		parameters, found := groups[declaration.EvidenceGroup]
		if !found || len(parameters) != len(declaration.Values) {
			return askUserExecutionParameterContractCorrection(declaration.EvidenceGroup, parameters,
				"The option's controlled execution-parameter declaration does not match the active capability registry.")
		}
		arguments := make([]managedExecutionEvidenceArgument, len(parameters))
		for index, parameter := range parameters {
			arguments[index] = managedExecutionEvidenceArgument{
				parameter: parameter,
				value:     strconv.FormatFloat(declaration.Values[index], 'g', -1, 64),
			}
		}
		if !managedExecutionTextContainsNumericValues(routeDescription, declaration.Values) {
			return askUserExecutionParameterContractCorrection(declaration.EvidenceGroup, parameters,
				"The option's displayed route does not contain the declared controlled execution-parameter values.")
		}
		if resolvedUserEvidenceContainsArguments(userEvidence, declaration.EvidenceGroup, arguments) {
			continue
		}
		names := make([]string, len(parameters))
		for index, parameter := range parameters {
			names[index] = parameter.Name
		}
		return map[string]any{
			"ok": false, "executed": false, "status": "ask_user_parameter_evidence_required",
			"decision_required": true, "evidence_group": declaration.EvidenceGroup, "parameters": names,
			"message":  "A controlled execution-parameter tuple appears in an option but is absent from explicit current-task user evidence.",
			"recovery": "Ask the user to supply the missing parameter group or an authoritative input that resolves it. Offer choices between valid input sources or stopping; do not propose model-derived numeric values, defaults, centroids, or estimates as selectable user evidence.",
		}
	}
	return nil
}

func askUserManagedExecutionSafeQuestion(
	group string,
	parameters []sciencecapability.ExecutionParameter,
	original string,
) string {
	names := make([]string, len(parameters))
	for index, parameter := range parameters {
		names[index] = parameter.Name
	}
	if strings.IndexFunc(original, func(char rune) bool { return unicode.Is(unicode.Han, char) }) >= 0 {
		label := group
		for _, parameter := range parameters {
			for _, term := range parameter.EvidenceTerms {
				if strings.IndexFunc(term, func(char rune) bool { return unicode.Is(unicode.Han, char) }) >= 0 {
					label = term
					break
				}
			}
			if label != group {
				break
			}
		}
		return "请提供可验证的" + label + "参数（" + strings.Join(names, "、") + "），或补充能够确定这些参数的权威输入。"
	}
	return "Provide authoritative values for " + strings.Join(names, ", ") +
		" or an authoritative input that resolves " + group + "."
}

const askUserEvidenceValueProximityBytes = 48

// askUserRouteContainsUndeclaredEvidenceValues derives its vocabulary from the
// active registry and requires a complete numeric group near that vocabulary
// in the same clause. This catches untyped "center x=..." proposals without
// treating later counts, durations, versions, or resources as center values.
func askUserRouteContainsUndeclaredEvidenceValues(
	text, group string,
	parameters []sciencecapability.ExecutionParameter,
) bool {
	return len(managedExecutionRouteNumericTuples(text, group, parameters)) > 0
}

// managedExecutionRouteNumericTuples returns complete registry-controlled
// numeric groups that appear next to the group's own vocabulary. Keeping the
// tuple extraction shared lets option validation and public narration apply
// the same evidence boundary without broad number stripping.
func managedExecutionRouteNumericTuples(
	text, group string,
	parameters []sciencecapability.ExecutionParameter,
) [][]float64 {
	if len(parameters) == 0 {
		return nil
	}
	terms := managedExecutionRouteTerms(group, parameters)
	if len(terms) == 0 {
		return nil
	}
	patternParts := make([]string, len(terms))
	for index, term := range terms {
		patternParts[index] = regexp.QuoteMeta(term)
	}
	termPattern := regexp.MustCompile(`(?i)(?:^|[^[:alnum:]])(?:` + strings.Join(patternParts, "|") + `)(?:$|[^[:alnum:]])`)
	result := make([][]float64, 0, 1)
	for _, clause := range strings.FieldsFunc(text, func(char rune) bool {
		return strings.ContainsRune(";；\n\r。！？!?", char)
	}) {
		numbers := managedExecutionEvidenceNumberPattern.FindAllStringIndex(clause, -1)
		if len(numbers) < len(parameters) {
			continue
		}
		termLocations := termPattern.FindAllStringIndex(clause, -1)
		for start := 0; start+len(parameters) <= len(numbers); start++ {
			valueStart := numbers[start][0]
			valueEnd := numbers[start+len(parameters)-1][1]
			for _, location := range termLocations {
				var distance, gapStart, gapEnd int
				switch {
				case valueEnd <= location[0]:
					distance = location[0] - valueEnd
					gapStart, gapEnd = valueEnd, location[0]
				case location[1] <= valueStart:
					distance = valueStart - location[1]
					gapStart, gapEnd = location[1], valueStart
				default:
					// A sliding numeric window can straddle the group label when
					// unrelated tuples occur on either side. Such a window is not
					// one ordered value group.
					continue
				}
				if distance <= askUserEvidenceValueProximityBytes &&
					managedExecutionControlledSeparator(clause[gapStart:gapEnd], parameters) {
					values := make([]float64, len(parameters))
					valid := true
					for index := range parameters {
						value, err := strconv.ParseFloat(clause[numbers[start+index][0]:numbers[start+index][1]], 64)
						if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
							valid = false
							break
						}
						values[index] = value
					}
					if valid {
						result = append(result, values)
					}
					break
				}
			}
		}
	}
	return result
}

func managedExecutionRouteContainsUnauthorizedEvidenceValues(
	text, group string,
	parameters []sciencecapability.ExecutionParameter,
	userEvidence []managedExecutionUserEvidence,
) bool {
	for _, values := range managedExecutionRouteNumericTuples(text, group, parameters) {
		arguments := make([]managedExecutionEvidenceArgument, len(parameters))
		for index, parameter := range parameters {
			arguments[index] = managedExecutionEvidenceArgument{
				parameter: parameter,
				value:     strconv.FormatFloat(values[index], 'g', -1, 64),
			}
		}
		if !resolvedUserEvidenceContainsArguments(userEvidence, group, arguments) {
			return true
		}
	}
	return false
}

func managedExecutionTextContainsNumericValues(text string, expected []float64) bool {
	if len(expected) == 0 {
		return false
	}
	matches := managedExecutionEvidenceNumberPattern.FindAllString(text, -1)
	for start := 0; start+len(expected) <= len(matches); start++ {
		matched := true
		for index := range expected {
			value, err := strconv.ParseFloat(matches[start+index], 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value != expected[index] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func managedExecutionUserEvidenceContainsNumericArguments(
	record managedExecutionUserEvidence,
	arguments []managedExecutionEvidenceArgument,
	expected []float64,
) bool {
	if len(arguments) == 0 || len(arguments) != len(expected) {
		return false
	}
	parameters := make([]sciencecapability.ExecutionParameter, len(arguments))
	for index, argument := range arguments {
		parameters[index] = argument.parameter
	}
	terms := managedExecutionAuthorizationTerms(parameters)
	if len(terms) == 0 {
		return false
	}
	if record.Source == "ask_user" {
		return len(managedExecutionAuthorizationTermLocations(record.Question, terms)) > 0 &&
			len(managedExecutionNumericTupleLocations(record.Text, parameters, expected)) > 0
	}
	for _, clause := range strings.FieldsFunc(record.Text, func(char rune) bool {
		return strings.ContainsRune(";；\n\r。！？!?", char)
	}) {
		termLocations := managedExecutionAuthorizationTermLocations(clause, terms)
		for _, values := range managedExecutionNumericTupleLocations(clause, parameters, expected) {
			for _, term := range termLocations {
				if managedExecutionLocationsHaveControlledGap(clause, term, values, parameters) {
					return true
				}
			}
		}
	}
	return false
}

func managedExecutionAuthorizationTermLocations(text string, terms []string) [][]int {
	parts := make([]string, 0, len(terms))
	for _, term := range terms {
		words := strings.Fields(term)
		for index := range words {
			words[index] = regexp.QuoteMeta(words[index])
		}
		if len(words) > 0 {
			parts = append(parts, strings.Join(words, `\s+`))
		}
	}
	if len(parts) == 0 {
		return nil
	}
	pattern := regexp.MustCompile(`(?i)(?:^|[^[:alnum:]])(?:` + strings.Join(parts, "|") + `)(?:$|[^[:alnum:]])`)
	return pattern.FindAllStringIndex(text, -1)
}

func managedExecutionNumericTupleLocations(
	text string,
	parameters []sciencecapability.ExecutionParameter,
	expected []float64,
) [][]int {
	numbers := managedExecutionEvidenceNumberPattern.FindAllStringIndex(text, -1)
	result := make([][]int, 0, 1)
	for start := 0; start+len(expected) <= len(numbers); start++ {
		matched := true
		for index := range expected {
			raw := text[numbers[start+index][0]:numbers[start+index][1]]
			value, err := strconv.ParseFloat(raw, 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value != expected[index] {
				matched = false
				break
			}
			if index > 0 && !managedExecutionControlledSeparator(text[numbers[start+index-1][1]:numbers[start+index][0]], parameters) {
				matched = false
				break
			}
		}
		if matched {
			result = append(result, []int{numbers[start][0], numbers[start+len(expected)-1][1]})
		}
	}
	return result
}

func managedExecutionLocationsHaveControlledGap(
	text string,
	left, right []int,
	parameters []sciencecapability.ExecutionParameter,
) bool {
	start, end := left[1], right[0]
	if right[1] <= left[0] {
		start, end = right[1], left[0]
	}
	if start > end || end-start > askUserEvidenceValueProximityBytes {
		return false
	}
	return managedExecutionControlledSeparator(text[start:end], parameters)
}

func managedExecutionControlledSeparator(text string, parameters []sciencecapability.ExecutionParameter) bool {
	allowed := map[string]bool{}
	for _, source := range managedExecutionParameterIdentifiersWithoutEvidenceTerms(parameters) {
		for _, term := range strings.FieldsFunc(strings.ToLower(source), func(char rune) bool {
			return !unicode.IsLetter(char) && !unicode.IsDigit(char)
		}) {
			if term != "" {
				allowed[term] = true
			}
		}
	}
	for _, term := range strings.FieldsFunc(strings.ToLower(text), func(char rune) bool {
		return !unicode.IsLetter(char) && !unicode.IsDigit(char)
	}) {
		if !allowed[term] {
			return false
		}
	}
	return true
}

func managedExecutionRouteTerms(
	group string,
	parameters []sciencecapability.ExecutionParameter,
) []string {
	terms := map[string]bool{}
	for _, source := range append([]string{group}, managedExecutionParameterIdentifiers(parameters)...) {
		for _, term := range strings.FieldsFunc(strings.ToLower(source), func(char rune) bool {
			return !unicode.IsLetter(char) && !unicode.IsDigit(char)
		}) {
			if utf8.RuneCountInString(term) > 1 {
				terms[term] = true
			}
		}
	}
	result := make([]string, 0, len(terms))
	for term := range terms {
		result = append(result, term)
	}
	sort.Strings(result)
	return result
}

// managedExecutionAuthorizationTerms retains only full, registry-authored
// semantic phrases. Parameter identifiers such as center_x are useful for
// validating tool routes but are too broad to authorize values in user prose.
func managedExecutionAuthorizationTerms(parameters []sciencecapability.ExecutionParameter) []string {
	terms := map[string]bool{}
	for _, parameter := range parameters {
		for _, rawTerm := range parameter.EvidenceTerms {
			term := strings.ToLower(strings.Join(strings.Fields(rawTerm), " "))
			if term != "" {
				terms[term] = true
			}
		}
	}
	result := make([]string, 0, len(terms))
	for term := range terms {
		result = append(result, term)
	}
	sort.Strings(result)
	return result
}

func managedExecutionParameterIdentifiers(parameters []sciencecapability.ExecutionParameter) []string {
	result := managedExecutionParameterIdentifiersWithoutEvidenceTerms(parameters)
	for _, parameter := range parameters {
		result = append(result, parameter.EvidenceTerms...)
	}
	return result
}

func managedExecutionParameterIdentifiersWithoutEvidenceTerms(
	parameters []sciencecapability.ExecutionParameter,
) []string {
	result := make([]string, 0, len(parameters)*2)
	for _, parameter := range parameters {
		result = append(result, parameter.Name, parameter.Argument)
	}
	return result
}

func askUserExecutionParameterValuesMetadata(value any) ([]askUserExecutionParameterValues, bool) {
	if value == nil {
		return nil, true
	}
	if typed, ok := value.([]askUserExecutionParameterValues); ok {
		return append([]askUserExecutionParameterValues(nil), typed...), len(typed) > 0
	}
	rawItems, ok := value.([]any)
	if !ok || len(rawItems) == 0 {
		return nil, false
	}
	result := make([]askUserExecutionParameterValues, 0, len(rawItems))
	for _, rawItem := range rawItems {
		item, ok := rawItem.(map[string]any)
		if !ok {
			return nil, false
		}
		group := strings.TrimSpace(stringValue(item["evidence_group"]))
		rawValues, ok := item["values"].([]any)
		if !askUserExecutionParameterGroupIdentifier(group) || !ok || len(rawValues) == 0 {
			return nil, false
		}
		values := make([]float64, len(rawValues))
		for index, rawValue := range rawValues {
			number, ok := askUserFiniteNumber(rawValue)
			if !ok {
				return nil, false
			}
			values[index] = number
		}
		result = append(result, askUserExecutionParameterValues{EvidenceGroup: group, Values: values})
	}
	return result, true
}

func askUserExecutionParameterContractCorrection(
	group string,
	parameters []sciencecapability.ExecutionParameter,
	message string,
) map[string]any {
	names := make([]string, len(parameters))
	for index, parameter := range parameters {
		names[index] = parameter.Name
	}
	return map[string]any{
		"ok": false, "executed": false, "status": "ask_user_parameter_contract_invalid",
		"decision_required": true, "evidence_group": group, "parameters": names,
		"message":  message,
		"recovery": "Use the exact registry evidence_group and ordered numeric values in execution_parameter_values when an option proposes controlled execution inputs. Do not declare unrelated resource, version, duration, count, or outcome numbers.",
	}
}

func managedExecutionEvidenceGroupsForSelectedImplementations(
	skillCatalog *skills.Catalog,
	capabilityCatalog *sciencecapability.Catalog,
	run *sessionRunnerChatRun,
) map[string][]sciencecapability.ExecutionParameter {
	groups := map[string][]sciencecapability.ExecutionParameter{}
	for _, implementation := range run.selectedImplementationsSnapshot() {
		skill, found := dedicatedSkillForImplementation(skillCatalog, implementation)
		if !found {
			continue
		}
		for _, engine := range capabilityCatalog.LocalExecutionPacksForSkill(skill.Name) {
			for _, parameter := range engine.ExecutionPack.Parameters {
				if parameter.Evidence != "resolved-user-input" ||
					(parameter.Type != "number" && parameter.Type != "integer") {
					continue
				}
				group := strings.TrimSpace(parameter.EvidenceGroup)
				if group == "" {
					group = parameter.Name
				}
				groups[group] = append(groups[group], parameter)
			}
		}
	}
	return groups
}

// managedExecutionEvidenceResolversForSelectedImplementations returns the
// registry-owned routes that can produce otherwise user-controlled execution
// evidence. Keeping this lookup beside the parameter-group lookup ensures that
// AskUser decisions are driven by the closed capability contract rather than
// Skill prose, task wording, filenames, or an engine-specific server branch.
func managedExecutionEvidenceResolversForSelectedImplementations(
	skillCatalog *skills.Catalog,
	capabilityCatalog *sciencecapability.Catalog,
	run *sessionRunnerChatRun,
) map[string][]sciencecapability.ExecutionEvidenceResolver {
	resolvers := map[string][]sciencecapability.ExecutionEvidenceResolver{}
	seen := map[string]bool{}
	skillNames := run.executedSkillNamesSnapshot()
	for _, implementation := range run.selectedImplementationsSnapshot() {
		skill, found := dedicatedSkillForImplementation(skillCatalog, implementation)
		if !found {
			continue
		}
		skillNames = append(skillNames, skill.Name)
	}
	// Completed Skill identities are pinned durable execution authority even
	// after the older implementation-selection receipt leaves the bounded
	// provider replay window. Resolver admission still requires an exact
	// declaration on that Skill's current local execution pack.
	for _, skillName := range uniqueSortedFolded(skillNames) {
		skill, found := findCatalogSkill(skillCatalog, skillName)
		if !found {
			continue
		}
		for _, engine := range capabilityCatalog.LocalExecutionPacksForSkill(skill.Name) {
			for _, resolver := range engine.ExecutionPack.EvidenceResolvers {
				group := strings.TrimSpace(resolver.EvidenceGroup)
				key := strings.ToLower(group + "\x00" + strings.TrimSpace(resolver.Skill) + "\x00" + strings.TrimSpace(resolver.Implementation))
				if group == "" || seen[key] {
					continue
				}
				seen[key] = true
				resolvers[group] = append(resolvers[group], resolver)
			}
		}
	}
	for group := range resolvers {
		sort.Slice(resolvers[group], func(left, right int) bool {
			leftKey := strings.ToLower(resolvers[group][left].Implementation + "\x00" + resolvers[group][left].Skill)
			rightKey := strings.ToLower(resolvers[group][right].Implementation + "\x00" + resolvers[group][right].Skill)
			return leftKey < rightKey
		})
	}
	return resolvers
}

// validatedSelectedAskUserEvidenceResolvers intersects durable AskUser state
// with the current primary implementation's closed registry contract. A
// transcript cannot authorize an arbitrary auxiliary engine merely by naming
// a Skill or implementation in persisted metadata.
func validatedSelectedAskUserEvidenceResolvers(
	skillCatalog *skills.Catalog,
	capabilityCatalog *sciencecapability.Catalog,
	run *sessionRunnerChatRun,
	candidates []sciencecapability.ExecutionEvidenceResolver,
) ([]sciencecapability.ExecutionEvidenceResolver, bool) {
	if len(candidates) == 0 {
		return nil, true
	}
	allowedByGroup := managedExecutionEvidenceResolversForSelectedImplementations(
		skillCatalog, capabilityCatalog, run,
	)
	validated := make([]sciencecapability.ExecutionEvidenceResolver, 0, len(candidates))
	seen := map[string]bool{}
	derivedPrimary := ""
	for _, candidate := range candidates {
		group := strings.TrimSpace(candidate.EvidenceGroup)
		skill := strings.TrimSpace(candidate.Skill)
		implementation := strings.TrimSpace(candidate.Implementation)
		matched := false
		for _, allowed := range allowedByGroup[group] {
			if strings.EqualFold(strings.TrimSpace(allowed.Skill), skill) &&
				strings.EqualFold(strings.TrimSpace(allowed.Implementation), implementation) {
				matched = true
				break
			}
		}
		if !matched {
			// An answered AskUser continuation is a server-constructed,
			// user-owned receipt. Its parent implementation checkpoint may age
			// out of a bounded provider window, so accept the exact resolver only
			// when the current catalog assigns that tuple to one and only one
			// local parent execution pack. Ambiguous or removed relationships
			// remain fail-closed.
			primary, unique := uniqueRegisteredEvidenceResolver(
				skillCatalog, capabilityCatalog, sciencecapability.ExecutionEvidenceResolver{
					EvidenceGroup: group, Skill: skill, Implementation: implementation,
				},
			)
			matched = unique
			if unique {
				if derivedPrimary != "" && !askUserImplementationIdentityMatches(derivedPrimary, primary) {
					return nil, false
				}
				derivedPrimary = primary
			}
		}
		key := strings.ToLower(group + "\x00" + skill + "\x00" + implementation)
		if !matched {
			return nil, false
		}
		if !seen[key] {
			seen[key] = true
			validated = append(validated, sciencecapability.ExecutionEvidenceResolver{
				EvidenceGroup: group, Skill: skill, Implementation: implementation,
			})
		}
	}
	if len(run.selectedImplementationsSnapshot()) == 0 && derivedPrimary != "" {
		run.setSelectedImplementations(derivedPrimary)
	}
	return validated, true
}

func uniqueRegisteredEvidenceResolver(
	skillCatalog *skills.Catalog,
	capabilityCatalog *sciencecapability.Catalog,
	candidate sciencecapability.ExecutionEvidenceResolver,
) (string, bool) {
	if skillCatalog == nil || capabilityCatalog == nil {
		return "", false
	}
	resolverSkill, found := findCatalogSkill(skillCatalog, candidate.Skill)
	if !found || !skillSupportsSelectedImplementation(
		resolverSkill, []string{candidate.Implementation},
	) {
		return "", false
	}
	parents := map[string]string{}
	for _, capability := range capabilityCatalog.Capabilities {
		for _, engine := range capability.AcceptedEngines {
			pack := engine.ExecutionPack
			if pack.Mode != "local" {
				continue
			}
			for _, resolver := range pack.EvidenceResolvers {
				if strings.EqualFold(strings.TrimSpace(resolver.EvidenceGroup), strings.TrimSpace(candidate.EvidenceGroup)) &&
					strings.EqualFold(strings.TrimSpace(resolver.Skill), strings.TrimSpace(candidate.Skill)) &&
					strings.EqualFold(strings.TrimSpace(resolver.Implementation), strings.TrimSpace(candidate.Implementation)) {
					parents[pack.ID] = pack.Skill
				}
			}
		}
	}
	if len(parents) != 1 {
		return "", false
	}
	parentSkillName := ""
	for _, skillName := range parents {
		parentSkillName = skillName
	}
	parentSkill, found := findCatalogSkill(skillCatalog, parentSkillName)
	if !found {
		return "", false
	}
	identities := uniqueSortedFolded(parentSkill.ImplementationIdentities)
	if len(identities) == 0 {
		return "", false
	}
	return identities[0], true
}

func askUserManagedExecutionOptionText(option askUserQuestionOption) string {
	parts := []string{
		option.Label,
		option.Description,
		option.Pros,
		option.Cons,
		option.Preview,
		stringValue(option.Metadata["route_description"]),
	}
	visible := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			visible = append(visible, part)
		}
	}
	return strings.Join(visible, " ")
}
