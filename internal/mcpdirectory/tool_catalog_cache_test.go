package mcpdirectory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/tools/mcpstdio"
)

func TestUnifiedConnectorToolCatalogSurvivesRestartAndProviderOutage(t *testing.T) {
	var unavailable atomic.Bool
	var toolsListCalls atomic.Int64
	provider := newToolCatalogTestServer(t, &unavailable, &toolsListCalls, nil)
	defer provider.Close()
	client, publicURL := publicMappedTLSClient(t, provider)
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	service := newToolCatalogTestService(store, t.TempDir(), client, publicURL)
	tools, err := service.ListUnifiedConnectorTools(context.Background(), "owner-a", "bundled:pubmed")
	if err != nil {
		t.Fatal(err)
	}
	assertCachedPubMedTool(t, tools)
	if toolsListCalls.Load() != 1 {
		t.Fatalf("initial tools/list calls=%d", toolsListCalls.Load())
	}
	catalog, found, err := store.GetMCPConnectorToolCatalog("owner-a", "bundled", "bundled:pubmed")
	if err != nil || !found {
		t.Fatalf("catalog found=%t err=%v", found, err)
	}
	if strings.Contains(catalog.CatalogJSON, "catalog-test-secret") || strings.Contains(catalog.CatalogJSON, publicURL) {
		t.Fatalf("connector configuration leaked into persisted tool catalog: %s", catalog.CatalogJSON)
	}
	closeServiceForToolCatalogTest(t, service)

	unavailable.Store(true)
	restarted := newToolCatalogTestService(store, t.TempDir(), client, publicURL)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	tools, err = restarted.ListUnifiedConnectorTools(ctx, "owner-a", "bundled:pubmed")
	if err != nil {
		t.Fatalf("cached restart discovery reached unavailable provider: %v", err)
	}
	assertCachedPubMedTool(t, tools)
	if toolsListCalls.Load() != 1 {
		t.Fatalf("cached restart re-probed provider: calls=%d", toolsListCalls.Load())
	}
	closeServiceForToolCatalogTest(t, restarted)
}

func TestConnectorToolCatalogIdentityIncludesBundledDefinition(t *testing.T) {
	base := RuntimeConnector{
		ID: "bundled:drug-regulatory", Name: "drug-regulatory", Source: "bundled",
		DefinitionSHA256: strings.Repeat("a", 64),
		Config:           mcpstdio.ServerConfig{Type: "stdio", Command: "python3", Args: []string{"run_server.py", "mcp_drug_regulatory"}},
	}
	changed := base
	changed.DefinitionSHA256 = strings.Repeat("b", 64)
	if connectorToolConfigurationSHA256(base) == connectorToolConfigurationSHA256(changed) {
		t.Fatal("bundled asset identity did not invalidate the durable tool catalog")
	}
}

func TestBundledRuntimeConnectorAuthorityIgnoresEphemeralEnvironmentRefresh(t *testing.T) {
	base := RuntimeConnector{
		ID: "bundled:pubmed", Name: "PubMed", Source: "bundled", DefinitionSHA256: strings.Repeat("a", 64),
		Config: mcpstdio.ServerConfig{Type: "stdio", Command: "python3", Env: map[string]string{"OPERON_CONTACT_EMAIL": "old@example.invalid"}},
	}
	refreshed := base
	refreshed.Config.Env = map[string]string{"OPERON_CONTACT_EMAIL": "new@example.invalid"}
	if !runtimeConnectorAuthorityEquivalent(refreshed, base) {
		t.Fatal("bundled ephemeral environment refresh was treated as authority change")
	}
	changedDefinition := base
	changedDefinition.DefinitionSHA256 = strings.Repeat("b", 64)
	if runtimeConnectorAuthorityEquivalent(changedDefinition, base) {
		t.Fatal("bundled definition change was treated as equivalent")
	}
	changedDisabled := base
	changedDisabled.Config.Disabled = true
	if runtimeConnectorAuthorityEquivalent(changedDisabled, base) {
		t.Fatal("bundled disabled-state change was treated as equivalent")
	}
	custom := base
	custom.Source = "custom"
	custom.Config.Env = map[string]string{"TOKEN": "changed"}
	if runtimeConnectorAuthorityEquivalent(custom, base) {
		t.Fatal("custom connector refresh was treated as equivalent")
	}
}

func TestUnifiedConnectorToolCatalogUsesStaleWhileRevalidateButRejectsExpiredAndConfigChanged(t *testing.T) {
	var unavailable atomic.Bool
	provider := newToolCatalogTestServer(t, &unavailable, nil, nil)
	defer provider.Close()
	client, publicURL := publicMappedTLSClient(t, provider)
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := newToolCatalogTestService(store, t.TempDir(), client, publicURL)
	if _, err := service.ListUnifiedConnectorTools(context.Background(), "owner-a", "bundled:pubmed"); err != nil {
		t.Fatal(err)
	}
	closeServiceForToolCatalogTest(t, service)
	unavailable.Store(true)

	catalog, found, err := store.GetMCPConnectorToolCatalog("owner-a", "bundled", "bundled:pubmed")
	if err != nil || !found {
		t.Fatalf("catalog found=%t err=%v", found, err)
	}
	catalog.RefreshedAt = time.Now().UTC().Add(-10 * time.Minute)
	if err := store.PutMCPConnectorToolCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	stale := newToolCatalogTestService(store, t.TempDir(), client, publicURL)
	tools, err := stale.ListUnifiedConnectorTools(context.Background(), "owner-a", "bundled:pubmed")
	if err != nil {
		t.Fatalf("stale-while-revalidate did not serve valid cache: %v", err)
	}
	assertCachedPubMedTool(t, tools)
	closeServiceForToolCatalogTest(t, stale)

	catalog.RefreshedAt = time.Now().UTC().Add(-8 * 24 * time.Hour)
	if err := store.PutMCPConnectorToolCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	expired := newToolCatalogTestService(store, t.TempDir(), client, publicURL)
	if _, err := expired.ListUnifiedConnectorTools(context.Background(), "owner-a", "bundled:pubmed"); err == nil {
		t.Fatal("expired tool catalog was served while provider was unavailable")
	}
	closeServiceForToolCatalogTest(t, expired)

	catalog.RefreshedAt = time.Now().UTC()
	if err := store.PutMCPConnectorToolCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	changed := newToolCatalogTestService(store, t.TempDir(), client, publicURL)
	item := changed.bundled["bundled:pubmed"]
	item.Config.Scope = "changed-authority"
	changed.bundled["bundled:pubmed"] = item
	if _, err := changed.ListUnifiedConnectorTools(context.Background(), "owner-a", "bundled:pubmed"); err == nil {
		t.Fatal("configuration-changed connector reused an old tool catalog")
	}
	closeServiceForToolCatalogTest(t, changed)
}

func TestUnifiedConnectorToolCatalogStableWaitsForStaleRefresh(t *testing.T) {
	var toolsListCalls atomic.Int64
	provider := newToolCatalogTestServer(t, nil, &toolsListCalls, nil)
	defer provider.Close()
	client, publicURL := publicMappedTLSClient(t, provider)
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	service := newToolCatalogTestService(store, t.TempDir(), client, publicURL)
	if _, err := service.ListUnifiedConnectorTools(context.Background(), "owner-a", "bundled:pubmed"); err != nil {
		t.Fatal(err)
	}
	catalog, found, err := store.GetMCPConnectorToolCatalog("owner-a", "bundled", "bundled:pubmed")
	if err != nil || !found {
		t.Fatalf("catalog found=%t err=%v", found, err)
	}
	catalog.RefreshedAt = time.Now().UTC().Add(-10 * time.Minute)
	if err := store.PutMCPConnectorToolCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	stable, err := service.ListUnifiedConnectorToolsStable(context.Background(), "owner-a", "bundled:pubmed")
	if err != nil {
		t.Fatalf("stable catalog refresh failed: %v", err)
	}
	assertCachedPubMedTool(t, stable)
	if toolsListCalls.Load() != 2 {
		t.Fatalf("stable catalog did not wait for one refresh: tools/list calls=%d", toolsListCalls.Load())
	}
	closeServiceForToolCatalogTest(t, service)
}

func TestUnifiedConnectorToolCatalogBacksOffRepeatedProviderFailures(t *testing.T) {
	var unavailable atomic.Bool
	var providerRequests atomic.Int64
	provider := newToolCatalogTestServerWithRequests(t, &unavailable, nil, &providerRequests, nil)
	defer provider.Close()
	client, publicURL := publicMappedTLSClient(t, provider)
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := newToolCatalogTestService(store, t.TempDir(), client, publicURL)
	unavailable.Store(true)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := service.ListUnifiedConnectorTools(ctx, "owner-a", "bundled:pubmed"); err == nil {
		t.Fatal("unavailable provider unexpectedly returned a tool catalog")
	}
	initialRequests := providerRequests.Load()
	if initialRequests == 0 {
		t.Fatal("initial unavailable provider attempt did not reach the provider")
	}
	if _, err := service.ListUnifiedConnectorTools(ctx, "owner-a", "bundled:pubmed"); err == nil {
		t.Fatal("backoff call unexpectedly returned a tool catalog")
	}
	if calls := providerRequests.Load(); calls != initialRequests {
		t.Fatalf("backoff reprobed unavailable provider: requests=%d initial=%d", calls, initialRequests)
	}
	closeServiceForToolCatalogTest(t, service)
}

func TestUnifiedConnectorToolCatalogSkipsUnauthorizedConnector(t *testing.T) {
	var providerRequests atomic.Int64
	provider := newToolCatalogTestServerWithRequests(t, nil, nil, &providerRequests, nil)
	defer provider.Close()
	client, publicURL := publicMappedTLSClient(t, provider)
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store, t.TempDir(), client)
	service.bundled = map[string]bundledConnector{
		"bundled:private": {
			ID: "bundled:private", Name: "private", AuthRequired: true,
			Config: mcpstdio.ServerConfig{Type: "streamable-http", URL: publicURL, Scope: "bundled"},
		},
	}
	_, err = service.ListUnifiedConnectorTools(context.Background(), "owner-a", "bundled:private")
	if !errors.Is(err, ErrConnectorAuthorizationRequired) {
		t.Fatalf("unauthorized connector error=%v", err)
	}
	if calls := providerRequests.Load(); calls != 0 {
		t.Fatalf("unauthorized connector reached provider: requests=%d", calls)
	}
	closeServiceForToolCatalogTest(t, service)
}

func TestBundledToolCatalogWarmupAndRuntimeDiscoveryShareOneFlight(t *testing.T) {
	var toolsListCalls atomic.Int64
	started := make(chan struct{})
	release := make(chan struct{})
	provider := newToolCatalogTestServer(t, nil, &toolsListCalls, func() {
		close(started)
		<-release
	})
	defer provider.Close()
	client, publicURL := publicMappedTLSClient(t, provider)
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := newToolCatalogTestService(store, t.TempDir(), client, publicURL)
	if !service.ScheduleBundledToolCatalogWarmup("owner-a") {
		t.Fatal("catalog warmup was not scheduled")
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("catalog warmup did not reach tools/list")
	}
	var wg sync.WaitGroup
	results := make(chan error, 6)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			tools, err := service.ListUnifiedConnectorTools(ctx, "owner-a", "bundled:pubmed")
			if err == nil {
				err = cachedPubMedToolError(tools)
			}
			results <- err
		}()
	}
	close(release)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if toolsListCalls.Load() != 1 {
		t.Fatalf("warmup/runtime singleflight tools/list calls=%d", toolsListCalls.Load())
	}
	closeServiceForToolCatalogTest(t, service)
}

func newToolCatalogTestService(store *workspace.Store, root string, client *http.Client, publicURL string) *Service {
	service := New(store, root, client)
	service.bundled = map[string]bundledConnector{
		"bundled:pubmed": {
			ID: "bundled:pubmed", Name: "pubmed", DisplayName: "PubMed",
			Config: mcpstdio.ServerConfig{
				Type: "streamable-http", URL: publicURL, Scope: "bundled",
				Headers: map[string]string{"Authorization": "Bearer catalog-test-secret"},
			},
		},
	}
	return service
}

func newToolCatalogTestServer(t *testing.T, unavailable *atomic.Bool, calls *atomic.Int64, onToolsList func()) *httptest.Server {
	return newToolCatalogTestServerWithRequests(t, unavailable, calls, nil, onToolsList)
}

func newToolCatalogTestServerWithRequests(t *testing.T, unavailable *atomic.Bool, toolsListCalls, providerRequests *atomic.Int64, onToolsList func()) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if providerRequests != nil {
			providerRequests.Add(1)
		}
		// Count attempted tools/list requests even when the provider is down. This
		// keeps the failure-backoff regression observable instead of reporting zero
		// because the synthetic outage short-circuited before method dispatch.
		if request.Method == "tools/list" && toolsListCalls != nil {
			toolsListCalls.Add(1)
		}
		if unavailable != nil && unavailable.Load() {
			http.Error(w, "provider unavailable", http.StatusServiceUnavailable)
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
			if onToolsList != nil {
				onToolsList()
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"tools": []map[string]any{{
				"name": "search_articles", "description": "Search PubMed",
				"inputSchema":  map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}},
				"outputSchema": map[string]any{"type": "object", "properties": map[string]any{"pmids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}},
				"annotations":  map[string]any{"readOnlyHint": true},
			}}}})
		default:
			t.Errorf("method=%q", request.Method)
		}
	}))
}

func assertCachedPubMedTool(t *testing.T, tools []mcpstdio.ToolProjection) {
	t.Helper()
	if err := cachedPubMedToolError(tools); err != nil {
		t.Fatal(err)
	}
}

func cachedPubMedToolError(tools []mcpstdio.ToolProjection) error {
	if len(tools) != 1 || tools[0].Name != "mcp__pubmed__search_articles" ||
		tools[0].ToolName != "search_articles" || !tools[0].ReadOnlyHint ||
		len(tools[0].InputProperties) != 1 || tools[0].InputProperties[0] != "query" ||
		!tools[0].HasOutputSchema || mapValueForToolCatalogTest(tools[0].OutputSchema["properties"])["pmids"] == nil {
		return fmt.Errorf("PubMed tool projection=%#v", tools)
	}
	return nil
}

func mapValueForToolCatalogTest(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func closeServiceForToolCatalogTest(t *testing.T, service *Service) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := service.Close(ctx); err != nil {
		t.Fatalf("close MCP directory service: %v", err)
	}
}
