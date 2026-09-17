package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"synon-go/internal/buildinfo"
)

const (
	mcpMarketplaceRegistryURL = "https://registry.modelcontextprotocol.io/v0.1/servers"
	mcpMarketplaceMaxQuery    = 80
	mcpMarketplaceMaxResults  = 40
	mcpMarketplaceMaxBody     = 2 * 1024 * 1024
	mcpMarketplaceTimeout     = 12 * time.Second
)

func (s *Server) handleMCPMarketplace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	if len(search) > mcpMarketplaceMaxQuery {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "marketplace search is too long"})
		return
	}
	limit := mcpMarketplaceMaxResults
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > mcpMarketplaceMaxResults {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "marketplace limit is invalid"})
			return
		}
		limit = parsed
	}
	if version := strings.TrimSpace(r.URL.Query().Get("version")); version != "" && version != "latest" {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "marketplace version is invalid"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), mcpMarketplaceTimeout)
	defer cancel()
	payload, err := fetchMCPMarketplace(ctx, s.httpClient, mcpMarketplaceRegistryURL, search, limit)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadGateway, map[string]any{"message": "MCP Registry is temporarily unavailable"})
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=60")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

func fetchMCPMarketplace(
	ctx context.Context,
	client *http.Client,
	registryURL, search string,
	limit int,
) ([]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	target, err := url.Parse(registryURL)
	if err != nil || target.Scheme == "" || target.Host == "" || limit <= 0 || limit > mcpMarketplaceMaxResults {
		return nil, errors.New("valid marketplace registry and bounded limit are required")
	}
	query := target.Query()
	query.Set("version", "latest")
	query.Set("limit", strconv.Itoa(limit))
	if search = strings.TrimSpace(search); search != "" {
		if len(search) > mcpMarketplaceMaxQuery {
			return nil, errors.New("marketplace search is too long")
		}
		query.Set("search", search)
	}
	target.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", buildinfo.UserAgent())
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("MCP Registry returned status %d", response.StatusCode)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, mcpMarketplaceMaxBody+1))
	if err != nil {
		return nil, err
	}
	if len(payload) == 0 || len(payload) > mcpMarketplaceMaxBody || !json.Valid(payload) {
		return nil, errors.New("MCP Registry returned an invalid response")
	}
	return payload, nil
}
