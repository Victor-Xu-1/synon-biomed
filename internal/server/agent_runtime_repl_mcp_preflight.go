package server

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"synon-go/internal/agentruntime"
)

var (
	literalMCPCallPattern = regexp.MustCompile(`(?:host\s*\.\s*mcp(?:\s*\.\s*call)?|mcp(?:\s*\.\s*call)?)\s*\(`)
	pythonDictKeyPattern  = regexp.MustCompile(`(?m)["']([A-Za-z_][A-Za-z0-9_]*)["']\s*:`)
	pythonKeywordPattern  = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*=`)
)

type literalMCPCall struct {
	server       string
	method       string
	keys         map[string]struct{}
	valueKinds   map[string]string
	stringValues map[string]string
	inspectable  bool
	assignedName string
	callEnd      int
}

// agentRuntimeREPLMCPContractPreflight validates only statically decidable
// host.mcp calls. Dynamic calls remain governed by the live host bridge. This
// mirrors Codex's parse-before-dispatch boundary while retaining the reference
// Harness's single REPL transport for MCP.
func agentRuntimeREPLMCPContractPreflight(
	publicName string,
	input map[string]any,
	schemas []agentruntime.ToolSchema,
	requiredSourceClass ...string,
) map[string]any {
	if !strings.EqualFold(strings.TrimSpace(publicName), "repl") {
		return nil
	}
	code := stringValue(input["code"])
	calls := parseLiteralMCPCalls(code)
	requiredClass := ""
	if len(requiredSourceClass) > 0 {
		requiredClass = strings.ToLower(strings.TrimSpace(requiredSourceClass[0]))
	}
	if requiredClass != "" && len(calls) == 0 {
		return literalMCPPreflightValue(
			"This repair requires an authoritative "+requiredClass+" source record, but the REPL cell does not call the live MCP catalog.",
			"Use the connector Skill already loaded for this repair, then issue one literal host.mcp call to a live "+requiredClass+" method. Inspect or print the returned structured record before editing any artifact. Do not use REPL for file rewriting or unrelated calculations in this source-read step.",
		)
	}
	if requiredClass != "" {
		for _, call := range calls {
			class, _ := runnerCorrectionMCPMethodClass(call.server, call.method)
			schema, schemaFound := literalMCPToolSchema(schemas, call.server, call.method)
			if class != requiredClass && (!schemaFound || !literalMCPDefersSourceClassToRegistry(schema)) {
				return literalMCPPreflightValue(
					fmt.Sprintf("The current repair requires a %s source record, but the literal host.mcp call targets %s/%s.", requiredClass, call.server, call.method),
					"Choose one live MCP method from the loaded connector whose source class matches the current repair. Preserve existing artifacts until that structured record has been inspected.",
				)
			}
		}
	}
	if len(calls) == 0 {
		return nil
	}
	for _, call := range calls {
		schema, found := literalMCPToolSchema(schemas, call.server, call.method)
		if !found || !call.inspectable {
			continue
		}
		properties := mapValue(schema.Parameters["properties"])
		allowed := make([]string, 0, len(properties))
		for key := range properties {
			allowed = append(allowed, key)
		}
		sort.Strings(allowed)
		invalid := make([]string, 0)
		for key := range call.keys {
			if _, ok := properties[key]; !ok {
				invalid = append(invalid, key)
			}
		}
		sort.Strings(invalid)
		missing := make([]string, 0)
		for _, required := range stringArrayValue(schema.Parameters["required"]) {
			if _, ok := call.keys[required]; !ok {
				missing = append(missing, required)
			}
		}
		wrongTypes := make([]string, 0)
		for key, kind := range call.valueKinds {
			property := mapValue(properties[key])
			if len(property) > 0 && !literalMCPValueKindAllowed(property, kind) {
				wrongTypes = append(wrongTypes, fmt.Sprintf("%s=%s (expected %s)", key, kind, literalMCPSchemaTypeSummary(property)))
			}
		}
		sort.Strings(wrongTypes)
		wrongValues := make([]string, 0)
		for key, value := range call.stringValues {
			property := mapValue(properties[key])
			allowedValues, constrained := literalMCPStringEnumValues(property)
			if constrained && !stringSliceContains(allowedValues, value) {
				wrongValues = append(wrongValues, fmt.Sprintf("%s=%q (allowed %s)", key, value, strings.Join(quotedStrings(allowedValues), ", ")))
			}
		}
		sort.Strings(wrongValues)
		if len(invalid) > 0 || len(missing) > 0 || len(wrongTypes) > 0 || len(wrongValues) > 0 {
			recovery := fmt.Sprintf(
				"Use only these exact input keys and literal values: %s. Remove unsupported keys %s, add required keys %s, and correct incompatible values %s before issuing one corrected REPL cell.",
				strings.Join(allowed, ", "), strings.Join(invalid, ", "), strings.Join(missing, ", "), strings.Join(append(wrongTypes, wrongValues...), ", "),
			)
			if literalMCPInvalidSearchIntent(invalid) && !literalMCPSchemaSupportsSearch(properties) {
				recovery = fmt.Sprintf(
					"The live %s/%s method does not accept a free-text search query; do not rename or guess another query field. Inspect host.mcp.list_methods(%q) and choose a method whose live schema declares a query-like parameter, or use the advertised web search route. Preserve any evidence already retrieved.",
					call.server, call.method, call.server,
				)
			}
			value := literalMCPPreflightValue(
				fmt.Sprintf("The literal host.mcp call for %s/%s does not match its live input schema.", call.server, call.method),
				recovery,
			)
			value["required_skill"] = "mcp-" + runtimeMCPConnectorSkillName(call.server)
			value["required_filter"] = call.method
			value["allowed_input_keys"] = allowed
			return value
		}
		if misuse := literalMCPOutputMisuse(code, call, schema.OutputSchema); misuse != "" {
			return literalMCPPreflightValue(
				fmt.Sprintf("The REPL cell assumes an output wrapper or iteration shape that is absent from the live %s/%s output schema.", call.server, call.method),
				misuse,
			)
		}
	}
	if len(calls) > 1 {
		return literalMCPPreflightValue(
			"The REPL cell combines multiple host.mcp calls before the first result shape is established.",
			"Issue one host.mcp call, persist its raw returned value, and print only its type and top-level keys. After that cell succeeds, use a new cell for the next connector call or analysis.",
		)
	}
	return nil
}

// literalMCPDefersSourceClassToRegistry identifies a live source broker from
// its admitted schema rather than from connector or method names. The broker's
// selected source and returned record determine the evidence class at runtime;
// preflight must not reject that governed route merely because the outer MCP
// method is class-neutral. The normal evidence validator still decides whether
// the returned record actually satisfies the pending class.
func literalMCPDefersSourceClassToRegistry(schema agentruntime.ToolSchema) bool {
	properties := mapValue(schema.Parameters["properties"])
	required := make(map[string]bool)
	for _, name := range stringArrayValue(schema.Parameters["required"]) {
		required[strings.TrimSpace(name)] = true
	}
	for _, field := range []struct {
		name string
		kind string
	}{
		{name: "source_id", kind: "string"},
		{name: "operation", kind: "string"},
		{name: "input", kind: "object"},
	} {
		property := mapValue(properties[field.name])
		if !required[field.name] || len(property) == 0 || !literalMCPValueKindAllowed(property, field.kind) {
			return false
		}
	}
	return true
}

func literalMCPInvalidSearchIntent(invalid []string) bool {
	for _, name := range invalid {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "q", "query", "search", "search_query", "text", "term", "keywords":
			return true
		}
	}
	return false
}

func literalMCPSchemaSupportsSearch(properties map[string]any) bool {
	for name := range properties {
		if literalMCPInvalidSearchIntent([]string{name}) {
			return true
		}
	}
	return false
}

func literalMCPPreflightValue(message, recovery string) map[string]any {
	return map[string]any{
		"ok": false, "status": "mcp_schema_preflight_required", "executed": false,
		"message": strings.TrimSpace(message), "recovery": strings.TrimSpace(recovery),
	}
}

func literalMCPToolSchema(schemas []agentruntime.ToolSchema, server, method string) (agentruntime.ToolSchema, bool) {
	wanted := "mcp__" + strings.TrimSpace(server) + "__" + strings.TrimSpace(method)
	for _, schema := range schemas {
		if strings.EqualFold(strings.TrimSpace(schema.Name), wanted) {
			return schema, true
		}
	}
	return agentruntime.ToolSchema{}, false
}

func parseLiteralMCPCalls(code string) []literalMCPCall {
	matches := literalMCPCallPattern.FindAllStringIndex(code, -1)
	result := make([]literalMCPCall, 0, len(matches))
	for _, match := range matches {
		open := strings.LastIndex(code[match[0]:match[1]], "(") + match[0]
		close := pythonBalancedCallEnd(code, open)
		if close <= open {
			continue
		}
		parts := pythonTopLevelArguments(code[open+1 : close])
		if len(parts) < 2 {
			continue
		}
		server, okServer := pythonStringLiteral(parts[0])
		method, okMethod := pythonStringLiteral(parts[1])
		if !okServer || !okMethod {
			continue
		}
		call := literalMCPCall{
			server: server, method: method, keys: map[string]struct{}{}, valueKinds: map[string]string{}, stringValues: map[string]string{}, callEnd: close + 1,
		}
		if len(parts) >= 3 {
			third := strings.TrimSpace(parts[2])
			if strings.HasPrefix(third, "{") && strings.HasSuffix(third, "}") && !strings.Contains(third, "**") {
				call.inspectable = true
				for _, entry := range pythonTopLevelArguments(strings.TrimSpace(third[1 : len(third)-1])) {
					colon := pythonTopLevelColon(entry)
					if colon <= 0 {
						continue
					}
					key, ok := pythonStringLiteral(entry[:colon])
					if !ok {
						continue
					}
					call.keys[key] = struct{}{}
					if kind := pythonLiteralValueKind(entry[colon+1:]); kind != "" {
						call.valueKinds[key] = kind
					}
					if value, ok := pythonStringLiteral(entry[colon+1:]); ok {
						call.stringValues[key] = value
					}
				}
			}
		}
		for _, part := range parts[2:] {
			if keyword := pythonKeywordPattern.FindStringSubmatch(strings.TrimSpace(part)); len(keyword) == 2 {
				call.inspectable = true
				call.keys[keyword[1]] = struct{}{}
				if equal := strings.Index(part, "="); equal >= 0 {
					if kind := pythonLiteralValueKind(part[equal+1:]); kind != "" {
						call.valueKinds[keyword[1]] = kind
					}
					if value, ok := pythonStringLiteral(part[equal+1:]); ok {
						call.stringValues[keyword[1]] = value
					}
				}
			}
		}
		lineStart := strings.LastIndex(code[:match[0]], "\n") + 1
		prefix := strings.TrimSpace(code[lineStart:match[0]])
		if equal := strings.Index(prefix, "="); equal > 0 {
			candidate := strings.TrimSpace(prefix[:equal])
			if regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(candidate) {
				call.assignedName = candidate
			}
		}
		result = append(result, call)
	}
	return result
}

func pythonTopLevelColon(value string) int {
	depth := 0
	quote := byte(0)
	escaped := false
	for index := 0; index < len(value); index++ {
		current := value[index]
		if quote != 0 {
			if escaped {
				escaped = false
				continue
			}
			if current == '\\' {
				escaped = true
				continue
			}
			if current == quote {
				quote = 0
			}
			continue
		}
		if current == '\'' || current == '"' {
			quote = current
			continue
		}
		switch current {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case ':':
			if depth == 0 {
				return index
			}
		}
	}
	return -1
}

func pythonLiteralValueKind(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if _, ok := pythonStringLiteral(value); ok {
		return "string"
	}
	switch value {
	case "None":
		return "null"
	case "True", "False":
		return "boolean"
	}
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		return "array"
	}
	if strings.HasPrefix(value, "{") && strings.HasSuffix(value, "}") {
		return "object"
	}
	if regexp.MustCompile(`^-?[0-9]+$`).MatchString(value) {
		return "integer"
	}
	if regexp.MustCompile(`^-?(?:[0-9]+\.[0-9]*|[0-9]*\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`).MatchString(value) {
		return "number"
	}
	return ""
}

func literalMCPValueKindAllowed(schema map[string]any, kind string) bool {
	for _, keyword := range []string{"anyOf", "oneOf"} {
		if alternatives := anySliceValue(schema[keyword]); len(alternatives) > 0 {
			for _, raw := range alternatives {
				if branch := mapValue(raw); len(branch) > 0 && literalMCPValueKindAllowed(branch, kind) {
					return true
				}
			}
			return false
		}
	}
	types := stringArrayValue(schema["type"])
	if len(types) == 0 {
		if single := strings.TrimSpace(stringValue(schema["type"])); single != "" {
			types = []string{single}
		}
	}
	for _, allowed := range types {
		allowed = strings.ToLower(strings.TrimSpace(allowed))
		if allowed == kind || allowed == "number" && kind == "integer" {
			return true
		}
	}
	return len(types) == 0
}

func literalMCPSchemaTypeSummary(schema map[string]any) string {
	for _, keyword := range []string{"anyOf", "oneOf"} {
		if alternatives := anySliceValue(schema[keyword]); len(alternatives) > 0 {
			summaries := make([]string, 0, len(alternatives))
			for _, raw := range alternatives {
				if branch := mapValue(raw); len(branch) > 0 {
					summaries = append(summaries, literalMCPSchemaTypeSummary(branch))
				}
			}
			if len(summaries) > 0 {
				return strings.Join(uniqueSortedFolded(summaries), "|")
			}
		}
	}
	types := stringArrayValue(schema["type"])
	if len(types) == 0 {
		if single := strings.TrimSpace(stringValue(schema["type"])); single != "" {
			types = []string{single}
		}
	}
	if len(types) == 0 {
		return "declared schema"
	}
	return strings.Join(types, "|")
}

func literalMCPStringEnumValues(schema map[string]any) ([]string, bool) {
	for _, keyword := range []string{"anyOf", "oneOf"} {
		if alternatives := anySliceValue(schema[keyword]); len(alternatives) > 0 {
			values := make([]string, 0)
			matchedStringBranch := false
			for _, raw := range alternatives {
				branch := mapValue(raw)
				if len(branch) == 0 || !literalMCPValueKindAllowed(branch, "string") {
					continue
				}
				matchedStringBranch = true
				branchValues, constrained := literalMCPStringEnumValues(branch)
				if !constrained {
					return nil, false
				}
				values = append(values, branchValues...)
			}
			if matchedStringBranch && len(values) > 0 {
				return uniqueSortedFolded(values), true
			}
			return nil, false
		}
	}
	values := make([]string, 0)
	for _, raw := range anySliceValue(schema["enum"]) {
		if value, ok := raw.(string); ok {
			values = append(values, value)
		}
	}
	if len(values) == 0 {
		return nil, false
	}
	return uniqueSortedFolded(values), true
}

func quotedStrings(values []string) []string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, fmt.Sprintf("%q", value))
	}
	return quoted
}

func pythonBalancedCallEnd(code string, open int) int {
	depth := 0
	quote := byte(0)
	escaped := false
	for index := open; index < len(code); index++ {
		value := code[index]
		if quote != 0 {
			if escaped {
				escaped = false
				continue
			}
			if value == '\\' {
				escaped = true
				continue
			}
			if value == quote {
				quote = 0
			}
			continue
		}
		if value == '\'' || value == '"' {
			quote = value
			continue
		}
		switch value {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth == 0 {
				return index
			}
		}
	}
	return -1
}

func pythonTopLevelArguments(value string) []string {
	parts := make([]string, 0, 4)
	start, depth := 0, 0
	quote := byte(0)
	escaped := false
	for index := 0; index < len(value); index++ {
		current := value[index]
		if quote != 0 {
			if escaped {
				escaped = false
				continue
			}
			if current == '\\' {
				escaped = true
				continue
			}
			if current == quote {
				quote = 0
			}
			continue
		}
		if current == '\'' || current == '"' {
			quote = current
			continue
		}
		switch current {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, strings.TrimSpace(value[start:index]))
				start = index + 1
			}
		}
	}
	parts = append(parts, strings.TrimSpace(value[start:]))
	return parts
}

func pythonStringLiteral(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if len(value) < 2 || (value[0] != value[len(value)-1]) || (value[0] != '\'' && value[0] != '"') {
		return "", false
	}
	inner := value[1 : len(value)-1]
	inner = strings.ReplaceAll(inner, `\`+string(value[0]), string(value[0]))
	inner = strings.ReplaceAll(inner, `\\`, `\`)
	return inner, strings.TrimSpace(inner) != ""
}

func literalMCPOutputMisuse(code string, call literalMCPCall, outputSchema map[string]any) string {
	if call.assignedName == "" || call.callEnd >= len(code) {
		return ""
	}
	properties := mapValue(outputSchema["properties"])
	if strings.TrimSpace(stringValue(outputSchema["type"])) != "object" || len(properties) == 0 {
		if literalMCPResultDeeplyConsumed(code[call.callEnd:], call.assignedName) {
			return "The live MCP output schema does not declare a closed result shape. Persist or print the raw returned value and inspect only its type and top-level keys in this cell; consume concrete fields in a later cell using the exact observed result, not guessed field names."
		}
		return ""
	}
	remainder := code[call.callEnd:]
	// A host.mcp result with an object schema is still a mapping at the Python
	// boundary. Slicing that mapping guesses list semantics and produces a late
	// KeyError (for example result[:5]). Field access declared by the live schema
	// remains valid; only slice syntax is rejected before execution.
	objectSlice := regexp.MustCompile(`\b` + regexp.QuoteMeta(call.assignedName) + `\s*\[\s*(?:[0-9]*\s*:|:)`)
	if strings.TrimSpace(stringValue(outputSchema["type"])) == "object" && objectSlice.MatchString(remainder) {
		return "The returned value is an object, not a list. Persist or print its type and top-level keys in this cell, then slice only an exact observed array field in a later cell."
	}
	getter := regexp.MustCompile(`\b` + regexp.QuoteMeta(call.assignedName) + `\s*\.\s*get\s*\(\s*["']([A-Za-z_][A-Za-z0-9_]*)["']`)
	for _, match := range getter.FindAllStringSubmatch(remainder, -1) {
		if _, found := properties[match[1]]; !found {
			keys := make([]string, 0, len(properties))
			for key := range properties {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			return fmt.Sprintf("The returned object has these top-level keys: %s. Persist the raw object and inspect those keys; do not unwrap through %q.", strings.Join(keys, ", "), match[1])
		}
	}
	// Direct indexing is another common way a model guesses a wrapper layer
	// (for example result["result"]["records"]). Validate the first object
	// key against the live schema before the code reaches the kernel. Valid
	// top-level fields remain allowed and are handled by the array-loop checks
	// below.
	directObjectField := regexp.MustCompile(`\b` + regexp.QuoteMeta(call.assignedName) + `\s*\[\s*["']([A-Za-z_][A-Za-z0-9_]*)["']\s*\]`)
	for _, match := range directObjectField.FindAllStringSubmatch(remainder, -1) {
		if _, found := properties[match[1]]; found {
			continue
		}
		keys := make([]string, 0, len(properties))
		for key := range properties {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return fmt.Sprintf("The returned object has these top-level keys: %s. The cell indexes undeclared field %q; persist and inspect the exact object instead of assuming a result wrapper.", strings.Join(keys, ", "), match[1])
	}
	directIteration := regexp.MustCompile(`(?m)^\s*for\s+.+\s+in\s+` + regexp.QuoteMeta(call.assignedName) + `\s*:`)
	if directIteration.MatchString(remainder) {
		arrays := make([]string, 0)
		for key, raw := range properties {
			if strings.TrimSpace(stringValue(mapValue(raw)["type"])) == "array" {
				arrays = append(arrays, key)
			}
		}
		sort.Strings(arrays)
		return fmt.Sprintf("The returned value is an object, not an array. Persist and inspect it first; iterate an array field declared by the output schema, such as: %s.", strings.Join(arrays, ", "))
	}
	for field, rawFieldSchema := range properties {
		fieldSchema := mapValue(rawFieldSchema)
		if strings.TrimSpace(stringValue(fieldSchema["type"])) != "array" {
			continue
		}
		itemProperties := mapValue(mapValue(fieldSchema["items"])["properties"])
		itemSchema := mapValue(fieldSchema["items"])
		requiredItemProperties := make(map[string]struct{})
		for _, required := range stringArrayValue(itemSchema["required"]) {
			requiredItemProperties[required] = struct{}{}
		}
		// Accept both common Python spellings for consuming an array field:
		// result.get("items", []) and result["items"], with enumerate(...) as
		// an optional wrapper. The previous pattern only recognized .get(...),
		// allowing enumerate(result["items"]) to reach the kernel and fail late
		// on an undeclared item field.
		loopPattern := regexp.MustCompile(`(?m)^\s*for\s+(?:[A-Za-z_][A-Za-z0-9_]*\s*,\s*)?([A-Za-z_][A-Za-z0-9_]*)\s+in\s+(?:enumerate\s*\(\s*)?` +
			regexp.QuoteMeta(call.assignedName) + `(?:\s*\.\s*get\s*\(\s*["']` + regexp.QuoteMeta(field) + `["'][^)]*\)|\s*\[\s*["']` + regexp.QuoteMeta(field) + `["']\s*\])(?:\s*\[[^\n]*\])?(?:\s*\))?\s*:\s*$`)
		for _, loop := range loopPattern.FindAllStringSubmatchIndex(remainder, -1) {
			if len(loop) < 4 {
				continue
			}
			itemName := remainder[loop[2]:loop[3]]
			loopBody := remainder[loop[1]:]
			accessPattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(itemName) + `\s*(?:\[\s*["']([A-Za-z_][A-Za-z0-9_]*)["']\s*\]|\.\s*get\s*\(\s*["']([A-Za-z_][A-Za-z0-9_]*)["'])`)
			accesses := accessPattern.FindAllStringSubmatch(loopBody, -1)
			if len(itemProperties) == 0 && len(accesses) > 0 {
				return fmt.Sprintf("Items in %q do not have a closed field schema. Persist or print the raw result and inspect item keys in this cell; use only exact observed fields in a later cell.", field)
			}
			for _, access := range accesses {
				key := access[1]
				if key == "" {
					key = access[2]
				}
				if _, found := itemProperties[key]; found {
					// A declared but non-required field is allowed to be absent in
					// a record. Catch direct indexing before execution and make the
					// safe optional access explicit; .get(...) remains valid.
					if access[1] != "" && len(requiredItemProperties) > 0 {
						if _, required := requiredItemProperties[key]; !required {
							return fmt.Sprintf("Items in %q may omit optional field %q. Use item.get(%q) or check membership before indexing, then run the corrected cell once.", field, key, key)
						}
					}
					continue
				}
				keys := make([]string, 0, len(itemProperties))
				for declared := range itemProperties {
					keys = append(keys, declared)
				}
				sort.Strings(keys)
				return fmt.Sprintf("Items in %q declare only these fields: %s. The cell reads undeclared item field %q. Persist the raw result and inspect it in this cell; use a new MCP detail call in a later cell before reading fields that belong to a richer record type.", field, strings.Join(keys, ", "), key)
			}
		}
	}
	return ""
}

func literalMCPResultDeeplyConsumed(remainder, assignedName string) bool {
	name := regexp.QuoteMeta(assignedName)
	for _, pattern := range []string{
		`\b` + name + `\s*\.\s*get\s*\(`,
		`\b` + name + `\s*\[`,
		`(?m)^\s*for\s+.+\s+in\s+` + name + `\s*:`,
	} {
		if regexp.MustCompile(pattern).MatchString(remainder) {
			return true
		}
	}
	return false
}
