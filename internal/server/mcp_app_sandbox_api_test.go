package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

func TestMCPAppSandboxAuthorityIsExactASCIIAndPortBound(t *testing.T) {
	tests := []struct {
		host     string
		want     string
		accepted bool
	}{
		{host: "localhost", want: "mcp-app.localhost", accepted: true},
		{host: "LOCALHOST:8765", want: "mcp-app.localhost:8765", accepted: true},
		{host: "127.0.0.1:1", want: "mcp-app.localhost:1", accepted: true},
		{host: "[::1]:65535", want: "mcp-app.localhost:65535", accepted: true},
		{host: "", accepted: false},
		{host: "attacker.test", accepted: false},
		{host: "localhost.attacker.test", accepted: false},
		{host: "localhost.", accepted: false},
		{host: "localhoſt", accepted: false},
		{host: "127.0.0.2", accepted: false},
		{host: "::1", accepted: false},
		{host: "user@localhost", accepted: false},
		{host: "localhost/path", accepted: false},
		{host: "localhost:0", accepted: false},
		{host: "localhost:", accepted: false},
		{host: "localhost:65536", accepted: false},
	}
	for _, test := range tests {
		t.Run(test.host, func(t *testing.T) {
			got, ok := mcpAppSandboxAuthority(test.host)
			if ok != test.accepted || got != test.want {
				t.Fatalf("mcpAppSandboxAuthority(%q)=(%q,%t), want (%q,%t)", test.host, got, ok, test.want, test.accepted)
			}
		})
	}
}

func TestMCPAppSandboxTicketIsOwnerResolvedSingleUseAndHostBound(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	t.Setenv("SYNON_BUNDLED_CONNECTOR_EXECUTABLE", "mcp-ketcher")
	app := newV11TestServer(t, Options{Workspace: store, FileRoot: root})
	handler := app.Handler()

	bindings := httptest.NewRecorder()
	handler.ServeHTTP(bindings, compatRequest(t, http.MethodGet, "/api/mcp-apps/viewer-bindings", "owner-a", nil))
	if bindings.Code != http.StatusOK || !strings.Contains(bindings.Body.String(), `"serverId":"bundled:ketcher-chemistry"`) {
		t.Fatalf("viewer bindings=%d: %s", bindings.Code, bindings.Body.String())
	}

	ticketRequest := compatRequest(t, http.MethodPost, "/api/mcp/apps/resource-tickets", "owner-a", map[string]any{
		"server_id": "bundled:ketcher-chemistry", "resource_uri": "ui://ketcher-chemistry/editor",
		"arguments": map[string]any{"ket": `{"root":{"nodes":[]}}`, "filename": "empty.ket"},
	})
	ticketRequest.Host = "localhost:38180"
	ticketResponse := httptest.NewRecorder()
	handler.ServeHTTP(ticketResponse, ticketRequest)
	if ticketResponse.Code != http.StatusCreated {
		t.Fatalf("ticket=%d: %s", ticketResponse.Code, ticketResponse.Body.String())
	}
	var payload struct {
		Ticket     string         `json:"ticket"`
		ToolInput  map[string]any `json:"tool_input"`
		ToolResult map[string]any `json:"tool_result"`
	}
	if err := json.Unmarshal(ticketResponse.Body.Bytes(), &payload); err != nil || len(payload.Ticket) < 32 {
		t.Fatalf("ticket payload=%q err=%v", ticketResponse.Body.String(), err)
	}
	structured, _ := payload.ToolResult["structuredContent"].(map[string]any)
	if payload.ToolInput["filename"] != "empty.ket" || structured["ket"] != `{"root":{"nodes":[]}}` {
		t.Fatalf("mount bootstrap payload=%#v", payload)
	}
	path := "/mcp-app-resource?ticket=" + payload.Ticket
	untrustedRemote := httptest.NewRecorder()
	untrustedRemoteRequest := httptest.NewRequest(http.MethodGet, path, nil)
	untrustedRemoteRequest.Host = "mcp-app.localhost:38180"
	untrustedRemoteRequest.RemoteAddr = "203.0.113.10:60000"
	handler.ServeHTTP(untrustedRemote, untrustedRemoteRequest)
	if untrustedRemote.Code != http.StatusNotFound {
		t.Fatalf("untrusted remote=%d: %s", untrustedRemote.Code, untrustedRemote.Body.String())
	}

	wrongHost := httptest.NewRecorder()
	wrongHostRequest := httptest.NewRequest(http.MethodGet, path, nil)
	wrongHostRequest.Host = "mcp-app.localhost:38181"
	wrongHostRequest.RemoteAddr = "127.0.0.1:60001"
	handler.ServeHTTP(wrongHost, wrongHostRequest)
	if wrongHost.Code != http.StatusGone {
		t.Fatalf("wrong sandbox authority=%d: %s", wrongHost.Code, wrongHost.Body.String())
	}

	resource := httptest.NewRecorder()
	resourceRequest := httptest.NewRequest(http.MethodGet, path, nil)
	resourceRequest.Host = "mcp-app.localhost:38180"
	resourceRequest.RemoteAddr = "127.0.0.1:60002"
	handler.ServeHTTP(resource, resourceRequest)
	if resource.Code != http.StatusOK || !strings.Contains(resource.Body.String(), "ketcher-chemistry") {
		t.Fatalf("resource=%d bytes=%d", resource.Code, resource.Body.Len())
	}
	wantCSP := "default-src 'none'; base-uri 'none'; object-src 'none'; form-action 'none'; frame-ancestors http://localhost:38180; script-src 'unsafe-inline' 'unsafe-eval' blob:; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; connect-src 'none'; worker-src blob:"
	if resource.Header().Get("Content-Security-Policy") != wantCSP || resource.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("sandbox headers=%#v", resource.Header())
	}
	if resource.Header().Get("Set-Cookie") != "" || resource.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("sandbox leaked ambient authority headers=%#v", resource.Header())
	}

	for _, test := range []struct {
		name      string
		ket       string
		filename  string
		forbidden string
	}{
		{name: "html-sensitive", ket: strings.Repeat("<", 700*1024), filename: "html-sensitive.ket", forbidden: `\u003c`},
		{name: "line-separator", ket: strings.Repeat("\u2028", 600*1024), filename: "line-separator.ket", forbidden: `\u2028`},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Build the request with JSON.stringify-compatible literal UTF-8.
			// json.Marshal would escape U+2028 and test a different wire contract.
			requestBody := `{"server_id":"bundled:ketcher-chemistry","resource_uri":"ui://ketcher-chemistry/editor","arguments":{"ket":"` +
				test.ket + `","filename":"` + test.filename + `"}}`
			largeTicketRequest := newLoopbackTestRequest(
				http.MethodPost, "/api/mcp/apps/resource-tickets", strings.NewReader(requestBody),
			)
			largeTicketRequest.Header.Set("Content-Type", "application/json")
			largeTicketRequest.Header.Set("X-Synon-User-Id", "owner-a")
			largeTicketRequest.Host = "localhost:38180"
			largeTicketResponse := httptest.NewRecorder()
			handler.ServeHTTP(largeTicketResponse, largeTicketRequest)
			if largeTicketResponse.Code != http.StatusCreated || largeTicketResponse.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("ticket=%d headers=%#v body=%s", largeTicketResponse.Code, largeTicketResponse.Header(), largeTicketResponse.Body.String())
			}
			if strings.Contains(largeTicketResponse.Body.String(), test.forbidden) || largeTicketResponse.Body.Len() > mcpAppTicketResponseLimit {
				t.Fatalf("ticket was escaped or exceeded client bound: bytes=%d", largeTicketResponse.Body.Len())
			}
			if largeTicketResponse.Header().Get("Content-Length") != strconv.Itoa(largeTicketResponse.Body.Len()) {
				t.Fatalf("ticket content length=%q bytes=%d", largeTicketResponse.Header().Get("Content-Length"), largeTicketResponse.Body.Len())
			}
			var largePayload struct {
				ToolInput  map[string]any `json:"tool_input"`
				ToolResult map[string]any `json:"tool_result"`
			}
			if err := json.Unmarshal(largeTicketResponse.Body.Bytes(), &largePayload); err != nil || largePayload.ToolInput["ket"] != test.ket {
				t.Fatalf("ticket payload mismatch: err=%v", err)
			}
			structured, _ := largePayload.ToolResult["structuredContent"].(map[string]any)
			if structured["ket"] != test.ket {
				t.Fatalf("ticket tool result mismatch")
			}
		})
	}

	reused := httptest.NewRecorder()
	handler.ServeHTTP(reused, resourceRequest.Clone(resourceRequest.Context()))
	if reused.Code != http.StatusGone {
		t.Fatalf("reused ticket=%d: %s", reused.Code, reused.Body.String())
	}

	for _, host := range []string{"attacker.test", "localhost.attacker.test", "localhoſt"} {
		request := compatRequest(t, http.MethodPost, "/api/mcp/apps/resource-tickets", "owner-a", map[string]any{
			"server_id": "bundled:ketcher-chemistry", "resource_uri": "ui://ketcher-chemistry/editor",
		})
		request.Host = host
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("host %q status=%d body=%s", host, response.Code, response.Body.String())
		}
	}
}

func TestMCPAppTicketJSONPreservesLiteralLineSeparatorEscapeText(t *testing.T) {
	response := httptest.NewRecorder()
	writeMCPAppTicketJSON(response, http.StatusCreated, map[string]string{
		"actual":  "\u2028\u2029",
		"literal": `\u2028\u2029`,
	})
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, "\u2028\u2029") || !strings.Contains(body, `\\u2028\\u2029`) {
		t.Fatalf("line separator encoding=%q", body)
	}
	var payload map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["actual"] != "\u2028\u2029" || payload["literal"] != `\u2028\u2029` {
		t.Fatalf("payload=%#v", payload)
	}
}

func TestMCPAppSandboxTicketExpiresAndCannotBeConsumedByAnotherAuthority(t *testing.T) {
	now := time.Date(2026, 7, 27, 1, 2, 3, 0, time.UTC)
	store := newMCPAppResourceTicketStore()
	store.now = func() time.Time { return now }
	token, _, err := store.Issue("owner-a", "server-a", "ui://app", "sha", "http://localhost:8765", "mcp-app.localhost:8765")
	if err != nil {
		t.Fatal(err)
	}
	if _, found := store.Consume(token, "mcp-app.localhost:9000"); found {
		t.Fatal("ticket was consumed by a different sandbox authority")
	}
	if _, found := store.Consume(token, "mcp-app.localhost:8765"); !found {
		t.Fatal("authority mismatch consumed or removed the ticket")
	}
	token, _, err = store.Issue("owner-a", "server-a", "ui://app", "sha", "http://localhost:8765", "mcp-app.localhost:8765")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(mcpAppResourceTicketTTL)
	if _, found := store.Consume(token, "mcp-app.localhost:8765"); found {
		t.Fatal("expired ticket was accepted")
	}
	store = newMCPAppResourceTicketStore()
	store.limit = 1
	if _, _, err := store.Issue("owner-a", "server-a", "ui://app", "sha", "http://localhost:8765", "mcp-app.localhost:8765"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Issue("owner-a", "server-a", "ui://app", "sha", "http://localhost:8765", "mcp-app.localhost:8765"); err == nil {
		t.Fatal("ticket store exceeded its bounded capacity")
	}
}

func TestMCPAppRuntimeMutationRoutesPersistScopedArtifactsAndEvents(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", UserID: "owner-a", Name: "Research", Path: root}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "frame-1", ProjectID: "project-1", AgentName: "OPERON", Status: "processing", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
	handler := New(Options{Workspace: store, FileRoot: root}).Handler()
	for _, request := range []struct {
		path       string
		body       map[string]any
		wantStatus int
	}{
		{path: "/api/mcp/apps/tool-call", body: map[string]any{
			"root_frame_id": "frame-1", "request_id": "request-1", "mount_id": "mount-1",
			"name": "render", "arguments": map[string]any{"mode": "full"},
		}, wantStatus: http.StatusAccepted},
		{path: "/api/mcp/apps/pin", body: map[string]any{
			"root_frame_id": "frame-1", "filename": "state.json", "content_type": "application/json",
			"content": `{"ready":true}`, "tool": "open_app", "arguments": map[string]any{},
		}, wantStatus: http.StatusCreated},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, compatRequest(t, http.MethodPost, request.path, "owner-a", request.body))
		if response.Code != request.wantStatus {
			t.Fatalf("%s status=%d body=%s", request.path, response.Code, response.Body.String())
		}
	}
	artifacts, err := store.ListArtifacts("project-1", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].Name != "state.json" {
		t.Fatalf("MCP app pin artifacts: %#v", artifacts)
	}
	events, err := store.ListRealtimeEvents(workspace.RealtimeEventFilter{UserID: "owner-a", IncludeGlobal: true, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	seenToolCall, seenPin := false, false
	for _, event := range events {
		seenToolCall = seenToolCall || event.Type == "mcp_app_tool_call"
		seenPin = seenPin || event.Type == "mcp_app_pin"
	}
	if !seenToolCall || !seenPin {
		t.Fatalf("MCP app runtime events missing: %#v", events)
	}
}

func TestMCPAppSandboxTicketsAreRevokedWhenServerCloses(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	t.Setenv("SYNON_BUNDLED_CONNECTOR_EXECUTABLE", "mcp-ketcher")
	app := newV11TestServer(t, Options{Workspace: store, FileRoot: root})
	handler := app.Handler()
	issue := func() *httptest.ResponseRecorder {
		request := compatRequest(t, http.MethodPost, "/api/mcp/apps/resource-tickets", "owner-a", map[string]any{
			"server_id": "bundled:ketcher-chemistry", "resource_uri": "ui://ketcher-chemistry/editor",
		})
		request.Host = "localhost:38180"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	issued := issue()
	if issued.Code != http.StatusCreated {
		t.Fatalf("issue=%d: %s", issued.Code, issued.Body.String())
	}
	var payload struct {
		Ticket string `json:"ticket"`
	}
	if err := json.Unmarshal(issued.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if err := app.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := app.Close(context.Background()); err != nil {
		t.Fatalf("repeated close: %v", err)
	}
	resource := httptest.NewRecorder()
	resourceRequest := httptest.NewRequest(http.MethodGet, "/mcp-app-resource?ticket="+payload.Ticket, nil)
	resourceRequest.Host = "mcp-app.localhost:38180"
	resourceRequest.RemoteAddr = "127.0.0.1:60003"
	handler.ServeHTTP(resource, resourceRequest)
	if resource.Code != http.StatusGone {
		t.Fatalf("ticket survived close: %d %s", resource.Code, resource.Body.String())
	}
	if response := issue(); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("ticket issued after close: %d %s", response.Code, response.Body.String())
	}
}
