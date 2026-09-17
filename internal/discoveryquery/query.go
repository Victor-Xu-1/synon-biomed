// Package discoveryquery normalizes user queries for Skill, Tool, and MCP
// discovery without changing authorization or execution policy.
package discoveryquery

import (
	"strings"
	"unicode"
)

const maxTerms = 96

var englishStopTerms = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "be": {}, "by": {},
	"for": {}, "from": {}, "in": {}, "is": {}, "of": {}, "on": {}, "or": {}, "the": {},
	"to": {}, "with": {},
}

type aliasGroup struct {
	phrases []string
	terms   []string
}

var chineseAliases = []aliasGroup{
	{phrases: []string{"\u641c\u7d22", "\u67e5\u8be2", "\u67e5\u627e", "\u68c0\u7d22"}, terms: []string{"search", "query", "find", "lookup"}},
	{phrases: []string{"\u8bfb\u53d6", "\u6253\u5f00", "\u67e5\u770b", "\u83b7\u53d6"}, terms: []string{"read", "get", "fetch"}},
	{phrases: []string{"\u4e0b\u8f7d"}, terms: []string{"download"}},
	{phrases: []string{"\u4fdd\u5b58", "\u4ea7\u7269"}, terms: []string{"save", "artifact"}},
	{phrases: []string{"\u6587\u4ef6"}, terms: []string{"file"}},
	{phrases: []string{"\u9879\u76ee"}, terms: []string{"project"}},
	{phrases: []string{"\u4efb\u52a1", "\u4f1a\u8bdd", "\u5bf9\u8bdd"}, terms: []string{"task", "session", "conversation"}},
	{phrases: []string{"\u5de5\u5177"}, terms: []string{"tool"}},
	{phrases: []string{"\u6280\u80fd"}, terms: []string{"skill"}},
	{phrases: []string{"\u5316\u5408\u7269", "\u5c0f\u5206\u5b50"}, terms: []string{"compound", "molecule", "chemistry"}},
	{phrases: []string{"\u5206\u5b50"}, terms: []string{"molecule"}},
	{phrases: []string{"\u7ed3\u5408\u53e3\u888b", "\u86cb\u767d\u53e3\u888b", "\u53d7\u4f53\u53e3\u888b", "\u53e3\u888b\u6761\u4ef6", "\u53e3\u888b\u9a71\u52a8"}, terms: []string{"pocket", "structure-based"}},
	{phrases: []string{"\u5206\u5b50\u751f\u6210", "\u5206\u5b50\u8bbe\u8ba1", "\u5206\u5b50\u5019\u9009", "\u5c0f\u5206\u5b50\u5019\u9009"}, terms: []string{"generation", "design"}},
	{phrases: []string{"\u86cb\u767d\u8bbe\u8ba1", "\u86cb\u767d\u8d28\u8bbe\u8ba1", "\u7ed3\u5408\u86cb\u767d", "\u86cb\u767d\u9aa8\u67b6\u751f\u6210"}, terms: []string{"protein", "design", "binder", "backbone"}},
	{phrases: []string{"\u6297\u4f53", "\u6297\u4f53\u8bbe\u8ba1", "\u7eb3\u7c73\u6297\u4f53", "\u7eb3\u7c73\u6297\u4f53\u8bbe\u8ba1", "cdr", "cdr\u8bbe\u8ba1", "cdr\u4f18\u5316", "\u6297\u4f53\u4eba\u6e90\u5316"}, terms: []string{"antibody", "nanobody", "cdr", "design"}},
	{phrases: []string{"rna\u8bbe\u8ba1", "rna\u53cd\u5411\u6298\u53e0", "\u4e8c\u7ea7rna\u7ed3\u6784\u8bbe\u8ba1", "\u4e09\u7ef4rna\u8bbe\u8ba1"}, terms: []string{"rna", "design", "inverse-folding"}},
	{phrases: []string{"\u6838\u9178\u8bbe\u8ba1"}, terms: []string{"nucleic-acid", "design"}},
	{phrases: []string{"\u914d\u4f53"}, terms: []string{"ligand"}},
	{phrases: []string{"\u5206\u5b50\u5bf9\u63a5", "\u5bf9\u63a5"}, terms: []string{"docking", "dock"}},
	{phrases: []string{"\u7ed3\u5408\u6a21\u5f0f", "\u76f8\u4e92\u4f5c\u7528"}, terms: []string{"interaction", "binding"}},
	{phrases: []string{"\u7406\u5316\u6027\u8d28", "\u5316\u5b66\u6027\u8d28"}, terms: []string{"chemical", "properties"}},
	{phrases: []string{"\u7ed3\u6784\u56fe", "\u6e32\u67d3"}, terms: []string{"render", "structure"}},
	{phrases: []string{"\u7ed3\u6784"}, terms: []string{"structure"}},
	{phrases: []string{"\u86cb\u767d\u8d28", "\u86cb\u767d"}, terms: []string{"protein"}},
	{phrases: []string{"\u5e8f\u5217"}, terms: []string{"sequence"}},
	{phrases: []string{"\u4e13\u5229"}, terms: []string{"patent"}},
	{phrases: []string{"\u6587\u732e", "\u8bba\u6587"}, terms: []string{"literature", "article", "paper"}},
	{phrases: []string{"\u6570\u636e"}, terms: []string{"data"}},
	{phrases: []string{"\u5206\u6790"}, terms: []string{"analysis"}},
	{phrases: []string{"\u809d\u810f", "\u809d"}, terms: []string{"liver"}},
	{phrases: []string{"\u4ee3\u8c22"}, terms: []string{"metabolism"}},
	{phrases: []string{"\u8f6c\u8fd0\u4f53", "\u8f6c\u8fd0\u86cb\u767d"}, terms: []string{"transporter"}},
	{phrases: []string{"\u8bc4\u4f30", "\u8bc4\u4ef7"}, terms: []string{"assessment"}},
	{phrases: []string{"\u65b9\u6cd5", "\u7b56\u7565"}, terms: []string{"methods"}},
	{phrases: []string{"\u5236\u5242", "\u5904\u65b9"}, terms: []string{"formulation", "dosage", "pharmaceutical"}},
	{phrases: []string{"\u5de5\u827a"}, terms: []string{"process", "manufacturing"}},
	{phrases: []string{"\u653e\u5927"}, terms: []string{"scale-up", "scaleup"}},
	{phrases: []string{"\u7a33\u5b9a\u6027"}, terms: []string{"stability"}},
	{phrases: []string{"\u4e34\u5e8a"}, terms: []string{"clinical"}},
	{phrases: []string{"\u836f\u7406"}, terms: []string{"pharmacology", "pharmacological"}},
	{phrases: []string{"\u6676\u578b"}, terms: []string{"polymorph", "solid-state", "crystal"}},
	{phrases: []string{"\u836f\u7269"}, terms: []string{"drug", "pharmaceutical"}},
	{phrases: []string{"\u53e3\u670d"}, terms: []string{"oral"}},
	{phrases: []string{"\u80bd", "\u591a\u80bd"}, terms: []string{"peptide"}},
	{phrases: []string{"\u9012\u9001", "\u7ed9\u836f"}, terms: []string{"delivery", "dosing"}},
	{phrases: []string{"\u5438\u6536\u4fc3\u8fdb", "\u6e17\u900f\u4fc3\u8fdb"}, terms: []string{"absorption", "permeation", "enhancer"}},
	{phrases: []string{"\u8102\u8d28\u7eb3\u7c73", "\u8102\u8d28\u8f7d\u4f53"}, terms: []string{"lipid", "nanoparticle", "carrier"}},
	{phrases: []string{"\u79bb\u5b50\u6db2\u4f53"}, terms: []string{"ionic", "liquid"}},
	{phrases: []string{"\u4e34\u5e8a\u8bd5\u9a8c"}, terms: []string{"clinical", "trial"}},
	{phrases: []string{"\u611f\u67d3"}, terms: []string{"infection"}},
	{phrases: []string{"\u6210\u4eba"}, terms: []string{"adult"}},
}

// Terms returns deterministic search terms for multilingual discovery. CJK
// n-grams match metadata written in the same language, while bounded aliases
// bridge common Chinese intents to English-only capability descriptions.
func Terms(query string) []string {
	normalized := strings.ToLower(strings.TrimSpace(query))
	if normalized == "" {
		return nil
	}
	compactNormalized := compactAliasText(normalized)

	terms := make([]string, 0, 24)
	seen := make(map[string]struct{}, 24)
	appendTerm := func(term string) {
		term = strings.TrimSpace(strings.ToLower(term))
		if term == "" || numericMeasurementTerm(term) || len(terms) >= maxTerms {
			return
		}
		if _, stop := englishStopTerms[term]; stop {
			return
		}
		if _, exists := seen[term]; exists {
			return
		}
		seen[term] = struct{}{}
		terms = append(terms, term)
	}

	tokens := tokenize(normalized)
	for _, token := range tokens {
		appendTerm(token)
	}

	for _, group := range chineseAliases {
		matched := false
		for _, phrase := range group.phrases {
			if aliasPhraseMatches(normalized, compactNormalized, phrase) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		for _, term := range group.terms {
			appendTerm(term)
		}
	}

	for _, token := range tokens {
		runes := []rune(token)
		if !containsHan(runes) || len(runes) < 2 {
			continue
		}
		for size := 2; size <= 4 && size <= len(runes); size++ {
			for start := 0; start+size <= len(runes); start++ {
				appendTerm(string(runes[start : start+size]))
			}
		}
	}
	return terms
}

var crossLanguageOperationalTerms = map[string]bool{
	"search": true, "query": true, "find": true, "lookup": true,
	"read": true, "get": true, "fetch": true, "download": true,
	"save": true, "artifact": true, "file": true, "project": true,
	"task": true, "session": true, "conversation": true, "tool": true,
	"skill": true, "data": true, "analysis": true,
}

// CrossLanguageVariants derives bounded Chinese/English discovery variants
// from the shared scientific vocabulary. It preserves explicit identifiers
// and complements caller-supplied expert synonyms; it is not a general-purpose
// translation service and never rewrites the user's primary query.
func CrossLanguageVariants(query string) []string {
	normalized := strings.ToLower(strings.TrimSpace(query))
	if normalized == "" {
		return nil
	}
	compactNormalized := compactAliasText(normalized)
	hasChinese := containsHan([]rune(normalized))
	parts := make([]string, 0, 16)
	seen := map[string]bool{}
	appendPart := func(value string) {
		value = strings.TrimSpace(strings.ToLower(value))
		if value == "" || seen[value] || crossLanguageOperationalTerms[value] {
			return
		}
		seen[value] = true
		parts = append(parts, value)
	}
	if hasChinese {
		for _, token := range tokenize(normalized) {
			if !containsHan([]rune(token)) {
				appendPart(token)
			}
		}
		for _, group := range chineseAliases {
			matched := false
			for _, phrase := range group.phrases {
				if aliasPhraseMatches(normalized, compactNormalized, phrase) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
			// A cross-language bridge is a discovery lane, not a thesaurus.
			// Keep one representative per matched concept so a generic bag of
			// synonyms cannot make unrelated pages look relevant. The full
			// synonym set remains available to Terms for screening metadata.
			if len(group.terms) > 0 {
				appendPart(group.terms[0])
			}
		}
	} else {
		terms := map[string]bool{}
		for _, token := range tokenize(normalized) {
			terms[strings.ToLower(token)] = true
			if scientificIdentifierToken(token) {
				appendPart(token)
			}
		}
		for _, group := range chineseAliases {
			matched := false
			for _, term := range group.terms {
				if terms[strings.ToLower(term)] && !crossLanguageOperationalTerms[strings.ToLower(term)] {
					matched = true
					break
				}
			}
			if matched && len(group.phrases) > 0 {
				appendPart(group.phrases[0])
			}
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return []string{strings.Join(parts, " ")}
}

func aliasPhraseMatches(normalized, compactNormalized, phrase string) bool {
	phrase = strings.ToLower(strings.TrimSpace(phrase))
	if phrase == "" {
		return false
	}
	return strings.Contains(normalized, phrase) ||
		strings.Contains(compactNormalized, compactAliasText(phrase))
}

func compactAliasText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, value)
}

func scientificIdentifierToken(value string) bool {
	hasLetter := false
	hasDigit := false
	for _, character := range value {
		switch {
		case unicode.IsLetter(character):
			hasLetter = true
		case unicode.IsDigit(character):
			hasDigit = true
		}
	}
	return hasLetter && hasDigit
}

// numericMeasurementTerm removes values such as 1, 25, 0.10, and -3.5 from
// capability discovery. They are essential task data but almost never identify
// a Skill, and otherwise match version numbers throughout large Skill bodies.
// Alphanumeric scientific identifiers such as 9xzg, tp53, or q1e remain.
func numericMeasurementTerm(value string) bool {
	hasDigit := false
	for _, r := range value {
		switch {
		case unicode.IsDigit(r):
			hasDigit = true
		case r == '.', r == '+', r == '-':
		default:
			return false
		}
	}
	return hasDigit
}

func tokenize(query string) []string {
	return strings.FieldsFunc(query, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.' || r == '/' || r == ':')
	})
}

func containsHan(value []rune) bool {
	for _, r := range value {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}
