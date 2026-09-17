package mcpstdio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func ListTools(ctx context.Context, root string, serverFilter string) (ToolListResult, error) {
	configs, err := LoadServers(root)
	if err != nil {
		return ToolListResult{}, err
	}
	selected, err := selectServers(configs, serverFilter)
	if err != nil {
		return ToolListResult{}, err
	}
	result := ToolListResult{Servers: []ServerProjection{}, Tools: []ToolProjection{}}
	for _, entry := range selected {
		projection := ServerProjection{Name: entry.name, Status: "configured-not-connected", Configured: true, Scope: entry.config.Scope}
		if entry.config.Disabled {
			projection.Status = "disabled"
			result.Servers = append(result.Servers, projection)
			continue
		}
		if !entry.config.isCallableTransport() {
			projection.Error = fmt.Sprintf("MCP transport %q is not callable by the Go runtime.", entry.config.transportLabel())
			result.Servers = append(result.Servers, projection)
			continue
		}
		tools, err := listServerTools(ctx, root, entry.name, entry.config)
		if err != nil {
			projection.Error = err.Error()
			if isAuthRequiredError(err) {
				projection.Status = "needs_auth"
				projection.ToolCount = 1
				result.Servers = append(result.Servers, projection)
				result.Tools = append(result.Tools, authToolProjection(entry.name, entry.config))
				continue
			}
			projection.Status = "failed"
			result.Servers = append(result.Servers, projection)
			continue
		}
		projection.Status = "connected"
		projection.ToolCount = len(tools)
		result.Servers = append(result.Servers, projection)
		result.Tools = append(result.Tools, tools...)
	}
	return result, nil
}

func ListResources(ctx context.Context, root string, serverFilter string) ([]Resource, error) {
	configs, err := LoadServers(root)
	if err != nil {
		return nil, err
	}
	selected, err := selectServers(configs, serverFilter)
	if err != nil {
		return nil, err
	}
	resources := []Resource{}
	for _, entry := range selected {
		if entry.config.Disabled || !entry.config.isCallableTransport() {
			continue
		}
		serverResources, err := listServerResources(ctx, root, entry.name, entry.config)
		if err != nil {
			continue
		}
		resources = append(resources, serverResources...)
	}
	return resources, nil
}

func ReadResource(ctx context.Context, root string, serverName string, uri string) (ResourceReadResult, error) {
	if strings.TrimSpace(serverName) == "" {
		return ResourceReadResult{}, errors.New("server is required")
	}
	if strings.TrimSpace(uri) == "" {
		return ResourceReadResult{}, errors.New("uri is required")
	}
	configs, err := LoadServers(root)
	if err != nil {
		return ResourceReadResult{}, err
	}
	config, ok := findServer(configs, serverName)
	if !ok {
		return ResourceReadResult{}, fmt.Errorf("Server %q not found. Available servers: %s", serverName, strings.Join(serverNames(configs), ", "))
	}
	if config.Disabled {
		return ResourceReadResult{}, fmt.Errorf("Server %q is disabled", serverName)
	}
	if !config.isCallableTransport() {
		return ResourceReadResult{}, fmt.Errorf("Server %q transport %q is not callable", serverName, config.transportLabel())
	}
	raw, err := callServer(ctx, root, config, "resources/read", map[string]any{"uri": uri})
	if err != nil {
		return ResourceReadResult{}, err
	}
	var decoded struct {
		Contents []map[string]any `json:"contents"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return ResourceReadResult{}, err
	}
	output := ResourceReadResult{Contents: []ResourceContent{}}
	for index, content := range decoded.Contents {
		item := ResourceContent{
			URI:      stringField(content, "uri"),
			MimeType: stringField(content, "mimeType"),
			Text:     stringField(content, "text"),
		}
		if item.Text == "" {
			if blob := stringField(content, "blob"); blob != "" {
				path, text, err := persistBlob(root, serverName, uri, item.MimeType, index, blob)
				if err != nil {
					item.Text = "Binary content could not be saved to disk: " + err.Error()
				} else {
					item.BlobSavedTo = path
					item.Text = text
				}
			}
		}
		output.Contents = append(output.Contents, item)
	}
	return output, nil
}

func Authenticate(ctx context.Context, root string, serverName string) (OAuthStartResult, error) {
	if strings.TrimSpace(serverName) == "" {
		return OAuthStartResult{}, errors.New("server is required")
	}
	configs, err := LoadServers(root)
	if err != nil {
		return OAuthStartResult{}, err
	}
	config, ok := findServer(configs, serverName)
	if !ok {
		return OAuthStartResult{}, fmt.Errorf("Server %q not found. Available servers: %s", serverName, strings.Join(serverNames(configs), ", "))
	}
	return AuthenticateServer(ctx, root, serverName, config)
}

// AuthenticateServer starts OAuth/PKCE for a dynamically discovered connector
// without requiring it to be written into .mcp.json.
func AuthenticateServer(ctx context.Context, root string, serverName string, config ServerConfig) (OAuthStartResult, error) {
	serverName = strings.TrimSpace(serverName)
	if serverName == "" {
		return OAuthStartResult{}, errors.New("server is required")
	}
	if !config.isRemoteHTTP() {
		return OAuthStartResult{Status: "unsupported", Message: fmt.Sprintf("Server %q transport %q does not support OAuth from this tool.", serverName, config.transportLabel())}, nil
	}
	config = prepareServerConfig(serverName, config)
	discovery, err := discoverOAuthAuthorization(ctx, config)
	if err != nil {
		return OAuthStartResult{}, err
	}
	metadata, err := selectOAuthMetadata(ctx, discovery.MetadataURLs)
	if err != nil {
		return OAuthStartResult{}, err
	}
	redirectURI, callbackServer, callbacks, err := startOAuthCallbackServer()
	if err != nil {
		return OAuthStartResult{}, err
	}
	state, err := randomURLToken(24)
	if err != nil {
		_ = callbackServer.Close()
		return OAuthStartResult{}, err
	}
	verifier, err := randomURLToken(48)
	if err != nil {
		_ = callbackServer.Close()
		return OAuthStartResult{}, err
	}
	challenge := pkceChallenge(verifier)
	credentials, err := registerOAuthClient(ctx, metadata, redirectURI)
	if err != nil {
		_ = callbackServer.Close()
		return OAuthStartResult{}, err
	}
	resource := discovery.Resource
	authURL, err := buildOAuthAuthURLForClient(metadata.AuthorizationEndpoint, resource, redirectURI, state, challenge, discovery.Scope, credentials.ClientID)
	if err != nil {
		_ = callbackServer.Close()
		return OAuthStartResult{}, err
	}
	go completeOAuthAuthorization(context.WithoutCancel(ctx), root, config.ConnectorID, metadata, credentials, resource, redirectURI, state, verifier, callbackServer, callbacks)
	return OAuthStartResult{Status: "auth_url", AuthURL: authURL, RedirectURI: redirectURI, Message: fmt.Sprintf("Open this URL to authorize the %s MCP server.", serverName)}, nil
}

// ListToolsForServer probes one dynamic connector through the production MCP
// initialize/tools-list transport and returns normalized tool projections.
func ListToolsForServer(ctx context.Context, root string, serverName string, config ServerConfig) ([]ToolProjection, error) {
	serverName = strings.TrimSpace(serverName)
	if serverName == "" {
		return nil, errors.New("server is required")
	}
	if config.Disabled {
		return nil, fmt.Errorf("Server %q is disabled", serverName)
	}
	config = prepareServerConfig(serverName, config)
	if !config.isCallableTransport() {
		return nil, fmt.Errorf("Server %q transport %q is not callable", serverName, config.transportLabel())
	}
	return listServerTools(ctx, root, serverName, config)
}

func OAuthConnected(root, serverName string) bool {
	_, ok := loadOAuthAccessToken(root, serverName)
	return ok
}

func DisconnectOAuth(root, serverName string) error {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(serverName) == "" {
		return errors.New("OAuth token root and server are required")
	}
	oauthTokenCacheMu.Lock()
	defer oauthTokenCacheMu.Unlock()
	store := oauthTokenVault(root)
	if _, err := store.DeleteForUser(oauthTokenSecretID(serverName), oauthTokenVaultUserID); err != nil {
		return fmt.Errorf("delete MCP OAuth vault entry: %w", err)
	}
	return removeLegacyOAuthToken(root, serverName)
}

func CallTool(ctx context.Context, root string, serverName string, toolName string, input map[string]any) (string, error) {
	if strings.TrimSpace(serverName) == "" {
		return "", errors.New("server is required")
	}
	if strings.TrimSpace(toolName) == "" {
		return "", errors.New("toolName is required")
	}
	configs, err := LoadServers(root)
	if err != nil {
		return "", err
	}
	config, ok := findServer(configs, serverName)
	if !ok {
		return "", fmt.Errorf("Server %q not found. Available servers: %s", serverName, strings.Join(serverNames(configs), ", "))
	}
	if config.Disabled {
		return "", fmt.Errorf("Server %q is disabled", serverName)
	}
	if !config.isCallableTransport() {
		return "", fmt.Errorf("Server %q transport %q is not callable", serverName, config.transportLabel())
	}
	return CallToolForServer(ctx, root, serverName, toolName, input, config)
}

// CallToolForServer invokes one connector whose configuration comes from an
// authoritative store other than .mcp.json, while retaining the production
// MCP initialize/session/call transport and result normalization.
func CallToolForServer(ctx context.Context, root string, serverName string, toolName string, input map[string]any, config ServerConfig) (string, error) {
	serverName = strings.TrimSpace(serverName)
	toolName = strings.TrimSpace(toolName)
	if serverName == "" {
		return "", errors.New("server is required")
	}
	if toolName == "" {
		return "", errors.New("toolName is required")
	}
	if config.Disabled {
		return "", fmt.Errorf("Server %q is disabled", serverName)
	}
	config = prepareServerConfig(serverName, config)
	if !config.isCallableTransport() {
		return "", fmt.Errorf("Server %q transport %q is not callable", serverName, config.transportLabel())
	}
	return InspectAndCallToolForServer(ctx, root, serverName, toolName, input, config, func(tool ToolProjection) error {
		return validateToolInput(tool.InputSchema, input)
	})
}

type oauthCallbackResult struct {
	Code  string
	State string
	ISS   string
	Err   error
}
