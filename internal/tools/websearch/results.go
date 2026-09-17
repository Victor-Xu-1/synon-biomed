package websearch

import (
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"
)

func normalizeHit(item scriptResult) (Hit, bool) {
	rawURL := firstNonEmpty(item.URL, item.Href, item.Link)
	title := normalizeSpace(item.Title)
	link := strings.TrimSpace(rawURL)
	snippet := compactString(normalizeSpace(firstNonEmpty(item.Snippet, item.Body)), maxSearchSnippetCharacters)
	if title == "" || link == "" || !strings.HasPrefix(link, "http") {
		return Hit{}, false
	}
	if _, err := url.ParseRequestURI(link); err != nil {
		return Hit{}, false
	}
	return Hit{Title: title, URL: link, Snippet: snippet, Record: item.Record}, true
}

func searchDisplayHits(hits []Hit) []Hit {
	result := make([]Hit, 0, len(hits))
	for _, hit := range hits {
		hit.Record = nil
		result = append(result, hit)
	}
	return result
}

func evidenceForHit(query string, hit Hit, rank int, retrievedAt string) Evidence {
	qualityScore, qualitySignals := qualityForHit(hit, rank)
	return Evidence{
		SchemaVersion:  1,
		Kind:           "search_result",
		Status:         "search_result",
		EvidenceState:  "discovered",
		SourceQuality:  "discovery",
		URL:            hit.URL,
		CanonicalURL:   canonicalURL(hit.URL),
		Host:           hostOf(hit.URL),
		DomainKey:      domainKey(hit.URL),
		Title:          hit.Title,
		Provider:       providerName,
		Rank:           rank,
		Snippet:        hit.Snippet,
		QualityScore:   qualityScore,
		QualitySignals: qualitySignals,
		RetrievedAt:    retrievedAt,
		Record:         hit.Record,
		Metadata: map[string]any{
			"selectionReason":    "ranked_by_query_relevance_then_source_quality_then_provider_order",
			"relevanceScore":     searchHitRelevanceScore(query, hit),
			"sourceQualityScore": searchHitSourceQualityScore(hit),
			"originalRank":       rank,
		},
	}
}

func qualityForHit(hit Hit, rank int) (int, []string) {
	score := 45
	signals := []string{"search_result"}
	if hit.Snippet != "" {
		score += 15
		signals = append(signals, "has_snippet")
	}
	if rank <= 3 {
		score += 15
		signals = append(signals, "top_rank")
	}
	if authority := searchHitSourceQualityScore(hit); authority >= 20 {
		score += 10
		signals = append(signals, "authoritative_domain")
	}
	if strings.HasPrefix(hit.URL, "https://") {
		score += 5
		signals = append(signals, "https")
	}
	if score > 100 {
		score = 100
	}
	sort.Strings(signals)
	return score, signals
}

func domainAllowed(rawURL string, domains []string) bool {
	normalized := normalizeDomains(domains)
	if len(normalized) == 0 {
		return true
	}
	host := hostOf(rawURL)
	for _, domain := range normalized {
		expected := strings.TrimPrefix(domain, "*.")
		if host == expected || strings.HasSuffix(host, "."+expected) {
			return true
		}
	}
	return false
}

func domainBlocked(rawURL string, domains []string) bool {
	normalized := normalizeDomains(domains)
	if len(normalized) == 0 {
		return false
	}
	host := hostOf(rawURL)
	for _, domain := range normalized {
		expected := strings.TrimPrefix(domain, "*.")
		if host == expected || strings.HasSuffix(host, "."+expected) {
			return true
		}
	}
	return false
}

func normalizeDomains(domains []string) []string {
	out := []string{}
	for _, domain := range domains {
		value := strings.ToLower(strings.TrimSpace(domain))
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func queryWithAllowedDomains(query string, domains []string) string {
	normalized := normalizeDomains(domains)
	if len(normalized) == 0 {
		return query
	}
	parts := make([]string, 0, len(normalized))
	for _, domain := range normalized {
		parts = append(parts, "site:"+domain)
	}
	return query + " " + strings.Join(parts, " OR ")
}

func normalizeMaxResults(value int) int {
	if value <= 0 {
		return defaultSearchResultLimit
	}
	if value > maxSearchResultLimit {
		return maxSearchResultLimit
	}
	return value
}

func canonicalURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	parsed.Fragment = ""
	parsed.Host = strings.ToLower(parsed.Host)
	return parsed.String()
}

func hostOf(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

func domainKey(rawURL string) string {
	host := hostOf(rawURL)
	parts := strings.Split(host, ".")
	if len(parts) <= 2 {
		return host
	}
	return strings.Join(parts[len(parts)-2:], ".")
}

func normalizeSpace(value string) string {
	return strings.Join(strings.Fields(htmlDecode(value)), " ")
}

func htmlDecode(value string) string {
	replacer := strings.NewReplacer(
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", "\"",
		"&#39;", "'",
	)
	return replacer.Replace(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func compactString(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	const suffix = "...[truncated]"
	contentLimit := limit - len(suffix)
	appendSuffix := contentLimit > 0
	if !appendSuffix {
		contentLimit = limit
	}
	prefix := value[:contentLimit]
	for len(prefix) > 0 && !utf8.ValidString(prefix) {
		prefix = prefix[:len(prefix)-1]
	}
	if appendSuffix {
		return prefix + suffix
	}
	return prefix
}
