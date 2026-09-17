package server

import (
	"context"
	"strings"
	"sync"
)

type webResearchFetchCandidateResult struct {
	sources   []any
	documents []any
}

// webResearchFetchCandidates performs a bounded, deterministic fan-out. The
// result order follows the quality-ranked shortlist rather than completion
// timing, so replayed transcripts and quality calculations remain stable.
func (s *Server) webResearchFetchCandidates(ctx context.Context, query string, candidates []any) []webResearchFetchCandidateResult {
	if len(candidates) == 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	results := make([]webResearchFetchCandidateResult, len(candidates))
	semaphore := make(chan struct{}, webResearchFetchConcurrency)
	var wait sync.WaitGroup
	for index, raw := range candidates {
		index, raw := index, raw
		wait.Add(1)
		go func() {
			defer wait.Done()
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-semaphore }()
			item := mapValue(raw)
			urlValue := strings.TrimSpace(stringValue(item["url"]))
			if urlValue == "" {
				return
			}
			fetchQuery := webResearchCandidateQueryVariant(item)
			if fetchQuery == "" {
				fetchQuery = query
			}
			out, err := s.webResearchFetchOperation(ctx, "fetch", urlValue, map[string]any{"query": fetchQuery})
			if err != nil {
				results[index].sources = []any{webResearchUnavailableSource(
					firstNonEmpty(stringValue(item["title"]), urlValue), urlValue, err.Error(),
				)}
				return
			}
			outMap := mapValue(out)
			fetchedSources := anySliceValue(outMap["sources"])
			webResearchAttachDiscoveryContext(fetchedSources, item)
			fetchedDocuments := anySliceValue(outMap["documents"])
			webResearchAttachDiscoveryContext(fetchedDocuments, item)
			results[index] = webResearchFetchCandidateResult{sources: fetchedSources, documents: fetchedDocuments}
		}()
	}
	wait.Wait()
	return results
}

// webResearchSelectFetchCandidates keeps retrieval balanced: high-quality
// candidates are preferred, but independent domains receive a diversity bonus
// until the target's domain coverage is met. Only the remaining evidence quota
// is fetched in each round; unavailable candidates are left for the next
// productive discovery round instead of creating an oversized speculative
// fan-out. The discovery window remains broad and independently auditable.
func webResearchSelectFetchCandidates(candidates, fetched []any, target webResearchQualityTarget, maxSources int) []any {
	if len(candidates) == 0 || maxSources <= 0 {
		return nil
	}
	fetchedCount := webResearchFetchedCount(fetched)
	remainingCapacity := maxSources - fetchedCount
	if remainingCapacity <= 0 {
		return nil
	}
	domains := map[string]bool{}
	languages := map[string]bool{}
	queryTracks := map[string]bool{}
	deepRead := 0
	for _, raw := range fetched {
		if stringValue(mapValue(raw)["status"]) == "fetched" {
			if domain := webResearchIndependentDomain(stringValue(mapValue(raw)["url"])); domain != "" {
				domains[domain] = true
			}
			if webResearchSourceDeepRead(mapValue(raw)) {
				deepRead++
			}
			if language := webResearchCandidateLanguage(raw); language != "" {
				languages[language] = true
			}
			if track := webResearchCandidateQueryTrack(raw); track != "" {
				queryTracks[track] = true
			}
		}
	}
	needed := max(
		target.MinFetchedSources-fetchedCount,
		target.MinIndependentDomains-len(domains),
		target.MinDeepReadSources-deepRead,
	)
	if needed <= 0 {
		return nil
	}
	ordered := make([]any, 0, len(candidates))
	for _, candidate := range candidates {
		if webResearchCandidateEligibleForFetch(mapValue(candidate)) {
			ordered = append(ordered, candidate)
		}
	}
	shortlistSize := min(len(ordered), min(remainingCapacity, needed))
	selected := make([]any, 0, shortlistSize)
	for len(selected) < shortlistSize && len(ordered) > 0 {
		preferNewDomain := len(domains) < target.MinIndependentDomains
		availableLanguages := webResearchCandidateLanguages(ordered)
		preferNewLanguage := len(availableLanguages) > 1 && len(languages) < len(availableLanguages)
		preferNewQueryTrack := webResearchCandidatesHaveNewQueryTrack(ordered, queryTracks)
		bestBaseScore := 0
		if preferNewDomain || preferNewLanguage || preferNewQueryTrack {
			for _, candidate := range ordered {
				if score := webResearchCandidateScore(candidate, domains); score > bestBaseScore {
					bestBaseScore = score
				}
			}
		}
		bestIndex := 0
		bestScore := webResearchCandidateScore(ordered[0], domains)
		if preferNewDomain && webResearchCandidateHasNewDomain(ordered[0], domains) {
			bestScore += bestBaseScore + 1
		}
		if preferNewLanguage && webResearchCandidateHasNewLanguage(ordered[0], languages) {
			bestScore += bestBaseScore + 1
		}
		if preferNewQueryTrack && webResearchCandidateHasNewQueryTrack(ordered[0], queryTracks) {
			bestScore += bestBaseScore + 1
		}
		for index := 1; index < len(ordered); index++ {
			score := webResearchCandidateScore(ordered[index], domains)
			if preferNewDomain && webResearchCandidateHasNewDomain(ordered[index], domains) {
				score += bestBaseScore + 1
			}
			if preferNewLanguage && webResearchCandidateHasNewLanguage(ordered[index], languages) {
				score += bestBaseScore + 1
			}
			if preferNewQueryTrack && webResearchCandidateHasNewQueryTrack(ordered[index], queryTracks) {
				score += bestBaseScore + 1
			}
			if score > bestScore {
				bestIndex, bestScore = index, score
			}
		}
		candidate := ordered[bestIndex]
		selected = append(selected, candidate)
		if domain := webResearchIndependentDomain(stringValue(mapValue(candidate)["url"])); domain != "" {
			domains[domain] = true
		}
		if language := webResearchCandidateLanguage(candidate); language != "" {
			languages[language] = true
		}
		if track := webResearchCandidateQueryTrack(candidate); track != "" {
			queryTracks[track] = true
		}
		ordered = append(ordered[:bestIndex], ordered[bestIndex+1:]...)
	}
	return selected
}

// webResearchSourceFrontier reports whether each discovered language lane has a
// complete relevant read or has exhausted its untried routes. query_variants are
// alternative discovery routes for one investigation, not independent completion
// obligations. One inaccessible URL therefore cannot keep a language lane open
// after another route produced a complete relevant read. Numeric quality targets
// remain telemetry; they never decide this continuation or logical-task success.
func webResearchSourceFrontier(candidates []any, selected map[string]bool, sources []any) map[string]any {
	candidateByURL := map[string]map[string]any{}
	discoveredLanes := map[string]bool{}
	excludedIrrelevant := 0
	for _, raw := range candidates {
		item := mapValue(raw)
		canonical := canonicalWebResearchURL(stringValue(item["url"]))
		if canonical == "" {
			continue
		}
		if !webResearchCandidateRelevantForFrontier(item) {
			excludedIrrelevant++
			continue
		}
		candidateByURL[canonical] = item
		discoveredLanes[webResearchCandidateLane(item)] = true
	}
	resolvedRoutes := map[string]bool{}
	resolvedLanes := map[string]bool{}
	for _, raw := range sources {
		item := mapValue(raw)
		if stringValue(item["status"]) != "fetched" || !webResearchSourceDeepRead(item) {
			continue
		}
		locator := firstNonEmpty(strings.TrimSpace(stringValue(item["sourceLocator"])), strings.TrimSpace(stringValue(item["url"])))
		if canonical := canonicalWebResearchURL(locator); canonical != "" {
			resolvedRoutes[canonical] = true
			lane := webResearchCandidateLane(item)
			if lane == "query:default" {
				if candidate := candidateByURL[canonical]; len(candidate) > 0 {
					lane = webResearchCandidateLane(candidate)
				}
			}
			resolvedLanes[lane] = true
		}
	}
	unresolvedLanes := map[string]bool{}
	for lane := range discoveredLanes {
		if !resolvedLanes[lane] {
			unresolvedLanes[lane] = true
		}
	}
	alternatives := map[string]bool{}
	nextRoutes := []any{}
	for _, raw := range candidates {
		item := mapValue(raw)
		canonical := canonicalWebResearchURL(stringValue(item["url"]))
		if canonical == "" || selected[canonical] || !unresolvedLanes[webResearchCandidateLane(item)] {
			continue
		}
		if _, relevant := candidateByURL[canonical]; !relevant {
			continue
		}
		alreadyKnown := alternatives[canonical]
		alternatives[canonical] = true
		if alreadyKnown || len(nextRoutes) >= maxResearchContinuationActions {
			continue
		}
		route := map[string]any{"url": canonical, "lane": webResearchCandidateLane(item)}
		if title := strings.TrimSpace(stringValue(item["title"])); title != "" {
			route["title"] = title
		}
		nextRoutes = append(nextRoutes, route)
	}
	return map[string]any{
		"attempted_routes":               float64(len(selected)),
		"resolved_routes":                float64(len(resolvedRoutes)),
		"discovered_lanes":               float64(len(discoveredLanes)),
		"resolved_lanes":                 float64(len(resolvedLanes)),
		"unresolved_lanes":               float64(len(unresolvedLanes)),
		"excluded_irrelevant_candidates": float64(excludedIrrelevant),
		"alternative_routes":             float64(len(alternatives)),
		"next_routes":                    nextRoutes,
		"continue_recommended":           len(unresolvedLanes) > 0 && len(alternatives) > 0,
		"decision_basis":                 "discovered_language_lane_resolution_and_untried_ranked_alternatives",
	}
}

// webResearchCandidateRelevantForFrontier reuses the same query-relevance
// contract applied after a full document read. Search-result metadata that
// cannot bind to its own matched query remains visible in discovery telemetry,
// but it cannot keep a language lane open or force serial follow-up fetches.
func webResearchCandidateRelevantForFrontier(candidate map[string]any) bool {
	query := strings.TrimSpace(stringValue(candidate["matchedQueryVariant"]))
	if query == "" {
		return true
	}
	title := strings.TrimSpace(stringValue(candidate["title"]))
	snippet := strings.TrimSpace(stringValue(candidate["snippet"]))
	if title == "" && snippet == "" {
		return true
	}
	preview := strings.Join([]string{
		title, snippet, stringValue(candidate["url"]),
	}, "\n")
	relevant, _, _ := webResearchDocumentQueryRelevance(query, preview)
	return relevant
}

// webResearchCandidateEligibleForFetch uses only explicit identifiers as a
// pre-fetch exclusion boundary. Search metadata can be terse, so broader
// topical relevance remains a document-read decision; however, a candidate
// that omits a caller-supplied entity identifier cannot consume a fetch slot.
func webResearchCandidateEligibleForFetch(candidate map[string]any) bool {
	query := strings.TrimSpace(stringValue(candidate["matchedQueryVariant"]))
	if query == "" {
		return true
	}
	preview := strings.ToLower(strings.Join([]string{
		stringValue(candidate["title"]), stringValue(candidate["snippet"]), stringValue(candidate["url"]),
	}, "\n"))
	if strings.TrimSpace(preview) == "" {
		return true
	}
	for _, identifier := range webResearchExplicitIdentifierTerms(query) {
		if !webResearchDocumentContainsIdentifier(preview, identifier) {
			return false
		}
	}
	return true
}

func webResearchCandidateLane(raw any) string {
	if language := webResearchCandidateLanguage(raw); language != "" {
		return "language:" + language
	}
	return "query:default"
}

func webResearchCandidateHasNewDomain(raw any, existingDomains map[string]bool) bool {
	domain := webResearchIndependentDomain(stringValue(mapValue(raw)["url"]))
	return domain != "" && !existingDomains[domain]
}

func webResearchCandidateLanguages(candidates []any) map[string]bool {
	languages := map[string]bool{}
	for _, raw := range candidates {
		if language := webResearchCandidateLanguage(raw); language != "" {
			languages[language] = true
		}
	}
	return languages
}

func webResearchCandidateHasNewLanguage(raw any, existingLanguages map[string]bool) bool {
	language := webResearchCandidateLanguage(raw)
	return language != "" && !existingLanguages[language]
}

func webResearchCandidateQueryVariant(raw any) string {
	item := mapValue(raw)
	if variant := strings.TrimSpace(stringValue(item["matchedQueryVariant"])); variant != "" {
		return variant
	}
	return strings.TrimSpace(stringValue(mapValue(item["discovery"])["matchedQueryVariant"]))
}

func webResearchCandidateQueryTrack(raw any) string {
	return strings.ToLower(strings.Join(strings.Fields(webResearchCandidateQueryVariant(raw)), " "))
}

func webResearchCandidateHasNewQueryTrack(raw any, existing map[string]bool) bool {
	track := webResearchCandidateQueryTrack(raw)
	return track != "" && !existing[track]
}

func webResearchCandidatesHaveNewQueryTrack(candidates []any, existing map[string]bool) bool {
	for _, candidate := range candidates {
		if webResearchCandidateHasNewQueryTrack(candidate, existing) {
			return true
		}
	}
	return false
}

// webResearchCandidateLanguage reports provenance of a discovery track. It
// intentionally uses only the presence of Han characters versus a non-Han
// track; it does not translate scientific content or reject a source. When a
// provider does not return a track marker, the candidate simply participates
// in the normal quality/domain ranking.
func webResearchCandidateLanguage(raw any) string {
	item := mapValue(raw)
	if explicit := strings.ToLower(strings.TrimSpace(stringValue(item["queryLanguage"]))); explicit != "" {
		return explicit
	}
	if discovery := mapValue(item["discovery"]); len(discovery) > 0 {
		if explicit := strings.ToLower(strings.TrimSpace(stringValue(discovery["queryLanguage"]))); explicit != "" {
			return explicit
		}
		if variant := strings.TrimSpace(stringValue(discovery["matchedQueryVariant"])); variant != "" {
			return webResearchQueryLanguage(variant)
		}
	}
	variant := strings.TrimSpace(stringValue(item["matchedQueryVariant"]))
	if variant == "" {
		return ""
	}
	for _, character := range variant {
		if character >= '\u3400' && character <= '\u9fff' {
			return "zh"
		}
	}
	return "en"
}

func webResearchQueryLanguage(query string) string {
	for _, character := range query {
		if character >= '\u3400' && character <= '\u9fff' {
			return "zh"
		}
	}
	if strings.TrimSpace(query) == "" {
		return ""
	}
	return "en"
}

func webResearchCandidateScore(raw any, existingDomains map[string]bool) int {
	item := mapValue(raw)
	score := int(numberValue(item["qualityScore"])) * 100
	score += int(numberValue(item["rank"])) * -2
	if strings.TrimSpace(stringValue(item["snippet"])) != "" {
		score += 8
	}
	if domain := webResearchIndependentDomain(stringValue(item["url"])); domain != "" && !existingDomains[domain] {
		score += 60
	}
	for _, signal := range stringArrayValue(item["qualitySignals"]) {
		switch strings.ToLower(strings.TrimSpace(signal)) {
		case "primary_source", "full_text", "authoritative_domain":
			score += 15
		}
	}
	return score
}

func webResearchFetchPrompt(operation string) string {
	switch operation {
	case "fetch_pdf_text":
		return "Extract the available text from this PDF/source. Preserve source-specific facts and section headings."
	case "extract_tables":
		return "Extract tables from this source as markdown tables. Include nearby captions or headings when present."
	case "extract_links":
		return "Extract outbound links and surrounding text that explains what the links reference."
	default:
		return "Extract concise source evidence relevant to the research query. Preserve dates, named entities, and claims."
	}
}

func webResearchEvidence(role string, sourceType string, statusCode int) map[string]any {
	return map[string]any{"role": role, "sourceType": sourceType, "statusCode": float64(statusCode), "qualityScore": float64(60)}
}

func webResearchUnavailableSource(title string, sourceURL string, message string) map[string]any {
	failure := map[string]any{"kind": "source_unavailable", "message": message, "recoverable": true}
	return map[string]any{
		"title":   firstNonEmpty(title, sourceURL),
		"url":     sourceURL,
		"status":  "sourceUnavailable",
		"error":   message,
		"failure": failure,
		"evidence": map[string]any{
			"schemaVersion":  1,
			"kind":           "document",
			"status":         "sourceUnavailable",
			"evidenceState":  "unavailable",
			"sourceQuality":  "unavailable",
			"title":          firstNonEmpty(title, sourceURL),
			"url":            sourceURL,
			"failure":        failure,
			"role":           "unavailable",
			"sourceType":     "unavailable",
			"statusCode":     float64(0),
			"qualityScore":   float64(0),
			"recoverable":    true,
			"sourceEvidence": false,
		},
	}
}
