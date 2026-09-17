package server

import (
	"crypto/sha256"

	"encoding/hex"

	"errors"
	"fmt"
	"math"

	"strconv"
	"strings"

	"time"
	"unicode/utf8"

	taskstore "synon-go/internal/persistence/tasks"

	"synon-go/internal/tools/fileops"
)

type askUserQuestionOption struct {
	Label       string         `json:"label"`
	Description string         `json:"description,omitempty"`
	Pros        string         `json:"pros,omitempty"`
	Cons        string         `json:"cons,omitempty"`
	Preview     string         `json:"preview,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

type askUserQuestion struct {
	Question    string                  `json:"question"`
	Header      string                  `json:"header"`
	Options     []askUserQuestionOption `json:"options"`
	MultiSelect bool                    `json:"multiSelect"`
}

type askUserResourceProfile struct {
	CPU    string `json:"cpu"`
	Memory string `json:"memory"`
	GPU    string `json:"gpu"`
}

type askUserExecutionParameterValues struct {
	EvidenceGroup string    `json:"evidence_group"`
	Values        []float64 `json:"values"`
}

func patchOperationsValue(value any) []fileops.PatchOperation {
	rawItems, ok := value.([]any)
	if !ok {
		return nil
	}
	operations := make([]fileops.PatchOperation, 0, len(rawItems))
	for _, rawItem := range rawItems {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		operations = append(operations, fileops.PatchOperation{
			Type:      stringValue(item["type"]),
			StartLine: int(numberValue(item["startLine"])),
			EndLine:   int(numberValue(item["endLine"])),
			Line:      int(numberValue(item["line"])),
			Content:   stringValue(item["content"]),
		})
	}
	return operations
}

func jsonPatchOperationsValue(value any) []fileops.JSONPatchOperation {
	rawItems, ok := value.([]any)
	if !ok {
		return nil
	}
	operations := make([]fileops.JSONPatchOperation, 0, len(rawItems))
	for _, rawItem := range rawItems {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		operations = append(operations, fileops.JSONPatchOperation{
			Op:    stringValue(item["op"]),
			Path:  stringValue(item["path"]),
			Value: item["value"],
		})
	}
	return operations
}

func codeIndexOptionsValue(input map[string]any) fileops.CodeIndexOptions {
	return fileops.CodeIndexOptions{
		Limit:       numberValue(input["limit"]),
		SymbolLimit: numberValue(input["symbolLimit"]),
		Extensions:  stringArrayValue(input["extensions"]),
		SymbolKinds: stringArrayValue(input["symbolKinds"]),
		Query:       stringValue(input["query"]),
	}
}

func codeReferencesOptionsValue(input map[string]any) fileops.CodeReferencesOptions {
	return fileops.CodeReferencesOptions{
		Limit:        numberValue(input["limit"]),
		Extensions:   stringArrayValue(input["extensions"]),
		ContextLines: numberValue(input["contextLines"]),
	}
}

func grepOptionsValue(input map[string]any) fileops.GrepOptions {
	before := numberValue(input["-B"])
	if before == 0 {
		before = numberValue(input["before"])
	}
	after := numberValue(input["-A"])
	if after == 0 {
		after = numberValue(input["after"])
	}
	contextLines := numberValue(input["-C"])
	if contextLines == 0 {
		contextLines = numberValue(input["context"])
	}
	headLimit := numberValue(input["head_limit"])
	if headLimit == 0 {
		headLimit = numberValue(input["headLimit"])
	}
	return fileops.GrepOptions{
		Glob:            stringValue(input["glob"]),
		OutputMode:      stringValue(input["output_mode"]),
		Before:          before,
		After:           after,
		Context:         contextLines,
		ShowLineNumbers: boolValue(input["-n"], true),
		CaseInsensitive: boolValue(input["-i"], false),
		Type:            stringValue(input["type"]),
		HeadLimit:       headLimit,
		Offset:          numberValue(input["offset"]),
		Multiline:       boolValue(input["multiline"], false),
	}
}

func normalizeAskUserToolInput(input map[string]any) (map[string]any, error) {
	if input == nil {
		return nil, errors.New("ask_user input is required")
	}
	if _, direct := input["question"]; direct {
		return copyMapAny(input), nil
	}
	rawQuestions, legacy := input["questions"]
	if !legacy {
		return copyMapAny(input), nil
	}
	questions, ok := rawQuestions.([]any)
	if !ok || len(questions) != 1 {
		return nil, errors.New("legacy ask_user questions must contain exactly one item")
	}
	question, ok := questions[0].(map[string]any)
	if !ok {
		return nil, errors.New("legacy ask_user question must be an object")
	}
	normalized := map[string]any{
		"question": question["question"],
		"header":   question["header"],
		"options":  question["options"],
	}
	if value, found := question["multi_select"]; found {
		normalized["multi_select"] = value
	} else if value, found := question["multiSelect"]; found {
		normalized["multi_select"] = value
	}
	return normalized, nil
}

func askUserQuestionValue(input map[string]any) ([]askUserQuestion, error) {
	questionText := strings.TrimSpace(stringValue(input["question"]))
	if questionText == "" {
		return nil, errors.New("ask_user.question is required")
	}
	if utf8.RuneCountInString(questionText) < 4 {
		return nil, errors.New("ask_user.question must be a concrete question of at least 4 characters")
	}
	header := strings.TrimSpace(stringValue(input["header"]))
	if header == "" {
		return nil, errors.New("ask_user.header is required")
	}
	if utf8.RuneCountInString(header) < 2 {
		return nil, errors.New("ask_user.header must contain at least 2 characters")
	}
	options, err := askUserQuestionOptionsValue(input["options"], 0, questionText)
	if err != nil {
		return nil, err
	}
	return []askUserQuestion{{
		Question: questionText, Header: header, Options: options,
		MultiSelect: boolValue(input["multi_select"], false),
	}}, nil
}

func askUserQuestionOptionsValue(value any, questionIndex int, questionText string) ([]askUserQuestionOption, error) {
	rawItems, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("questions[%d].options must be an array", questionIndex)
	}
	if len(rawItems) < 2 || len(rawItems) > 4 {
		return nil, fmt.Errorf("questions[%d].options must contain 2 to 4 items", questionIndex)
	}
	options := make([]askUserQuestionOption, 0, len(rawItems))
	seenLabels := map[string]bool{}
	recommendedCount := 0
	for index, rawItem := range rawItems {
		item, ok := rawItem.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("questions[%d].options[%d] must be an object", questionIndex, index)
		}
		label := strings.TrimSpace(stringValue(item["label"]))
		if label == "" {
			return nil, fmt.Errorf("questions[%d].options[%d].label is required", questionIndex, index)
		}
		if utf8.RuneCountInString(label) < 2 {
			return nil, fmt.Errorf("questions[%d].options[%d].label must contain at least 2 characters", questionIndex, index)
		}
		if utf8.RuneCountInString(label) > 80 {
			return nil, fmt.Errorf("questions[%d].options[%d].label must contain at most 80 characters", questionIndex, index)
		}
		if seenLabels[label] {
			return nil, fmt.Errorf("questions[%d].options[%d].label duplicates another option", questionIndex, index)
		}
		seenLabels[label] = true
		description, err := requiredAskUserDecisionText(item, questionIndex, index, "description", 4, 400)
		if err != nil {
			return nil, err
		}
		pros, err := requiredAskUserDecisionText(item, questionIndex, index, "pros", 2, 280)
		if err != nil {
			return nil, err
		}
		cons, err := requiredAskUserDecisionText(item, questionIndex, index, "cons", 2, 280)
		if err != nil {
			return nil, err
		}
		readiness, err := requiredAskUserDecisionText(item, questionIndex, index, "readiness", 4, 400)
		if err != nil {
			return nil, err
		}
		readinessStatus, err := askUserReadinessStatusValue(item["readiness_status"], questionIndex, index)
		if err != nil {
			return nil, err
		}
		decisionEvidence, err := askUserEvidenceReferencesValue(item["decision_evidence"], questionIndex, index, "decision_evidence", 1)
		if err != nil {
			return nil, err
		}
		readinessEvidence, err := askUserEvidenceReferencesValue(item["readiness_evidence"], questionIndex, index, "readiness_evidence", 0)
		if err != nil {
			return nil, err
		}
		selectionBasis, err := askUserSelectionBasisValue(item["selection_basis"], questionIndex, index)
		if err != nil {
			return nil, err
		}
		resources, err := askUserResourceProfileValue(item["resources"], questionIndex, index)
		if err != nil {
			return nil, err
		}
		implementation := strings.TrimSpace(stringValue(item["implementation"]))
		if resources != nil && implementation == "" {
			return nil, fmt.Errorf("questions[%d].options[%d].implementation is required when resources are provided", questionIndex, index)
		}
		if utf8.RuneCountInString(implementation) > 100 || strings.ContainsAny(implementation, "\r\n") {
			return nil, fmt.Errorf("questions[%d].options[%d].implementation must be one line of at most 100 characters", questionIndex, index)
		}
		executionParameterValues, err := askUserExecutionParameterValuesValue(
			item["execution_parameter_values"], questionIndex, index,
		)
		if err != nil {
			return nil, err
		}
		expectedOutcome, err := requiredAskUserDecisionText(item, questionIndex, index, "expected_outcome", 3, 400)
		if err != nil {
			return nil, err
		}
		selectionRationale, err := requiredAskUserDecisionText(item, questionIndex, index, "selection_rationale", 3, 400)
		if err != nil {
			return nil, err
		}
		recommended, ok := item["recommended"].(bool)
		if !ok {
			return nil, fmt.Errorf("questions[%d].options[%d].recommended must be a boolean", questionIndex, index)
		}
		if recommended {
			recommendedCount++
		}
		readinessDisplay := askUserReadinessDisplayText(readinessStatus, sessionRunnerResponseLanguage(questionText) == "zh")
		metadata := map[string]any{}
		if rawMetadata, ok := item["metadata"].(map[string]any); ok {
			metadata = copyMapAny(rawMetadata)
		}
		delete(metadata, "execution_parameter_values")
		metadata["decision_contract"] = "ask_user_option_v3"
		metadata["route_description"] = description
		metadata["reported_readiness"] = readiness
		metadata["readiness"] = readinessDisplay
		metadata["readiness_status"] = readinessStatus
		metadata["decision_evidence"] = append([]string(nil), decisionEvidence...)
		metadata["readiness_evidence"] = append([]string(nil), readinessEvidence...)
		metadata["selection_basis"] = selectionBasis
		if resources != nil {
			metadata["resources"] = map[string]any{
				"cpu": resources.CPU, "memory": resources.Memory, "gpu": resources.GPU,
			}
		}
		if implementation != "" {
			metadata["implementation"] = implementation
		}
		if len(executionParameterValues) > 0 {
			metadata["execution_parameter_values"] = executionParameterValues
		}
		// Older persisted questions used one free-form requirements sentence. Keep
		// it only as non-public audit evidence; new tool calls use the typed three-
		// field resource profile so unrelated details cannot leak into the card.
		if legacy := strings.TrimSpace(stringValue(item["requirements"])); legacy != "" {
			metadata["legacy_requirements"] = legacy
		}
		metadata["expected_outcome"] = expectedOutcome
		metadata["selection_rationale"] = selectionRationale
		metadata["recommended"] = recommended
		option := askUserQuestionOption{
			Label: askUserPublicOptionLabel(label, implementation),
			Description: askUserDecisionDescription(
				questionText, description, readinessStatus, resources,
			),
			Pros: pros, Cons: cons, Metadata: metadata,
		}
		if preview := stringValue(item["preview"]); strings.TrimSpace(preview) != "" {
			option.Preview = preview
		}
		options = append(options, option)
	}
	if recommendedCount != 1 {
		return nil, fmt.Errorf("questions[%d].options must mark exactly one recommended option", questionIndex)
	}
	return options, nil
}

func askUserExecutionParameterValuesValue(value any, questionIndex, optionIndex int) ([]askUserExecutionParameterValues, error) {
	if value == nil {
		return nil, nil
	}
	rawItems, ok := value.([]any)
	if !ok || len(rawItems) < 1 || len(rawItems) > 8 {
		return nil, fmt.Errorf("questions[%d].options[%d].execution_parameter_values must contain 1 to 8 items", questionIndex, optionIndex)
	}
	result := make([]askUserExecutionParameterValues, 0, len(rawItems))
	seenGroups := map[string]bool{}
	for valueIndex, rawItem := range rawItems {
		item, ok := rawItem.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("questions[%d].options[%d].execution_parameter_values[%d] must be an object", questionIndex, optionIndex, valueIndex)
		}
		group := strings.TrimSpace(stringValue(item["evidence_group"]))
		if !askUserExecutionParameterGroupIdentifier(group) || seenGroups[group] {
			return nil, fmt.Errorf("questions[%d].options[%d].execution_parameter_values[%d].evidence_group must be a unique registry identifier", questionIndex, optionIndex, valueIndex)
		}
		seenGroups[group] = true
		rawValues, ok := item["values"].([]any)
		if !ok || len(rawValues) < 1 || len(rawValues) > 16 {
			return nil, fmt.Errorf("questions[%d].options[%d].execution_parameter_values[%d].values must contain 1 to 16 numbers", questionIndex, optionIndex, valueIndex)
		}
		values := make([]float64, len(rawValues))
		for numberIndex, rawValue := range rawValues {
			number, ok := askUserFiniteNumber(rawValue)
			if !ok {
				return nil, fmt.Errorf("questions[%d].options[%d].execution_parameter_values[%d].values[%d] must be a finite number", questionIndex, optionIndex, valueIndex, numberIndex)
			}
			values[numberIndex] = number
		}
		result = append(result, askUserExecutionParameterValues{EvidenceGroup: group, Values: values})
	}
	return result, nil
}

func askUserExecutionParameterGroupIdentifier(value string) bool {
	if value == "" || len(value) > 100 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	previousHyphen := false
	for index := 1; index < len(value); index++ {
		char := value[index]
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') {
			previousHyphen = false
			continue
		}
		if char != '-' || previousHyphen || index == len(value)-1 {
			return false
		}
		previousHyphen = true
	}
	return true
}

func askUserFiniteNumber(value any) (float64, bool) {
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case float32:
		number = float64(typed)
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	case int32:
		number = float64(typed)
	default:
		return 0, false
	}
	return number, !math.IsNaN(number) && !math.IsInf(number, 0)
}

func askUserPublicOptionLabel(label, implementation string) string {
	label = strings.TrimSpace(label)
	implementation = strings.TrimSpace(implementation)
	if implementation == "" || strings.Contains(strings.ToLower(label), strings.ToLower(implementation)) {
		return label
	}
	// Lowercase path-like implementation keys bind the answer to runtime
	// authority but are not product names. Keep them in typed metadata and let
	// the model's concise public label name the method or software.
	if strings.ToLower(implementation) == implementation && strings.ContainsAny(implementation, "-_/:") {
		return label
	}
	return label + " · " + implementation
}

func askUserResourceProfileValue(value any, questionIndex, optionIndex int) (*askUserResourceProfile, error) {
	if value == nil {
		return nil, nil
	}
	raw, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("questions[%d].options[%d].resources must be an object", questionIndex, optionIndex)
	}
	for key := range raw {
		switch key {
		case "cpu", "memory", "gpu":
		default:
			return nil, fmt.Errorf("questions[%d].options[%d].resources.%s is not supported", questionIndex, optionIndex, key)
		}
	}
	cpu, err := requiredAskUserDecisionText(raw, questionIndex, optionIndex, "cpu", 1, 100)
	if err != nil {
		return nil, err
	}
	memory, err := requiredAskUserDecisionText(raw, questionIndex, optionIndex, "memory", 1, 100)
	if err != nil {
		return nil, err
	}
	gpu, err := requiredAskUserDecisionText(raw, questionIndex, optionIndex, "gpu", 1, 100)
	if err != nil {
		return nil, err
	}
	return &askUserResourceProfile{CPU: cpu, Memory: memory, GPU: gpu}, nil
}

func askUserResourceProfileMetadataValue(value any) *askUserResourceProfile {
	raw, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	cpu := strings.TrimSpace(stringValue(raw["cpu"]))
	memory := strings.TrimSpace(stringValue(raw["memory"]))
	gpu := strings.TrimSpace(stringValue(raw["gpu"]))
	if cpu == "" || memory == "" || gpu == "" {
		return nil
	}
	return &askUserResourceProfile{CPU: cpu, Memory: memory, GPU: gpu}
}

func askUserResourceProfileTexts(value any) []string {
	profile := askUserResourceProfileMetadataValue(value)
	if profile == nil {
		return nil
	}
	return []string{profile.CPU, profile.Memory, profile.GPU}
}

func requiredAskUserDecisionText(item map[string]any, questionIndex, optionIndex int, field string, minRunes, maxRunes int) (string, error) {
	value, ok := item[field].(string)
	value = strings.TrimSpace(value)
	if !ok || value == "" {
		return "", fmt.Errorf("questions[%d].options[%d].%s is required", questionIndex, optionIndex, field)
	}
	length := utf8.RuneCountInString(value)
	if length < minRunes {
		return "", fmt.Errorf("questions[%d].options[%d].%s must contain at least %d characters", questionIndex, optionIndex, field, minRunes)
	}
	if length > maxRunes {
		return "", fmt.Errorf("questions[%d].options[%d].%s must contain at most %d characters", questionIndex, optionIndex, field, maxRunes)
	}
	return value, nil
}

func askUserEvidenceReferencesValue(
	value any,
	questionIndex, optionIndex int,
	field string,
	minItems int,
) ([]string, error) {
	rawValues, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("questions[%d].options[%d].%s must be an array of exact evidence references", questionIndex, optionIndex, field)
	}
	if len(rawValues) < minItems {
		return nil, fmt.Errorf("questions[%d].options[%d].%s must contain at least %d exact evidence reference(s)", questionIndex, optionIndex, field, minItems)
	}
	if len(rawValues) > 8 {
		return nil, fmt.Errorf("questions[%d].options[%d].%s must contain at most 8 exact evidence references", questionIndex, optionIndex, field)
	}
	result := make([]string, 0, len(rawValues))
	seen := map[string]bool{}
	for evidenceIndex, rawValue := range rawValues {
		evidence, ok := rawValue.(string)
		evidence = strings.TrimSpace(evidence)
		length := utf8.RuneCountInString(evidence)
		if !ok || length < 3 || length > 180 || strings.ContainsAny(evidence, "\r\n") || seen[evidence] {
			return nil, fmt.Errorf("questions[%d].options[%d].%s[%d] is invalid", questionIndex, optionIndex, field, evidenceIndex)
		}
		seen[evidence] = true
		result = append(result, evidence)
	}
	return result, nil
}

func askUserSelectionBasisValue(value any, questionIndex, optionIndex int) (string, error) {
	basis, ok := value.(string)
	basis = strings.ToLower(strings.TrimSpace(basis))
	if !ok {
		return "", fmt.Errorf("questions[%d].options[%d].selection_basis is required", questionIndex, optionIndex)
	}
	switch basis {
	case "user_objective", "scientific_evidence", "execution_readiness", "balanced_tradeoff":
		return basis, nil
	default:
		return "", fmt.Errorf("questions[%d].options[%d].selection_basis must be user_objective, scientific_evidence, execution_readiness, or balanced_tradeoff", questionIndex, optionIndex)
	}
}

func askUserReadinessStatusValue(value any, questionIndex, optionIndex int) (string, error) {
	status, ok := value.(string)
	status = strings.ToLower(strings.TrimSpace(status))
	if !ok {
		return "", fmt.Errorf("questions[%d].options[%d].readiness_status is required", questionIndex, optionIndex)
	}
	switch status {
	case "not_applicable", "unverified", "configured", "verified_ready":
		return status, nil
	default:
		return "", fmt.Errorf("questions[%d].options[%d].readiness_status must be not_applicable, unverified, configured, or verified_ready", questionIndex, optionIndex)
	}
}

func askUserDecisionDescription(
	question, description, readinessStatus string,
	resources *askUserResourceProfile,
) string {
	if sessionRunnerResponseLanguage(question) == "zh" {
		lines := []string{"方案：" + description}
		if resources == nil {
			return lines[0]
		}
		resourceLabel := "资源："
		if readinessStatus == "unverified" {
			resourceLabel = "资源待核验："
		}
		return strings.Join(append(lines, resourceLabel+
			"CPU："+resources.CPU+" · 内存："+resources.Memory+" · GPU："+resources.GPU), "\n")
	}
	lines := []string{"Route: " + description}
	if resources == nil {
		return lines[0]
	}
	resourceLabel := "Resources: "
	if readinessStatus == "unverified" {
		resourceLabel = "Resources to verify: "
	}
	return strings.Join(append(lines, resourceLabel+
		"CPU: "+resources.CPU+" · Memory: "+resources.Memory+" · GPU: "+resources.GPU), "\n")
}

func askUserReadinessStatusLabel(status string, chinese bool) string {
	if chinese {
		switch status {
		case "not_applicable":
			return "不适用"
		case "unverified":
			return "未核验"
		case "configured":
			return "已配置"
		case "verified_ready":
			return "已核验可用"
		}
	}
	switch status {
	case "not_applicable":
		return "Not applicable"
	case "unverified":
		return "Unverified"
	case "configured":
		return "Configured"
	case "verified_ready":
		return "Verified ready"
	default:
		return "Unknown"
	}
}

func askUserReadinessDisplayText(status string, chinese bool) string {
	switch status {
	case "not_applicable":
		if chinese {
			return "无需运行环境预检。"
		}
		return "No execution-environment preflight is needed."
	case "unverified":
		if chinese {
			return "尚未完成运行预检；选择后先验证所需环境或服务。"
		}
		return "The required environment or service has not been preflighted yet."
	case "configured":
		if chinese {
			return "所需环境或服务已配置；首次运行仍需验证。"
		}
		return "The environment or service is configured; representative execution is still pending."
	case "verified_ready":
		if chinese {
			return "已通过当前任务的运行预检或代表性验证。"
		}
		return "A current-task preflight or representative run has succeeded."
	default:
		return ""
	}
}

func stringRecordValue(value any, field string) (map[string]string, error) {
	result := map[string]string{}
	if value == nil {
		return result, nil
	}
	raw, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an object", field)
	}
	for key, rawValue := range raw {
		text, ok := rawValue.(string)
		if !ok {
			return nil, fmt.Errorf("%s.%s must be a string", field, key)
		}
		result[key] = text
	}
	return result, nil
}

func annotationRecordValue(value any) (map[string]map[string]string, error) {
	if value == nil {
		return nil, nil
	}
	raw, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("annotations must be an object")
	}
	result := map[string]map[string]string{}
	for question, rawAnnotation := range raw {
		annotation, ok := rawAnnotation.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("annotations.%s must be an object", question)
		}
		cleaned := map[string]string{}
		for _, field := range []string{"preview", "notes"} {
			if rawValue, exists := annotation[field]; exists {
				text, ok := rawValue.(string)
				if !ok {
					return nil, fmt.Errorf("annotations.%s.%s must be a string", question, field)
				}
				cleaned[field] = text
			}
		}
		result[question] = cleaned
	}
	return result, nil
}

func todoListValue(value any) ([]todoItem, error) {
	rawItems, ok := value.([]any)
	if !ok {
		if typed, ok := value.([]todoItem); ok {
			return append([]todoItem(nil), typed...), nil
		}
		return nil, errors.New("todos must be an array")
	}
	todos := make([]todoItem, 0, len(rawItems))
	for index, rawItem := range rawItems {
		item, ok := rawItem.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("todos[%d] must be an object", index)
		}
		for field := range item {
			switch field {
			case "content", "status", "activeForm":
			default:
				return nil, fmt.Errorf("todos[%d].%s is unsupported", index, field)
			}
		}
		content, contentOK := item["content"].(string)
		status, statusOK := item["status"].(string)
		activeForm, activeFormOK := item["activeForm"].(string)
		if !contentOK {
			return nil, fmt.Errorf("todos[%d].content must be a string", index)
		}
		if !statusOK {
			return nil, fmt.Errorf("todos[%d].status must be a string", index)
		}
		if !activeFormOK {
			return nil, fmt.Errorf("todos[%d].activeForm must be a string", index)
		}
		todo := todoItem{
			Content:    strings.TrimSpace(content),
			Status:     strings.TrimSpace(status),
			ActiveForm: strings.TrimSpace(activeForm),
		}
		if todo.Content == "" {
			return nil, fmt.Errorf("todos[%d].content is required", index)
		}
		if todo.ActiveForm == "" {
			return nil, fmt.Errorf("todos[%d].activeForm is required", index)
		}
		if !validTodoStatus(todo.Status) {
			return nil, fmt.Errorf("todos[%d].status is unsupported: %s", index, todo.Status)
		}
		todos = append(todos, todo)
	}
	return todos, nil
}

func validTodoStatus(status string) bool {
	switch status {
	case "pending", "in_progress", "completed":
		return true
	default:
		return false
	}
}

func taskCreateOptions(input map[string]any) taskstore.CreateOptions {
	return taskstore.CreateOptions{
		Subject:     stringValue(input["subject"]),
		Description: stringValue(input["description"]),
		ActiveForm:  stringValue(input["activeForm"]),
		Status:      "pending",
		Owner:       stringValue(input["owner"]),
		Blocks:      stringArrayValue(input["blocks"]),
		BlockedBy:   stringArrayValue(input["blockedBy"]),
		Metadata:    mapValue(input["metadata"]),
	}
}

func taskUpdateOptions(input map[string]any) (taskstore.UpdateOptions, error) {
	status := stringPtrValue(input, "status")
	if status != nil && *status == "deleted" {
		return taskstore.UpdateOptions{Delete: true}, nil
	}
	return taskstore.UpdateOptions{
		Subject:      stringPtrValue(input, "subject"),
		Description:  stringPtrValue(input, "description"),
		ActiveForm:   stringPtrValue(input, "activeForm"),
		Status:       status,
		Owner:        stringPtrValue(input, "owner"),
		Metadata:     mapValue(input["metadata"]),
		MetadataSet:  input["metadata"] != nil,
		AddBlocks:    stringArrayValue(input["addBlocks"]),
		AddBlockedBy: stringArrayValue(input["addBlockedBy"]),
	}, nil
}

func originalTaskDetail(task taskstore.Task) map[string]any {
	return map[string]any{
		"id":          task.ID,
		"subject":     task.Subject,
		"description": task.Description,
		"status":      task.Status,
		"blocks":      task.Blocks,
		"blockedBy":   task.BlockedBy,
	}
}

func configHasModelDefaultFormat(specs map[string]configSettingSpec) bool {
	spec, ok := specs["model"]
	return ok && spec.Type == "string" && spec.DefaultValue == "default"
}

func configRemoteDefaultResolvesUnset(specs map[string]configSettingSpec) bool {
	spec, ok := specs["remoteControlAtStartup"]
	return ok && spec.Type == "boolean" && stringSliceContains(spec.Options, "default") && defaultRemoteControlAtStartup() == false
}

func secretDiagnosticRedactionReady() bool {
	return redactDiagnosticSecretSource("sk-live-redaction-check") == "<redacted>" &&
		redactDiagnosticSecretSource("Bearer token") == "<redacted>" &&
		redactDiagnosticSecretSource("env:SYNON_RUNNER_CHAT_API_KEY") == "env:SYNON_RUNNER_CHAT_API_KEY" &&
		redactDiagnosticSecretSource("") == "none"
}

func supportedConfigSettings() map[string]configSettingSpec {
	return map[string]configSettingSpec{
		"theme":                        {Type: "string", Options: []string{"auto", "dark", "light", "light-daltonized", "dark-daltonized", "light-ansi", "dark-ansi"}},
		"editorMode":                   {Type: "string", Options: []string{"normal", "vim"}},
		"verbose":                      {Type: "boolean"},
		"preferredNotifChannel":        {Type: "string", Options: []string{"auto", "iterm2", "iterm2_with_bell", "terminal_bell", "kitty", "ghostty", "notifications_disabled"}},
		"autoCompactEnabled":           {Type: "boolean"},
		"autoCompactTokenThreshold":    {Type: "number"},
		"autoDreamEnabled":             {Type: "boolean"},
		"fileCheckpointingEnabled":     {Type: "boolean"},
		"showTurnDuration":             {Type: "boolean"},
		"terminalProgressBarEnabled":   {Type: "boolean"},
		"todoFeatureEnabled":           {Type: "boolean"},
		"model":                        {Type: "string", DefaultValue: "default"},
		"alwaysThinkingEnabled":        {Type: "boolean"},
		"permissions.defaultMode":      {Type: "string", Options: []string{"default", "plan", "acceptEdits", "dontAsk", "auto"}},
		"language":                     {Type: "string"},
		"teammateMode":                 {Type: "string", Options: []string{"auto", "tmux", "in-process"}},
		"classifierPermissionsEnabled": {Type: "boolean"},
		"remoteControlAtStartup":       {Type: "boolean", Options: []string{"default"}},
		"taskCompleteNotifEnabled":     {Type: "boolean"},
		"inputNeededNotifEnabled":      {Type: "boolean"},
		"agentPushNotifEnabled":        {Type: "boolean"},
	}
}

func validateConfigValue(setting string, spec configSettingSpec, value any) (any, error) {
	switch spec.Type {
	case "boolean":
		if text, ok := value.(string); ok {
			switch strings.ToLower(strings.TrimSpace(text)) {
			case "true":
				return true, nil
			case "false":
				return false, nil
			case "default":
				if stringSliceContains(spec.Options, "default") {
					return "default", nil
				}
			}
		}
		if boolean, ok := value.(bool); ok {
			return boolean, nil
		}
		return nil, fmt.Errorf("%s requires true or false.", setting)
	case "string":
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("%s requires a string.", setting)
		}
		if len(spec.Options) > 0 && !stringSliceContains(spec.Options, text) {
			return nil, fmt.Errorf("Invalid value %q. Options: %s", text, strings.Join(spec.Options, ", "))
		}
		return text, nil
	case "number":
		switch typed := value.(type) {
		case int:
			if typed <= 0 {
				return nil, fmt.Errorf("%s requires a positive number.", setting)
			}
			return typed, nil
		case int64:
			if typed <= 0 {
				return nil, fmt.Errorf("%s requires a positive number.", setting)
			}
			return typed, nil
		case float64:
			if typed <= 0 {
				return nil, fmt.Errorf("%s requires a positive number.", setting)
			}
			return typed, nil
		case string:
			parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
			if err != nil || parsed <= 0 {
				return nil, fmt.Errorf("%s requires a positive number.", setting)
			}
			return parsed, nil
		default:
			return nil, fmt.Errorf("%s requires a positive number.", setting)
		}
	default:
		return value, nil
	}
}

func stringSliceContains(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func stringPtrValue(input map[string]any, key string) *string {
	value, ok := input[key]
	if !ok || value == nil {
		return nil
	}
	text := stringValue(value)
	return &text
}

func todosAllCompleted(todos []todoItem) bool {
	for _, todo := range todos {
		if todo.Status != "completed" {
			return false
		}
	}
	return true
}

func todoScopeKey(agentID string, sessionID string) string {
	scope := strings.TrimSpace(agentID)
	if scope != "" {
		scope = "agent:" + scope
	} else if session := strings.TrimSpace(sessionID); session != "" {
		scope = "session:" + session
	} else {
		return "default"
	}
	sum := sha256.Sum256([]byte(scope))
	return "scope-" + hex.EncodeToString(sum[:8])
}

func sleepDuration(input map[string]any) (time.Duration, error) {
	durationMs := firstNumber(input, "durationMs", "duration_ms", "milliseconds", "ms")
	if durationMs <= 0 {
		if seconds := firstNumber(input, "seconds"); seconds > 0 {
			durationMs = seconds * 1000
		}
	}
	if durationMs <= 0 {
		if duration := firstNumber(input, "duration"); duration > 0 {
			switch strings.ToLower(strings.TrimSpace(stringValue(input["unit"]))) {
			case "", "s", "sec", "secs", "second", "seconds":
				durationMs = duration * 1000
			case "ms", "millisecond", "milliseconds":
				durationMs = duration
			default:
				return 0, fmt.Errorf("unsupported sleep unit: %s", stringValue(input["unit"]))
			}
		}
	}
	if durationMs <= 0 {
		return 0, errors.New("Sleep.durationMs, seconds, or duration is required")
	}
	if durationMs > 5*60*1000 {
		return 0, errors.New("Sleep duration must be at most 300000 milliseconds")
	}
	return time.Duration(durationMs) * time.Millisecond, nil
}

func firstNumber(input map[string]any, keys ...string) int64 {
	for _, key := range keys {
		if value, ok := input[key]; ok {
			if number := numberValue(value); number > 0 {
				return number
			}
		}
	}
	return 0
}

func stringArrayValue(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}

func anySliceValue(value any) []any {
	switch typed := value.(type) {
	case []any:
		return append([]any(nil), typed...)
	case []map[string]any:
		result := make([]any, 0, len(typed))
		for _, item := range typed {
			result = append(result, item)
		}
		return result
	case []string:
		result := make([]any, 0, len(typed))
		for _, item := range typed {
			result = append(result, item)
		}
		return result
	default:
		return nil
	}
}

func objectMapValue(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = item
		}
		return result
	default:
		return map[string]any{}
	}
}

func numberValue(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	default:
		return 0
	}
}

func boolValue(value any, fallback bool) bool {
	if typed, ok := value.(bool); ok {
		return typed
	}
	return fallback
}
