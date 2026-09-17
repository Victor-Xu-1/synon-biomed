package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMCPMarketplaceProxyFetchesBoundedRegistryResponse(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: marketplaceRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.Method != http.MethodGet || request.URL.Scheme != "https" || request.URL.Host != "registry.modelcontextprotocol.io" || request.URL.Path != "/v0.1/servers" {
			t.Fatalf("registry request = %s %s", request.Method, request.URL.String())
		}
		query := request.URL.Query()
		if query.Get("search") != "pubmed" || query.Get("version") != "latest" || query.Get("limit") != "12" {
			t.Fatalf("registry query = %s", request.URL.RawQuery)
		}
		if request.Header.Get("Accept") != "application/json" || request.Header.Get("User-Agent") != "synon-biomed/0.1.1" {
			t.Fatalf("registry headers = %#v", request.Header)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"servers":[{"server":{"name":"com.example/pubmed"}}]}`)),
			Request:    request,
		}, nil
	})}
	app := New(Options{HTTPClient: client}).Handler()
	response := httptest.NewRecorder()
	app.ServeHTTP(response, authenticatedWorkspaceURLRequest(
		http.MethodGet,
		"/api/mcp-servers/marketplace?search=pubmed&version=latest&limit=12",
		nil,
	))
	if response.Code != http.StatusOK {
		t.Fatalf("marketplace status = %d: %s", response.Code, response.Body.String())
	}
	if requests != 1 {
		t.Fatalf("registry request count = %d, want 1", requests)
	}
	if response.Header().Get("Cache-Control") != "private, max-age=60" || !bytes.Contains(response.Body.Bytes(), []byte("com.example/pubmed")) {
		t.Fatalf("marketplace response headers=%#v body=%s", response.Header(), response.Body.String())
	}
}

func TestMCPMarketplaceProxyRejectsInvalidRequestsBeforeUpstream(t *testing.T) {
	client := &http.Client{Transport: marketplaceRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("unexpected registry request: %s", request.URL.String())
		return nil, nil
	})}
	app := New(Options{HTTPClient: client}).Handler()
	for _, test := range []struct {
		name   string
		method string
		path   string
		status int
	}{
		{name: "method", method: http.MethodPost, path: "/api/mcp-servers/marketplace", status: http.StatusMethodNotAllowed},
		{name: "search", method: http.MethodGet, path: "/api/mcp-servers/marketplace?search=" + strings.Repeat("x", mcpMarketplaceMaxQuery+1), status: http.StatusBadRequest},
		{name: "limit", method: http.MethodGet, path: "/api/mcp-servers/marketplace?limit=41", status: http.StatusBadRequest},
		{name: "version", method: http.MethodGet, path: "/api/mcp-servers/marketplace?version=v1", status: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			app.ServeHTTP(response, authenticatedWorkspaceURLRequest(test.method, test.path, nil))
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.status, response.Body.String())
			}
		})
	}
}

func TestFetchMCPMarketplaceRejectsUnsuccessfulOversizedAndInvalidResponses(t *testing.T) {
	oversized := strings.Repeat("x", mcpMarketplaceMaxBody+1)
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "upstream status", status: http.StatusServiceUnavailable, body: `{}`},
		{name: "invalid json", status: http.StatusOK, body: `{`},
		{name: "oversized", status: http.StatusOK, body: oversized},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: marketplaceRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: test.status,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(test.body)),
					Request:    request,
				}, nil
			})}
			if _, err := fetchMCPMarketplace(context.Background(), client, mcpMarketplaceRegistryURL, "", 10); err == nil {
				t.Fatal("fetchMCPMarketplace() error = nil")
			}
		})
	}
}
