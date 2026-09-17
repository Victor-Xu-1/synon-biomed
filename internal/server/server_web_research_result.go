package server

import (
	"fmt"
	"strings"

	"synon-go/internal/tools/websearch"
)

func webResearchSourcesFromSearch(search any, limit int) []any {
	if limit <= 0 {
		limit = 3
	}
	out := []any{}
	if typed, ok := search.(websearch.Output); ok {
		for _, item := range typed.Sources {
			urlValue := strings.TrimSpace(item.URL)
			if urlValue == "" {
				continue
			}
			candidate := map[string]any{
				"title":          firstNonEmpty(item.Title, urlValue),
				"url":            urlValue,
				"status":         "search_result",
				"snippet":        item.Snippet,
				"rank":           float64(item.Rank),
				"host":           item.Host,
				"qualityScore":   float64(item.QualityScore),
				"qualitySignals": item.QualitySignals,
				"evidence":       webResearchEvidence("supporting_context", "unknown", 0),
			}
			// Preserve the discovery lane that produced the candidate. The
			// language track is not a translation or a task-specific rule; it is
			// provenance used only to avoid selecting every source from one
			// language when both tracks returned usable evidence.
			if item.Metadata != nil {
				if variant := strings.TrimSpace(stringValue(item.Metadata["matchedQueryVariant"])); variant != "" {
					candidate["matchedQueryVariant"] = variant
					candidate["queryLanguage"] = webResearchQueryLanguage(variant)
				}
			}
			out = append(out, candidate)
			if len(out) >= limit {
				return out
			}
		}
		return out
	}
	root := mapValue(search)
	for _, raw := range anySliceValue(root["sources"]) {
		item := mapValue(raw)
		urlValue := strings.TrimSpace(stringValue(item["url"]))
		if urlValue == "" {
			continue
		}
		candidate := map[string]any{"title": firstNonEmpty(stringValue(item["title"]), urlValue), "url": urlValue, "status": "search_result", "snippet": stringValue(item["snippet"]), "evidence": webResearchEvidence("supporting_context", "unknown", 0)}
		if variant := strings.TrimSpace(stringValue(item["matchedQueryVariant"])); variant != "" {
			candidate["matchedQueryVariant"] = variant
			candidate["queryLanguage"] = webResearchQueryLanguage(variant)
		}
		out = append(out, candidate)
		if len(out) >= limit {
			return out
		}
	}
	return out
}

// webResearchSearchFailure keeps a failed search structured without exposing
// backend diagnostics (which can include transport-specific details). Callers
// can use the stable kind and recovery guidance to decide whether to retry.
func webResearchSearchFailure(search any) map[string]any {
	if typed, ok := search.(websearch.Output); ok && typed.Failure != nil {
		return map[string]any{
			"kind":        "research_unavailable",
			"message":     "Web research search returned no usable sources.",
			"recoverable": typed.Failure.Recoverable,
			"recovery":    "retry_later_or_refine_the_query",
		}
	}
	return webResearchNoEvidenceFailure("no_sources")
}

func webResearchNoEvidenceFailure(stopReason string) map[string]any {
	return map[string]any{
		"kind":        "research_unavailable",
		"message":     "Web research returned no fetched source evidence.",
		"recoverable": true,
		"recovery":    "retry_later_or_refine_the_query",
		"stopReason":  stopReason,
	}
}

func webResearchQuality(sources []any, target int) map[string]any {
	if target <= 0 {
		target = 1
	}
	qualityTarget := webResearchQualityTarget{
		MinFetchedSources: target, MinIndependentDomains: 1,
		MinDeepReadSources: target, MaxSearchRounds: 1,
	}
	return webResearchQualityWithTarget(sources, qualityTarget, 1)
}

func webResearchQualityWithTarget(sources []any, target webResearchQualityTarget, searchRounds int) map[string]any {
	if target.MinFetchedSources <= 0 {
		target.MinFetchedSources = 1
	}
	if target.MinIndependentDomains <= 0 {
		target.MinIndependentDomains = 1
	}
	if target.MaxSearchRounds <= 0 {
		target.MaxSearchRounds = 1
	}
	if target.MinDeepReadSources < 0 {
		target.MinDeepReadSources = 0
	}
	if searchRounds <= 0 {
		searchRounds = 1
	}
	fetched := 0
	deepRead := 0
	extractableCharacters := 0
	unavailable := 0
	irrelevant := 0
	domains := map[string]bool{}
	for _, raw := range sources {
		item := mapValue(raw)
		switch stringValue(item["status"]) {
		case "fetched":
			fetched++
			if webResearchSourceDeepRead(item) {
				deepRead++
			}
			extractableCharacters += int(numberValue(mapValue(item["readReceipt"])["extractableCharacters"]))
		case "sourceUnavailable":
			unavailable++
		case "offTopic":
			irrelevant++
		}
		if stringValue(item["status"]) == "fetched" {
			if domain := webResearchIndependentDomain(stringValue(item["url"])); domain != "" {
				domains[domain] = true
			}
		}
	}
	return map[string]any{
		"fetchedSources":        float64(fetched),
		"deepReadSources":       float64(deepRead),
		"extractableCharacters": float64(extractableCharacters),
		"unavailableSources":    float64(unavailable),
		"irrelevantSources":     float64(irrelevant),
		"independentDomains":    float64(len(domains)),
		"searchRounds":          float64(searchRounds),
		"meetsTarget": fetched >= target.MinFetchedSources &&
			len(domains) >= target.MinIndependentDomains && deepRead >= target.MinDeepReadSources,
		"target": map[string]any{
			"min_fetched_sources":     float64(target.MinFetchedSources),
			"min_independent_domains": float64(target.MinIndependentDomains),
			"min_deep_read_sources":   float64(target.MinDeepReadSources),
			"max_search_rounds":       float64(target.MaxSearchRounds),
		},
	}
}

func webResearchResolveQualityTarget(input map[string]any, operation string,
	maxSources int) webResearchQualityTarget {
	targetInput := mapValue(input["quality_target"])
	researchDepth := strings.ToLower(strings.TrimSpace(stringValue(input["research_depth"])))
	minFetched := int(numberValue(targetInput["min_fetched_sources"]))
	minDomains := int(numberValue(targetInput["min_independent_domains"]))
	maxRounds := int(numberValue(targetInput["max_search_rounds"]))
	minDeepRead := int(numberValue(targetInput["min_deep_read_sources"]))
	if minFetched <= 0 {
		if operation == "search_and_fetch" && researchDepth == "systematic" {
			minFetched = min(max(1, maxSources), 12)
		} else if operation == "search_and_fetch" && researchDepth == "deep" {
			minFetched = min(max(1, maxSources), 8)
		} else if operation == "verify_fact" {
			minFetched = 2
		} else if operation == "search_and_fetch" {
			minFetched = min(max(1, maxSources), 4)
		} else {
			minFetched = 1
		}
	}
	if minDomains <= 0 {
		if operation == "search_and_fetch" && researchDepth == "systematic" {
			minDomains = min(minFetched, 5)
		} else if operation == "search_and_fetch" && researchDepth == "deep" {
			minDomains = min(minFetched, 4)
		} else if operation == "verify_fact" {
			minDomains = 2
		} else if operation == "search_and_fetch" {
			minDomains = min(minFetched, 3)
		} else {
			minDomains = 1
		}
	}
	if maxRounds <= 0 {
		if operation == "search_and_fetch" && researchDepth == "systematic" {
			// Multiple bounded waves let the selector advance past inaccessible
			// candidates while preserving the caller's exact query semantics.
			maxRounds = 4
		} else if operation == "search_and_fetch" && researchDepth == "deep" {
			maxRounds = 3
		} else if operation == "verify_fact" || operation == "search_and_fetch" {
			maxRounds = 2
		} else {
			maxRounds = 1
		}
	}
	if maxRounds > 5 {
		maxRounds = 5
	}
	if minDeepRead <= 0 {
		// Broad discovery and deep reading are separate jobs. workspace
		// reads the strongest two or three sources deeply after the sweep;
		// requiring every fetched result to be substantive turns source
		// breadth into a slow and brittle gate.
		if operation == "verify_fact" {
			minDeepRead = minFetched
		} else {
			minDeepRead = min(3, minFetched)
		}
	}
	return webResearchQualityTarget{
		MinFetchedSources: minFetched, MinIndependentDomains: minDomains,
		MinDeepReadSources: minDeepRead, MaxSearchRounds: maxRounds,
	}
}

func webResearchSearchInput(query string, maxSources int, input map[string]any) map[string]any {
	searchInput := map[string]any{"query": query, "max_results": float64(webResearchDiscoveryBudget(maxSources))}
	if variants := stringArrayValue(input["query_variants"]); len(variants) > 0 {
		searchInput["query_variants"] = variants
	}
	if allowed := stringArrayValue(input["allowed_domains"]); len(allowed) > 0 {
		searchInput["allowed_domains"] = allowed
	}
	if blocked := stringArrayValue(input["blocked_domains"]); len(blocked) > 0 {
		searchInput["blocked_domains"] = blocked
	}
	return searchInput
}

func webResearchDiscoveryBudget(maxSources int) int {
	if maxSources <= 0 {
		maxSources = 6
	}
	budget := maxSources * 10
	if budget < 100 {
		budget = 100
	}
	if budget > 200 {
		budget = 200
	}
	return budget
}

func webResearchAttachDiscoverySummary(result map[string]any, budget int, candidates int) {
	result["discovery"] = map[string]any{
		"candidateBudgetPerRound": float64(budget),
		"discoveredCandidates":    float64(candidates),
		"selectionMode":           "broad_discovery_then_rank_deduplicate_and_fetch",
	}
}

func webResearchAttachDiscoveryContext(records []any, candidate map[string]any) {
	if len(records) == 0 || len(candidate) == 0 {
		return
	}
	discovery := map[string]any{
		"rank":           candidate["rank"],
		"host":           candidate["host"],
		"qualityScore":   candidate["qualityScore"],
		"qualitySignals": candidate["qualitySignals"],
		"snippet":        candidate["snippet"],
	}
	if variant := strings.TrimSpace(stringValue(candidate["matchedQueryVariant"])); variant != "" {
		discovery["matchedQueryVariant"] = variant
		discovery["queryLanguage"] = webResearchQueryLanguage(variant)
	}
	for _, raw := range records {
		record := mapValue(raw)
		if len(record) == 0 {
			continue
		}
		record["discovery"] = discovery
		if evidence := mapValue(record["evidence"]); len(evidence) > 0 {
			if score := numberValue(candidate["qualityScore"]); score > 0 {
				evidence["qualityScore"] = score
			}
		}
	}
}

func webResearchFilterCandidates(candidates []any, session *webResearchSessionRef, selected map[string]bool) []any {
	out := []any{}
	seenThisRound := map[string]bool{}
	for _, raw := range candidates {
		item := mapValue(raw)
		canonical := canonicalWebResearchURL(stringValue(item["url"]))
		if canonical == "" || seenThisRound[canonical] || selected[canonical] {
			continue
		}
		if session != nil && session.State != nil && session.State.SeenCanonicalURLs[canonical] {
			continue
		}
		seenThisRound[canonical] = true
		out = append(out, raw)
	}
	return out
}

func webResearchFetchedCount(sources []any) int {
	count := 0
	for _, raw := range sources {
		if stringValue(mapValue(raw)["status"]) == "fetched" {
			count++
		}
	}
	return count
}

func webResearchNoFetchedSourceStopReason(sources []any) string {
	for _, raw := range sources {
		if stringValue(mapValue(raw)["status"]) == "offTopic" {
			return "no_relevant_sources"
		}
	}
	return "source_unavailable"
}

func webResearchVerifyFactResult(result map[string]any, fact string) map[string]any {
	documents := anySliceValue(result["documents"])
	supportingURLs := []any{}
	independent := map[string]bool{}
	for _, raw := range documents {
		item := mapValue(raw)
		if !webResearchSourceDeepRead(item) {
			continue
		}
		content := strings.ToLower(stringValue(item["content"]))
		if strings.Contains(content, strings.ToLower(fact)) {
			sourceURL := stringValue(item["url"])
			supportingURLs = append(supportingURLs, sourceURL)
			if domain := webResearchIndependentDomain(sourceURL); domain != "" {
				independent[domain] = true
			}
		}
	}
	verdict := "supported"
	reason := "At least two independent, completely read substantive sources contain the fact text."
	if len(independent) < 2 {
		verdict = "insufficient_evidence"
		reason = "verify_fact requires at least two independent, completely read substantive sources before making a supported/not-supported claim."
	}
	result["verdict"] = verdict
	result["reason"] = reason
	claimCheck := map[string]any{
		"method":                       "deep_read_source_fact_check",
		"claim":                        fact,
		"verdict":                      verdict,
		"supportingFetchedSources":     float64(len(supportingURLs)),
		"supportingDeepReadSources":    float64(len(supportingURLs)),
		"supportingIndependentDomains": float64(len(independent)),
		"matchedSignals":               supportingURLs,
		"semanticReviewRequired":       true,
	}
	result["claimCheck"] = claimCheck
	if pack := mapValue(result["synthesisPack"]); len(pack) > 0 {
		pack["claimCheck"] = claimCheck
		result["synthesisPack"] = pack
	}
	return result
}

func webResearchAttachMetadata(result map[string]any) {
	quality := mapValue(result["quality"])
	gaps := webResearchEvidenceGaps(quality)
	nextActions := []any{}
	frontier := mapValue(result["sourceFrontier"])
	continueRecommended := boolValue(frontier["continue_recommended"], false)
	if continueRecommended {
		for _, raw := range anySliceValue(frontier["next_routes"]) {
			route := mapValue(raw)
			if targetURL := strings.TrimSpace(stringValue(route["url"])); targetURL != "" {
				nextActions = append(nextActions, map[string]any{
					"action": "fetch",
					"reason": "an untried ranked source remains in an unresolved discovery lane",
					"url":    targetURL,
				})
			}
		}
	}
	stopReason := strings.TrimSpace(stringValue(result["stopReason"]))
	result["evidenceGaps"] = gaps
	result["nextActions"] = nextActions
	result["retrievalDecision"] = map[string]any{
		"mode":                map[bool]string{true: "source_frontier_open", false: "source_frontier_resolved"}[continueRecommended],
		"continueRecommended": continueRecommended,
		"stopReason":          stopReason,
		"decisionBasis":       frontier["decision_basis"],
	}
	result["synthesisPack"] = map[string]any{
		"sources":           anySliceValue(result["sources"]),
		"documents":         anySliceValue(result["documents"]),
		"quality":           quality,
		"evidenceGaps":      gaps,
		"nextActions":       nextActions,
		"sourceFrontier":    frontier,
		"stopReason":        stringValue(result["stopReason"]),
		"retrievalDecision": result["retrievalDecision"],
	}
}

func webResearchEvidenceGaps(quality map[string]any) []any {
	gaps := []any{}
	target := mapValue(quality["target"])
	fetched := int(numberValue(quality["fetchedSources"]))
	minFetched := int(numberValue(target["min_fetched_sources"]))
	if minFetched > 0 && fetched < minFetched {
		gaps = append(gaps, map[string]any{
			"kind":    "insufficient_fetched_sources",
			"message": fmt.Sprintf("Need %d fetched sources; found %d.", minFetched, fetched),
		})
	}
	domains := int(numberValue(quality["independentDomains"]))
	minDomains := int(numberValue(target["min_independent_domains"]))
	if minDomains > 0 && domains < minDomains {
		gaps = append(gaps, map[string]any{
			"kind":    "insufficient_independent_domains",
			"message": fmt.Sprintf("Need %d independent fetched domains; found %d.", minDomains, domains),
		})
	}
	deepRead := int(numberValue(quality["deepReadSources"]))
	minDeepRead := int(numberValue(target["min_deep_read_sources"]))
	if minDeepRead > 0 && deepRead < minDeepRead {
		gaps = append(gaps, map[string]any{
			"kind":    "insufficient_deep_reads",
			"message": fmt.Sprintf("Need %d completely read substantive sources; found %d.", minDeepRead, deepRead),
		})
	}
	return gaps
}
