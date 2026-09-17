package registry

import (
	"context"
	"strings"
	"testing"
)

func TestResearchSourceSearchContractExposesTypedTimeAndContinuationWithoutChangingToolAuthority(t *testing.T) {
	registry := Default()
	search, found := registry.Get("web_search")
	if !found || search.Exposure != ToolExposureDirect || !search.Executable {
		t.Fatalf("canonical web_search authority=%#v found=%v", search, found)
	}
	for _, name := range []string{"published_after", "published_before", "sort", "provider_continuations"} {
		field, exists := search.Input[name]
		if !exists || field.Required {
			t.Fatalf("optional research search field %q=%#v found=%v", name, field, exists)
		}
	}
	if got := search.Input["sort"].Schema["enum"]; len(got.([]string)) != 2 {
		t.Fatalf("search sort contract=%#v", got)
	}
	continuations := search.Input["provider_continuations"].Schema
	if continuations["maxProperties"] != 8 || continuations["additionalProperties"] == nil {
		t.Fatalf("provider continuation bounds=%#v", continuations)
	}
}

func TestResearchSourceSearchRejectsInvalidTimeAndAmbiguousContinuationBeforeNetwork(t *testing.T) {
	registry := Default()
	_, err := registry.Execute(context.Background(), "web_search", map[string]any{
		"query": "targeted therapy", "published_before": "2026-02-31",
	})
	if err == nil || !strings.Contains(err.Error(), "published_before") {
		t.Fatalf("invalid calendar date error=%v", err)
	}
	_, err = registry.Execute(context.Background(), "web_search", map[string]any{
		"query": "targeted therapy", "query_variants": []any{"靶向治疗"},
		"provider_continuations": map[string]any{"crossref": "cursor"},
	})
	if err == nil || !strings.Contains(err.Error(), "one query") {
		t.Fatalf("ambiguous multi-query continuation error=%v", err)
	}
	_, err = registry.Execute(context.Background(), "web_search", map[string]any{
		"query": "targeted therapy", "provider_continuations": map[string]any{"crossref": 42},
	})
	if err == nil || !strings.Contains(err.Error(), "provider_continuations") {
		t.Fatalf("non-string continuation error=%v", err)
	}
}
