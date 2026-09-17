package mcpstdio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"

	"github.com/coder/websocket"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ToolCallInspector runs after tools/list and before tools/call on the same
// initialized MCP session. Returning an error guarantees tools/call is not
// sent on that session.
type ToolCallInspector func(ToolProjection) error

// InspectAndCallToolForServer binds schema inspection, policy validation, and
// invocation to one connector session so a restart cannot swap the approved
// tool definition between tools/list and tools/call.
func InspectAndCallToolForServer(
	ctx context.Context,
	root, serverName, toolName string,
	input map[string]any,
	config ServerConfig,
	inspect ToolCallInspector,
) (string, error) {
	serverName = strings.TrimSpace(serverName)
	toolName = strings.TrimSpace(toolName)
	if serverName == "" || toolName == "" || inspect == nil {
		return "", errors.New("server, toolName, and inspector are required")
	}
	if config.Disabled {
		return "", fmt.Errorf("Server %q is disabled", serverName)
	}
	config = prepareServerConfig(serverName, config)
	if !config.isCallableTransport() {
		return "", fmt.Errorf("Server %q transport %q is not callable", serverName, config.transportLabel())
	}
	if config.isRemoteWebSocket() {
		return "", errors.New("remote MCP transport must use public HTTPS")
	}
	// Discovery sizing depends on the live tools/list schema. Keep the prepared
	// arguments private to this call, then update that map in the inspector so
	// every transport sends the same schema-bounded input to tools/call.
	preparedInput, inspectWithDiscoveryDefaults := prepareDiscoveryInspector(input, inspect)
	input = preparedInput
	ctx, cancel := context.WithTimeout(ctx, timeoutFromEnv())
	defer cancel()
	if config.isRemoteHTTP() {
		return inspectAndCallRemoteHTTP(ctx, root, serverName, toolName, input, config, inspectWithDiscoveryDefaults)
	}
	if config.isRemoteWebSocket() {
		return inspectAndCallRemoteWebSocket(ctx, serverName, toolName, input, config, inspectWithDiscoveryDefaults)
	}
	if config.isSDK() {
		sess, err := startSDKBridgeSession(ctx, root, config)
		if err != nil {
			return "", err
		}
		defer sess.close()
		return inspectAndCallLocalSession(ctx, sess, serverName, toolName, input, inspectWithDiscoveryDefaults)
	}
	sess, err := startSession(ctx, root, config)
	if err != nil {
		return "", err
	}
	defer sess.close()
	return inspectAndCallLocalSession(ctx, sess, serverName, toolName, input, inspectWithDiscoveryDefaults)
}

func prepareDiscoveryInspector(input map[string]any, inspect ToolCallInspector) (map[string]any, ToolCallInspector) {
	preparedInput := cloneInput(input)
	return preparedInput, func(tool ToolProjection) error {
		prepared := applyDiscoveryDefaults(tool, preparedInput)
		if !reflect.DeepEqual(prepared, preparedInput) {
			replaceInput(preparedInput, prepared)
		}
		return inspect(tool)
	}
}

type inspectCallLocalSession interface {
	request(context.Context, int, string, any) (json.RawMessage, error)
	notify(context.Context, string, any) error
}

func inspectAndCallLocalSession(ctx context.Context, sess inspectCallLocalSession, serverName, toolName string, input map[string]any, inspect ToolCallInspector) (string, error) {
	lifecycle, err := negotiateLocalProtocol(ctx, sess)
	if err != nil {
		return "", err
	}
	rawTools, err := lifecycle.requestLocal(ctx, sess, "tools/list", nil)
	if err != nil {
		return "", err
	}
	if err := inspectListedTool(rawTools, serverName, toolName, inspect); err != nil {
		return "", err
	}
	raw, err := lifecycle.requestLocal(ctx, sess, "tools/call", map[string]any{"name": toolName, "arguments": input})
	if err != nil {
		return "", err
	}
	return formatToolCallResult(raw)
}

func inspectAndCallRemoteHTTP(ctx context.Context, root, serverName, toolName string, input map[string]any, config ServerConfig, inspect ToolCallInspector) (string, error) {
	lifecycle, err := negotiateRemoteProtocol(ctx, root, config)
	if err != nil {
		return "", err
	}
	rawTools, err := lifecycle.requestRemote(ctx, root, config, "tools/list", nil)
	if err != nil {
		return "", err
	}
	if err := inspectListedTool(rawTools, serverName, toolName, inspect); err != nil {
		return "", err
	}
	raw, err := lifecycle.requestRemote(ctx, root, config, "tools/call", map[string]any{"name": toolName, "arguments": input})
	if err != nil {
		return "", err
	}
	return formatToolCallResult(raw)
}

func inspectAndCallRemoteWebSocket(ctx context.Context, serverName, toolName string, input map[string]any, config ServerConfig, inspect ToolCallInspector) (string, error) {
	headers := http.Header{}
	for key, value := range config.Headers {
		if strings.TrimSpace(key) != "" {
			headers.Set(key, value)
		}
	}
	conn, _, err := websocket.Dial(ctx, strings.TrimSpace(config.URL), &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		return "", err
	}
	conn.SetReadLimit(maxRemoteResponseBytes)
	defer conn.Close(websocket.StatusNormalClosure, "synon-go mcp request complete")
	sess := &remoteWebSocketSession{conn: conn, secrets: remoteMCPSecretValues(config, headers)}
	if _, err := sess.request(ctx, 1, "initialize", map[string]any{
		"protocolVersion": latestLegacyProtocolVersion,
		"capabilities":    map[string]any{"roots": map[string]any{"listChanged": false}},
		"clientInfo":      productClientInfo(),
	}); err != nil {
		return "", err
	}
	if err := sess.notify(ctx, "notifications/initialized", map[string]any{}); err != nil {
		return "", err
	}
	rawTools, err := sess.request(ctx, 2, "tools/list", nil)
	if err != nil {
		return "", err
	}
	if err := inspectListedTool(rawTools, serverName, toolName, inspect); err != nil {
		return "", err
	}
	raw, err := sess.request(ctx, 3, "tools/call", map[string]any{"name": toolName, "arguments": input})
	if err != nil {
		return "", err
	}
	return formatToolCallResult(raw)
}

func inspectListedTool(raw json.RawMessage, serverName, toolName string, inspect ToolCallInspector) error {
	var decoded struct {
		Tools []struct {
			Name         string          `json:"name"`
			Description  string          `json:"description"`
			InputSchema  json.RawMessage `json:"inputSchema"`
			OutputSchema json.RawMessage `json:"outputSchema"`
			Annotations  struct {
				ReadOnlyHint bool `json:"readOnlyHint"`
			} `json:"annotations"`
		} `json:"tools"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("tools/list response contains multiple JSON values")
		}
		return err
	}
	var matched *ToolProjection
	for _, tool := range decoded.Tools {
		if tool.Name != toolName {
			continue
		}
		if matched != nil {
			return errors.New("tools/list returned a duplicate tool")
		}
		inputSchema, err := decodeToolInputSchema(tool.InputSchema)
		if err != nil {
			return err
		}
		outputSchema, err := decodeToolOutputSchema(tool.OutputSchema)
		if err != nil {
			return err
		}
		projection := ToolProjection{
			Name: BuildToolName(serverName, tool.Name), Server: serverName, ToolName: tool.Name,
			Description: strings.TrimSpace(tool.Description), ServerStatus: "connected",
			HasInputSchema: true, InputProperties: schemaProperties(inputSchema),
			InputSchema: inputSchema, HasOutputSchema: len(outputSchema) > 0,
			OutputSchema: outputSchema, ReadOnlyHint: tool.Annotations.ReadOnlyHint,
		}
		matched = &projection
	}
	if matched == nil {
		return errors.New("tools/list did not contain the requested tool")
	}
	return inspect(*matched)
}

func decodeToolOutputSchema(raw json.RawMessage) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	schema, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("MCP tool outputSchema must be a JSON object")
	}
	return schema, nil
}

func decodeToolInputSchema(raw json.RawMessage) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("MCP tool inputSchema is required")
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	switch typed := value.(type) {
	case map[string]any:
		if schemaType, _ := typed["type"].(string); strings.TrimSpace(schemaType) != "object" {
			return nil, errors.New("MCP tool inputSchema must declare type object")
		}
		return typed, nil
	default:
		return nil, errors.New("MCP tool inputSchema must be a JSON object")
	}
}

func validateToolInput(schema map[string]any, input map[string]any) error {
	if schema == nil {
		return errors.New("MCP tool inputSchema is required")
	}
	if dialect, ok := schema["$schema"].(string); ok && strings.TrimSpace(dialect) != "" {
		normalized := strings.TrimSpace(dialect)
		if normalized != "http://json-schema.org/draft-07/schema#" && normalized != "http://json-schema.org/draft-07/schema" {
			return errors.New("MCP tool inputSchema dialect is unsupported")
		}
	}
	rawSchema, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("encode MCP tool inputSchema: %w", err)
	}
	normalizedSchema, err := jsonschema.UnmarshalJSON(bytes.NewReader(rawSchema))
	if err != nil {
		return fmt.Errorf("parse MCP tool inputSchema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft7)
	compiler.UseLoader(mcpSchemaDenyLoader{})
	if err := compiler.AddResource("mcp-tool-input-schema.json", normalizedSchema); err != nil {
		return fmt.Errorf("add MCP tool inputSchema: %w", err)
	}
	compiled, err := compiler.Compile("mcp-tool-input-schema.json")
	if err != nil {
		return fmt.Errorf("compile MCP tool inputSchema: %w", err)
	}
	rawInput, err := json.Marshal(input)
	if err != nil {
		return errors.New("MCP tool input is not valid JSON")
	}
	normalizedInput, err := jsonschema.UnmarshalJSON(bytes.NewReader(rawInput))
	if err != nil {
		return errors.New("MCP tool input is not valid JSON")
	}
	if err := compiled.Validate(normalizedInput); err != nil {
		return fmt.Errorf("MCP tool input validation failed: %w", err)
	}
	return nil
}

type mcpSchemaDenyLoader struct{}

func (mcpSchemaDenyLoader) Load(string) (any, error) {
	return nil, errors.New("external MCP schema references are not allowed")
}
