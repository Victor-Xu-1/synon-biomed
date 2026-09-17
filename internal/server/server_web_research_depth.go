package server

import (
	"html"
	"regexp"
	"strings"

	"synon-go/internal/discoveryquery"
	"synon-go/internal/tools/webfetch"
)

const webResearchMinimumSubstantiveCharacters = 1200

var (
	webResearchHTMLTitlePattern = regexp.MustCompile(`(?is)<title\b[^>]*>(.*?)</title>`)
	webResearchHTMLBreakPattern = regexp.MustCompile(`(?is)<(?:br\s*/?|/?(?:p|div|li|tr|h[1-6]|section|article))\b[^>]*>`)
)

// webResearchReadableDocument projects a fetched page into the evidence text
// the model actually needs to read. Keeping navigation markup, scripts, and
// styling in every research document consumed context without adding evidence
// and made a nominal "fetch" look deeper than it was.
func webResearchReadableDocument(content, contentType string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	lowerType := strings.ToLower(strings.TrimSpace(contentType))
	looksHTML := strings.Contains(lowerType, "html") || strings.Contains(strings.ToLower(content), "<html")
	if !looksHTML {
		return content
	}
	body := content
	if match := webResearchHTMLBodyPattern.FindStringSubmatch(content); len(match) == 2 {
		body = match[1]
	}
	body = webResearchNonContentHTMLPattern.ReplaceAllString(body, " ")
	body = webResearchHTMLBreakPattern.ReplaceAllString(body, "\n")
	body = webResearchHTMLTagPattern.ReplaceAllString(body, " ")
	body = html.UnescapeString(body)
	lines := make([]string, 0)
	for _, line := range strings.Split(body, "\n") {
		if normalized := strings.Join(strings.Fields(line), " "); normalized != "" {
			lines = append(lines, normalized)
		}
	}
	return strings.Join(lines, "\n")
}

func webResearchDocumentTitle(content, fallback string) string {
	if match := webResearchHTMLTitlePattern.FindStringSubmatch(content); len(match) == 2 {
		if title := strings.Join(strings.Fields(html.UnescapeString(webResearchHTMLTagPattern.ReplaceAllString(match[1], " "))), " "); title != "" {
			return title
		}
	}
	return fallback
}

func webResearchReadReceipt(result webfetch.Result, readable string, optionalQuery ...string) map[string]any {
	characters := len([]rune(strings.TrimSpace(readable)))
	complete := !result.Truncated && !result.Partial && !result.Binary && !result.SourceUnavailable
	query := ""
	if len(optionalQuery) > 0 {
		query = strings.TrimSpace(optionalQuery[0])
	}
	queryRelevant, matchedTerms, comparableTerms := webResearchDocumentQueryRelevance(query, readable)
	depth := "thin_page"
	deep := false
	if result.Truncated || result.Partial {
		depth = "partial_page"
	} else if complete && query != "" && !queryRelevant {
		depth = "off_topic_page"
	} else if complete && characters >= webResearchMinimumSubstantiveCharacters {
		depth = "complete_page"
		deep = true
	}
	return map[string]any{
		"depth":                 depth,
		"deepRead":              deep,
		"responseComplete":      complete,
		"extractableCharacters": float64(characters),
		"bytesRead":             float64(result.BytesRead),
		"contentLength":         float64(result.ContentLength),
		"contentType":           result.ContentType,
		"truncated":             result.Truncated,
		"partial":               result.Partial,
		"queryRelevant":         queryRelevant,
		"matchedQueryTerms":     matchedTerms,
		"matchedQueryTermCount": float64(len(matchedTerms)),
		"queryTermCount":        float64(comparableTerms),
	}
}

var webResearchNonSemanticQueryTerms = map[string]bool{
	"search": true, "query": true, "find": true, "lookup": true, "read": true,
	"get": true, "fetch": true, "data": true, "analysis": true, "research": true,
	"article": true, "paper": true, "literature": true, "result": true, "results": true,
}

var webResearchASCIIQueryTokenPattern = regexp.MustCompile(`[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?`)

// webResearchDocumentQueryRelevance prevents a completely read but unrelated
// page from satisfying a deep-research target. Cross-language aliases come
// from the same bounded discovery vocabulary used by Tool and Skill routing;
// this is language normalization rather than task-specific ranking data.
func webResearchDocumentQueryRelevance(query, readable string) (bool, []string, int) {
	query = strings.TrimSpace(query)
	if query == "" {
		return true, nil, 0
	}
	lowerDocument := strings.ToLower(readable)
	for _, identifier := range webResearchExplicitIdentifierTerms(query) {
		if !webResearchDocumentContainsIdentifier(lowerDocument, identifier) {
			return false, nil, len(discoveryquery.Terms(query))
		}
	}
	seen := map[string]bool{}
	matched := make([]string, 0, 8)
	comparable := 0
	for _, raw := range discoveryquery.Terms(query) {
		term := strings.ToLower(strings.TrimSpace(raw))
		if term == "" || seen[term] || webResearchNonSemanticQueryTerms[term] {
			continue
		}
		seen[term] = true
		// Long CJK tokens represent an entire unsegmented phrase. Their 2-4
		// character n-grams and bilingual aliases provide the comparable units.
		if runes := []rune(term); len(runes) > 4 && containsHanRunes(runes) {
			continue
		}
		comparable++
		if strings.Contains(lowerDocument, term) {
			matched = append(matched, term)
		}
	}
	required := (comparable + 2) / 3
	if required < 2 {
		required = min(2, comparable)
	}
	if required > 4 {
		required = 4
	}
	return required > 0 && len(matched) >= required, matched, comparable
}

// Explicit acronyms and alphanumeric identifiers bind a source to the entity
// the caller asked about. Generic topical overlap must not turn a document
// about a different named entity into evidence. This is a provider-neutral
// lexical boundary; it contains no domain or task vocabulary.
func webResearchExplicitIdentifierTerms(query string) []string {
	seen := map[string]bool{}
	identifiers := make([]string, 0, 8)
	for _, token := range webResearchASCIIQueryTokenPattern.FindAllString(query, -1) {
		letters, digits := 0, 0
		for _, character := range token {
			switch {
			case character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z':
				letters++
			case character >= '0' && character <= '9':
				digits++
			}
		}
		allCaps := letters >= 2 && token == strings.ToUpper(token) && token != strings.ToLower(token)
		if letters == 0 || (digits == 0 && !allCaps) {
			continue
		}
		normalized := strings.ToLower(token)
		if seen[normalized] {
			continue
		}
		seen[normalized] = true
		identifiers = append(identifiers, normalized)
	}
	return identifiers
}

func webResearchDocumentContainsIdentifier(lowerDocument, identifier string) bool {
	if strings.Contains(lowerDocument, identifier) {
		return true
	}
	compact := strings.NewReplacer("-", "", "_", "", ".", "").Replace(identifier)
	if compact == identifier || compact == "" {
		return false
	}
	return strings.Contains(strings.NewReplacer("-", "", "_", "", ".", "").Replace(lowerDocument), compact)
}

func containsHanRunes(value []rune) bool {
	for _, character := range value {
		if character >= '\u4e00' && character <= '\u9fff' {
			return true
		}
	}
	return false
}

func webResearchSourceDeepRead(source map[string]any) bool {
	receipt := mapValue(source["readReceipt"])
	if len(receipt) == 0 {
		return false
	}
	return boolValue(receipt["deepRead"], false)
}

func webResearchEvidenceDocumentEligible(readReceipt map[string]any) bool {
	return boolValue(readReceipt["queryRelevant"], false)
}

// Meeting a retrieval batch target ends this call, not the scientific task.
// A partial batch must never be promoted to sufficient research by a heuristic.
func webResearchQualityStopReason(sources []any, target webResearchQualityTarget) string {
	quality := webResearchQualityWithTarget(sources, target, 1)
	if boolValue(quality["meetsTarget"], false) {
		return "quality_target_met"
	}
	return ""
}
