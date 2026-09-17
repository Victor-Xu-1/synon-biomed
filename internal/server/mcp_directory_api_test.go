package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	workspace "synon-go/internal/persistence/workspace"
)

func TestMCPDirectoryHTTPAPIUsesBaselineRoutes(t *testing.T) {
	etag := string([]byte{34}) + "v1" + string([]byte{34})
	catalog := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/catalog" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-Synon-Catalog-Uuid") != "4f5b9638-2dc3-4b70-bbea-9af732ba57ef" {
			t.Errorf("catalog UUID header = %q", r.Header.Get("X-Synon-Catalog-Uuid"))
		}
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"catalogUuid": "4f5b9638-2dc3-4b70-bbea-9af732ba57ef",
			"connectors": []map[string]any{{
				"id": "directory:private", "name": "Private", "url": "https://" + r.Host + "/mcp",
				"transport": "http", "authRequired": true,
			}},
		})
	}))
	defer catalog.Close()
	catalogURL, catalogClient := mcpDirectoryPublicTLSClient(t, catalog)

	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := New(Options{
		Workspace: store, FileRoot: t.TempDir(), HTTPClient: catalogClient,
	})
	app := server.Handler()
	httpServer := httptest.NewServer(app)
	t.Cleanup(httpServer.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	websocketURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") +
		"/api/events/ws?userId=user-1&after_sequence=0"
	liveConnection, _, err := websocket.Dial(ctx, websocketURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if connected := readCompatWebSocketTest(t, ctx, liveConnection); connected["type"] != "connected" {
		t.Fatalf("live websocket handshake=%#v", connected)
	}
	invalidAdd := httptest.NewRecorder()
	invalidAddRequest := authenticatedWorkspaceRequest(http.MethodPost, "/api/mcp-servers/directory", "user-1", bytes.NewBufferString(`{}`))
	invalidAddRequest.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(invalidAdd, invalidAddRequest)
	if invalidAdd.Code != http.StatusBadRequest || !bytes.Equal(bytes.TrimSpace(invalidAdd.Body.Bytes()), []byte(`{"error":"invalid directory add body"}`)) {
		t.Fatalf("invalid directory add=%d: %s", invalidAdd.Code, invalidAdd.Body.String())
	}
	invalidAuthorize := httptest.NewRecorder()
	app.ServeHTTP(invalidAuthorize, authenticatedWorkspaceRequest(http.MethodPost,
		"/api/mcp-servers/connectors/directory:missing/authorize", "user-1", nil))
	if invalidAuthorize.Code != http.StatusBadRequest || !bytes.Equal(bytes.TrimSpace(invalidAuthorize.Body.Bytes()), []byte(`{"detail":"Invalid arguments: id: Invalid uuid"}`)) {
		t.Fatalf("invalid authorize=%d: %s", invalidAuthorize.Code, invalidAuthorize.Body.String())
	}
	body, err := json.Marshal(map[string]any{
		"name": "Primary", "url": catalogURL + "/catalog",
		"catalogUuid": "4f5b9638-2dc3-4b70-bbea-9af732ba57ef",
	})
	if err != nil {
		t.Fatal(err)
	}
	add := httptest.NewRecorder()
	addRequest := authenticatedWorkspaceRequest(http.MethodPost, "/api/mcp-servers/directory?user_id=user-1", "user-1", bytes.NewReader(body))
	addRequest.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(add, addRequest)
	if add.Code != http.StatusOK || !bytes.Contains(add.Body.Bytes(), []byte("connectorCount")) {
		t.Fatalf("add directory = %d: %s", add.Code, add.Body.String())
	}
	if _, err := store.CreateMCPServer(workspace.MCPServerInput{
		ID: "custom:archive", UserID: "user-1", Name: "Archive", URL: "https://archive.example.test/mcp", Transport: "streamable-http",
	}); err != nil {
		t.Fatal(err)
	}
	connectors := httptest.NewRecorder()
	app.ServeHTTP(connectors, authenticatedWorkspaceRequest(http.MethodGet, "/api/mcp-servers/connectors?user_id=user-1", "user-1", nil))
	if connectors.Code != http.StatusOK || !bytes.Contains(connectors.Body.Bytes(), []byte("connectionStatus")) || !bytes.Contains(connectors.Body.Bytes(), []byte("auth_required")) || !bytes.Contains(connectors.Body.Bytes(), []byte("custom:archive")) {
		t.Fatalf("connectors = %d: %s", connectors.Code, connectors.Body.String())
	}
	health := httptest.NewRecorder()
	app.ServeHTTP(health, authenticatedWorkspaceRequest(http.MethodGet, "/api/mcp-servers/directory-health?user_id=user-1", "user-1", nil))
	if health.Code != http.StatusOK || !bytes.Contains(health.Body.Bytes(), []byte("lastStatus")) ||
		!bytes.Contains(health.Body.Bytes(), []byte("runtimeSummary")) ||
		!bytes.Contains(health.Body.Bytes(), []byte("repl/host.mcp")) ||
		!bytes.Contains(health.Body.Bytes(), []byte("flattenedModelTools")) {
		t.Fatalf("directory health = %d: %s", health.Code, health.Body.String())
	}
	reconcile := httptest.NewRecorder()
	app.ServeHTTP(reconcile, authenticatedWorkspaceRequest(http.MethodPost, "/api/mcp-servers/reconcile?user_id=user-1", "user-1", nil))
	if reconcile.Code != http.StatusOK || !bytes.Equal(bytes.TrimSpace(reconcile.Body.Bytes()), []byte("{}")) {
		t.Fatalf("reconcile = %d: %s", reconcile.Code, reconcile.Body.String())
	}
	disabled := httptest.NewRecorder()
	disableBody, err := json.Marshal(map[string]any{"enabled": false})
	if err != nil {
		t.Fatal(err)
	}
	disableRequest := authenticatedWorkspaceRequest(http.MethodPut, "/api/mcp-servers/connectors/directory:private/enabled?user_id=user-1", "user-1", bytes.NewReader(disableBody))
	disableRequest.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(disabled, disableRequest)
	if disabled.Code != http.StatusOK || !bytes.Contains(disabled.Body.Bytes(), []byte("enabled")) {
		t.Fatalf("disable connector = %d: %s", disabled.Code, disabled.Body.String())
	}
	realtime := runtimeCompatJSON(t, app, http.MethodGet, "/api/events?limit=100", "user-1", nil, http.StatusOK)
	counts := map[string]int{}
	for _, raw := range realtime["events"].([]any) {
		counts[raw.(map[string]any)["type"].(string)]++
	}
	if counts["connector_snapshot"] != 2 || counts["connector_update"] != 1 {
		t.Fatalf("MCP directory realtime event counts=%#v", counts)
	}
	target := map[string]int{"connector_snapshot": 2, "connector_update": 1}
	liveEvents, err := readDomainWebSocketEvents(ctx, liveConnection, target, 3)
	if err != nil {
		t.Fatalf("read live MCP directory events: %v", err)
	}
	if err := liveConnection.Close(websocket.StatusNormalClosure, "reconnect for replay"); err != nil {
		t.Fatal(err)
	}
	replayConnection, _, err := websocket.Dial(ctx, websocketURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer replayConnection.Close(websocket.StatusNormalClosure, "test complete")
	if connected := readCompatWebSocketTest(t, ctx, replayConnection); connected["type"] != "connected" {
		t.Fatalf("replay websocket handshake=%#v", connected)
	}
	replayedEvents, err := readDomainWebSocketEvents(ctx, replayConnection, target, 3)
	if err != nil {
		t.Fatalf("read replayed MCP directory events: %v", err)
	}
	if !reflect.DeepEqual(liveEvents, replayedEvents) {
		t.Fatalf("live and replayed directory events differ: live=%#v replay=%#v", liveEvents, replayedEvents)
	}
	for _, event := range liveEvents {
		assertDomainInvalidation(t, event, "mcpConnectors", []any{"mcp-connectors"}, "debounced", "exact")
		assertDomainInvalidation(t, event, "mcpAttachmentCounts", []any{"mcp-attachment-counts"}, "debounced", "exact")
		if event["type"] == "connector_update" {
			assertDomainInvalidation(t, event, "customMCPServer",
				[]any{"custom-mcp-server", "directory:private"}, "debounced", "exact")
			assertDomainInvalidation(t, event, "mcpToolGrants",
				[]any{"mcp-tool-grants", "directory:private"}, "debounced", "exact")
		}
	}
}
func TestMCPDirectoryConnectorDiscoveryUsesV11ArrayShapeOwnerScopeFilteringAndDurableHealth(t *testing.T) {
	var toolListCalls atomic.Int64
	t.Setenv("SYNON_BUNDLED_CONNECTOR_EXECUTABLE", "mcp-ketcher")
	catalog := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/catalog":
			w.Header().Set("ETag", `"directory-v1"`)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"catalogUuid": "4f5b9638-2dc3-4b70-bbea-9af732ba57ef",
				"connectors": []map[string]any{{
					"id": "directory:evidence", "name": "Evidence Search",
					"description": "Search durable evidence", "url": "https://" + r.Host + "/mcp",
					"transport": "http", "authRequired": false,
				}},
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
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0", "id": request.ID,
					"error": map[string]any{"code": -32601, "message": "Method not found"},
				})
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
						{"name": "search", "description": "Search evidence", "inputSchema": map[string]any{"type": "object"}},
						{"name": "fetch", "description": "Fetch evidence", "inputSchema": map[string]any{"type": "object"}},
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
	catalogURL, catalogClient := mcpDirectoryPublicTLSClient(t, catalog)

	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	root := t.TempDir()
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	app := newV11TestServer(t, Options{Workspace: store, FileRoot: root, HTTPClient: catalogClient}).Handler()
	addBody, err := json.Marshal(map[string]any{
		"name": "Primary", "url": catalogURL + "/catalog",
		"catalogUuid": "4f5b9638-2dc3-4b70-bbea-9af732ba57ef",
	})
	if err != nil {
		t.Fatal(err)
	}
	add := httptest.NewRecorder()
	addRequest := authenticatedWorkspaceRequest(http.MethodPost, "/api/mcp-servers/directory?user_id=user-1", "user-1", bytes.NewReader(addBody))
	addRequest.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(add, addRequest)
	if add.Code != http.StatusOK {
		t.Fatalf("add directory = %d: %s", add.Code, add.Body.String())
	}
	var addResult map[string]any
	if err := json.Unmarshal(add.Body.Bytes(), &addResult); err != nil {
		t.Fatal(err)
	}
	if addResult["ok"] != true || addResult["status"] != float64(http.StatusOK) || addResult["connectorCount"] != float64(1) {
		t.Fatalf("add directory result = %#v", addResult)
	}
	if _, err := store.CreateMCPServer(workspace.MCPServerInput{
		ID: "custom:archive", UserID: "user-1", Name: "Archive",
		URL: "https://archive.example.test/mcp", Transport: "streamable-http",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMCPServer(workspace.MCPServerInput{
		ID: "custom:foreign", UserID: "user-2", Name: "Foreign",
		URL: "https://foreign.example.test/mcp", Transport: "streamable-http",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app = newV11TestServer(t, Options{Workspace: store, FileRoot: root, HTTPClient: catalogClient}).Handler()

	list := httptest.NewRecorder()
	app.ServeHTTP(list, authenticatedWorkspaceRequest(http.MethodGet, "/api/mcp-servers/connectors?user_id=user-1", "user-1", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("list connectors = %d: %s", list.Code, list.Body.String())
	}
	var connectors []map[string]any
	if err := json.Unmarshal(list.Body.Bytes(), &connectors); err != nil {
		t.Fatalf("v1.1 connector response must be a bare array: %v; body=%s", err, list.Body.String())
	}
	// The bundled catalog is intentionally extensible. Assert the owner scope
	// and required directory/custom entries below instead of freezing a roster
	// count that becomes stale whenever a new connector is admitted.
	if len(connectors) < 3 {
		t.Fatalf("owner-scoped connectors unexpectedly small = %#v", connectors)
	}
	byID := map[string]map[string]any{}
	for _, connector := range connectors {
		byID[connector["id"].(string)] = connector
	}
	directory := byID["directory:evidence"]
	if directory == nil || directory["source"] != "directory" || directory["authState"] != "not-required" ||
		directory["connectionStatus"] != "connected" || directory["toolCount"] != float64(2) || directory["enabled"] != true {
		t.Fatalf("directory connector projection = %#v", directory)
	}
	health, ok := directory["health"].(map[string]any)
	if !ok || health["ok"] != true || health["toolCount"] != float64(2) {
		t.Fatalf("directory connector health = %#v", directory["health"])
	}
	if byID["custom:archive"] == nil || byID["custom:foreign"] != nil {
		t.Fatalf("custom owner scope = %#v", byID)
	}
	if toolListCalls.Load() != 1 {
		t.Fatalf("real MCP tools/list calls = %d, want 1 before durable reopen", toolListCalls.Load())
	}

	filtered := httptest.NewRecorder()
	app.ServeHTTP(filtered, authenticatedWorkspaceRequest(http.MethodGet,
		"/api/mcp-servers/connectors?user_id=user-1&source=directory&status=connected&q=evidence&limit=1&offset=0", "user-1", nil))
	if filtered.Code != http.StatusOK {
		t.Fatalf("filtered connectors = %d: %s", filtered.Code, filtered.Body.String())
	}
	var filteredConnectors []map[string]any
	if err := json.Unmarshal(filtered.Body.Bytes(), &filteredConnectors); err != nil {
		t.Fatal(err)
	}
	if len(filteredConnectors) != 1 || filteredConnectors[0]["id"] != "directory:evidence" ||
		filtered.Header().Get("X-Total-Count") != "1" || filtered.Header().Get("X-Limit") != "1" || filtered.Header().Get("X-Offset") != "0" {
		t.Fatalf("filtered page headers=%v body=%#v", filtered.Header(), filteredConnectors)
	}

	invalid := httptest.NewRecorder()
	app.ServeHTTP(invalid, authenticatedWorkspaceRequest(http.MethodGet,
		"/api/mcp-servers/connectors?user_id=user-1&limit=0", "user-1", nil))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid pagination = %d: %s", invalid.Code, invalid.Body.String())
	}

	local := httptest.NewRecorder()
	app.ServeHTTP(local, authenticatedWorkspaceRequest(http.MethodGet, "/api/mcp-servers/connectors", "local", nil))
	if local.Code != http.StatusOK {
		t.Fatalf("default local owner list = %d: %s", local.Code, local.Body.String())
	}
	var localConnectors []map[string]any
	if err := json.Unmarshal(local.Body.Bytes(), &localConnectors); err != nil {
		t.Fatalf("default local owner response must be a bare array: %v; body=%s", err, local.Body.String())
	}
	if len(localConnectors) != 33 {
		t.Fatalf("default local owner bundled catalog = %#v", localConnectors)
	}
	for _, connector := range localConnectors {
		if connector["source"] != "bundled" {
			t.Fatalf("default local owner leaked foreign connector: %#v", connector)
		}
	}
}

func TestMCPDirectoryAuthorizeHTTPUsesRealPKCEAndStrictOwnerAndDisabledStates(t *testing.T) {
	connectorID := "11111111-1111-4111-8111-111111111111"
	var tokenExchanges atomic.Int64
	authBaseURL := ""
	var authServer *httptest.Server
	authServer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": authBaseURL, "authorization_endpoint": authBaseURL + "/authorize",
				"token_endpoint":           authBaseURL + "/token",
				"response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code"},
				"code_challenge_methods_supported": []string{"S256"},
			})
		case "/authorize":
			http.Redirect(w, r, r.URL.Query().Get("redirect_uri")+"?code=http-code&state="+r.URL.Query().Get("state"), http.StatusFound)
		case "/token":
			if err := r.ParseForm(); err != nil || r.Form.Get("code") != "http-code" || r.Form.Get("code_verifier") == "" {
				t.Errorf("invalid token request: form=%#v err=%v", r.Form, err)
				http.Error(w, "invalid token request", http.StatusBadRequest)
				return
			}
			tokenExchanges.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "http-access-token", "token_type": "Bearer", "expires_in": 3600,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer authServer.Close()

	var catalog *httptest.Server
	catalog = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/catalog":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"catalogUuid": "4f5b9638-2dc3-4b70-bbea-9af732ba57ef",
				"connectors": []map[string]any{{
					"id": connectorID, "name": "HTTP Secure",
					"url": "https://" + r.Host + "/mcp", "transport": "http",
					"authRequired": true, "authHint": "Sign in",
				}},
			})
		case "/mcp":
			if r.Header.Get("Authorization") != "Bearer http-access-token" {
				w.Header().Set("WWW-Authenticate",
					`Bearer authorization_metadata="`+authBaseURL+`/.well-known/oauth-authorization-server"`)
				http.Error(w, "authorization required", http.StatusUnauthorized)
				return
			}
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
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0", "id": request.ID,
					"error": map[string]any{"code": -32601, "message": "Method not found"},
				})
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
	defer catalog.Close()
	catalogURL, resolvedAuthURL, catalogClient := mcpDirectoryPublicTLSPairClient(t, catalog, authServer)
	authBaseURL = resolvedAuthURL

	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app := New(Options{Workspace: store, FileRoot: t.TempDir(), HTTPClient: catalogClient}).Handler()
	addBody, err := json.Marshal(map[string]any{
		"name": "Secure", "url": catalogURL + "/catalog",
		"catalogUuid": "4f5b9638-2dc3-4b70-bbea-9af732ba57ef",
	})
	if err != nil {
		t.Fatal(err)
	}
	add := httptest.NewRecorder()
	addRequest := authenticatedWorkspaceRequest(http.MethodPost, "/api/mcp-servers/directory?user_id=user-1", "user-1", bytes.NewReader(addBody))
	addRequest.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(add, addRequest)
	if add.Code != http.StatusOK {
		t.Fatalf("add secure directory=%d: %s", add.Code, add.Body.String())
	}

	authorize := httptest.NewRecorder()
	app.ServeHTTP(authorize, authenticatedWorkspaceRequest(http.MethodPost,
		"/api/mcp-servers/connectors/"+connectorID+"/authorize?user_id=user-1", "user-1", nil))
	if authorize.Code != http.StatusOK {
		t.Fatalf("authorize=%d: %s", authorize.Code, authorize.Body.String())
	}
	var started map[string]any
	if err := json.Unmarshal(authorize.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started["status"] != "auth_url" || started["authUrl"] == "" || started["redirectUri"] == "" {
		t.Fatalf("authorize payload=%#v", started)
	}
	response, err := catalogClient.Get(started["authUrl"].(string))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()

	var enabled map[string]any
	for attempt := 0; attempt < 50; attempt++ {
		body := bytes.NewBufferString(`{"enabled":true}`)
		recorder := httptest.NewRecorder()
		request := authenticatedWorkspaceRequest(http.MethodPut,
			"/api/mcp-servers/connectors/"+connectorID+"/enabled?user_id=user-1", "user-1", body)
		request.Header.Set("Content-Type", "application/json")
		app.ServeHTTP(recorder, request)
		if recorder.Code == http.StatusOK {
			if err := json.Unmarshal(recorder.Body.Bytes(), &enabled); err != nil {
				t.Fatal(err)
			}
			if enabled["connectionStatus"] == "connected" {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if enabled["id"] != connectorID || enabled["enabled"] != true ||
		enabled["connectionStatus"] != "connected" || enabled["toolCount"] != float64(1) ||
		tokenExchanges.Load() != 1 {
		t.Fatalf("enabled payload=%#v tokenExchanges=%d", enabled, tokenExchanges.Load())
	}

	foreign := httptest.NewRecorder()
	app.ServeHTTP(foreign, authenticatedWorkspaceRequest(http.MethodPost,
		"/api/mcp-servers/connectors/"+connectorID+"/authorize?user_id=user-2", "user-2", nil))
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign authorize=%d: %s", foreign.Code, foreign.Body.String())
	}

	disable := httptest.NewRecorder()
	disableRequest := authenticatedWorkspaceRequest(http.MethodPut,
		"/api/mcp-servers/connectors/"+connectorID+"/enabled?user_id=user-1",
		"user-1", bytes.NewBufferString(`{"enabled":false}`))
	disableRequest.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(disable, disableRequest)
	if disable.Code != http.StatusOK {
		t.Fatalf("disable=%d: %s", disable.Code, disable.Body.String())
	}
	disabledAuthorize := httptest.NewRecorder()
	app.ServeHTTP(disabledAuthorize, authenticatedWorkspaceRequest(http.MethodPost,
		"/api/mcp-servers/connectors/"+connectorID+"/authorize?user_id=user-1", "user-1", nil))
	if disabledAuthorize.Code != http.StatusConflict {
		t.Fatalf("disabled authorize=%d: %s", disabledAuthorize.Code, disabledAuthorize.Body.String())
	}
}

func mcpDirectoryPublicTLSClient(t *testing.T, server *httptest.Server) (string, *http.Client) {
	primaryURL, _, client := mcpDirectoryPublicTLSPairClient(t, server, nil)
	return primaryURL, client
}

func mcpDirectoryPublicTLSPairClient(t *testing.T, primary, secondary *httptest.Server) (string, string, *http.Client) {
	t.Helper()
	primaryURL, err := url.Parse(primary.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, primaryPort, err := net.SplitHostPort(primaryURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	primaryAddress := net.JoinHostPort("8.8.8.8", primaryPort)
	secondaryAddress := ""
	secondaryTarget := ""
	secondaryBaseURL := ""
	roots := x509.NewCertPool()
	roots.AddCert(primary.Certificate())
	if secondary != nil {
		secondaryURL, parseErr := url.Parse(secondary.URL)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		_, secondaryPort, splitErr := net.SplitHostPort(secondaryURL.Host)
		if splitErr != nil {
			t.Fatal(splitErr)
		}
		secondaryAddress = net.JoinHostPort("8.8.4.4", secondaryPort)
		secondaryTarget = secondaryURL.Host
		secondaryBaseURL = "https://" + secondaryAddress
		roots.AddCert(secondary.Certificate())
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	baseDial := transport.DialContext
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		switch address {
		case primaryAddress:
			return baseDial(ctx, network, primaryURL.Host)
		case secondaryAddress:
			return baseDial(ctx, network, secondaryTarget)
		}
		return baseDial(ctx, network, address)
	}
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, ServerName: primaryURL.Hostname(), MinVersion: tls.VersionTLS12}
	return "https://" + primaryAddress, secondaryBaseURL, &http.Client{Transport: transport}
}
