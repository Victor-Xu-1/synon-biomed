package websearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestResearchSourceSearchPreservesCrossrefRecordMetadata(t *testing.T) {
	abstract := strings.Repeat("Complete abstract evidence. ", 80)
	body, err := json.Marshal(map[string]any{"message": map[string]any{"items": []any{map[string]any{
		"DOI": "10.1000/primary", "title": []any{"Primary article"},
		"URL": "https://doi.org/10.1000/primary", "abstract": "<jats:p>" + abstract + "</jats:p>",
		"publisher": "Evidence Publisher", "container-title": []any{"Evidence Journal"},
		"author":    []any{map[string]any{"given": "Ada", "family": "Lovelace"}},
		"published": map[string]any{"date-parts": []any{[]any{2026, 5, 18}}},
	}}}})
	if err != nil {
		t.Fatal(err)
	}

	results, err := parseCrossrefResults(body)
	if err != nil || len(results) != 1 {
		t.Fatalf("results=%#v err=%v", results, err)
	}
	record := results[0].Record
	if record == nil || record.Provider != "crossref" || record.Publisher != "Evidence Publisher" ||
		record.Journal != "Evidence Journal" || record.Authors != "Ada Lovelace" ||
		record.RecordDepth != "abstract_record" || !record.AbstractComplete || record.Abstract != strings.TrimSpace(abstract) {
		t.Fatalf("Crossref record metadata was lost: %#v", record)
	}
	if !sourceRecordHasIdentifier(record, "doi", "10.1000/primary") {
		t.Fatalf("Crossref DOI identity was lost: %#v", record)
	}
	if record.Published == nil || record.Published.Value != "2026-05-18" ||
		record.Published.Precision != "day" || record.Published.SourceField != "published.date-parts" {
		t.Fatalf("Crossref publication date was lost or overclaimed: %#v", record.Published)
	}
	if record.CitationHandle != "doi:10.1000/primary" || !strings.Contains(record.CitationText, "Primary article") ||
		!strings.Contains(record.CitationText, "DOI:10.1000/primary") {
		t.Fatalf("deterministic Crossref citation=%#v", record)
	}
	if len([]byte(results[0].Snippet)) > maxSearchSnippetCharacters {
		t.Fatalf("display snippet exceeded its presentation budget: %d", len([]byte(results[0].Snippet)))
	}
}

func TestResearchSourceSearchReportsAppliedProviderCoverageThroughRealHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		if request.URL.Path == "/crossref" {
			if !strings.Contains(query.Get("filter"), "from-pub-date:2025-01-01") ||
				!strings.Contains(query.Get("filter"), "until-pub-date:2026-09-10") ||
				query.Get("sort") != "published" || query.Get("cursor") != "*" {
				http.Error(w, "Crossref constraints missing", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"message":{"total-results":2,"next-cursor":"cross-next","items":[{"DOI":"10.1000/targeted","title":["Targeted therapy evidence"],"URL":"https://doi.org/10.1000/targeted","published":{"date-parts":[[2026,1,2]]}}]}}`))
			return
		}
		if !strings.Contains(query.Get("query"), "FIRST_PDATE:[2025-01-01 TO 2026-09-10]") ||
			!strings.Contains(query.Get("query"), "sort_date:y") || query.Get("cursorMark") != "*" {
			http.Error(w, "Europe PMC constraints missing", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hitCount":3,"nextCursorMark":"europe-next","resultList":{"result":[{"title":"Targeted therapy registry evidence","pmid":"123","source":"MED","id":"123","pubYear":"2025"}]}}`))
	}))
	defer server.Close()

	out, err := Search(context.Background(), Input{
		Query: "targeted therapy", MaxResults: 10, PublishedAfter: "2025-01-01",
		PublishedBefore: "2026-09-10", Sort: "published_desc",
	}, Options{
		HTTPBackends: []httpSearchBackend{
			{Name: "crossref", Format: searchFormatCrossref, Endpoint: server.URL + "/crossref?query.bibliographic={query}&filter=type:journal-article"},
			{Name: "europe-pmc", Format: searchFormatEuropePMC, Endpoint: server.URL + "/europe?query={query}&resultType=core&format=json"},
		},
		ClientForURL: testHTTPClientFor(server.Client()),
		Now:          func() time.Time { return time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil || out.Failure != nil || len(out.Sources) != 2 {
		t.Fatalf("real provider integration output=%#v err=%v", out, err)
	}
	continuations, ok := out.Retrieval["provider_continuations"].(map[string]string)
	if !ok || continuations["crossref"] != "cross-next" || continuations["europe-pmc"] != "europe-next" {
		t.Fatalf("provider continuations=%#v", out.Retrieval)
	}
	totals, ok := out.Retrieval["provider_totals"].(map[string]any)
	if !ok || totals["crossref"] != 2 || totals["europe-pmc"] != 3 {
		t.Fatalf("provider totals=%#v", out.Retrieval)
	}
}

func TestResearchSourceSearchBuildsProviderSpecificTemporalContinuationRequests(t *testing.T) {
	input := Input{
		Query: "targeted therapy", PublishedAfter: "2025-01-01", PublishedBefore: "2026-09-10",
		Sort: "published_desc", ProviderContinuations: map[string]string{
			"crossref": "crossref-cursor", "europe-pmc": "europe-cursor",
		},
	}

	crossrefTarget, crossrefState, err := searchHTTPBackendTarget(input, httpSearchBackend{
		Name: "crossref", Format: searchFormatCrossref,
		Endpoint: "https://api.crossref.org/works?query.bibliographic={query}&rows={limit}&filter=type:journal-article",
	}, 50)
	if err != nil {
		t.Fatal(err)
	}
	crossrefURL, _ := url.Parse(crossrefTarget)
	filter := crossrefURL.Query().Get("filter")
	if !strings.Contains(filter, "type:journal-article") || !strings.Contains(filter, "from-pub-date:2025-01-01") ||
		!strings.Contains(filter, "until-pub-date:2026-09-10") || crossrefURL.Query().Get("cursor") != "crossref-cursor" ||
		crossrefURL.Query().Get("sort") != "published" || crossrefURL.Query().Get("order") != "desc" {
		t.Fatalf("Crossref request lost an applied constraint: %s", crossrefTarget)
	}
	if !crossrefState.PublishedFilterApplied || !crossrefState.SortApplied || !crossrefState.ContinuationApplied {
		t.Fatalf("Crossref request state=%#v", crossrefState)
	}

	europeTarget, europeState, err := searchHTTPBackendTarget(input, httpSearchBackend{
		Name: "europe-pmc", Format: searchFormatEuropePMC,
		Endpoint: "https://www.ebi.ac.uk/europepmc/webservices/rest/search?query={query}&pageSize={limit}&resultType=core&format=json",
	}, 50)
	if err != nil {
		t.Fatal(err)
	}
	europeURL, _ := url.Parse(europeTarget)
	europeQuery := europeURL.Query().Get("query")
	if !strings.Contains(europeQuery, "targeted therapy") ||
		!strings.Contains(europeQuery, "FIRST_PDATE:[2025-01-01 TO 2026-09-10]") ||
		!strings.Contains(europeQuery, "sort_date:y") || europeURL.Query().Get("cursorMark") != "europe-cursor" {
		t.Fatalf("Europe PMC request lost an applied constraint: %s", europeTarget)
	}
	if !europeState.PublishedFilterApplied || !europeState.SortApplied || !europeState.ContinuationApplied {
		t.Fatalf("Europe PMC request state=%#v", europeState)
	}

	genericTarget, genericState, err := searchHTTPBackendTarget(input, httpSearchBackend{
		Name: "bing-html", Format: searchFormatHTML,
		Endpoint: "https://www.bing.com/search?q={query}&count={limit}",
	}, 50)
	if err != nil || !strings.Contains(genericTarget, "targeted+therapy") {
		t.Fatalf("generic request=%s err=%v", genericTarget, err)
	}
	if genericState.PublishedFilterApplied || genericState.SortApplied || genericState.ContinuationApplied ||
		len(genericState.Unsupported) != 2 {
		t.Fatalf("generic provider overstated applied capabilities: %#v", genericState)
	}
}

func TestResearchSourceSearchPreservesProviderCoverageAndContinuation(t *testing.T) {
	crossref, err := parseHTTPBackendResponse(httpSearchBackend{Format: searchFormatCrossref}, []byte(`{
		"message":{"total-results":71,"next-cursor":"next-crossref","items":[
			{"DOI":"10.1000/a","title":["A"],"URL":"https://doi.org/10.1000/a"}
		]}}`))
	if err != nil || len(crossref.Results) != 1 || !crossref.TotalKnown || crossref.TotalResults != 71 ||
		crossref.NextCursor != "next-crossref" {
		t.Fatalf("Crossref coverage=%#v err=%v", crossref, err)
	}
	europe, err := parseHTTPBackendResponse(httpSearchBackend{Format: searchFormatEuropePMC}, []byte(`{
		"hitCount":19,"nextCursorMark":"next-europe","resultList":{"result":[
			{"title":"B","pmid":"123","source":"MED","id":"123"}
		]}}`))
	if err != nil || len(europe.Results) != 1 || !europe.TotalKnown || europe.TotalResults != 19 ||
		europe.NextCursor != "next-europe" {
		t.Fatalf("Europe PMC coverage=%#v err=%v", europe, err)
	}
	unknown, err := parseHTTPBackendResponse(httpSearchBackend{Format: searchFormatCrossref}, []byte(`{
		"message":{"items":[{"DOI":"10.1000/c","title":["C"],"URL":"https://doi.org/10.1000/c"}]}}`))
	if err != nil || unknown.TotalKnown {
		t.Fatalf("missing provider total was relabeled as known zero: %#v err=%v", unknown, err)
	}
}

func TestResearchSourceSearchPreservesEuropePMCRecordMetadata(t *testing.T) {
	abstract := strings.Repeat("Complete registry abstract. ", 80)
	body, err := json.Marshal(map[string]any{"resultList": map[string]any{"result": []any{map[string]any{
		"title": "Registry article", "authorString": "Grace Hopper et al.",
		"journalTitle": "Evidence Medicine", "pubYear": "2025", "firstPublicationDate": "2025-11-02",
		"doi": "10.1000/registry", "pmid": "12345678", "pmcid": "PMC1234567",
		"source": "MED", "id": "12345678", "abstractText": abstract,
	}}}})
	if err != nil {
		t.Fatal(err)
	}

	results, err := parseEuropePMCResults(body)
	if err != nil || len(results) != 1 {
		t.Fatalf("results=%#v err=%v", results, err)
	}
	record := results[0].Record
	if record == nil || record.Provider != "europe-pmc" || record.Journal != "Evidence Medicine" ||
		record.Authors != "Grace Hopper et al." || record.RecordDepth != "abstract_record" ||
		!record.AbstractComplete || record.Abstract != strings.TrimSpace(abstract) {
		t.Fatalf("Europe PMC record metadata was lost: %#v", record)
	}
	for namespace, value := range map[string]string{
		"doi": "10.1000/registry", "pmid": "12345678", "pmcid": "PMC1234567",
	} {
		if !sourceRecordHasIdentifier(record, namespace, value) {
			t.Fatalf("Europe PMC %s identity was lost: %#v", namespace, record)
		}
	}
	if record.Published == nil || record.Published.Value != "2025-11-02" ||
		record.Published.Precision != "day" || record.Published.SourceField != "firstPublicationDate" {
		t.Fatalf("Europe PMC publication date was lost: %#v", record.Published)
	}
	if record.CitationHandle != "doi:10.1000/registry" || !strings.Contains(record.CitationText, "Registry article") {
		t.Fatalf("deterministic Europe PMC citation=%#v", record)
	}
}
