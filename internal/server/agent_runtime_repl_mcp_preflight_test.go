package server

import (
	"encoding/json"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
)

func literalMCPPreflightSchemas() []agentruntime.ToolSchema {
	return []agentruntime.ToolSchema{
		{
			Name: "mcp__pubmed__search_articles",
			Parameters: map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"query": map[string]any{"type": "string"}, "max_results": map[string]any{"type": "integer"},
				},
				"required": []string{"query"},
			},
			OutputSchema: map[string]any{
				"type": "object", "properties": map[string]any{
					"pmids": map[string]any{"type": "array"}, "returned_count": map[string]any{"type": "number"},
				},
			},
		},
		{
			Name: "mcp__pubmed__get_article_metadata",
			Parameters: map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{"pmids": map[string]any{"type": "array"}},
				"required":   []string{"pmids"},
			},
			OutputSchema: map[string]any{
				"type": "object", "properties": map[string]any{
					"articles": map[string]any{"type": "array"}, "count": map[string]any{"type": "number"},
				},
			},
		},
		{
			Name: "mcp__structures-interactions__pdb_search_structures",
			Parameters: map[string]any{
				"type": "object", "properties": map[string]any{"uniprot_accession": map[string]any{"type": "string"}},
			},
			OutputSchema: map[string]any{
				"type": "object", "properties": map[string]any{
					"records": map[string]any{
						"type": "array", "items": map[string]any{
							"type": "object", "properties": map[string]any{
								"pdb_id": map[string]any{"type": "string"}, "score": map[string]any{"type": "number"},
							},
						},
					},
				},
			},
		},
		{
			Name: "mcp__structures-interactions__pdb_get_structures",
			Parameters: map[string]any{
				"type": "object", "properties": map[string]any{"pdb_ids": map[string]any{"type": "array"}},
			},
			OutputSchema: map[string]any{
				"type": "object", "properties": map[string]any{
					"records": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
				},
			},
		},
	}
}

func TestREPLMCPPreflightRejectsLiteralInputSchemaDriftBeforeExecution(t *testing.T) {
	value := agentRuntimeREPLMCPContractPreflight("repl", map[string]any{
		"code": `detail = host.mcp("pubmed", "get_article_metadata", {"pmid": pmid})`,
	}, literalMCPPreflightSchemas())
	if value == nil || value["status"] != "mcp_schema_preflight_required" || value["executed"] != false ||
		!strings.Contains(stringValue(value["recovery"]), "pmids") ||
		!strings.Contains(stringValue(value["recovery"]), "pmid") {
		t.Fatalf("literal MCP input preflight=%#v", value)
	}
	if value["required_skill"] != "mcp-pubmed" || value["required_filter"] != "get_article_metadata" {
		t.Fatalf("literal MCP connector recovery=%#v", value)
	}
}

func TestREPLMCPPreflightValidatesImportedMCPCallAliasBeforeExecution(t *testing.T) {
	schemas := append(literalMCPPreflightSchemas(), agentruntime.ToolSchema{
		Name: "mcp__clinical-trials__search_trials",
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"condition": map[string]any{"type": "string"},
				"status":    map[string]any{"type": "null"},
				"max_rows":  map[string]any{"type": "integer"},
			},
		},
	})
	value := agentRuntimeREPLMCPContractPreflight("repl", map[string]any{
		"code": `from host import mcp
result = mcp.call("clinical-trials", "search_trials", {
    "condition": "Type 2 Diabetes",
    "from_date": "2021-01-01",
    "status": "ALL",
    "max_rows": 50
})`,
	}, schemas)
	if value == nil || value["status"] != "mcp_schema_preflight_required" || value["executed"] != false ||
		!strings.Contains(stringValue(value["recovery"]), "from_date") ||
		!strings.Contains(stringValue(value["recovery"]), "status=string (expected null)") {
		t.Fatalf("imported mcp.call preflight=%#v", value)
	}
}

func TestREPLMCPPreflightValidatesUnionTypesFromLiveConnectorSchema(t *testing.T) {
	schemas := append(literalMCPPreflightSchemas(), agentruntime.ToolSchema{
		Name: "mcp__clinical-trials__search_trials",
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"condition": map[string]any{"type": "string"},
				"status": map[string]any{"anyOf": []any{
					map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					map[string]any{"type": "null"},
				}},
				"max_rows": map[string]any{"type": "integer"},
			},
		},
	})
	invalid := agentRuntimeREPLMCPContractPreflight("repl", map[string]any{
		"code": `result = host.mcp("clinical-trials", "search_trials",
    condition="Hypercholesterolemia", status="ALL", max_rows=50)`,
	}, schemas)
	if invalid == nil || invalid["status"] != "mcp_schema_preflight_required" ||
		!strings.Contains(stringValue(invalid["recovery"]), "status=string") ||
		!strings.Contains(stringValue(invalid["recovery"]), "array|null") {
		t.Fatalf("union input preflight=%#v", invalid)
	}
	valid := agentRuntimeREPLMCPContractPreflight("repl", map[string]any{
		"code": `result = host.mcp("clinical-trials", "search_trials",
    condition="Hypercholesterolemia", status=["RECRUITING"], max_rows=50)`,
	}, schemas)
	if valid != nil {
		t.Fatalf("valid union input was rejected: %#v", valid)
	}
}

func TestREPLMCPPreflightValidatesLiteralEnumsFromLiveConnectorSchema(t *testing.T) {
	schemas := append(literalMCPPreflightSchemas(), agentruntime.ToolSchema{
		Name: "mcp__pubmed-live__search_articles",
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"query":    map[string]any{"type": "string"},
				"datetype": map[string]any{"type": "string", "enum": []any{"pdat", "edat", "mdat"}},
			},
			"required": []string{"query"},
		},
	})
	invalid := agentRuntimeREPLMCPContractPreflight("repl", map[string]any{
		"code": `result = host.mcp("pubmed-live", "search_articles",
    query="oral small molecule PCSK9 inhibitor", datetype="ddat")`,
	}, schemas)
	if invalid == nil || invalid["status"] != "mcp_schema_preflight_required" ||
		!strings.Contains(stringValue(invalid["recovery"]), `datetype="ddat"`) ||
		!strings.Contains(stringValue(invalid["recovery"]), `"edat"`) {
		t.Fatalf("enum input preflight=%#v", invalid)
	}
	valid := agentRuntimeREPLMCPContractPreflight("repl", map[string]any{
		"code": `result = host.mcp("pubmed-live", "search_articles",
    query="oral small molecule PCSK9 inhibitor", datetype="edat")`,
	}, schemas)
	if valid != nil {
		t.Fatalf("valid enum input was rejected: %#v", valid)
	}
}

func TestREPLMCPPreflightDoesNotInviteGuessedQueryFieldsForNonSearchMethod(t *testing.T) {
	schemas := append(literalMCPPreflightSchemas(), agentruntime.ToolSchema{
		Name: "mcp__biorxiv__search_preprints",
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"category": map[string]any{"type": "string"},
				"limit":    map[string]any{"type": "integer"},
			},
		},
	})
	value := agentRuntimeREPLMCPContractPreflight("repl", map[string]any{
		"code": `result = host.mcp("biorxiv", "search_preprints", {"query": "KRAS G12C", "limit": 5})`,
	}, schemas)
	if value == nil || !strings.Contains(stringValue(value["recovery"]), "does not accept a free-text search query") ||
		!strings.Contains(stringValue(value["recovery"]), `host.mcp.list_methods("biorxiv")`) ||
		strings.Contains(stringValue(value["recovery"]), "add required keys") {
		t.Fatalf("non-search MCP recovery=%#v", value)
	}
}

func TestREPLMCPPreflightRejectsUndeclaredOutputWrapperAndDirectObjectIteration(t *testing.T) {
	tests := []string{
		`result = mcp("pubmed", "search_articles", {"query": "drug", "max_results": 20})
records = result.get("result", {}).get("records", [])`,
		`result = mcp("pubmed", "search_articles", {"query": "drug", "max_results": 20})
records = result["result"]["records"]`,
		`details = mcp("pubmed", "get_article_metadata", {"pmids": pmids})
for article in details:
    print(article)`,
		`search_results = host.mcp("pubmed", "search_articles", query="starting dose")
for article in search_results[:5]:
    print(article)`,
	}
	for _, code := range tests {
		value := agentRuntimeREPLMCPContractPreflight("repl", map[string]any{"code": code}, literalMCPPreflightSchemas())
		if value == nil || value["status"] != "mcp_schema_preflight_required" || value["executed"] != false ||
			!strings.Contains(stringValue(value["message"]), "output") {
			t.Fatalf("literal MCP output preflight for %q = %#v", code, value)
		}
	}
}

func TestREPLMCPPreflightRequiresOneShapeEstablishingCallPerCell(t *testing.T) {
	value := agentRuntimeREPLMCPContractPreflight("repl", map[string]any{
		"code": `search = mcp("pubmed", "search_articles", {"query": "drug"})
details = mcp("pubmed", "get_article_metadata", {"pmids": ["1"]})`,
	}, literalMCPPreflightSchemas())
	if value == nil || !strings.Contains(stringValue(value["message"]), "multiple host.mcp calls") {
		t.Fatalf("multi-call preflight=%#v", value)
	}

	allowed := agentRuntimeREPLMCPContractPreflight("repl", map[string]any{
		"code": `details = mcp("pubmed", "get_article_metadata", {"pmids": pmids})
with open("raw.json", "w") as handle:
    json.dump(details, handle)
print(type(details), list(details))`,
	}, literalMCPPreflightSchemas())
	if allowed != nil {
		t.Fatalf("single shape-establishing call was blocked: %#v", allowed)
	}
}

func TestREPLMCPPreflightRejectsUndeclaredNestedRecordFieldBeforeExecution(t *testing.T) {
	value := agentRuntimeREPLMCPContractPreflight("repl", map[string]any{
		"code": `pdb_search = host.mcp("structures-interactions", "pdb_search_structures", uniprot_accession="Q96SW2")
for i, rec in enumerate(pdb_search.get("records", [])[:10]):
    print(rec["pdb_id"], rec["initial_release_date"], rec.get("resolution_angstrom"))`,
	}, literalMCPPreflightSchemas())
	if value == nil || value["status"] != "mcp_schema_preflight_required" ||
		!strings.Contains(stringValue(value["recovery"]), "initial_release_date") ||
		!strings.Contains(stringValue(value["recovery"]), "pdb_id, score") {
		t.Fatalf("nested record output preflight=%#v", value)
	}
}

func TestREPLMCPPreflightRecognizesEnumerateAndOptionalRecordFields(t *testing.T) {
	schemas := []agentruntime.ToolSchema{{
		Name: "mcp__pubmed__get_article_metadata",
		Parameters: map[string]any{
			"type": "object", "properties": map[string]any{"pmids": map[string]any{"type": "array"}},
			"required": []string{"pmids"},
		},
		OutputSchema: map[string]any{
			"type": "object", "properties": map[string]any{
				"articles": map[string]any{"type": "array", "items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"identifiers": map[string]any{"type": "object"},
						"title":       map[string]any{"type": "string"},
						"doi":         map[string]any{"type": "string"},
					},
					"required": []string{"identifiers", "title"},
				}},
			},
		},
	}}
	code := `result = host.mcp("pubmed", "get_article_metadata", {"pmids": pmids})
for i, article in enumerate(result['articles']):
    print(f"{i+1}. PMID {article['pmid']}: {article['title']}")`
	value := agentRuntimeREPLMCPContractPreflight("repl", map[string]any{"code": code}, schemas)
	if value == nil || value["status"] != "mcp_schema_preflight_required" ||
		!strings.Contains(stringValue(value["recovery"]), "doi, identifiers, title") ||
		!strings.Contains(stringValue(value["recovery"]), "new MCP detail call") {
		t.Fatalf("enumerate output preflight=%#v", value)
	}

	safe := `result = host.mcp("pubmed", "get_article_metadata", {"pmids": pmids})
for article in result.get("articles", []):
    print(article.get("identifiers"), article.get("doi"), article["title"])`
	if value := agentRuntimeREPLMCPContractPreflight("repl", map[string]any{"code": safe}, schemas); value != nil {
		t.Fatalf("safe optional output access was blocked: %#v", value)
	}
}

func TestREPLMCPPreflightRequiresShapeInspectionWhenNestedSchemaIsOpen(t *testing.T) {
	value := agentRuntimeREPLMCPContractPreflight("repl", map[string]any{
		"code": `details = host.mcp("structures-interactions", "pdb_get_structures", pdb_ids=["9V09"])
for rec in details.get("records", []):
    print(rec["pdb_id"], rec["experimental_method"])`,
	}, literalMCPPreflightSchemas())
	if value == nil || value["status"] != "mcp_schema_preflight_required" ||
		!strings.Contains(stringValue(value["recovery"]), "do not have a closed field schema") {
		t.Fatalf("open nested schema preflight=%#v", value)
	}
	allowed := agentRuntimeREPLMCPContractPreflight("repl", map[string]any{
		"code": `details = host.mcp("structures-interactions", "pdb_get_structures", pdb_ids=["9V09"])
print(type(details), list(details.keys()))`,
	}, literalMCPPreflightSchemas())
	if allowed != nil {
		t.Fatalf("shape-only inspection was blocked: %#v", allowed)
	}
}

func TestGatewayUsesREPLMCPPreflightFromLiveToolSnapshot(t *testing.T) {
	gateway := serverAgentRuntimeToolGateway{
		server: &Server{}, taskRun: &sessionRunnerChatRun{},
		allowedTools: []string{"repl"}, toolSchemas: append(literalMCPPreflightSchemas(), agentruntime.ToolSchema{
			Name: "repl", Parameters: agentKernelReplToolSchema().Parameters,
		}), hasToolSnapshot: true,
	}
	call := agentruntime.ToolCall{ID: "mcp", Name: "repl", Arguments: json.RawMessage(
		`{"code":"detail = host.mcp(\"pubmed\", \"get_article_metadata\", {\"pmid\": \"1\"})","human_description":"Checking metadata"}`,
	)}
	diagnostic := gateway.ToolCallPreflightDiagnostic(call)
	if !strings.Contains(diagnostic, "mcp_schema_preflight_required") || !strings.Contains(diagnostic, "pmids") {
		t.Fatalf("gateway MCP diagnostic=%s", diagnostic)
	}
}

func TestSourceRepairUsesOnlyLiveSchemaPreflightWithoutACompetingRecordGate(t *testing.T) {
	schemas := append(literalMCPPreflightSchemas(), agentruntime.ToolSchema{
		Name: "mcp__literature__openalex_get_work",
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{"work_id": map[string]any{"type": "string"}},
			"required":   []string{"work_id"},
		},
	})
	gateway := serverAgentRuntimeToolGateway{
		server: &Server{}, taskRun: &sessionRunnerChatRun{
			CorrectionReason: "artifact_reference_correction_required",
			CorrectionDetail: "evidence_record_class_missing:source_type=publication",
		},
		allowedTools: []string{"repl"}, toolSchemas: append(schemas, agentruntime.ToolSchema{
			Name: "repl", Parameters: agentKernelReplToolSchema().Parameters,
		}), hasToolSnapshot: true,
	}

	for _, code := range []string{
		`methods = host.mcp.list_methods("literature")
print(methods)`,
		`record = host.mcp("literature", "openalex_get_work", {"work_id":"10.1000/example"})
print(type(record), list(record))`,
		`result = host.mcp("pubmed", "search_articles", {"query":"oral PCSK9", "max_results":100})
print(type(result), list(result))`,
	} {
		call := agentruntime.ToolCall{ID: "source", Name: "repl", Arguments: json.RawMessage(
			`{"code":` + string(mustJSONMarshal(t, code)) + `,"human_description":"Inspecting an authoritative record"}`,
		)}
		if diagnostic := gateway.ToolCallPreflightDiagnostic(call); diagnostic != "" {
			t.Fatalf("valid source-repair call was blocked by a competing gate: %s", diagnostic)
		}
	}

	invalid := agentruntime.ToolCall{ID: "source-invalid", Name: "repl", Arguments: json.RawMessage(
		`{"code":"record = host.mcp(\"literature\", \"openalex_get_work\", {\"doi\":\"10.1000/example\"})","human_description":"Inspecting an authoritative record"}`,
	)}
	diagnostic := gateway.ToolCallPreflightDiagnostic(invalid)
	if !strings.Contains(diagnostic, "mcp_schema_preflight_required") ||
		!strings.Contains(diagnostic, "work_id") {
		t.Fatalf("live schema boundary did not diagnose invalid input: %s", diagnostic)
	}
}

func TestREPLMCPPreflightRequiresTheSelectedSourceClassDuringRepair(t *testing.T) {
	value := agentRuntimeREPLMCPContractPreflight(
		"repl",
		map[string]any{"code": `print("rewriting sources.csv")`},
		literalMCPPreflightSchemas(),
		"publication",
	)
	if value == nil || value["status"] != "mcp_schema_preflight_required" ||
		!strings.Contains(stringValue(value["recovery"]), "host.mcp") {
		t.Fatalf("non-MCP REPL cell was admitted for a publication repair: %#v", value)
	}

	allowed := agentRuntimeREPLMCPContractPreflight(
		"repl",
		map[string]any{"code": `result = host.mcp("pubmed", "search_articles", {"query":"oral PCSK9"})
print(type(result), list(result.keys()))`},
		literalMCPPreflightSchemas(),
		"publication",
	)
	if allowed != nil {
		t.Fatalf("matching publication MCP call was blocked: %#v", allowed)
	}

	wrongClass := agentRuntimeREPLMCPContractPreflight(
		"repl",
		map[string]any{"code": `result = host.mcp("clinical-trials", "get_trial_details", {"nct_id":"NCT00000000"})`},
		literalMCPPreflightSchemas(),
		"publication",
	)
	if wrongClass == nil || !strings.Contains(stringValue(wrongClass["message"]), "publication") {
		t.Fatalf("wrong source class was admitted: %#v", wrongClass)
	}
}

func TestREPLMCPPreflightAllowsRegistryClassifiedSourceBroker(t *testing.T) {
	schemas := append(literalMCPPreflightSchemas(), agentruntime.ToolSchema{
		Name: "mcp__source-broker__read_source",
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"source_id": map[string]any{"type": "string"},
				"operation": map[string]any{"type": "string"},
				"input":     map[string]any{"type": "object"},
			},
			"required": []string{"source_id", "operation", "input"},
		},
	})
	value := agentRuntimeREPLMCPContractPreflight(
		"repl",
		map[string]any{"code": `record = host.mcp("source-broker", "read_source", {"source_id":"registry-source","operation":"lookup","input":{"id":"record-1"}})`},
		schemas,
		"publication",
	)
	if value != nil {
		t.Fatalf("registry-classified source broker was rejected before runtime evidence classification: %#v", value)
	}
}

func TestGatewayAppliesTaskScopedMCPSourceRequirementBeforeREPLExecution(t *testing.T) {
	run := &sessionRunnerChatRun{}
	run.setRequiredMCPSourceClass("publication")
	gateway := serverAgentRuntimeToolGateway{
		server: &Server{}, taskRun: run,
		allowedTools: []string{"repl"}, toolSchemas: append(literalMCPPreflightSchemas(), agentruntime.ToolSchema{
			Name: "repl", Parameters: agentKernelReplToolSchema().Parameters,
		}), hasToolSnapshot: true,
	}
	call := agentruntime.ToolCall{ID: "rewrite", Name: "repl", Arguments: json.RawMessage(
		`{"code":"print(\"rewriting sources.csv\")","human_description":"Updating the source table"}`,
	)}
	diagnostic := gateway.ToolCallPreflightDiagnostic(call)
	if !strings.Contains(diagnostic, "mcp_schema_preflight_required") || !strings.Contains(diagnostic, "host.mcp") {
		t.Fatalf("task-scoped source requirement was not enforced: %s", diagnostic)
	}
}

func mustJSONMarshal(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
