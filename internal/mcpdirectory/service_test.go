package mcpdirectory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

func TestDirectoryReconcileUsesTLSManifestETagAndRealMCPProbe(t *testing.T) {
	var mu sync.Mutex
	etag := `"v1"`
	includeConnector := true
	catalogRequests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/catalog":
			mu.Lock()
			defer mu.Unlock()
			catalogRequests++
			if r.Header.Get("X-Synon-Catalog-Uuid") != "4f5b9638-2dc3-4b70-bbea-9af732ba57ef" {
				t.Errorf("catalog header = %q", r.Header.Get("X-Synon-Catalog-Uuid"))
			}
			if r.Header.Get("If-None-Match") == etag && catalogRequests > 1 {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", etag)
			connectors := []map[string]any{}
			if includeConnector {
				connectors = append(connectors, map[string]any{
					"id": "directory:literature", "name": "Literature",
					"description": "Remote evidence tools", "url": serverURL(r) + "/mcp",
					"transport": "http", "authRequired": false,
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"catalogUuid": "4f5b9638-2dc3-4b70-bbea-9af732ba57ef",
				"connectors":  connectors,
			})
		case "/mcp":
			var request struct {
				ID     any    `json:"id"`
				Method string `json:"method"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode MCP request: %v", err)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			switch request.Method {
			case "server/discover":
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
			case "initialize":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0", "id": request.ID,
					"result": map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}},
				})
			case "notifications/initialized":
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{}})
			case "tools/list":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0", "id": request.ID,
					"result": map[string]any{"tools": []map[string]any{{
						"name": "search", "description": "Search evidence",
						"inputSchema": map[string]any{"type": "object"},
					}}},
				})
			default:
				t.Errorf("unexpected MCP method %q", request.Method)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, publicURL := publicMappedTLSClient(t, server)
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := New(store, t.TempDir(), client)
	directory, result, err := service.AddDirectory(context.Background(), "user-1", AddDirectoryInput{
		Name: "Primary", URL: publicURL + "/catalog",
		CatalogUUID: "4f5b9638-2dc3-4b70-bbea-9af732ba57ef",
	})
	if err != nil {
		t.Fatal(err)
	}
	if directory.ID == "" || result.Status != "ok" || result.ConnectorCount != 1 {
		t.Fatalf("directory=%#v result=%#v", directory, result)
	}
	connectors, err := service.ListConnectors(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(connectors) != 1 || connectors[0].ConnectionStatus != "connected" ||
		connectors[0].ToolCount != 1 || connectors[0].Source != "directory" {
		t.Fatalf("connectors = %#v", connectors)
	}
	if foreign, err := service.ListConnectors(context.Background(), "user-2"); err != nil || len(foreign) != 0 {
		t.Fatalf("cross-user connectors = %#v, %v", foreign, err)
	}
	results, err := service.Reconcile(context.Background(), "user-1")
	if err != nil || len(results) != 1 || results[0].Status != "not-modified" {
		t.Fatalf("not-modified reconcile = %#v, %v", results, err)
	}

	mu.Lock()
	etag = `"v2"`
	includeConnector = false
	mu.Unlock()
	results, err = service.Reconcile(context.Background(), "user-1")
	if err != nil || len(results) != 1 || results[0].ConnectorCount != 0 {
		t.Fatalf("stale reconcile = %#v, %v", results, err)
	}
	connectors, err = service.ListConnectors(context.Background(), "user-1")
	if err != nil || len(connectors) != 0 {
		t.Fatalf("stale connector remained = %#v, %v", connectors, err)
	}
}

func TestAddDirectoryRejectsInsecureURLAndInvalidCatalog(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := New(store, t.TempDir(), http.DefaultClient)
	for _, input := range []AddDirectoryInput{
		{Name: "bad", URL: "http://example.test/catalog", CatalogUUID: "4f5b9638-2dc3-4b70-bbea-9af732ba57ef"},
		{Name: "bad", URL: "https://example.test/catalog", CatalogUUID: "not-a-uuid"},
	} {
		if _, _, err := service.AddDirectory(context.Background(), "user-1", input); err == nil {
			t.Fatalf("invalid directory accepted: %#v", input)
		}
	}
}

func serverURL(r *http.Request) string {
	return "https://" + r.Host
}
func TestConnectorPageAndDirectoryHealthAreOwnerScopedFilteredAndDurable(t *testing.T) {
	var toolListCalls atomic.Int64
	catalog := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/catalog":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"catalogUuid": "4f5b9638-2dc3-4b70-bbea-9af732ba57ef",
				"connectors": []map[string]any{{
					"id": "directory:literature", "name": "Literature Search",
					"description": "Search biomedical literature", "url": serverURL(r) + "/mcp",
					"transport": "http", "authRequired": false,
				}},
			})
		case "/broken":
			http.Error(w, "catalog unavailable", http.StatusServiceUnavailable)
		case "/mcp":
			var request struct {
				ID     any    `json:"id"`
				Method string `json:"method"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode MCP request: %v", err)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			switch request.Method {
			case "server/discover":
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
			case "initialize":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0", "id": request.ID,
					"result": map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}},
				})
			case "notifications/initialized":
				w.WriteHeader(http.StatusAccepted)
			case "tools/list":
				toolListCalls.Add(1)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0", "id": request.ID,
					"result": map[string]any{"tools": []map[string]any{
						{"name": "search", "inputSchema": map[string]any{"type": "object"}},
						{"name": "fetch", "inputSchema": map[string]any{"type": "object"}},
					}},
				})
			default:
				t.Errorf("unexpected MCP method %q", request.Method)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer catalog.Close()

	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	client, publicURL := publicMappedTLSClient(t, catalog)
	root := t.TempDir()
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	service := New(store, root, client)
	for _, userID := range []string{"user-1", "user-2"} {
		if _, result, err := service.AddDirectory(context.Background(), userID, AddDirectoryInput{
			Name: "Primary", URL: publicURL + "/catalog",
			CatalogUUID: "4f5b9638-2dc3-4b70-bbea-9af732ba57ef",
		}); err != nil || result.Status != "ok" {
			t.Fatalf("add directory for %s: result=%#v err=%v", userID, result, err)
		}
	}
	if _, result, err := service.AddDirectory(context.Background(), "user-1", AddDirectoryInput{
		Name: "Broken", URL: publicURL + "/broken",
		CatalogUUID: "11111111-1111-4111-8111-111111111111",
	}); err == nil || result.Status != "error" {
		t.Fatalf("broken directory result=%#v err=%v", result, err)
	}
	if toolListCalls.Load() != 2 {
		t.Fatalf("real tools/list calls=%d, want 2", toolListCalls.Load())
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service = New(store, root, client)

	page, err := service.ListConnectorPage(context.Background(), "user-1", ConnectorListOptions{
		Source: "directory", Status: "connected", Query: "literature", Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Limit != 1 || page.Offset != 0 || len(page.Connectors) != 1 {
		t.Fatalf("connector page=%#v", page)
	}
	connector := page.Connectors[0]
	if connector.ID != "directory:literature" || connector.AuthState != "not-required" ||
		connector.ConnectionStatus != "connected" || connector.ToolCount != 2 ||
		connector.Health == nil || !connector.Health.OK || connector.Health.ToolCount != 2 {
		t.Fatalf("connector projection=%#v", connector)
	}
	foreign, err := service.ListConnectorPage(context.Background(), "user-2", ConnectorListOptions{})
	if err != nil || foreign.Total != 1 || len(foreign.Connectors) != 1 {
		t.Fatalf("foreign owner page=%#v err=%v", foreign, err)
	}
	if _, err := service.ListConnectorPage(context.Background(), "user-1", ConnectorListOptions{Source: "unknown"}); err == nil {
		t.Fatal("unknown connector source must fail")
	}
	if _, err := service.ListConnectorPage(context.Background(), "user-1", ConnectorListOptions{Limit: -1}); err == nil {
		t.Fatal("negative connector limit must fail")
	}

	health, err := service.DirectoryHealth(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if health.OK || health.Total != 2 || health.Healthy != 1 || health.Failed != 1 || len(health.Directories) != 2 {
		t.Fatalf("user-1 directory health=%#v", health)
	}
	foreignHealth, err := service.DirectoryHealth(context.Background(), "user-2")
	if err != nil {
		t.Fatal(err)
	}
	if !foreignHealth.OK || foreignHealth.Total != 1 || foreignHealth.Healthy != 1 ||
		foreignHealth.Failed != 0 || len(foreignHealth.Directories) != 1 {
		t.Fatalf("user-2 directory health=%#v", foreignHealth)
	}
	if toolListCalls.Load() != 2 {
		t.Fatalf("durable health unexpectedly re-probed tools, calls=%d", toolListCalls.Load())
	}
}
func TestDirectoryOAuthIsOwnerScopedAndUsesRealPKCEAndMCPProbe(t *testing.T) {
	var tokenExchanges atomic.Int64
	var bearerProbes atomic.Int64
	var authServer *httptest.Server
	var authPublicURL string
	authServer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": authPublicURL, "authorization_endpoint": authPublicURL + "/authorize",
				"token_endpoint":           authPublicURL + "/token",
				"response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code"},
				"code_challenge_methods_supported": []string{"S256"},
			})
		case "/authorize":
			redirectURI := r.URL.Query().Get("redirect_uri")
			state := r.URL.Query().Get("state")
			if redirectURI == "" || state == "" || r.URL.Query().Get("code_challenge") == "" {
				t.Errorf("invalid authorize query: %s", r.URL.RawQuery)
				http.Error(w, "invalid authorize query", http.StatusBadRequest)
				return
			}
			http.Redirect(w, r, redirectURI+"?code=directory-code&state="+state, http.StatusFound)
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse token form: %v", err)
				http.Error(w, "invalid token form", http.StatusBadRequest)
				return
			}
			if r.Form.Get("code") != "directory-code" || r.Form.Get("code_verifier") == "" {
				t.Errorf("invalid token form: %#v", r.Form)
				http.Error(w, "invalid token form", http.StatusBadRequest)
				return
			}
			tokenExchanges.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "directory-access-token", "token_type": "Bearer", "expires_in": 3600,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer authServer.Close()

	var connectorServer *httptest.Server
	connectorServer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/catalog":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"catalogUuid": "4f5b9638-2dc3-4b70-bbea-9af732ba57ef",
				"connectors": []map[string]any{{
					"id": "directory:secure", "name": "Secure Evidence",
					"url": "https://" + r.Host + "/mcp", "transport": "http",
					"authRequired": true, "authHint": "Sign in",
				}},
			})
		case "/mcp":
			if r.Header.Get("Authorization") != "Bearer directory-access-token" {
				w.Header().Set("WWW-Authenticate",
					`Bearer authorization_metadata="`+authPublicURL+`/.well-known/oauth-authorization-server"`)
				http.Error(w, "authorization required", http.StatusUnauthorized)
				return
			}
			bearerProbes.Add(1)
			var request struct {
				ID     any    `json:"id"`
				Method string `json:"method"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode MCP request: %v", err)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			switch request.Method {
			case "server/discover":
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
			case "initialize":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0", "id": request.ID,
					"result": map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}},
				})
			case "notifications/initialized":
				w.WriteHeader(http.StatusAccepted)
			case "tools/list":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0", "id": request.ID,
					"result": map[string]any{"tools": []map[string]any{{
						"name": "secure_search", "inputSchema": map[string]any{"type": "object"},
					}}},
				})
			default:
				t.Errorf("unexpected MCP method %q", request.Method)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer connectorServer.Close()

	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	authPublicURL = publicMappedServerURL(t, authServer)
	client, publicURL := publicMappedTLSClient(t, connectorServer, authServer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := New(store, t.TempDir(), client)
	for _, userID := range []string{"user-1", "user-2"} {
		_, result, err := service.AddDirectory(context.Background(), userID, AddDirectoryInput{
			Name: "Secure", URL: publicURL + "/catalog",
			CatalogUUID: "4f5b9638-2dc3-4b70-bbea-9af732ba57ef",
		})
		if err != nil || result.Status != "ok" {
			t.Fatalf("add secure directory for %s: result=%#v err=%v", userID, result, err)
		}
	}

	started, err := service.Authorize(context.Background(), "user-1", "directory:secure")
	if err != nil {
		t.Fatal(err)
	}
	if started.Status != "auth_url" || started.AuthURL == "" || started.RedirectURI == "" {
		t.Fatalf("authorize result=%#v", started)
	}
	response, err := client.Get(started.AuthURL)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()

	var userOne Connector
	for attempt := 0; attempt < 50; attempt++ {
		if _, err := service.SetEnabled(context.Background(), "user-1", "directory:secure", false); err != nil {
			t.Fatal(err)
		}
		userOne, err = service.SetEnabled(context.Background(), "user-1", "directory:secure", true)
		if err == nil && userOne.ConnectionStatus == "connected" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if userOne.ConnectionStatus != "connected" || userOne.AuthState != "authorized" ||
		tokenExchanges.Load() != 1 || bearerProbes.Load() == 0 {
		t.Fatalf("authorized owner connector=%#v exchanges=%d bearerProbes=%d",
			userOne, tokenExchanges.Load(), bearerProbes.Load())
	}
	userTwo, err := service.ListConnectorPage(context.Background(), "user-2", ConnectorListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(userTwo.Connectors) != 1 || userTwo.Connectors[0].ConnectionStatus != "auth_required" ||
		userTwo.Connectors[0].AuthState != "unauthorized" {
		t.Fatalf("foreign owner inherited authorization: %#v", userTwo)
	}

	if err := service.Disconnect(context.Background(), "user-2", "directory:secure"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetEnabled(context.Background(), "user-1", "directory:secure", false); err != nil {
		t.Fatal(err)
	}
	userOne, err = service.SetEnabled(context.Background(), "user-1", "directory:secure", true)
	if err != nil {
		t.Fatal(err)
	}
	if userOne.ConnectionStatus != "connected" || userOne.AuthState != "authorized" {
		t.Fatalf("foreign disconnect removed owner token: %#v", userOne)
	}

	if _, err := service.Authorize(context.Background(), "user-3", "directory:secure"); err == nil {
		t.Fatal("foreign owner must receive not found")
	}
	if _, err := service.SetEnabled(context.Background(), "user-2", "directory:secure", false); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authorize(context.Background(), "user-2", "directory:secure"); err == nil {
		t.Fatal("disabled connector must not start authorization")
	}
}
