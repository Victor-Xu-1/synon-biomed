package websearch

import (
	"encoding/base64"
	"html"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"synon-go/internal/discoveryquery"
)

func deduplicateHits(input []Hit, limit int) []Hit {
	seen := make(map[string]struct{}, len(input))
	output := make([]Hit, 0, min(limit, len(input)))
	for _, hit := range input {
		key := canonicalURL(hit.URL)
		if key == "" {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		output = append(output, hit)
		if len(output) >= limit {
			break
		}
	}
	return output
}

func parseHTTPHTMLResults(body string) []scriptResult {
	if results := parseBingHTMLResults(body); len(results) > 0 {
		return results
	}
	if results := parseDuckDuckGoHTMLResults(body); len(results) > 0 {
		return results
	}
	anchorRe := regexp.MustCompile(`(?is)<a\b[^>]*href=["']([^"']+)["'][^>]*>(.*?)</a>`)
	snippetRe := regexp.MustCompile(`(?is)<[^>]*class=["'][^"']*(?:result__snippet|snippet)[^"']*["'][^>]*>(.*?)</[^>]+>`)
	snippets := snippetRe.FindAllStringSubmatch(body, -1)
	anchors := anchorRe.FindAllStringSubmatch(body, -1)
	results := make([]scriptResult, 0, len(anchors))
	for index, anchor := range anchors {
		if len(anchor) < 3 {
			continue
		}
		link := normalizeSearchResultURL(anchor[1])
		title := normalizeSpace(stripHTML(anchor[2]))
		if link == "" || title == "" {
			continue
		}
		snippet := ""
		if index < len(snippets) && len(snippets[index]) >= 2 {
			snippet = normalizeSpace(stripHTML(snippets[index][1]))
		}
		results = append(results, scriptResult{Title: title, URL: link, Snippet: snippet})
	}
	return results
}

func parseBingHTMLResults(body string) []scriptResult {
	blockRe := regexp.MustCompile(`(?is)<li\b[^>]*class=["'][^"']*\bb_algo\b[^"']*["'][^>]*>(.*?)</li>`)
	anchorRe := regexp.MustCompile(`(?is)<h2\b[^>]*>\s*<a\b[^>]*href=["']([^"']+)["'][^>]*>(.*?)</a>`)
	snippetRe := regexp.MustCompile(`(?is)<p\b[^>]*>(.*?)</p>`)
	blocks := blockRe.FindAllStringSubmatch(body, -1)
	results := make([]scriptResult, 0, len(blocks))
	for _, block := range blocks {
		if len(block) < 2 {
			continue
		}
		anchor := anchorRe.FindStringSubmatch(block[1])
		if len(anchor) < 3 {
			continue
		}
		link := normalizeSearchResultURL(anchor[1])
		title := normalizeSpace(stripHTML(anchor[2]))
		if link == "" || title == "" {
			continue
		}
		snippet := ""
		if match := snippetRe.FindStringSubmatch(block[1]); len(match) >= 2 {
			snippet = normalizeSpace(stripHTML(match[1]))
		}
		results = append(results, scriptResult{Title: title, URL: link, Snippet: snippet})
	}
	return results
}

func normalizeSearchResultURL(raw string) string {
	value := strings.TrimSpace(html.UnescapeString(raw))
	if strings.HasPrefix(value, "//") {
		value = "https:" + value
	}
	if strings.HasPrefix(value, "/") {
		parsed, err := url.Parse("https://duckduckgo.com" + value)
		if err == nil {
			if uddg := parsed.Query().Get("uddg"); strings.TrimSpace(uddg) != "" {
				return uddg
			}
		}
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	// DuckDuckGo Lite uses protocol-relative /l/?uddg= redirect links. Decode
	// them after URL normalization as well as for root-relative links; treating
	// the redirect host as the result made every otherwise valid Lite hit fail
	// provider-domain filtering.
	if strings.HasSuffix(strings.ToLower(parsed.Hostname()), "duckduckgo.com") {
		if target := strings.TrimSpace(parsed.Query().Get("uddg")); target != "" {
			decoded, err := url.Parse(target)
			if err == nil && (decoded.Scheme == "http" || decoded.Scheme == "https") && decoded.Hostname() != "" {
				return decoded.String()
			}
		}
	}
	if target := decodeBingResultURL(parsed); target != "" {
		return target
	}
	return parsed.String()
}

// Bing's HTML results frequently use /ck/a redirect links. The destination is
// carried in the `u` query parameter as URL-safe base64 with an `a1` marker.
// Decode it before provider-domain filtering so a real external result is not
// mistaken for a Bing navigation link and discarded.
func decodeBingResultURL(parsed *url.URL) string {
	if parsed == nil || !strings.HasSuffix(strings.ToLower(parsed.Hostname()), "bing.com") || parsed.Path != "/ck/a" {
		return ""
	}
	encoded := strings.TrimSpace(parsed.Query().Get("u"))
	if len(encoded) <= 2 || !strings.EqualFold(encoded[:2], "a1") {
		return ""
	}
	encoded = encoded[2:]
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		decoded, err = base64.URLEncoding.DecodeString(encoded)
	}
	if err != nil {
		return ""
	}
	target, err := url.Parse(strings.TrimSpace(string(decoded)))
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Hostname() == "" {
		return ""
	}
	return target.String()
}

func stripHTML(value string) string {
	withoutTags := regexp.MustCompile(`(?is)<[^>]+>`).ReplaceAllString(value, " ")
	return html.UnescapeString(withoutTags)
}

func resolveScriptPath(_ string) string {
	if value := strings.TrimSpace(os.Getenv("SYNON_WEBSEARCH_SCRIPT")); value != "" {
		return filepath.Clean(value)
	}
	return ""
}

func resolvePython(root string) (string, bool) {
	if value := strings.TrimSpace(os.Getenv("SYNON_WEBSEARCH_PYTHON")); value != "" {
		return value, fileExists(value)
	}
	candidates := []string{}
	if root != "" {
		candidates = append(candidates, filepath.Join(root, ".venv-websearch", "bin", "python"))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, ".venv-websearch", "bin", "python"))
	}
	for _, candidate := range candidates {
		if fileExists(candidate) {
			return candidate, true
		}
	}
	for _, name := range []string{"python3", "python"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, true
		}
	}
	return "python3", false
}

func selectHits(raw []scriptResult, input Input, maxResults int) []Hit {
	seen := map[string]struct{}{}
	hits := []Hit{}
	for _, item := range raw {
		hit, ok := normalizeHit(item)
		if !ok {
			continue
		}
		if !domainAllowed(hit.URL, input.AllowedDomains) || domainBlocked(hit.URL, input.BlockedDomains) {
			continue
		}
		key := canonicalURL(hit.URL)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		hits = append(hits, hit)
		if len(hits) >= maxResults {
			break
		}
	}
	return hits
}

func selectRelevantHits(raw []scriptResult, input Input, maxResults int) []Hit {
	relevant := make([]scriptResult, 0, len(raw))
	for _, item := range raw {
		if searchResultRelevant(input.Query, item) {
			relevant = append(relevant, item)
		}
	}
	return selectHits(relevant, input, maxResults)
}

func searchResultRelevant(query string, item scriptResult) bool {
	contentTokens := searchTokenSet(item.Title + " " + item.Snippet + " " + item.URL)
	identifiers := searchQueryExactIdentifiers(query)
	if len(identifiers) > 0 {
		for _, identifier := range identifiers {
			if contentTokens[identifier] {
				return true
			}
		}
		return false
	}
	biomedicalIdentifiers := searchQueryBiomedicalIdentifiers(query)
	if len(biomedicalIdentifiers) > 0 {
		for _, identifier := range biomedicalIdentifiers {
			if contentTokens[identifier] {
				return true
			}
		}
		return false
	}
	terms := searchQueryTerms(query)
	if len(terms) == 0 {
		return false
	}
	matched := 0
	requiredMatches := 2
	if len(terms) < requiredMatches {
		requiredMatches = len(terms)
	}
	for _, term := range terms {
		if contentTokens[term] {
			matched++
			if matched >= requiredMatches {
				return true
			}
		}
	}
	return false
}

func searchQueryContainsNonLatinLetterOrNumber(query string) bool {
	for _, character := range query {
		if (unicode.IsLetter(character) || unicode.IsDigit(character)) &&
			!((character >= 'a' && character <= 'z') ||
				(character >= 'A' && character <= 'Z') ||
				(character >= '0' && character <= '9')) {
			return true
		}
	}
	return false
}

func searchQueryTerms(query string) []string {
	stopWords := map[string]bool{
		"and": true, "for": true, "from": true, "into": true, "the": true, "with": true,
		"search": true, "latest": true, "news": true, "result": true, "results": true,
		"article": true, "archive": true, "data": true, "database": true, "databank": true,
		"journal": true, "page": true, "paper": true, "pdb": true, "pmc": true, "protein": true,
		"pubmed": true, "rcsb": true, "research": true, "structure": true, "structures": true,
		"study": true, "tutorial": true, "website": true, "wwpdb": true,
	}
	seen := map[string]bool{}
	terms := []string{}
	words := append([]string(nil), strings.Fields(normalizeSearchText(query))...)
	words = append(words, discoveryquery.Terms(query)...)
	for _, word := range words {
		word = canonicalSearchWord(strings.ToLower(strings.TrimSpace(word)))
		if len([]rune(word)) < 2 || stopWords[word] || seen[word] {
			continue
		}
		seen[word] = true
		terms = append(terms, word)
	}
	return terms
}

func searchQueryExactIdentifiers(query string) []string {
	identifiers := []string{}
	seen := map[string]bool{}
	for _, token := range strings.Fields(normalizeSearchText(query)) {
		if !isPDBIdentifierToken(token) || seen[token] {
			continue
		}
		seen[token] = true
		identifiers = append(identifiers, token)
	}
	return identifiers
}

// searchQueryBiomedicalIdentifiers preserves explicit target, variant, and
// compound identifiers such as LRRK2, G2019S, or BIIB122. Generic topical
// overlap (for example only "PROTAC degrader") is not adequate evidence for
// a query that names one of these identifiers. PDB identifiers remain the
// stronger, separately handled contract above.
func searchQueryBiomedicalIdentifiers(query string) []string {
	identifiers := []string{}
	seen := map[string]bool{}
	for _, token := range strings.Fields(normalizeSearchText(query)) {
		token = canonicalSearchWord(token)
		if isPDBIdentifierToken(token) || !isBiomedicalIdentifierToken(token) || seen[token] {
			continue
		}
		seen[token] = true
		identifiers = append(identifiers, token)
	}
	return identifiers
}

func isBiomedicalIdentifierToken(token string) bool {
	if len(token) < 3 || len(token) > 24 || strings.HasPrefix(token, "phase") {
		return false
	}
	hasLetter := false
	hasDigit := false
	for _, character := range token {
		switch {
		case character >= 'a' && character <= 'z':
			hasLetter = true
		case character >= '0' && character <= '9':
			hasDigit = true
		default:
			return false
		}
	}
	return hasLetter && hasDigit
}

func isPDBIdentifierToken(token string) bool {
	if len(token) != 4 || token[0] < '0' || token[0] > '9' {
		return false
	}
	hasLetter := false
	for index := 1; index < len(token); index++ {
		character := token[index]
		if character >= 'a' && character <= 'z' {
			hasLetter = true
			continue
		}
		if character < '0' || character > '9' {
			return false
		}
	}
	return hasLetter
}

func searchTokenSet(value string) map[string]bool {
	tokens := map[string]bool{}
	words := append([]string(nil), strings.Fields(normalizeSearchText(value))...)
	words = append(words, discoveryquery.Terms(value)...)
	for _, token := range words {
		token = canonicalSearchWord(strings.ToLower(strings.TrimSpace(token)))
		if token == "" {
			continue
		}
		tokens[token] = true
	}
	return tokens
}

func canonicalSearchWord(word string) string {
	if len(word) > 4 && strings.HasSuffix(word, "s") && !strings.HasSuffix(word, "ss") {
		return strings.TrimSuffix(word, "s")
	}
	return word
}

func normalizeSearchText(value string) string {
	var builder strings.Builder
	for _, character := range strings.ToLower(htmlDecode(value)) {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			builder.WriteRune(character)
		} else if character == '-' || character == '_' {
			continue
		} else {
			builder.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(builder.String()), " ")
}
