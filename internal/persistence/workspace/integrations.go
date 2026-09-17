package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"
)

type ModelProvider struct {
	ID          string    `json:"id"`
	UserID      string    `json:"userId"`
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	BaseURL     string    `json:"baseUrl"`
	Model       string    `json:"model"`
	SecretRef   string    `json:"secretRef,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
	MaxTokens   *int      `json:"maxTokens,omitempty"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type ModelProviderInput struct {
	ID          string
	UserID      string
	Name        string
	Type        string
	BaseURL     string
	Model       string
	SecretRef   string
	Temperature *float64
	MaxTokens   *int
	// MaxTokensSet distinguishes an explicit provider-default reset from an
	// omitted update. Existing callers that omit generation controls retain them.
	MaxTokensSet bool
	Enabled      *bool
}

type MCPServer struct {
	ID             string    `json:"id"`
	UserID         string    `json:"userId"`
	Name           string    `json:"name"`
	Description    string    `json:"description,omitempty"`
	URL            string    `json:"url"`
	Transport      string    `json:"transport"`
	OAuthServerURL string    `json:"oauthServerUrl,omitempty"`
	ClientID       string    `json:"clientId,omitempty"`
	Scopes         string    `json:"scopes,omitempty"`
	HeadersHelper  string    `json:"headersHelper,omitempty"`
	ConfigJSON     string    `json:"configJson,omitempty"`
	Builtin        bool      `json:"builtin,omitempty"`
	Enabled        bool      `json:"enabled"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type MCPServerInput struct {
	ID             string
	UserID         string
	Name           string
	Description    string
	URL            string
	Transport      string
	OAuthServerURL string
	ClientID       string
	Scopes         string
	HeadersHelper  string
	ConfigJSON     string
	Builtin        bool
	Enabled        *bool
}

type MCPToolGrant struct {
	ID          string    `json:"id"`
	MCPServerID string    `json:"mcpServerId"`
	UserID      string    `json:"userId"`
	AgentName   string    `json:"agentName"`
	ToolName    string    `json:"toolName"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type MCPToolGrantInput struct {
	ID          string
	MCPServerID string
	UserID      string
	AgentName   string
	ToolName    string
	Enabled     bool
}

type MCPAssignment struct {
	ID          string    `json:"id"`
	MCPServerID string    `json:"mcpServerId"`
	UserID      string    `json:"userId"`
	AgentName   string    `json:"agentName"`
	CreatedAt   time.Time `json:"createdAt"`
}

type MCPAssignmentInput struct {
	ID          string
	MCPServerID string
	UserID      string
	AgentName   string
}

type MCPOAuthStatus struct {
	MCPServerID    string     `json:"mcpServerId"`
	UserID         string     `json:"userId"`
	AccessTokenRef string     `json:"accessTokenRef"`
	TokenType      string     `json:"tokenType"`
	ExpiresAt      *time.Time `json:"expiresAt,omitempty"`
	Scopes         []string   `json:"scopes"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

type MCPOAuthStatusInput struct {
	MCPServerID    string
	UserID         string
	AccessTokenRef string
	TokenType      string
	ExpiresAt      *time.Time
	Scopes         []string
}

type ComputeProvider struct {
	Name              string         `json:"name"`
	UserID            string         `json:"userId"`
	Family            string         `json:"family"`
	Endpoint          string         `json:"endpoint,omitempty"`
	SkillName         string         `json:"skillName,omitempty"`
	CredentialName    string         `json:"credentialName,omitempty"`
	Hosted            bool           `json:"hosted"`
	Environments      []string       `json:"environments"`
	DataRoots         []string       `json:"dataRoots"`
	SSHOverrides      map[string]any `json:"sshOverrides,omitempty"`
	MaxConcurrentJobs *int           `json:"maxConcurrentJobs,omitempty"`
	MaxTimeoutSec     *int           `json:"maxTimeoutSec,omitempty"`
	MemoryMD          string         `json:"memoryMd,omitempty"`
	ScratchRoot       string         `json:"scratchRoot,omitempty"`
	Scheduler         string         `json:"scheduler,omitempty"`
	DetailsMD         string         `json:"detailsMd,omitempty"`
	DetailsRev        int            `json:"detailsRev"`
	ProbedAt          *time.Time     `json:"probedAt,omitempty"`
	UpdatedAt         time.Time      `json:"updatedAt"`
}

type ComputeProviderInput struct {
	Name              string
	UserID            string
	Family            string
	Endpoint          string
	SkillName         string
	CredentialName    string
	Hosted            bool
	Environments      []string
	DataRoots         []string
	SSHOverrides      map[string]any
	MaxConcurrentJobs *int
	MaxTimeoutSec     *int
	MemoryMD          string
	ScratchRoot       string
	Scheduler         string
	DetailsMD         string
}

type ComputeState string

const (
	ComputeStatePending    ComputeState = "pending"
	ComputeStateStaging    ComputeState = "staging"
	ComputeStateQueued     ComputeState = "queued"
	ComputeStateRunning    ComputeState = "running"
	ComputeStateHarvesting ComputeState = "harvesting"
	ComputeStateDone       ComputeState = "done"
	ComputeStateFailed     ComputeState = "failed"
	ComputeStateTimedOut   ComputeState = "timed_out"
	ComputeStateOrphaned   ComputeState = "orphaned"
)

type ComputeUsage struct {
	ID              string          `json:"id"`
	JobID           string          `json:"jobId"`
	Environment     string          `json:"environment"`
	TierType        string          `json:"tierType"`
	Provider        string          `json:"provider"`
	State           ComputeState    `json:"state"`
	StartedAt       time.Time       `json:"startedAt"`
	EndedAt         *time.Time      `json:"endedAt,omitempty"`
	ExpiresAt       *time.Time      `json:"expiresAt,omitempty"`
	FrameID         string          `json:"frameId,omitempty"`
	RootFrameID     string          `json:"rootFrameId,omitempty"`
	ProjectID       string          `json:"projectId,omitempty"`
	ClientUUID      string          `json:"clientUuid,omitempty"`
	SubmitCellID    string          `json:"submitCellId,omitempty"`
	OriginToolUseID string          `json:"originToolUseId,omitempty"`
	RemoteWorkdir   string          `json:"remoteWorkdir,omitempty"`
	RemoteHandle    json.RawMessage `json:"-"`
	OutputSpecs     json.RawMessage `json:"outputSpecs,omitempty"`
	Result          json.RawMessage `json:"result,omitempty"`
	Intent          string          `json:"intent,omitempty"`
	HardwareDetails string          `json:"hardwareDetails,omitempty"`
}

type ComputeUsageInput struct {
	ID              string
	JobID           string
	Environment     string
	TierType        string
	Provider        string
	StartedAt       time.Time
	ExpiresAt       *time.Time
	FrameID         string
	RootFrameID     string
	ProjectID       string
	ClientUUID      string
	SubmitCellID    string
	OriginToolUseID string
	RemoteWorkdir   string
	RemoteHandle    json.RawMessage
	OutputSpecs     json.RawMessage
	Intent          string
	HardwareDetails string
}

func (s *Store) RegisterModelProvider(input ModelProviderInput) (ModelProvider, error) {
	if s == nil || s.db == nil {
		return ModelProvider{}, errors.New("workspace store is closed")
	}
	for field, value := range map[string]string{"model provider id": input.ID, "model provider user id": input.UserID, "model provider name": input.Name, "model provider type": input.Type, "model provider base url": input.BaseURL, "model provider model": input.Model} {
		if strings.TrimSpace(value) == "" {
			return ModelProvider{}, fmt.Errorf("%s is required", field)
		}
	}
	if _, err := url.ParseRequestURI(input.BaseURL); err != nil {
		return ModelProvider{}, fmt.Errorf("invalid model provider base url: %w", err)
	}
	if err := validateModelProviderGenerationControls(input.Temperature, input.MaxTokens); err != nil {
		return ModelProvider{}, err
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	now := s.now().UTC()
	provider := ModelProvider{ID: input.ID, UserID: input.UserID, Name: input.Name, Type: input.Type, BaseURL: input.BaseURL, Model: input.Model, SecretRef: input.SecretRef, Temperature: input.Temperature, MaxTokens: input.MaxTokens, Enabled: enabled, CreatedAt: now, UpdatedAt: now}
	if _, err := s.db.ExecContext(context.Background(), `
		INSERT INTO model_providers (id, user_id, name, type, base_url, model, secret_ref, temperature, max_tokens, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, provider.ID, provider.UserID, provider.Name, provider.Type, provider.BaseURL, provider.Model, provider.SecretRef, provider.Temperature, provider.MaxTokens, provider.Enabled, provider.CreatedAt, provider.UpdatedAt); err != nil {
		return ModelProvider{}, fmt.Errorf("insert model provider: %w", err)
	}
	return provider, nil
}

// UpsertModelProvider creates or updates a provider while preserving its
// original creation timestamp and enforcing user ownership of stable IDs.
func (s *Store) UpsertModelProvider(input ModelProviderInput) (ModelProvider, error) {
	if s == nil || s.db == nil {
		return ModelProvider{}, errors.New("workspace store is closed")
	}
	for field, value := range map[string]string{"model provider id": input.ID, "model provider user id": input.UserID, "model provider name": input.Name, "model provider type": input.Type, "model provider base url": input.BaseURL, "model provider model": input.Model} {
		if strings.TrimSpace(value) == "" {
			return ModelProvider{}, fmt.Errorf("%s is required", field)
		}
	}
	if _, err := url.ParseRequestURI(input.BaseURL); err != nil {
		return ModelProvider{}, fmt.Errorf("invalid model provider base url: %w", err)
	}
	if err := validateModelProviderGenerationControls(input.Temperature, input.MaxTokens); err != nil {
		return ModelProvider{}, err
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	now := s.now().UTC()
	provider := ModelProvider{ID: strings.TrimSpace(input.ID), UserID: strings.TrimSpace(input.UserID), Name: strings.TrimSpace(input.Name), Type: strings.TrimSpace(input.Type), BaseURL: strings.TrimSpace(input.BaseURL), Model: strings.TrimSpace(input.Model), SecretRef: strings.TrimSpace(input.SecretRef), Temperature: input.Temperature, MaxTokens: input.MaxTokens, Enabled: enabled, CreatedAt: now, UpdatedAt: now}
	row := s.db.QueryRowContext(context.Background(), `
		INSERT INTO model_providers (id, user_id, name, type, base_url, model, secret_ref, temperature, max_tokens, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, type=excluded.type, base_url=excluded.base_url,
			model=excluded.model, secret_ref=excluded.secret_ref,
			temperature=COALESCE(excluded.temperature,model_providers.temperature),
			max_tokens=CASE WHEN ? THEN excluded.max_tokens ELSE COALESCE(excluded.max_tokens,model_providers.max_tokens) END,
			enabled=excluded.enabled, updated_at=excluded.updated_at
		WHERE model_providers.user_id=excluded.user_id
		RETURNING id, user_id, name, type, base_url, model, secret_ref, temperature, max_tokens, enabled, created_at, updated_at`,
		provider.ID, provider.UserID, provider.Name, provider.Type, provider.BaseURL,
		provider.Model, provider.SecretRef, provider.Temperature, provider.MaxTokens, provider.Enabled, provider.CreatedAt, provider.UpdatedAt, input.MaxTokensSet)
	if err := row.Scan(&provider.ID, &provider.UserID, &provider.Name, &provider.Type, &provider.BaseURL, &provider.Model, &provider.SecretRef, &provider.Temperature, &provider.MaxTokens, &provider.Enabled, &provider.CreatedAt, &provider.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ModelProvider{}, fmt.Errorf("model provider %q belongs to another user", input.ID)
		}
		return ModelProvider{}, fmt.Errorf("upsert model provider: %w", err)
	}
	return provider, nil
}

func (s *Store) GetModelProvider(userID, id string) (ModelProvider, bool, error) {
	if s == nil || s.db == nil {
		return ModelProvider{}, false, errors.New("workspace store is closed")
	}
	var provider ModelProvider
	err := s.db.QueryRowContext(context.Background(), `
		SELECT id, user_id, name, type, base_url, model, secret_ref, temperature, max_tokens, enabled, created_at, updated_at
		FROM model_providers WHERE user_id=? AND id=?`, strings.TrimSpace(userID), strings.TrimSpace(id)).Scan(
		&provider.ID, &provider.UserID, &provider.Name, &provider.Type, &provider.BaseURL,
		&provider.Model, &provider.SecretRef, &provider.Temperature, &provider.MaxTokens, &provider.Enabled, &provider.CreatedAt, &provider.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ModelProvider{}, false, nil
	}
	if err != nil {
		return ModelProvider{}, false, fmt.Errorf("get model provider: %w", err)
	}
	return provider, true, nil
}

func (s *Store) DeleteModelProvider(userID, id string) (ModelProvider, bool, error) {
	provider, found, err := s.GetModelProvider(userID, id)
	if err != nil || !found {
		return provider, found, err
	}
	result, err := s.db.ExecContext(context.Background(), `DELETE FROM model_providers WHERE user_id=? AND id=?`, strings.TrimSpace(userID), strings.TrimSpace(id))
	if err != nil {
		return ModelProvider{}, false, fmt.Errorf("delete model provider: %w", err)
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return ModelProvider{}, false, fmt.Errorf("count deleted model providers: %w", err)
	}
	return provider, removed == 1, nil
}

func (s *Store) ListModelProviders(userID string) ([]ModelProvider, error) {
	return s.ListModelProvidersWithContext(context.Background(), userID)
}

// ListModelProvidersWithContext keeps provider authority resolution on the
// query-only read pool and lets a runner preparation deadline cancel both
// connection acquisition and the SQLite query.
func (s *Store) ListModelProvidersWithContext(ctx context.Context, userID string) ([]ModelProvider, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	if ctx == nil {
		return nil, errors.New("model provider context is required")
	}
	if strings.TrimSpace(userID) == "" {
		return nil, errors.New("model provider user id is required")
	}
	rows, err := s.readDatabase().QueryContext(ctx, `
		SELECT id, user_id, name, type, base_url, model, secret_ref, temperature, max_tokens, enabled, created_at, updated_at
		FROM model_providers WHERE user_id = ? ORDER BY updated_at DESC, id DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list model providers: %w", err)
	}
	defer rows.Close()
	providers := make([]ModelProvider, 0)
	for rows.Next() {
		var provider ModelProvider
		if err := rows.Scan(&provider.ID, &provider.UserID, &provider.Name, &provider.Type, &provider.BaseURL, &provider.Model, &provider.SecretRef, &provider.Temperature, &provider.MaxTokens, &provider.Enabled, &provider.CreatedAt, &provider.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan model provider: %w", err)
		}
		providers = append(providers, provider)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate model providers: %w", err)
	}
	return providers, nil
}

func validateModelProviderGenerationControls(temperature *float64, maxTokens *int) error {
	if temperature != nil && (math.IsNaN(*temperature) || math.IsInf(*temperature, 0) || *temperature < 0 || *temperature > 2) {
		return errors.New("model provider temperature must be between 0 and 2")
	}
	if maxTokens != nil && (*maxTokens < 1 || *maxTokens > 1_000_000) {
		return errors.New("model provider max tokens must be between 1 and 1000000")
	}
	return nil
}

func (s *Store) CreateMCPServer(input MCPServerInput) (MCPServer, error) {
	if s == nil || s.db == nil {
		return MCPServer{}, errors.New("workspace store is closed")
	}
	server, err := prepareMCPServer(input, s.now().UTC())
	if err != nil {
		return MCPServer{}, err
	}
	if err := insertMCPServer(context.Background(), s.db, server); err != nil {
		return MCPServer{}, err
	}
	return server, nil
}

// CreateMCPServers atomically persists a validated import batch. A duplicate
// or invalid record leaves the catalog unchanged.
func (s *Store) CreateMCPServers(inputs []MCPServerInput) ([]MCPServer, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	if len(inputs) == 0 {
		return nil, errors.New("at least one mcp server is required")
	}
	if len(inputs) > 100 {
		return nil, errors.New("mcp server import is limited to 100 records")
	}
	now := s.now().UTC()
	servers := make([]MCPServer, 0, len(inputs))
	seenIDs := make(map[string]struct{}, len(inputs))
	seenNames := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		server, err := prepareMCPServer(input, now)
		if err != nil {
			return nil, err
		}
		nameKey := strings.ToLower(strings.TrimSpace(server.Name))
		if _, exists := seenIDs[server.ID]; exists {
			return nil, fmt.Errorf("duplicate mcp server id %q in import", server.ID)
		}
		if _, exists := seenNames[nameKey]; exists {
			return nil, fmt.Errorf("duplicate mcp server name %q in import", server.Name)
		}
		seenIDs[server.ID] = struct{}{}
		seenNames[nameKey] = struct{}{}
		servers = append(servers, server)
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return nil, fmt.Errorf("begin mcp server import: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, server := range servers {
		if err := insertMCPServer(context.Background(), tx, server); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit mcp server import: %w", err)
	}
	return servers, nil
}

type mcpServerExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func prepareMCPServer(input MCPServerInput, now time.Time) (MCPServer, error) {
	for field, value := range map[string]string{"mcp server id": input.ID, "mcp server user id": input.UserID, "mcp server name": input.Name, "mcp server url": input.URL, "mcp transport": input.Transport} {
		if strings.TrimSpace(value) == "" {
			return MCPServer{}, fmt.Errorf("%s is required", field)
		}
	}
	if _, err := url.ParseRequestURI(input.URL); err != nil {
		return MCPServer{}, fmt.Errorf("invalid mcp server url: %w", err)
	}
	if !validMCPTransport(input.Transport) {
		return MCPServer{}, fmt.Errorf("unsupported mcp transport %q", input.Transport)
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	server := MCPServer{
		ID: input.ID, UserID: input.UserID, Name: input.Name, Description: input.Description,
		URL: input.URL, Transport: input.Transport, OAuthServerURL: input.OAuthServerURL,
		ClientID: input.ClientID, Scopes: input.Scopes, HeadersHelper: input.HeadersHelper,
		ConfigJSON: input.ConfigJSON, Builtin: input.Builtin, Enabled: enabled,
		CreatedAt: now, UpdatedAt: now,
	}
	if strings.TrimSpace(server.ConfigJSON) == "" {
		server.ConfigJSON = "{}"
	}
	if !json.Valid([]byte(server.ConfigJSON)) {
		return MCPServer{}, errors.New("mcp server config JSON is invalid")
	}
	return server, nil
}

func insertMCPServer(ctx context.Context, executor mcpServerExecer, server MCPServer) error {
	if _, err := executor.ExecContext(ctx, `
		INSERT INTO custom_mcp_servers
			(id, user_id, name, description, url, transport, oauth_server_url, client_id, scopes, headers_helper, config_json, builtin, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		server.ID, server.UserID, server.Name, server.Description, server.URL, server.Transport,
		server.OAuthServerURL, server.ClientID, server.Scopes, server.HeadersHelper,
		server.ConfigJSON, server.Builtin, server.Enabled, server.CreatedAt, server.UpdatedAt); err != nil {
		return fmt.Errorf("insert mcp server: %w", err)
	}
	return nil
}

func validMCPTransport(transport string) bool {
	switch transport {
	case "stdio", "sse", "streamable-http", "websocket":
		return true
	default:
		return false
	}
}

func (s *Store) SetMCPToolGrant(input MCPToolGrantInput) (MCPToolGrant, error) {
	if s == nil || s.db == nil {
		return MCPToolGrant{}, errors.New("workspace store is closed")
	}
	for field, value := range map[string]string{"mcp tool grant id": input.ID, "mcp server id": input.MCPServerID, "grant user id": input.UserID, "grant agent name": input.AgentName, "grant tool name": input.ToolName} {
		if strings.TrimSpace(value) == "" {
			return MCPToolGrant{}, fmt.Errorf("%s is required", field)
		}
	}
	var ownerID string
	if err := s.db.QueryRowContext(context.Background(), `SELECT user_id FROM custom_mcp_servers WHERE id = ?`, input.MCPServerID).Scan(&ownerID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return MCPToolGrant{}, fmt.Errorf("mcp server %q does not exist", input.MCPServerID)
		}
		return MCPToolGrant{}, fmt.Errorf("look up mcp server owner: %w", err)
	}
	if ownerID != input.UserID {
		return MCPToolGrant{}, errors.New("mcp tool grant user must own the mcp server")
	}
	now := s.now().UTC()
	grant := MCPToolGrant{ID: input.ID, MCPServerID: input.MCPServerID, UserID: input.UserID, AgentName: input.AgentName, ToolName: input.ToolName, Enabled: input.Enabled, CreatedAt: now, UpdatedAt: now}
	if _, err := s.db.ExecContext(context.Background(), `
		INSERT INTO mcp_tool_grants (id, mcp_server_id, user_id, agent_name, tool_name, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(mcp_server_id, user_id, agent_name, tool_name) DO UPDATE SET enabled = excluded.enabled, updated_at = excluded.updated_at`,
		grant.ID, grant.MCPServerID, grant.UserID, grant.AgentName, grant.ToolName, grant.Enabled, grant.CreatedAt, grant.UpdatedAt); err != nil {
		return MCPToolGrant{}, fmt.Errorf("upsert mcp tool grant: %w", err)
	}
	return grant, nil
}

// AssignMCPServerToAgent grants an agent access to an owned MCP server before
// per-tool grants are evaluated by the MCP transport layer.
func (s *Store) AssignMCPServerToAgent(input MCPAssignmentInput) (MCPAssignment, error) {
	if s == nil || s.db == nil {
		return MCPAssignment{}, errors.New("workspace store is closed")
	}
	for field, value := range map[string]string{"mcp assignment id": input.ID, "mcp server id": input.MCPServerID, "assignment user id": input.UserID, "assignment agent name": input.AgentName} {
		if strings.TrimSpace(value) == "" {
			return MCPAssignment{}, fmt.Errorf("%s is required", field)
		}
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MCPAssignment{}, fmt.Errorf("begin mcp assignment: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var serverOwner, agentID string
	if err := tx.QueryRowContext(ctx, `SELECT user_id FROM custom_mcp_servers WHERE id = ?`, input.MCPServerID).Scan(&serverOwner); err != nil {
		return MCPAssignment{}, fmt.Errorf("look up mcp assignment server: %w", err)
	}
	if serverOwner != input.UserID {
		return MCPAssignment{}, errors.New("mcp assignment user must own the mcp server")
	}
	if err := tx.QueryRowContext(ctx, `SELECT id FROM user_agents WHERE user_id = ? AND name = ?`, input.UserID, input.AgentName).Scan(&agentID); err != nil {
		return MCPAssignment{}, fmt.Errorf("look up mcp assignment agent: %w", err)
	}
	assignment := MCPAssignment{ID: input.ID, MCPServerID: input.MCPServerID, UserID: input.UserID, AgentName: input.AgentName, CreatedAt: s.now().UTC()}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO mcp_agent_assignments (id, mcp_server_id, user_id, agent_name, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(mcp_server_id, user_id, agent_name) DO NOTHING`, assignment.ID, assignment.MCPServerID, assignment.UserID, assignment.AgentName, assignment.CreatedAt); err != nil {
		return MCPAssignment{}, fmt.Errorf("insert mcp assignment: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return MCPAssignment{}, fmt.Errorf("commit mcp assignment: %w", err)
	}
	return assignment, nil
}

// UpsertMCPOAuthStatus stores a reference to an externally protected token,
// never a bearer token itself. This keeps SQLite exports credential-safe.
func (s *Store) UpsertMCPOAuthStatus(input MCPOAuthStatusInput) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	for field, value := range map[string]string{"mcp server id": input.MCPServerID, "oauth user id": input.UserID, "oauth access token reference": input.AccessTokenRef, "oauth token type": input.TokenType} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", field)
		}
	}
	var ownerID string
	if err := s.db.QueryRowContext(context.Background(), `SELECT user_id FROM custom_mcp_servers WHERE id = ?`, input.MCPServerID).Scan(&ownerID); err != nil {
		return fmt.Errorf("look up oauth mcp server: %w", err)
	}
	if ownerID != input.UserID {
		return errors.New("oauth user must own the mcp server")
	}
	scopes, err := json.Marshal(input.Scopes)
	if err != nil {
		return fmt.Errorf("marshal oauth scopes: %w", err)
	}
	now := s.now().UTC()
	var expiresAt any
	if input.ExpiresAt != nil {
		expiresAt = input.ExpiresAt.UTC()
	}
	_, err = s.db.ExecContext(context.Background(), `
		INSERT INTO mcp_oauth_status (mcp_server_id, user_id, access_token_ref, token_type, expires_at, scopes, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(mcp_server_id, user_id) DO UPDATE SET access_token_ref = excluded.access_token_ref, token_type = excluded.token_type, expires_at = excluded.expires_at, scopes = excluded.scopes, updated_at = excluded.updated_at`,
		input.MCPServerID, input.UserID, input.AccessTokenRef, input.TokenType, expiresAt, string(scopes), now, now)
	if err != nil {
		return fmt.Errorf("upsert mcp oauth status: %w", err)
	}
	return nil
}

func (s *Store) GetMCPOAuthStatus(serverID, userID string) (MCPOAuthStatus, bool, error) {
	if s == nil || s.db == nil {
		return MCPOAuthStatus{}, false, errors.New("workspace store is closed")
	}
	var status MCPOAuthStatus
	var rawScopes string
	err := s.db.QueryRowContext(context.Background(), `
		SELECT mcp_server_id, user_id, access_token_ref, token_type, expires_at, scopes, created_at, updated_at
		FROM mcp_oauth_status WHERE mcp_server_id = ? AND user_id = ?`, serverID, userID).Scan(&status.MCPServerID, &status.UserID, &status.AccessTokenRef, &status.TokenType, &status.ExpiresAt, &rawScopes, &status.CreatedAt, &status.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return MCPOAuthStatus{}, false, nil
	}
	if err != nil {
		return MCPOAuthStatus{}, false, fmt.Errorf("get mcp oauth status: %w", err)
	}
	if err := json.Unmarshal([]byte(rawScopes), &status.Scopes); err != nil {
		return MCPOAuthStatus{}, false, fmt.Errorf("decode oauth scopes: %w", err)
	}
	return status, true, nil
}

func (s *Store) DisconnectMCPOAuthCount(serverID, userID string) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("workspace store is closed")
	}
	if strings.TrimSpace(serverID) == "" || strings.TrimSpace(userID) == "" {
		return false, errors.New("mcp server id and user id are required")
	}
	result, err := s.db.ExecContext(context.Background(), `
		DELETE FROM mcp_oauth_status
		WHERE mcp_server_id = ? AND user_id = ?
			AND EXISTS (SELECT 1 FROM custom_mcp_servers WHERE id = ? AND user_id = ?)`,
		serverID, userID, serverID, userID)
	if err != nil {
		return false, fmt.Errorf("disconnect mcp oauth: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("count disconnected mcp oauth rows: %w", err)
	}
	return count > 0, nil
}

func (s *Store) DisconnectMCPOAuth(serverID, userID string) error {
	_, err := s.DisconnectMCPOAuthCount(serverID, userID)
	return err
}

func (s *Store) UpsertComputeProvider(input ComputeProviderInput) (ComputeProvider, error) {
	if s == nil || s.db == nil {
		return ComputeProvider{}, errors.New("workspace store is closed")
	}
	if strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.Family) == "" {
		return ComputeProvider{}, errors.New("compute provider name and family are required")
	}
	userID := strings.TrimSpace(input.UserID)
	if userID == "" {
		userID = "local"
	}
	environments, err := json.Marshal(input.Environments)
	if err != nil {
		return ComputeProvider{}, fmt.Errorf("marshal compute environments: %w", err)
	}
	now := s.now().UTC()
	provider := ComputeProvider{
		Name: input.Name, UserID: userID, Family: input.Family, Endpoint: input.Endpoint,
		SkillName: input.SkillName, CredentialName: input.CredentialName, Hosted: input.Hosted,
		Environments: append([]string(nil), input.Environments...), MemoryMD: input.MemoryMD,
		ScratchRoot: input.ScratchRoot, Scheduler: input.Scheduler, UpdatedAt: now,
	}
	stored, err := scanComputeProvider(s.db.QueryRowContext(context.Background(), `
		INSERT INTO compute_providers (
			name, owner_user_id, family, endpoint, skill_name, credential_name, hosted,
			environments, memory_md, scratch_root, scheduler, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			family = excluded.family,
			endpoint = excluded.endpoint,
			skill_name = excluded.skill_name,
			credential_name = excluded.credential_name,
			hosted = excluded.hosted,
			environments = excluded.environments,
			memory_md = excluded.memory_md,
			scratch_root = excluded.scratch_root,
			scheduler = excluded.scheduler,
			updated_at = excluded.updated_at
		WHERE compute_providers.owner_user_id = excluded.owner_user_id
		RETURNING `+computeProviderColumns,
		provider.Name, provider.UserID, provider.Family, provider.Endpoint, provider.SkillName,
		provider.CredentialName, provider.Hosted, string(environments), provider.MemoryMD,
		provider.ScratchRoot, provider.Scheduler, provider.UpdatedAt))
	if errors.Is(err, sql.ErrNoRows) {
		return ComputeProvider{}, fmt.Errorf("compute provider %q belongs to another user", provider.Name)
	}
	if err != nil {
		return ComputeProvider{}, fmt.Errorf("upsert compute provider: %w", err)
	}
	return stored, nil
}

func (s *Store) CreateComputeUsage(input ComputeUsageInput) (ComputeUsage, error) {
	if s == nil || s.db == nil {
		return ComputeUsage{}, errors.New("workspace store is closed")
	}
	for field, value := range map[string]string{"compute usage id": input.ID, "compute job id": input.JobID, "compute environment": input.Environment, "compute tier type": input.TierType, "compute provider": input.Provider} {
		if strings.TrimSpace(value) == "" {
			return ComputeUsage{}, fmt.Errorf("%s is required", field)
		}
	}
	var providerName string
	if err := s.db.QueryRowContext(context.Background(), `SELECT name FROM compute_providers WHERE name = ?`, input.Provider).Scan(&providerName); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ComputeUsage{}, fmt.Errorf("compute provider %q does not exist", input.Provider)
		}
		return ComputeUsage{}, fmt.Errorf("look up compute provider: %w", err)
	}
	startedAt := input.StartedAt.UTC()
	if startedAt.IsZero() {
		startedAt = s.now().UTC()
	}
	remoteHandle, err := normalizeComputeJSON("remote handle", input.RemoteHandle)
	if err != nil {
		return ComputeUsage{}, err
	}
	outputSpecs, err := normalizeComputeJSON("output specs", input.OutputSpecs)
	if err != nil {
		return ComputeUsage{}, err
	}
	var expiresAt *time.Time
	if input.ExpiresAt != nil {
		value := input.ExpiresAt.UTC()
		expiresAt = &value
	}
	usage := ComputeUsage{
		ID: strings.TrimSpace(input.ID), JobID: strings.TrimSpace(input.JobID),
		Environment: strings.TrimSpace(input.Environment), TierType: strings.TrimSpace(input.TierType),
		Provider: strings.TrimSpace(input.Provider), State: ComputeStatePending, StartedAt: startedAt,
		ExpiresAt: expiresAt, FrameID: strings.TrimSpace(input.FrameID),
		RootFrameID: strings.TrimSpace(input.RootFrameID), ProjectID: strings.TrimSpace(input.ProjectID),
		ClientUUID: strings.TrimSpace(input.ClientUUID), SubmitCellID: strings.TrimSpace(input.SubmitCellID),
		OriginToolUseID: strings.TrimSpace(input.OriginToolUseID),
		RemoteWorkdir:   strings.TrimSpace(input.RemoteWorkdir), RemoteHandle: remoteHandle,
		OutputSpecs: outputSpecs, Intent: strings.TrimSpace(input.Intent),
		HardwareDetails: strings.TrimSpace(input.HardwareDetails),
	}
	if _, err := s.db.ExecContext(context.Background(), `
		INSERT INTO compute_usage (
			id,job_id,environment,tier_type,provider,state,started_at,expires_at,
			frame_id,root_frame_id,project_id,client_uuid,submit_cell_id,origin_tool_use_id,
			remote_workdir,remote_handle,output_specs,intent,hardware_details
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		usage.ID, usage.JobID, usage.Environment, usage.TierType, usage.Provider, usage.State,
		usage.StartedAt, usage.ExpiresAt, nullableString(usage.FrameID), nullableString(usage.RootFrameID),
		nullableString(usage.ProjectID), nullableString(usage.ClientUUID), nullableString(usage.SubmitCellID),
		nullableString(usage.OriginToolUseID), nullableString(usage.RemoteWorkdir), nullableRawJSON(usage.RemoteHandle),
		nullableRawJSON(usage.OutputSpecs), nullableString(usage.Intent), nullableString(usage.HardwareDetails)); err != nil {
		return ComputeUsage{}, fmt.Errorf("insert compute usage: %w", err)
	}
	return usage, nil
}

func (s *Store) GetComputeUsage(id string) (ComputeUsage, bool, error) {
	if s == nil || s.db == nil {
		return ComputeUsage{}, false, errors.New("workspace store is closed")
	}
	usage, err := scanComputeUsage(s.db.QueryRowContext(
		context.Background(), computeUsageSelect+` WHERE id = ?`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return ComputeUsage{}, false, nil
	}
	if err != nil {
		return ComputeUsage{}, false, err
	}
	return usage, true, nil
}

func (s *Store) GetComputeUsageByJobID(jobID string) (ComputeUsage, bool, error) {
	if s == nil || s.db == nil {
		return ComputeUsage{}, false, errors.New("workspace store is closed")
	}
	usage, err := scanComputeUsage(s.db.QueryRowContext(
		context.Background(), computeUsageSelect+` WHERE job_id = ?`, strings.TrimSpace(jobID)))
	if errors.Is(err, sql.ErrNoRows) {
		return ComputeUsage{}, false, nil
	}
	if err != nil {
		return ComputeUsage{}, false, err
	}
	return usage, true, nil
}

func (s *Store) UpdateComputeUsageState(id string, next ComputeState, at time.Time) (ComputeUsage, error) {
	if s == nil || s.db == nil {
		return ComputeUsage{}, errors.New("workspace store is closed")
	}
	if id == "" || !validComputeState(next) {
		return ComputeUsage{}, errors.New("compute usage id and valid target state are required")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ComputeUsage{}, fmt.Errorf("begin compute state transition: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	usage, err := scanComputeUsage(tx.QueryRowContext(ctx, computeUsageSelect+` WHERE id = ?`, id))
	if err != nil {
		return ComputeUsage{}, fmt.Errorf("get compute usage: %w", err)
	}
	if !canTransitionCompute(usage.State, next) {
		return ComputeUsage{}, fmt.Errorf("invalid compute state transition %q -> %q", usage.State, next)
	}
	at = at.UTC()
	if at.IsZero() {
		at = s.now().UTC()
	}
	var endedAt any
	if isTerminalComputeState(next) {
		endedAt = at
	}
	result, err := tx.ExecContext(ctx,
		`UPDATE compute_usage SET state = ?, ended_at = ? WHERE id = ? AND state = ?`,
		next, endedAt, id, usage.State)
	if err != nil {
		return ComputeUsage{}, fmt.Errorf("update compute state: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return ComputeUsage{}, errors.New("compute usage changed during state transition")
	}
	usage.State = next
	if isTerminalComputeState(next) {
		usage.EndedAt = &at
	}
	if err := tx.Commit(); err != nil {
		return ComputeUsage{}, fmt.Errorf("commit compute state transition: %w", err)
	}
	return usage, nil
}

func (s *Store) UpdateComputeUsageRemote(id, remoteWorkdir string, remoteHandle, outputSpecs json.RawMessage, expiresAt *time.Time) (ComputeUsage, error) {
	if s == nil || s.db == nil {
		return ComputeUsage{}, errors.New("workspace store is closed")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return ComputeUsage{}, errors.New("compute usage id is required")
	}
	handle, err := normalizeComputeJSON("remote handle", remoteHandle)
	if err != nil {
		return ComputeUsage{}, err
	}
	if len(handle) == 0 {
		return ComputeUsage{}, errors.New("remote handle is required")
	}
	specs, err := normalizeComputeJSON("output specs", outputSpecs)
	if err != nil {
		return ComputeUsage{}, err
	}
	current, found, err := s.GetComputeUsage(id)
	if err != nil {
		return ComputeUsage{}, err
	}
	if !found {
		return ComputeUsage{}, sql.ErrNoRows
	}
	if isTerminalComputeState(current.State) {
		return ComputeUsage{}, fmt.Errorf("cannot bind remote metadata to terminal compute usage %q", current.State)
	}
	var expiry any
	if expiresAt != nil {
		expiry = expiresAt.UTC()
	}
	result, err := s.db.ExecContext(context.Background(), `
		UPDATE compute_usage SET remote_workdir=?,remote_handle=?,output_specs=?,expires_at=?
		WHERE id=? AND state=?`,
		nullableString(strings.TrimSpace(remoteWorkdir)), string(handle), nullableRawJSON(specs),
		expiry, id, current.State)
	if err != nil {
		return ComputeUsage{}, err
	}
	rows, _ := result.RowsAffected()
	if rows != 1 {
		return ComputeUsage{}, errors.New("compute usage changed while binding remote metadata")
	}
	updated, _, err := s.GetComputeUsage(id)
	return updated, err
}

func (s *Store) CompleteComputeUsage(id string, next ComputeState, result json.RawMessage, at time.Time) (ComputeUsage, error) {
	if !isTerminalComputeState(next) {
		return ComputeUsage{}, errors.New("terminal compute state is required")
	}
	normalizedResult, err := normalizeComputeJSON("compute result", result)
	if err != nil {
		return ComputeUsage{}, err
	}
	current, found, err := s.GetComputeUsage(id)
	if err != nil {
		return ComputeUsage{}, err
	}
	if !found {
		return ComputeUsage{}, sql.ErrNoRows
	}
	if !canTransitionCompute(current.State, next) {
		return ComputeUsage{}, fmt.Errorf("invalid compute state transition %q -> %q", current.State, next)
	}
	at = at.UTC()
	if at.IsZero() {
		at = s.now().UTC()
	}
	updated, err := s.db.ExecContext(context.Background(), `
		UPDATE compute_usage SET state=?,ended_at=?,result=? WHERE id=? AND state=?`,
		next, at, nullableRawJSON(normalizedResult), strings.TrimSpace(id), current.State)
	if err != nil {
		return ComputeUsage{}, err
	}
	rows, _ := updated.RowsAffected()
	if rows != 1 {
		return ComputeUsage{}, errors.New("compute usage changed while completing")
	}
	completed, _, err := s.GetComputeUsage(id)
	return completed, err
}

func (s *Store) ListActiveComputeUsage(provider string) ([]ComputeUsage, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	rows, err := s.db.QueryContext(context.Background(), computeUsageSelect+`
		WHERE provider = ? AND state IN (?,?,?,?) AND remote_handle IS NOT NULL
		ORDER BY started_at,id`, strings.TrimSpace(provider), ComputeStateStaging,
		ComputeStateQueued, ComputeStateRunning, ComputeStateHarvesting)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var usages []ComputeUsage
	for rows.Next() {
		usage, err := scanComputeUsage(rows)
		if err != nil {
			return nil, err
		}
		usages = append(usages, usage)
	}
	return usages, rows.Err()
}

func validComputeState(state ComputeState) bool {
	switch state {
	case ComputeStatePending, ComputeStateStaging, ComputeStateQueued, ComputeStateRunning,
		ComputeStateHarvesting, ComputeStateDone, ComputeStateFailed, ComputeStateTimedOut,
		ComputeStateOrphaned:
		return true
	default:
		return false
	}
}

func canTransitionCompute(current, next ComputeState) bool {
	failureTerminal := next == ComputeStateFailed || next == ComputeStateTimedOut || next == ComputeStateOrphaned
	switch current {
	case ComputeStatePending:
		return next == ComputeStateStaging || next == ComputeStateQueued || next == ComputeStateRunning || failureTerminal
	case ComputeStateStaging:
		return next == ComputeStateQueued || next == ComputeStateRunning || failureTerminal
	case ComputeStateQueued:
		return next == ComputeStateRunning || failureTerminal
	case ComputeStateRunning:
		return next == ComputeStateHarvesting || next == ComputeStateDone || failureTerminal
	case ComputeStateHarvesting:
		return next == ComputeStateDone || failureTerminal
	default:
		return false
	}
}

func isTerminalComputeState(state ComputeState) bool {
	return state == ComputeStateDone || state == ComputeStateFailed ||
		state == ComputeStateTimedOut || state == ComputeStateOrphaned
}

const computeUsageSelect = `SELECT id,job_id,environment,tier_type,provider,state,started_at,ended_at,
	expires_at,frame_id,root_frame_id,project_id,client_uuid,submit_cell_id,origin_tool_use_id,
	remote_workdir,remote_handle,output_specs,result,intent,hardware_details FROM compute_usage`

func scanComputeUsage(row rowScanner) (ComputeUsage, error) {
	var usage ComputeUsage
	var endedAt, expiresAt sql.NullTime
	var frameID, rootFrameID, projectID, clientUUID, submitCellID, originToolUseID sql.NullString
	var remoteWorkdir, remoteHandle, outputSpecs, result, intent, hardwareDetails sql.NullString
	if err := row.Scan(
		&usage.ID, &usage.JobID, &usage.Environment, &usage.TierType, &usage.Provider,
		&usage.State, &usage.StartedAt, &endedAt, &expiresAt, &frameID, &rootFrameID,
		&projectID, &clientUUID, &submitCellID, &originToolUseID, &remoteWorkdir,
		&remoteHandle, &outputSpecs, &result, &intent, &hardwareDetails,
	); err != nil {
		return ComputeUsage{}, err
	}
	if endedAt.Valid {
		value := endedAt.Time.UTC()
		usage.EndedAt = &value
	}
	if expiresAt.Valid {
		value := expiresAt.Time.UTC()
		usage.ExpiresAt = &value
	}
	usage.FrameID = frameID.String
	usage.RootFrameID = rootFrameID.String
	usage.ProjectID = projectID.String
	usage.ClientUUID = clientUUID.String
	usage.SubmitCellID = submitCellID.String
	usage.OriginToolUseID = originToolUseID.String
	usage.RemoteWorkdir = remoteWorkdir.String
	usage.RemoteHandle = rawJSONFromNullString(remoteHandle)
	usage.OutputSpecs = rawJSONFromNullString(outputSpecs)
	usage.Result = rawJSONFromNullString(result)
	usage.Intent = intent.String
	usage.HardwareDetails = hardwareDetails.String
	return usage, nil
}

func normalizeComputeJSON(name string, value json.RawMessage) (json.RawMessage, error) {
	if len(value) == 0 {
		return nil, nil
	}
	if !json.Valid(value) {
		return nil, fmt.Errorf("%s must be valid JSON", name)
	}
	return append(json.RawMessage(nil), value...), nil
}

func nullableRawJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return string(value)
}

func rawJSONFromNullString(value sql.NullString) json.RawMessage {
	if !value.Valid || value.String == "" {
		return nil
	}
	return json.RawMessage(append([]byte(nil), value.String...))
}
