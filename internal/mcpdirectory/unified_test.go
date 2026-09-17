package mcpdirectory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/tools/mcpstdio"
)

func TestUnifiedCatalogMatchesV11BundledIDsAndRejectsCrossSourceCollision(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SYNON_BUNDLED_CONNECTOR_EXECUTABLE", executable)
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store, t.TempDir(), nil)
	connectors, err := service.ListUnifiedConnectors(context.Background(), "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	var bundled []string
	for _, connector := range connectors {
		if connector.Source == "bundled" {
			bundled = append(bundled, connector.ID)
			if !connector.Enabled {
				t.Fatalf("bundled connector is disabled: %#v", connector)
			}
			switch connector.ID {
			case "bundled:open-targets-official":
				if connector.Transport != "streamable-http" || connector.AuthState != "not-required" || connector.URL == "" {
					t.Fatalf("Open Targets projection=%#v", connector)
				}
			case "bundled:omtx-om", "bundled:inductive-bio":
				if connector.Transport != "streamable-http" || connector.AuthState != "unauthorized" ||
					connector.ConnectionStatus != "auth_required" || connector.APIKeyConfigurable || connector.APIKeyConfigured {
					t.Fatalf("Om hosted projection=%#v", connector)
				}
			case "bundled:patsnap-chemical-molecular", "bundled:boltz-api-official", "bundled:tamarind-bio", "bundled:adaptyv-cloud-lab":
				if connector.Transport != "streamable-http" || connector.AuthState != "unauthorized" ||
					connector.ConnectionStatus != "auth_required" || !connector.APIKeyConfigurable || connector.APIKeyConfigured {
					t.Fatalf("authenticated hosted projection=%#v", connector)
				}
			case "bundled:idc-rest":
				if connector.Transport != "streamable-http" || connector.AuthState != "not-required" || connector.URL == "" {
					t.Fatalf("IDC hosted projection=%#v", connector)
				}
			default:
				if connector.Transport != "stdio" || connector.AuthState != "not-required" {
					t.Fatalf("local bundled projection=%#v", connector)
				}
			}
		}
	}
	want := []string{
		"bundled:biomart", "bundled:pubmed", "bundled:clinical-trials", "bundled:chembl",
		"bundled:biorxiv", "bundled:variants", "bundled:clinical-genomics", "bundled:expression",
		"bundled:regulation", "bundled:protein-annotation", "bundled:rna",
		"bundled:structures-interactions", "bundled:omics-archives", "bundled:genes-ontologies",
		"bundled:drug-regulatory", "bundled:research-resources", "bundled:synon-research", "bundled:cancer-models",
		"bundled:chemistry", "bundled:human-genetics", "bundled:literature", "bundled:genomes",
		"bundled:cellguide", "bundled:zinc", "bundled:omtx-om", "bundled:patsnap-chemical-molecular", "bundled:inductive-bio",
		"bundled:boltz-api-official", "bundled:open-targets-official", "bundled:tamarind-bio",
		"bundled:adaptyv-cloud-lab", "bundled:ketcher-chemistry", "bundled:idc-rest",
	}
	if strings.Join(bundled, ",") != strings.Join(want, ",") {
		t.Fatalf("bundled ids=%v", bundled)
	}
	first := connectors[0]
	if first.ID != "bundled:biomart" || first.ConnectionStatus != "connecting" ||
		len(first.AttachedAgents) != 1 || first.AttachedAgents[0] != "OPERON" ||
		len(first.Upstreams) != 1 || first.Upstreams[0].Name != "Ensembl BioMart" ||
		first.Description != "Ensembl BioMart — genomic annotations, identifier translation, and cross-reference queries." {
		t.Fatalf("v1.1 bundled metadata=%#v", first)
	}
	if _, err := store.CreateMCPServer(workspace.MCPServerInput{ID: "bundled:pubmed", UserID: "owner-a", Name: "collision", URL: "https://example.test/mcp", Transport: "streamable-http"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListUnifiedConnectors(context.Background(), "owner-a"); err == nil || !strings.Contains(err.Error(), "duplicate MCP connector id") {
		t.Fatalf("collision err=%v", err)
	}
	if foreign, err := service.ListUnifiedConnectors(context.Background(), "owner-b"); err != nil || len(foreign) != len(want) {
		t.Fatalf("foreign owner catalog len=%d err=%v", len(foreign), err)
	}
}

func TestBundledRuntimeConnectorReceivesOnlyOwnerConsentedContactEmail(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store, t.TempDir(), nil)
	service.SetContactEmailProvider(func(userID string) (string, bool, error) {
		if userID == "owner-allowed" {
			return "researcher@example.org", true, nil
		}
		return "", false, nil
	})

	allowed, found, err := service.ResolveRuntimeConnector(context.Background(), "owner-allowed", "bundled:pubmed")
	if err != nil || !found || allowed.Config.Env["OPERON_CONTACT_EMAIL"] != "researcher@example.org" {
		t.Fatalf("allowed connector=%#v found=%t err=%v", allowed, found, err)
	}
	denied, found, err := service.ResolveRuntimeConnector(context.Background(), "owner-denied", "bundled:pubmed")
	if err != nil || !found {
		t.Fatalf("denied connector=%#v found=%t err=%v", denied, found, err)
	}
	if _, leaked := denied.Config.Env["OPERON_CONTACT_EMAIL"]; leaked {
		t.Fatalf("contact email leaked across owners: %#v", denied.Config.Env)
	}
}

func TestBundledAPIKeyIsOwnerScopedInjectedOnlyAtTransportAndNeverProjected(t *testing.T) {
	var authenticatedCalls atomic.Int64
	mcp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer owner-a-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		authenticatedCalls.Add(1)
		var request struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch request.Method {
		case "server/discover":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": request.ID,
				"result": map[string]any{
					"supportedVersions": []string{"2026-07-28"},
					"capabilities":      map[string]any{},
					"serverInfo":        map[string]any{"name": "key-fixture", "version": "1"},
				},
			})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": request.ID,
				"result": map[string]any{"tools": []map[string]any{{
					"name": "inspect", "inputSchema": map[string]any{"type": "object"},
				}}},
			})
		default:
			t.Errorf("unexpected method %q", request.Method)
		}
	}))
	defer mcp.Close()
	client, publicURL := publicMappedTLSClient(t, mcp)
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store, t.TempDir(), client)
	service.bundled = map[string]bundledConnector{
		"bundled:key-fixture": {
			ID: "bundled:key-fixture", Name: "key-fixture", DisplayName: "Key Fixture",
			Config:       mcpstdio.ServerConfig{Type: "streamable-http", URL: publicURL, Scope: "bundled"},
			AuthRequired: true, APIKeyHeader: "Authorization", APIKeyPrefix: "Bearer ", APIKeyLabel: "Provider token",
		},
	}
	service.SetBundledAPIKeyProvider(func(userID, connectorID string) (string, bool, error) {
		if userID == "owner-a" && connectorID == "bundled:key-fixture" {
			return "owner-a-secret", true, nil
		}
		return "", false, nil
	})

	ownerA, found, err := service.ResolveRuntimeConnector(context.Background(), "owner-a", "bundled:key-fixture")
	if err != nil || !found || ownerA.Config.Headers["Authorization"] != "Bearer owner-a-secret" {
		t.Fatalf("owner A runtime connector=%#v found=%t err=%v", ownerA, found, err)
	}
	ownerB, found, err := service.ResolveRuntimeConnector(context.Background(), "owner-b", "bundled:key-fixture")
	if err != nil || !found {
		t.Fatalf("owner B runtime connector=%#v found=%t err=%v", ownerB, found, err)
	}
	if _, leaked := ownerB.Config.Headers["Authorization"]; leaked {
		t.Fatalf("owner A key leaked into owner B runtime: %#v", ownerB.Config.Headers)
	}
	connected, err := service.SetUnifiedEnabled(context.Background(), "owner-a", "bundled:key-fixture", true)
	if err != nil || connected.ConnectionStatus != "connected" || !connected.APIKeyConfigured || connected.ToolCount != 1 {
		t.Fatalf("connected projection=%#v err=%v", connected, err)
	}
	raw, err := json.Marshal(connected)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "owner-a-secret") || authenticatedCalls.Load() == 0 {
		t.Fatalf("credential projection=%s authenticatedCalls=%d", raw, authenticatedCalls.Load())
	}
	foreign, err := service.ListUnifiedConnectors(context.Background(), "owner-b")
	if err != nil || len(foreign) != 1 || foreign[0].APIKeyConfigured || foreign[0].AuthState != "unauthorized" {
		t.Fatalf("foreign projection=%#v err=%v", foreign, err)
	}
}

func TestBundledAPIKeyQueryParamIsOwnerScopedInjectedOnlyAtTransport(t *testing.T) {
	const sentinel = "owner-a-query-secret"
	var authenticatedCalls atomic.Int64
	mcp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") != sentinel {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		authenticatedCalls.Add(1)
		var request struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch request.Method {
		case "server/discover":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": request.ID,
				"result": map[string]any{
					"supportedVersions": []string{"2026-07-28"},
					"capabilities":      map[string]any{},
					"serverInfo":        map[string]any{"name": "query-fixture", "version": "1"},
				},
			})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": request.ID,
				"result": map[string]any{"tools": []map[string]any{{
					"name": "inspect", "inputSchema": map[string]any{"type": "object"},
				}}},
			})
		default:
			t.Errorf("unexpected method %q", request.Method)
		}
	}))
	defer mcp.Close()
	client, publicURL := publicMappedTLSClient(t, mcp)
	service := newTestService(t, client)
	service.bundled = map[string]bundledConnector{
		"bundled:query-fixture": {
			ID: "bundled:query-fixture", Name: "query-fixture", DisplayName: "Query Fixture",
			Config:       mcpstdio.ServerConfig{Type: "streamable-http", URL: publicURL, Scope: "bundled"},
			AuthRequired: true, APIKeyQueryParam: "apikey", APIKeyLabel: "Provider key",
		},
	}
	service.SetBundledAPIKeyProvider(func(userID, connectorID string) (string, bool, error) {
		if userID == "owner-a" && connectorID == "bundled:query-fixture" {
			return sentinel, true, nil
		}
		return "", false, nil
	})

	ownerA, found, err := service.ResolveRuntimeConnector(context.Background(), "owner-a", "bundled:query-fixture")
	if err != nil || !found || ownerA.Config.QueryParams["apikey"] != sentinel || strings.Contains(ownerA.Config.URL, sentinel) {
		t.Fatalf("owner A runtime connector=%#v found=%t err=%v", ownerA, found, err)
	}
	ownerB, found, err := service.ResolveRuntimeConnector(context.Background(), "owner-b", "bundled:query-fixture")
	if err != nil || !found {
		t.Fatalf("owner B runtime connector=%#v found=%t err=%v", ownerB, found, err)
	}
	if len(ownerB.Config.QueryParams) != 0 {
		t.Fatalf("owner A key leaked into owner B runtime: %#v", ownerB.Config.QueryParams)
	}
	serialized, err := json.Marshal(ownerA.Config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), sentinel) {
		t.Fatalf("query credential entered serialized runtime config: %s", serialized)
	}
	connected, err := service.SetUnifiedEnabled(context.Background(), "owner-a", "bundled:query-fixture", true)
	if err != nil || connected.ConnectionStatus != "connected" || !connected.APIKeyConfigured || connected.ToolCount != 1 {
		t.Fatalf("connected projection=%#v err=%v", connected, err)
	}
	projected, err := json.Marshal(connected)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(projected), sentinel) || authenticatedCalls.Load() == 0 {
		t.Fatalf("query credential leaked or was not sent: projection=%s calls=%d", projected, authenticatedCalls.Load())
	}
}

func TestTamarindAuthorizationRequiresOwnerAPIKeyInsteadOfUnsupportedOAuth(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store, t.TempDir(), nil)

	if _, err := service.AuthorizeUnified(context.Background(), "owner", "bundled:tamarind-bio"); err == nil ||
		!strings.Contains(err.Error(), "owner-provided API key") {
		t.Fatalf("Tamarind authorize error=%v", err)
	}
	connectors, err := service.ListUnifiedConnectors(context.Background(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	for _, connector := range connectors {
		if connector.ID != "bundled:tamarind-bio" {
			continue
		}
		if connector.OAuthSupported || !connector.APIKeyConfigurable || connector.APIKeyConfigured {
			t.Fatalf("Tamarind authentication projection=%#v", connector)
		}
		return
	}
	t.Fatal("Tamarind connector is missing")
}

func TestBundledRuntimeConnectorUsesManagedPythonCommand(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store, t.TempDir(), nil)
	want := filepath.Join(t.TempDir(), "managed-python")
	service.SetBundledPythonCommandProvider(func() (string, error) { return want, nil })
	connector, found, err := service.ResolveRuntimeConnector(context.Background(), "owner", "bundled:pubmed")
	if err != nil || !found {
		t.Fatalf("connector=%#v found=%t err=%v", connector, found, err)
	}
	if connector.Config.Command != want {
		t.Fatalf("command=%q want=%q", connector.Config.Command, want)
	}
	if len(connector.Config.Args) != 2 || connector.Config.Args[1] != "mcp_pubmed" {
		t.Fatalf("args=%#v", connector.Config.Args)
	}
}

func TestAllBundledPythonConnectorsUseManagedPythonCommand(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	service := New(store, t.TempDir(), nil)
	want := filepath.Join(t.TempDir(), "managed-python")
	service.SetBundledPythonCommandProvider(func() (string, error) { return want, nil })

	checked := 0
	for id, item := range service.bundled {
		if item.Package == "" {
			continue
		}
		connector, found, err := service.ResolveRuntimeConnector(context.Background(), "owner", id)
		if err != nil || !found {
			t.Fatalf("resolve bundled connector %q: found=%v err=%v", id, found, err)
		}
		if connector.Config.Command != want {
			t.Fatalf("bundled connector %q command = %q, want %q", id, connector.Config.Command, want)
		}
		checked++
	}
	if checked != 24 {
		t.Fatalf("checked %d bundled Python connectors, want 24", checked)
	}
}

func TestBundledProbeDoesNotPersistFailureBeforeManagedPythonReady(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store, t.TempDir(), nil)
	service.bundled = map[string]bundledConnector{
		"bundled:pubmed": {
			ID: "bundled:pubmed", Name: "PubMed", Package: "mcp_pubmed",
			Config: mcpstdio.ServerConfig{Type: "stdio", Command: "python3", Scope: "bundled"},
		},
	}
	service.SetBundledPythonCommandProvider(func() (string, error) {
		return "", errors.New("runtime installing")
	})
	if err := service.ProbeMissingBundled(context.Background(), "owner"); err == nil || !strings.Contains(err.Error(), "runtime installing") {
		t.Fatalf("probe err=%v", err)
	}
	states, err := store.ListMCPConnectorRuntimeStates("owner")
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 0 {
		t.Fatalf("runtime failure was persisted before managed Python became ready: %#v", states)
	}
}

func TestUnifiedCustomAndBundledProbePersistHealthSchemaAndOwnerState(t *testing.T) {
	var calls atomic.Int64
	mcp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch request.Method {
		case "server/discover":
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}}})
		case "notifications/initialized":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{}})
		case "tools/list":
			calls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"tools": []map[string]any{{"name": "lookup", "description": "lookup", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}}}}}})
		default:
			t.Errorf("method=%q", request.Method)
		}
	}))
	defer mcp.Close()
	db := filepath.Join(t.TempDir(), "workspace.db")
	client, publicURL := publicMappedTLSClient(t, mcp)
	store, err := workspace.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMCPServer(workspace.MCPServerInput{ID: "custom:tls", UserID: "owner-a", Name: "TLS", URL: publicURL, Transport: "streamable-http"}); err != nil {
		t.Fatal(err)
	}
	service := New(store, t.TempDir(), client)
	service.bundled["bundled:pubmed"] = bundledConnector{ID: "bundled:pubmed", Name: "pubmed", DisplayName: "PubMed", Config: mcpstdio.ServerConfig{Type: "streamable-http", URL: publicURL, Scope: "bundled"}}
	custom, err := service.SetUnifiedEnabled(context.Background(), "owner-a", "custom:tls", true)
	if err != nil {
		t.Fatal(err)
	}
	bundled, err := service.SetUnifiedEnabled(context.Background(), "owner-a", "bundled:pubmed", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, connector := range []Connector{custom, bundled} {
		if connector.ConnectionStatus != "connected" || connector.ToolCount != 1 || len(connector.SchemaSHA256) != 64 {
			t.Fatalf("probe projection=%#v", connector)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("tools/list calls=%d", calls.Load())
	}
	if _, err := service.SetUnifiedEnabled(context.Background(), "owner-b", "custom:tls", false); err == nil {
		t.Fatal("foreign owner changed custom connector")
	}
	if _, err := service.SetUnifiedEnabled(context.Background(), "owner-a", "bundled:pubmed", false); err != nil {
		t.Fatal(err)
	}
	ownerB, err := service.ListUnifiedConnectors(context.Background(), "owner-b")
	if err != nil {
		t.Fatal(err)
	}
	for _, connector := range ownerB {
		if connector.ID == "bundled:pubmed" && !connector.Enabled {
			t.Fatal("bundled preference leaked across owners")
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := workspace.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted := New(reopened, t.TempDir(), client)
	list, err := restarted.ListUnifiedConnectors(context.Background(), "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Connector{}
	for _, connector := range list {
		byID[connector.ID] = connector
	}
	if byID["custom:tls"].ConnectionStatus != "connected" || len(byID["custom:tls"].SchemaSHA256) != 64 {
		t.Fatalf("custom after restart=%#v", byID["custom:tls"])
	}
	if byID["bundled:pubmed"].Enabled || byID["bundled:pubmed"].ConnectionStatus != "disabled" {
		t.Fatalf("bundled after restart=%#v", byID["bundled:pubmed"])
	}
	if calls.Load() != 2 {
		t.Fatalf("GET catalog re-probed connectors: calls=%d", calls.Load())
	}
}

func TestPackagedBundledConnectorUsesRealStdioInitializeAndToolsList(t *testing.T) {
	root := discoverBioToolsRoot()
	if _, err := filepath.Abs(root); err != nil {
		t.Fatal(err)
	}
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store, t.TempDir(), nil)
	connector, err := service.SetUnifiedEnabled(context.Background(), "owner-a", "bundled:pubmed", true)
	if err != nil {
		t.Fatal(err)
	}
	if connector.ConnectionStatus != "connected" || connector.ToolCount < 1 || len(connector.SchemaSHA256) != 64 {
		t.Fatalf("packaged bundled probe=%#v", connector)
	}
}

func TestScheduleMissingBundledProbeInitializesNewOwnerOnce(t *testing.T) {
	var calls atomic.Int64
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	mcp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch request.Method {
		case "server/discover":
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": request.ID,
				"error": map[string]any{"code": -32601, "message": "method not found"},
			})
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": request.ID,
				"result": map[string]any{
					"protocolVersion": "2024-11-05",
					"capabilities":    map[string]any{},
				},
			})
		case "notifications/initialized":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{},
			})
		case "tools/list":
			calls.Add(1)
			startedOnce.Do(func() { close(started) })
			<-release
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": request.ID,
				"result": map[string]any{"tools": []map[string]any{{
					"name": "search_articles", "description": "search",
					"inputSchema": map[string]any{"type": "object"},
				}}},
			})
		default:
			t.Errorf("method=%q", request.Method)
		}
	}))
	defer mcp.Close()
	client, publicURL := publicMappedTLSClient(t, mcp)
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store, t.TempDir(), client)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := service.Close(ctx); err != nil {
			t.Errorf("close MCP directory service: %v", err)
		}
	})
	service.bundled = map[string]bundledConnector{
		"bundled:pubmed": {
			ID: "bundled:pubmed", Name: "pubmed", DisplayName: "PubMed",
			Config: mcpstdio.ServerConfig{
				Type: "streamable-http", URL: publicURL, Scope: "bundled",
			},
		},
	}
	if !service.ScheduleMissingBundledProbe("owner-new") {
		t.Fatal("first owner bootstrap was not scheduled")
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("owner bootstrap did not reach tools/list")
	}
	if service.ScheduleMissingBundledProbe("owner-new") {
		close(release)
		t.Fatal("duplicate owner bootstrap was scheduled while the first probe was active")
	}
	close(release)
	deadline := time.Now().Add(3 * time.Second)
	var connectors []Connector
	for time.Now().Before(deadline) {
		connectors, err = service.ListUnifiedConnectors(context.Background(), "owner-new")
		if err != nil {
			t.Fatal(err)
		}
		if len(connectors) == 1 && connectors[0].ConnectionStatus == "connected" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if calls.Load() != 1 {
		t.Fatalf("tools/list calls = %d, want 1", calls.Load())
	}
	if len(connectors) != 1 || connectors[0].ConnectionStatus != "connected" ||
		connectors[0].Health == nil || !connectors[0].Health.OK ||
		connectors[0].ToolCount != 1 || len(connectors[0].SchemaSHA256) != 64 {
		t.Fatalf("probed connector = %#v", connectors)
	}
}

func TestScheduleMissingBundledProbeDoesNotOverwriteConcurrentDisable(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	mcp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch request.Method {
		case "server/discover":
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": request.ID,
				"error": map[string]any{"code": -32601, "message": "method not found"},
			})
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": request.ID,
				"result": map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}},
			})
		case "notifications/initialized":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{},
			})
		case "tools/list":
			startedOnce.Do(func() { close(started) })
			<-release
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": request.ID,
				"result": map[string]any{"tools": []map[string]any{{
					"name": "search_articles", "description": "search",
					"inputSchema": map[string]any{"type": "object"},
				}}},
			})
		default:
			t.Errorf("method=%q", request.Method)
		}
	}))
	defer mcp.Close()
	client, publicURL := publicMappedTLSClient(t, mcp)
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store, t.TempDir(), client)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := service.Close(ctx); err != nil {
			t.Errorf("close MCP directory service: %v", err)
		}
	})
	service.bundled = map[string]bundledConnector{
		"bundled:pubmed": {
			ID: "bundled:pubmed", Name: "pubmed", DisplayName: "PubMed",
			Config: mcpstdio.ServerConfig{Type: "streamable-http", URL: publicURL, Scope: "bundled"},
		},
	}
	if !service.ScheduleMissingBundledProbe("owner-new") {
		t.Fatal("owner bootstrap was not scheduled")
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("owner bootstrap did not reach tools/list")
	}
	if _, err := service.SetUnifiedEnabled(context.Background(), "owner-new", "bundled:pubmed", false); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		service.bootstrapMu.Lock()
		running := len(service.bootstrapOwners) != 0
		service.bootstrapMu.Unlock()
		if !running {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	connectors, err := service.ListUnifiedConnectors(context.Background(), "owner-new")
	if err != nil {
		t.Fatal(err)
	}
	if len(connectors) != 1 || connectors[0].Enabled || connectors[0].ConnectionStatus != "disabled" {
		t.Fatalf("concurrent explicit disable was overwritten by bootstrap: %#v", connectors)
	}
}

func TestClosedServiceDoesNotScheduleBundledProbe(t *testing.T) {
	service := New(nil, t.TempDir(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if service.ScheduleMissingBundledProbe("owner-after-close") {
		t.Fatal("closed MCP directory service accepted a background probe")
	}
	if err := service.Close(ctx); err != nil {
		t.Fatalf("idempotent close: %v", err)
	}
}
