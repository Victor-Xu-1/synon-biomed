package mcpdirectory

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"synon-go/internal/tools/mcpstdio"
)

var ErrConnectorNotFound = errors.New("MCP connector not found")
var ErrConnectorChanged = errors.New("MCP connector authority changed")
var ErrConnectorAuthorizationRequired = errors.New("MCP connector authorization is required")

type RuntimeConnector struct {
	ID               string
	Name             string
	Description      string
	Source           string
	DefinitionSHA256 string
	AuthRequired     bool
	Authorized       bool
	Config           mcpstdio.ServerConfig
}

// RuntimeConnectorAuthorityEquivalent compares the immutable authority that
// protects a resolved connector execution. Bundled connectors may refresh
// owner-scoped runtime environment fields (for example an etiquette contact
// value) without changing the connector definition or endpoint. Custom and
// directory connectors remain exact-match fail-closed because their endpoint
// and credential authority is user-controlled.
func RuntimeConnectorAuthorityEquivalent(left, right RuntimeConnector) bool {
	if left.Source != "bundled" || right.Source != "bundled" {
		return reflect.DeepEqual(left, right)
	}
	return left.ID == right.ID && left.Name == right.Name && left.Source == right.Source &&
		left.DefinitionSHA256 == right.DefinitionSHA256 && left.Config.Disabled == right.Config.Disabled
}

func runtimeConnectorAuthorityEquivalent(left, right RuntimeConnector) bool {
	return RuntimeConnectorAuthorityEquivalent(left, right)
}

// ResolveRuntimeConnector returns the owner-scoped executable definition for
// bundled and directory connectors. Custom connectors are resolved by the
// server because their transport secrets live in the encrypted user vault.
func (s *Service) ResolveRuntimeConnector(_ context.Context, userID, connectorID string) (RuntimeConnector, bool, error) {
	if s == nil || s.store == nil {
		return RuntimeConnector{}, false, errors.New("MCP directory service is not configured")
	}
	userID = strings.TrimSpace(userID)
	connectorID = strings.TrimSpace(connectorID)
	if userID == "" || connectorID == "" {
		return RuntimeConnector{}, false, errors.New("MCP connector user and id are required")
	}
	if item, found := s.bundled[connectorID]; found {
		if item.Deferred && !s.optionalConnectorInstalled(item) {
			return RuntimeConnector{}, false, errors.New("optional MCP connector is not installed")
		}
		enabled := true
		states, err := s.store.ListMCPConnectorRuntimeStates(userID)
		if err != nil {
			return RuntimeConnector{}, false, err
		}
		for _, state := range states {
			if state.Source == "bundled" && state.ConnectorID == connectorID {
				enabled = state.Enabled
				break
			}
		}
		config, err := s.bundledRuntimeConfigForUser(userID, item)
		if err != nil {
			return RuntimeConnector{}, false, err
		}
		config.Name = item.Name
		config.ConnectorID = bundledOAuthKey(userID, item.ID)
		config.Disabled = !enabled
		contactEmail, shared, err := s.ownerContactEmail(userID)
		if err != nil {
			return RuntimeConnector{}, false, err
		}
		if shared && strings.TrimSpace(contactEmail) != "" {
			if config.Env == nil {
				config.Env = map[string]string{}
			}
			config.Env["OPERON_CONTACT_EMAIL"] = strings.TrimSpace(contactEmail)
		}
		apiKeyConfigured, err := s.bundledAPIKeyConfigured(userID, item)
		if err != nil {
			return RuntimeConnector{}, false, err
		}
		authorized := mcpstdio.OAuthConnected(s.root, bundledOAuthKey(userID, item.ID)) || apiKeyConfigured
		return RuntimeConnector{
			ID: item.ID, Name: item.Name, Description: item.Description, Source: "bundled",
			DefinitionSHA256: item.DefinitionSHA256, AuthRequired: item.AuthRequired,
			Authorized: !item.AuthRequired || authorized, Config: config,
		}, true, nil
	}
	row, found, err := s.store.GetMCPDirectoryConnector(userID, connectorID)
	if err != nil || !found {
		return RuntimeConnector{}, found, err
	}
	config := s.applyTLSPosture(serverConfig(row))
	config.Name = directoryOAuthKey(userID, row.ID)
	authorized := !row.AuthRequired || mcpstdio.OAuthConnected(s.root, directoryOAuthKey(userID, row.ID))
	return RuntimeConnector{
		ID: row.ID, Name: row.Name, Description: row.Description, Source: "directory",
		AuthRequired: row.AuthRequired, Authorized: authorized, Config: config,
	}, true, nil
}

func (s *Service) ListUnifiedConnectorTools(ctx context.Context, userID, connectorID string) ([]mcpstdio.ToolProjection, error) {
	connector, found, err := s.ResolveRuntimeConnector(ctx, userID, connectorID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrConnectorNotFound
	}
	if connector.Config.Disabled {
		return nil, errors.New("MCP connector is disabled")
	}
	if connector.AuthRequired && !connector.Authorized {
		return nil, ErrConnectorAuthorizationRequired
	}
	ctx, cancel := context.WithTimeout(ctx, connectorToolCatalogRequestTimeout)
	defer cancel()
	return s.listCachedOrRefreshConnectorTools(ctx, userID, connector)
}

// ListUnifiedConnectorToolsStable returns a connector tool catalog only after
// a stale-while-revalidate flight has settled. Kernel-host execution uses this
// boundary because it must validate the same schema that it will execute; a
// stale catalog and a newly refreshed catalog must never race across the
// approval/authority boundary.
func (s *Service) ListUnifiedConnectorToolsStable(ctx context.Context, userID, connectorID string) ([]mcpstdio.ToolProjection, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("MCP directory service is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for attempt := 0; attempt < 2; attempt++ {
		connector, found, err := s.ResolveRuntimeConnector(ctx, userID, connectorID)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, ErrConnectorNotFound
		}
		if connector.Config.Disabled {
			return nil, errors.New("MCP connector is disabled")
		}
		if connector.AuthRequired && !connector.Authorized {
			return nil, ErrConnectorAuthorizationRequired
		}
		tools, found, refresh, err := s.loadConnectorToolCatalog(userID, connector, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		if found && !refresh {
			return tools, nil
		}
		flight, _, err := s.startConnectorToolCatalogRefresh(userID, connector)
		if err != nil {
			return nil, err
		}
		select {
		case <-flight.done:
			if flight.err == nil {
				return append([]mcpstdio.ToolProjection(nil), flight.tools...), nil
			}
			if !errors.Is(flight.err, ErrConnectorChanged) || attempt == 1 {
				return nil, flight.err
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, ErrConnectorChanged
}

func (s *Service) CallUnifiedConnectorTool(ctx context.Context, userID, connectorID, toolName string, input map[string]any) (string, error) {
	connector, found, err := s.ResolveRuntimeConnector(ctx, userID, connectorID)
	if err != nil {
		return "", err
	}
	if !found {
		return "", ErrConnectorNotFound
	}
	ctx, err = s.runtimeConnectorContext(ctx, connector.Config)
	if err != nil {
		return "", err
	}
	output, callErr := mcpstdio.CallToolForServer(ctx, s.root, connector.Name, toolName, input, connector.Config)
	s.recordConnectorUsage(ctx, userID, connector, toolName, callErr)
	return output, callErr
}

// CallResolvedConnectorTool executes one previously resolved owner-scoped
// connector snapshot. It revalidates that snapshot, then deliberately uses the
// captured configuration so an approval cannot be redirected by a concurrent
// endpoint or transport mutation.
func (s *Service) CallResolvedConnectorTool(ctx context.Context, userID string, connector RuntimeConnector, toolName string, input map[string]any) (string, error) {
	current, found, err := s.ResolveRuntimeConnector(ctx, userID, connector.ID)
	if err != nil {
		return "", err
	}
	if !found {
		return "", ErrConnectorNotFound
	}
	if !RuntimeConnectorAuthorityEquivalent(current, connector) {
		return "", ErrConnectorChanged
	}
	ctx, err = s.runtimeConnectorContext(ctx, connector.Config)
	if err != nil {
		return "", err
	}
	output, callErr := mcpstdio.CallToolForServer(ctx, s.root, connector.Name, toolName, input, connector.Config)
	s.recordConnectorUsage(ctx, userID, connector, toolName, callErr)
	return output, callErr
}

// InspectAndCallResolvedConnectorTool revalidates an owner-scoped executable
// snapshot, then binds tools/list inspection and tools/call to one initialized
// connector session.
func (s *Service) InspectAndCallResolvedConnectorTool(
	ctx context.Context,
	userID string,
	connector RuntimeConnector,
	toolName string,
	input map[string]any,
	inspect mcpstdio.ToolCallInspector,
) (string, error) {
	current, found, err := s.ResolveRuntimeConnector(ctx, userID, connector.ID)
	if err != nil {
		return "", err
	}
	if !found {
		return "", ErrConnectorNotFound
	}
	if !RuntimeConnectorAuthorityEquivalent(current, connector) {
		return "", ErrConnectorChanged
	}
	ctx, err = s.runtimeConnectorContext(ctx, connector.Config)
	if err != nil {
		return "", err
	}
	output, callErr := mcpstdio.InspectAndCallToolForServer(ctx, s.root, connector.Name, toolName, input, connector.Config, inspect)
	s.recordConnectorUsage(ctx, userID, connector, toolName, callErr)
	return output, callErr
}

func (s *Service) recordConnectorUsage(ctx context.Context, userID string, connector RuntimeConnector, toolName string, callErr error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	recorder := s.usageRecorder
	s.mu.Unlock()
	if recorder != nil {
		recorder(ctx, userID, connector, toolName, callErr)
	}
}

func (s *Service) runtimeConnectorContext(ctx context.Context, config mcpstdio.ServerConfig) (context.Context, error) {
	if strings.TrimSpace(config.URL) == "" {
		return ctx, nil
	}
	client, err := s.secureHTTPClient(ctx, config.URL)
	if err != nil {
		return nil, fmt.Errorf("secure MCP connector client: %w", err)
	}
	return mcpstdio.WithHTTPClient(ctx, client), nil
}

func cloneRuntimeServerConfig(input mcpstdio.ServerConfig) mcpstdio.ServerConfig {
	output := input
	output.Args = append([]string(nil), input.Args...)
	output.Env = cloneRuntimeStringMap(input.Env)
	output.Headers = cloneRuntimeStringMap(input.Headers)
	output.QueryParams = cloneRuntimeStringMap(input.QueryParams)
	output.BridgeArgs = append([]string(nil), input.BridgeArgs...)
	output.BridgeEnv = cloneRuntimeStringMap(input.BridgeEnv)
	if input.TrustedTLS != nil {
		posture := *input.TrustedTLS
		output.TrustedTLS = &posture
	}
	return output
}

func cloneRuntimeStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
