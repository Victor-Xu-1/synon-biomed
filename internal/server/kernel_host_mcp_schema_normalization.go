package server

import (
	"strings"
	"unicode"
)

// kernelMCPNormalizeSchemaEnumAliases canonicalizes only exact, unambiguous
// formatting variants of enum values advertised by the live MCP schema. It
// does not guess synonyms or relax validation: values with zero or multiple
// normalized matches remain unchanged and are rejected by the validator.
func kernelMCPNormalizeSchemaEnumAliases(input map[string]any, schema map[string]any) map[string]any {
	normalized, _ := kernelMCPNormalizeSchemaValue(kernelMCPCloneJSONValue(input), schema).(map[string]any)
	if normalized == nil {
		return map[string]any{}
	}
	return normalized
}

func kernelMCPCloneJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		cloned := make(map[string]any, len(typed))
		for key, child := range typed {
			cloned[key] = kernelMCPCloneJSONValue(child)
		}
		return cloned
	case []any:
		cloned := make([]any, len(typed))
		for index, child := range typed {
			cloned[index] = kernelMCPCloneJSONValue(child)
		}
		return cloned
	default:
		return value
	}
}

func kernelMCPNormalizeSchemaValue(value any, schema map[string]any) any {
	if value == nil || len(schema) == 0 {
		return value
	}
	schema = kernelMCPSelectSchemaBranch(value, schema)
	switch typed := value.(type) {
	case map[string]any:
		properties, _ := schema["properties"].(map[string]any)
		for key, child := range typed {
			childSchema, _ := properties[key].(map[string]any)
			if len(childSchema) > 0 {
				typed[key] = kernelMCPNormalizeSchemaValue(child, childSchema)
			}
		}
		return typed
	case []any:
		itemSchema, _ := schema["items"].(map[string]any)
		if len(itemSchema) == 0 {
			return typed
		}
		for index, child := range typed {
			typed[index] = kernelMCPNormalizeSchemaValue(child, itemSchema)
		}
		return typed
	case string:
		return kernelMCPNormalizeSchemaEnumString(typed, schema)
	default:
		return value
	}
}

func kernelMCPSelectSchemaBranch(value any, schema map[string]any) map[string]any {
	for _, keyword := range []string{"anyOf", "oneOf"} {
		branches, _ := schema[keyword].([]any)
		for _, raw := range branches {
			branch, _ := raw.(map[string]any)
			if kernelMCPSchemaBranchMatchesValue(value, branch) {
				return branch
			}
		}
	}
	return schema
}

func kernelMCPSchemaBranchMatchesValue(value any, schema map[string]any) bool {
	typeName, _ := schema["type"].(string)
	switch typeName {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "null":
		return value == nil
	default:
		return len(kernelMCPStringEnum(schema)) > 0
	}
}

func kernelMCPNormalizeSchemaEnumString(value string, schema map[string]any) string {
	enum := kernelMCPStringEnum(schema)
	if len(enum) == 0 {
		return value
	}
	key := kernelMCPEnumAliasKey(value)
	if key == "" {
		return value
	}
	match := ""
	for _, candidate := range enum {
		if candidate == value {
			return value
		}
		if kernelMCPEnumAliasKey(candidate) != key {
			continue
		}
		if match != "" && match != candidate {
			return value
		}
		match = candidate
	}
	if match == "" {
		return value
	}
	return match
}

func kernelMCPStringEnum(schema map[string]any) []string {
	values, _ := schema["enum"].([]any)
	result := make([]string, 0, len(values))
	for _, raw := range values {
		if value, ok := raw.(string); ok {
			result = append(result, value)
		}
	}
	return result
}

func kernelMCPEnumAliasKey(value string) string {
	var builder strings.Builder
	for _, character := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			builder.WriteRune(character)
		}
	}
	return builder.String()
}
