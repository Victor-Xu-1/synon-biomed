package memoryextract

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	forkedJSONFencePrefix = regexp.MustCompile(`(?i)^\s*` + "```" + `(?:json)?\s*\n?`)
	forkedJSONFenceSuffix = regexp.MustCompile(`\n?` + "```" + `\s*$`)
)

// ParseForkedResponse reproduces the workspace tolerant forked-extraction
// parser: find the first balanced JSON object and treat every malformed or
// non-object response as an empty operation batch.
func ParseForkedResponse(value string) map[string]any {
	value = forkedJSONFencePrefix.ReplaceAllString(value, "")
	value = forkedJSONFenceSuffix.ReplaceAllString(value, "")
	start := strings.IndexByte(value, '{')
	if start < 0 {
		return map[string]any{}
	}
	depth := 0
	inString := false
	escaped := false
	for index := start; index < len(value); index++ {
		current := value[index]
		if inString {
			switch {
			case escaped:
				escaped = false
			case current == '\\':
				escaped = true
			case current == '"':
				inString = false
			}
			continue
		}
		switch current {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				var decoded map[string]any
				if err := json.Unmarshal([]byte(value[start:index+1]), &decoded); err != nil || decoded == nil {
					return map[string]any{}
				}
				return decoded
			}
		}
	}
	return map[string]any{}
}
