package server

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"synon-go/internal/sciencecapability"
)

type managedExecutionEvidenceArgument struct {
	parameter sciencecapability.ExecutionParameter
	value     string
}

type managedExecutionUserEvidence struct {
	Source   string
	Question string
	Text     string
}

func managedExecutionPackParameterEvidencePreflight(
	pack sciencecapability.ExecutionPack,
	content string,
	userEvidence []managedExecutionUserEvidence,
	responseLanguage string,
	selectedResolvers []sciencecapability.ExecutionEvidenceResolver,
) map[string]any {
	argumentValues := managedExecutionArgumentValues(content)
	for _, parameter := range pack.Parameters {
		if parameter.Evidence != "selected-evidence-resolver" {
			continue
		}
		value, present := argumentValues[parameter.Argument]
		selected, found := selectedEvidenceResolverParameterValue(pack, selectedResolvers)
		if present && found && taskImplementationMatchesRegistered(value, selected) {
			continue
		}
		return map[string]any{
			"ok": false, "status": "execution_selected_resolver_parameter_required", "executed": false,
			"execution_pack_id": pack.ID, "parameter": parameter.Name,
			"message":  "The registered execution pack must use the implementation selected by the task's evidence-resolver receipt.",
			"recovery": "Retry the same registered execution-pack entrypoint without overriding the resolver-owned implementation argument.",
		}
	}
	for _, parameter := range pack.Parameters {
		if parameter.Evidence != "runtime-response-language" {
			continue
		}
		value, present := argumentValues[parameter.Argument]
		if present && strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(responseLanguage)) {
			continue
		}
		return map[string]any{
			"ok": false, "status": "execution_runtime_parameter_required", "executed": false,
			"execution_pack_id": pack.ID, "parameter": parameter.Name,
			"message":  "The registered execution pack must use the canonical task response language supplied by the runtime.",
			"recovery": "Retry the same registered execution-pack entrypoint without overriding the runtime-owned language argument.",
		}
	}
	expectedByGroup := map[string]int{}
	presentByGroup := map[string][]managedExecutionEvidenceArgument{}
	for _, parameter := range pack.Parameters {
		if parameter.Evidence != "resolved-user-input" {
			continue
		}
		group := parameter.EvidenceGroup
		if group == "" {
			group = parameter.Name
		}
		expectedByGroup[group]++
		if value, present := argumentValues[parameter.Argument]; present {
			presentByGroup[group] = append(presentByGroup[group], managedExecutionEvidenceArgument{
				parameter: parameter, value: value,
			})
		}
	}
	groups := make([]string, 0, len(presentByGroup))
	for group := range presentByGroup {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	for _, group := range groups {
		arguments := presentByGroup[group]
		if len(arguments) == expectedByGroup[group] && resolvedUserEvidenceContainsArguments(userEvidence, group, arguments) {
			continue
		}
		parameters := make([]string, 0, len(arguments))
		for _, argument := range arguments {
			parameters = append(parameters, argument.parameter.Name)
		}
		return map[string]any{
			"ok": false, "status": "execution_parameter_evidence_required", "executed": false,
			"execution_pack_id": pack.ID, "evidence_group": group, "parameters": parameters,
			"message":  "The registered execution pack requires this parameter group to match explicit current-task user input; model-derived values are not execution authority.",
			"recovery": "Use a value group stated by the user in the task or a resolved answer. If it is absent, ask one concise question for that missing scientific input; do not infer, default, or derive the values from unrelated text.",
		}
	}
	return nil
}

func selectedEvidenceResolverParameterValue(
	pack sciencecapability.ExecutionPack,
	selected []sciencecapability.ExecutionEvidenceResolver,
) (string, bool) {
	values := make([]string, 0, 1)
	for _, resolver := range selected {
		if strings.EqualFold(strings.TrimSpace(resolver.Skill), strings.TrimSpace(pack.Skill)) &&
			strings.TrimSpace(resolver.Implementation) != "" {
			values = append(values, strings.TrimSpace(resolver.Implementation))
		}
	}
	values = uniqueSortedFolded(values)
	return firstString(values), len(values) == 1
}

func managedExecutionArgumentValues(content string) map[string]string {
	tokens := managedExecutionCommandTokens(content)
	result := map[string]string{}
	for index := 0; index < len(tokens); index++ {
		token := strings.TrimSpace(tokens[index])
		if !strings.HasPrefix(token, "--") {
			continue
		}
		if equals := strings.IndexByte(token, '='); equals > 2 {
			result[token[:equals]] = token[equals+1:]
			continue
		}
		if index+1 < len(tokens) {
			result[token] = strings.TrimSpace(tokens[index+1])
		}
	}
	return result
}

func managedExecutionCommandTokens(content string) []string {
	content = strings.ReplaceAll(content, "\\\n", " ")
	return strings.FieldsFunc(content, func(char rune) bool {
		return unicode.IsSpace(char) || char == '\\' || char == '\'' || char == '"'
	})
}

var managedExecutionEvidenceNumberPattern = regexp.MustCompile(`[-+]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][-+]?\d+)?`)

func resolvedUserEvidenceContainsArguments(
	records []managedExecutionUserEvidence,
	group string,
	arguments []managedExecutionEvidenceArgument,
) bool {
	if len(arguments) == 0 {
		return true
	}
	numeric := true
	expected := make([]float64, len(arguments))
	for index, argument := range arguments {
		argumentGroup := strings.TrimSpace(argument.parameter.EvidenceGroup)
		if argumentGroup == "" {
			argumentGroup = argument.parameter.Name
		}
		if argumentGroup != group {
			return false
		}
		if argument.parameter.Type != "number" && argument.parameter.Type != "integer" {
			numeric = false
			break
		}
		value, err := strconv.ParseFloat(argument.value, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
		expected[index] = value
	}
	for _, record := range records {
		if numeric {
			if managedExecutionUserEvidenceContainsNumericArguments(record, arguments, expected) {
				return true
			}
			continue
		}
		cursor := 0
		lowerText := strings.ToLower(strings.TrimSpace(record.Question + " " + record.Text))
		matched := true
		for _, argument := range arguments {
			value := strings.ToLower(strings.TrimSpace(argument.value))
			offset := strings.Index(lowerText[cursor:], value)
			if value == "" || offset < 0 {
				matched = false
				break
			}
			cursor += offset + len(value)
		}
		if matched {
			return true
		}
	}
	return false
}
