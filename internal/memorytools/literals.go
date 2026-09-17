package memorytools

import (
	"regexp"
	"strings"

	workspace "synon-go/internal/persistence/workspace"
)

var (
	durableLiteralPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`),
		regexp.MustCompile(`(?i)\b[0-9a-f]{7,}\b`),
		regexp.MustCompile(`/?\b[\w.-]+/[\w./-]+\b`),
		regexp.MustCompile(`(?i)\bv?\d+(?:\.\d+){2,}(?:[-+][\w.]+)*\b`),
		regexp.MustCompile(`\b\d+\.\d+\.\d+\.\d+(?::\d+)?\b`),
		regexp.MustCompile(`(?i)\bhttps?://\S+`),
		regexp.MustCompile(`\b\d{3,}\b`),
		regexp.MustCompile(`\b\d+\.\d+\b`),
	}
	durableLiteralTail   = regexp.MustCompile(`[.,;:)\]"'` + "`" + `>}!?*_]+$`)
	durableWasClause     = regexp.MustCompile(`(?i)\(was\b[^)]*\)`)
	durableWhitespace    = regexp.MustCompile(`\s+`)
	durableHexDigit      = regexp.MustCompile(`[0-9]`)
	durableSimpleYear    = regexp.MustCompile(`^\d{3,4}$`)
	durableAlphanumeric  = regexp.MustCompile(`[a-z0-9]`)
	durableBoundaryPunct = regexp.MustCompile(`[./_-]`)
)

var ignoredDurableLiterals = map[string]struct{}{
	"2024": {}, "2025": {}, "2026": {}, "100": {}, "200": {}, "404": {}, "500": {},
}

func missingDurableLiterals(current, replacement string) []string {
	return missingDurableLiteralsMode(current, replacement, true)
}

func missingDurableLiteralsMode(current, replacement string, ignoreWasClause bool) []string {
	original := workspace.RedactMemoryCredentials(current)
	if ignoreWasClause {
		original = durableWasClause.ReplaceAllString(original, " ")
	}
	originalNormalized := normalizeDurableLiteralText(original)
	replacementNormalized := normalizeDurableLiteralText(replacement)
	missing := make([]string, 0)
	for _, literal := range extractDurableLiterals(original) {
		if !literalConserved(originalNormalized, replacementNormalized, literal.normalized) {
			missing = append(missing, literal.raw)
		}
	}
	return missing
}

// MissingDurableLiterals exposes the literal-preservation check used by
// the reference runtime before an extractor replacement supersedes a durable row.
func MissingDurableLiterals(current, replacement string) []string {
	return missingDurableLiterals(current, replacement)
}

// MissingUpdatedDurableLiterals checks every literal in the proposed update,
// including values inside a "(was ...)" clause. The reference repair guard
// treats the full updated text as authoritative input and may not drop values
// from it while restoring literals from the previous row.
func MissingUpdatedDurableLiterals(updated, candidate string) []string {
	return missingDurableLiteralsMode(updated, candidate, false)
}

// UnexpectedDurableLiterals returns values introduced by candidate that are
// absent from every allowed source. The reference runtime applies this after its
// repair model so a repair cannot invent IDs, paths, versions, or numbers.
func UnexpectedDurableLiterals(candidate string, allowed ...string) []string {
	allowedValues := make(map[string]struct{})
	for _, value := range allowed {
		for _, literal := range extractDurableLiterals(workspace.RedactMemoryCredentials(value)) {
			allowedValues[literal.normalized] = struct{}{}
		}
	}
	unexpected := make([]string, 0)
	for _, literal := range extractDurableLiterals(workspace.RedactMemoryCredentials(candidate)) {
		if _, ok := allowedValues[literal.normalized]; !ok {
			unexpected = append(unexpected, literal.raw)
		}
	}
	return unexpected
}

type durableLiteral struct {
	normalized string
	raw        string
}

func extractDurableLiterals(value string) []durableLiteral {
	result := make([]durableLiteral, 0)
	seen := make(map[string]struct{})
	normalizedCommas := normalizeNumericCommas(value)
	for index, pattern := range durableLiteralPatterns {
		for _, raw := range pattern.FindAllString(normalizedCommas, -1) {
			literal := durableLiteralTail.ReplaceAllString(raw, "")
			if literal == "" {
				continue
			}
			if index == 1 && !durableHexDigit.MatchString(literal) {
				continue
			}
			normalized := strings.ToLower(literal)
			if durableSimpleYear.MatchString(normalized) {
				if _, ignored := ignoredDurableLiterals[normalized]; ignored {
					continue
				}
			}
			if !durableLiteralEligible(literal) {
				continue
			}
			if _, exists := seen[normalized]; !exists {
				seen[normalized] = struct{}{}
				result = append(result, durableLiteral{normalized: normalized, raw: literal})
			}
		}
	}
	return result
}

func durableLiteralEligible(value string) bool {
	if strings.HasPrefix(strings.ToLower(value), "http://") || strings.HasPrefix(strings.ToLower(value), "https://") {
		return true
	}
	if !strings.Contains(value, "/") {
		return true
	}
	return strings.Count(value, "/") >= 2 || strings.ContainsAny(value, "0123456789.")
}

func normalizeDurableLiteralText(value string) string {
	return strings.ToLower(strings.TrimSpace(durableWhitespace.ReplaceAllString(normalizeNumericCommas(value), " ")))
}

func normalizeNumericCommas(value string) string {
	var out strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] == ',' && index > 0 && isASCIIDigit(value[index-1]) && index+3 < len(value) &&
			isASCIIDigit(value[index+1]) && isASCIIDigit(value[index+2]) && isASCIIDigit(value[index+3]) &&
			(index+4 == len(value) || !isASCIIDigit(value[index+4])) {
			continue
		}
		out.WriteByte(value[index])
	}
	return out.String()
}

func literalConserved(original, replacement, literal string) bool {
	if hasLiteral(original, literal, true) {
		return hasLiteral(replacement, literal, true)
	}
	return hasLiteral(replacement, literal, false)
}

func hasLiteral(value, literal string, standalone bool) bool {
	if literal == "" {
		return true
	}
	for start := 0; ; {
		relative := strings.Index(value[start:], literal)
		if relative < 0 {
			return false
		}
		index := start + relative
		before := byteAt(value, index-1)
		after := byteAt(value, index+len(literal))
		if !isDurableAlphanumeric(before) && !isDurableAlphanumeric(after) {
			if !standalone || !literalBridgedByPunctuation(value, index, len(literal)) {
				return true
			}
		}
		start = index + 1
	}
}

func literalBridgedByPunctuation(value string, index, width int) bool {
	before := byteAt(value, index-1)
	before2 := byteAt(value, index-2)
	after := byteAt(value, index+width)
	after2 := byteAt(value, index+width+1)
	left := isDurableAlphanumeric(before) || isDurableBoundaryPunct(before) && isDurableAlphanumeric(before2)
	right := isDurableAlphanumeric(after) || isDurableBoundaryPunct(after) && isDurableAlphanumeric(after2)
	return left || right
}

func byteAt(value string, index int) byte {
	if index < 0 || index >= len(value) {
		return 0
	}
	return value[index]
}

func isDurableAlphanumeric(value byte) bool {
	return durableAlphanumeric.MatchString(string([]byte{value}))
}

func isDurableBoundaryPunct(value byte) bool {
	return durableBoundaryPunct.MatchString(string([]byte{value}))
}

func isASCIIDigit(value byte) bool { return value >= '0' && value <= '9' }
