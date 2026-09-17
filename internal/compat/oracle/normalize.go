package oracle

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

var (
	ErrInvalidCapture   = errors.New("invalid behavior capture")
	ErrBehaviorMismatch = errors.New("behavior capture mismatch")
)

type normalizedMarker struct{}

type ComparisonMode string

const (
	ExactComparison   ComparisonMode = "exact"
	CandidateSuperset ComparisonMode = "candidate-superset"
)

func CompareJSON(baseline []byte, candidate []byte, normalizationPointers []string) error {
	return CompareJSONMode(baseline, candidate, normalizationPointers, ExactComparison)
}

func CompareJSONMode(baseline []byte, candidate []byte, normalizationPointers []string, mode ComparisonMode) error {
	if mode == "" {
		mode = ExactComparison
	}
	if mode != ExactComparison && mode != CandidateSuperset {
		return fmt.Errorf("%w: unknown comparison mode %q", ErrInvalidCapture, mode)
	}
	baselineValue, err := decodeCaptureJSON("baseline", baseline)
	if err != nil {
		return err
	}
	candidateValue, err := decodeCaptureJSON("candidate", candidate)
	if err != nil {
		return err
	}

	pointers := append([]string{}, normalizationPointers...)
	sort.Strings(pointers)
	pointers = compactPointerList(pointers)
	for _, pointer := range pointers {
		if err := replacePointer(&baselineValue, pointer, normalizedMarker{}); err != nil {
			return fmt.Errorf("%w: baseline normalization %q: %v", ErrInvalidCapture, pointer, err)
		}
		if err := replacePointer(&candidateValue, pointer, normalizedMarker{}); err != nil {
			return fmt.Errorf("%w: candidate normalization %q: %v", ErrInvalidCapture, pointer, err)
		}
	}

	if difference := firstDifference("", baselineValue, candidateValue, mode); difference != "" {
		return fmt.Errorf("%w at %s", ErrBehaviorMismatch, difference)
	}
	return nil
}

func RedactJSON(capture []byte, redactionPointers []string) ([]byte, error) {
	value, err := decodeCaptureJSON("redaction input", capture)
	if err != nil {
		return nil, err
	}
	pointers := append([]string{}, redactionPointers...)
	sort.Strings(pointers)
	pointers = compactPointerList(pointers)
	for _, pointer := range pointers {
		if err := replacePointer(&value, pointer, "<redacted>"); err != nil {
			return nil, fmt.Errorf("%w: redaction %q: %v", ErrInvalidCapture, pointer, err)
		}
	}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return nil, fmt.Errorf("%w: marshal redacted capture: %v", ErrInvalidCapture, err)
	}
	return output.Bytes(), nil
}

func decodeCaptureJSON(label string, data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("%w: decode %s JSON: %v", ErrInvalidCapture, label, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("%w: %s JSON has trailing value", ErrInvalidCapture, label)
		}
		return nil, fmt.Errorf("%w: decode %s trailing JSON: %v", ErrInvalidCapture, label, err)
	}
	return value, nil
}

func replacePointer(root *any, pointer string, replacement any) error {
	tokens, err := parseJSONPointer(pointer)
	if err != nil {
		return err
	}
	return replaceNested(root, tokens, replacement)
}

func replaceNested(current *any, tokens []string, replacement any) error {
	if len(tokens) == 0 {
		*current = replacement
		return nil
	}
	token := tokens[0]
	last := len(tokens) == 1
	switch typed := (*current).(type) {
	case map[string]any:
		value, exists := typed[token]
		if !exists {
			return fmt.Errorf("object key %q does not exist", token)
		}
		if last {
			typed[token] = replacement
			return nil
		}
		if err := replaceNested(&value, tokens[1:], replacement); err != nil {
			return err
		}
		typed[token] = value
		return nil
	case []any:
		position, err := strconv.Atoi(token)
		if err != nil || position < 0 || position >= len(typed) {
			return fmt.Errorf("array index %q is invalid", token)
		}
		if last {
			typed[position] = replacement
			return nil
		}
		value := typed[position]
		if err := replaceNested(&value, tokens[1:], replacement); err != nil {
			return err
		}
		typed[position] = value
		return nil
	default:
		return fmt.Errorf("cannot traverse token %q through %T", token, *current)
	}
}

func parseJSONPointer(pointer string) ([]string, error) {
	if pointer == "" || !strings.HasPrefix(pointer, "/") {
		return nil, errors.New("normalization pointer must be a non-root RFC 6901 JSON pointer")
	}
	rawTokens := strings.Split(pointer[1:], "/")
	tokens := make([]string, 0, len(rawTokens))
	for _, raw := range rawTokens {
		var decoded strings.Builder
		for index := 0; index < len(raw); index++ {
			if raw[index] != '~' {
				decoded.WriteByte(raw[index])
				continue
			}
			if index+1 >= len(raw) {
				return nil, errors.New("normalization pointer has an incomplete escape")
			}
			index++
			switch raw[index] {
			case '0':
				decoded.WriteByte('~')
			case '1':
				decoded.WriteByte('/')
			default:
				return nil, fmt.Errorf("normalization pointer has invalid escape ~%c", raw[index])
			}
		}
		tokens = append(tokens, decoded.String())
	}
	return tokens, nil
}

func firstDifference(path string, baseline any, candidate any, mode ComparisonMode) string {
	if _, ok := baseline.(normalizedMarker); ok {
		if _, candidateOK := candidate.(normalizedMarker); candidateOK {
			return ""
		}
		return displayPointer(path)
	}
	switch baselineTyped := baseline.(type) {
	case nil:
		if candidate == nil {
			return ""
		}
	case bool:
		if candidateTyped, ok := candidate.(bool); ok && candidateTyped == baselineTyped {
			return ""
		}
	case string:
		if candidateTyped, ok := candidate.(string); ok && candidateTyped == baselineTyped {
			return ""
		}
	case json.Number:
		if candidateTyped, ok := candidate.(json.Number); ok && candidateTyped.String() == baselineTyped.String() {
			return ""
		}
	case map[string]any:
		candidateTyped, ok := candidate.(map[string]any)
		if !ok {
			return displayPointer(path)
		}
		keys := make([]string, 0, len(baselineTyped)+len(candidateTyped))
		seen := map[string]struct{}{}
		for key := range baselineTyped {
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
		if mode == ExactComparison {
			for key := range candidateTyped {
				if _, exists := seen[key]; !exists {
					keys = append(keys, key)
				}
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			baselineChild, baselineExists := baselineTyped[key]
			candidateChild, candidateExists := candidateTyped[key]
			childPath := path + "/" + escapeJSONPointerToken(key)
			if !baselineExists || !candidateExists {
				return displayPointer(childPath)
			}
			if difference := firstDifference(childPath, baselineChild, candidateChild, mode); difference != "" {
				return difference
			}
		}
		return ""
	case []any:
		candidateTyped, ok := candidate.([]any)
		if !ok {
			return displayPointer(path)
		}
		if mode == CandidateSuperset {
			baselineNamed, baselineNames, baselineOK := uniqueNamedObjectArray(baselineTyped)
			candidateNamed, _, candidateOK := uniqueNamedObjectArray(candidateTyped)
			if baselineOK && candidateOK {
				for _, name := range baselineNames {
					candidateItem, exists := candidateNamed[name]
					childPath := path + "/" + escapeJSONPointerToken(name)
					if !exists {
						return displayPointer(childPath)
					}
					if difference := firstDifference(childPath, baselineNamed[name], candidateItem, mode); difference != "" {
						return difference
					}
				}
				return ""
			}
		}
		if len(candidateTyped) != len(baselineTyped) {
			return displayPointer(path)
		}
		for index := range baselineTyped {
			childPath := path + "/" + strconv.Itoa(index)
			if difference := firstDifference(childPath, baselineTyped[index], candidateTyped[index], mode); difference != "" {
				return difference
			}
		}
		return ""
	}
	return displayPointer(path)
}

func uniqueNamedObjectArray(values []any) (map[string]any, []string, bool) {
	if len(values) == 0 {
		return nil, nil, false
	}
	byName := make(map[string]any, len(values))
	names := make([]string, 0, len(values))
	for _, value := range values {
		object, ok := value.(map[string]any)
		if !ok {
			return nil, nil, false
		}
		name, ok := object["name"].(string)
		if !ok || name == "" {
			return nil, nil, false
		}
		if _, duplicate := byName[name]; duplicate {
			return nil, nil, false
		}
		byName[name] = value
		names = append(names, name)
	}
	return byName, names, true
}

func displayPointer(path string) string {
	if path == "" {
		return "/"
	}
	return path
}

func escapeJSONPointerToken(token string) string {
	token = strings.ReplaceAll(token, "~", "~0")
	return strings.ReplaceAll(token, "/", "~1")
}

func compactPointerList(values []string) []string {
	if len(values) == 0 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}
