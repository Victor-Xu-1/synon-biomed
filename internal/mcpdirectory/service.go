package mcpdirectory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/tools/mcpstdio"
)

const maxManifestBytes = 2 << 20
const (
	maxManifestConnectors = 500
	maxConnectorPageSize  = 500
)

var connectorIDPattern = regexp.MustCompile("^[A-Za-z0-9:_-]{1,128}$")

type Service struct {
	store                 *workspace.Store
	root                  string
	client                *http.Client
	resolver              publicIPResolver
	startedAt             time.Time
	mu                    sync.Mutex
	tlsMu                 sync.RWMutex
	probeMu               sync.Mutex
	bootstrapMu           sync.Mutex
	bootstrapOwners       map[string]struct{}
	bootstrapCtx          context.Context
	bootstrapCancel       context.CancelFunc
	bootstrapWG           sync.WaitGroup
	closed                bool
	catalogMu             sync.Mutex
	catalogFlights        map[string]*connectorToolCatalogFlight
	catalogFailures       map[string]connectorToolCatalogFailure
	catalogSlots          chan struct{}
	catalogWG             sync.WaitGroup
	bundled               map[string]bundledConnector
	contactEmail          func(string) (string, bool, error)
	bundledPython         func() (string, error)
	bundledAPIKey         func(userID, connectorID string) (string, bool, error)
	optional              map[string]optionalConnectorDefinition
	optionalInstallMu     sync.Mutex
	optionalInstallCtx    context.Context
	optionalInstallCancel context.CancelFunc
	optionalInstallWG     sync.WaitGroup
	usageRecorder         ConnectorUsageRecorder
	tlsPosture            func() mcpstdio.TLSPosture
}

// ConnectorUsageRecorder receives one notification for each actual MCP
// tools/call attempt that reaches a connector transport. The recorder is
// deliberately injected by the server so the directory service remains
// responsible only for connector execution and authority.
type ConnectorUsageRecorder func(ctx context.Context, userID string, connector RuntimeConnector, toolName string, callErr error)

type BundledAPIKeySpec struct {
	Header     string
	QueryParam string
	Prefix     string
	Label      string
}

type AddDirectoryInput struct {
	Name        string
	URL         string
	CatalogUUID string
}

type ReconcileResult struct {
	DirectoryID    string `json:"directoryId"`
	Status         string `json:"status"`
	ConnectorCount int    `json:"connectorCount"`
	Error          string `json:"error,omitempty"`
}

type ConnectorHealth struct {
	OK           bool   `json:"ok"`
	ToolCount    int    `json:"toolCount,omitempty"`
	Error        string `json:"error,omitempty"`
	AuthRequired bool   `json:"authRequired,omitempty"`
}

type ConnectorUpstream struct {
	Name          string `json:"name"`
	License       string `json:"license,omitempty"`
	TermsURL      string `json:"termsUrl,omitempty"`
	InfoLabel     string `json:"infoLabel,omitempty"`
	InfoURL       string `json:"infoUrl,omitempty"`
	HomepageURL   string `json:"homepageUrl,omitempty"`
	CredentialURL string `json:"credentialUrl,omitempty"`
	PrivacyURL    string `json:"privacyUrl,omitempty"`
}

type ConnectorUsage struct {
	InvocationCount int     `json:"invocationCount"`
	LastUsedAt      *string `json:"lastUsedAt"`
}

type Connector struct {
	ID                 string              `json:"id"`
	Name               string              `json:"name"`
	DisplayName        string              `json:"displayName,omitempty"`
	Description        string              `json:"description,omitempty"`
	URL                string              `json:"url,omitempty"`
	Transport          string              `json:"transport"`
	Source             string              `json:"source"`
	Enabled            bool                `json:"enabled"`
	HostedBySynon      bool                `json:"hostedBySynon"`
	AuthRequired       bool                `json:"authRequired,omitempty"`
	OAuthSupported     bool                `json:"oauthSupported"`
	AuthState          string              `json:"authState"`
	AuthHint           string              `json:"authHint,omitempty"`
	APIKeyConfigurable bool                `json:"apiKeyConfigurable,omitempty"`
	APIKeyConfigured   bool                `json:"apiKeyConfigured,omitempty"`
	APIKeyLabel        string              `json:"apiKeyLabel,omitempty"`
	ConnectionStatus   string              `json:"connectionStatus,omitempty"`
	ConnectionError    string              `json:"connectionError,omitempty"`
	ToolCount          int                 `json:"toolCount,omitempty"`
	SchemaSHA256       string              `json:"schemaSha256,omitempty"`
	DirectoryID        string              `json:"directoryId,omitempty"`
	AttachedAgents     []string            `json:"attachedAgents"`
	Upstreams          []ConnectorUpstream `json:"upstreams,omitempty"`
	Health             *ConnectorHealth    `json:"health,omitempty"`
	Usage              ConnectorUsage      `json:"usage"`
}

type ConnectorListOptions struct {
	Source  string
	Status  string
	Query   string
	Enabled *bool
	Limit   int
	Offset  int
}

type ConnectorPage struct {
	Connectors []Connector
	Total      int
	Limit      int
	Offset     int
}

type DirectoryHealthSummary struct {
	OK          bool                     `json:"ok"`
	Total       int                      `json:"total"`
	Healthy     int                      `json:"healthy"`
	Failed      int                      `json:"failed"`
	Pending     int                      `json:"pending"`
	Directories []workspace.MCPDirectory `json:"directories"`
}

type manifest struct {
	CatalogUUID string
	Connectors  []manifestConnector
}

type manifestConnector struct {
	ID           string
	Name         string
	Description  string
	URL          string
	Transport    string
	AuthRequired bool
	AuthHint     string
}

func New(store *workspace.Store, root string, client *http.Client) *Service {
	if client == nil {
		client = http.DefaultClient
	}
	bootstrapCtx, bootstrapCancel := context.WithCancel(context.Background())
	optionalInstallCtx, optionalInstallCancel := context.WithCancel(context.Background())
	bundled := defaultBundledConnectors()
	optional := loadOptionalConnectorDefinitions()
	loadInstalledOptionalConnectors(root, bundled, optional)
	return &Service{
		store: store, root: root, client: client, startedAt: time.Now().UTC(), bundled: bundled, optional: optional,
		bootstrapCtx: bootstrapCtx, bootstrapCancel: bootstrapCancel,
		optionalInstallCtx: optionalInstallCtx, optionalInstallCancel: optionalInstallCancel,
		catalogFlights:  make(map[string]*connectorToolCatalogFlight),
		catalogFailures: make(map[string]connectorToolCatalogFailure),
		catalogSlots:    make(chan struct{}, connectorToolCatalogRefreshConcurrency),
	}
}

func (s *Service) SetConnectorUsageRecorder(recorder ConnectorUsageRecorder) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.usageRecorder = recorder
	s.mu.Unlock()
}

// Close cancels and joins background bundled-connector probes started by this
// service. The service does not own the workspace store or HTTP client; their
// owners remain responsible for closing those resources.
func (s *Service) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("MCP directory close context is required")
	}
	s.bootstrapMu.Lock()
	if !s.closed {
		s.closed = true
		if s.bootstrapCancel != nil {
			s.bootstrapCancel()
		}
		if s.optionalInstallCancel != nil {
			s.optionalInstallCancel()
		}
	}
	s.bootstrapMu.Unlock()
	done := make(chan struct{})
	go func() {
		s.bootstrapWG.Wait()
		s.catalogWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		optionalDone := make(chan struct{})
		go func() {
			s.optionalInstallWG.Wait()
			close(optionalDone)
		}()
		select {
		case <-optionalDone:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SetContactEmailProvider supplies a current owner-scoped, explicitly
// consented contact address for bundled research connectors.
func (s *Service) SetContactEmailProvider(provider func(string) (string, bool, error)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contactEmail = provider
}

// SetBundledPythonCommandProvider binds bundled Python MCP processes to the
// service-owned, verified interpreter. The provider is intentionally lazy:
// the managed environment may still be provisioning when the directory is
// constructed, and each execution/probe can then retry after it becomes
// ready.
func (s *Service) SetBundledPythonCommandProvider(provider func() (string, error)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bundledPython = provider
}

// SetBundledAPIKeyProvider binds owner-scoped encrypted BYOK credentials to
// trusted hosted connectors. The provider returns only at transport assembly
// time; connector projections and model context receive status, never values.
func (s *Service) SetBundledAPIKeyProvider(provider func(userID, connectorID string) (string, bool, error)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bundledAPIKey = provider
}

func (s *Service) BundledAPIKeySpec(connectorID string) (BundledAPIKeySpec, bool) {
	if s == nil {
		return BundledAPIKeySpec{}, false
	}
	item, found := s.bundled[strings.TrimSpace(connectorID)]
	if !found || (strings.TrimSpace(item.APIKeyHeader) == "" && strings.TrimSpace(item.APIKeyQueryParam) == "") {
		return BundledAPIKeySpec{}, false
	}
	label := strings.TrimSpace(item.APIKeyLabel)
	if label == "" {
		label = "API Key"
	}
	return BundledAPIKeySpec{Header: item.APIKeyHeader, QueryParam: item.APIKeyQueryParam, Prefix: item.APIKeyPrefix, Label: label}, true
}

// SetTLSPostureProvider binds every local MCP spawn to the host's single
// certificate-policy authority. The provider is intentionally not part of
// connector configuration: user-controlled MCP JSON cannot relax the policy.
func (s *Service) SetTLSPostureProvider(provider func() mcpstdio.TLSPosture) {
	if s == nil {
		return
	}
	s.tlsMu.Lock()
	s.tlsPosture = provider
	s.tlsMu.Unlock()
}

func (s *Service) applyTLSPosture(config mcpstdio.ServerConfig) mcpstdio.ServerConfig {
	if s == nil {
		return mcpstdio.ApplyTLSPosture(config, mcpstdio.TLSPosture{Strict: true})
	}
	s.tlsMu.RLock()
	provider := s.tlsPosture
	s.tlsMu.RUnlock()
	if provider == nil {
		return mcpstdio.ApplyTLSPosture(config, mcpstdio.TLSPosture{Strict: true})
	}
	return mcpstdio.ApplyTLSPosture(config, provider())
}

func (s *Service) bundledRuntimeConfig(item bundledConnector) (mcpstdio.ServerConfig, error) {
	config := cloneRuntimeServerConfig(item.Config)
	if item.Package == "" && !item.useBundledPython {
		return s.applyTLSPosture(config), nil
	}
	s.mu.Lock()
	provider := s.bundledPython
	s.mu.Unlock()
	if provider == nil {
		return s.applyTLSPosture(config), nil
	}
	command, err := provider()
	if err != nil {
		return mcpstdio.ServerConfig{}, fmt.Errorf("managed Python runtime for bundled MCP %s is unavailable: %w", item.ID, err)
	}
	if strings.TrimSpace(command) == "" {
		return mcpstdio.ServerConfig{}, fmt.Errorf("managed Python runtime for bundled MCP %s returned an empty executable", item.ID)
	}
	config.Command = command
	return s.applyTLSPosture(config), nil
}

func (s *Service) bundledRuntimeConfigForUser(userID string, item bundledConnector) (mcpstdio.ServerConfig, error) {
	config, err := s.bundledRuntimeConfig(item)
	if err != nil {
		return mcpstdio.ServerConfig{}, err
	}
	if strings.TrimSpace(item.APIKeyHeader) == "" && strings.TrimSpace(item.APIKeyQueryParam) == "" {
		return config, nil
	}
	s.mu.Lock()
	provider := s.bundledAPIKey
	s.mu.Unlock()
	if provider == nil {
		return config, nil
	}
	value, configured, err := provider(strings.TrimSpace(userID), item.ID)
	if err != nil {
		return mcpstdio.ServerConfig{}, fmt.Errorf("load bundled MCP %s API key: %w", item.ID, err)
	}
	if !configured {
		return config, nil
	}
	if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\r\n") {
		return mcpstdio.ServerConfig{}, fmt.Errorf("bundled MCP %s API key is invalid", item.ID)
	}
	if strings.TrimSpace(item.APIKeyHeader) != "" {
		if config.Headers == nil {
			config.Headers = map[string]string{}
		}
		config.Headers[item.APIKeyHeader] = item.APIKeyPrefix + value
	}
	if strings.TrimSpace(item.APIKeyQueryParam) != "" {
		if config.QueryParams == nil {
			config.QueryParams = map[string]string{}
		}
		config.QueryParams[item.APIKeyQueryParam] = value
	}
	return config, nil
}

func (s *Service) bundledAPIKeyConfigured(userID string, item bundledConnector) (bool, error) {
	if strings.TrimSpace(item.APIKeyHeader) == "" && strings.TrimSpace(item.APIKeyQueryParam) == "" {
		return false, nil
	}
	s.mu.Lock()
	provider := s.bundledAPIKey
	s.mu.Unlock()
	if provider == nil {
		return false, nil
	}
	value, configured, err := provider(strings.TrimSpace(userID), item.ID)
	if err != nil {
		return false, err
	}
	return configured && strings.TrimSpace(value) != "", nil
}

func (s *Service) ownerContactEmail(userID string) (string, bool, error) {
	if s == nil {
		return "", false, nil
	}
	s.mu.Lock()
	provider := s.contactEmail
	s.mu.Unlock()
	if provider == nil {
		return "", false, nil
	}
	return provider(userID)
}

func (s *Service) AddDirectory(ctx context.Context, userID string, input AddDirectoryInput) (workspace.MCPDirectory, ReconcileResult, error) {
	if s == nil || s.store == nil {
		return workspace.MCPDirectory{}, ReconcileResult{}, errors.New("MCP directory service is not configured")
	}
	if strings.TrimSpace(userID) == "" {
		return workspace.MCPDirectory{}, ReconcileResult{}, errors.New("user id is required")
	}
	if err := validateDirectoryInput(input); err != nil {
		return workspace.MCPDirectory{}, ReconcileResult{}, err
	}
	directory, err := s.store.CreateMCPDirectory(workspace.CreateMCPDirectoryInput{
		UserID: userID, Name: strings.TrimSpace(input.Name), URL: strings.TrimSpace(input.URL),
		CatalogUUID: strings.TrimSpace(input.CatalogUUID),
	})
	if err != nil {
		return workspace.MCPDirectory{}, ReconcileResult{}, err
	}
	result := s.reconcileDirectory(ctx, userID, directory)
	refreshed, found, lookupErr := s.store.GetMCPDirectory(directory.ID, userID)
	if lookupErr != nil {
		return workspace.MCPDirectory{}, result, lookupErr
	}
	if found {
		directory = refreshed
	}
	if result.Error != "" {
		return directory, result, errors.New(result.Error)
	}
	return directory, result, nil
}

func (s *Service) Reconcile(ctx context.Context, userID string) ([]ReconcileResult, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("MCP directory service is not configured")
	}
	directories, err := s.store.ListMCPDirectories(userID)
	if err != nil {
		return nil, err
	}
	results := make([]ReconcileResult, 0, len(directories))
	for _, directory := range directories {
		results = append(results, s.reconcileDirectory(ctx, userID, directory))
	}
	return results, nil
}

func (s *Service) ListConnectors(ctx context.Context, userID string) ([]Connector, error) {
	page, err := s.ListConnectorPage(ctx, userID, ConnectorListOptions{})
	if err != nil {
		return nil, err
	}
	return page.Connectors, nil
}

func (s *Service) ListConnectorPage(_ context.Context, userID string, options ConnectorListOptions) (ConnectorPage, error) {
	if s == nil || s.store == nil {
		return ConnectorPage{}, errors.New("MCP directory service is not configured")
	}
	rows, err := s.store.ListMCPDirectoryConnectors(userID, "")
	if err != nil {
		return ConnectorPage{}, err
	}
	connectors := make([]Connector, 0, len(rows))
	for _, row := range rows {
		connectors = append(connectors, connectorProjection(row))
	}
	return FilterConnectorPage(connectors, options)
}

func FilterConnectorPage(connectors []Connector, options ConnectorListOptions) (ConnectorPage, error) {
	options.Source = strings.ToLower(strings.TrimSpace(options.Source))
	options.Status = strings.ToLower(strings.TrimSpace(options.Status))
	options.Query = strings.ToLower(strings.TrimSpace(options.Query))
	if options.Source != "" {
		switch options.Source {
		case "directory", "custom", "bundled", "local-stdio", "antmcp":
		default:
			return ConnectorPage{}, fmt.Errorf("unsupported MCP connector source %q", options.Source)
		}
	}
	if len(options.Query) > 256 {
		return ConnectorPage{}, errors.New("MCP connector query must be at most 256 characters")
	}
	if options.Limit < 0 || options.Limit > maxConnectorPageSize {
		return ConnectorPage{}, fmt.Errorf("MCP connector limit must be between 0 and %d", maxConnectorPageSize)
	}
	if options.Offset < 0 {
		return ConnectorPage{}, errors.New("MCP connector offset must be non-negative")
	}
	matches := make([]Connector, 0, len(connectors))
	for _, connector := range connectors {
		if options.Source != "" && !strings.EqualFold(connector.Source, options.Source) {
			continue
		}
		if options.Status != "" && !strings.EqualFold(connector.ConnectionStatus, options.Status) {
			continue
		}
		if options.Enabled != nil && connector.Enabled != *options.Enabled {
			continue
		}
		if options.Query != "" {
			haystack := strings.ToLower(strings.Join([]string{
				connector.ID, connector.Name, connector.DisplayName, connector.Description,
			}, "\n"))
			if !strings.Contains(haystack, options.Query) {
				continue
			}
		}
		matches = append(matches, connector)
	}
	page := ConnectorPage{Total: len(matches), Offset: options.Offset}
	if options.Offset >= len(matches) {
		page.Connectors = []Connector{}
		return page, nil
	}
	end := len(matches)
	if options.Limit > 0 && options.Offset+options.Limit < end {
		end = options.Offset + options.Limit
	}
	page.Connectors = append([]Connector(nil), matches[options.Offset:end]...)
	page.Limit = len(page.Connectors)
	return page, nil
}

func (s *Service) Health(ctx context.Context, userID string) ([]workspace.MCPDirectory, error) {
	health, err := s.DirectoryHealth(ctx, userID)
	if err != nil {
		return nil, err
	}
	return health.Directories, nil
}

func (s *Service) DirectoryHealth(_ context.Context, userID string) (DirectoryHealthSummary, error) {
	if s == nil || s.store == nil {
		return DirectoryHealthSummary{}, errors.New("MCP directory service is not configured")
	}
	directories, err := s.store.ListMCPDirectories(userID)
	if err != nil {
		return DirectoryHealthSummary{}, err
	}
	summary := DirectoryHealthSummary{OK: true, Total: len(directories), Directories: directories}
	for _, directory := range directories {
		switch directory.LastStatus {
		case "ok", "not-modified":
			summary.Healthy++
		case "error":
			summary.Failed++
			summary.OK = false
		default:
			summary.Pending++
			summary.OK = false
		}
	}
	return summary, nil
}

func (s *Service) SetEnabled(ctx context.Context, userID, id string, enabled bool) (Connector, error) {
	if s == nil || s.store == nil {
		return Connector{}, errors.New("MCP directory service is not configured")
	}
	row, err := s.store.SetMCPDirectoryConnectorEnabled(userID, id, enabled)
	if err != nil {
		return Connector{}, err
	}
	if !enabled {
		if err := s.store.UpdateMCPDirectoryConnectorHealth(userID, id, "disabled", "", 0); err != nil {
			return Connector{}, err
		}
		row, _, err = s.store.GetMCPDirectoryConnector(userID, id)
		if err != nil {
			return Connector{}, err
		}
		return connectorProjection(row), nil
	}
	status, message, count := s.probe(ctx, userID, row)
	if err := s.store.UpdateMCPDirectoryConnectorHealth(userID, id, status, message, count); err != nil {
		return Connector{}, err
	}
	row, _, err = s.store.GetMCPDirectoryConnector(userID, id)
	if err != nil {
		return Connector{}, err
	}
	return connectorProjection(row), nil
}

func (s *Service) Authorize(ctx context.Context, userID, id string) (mcpstdio.OAuthStartResult, error) {
	if s == nil || s.store == nil {
		return mcpstdio.OAuthStartResult{}, errors.New("MCP directory service is not configured")
	}
	row, found, err := s.store.GetMCPDirectoryConnector(userID, id)
	if err != nil {
		return mcpstdio.OAuthStartResult{}, err
	}
	if !found {
		return mcpstdio.OAuthStartResult{}, errors.New("MCP directory connector not found")
	}
	if !row.Enabled {
		return mcpstdio.OAuthStartResult{}, errors.New("MCP directory connector is disabled")
	}
	oauthKey := directoryOAuthKey(userID, row.ID)
	config := s.applyTLSPosture(serverConfig(row))
	config.Name = oauthKey
	result, err := mcpstdio.AuthenticateServer(
		mcpstdio.WithHTTPClient(ctx, s.client), s.root, oauthKey, config,
	)
	if err != nil {
		return mcpstdio.OAuthStartResult{}, err
	}
	if result.Status == "auth_url" {
		result.Message = fmt.Sprintf("Open this URL to authorize the %s MCP connector.", row.Name)
	}
	return result, nil
}

func (s *Service) Disconnect(_ context.Context, userID, id string) error {
	row, found, err := s.store.GetMCPDirectoryConnector(userID, id)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("MCP directory connector not found")
	}
	if err := mcpstdio.DisconnectOAuth(s.root, directoryOAuthKey(userID, row.ID)); err != nil {
		return err
	}
	status := "configured"
	if row.AuthRequired {
		status = "auth_required"
	}
	return s.store.UpdateMCPDirectoryConnectorHealth(userID, row.ID, status, "", 0)
}

func (s *Service) reconcileDirectory(ctx context.Context, userID string, directory workspace.MCPDirectory) ReconcileResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := ReconcileResult{DirectoryID: directory.ID, Status: "error"}
	manifest, etag, notModified, err := s.fetchManifest(ctx, directory)
	if err != nil {
		_ = s.store.RecordMCPDirectoryHealth(directory.ID, userID, "error", err.Error(), "")
		result.Error = err.Error()
		return result
	}
	if notModified {
		_ = s.store.RecordMCPDirectoryHealth(directory.ID, userID, "not-modified", "", "")
		existing, listErr := s.store.ListMCPDirectoryConnectors(userID, directory.ID)
		if listErr != nil {
			result.Error = listErr.Error()
			return result
		}
		result.Status, result.ConnectorCount = "not-modified", len(existing)
		return result
	}
	inputs := make([]workspace.MCPDirectoryConnectorInput, 0, len(manifest.Connectors))
	for _, item := range manifest.Connectors {
		normalized, normalizeErr := normalizeConnector(item)
		if normalizeErr != nil {
			result.Error = normalizeErr.Error()
			_ = s.store.RecordMCPDirectoryHealth(directory.ID, userID, "error", result.Error, "")
			return result
		}
		inputs = append(inputs, normalized)
	}
	rows, err := s.store.ReplaceMCPDirectoryConnectors(directory.ID, userID, etag, inputs)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		status, message, count := s.probe(ctx, userID, row)
		if err := s.store.UpdateMCPDirectoryConnectorHealth(userID, row.ID, status, message, count); err != nil {
			result.Error = err.Error()
			return result
		}
	}
	result.Status, result.ConnectorCount = "ok", len(rows)
	return result
}

func (s *Service) fetchManifest(ctx context.Context, directory workspace.MCPDirectory) (manifest, string, bool, error) {
	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, directory.URL, nil)
	if err != nil {
		return manifest{}, "", false, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Synon-Catalog-Uuid", directory.CatalogUUID)
	if directory.ETag != "" {
		req.Header.Set("If-None-Match", directory.ETag)
	}
	client, err := s.secureHTTPClient(requestCtx, directory.URL)
	if err != nil {
		return manifest{}, "", false, err
	}
	response, err := client.Do(req)
	if err != nil {
		return manifest{}, "", false, fmt.Errorf("request MCP directory: %w", err)
	}
	defer response.Body.Close()
	if response.Request == nil || response.Request.URL == nil || response.Request.URL.Scheme != "https" {
		return manifest{}, "", false, errors.New("MCP directory must resolve to HTTPS")
	}
	if response.StatusCode == http.StatusNotModified {
		return manifest{}, "", true, nil
	}
	if response.StatusCode != http.StatusOK {
		return manifest{}, "", false, fmt.Errorf("MCP directory returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxManifestBytes+1))
	if err != nil {
		return manifest{}, "", false, fmt.Errorf("read MCP directory: %w", err)
	}
	if len(body) > maxManifestBytes {
		return manifest{}, "", false, errors.New("MCP directory manifest exceeds 2 MiB")
	}
	parsed, err := decodeManifest(body)
	if err != nil {
		return manifest{}, "", false, err
	}
	if !strings.EqualFold(parsed.CatalogUUID, directory.CatalogUUID) {
		return manifest{}, "", false, errors.New("MCP directory catalog UUID does not match configured catalog")
	}
	if len(parsed.Connectors) > maxManifestConnectors {
		return manifest{}, "", false, errors.New("MCP directory has too many connectors")
	}
	return parsed, response.Header.Get("ETag"), false, nil
}

func decodeManifest(body []byte) (manifest, error) {
	var wire struct {
		CatalogUUID      string
		CatalogUUIDSnake string
		Connectors       []struct {
			ID           string
			Name         string
			Description  string
			URL          string
			Transport    string
			AuthRequired bool
			AuthHint     string
		}
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return manifest{}, fmt.Errorf("decode MCP directory manifest: %w", err)
	}
	_ = json.Unmarshal(raw["catalogUuid"], &wire.CatalogUUID)
	if wire.CatalogUUID == "" {
		_ = json.Unmarshal(raw["catalog_uuid"], &wire.CatalogUUIDSnake)
		wire.CatalogUUID = wire.CatalogUUIDSnake
	}
	connectorsRaw, ok := raw["connectors"]
	if !ok {
		return manifest{}, errors.New("MCP directory manifest connectors are required")
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(connectorsRaw, &entries); err != nil {
		return manifest{}, errors.New("MCP directory connectors must be an array")
	}
	output := manifest{CatalogUUID: wire.CatalogUUID, Connectors: make([]manifestConnector, 0, len(entries))}
	for _, entry := range entries {
		var item manifestConnector
		_ = json.Unmarshal(entry["id"], &item.ID)
		_ = json.Unmarshal(entry["name"], &item.Name)
		_ = json.Unmarshal(entry["description"], &item.Description)
		_ = json.Unmarshal(entry["url"], &item.URL)
		_ = json.Unmarshal(entry["transport"], &item.Transport)
		_ = json.Unmarshal(entry["authRequired"], &item.AuthRequired)
		if !item.AuthRequired {
			_ = json.Unmarshal(entry["auth_required"], &item.AuthRequired)
		}
		_ = json.Unmarshal(entry["authHint"], &item.AuthHint)
		if item.AuthHint == "" {
			_ = json.Unmarshal(entry["auth_hint"], &item.AuthHint)
		}
		output.Connectors = append(output.Connectors, item)
	}
	return output, nil
}

func validateDirectoryInput(input AddDirectoryInput) error {
	if strings.TrimSpace(input.Name) == "" || len(strings.TrimSpace(input.Name)) > 120 {
		return errors.New("MCP directory name is required and must be at most 120 characters")
	}
	if _, err := uuid.Parse(strings.TrimSpace(input.CatalogUUID)); err != nil {
		return errors.New("MCP directory catalog UUID must be a UUID")
	}
	_, err := validateHTTPSURL(input.URL, false)
	return err
}

func normalizeConnector(item manifestConnector) (workspace.MCPDirectoryConnectorInput, error) {
	item.ID = strings.TrimSpace(item.ID)
	item.Name = strings.TrimSpace(item.Name)
	if !connectorIDPattern.MatchString(item.ID) {
		return workspace.MCPDirectoryConnectorInput{}, fmt.Errorf("invalid MCP connector id %q", item.ID)
	}
	if item.Name == "" || len(item.Name) > 120 {
		return workspace.MCPDirectoryConnectorInput{}, fmt.Errorf("invalid MCP connector name for %q", item.ID)
	}
	transport := strings.ToLower(strings.TrimSpace(item.Transport))
	switch transport {
	case "http", "sse", "streamable-http", "streamable_http":
		transport = "http"
	case "websocket", "ws":
		return workspace.MCPDirectoryConnectorInput{}, fmt.Errorf("unsupported MCP connector transport for %q: websocket address pinning is unavailable", item.ID)
	default:
		return workspace.MCPDirectoryConnectorInput{}, fmt.Errorf("unsupported MCP connector transport for %q", item.ID)
	}
	parsed, err := validateHTTPSURL(item.URL, transport == "websocket")
	if err != nil {
		return workspace.MCPDirectoryConnectorInput{}, fmt.Errorf("invalid MCP connector URL for %q: %w", item.ID, err)
	}
	canonical, err := json.Marshal(struct {
		ID           string
		Name         string
		Description  string
		URL          string
		Transport    string
		AuthRequired bool
		AuthHint     string
	}{item.ID, item.Name, strings.TrimSpace(item.Description), parsed.String(), transport, item.AuthRequired, strings.TrimSpace(item.AuthHint)})
	if err != nil {
		return workspace.MCPDirectoryConnectorInput{}, err
	}
	digest := sha256.Sum256(canonical)
	return workspace.MCPDirectoryConnectorInput{
		ID: item.ID, Name: item.Name, Description: strings.TrimSpace(item.Description),
		URL: parsed.String(), Transport: transport, AuthRequired: item.AuthRequired,
		AuthHint: strings.TrimSpace(item.AuthHint), ContentSHA: hex.EncodeToString(digest[:]),
	}, nil
}

func validateHTTPSURL(raw string, websocket bool) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || parsed.Opaque != "" {
		return nil, errors.New("URL must be an absolute HTTPS URL without credentials or fragments")
	}
	allowed := "https"
	if websocket {
		allowed = "wss"
	}
	if strings.ToLower(parsed.Scheme) != allowed {
		return nil, fmt.Errorf("URL scheme must be %s", allowed)
	}
	host := normalizeHost(parsed.Hostname())
	if isLocalHostname(host) {
		return nil, errors.New("URL must not target a local host")
	}
	if parsed.Port() != "" {
		port, portErr := strconv.Atoi(parsed.Port())
		if portErr != nil || port < 1 || port > 65535 {
			return nil, errors.New("URL port must be between 1 and 65535")
		}
	}
	if literal, parseErr := netip.ParseAddr(host); parseErr == nil && !isPublicOutboundIP(literal) {
		return nil, errors.New("URL must not target a loopback, private, link-local, multicast, unspecified, or reserved address")
	}
	return parsed, nil
}

func (s *Service) probe(ctx context.Context, userID string, row workspace.MCPDirectoryConnector) (string, string, int) {
	oauthKey := directoryOAuthKey(userID, row.ID)
	if row.AuthRequired && !mcpstdio.OAuthConnected(s.root, oauthKey) {
		return "auth_required", "connector authorization is required", 0
	}
	client, err := s.secureHTTPClient(ctx, row.URL)
	if err != nil {
		return "error", truncateError(err), 0
	}
	probeCtx, cancel := context.WithTimeout(mcpstdio.WithHTTPClient(ctx, client), 12*time.Second)
	defer cancel()
	config := s.applyTLSPosture(serverConfig(row))
	config.Name = oauthKey
	tools, err := mcpstdio.ListToolsForServer(probeCtx, s.root, oauthKey, config)
	if err != nil {
		return "error", truncateError(err), 0
	}
	return "connected", "", len(tools)
}

func directoryOAuthKey(userID, connectorID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(userID) + "\x00" + strings.TrimSpace(connectorID)))
	return "directory-" + hex.EncodeToString(digest[:])
}

func serverConfig(row workspace.MCPDirectoryConnector) mcpstdio.ServerConfig {
	return mcpstdio.ServerConfig{
		Name: row.ID, Type: row.Transport, URL: row.URL, Disabled: !row.Enabled,
		Scope: "directory",
	}
}

func connectorProjection(row workspace.MCPDirectoryConnector) Connector {
	status := strings.TrimSpace(row.LastStatus)
	if status == "" {
		status = "configured"
	}
	authState := "not-required"
	if row.AuthRequired {
		authState = "unauthorized"
		if status == "connected" {
			authState = "authorized"
		}
	}
	var health *ConnectorHealth
	switch status {
	case "connected":
		health = &ConnectorHealth{OK: true, ToolCount: row.ToolCount}
	case "auth_required":
		health = &ConnectorHealth{OK: false, Error: row.LastError, AuthRequired: true}
	case "error":
		health = &ConnectorHealth{OK: false, Error: row.LastError}
	}
	return Connector{
		ID: row.ID, DirectoryID: row.DirectoryID, Name: row.Name, DisplayName: row.Name,
		Description: row.Description, URL: row.URL, Transport: row.Transport,
		Source: "directory", Enabled: row.Enabled, AuthRequired: row.AuthRequired, OAuthSupported: row.AuthRequired,
		AuthState: authState, AuthHint: row.AuthHint, ConnectionStatus: status,
		ConnectionError: row.LastError, ToolCount: row.ToolCount,
		AttachedAgents: []string{}, Health: health,
	}
}

func truncateError(err error) string {
	message := strings.TrimSpace(err.Error())
	if len(message) > 500 {
		return message[:500]
	}
	return message
}
