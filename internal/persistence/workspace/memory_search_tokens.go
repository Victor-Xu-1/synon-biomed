package workspace

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Explicit search_memory keeps the workspace ASCII lexical contract. The
// automatic recall path uses memoryTokens, which additionally understands Han
// text and harness attachment metadata. Keeping the two token projections in
// one package avoids a second ranking implementation while preserving the
// externally observable tool behavior.
var (
	memorySearchCamelBoundaryLower = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	memorySearchCamelBoundaryUpper = regexp.MustCompile(`([A-Z]+)([A-Z][a-z])`)
	memorySearchSplit              = regexp.MustCompile(`[^a-zA-Z0-9]+`)
	memorySearchStopWords          = map[string]struct{}{
		"the": {}, "a": {}, "an": {}, "and": {}, "or": {}, "of": {}, "to": {}, "in": {}, "on": {}, "for": {}, "with": {},
		"is": {}, "are": {}, "be": {}, "this": {}, "that": {}, "it": {}, "as": {}, "at": {}, "by": {}, "from": {},
		"you": {}, "your": {}, "i": {}, "we": {}, "my": {}, "our": {}, "me": {}, "do": {}, "does": {}, "can": {}, "will": {},
		"please": {}, "run": {}, "use": {}, "using": {}, "make": {}, "need": {}, "want": {}, "how": {}, "what": {},
		"ok": {}, "okay": {}, "sounds": {}, "great": {}, "good": {}, "sure": {}, "thanks": {}, "thank": {}, "yes": {}, "yeah": {},
		"yep": {}, "no": {}, "nope": {}, "ahead": {}, "go": {}, "now": {}, "let": {}, "lets": {},
	}
)

func memorySearchTokens(value string) []string {
	result := make([]string, 0)
	for _, raw := range memorySearchSplit.Split(value, -1) {
		if raw == "" {
			continue
		}
		camelSplit := memorySearchCamelBoundaryLower.ReplaceAllString(raw, "$1 $2")
		camelSplit = memorySearchCamelBoundaryUpper.ReplaceAllString(camelSplit, "$1 $2")
		candidates := []string{raw}
		if camelSplit != raw {
			candidates = append(candidates, strings.Fields(camelSplit)...)
		}
		for _, candidate := range candidates {
			token := strings.ToLower(candidate)
			if utf8.RuneCountInString(token) < 2 {
				continue
			}
			if _, stopped := memorySearchStopWords[token]; stopped {
				continue
			}
			if len(token) > 3 && strings.HasSuffix(token, "s") && !strings.HasSuffix(token, "ss") {
				token = strings.TrimSuffix(token, "s")
			}
			result = append(result, token)
		}
	}
	return result
}
