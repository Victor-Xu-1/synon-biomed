package server

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"synon-go/internal/mcpdirectory"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/tools/mcpstdio"
)

func TestKernelMCPSourceEvidenceRequiresResolvedBundledReadOnlyTool(t *testing.T) {
	config := mcpstdio.ServerConfig{Name: "PubMed"}
	snapshot := &mcpdirectory.RuntimeConnector{ID: "bundled:pubmed", Name: "PubMed", Source: "bundled", Config: config}
	resolution := kernelMCPResolution{
		connector:       workspaceMCPRuntimeConnector{ID: snapshot.ID, Name: snapshot.Name, Source: "bundled", Enabled: true},
		runtimeSnapshot: snapshot, executable: &config,
		tool: mcpstdio.ToolProjection{
			Name: "mcp__pubmed__search_articles", ToolName: "search_articles", ReadOnlyHint: true,
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}},
		},
	}
	attestation, ok := kernelMCPResolutionSourceEvidenceAttestation(resolution)
	if !ok || attestation.Class == "" || attestation.ConnectorID != snapshot.ID ||
		attestation.ConnectorSource != "bundled" || !attestation.ReadOnlyHint || len(attestation.InputSchemaSHA256) != 64 {
		t.Fatalf("bundled evidence attestation=%#v ok=%t", attestation, ok)
	}
	for name, mutate := range map[string]func(*kernelMCPResolution){
		"custom connector": func(value *kernelMCPResolution) { value.connector.Source = "custom" },
		"directory connector": func(value *kernelMCPResolution) {
			value.connector.Source, value.runtimeSnapshot.Source = "directory", "directory"
		},
		"write capable tool":       func(value *kernelMCPResolution) { value.tool.ReadOnlyHint = false },
		"missing runtime snapshot": func(value *kernelMCPResolution) { value.runtimeSnapshot = nil },
		"non MCP tool name":        func(value *kernelMCPResolution) { value.tool.Name = "pubmed_search_articles" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := resolution
			snapshotCopy := *snapshot
			candidate.runtimeSnapshot = &snapshotCopy
			mutate(&candidate)
			if attestation, ok := kernelMCPResolutionSourceEvidenceAttestation(candidate); ok {
				t.Fatalf("untrusted resolution produced attestation %#v", attestation)
			}
		})
	}
}

func TestKernelMCPResolutionsEquivalentRequiresStableAuthorityAndSchema(t *testing.T) {
	config := mcpstdio.ServerConfig{Name: "PubMed", Env: map[string]string{"OPERON_CONTACT_EMAIL": "first@example.org"}}
	base := kernelMCPResolution{
		connector:       workspaceMCPRuntimeConnector{ID: "bundled:pubmed", Name: "PubMed", Source: "bundled", Enabled: true},
		runtimeSnapshot: &mcpdirectory.RuntimeConnector{ID: "bundled:pubmed", Name: "PubMed", Source: "bundled", DefinitionSHA256: "definition", Config: config},
		executable:      &config,
		tool:            mcpstdio.ToolProjection{Name: "mcp__pubmed__search_articles", ToolName: "search_articles", ReadOnlyHint: true, InputSchema: map[string]any{"type": "object"}},
	}
	if !kernelMCPResolutionsEquivalent(base, base) {
		t.Fatal("identical MCP resolutions were not equivalent")
	}
	changed := base
	changed.tool.InputSchema = map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}}
	if kernelMCPResolutionsEquivalent(base, changed) {
		t.Fatal("schema change was incorrectly treated as stable authority")
	}
	environmentRefresh := base
	refreshedConfig := config
	refreshedConfig.Env = map[string]string{"OPERON_CONTACT_EMAIL": "second@example.org"}
	environmentRefresh.runtimeSnapshot = &mcpdirectory.RuntimeConnector{
		ID: "bundled:pubmed", Name: "PubMed", Source: "bundled", DefinitionSHA256: "definition", Config: refreshedConfig,
	}
	environmentRefresh.executable = &refreshedConfig
	if !kernelMCPResolutionsEquivalent(base, environmentRefresh) {
		t.Fatal("bundled ephemeral environment refresh was treated as an authority change")
	}
	custom := environmentRefresh
	custom.runtimeSnapshot = &mcpdirectory.RuntimeConnector{
		ID: "custom:pubmed", Name: "PubMed", Source: "custom", DefinitionSHA256: "definition", Config: refreshedConfig,
	}
	custom.connector.Source = "custom"
	if kernelMCPResolutionsEquivalent(base, custom) {
		t.Fatal("custom connector authority change was treated as stable")
	}
}

func TestKernelMCPBundledReadOnlySchemaRefreshRevalidatesAtExecution(t *testing.T) {
	resolved := mcpstdio.ToolProjection{
		Name: "mcp__drug-regulatory__search_drug_labels", ToolName: "search_drug_labels",
		ReadOnlyHint: true, InputSchema: map[string]any{"type": "object"},
	}
	live := resolved
	live.InputSchema = map[string]any{
		"type": "object", "properties": map[string]any{
			"generic_name": map[string]any{"type": "string"},
		},
	}
	if !kernelMCPToolProjectionCompatibleForExecution("bundled", resolved, live) {
		t.Fatal("bundled read-only schema refresh was treated as an authority swap")
	}
	if kernelMCPToolProjectionCompatibleForExecution("custom", resolved, live) {
		t.Fatal("custom schema change was treated as a stable authority")
	}
	live.ReadOnlyHint = false
	if kernelMCPToolProjectionCompatibleForExecution("bundled", resolved, live) {
		t.Fatal("read-only to write-capable transition was treated as safe")
	}
	live = resolved
	live.Name = "mcp__drug-regulatory__other"
	if kernelMCPToolProjectionCompatibleForExecution("bundled", resolved, live) {
		t.Fatal("tool-name change was treated as a stable authority")
	}
}

func TestWorkspaceMCPSourceEvidenceRequiresBundledReadOnlyTool(t *testing.T) {
	connector := workspaceMCPRuntimeConnector{ID: "bundled:pubmed", Name: "PubMed", Source: "bundled", Enabled: true}
	tool := mcpstdio.ToolProjection{
		Name: "mcp__pubmed__search_articles", ToolName: "search_articles", ReadOnlyHint: true,
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}},
	}
	attestation, ok := workspaceMCPSourceEvidenceAttestationForTool(connector, tool)
	if !ok || attestation.Schema != workspaceMCPSourceEvidenceSchemaV1 ||
		attestation.EvidenceClass != workspace.KernelMCPEvidenceClassBundledReadOnly || attestation.ConnectorID != connector.ID ||
		attestation.ConnectorSource != "bundled" || !attestation.ReadOnlyHint || len(attestation.InputSchemaSHA256) != 64 {
		t.Fatalf("workspace MCP evidence attestation=%#v ok=%t", attestation, ok)
	}
	for name, mutate := range map[string]func(*workspaceMCPRuntimeConnector, *mcpstdio.ToolProjection){
		"custom connector": func(connector *workspaceMCPRuntimeConnector, _ *mcpstdio.ToolProjection) { connector.Source = "custom" },
		"directory connector": func(connector *workspaceMCPRuntimeConnector, _ *mcpstdio.ToolProjection) {
			connector.Source = "directory"
		},
		"write capable tool": func(_ *workspaceMCPRuntimeConnector, tool *mcpstdio.ToolProjection) { tool.ReadOnlyHint = false },
		"missing schema":     func(_ *workspaceMCPRuntimeConnector, tool *mcpstdio.ToolProjection) { tool.InputSchema = nil },
	} {
		t.Run(name, func(t *testing.T) {
			candidateConnector, candidateTool := connector, tool
			mutate(&candidateConnector, &candidateTool)
			if candidate, ok := workspaceMCPSourceEvidenceAttestationForTool(candidateConnector, candidateTool); ok {
				t.Fatalf("untrusted workspace MCP tool produced attestation %#v", candidate)
			}
		})
	}
}

func TestKernelMCPSourceEvidenceRequiresStructuredResult(t *testing.T) {
	for name, test := range map[string]struct {
		value any
		want  bool
	}{
		"JSON object string": {value: `{"pmids":["42486940"]}`, want: true},
		"structured map":     {value: map[string]any{"pmids": []any{"42486940"}}, want: true},
		"plain prose":        {value: "PMID 42486940", want: false},
		"JSON scalar":        {value: `"PMID 42486940"`, want: false},
		"nil":                {value: nil, want: false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := kernelMCPStructuredEvidenceResult(test.value); got != test.want {
				t.Fatalf("structured result=%t want=%t value=%#v", got, test.want, test.value)
			}
		})
	}
}

func TestKernelMCPDurableEvidenceUpdatesLiveScientificRun(t *testing.T) {
	run := &sessionRunnerChatRun{}
	ctx := context.WithValue(context.Background(), transcriptRunnerChatRunContextKey{}, run)
	bindKernelMCPTrustedScientificEvidence(ctx, kernelMCPSourceEvidenceAttestation{
		Class:             workspace.KernelMCPEvidenceClassBundledReadOnly,
		ConnectorID:       "bundled:pubmed",
		ConnectorSource:   "bundled",
		InputSchemaSHA256: strings.Repeat("a", 64),
		ReadOnlyHint:      true,
	}, "mcp__literature__openalex_get_work", map[string]any{
		"work_id": "https://doi.org/10.1000/example",
	}, map[string]any{
		"doi": "10.1000/example", "abstract_inverted_index": map[string]any{"measured": []any{0}},
	})
	if signals := run.trustedScientificReviewSignalsSnapshot(); !sessionRunnerHasAuthoritativeSourceEvidence(signals) {
		t.Fatalf("durable kernel MCP evidence was not bound to the live run: %#v", signals)
	} else if !stringSliceContains(signals, "evidence-record:publication:doi:10.1000/example") {
		t.Fatalf("exact MCP publication record was lost at the REPL boundary: %#v", signals)
	}

	untrusted := &sessionRunnerChatRun{}
	untrustedCtx := context.WithValue(context.Background(), transcriptRunnerChatRunContextKey{}, untrusted)
	bindKernelMCPTrustedScientificEvidence(untrustedCtx, kernelMCPSourceEvidenceAttestation{
		Class:             workspace.KernelMCPEvidenceClassBundledReadOnly,
		ConnectorID:       "custom:pubmed",
		ConnectorSource:   "custom",
		InputSchemaSHA256: strings.Repeat("b", 64),
		ReadOnlyHint:      true,
	}, "mcp__literature__openalex_get_work", map[string]any{
		"work_id": "https://doi.org/10.1000/untrusted",
	}, map[string]any{
		"doi": "10.1000/untrusted", "abstract": "untrusted result",
	})
	if signals := untrusted.trustedScientificReviewSignalsSnapshot(); len(signals) != 0 {
		t.Fatalf("untrusted kernel MCP evidence was bound to the live run: %#v", signals)
	}
}

func TestKernelMCPFailureDoesNotCreateTrustedSourceEvidence(t *testing.T) {
	run := &sessionRunnerChatRun{}
	ctx := context.WithValue(context.Background(), transcriptRunnerChatRunContextKey{}, run)
	bindKernelMCPTrustedScientificEvidence(ctx, kernelMCPSourceEvidenceAttestation{
		Class:             workspace.KernelMCPEvidenceClassBundledReadOnly,
		ConnectorID:       "bundled:synon-research",
		ConnectorSource:   "bundled",
		InputSchemaSHA256: strings.Repeat("a", 64),
		ReadOnlyHint:      true,
	}, "mcp__synon-research__life_science_sources", map[string]any{"query": "target"}, `{
		"ok": false,
		"error": {"code": "package_unavailable", "message": "missing package"}
	}`)
	if signals := run.trustedScientificReviewSignalsSnapshot(); len(signals) != 0 {
		t.Fatalf("failed MCP result created trusted source evidence: %#v", signals)
	}
}

func TestKernelMCPDurableEvidenceUpdatesDetachedActiveSessionRun(t *testing.T) {
	run := &sessionRunnerChatRun{}
	server := &Server{sessionRuns: map[string]*activeSessionRun{
		"frame-detached": {chatRun: run},
	}}
	server.bindKernelMCPTrustedScientificEvidenceForSession(
		context.Background(),
		"frame-detached",
		kernelMCPSourceEvidenceAttestation{
			Class: workspace.KernelMCPEvidenceClassBundledReadOnly, ConnectorID: "bundled:clinical-trials",
			ConnectorSource: "bundled", InputSchemaSHA256: strings.Repeat("a", 64), ReadOnlyHint: true,
		},
		"mcp__clinical-trials__get_trial_details",
		map[string]any{"nct_id": "NCT04616014"},
		`{"found":true,"trial":{"nct_id":"NCT04616014","brief_summary":"Measured exposure and safety.","primary_outcomes":[{"measure":"Exposure"}]}}`,
	)
	if signals := run.trustedScientificReviewSignalsSnapshot(); !stringSliceContains(signals, "evidence-record:trial:nct04616014") {
		t.Fatalf("detached MCP trial record was not bound to the active logical task: %#v", signals)
	}
}

func TestKernelMCPInputSchemaValidation(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string", "minLength": float64(2)},
			"limit": map[string]any{"type": "integer", "minimum": float64(1), "maximum": float64(10)},
			"tags":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "uniqueItems": true},
			"mode":  map[string]any{"$ref": "#/$defs/mode"},
		},
		"required":             []any{"query"},
		"additionalProperties": false,
		"$defs": map[string]any{
			"mode": map[string]any{"type": "string", "enum": []any{"brief", "full"}},
		},
	}
	if err := validateKernelMCPInput(schema, map[string]any{
		"query": "NEK7", "limit": 2, "tags": []any{"gene", "inflammasome"}, "mode": "brief",
	}); err != nil {
		t.Fatalf("valid input error=%v", err)
	}
	for name, input := range map[string]map[string]any{
		"missing":         {"limit": 2},
		"short":           {"query": "N"},
		"integer":         {"query": "NEK7", "limit": 2.5},
		"minimum":         {"query": "NEK7", "limit": 0},
		"maximum":         {"query": "NEK7", "limit": 11},
		"duplicate array": {"query": "NEK7", "tags": []any{"gene", "gene"}},
		"enum":            {"query": "NEK7", "mode": "unknown"},
		"extra":           {"query": "NEK7", "unexpected": true},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateKernelMCPInput(schema, input); err == nil {
				t.Fatalf("input unexpectedly passed: %#v", input)
			}
		})
	}
}

func TestKernelMCPNormalizeSchemaEnumAliasesUsesOnlyUniqueAdvertisedValues(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"sort": map[string]any{"type": "string", "enum": []any{"relevance", "pub_date"}},
			"study_type": map[string]any{"anyOf": []any{
				map[string]any{"type": "string", "enum": []any{"INTERVENTIONAL", "OBSERVATIONAL"}},
				map[string]any{"type": "null"},
			}},
			"phases": map[string]any{"type": "array", "items": map[string]any{
				"type": "string", "enum": []any{"PHASE1", "PHASE2"},
			}},
			"ambiguous": map[string]any{"type": "string", "enum": []any{"AB", "A_B"}},
		},
	}
	input := map[string]any{
		"sort": "pubdate", "study_type": "Interventional",
		"phases": []any{"phase1", "PHASE2"}, "ambiguous": "ab",
	}
	got := kernelMCPNormalizeSchemaEnumAliases(input, schema)
	want := map[string]any{
		"sort": "pub_date", "study_type": "INTERVENTIONAL",
		"phases": []any{"PHASE1", "PHASE2"}, "ambiguous": "ab",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized=%#v want=%#v", got, want)
	}
	if input["sort"] != "pubdate" || !reflect.DeepEqual(input["phases"], []any{"phase1", "PHASE2"}) {
		t.Fatalf("source input mutated: %#v", input)
	}
}

func TestNormalizeAgentRuntimeMCPArgumentsSharesClinicalTrialsLimitContract(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"condition": map[string]any{"type": "string"},
		"page_size": map[string]any{"type": "integer"},
		"max_rows":  map[string]any{"type": "integer"},
	}}
	input := map[string]any{"condition": "oral peptide", "page_size": 20, "max_rows": 50}
	got := normalizeAgentRuntimeMCPArguments("mcp__clinical-trials__search_trials", schema, input)
	if got["page_size"] != 20 {
		t.Fatalf("page_size=%#v", got)
	}
	if _, found := got["max_rows"]; found {
		t.Fatalf("legacy conflicting max_rows survived normalization=%#v", got)
	}
	if _, found := input["max_rows"]; !found {
		t.Fatalf("source input mutated=%#v", input)
	}
}

func TestKernelMCPInputSchemaRejectsUndeclaredRootArgumentsByDefault(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"uniprot_accession":       map[string]any{"type": "string"},
			"include_computed_models": map[string]any{"type": "boolean"},
			"max_rows":                map[string]any{"type": "integer"},
		},
	}
	if err := validateKernelMCPInput(schema, map[string]any{
		"uniprot_accession": "Q96SW2", "include_computed_models": false, "max_rows": 1000,
	}); err != nil {
		t.Fatalf("declared arguments failed: %v", err)
	}
	if err := validateKernelMCPInput(schema, map[string]any{
		"uniprot_accession": "Q96SW2", "include_computed_models": false,
		"max_rows": 1000, "ligand_required": true,
	}); err == nil {
		t.Fatal("undeclared scientific filter was silently accepted")
	}
	if _, declared := schema["additionalProperties"]; declared {
		t.Fatal("validator mutated the connector-owned source schema")
	}
}

func TestKernelMCPInputSchemaUsesCompleteECMAScriptValidation(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string", "pattern": `^(?=NEK)[A-Z0-9]+$`},
			"mode":  map[string]any{"type": "string"},
			"limit": map[string]any{"type": "integer", "multipleOf": 2},
		},
		"required":             []any{"query"},
		"not":                  map[string]any{"required": []any{"forbidden"}},
		"if":                   map[string]any{"properties": map[string]any{"mode": map[string]any{"const": "bounded"}}, "required": []any{"mode"}},
		"then":                 map[string]any{"required": []any{"limit"}},
		"additionalProperties": false,
	}
	if err := validateKernelMCPInput(schema, map[string]any{"query": "NEK7", "mode": "bounded", "limit": 4}); err != nil {
		t.Fatalf("valid complete-schema input error=%v", err)
	}
	for name, input := range map[string]map[string]any{
		"not":        {"query": "NEK7", "forbidden": true},
		"then":       {"query": "NEK7", "mode": "bounded"},
		"multipleOf": {"query": "NEK7", "limit": 3},
		"lookahead":  {"query": "ABC7"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateKernelMCPInput(schema, input); err == nil {
				t.Fatalf("input unexpectedly passed: %#v", input)
			}
		})
	}
	if err := validateKernelMCPInput(map[string]any{"type": "object", "required": "query"}, map[string]any{}); err != nil {
		t.Fatalf("malformed schema did not follow Claude fail-open semantics: %v", err)
	}
}

func TestKernelMCPInputSchemaUsesDraft7LocalReferencesAndExactNumbers(t *testing.T) {
	if err := validateKernelMCPInput(map[string]any{}, map[string]any{"anything": true}); err != nil {
		t.Fatalf("empty schema rejected valid input: %v", err)
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tuple": map[string]any{
				"type": "array", "items": []any{map[string]any{"type": "string"}, map[string]any{"type": "integer"}},
				"additionalItems": false,
			},
			"identifier": map[string]any{"$ref": "#/definitions/identifier"},
			"amount":     map[string]any{"type": "integer", "minimum": json.Number("9007199254740993")},
		},
		"propertyNames": map[string]any{"pattern": "^[a-z]+$"},
		"dependencies":  map[string]any{"amount": []any{"identifier"}},
		"definitions": map[string]any{
			"identifier": map[string]any{"type": "string", "pattern": `^([A-Z]+)-\1$`},
		},
		"additionalProperties": false,
	}
	if err := validateKernelMCPInput(schema, map[string]any{
		"tuple": []any{"answer", json.Number("42")}, "identifier": "NEK-NEK", "amount": json.Number("9007199254740993"),
	}); err != nil {
		t.Fatalf("draft7 exact input error=%v", err)
	}
	for name, input := range map[string]map[string]any{
		"tuple extra":   {"tuple": []any{"answer", json.Number("42"), true}},
		"backreference": {"identifier": "NEK-NLRP3"},
		"dependency":    {"amount": json.Number("9007199254740993")},
		"exact number":  {"identifier": "NEK-NEK", "amount": json.Number("9007199254740992")},
		"property name": {"Bad": true},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateKernelMCPInput(schema, input); err == nil {
				t.Fatalf("input unexpectedly passed: %#v", input)
			}
		})
	}
	for name, invalidSchema := range map[string]map[string]any{
		"external reference":  {"$ref": "https://example.invalid/schema.json"},
		"unsupported dialect": {"$schema": "https://example.invalid/schema-draft"},
	} {
		t.Run(name, func(t *testing.T) {
			validator, err := compileKernelMCPInputValidator(context.Background(), invalidSchema)
			if err != nil {
				t.Fatalf("schema compile did not fail open: %v", err)
			}
			if err := validator.Validate(map[string]any{"unvalidated": true}); err != nil {
				t.Fatalf("fail-open validator rejected input: %v", err)
			}
		})
	}
}

func TestKernelMCPMissingSchemaUsesClaudeObjectDefault(t *testing.T) {
	if err := validateKernelMCPInput(nil, map[string]any{"anything": true}); err != nil {
		t.Fatalf("default object schema rejected object input: %v", err)
	}
}
func TestCanonicalKernelMCPServerNameNormalizesBundledAlias(t *testing.T) {
	cases := map[string]string{
		"pubmed": "pubmed", "bundled:pubmed": "pubmed", " BUNDLED:pubmed ": "pubmed", "custom:remote": "custom:remote",
	}
	for input, want := range cases {
		t.Run(input, func(t *testing.T) {
			if got := canonicalKernelMCPServerName(input); got != want {
				t.Fatalf("canonical server name=%q want=%q", got, want)
			}
		})
	}
}
