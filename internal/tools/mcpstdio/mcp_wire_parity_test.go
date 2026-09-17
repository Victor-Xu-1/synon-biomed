package mcpstdio

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestPackagedBioToolResultsMatchWorkspaceWireShape(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", ".."))
	runServer := filepath.Join(repositoryRoot, "assets", "optional", "mcp-servers", "bio-tools", "run_server.py")

	for _, tc := range []struct {
		name             string
		serverPackage    string
		tool             string
		arguments        map[string]any
		siteCustomize    string
		wantText         string
		wantStructured   map[string]any
		wantOutputSchema bool
		wantIsError      bool
	}{
		{
			name:          "tier1-standalone",
			serverPackage: "mcp_biomart",
			tool:          "get_data",
			arguments: map[string]any{
				"mart":       "ENSEMBL_MART_ENSEMBL",
				"dataset":    "hsapiens_gene_ensembl",
				"attributes": []string{"ensembl_gene_id"},
				"filters":    map[string]any{"gene": " "},
			},
			wantText: "Error: filter(s) gene have empty values — an empty filter matches nothing; omit the filter instead",
		},
		{
			name:          "tier1-aggregate",
			serverPackage: "mcp_bio",
			tool:          "get_data",
			arguments: map[string]any{
				"mart":       "ENSEMBL_MART_ENSEMBL",
				"dataset":    "hsapiens_gene_ensembl",
				"attributes": []string{"ensembl_gene_id"},
				"filters":    map[string]any{"gene": " "},
			},
			wantText: "Error: filter(s) gene have empty values — an empty filter matches nothing; omit the filter instead",
		},
		{
			name:             "tier1-output-schema-standalone",
			serverPackage:    "mcp_biomart",
			tool:             "batch_translate",
			arguments:        map[string]any{"mart": "ENSEMBL_MART_ENSEMBL", "dataset": "dataset", "from_attr": "from", "to_attr": "to", "targets": []string{"TP53", "MISSING"}},
			siteCustomize:    biomartMappingSiteCustomize,
			wantOutputSchema: true,
			wantStructured: map[string]any{
				"translations":    map[string]any{"TP53": "ENSG00000141510"},
				"not_found":       []any{"MISSING"},
				"found_count":     float64(1),
				"not_found_count": float64(1),
			},
		},
		{
			name:          "tier1-output-schema-aggregate",
			serverPackage: "mcp_bio",
			tool:          "batch_translate",
			arguments:     map[string]any{"mart": "ENSEMBL_MART_ENSEMBL", "dataset": "dataset", "from_attr": "from", "to_attr": "to", "targets": []string{"TP53", "MISSING"}},
			siteCustomize: biomartMappingSiteCustomize,
			wantText:      "structured-json",
		},
		{
			name:          "tier1-json-error-standalone",
			serverPackage: "mcp_biomart",
			tool:          "list_marts",
			arguments:     map[string]any{},
			siteCustomize: biomartErrorSiteCustomize,
			wantText:      `{"error":"synthetic"}`,
			wantIsError:   true,
		},
		{
			name:          "tier1-json-error-aggregate",
			serverPackage: "mcp_bio",
			tool:          "list_marts",
			arguments:     map[string]any{},
			siteCustomize: biomartErrorSiteCustomize,
			wantText:      `{"error":"synthetic"}`,
		},
		{
			name:             "tier2-explicit-envelope-standalone",
			serverPackage:    "mcp_structures_interactions",
			tool:             "pdb_search_structures",
			arguments:        map[string]any{"text": "NEK7", "max_rows": 7},
			siteCustomize:    pdbSearchSiteCustomize,
			wantOutputSchema: true,
			wantStructured: map[string]any{
				"total_count": float64(0),
				"n_retrieved": float64(0),
				"truncated":   false,
				"records":     []any{},
				"max_rows":    float64(7),
			},
		},
		{
			name:             "tier2-explicit-envelope-aggregate",
			serverPackage:    "mcp_bio",
			tool:             "pdb_search_structures",
			arguments:        map[string]any{"text": "NEK7", "max_rows": 7},
			siteCustomize:    pdbSearchSiteCustomize,
			wantOutputSchema: true,
			wantStructured: map[string]any{
				"total_count": float64(0),
				"n_retrieved": float64(0),
				"truncated":   false,
				"records":     []any{},
				"max_rows":    float64(7),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observation := callPackagedMCPRaw(t, runServer, tc.serverPackage, tc.tool, tc.arguments, tc.siteCustomize)
			result := observation.Result
			_, hasOutputSchema := observation.Tool["outputSchema"]
			if hasOutputSchema != tc.wantOutputSchema {
				t.Fatalf("tools/list outputSchema present = %v, want %v: %#v", hasOutputSchema, tc.wantOutputSchema, observation.Tool)
			}
			wantResultKeys := 2
			if tc.wantStructured != nil {
				wantResultKeys++
			}
			if len(result) != wantResultKeys {
				t.Fatalf("raw result keys = %#v, want exactly content/isError plus optional structuredContent", result)
			}
			content, ok := result["content"].([]any)
			if !ok || len(content) != 1 {
				t.Fatalf("content = %#v, want one block", result["content"])
			}
			block, ok := content[0].(map[string]any)
			if !ok || block["type"] != "text" {
				t.Fatalf("content block = %#v", content[0])
			}
			if len(block) != 2 {
				t.Fatalf("raw content block has optional/invented fields: %#v", block)
			}
			if tc.wantText != "" && tc.wantText != "structured-json" && block["text"] != tc.wantText {
				t.Fatalf("text = %#v, want %#v", block["text"], tc.wantText)
			}
			if got, ok := result["isError"].(bool); !ok || got != tc.wantIsError {
				t.Fatalf("isError = %#v, want %v", result["isError"], tc.wantIsError)
			}

			structured, hasStructured := result["structuredContent"]
			if tc.wantStructured == nil {
				if hasStructured {
					t.Fatalf("structuredContent = %#v, want field absent for this tool contract", structured)
				}
				return
			}
			if !hasStructured {
				t.Fatal("structuredContent field is absent")
			}
			payload, ok := structured.(map[string]any)
			if !ok {
				t.Fatalf("structuredContent = %#v, want object", structured)
			}
			if !reflect.DeepEqual(payload, tc.wantStructured) {
				t.Fatalf("structuredContent = %#v, want exact %#v", payload, tc.wantStructured)
			}
			var textPayload map[string]any
			if err := json.Unmarshal([]byte(block["text"].(string)), &textPayload); err != nil {
				t.Fatalf("content text is not the structured JSON: %v\n%v", err, block["text"])
			}
			if !reflect.DeepEqual(textPayload, tc.wantStructured) {
				t.Fatalf("content text payload = %#v, want exact %#v", textPayload, tc.wantStructured)
			}
		})
	}
}

func TestSynonResearchStandaloneMCPStdioDiscoveryAndExecution(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", ".."))
	runServer := filepath.Join(repositoryRoot, "assets", "optional", "mcp-servers", "bio-tools", "run_server.py")
	pack := t.TempDir()
	mustWrite := func(relative, content string, mode os.FileMode) {
		path := filepath.Join(pack, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(".codex-plugin/plugin.json", `{"name":"life-science-research","version":"1.0.3"}`, 0o600)
	mustWrite("skills/example-source-skill/SKILL.md", "---\nname: example-source-skill\ndescription: Example target evidence source\n---\n", 0o600)
	mustWrite("skills/example-source-skill/scripts/query.py", "import json,sys\np=json.load(sys.stdin)\nprint(json.dumps({'ok':True,'records':[p]}))\n", 0o700)
	t.Setenv("SYNON_RESEARCH_ROOT", pack)
	t.Setenv("SYNON_RESEARCH_EXPECTED_VERSION", "1.0.3")

	discovery := callPackagedMCPRaw(t, runServer, "mcp_synon_research", "life_science_sources", map[string]any{"query": "target"}, "")
	if discovery.Tool["name"] != "life_science_sources" {
		t.Fatalf("discovery tool = %#v", discovery.Tool)
	}
	var discoveryPayload map[string]any
	discoveryBlocks, _ := discovery.Result["content"].([]any)
	if len(discoveryBlocks) != 1 {
		t.Fatalf("discovery content = %#v", discovery.Result)
	}
	if err := json.Unmarshal([]byte(discoveryBlocks[0].(map[string]any)["text"].(string)), &discoveryPayload); err != nil {
		t.Fatalf("decode discovery: %v", err)
	}
	if discoveryPayload["ok"] != true || discoveryPayload["returned"] != float64(1) {
		t.Fatalf("discovery payload = %#v", discoveryPayload)
	}

	execution := callPackagedMCPRaw(t, runServer, "mcp_synon_research", "life_science_source", map[string]any{
		"source_id": "example-source-skill",
		"operation": "query",
		"input":     map[string]any{"target": "USP1"},
	}, "")
	blocks, _ := execution.Result["content"].([]any)
	if len(blocks) != 1 {
		t.Fatalf("execution content = %#v", execution.Result)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(blocks[0].(map[string]any)["text"].(string)), &payload); err != nil {
		t.Fatalf("decode execution: %v", err)
	}
	result, _ := payload["result"].(map[string]any)
	receipt, _ := payload["receipt"].(map[string]any)
	if payload["ok"] != true || result["ok"] != true || receipt["operation"] != "query" {
		t.Fatalf("execution payload = %#v", payload)
	}
}

func TestSynonResearchConfiguredPackLiveOpenTargets(t *testing.T) {
	pack := strings.TrimSpace(os.Getenv("SYNON_TEST_SYNON_RESEARCH_ROOT"))
	if pack == "" {
		t.Skip("set SYNON_TEST_SYNON_RESEARCH_ROOT for the live external-pack protocol check")
	}
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", ".."))
	runServer := filepath.Join(repositoryRoot, "assets", "optional", "mcp-servers", "bio-tools", "run_server.py")
	t.Setenv("SYNON_RESEARCH_ROOT", pack)
	t.Setenv("SYNON_RESEARCH_EXPECTED_VERSION", "1.0.3")

	inventory := callPackagedMCPRaw(t, runServer, "mcp_synon_research", "life_science_sources", map[string]any{"max_results": 100}, "")
	inventoryBlocks, _ := inventory.Result["content"].([]any)
	if len(inventoryBlocks) != 1 {
		t.Fatalf("inventory content = %#v", inventory.Result)
	}
	var inventoryPayload map[string]any
	if err := json.Unmarshal([]byte(inventoryBlocks[0].(map[string]any)["text"].(string)), &inventoryPayload); err != nil {
		t.Fatal(err)
	}
	if inventoryPayload["ok"] != true || inventoryPayload["total"] != float64(49) || inventoryPayload["truncated"] != false {
		t.Fatalf("pinned Synon-research inventory = %#v", inventoryPayload)
	}

	discovery := callPackagedMCPRaw(t, runServer, "mcp_synon_research", "life_science_sources", map[string]any{"query": "Open Targets", "max_results": 10}, "")
	blocks, _ := discovery.Result["content"].([]any)
	if len(blocks) != 1 {
		t.Fatalf("discovery content = %#v", discovery.Result)
	}
	var found map[string]any
	if err := json.Unmarshal([]byte(blocks[0].(map[string]any)["text"].(string)), &found); err != nil {
		t.Fatal(err)
	}
	if found["ok"] != true {
		t.Fatalf("Open Targets source discovery = %#v", found)
	}
	sources, _ := found["sources"].([]any)
	hasOpenTargets := false
	for _, item := range sources {
		record, _ := item.(map[string]any)
		if record["source_id"] == "opentargets-skill" {
			hasOpenTargets = true
			break
		}
	}
	if !hasOpenTargets {
		t.Fatalf("Open Targets source is absent from discovery: %#v", sources)
	}

	execution := callPackagedMCPRaw(t, runServer, "mcp_synon_research", "life_science_source", map[string]any{
		"source_id": "opentargets-skill",
		"operation": "opentargets_graphql",
		"input": map[string]any{
			"query":     `query searchAny($q: String!) { search(queryString: $q) { total hits { entity score object { ... on Target { id approvedSymbol biotype } } } } }`,
			"variables": map[string]any{"q": "USP1"},
			"max_items": 5,
			"max_depth": 5,
		},
		"timeout_seconds": 90,
	}, "")
	blocks, _ = execution.Result["content"].([]any)
	if len(blocks) != 1 {
		t.Fatalf("execution content = %#v", execution.Result)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(blocks[0].(map[string]any)["text"].(string)), &payload); err != nil {
		t.Fatal(err)
	}
	result, _ := payload["result"].(map[string]any)
	receipt, _ := payload["receipt"].(map[string]any)
	if payload["ok"] != true || result["source"] != "opentargets-graphql" || receipt["source_id"] != "opentargets-skill" {
		t.Fatalf("live Open Targets result = %#v", payload)
	}
	summary := strings.ToUpper(fmt.Sprint(result["summary"]))
	if !strings.Contains(summary, "USP1") && !strings.Contains(summary, "ENSG00000162607") {
		t.Fatalf("live Open Targets result did not resolve USP1: %#v", result["summary"])
	}
}

const biomartMappingSiteCustomize = `
import mcp_biomart.server as biomart
biomart._translation_dict = lambda dataset, from_attr, to_attr: {"TP53": "ENSG00000141510"}
`

const biomartErrorSiteCustomize = `
import mcp_biomart.server as biomart
biomart.HANDLERS["list_marts"] = lambda arguments: '{"error":"synthetic"}'
`

const pdbSearchSiteCustomize = `
import mcp_structures_interactions.server as structures
structures._pdb_client = lambda: object()
structures.search_structures = lambda *args, **kwargs: {
    "total_count": 0,
    "n_retrieved": 0,
    "truncated": False,
    "records": [],
}
`

type rawMCPObservation struct {
	Tool   map[string]any `json:"tool"`
	Result map[string]any `json:"result"`
}

func callPackagedMCPRaw(t *testing.T, runServer, serverPackage, tool string, arguments map[string]any, siteCustomize string) rawMCPObservation {
	t.Helper()
	encodedArguments, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	const script = `
import json
import subprocess
import sys

from mcp.types import LATEST_PROTOCOL_VERSION

process = subprocess.Popen(
    [sys.executable, sys.argv[1], sys.argv[2]],
    stdin=subprocess.PIPE,
    stdout=subprocess.PIPE,
    stderr=subprocess.PIPE,
    text=True,
    encoding="utf-8",
)

def send(message):
    process.stdin.write(json.dumps(message, separators=(",", ":")) + "\n")
    process.stdin.flush()

def receive(request_id):
    while True:
        line = process.stdout.readline()
        if not line:
            raise RuntimeError("MCP server closed stdout: " + process.stderr.read())
        message = json.loads(line)
        if message.get("id") == request_id:
            if "error" in message:
                raise RuntimeError(json.dumps(message["error"], ensure_ascii=False))
            return message["result"]

try:
    send({
        "jsonrpc": "2.0",
        "id": 1,
        "method": "initialize",
        "params": {
            "protocolVersion": LATEST_PROTOCOL_VERSION,
            "capabilities": {},
            "clientInfo": {"name": "synon-wire-parity", "version": "0.1.0"},
        },
    })
    receive(1)
    send({"jsonrpc": "2.0", "method": "notifications/initialized"})
    send({"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}})
    tools = receive(2)["tools"]
    selected = next(candidate for candidate in tools if candidate["name"] == sys.argv[3])
    send({
        "jsonrpc": "2.0",
        "id": 3,
        "method": "tools/call",
        "params": {"name": sys.argv[3], "arguments": json.loads(sys.argv[4])},
    })
    result = receive(3)
    print(json.dumps({"tool": selected, "result": result}, ensure_ascii=False, separators=(",", ":")))
finally:
    if process.stdin and not process.stdin.closed:
        process.stdin.close()
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        process.terminate()
        process.wait(timeout=5)
`
	command := exec.Command("python3", "-c", script, runServer, serverPackage, tool, string(encodedArguments))
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1", "PYTHONIOENCODING=utf-8")
	if siteCustomize != "" {
		temporary := t.TempDir()
		if err := os.WriteFile(filepath.Join(temporary, "sitecustomize.py"), []byte(siteCustomize), 0o600); err != nil {
			t.Fatal(err)
		}
		pythonPath := temporary + string(os.PathListSeparator) + filepath.Join(filepath.Dir(runServer), "lib")
		if inherited := os.Getenv("PYTHONPATH"); inherited != "" {
			pythonPath += string(os.PathListSeparator) + inherited
		}
		command.Env = append(command.Env, "PYTHONPATH="+pythonPath)
	}
	output, err := command.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("raw MCP call failed: %v\n%s", err, exitErr.Stderr)
		}
		t.Fatal(err)
	}
	var observation rawMCPObservation
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(output))), &observation); err != nil {
		t.Fatalf("decode raw MCP result: %v\n%s", err, output)
	}
	return observation
}
