package providers

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// normalizeProviderToolArgumentsObject is the single provider-boundary
// authority for function arguments. It accepts a JSON object, a JSON string
// containing an object, or an exact Markdown JSON fence. Some OpenAI-compatible
// gateways also stream literal control characters inside JSON strings; escaping
// those characters preserves their value without guessing missing fields or
// changing object structure.
func normalizeProviderToolArgumentsObject(raw string) (json.RawMessage, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return json.RawMessage(`{}`), true
	}
	if normalized, ok := normalizeProviderToolArgumentsCandidate(raw); ok {
		return normalized, true
	}

	var encoded string
	if json.Unmarshal([]byte(raw), &encoded) == nil {
		if normalized, ok := normalizeProviderToolArgumentsCandidate(encoded); ok {
			return normalized, true
		}
	}

	if unfenced, ok := unwrapProviderToolArgumentsFence(raw); ok {
		if normalized, ok := normalizeProviderToolArgumentsCandidate(unfenced); ok {
			return normalized, true
		}
	}
	return nil, false
}

// providerToolArgumentsSyntaxDiagnostic exposes only structural parser state.
// It deliberately omits argument bytes so provider failures remain diagnosable
// without logging prompts, credentials, paths, or model-authored content.
func providerToolArgumentsSyntaxDiagnostic(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "syntax=empty"
	}
	var value any
	err := json.Unmarshal([]byte(trimmed), &value)
	if err == nil {
		return "syntax=valid root=" + providerToolArgumentsByteClass(trimmed[0])
	}
	var syntax *json.SyntaxError
	if !errors.As(err, &syntax) || syntax.Offset <= 0 {
		return "syntax=invalid root=" + providerToolArgumentsByteClass(trimmed[0])
	}
	index := int(syntax.Offset) - 1
	if index >= len(trimmed) {
		index = len(trimmed) - 1
	}
	return fmt.Sprintf(
		"syntax=invalid offset=%d/%d token=%s prev=%s next=%s",
		syntax.Offset, len(trimmed), providerToolArgumentsByteClass(trimmed[index]),
		providerToolArgumentsAdjacentClass(trimmed, index, -1),
		providerToolArgumentsAdjacentClass(trimmed, index, 1),
	)
}

func providerToolArgumentsAdjacentClass(raw string, index, direction int) string {
	for index += direction; index >= 0 && index < len(raw); index += direction {
		if raw[index] == ' ' || raw[index] == '\n' || raw[index] == '\r' || raw[index] == '\t' {
			continue
		}
		return providerToolArgumentsByteClass(raw[index])
	}
	return "boundary"
}

func providerToolArgumentsByteClass(value byte) string {
	switch value {
	case '{':
		return "object_open"
	case '}':
		return "object_close"
	case '[':
		return "array_open"
	case ']':
		return "array_close"
	case '"':
		return "quote"
	case ':':
		return "colon"
	case ',':
		return "comma"
	case '\\':
		return "escape"
	case ' ', '\n', '\r', '\t':
		return "whitespace"
	}
	if value >= '0' && value <= '9' {
		return "digit"
	}
	if value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value == '_' {
		return "identifier"
	}
	if value >= 0x80 {
		return "utf8"
	}
	if value < 0x20 {
		return "control"
	}
	return "punctuation"
}

func normalizeProviderToolArgumentsCandidate(raw string) (json.RawMessage, bool) {
	raw = strings.TrimSpace(raw)
	if normalized, ok := providerToolArgumentsObject(raw); ok {
		return normalized, true
	}
	repaired := escapeProviderToolArgumentStringControls(raw)
	if normalized, ok := providerToolArgumentsObject(repaired); ok {
		return normalized, true
	}
	repaired = removeProviderToolArgumentTrailingCommas(repaired)
	return providerToolArgumentsObject(repaired)
}

// removeProviderToolArgumentTrailingCommas repairs only an unambiguous JSON
// compatibility defect observed from OpenAI-compatible tool streams: a comma
// immediately before an array or object close. String content and every other
// malformed structure remain untouched and must still pass the strict object
// parser before execution.
func removeProviderToolArgumentTrailingCommas(raw string) string {
	var output strings.Builder
	output.Grow(len(raw))
	inString, escaped := false, false
	changed := false
	for index := 0; index < len(raw); index++ {
		value := raw[index]
		if inString {
			output.WriteByte(value)
			if escaped {
				escaped = false
			} else if value == '\\' {
				escaped = true
			} else if value == '"' {
				inString = false
			}
			continue
		}
		if value == '"' {
			inString = true
			output.WriteByte(value)
			continue
		}
		if value == ',' {
			next := index + 1
			for next < len(raw) && (raw[next] == ' ' || raw[next] == '\n' || raw[next] == '\r' || raw[next] == '\t') {
				next++
			}
			if next < len(raw) && (raw[next] == '}' || raw[next] == ']') {
				changed = true
				continue
			}
		}
		output.WriteByte(value)
	}
	if !changed {
		return raw
	}
	return output.String()
}

func providerToolArgumentsObject(raw string) (json.RawMessage, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw[0] != '{' || !json.Valid([]byte(raw)) {
		return nil, false
	}
	var object map[string]any
	if json.Unmarshal([]byte(raw), &object) != nil || object == nil {
		return nil, false
	}
	return json.RawMessage(raw), true
}

func unwrapProviderToolArgumentsFence(raw string) (string, bool) {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if len(lines) < 3 || !strings.HasPrefix(strings.TrimSpace(lines[0]), "```") || strings.TrimSpace(lines[len(lines)-1]) != "```" {
		return "", false
	}
	language := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[0]), "```"))
	if language != "" && !strings.EqualFold(language, "json") {
		return "", false
	}
	return strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n")), true
}

func escapeProviderToolArgumentStringControls(raw string) string {
	var output strings.Builder
	output.Grow(len(raw))
	inString := false
	escaped := false
	changed := false
	for index := 0; index < len(raw); index++ {
		value := raw[index]
		if !inString {
			output.WriteByte(value)
			if value == '"' {
				inString = true
			}
			continue
		}
		if escaped {
			output.WriteByte(value)
			escaped = false
			continue
		}
		switch value {
		case '\\':
			output.WriteByte(value)
			escaped = true
		case '"':
			output.WriteByte(value)
			inString = false
		case '\n':
			output.WriteString(`\n`)
			changed = true
		case '\r':
			output.WriteString(`\r`)
			changed = true
		case '\t':
			output.WriteString(`\t`)
			changed = true
		default:
			if value < 0x20 {
				_, _ = fmt.Fprintf(&output, `\u%04x`, value)
				changed = true
			} else {
				output.WriteByte(value)
			}
		}
	}
	if !changed {
		return raw
	}
	return output.String()
}
