package server

import (
	"strings"
	"testing"

	"synon-go/internal/tools/webfetch"
)

func TestWebResearchReadableDocumentRemovesPageChromeAndPreservesEvidence(t *testing.T) {
	html := `<html><head><title>Primary evidence</title><script>secretNoise()</script></head><body>` +
		`<nav>Navigation</nav><h1>Study result</h1><p>The treated cohort had a measured response.</p>` +
		`<style>.hidden{display:none}</style></body></html>`
	readable := webResearchReadableDocument(html, "text/html")
	if !strings.Contains(readable, "Study result\nThe treated cohort") ||
		strings.Contains(readable, "secretNoise") || strings.Contains(readable, "display:none") {
		t.Fatalf("readable document = %q", readable)
	}
	if title := webResearchDocumentTitle(html, "fallback"); title != "Primary evidence" {
		t.Fatalf("title = %q", title)
	}
}

func TestWebResearchReadReceiptDistinguishesCompleteAndPartialPages(t *testing.T) {
	text := strings.Repeat("evidence ", 200)
	complete := webResearchReadReceipt(webfetch.Result{
		StatusCode: 200, ContentType: "text/plain", BytesRead: len(text), ContentLength: int64(len(text)),
	}, text)
	if complete["deepRead"] != true || complete["depth"] != "complete_page" || complete["responseComplete"] != true {
		t.Fatalf("complete receipt = %#v", complete)
	}
	partial := webResearchReadReceipt(webfetch.Result{
		StatusCode: 200, ContentType: "text/plain", BytesRead: len(text), Truncated: true, Partial: true,
	}, text)
	if partial["deepRead"] != false || partial["depth"] != "partial_page" || partial["responseComplete"] != false {
		t.Fatalf("partial receipt = %#v", partial)
	}
}

func TestWebResearchReadReceiptDoesNotCountACompletelyReadIrrelevantPageAsDeepEvidence(t *testing.T) {
	text := strings.Repeat("Five Hundred Miles is a song lyric with no scientific evidence. ", 20)
	receipt := webResearchReadReceipt(webfetch.Result{
		StatusCode: 200, ContentType: "text/plain", BytesRead: len(text), ContentLength: int64(len(text)),
	}, text, "口服肽类药物的吸收促进与脂质纳米递送")
	if receipt["deepRead"] != false || receipt["depth"] != "off_topic_page" || receipt["queryRelevant"] != false {
		t.Fatalf("irrelevant complete page receipt = %#v", receipt)
	}
}

func TestWebResearchReadReceiptCountsRelevantCrossLanguageSourceContent(t *testing.T) {
	text := strings.Repeat("Oral peptide delivery using lipid nanoparticles improved absorption and permeation. ", 20)
	receipt := webResearchReadReceipt(webfetch.Result{
		StatusCode: 200, ContentType: "text/plain", BytesRead: len(text), ContentLength: int64(len(text)),
	}, text, "口服肽类药物的吸收促进与脂质纳米递送")
	if receipt["deepRead"] != true || receipt["depth"] != "complete_page" || receipt["queryRelevant"] != true {
		t.Fatalf("relevant complete page receipt = %#v", receipt)
	}
}

func TestWebResearchReadReceiptRejectsAConjunctiveIdentifierMismatch(t *testing.T) {
	text := strings.Repeat("ABC-123 is a selective QRS2 inhibitor in clinical development. ", 30)
	receipt := webResearchReadReceipt(webfetch.Result{
		StatusCode: 200, ContentType: "text/plain", BytesRead: len(text), ContentLength: int64(len(text)),
	}, text, "ABC-123 XYZ1 inhibitor clinical development")
	if receipt["queryRelevant"] != false || receipt["deepRead"] != false || receipt["depth"] != "off_topic_page" {
		t.Fatalf("source missing one explicit identifier was admitted as evidence: %#v", receipt)
	}
}

func TestWebResearchEvidenceDocumentEligibilityExcludesOffTopicContent(t *testing.T) {
	if webResearchEvidenceDocumentEligible(map[string]any{"queryRelevant": false}) {
		t.Fatal("off-topic document content remained eligible for synthesis")
	}
	if !webResearchEvidenceDocumentEligible(map[string]any{"queryRelevant": true, "partial": true}) {
		t.Fatal("relevant partial content was incorrectly discarded")
	}
}

func TestWebResearchQualityExcludesOffTopicSources(t *testing.T) {
	quality := webResearchQualityWithTarget([]any{map[string]any{
		"status": "offTopic", "url": "https://unrelated.example/article",
		"readReceipt": map[string]any{"queryRelevant": false, "extractableCharacters": float64(5000)},
	}}, webResearchQualityTarget{MinFetchedSources: 1, MinIndependentDomains: 1}, 1)
	if quality["fetchedSources"] != float64(0) || quality["independentDomains"] != float64(0) ||
		quality["irrelevantSources"] != float64(1) || quality["meetsTarget"] != false {
		t.Fatalf("off-topic source affected evidence quality: %#v", quality)
	}
}

func TestWebResearchSystematicDepthRaisesReadingTargetWithoutUsingHitCount(t *testing.T) {
	target := webResearchResolveQualityTarget(map[string]any{
		"research_depth": "systematic",
	}, "search_and_fetch", 20)
	if target.MinFetchedSources != 12 || target.MinDeepReadSources != 3 ||
		target.MinIndependentDomains != 5 || target.MaxSearchRounds != 4 {
		t.Fatalf("systematic target = %+v", target)
	}
	sources := []any{
		map[string]any{"status": "fetched", "url": "https://one.example/a", "readReceipt": map[string]any{"deepRead": true, "extractableCharacters": float64(800)}},
		map[string]any{"status": "fetched", "url": "https://two.example/b", "readReceipt": map[string]any{"deepRead": false, "extractableCharacters": float64(1200)}},
	}
	quality := webResearchQualityWithTarget(sources, webResearchQualityTarget{
		MinFetchedSources: 2, MinIndependentDomains: 2, MinDeepReadSources: 2, MaxSearchRounds: 2,
	}, 1)
	if quality["fetchedSources"] != float64(2) || quality["deepReadSources"] != float64(1) || quality["meetsTarget"] != false {
		t.Fatalf("depth-aware quality = %#v", quality)
	}
}

func TestWebResearchVerifyFactUsesOnlyDeepReadDocuments(t *testing.T) {
	fact := "the trial met its primary endpoint"
	result := map[string]any{
		"documents": []any{
			map[string]any{"url": "https://thin.example/a", "content": fact, "readReceipt": map[string]any{"deepRead": false}},
			map[string]any{"url": "https://one.example/a", "content": fact, "readReceipt": map[string]any{"deepRead": true}},
			map[string]any{"url": "https://two.example/b", "content": fact, "readReceipt": map[string]any{"deepRead": true}},
		},
	}
	verified := webResearchVerifyFactResult(result, fact)
	if verified["verdict"] != "supported" {
		t.Fatalf("deep-read verdict=%#v", verified)
	}
	check := mapValue(verified["claimCheck"])
	if check["supportingFetchedSources"] != float64(2) ||
		check["supportingIndependentDomains"] != float64(2) ||
		check["semanticReviewRequired"] != true {
		t.Fatalf("deep-read claim check=%#v", check)
	}
}

func TestWebResearchDoesNotDeclarePartialBatchSufficient(t *testing.T) {
	target := webResearchQualityTarget{
		MinFetchedSources: 8, MinIndependentDomains: 4,
		MinDeepReadSources: 3, MaxSearchRounds: 3,
	}
	sources := []any{
		map[string]any{"status": "fetched", "url": "https://one.example/a", "readReceipt": map[string]any{"deepRead": true}},
		map[string]any{"status": "fetched", "url": "https://two.example/b", "readReceipt": map[string]any{"deepRead": true}},
		map[string]any{"status": "fetched", "url": "https://three.example/c", "readReceipt": map[string]any{"deepRead": true}},
		map[string]any{"status": "fetched", "url": "https://four.example/d", "readReceipt": map[string]any{"deepRead": false}},
	}
	if reason := webResearchQualityStopReason(sources, target); reason != "" {
		t.Fatalf("partial batch was declared sufficient: %q", reason)
	}
	for index := 0; index < 4; index++ {
		sources = append(sources, sources[index])
	}
	if reason := webResearchQualityStopReason(sources, target); reason != "quality_target_met" {
		t.Fatalf("completed batch target=%q", reason)
	}
	result := map[string]any{
		"quality": webResearchQualityWithTarget(sources[:4], target, 1), "stopReason": "max_rounds_reached",
		"sourceFrontier": map[string]any{
			"continue_recommended": true,
			"decision_basis":       "selected_route_resolution_and_untried_ranked_alternatives",
			"next_routes": []any{map[string]any{
				"url": "https://alternative.example/source", "lane": "language:en",
			}},
		},
	}
	webResearchAttachMetadata(result)
	actions := anySliceValue(result["nextActions"])
	if len(actions) != 1 {
		t.Fatalf("partial batch next actions=%#v", actions)
	}
	action := mapValue(actions[0])
	if mapValue(result["retrievalDecision"])["continueRecommended"] != true ||
		action["action"] != "fetch" || action["url"] != "https://alternative.example/source" {
		t.Fatalf("partial batch lost its continuation state: %#v", result)
	}
}

func TestWebResearchSourceFrontierContinuesOnlyForUnresolvedLanguageRoutes(t *testing.T) {
	selected := map[string]bool{
		canonicalWebResearchURL("https://journal.example/a"): true,
		canonicalWebResearchURL("https://blocked.example/b"): true,
	}
	candidates := []any{
		map[string]any{"url": "https://journal.example/a", "matchedQueryVariant": "mechanism evidence", "queryLanguage": "en"},
		map[string]any{"url": "https://blocked.example/b", "matchedQueryVariant": "临床证据", "queryLanguage": "zh"},
		map[string]any{"url": "https://alternative.example/c", "matchedQueryVariant": "临床证据", "queryLanguage": "zh"},
	}
	sources := []any{
		map[string]any{
			"status": "fetched", "sourceLocator": "https://journal.example/a",
			"readReceipt": map[string]any{"deepRead": true},
			"discovery":   map[string]any{"matchedQueryVariant": "mechanism evidence", "queryLanguage": "en"},
		},
		map[string]any{"status": "sourceUnavailable", "sourceLocator": "https://blocked.example/b"},
	}
	frontier := webResearchSourceFrontier(candidates, selected, sources)
	if !boolValue(frontier["continue_recommended"], false) ||
		frontier["unresolved_lanes"] != float64(1) || frontier["alternative_routes"] != float64(1) ||
		stringValue(mapValue(anySliceValue(frontier["next_routes"])[0])["url"]) != "https://alternative.example/c" {
		t.Fatalf("unresolved source route did not open a ranked alternative: %#v", frontier)
	}

	sources = append(sources, map[string]any{
		"status": "fetched", "sourceLocator": "https://blocked.example/b",
		"readReceipt": map[string]any{"deepRead": true},
		"discovery":   map[string]any{"matchedQueryVariant": "临床证据", "queryLanguage": "zh"},
	})
	frontier = webResearchSourceFrontier(candidates, selected, sources)
	if boolValue(frontier["continue_recommended"], false) || frontier["unresolved_lanes"] != float64(0) {
		t.Fatalf("resolved selected routes incorrectly kept the frontier open: %#v", frontier)
	}
}

func TestWebResearchSourceFrontierDoesNotChaseBadURLInResolvedLane(t *testing.T) {
	selected := map[string]bool{
		canonicalWebResearchURL("https://journal.example/a"): true,
		canonicalWebResearchURL("https://blocked.example/b"): true,
	}
	candidates := []any{
		map[string]any{"url": "https://journal.example/a", "matchedQueryVariant": "clinical evidence"},
		map[string]any{"url": "https://blocked.example/b", "matchedQueryVariant": "clinical evidence"},
		map[string]any{"url": "https://alternative.example/c", "matchedQueryVariant": "clinical evidence"},
	}
	sources := []any{
		map[string]any{
			"status": "fetched", "sourceLocator": "https://journal.example/a",
			"readReceipt": map[string]any{"deepRead": true},
			"discovery":   map[string]any{"matchedQueryVariant": "clinical evidence"},
		},
		map[string]any{"status": "sourceUnavailable", "sourceLocator": "https://blocked.example/b"},
	}
	frontier := webResearchSourceFrontier(candidates, selected, sources)
	if boolValue(frontier["continue_recommended"], false) || frontier["unresolved_lanes"] != float64(0) {
		t.Fatalf("one inaccessible sibling kept an already resolved query lane open: %#v", frontier)
	}
}

func TestWebResearchSourceFrontierTreatsSameLanguageQueryVariantsAsAlternativeRoutes(t *testing.T) {
	selected := map[string]bool{
		canonicalWebResearchURL("https://journal.example/a"): true,
		canonicalWebResearchURL("https://blocked.example/b"): true,
	}
	candidates := []any{
		map[string]any{"url": "https://journal.example/a", "matchedQueryVariant": "mechanism evidence", "queryLanguage": "en"},
		map[string]any{"url": "https://blocked.example/b", "matchedQueryVariant": "clinical evidence", "queryLanguage": "en"},
		map[string]any{"url": "https://alternative.example/c", "matchedQueryVariant": "clinical evidence", "queryLanguage": "en"},
	}
	sources := []any{
		map[string]any{
			"status": "fetched", "sourceLocator": "https://journal.example/a",
			"readReceipt": map[string]any{"deepRead": true},
			"discovery": map[string]any{
				"matchedQueryVariant": "mechanism evidence", "queryLanguage": "en",
			},
		},
		map[string]any{"status": "sourceUnavailable", "sourceLocator": "https://blocked.example/b"},
	}
	frontier := webResearchSourceFrontier(candidates, selected, sources)
	if boolValue(frontier["continue_recommended"], false) ||
		frontier["discovered_lanes"] != float64(1) || frontier["unresolved_lanes"] != float64(0) {
		t.Fatalf("same-language query variants became independent completion obligations: %#v", frontier)
	}
}

func TestWebResearchSourceFrontierExcludesIrrelevantLanguageLaneCandidates(t *testing.T) {
	selected := map[string]bool{
		canonicalWebResearchURL("https://journal.example/polq"): true,
	}
	candidates := []any{
		map[string]any{
			"url": "https://journal.example/polq", "matchedQueryVariant": "POLQ inhibitor mechanism",
			"queryLanguage": "en", "title": "POLQ inhibitor mechanism evidence",
		},
		map[string]any{
			"url": "https://irrelevant.example/social", "matchedQueryVariant": "POLQ inhibitor mechanism",
			"queryLanguage": "zh", "title": "Juvenile crime social support mechanism",
		},
	}
	sources := []any{map[string]any{
		"status": "fetched", "sourceLocator": "https://journal.example/polq",
		"readReceipt": map[string]any{"deepRead": true},
		"discovery": map[string]any{
			"matchedQueryVariant": "POLQ inhibitor mechanism", "queryLanguage": "en",
		},
	}}

	frontier := webResearchSourceFrontier(candidates, selected, sources)
	if boolValue(frontier["continue_recommended"], false) ||
		frontier["discovered_lanes"] != float64(1) || frontier["unresolved_lanes"] != float64(0) ||
		frontier["excluded_irrelevant_candidates"] != float64(1) || len(anySliceValue(frontier["next_routes"])) != 0 {
		t.Fatalf("irrelevant language candidate kept the source frontier open: %#v", frontier)
	}
}

func TestWebResearchSourceFrontierQueuesOnlyRelevantRoutesWithinUnresolvedLane(t *testing.T) {
	selected := map[string]bool{
		canonicalWebResearchURL("https://journal.example/polq"): true,
	}
	candidates := []any{
		map[string]any{
			"url": "https://journal.example/polq", "matchedQueryVariant": "POLQ inhibitor mechanism",
			"queryLanguage": "en", "title": "POLQ inhibitor mechanism evidence",
		},
		map[string]any{
			"url": "https://irrelevant.example/social", "matchedQueryVariant": "POLQ inhibitor mechanism",
			"queryLanguage": "zh", "title": "Juvenile crime social support mechanism",
		},
		map[string]any{
			"url": "https://relevant.example/polq", "matchedQueryVariant": "POLQ inhibitor mechanism",
			"queryLanguage": "zh", "title": "POLQ inhibitor mechanism study",
		},
	}
	sources := []any{map[string]any{
		"status": "fetched", "sourceLocator": "https://journal.example/polq",
		"readReceipt": map[string]any{"deepRead": true},
		"discovery": map[string]any{
			"matchedQueryVariant": "POLQ inhibitor mechanism", "queryLanguage": "en",
		},
	}}

	frontier := webResearchSourceFrontier(candidates, selected, sources)
	routes := anySliceValue(frontier["next_routes"])
	if !boolValue(frontier["continue_recommended"], false) || frontier["unresolved_lanes"] != float64(1) ||
		frontier["excluded_irrelevant_candidates"] != float64(1) || len(routes) != 1 ||
		stringValue(mapValue(routes[0])["url"]) != "https://relevant.example/polq" {
		t.Fatalf("unresolved lane queued an irrelevant route: %#v", frontier)
	}
}
