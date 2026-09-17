package mcpstdio

import (
	"reflect"
	"testing"
)

func TestApplyDiscoveryDefaultsUsesBroadSchemaBoundedPage(t *testing.T) {
	tool := ToolProjection{
		ToolName: "search_trials",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"query":     map[string]any{"type": "string"},
			"page_size": map[string]any{"type": "integer", "minimum": float64(1), "maximum": float64(100)},
		}},
	}
	original := map[string]any{"query": "melanoma"}
	got := applyDiscoveryDefaults(tool, original)
	if got["page_size"] != 100 || len(original) != 1 {
		t.Fatalf("defaulted=%#v original=%#v", got, original)
	}

	tool.InputSchema["properties"].(map[string]any)["page_size"].(map[string]any)["maximum"] = float64(25)
	if got := applyDiscoveryDefaults(tool, original); got["page_size"] != 25 {
		t.Fatalf("schema-bounded default=%#v", got)
	}
}

func TestApplyDiscoveryDefaultsRecognizesPaginationSchemaWithoutSearchVerb(t *testing.T) {
	tool := ToolProjection{ToolName: "articles", InputSchema: map[string]any{
		"type": "object", "properties": map[string]any{
			"query":                map[string]any{"type": "string"},
			"max_records_returned": map[string]any{"type": "integer", "maximum": float64(500)},
		},
	}}
	got := applyDiscoveryDefaults(tool, map[string]any{"query": "KRAS"})
	if got["max_records_returned"] != 100 {
		t.Fatalf("schema-driven discovery default = %#v", got)
	}
}

func TestApplyDiscoveryDefaultsDoesNotTreatAmbiguousSizeAsPagination(t *testing.T) {
	tool := ToolProjection{ToolName: "render_molecule", InputSchema: map[string]any{
		"type": "object", "properties": map[string]any{
			"smiles": map[string]any{"type": "string"},
			"size":   map[string]any{"type": "integer", "maximum": float64(2000)},
		},
	}}
	input := map[string]any{"smiles": "CCO"}
	got := applyDiscoveryDefaults(tool, input)
	if _, exists := got["size"]; exists {
		t.Fatalf("ambiguous render size must not be defaulted as pagination: %#v", got)
	}
}

func TestApplyDiscoveryDefaultsPreservesExplicitChoiceAndNonDiscoveryInput(t *testing.T) {
	tool := ToolProjection{
		ToolName: "search_articles",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"max_results": map[string]any{"type": "integer", "minimum": float64(1), "maximum": float64(200)},
		}},
	}
	explicit := map[string]any{"max_results": 7}
	if got := applyDiscoveryDefaults(tool, explicit); !reflect.DeepEqual(got, explicit) {
		t.Fatalf("explicit input changed: %#v", got)
	}
	tool.ToolName = "get_article"
	implicit := map[string]any{"pmid": "42486940"}
	if got := applyDiscoveryDefaults(tool, implicit); !reflect.DeepEqual(got, implicit) {
		t.Fatalf("non-discovery input changed: %#v", got)
	}
}

func TestApplyDiscoveryDefaultsSupportsCamelCaseAndRecordCaps(t *testing.T) {
	for _, tc := range []struct {
		name, method, parameter string
	}{
		{name: "camel case", method: "find_resources", parameter: "pageSize"},
		{name: "record cap", method: "openalex_search_authors", parameter: "max_records"},
		{name: "query alias", method: "articles", parameter: "resultsPerPage"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool := ToolProjection{
				ToolName: tc.method,
				InputSchema: map[string]any{"type": "object", "properties": map[string]any{
					tc.parameter: map[string]any{"type": "integer", "minimum": float64(1), "maximum": float64(500)},
				}},
			}
			queryField := "query"
			if tc.name == "query alias" {
				queryField = "searchQuery"
			}
			tool.InputSchema["properties"].(map[string]any)[queryField] = map[string]any{"type": "string"}
			if got := applyDiscoveryDefaults(tool, map[string]any{queryField: "David Liu"}); got[tc.parameter] != 100 {
				t.Fatalf("default=%#v", got)
			}
		})
	}
}

func TestPrepareDiscoveryInspectorMutatesOnlyPrivateTransportInput(t *testing.T) {
	original := map[string]any{"query": "KRAS G12C"}
	inspected := false
	prepared, inspect := prepareDiscoveryInspector(original, func(tool ToolProjection) error {
		inspected = true
		return nil
	})
	tool := ToolProjection{
		ToolName: "search_trials",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"page_size": map[string]any{"type": "integer", "minimum": float64(1), "maximum": float64(100)},
		}},
	}
	if err := inspect(tool); err != nil {
		t.Fatal(err)
	}
	if !inspected || prepared["page_size"] != 100 {
		t.Fatalf("inspected=%v prepared=%#v", inspected, prepared)
	}
	if _, changed := original["page_size"]; changed {
		t.Fatalf("caller input was mutated: %#v", original)
	}
}
