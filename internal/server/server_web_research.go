package server

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"synon-go/internal/tools/webfetch"
)

type webResearchQualityTarget struct {
	MinFetchedSources     int
	MinIndependentDomains int
	MinDeepReadSources    int
	MaxSearchRounds       int
}

// A discovery wave that produces no new canonical source is not evidence.
// Allow one transient empty response, then stop instead of spending the remaining
// rounds replaying an exhausted search surface. This is a progress guard, not
// a wall-clock limit: a productive research run can continue for as long as
// it keeps adding sources or meeting its evidence target.
const maxWebResearchStagnantRounds = 2

// Fetching a shortlist concurrently keeps a broad evidence sweep responsive
// without turning one compound research call into an unbounded fan-out. The
// shortlist itself is still selected by quality and domain coverage below.
const webResearchFetchConcurrency = 4

func webResearchAdvanceStagnantRounds(previous, newCandidates int) (int, bool) {
	if newCandidates > 0 {
		return 0, false
	}
	current := previous + 1
	return current, current >= maxWebResearchStagnantRounds
}

func (s *Server) executeWebResearchTool(ctx context.Context, input map[string]any) (any, error) {
	normalizedInput, err := normalizeWebResearchInput(input)
	if err != nil {
		return nil, err
	}
	if err := s.validateRegisteredTool("web_research", normalizedInput); err != nil {
		return nil, err
	}
	input = normalizedInput
	operation := strings.TrimSpace(stringValue(input["operation"]))
	query := strings.TrimSpace(stringValue(input["query"]))
	targetURL := strings.TrimSpace(stringValue(input["url"]))
	fact := strings.TrimSpace(stringValue(input["fact"]))
	researchDepth := strings.ToLower(strings.TrimSpace(stringValue(input["research_depth"])))
	maxSources := int(numberValue(input["max_sources"]))
	if maxSources <= 0 {
		switch researchDepth {
		case "deep":
			maxSources = 12
		case "systematic":
			maxSources = 20
		default:
			maxSources = 6
		}
	}
	if maxSources > 20 {
		maxSources = 20
	}
	if len(stringArrayValue(input["allowed_domains"])) > 0 && len(stringArrayValue(input["blocked_domains"])) > 0 {
		return nil, errors.New("Cannot specify both allowed_domains and blocked_domains.")
	}
	switch operation {
	case "search", "search_and_fetch", "verify_fact":
		if query == "" {
			return nil, fmt.Errorf("%s requires query", operation)
		}
	case "fetch", "extract_links", "extract_tables", "fetch_pdf_text":
		if targetURL == "" {
			return nil, fmt.Errorf("%s requires url", operation)
		}
	default:
		return nil, fmt.Errorf("unsupported WebResearch operation: %s", operation)
	}
	if operation == "verify_fact" && fact == "" {
		return nil, errors.New("verify_fact requires fact")
	}

	target := webResearchResolveQualityTarget(input, operation, maxSources)
	session, err := s.resolveWebResearchSession(ctx, input)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"operation": operation, "sources": []any{}}
	if query != "" {
		result["query"] = query
	}
	if targetURL != "" {
		result["url"] = targetURL
	}
	if fact != "" {
		result["fact"] = fact
	}
	if researchDepth != "" {
		result["researchDepth"] = researchDepth
	}

	switch operation {
	case "fetch", "extract_links", "extract_tables", "fetch_pdf_text":
		out, err := s.webResearchFetchOperation(ctx, operation, targetURL, result)
		if err != nil {
			return nil, err
		}
		outMap := mapValue(out)
		s.recordWebResearchSessionSources(session, anySliceValue(outMap["sources"]))
		outMap["research_session"] = summarizeWebResearchSession(session)
		webResearchAttachMetadata(outMap)
		return outMap, nil
	case "search", "search_and_fetch", "verify_fact":
		if operation == "search" {
			search, err := s.executeWebSearchTool(ctx, webResearchSearchInput(query, maxSources, input))
			if err != nil {
				return nil, err
			}
			discoveryBudget := webResearchDiscoveryBudget(maxSources)
			candidates := webResearchSourcesFromSearch(search, discoveryBudget)
			s.recordWebResearchSessionSources(session, candidates)
			result["candidateSources"] = candidates
			result["sources"] = candidates
			webResearchAttachDiscoverySummary(result, discoveryBudget, len(candidates))
			result["quality"] = webResearchQualityWithTarget(candidates, target, 1)
			result["research_session"] = summarizeWebResearchSession(session)
			if len(candidates) == 0 {
				result["failure"] = webResearchSearchFailure(search)
				result["stopReason"] = "search_unavailable"
			} else {
				result["stopReason"] = "search_completed"
			}
			webResearchAttachMetadata(result)
			return result, nil
		}
		fetchedResult, err := s.webResearchSearchAndFetch(ctx, input, query, operation, maxSources, target, session)
		if err != nil {
			return nil, err
		}
		if operation == "verify_fact" {
			return webResearchVerifyFactResult(fetchedResult, fact), nil
		}
		return fetchedResult, nil
	}
	return nil, fmt.Errorf("unsupported WebResearch operation: %s", operation)
}

func normalizeWebResearchInput(input map[string]any) (map[string]any, error) {
	normalized := make(map[string]any, len(input))
	for key, value := range input {
		normalized[key] = value
	}
	operation, exists := normalized["operation"]
	if !exists {
		return normalized, nil
	}
	value, ok := operation.(string)
	if !ok {
		return nil, errors.New("WebResearch.operation must be a string")
	}
	switch strings.TrimSpace(value) {
	case "research":
		normalized["operation"] = "search_and_fetch"
	case "search", "search_and_fetch", "verify_fact", "fetch", "extract_links", "extract_tables", "fetch_pdf_text":
		normalized["operation"] = strings.TrimSpace(value)
	default:
		return nil, fmt.Errorf("unsupported WebResearch operation %q; use search_and_fetch for normal research", value)
	}
	return normalized, nil
}

func (s *Server) webResearchFetchOperation(ctx context.Context, operation string, targetURL string, result map[string]any) (any, error) {
	fetched, err := s.executeRegisteredTool(ctx, "web_fetch", map[string]any{"url": targetURL, "prompt": webResearchFetchPrompt(operation)})
	if err != nil {
		return nil, err
	}
	fetchedResult, ok := fetched.(webfetch.Result)
	if !ok {
		return nil, errors.New("web_fetch returned an invalid result contract")
	}
	content := fetchedResult.Body
	statusCode := fetchedResult.StatusCode
	if fetchedResult.SourceUnavailable || statusCode == 0 {
		message := firstNonEmpty(fetchedResult.Error, "Source unavailable.")
		source := webResearchUnavailableSource(targetURL, targetURL, message)
		result["sources"] = []any{source}
		result["documents"] = []any{}
		result["quality"] = webResearchQuality([]any{source}, 1)
		result["stopReason"] = "source_unavailable"
		return result, nil
	}
	readableContent := webResearchReadableDocument(content, fetchedResult.ContentType)
	if strings.TrimSpace(readableContent) == "" {
		message := "Fetched page contains no extractable source content; it appears to be an application shell rather than substantive evidence."
		source := webResearchUnavailableSource(targetURL, targetURL, message)
		result["sources"] = []any{source}
		result["documents"] = []any{}
		result["quality"] = webResearchQuality([]any{source}, 1)
		result["stopReason"] = "source_unavailable"
		return result, nil
	}
	readReceipt := webResearchReadReceipt(fetchedResult, readableContent, stringValue(result["query"]))
	evidence := webResearchEvidence("primary_evidence", "unknown", statusCode)
	evidence["evidenceState"] = "document_read"
	evidence["readDepth"] = stringValue(readReceipt["depth"])
	sourceStatus := "fetched"
	if !webResearchEvidenceDocumentEligible(readReceipt) {
		sourceStatus = "offTopic"
		evidence["evidenceState"] = "off_topic"
		evidence["role"] = "excluded_context"
		evidence["sourceEvidence"] = false
	}
	resolvedURL := firstNonEmpty(strings.TrimSpace(fetchedResult.URL), targetURL)
	source := map[string]any{
		"title": webResearchDocumentTitle(content, targetURL), "url": resolvedURL,
		"sourceLocator": targetURL,
		"status":        sourceStatus, "readReceipt": readReceipt, "evidence": evidence,
	}
	result["sources"] = []any{source}
	result["documents"] = []any{}
	if webResearchEvidenceDocumentEligible(readReceipt) {
		result["documents"] = []any{map[string]any{
			"url": resolvedURL, "sourceLocator": targetURL, "title": source["title"], "content": readableContent,
			"readReceipt": readReceipt,
		}}
	}
	result["quality"] = webResearchQuality([]any{source}, 1)
	result["stopReason"] = "single_fetch_completed"
	if sourceStatus == "offTopic" {
		result["stopReason"] = "source_irrelevant"
	}
	if operation == "extract_links" {
		result["links"] = extractWebResearchLinks(targetURL, content)
	}
	if operation == "extract_tables" {
		result["tables"] = extractWebResearchTables(content)
	}
	return result, nil
}

var (
	webResearchHTMLBodyPattern       = regexp.MustCompile(`(?is)<body\b[^>]*>(.*?)</body>`)
	webResearchNonContentHTMLPattern = regexp.MustCompile(`(?is)<(?:head|script|style|noscript|template|svg|nav|footer|aside|form)\b[^>]*>.*?</(?:head|script|style|noscript|template|svg|nav|footer|aside|form)>`)
	webResearchHTMLTagPattern        = regexp.MustCompile(`(?is)<[^>]+>`)
)

func (s *Server) webResearchSearchAndFetch(ctx context.Context, input map[string]any, query string, operation string, maxSources int, target webResearchQualityTarget, session *webResearchSessionRef) (map[string]any, error) {
	result := map[string]any{"operation": operation, "query": query, "sources": []any{}, "documents": []any{}}
	if fact := strings.TrimSpace(stringValue(input["fact"])); fact != "" {
		result["fact"] = fact
	}
	allSources := []any{}
	documents := []any{}
	selected := map[string]bool{}
	discoveryBudget := webResearchDiscoveryBudget(maxSources)
	allCandidates := []any{}
	searchRounds := 0
	stagnantRounds := 0
	stopReason := "max_sources_reached"
	for round := 0; round < target.MaxSearchRounds; round++ {
		searchRounds++
		// Query semantics belong to the caller and its explicit variants. Each
		// wave advances through canonical candidates not selected earlier in
		// this call/session; the Harness must not invent fixed English suffixes
		// that alter a Chinese or domain-specific research question.
		search, err := s.executeWebSearchTool(ctx, webResearchSearchInput(query, maxSources, input))
		if err != nil {
			return nil, err
		}
		candidates := webResearchFilterCandidates(webResearchSourcesFromSearch(search, discoveryBudget), session, selected)
		allCandidates = append(allCandidates, candidates...)
		var stopForStagnation bool
		stagnantRounds, stopForStagnation = webResearchAdvanceStagnantRounds(stagnantRounds, len(candidates))
		result["candidateSources"] = allCandidates
		webResearchAttachDiscoverySummary(result, discoveryBudget, len(allCandidates))
		if stopForStagnation {
			stopReason = "no_new_candidates"
			break
		}
		fetchCandidates := webResearchSelectFetchCandidates(candidates, allSources, target, maxSources)
		for _, raw := range fetchCandidates {
			item := mapValue(raw)
			if canonical := canonicalWebResearchURL(stringValue(item["url"])); canonical != "" {
				selected[canonical] = true
			}
		}
		fetchedBatch := s.webResearchFetchCandidates(ctx, query, fetchCandidates)
		for _, fetched := range fetchedBatch {
			allSources = append(allSources, fetched.sources...)
			documents = append(documents, fetched.documents...)
			s.recordWebResearchSessionSources(session, fetched.sources)
			quality := webResearchQualityWithTarget(allSources, target, searchRounds)
			if candidateStopReason := webResearchQualityStopReason(allSources, target); candidateStopReason != "" {
				stopReason = candidateStopReason
				result["sources"] = allSources
				result["documents"] = documents
				result["quality"] = quality
				result["research_session"] = summarizeWebResearchSession(session)
				result["stopReason"] = stopReason
				result["sourceFrontier"] = webResearchSourceFrontier(allCandidates, selected, allSources)
				webResearchAttachMetadata(result)
				return result, nil
			}
			if webResearchFetchedCount(allSources) >= maxSources {
				result["sources"] = allSources
				result["documents"] = documents
				result["quality"] = quality
				result["research_session"] = summarizeWebResearchSession(session)
				result["stopReason"] = stopReason
				result["sourceFrontier"] = webResearchSourceFrontier(allCandidates, selected, allSources)
				webResearchAttachMetadata(result)
				return result, nil
			}
		}
	}
	quality := webResearchQualityWithTarget(allSources, target, searchRounds)
	if candidateStopReason := webResearchQualityStopReason(allSources, target); candidateStopReason != "" {
		stopReason = candidateStopReason
	} else if len(allSources) == 0 {
		stopReason = "no_sources"
	} else if webResearchFetchedCount(allSources) == 0 && len(allSources) > 0 {
		stopReason = webResearchNoFetchedSourceStopReason(allSources)
	} else if searchRounds >= target.MaxSearchRounds {
		stopReason = "max_rounds_reached"
	}
	result["sources"] = allSources
	result["documents"] = documents
	result["quality"] = quality
	result["research_session"] = summarizeWebResearchSession(session)
	result["stopReason"] = stopReason
	result["sourceFrontier"] = webResearchSourceFrontier(allCandidates, selected, allSources)
	if webResearchFetchedCount(allSources) == 0 {
		result["failure"] = webResearchNoEvidenceFailure(stopReason)
	}
	webResearchAttachMetadata(result)
	return result, nil
}
