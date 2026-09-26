package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"synon-go/internal/buildinfo"
	"synon-go/internal/httpreliability"
)

func webSearchUserAgent() string {
	identity := buildinfo.Release()
	return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36 " +
		identity.MachineSlug + "-web-search/" + identity.Version
}

func searchHTTPBackends(ctx context.Context, input Input, maxResults int, options Options) ([]Hit, map[string]any, error) {
	backends := resolveHTTPBackends(options)
	candidateLimit := webSearchCandidatePoolLimit(maxResults)
	diagnostics := map[string]any{
		"goHTTPBackend": true, "httpBackendCount": len(backends),
		"candidatePoolLimit": candidateLimit, "returnedResultLimit": maxResults,
		"selectionMode": "broad_candidate_pool_then_rank_deduplicate_and_select",
	}
	if len(backends) == 1 {
		diagnostics["httpEndpoint"] = backends[0].Endpoint
	}
	timeout := options.HeaderTimeout
	if timeout <= 0 {
		timeout = httpreliability.DefaultHeaderTimeout
	}
	clientForURL := options.ClientForURL
	if clientForURL == nil {
		clientForURL = newWebSearchHTTPClient(timeout, options.BaseHTTPClient)
	}
	results := make(chan httpSearchBackendResult, len(backends))
	for index, backend := range backends {
		go func() {
			result := searchOneHTTPBackend(ctx, input, candidateLimit, options, index, backend, clientForURL)
			results <- result
		}()
	}
	collected := make([]httpSearchBackendResult, len(backends))
	for range backends {
		result := <-results
		collected[result.Index] = result
	}

	providerStates := make([]map[string]any, 0, len(collected))
	providerTotals := make(map[string]any)
	providerContinuations := make(map[string]string)
	completedBackends := 0
	for _, result := range collected {
		state := map[string]any{
			"name": result.Name, "status": result.StatusCode, "rawResults": result.RawResults,
			"returnedResults": len(result.Hits), "requestedLimit": result.RequestedLimit,
			"request": result.RequestState.mapValue(),
		}
		state["outcome"], state["attempts"] = result.Outcome, result.Attempts
		if result.Err == nil {
			completedBackends++
		}
		if result.TotalKnown {
			state["providerTotalKnown"] = true
			state["providerTotal"] = result.TotalResults
			providerTotals[result.Name] = result.TotalResults
		}
		if result.NextCursor != "" {
			state["continuationAvailable"] = true
			providerContinuations[result.Name] = result.NextCursor
		}
		if result.Err != nil {
			state["error"] = compactString(result.Err.Error(), 300)
		}
		providerStates = append(providerStates, state)
	}
	diagnostics["httpBackends"] = providerStates
	diagnostics["completedBackends"] = completedBackends
	diagnostics["failedBackends"] = len(backends) - completedBackends
	if len(providerTotals) > 0 {
		diagnostics["providerTotals"] = providerTotals
	}
	if len(providerContinuations) > 0 {
		diagnostics["providerContinuations"] = providerContinuations
	}
	combined := rankHTTPBackendHits(input.Query, interleaveHTTPBackendHits(collected))
	hits := deduplicateHits(combined, maxResults)
	diagnostics["candidateResults"] = len(combined)
	diagnostics["duplicateOrOverflowResults"] = max(0, len(combined)-len(hits))
	providerPageMayBeTruncated := false
	for _, result := range collected {
		if result.RequestedLimit > 0 && result.RawResults >= result.RequestedLimit {
			providerPageMayBeTruncated = true
			break
		}
	}
	diagnostics["providerPageMayBeTruncated"] = providerPageMayBeTruncated
	if len(hits) == 0 && completedBackends < len(backends) {
		return nil, diagnostics, fmt.Errorf("%d of %d HTTP search backends did not complete", len(backends)-completedBackends, len(backends))
	}
	return hits, diagnostics, nil
}

func webSearchCandidatePoolLimit(returnedLimit int) int {
	returnedLimit = normalizeMaxResults(returnedLimit)
	if returnedLimit < defaultSearchResultLimit {
		return defaultSearchResultLimit
	}
	return returnedLimit
}

func interleaveHTTPBackendHits(results []httpSearchBackendResult) []Hit {
	combined := make([]Hit, 0)
	for rank := 0; ; rank++ {
		added := false
		for _, result := range results {
			if result.Err != nil || rank >= len(result.Hits) {
				continue
			}
			combined = append(combined, result.Hits[rank])
			added = true
		}
		if !added {
			return combined
		}
	}
}

// rankHTTPBackendHits preserves provider diversity for equal-quality results
// while promoting hits that cover more of the user's discriminative query in
// the title and URL. This prevents a first result from every vertical backend
// from outranking a substantially better web result merely by provider order.
func rankHTTPBackendHits(query string, hits []Hit) []Hit {
	ranked := append([]Hit(nil), hits...)
	sort.SliceStable(ranked, func(i, j int) bool {
		leftRelevance := searchHitRelevanceScore(query, ranked[i])
		rightRelevance := searchHitRelevanceScore(query, ranked[j])
		if leftRelevance != rightRelevance {
			return leftRelevance > rightRelevance
		}
		return searchHitSourceQualityScore(ranked[i]) > searchHitSourceQualityScore(ranked[j])
	})
	return ranked
}

func searchHitSourceQualityScore(hit Hit) int {
	score := 0
	host := strings.ToLower(hostOf(hit.URL))
	if strings.HasPrefix(strings.ToLower(hit.URL), "https://") {
		score += 5
	}
	if strings.TrimSpace(hit.Snippet) != "" {
		score += 5
	}
	switch {
	case host == "doi.org" || strings.HasSuffix(host, ".gov") || strings.Contains(host, ".gov."):
		score += 30
	case strings.HasSuffix(host, ".edu") || strings.Contains(host, ".edu.") || strings.Contains(host, ".ac."):
		score += 25
	case strings.HasSuffix(host, ".int"):
		score += 20
	}
	return score
}

func searchHitRelevanceScore(query string, hit Hit) int {
	terms := searchQueryTerms(query)
	if len(terms) == 0 {
		terms = append(terms, searchQueryExactIdentifiers(query)...)
		terms = append(terms, searchQueryBiomedicalIdentifiers(query)...)
	}
	titleTokens := searchTokenSet(hit.Title)
	urlTokens := searchTokenSet(hit.URL)
	snippetTokens := searchTokenSet(hit.Snippet)
	matched := 0
	score := 0
	for _, term := range terms {
		weight := 0
		if titleTokens[term] {
			weight += 8
		}
		if urlTokens[term] {
			weight += 6
		}
		if snippetTokens[term] {
			weight += 3
		}
		if weight == 0 {
			continue
		}
		matched++
		score += weight
	}
	if len(terms) > 0 {
		score += matched * 40 / len(terms)
	}
	normalizedQuery := normalizeSearchText(query)
	if normalizedQuery != "" {
		if strings.Contains(normalizeSearchText(hit.Title), normalizedQuery) {
			score += 30
		}
		if strings.Contains(normalizeSearchText(hit.URL), normalizedQuery) {
			score += 24
		}
	}
	return score
}

// newWebSearchHTTPClient returns the outbound client used for the built-in
// search backends. The search endpoints are fixed public providers (Bing,
// Mojeek, Wikipedia, Crossref, DuckDuckGo Lite) rather than user-supplied URLs, so the
// client mirrors the v1.1 alignment contract: IPv4-only dialing, no address
// pinning, and a bounded per-request timeout. Pinning is deliberately not used
// here because search engines serve from anycast/CDN pools whose addresses
// change between lookups.
func newWebSearchHTTPClient(timeout time.Duration, optionalBase ...*http.Client) func(context.Context, string) (*http.Client, error) {
	return func(context.Context, string) (*http.Client, error) {
		base := http.DefaultClient
		if len(optionalBase) > 0 && optionalBase[0] != nil {
			base = optionalBase[0]
		}
		var transport *http.Transport
		switch value := base.Transport.(type) {
		case nil:
			transport = http.DefaultTransport.(*http.Transport).Clone()
		case *http.Transport:
			transport = value.Clone()
		default:
			return nil, fmt.Errorf("web search HTTP transport %T cannot enforce IPv4 dialing", base.Transport)
		}
		dialer := net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
		transport.DialContext = func(dialCtx context.Context, _ string, address string) (net.Conn, error) {
			return dialer.DialContext(dialCtx, "tcp4", address)
		}
		return &http.Client{Transport: transport}, nil
	}
}

func resolveHTTPBackends(options Options) []httpSearchBackend {
	if len(options.HTTPBackends) > 0 {
		return append([]httpSearchBackend(nil), options.HTTPBackends...)
	}
	if endpoint := strings.TrimSpace(os.Getenv("SYNON_WEBSEARCH_ENDPOINT")); endpoint != "" {
		return []httpSearchBackend{{Name: "configured", Endpoint: endpoint}}
	}
	return append([]httpSearchBackend(nil), defaultHTTPBackends...)
}

func parseHTTPBackendResults(backend httpSearchBackend, body []byte) ([]scriptResult, error) {
	parsed, err := parseHTTPBackendResponse(backend, body)
	return parsed.Results, err
}

type parsedHTTPBackendResponse struct {
	Results      []scriptResult
	TotalResults int
	TotalKnown   bool
	NextCursor   string
}

func parseHTTPBackendResponse(backend httpSearchBackend, body []byte) (parsedHTTPBackendResponse, error) {
	var result parsedHTTPBackendResponse
	var err error
	switch strings.TrimSpace(backend.Format) {
	case "", searchFormatHTML:
		result.Results = parseHTTPHTMLResults(string(body))
	case searchFormatMediaWikiOpenSearch:
		result.Results, err = parseMediaWikiOpenSearchResults(body)
	case searchFormatCrossref:
		result.Results, err = parseCrossrefResults(body)
		var metadata struct {
			Message struct {
				TotalResults *int   `json:"total-results"`
				NextCursor   string `json:"next-cursor"`
			} `json:"message"`
		}
		if err == nil && json.Unmarshal(body, &metadata) == nil {
			if metadata.Message.TotalResults != nil {
				result.TotalResults, result.TotalKnown = *metadata.Message.TotalResults, true
			}
			result.NextCursor = strings.TrimSpace(metadata.Message.NextCursor)
		}
	case searchFormatEuropePMC:
		result.Results, err = parseEuropePMCResults(body)
		var metadata struct {
			HitCount       *int   `json:"hitCount"`
			NextCursorMark string `json:"nextCursorMark"`
		}
		if err == nil && json.Unmarshal(body, &metadata) == nil {
			if metadata.HitCount != nil {
				result.TotalResults, result.TotalKnown = *metadata.HitCount, true
			}
			result.NextCursor = strings.TrimSpace(metadata.NextCursorMark)
		}
	default:
		err = fmt.Errorf("unsupported search response format %q", backend.Format)
	}
	return result, err
}

func parseMediaWikiOpenSearchResults(body []byte) ([]scriptResult, error) {
	var payload []json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode MediaWiki OpenSearch response: %w", err)
	}
	if len(payload) < 4 {
		return nil, errors.New("MediaWiki OpenSearch response must contain titles, descriptions, and URLs")
	}
	var titles, descriptions, urls []string
	if err := json.Unmarshal(payload[1], &titles); err != nil {
		return nil, fmt.Errorf("decode MediaWiki titles: %w", err)
	}
	if err := json.Unmarshal(payload[2], &descriptions); err != nil {
		return nil, fmt.Errorf("decode MediaWiki descriptions: %w", err)
	}
	if err := json.Unmarshal(payload[3], &urls); err != nil {
		return nil, fmt.Errorf("decode MediaWiki URLs: %w", err)
	}
	results := make([]scriptResult, 0, min(len(titles), len(urls)))
	for index := 0; index < len(titles) && index < len(urls); index++ {
		description := ""
		if index < len(descriptions) {
			description = descriptions[index]
		}
		results = append(results, scriptResult{Title: titles[index], URL: urls[index], Snippet: description})
	}
	return results, nil
}

func parseCrossrefResults(body []byte) ([]scriptResult, error) {
	var payload struct {
		Message struct {
			Items []struct {
				DOI            string   `json:"DOI"`
				Title          []string `json:"title"`
				URL            string   `json:"URL"`
				Abstract       string   `json:"abstract"`
				Publisher      string   `json:"publisher"`
				ContainerTitle []string `json:"container-title"`
				Author         []struct {
					Given  string `json:"given"`
					Family string `json:"family"`
					Name   string `json:"name"`
				} `json:"author"`
				Published struct {
					DateParts [][]int `json:"date-parts"`
				} `json:"published"`
			} `json:"items"`
		} `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode Crossref response: %w", err)
	}
	results := make([]scriptResult, 0, len(payload.Message.Items))
	for _, item := range payload.Message.Items {
		doi := strings.TrimSpace(item.DOI)
		if len(item.Title) == 0 || crossrefSupplementaryDOIPattern.MatchString(doi) {
			continue
		}
		link := strings.TrimSpace(item.URL)
		if link == "" && doi != "" {
			link = "https://doi.org/" + url.PathEscape(doi)
		}
		journal := ""
		if len(item.ContainerTitle) > 0 {
			journal = item.ContainerTitle[0]
		}
		authors := make([]string, 0, len(item.Author))
		for _, author := range item.Author {
			name := normalizeSpace(strings.TrimSpace(author.Given + " " + author.Family))
			if name == "" {
				name = normalizeSpace(author.Name)
			}
			if name != "" {
				authors = append(authors, name)
			}
		}
		results = append(results, scriptResult{
			Title:   item.Title[0],
			URL:     link,
			Snippet: compactString(normalizeSpace(stripHTML(item.Abstract)), maxSearchSnippetCharacters),
			Record: sourceRecord("crossref", item.Title[0], []SourceIdentifier{
				sourceIdentifier("doi", doi),
			}, item.Publisher, journal, strings.Join(authors, ", "), item.Abstract,
				sourceDateFromParts(item.Published.DateParts, "published.date-parts")),
		})
	}
	return results, nil
}

func parseEuropePMCResults(body []byte) ([]scriptResult, error) {
	var payload struct {
		ResultList struct {
			Results []struct {
				Title                string `json:"title"`
				AuthorString         string `json:"authorString"`
				JournalTitle         string `json:"journalTitle"`
				PubYear              string `json:"pubYear"`
				DOI                  string `json:"doi"`
				PMID                 string `json:"pmid"`
				PMCID                string `json:"pmcid"`
				Source               string `json:"source"`
				ID                   string `json:"id"`
				AbstractText         string `json:"abstractText"`
				FirstPublicationDate string `json:"firstPublicationDate"`
			} `json:"result"`
		} `json:"resultList"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode Europe PMC response: %w", err)
	}
	results := make([]scriptResult, 0, len(payload.ResultList.Results))
	for _, item := range payload.ResultList.Results {
		title := normalizeSpace(stripHTML(item.Title))
		if title == "" {
			continue
		}
		doi := strings.TrimSpace(item.DOI)
		pmid := strings.TrimSpace(item.PMID)
		link := ""
		switch {
		case doi != "":
			link = "https://doi.org/" + url.PathEscape(doi)
		case pmid != "":
			link = "https://europepmc.org/article/MED/" + url.PathEscape(pmid)
		case strings.TrimSpace(item.Source) != "" && strings.TrimSpace(item.ID) != "":
			link = "https://europepmc.org/article/" + url.PathEscape(strings.TrimSpace(item.Source)) + "/" + url.PathEscape(strings.TrimSpace(item.ID))
		default:
			continue
		}
		bibliographic := strings.TrimSpace(strings.Join([]string{
			strings.TrimSpace(item.AuthorString), strings.TrimSpace(item.JournalTitle), strings.TrimSpace(item.PubYear),
		}, " "))
		snippet := normalizeSpace(stripHTML(strings.TrimSpace(bibliographic + " " + item.AbstractText)))
		published := sourceDateFromText(item.FirstPublicationDate, "firstPublicationDate")
		if published == nil {
			published = sourceDateFromText(item.PubYear, "pubYear")
		}
		results = append(results, scriptResult{
			Title: title, URL: link, Snippet: compactString(snippet, maxSearchSnippetCharacters),
			Record: sourceRecord("europe-pmc", title, []SourceIdentifier{
				sourceIdentifier("doi", doi), sourceIdentifier("pmid", pmid),
				sourceIdentifier("pmcid", item.PMCID),
			}, "", item.JournalTitle, item.AuthorString, item.AbstractText, published),
		})
	}
	return results, nil
}

func searchHTTPEndpoint(query string) (string, string) {
	endpoint := strings.TrimSpace(os.Getenv("SYNON_WEBSEARCH_ENDPOINT"))
	if endpoint == "" {
		endpoint = defaultHTTPEndpoint
	}
	return searchHTTPEndpointFor(query, endpoint), endpoint
}

func searchHTTPEndpointFor(query string, endpoint string) string {
	return searchHTTPEndpointForLimit(query, endpoint, defaultSearchResultLimit)
}

func searchHTTPEndpointForLimit(query string, endpoint string, limit int) string {
	encoded := url.QueryEscape(query)
	endpoint = strings.ReplaceAll(endpoint, "{limit}", fmt.Sprintf("%d", normalizeMaxResults(limit)))
	if strings.Contains(endpoint, "{query}") {
		return strings.ReplaceAll(endpoint, "{query}", encoded)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	values := parsed.Query()
	if values.Get("q") == "" {
		values.Set("q", query)
	}
	parsed.RawQuery = values.Encode()
	return parsed.String()
}

func httpBackendRequestLimit(backend httpSearchBackend, requested int) int {
	limit := normalizeMaxResults(requested)
	switch strings.TrimSpace(backend.Name) {
	case "bing-html":
		return min(limit, maxBingSearchResultLimit)
	case "wikipedia":
		return min(limit, maxWikipediaSearchResultLimit)
	default:
		return limit
	}
}
