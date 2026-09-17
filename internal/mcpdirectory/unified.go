package mcpdirectory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/tools/mcpstdio"
)

const bundledProbeConcurrency = 8
const bundledBootstrapTimeout = 60 * time.Second

func (s *Service) ListUnifiedConnectors(ctx context.Context, userID string) ([]Connector, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("MCP directory service is not configured")
	}
	directoryConnectors, err := s.ListConnectors(ctx, userID)
	if err != nil {
		return nil, err
	}
	states, err := s.store.ListMCPConnectorRuntimeStates(userID)
	if err != nil {
		return nil, err
	}
	stateByKey := map[string]workspace.MCPConnectorRuntimeState{}
	for _, state := range states {
		stateByKey[state.Source+"\x00"+state.ConnectorID] = state
	}
	seen := map[string]string{}
	connectors := make([]Connector, 0, len(s.bundled)+len(directoryConnectors))
	for _, item := range orderedBundled(s.bundled) {
		if item.Deferred && !s.optionalConnectorInstalled(item) {
			continue
		}
		if prior := seen[item.ID]; prior != "" {
			return nil, duplicateConnectorError(item.ID, prior, "bundled")
		}
		seen[item.ID] = "bundled"
		apiKeyConfigured, err := s.bundledAPIKeyConfigured(userID, item)
		if err != nil {
			return nil, err
		}
		authorized := mcpstdio.OAuthConnected(s.root, bundledOAuthKey(userID, item.ID)) || apiKeyConfigured
		authState, connectionStatus := "not-required", "connecting"
		if item.AuthRequired {
			authState, connectionStatus = "unauthorized", "auth_required"
			if authorized {
				authState, connectionStatus = "authorized", "connecting"
			}
		}
		connector := Connector{
			ID: item.ID, Name: item.Name, DisplayName: item.DisplayName, Description: item.Description,
			URL: item.Config.URL, Transport: transportLabel(item.Config), Source: "bundled", Enabled: true,
			HostedBySynon: item.HostedBySynon, AuthRequired: item.AuthRequired, OAuthSupported: item.OAuthSupported,
			AuthState: authState, AuthHint: item.AuthHint, ConnectionStatus: connectionStatus,
			APIKeyConfigurable: item.APIKeyHeader != "" || item.APIKeyQueryParam != "", APIKeyConfigured: apiKeyConfigured,
			APIKeyLabel:    item.APIKeyLabel,
			AttachedAgents: []string{"OPERON"}, Upstreams: item.Upstreams,
		}
		if state, ok := stateByKey["bundled\x00"+item.ID]; ok {
			// A persisted enabled snapshot belongs to an earlier service
			// lifetime until it has been checked by this instance. Keep the
			// connector in its honest connecting state while the bootstrap
			// probe runs instead of presenting stale connected health.
			if !state.Enabled || bundledRuntimeStateIsFresh(state, s.startedAt) {
				applyRuntimeState(&connector, state)
			}
		}
		if item.AuthRequired && !authorized {
			connector.AuthState = "unauthorized"
			connector.ConnectionStatus = "auth_required"
			connector.ConnectionError = "connector authorization is required"
			connector.ToolCount = 0
			connector.SchemaSHA256 = ""
			connector.Health = &ConnectorHealth{OK: false, Error: connector.ConnectionError, AuthRequired: true}
		}
		connectors = append(connectors, connector)
	}
	for i := range directoryConnectors {
		connector := directoryConnectors[i]
		if prior := seen[connector.ID]; prior != "" {
			return nil, duplicateConnectorError(connector.ID, prior, connector.Source)
		}
		seen[connector.ID] = connector.Source
		if state, ok := stateByKey["directory\x00"+connector.ID]; ok {
			connector.SchemaSHA256 = state.SchemaSHA256
		}
		connectors = append(connectors, connector)
	}
	custom, err := s.store.ListMCPServers(userID)
	if err != nil {
		return nil, err
	}
	for _, server := range custom {
		if prior := seen[server.ID]; prior != "" {
			return nil, duplicateConnectorError(server.ID, prior, "custom")
		}
		seen[server.ID] = "custom"
		assignments, err := s.store.ListMCPAssignments(server.ID, userID)
		if err != nil {
			return nil, err
		}
		agents := make([]string, 0, len(assignments))
		for _, a := range assignments {
			agents = append(agents, a.AgentName)
		}
		status := "configured"
		if !server.Enabled {
			status = "disabled"
		}
		connector := Connector{ID: server.ID, Name: server.Name, DisplayName: server.Name, URL: server.URL, Transport: server.Transport, Source: "custom", Enabled: server.Enabled, AuthState: "not-required", ConnectionStatus: status, AttachedAgents: agents}
		if state, ok := stateByKey["custom\x00"+server.ID]; ok {
			applyRuntimeState(&connector, state)
			connector.Enabled = server.Enabled
		}
		connectors = append(connectors, connector)
	}
	return connectors, nil
}

func (s *Service) SetUnifiedEnabled(ctx context.Context, userID, id string, enabled bool) (Connector, error) {
	if strings.HasPrefix(id, "bundled:") {
		return s.setBundledEnabled(ctx, userID, id, enabled)
	}
	if row, found, err := s.store.GetMCPDirectoryConnector(userID, id); err != nil {
		return Connector{}, err
	} else if found {
		_ = row
		return s.SetEnabled(ctx, userID, id, enabled)
	}
	if server, found, err := s.store.GetMCPServer(id, userID); err != nil {
		return Connector{}, err
	} else if found {
		return s.setCustomEnabled(ctx, userID, server, enabled)
	}
	return Connector{}, errors.New("MCP connector not found")
}

func (s *Service) AuthorizeUnified(ctx context.Context, userID, id string) (mcpstdio.OAuthStartResult, error) {
	if item, found := s.bundled[strings.TrimSpace(id)]; found {
		if !item.AuthRequired {
			return mcpstdio.OAuthStartResult{}, errors.New("MCP bundled connector does not require authorization")
		}
		if !item.OAuthSupported {
			return mcpstdio.OAuthStartResult{}, errors.New("MCP bundled connector requires an owner-provided API key")
		}
		config, err := s.bundledRuntimeConfigForUser(userID, item)
		if err != nil {
			return mcpstdio.OAuthStartResult{}, err
		}
		config.ConnectorID = bundledOAuthKey(userID, item.ID)
		config.Name = item.Name
		return mcpstdio.AuthenticateServer(ctx, s.root, item.Name, config)
	}
	return s.Authorize(ctx, userID, id)
}

func (s *Service) DisconnectUnified(ctx context.Context, userID, id string) error {
	if item, found := s.bundled[strings.TrimSpace(id)]; found {
		if err := mcpstdio.DisconnectOAuth(s.root, bundledOAuthKey(userID, item.ID)); err != nil {
			return err
		}
		state, stateFound, err := s.store.GetMCPConnectorRuntimeState(userID, "bundled", item.ID)
		if err != nil {
			return err
		}
		if stateFound && state.Enabled {
			state.LastStatus = "auth_required"
			state.LastError = "connector authorization is required"
			state.ToolCount = 0
			state.SchemaSHA256 = ""
			return s.store.PutMCPConnectorRuntimeState(state)
		}
		return nil
	}
	return s.Disconnect(ctx, userID, id)
}

// ProbeMissingBundled persists a real tools/list health snapshot for bundled
// connectors that have never been checked for this owner. Existing enabled,
// disabled, and failed states remain stable until an explicit user action.
func (s *Service) ProbeMissingBundled(ctx context.Context, userID string) error {
	if s == nil || s.store == nil {
		return errors.New("MCP directory service is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return errors.New("MCP connector owner is required")
	}
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	s.bootstrapMu.Lock()
	closed := s.closed
	s.bootstrapMu.Unlock()
	if closed {
		return errors.New("MCP directory service is closed")
	}

	states, err := s.store.ListMCPConnectorRuntimeStates(userID)
	if err != nil {
		return err
	}
	stateByID := make(map[string]workspace.MCPConnectorRuntimeState, len(states))
	for _, state := range states {
		if state.Source == "bundled" {
			stateByID[state.ConnectorID] = state
		}
	}
	pending := make([]bundledConnector, 0, len(s.bundled))
	for _, item := range orderedBundled(s.bundled) {
		if item.Deferred && !s.optionalConnectorInstalled(item) {
			continue
		}
		state, found := stateByID[item.ID]
		if !found || bundledRuntimeStateNeedsProbe(state, s.startedAt) {
			pending = append(pending, item)
		}
	}
	if len(pending) == 0 {
		return nil
	}

	workerCount := min(bundledProbeConcurrency, len(pending))
	jobs := make(chan bundledConnector)
	var workers sync.WaitGroup
	var failuresMu sync.Mutex
	var failures []error
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for item := range jobs {
				apiKeyConfigured, keyErr := s.bundledAPIKeyConfigured(userID, item)
				if keyErr != nil {
					failuresMu.Lock()
					failures = append(failures, keyErr)
					failuresMu.Unlock()
					continue
				}
				if item.AuthRequired && !mcpstdio.OAuthConnected(s.root, bundledOAuthKey(userID, item.ID)) && !apiKeyConfigured {
					state := workspace.MCPConnectorRuntimeState{
						UserID: userID, Source: "bundled", ConnectorID: item.ID, Enabled: true,
						LastStatus: "auth_required", LastError: "connector authorization is required",
					}
					if err := s.store.PutMCPConnectorRuntimeState(state); err != nil {
						failuresMu.Lock()
						failures = append(failures, fmt.Errorf("persist bundled connector %s authorization state: %w", item.ID, err))
						failuresMu.Unlock()
					}
					continue
				}
				config, configErr := s.bundledRuntimeConfigForUser(userID, item)
				if configErr != nil {
					failuresMu.Lock()
					failures = append(failures, configErr)
					failuresMu.Unlock()
					continue
				}
				config.ConnectorID = bundledOAuthKey(userID, item.ID)
				status, probeError, toolCount, schemaSHA256 := s.probeConfig(ctx, item.ID, config)
				if ctx.Err() != nil {
					failuresMu.Lock()
					failures = append(failures, ctx.Err())
					failuresMu.Unlock()
					continue
				}
				state := workspace.MCPConnectorRuntimeState{
					UserID: userID, Source: "bundled", ConnectorID: item.ID, Enabled: true,
					LastStatus: status, LastError: probeError, ToolCount: toolCount, SchemaSHA256: schemaSHA256,
				}
				existing, found, err := s.store.GetMCPConnectorRuntimeState(userID, "bundled", item.ID)
				if err != nil {
					failuresMu.Lock()
					failures = append(failures, fmt.Errorf("read bundled connector %s health before persist: %w", item.ID, err))
					failuresMu.Unlock()
					continue
				}
				var persistErr error
				if !found {
					_, persistErr = s.store.CreateMCPConnectorRuntimeState(state)
				} else if !existing.Enabled {
					continue
				} else if stateWasStale, err := s.store.RefreshMCPConnectorRuntimeStateIfEnabledAndStale(state, s.startedAt); err != nil {
					persistErr = err
				} else if !stateWasStale {
					// The owner changed the connector while this probe was in
					// flight, so the newer decision wins.
					continue
				}
				if persistErr != nil {
					failuresMu.Lock()
					failures = append(failures, fmt.Errorf("persist bundled connector %s health: %w", item.ID, persistErr))
					failuresMu.Unlock()
				}
			}
		}()
	}
	for _, item := range pending {
		if ctx.Err() != nil {
			break
		}
		jobs <- item
	}
	close(jobs)
	workers.Wait()
	return errors.Join(failures...)
}

// bundledRuntimeStateIsFresh reports whether a runtime snapshot was written
// by this service lifetime. A persisted snapshot from a previous process is
// useful as history, but cannot prove that the connector is reachable now.
func bundledRuntimeStateIsFresh(state workspace.MCPConnectorRuntimeState, startedAt time.Time) bool {
	return !state.UpdatedAt.IsZero() && !startedAt.IsZero() && !state.UpdatedAt.Before(startedAt)
}

func bundledRuntimeStateNeedsProbe(state workspace.MCPConnectorRuntimeState, startedAt time.Time) bool {
	return state.Enabled && !bundledRuntimeStateIsFresh(state, startedAt)
}

// ScheduleMissingBundledProbe initializes a new owner's bundled connectors in
// the background. It deduplicates concurrent list requests while preserving
// ProbeMissingBundled's persisted enabled/disabled decisions and real
// tools/list health snapshots.
func (s *Service) ScheduleMissingBundledProbe(userID string) bool {
	if s == nil || s.store == nil {
		return false
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false
	}

	s.bootstrapMu.Lock()
	if s.bootstrapOwners == nil {
		s.bootstrapOwners = make(map[string]struct{})
	}
	if s.closed {
		s.bootstrapMu.Unlock()
		return false
	}
	if _, running := s.bootstrapOwners[userID]; running {
		s.bootstrapMu.Unlock()
		return false
	}
	s.bootstrapOwners[userID] = struct{}{}
	bootstrapCtx := s.bootstrapCtx
	if bootstrapCtx == nil {
		bootstrapCtx = context.Background()
	}
	s.bootstrapWG.Add(1)
	s.bootstrapMu.Unlock()

	go func() {
		defer s.bootstrapWG.Done()
		defer func() {
			s.bootstrapMu.Lock()
			delete(s.bootstrapOwners, userID)
			s.bootstrapMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(bootstrapCtx, bundledBootstrapTimeout)
		defer cancel()
		if err := s.ProbeMissingBundled(ctx, userID); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("initialize bundled MCP connector health: %v", err)
		}
	}()
	return true
}

func (s *Service) setBundledEnabled(ctx context.Context, userID, id string, enabled bool) (Connector, error) {
	item, ok := s.bundled[id]
	if !ok {
		return Connector{}, errors.New("MCP bundled connector not found")
	}
	connector := Connector{
		ID: item.ID, Name: item.Name, DisplayName: item.DisplayName, Description: item.Description,
		URL: item.Config.URL, Transport: transportLabel(item.Config), Source: "bundled", Enabled: enabled,
		HostedBySynon: item.HostedBySynon, AuthRequired: item.AuthRequired, OAuthSupported: item.OAuthSupported, AuthHint: item.AuthHint,
		APIKeyConfigurable: item.APIKeyHeader != "" || item.APIKeyQueryParam != "", APIKeyLabel: item.APIKeyLabel,
		AuthState: "not-required", AttachedAgents: []string{"OPERON"}, Upstreams: item.Upstreams,
	}
	state := workspace.MCPConnectorRuntimeState{UserID: userID, Source: "bundled", ConnectorID: id, Enabled: enabled, LastStatus: "disabled"}
	if enabled {
		apiKeyConfigured, err := s.bundledAPIKeyConfigured(userID, item)
		if err != nil {
			return Connector{}, err
		}
		connector.APIKeyConfigured = apiKeyConfigured
		if item.AuthRequired && !mcpstdio.OAuthConnected(s.root, bundledOAuthKey(userID, item.ID)) && !apiKeyConfigured {
			connector.AuthState = "unauthorized"
			state.LastStatus = "auth_required"
			state.LastError = "connector authorization is required"
		} else {
			if item.AuthRequired {
				connector.AuthState = "authorized"
			}
			config, err := s.bundledRuntimeConfigForUser(userID, item)
			if err != nil {
				return Connector{}, err
			}
			config.ConnectorID = bundledOAuthKey(userID, item.ID)
			state.LastStatus, state.LastError, state.ToolCount, state.SchemaSHA256 = s.probeConfig(ctx, id, config)
		}
	}
	if err := s.store.PutMCPConnectorRuntimeState(state); err != nil {
		return Connector{}, err
	}
	applyRuntimeState(&connector, state)
	return connector, nil
}

func (s *Service) setCustomEnabled(ctx context.Context, userID string, server workspace.MCPServer, enabled bool) (Connector, error) {
	config := s.applyTLSPosture(mcpstdio.ServerConfig{Name: server.ID, Type: server.Transport, URL: server.URL, Scope: "custom"})
	return s.SetCustomEnabledWithConfig(ctx, userID, server, enabled, config)
}

// SetCustomEnabledWithConfig updates unified custom-connector state while
// allowing the server layer to supply the decrypted owner-scoped transport.
func (s *Service) SetCustomEnabledWithConfig(ctx context.Context, userID string, server workspace.MCPServer, enabled bool, config mcpstdio.ServerConfig) (Connector, error) {
	config = s.applyTLSPosture(config)
	updated, err := s.store.UpdateMCPServer(server.ID, userID, workspace.UpdateMCPServerInput{Enabled: &enabled})
	if err != nil {
		return Connector{}, err
	}
	state := workspace.MCPConnectorRuntimeState{UserID: userID, Source: "custom", ConnectorID: server.ID, Enabled: enabled, LastStatus: "disabled"}
	if enabled {
		config.Disabled = false
		if strings.TrimSpace(config.Name) == "" {
			config.Name = updated.Name
		}
		config.Scope = "custom"
		state.LastStatus, state.LastError, state.ToolCount, state.SchemaSHA256 = s.probeConfig(ctx, server.ID, config)
	}
	if err := s.store.PutMCPConnectorRuntimeState(state); err != nil {
		return Connector{}, err
	}
	assignments, err := s.store.ListMCPAssignments(updated.ID, userID)
	if err != nil {
		return Connector{}, err
	}
	agents := make([]string, 0, len(assignments))
	for _, assignment := range assignments {
		agents = append(agents, assignment.AgentName)
	}
	connector := Connector{ID: updated.ID, Name: updated.Name, DisplayName: updated.Name, URL: updated.URL, Transport: updated.Transport, Source: "custom", Enabled: updated.Enabled, AuthState: "not-required", AttachedAgents: agents}
	applyRuntimeState(&connector, state)
	return connector, nil
}

func (s *Service) probeConfig(ctx context.Context, id string, config mcpstdio.ServerConfig) (string, string, int, string) {
	client := s.client
	if strings.TrimSpace(config.URL) != "" {
		secureClient, err := s.secureHTTPClient(ctx, config.URL)
		if err != nil {
			return "error", truncateError(err), 0, ""
		}
		client = secureClient
	}
	probeCtx, cancel := context.WithTimeout(mcpstdio.WithHTTPClient(ctx, client), 12*time.Second)
	defer cancel()
	if strings.EqualFold(config.Type, "streamable-http") {
		config.Type = "streamable_http"
	}
	tools, err := mcpstdio.ListToolsForServer(probeCtx, s.root, id, config)
	if err != nil {
		return "error", truncateError(err), 0, ""
	}
	return "connected", "", len(tools), toolSchemaHash(tools)
}

func toolSchemaHash(tools []mcpstdio.ToolProjection) string {
	type schema struct {
		Name  string         `json:"name"`
		Input map[string]any `json:"inputSchema"`
	}
	items := make([]schema, 0, len(tools))
	for _, tool := range tools {
		items = append(items, schema{Name: tool.ToolName, Input: tool.InputSchema})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	raw, _ := json.Marshal(items)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func applyRuntimeState(c *Connector, s workspace.MCPConnectorRuntimeState) {
	c.Enabled = s.Enabled
	c.ConnectionStatus = s.LastStatus
	c.ConnectionError = s.LastError
	c.ToolCount = s.ToolCount
	c.SchemaSHA256 = s.SchemaSHA256
	if s.LastStatus == "connected" {
		c.Health = &ConnectorHealth{OK: true, ToolCount: s.ToolCount}
		if c.AuthRequired {
			c.AuthState = "authorized"
		}
	} else if s.LastStatus == "auth_required" {
		c.Health = &ConnectorHealth{OK: false, Error: s.LastError, AuthRequired: true}
		c.AuthState = "unauthorized"
	} else if s.LastStatus == "error" {
		c.Health = &ConnectorHealth{OK: false, Error: s.LastError}
	}
}
func duplicateConnectorError(id, a, b string) error {
	return fmt.Errorf("duplicate MCP connector id %q across %s and %s sources", id, a, b)
}

func bundledOAuthKey(userID, connectorID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(userID) + "\x00" + strings.TrimSpace(connectorID)))
	return "bundled-" + hex.EncodeToString(digest[:])
}
func transportLabel(config mcpstdio.ServerConfig) string {
	if config.Type != "" {
		return config.Type
	}
	if config.Command != "" {
		return "stdio"
	}
	return "streamable-http"
}
