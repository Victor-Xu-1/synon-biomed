package mcpstdio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/coder/websocket"
	"io"
	"net/http"
	"os"
	"strings"
)

func ResolveDynamicTool(ctx context.Context, root string, fullName string) (string, string, error) {
	if !strings.HasPrefix(fullName, "mcp__") {
		return "", "", fmt.Errorf("not an MCP tool name: %s", fullName)
	}
	result, err := ListTools(ctx, root, "")
	if err != nil {
		return "", "", err
	}
	for _, tool := range result.Tools {
		if tool.Name == fullName {
			return tool.Server, tool.ToolName, nil
		}
	}
	return "", "", fmt.Errorf("MCP tool %q not found", fullName)
}

func LoadServers(root string) (map[string]ServerConfig, error) {
	if raw := os.Getenv("SYNON_MCP_CONFIG_JSON"); strings.TrimSpace(raw) != "" {
		return parseConfig([]byte(raw), "SYNON_MCP_CONFIG_JSON")
	}
	// A repository-controlled .mcp.json can run arbitrary local commands or
	// direct requests. It is deliberately not an executable configuration
	// source. Deployments may explicitly nominate an administrator-controlled
	// configuration through SYNON_MCP_CONFIG instead.
	path := strings.TrimSpace(os.Getenv("SYNON_MCP_CONFIG"))
	if path == "" {
		return map[string]ServerConfig{}, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]ServerConfig{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read MCP config %s: %w", path, err)
	}
	return parseConfig(raw, path)
}

func parseConfig(raw []byte, source string) (map[string]ServerConfig, error) {
	var parsed fileConfig
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("parse MCP config %s: %w", source, err)
	}
	if parsed.MCPServers == nil {
		return map[string]ServerConfig{}, nil
	}
	for name, config := range parsed.MCPServers {
		if strings.TrimSpace(config.Name) == "" {
			config.Name = name
		}
		if config.Scope == "" {
			config.Scope = "project"
		}
		config.ConnectorID = name
		parsed.MCPServers[name] = config
	}
	return parsed.MCPServers, nil
}

func isAuthRequiredError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "HTTP 401") || strings.Contains(strings.ToLower(message), "unauthorized")
}

func authToolProjection(serverName string, config ServerConfig) ToolProjection {
	transport := config.transportLabel()
	location := strings.TrimSpace(config.URL)
	if location == "" {
		location = transport
	}
	return ToolProjection{
		Name:            BuildToolName(serverName, "authenticate"),
		Server:          serverName,
		ToolName:        "authenticate",
		Description:     fmt.Sprintf("The %s MCP server (%s) requires authentication. Call this pseudo-tool to start or inspect the OAuth authentication flow.", serverName, location),
		ServerStatus:    "needs_auth",
		HasInputSchema:  true,
		InputProperties: []string{},
		InputSchema:     map[string]any{"type": "object", "properties": map[string]any{}},
	}
}

const maxMCPListPages = 100

type mcpToolListItem struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema"`
	Annotations  struct {
		ReadOnlyHint bool `json:"readOnlyHint"`
	} `json:"annotations"`
}

type mcpToolListPage struct {
	Tools      []mcpToolListItem `json:"tools"`
	NextCursor string            `json:"nextCursor"`
}

type mcpResourceListPage struct {
	Resources  []Resource `json:"resources"`
	NextCursor string     `json:"nextCursor"`
}

func listServerTools(ctx context.Context, root string, serverName string, config ServerConfig) ([]ToolProjection, error) {
	ctx, cancel := context.WithTimeout(ctx, timeoutFromEnv())
	defer cancel()
	call, closeSession, err := openServerCaller(ctx, root, config)
	if err != nil {
		return nil, err
	}
	defer closeSession()
	tools := []ToolProjection{}
	cursor := ""
	seenCursors := map[string]bool{}
	seenTools := map[string]bool{}
	for page := 0; page < maxMCPListPages; page++ {
		raw, err := call("tools/list", mcpListPageInput(cursor))
		if err != nil {
			return nil, err
		}
		decoded := mcpToolListPage{}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&decoded); err != nil {
			return nil, err
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			if err == nil {
				err = errors.New("tools/list response contains multiple JSON values")
			}
			return nil, err
		}
		for _, tool := range decoded.Tools {
			key := strings.ToLower(strings.TrimSpace(tool.Name))
			if key == "" || seenTools[key] {
				continue
			}
			inputSchema, err := decodeToolInputSchema(tool.InputSchema)
			if err != nil {
				return nil, err
			}
			outputSchema, err := decodeToolOutputSchema(tool.OutputSchema)
			if err != nil {
				return nil, err
			}
			properties := schemaProperties(inputSchema)
			tools = append(tools, ToolProjection{
				Name:            BuildToolName(serverName, tool.Name),
				Server:          serverName,
				ToolName:        tool.Name,
				Description:     strings.TrimSpace(tool.Description),
				ServerStatus:    "connected",
				HasInputSchema:  true,
				InputProperties: properties,
				InputSchema:     inputSchema,
				HasOutputSchema: len(outputSchema) > 0,
				OutputSchema:    outputSchema,
				ReadOnlyHint:    tool.Annotations.ReadOnlyHint,
			})
			seenTools[key] = true
		}
		next := strings.TrimSpace(decoded.NextCursor)
		if next == "" {
			return tools, nil
		}
		if seenCursors[next] {
			return nil, errors.New("tools/list pagination repeated a cursor")
		}
		seenCursors[next] = true
		cursor = next
	}
	return nil, fmt.Errorf("tools/list pagination exceeded %d pages", maxMCPListPages)
}

func listServerResources(ctx context.Context, root string, serverName string, config ServerConfig) ([]Resource, error) {
	ctx, cancel := context.WithTimeout(ctx, timeoutFromEnv())
	defer cancel()
	call, closeSession, err := openServerCaller(ctx, root, config)
	if err != nil {
		return nil, err
	}
	defer closeSession()
	resources := []Resource{}
	cursor := ""
	seenCursors := map[string]bool{}
	seenResources := map[string]bool{}
	for page := 0; page < maxMCPListPages; page++ {
		raw, err := call("resources/list", mcpListPageInput(cursor))
		if err != nil {
			return nil, err
		}
		decoded := mcpResourceListPage{}
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil, err
		}
		for _, resource := range decoded.Resources {
			resource.Server = serverName
			key := strings.TrimSpace(resource.URI)
			if key == "" {
				key = strings.TrimSpace(resource.Name)
			}
			if key == "" || seenResources[key] {
				continue
			}
			seenResources[key] = true
			resources = append(resources, resource)
		}
		next := strings.TrimSpace(decoded.NextCursor)
		if next == "" {
			return resources, nil
		}
		if seenCursors[next] {
			return nil, errors.New("resources/list pagination repeated a cursor")
		}
		seenCursors[next] = true
		cursor = next
	}
	return nil, fmt.Errorf("resources/list pagination exceeded %d pages", maxMCPListPages)
}

func mcpListPageInput(cursor string) any {
	cursor = strings.TrimSpace(cursor)
	if cursor == "" {
		return nil
	}
	return map[string]any{"cursor": cursor}
}

func callServer(ctx context.Context, root string, config ServerConfig, method string, params any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, timeoutFromEnv())
	defer cancel()
	call, closeSession, err := openServerCaller(ctx, root, config)
	if err != nil {
		return nil, err
	}
	defer closeSession()
	return call(method, params)
}

type serverCaller func(method string, params any) (json.RawMessage, error)

type initializedServerSession interface {
	request(context.Context, int, string, any) (json.RawMessage, error)
	notify(context.Context, string, any) error
	close()
}

func openServerCaller(ctx context.Context, root string, config ServerConfig) (serverCaller, func(), error) {
	if config.isRemoteHTTP() {
		return func(method string, params any) (json.RawMessage, error) {
			return callRemoteHTTPServer(ctx, root, config, method, params)
		}, func() {}, nil
	}
	if config.isRemoteWebSocket() {
		return nil, nil, errors.New("remote MCP transport must use public HTTPS")
	}
	var sess initializedServerSession
	var err error
	if config.isSDK() {
		sess, err = startSDKBridgeSession(ctx, root, config)
	} else {
		sess, err = startSession(ctx, root, config)
	}
	if err != nil {
		return nil, nil, err
	}
	if _, err := sess.request(ctx, 1, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{"roots": map[string]any{"listChanged": false}},
		"clientInfo":      productClientInfo(),
	}); err != nil {
		sess.close()
		return nil, nil, err
	}
	if err := sess.notify(ctx, "notifications/initialized", map[string]any{}); err != nil {
		sess.close()
		return nil, nil, err
	}
	nextID := 2
	return func(method string, params any) (json.RawMessage, error) {
		result, err := sess.request(ctx, nextID, method, params)
		nextID++
		return result, err
	}, sess.close, nil
}

func callSDKServer(ctx context.Context, root string, config ServerConfig, method string, params any) (json.RawMessage, error) {
	sess, err := startSDKBridgeSession(ctx, root, config)
	if err != nil {
		return nil, err
	}
	defer sess.close()

	lifecycle, err := negotiateLocalProtocol(ctx, sess)
	if err != nil {
		return nil, err
	}
	return lifecycle.requestLocal(ctx, sess, method, params)
}

func callRemoteHTTPServer(ctx context.Context, root string, config ServerConfig, method string, params any) (json.RawMessage, error) {
	lifecycle, err := negotiateRemoteProtocol(ctx, root, config)
	if err != nil {
		return nil, err
	}
	return lifecycle.requestRemote(ctx, root, config, method, params)
}

func callRemoteWebSocketServer(ctx context.Context, config ServerConfig, method string, params any) (json.RawMessage, error) {
	headers := http.Header{}
	for key, value := range config.Headers {
		if strings.TrimSpace(key) != "" {
			headers.Set(key, value)
		}
	}
	conn, _, err := websocket.Dial(ctx, strings.TrimSpace(config.URL), &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(maxRemoteResponseBytes)
	defer conn.Close(websocket.StatusNormalClosure, "synon-go mcp request complete")
	ws := &remoteWebSocketSession{conn: conn, secrets: remoteMCPSecretValues(config, headers)}
	if _, err := ws.request(ctx, 1, "initialize", map[string]any{
		"protocolVersion": latestLegacyProtocolVersion,
		"capabilities":    map[string]any{"roots": map[string]any{"listChanged": false}},
		"clientInfo":      productClientInfo(),
	}); err != nil {
		return nil, err
	}
	if err := ws.notify(ctx, "notifications/initialized", map[string]any{}); err != nil {
		return nil, err
	}
	return ws.request(ctx, 2, method, params)
}
