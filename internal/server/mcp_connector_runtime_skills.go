package server

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"synon-go/internal/agentruntime"
	"synon-go/internal/skills"
)

const (
	runtimeMCPConnectorSkillSchemaBytes       = 96 << 10
	runtimeMCPConnectorMethodDescriptionRunes = 1200
	runtimeMCPConnectorActivationRunes        = 4096
	runtimeMCPConnectorInlineMethodLimit      = 20
	runtimeMCPConnectorFilteredMethodLimit    = 50
	runtimeMCPConnectorGuidance               = "This Skill is generated from the exact live, session-scoped MCP schema snapshot. " +
		"Invoke its methods from `repl` through `host.mcp(server, method, input)`; MCP calls do not run in analysis Python or R. " +
		"Use the displayed method name and JSON Schema exactly. Treat the returned JSON value as the method result and inspect its documented shape before reading fields. " +
		"The connector runtime owns pagination, receipts, retries, and result metadata exposed by `host.mcp.call`, `host.mcp.collect`, or `host.mcp.search`."
)

// runtimeMCPConnectorSkills mirrors the reference Harness boundary: MCP method
// schemas are exposed as load-on-demand Skills instead of competing flattened
// root tools. The Skills are built from the exact session-scoped schema
// snapshot, so custom connector details never enter the global Skill catalog.
func runtimeMCPConnectorSkills(schemas []agentruntime.ToolSchema) []skills.Skill {
	type connector struct {
		server  string
		methods []agentruntime.ToolSchema
	}
	grouped := map[string]*connector{}
	for _, schema := range schemas {
		name := strings.TrimSpace(schema.Name)
		parts := strings.SplitN(name, "__", 3)
		if len(parts) != 3 || !strings.EqualFold(parts[0], "mcp") ||
			strings.TrimSpace(parts[1]) == "" || strings.TrimSpace(parts[2]) == "" {
			continue
		}
		server := strings.TrimSpace(parts[1])
		key := strings.ToLower(server)
		entry := grouped[key]
		if entry == nil {
			entry = &connector{server: server}
			grouped[key] = entry
		}
		entry.methods = append(entry.methods, schema)
	}
	keys := make([]string, 0, len(grouped))
	for key := range grouped {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]skills.Skill, 0, len(keys))
	for _, key := range keys {
		entry := grouped[key]
		sort.SliceStable(entry.methods, func(i, j int) bool {
			return entry.methods[i].Name < entry.methods[j].Name
		})
		body, keywords := runtimeMCPConnectorSkillBody(entry.server, entry.methods)
		name := "mcp-" + runtimeMCPConnectorSkillName(entry.server)
		result = append(result, skills.Skill{
			Name:        name,
			Description: runtimeMCPConnectorActivationDescription(entry.server, entry.methods),
			Tags:        []string{"mcp", "connector", strings.ToLower(entry.server)},
			Keywords:    keywords,
			Tools:       []string{"repl"},
			References:  []string{"runtime:mcp:" + entry.server},
			Path:        "builtin:" + name,
			Body:        body,
			BodyHash:    serverStringHash(body),
		})
	}
	return result
}

func runtimeMCPConnectorActivationDescription(
	server string,
	methods []agentruntime.ToolSchema,
) string {
	parts := []string{fmt.Sprintf(
		"Exact live method schemas and invocation examples for the %s MCP connector.", server,
	)}
	for _, schema := range methods {
		nameParts := strings.SplitN(strings.TrimSpace(schema.Name), "__", 3)
		if len(nameParts) != 3 {
			continue
		}
		method := strings.ReplaceAll(strings.TrimSpace(nameParts[2]), "_", " ")
		description := runtimeMCPConnectorOneLineDescription(schema.Description)
		if description == "" {
			parts = append(parts, method)
		} else {
			parts = append(parts, method+": "+description)
		}
	}
	return strings.TrimSpace(truncateUTF8ByRunes(
		strings.Join(parts, " "), runtimeMCPConnectorActivationRunes,
	))
}

// runtimeSkillsByNameWithConnectorSchemas restores both repository Skills and
// session-scoped connector Skills from one durable name set. Connector Skills
// deliberately never enter the global catalog, so recovery must rebuild them
// from the same admitted MCP schema snapshot instead of treating their names
// as missing static files.
func (s *Server) runtimeSkillsByNameWithConnectorSchemas(
	names []string,
	excluded []string,
	schemas []agentruntime.ToolSchema,
) ([]skills.Skill, error) {
	connectors := runtimeMCPConnectorSkills(schemas)
	connectorByName := make(map[string]skills.Skill, len(connectors))
	for _, skill := range connectors {
		connectorByName[strings.ToLower(strings.TrimSpace(skill.Name))] = skill
	}
	excludedNames := make(map[string]struct{}, len(excluded))
	for _, name := range excluded {
		excludedNames[strings.ToLower(strings.TrimPrefix(strings.TrimSpace(name), "/"))] = struct{}{}
	}
	staticNames := make([]string, 0, len(names))
	selectedConnectors := make(map[string]skills.Skill)
	for _, rawName := range names {
		name := strings.TrimPrefix(strings.TrimSpace(rawName), "/")
		key := strings.ToLower(name)
		if name == "" {
			continue
		}
		if connector, found := connectorByName[key]; found {
			if _, blocked := excludedNames[key]; !blocked {
				selectedConnectors[key] = connector
			}
			continue
		}
		staticNames = append(staticNames, name)
	}
	selected, err := s.runtimeSkillsByName(uniqueSortedFolded(staticNames), excluded)
	if err != nil {
		return nil, err
	}
	// A repository Skill may depend on a live connector Skill. Resolve those
	// connector dependencies from the same immutable schema snapshot without
	// publishing them into the global slash-command catalog. Walk static
	// dependencies as well so composition remains transitive and domain-neutral.
	staticByName := make(map[string]skills.Skill)
	if s != nil && s.skillCatalog != nil {
		for _, skill := range s.skillCatalog.Skills() {
			if name := normalizeRuntimeSkillDependencyName(skill.Name); name != "" {
				staticByName[name] = skill
			}
		}
	}
	queue := append([]skills.Skill(nil), selected...)
	visited := make(map[string]struct{}, len(queue))
	for len(queue) > 0 {
		skill := queue[0]
		queue = queue[1:]
		name := normalizeRuntimeSkillDependencyName(skill.Name)
		if _, seen := visited[name]; seen {
			continue
		}
		visited[name] = struct{}{}
		for _, rawDependency := range skill.RequiredSkills {
			dependencyName := normalizeRuntimeSkillDependencyName(rawDependency)
			if connector, found := connectorByName[dependencyName]; found {
				selectedConnectors[dependencyName] = connector
				continue
			}
			if dependency, found := staticByName[dependencyName]; found {
				queue = append(queue, dependency)
			}
		}
	}
	connectorNames := make([]string, 0, len(selectedConnectors))
	for name := range selectedConnectors {
		connectorNames = append(connectorNames, name)
	}
	sort.Strings(connectorNames)
	for _, name := range connectorNames {
		selected = append(selected, selectedConnectors[name])
	}
	return selected, nil
}

func runtimeSkillAllowedNamesWithConnectorDependencies(allowed []string, selected []skills.Skill) []string {
	result := append([]string(nil), allowed...)
	for _, skill := range selected {
		if strings.HasPrefix(strings.TrimSpace(skill.Path), "builtin:mcp-") {
			result = appendUniqueFolded(result, skill.Name)
		}
	}
	return result
}

func runtimeMCPConnectorSkillName(server string) string {
	var builder strings.Builder
	lastDash := false
	for _, value := range strings.ToLower(strings.TrimSpace(server)) {
		if unicode.IsLetter(value) || unicode.IsDigit(value) {
			builder.WriteRune(value)
			lastDash = false
			continue
		}
		if builder.Len() > 0 && !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

func runtimeSelectionContainsMCPConnectorSkill(names []string) bool {
	for _, name := range names {
		if strings.HasPrefix(strings.ToLower(strings.TrimPrefix(strings.TrimSpace(name), "/")), "mcp-") {
			return true
		}
	}
	return false
}

func runtimeMCPConnectorSkillBody(server string, methods []agentruntime.ToolSchema) (string, []string) {
	return runtimeMCPConnectorFilteredSkillBody(server, methods, "")
}

func runtimeMCPConnectorFilteredSkillBody(server string, methods []agentruntime.ToolSchema, filter string) (string, []string) {
	var body strings.Builder
	fmt.Fprintf(&body, "# %s MCP connector\n\n", server)
	body.WriteString(runtimeMCPConnectorGuidance)
	body.WriteString("\n\n")
	serverName := strings.ToLower(server)
	keywords := map[string]struct{}{
		serverName:                               {},
		strings.ReplaceAll(serverName, "-", " "): {},
		strings.ReplaceAll(serverName, "_", " "): {},
	}
	selected := runtimeMCPConnectorFilteredMethods(methods, filter)
	if strings.TrimSpace(filter) == "" && len(selected) > runtimeMCPConnectorInlineMethodLimit {
		fmt.Fprintf(&body, "Full parameter schemas are omitted for this %d-method connector. Call `skill` again with the same Skill name and a `filter` containing the intended method name or keywords.\n\n", len(selected))
		for _, schema := range selected {
			parts := strings.SplitN(strings.TrimSpace(schema.Name), "__", 3)
			if len(parts) != 3 {
				continue
			}
			method := parts[2]
			methodName := strings.ToLower(method)
			keywords[methodName] = struct{}{}
			keywords[strings.ReplaceAll(methodName, "_", " ")] = struct{}{}
			fmt.Fprintf(&body, "- **%s** — %s\n", method, runtimeMCPConnectorOneLineDescription(schema.Description))
		}
		return strings.TrimSpace(body.String()), runtimeMCPConnectorKeywordValues(keywords)
	}
	if trimmed := strings.TrimSpace(filter); trimmed != "" {
		fmt.Fprintf(&body, "Methods matching `%s`:\n\n", trimmed)
	}
	for _, schema := range selected {
		parts := strings.SplitN(strings.TrimSpace(schema.Name), "__", 3)
		if len(parts) != 3 {
			continue
		}
		method := parts[2]
		methodName := strings.ToLower(method)
		keywords[methodName] = struct{}{}
		keywords[strings.ReplaceAll(methodName, "_", " ")] = struct{}{}
		fmt.Fprintf(&body, "## %s\n\n", method)
		if description := strings.TrimSpace(schema.Description); description != "" {
			body.WriteString(runtimeMCPConnectorBoundedDescription(description))
			body.WriteString("\n\n")
		}
		encoded, err := json.Marshal(schema.Parameters)
		if err != nil {
			encoded = []byte(`{"type":"object","properties":{}}`)
		}
		body.WriteString("Input schema:\n```json\n")
		body.Write(encoded)
		body.WriteString("\n```\n\n")
		if shape := runtimeMCPConnectorOutputShape(schema.OutputSchema); shape != "" {
			body.WriteString("Output shape derived from the live outputSchema:\n```text\n")
			body.WriteString(shape)
			body.WriteString("\n```\n\n")
		}
		body.WriteString("Example:\n```python\n")
		fmt.Fprintf(&body, "result = host.mcp(%q, %q, %s)\n", server, method, runtimeMCPConnectorExampleInput(schema.Parameters))
		body.WriteString("print(type(result), list(result) if isinstance(result, dict) else None)\n")
		body.WriteString("print(getattr(result, 'retrieval', None))\n")
		body.WriteString("```\n\n")
		if body.Len() >= runtimeMCPConnectorSkillSchemaBytes {
			body.WriteString("Additional methods were omitted from this bounded Skill body; call `host.mcp.list_methods(server)` for the remaining exact live schemas.\n")
			break
		}
	}
	if omitted := len(methods) - len(selected); strings.TrimSpace(filter) != "" && omitted > 0 {
		fmt.Fprintf(&body, "%d more methods are available; call `skill` again with another filter or omit it for the method index.\n", omitted)
	}
	return strings.TrimSpace(body.String()), runtimeMCPConnectorKeywordValues(keywords)
}

func runtimeMCPConnectorFilteredMethods(methods []agentruntime.ToolSchema, filter string) []agentruntime.ToolSchema {
	query := strings.FieldsFunc(strings.ToLower(strings.TrimSpace(filter)), func(value rune) bool {
		return !(unicode.IsLetter(value) || unicode.IsDigit(value))
	})
	if len(query) == 0 {
		return append([]agentruntime.ToolSchema(nil), methods...)
	}
	type rankedMethod struct {
		schema agentruntime.ToolSchema
		score  int
	}
	ranked := make([]rankedMethod, 0, len(methods))
	for _, schema := range methods {
		parts := strings.SplitN(strings.TrimSpace(schema.Name), "__", 3)
		if len(parts) != 3 {
			continue
		}
		name := strings.ToLower(parts[2])
		haystack := name + " " + strings.ToLower(schema.Description)
		score := 0
		for _, token := range query {
			if strings.Contains(name, token) {
				score += 4
			} else if strings.Contains(haystack, token) {
				score++
			}
		}
		if score > 0 {
			ranked = append(ranked, rankedMethod{schema: schema, score: score})
		}
	}
	if len(ranked) == 0 {
		return append([]agentruntime.ToolSchema(nil), methods...)
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].schema.Name < ranked[j].schema.Name
	})
	if len(ranked) > runtimeMCPConnectorFilteredMethodLimit {
		ranked = ranked[:runtimeMCPConnectorFilteredMethodLimit]
	}
	selected := make([]agentruntime.ToolSchema, 0, len(ranked))
	for _, method := range ranked {
		selected = append(selected, method.schema)
	}
	return selected
}

func runtimeMCPConnectorOneLineDescription(description string) string {
	value := strings.TrimSpace(description)
	if newline := strings.IndexByte(value, '\n'); newline >= 0 {
		value = strings.TrimSpace(value[:newline])
	}
	values := []rune(value)
	if len(values) > 200 {
		return strings.TrimSpace(string(values[:200])) + "…"
	}
	return value
}

func runtimeMCPConnectorKeywordValues(keywords map[string]struct{}) []string {
	values := make([]string, 0, len(keywords))
	for keyword := range keywords {
		values = append(values, keyword)
	}
	sort.Strings(values)
	return values
}

func runtimeMCPConnectorFilterRenderedBody(body string, filter string) (string, bool) {
	query := strings.FieldsFunc(strings.ToLower(strings.TrimSpace(filter)), func(value rune) bool {
		return !(unicode.IsLetter(value) || unicode.IsDigit(value))
	})
	if len(query) == 0 {
		return body, false
	}
	lines := strings.Split(body, "\n")
	firstMethod := len(lines)
	for index, line := range lines {
		if strings.HasPrefix(line, "## ") {
			firstMethod = index
			break
		}
	}
	if firstMethod == len(lines) {
		return body, false
	}
	type section struct {
		text  string
		score int
		name  string
	}
	sections := make([]section, 0)
	for start := firstMethod; start < len(lines); {
		end := start + 1
		for end < len(lines) && !strings.HasPrefix(lines[end], "## ") {
			end++
		}
		text := strings.Join(lines[start:end], "\n")
		name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(lines[start], "## ")))
		haystack := strings.ToLower(text)
		score := 0
		for _, token := range query {
			if strings.Contains(name, token) {
				score += 4
			} else if strings.Contains(haystack, token) {
				score++
			}
		}
		if score > 0 {
			sections = append(sections, section{text: text, score: score, name: name})
		}
		start = end
	}
	if len(sections) == 0 {
		return body, false
	}
	sort.SliceStable(sections, func(i, j int) bool {
		if sections[i].score != sections[j].score {
			return sections[i].score > sections[j].score
		}
		return sections[i].name < sections[j].name
	})
	if len(sections) > 10 {
		sections = sections[:10]
	}
	result := strings.TrimSpace(strings.Join(lines[:firstMethod], "\n")) +
		fmt.Sprintf("\n\nMethods matching `%s`:\n\n", strings.TrimSpace(filter))
	for _, item := range sections {
		result += strings.TrimSpace(item.text) + "\n\n"
	}
	return strings.TrimSpace(result), true
}

func runtimeMCPConnectorBoundedDescription(description string) string {
	values := []rune(strings.TrimSpace(description))
	if len(values) <= runtimeMCPConnectorMethodDescriptionRunes {
		return string(values)
	}
	return strings.TrimSpace(string(values[:runtimeMCPConnectorMethodDescriptionRunes])) + "…"
}

func runtimeMCPConnectorOutputShape(schema map[string]any) string {
	if len(schema) == 0 {
		return ""
	}
	return runtimeMCPConnectorSchemaShape(schema, 0)
}

func runtimeMCPConnectorSchemaShape(schema map[string]any, depth int) string {
	typeName := strings.TrimSpace(stringValue(schema["type"]))
	if typeName == "" {
		if variants := anySliceValue(schema["anyOf"]); len(variants) > 0 {
			parts := make([]string, 0, len(variants))
			for _, variant := range variants {
				if item := mapValue(variant); len(item) > 0 {
					parts = append(parts, runtimeMCPConnectorSchemaShape(item, depth+1))
				}
			}
			return strings.Join(uniqueSortedFolded(parts), "|")
		}
		return "value"
	}
	if depth >= 2 {
		return typeName
	}
	switch typeName {
	case "array":
		return "array<" + runtimeMCPConnectorSchemaShape(mapValue(schema["items"]), depth+1) + ">"
	case "object":
		properties := mapValue(schema["properties"])
		keys := make([]string, 0, len(properties))
		for key := range properties {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, key+":"+runtimeMCPConnectorSchemaShape(mapValue(properties[key]), depth+1))
		}
		return "object{" + strings.Join(parts, ", ") + "}"
	default:
		return typeName
	}
}

func runtimeMCPConnectorExampleInput(schema map[string]any) string {
	properties, _ := schema["properties"].(map[string]any)
	required := stringArrayValue(schema["required"])
	values := make(map[string]any, len(required))
	for _, name := range required {
		property, _ := properties[name].(map[string]any)
		values[name] = runtimeMCPConnectorExampleValue(property)
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func runtimeMCPConnectorExampleValue(schema map[string]any) any {
	if value, found := schema["default"]; found {
		return value
	}
	if values, ok := schema["enum"].([]any); ok && len(values) > 0 {
		return values[0]
	}
	switch strings.ToLower(strings.TrimSpace(stringValue(schema["type"]))) {
	case "array":
		item, _ := schema["items"].(map[string]any)
		return []any{runtimeMCPConnectorExampleValue(item)}
	case "object":
		return map[string]any{}
	case "integer":
		return 1
	case "number":
		return 1.0
	case "boolean":
		return true
	default:
		return "value"
	}
}
