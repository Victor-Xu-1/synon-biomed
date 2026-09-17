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
	"time"
	"unicode"

	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/tools/mcpstdio"
)

const (
	connectorToolCatalogVersion            = 2
	connectorToolCatalogMaxTools           = 512
	connectorToolCatalogMaxDescription     = 16 << 10
	connectorToolCatalogFreshFor           = 5 * time.Minute
	connectorToolCatalogMaxAge             = 7 * 24 * time.Hour
	connectorToolCatalogRequestTimeout     = 30 * time.Second
	connectorToolCatalogRefreshTimeout     = 30 * time.Second
	connectorToolCatalogRefreshConcurrency = 8
	// A connector that is currently unauthorized or unreachable should not be
	// reprobed on every model turn. The failure is retried automatically after
	// this short cooldown, so credentials or network recovery are picked up
	// without making the task wait on repeated doomed handshakes.
	connectorToolCatalogFailureBackoff = 2 * time.Minute
)

type connectorToolCatalogEntry struct {
	ToolName     string         `json:"toolName"`
	Description  string         `json:"description,omitempty"`
	InputSchema  map[string]any `json:"inputSchema,omitempty"`
	OutputSchema map[string]any `json:"outputSchema,omitempty"`
	ReadOnlyHint bool           `json:"readOnlyHint,omitempty"`
}

type connectorToolCatalogDocument struct {
	Version int                         `json:"version"`
	Tools   []connectorToolCatalogEntry `json:"tools"`
}

type connectorToolCatalogFlight struct {
	done  chan struct{}
	tools []mcpstdio.ToolProjection
	err   error
}

type connectorToolCatalogFailure struct {
	err     error
	retryAt time.Time
}

func (s *Service) listCachedOrRefreshConnectorTools(ctx context.Context, userID string, connector RuntimeConnector) ([]mcpstdio.ToolProjection, error) {
	tools, found, refresh, err := s.loadConnectorToolCatalog(userID, connector, time.Now().UTC())
	if err != nil {
		log.Printf("discard invalid MCP connector %s tool catalog: %v", connector.ID, err)
		if deleteErr := s.store.DeleteMCPConnectorToolCatalog(userID, connector.Source, connector.ID); deleteErr != nil {
			return nil, errors.Join(err, deleteErr)
		}
		found = false
	}
	if found {
		if refresh {
			_, _, _ = s.startConnectorToolCatalogRefresh(userID, connector)
		}
		return tools, nil
	}
	flight, _, err := s.startConnectorToolCatalogRefresh(userID, connector)
	if err != nil {
		return nil, err
	}
	select {
	case <-flight.done:
		return append([]mcpstdio.ToolProjection(nil), flight.tools...), flight.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ScheduleBundledToolCatalogWarmup starts bounded, deduplicated refreshes for
// missing or stale owner-scoped bundled catalogs. Valid catalogs are served
// immediately while stale entries refresh in the background.
func (s *Service) ScheduleBundledToolCatalogWarmup(userID string) bool {
	if s == nil || s.store == nil {
		return false
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false
	}
	scheduled := false
	for _, item := range orderedBundled(s.bundled) {
		connector, found, err := s.ResolveRuntimeConnector(context.Background(), userID, item.ID)
		if err != nil || !found || connector.Config.Disabled {
			continue
		}
		_, cached, refresh, cacheErr := s.loadConnectorToolCatalog(userID, connector, time.Now().UTC())
		if cacheErr != nil {
			log.Printf("discard invalid MCP connector %s tool catalog during warmup: %v", connector.ID, cacheErr)
			_ = s.store.DeleteMCPConnectorToolCatalog(userID, connector.Source, connector.ID)
			cached = false
		}
		if cached && !refresh {
			continue
		}
		if _, started, startErr := s.startConnectorToolCatalogRefresh(userID, connector); startErr == nil && started {
			scheduled = true
		}
	}
	return scheduled
}

func (s *Service) loadConnectorToolCatalog(userID string, connector RuntimeConnector, now time.Time) ([]mcpstdio.ToolProjection, bool, bool, error) {
	catalog, found, err := s.store.GetMCPConnectorToolCatalog(userID, connector.Source, connector.ID)
	if err != nil || !found {
		return nil, false, false, err
	}
	if catalog.ConfigSHA256 != connectorToolConfigurationSHA256(connector) {
		return nil, false, false, nil
	}
	age := now.Sub(catalog.RefreshedAt.UTC())
	if age < 0 {
		age = 0
	}
	if age > connectorToolCatalogMaxAge {
		return nil, false, false, nil
	}
	tools, err := decodeConnectorToolCatalog(connector, []byte(catalog.CatalogJSON), catalog.CatalogSHA256)
	if err != nil {
		return nil, false, false, err
	}
	return tools, true, age > connectorToolCatalogFreshFor, nil
}

func (s *Service) startConnectorToolCatalogRefresh(userID string, connector RuntimeConnector) (*connectorToolCatalogFlight, bool, error) {
	key := strings.TrimSpace(userID) + "\x00" + connector.Source + "\x00" + connector.ID
	s.bootstrapMu.Lock()
	if s.closed {
		s.bootstrapMu.Unlock()
		return nil, false, errors.New("MCP directory service is closed")
	}
	base := s.bootstrapCtx
	if base == nil {
		base = context.Background()
	}
	s.catalogMu.Lock()
	if flight := s.catalogFlights[key]; flight != nil {
		s.catalogMu.Unlock()
		s.bootstrapMu.Unlock()
		return flight, false, nil
	}
	if failure, found := s.catalogFailures[key]; found && time.Now().UTC().Before(failure.retryAt) {
		flight := completedConnectorToolCatalogFailure(failure.err)
		s.catalogMu.Unlock()
		s.bootstrapMu.Unlock()
		return flight, false, nil
	}
	flight := &connectorToolCatalogFlight{done: make(chan struct{})}
	s.catalogFlights[key] = flight
	s.catalogWG.Add(1)
	s.catalogMu.Unlock()
	s.bootstrapMu.Unlock()

	go func() {
		defer s.catalogWG.Done()
		refreshCtx, cancel := context.WithTimeout(base, connectorToolCatalogRefreshTimeout)
		defer cancel()
		flight.tools, flight.err = s.refreshConnectorToolCatalog(refreshCtx, userID, connector)
		s.catalogMu.Lock()
		if flight.err != nil {
			s.catalogFailures[key] = connectorToolCatalogFailure{
				err: flight.err, retryAt: time.Now().UTC().Add(connectorToolCatalogFailureBackoff),
			}
		} else {
			delete(s.catalogFailures, key)
		}
		close(flight.done)
		delete(s.catalogFlights, key)
		s.catalogMu.Unlock()
	}()
	return flight, true, nil
}

func completedConnectorToolCatalogFailure(err error) *connectorToolCatalogFlight {
	flight := &connectorToolCatalogFlight{done: make(chan struct{}), err: err}
	close(flight.done)
	return flight
}

func (s *Service) refreshConnectorToolCatalog(ctx context.Context, userID string, connector RuntimeConnector) ([]mcpstdio.ToolProjection, error) {
	if s.catalogSlots != nil {
		select {
		case s.catalogSlots <- struct{}{}:
			defer func() { <-s.catalogSlots }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	current, found, err := s.ResolveRuntimeConnector(ctx, userID, connector.ID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrConnectorNotFound
	}
	if current.Config.Disabled {
		return nil, errors.New("MCP connector is disabled")
	}
	expectedConfigSHA256 := connectorToolConfigurationSHA256(connector)
	if connectorToolConfigurationSHA256(current) != expectedConfigSHA256 {
		return nil, ErrConnectorChanged
	}
	runtimeCtx, err := s.runtimeConnectorContext(ctx, current.Config)
	if err != nil {
		return nil, err
	}
	liveTools, err := mcpstdio.ListToolsForServer(runtimeCtx, s.root, current.Name, current.Config)
	if err != nil {
		return nil, err
	}
	latest, found, err := s.ResolveRuntimeConnector(ctx, userID, current.ID)
	if err != nil {
		return nil, err
	}
	if !found || latest.Config.Disabled || connectorToolConfigurationSHA256(latest) != expectedConfigSHA256 {
		return nil, ErrConnectorChanged
	}
	return s.persistConnectorToolCatalog(userID, latest, liveTools)
}

func (s *Service) persistConnectorToolCatalog(userID string, connector RuntimeConnector, liveTools []mcpstdio.ToolProjection) ([]mcpstdio.ToolProjection, error) {
	if len(liveTools) > connectorToolCatalogMaxTools {
		return nil, fmt.Errorf("MCP connector %s exposed %d tools; limit is %d", connector.ID, len(liveTools), connectorToolCatalogMaxTools)
	}
	entries := make([]connectorToolCatalogEntry, 0, len(liveTools))
	seen := make(map[string]struct{}, len(liveTools))
	for _, tool := range liveTools {
		name := strings.TrimSpace(tool.ToolName)
		if err := validateConnectorToolName(name); err != nil {
			return nil, fmt.Errorf("MCP connector %s tool name: %w", connector.ID, err)
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf("MCP connector %s exposed duplicate tool %q", connector.ID, name)
		}
		seen[name] = struct{}{}
		description := strings.TrimSpace(tool.Description)
		if len(description) > connectorToolCatalogMaxDescription {
			return nil, fmt.Errorf("MCP connector %s tool %s description is too large", connector.ID, name)
		}
		entries = append(entries, connectorToolCatalogEntry{
			ToolName: name, Description: description, InputSchema: tool.InputSchema,
			OutputSchema: tool.OutputSchema, ReadOnlyHint: tool.ReadOnlyHint,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ToolName < entries[j].ToolName })
	raw, err := json.Marshal(connectorToolCatalogDocument{Version: connectorToolCatalogVersion, Tools: entries})
	if err != nil {
		return nil, fmt.Errorf("encode MCP connector %s tool catalog: %w", connector.ID, err)
	}
	if len(raw) > workspace.MaxMCPConnectorToolCatalogBytes {
		return nil, fmt.Errorf("MCP connector %s tool catalog is too large", connector.ID)
	}
	sum := sha256.Sum256(raw)
	catalogSHA256 := hex.EncodeToString(sum[:])
	tools, err := decodeConnectorToolCatalog(connector, raw, catalogSHA256)
	if err != nil {
		return nil, err
	}
	if err := s.store.PutMCPConnectorToolCatalog(workspace.MCPConnectorToolCatalog{
		UserID: userID, Source: connector.Source, ConnectorID: connector.ID,
		ConfigSHA256: connectorToolConfigurationSHA256(connector), CatalogSHA256: catalogSHA256,
		CatalogJSON: string(raw), RefreshedAt: time.Now().UTC(),
	}); err != nil {
		return nil, err
	}
	return tools, nil
}

func decodeConnectorToolCatalog(connector RuntimeConnector, raw []byte, expectedSHA256 string) ([]mcpstdio.ToolProjection, error) {
	if len(raw) == 0 || len(raw) > workspace.MaxMCPConnectorToolCatalogBytes {
		return nil, errors.New("MCP connector tool catalog is empty or too large")
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != expectedSHA256 {
		return nil, errors.New("MCP connector tool catalog digest mismatch")
	}
	var document connectorToolCatalogDocument
	if err := json.Unmarshal(raw, &document); err != nil || document.Version != connectorToolCatalogVersion {
		return nil, errors.New("MCP connector tool catalog version is invalid")
	}
	if len(document.Tools) > connectorToolCatalogMaxTools {
		return nil, errors.New("MCP connector tool catalog exceeds the tool limit")
	}
	tools := make([]mcpstdio.ToolProjection, 0, len(document.Tools))
	seen := make(map[string]struct{}, len(document.Tools))
	for _, entry := range document.Tools {
		entry.ToolName = strings.TrimSpace(entry.ToolName)
		if err := validateConnectorToolName(entry.ToolName); err != nil {
			return nil, err
		}
		if _, duplicate := seen[entry.ToolName]; duplicate {
			return nil, errors.New("MCP connector tool catalog contains duplicate tools")
		}
		seen[entry.ToolName] = struct{}{}
		if len(entry.Description) > connectorToolCatalogMaxDescription {
			return nil, errors.New("MCP connector tool description exceeds the size limit")
		}
		schema := entry.InputSchema
		if schema == nil {
			schema = map[string]any{}
		}
		properties := connectorToolInputProperties(schema)
		tools = append(tools, mcpstdio.ToolProjection{
			Name: mcpstdio.BuildToolName(connector.Name, entry.ToolName), Server: connector.Name,
			ToolName: entry.ToolName, Description: strings.TrimSpace(entry.Description),
			ServerStatus: "connected", HasInputSchema: len(schema) > 0,
			InputProperties: properties, InputSchema: schema,
			HasOutputSchema: len(entry.OutputSchema) > 0, OutputSchema: entry.OutputSchema,
			ReadOnlyHint: entry.ReadOnlyHint,
		})
	}
	return tools, nil
}

func connectorToolConfigurationSHA256(connector RuntimeConnector) string {
	raw, _ := json.Marshal(struct {
		ID, Name, Source, DefinitionSHA256 string
		Config                             mcpstdio.ServerConfig
	}{
		ID: connector.ID, Name: connector.Name, Source: connector.Source,
		DefinitionSHA256: connector.DefinitionSHA256, Config: connector.Config,
	})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func validateConnectorToolName(name string) error {
	if name == "" || len(name) > 256 {
		return errors.New("tool name is empty or too long")
	}
	for _, value := range name {
		if unicode.IsControl(value) || unicode.IsSpace(value) {
			return errors.New("tool name contains whitespace or control characters")
		}
	}
	return nil
}

func connectorToolInputProperties(schema map[string]any) []string {
	properties, _ := schema["properties"].(map[string]any)
	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
