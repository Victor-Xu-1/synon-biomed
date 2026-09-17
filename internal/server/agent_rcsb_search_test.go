package server

import (
	"context"
	"errors"
	"testing"

	"synon-go/internal/agentruntime"
	"synon-go/internal/tools/rcsbsearch"
)

type agentRCSBSearcher struct {
	calls  []rcsbsearch.Input
	result rcsbsearch.Result
	err    error
}

func (s *agentRCSBSearcher) Search(_ context.Context, input rcsbsearch.Input) (rcsbsearch.Result, error) {
	s.calls = append(s.calls, input)
	return s.result, s.err
}

func TestAgentRCSBSearchUsesValidatedHostSourceAndReturnsProvenance(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	searcher := &agentRCSBSearcher{result: rcsbsearch.Result{
		Query: "KEAP1 NRF2", TermMode: rcsbsearch.TermModeAll, Organism: "Homo sapiens",
		SortBy: rcsbsearch.SortReleaseDateDesc, TotalCount: 1, RetrievedAt: "2026-08-16T08:00:00Z",
		SearchRequestSHA256:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SearchDocumentationURL: "https://search.rcsb.org/",
		Entries: []rcsbsearch.Entry{{
			EntryID: "8EHV", Title: "Kelch domain of human KEAP1 bound to Nrf2 cyclic peptide",
			InitialReleaseDate: "2023-09-20T00:00:00.000+00:00",
			StructureURL:       "https://www.rcsb.org/structure/8EHV",
			MetadataURL:        "https://data.rcsb.org/rest/v1/core/entry/8EHV",
		}},
	}}
	fixture.server.rcsbSearch = searcher
	input := map[string]any{
		"query": "KEAP1 NRF2", "term_mode": "all", "organism": "Homo sapiens",
		"experimental_method": "X-RAY DIFFRACTION", "minimum_polymer_entity_count": float64(2),
		"maximum_resolution": 3.0, "sort_by": "release_date_desc", "limit": float64(5),
	}
	result, err := fixture.server.executeAgentRCSBSearch(
		fixture.toolContext(t, "rcsb-search-live", input), fixture.identity, input,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(searcher.calls) != 1 || searcher.calls[0].Query != "KEAP1 NRF2" ||
		searcher.calls[0].MinimumPolymerEntityCount != 2 || searcher.calls[0].Limit != 5 {
		t.Fatalf("calls=%#v", searcher.calls)
	}
	entries, ok := result["entries"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("result=%#v", result)
	}
	entry, ok := entries[0].(map[string]any)
	if !ok || entry["entry_id"] != "8EHV" || result["retrieved_at"] != "2026-08-16T08:00:00Z" ||
		result["search_request_sha256"] == "" {
		t.Fatalf("result=%#v", result)
	}
}

func TestAgentRCSBSearchRejectsStaleAuthorityBeforeNetwork(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	searcher := &agentRCSBSearcher{}
	fixture.server.rcsbSearch = searcher
	input := map[string]any{"query": "valid target"}
	if _, err := fixture.server.executeAgentRCSBSearch(context.Background(), fixture.identity, input); !errors.Is(err, errAgentRCSBSearchAuthority) {
		t.Fatalf("err=%v", err)
	}
	if len(searcher.calls) != 0 {
		t.Fatalf("unauthorized search reached network: %#v", searcher.calls)
	}
}

func TestAgentKernelAdvertisesRCSBSearchOnlyWhenAllowed(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	fixture.server.rcsbSearch = &agentRCSBSearcher{}
	allowed := map[string]struct{}{"search_rcsb_structures": {}}
	if !agentToolSchemasContain(fixture.server.agentKernelToolSchemas(fixture.identity, allowed), "search_rcsb_structures") {
		t.Fatal("allowed RCSB search schema is missing")
	}
	if agentToolSchemasContain(
		fixture.server.agentKernelToolSchemas(fixture.identity, map[string]struct{}{"save_artifacts": {}}),
		"search_rcsb_structures",
	) {
		t.Fatal("RCSB search schema escaped the selected-skill allowlist")
	}
}

func agentToolSchemasContain(schemas []agentruntime.ToolSchema, name string) bool {
	for _, schema := range schemas {
		if schema.Name == name {
			return true
		}
	}
	return false
}
