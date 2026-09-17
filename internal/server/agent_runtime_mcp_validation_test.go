package server

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	kernelruntime "synon-go/internal/kernel"
	"synon-go/internal/skills"
	"synon-go/internal/software"
)

func TestRuntimeMCPContextRequiresExactNamesTypesAndBoundedRepair(t *testing.T) {
	context := runtimeMCPContext([]agentruntime.ToolSchema{{
		Name: "mcp__chembl__drug_search",
		Parameters: map[string]any{"type": "object", "required": []any{"query"}, "properties": map[string]any{
			"query":     map[string]any{"type": "string"},
			"max_phase": map[string]any{"type": "integer"},
		}},
	}}, nil)
	for _, required := range []string{
		"exact tool name", "numbers are unquoted JSON numbers", "arrays are JSON arrays",
		"invalid_tool_arguments", "do not repeat the same invalid arguments",
	} {
		if !strings.Contains(context, required) {
			t.Fatalf("MCP context missing %q: %s", required, context)
		}
	}
	for _, duplicated := range []string{"max_phase:integer", "query:string!", "description="} {
		if strings.Contains(context, duplicated) {
			t.Fatalf("MCP context duplicated JSON Schema detail %q: %s", duplicated, context)
		}
	}
}

func TestRuntimeMCPContextListsAllAdmittedNamesAndOnlyAnnotatesSkillSelections(t *testing.T) {
	context := runtimeMCPContext([]agentruntime.ToolSchema{
		{Name: "mcp__chembl__drug_search", Description: "already in JSON Schema"},
		{Name: "mcp__pubmed__search", Description: "must not be repeated"},
	}, []skills.Skill{{Name: "chemistry", Tools: []string{"mcp__chembl__drug_search"}}})
	if !strings.Contains(context, "mcp__chembl__drug_search | selectedBySkill=chemistry") {
		t.Fatalf("selected tool annotation missing: %s", context)
	}
	if !strings.Contains(context, "- mcp__pubmed__search") || strings.Contains(context, "mcp__pubmed__search | selectedBySkill=") ||
		strings.Contains(context, "already in JSON Schema") {
		t.Fatalf("admitted tool names or annotations are inconsistent: %s", context)
	}
}

func TestAgentRuntimeMCPArgumentsRequireExactJSONTypes(t *testing.T) {
	schema := agentruntime.ToolSchema{
		Name: "mcp__chembl__drug_search",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"max_phase": map[string]any{"type": []any{"integer", "null"}},
				"sections":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
			"additionalProperties": false,
		},
	}
	validator := compileAgentRuntimeMCPValidator(schema)

	for _, test := range []struct {
		name  string
		input map[string]any
		path  string
		want  []string
	}{
		{name: "quoted integer", input: map[string]any{"max_phase": "4"}, path: "/max_phase", want: []string{"integer", "null"}},
		{name: "encoded array", input: map[string]any{"sections": `["indications"]`}, path: "/sections", want: []string{"array"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := validator.Validate(test.input)
			if result == nil || result["code"] != "invalid_tool_arguments" || result["retryable"] != true {
				t.Fatalf("validation result = %#v", result)
			}
			issues, _ := result["issues"].([]map[string]any)
			if len(issues) == 0 || issues[0]["path"] != test.path || !reflect.DeepEqual(issues[0]["expected"], test.want) {
				t.Fatalf("issues = %#v", issues)
			}
			if _, leaked := issues[0]["value"]; leaked {
				t.Fatalf("validation issue leaked input value: %#v", issues[0])
			}
			contract := result["expectedArguments"].(map[string]any)
			if fields := contract["fields"].(map[string]any); fields[test.path[1:]] == nil {
				t.Fatalf("validation result omitted the bounded repair contract: %#v", result)
			}
		})
	}
	if result := validator.Validate(map[string]any{"max_phase": float64(4), "sections": []any{"indications"}}); result != nil {
		t.Fatalf("valid exact JSON types rejected: %#v", result)
	}
}

func TestAgentRuntimeMCPValidatorFailsClosedOnInvalidSchema(t *testing.T) {
	validator := compileAgentRuntimeMCPValidator(agentruntime.ToolSchema{
		Name:       "mcp__fixture__broken",
		Parameters: map[string]any{"$schema": "https://json-schema.org/draft/2020-12/schema"},
	})
	result := validator.Validate(map[string]any{})
	if result == nil || result["code"] != "tool_schema_invalid" || result["retryable"] != false {
		t.Fatalf("invalid schema result = %#v", result)
	}
}

func TestAgentRuntimeMCPValidationNamesMissingRequiredProperties(t *testing.T) {
	validator := compileAgentRuntimeMCPValidator(agentruntime.ToolSchema{
		Name: "mcp__chembl__compound_search",
		Parameters: map[string]any{
			"type": "object", "required": []any{"query"},
			"properties": map[string]any{"query": map[string]any{"type": "string", "minLength": 1}},
		},
	})
	result := validator.Validate(map[string]any{"chembl_id": "CHEMBL203"})
	issues, _ := result["issues"].([]map[string]any)
	if len(issues) != 1 || issues[0]["path"] != "/query" || issues[0]["keyword"] != "required" || issues[0]["actual"] != "missing" {
		t.Fatalf("missing required issue = %#v", issues)
	}
	if !reflect.DeepEqual(issues[0]["expected"], []string{"string"}) {
		t.Fatalf("missing required expected types = %#v", issues[0]["expected"])
	}
	if _, leaked := issues[0]["value"]; leaked {
		t.Fatalf("missing required issue leaked input value: %#v", issues[0])
	}
}

func TestAgentRuntimeMCPToolSuggestionUsesOnlyUniqueSnapshotMatch(t *testing.T) {
	schemas := []agentruntime.ToolSchema{
		{Name: "mcp__drug-regulatory__search_drug_labels"},
		{Name: "mcp__chembl__drug_search"},
		{Name: "binding_mode_analysis"},
	}
	if got := agentRuntimeMCPToolSuggestion("mcp__chembl__search_drug_labels", schemas); got != "mcp__drug-regulatory__search_drug_labels" {
		t.Fatalf("unique suggestion = %q", got)
	}
	if got := agentRuntimeMCPToolSuggestion("mcp__unknown__search", append(schemas,
		agentruntime.ToolSchema{Name: "mcp__pubmed__search"},
		agentruntime.ToolSchema{Name: "mcp__chembl__search"},
	)); got != "" {
		t.Fatalf("ambiguous suggestion = %q", got)
	}
	if got := agentRuntimeMCPToolSuggestion("mcp__structures-interactions__binding_mode_analysis", schemas); got != "binding_mode_analysis" {
		t.Fatalf("native exact-terminal suggestion = %q", got)
	}
}

func TestAgentRuntimeGatewayValidatesNativeSnapshotSchemaBeforeExecution(t *testing.T) {
	schema := agentruntime.ToolSchema{
		Name: "strict_native_tool",
		Parameters: map[string]any{
			"type": "object", "required": []any{"language"},
			"properties": map[string]any{"language": map[string]any{"type": "string", "minLength": 1}},
		},
	}
	gateway := serverAgentRuntimeToolGateway{
		server: New(Options{FileRoot: t.TempDir()}), toolSchemas: []agentruntime.ToolSchema{schema},
		toolValidators: agentRuntimeToolValidators([]agentruntime.ToolSchema{schema}), hasToolSnapshot: true,
	}
	result, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID: "native-invalid-before-execution", Name: schema.Name, Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	value, _ := result.Value.(map[string]any)
	if value["code"] != "invalid_tool_arguments" {
		t.Fatalf("native validation result = %#v", result.Value)
	}
}

func TestAgentRuntimeGatewayAdmissionCheckerRecognizesCorrectedArguments(t *testing.T) {
	schema := agentruntime.ToolSchema{
		Name: "web_fetch",
		Parameters: map[string]any{
			"type": "object", "required": []any{"url"}, "additionalProperties": false,
			"properties": map[string]any{"url": map[string]any{"type": "string"}},
		},
	}
	gateway := serverAgentRuntimeToolGateway{
		server: New(Options{FileRoot: t.TempDir()}), toolSchemas: []agentruntime.ToolSchema{schema},
		toolValidators: agentRuntimeToolValidators([]agentruntime.ToolSchema{schema}), hasToolSnapshot: true,
	}
	if gateway.AdmitsToolCall(agentruntime.ToolCall{
		Name: schema.Name, Arguments: json.RawMessage(`{"url":"https://example.test","prompt":"extract"}`),
	}) {
		t.Fatal("admission checker accepted arguments outside the admitted schema")
	}
	if !gateway.AdmitsToolCall(agentruntime.ToolCall{
		Name: schema.Name, Arguments: json.RawMessage(`{"url":"https://example.test"}`),
	}) {
		t.Fatal("admission checker rejected corrected arguments")
	}
	if !gateway.AdmitsToolCall(agentruntime.ToolCall{
		Name: schema.Name, Arguments: json.RawMessage(`{"url":"https://example.test","human_description":"Fetching official label"}`),
	}) {
		t.Fatal("admission checker rejected non-executable presentation metadata")
	}
}

func TestAgentRuntimeGatewayNormalizesEmptyUnknownPropertiesAndRedundantLocaleCompanions(t *testing.T) {
	srv := New(Options{FileRoot: t.TempDir()})
	var schema agentruntime.ToolSchema
	for _, candidate := range srv.agentRuntimeToolSchemas([]string{"TodoWrite"}) {
		if candidate.Name == "TodoWrite" {
			schema = candidate
			break
		}
	}
	if schema.Name == "" {
		t.Fatal("TodoWrite schema was not exposed")
	}
	gateway := serverAgentRuntimeToolGateway{
		server: srv, toolSchemas: []agentruntime.ToolSchema{schema},
		toolValidators: agentRuntimeToolValidators([]agentruntime.ToolSchema{schema}), hasToolSnapshot: true,
	}
	input := map[string]any{"todos": []any{map[string]any{
		"content": "获取茶碱药代参数", "status": "in_progress", "activeForm": "正在获取茶碱药代参数",
		"status2": "", "emptyList": []any{}, "emptyObject": map[string]any{},
		"status_zh": "", "activeForm_zh": "正在获取茶碱药代参数", "content_en": nil,
	}}}
	normalized := gateway.normalizeAdmittedToolArguments(schema.Name, input)
	todos, _ := normalized["todos"].([]any)
	item, _ := todos[0].(map[string]any)
	if _, found := item["status_zh"]; found {
		t.Fatalf("empty status locale companion was preserved: %#v", item)
	}
	if _, found := item["activeForm_zh"]; found {
		t.Fatalf("duplicate activeForm locale companion was preserved: %#v", item)
	}
	if _, found := item["content_en"]; found {
		t.Fatalf("null content locale companion was preserved: %#v", item)
	}
	for _, name := range []string{"status2", "emptyList", "emptyObject"} {
		if _, found := item[name]; found {
			t.Fatalf("empty unknown property %q was preserved: %#v", name, item)
		}
	}
	if value := gateway.validateAdmittedToolArguments(schema.Name, normalized); value != nil {
		t.Fatalf("normalized TodoWrite arguments rejected = %#v", value)
	}
	originalItem := input["todos"].([]any)[0].(map[string]any)
	if _, found := originalItem["status_zh"]; !found {
		t.Fatal("normalization mutated the caller input")
	}
	if !gateway.AdmitsToolCall(agentruntime.ToolCall{Name: schema.Name, Arguments: json.RawMessage(`{
		"todos":[{"content":"获取参数","status":"in_progress","status2":"","status_zh":"","activeForm":"正在获取参数","activeForm_zh":""}]
	}`)}) {
		t.Fatal("central admission path rejected empty model placeholders")
	}

	for name, arguments := range map[string]string{
		"non-empty unknown field":                 `{"todos":[{"content":"x","status":"pending","activeForm":"doing","extra":"unexpected"}]}`,
		"non-empty locale companion":              `{"todos":[{"content":"x","status":"pending","status_zh":"待处理","activeForm":"doing"}]}`,
		"non-empty locale companion without base": `{"todos":[{"content":"x","status":"pending","activeForm":"doing","missing_zh":"unexpected"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if gateway.AdmitsToolCall(agentruntime.ToolCall{Name: schema.Name, Arguments: json.RawMessage(arguments)}) {
				t.Fatalf("strict schema admitted %s", arguments)
			}
		})
	}
}

func TestAgentRuntimeGatewayPreservesExplicitlyAdmittedLocaleFields(t *testing.T) {
	schema := agentruntime.ToolSchema{Name: "localized_tool", Parameters: map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"label":    map[string]any{"type": "string"},
			"label_zh": map[string]any{"type": "string"},
		},
	}}
	gateway := serverAgentRuntimeToolGateway{
		toolSchemas:    []agentruntime.ToolSchema{schema},
		toolValidators: agentRuntimeToolValidators([]agentruntime.ToolSchema{schema}), hasToolSnapshot: true,
	}
	normalized := gateway.normalizeAdmittedToolArguments(schema.Name, map[string]any{"label": "A", "label_zh": ""})
	if value, found := normalized["label_zh"]; !found || value != "" {
		t.Fatalf("explicitly admitted locale field was changed: %#v", normalized)
	}
	if value := gateway.validateAdmittedToolArguments(schema.Name, normalized); value != nil {
		t.Fatalf("explicitly admitted locale field rejected = %#v", value)
	}
}

func TestAgentRuntimeGatewaySynthesizesMissingRequiredPresentationMetadata(t *testing.T) {
	schema := agentruntime.ToolSchema{Name: "native_operation", Parameters: map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"mode":              map[string]any{"type": "string"},
			"human_description": map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
		},
		"required": []string{"mode", "human_description"},
	}}
	gateway := serverAgentRuntimeToolGateway{
		toolSchemas:     []agentruntime.ToolSchema{schema},
		toolValidators:  agentRuntimeToolValidators([]agentruntime.ToolSchema{schema}),
		hasToolSnapshot: true,
	}

	input := map[string]any{"mode": "list"}
	normalized := gateway.normalizeAdmittedToolArguments(schema.Name, input)
	if strings.TrimSpace(stringValue(normalized["human_description"])) == "" {
		t.Fatalf("missing presentation metadata was not synthesized: %#v", normalized)
	}
	if _, found := input["human_description"]; found {
		t.Fatal("normalization mutated the original model input")
	}
	if value := gateway.validateAdmittedToolArguments(schema.Name, normalized); value != nil {
		t.Fatalf("executable arguments were rejected for missing presentation metadata: %#v", value)
	}
	missingMode := gateway.normalizeAdmittedToolArguments(schema.Name, map[string]any{})
	if value := gateway.validateAdmittedToolArguments(schema.Name, missingMode); value == nil {
		t.Fatal("presentation recovery admitted a call missing executable semantics")
	}
}

func TestAgentRuntimeGatewayNormalizesUnambiguousSaveArtifactShapes(t *testing.T) {
	schema := agentSaveArtifactsToolSchema()
	gateway := serverAgentRuntimeToolGateway{
		toolSchemas: []agentruntime.ToolSchema{schema}, toolValidators: agentRuntimeToolValidators([]agentruntime.ToolSchema{schema}),
		hasToolSnapshot: true,
	}

	for name, input := range map[string]map[string]any{
		"file descriptors": {
			"files": []any{
				map[string]any{"file_path": "report.md", "description": "Report"},
				map[string]any{"path": "evidence.csv", "description": "Evidence"},
			},
			"human_description": "Saving generated files",
		},
		"path map": {
			"report.md": "Report", "evidence.csv": "Evidence",
		},
	} {
		t.Run(name, func(t *testing.T) {
			normalized := gateway.normalizeAdmittedToolArguments(schema.Name, input)
			files := stringValueSlice(normalized["files"])
			if len(files) != 2 || stringValue(normalized["language"]) != "text" ||
				strings.TrimSpace(stringValue(normalized["human_description"])) == "" {
				t.Fatalf("save normalization=%#v", normalized)
			}
			if value := gateway.validateAdmittedToolArguments(schema.Name, normalized); value != nil {
				t.Fatalf("normalized save call rejected=%#v", value)
			}
		})
	}

	ambiguous := gateway.normalizeAdmittedToolArguments(schema.Name, map[string]any{"human_description": "Saving"})
	if value := gateway.validateAdmittedToolArguments(schema.Name, ambiguous); value == nil {
		t.Fatal("ambiguous non-file object was converted into a save request")
	}

	inlineDocument := map[string]any{
		"path": "report/assessment.md", "encoding": "UTF-8",
		"markdown": strings.Repeat("# Inline document body\n", 32),
	}
	normalizedInline := gateway.normalizeAdmittedToolArguments(schema.Name, inlineDocument)
	if _, converted := normalizedInline["files"]; converted {
		t.Fatalf("inline document payload was misclassified as a path map: %#v", normalizedInline)
	}
	if value := gateway.validateAdmittedToolArguments(schema.Name, normalizedInline); value == nil {
		t.Fatal("inline document payload bypassed the canonical save_artifacts schema")
	}
}

func TestAgentRuntimeProductionNativeSchemasCompileAndRejectMissingRequiredInput(t *testing.T) {
	schemas := []agentruntime.ToolSchema{agentSaveArtifactsToolSchema(), agentKernelBashToolSchema()}
	schemas = append(schemas, agentEnvironmentManagementToolSchemas()...)
	validators := agentRuntimeToolValidators(schemas)
	for _, toolName := range []string{"save_artifacts", "bash", manageEnvironmentsToolName, managePackagesToolName} {
		validator, found := validators[toolName]
		if !found {
			t.Fatalf("production schema %q was not admitted: %#v", toolName, agentRuntimeToolSchemaNames(schemas))
		}
		if validator.compileErr != nil {
			t.Fatalf("production schema %q did not compile: %v", toolName, validator.compileErr)
		}
		result := validator.Validate(map[string]any{})
		if result == nil || result["code"] != "invalid_tool_arguments" {
			t.Fatalf("production schema %q accepted missing required input: %#v", toolName, result)
		}
	}
}

func TestAgentRuntimeGatewayNormalizesUnambiguousModelArgumentsBeforeValidation(t *testing.T) {
	schema := agentruntime.ToolSchema{
		Name: "mcp__chembl__drug_search",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"max_phase": map[string]any{"type": "integer"},
		}},
	}
	gateway := serverAgentRuntimeToolGateway{
		server: New(Options{FileRoot: t.TempDir()}), toolSchemas: []agentruntime.ToolSchema{schema},
		toolValidators: agentRuntimeToolValidators([]agentruntime.ToolSchema{schema}), hasToolSnapshot: true,
	}
	normalized := gateway.normalizeAdmittedToolArguments(schema.Name, map[string]any{"max_phase": "4"})
	if normalized["max_phase"] != int64(4) {
		t.Fatalf("normalized arguments = %#v", normalized)
	}
	if value := gateway.validateAdmittedToolArguments(schema.Name, normalized); value != nil {
		t.Fatalf("normalized arguments rejected = %#v", value)
	}
}

func TestAgentRuntimeGatewayNormalizesEditFileFullWriteAliases(t *testing.T) {
	schema := agentWorkspaceEditFileToolSchema()
	gateway := serverAgentRuntimeToolGateway{
		toolSchemas: []agentruntime.ToolSchema{schema}, toolValidators: agentRuntimeToolValidators([]agentruntime.ToolSchema{schema}),
		hasToolSnapshot: true,
	}

	normalized := gateway.normalizeAdmittedToolArguments("edit_file", map[string]any{
		"path":              "validation.json",
		"human_description": "Writing validation record",
		"content": map[string]any{
			"input_checks":  map[string]any{"exists": true},
			"output_checks": map[string]any{"exists": true},
			"errors":        []any{},
		},
	})
	if normalized["file_path"] != "validation.json" || normalized["old_string"] != "" {
		t.Fatalf("normalized edit target = %#v", normalized)
	}
	content, ok := normalized["new_string"].(string)
	if !ok || !strings.Contains(content, `"input_checks"`) || !strings.HasSuffix(content, "\n") {
		t.Fatalf("normalized edit content = %#v", normalized["new_string"])
	}
	if _, legacy := normalized["path"]; legacy {
		t.Fatalf("normalized edit retained path alias: %#v", normalized)
	}
	if _, legacy := normalized["content"]; legacy {
		t.Fatalf("normalized edit retained content alias: %#v", normalized)
	}
	if value := gateway.validateAdmittedToolArguments("edit_file", normalized); value != nil {
		t.Fatalf("normalized edit arguments rejected = %#v", value)
	}
}

func TestAgentRuntimeGatewaySerializesCanonicalJSONEditContent(t *testing.T) {
	schema := agentWorkspaceEditFileToolSchema()
	gateway := serverAgentRuntimeToolGateway{
		toolSchemas: []agentruntime.ToolSchema{schema}, toolValidators: agentRuntimeToolValidators([]agentruntime.ToolSchema{schema}),
		hasToolSnapshot: true,
	}

	normalized := gateway.normalizeAdmittedToolArguments("edit_file", map[string]any{
		"file_path":         "validation.json",
		"old_string":        "",
		"human_description": "Writing validation record",
		"new_string":        map[string]any{"errors": []any{}, "checks": map[string]any{"valid": true}},
	})
	content, ok := normalized["new_string"].(string)
	if !ok || !json.Valid([]byte(content)) || !strings.Contains(content, `"checks"`) {
		t.Fatalf("canonical JSON edit content was not serialized: %#v", normalized)
	}
	if value := gateway.validateAdmittedToolArguments("edit_file", normalized); value != nil {
		t.Fatalf("serialized canonical JSON edit was rejected = %#v", value)
	}

	ambiguous := gateway.normalizeAdmittedToolArguments("edit_file", map[string]any{
		"file_path": "report.md", "old_string": "", "new_string": map[string]any{"title": "report"},
		"human_description": "Writing report",
	})
	if _, converted := ambiguous["new_string"].(string); converted {
		t.Fatalf("non-JSON canonical object was serialized: %#v", ambiguous)
	}
	if value := gateway.validateAdmittedToolArguments("edit_file", ambiguous); value == nil {
		t.Fatalf("non-JSON canonical object was admitted: %#v", ambiguous)
	}
}

func TestAgentRuntimeGatewayKeepsAmbiguousEditFileContentInvalid(t *testing.T) {
	schema := agentWorkspaceEditFileToolSchema()
	gateway := serverAgentRuntimeToolGateway{
		toolSchemas: []agentruntime.ToolSchema{schema}, toolValidators: agentRuntimeToolValidators([]agentruntime.ToolSchema{schema}),
		hasToolSnapshot: true,
	}

	normalized := gateway.normalizeAdmittedToolArguments("edit_file", map[string]any{
		"path": "report.md", "content": map[string]any{"unexpected": true}, "mode": "append",
	})
	if _, converted := normalized["new_string"]; converted {
		t.Fatalf("ambiguous non-JSON content was converted: %#v", normalized)
	}
	if value := gateway.validateAdmittedToolArguments("edit_file", normalized); value == nil {
		t.Fatalf("ambiguous edit arguments were admitted: %#v", normalized)
	}
}

func TestAgentRuntimeGatewayNormalizesNullSentinelOnlyForUnambiguousNullableSchema(t *testing.T) {
	schema := agentruntime.ToolSchema{
		Name: "mcp__chembl__get_bioactivity",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"activity_type": map[string]any{"type": []any{"null", "string"}, "enum": []any{"IC50", "Ki", nil}},
			"min_value":     map[string]any{"type": []any{"number", "null"}},
			"literal":       map[string]any{"type": []any{"string", "null"}},
			"enum_literal":  map[string]any{"enum": []any{"null", nil}},
		}},
	}
	gateway := serverAgentRuntimeToolGateway{
		toolSchemas:    []agentruntime.ToolSchema{schema},
		toolValidators: agentRuntimeToolValidators([]agentruntime.ToolSchema{schema}), hasToolSnapshot: true,
	}
	normalized := gateway.normalizeAdmittedToolArguments(schema.Name, map[string]any{
		"activity_type": "None",
		"min_value":     " none ",
		"literal":       "None",
		"enum_literal":  "null",
	})
	if normalized["activity_type"] != nil || normalized["min_value"] != nil ||
		normalized["literal"] != "None" || normalized["enum_literal"] != "null" {
		t.Fatalf("nullable normalization = %#v", normalized)
	}
	if value := gateway.validateAdmittedToolArguments(schema.Name, normalized); value != nil {
		t.Fatalf("normalized nullable arguments rejected = %#v", value)
	}
}

func TestAgentRuntimeGatewayNormalizesLowercaseNullAndPythonNoneEqually(t *testing.T) {
	schema := agentruntime.ToolSchema{Name: "mcp__structures-interactions__pdb_get_entities", Parameters: map[string]any{
		"type": "object", "required": []any{"pdb_id"}, "properties": map[string]any{
			"pdb_id":     map[string]any{"type": "string", "minLength": 4},
			"entity_ids": map[string]any{"type": []any{"array", "null"}, "items": map[string]any{"type": "string"}},
		},
	}}
	gateway := serverAgentRuntimeToolGateway{
		toolSchemas:    []agentruntime.ToolSchema{schema},
		toolValidators: agentRuntimeToolValidators([]agentruntime.ToolSchema{schema}), hasToolSnapshot: true,
	}
	for _, sentinel := range []string{"null", " NULL ", "None", " none "} {
		normalized := gateway.normalizeAdmittedToolArguments(schema.Name, map[string]any{
			"pdb_id": "6M0J", "entity_ids": sentinel,
		})
		if normalized["entity_ids"] != nil {
			t.Fatalf("sentinel %q normalized to %#v", sentinel, normalized)
		}
		if value := gateway.validateAdmittedToolArguments(schema.Name, normalized); value != nil {
			t.Fatalf("sentinel %q rejected after normalization: %#v", sentinel, value)
		}
	}
}

func TestAgentRuntimeGatewayNormalizesPDBSearchPythonNoneFilters(t *testing.T) {
	schema := agentruntime.ToolSchema{Name: "mcp__structures-interactions__pdb_search_structures", Parameters: map[string]any{
		"type": "object", "additionalProperties": false, "properties": map[string]any{
			"text":                    map[string]any{"type": []any{"string", "null"}},
			"uniprot_accession":       map[string]any{"type": []any{"string", "null"}},
			"organism":                map[string]any{"type": []any{"string", "null"}},
			"taxonomy_id":             map[string]any{"type": []any{"integer", "null"}},
			"experimental_method":     map[string]any{"type": []any{"string", "null"}},
			"max_resolution_angstrom": map[string]any{"type": []any{"number", "null"}},
			"ligand_comp_id":          map[string]any{"type": []any{"string", "null"}},
			"include_computed_models": map[string]any{"type": "boolean"},
			"max_rows":                map[string]any{"type": "integer"},
		},
	}}
	gateway := serverAgentRuntimeToolGateway{
		toolSchemas:    []agentruntime.ToolSchema{schema},
		toolValidators: agentRuntimeToolValidators([]agentruntime.ToolSchema{schema}), hasToolSnapshot: true,
	}
	input := map[string]any{
		"text": "cereblon", "uniprot_accession": "Q16637", "organism": "None",
		"taxonomy_id": "None", "experimental_method": "None", "max_resolution_angstrom": "None",
		"ligand_comp_id": "None", "include_computed_models": false, "max_rows": 20,
	}
	normalized := gateway.normalizeAdmittedToolArguments(schema.Name, input)
	for _, field := range []string{"organism", "taxonomy_id", "experimental_method", "max_resolution_angstrom", "ligand_comp_id"} {
		if normalized[field] != nil {
			t.Fatalf("PDB field %s normalized to %#v", field, normalized[field])
		}
	}
	if normalized["text"] != "cereblon" || normalized["uniprot_accession"] != "Q16637" {
		t.Fatalf("PDB concrete filters changed = %#v", normalized)
	}
	if value := gateway.validateAdmittedToolArguments(schema.Name, normalized); value != nil {
		t.Fatalf("normalized PDB arguments rejected = %#v", value)
	}
	if input["experimental_method"] != "None" {
		t.Fatal("PDB normalization mutated caller input")
	}
}

func TestAgentRuntimeGatewayNormalizesEncodedArrayOnlyForArraySchema(t *testing.T) {
	schema := agentruntime.ToolSchema{Name: "mcp__fixture__labels", Parameters: map[string]any{
		"type": "object", "properties": map[string]any{
			"sections": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"label":    map[string]any{"type": "string"},
		},
	}}
	gateway := serverAgentRuntimeToolGateway{
		toolSchemas:    []agentruntime.ToolSchema{schema},
		toolValidators: agentRuntimeToolValidators([]agentruntime.ToolSchema{schema}), hasToolSnapshot: true,
	}
	normalized := gateway.normalizeAdmittedToolArguments(schema.Name, map[string]any{
		"sections": `["indications_and_usage","mechanism_of_action"]`,
		"label":    `["must remain text"]`,
	})
	if !reflect.DeepEqual(normalized["sections"], []any{"indications_and_usage", "mechanism_of_action"}) || normalized["label"] != `["must remain text"]` {
		t.Fatalf("normalized arguments = %#v", normalized)
	}
}

func TestAgentRuntimeGatewayNormalizesClinicalTrialsLegacyArguments(t *testing.T) {
	schema := agentruntime.ToolSchema{Name: "mcp__clinical-trials__search_trials", Parameters: map[string]any{
		"type": "object", "additionalProperties": false, "properties": map[string]any{
			"condition": map[string]any{"type": "string"},
			"page_size": map[string]any{"type": "integer"},
			"status":    map[string]any{"type": []any{"array", "null"}, "items": map[string]any{"type": "string"}},
		},
	}}
	gateway := serverAgentRuntimeToolGateway{
		toolSchemas:    []agentruntime.ToolSchema{schema},
		toolValidators: agentRuntimeToolValidators([]agentruntime.ToolSchema{schema}), hasToolSnapshot: true,
	}
	input := map[string]any{
		"condition":   "KRAS G12C",
		"max_results": "15",
		"page_size":   15,
		"status":      `["RECRUITING"]`,
	}
	normalized := gateway.normalizeAdmittedToolArguments(schema.Name, input)
	pageSize, pageSizeOK := admittedIntegerArgument(normalized["page_size"])
	if _, found := normalized["max_results"]; found || !pageSizeOK || pageSize != 15 ||
		!reflect.DeepEqual(normalized["status"], []any{"RECRUITING"}) {
		t.Fatalf("normalized arguments = %#v", normalized)
	}
	if _, found := input["max_results"]; !found {
		t.Fatal("normalization mutated the caller input")
	}
	if value := gateway.validateAdmittedToolArguments(schema.Name, normalized); value != nil {
		t.Fatalf("normalized clinical-trials arguments rejected = %#v", value)
	}
	conflict := gateway.normalizeAdmittedToolArguments(schema.Name, map[string]any{
		"condition": "KRAS G12C", "page_size": 10, "max_results": "15",
	})
	conflictPageSize, conflictPageSizeOK := admittedIntegerArgument(conflict["page_size"])
	if _, found := conflict["max_results"]; found || !conflictPageSizeOK || conflictPageSize != 10 {
		t.Fatalf("canonical page_size did not win over the stale legacy alias: %#v", conflict)
	}
	if value := gateway.validateAdmittedToolArguments(schema.Name, conflict); value != nil {
		t.Fatalf("canonical clinical-trials arguments rejected after dropping stale alias: %#v", value)
	}
	maxRowsConflict := gateway.normalizeAdmittedToolArguments(schema.Name, map[string]any{
		"condition": "KRAS G12C", "page_size": 10, "max_rows": 30,
	})
	maxRowsPageSize, maxRowsPageSizeOK := admittedIntegerArgument(maxRowsConflict["page_size"])
	if _, found := maxRowsConflict["max_rows"]; found || !maxRowsPageSizeOK || maxRowsPageSize != 10 {
		t.Fatalf("canonical page_size did not win over max_rows: %#v", maxRowsConflict)
	}
	if value := gateway.validateAdmittedToolArguments(schema.Name, maxRowsConflict); value != nil {
		t.Fatalf("canonical arguments with max_rows rejected after normalization: %#v", value)
	}
	maxRowsOnly := gateway.normalizeAdmittedToolArguments(schema.Name, map[string]any{
		"condition": "KRAS G12C", "max_rows": "25",
	})
	maxRowsOnlyPageSize, maxRowsOnlyPageSizeOK := admittedIntegerArgument(maxRowsOnly["page_size"])
	if _, found := maxRowsOnly["max_rows"]; found || !maxRowsOnlyPageSizeOK || maxRowsOnlyPageSize != 25 {
		t.Fatalf("max_rows alias was not promoted to page_size: %#v", maxRowsOnly)
	}
}

func TestAgentRuntimeGatewayNormalizesClinicalTrialsLegacyArgumentsInsideMCPTool(t *testing.T) {
	schema := agentruntime.ToolSchema{Name: "MCPTool", Parameters: map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"server":    map[string]any{"type": "string"},
			"toolName":  map[string]any{"type": "string"},
			"arguments": map[string]any{"type": "object"},
		},
		"required": []string{"server", "toolName", "arguments"},
	}}
	gateway := serverAgentRuntimeToolGateway{
		toolSchemas:    []agentruntime.ToolSchema{schema},
		toolValidators: agentRuntimeToolValidators([]agentruntime.ToolSchema{schema}), hasToolSnapshot: true,
	}
	normalized := gateway.normalizeAdmittedToolArguments(schema.Name, map[string]any{
		"server": "clinical-trials", "toolName": "search_trials",
		"arguments": map[string]any{"condition": "Parkinson disease", "page_size": 20, "max_results": 30},
	})
	arguments := objectMapValue(normalized["arguments"])
	pageSize, pageSizeOK := admittedIntegerArgument(arguments["page_size"])
	if _, found := arguments["max_results"]; found || !pageSizeOK || pageSize != 20 {
		t.Fatalf("normalized MCPTool arguments = %#v", normalized)
	}
	if value := gateway.validateAdmittedToolArguments(schema.Name, normalized); value != nil {
		t.Fatalf("normalized MCPTool envelope rejected = %#v", value)
	}
	maxRowsNormalized := gateway.normalizeAdmittedToolArguments(schema.Name, map[string]any{
		"server": "clinical-trials", "toolName": "search_trials",
		"arguments": map[string]any{"condition": "Parkinson disease", "page_size": 10, "max_rows": 30},
	})
	maxRowsArguments := objectMapValue(maxRowsNormalized["arguments"])
	maxRowsPageSize, maxRowsPageSizeOK := admittedIntegerArgument(maxRowsArguments["page_size"])
	if _, found := maxRowsArguments["max_rows"]; found || !maxRowsPageSizeOK || maxRowsPageSize != 10 {
		t.Fatalf("normalized MCPTool max_rows arguments = %#v", maxRowsNormalized)
	}
}

func TestAgentRuntimeGatewayRejectsUnknownMCPNameWithUniqueSnapshotSuggestion(t *testing.T) {
	known := agentruntime.ToolSchema{Name: "mcp__drug-regulatory__search_drug_labels", Parameters: map[string]any{"type": "object"}}
	gateway := serverAgentRuntimeToolGateway{
		server: New(Options{FileRoot: t.TempDir()}), toolSchemas: []agentruntime.ToolSchema{known}, hasToolSnapshot: true,
	}
	result, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID: "wrong-namespace", Name: "mcp__chembl__search_drug_labels", Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	value, _ := result.Value.(map[string]any)
	if value["code"] != "tool_not_in_snapshot" || value["suggestedToolName"] != known.Name || value["retryable"] != false {
		t.Fatalf("unknown-name result = %#v", result.Value)
	}
}

func TestAgentRuntimeContactEmailErrorHasExplicitRecoveryWithoutInventedIdentity(t *testing.T) {
	result := agentRuntimeToolErrorValue(errors.New("NCBI etiquette requires a contact email address"))
	if result["code"] != "contact_email_required" || result["retryable"] != false || result["recovery"] != "configure_contact_email" {
		t.Fatalf("contact recovery result = %#v", result)
	}
	if _, found := result["contactEmail"]; found {
		t.Fatalf("contact recovery invented an identity: %#v", result)
	}
}

func TestAgentRuntimeSecureFetchRedirectErrorIsNonRetryable(t *testing.T) {
	result := agentRuntimeToolErrorValue(errors.New("secure_fetch_redirect_denied"))
	if result["code"] != "secure_fetch_redirect_denied" || result["retryable"] != false ||
		!strings.Contains(stringValue(result["recovery"]), "do not retry") {
		t.Fatalf("secure fetch recovery result = %#v", result)
	}
}

func TestAgentRuntimeReadFileScopeErrorReturnsSameTaskRecovery(t *testing.T) {
	for _, message := range []string{
		"workspace file path is outside the authorized workspace",
		"workspace file path is invalid",
	} {
		result, recoverable := agentRuntimeRecoverableToolErrorValue(
			"read_file", nil, errors.New(message),
		)
		if !recoverable || result["ok"] != false || result["executed"] != false ||
			result["code"] != "workspace_file_scope" || result["retryable"] != true ||
			!strings.Contains(stringValue(result["recovery"]), "version_id") {
			t.Fatalf("read-file scope recovery message=%q result=%#v recoverable=%t", message, result, recoverable)
		}
	}
}

func TestAgentRuntimeScientificHostErrorCarriesSameTaskRepairContract(t *testing.T) {
	result := agentRuntimeToolErrorValue(kernelruntime.NewHostCallError(
		"invalid_arguments",
		"scientific docking input is not prepared PDBQT; Synon must prepare the source structure in the governed docking environment before submitting Vina",
	))
	if result["code"] != "invalid_arguments" || result["retryable"] != false || result["terminal"] != true ||
		result["repair_scope"] != "same_logical_task" ||
		result["recovery"] != "prepare_the_source_structure_as_valid_pdbqt_in_the_governed_scientific_environment_then_start_a_new_execution_with_new_artifact_versions" {
		t.Fatalf("scientific repair contract = %#v", result)
	}
}

func TestAgentRuntimeSoftwareErrorCarriesStructuredRepairContract(t *testing.T) {
	result := agentRuntimeToolErrorValue(software.NewOperationError(
		"software_install_failed", "solver could not satisfy the request",
		"correct_packages_or_channels_in_this_same_provider_plan", true,
	))
	if result["code"] != "software_install_failed" || result["retryable"] != false || result["terminal"] != true ||
		result["repair_scope"] != "same_provider_plan" ||
		result["recovery"] != "correct_packages_or_channels_in_this_same_provider_plan" {
		t.Fatalf("software repair contract = %#v", result)
	}
}

func TestScientificArtifactValidationDoesNotRequireAuthorityWithoutSelectedArtifacts(t *testing.T) {
	failures, err := (&Server{}).validateSessionRunnerScientificArtifacts(context.Background(), "", nil, "plain final answer")
	if err != nil || len(failures) != 0 {
		t.Fatalf("empty scientific artifact selection failures=%#v err=%v", failures, err)
	}
}

func TestResearchArtifactValidationDoesNotRequireAuthorityWithoutProducedArtifacts(t *testing.T) {
	result, err := (&Server{}).validateSessionRunnerResearchArtifactSelection(context.Background(), "", nil)
	if err != nil || len(result.Failures) != 0 || len(result.SelectedVersions) != 0 || result.ManifestFound {
		t.Fatalf("empty research artifact selection result=%#v err=%v", result, err)
	}
}

func TestAgentRuntimeReadFileMissingPathReturnsSameTaskRecovery(t *testing.T) {
	result, recoverable := agentRuntimeRecoverableToolErrorValue(
		"read_file", nil, errors.New("read_file file does not exist in task workspace: handoff/label/info.txt"),
	)
	if !recoverable || result["ok"] != false || result["executed"] != false ||
		result["code"] != "workspace_file_missing" || result["retryable"] != true ||
		result["requested_path"] != "handoff/label/info.txt" ||
		!strings.Contains(stringValue(result["recovery"]), "version_id") ||
		!strings.Contains(stringValue(result["recovery"]), "do_not_retry") {
		t.Fatalf("missing-file recovery result=%#v recoverable=%t", result, recoverable)
	}
}
