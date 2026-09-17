package registry

import (
	"fmt"
	"math"
	"strings"
)

func validateFieldType(toolName string, fieldName string, fieldType string, value any) error {
	switch fieldType {
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s.%s must be a string", toolName, fieldName)
		}
	case "number":
		switch value.(type) {
		case int, int64, float64:
			return nil
		default:
			return fmt.Errorf("%s.%s must be a number", toolName, fieldName)
		}
	case "object":
		if _, ok := value.(map[string]any); !ok {
			return fmt.Errorf("%s.%s must be an object", toolName, fieldName)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s.%s must be a boolean", toolName, fieldName)
		}
	case "array":
		switch value.(type) {
		case []any, []string:
			return nil
		default:
			return fmt.Errorf("%s.%s must be an array", toolName, fieldName)
		}
	case "any":
		return nil
	}
	return nil
}

func validateFieldSchema(toolName string, fieldName string, schema map[string]any, value any) error {
	if len(schema) == 0 {
		return nil
	}
	if numeric, ok := registrySchemaNumber(value); ok {
		if minimum, found := registrySchemaNumber(schema["minimum"]); found && numeric < minimum {
			return fmt.Errorf("%s.%s must be at least %v", toolName, fieldName, minimum)
		}
		if maximum, found := registrySchemaNumber(schema["maximum"]); found && numeric > maximum {
			return fmt.Errorf("%s.%s must be at most %v", toolName, fieldName, maximum)
		}
	}
	rawEnum, hasEnum := schema["enum"]
	if !hasEnum {
		return nil
	}
	allowed, ok := rawEnum.([]string)
	if !ok {
		return nil
	}
	text, ok := value.(string)
	if !ok {
		return nil
	}
	for _, candidate := range allowed {
		if text == candidate {
			return nil
		}
	}
	return fmt.Errorf("%s.%s must be one of: %s", toolName, fieldName, strings.Join(allowed, ", "))
}

func registrySchemaNumber(value any) (float64, bool) {
	var number float64
	switch typed := value.(type) {
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	case float64:
		number = typed
	default:
		return 0, false
	}
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, false
	}
	return number, true
}
