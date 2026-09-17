package server

import (
	"strings"
	"testing"

	"synon-go/internal/tools/websearch"
)

func TestWebResearchSearchInputUsesBroadCandidatePool(t *testing.T) {
	for _, test := range []struct {
		maxSources int
		want       float64
	}{
		{maxSources: 3, want: 100},
		{maxSources: 6, want: 100},
		{maxSources: 12, want: 120},
	} {
		input := webResearchSearchInput("clinical pharmacology evidence", test.maxSources, map[string]any{})
		if got := input["max_results"]; got != test.want {
			t.Fatalf("max_sources=%d max_results=%#v want %.0f", test.maxSources, got, test.want)
		}
	}
}

func TestWebResearchDiscoveryBudgetIsBoundedIndependentlyFromFetchedSources(t *testing.T) {
	if got := webResearchDiscoveryBudget(1); got != 100 {
		t.Fatalf("minimum discovery budget=%d want 100", got)
	}
	if got := webResearchDiscoveryBudget(20); got != 200 {
		t.Fatalf("maximum discovery budget=%d want 200", got)
	}
}

func TestWebResearchDefaultQualityTargetUsesIndependentEvidenceSet(t *testing.T) {
	target := webResearchResolveQualityTarget(
		map[string]any{}, "search_and_fetch", 6)
	if target.MinFetchedSources != 4 || target.MinIndependentDomains != 3 ||
		target.MinDeepReadSources != 3 || target.MaxSearchRounds != 2 {
		t.Fatalf("default target=%+v, want 4 fetched / 3 domains / 3 deep reads / 2 rounds", target)
	}

	narrow := webResearchResolveQualityTarget(
		map[string]any{}, "search_and_fetch", 2)
	if narrow.MinFetchedSources != 2 || narrow.MinIndependentDomains != 2 {
		t.Fatalf("narrow target=%+v, want quota-bounded target", narrow)
	}
}

func TestWebResearchDepthSeparatesBreadthFromFocusedReading(t *testing.T) {
	deep := webResearchResolveQualityTarget(
		map[string]any{"research_depth": "deep"}, "search_and_fetch", 12)
	if deep.MinFetchedSources != 8 || deep.MinIndependentDomains != 4 ||
		deep.MinDeepReadSources != 3 || deep.MaxSearchRounds != 3 {
		t.Fatalf("deep target=%+v, want 8 fetched / 4 domains / 3 deep reads / 3 rounds", deep)
	}
	systematic := webResearchResolveQualityTarget(
		map[string]any{"research_depth": "systematic"}, "search_and_fetch", 20)
	if systematic.MinFetchedSources != 12 || systematic.MinIndependentDomains != 5 ||
		systematic.MinDeepReadSources != 3 || systematic.MaxSearchRounds != 4 {
		t.Fatalf("systematic target=%+v, want 12 fetched / 5 domains / 3 deep reads / 4 rounds", systematic)
	}
	verify := webResearchResolveQualityTarget(
		map[string]any{}, "verify_fact", 6)
	if verify.MinDeepReadSources != verify.MinFetchedSources {
		t.Fatalf("verify_fact must deeply read every supporting source: %+v", verify)
	}
}

func TestWebResearchStagnantRoundGuardAllowsOneTransientEmptyDiscoveryWave(t *testing.T) {
	stagnant, stop := webResearchAdvanceStagnantRounds(0, 0)
	if stagnant != 1 || stop {
		t.Fatalf("first empty lens=(%d,%v), want (1,false)", stagnant, stop)
	}
	stagnant, stop = webResearchAdvanceStagnantRounds(stagnant, 0)
	if stagnant != 2 || !stop {
		t.Fatalf("second empty lens=(%d,%v), want (2,true)", stagnant, stop)
	}
	stagnant, stop = webResearchAdvanceStagnantRounds(stagnant, 1)
	if stagnant != 0 || stop {
		t.Fatalf("new candidate resets guard=(%d,%v), want (0,false)", stagnant, stop)
	}
}

func TestEffectiveWebSearchMaxResultsUsesBroadDefaultAndHonorsExplicitScope(t *testing.T) {
	for _, test := range []struct {
		requested int
		want      int
	}{
		{requested: 0, want: 50},
		{requested: 10, want: 10},
		{requested: 20, want: 20},
		{requested: 80, want: 80},
		{requested: 250, want: 200},
	} {
		if got := websearch.EffectiveResultLimit(test.requested); got != test.want {
			t.Fatalf("websearch.EffectiveResultLimit(%d)=%d want %d", test.requested, got, test.want)
		}
	}
}

func TestMergeWebSearchVariantOutputsPreservesLanguageCoverageAndDeduplicates(t *testing.T) {
	queries := []string{"poorly soluble weak base formulation", "难溶性弱碱药物 制剂开发"}
	outputs := []websearch.Output{
		{Sources: []websearch.Evidence{
			{URL: "https://example.org/shared", Title: "Shared evidence"},
			{URL: "https://example.org/en", Title: "English evidence"},
		}},
		{Sources: []websearch.Evidence{
			{URL: "https://example.org/shared#section", Title: "共享证据"},
			{URL: "https://example.cn/zh", Title: "中文证据"},
		}},
	}
	merged, err := websearch.MergeVariantOutputs(queries, outputs, make([]error, 2), 10)
	if err != nil || len(merged.Sources) != 3 {
		t.Fatalf("merged=%#v err=%v", merged, err)
	}
	if merged.Sources[0].Metadata["matchedQueryVariant"] != queries[0] ||
		merged.Sources[1].Metadata["matchedQueryVariant"] != queries[0] ||
		merged.Sources[2].Metadata["matchedQueryVariant"] != queries[1] {
		t.Fatalf("variant interleave=%#v", merged.Sources)
	}
	if merged.Diagnostics["returnedResults"] != 3 || len(merged.Results) != 2 {
		t.Fatalf("merged diagnostics/results=%#v / %#v", merged.Diagnostics, merged.Results)
	}
	if merged.Retrieval["query_variants"] != 2 || merged.Retrieval["candidates"] != 4 ||
		merged.Retrieval["returned"] != 3 || merged.Retrieval["duplicates_or_overflow"] != 1 ||
		merged.Retrieval["provider_total_known"] != false {
		t.Fatalf("merged retrieval coverage=%#v", merged.Retrieval)
	}
}

func TestNormalizedWebSearchQueriesKeepsExactIdentifierOnce(t *testing.T) {
	queries := websearch.NormalizeQueries("NCT04015076", []string{" NCT04015076 ", "KRAS inhibitor clinical trial", "KRAS 抑制剂 临床试验"})
	if len(queries) != 3 || queries[0] != "NCT04015076" {
		t.Fatalf("queries=%#v", queries)
	}
}

func TestNormalizedWebSearchQueriesDoesNotInventSemanticVariant(t *testing.T) {
	queries := websearch.NormalizeQueries("口服肽脂质纳米递送", nil)
	if len(queries) != 1 || queries[0] != "口服肽脂质纳米递送" {
		t.Fatalf("queries=%#v", queries)
	}
}

func TestNormalizedWebSearchQueriesKeepsExplicitVariantsWithCallerSynonyms(t *testing.T) {
	queries := websearch.NormalizeQueries(
		"口服小分子专利",
		[]string{"口服小分子化合物专利", "口服药物专利"},
	)
	if len(queries) != 3 || queries[0] != "口服小分子专利" ||
		!strings.Contains(queries[1], "口服小分子化合物专利") ||
		!strings.Contains(queries[2], "口服药物专利") {
		t.Fatalf("queries=%#v", queries)
	}
}

func TestNormalizedWebSearchQueriesPreservesCallerVariantLimit(t *testing.T) {
	queries := websearch.NormalizeQueries("口服小分子药物肝代谢", []string{
		"oral small molecule liver metabolism", "FDA transporter guidance",
		"clinical DDI evidence", "organ chip transporter", "AI transporter prediction",
		"extra same-language track",
	})
	if len(queries) != 7 || queries[len(queries)-1] != "extra same-language track" {
		t.Fatalf("queries=%#v; explicit variants were not preserved", queries)
	}
}

func TestWebResearchFetchedRecordsRetainDiscoveryProvenance(t *testing.T) {
	record := map[string]any{"status": "fetched", "evidence": map[string]any{"qualityScore": float64(60)}}
	webResearchAttachDiscoveryContext([]any{record}, map[string]any{
		"rank": float64(3), "host": "example.org", "qualityScore": float64(91),
		"qualitySignals": []string{"primary_source"}, "snippet": "candidate context",
	})
	discovery := mapValue(record["discovery"])
	if numberValue(discovery["rank"]) != 3 || discovery["host"] != "example.org" ||
		numberValue(mapValue(record["evidence"])["qualityScore"]) != 91 {
		t.Fatalf("discovery provenance=%#v evidence=%#v", discovery, record["evidence"])
	}
}

func TestWebResearchSelectFetchCandidatesBalancesQualityAndDomains(t *testing.T) {
	candidates := []any{
		map[string]any{"url": "https://journal.example.org/low", "qualityScore": float64(96), "rank": float64(1), "snippet": "evidence"},
		map[string]any{"url": "https://journal.example.org/second", "qualityScore": float64(95), "rank": float64(2), "snippet": "evidence"},
		map[string]any{"url": "https://registry.example.gov/study", "qualityScore": float64(82), "rank": float64(8), "snippet": "evidence"},
		map[string]any{"url": "https://publisher.example.com/article", "qualityScore": float64(80), "rank": float64(9), "snippet": "evidence"},
	}
	target := webResearchQualityTarget{MinIndependentDomains: 3}
	selected := webResearchSelectFetchCandidates(candidates, nil, target, 3)
	if len(selected) != 3 {
		t.Fatalf("selected=%d want exactly remaining fetch quota", len(selected))
	}
	domains := map[string]bool{}
	for _, raw := range selected {
		domains[webResearchIndependentDomain(stringValue(mapValue(raw)["url"]))] = true
	}
	if len(domains) != 3 {
		t.Fatalf("selected domains=%v want quality-ranked diverse shortlist", domains)
	}
}

func TestWebResearchSelectFetchCandidatesRespectsRemainingQuotaAfterFailures(t *testing.T) {
	fetched := []any{
		map[string]any{"url": "https://one.example/a", "status": "fetched"},
		map[string]any{"url": "https://two.example/b", "status": "sourceUnavailable"},
	}
	candidates := []any{
		map[string]any{"url": "https://three.example/c", "qualityScore": float64(80)},
		map[string]any{"url": "https://four.example/d", "qualityScore": float64(70)},
		map[string]any{"url": "https://five.example/e", "qualityScore": float64(60)},
	}
	selected := webResearchSelectFetchCandidates(candidates, fetched, webResearchQualityTarget{MinFetchedSources: 3}, 3)
	if len(selected) != 2 {
		t.Fatalf("selected=%d want two remaining fetch slots", len(selected))
	}
}

func TestWebResearchSourcesFromSearchRetainsDiscoveryLanguageTrack(t *testing.T) {
	result := websearch.Output{Sources: []websearch.Evidence{
		{URL: "https://en.example.org/article", Title: "English source", Metadata: map[string]any{
			"matchedQueryVariant": "oral peptide delivery clinical trial",
		}},
	}}
	candidates := webResearchSourcesFromSearch(result, 10)
	if len(candidates) != 1 || stringValue(mapValue(candidates[0])["queryLanguage"]) != "en" {
		t.Fatalf("candidate language provenance=%#v", candidates)
	}
	fetched := map[string]any{"status": "fetched"}
	webResearchAttachDiscoveryContext([]any{fetched}, mapValue(candidates[0]))
	if got := webResearchCandidateLanguage(fetched); got != "en" {
		t.Fatalf("fetched language provenance=%q record=%#v", got, fetched)
	}
}

func TestWebResearchSelectFetchCandidatesBalancesAvailableLanguageTracks(t *testing.T) {
	candidates := []any{
		map[string]any{"url": "https://journal.example.org/zh", "qualityScore": float64(96), "rank": float64(1), "queryLanguage": "zh"},
		map[string]any{"url": "https://registry.example.gov/en", "qualityScore": float64(72), "rank": float64(2), "queryLanguage": "en"},
		map[string]any{"url": "https://publisher.example.com/zh2", "qualityScore": float64(95), "rank": float64(3), "queryLanguage": "zh"},
	}
	selected := webResearchSelectFetchCandidates(candidates, nil, webResearchQualityTarget{
		MinFetchedSources: 3, MinIndependentDomains: 3,
	}, 3)
	languages := map[string]bool{}
	for _, raw := range selected {
		languages[webResearchCandidateLanguage(raw)] = true
	}
	if len(languages) != 2 {
		t.Fatalf("selected language tracks=%v candidates=%#v", languages, selected)
	}
}

func TestWebResearchSelectFetchCandidatesBalancesCallerQueryTracksAndSkipsIrrelevantCandidates(t *testing.T) {
	candidates := []any{
		map[string]any{
			"url": "https://one.example.org/mechanism", "qualityScore": float64(96),
			"matchedQueryVariant": "TARGET7 inhibitor mechanism", "title": "TARGET7 inhibitor mechanism evidence",
		},
		map[string]any{
			"url": "https://two.example.org/mechanism", "qualityScore": float64(95),
			"matchedQueryVariant": "TARGET7 inhibitor mechanism", "title": "TARGET7 inhibitor mechanism study",
		},
		map[string]any{
			"url": "https://three.example.org/resistance", "qualityScore": float64(40),
			"matchedQueryVariant": "TARGET7 clinical resistance", "title": "TARGET7 clinical resistance evidence",
		},
		map[string]any{
			"url": "https://irrelevant.example.org/agriculture", "qualityScore": float64(100),
			"matchedQueryVariant": "TARGET7 inhibitor mechanism", "title": "Agricultural cooperation mechanism",
		},
	}
	selected := webResearchSelectFetchCandidates(candidates, nil, webResearchQualityTarget{
		MinFetchedSources: 2,
	}, 2)
	if len(selected) != 2 {
		t.Fatalf("selected=%#v", selected)
	}
	tracks := map[string]bool{}
	for _, raw := range selected {
		item := mapValue(raw)
		if strings.Contains(stringValue(item["url"]), "irrelevant") {
			t.Fatalf("irrelevant candidate consumed a fetch slot: %#v", selected)
		}
		tracks[webResearchCandidateQueryTrack(item)] = true
	}
	if len(tracks) != 2 {
		t.Fatalf("caller-provided research tracks were not fairly represented: %#v", selected)
	}
}

func TestWebResearchCandidateFetchQueryUsesItsDiscoveryTrack(t *testing.T) {
	candidate := map[string]any{
		"matchedQueryVariant": "TARGET7 resistance mechanism",
		"discovery":           map[string]any{"matchedQueryVariant": "stale fallback"},
	}
	if got := webResearchCandidateQueryVariant(candidate); got != "TARGET7 resistance mechanism" {
		t.Fatalf("candidate fetch query=%q", got)
	}
	if got := webResearchCandidateQueryVariant(map[string]any{
		"discovery": map[string]any{"matchedQueryVariant": "TARGET7 clinical evidence"},
	}); got != "TARGET7 clinical evidence" {
		t.Fatalf("nested candidate fetch query=%q", got)
	}
	if !webResearchCandidateEligibleForFetch(map[string]any{
		"matchedQueryVariant": "treatment resistance mechanism", "title": "DNA repair dependency study",
	}) {
		t.Fatal("terse metadata without an explicit entity identifier was rejected before reading")
	}
}
