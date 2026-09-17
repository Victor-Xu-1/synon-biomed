package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/tools/mcpstdio"
)

const maxKernelMCPCatalogTools = 256

type kernelMCPCatalogRequest struct {
	server     string
	offset     int
	maxResults int
}

// handleKernelMCPCatalogHostCall gives repl code the same authority-scoped MCP
// inventory used by normal agent tool schemas. It is discovery only: callers
// must still invoke an exact catalog entry through host.mcp, where schema,
// policy, approval, audit, and connector-change checks run again.
func (s *Server) handleKernelMCPCatalogHostCall(
	ctx context.Context,
	bound kernelHostExecutionIdentity,
	current workspace.KernelFrameAccess,
	args []any,
	kwargs map[string]any,
) (any, error) {
	request, err := parseKernelMCPCatalogRequest(args, kwargs)
	if err != nil {
		return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
	}
	runtimeContext, found, err := s.workspaceMCPRuntimeContextWithContext(ctx, current.Frame.ID)
	if err != nil {
		return nil, kernelruntime.NewHostCallError("connector_error", "MCP connector inventory is unavailable")
	}
	if !found || runtimeContext.UserID != current.UserID {
		return nil, kernelruntime.NewHostCallError("permission_denied", "MCP connector owner is not authorized")
	}

	tools := make([]map[string]any, 0)
	unavailable := make([]map[string]any, 0)
	serverNames := map[string]string{}
	connectors := runtimeContext.Connectors
	if request.server != "" {
		connectors = kernelMCPConnectorMatches(connectors, request.server)
		if len(connectors) == 0 {
			return nil, kernelruntime.NewHostCallError("unknown_server", "MCP server is unknown or not connected")
		}
		if len(connectors) != 1 {
			return nil, kernelruntime.NewHostCallError("ambiguous_server", "Select an exact MCP connector identity from the catalog")
		}
		request.server = connectors[0].ID
	}
	// Filter before consulting schemas, so one server's discovery neither
	// contacts unrelated connectors nor inherits their availability failures.
	for _, connector := range connectors {
		if !connector.Enabled {
			continue
		}
		connectorTools, listErr := s.workspaceMCPRuntimeConnectorTools(ctx, runtimeContext.UserID, connector)
		if listErr != nil {
			unavailable = append(unavailable, map[string]any{
				"server": connector.ID,
				"name":   connector.Name,
				"status": "tool_catalog_unavailable",
			})
			continue
		}
		for _, tool := range connectorTools {
			if request.server != "" && connector.ID != request.server {
				continue
			}
			if runtimeContext.workspaceMCPToolExcluded(connector.ID, tool.ToolName) ||
				!kernelMCPToolAllowed(tool.Name, bound.allowedTools) {
				continue
			}
			if mode, explicit, policyErr := s.workspaceMCPRuntimeToolPolicy(runtimeContext.UserID, connector, tool.ToolName); policyErr != nil {
				unavailable = appendKernelMCPCatalogUnavailable(unavailable, connector)
				continue
			} else if explicit && mode == "deny" {
				continue
			}
			serverNames[connector.ID] = connector.Name
			tools = append(tools, kernelMCPCatalogTool(connector, tool, request.server != ""))
		}
	}
	sort.Slice(tools, func(i, j int) bool {
		leftServer, rightServer := stringValue(tools[i]["server"]), stringValue(tools[j]["server"])
		if leftServer == rightServer {
			return stringValue(tools[i]["method"]) < stringValue(tools[j]["method"])
		}
		return leftServer < rightServer
	})
	sort.Slice(unavailable, func(i, j int) bool {
		return stringValue(unavailable[i]["server"]) < stringValue(unavailable[j]["server"])
	})
	totalTools := len(tools)
	start := min(request.offset, totalTools)
	end := min(start+request.maxResults, totalTools)
	tools = tools[start:end]
	hasMore := end < totalTools
	servers := make([]string, 0, len(serverNames))
	serverDetails := make([]map[string]any, 0, len(serverNames))
	for server, name := range serverNames {
		servers = append(servers, server)
		serverDetails = append(serverDetails, map[string]any{"server": server, "name": name})
	}
	sort.Strings(servers)
	sort.Slice(serverDetails, func(i, j int) bool {
		return stringValue(serverDetails[i]["server"]) < stringValue(serverDetails[j]["server"])
	})
	nextOffset := any(nil)
	if hasMore {
		nextOffset = end
	}
	return map[string]any{
		"usage":                  "Choose an exact listed server and method, then call host.mcp(server, method, **input). Use host.mcp.list_servers() and host.mcp.list_methods(server) for exhaustive discovery. Do not probe or guess names.",
		"servers":                servers,
		"server_details":         serverDetails,
		"tools":                  tools,
		"unavailable_connectors": unavailable,
		"server_filter":          request.server,
		"offset":                 start,
		"max_results":            request.maxResults,
		"total_tools":            totalTools,
		"returned":               len(tools),
		"has_more":               hasMore,
		"next_offset":            nextOffset,
		"truncated":              hasMore,
	}, nil
}

func parseKernelMCPCatalogRequest(args []any, kwargs map[string]any) (kernelMCPCatalogRequest, error) {
	request := kernelMCPCatalogRequest{maxResults: maxKernelMCPCatalogTools}
	if len(args) != 0 {
		return request, fmt.Errorf("host.mcp catalog discovery accepts keyword arguments only")
	}
	for name := range kwargs {
		switch name {
		case "server", "offset", "max_results":
		default:
			return request, fmt.Errorf("host.mcp catalog discovery does not accept %q", name)
		}
	}
	if value, present := kwargs["server"]; present {
		request.server = strings.TrimSpace(stringValue(value))
		if request.server == "" {
			return request, fmt.Errorf("host.mcp catalog server must be a non-empty string")
		}
	}
	if value, present := kwargs["offset"]; present {
		offset, ok := kernelMCPCatalogInteger(value)
		if !ok || offset < 0 {
			return request, fmt.Errorf("host.mcp catalog offset must be a non-negative integer")
		}
		request.offset = offset
	}
	if value, present := kwargs["max_results"]; present {
		maxResults, ok := kernelMCPCatalogInteger(value)
		if !ok || maxResults < 1 || maxResults > maxKernelMCPCatalogTools {
			return request, fmt.Errorf("host.mcp catalog max_results must be an integer between 1 and %d", maxKernelMCPCatalogTools)
		}
		request.maxResults = maxResults
	}
	return request, nil
}

func kernelMCPCatalogInteger(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		if typed == float64(int(typed)) {
			return int(typed), true
		}
	}
	return 0, false
}

func appendKernelMCPCatalogUnavailable(values []map[string]any, connector workspaceMCPRuntimeConnector) []map[string]any {
	for _, value := range values {
		if stringValue(value["server"]) == connector.ID {
			return values
		}
	}
	return append(values, map[string]any{
		"server": connector.ID,
		"name":   connector.Name,
		"status": "tool_policy_unavailable",
	})
}

func kernelMCPCatalogTool(connector workspaceMCPRuntimeConnector, tool mcpstdio.ToolProjection, includeOutputSchema bool) map[string]any {
	result := map[string]any{
		"server":      connector.ID,
		"server_name": connector.Name,
		"method":      tool.ToolName,
		"tool":        tool.Name,
		"description": strings.TrimSpace(truncateServerString(tool.Description, 320)),
		"parameters":  kernelMCPCatalogParameters(tool),
		"read_only":   tool.ReadOnlyHint,
	}
	if includeOutputSchema && tool.HasOutputSchema && len(tool.OutputSchema) > 0 {
		// list_methods is server-filtered and may expose the real response shape
		// needed for safe structured parsing. Keep broad unfiltered discovery
		// lean, and bound any single connector-owned schema.
		if raw, err := json.Marshal(tool.OutputSchema); err == nil && len(raw) <= 64<<10 {
			result["output_schema"] = tool.OutputSchema
		}
	}
	return result
}

func kernelMCPCatalogParameters(tool mcpstdio.ToolProjection) []map[string]any {
	required := map[string]struct{}{}
	for _, name := range stringArrayValue(tool.InputSchema["required"]) {
		required[name] = struct{}{}
	}
	properties, _ := tool.InputSchema["properties"].(map[string]any)
	names := append([]string(nil), tool.InputProperties...)
	if len(names) == 0 {
		for name := range properties {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	parameters := make([]map[string]any, 0, len(names))
	for _, name := range names {
		parameter := map[string]any{"name": name}
		_, isRequired := required[name]
		parameter["required"] = isRequired
		if schema, ok := properties[name].(map[string]any); ok {
			for _, key := range []string{"type", "format", "default", "minimum", "maximum", "minLength", "maxLength", "items"} {
				if value, present := schema[key]; present {
					parameter[key] = value
				}
			}
			kernelMCPCatalogProjectNullableUnion(parameter, schema["anyOf"])
			if description := strings.TrimSpace(stringValue(schema["description"])); description != "" {
				parameter["description"] = truncateServerString(description, 240)
			}
			switch values := schema["enum"].(type) {
			case []any:
				if len(values) <= 32 {
					parameter["enum"] = values
				}
			case []string:
				if len(values) <= 32 {
					parameter["enum"] = values
				}
			}
		}
		parameters = append(parameters, parameter)
	}
	return parameters
}

func kernelMCPCatalogProjectNullableUnion(parameter map[string]any, value any) {
	branches, _ := value.([]any)
	if len(branches) == 0 {
		return
	}
	nullable := false
	for _, raw := range branches {
		branch, _ := raw.(map[string]any)
		if branch == nil {
			continue
		}
		if stringValue(branch["type"]) == "null" {
			nullable = true
			continue
		}
		for _, key := range []string{"type", "format", "minimum", "maximum", "minLength", "maxLength", "items"} {
			if _, exists := parameter[key]; exists {
				continue
			}
			if candidate, present := branch[key]; present {
				parameter[key] = candidate
			}
		}
		if _, exists := parameter["enum"]; !exists {
			switch enum := branch["enum"].(type) {
			case []any:
				if len(enum) <= 32 {
					parameter["enum"] = enum
				}
			case []string:
				if len(enum) <= 32 {
					parameter["enum"] = enum
				}
			}
		}
	}
	if nullable {
		parameter["nullable"] = true
	}
}
