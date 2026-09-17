package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"synon-go/internal/agentruntime"
)

func TestAgentRuntimeGatewayNormalizesObservedChEMBLArgumentsAgainstBundledSchemas(t *testing.T) {
	schemas := bundledChEMBLToolSchemas(t)
	gateway := serverAgentRuntimeToolGateway{
		toolSchemas:     schemas,
		toolValidators:  agentRuntimeToolValidators(schemas),
		hasToolSnapshot: true,
	}

	tests := []struct {
		name     string
		raw      string
		nilKeys  []string
		numbers  map[string]float64
		preserve map[string]string
	}{
		{
			name:    "mcp__chembl__get_bioactivity",
			raw:     `{"activity_type":"IC50","limit":50,"max_pchembl":"None","max_value":"1000","min_pchembl":"7","min_value":"None","molecule_chembl_id":"CHEMBL4535757","target_chembl_id":"None","unit":"nM"}`,
			nilKeys: []string{"max_pchembl", "min_value", "target_chembl_id"},
			numbers: map[string]float64{"max_value": 1000, "min_pchembl": 7},
		},
		{
			name:    "mcp__chembl__get_mechanism",
			raw:     `{"action_type":"None","limit":20,"molecule_chembl_id":"CHEMBL3353410","target_chembl_id":"None"}`,
			nilKeys: []string{"action_type", "target_chembl_id"},
		},
		{
			name:     "mcp__chembl__compound_search",
			raw:      `{"chembl_id":null,"limit":10,"max_phase":null,"name":"divarasib","similarity_threshold":null,"smiles":"None"}`,
			nilKeys:  []string{"chembl_id", "max_phase", "similarity_threshold", "smiles"},
			preserve: map[string]string{"name": "divarasib"},
		},
		{
			name:     "mcp__chembl__target_search",
			raw:      `{"gene_symbol":"KRAS","limit":5,"organism":"Homo sapiens","target_chembl_id":"None","target_name":"None","target_type":"SINGLE PROTEIN"}`,
			nilKeys:  []string{"target_chembl_id", "target_name"},
			preserve: map[string]string{"gene_symbol": "KRAS", "organism": "Homo sapiens"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := map[string]any{}
			if err := json.Unmarshal([]byte(test.raw), &input); err != nil {
				t.Fatal(err)
			}
			normalized := gateway.normalizeAdmittedToolArguments(test.name, input)
			for _, key := range test.nilKeys {
				if normalized[key] != nil {
					t.Fatalf("%s = %#v, want nil; normalized=%#v", key, normalized[key], normalized)
				}
			}
			for key, want := range test.numbers {
				got, ok := normalized[key].(float64)
				if !ok || got != want {
					t.Fatalf("%s = %#v, want %v", key, normalized[key], want)
				}
			}
			for key, want := range test.preserve {
				if normalized[key] != want {
					t.Fatalf("%s = %#v, want %q", key, normalized[key], want)
				}
			}
			if invalid := gateway.validateAdmittedToolArguments(test.name, normalized); invalid != nil {
				t.Fatalf("normalized observed arguments were rejected: %#v", invalid)
			}
		})
	}
}

func TestBundledChEMBLCompoundSearchAdmitsIDWithoutSyntheticName(t *testing.T) {
	schemas := bundledChEMBLToolSchemas(t)
	gateway := serverAgentRuntimeToolGateway{
		toolSchemas:     schemas,
		toolValidators:  agentRuntimeToolValidators(schemas),
		hasToolSnapshot: true,
	}
	input := map[string]any{"chembl_id": "CHEMBL25", "name": "None"}
	normalized := gateway.normalizeAdmittedToolArguments("mcp__chembl__compound_search", input)
	if normalized["name"] != "None" {
		t.Fatalf("non-nullable free-text name was rewritten: %#v", normalized)
	}
	delete(normalized, "name")
	if invalid := gateway.validateAdmittedToolArguments("mcp__chembl__compound_search", normalized); invalid != nil {
		t.Fatalf("ID-only compound search was rejected: %#v", invalid)
	}
}

func bundledChEMBLToolSchemas(t *testing.T) []agentruntime.ToolSchema {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	path := filepath.Join(filepath.Dir(sourceFile), "..", "..", "assets", "optional", "mcp-servers", "bio-tools", "lib", "mcp_chembl", "schemas.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Tools []struct {
			Name         string         `json:"name"`
			Description  string         `json:"description"`
			InputSchema  map[string]any `json:"input_schema"`
			OutputSchema map[string]any `json:"output_schema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	schemas := make([]agentruntime.ToolSchema, 0, len(document.Tools))
	for _, tool := range document.Tools {
		schemas = append(schemas, agentruntime.ToolSchema{
			Name: "mcp__chembl__" + tool.Name, Description: tool.Description,
			Parameters: tool.InputSchema, OutputSchema: tool.OutputSchema,
		})
	}
	return schemas
}
