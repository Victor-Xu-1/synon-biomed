package kernel

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
)

func TestReplHostMCPNormalizesPositionalInputAndStructuredEnvelope(t *testing.T) {
	manager, _ := newHostCallTestSession(t, nil)
	var calls atomic.Int64
	policy := &HostCallPolicy{
		AllowedMethods: []string{"mcp"},
		Handler: func(_ context.Context, call HostCall) (any, error) {
			calls.Add(1)
			if call.Method != "mcp" || len(call.Args) != 3 || len(call.Kwargs) != 0 {
				t.Fatalf("normalized host MCP call=%#v", call)
			}
			if call.Args[0] != "pubmed" || call.Args[1] != "search_articles" {
				t.Fatalf("normalized host MCP target=%#v", call.Args)
			}
			input, ok := call.Args[2].(map[string]any)
			if !ok || input["query"] != "NEK7" {
				t.Fatalf("normalized host MCP input=%#v", call.Args[2])
			}
			return map[string]any{"kind": "normalized"}, nil
		},
	}
	outcome := executeHostCallCell(t, manager, "exec-mcp-normalization", `
import host
print(host.mcp("pubmed", "search_articles", {"query": "NEK7"})["kind"])
print(host.mcp("pubmed", {"method": "search_articles", "input": {"query": "NEK7"}})["kind"])
print(host.mcp("mcp__pubmed__search_articles", {"query": "NEK7"})["kind"])
`, policy)
	if outcome.Err != nil || outcome.Response.Error != "" ||
		strings.Count(outcome.Response.Stdout, "normalized") != 3 || calls.Load() != 3 {
		t.Fatalf("normalized host MCP outcome=%#v calls=%d", outcome, calls.Load())
	}
}

func TestReplHostMCPRejectsAmbiguousNormalizedArgumentsBeforeRPC(t *testing.T) {
	manager, _ := newHostCallTestSession(t, nil)
	var calls atomic.Int64
	policy := &HostCallPolicy{
		AllowedMethods: []string{"mcp"},
		Handler: func(context.Context, HostCall) (any, error) {
			calls.Add(1)
			return nil, nil
		},
	}
	outcome := executeHostCallCell(t, manager, "exec-mcp-normalization-invalid", `
import host

def capture(call):
    try:
        call()
    except Exception as exc:
        print(type(exc).__name__ + ": " + str(exc))

capture(lambda: host.mcp("pubmed", "search_articles", ["bad"]))
capture(lambda: host.mcp("pubmed", {"method": "search_articles", "input": {}}, query="NEK7"))
capture(lambda: host.mcp("pubmed", {"input": {"query": "NEK7"}}))
`, policy)
	if outcome.Err != nil || outcome.Response.Error != "" || calls.Load() != 0 {
		t.Fatalf("invalid normalized host MCP outcome=%#v calls=%d", outcome, calls.Load())
	}
	for _, want := range []string{
		"host.mcp: positional input must be a mapping",
		"host.mcp: structured call cannot be combined with additional arguments",
		"host.mcp: structured call requires a non-empty method string",
	} {
		if !strings.Contains(outcome.Response.Stdout, want) {
			t.Fatalf("missing %q in stdout=%q", want, outcome.Response.Stdout)
		}
	}
}

func TestReplHostMCPAddsPortablePythonAliasesWithoutChangingWireResult(t *testing.T) {
	manager, _ := newHostCallTestSession(t, nil)
	policy := &HostCallPolicy{
		AllowedMethods: []string{"mcp"},
		Handler: func(context.Context, HostCall) (any, error) {
			return map[string]any{"targets": []any{map[string]any{
				"components": []any{map[string]any{"gene_symbol": "CRBN", "accession": "Q96SW2"}},
			}}}, nil
		},
	}
	outcome := executeHostCallCell(t, manager, "exec-mcp-portable-aliases", `
import host
result = host.mcp("chembl", "target_search", gene_symbol="CRBN")
target = result["targets"][0]
print(target["gene_symbol"])
print(target["components"][0]["gene"])
print(host.mjson.dumps({"ok": True}, sort_keys=True))
`, policy)
	if outcome.Err != nil || outcome.Response.Error != "" ||
		strings.Count(outcome.Response.Stdout, "CRBN") != 2 ||
		!strings.Contains(outcome.Response.Stdout, `{"ok": true}`) {
		t.Fatalf("portable aliases outcome=%#v", outcome)
	}
}

func TestReplHostMCPSupportsNumericAccessOnlyForOneUnambiguousRecordList(t *testing.T) {
	manager, _ := newHostCallTestSession(t, nil)
	policy := &HostCallPolicy{
		AllowedMethods: []string{"mcp"},
		Handler: func(_ context.Context, call HostCall) (any, error) {
			if call.Args[1] == "one_list" {
				return map[string]any{
					"articles": []any{map[string]any{"title": "Portable result"}},
					"count":    1,
				}, nil
			}
			return map[string]any{
				"articles": []any{map[string]any{"title": "Ambiguous"}},
				"warnings": []any{"review"},
			}, nil
		},
	}
	outcome := executeHostCallCell(t, manager, "exec-mcp-portable-record-list", `
import host
import json
one = host.mcp("literature", "one_list")
print(one[0]["title"])
print(json.dumps(one, sort_keys=True))
many = host.mcp("literature", "many_lists")
try:
    many[0]
except KeyError:
    print("AMBIGUOUS_FAILS_CLOSED")
`, policy)
	if outcome.Err != nil || outcome.Response.Error != "" ||
		!strings.Contains(outcome.Response.Stdout, "Portable result") ||
		!strings.Contains(outcome.Response.Stdout, `"articles"`) ||
		!strings.Contains(outcome.Response.Stdout, "AMBIGUOUS_FAILS_CLOSED") {
		t.Fatalf("portable record-list outcome=%#v", outcome)
	}
}

func TestReplHostMCPExposesRetrievalCoverageWithoutChangingSourceMapping(t *testing.T) {
	manager, _ := newHostCallTestSession(t, nil)
	policy := &HostCallPolicy{
		AllowedMethods: []string{"mcp"},
		Handler: func(context.Context, HostCall) (any, error) {
			return map[string]any{
				"total_count":    237,
				"returned_count": 2,
				"next_retstart":  2,
				"pmids":          []any{"100", "101"},
			}, nil
		},
	}
	outcome := executeHostCallCell(t, manager, "exec-mcp-retrieval-coverage", `
import host
import json
result = host.mcp("pubmed", "search_articles", query="kinase")
print(json.dumps(result.retrieval, sort_keys=True))
print(json.dumps(result.evidence, sort_keys=True))
print("retrieval" in result)
print(json.dumps(result, sort_keys=True))
`, policy)
	if outcome.Err != nil || outcome.Response.Error != "" ||
		!strings.Contains(outcome.Response.Stdout, `"complete": false`) ||
		!strings.Contains(outcome.Response.Stdout, `"has_more": true`) ||
		!strings.Contains(outcome.Response.Stdout, `"returned": 2`) ||
		!strings.Contains(outcome.Response.Stdout, `"total": 237`) ||
		!strings.Contains(outcome.Response.Stdout, `"next_retstart": 2`) ||
		!strings.Contains(outcome.Response.Stdout, `"stage": "discovery"`) ||
		!strings.Contains(outcome.Response.Stdout, `"claim_scope": "candidate_identification_only"`) ||
		!strings.Contains(outcome.Response.Stdout, "\nFalse\n") ||
		strings.Contains(outcome.Response.Stdout, `"retrieval":`) ||
		strings.Contains(outcome.Response.Stdout, `"evidence":`) {
		t.Fatalf("retrieval coverage outcome=%#v", outcome)
	}
}

func TestReplHostMCPClassifiesStructuredAndFullTextReads(t *testing.T) {
	manager, _ := newHostCallTestSession(t, nil)
	policy := &HostCallPolicy{
		AllowedMethods: []string{"mcp"},
		Handler: func(_ context.Context, call HostCall) (any, error) {
			method := fmt.Sprint(call.Args[1])
			if method == "get_trial_details" {
				return map[string]any{"found": true, "trial": map[string]any{
					"nct_id": "NCT00000001", "brief_summary": "Registry detail",
				}}, nil
			}
			return map[string]any{"articles": []any{map[string]any{
				"pmcid": "PMC1", "full_text": strings.Repeat("evidence ", 80),
			}}}, nil
		},
	}
	outcome := executeHostCallCell(t, manager, "exec-mcp-evidence-depth", `
import host
print(host.mcp("clinical-trials", "get_trial_details", nct_id="NCT00000001").evidence["stage"])
print(host.mcp("pubmed", "get_full_text_article", pmcids=["PMC1"]).evidence["stage"])
`, policy)
	if outcome.Err != nil || outcome.Response.Error != "" ||
		!strings.Contains(outcome.Response.Stdout, "structured_record_read") ||
		!strings.Contains(outcome.Response.Stdout, "full_text_read") {
		t.Fatalf("MCP evidence-depth outcome=%#v", outcome)
	}
}

func TestReplHostMCPCollectWalksDeclaredPaginationAndDeduplicates(t *testing.T) {
	manager, _ := newHostCallTestSession(t, nil)
	mcpCalls := 0
	policy := &HostCallPolicy{
		AllowedMethods: []string{"mcp.catalog", "mcp"},
		Handler: func(_ context.Context, call HostCall) (any, error) {
			if call.Method == "mcp.catalog" {
				if len(call.Kwargs) == 0 {
					return map[string]any{"servers": []any{"literature"}, "tools": []any{}}, nil
				}
				return map[string]any{"tools": []any{map[string]any{
					"server": "literature", "method": "search_articles",
					"parameters": []any{
						map[string]any{"name": "query", "required": true},
						map[string]any{"name": "retstart", "required": false},
					},
				}}, "has_more": false}, nil
			}
			mcpCalls++
			input, _ := call.Args[2].(map[string]any)
			if fmt.Sprint(input["retstart"]) == "2" {
				return map[string]any{
					"total_count": 3, "returned_count": 2, "has_more": false,
					"pmids": []any{"101", "102"},
				}, nil
			}
			return map[string]any{
				"total_count": 3, "returned_count": 2, "next_retstart": 2,
				"pmids": []any{"100", "101"},
			}, nil
		},
	}
	outcome := executeHostCallCell(t, manager, "exec-mcp-collect", `
import host
import json
result = host.mcp.collect(
    "literature", "search_articles", {"query": "kinase", "retstart": 0},
    max_records=10,
)
print(json.dumps(result.records))
print(json.dumps(result.retrieval, sort_keys=True))
`, policy)
	if outcome.Err != nil || outcome.Response.Error != "" || mcpCalls != 2 ||
		!strings.Contains(outcome.Response.Stdout, `["100", "101", "102"]`) ||
		!strings.Contains(outcome.Response.Stdout, `"complete": true`) ||
		!strings.Contains(outcome.Response.Stdout, `"duplicates_removed": 1`) ||
		!strings.Contains(outcome.Response.Stdout, `"pages": 2`) {
		t.Fatalf("host.mcp.collect outcome=%#v calls=%d", outcome, mcpCalls)
	}
}

func TestReplHostMCPCollectNormalizesNestedPageInfoAndDataRecords(t *testing.T) {
	manager, _ := newHostCallTestSession(t, nil)
	mcpCalls := 0
	policy := &HostCallPolicy{
		AllowedMethods: []string{"mcp.catalog", "mcp"},
		Handler: func(_ context.Context, call HostCall) (any, error) {
			if call.Method == "mcp.catalog" {
				if len(call.Kwargs) == 0 {
					return map[string]any{"servers": []any{"genomics"}, "tools": []any{}}, nil
				}
				return map[string]any{"tools": []any{map[string]any{
					"server": "genomics", "method": "query_variants",
					"parameters": []any{
						map[string]any{"name": "q", "required": true},
						map[string]any{"name": "after", "required": false},
					},
				}}, "has_more": false}, nil
			}
			mcpCalls++
			input, _ := call.Args[2].(map[string]any)
			if input["after"] == "cursor-1" {
				return map[string]any{
					"data":     []any{map[string]any{"id": "v2"}},
					"pageInfo": map[string]any{"totalCount": 2, "hasMore": false},
				}, nil
			}
			return map[string]any{
				"data": []any{map[string]any{"id": "v1"}},
				"pageInfo": map[string]any{
					"totalCount": 2, "hasMore": true, "endCursor": "cursor-1",
				},
			}, nil
		},
	}
	outcome := executeHostCallCell(t, manager, "exec-mcp-nested-page-info", `
import host
import json
result = host.mcp.collect(
    "genomics", "query_variants", {"q": "BRCA1"}, max_records=10,
)
print([item["id"] for item in result.records])
print(json.dumps(result.retrieval, sort_keys=True))
print(result["page_receipts"][0]["records_field"])
`, policy)
	if outcome.Err != nil || outcome.Response.Error != "" || mcpCalls != 2 ||
		!strings.Contains(outcome.Response.Stdout, `['v1', 'v2']`) ||
		!strings.Contains(outcome.Response.Stdout, `"complete": true`) ||
		!strings.Contains(outcome.Response.Stdout, `"provider_total": 2`) ||
		!strings.Contains(outcome.Response.Stdout, "data") {
		t.Fatalf("nested MCP pagination outcome=%#v calls=%d", outcome, mcpCalls)
	}
}

func TestReplHostMCPSearchMergesQueryVariantsWithTruthfulCoverage(t *testing.T) {
	manager, _ := newHostCallTestSession(t, nil)
	policy := &HostCallPolicy{
		AllowedMethods: []string{"mcp.catalog", "mcp"},
		Handler: func(_ context.Context, call HostCall) (any, error) {
			if call.Method == "mcp.catalog" {
				if len(call.Kwargs) == 0 {
					return map[string]any{"servers": []any{"trials"}, "tools": []any{}}, nil
				}
				return map[string]any{"tools": []any{map[string]any{
					"server": "trials", "method": "search_trials",
					"parameters": []any{map[string]any{"name": "query", "required": true}},
				}}, "has_more": false}, nil
			}
			input, _ := call.Args[2].(map[string]any)
			if strings.Contains(fmt.Sprint(input["query"]), "口服") {
				return map[string]any{
					"total": 2, "has_more": false,
					"trials": []any{
						map[string]any{"nct_id": "NCT001", "title": "共同记录"},
						map[string]any{"nct_id": "NCT002", "title": "中文来源"},
					},
				}, nil
			}
			return map[string]any{
				"total": 2, "has_more": false,
				"trials": []any{
					map[string]any{"nct_id": "NCT001", "title": "Shared record"},
					map[string]any{"nct_id": "NCT003", "title": "English source"},
				},
			}, nil
		},
	}
	outcome := executeHostCallCell(t, manager, "exec-mcp-search", `
import host
import json
result = host.mcp.search(
    "trials", "search_trials", "口服肽 临床试验",
    query_variants=["oral peptide clinical trial"], max_records=20,
)
print([item["nct_id"] for item in result.records])
print(json.dumps(result.retrieval, sort_keys=True))
`, policy)
	if outcome.Err != nil || outcome.Response.Error != "" ||
		!strings.Contains(outcome.Response.Stdout, `['NCT001', 'NCT002', 'NCT003']`) ||
		!strings.Contains(outcome.Response.Stdout, `"query_variants": 2`) ||
		!strings.Contains(outcome.Response.Stdout, `"candidates": 4`) ||
		!strings.Contains(outcome.Response.Stdout, `"unique_candidates": 3`) ||
		!strings.Contains(outcome.Response.Stdout, `"duplicates_removed": 1`) ||
		!strings.Contains(outcome.Response.Stdout, `"overflow": 0`) ||
		!strings.Contains(outcome.Response.Stdout, `"returned": 3`) ||
		!strings.Contains(outcome.Response.Stdout, `"complete": true`) {
		t.Fatalf("host.mcp.search outcome=%#v", outcome)
	}
}

func TestPythonSyntaxErrorReturnsNonExecutingPreflight(t *testing.T) {
	manager, _ := newHostCallTestSession(t, nil)
	outcome := executeHostCallCell(t, manager, "exec-syntax-preflight", "print(", nil)
	preflight := outcome.Response.Preflight
	if outcome.Err != nil || outcome.Response.Error != "" ||
		preflight["status"] != "code_preflight_required" || preflight["executed"] != false ||
		strings.TrimSpace(outcome.Response.Stdout) != "" || strings.TrimSpace(outcome.Response.Stderr) != "" {
		t.Fatalf("syntax preflight outcome=%#v", outcome)
	}
	normal := executeHostCallCell(t, manager, "exec-after-syntax-preflight", "print('still-alive')", nil)
	if normal.Err != nil || normal.Response.Error != "" || strings.TrimSpace(normal.Response.Stdout) != "still-alive" {
		t.Fatalf("worker after syntax preflight=%#v", normal)
	}
}

func TestPythonMissingTopLevelImportReturnsNonExecutingPreflight(t *testing.T) {
	manager, _ := newHostCallTestSession(t, nil)
	outcome := executeHostCallCell(t, manager, "exec-import-preflight", "import synon_package_that_does_not_exist\nprint('must-not-run')", nil)
	preflight := outcome.Response.Preflight
	if outcome.Err != nil || outcome.Response.Error != "" ||
		preflight["status"] != "code_preflight_required" || preflight["executed"] != false ||
		!strings.Contains(preflight["message"].(string), "synon_package_that_does_not_exist") ||
		strings.TrimSpace(outcome.Response.Stdout) != "" || strings.TrimSpace(outcome.Response.Stderr) != "" {
		t.Fatalf("import preflight outcome=%#v", outcome)
	}
	normal := executeHostCallCell(t, manager, "exec-after-import-preflight", "print('still-alive')", nil)
	if normal.Err != nil || normal.Response.Error != "" || strings.TrimSpace(normal.Response.Stdout) != "still-alive" {
		t.Fatalf("worker after import preflight=%#v", normal)
	}
}
