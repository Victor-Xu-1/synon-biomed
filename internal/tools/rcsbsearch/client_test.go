package rcsbsearch

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"synon-go/internal/tools/securefetch"
)

type fixtureFetcher struct {
	responses []string
	urls      []string
	policies  []securefetch.Policy
}

func (f *fixtureFetcher) Fetch(
	_ context.Context,
	target string,
	policy securefetch.Policy,
) (*securefetch.Response, error) {
	f.urls = append(f.urls, target)
	f.policies = append(f.policies, policy)
	if len(f.responses) == 0 {
		return nil, io.ErrUnexpectedEOF
	}
	body := f.responses[0]
	f.responses = f.responses[1:]
	return &securefetch.Response{
		Body: io.NopCloser(strings.NewReader(body)), StatusCode: 200, ContentType: "application/json",
	}, nil
}

func TestSearchBuildsBoundedOfficialQueryAndHydratesMetadata(t *testing.T) {
	fetcher := &fixtureFetcher{responses: []string{
		`{"total_count":2,"result_set":[{"identifier":"8EHV","score":1},{"identifier":"8EJR","score":0.9}]}`,
		entryFixture("8EHV", "Kelch domain of human KEAP1 bound to Nrf2 cyclic peptide", "2023-09-20T00:00:00.000+00:00", 2.29),
		entryFixture("8EJR", "Human KEAP1 in complex with an Nrf2 peptide", "2023-03-01T00:00:00.000+00:00", 1.85),
	}}
	now := time.Date(2026, 8, 16, 8, 0, 0, 0, time.UTC)
	client := New(Options{Fetcher: fetcher, MaxBytes: 1 << 20, Timeout: time.Second, Now: func() time.Time { return now }})
	result, err := client.Search(context.Background(), Input{
		Query: "KEAP1 NRF2", Organism: "Homo sapiens", ExperimentalMethod: "X-RAY DIFFRACTION",
		MinimumPolymerEntityCount: 2, MaximumResolution: 3, SortBy: SortReleaseDateDesc, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.TotalCount != 2 || len(result.Entries) != 2 || result.Entries[0].EntryID != "8EHV" ||
		result.Entries[0].InitialReleaseDate != "2023-09-20T00:00:00.000+00:00" ||
		result.RetrievedAt != now.Format(time.RFC3339) || len(result.SearchRequestSHA256) != 64 {
		t.Fatalf("result=%#v", result)
	}
	if len(fetcher.urls) != 3 || !strings.HasPrefix(fetcher.urls[0], productionSearchBaseURL+searchPath+"?json=") ||
		fetcher.urls[1] != productionDataBaseURL+"/rest/v1/core/entry/8EHV" ||
		fetcher.urls[2] != productionDataBaseURL+"/rest/v1/core/entry/8EJR" {
		t.Fatalf("urls=%#v", fetcher.urls)
	}
	parsed, err := url.Parse(fetcher.urls[0])
	if err != nil {
		t.Fatal(err)
	}
	var request map[string]any
	if err := json.Unmarshal([]byte(parsed.Query().Get("json")), &request); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(request)
	requestText := string(encoded)
	for _, required := range []string{
		`"value":"KEAP1 + NRF2"`,
		`"attribute":"rcsb_entity_source_organism.ncbi_scientific_name"`,
		`"attribute":"exptl.method"`,
		`"attribute":"rcsb_entry_info.polymer_entity_count"`,
		`"attribute":"rcsb_entry_info.resolution_combined"`,
		`"sort_by":"rcsb_accession_info.initial_release_date"`,
		`"rows":2`,
		`"start":0`,
	} {
		if !strings.Contains(requestText, required) {
			t.Fatalf("request missing %q: %s", required, requestText)
		}
	}
	if fetcher.policies[0].AllowedHosts[0] != "search.rcsb.org" ||
		fetcher.policies[1].AllowedHosts[0] != "data.rcsb.org" {
		t.Fatalf("policies=%#v", fetcher.policies)
	}
}

func TestSearchReportsContinuationForBroadStructureDiscovery(t *testing.T) {
	fetcher := &fixtureFetcher{responses: []string{
		`{"total_count":5,"result_set":[{"identifier":"8EHV","score":1},{"identifier":"8EJR","score":0.9}]}`,
		entryFixture("8EHV", "First structure", "2023-09-20T00:00:00.000+00:00", 2.29),
		entryFixture("8EJR", "Second structure", "2023-03-01T00:00:00.000+00:00", 1.85),
	}}
	client := New(Options{Fetcher: fetcher, MaxBytes: 1 << 20, Timeout: time.Second})
	result, err := client.Search(context.Background(), Input{Query: "KEAP1 NRF2", Limit: 2, Start: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !result.HasMore || result.Start != 2 || result.RetrievedCount != 2 || result.NextStart != 4 || result.TotalCount != 5 {
		t.Fatalf("paged result=%#v", result)
	}
}

func TestSearchRejectsMalformedInputBeforeNetwork(t *testing.T) {
	fetcher := &fixtureFetcher{}
	client := New(Options{Fetcher: fetcher, MaxBytes: 1 << 20, Timeout: time.Second})
	for _, input := range []Input{
		{Query: "x"},
		{Query: "valid target", TermMode: "raw"},
		{Query: "valid target", Limit: 26},
		{Query: "valid target", SortBy: "latest-ish"},
		{Query: "valid target", MaximumResolution: 101},
	} {
		if _, err := client.Search(context.Background(), input); err == nil || err.Error() != "rcsb_search_invalid_request" {
			t.Fatalf("input=%#v err=%v", input, err)
		}
	}
	if len(fetcher.urls) != 0 {
		t.Fatalf("invalid requests reached network: %#v", fetcher.urls)
	}
}

func TestSearchRejectsUntrustedCustomOrigins(t *testing.T) {
	fetcher := &fixtureFetcher{}
	client := New(Options{
		Fetcher: fetcher, SearchBaseURL: "https://attacker.invalid", DataBaseURL: productionDataBaseURL,
		MaxBytes: 1 << 20, Timeout: time.Second,
	})
	if _, err := client.Search(context.Background(), Input{Query: "valid target"}); err == nil ||
		err.Error() != "rcsb_search_invalid_configuration" || len(fetcher.urls) != 0 {
		t.Fatalf("err=%v urls=%#v", err, fetcher.urls)
	}
}

func TestSearchRejectsMismatchedMetadataIdentity(t *testing.T) {
	fetcher := &fixtureFetcher{responses: []string{
		`{"total_count":1,"result_set":[{"identifier":"8EHV","score":1}]}`,
		entryFixture("1X2R", "Wrong structure", "2005-01-01T00:00:00.000+00:00", 1.7),
	}}
	client := New(Options{Fetcher: fetcher, MaxBytes: 1 << 20, Timeout: time.Second})
	if _, err := client.Search(context.Background(), Input{Query: "KEAP1 NRF2"}); err == nil ||
		err.Error() != "rcsb_search_metadata_invalid" {
		t.Fatalf("err=%v", err)
	}
}

func TestSearchLiveOfficialRCSB(t *testing.T) {
	if os.Getenv("SYNON_TEST_LIVE_RCSB_SEARCH") != "1" {
		t.Skip("set SYNON_TEST_LIVE_RCSB_SEARCH=1 for the official Search API and Data API check")
	}
	client := New(Options{MaxBytes: 2 << 20, Timeout: 30 * time.Second})
	result, err := client.Search(context.Background(), Input{
		Query: "KEAP1 NRF2", Organism: "Homo sapiens", MinimumPolymerEntityCount: 2,
		SortBy: SortReleaseDateDesc, Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) == 0 || result.Entries[0].EntryID == "" || result.Entries[0].InitialReleaseDate == "" {
		t.Fatalf("live result=%#v", result)
	}
	for index := 1; index < len(result.Entries); index++ {
		if result.Entries[index-1].InitialReleaseDate < result.Entries[index].InitialReleaseDate {
			t.Fatalf("release dates not descending: %#v", result.Entries)
		}
	}
}

func entryFixture(id, title, release string, resolution float64) string {
	value := map[string]any{
		"entry": map[string]any{"id": id}, "struct": map[string]any{"title": title},
		"rcsb_accession_info": map[string]any{"initial_release_date": release, "revision_date": release},
		"exptl":               []any{map[string]any{"method": "X-RAY DIFFRACTION"}},
		"rcsb_entry_info": map[string]any{
			"resolution_combined": []float64{resolution}, "polymer_entity_count": 2,
			"nonpolymer_entity_count": 0, "polymer_composition": "heteromeric protein",
		},
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
