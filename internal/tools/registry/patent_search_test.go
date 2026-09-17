package registry

import (
	"context"
	"testing"

	"synon-go/internal/tools/patentsearch"
	"synon-go/internal/tools/websearch"
)

func TestDefaultRegistryAdvertisesPatentSearchContract(t *testing.T) {
	t.Parallel()

	tool, ok := Default().Get("patent_search")
	if !ok {
		t.Fatal("default registry does not advertise patent_search")
	}
	if !tool.Executable {
		t.Fatal("patent_search must be executable by the standalone registry")
	}
	operation := tool.Input["operation"]
	if !operation.Required || operation.Type != "string" {
		t.Fatalf("operation field = %#v", operation)
	}
	values, ok := operation.Schema["enum"].([]string)
	if !ok || len(values) != 3 {
		t.Fatalf("operation enum = %#v", operation.Schema["enum"])
	}
	for _, field := range []string{"query", "query_variants", "publication_number", "sources", "max_results"} {
		if _, ok := tool.Input[field]; !ok {
			t.Fatalf("patent_search is missing %q input", field)
		}
	}
}

func TestPatentSearchExecutesInjectedClient(t *testing.T) {
	t.Parallel()

	client := patentsearch.NewClient(patentsearch.Options{Search: func(_ context.Context, input websearch.Input, _ websearch.Options) (websearch.Output, error) {
		return websearch.Output{Sources: []websearch.Evidence{{
			URL: "https://patents.google.com/patent/WO2024123456A1/en", Title: input.Query, EvidenceState: "discovered",
		}}}, nil
	}})
	registry := DefaultWithPatentSearchClient(client)
	result, err := registry.Execute(context.Background(), "patent_search", map[string]any{
		"operation":   "search",
		"query":       "electric lamp filament",
		"sources":     []any{"google"},
		"max_results": float64(5),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	output, ok := result.(patentsearch.Output)
	if !ok {
		t.Fatalf("result type = %T", result)
	}
	if len(output.Records) != 1 || output.Records[0].Source != "google_patents" {
		t.Fatalf("output records = %#v", output.Records)
	}
}
