package mcpdirectory

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"synon-go/internal/tools/mcpstdio"
)

func TestKetcherBundledConnectorHasNativeExecutableTransport(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SYNON_BUNDLED_CONNECTOR_EXECUTABLE", executable)
	connectors := defaultBundledConnectors()
	connector, found := connectors["bundled:ketcher-chemistry"]
	if !found {
		t.Fatal("bundled Ketcher connector is missing")
	}
	if connector.Config.Command == "" || len(connector.Config.Args) != 3 || connector.Config.Args[0] != "mcp-ketcher" || connector.Config.Args[1] != "--widget-gzip" {
		t.Fatalf("Ketcher transport = %#v", connector.Config)
	}
	if filepath.Ext(connector.Config.Args[2]) != ".gz" {
		t.Fatalf("Ketcher widget path = %q", connector.Config.Args[2])
	}
	if info, err := os.Stat(connector.Config.Args[2]); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("Ketcher widget is not available: %q: %v", connector.Config.Args[2], err)
	}
}

func TestBundledPythonConnectorsServeThroughRealMCPHost(t *testing.T) {
	if os.Getenv("SYNON_RUN_REAL_MCP_FLEET") != "1" {
		t.Skip("set SYNON_RUN_REAL_MCP_FLEET=1 for the real 23-server protocol acceptance")
	}
	connectors := defaultBundledConnectors()
	checked := 0
	toolCount := 0
	for _, connector := range orderedBundled(connectors) {
		if connector.Package == "" {
			continue
		}
		connector := connector
		t.Run(connector.Name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			tools, err := mcpstdio.ListToolsForServer(ctx, "", connector.Name, connector.Config)
			if err != nil {
				t.Fatalf("real MCP tools/list failed: %v", err)
			}
			if len(tools) == 0 {
				t.Fatal("real MCP tools/list returned no tools")
			}
			checked++
			toolCount += len(tools)
		})
	}
	if checked != 23 {
		t.Fatalf("real MCP servers checked=%d, want 23", checked)
	}
	if toolCount < 200 {
		t.Fatalf("real MCP tools discovered=%d, want at least 200", toolCount)
	}
}

func TestBundledConnectorsCompleteRepresentativeRealTasks(t *testing.T) {
	if os.Getenv("SYNON_RUN_REAL_MCP_TASKS") != "1" {
		t.Skip("set SYNON_RUN_REAL_MCP_TASKS=1 for live upstream task acceptance")
	}
	type acceptanceCase struct {
		connector string
		tool      string
		arguments map[string]any
	}
	cases := []acceptanceCase{
		{"biomart", "list_marts", map[string]any{}},
		{"biorxiv", "get_categories", map[string]any{}},
		{"cancer-models", "cbioportal_list_studies", map[string]any{"keyword": "glioblastoma", "max_records": 1}},
		{"cellguide", "search_cell_types", map[string]any{"query": "T cell", "limit": 1}},
		{"chembl", "target_search", map[string]any{"gene_symbol": "EGFR", "limit": 1}},
		{"chemistry", "pubchem_search_compounds", map[string]any{"query": "aspirin", "max_cids": 1}},
		{"clinical-genomics", "civic_search_genes", map[string]any{"entrez_symbol": "EGFR"}},
		{"clinical-trials", "search_trials", map[string]any{"condition": "glioblastoma", "page_size": 1}},
		{"drug-regulatory", "search_drug_labels", map[string]any{"active_ingredient": "aspirin", "max_records": 1}},
		{"expression", "gtex_dataset_info", map[string]any{}},
		{"genes-ontologies", "list_ontologies", map[string]any{"ontology_ids": []string{"efo"}}},
		{"genomes", "ensembl_lookup", map[string]any{"query": "ENSG00000146648"}},
		{"human-genetics", "gwas_search_traits", map[string]any{"query": "glioblastoma", "max_records": 1}},
		{"literature", "openalex_search_works", map[string]any{"query": "EGFR inhibitor", "max_records": 1}},
		{"omics-archives", "arrayexpress_search_experiments", map[string]any{"query": "glioblastoma", "max_records": 1}},
		{"protein-annotation", "get_protein_atlas_gene", map[string]any{"gene": "EGFR"}},
		{"pubmed", "get_full_text_article", map[string]any{"pmc_ids": []string{"PMC9046468"}}},
		{"regulation", "jaspar_list_collections", map[string]any{}},
		{"research-resources", "get_antibody_registry_stats", map[string]any{}},
		{"rna", "get_family", map[string]any{"family": "RF00005"}},
		{"structures-interactions", "pdb_get_structures", map[string]any{"pdb_ids": []string{"1CRN"}}},
		{"variants", "gene_constraint", map[string]any{"gene_symbol": "EGFR"}},
		{"zinc", "zinc_search_by_id", map[string]any{"zinc_ids": []string{"ZINC000000000001"}, "max_results": 1, "timeout_s": 15}},
	}
	connectors := defaultBundledConnectors()
	for _, item := range cases {
		item := item
		t.Run(item.connector+"/"+item.tool, func(t *testing.T) {
			connector, ok := connectors["bundled:"+item.connector]
			if !ok {
				t.Fatalf("connector %q is not bundled", item.connector)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			defer cancel()
			output, err := mcpstdio.CallToolForServer(ctx, "", connector.Name, item.tool, item.arguments, connector.Config)
			if err != nil {
				t.Fatalf("real task failed: %v", err)
			}
			if strings.TrimSpace(output) == "" || strings.TrimSpace(output) == "null" {
				t.Fatal("real task returned an empty result")
			}
			t.Logf("real task returned %d bytes", len(output))
		})
	}

	// Ketcher's real stdio protocol and tool call are covered by
	// internal/ketchermcp's widget-integrity acceptance. Hosted MCPs have
	// separate remote/auth acceptance because account-scoped tools require the
	// owner's OAuth session or API key.
}

func TestOfficialOpenTargetsHostedMCPCompletesRealEGFRResolution(t *testing.T) {
	if os.Getenv("SYNON_RUN_REAL_REMOTE_MCP") != "1" {
		t.Skip("set SYNON_RUN_REAL_REMOTE_MCP=1 for official hosted MCP acceptance")
	}
	connector, found := defaultBundledConnectors()["bundled:open-targets-official"]
	if !found {
		t.Fatal("official Open Targets connector is missing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	tools, err := mcpstdio.ListToolsForServer(ctx, t.TempDir(), connector.Name, connector.Config)
	if err != nil {
		t.Fatalf("official hosted tools/list failed: %v", err)
	}
	if len(tools) != 5 {
		t.Fatalf("official hosted tool count=%d, want 5", len(tools))
	}
	searchFound := false
	for _, tool := range tools {
		if tool.ToolName == "search_entities" {
			searchFound = true
			break
		}
	}
	if !searchFound {
		t.Fatalf("official hosted catalog=%#v", tools)
	}
	output, err := mcpstdio.CallToolForServer(ctx, t.TempDir(), connector.Name, "search_entities", map[string]any{
		"query_strings": []string{"EGFR"},
	}, connector.Config)
	if err != nil {
		t.Fatalf("official hosted EGFR task failed: %v", err)
	}
	if !strings.Contains(output, "ENSG00000146648") || !strings.Contains(output, "EGFR") {
		t.Fatalf("official hosted EGFR task returned unexpected evidence: %s", output)
	}
}

func TestOfficialIDCRemoteMCPExposesRealReadOnlyCatalog(t *testing.T) {
	if os.Getenv("SYNON_RUN_REAL_REMOTE_MCP") != "1" {
		t.Skip("set SYNON_RUN_REAL_REMOTE_MCP=1 for official IDC remote MCP acceptance")
	}
	connector, found := defaultBundledConnectors()["bundled:idc-rest"]
	if !found {
		t.Fatal("official IDC connector is missing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	tools, err := mcpstdio.ListToolsForServer(ctx, t.TempDir(), connector.Name, connector.Config)
	if err != nil {
		t.Fatalf("official IDC tools/list failed: %v", err)
	}
	if len(tools) == 0 {
		t.Fatal("official IDC tools/list returned no tools")
	}
}

func TestHostedScientificMCPsExposeRealOAuthAuthorizationBoundary(t *testing.T) {
	if os.Getenv("SYNON_RUN_REAL_REMOTE_MCP") != "1" {
		t.Skip("set SYNON_RUN_REAL_REMOTE_MCP=1 for hosted OAuth discovery and registration acceptance")
	}
	connectors := defaultBundledConnectors()
	cases := []struct {
		id, authorizationHost string
		requiresDynamicClient bool
	}{
		{"bundled:adaptyv-cloud-lab", "mcp.adaptyvbio.com", true},
		{"bundled:omtx-om", "hymmmjstmvbgvbgabydk.supabase.co", true},
		{"bundled:inductive-bio", "beloved-emotion-53.authkit.app", true},
		{"bundled:boltz-api-official", "api.boltz.bio", false},
	}
	for _, acceptance := range cases {
		acceptance := acceptance
		t.Run(acceptance.id, func(t *testing.T) {
			connector, found := connectors[acceptance.id]
			if !found {
				t.Fatalf("connector %q is missing", acceptance.id)
			}
			config := connector.Config
			config.ConnectorID = "acceptance-" + connector.Name
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			started, err := mcpstdio.AuthenticateServer(ctx, t.TempDir(), connector.Name, config)
			if err != nil {
				t.Fatalf("hosted OAuth start failed: %v", err)
			}
			authorizationURL, err := url.Parse(started.AuthURL)
			if err != nil {
				t.Fatalf("parse authorization URL: %v", err)
			}
			query := authorizationURL.Query()
			if started.Status != "auth_url" || authorizationURL.Hostname() != acceptance.authorizationHost ||
				query.Get("client_id") == "" || (acceptance.requiresDynamicClient && query.Get("client_id") == "synon-go") ||
				query.Get("code_challenge_method") != "S256" || query.Get("resource") != connector.Config.URL {
				t.Fatalf("hosted OAuth result=%#v URL=%s", started, authorizationURL.Redacted())
			}
		})
	}
}

func TestOfficialOmPublicHealthEndpoint(t *testing.T) {
	if os.Getenv("SYNON_RUN_REAL_REMOTE_MCP") != "1" {
		t.Skip("set SYNON_RUN_REAL_REMOTE_MCP=1 for the official Om health check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.omtx.ai/v2/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := (&http.Client{Timeout: 20 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("official Om health request failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("official Om health status=%d", response.StatusCode)
	}
	var payload map[string]any
	if err := json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&payload); err != nil {
		t.Fatalf("decode official Om health response: %v", err)
	}
	if payload["status"] != "ok" {
		t.Fatalf("official Om health payload=%#v", payload)
	}
}

func TestOfficialInductiveMCPProtectedEndpoint(t *testing.T) {
	if os.Getenv("SYNON_RUN_REAL_REMOTE_MCP") != "1" {
		t.Skip("set SYNON_RUN_REAL_REMOTE_MCP=1 for the official Inductive MCP boundary check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.inductive.bio/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"synon-acceptance","version":"0.1"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 20 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("official Inductive MCP request failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("official Inductive MCP status=%d", response.StatusCode)
	}
	challenge := response.Header.Get("WWW-Authenticate")
	if !strings.Contains(challenge, `resource_metadata="https://api.inductive.bio/.well-known/oauth-protected-resource/mcp"`) {
		t.Fatalf("official Inductive MCP challenge=%q", challenge)
	}
	if _, err := io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024)); err != nil {
		t.Fatalf("read official Inductive MCP response: %v", err)
	}
}

func TestHostedScientificMCPsRejectMissingOrInvalidCredentialsWithoutLeaking(t *testing.T) {
	if os.Getenv("SYNON_RUN_REAL_REMOTE_MCP") != "1" {
		t.Skip("set SYNON_RUN_REAL_REMOTE_MCP=1 for hosted authentication-boundary acceptance")
	}
	connectors := defaultBundledConnectors()
	for _, id := range []string{
		"bundled:omtx-om", "bundled:patsnap-chemical-molecular", "bundled:boltz-api-official",
		"bundled:tamarind-bio", "bundled:adaptyv-cloud-lab",
	} {
		id := id
		t.Run(id, func(t *testing.T) {
			connector, found := connectors[id]
			if !found {
				t.Fatalf("connector %q is missing", id)
			}
			const sentinel = "synon-invalid-acceptance-key-must-not-be-echoed"
			config := connector.Config
			if connector.APIKeyHeader != "" {
				config.Headers = map[string]string{
					connector.APIKeyHeader: connector.APIKeyPrefix + sentinel,
				}
			}
			if connector.APIKeyQueryParam != "" {
				config.QueryParams = map[string]string{
					connector.APIKeyQueryParam: sentinel,
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			_, err := mcpstdio.ListToolsForServer(ctx, t.TempDir(), connector.Name, config)
			if err == nil {
				t.Fatal("hosted MCP tools/list unexpectedly accepted missing or invalid owner credentials")
			}
			message := strings.ToLower(err.Error())
			if !strings.Contains(message, "401") && !strings.Contains(message, "auth") {
				t.Fatalf("invalid-key rejection was not an authentication boundary: %v", err)
			}
			if strings.Contains(err.Error(), sentinel) {
				t.Fatalf("invalid-key rejection exposed credential material: %v", err)
			}
		})
	}
}

func TestBundledKetcherConnectorSkippedForGoTestBinaryWithoutOverride(t *testing.T) {
	t.Setenv("SYNON_BUNDLED_CONNECTOR_EXECUTABLE", "")
	connectors := defaultBundledConnectors()
	if _, found := connectors["bundled:ketcher-chemistry"]; found {
		t.Fatal("go test binary must not be spawned as the native Ketcher connector subprocess")
	}
}

func TestBundledPythonConnectorsBindToolCatalogsToAssetIdentity(t *testing.T) {
	connectors := defaultBundledConnectors()
	connector, found := connectors["bundled:drug-regulatory"]
	if !found {
		t.Fatal("bundled drug-regulatory connector is missing")
	}
	root := discoverBioToolsRoot()
	want := bundledBioToolsDefinitionSHA256(root)
	if want == "" || len(want) != 64 || connector.DefinitionSHA256 != want {
		t.Fatalf("bundled definition identity=%q want=%q", connector.DefinitionSHA256, want)
	}
}

func TestBundledConnectorRosterMatchesVendoredMethodAuthority(t *testing.T) {
	t.Setenv("SYNON_BUNDLED_CONNECTOR_EXECUTABLE", "mcp-ketcher")
	researchRoot := t.TempDir()
	t.Setenv("SYNON_RESEARCH_ROOT", researchRoot)
	t.Setenv("SYNON_RESEARCH_EXPECTED_VERSION", "1.0.3")
	connectors := defaultBundledConnectors()
	if len(connectors) != 33 {
		t.Fatalf("bundled connector count=%d, want 33", len(connectors))
	}
	synonResearch, found := connectors["bundled:synon-research"]
	if !found || synonResearch.Name != "synon-research" || synonResearch.Package != "mcp_synon_research" {
		t.Fatalf("Synon-research connector = %#v", synonResearch)
	}
	if synonResearch.Config.Type != "stdio" || len(synonResearch.Config.Args) != 2 || synonResearch.Config.Args[1] != "mcp_synon_research" {
		t.Fatalf("Synon-research stdio config = %#v", synonResearch.Config)
	}
	if synonResearch.Config.Env["SYNON_RESEARCH_ROOT"] != researchRoot ||
		synonResearch.Config.Env["SYNON_RESEARCH_EXPECTED_VERSION"] != "1.0.3" || len(synonResearch.Config.Env) != 2 {
		t.Fatalf("Synon-research explicit runtime environment = %#v", synonResearch.Config.Env)
	}
	if other := connectors["bundled:drug-regulatory"]; len(other.Config.Env) != 0 {
		t.Fatalf("connector-local runtime environment leaked into another MCP: %#v", other.Config.Env)
	}
	for _, name := range []string{"SYNON_API_KEY", "SYNON_ACCESS_TOKEN", "lowercase", "SYNON_BAD-NAME"} {
		if validBundledRuntimeEnvironmentName(name) {
			t.Fatalf("sensitive or malformed bundled runtime environment %q was accepted", name)
		}
	}

	root := discoverBioToolsRoot()
	raw, err := os.ReadFile(filepath.Join(root, "lib", "mcp_bio", "domains.json"))
	if err != nil {
		t.Fatal(err)
	}
	var domains map[string][]string
	if err := json.Unmarshal(raw, &domains); err != nil {
		t.Fatal(err)
	}
	if len(domains) != 23 {
		t.Fatalf("bundled bio domain count=%d, want 23", len(domains))
	}
	methodCount := 0
	seen := map[string]struct{}{}
	for domain, methods := range domains {
		connector, found := connectors["bundled:"+domain]
		if !found {
			t.Errorf("domain %q has no bundled connector", domain)
			continue
		}
		if connector.Package != "mcp_"+strings.ReplaceAll(domain, "-", "_") {
			t.Errorf("domain %q package=%q", domain, connector.Package)
		}
		for _, method := range methods {
			identity := domain + "/" + strings.TrimSpace(method)
			if strings.TrimSpace(method) == "" {
				t.Errorf("domain %q declares an empty method", domain)
			}
			if _, duplicate := seen[identity]; duplicate {
				t.Errorf("duplicate scoped MCP method %q", identity)
			}
			seen[identity] = struct{}{}
			methodCount++
		}
	}
	if methodCount != 244 {
		t.Fatalf("bundled bio method count=%d, want 244", methodCount)
	}
	if _, found := connectors["bundled:ketcher-chemistry"]; !found {
		t.Fatal("native Ketcher MCP App connector is missing")
	}
	for _, id := range []string{
		"bundled:open-targets-official", "bundled:omtx-om", "bundled:patsnap-chemical-molecular",
		"bundled:inductive-bio", "bundled:boltz-api-official", "bundled:tamarind-bio", "bundled:adaptyv-cloud-lab",
	} {
		connector, found := connectors[id]
		if !found || connector.Config.Type != "streamable-http" || connector.Config.URL == "" || len(connector.DefinitionSHA256) != 64 {
			t.Fatalf("hosted bundled connector %q = %#v", id, connector)
		}
	}
	tamarind := connectors["bundled:tamarind-bio"]
	if tamarind.OAuthSupported || tamarind.APIKeyHeader != "x-api-key" {
		t.Fatalf("Tamarind must use owner-provided API keys because its published OAuth client is not dynamically registrable: %#v", tamarind)
	}
	if adaptyv := connectors["bundled:adaptyv-cloud-lab"]; !adaptyv.OAuthSupported {
		t.Fatalf("Adaptyv OAuth capability is missing: %#v", adaptyv)
	}
	omtx := connectors["bundled:omtx-om"]
	if !omtx.OAuthSupported || omtx.APIKeyHeader != "" || omtx.APIKeyQueryParam != "" {
		t.Fatalf("Om must use hosted OAuth without a local/API-key fallback: %#v", omtx)
	}
	patsnap := connectors["bundled:patsnap-chemical-molecular"]
	if patsnap.OAuthSupported || patsnap.APIKeyQueryParam != "apikey" || patsnap.APIKeyHeader != "" {
		t.Fatalf("PatSnap must use the documented query API key boundary: %#v", patsnap)
	}
	inductive := connectors["bundled:inductive-bio"]
	if !inductive.OAuthSupported || inductive.APIKeyHeader != "" || inductive.APIKeyQueryParam != "" {
		t.Fatalf("Inductive must use hosted OAuth without a local/API-key fallback: %#v", inductive)
	}
	boltz := connectors["bundled:boltz-api-official"]
	if !boltz.OAuthSupported || boltz.APIKeyHeader != "x-api-key" {
		t.Fatalf("Boltz OAuth/API-key capability is missing: %#v", boltz)
	}
}
